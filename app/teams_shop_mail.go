package app

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/dependency"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/jekabolt/grbpwr-manager/internal/mail"
	"github.com/jekabolt/grbpwr-manager/internal/platformmail"
	"github.com/jekabolt/grbpwr-manager/internal/platformstore"
	"github.com/jekabolt/grbpwr-manager/internal/tenant"
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
	// shopEventRetryWindow: a bounce or complaint whose letter is not known yet (its owner is
	// recorded right after Resend accepts it, or by a later drain after a failure) is refused while
	// younger than this, so Resend retries it; an older one is dropped with a log line.
	shopEventRetryWindow = time.Hour
	defaultLoginTTL      = 10 * time.Minute // storefront_auth.login_challenge_ttl's own default
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
	if !p.ShopMailEnabled {
		return nil, "PLATFORM_SHOP_MAIL_ENABLED is off"
	}
	if !p.MailerEnabled {
		return nil, "PLATFORM_MAILER_ENABLED is off"
	}
	key := shopResendKey(p.ShopResendAPIKey, p.ResendAPIKey, a.c.Mailer.APIKey)
	if key == "" {
		return nil, "no Resend key (PLATFORM_SHOP_RESEND_API_KEY, PLATFORM_RESEND_API_KEY or the mailer key)"
	}
	if strings.TrimSpace(p.MailFrom) == "" {
		return nil, "PLATFORM_MAIL_FROM is not set"
	}
	if !shopEventsRouted(p.ShopResendAPIKey, p.ShopMailWebhookSecret, p.MailWebhookSecret) {
		return nil, "no signed route for the shop letters' bounces (PLATFORM_SHOP_MAIL_WEBHOOK_SECRET, " +
			"or PLATFORM_MAIL_WEBHOOK_SECRET when the shop letters share the platform key)"
	}
	mc := a.c.Mailer
	ids := newShopIdentities(a.platform)
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
		State:                      ids.state,
		Owner: func(ctx context.Context, teamID int64, mailID int, providerID string) error {
			return a.platform.RecordShopMailOwner(ctx, providerID, teamID, int64(mailID))
		},
	}, a.db.Mail(), ids.resolve,
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

// shopResendKey: the shop letters' own key, else the platform letters' key, else the mailer key.
// Only a key of a separate Resend account has its own rate limit and quota.
func shopResendKey(shop, platform, mailer string) string {
	for _, k := range []string{shop, platform, mailer} {
		if k = strings.TrimSpace(k); k != "" {
			return k
		}
	}
	return ""
}

// shopEventsRouted: the shop letters' bounces and complaints reach a signed route. Their own route
// (/api/webhooks/shop-mail) when its secret is set; else, with no shop key, the letters go over the
// platform letters' key (the same fallback chain), so their events arrive at platform-mail, which
// routes them on when its own secret is set. Without either the lane would send letters whose
// bounces nobody records, so it stays off.
func shopEventsRouted(shopKey, shopSecret, platformSecret string) bool {
	if strings.TrimSpace(shopSecret) != "" {
		return true
	}
	return strings.TrimSpace(shopKey) == "" && strings.TrimSpace(platformSecret) != ""
}

// mountShopMailEvents routes the team shops' Resend events into each team's suppression list:
// through the platform-mail route (a shared Resend account; ph may be nil when that route is off)
// and, with PLATFORM_SHOP_MAIL_WEBHOOK_SECRET, through /api/webhooks/shop-mail (the shops' own
// account). Neither depends on the storefront's webhooks, so both work under none.
func (a *App) mountShopMailEvents(ctx context.Context, ph *platformmail.EventHandler) error {
	b := &shopMailBounces{
		owners: a.platform,
		teams:  func() tenant.Directory { return a.workerTeams },
		mail:   a.db.Mail,
		now:    time.Now,
	}
	if ph != nil {
		ph.SetForeignLetters(b.apply)
	}
	secret := strings.TrimSpace(a.c.Platform.ShopMailWebhookSecret)
	if secret == "" {
		return nil // the lane checked a route exists (shopEventsRouted) or stays off
	}
	h, err := platformmail.NewForeignEventHandler(secret, b.apply)
	if err != nil {
		return err
	}
	a.hs.SetShopMailHandler(h)
	return nil
}

// shopMailOwners is what the bounce route reads from the control plane.
type shopMailOwners interface {
	ShopMailOwner(ctx context.Context, providerID string) (teamID, mailID int64, ok bool, err error)
}

// shopMailBounces puts the recipient of a team shop's letter on that team's own suppression list on
// a permanent bounce, Resend's own suppression or a complaint. Only a letter the shop lane sent
// (shop_mail_owner) of a live team counts, and only when the event names that letter's recipient;
// everything else is ignored. The list is honoured by the outbox claim before every send.
type shopMailBounces struct {
	owners shopMailOwners
	teams  func() tenant.Directory // read per event: the directory is built after the routes
	mail   func() dependency.Mail  // routed by the team in ctx
	now    func() time.Time        // nil: time.Now
}

// errShopLetterNotKnownYet makes Resend retry an event whose letter is not on record yet.
var errShopLetterNotKnownYet = errors.New("shop mail events: letter not on record yet")

// notKnown answers an event whose letter is not on record: a retry while it is young (the owner may
// still be recorded), else nothing.
func (b *shopMailBounces) notKnown(ctx context.Context, ev platformmail.Event, what string) error {
	now := time.Now
	if b.now != nil {
		now = b.now
	}
	if age := now().Sub(ev.At); age < shopEventRetryWindow {
		return errShopLetterNotKnownYet
	}
	slog.Default().WarnContext(ctx, "shop mail: bounce of a letter not on record dropped",
		slog.String("what", what), slog.String("type", ev.Type))
	return nil
}

func (b *shopMailBounces) apply(ctx context.Context, ev platformmail.Event) error {
	var reason entity.SuppressionReason
	switch ev.SuppressionReason() {
	case platformmail.ReasonBounce:
		reason = entity.SuppressionReasonBounce
	case platformmail.ReasonComplaint:
		reason = entity.SuppressionReasonComplaint
	default:
		return nil
	}
	teamID, mailID, ok, err := b.owners.ShopMailOwner(ctx, ev.EmailID)
	if err != nil {
		return err
	}
	if !ok {
		return b.notKnown(ctx, ev, "no owner")
	}
	dir := b.teams()
	if dir == nil {
		return errors.New("shop mail events: team directory not ready")
	}
	team, err := dir.ByID(ctx, teamID)
	if errors.Is(err, tenant.ErrNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	if team.Status != tenant.StatusActive {
		return nil // a suspended team's database is read-only; a deleted one is gone
	}
	tctx := tenant.With(ctx, team)
	m := b.mail()
	to, err := m.ProviderMailRecipient(tctx, int(mailID), ev.EmailID)
	if err != nil {
		return err
	}
	if to == "" {
		// The owner is recorded before the row is settled sent: the row may not carry the id yet.
		return b.notKnown(ctx, ev, "row without the id")
	}
	if !recipientNamed(to, ev.To) {
		return nil
	}
	if err := m.AddSuppression(tctx, to, reason); err != nil {
		return err
	}
	slog.Default().InfoContext(ctx, "shop mail: address suppressed",
		slog.Int64("tid", teamID), slog.Int64("mailId", mailID), slog.String("reason", string(reason)))
	return nil
}

// recipientNamed: the event names the letter's own address (case-insensitively).
func recipientNamed(letterTo string, to []string) bool {
	for _, t := range to {
		if strings.EqualFold(strings.TrimSpace(t), strings.TrimSpace(letterTo)) {
			return true
		}
	}
	return false
}

// loginChallengeTTL is how long a sign-in letter may wait: the storefront login challenge's TTL.
func loginChallengeTTL(v string) time.Duration {
	if d, err := time.ParseDuration(strings.TrimSpace(v)); err == nil && d > 0 {
		return d
	}
	return defaultLoginTTL
}

// state is a mail.ShopStateReader: the shop's switch read from the control plane now, never from
// the cache. A shop found not open also drops its cached identity, so queueing sees it at once.
func (s *shopIdentities) state(ctx context.Context, teamID int64) (mail.ShopState, error) {
	ts, err := s.p.TeamShop(ctx, teamID)
	if err != nil {
		return mail.ShopState{}, err
	}
	if !ts.Enabled || ts.ClosedByOperator || ts.EnabledAt == nil {
		s.mu.Lock()
		if e, ok := s.teams[teamID]; ok && e.id.Open {
			delete(s.teams, teamID)
		}
		s.mu.Unlock()
		return mail.ShopState{}, nil
	}
	return mail.ShopState{Open: true, OpenedAt: ts.EnabledAt.UTC()}, nil
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
