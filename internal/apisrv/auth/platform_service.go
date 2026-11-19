package auth

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"sync"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/jekabolt/grbpwr-manager/internal/platformauth"
	"github.com/jekabolt/grbpwr-manager/internal/tenant"
)

// platformServices holds the identity service of each server (*Server → *platformServiceEntry). The
// service is not a Server field because platform.go and auth.go belong to other lanes; the
// coordinator may fold it into platformHandle (built in SetPlatform) and drop this map.
var platformServices sync.Map

type platformServiceEntry struct {
	once sync.Once
	svc  *platformauth.Service
	err  error
}

// platformService is the identity service built from the server's control plane on first use. nil,
// nil = teams mode off (the platform RPCs answer Unimplemented).
func (s *Server) platformService() (*platformauth.Service, error) {
	if s.platform.store == nil {
		return nil, nil
	}
	v, _ := platformServices.LoadOrStore(s, &platformServiceEntry{})
	e := v.(*platformServiceEntry)
	e.once.Do(func() {
		e.svc, e.err = platformauth.New(platformauth.Deps{
			Store:   s.platform.store,
			Config:  s.platform.cfg,
			JWT:     s.JwtAuth,
			Hasher:  s.pwhash,
			TeamTTL: s.jwtTTL,
			Now:     s.clockNow,
			Hooks:   s.platformHooks(),
			// Read here only in teams mode; the coordinator may move both into config/cfg.go.
			SignupApproval: platformauth.SignupApprovalFromEnv(),
			TermsVersion:   platformauth.TermsVersionFromEnv(),
		})
		if e.err == nil {
			if err := e.svc.Ready(); err != nil {
				slog.Default().Error("platformauth: identity RPCs unavailable", slog.String("err", err.Error()))
			}
		}
	})
	return e.svc, e.err
}

// closePlatformService stops the service's limiters (StopRateLimiter calls it).
func (s *Server) closePlatformService() {
	if v, ok := platformServices.LoadAndDelete(s); ok {
		if e := v.(*platformServiceEntry); e.svc != nil {
			e.svc.Close()
		}
	}
}

// platformHooks reach the team databases through the admin repository under a team pin.
func (s *Server) platformHooks() platformauth.Hooks {
	return platformauth.Hooks{
		TeamAccount: func(ctx context.Context, team tenant.Team, handle string) (platformauth.TeamAccount, bool, error) {
			acc, err := s.adminRepository.GetAccountWithPermissions(tenant.With(ctx, team), handle)
			if errors.Is(err, sql.ErrNoRows) {
				return platformauth.TeamAccount{}, false, nil
			}
			if err != nil {
				return platformauth.TeamAccount{}, false, err
			}
			if acc == nil {
				return platformauth.TeamAccount{}, false, nil
			}
			return platformauth.TeamAccount{Super: acc.IsSuper, Perms: acc.Permissions, Disabled: acc.Disabled, JoinPending: acc.JoinPending}, true, nil
		},
		StageMember: func(ctx context.Context, team tenant.Team, handle string, super bool, perms []entity.AdminPermission) error {
			return s.stageTeamAdmin(tenant.With(ctx, team), handle, super, perms)
		},
		EnableMember: func(ctx context.Context, team tenant.Team, handle string) (bool, error) {
			return s.enableTeamAdmin(tenant.With(ctx, team), handle)
		},
		TeamOnePassword: func(ctx context.Context, handle, hash string) error {
			pctx, err := tenant.Pinned(ctx, s.platform.store.Directory(), 1)
			if err != nil {
				return err
			}
			if err := s.adminRepository.ChangePassword(pctx, handle, hash); err != nil {
				return fmt.Errorf("team one password: %w", err)
			}
			s.InvalidateAdminAccount(handle)
			return nil
		},
	}
}

// teamJoins is the team-side half of an invite's join (*admin.Store): the staged row and its
// compare-and-set finish. Not part of dependency.Admin so the shared interface and its mocks stay
// untouched; the coordinator may move it there.
type teamJoins interface {
	StageJoin(ctx context.Context, username, pwHash string, isSuper bool, perms []entity.AdminPermission) error
	FinishJoin(ctx context.Context, username string) (bool, error)
}

func (s *Server) teamJoins() (teamJoins, error) {
	j, ok := s.adminRepository.(teamJoins)
	if !ok {
		return nil, errors.New("admin repository cannot stage team joins")
	}
	return j, nil
}

// stageTeamAdmin writes an invited member's admins row in the pinned team DISABLED with these rights
// and the join_pending marker, in one transaction (a new row gets a hash nobody can match). Legacy
// Login refuses the row until enableTeamAdmin finishes the join.
func (s *Server) stageTeamAdmin(ctx context.Context, handle string, super bool, perms []entity.AdminPermission) error {
	defer s.InvalidateAdminAccount(handle)
	j, err := s.teamJoins()
	if err != nil {
		return err
	}
	hash, err := platformauth.UnusableHash(s.pwhash)()
	if err != nil {
		return fmt.Errorf("stage team admin: hash: %w", err)
	}
	if err := j.StageJoin(ctx, handle, hash, super, perms); err != nil {
		return fmt.Errorf("stage team admin: %w", err)
	}
	return nil
}

// enableTeamAdmin finishes a staged admins row in the pinned team: enabled only while its
// join_pending marker is still set. false = nothing enabled.
func (s *Server) enableTeamAdmin(ctx context.Context, handle string) (bool, error) {
	defer s.InvalidateAdminAccount(handle)
	j, err := s.teamJoins()
	if err != nil {
		return false, err
	}
	ok, err := j.FinishJoin(ctx, handle)
	if err != nil {
		return false, fmt.Errorf("enable team admin: %w", err)
	}
	return ok, nil
}
