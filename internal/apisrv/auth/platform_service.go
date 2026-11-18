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
	"github.com/jekabolt/grbpwr-manager/internal/teamsync"
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
		EnsureMember: func(ctx context.Context, team tenant.Team, handle string, super bool, perms []entity.AdminPermission) error {
			return teamsync.EnsureAdmin(tenant.With(ctx, team), s.adminRepository, handle, super, perms)
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
