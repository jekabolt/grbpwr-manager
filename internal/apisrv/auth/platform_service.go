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
			return platformauth.TeamAccount{Super: acc.IsSuper, Perms: acc.Permissions, Disabled: acc.Disabled}, true, nil
		},
		StageMember: func(ctx context.Context, team tenant.Team, handle string, super bool, perms []entity.AdminPermission) error {
			return s.stageTeamAdmin(tenant.With(ctx, team), handle, super, perms)
		},
		EnableMember: func(ctx context.Context, team tenant.Team, handle string) error {
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

// stageTeamAdmin writes an invited member's admins row in the pinned team DISABLED with these rights:
// an existing row is disabled before its rights change, a new one gets a hash nobody can match. The
// row stays unusable (legacy Login refuses it) until enableTeamAdmin, which the join runs only after
// the control plane made the member active.
func (s *Server) stageTeamAdmin(ctx context.Context, handle string, super bool, perms []entity.AdminPermission) error {
	defer s.InvalidateAdminAccount(handle)
	cur, err := s.adminRepository.GetAdminByUsername(ctx, handle)
	switch {
	case err == nil:
		if !cur.Disabled {
			if err := s.adminRepository.SetAccountDisabled(ctx, handle, true); err != nil {
				return fmt.Errorf("stage team admin: disable: %w", err)
			}
		}
		if err := s.adminRepository.SetAccountPermissions(ctx, handle, super, perms); err != nil {
			return fmt.Errorf("stage team admin: permissions: %w", err)
		}
		return nil
	case errors.Is(err, sql.ErrNoRows):
		hash, err := platformauth.UnusableHash(s.pwhash)()
		if err != nil {
			return fmt.Errorf("stage team admin: hash: %w", err)
		}
		if err := s.adminRepository.AddAccount(ctx, handle, hash, super, perms); err != nil {
			return fmt.Errorf("stage team admin: add: %w", err)
		}
		if err := s.adminRepository.SetAccountDisabled(ctx, handle, true); err != nil {
			return fmt.Errorf("stage team admin: disable: %w", err)
		}
		return nil
	default:
		return fmt.Errorf("stage team admin: read: %w", err)
	}
}

// enableTeamAdmin enables a staged admins row in the pinned team (idempotent).
func (s *Server) enableTeamAdmin(ctx context.Context, handle string) error {
	defer s.InvalidateAdminAccount(handle)
	cur, err := s.adminRepository.GetAdminByUsername(ctx, handle)
	if err != nil {
		return fmt.Errorf("enable team admin: read: %w", err)
	}
	if !cur.Disabled {
		return nil
	}
	if err := s.adminRepository.SetAccountDisabled(ctx, handle, false); err != nil {
		return fmt.Errorf("enable team admin: %w", err)
	}
	return nil
}
