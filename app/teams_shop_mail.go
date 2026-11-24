package app

import (
	"context"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/dependency"
	"github.com/jekabolt/grbpwr-manager/internal/mail"
	"github.com/jekabolt/grbpwr-manager/internal/platformstore"
)

// Team-shop customer mail (X18-L7): in teams mode the tenant mailer is wrapped in mail.ShopRouter,
// so a team shop's sign-in and order letters go to the shop lane (its own outbox, "<shop> via
// lekalo" over the platform sender) and nothing else of a team shop is ever sent as the
// storefront. A missing piece of config leaves the lane off with a /statusz problem; the server
// still boots.

const (
	shopMailOpenTTL   = 30 * time.Second // an open shop's identity (name, host, opening)
	shopMailClosedTTL = 5 * time.Second  // a shop that is not open: a fresh opening shows up fast
	shopMailNamesTTL  = time.Minute      // every team's names, for the look-alike check
	defaultLoginTTL   = 10 * time.Minute // storefront_auth.login_challenge_ttl's own default
)

// withShopMail wraps the tenant mailer in the team-shop router (teams mode only) and builds the
// shop lane when the platform mail config allows it.
func (a *App) withShopMail(ctx context.Context, storefront dependency.Mailer) dependency.Mailer {
	if a.platform == nil {
		return storefront
	}
	sm, problem := a.newShopMail(storefront)
	if problem != "" {
		a.noteProblem(ctx, "team-shop mail off: "+problem)
	}
	a.shopMail = sm
	return mail.NewShopRouter(storefront, sm, a.c.Platform.StorefrontTeamID)
}

// newShopMail builds the shop lane, or says which config is missing.
func (a *App) newShopMail(storefront dependency.Mailer) (*mail.ShopMailer, string) {
	p := a.c.Platform
	if !p.MailerEnabled {
		return nil, "PLATFORM_MAILER_ENABLED is off"
	}
	key := strings.TrimSpace(p.ResendAPIKey)
	if key == "" {
		key = strings.TrimSpace(a.c.Mailer.APIKey)
	}
	if key == "" {
		return nil, "no Resend key (PLATFORM_RESEND_API_KEY or the mailer key)"
	}
	if strings.TrimSpace(p.MailFrom) == "" {
		return nil, "PLATFORM_MAIL_FROM is not set"
	}
	mc := a.c.Mailer
	sm, err := mail.NewShop(mail.ShopConfig{
		APIKey:                     key,
		From:                       p.MailFrom,
		KeySpace:                   p.BaseDomain,
		StorefrontTeamID:           p.StorefrontTeamID,
		SignInTTL:                  loginChallengeTTL(a.c.StorefrontAuth.LoginChallengeTTL),
		WorkerInterval:             mc.WorkerInterval,
		MaxSendAttempts:            mc.MaxSendAttempts,
		RetryBaseInterval:          mc.RetryBaseInterval,
		RetryMaxInterval:           mc.RetryMaxInterval,
		ResendRequestsPerSecond:    mc.ResendRequestsPerSecond,
		ResendBurst:                mc.ResendBurst,
		TransactionalReserveTokens: mc.TransactionalReserveTokens,
	}, a.db.Mail(), newShopIdentities(a.platform).resolve,
		mail.ShopTeamLoop(a.workerTeamLoop("shopmail", activeTeams)), storefront)
	if err != nil {
		return nil, err.Error()
	}
	return sm, ""
}

// startShopMail runs the shop lane's worker; it needs the team directory (initWorkerTeams).
func (a *App) startShopMail(ctx context.Context) {
	if a.shopMail == nil {
		return
	}
	if err := a.shopMail.Start(ctx); err != nil {
		a.noteProblem(ctx, "team-shop mail off: "+err.Error())
		a.shopMail = nil
		return
	}
	slog.Default().InfoContext(ctx, "team-shop mail lane started")
}

// loginChallengeTTL is how long a sign-in letter may wait: the storefront login challenge's TTL.
func loginChallengeTTL(v string) time.Duration {
	if d, err := time.ParseDuration(strings.TrimSpace(v)); err == nil && d > 0 {
		return d
	}
	return defaultLoginTTL
}

// shopMailPlatform is what the identity lookup reads from the control plane.
type shopMailPlatform interface {
	TeamShop(ctx context.Context, teamID int64) (platformstore.TeamShop, error)
	ShopHosts(ctx context.Context, teamID int64) ([]platformstore.ShopHost, error)
	TeamMailNames(ctx context.Context) ([]platformstore.TeamMailName, error)
}

// shopIdentities caches team-shop identities for the mail lane. Errors are never cached.
type shopIdentities struct {
	p   shopMailPlatform
	now func() time.Time

	mu      sync.Mutex
	teams   map[int64]shopIdentityEntry
	names   []platformstore.TeamMailName
	namesAt time.Time
}

type shopIdentityEntry struct {
	id    mail.ShopIdentity
	until time.Time
}

func newShopIdentities(p shopMailPlatform) *shopIdentities {
	return &shopIdentities{p: p, now: time.Now, teams: map[int64]shopIdentityEntry{}}
}

// resolve is a mail.ShopResolver: whether the team's shop is open (on, not closed by the operator),
// when it last opened, its primary host, its name (brand name, else team name) and every other
// team's names.
func (s *shopIdentities) resolve(ctx context.Context, teamID int64) (mail.ShopIdentity, error) {
	now := s.now()
	s.mu.Lock()
	if e, ok := s.teams[teamID]; ok && now.Before(e.until) {
		s.mu.Unlock()
		return e.id, nil
	}
	s.mu.Unlock()

	id := mail.ShopIdentity{TeamID: teamID}
	ts, err := s.p.TeamShop(ctx, teamID)
	if err != nil {
		return mail.ShopIdentity{}, err
	}
	ttl := shopMailClosedTTL
	if ts.Enabled && !ts.ClosedByOperator && ts.EnabledAt != nil {
		names, err := s.teamNames(ctx, now)
		if err != nil {
			return mail.ShopIdentity{}, err
		}
		hosts, err := s.p.ShopHosts(ctx, teamID)
		if err != nil {
			return mail.ShopIdentity{}, err
		}
		id.Open, id.OpenedAt, ttl = true, ts.EnabledAt.UTC(), shopMailOpenTTL
		id.Host = shopLinkHost(hosts)
		for _, n := range names {
			if n.TeamID == teamID {
				id.Name = n.BrandName
				if strings.TrimSpace(id.Name) == "" {
					id.Name = n.Name
				}
				continue
			}
			for _, v := range []string{n.Name, n.BrandName} {
				if strings.TrimSpace(v) != "" {
					id.Taken = append(id.Taken, v)
				}
			}
		}
	}
	s.mu.Lock()
	s.teams[teamID] = shopIdentityEntry{id: id, until: now.Add(ttl)}
	s.mu.Unlock()
	return id, nil
}

// shopLinkHost is the host a shop letter links to: the main address when it is live and serves the
// shop itself, else the first live host that does (the platform subdomain comes first); "" for none.
func shopLinkHost(hosts []platformstore.ShopHost) string {
	serves := func(h platformstore.ShopHost) bool {
		return h.Status == platformstore.ShopHostLive && h.RedirectTo == ""
	}
	for _, h := range hosts {
		if h.Primary && serves(h) {
			return h.Host
		}
	}
	for _, h := range hosts {
		if serves(h) {
			return h.Host
		}
	}
	return ""
}

func (s *shopIdentities) teamNames(ctx context.Context, now time.Time) ([]platformstore.TeamMailName, error) {
	s.mu.Lock()
	if s.names != nil && now.Before(s.namesAt.Add(shopMailNamesTTL)) {
		names := s.names
		s.mu.Unlock()
		return names, nil
	}
	s.mu.Unlock()
	names, err := s.p.TeamMailNames(ctx)
	if err != nil {
		return nil, err
	}
	if names == nil {
		names = []platformstore.TeamMailName{}
	}
	s.mu.Lock()
	s.names, s.namesAt = names, now
	s.mu.Unlock()
	return names, nil
}
