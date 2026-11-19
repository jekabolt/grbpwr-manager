package auth

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/jekabolt/grbpwr-manager/internal/platformauth"
	"github.com/jekabolt/grbpwr-manager/internal/tenant"
)

// newPlatformService builds the identity service from the server's control plane (SetPlatform).
func (s *Server) newPlatformService() (*platformauth.Service, error) {
	svc, err := platformauth.New(platformauth.Deps{
		Store:          s.platform.store,
		Config:         s.platform.cfg,
		JWT:            s.JwtAuth,
		Hasher:         s.pwhash,
		TeamTTL:        s.jwtTTL,
		Now:            s.clockNow,
		Hooks:          s.platformHooks(),
		SignupApproval: s.platform.cfg.SignupApprovalOn(),
		TermsVersion:   s.platform.cfg.Terms(),
		SchemaBehind:   s.schemaBehind,
	})
	if err != nil {
		slog.Default().Error("platformauth: identity service not built", slog.String("err", err.Error()))
		return nil, err
	}
	if err := svc.Ready(); err != nil {
		slog.Default().Error("platformauth: identity RPCs unavailable", slog.String("err", err.Error()))
	}
	return svc, nil
}

// schemaBehind is the edge's schema predicate (set at boot by SetEdge, before serving); no edge =
// every team current.
func (s *Server) schemaBehind(t tenant.Team) bool {
	return s.edge != nil && s.edge.SchemaBehind(t)
}

// platformService is the identity service built in SetPlatform. nil, nil = teams mode off (the
// platform RPCs answer Unimplemented).
func (s *Server) platformService() (*platformauth.Service, error) {
	if s.platform.store == nil {
		return nil, nil
	}
	return s.platform.svc, s.platform.svcErr
}

// closePlatformService stops the service's limiters (StopRateLimiter calls it; idempotent).
func (s *Server) closePlatformService() {
	if s.platform.svc != nil {
		s.platform.svc.Close()
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
			// Mirrored like the admin-panel change: a few attempts, so the old team #1 Login
			// stops taking the old password.
			for attempt := 1; ; attempt++ {
				err = s.adminRepository.ChangePassword(pctx, handle, hash)
				if err == nil || attempt == passwordMirrorAttempts {
					break
				}
				if serr := sleepCtx(ctx, time.Duration(attempt)*100*time.Millisecond); serr != nil {
					break
				}
			}
			s.InvalidateAdminAccount(handle)
			if err != nil {
				return fmt.Errorf("team one password: %w", err)
			}
			return nil
		},
		PasswordChanged: s.dropRevokedCredentials,
	}
}

// dropRevokedCredentials drops what a password change revoked from the edge caches (best effort:
// the edge's TTL bounds a miss): the revoked sessions and the handle's legacy «not before».
func (s *Server) dropRevokedCredentials(_ context.Context, handle string, sids []string) {
	if inv, ok := s.edge.(interface{ InvalidateSession(sid string) }); ok {
		for _, sid := range sids {
			inv.InvalidateSession(sid)
		}
	}
	if inv, ok := s.edge.(interface{ InvalidateCredentials(handle string) }); ok {
		inv.InvalidateCredentials(handle)
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
