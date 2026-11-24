package mail

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	netmail "net/mail"
	"strings"
	"sync"
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/dependency"
	"github.com/jekabolt/grbpwr-manager/internal/dto"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/jekabolt/grbpwr-manager/internal/health"
	"github.com/jekabolt/grbpwr-manager/internal/saferun"
	"github.com/jekabolt/grbpwr-manager/internal/tenant"
	resend "github.com/jekabolt/grbpwr-manager/openapi/gen/resend"
)

// The shop lane: a team shop's customer letters (sign-in codes, order letters) over the platform
// sender, each team's outbox drained on its own. It never sends marketing, never a row queued
// before the shop last opened, never a sign-in letter past its TTL, and never as GRBPWR.

var (
	// ErrShopMailNoTeam: the context names no team, or names the storefront team (its letters are
	// the storefront mailer's).
	ErrShopMailNoTeam = errors.New("mail: shop letter without a shop team")
	// ErrShopMailClosed: the team's shop is off or closed by the operator; nothing is queued.
	ErrShopMailClosed = errors.New("mail: team shop is not open")
)

const (
	shopClaimBatch   = 10               // rows one team leases per tick: no team floods a tick
	shopTeamTimeout  = 15 * time.Second // one team's tick, its own deadline
	shopSharedPoll   = 50 * time.Millisecond
	shopOwnerTimeout = 5 * time.Second // recording an accepted letter's team, past the tick's deadline
	// shopOwnerSweepEvery is how often a team's outbox is searched for owners still owed when this
	// instance saw no failure (another instance's, or a crash between send and record).
	shopOwnerSweepEvery  = 10 * time.Minute
	defaultShopSignInTTL = 10 * time.Minute
	// defaultShopWorkerInterval stands in for an unset mailer worker interval (a ticker needs > 0).
	defaultShopWorkerInterval = 5 * time.Second
)

// shopKinds are the only kinds the shop lane queues and drains.
func shopKinds() []string { return []string{entity.MailKindShopSignIn, entity.MailKindShopOrder} }

// ShopIdentity is what the shop lane knows about a team: whether its shop is open (on, not closed by
// the operator), the name its letters carry (brand name, else team name), its primary host, when
// the shop last opened (rows queued before it never go out) and every other team's names (a shop
// may not read as another team).
type ShopIdentity struct {
	TeamID   int64
	Open     bool
	Name     string
	Host     string
	OpenedAt time.Time
	Taken    []string
}

// ShopResolver answers a team's identity; the app reads the platform store and caches it.
type ShopResolver func(ctx context.Context, teamID int64) (ShopIdentity, error)

// ShopState is a team shop's switch as stored right now: open (on, not closed by the operator) and
// when it last opened.
type ShopState struct {
	Open     bool
	OpenedAt time.Time
}

// ShopStateReader reads a team shop's state from the platform store, never from a cache: the worker
// asks it before every drain and again right before each provider call.
type ShopStateReader func(ctx context.Context, teamID int64) (ShopState, error)

// ShopOwnerRecorder remembers which team sent a letter Resend accepted (by Resend's email id), so a
// bounce or complaint event can reach that team's suppression list.
type ShopOwnerRecorder func(ctx context.Context, teamID int64, mailID int, providerID string) error

// ShopTeamLoop runs tick once per active team, each with its team's context, and returns whether
// the tick ran (the app's background team loop).
type ShopTeamLoop func(ctx context.Context, tick func(context.Context) bool) bool

// ShopConfig is the shop lane's setup: the platform's Resend key and sender, and the storefront's
// pacing and retry settings.
type ShopConfig struct {
	APIKey string
	// From is PLATFORM_MAIL_FROM; only its address is used, under "<shop> via lekalo".
	From string
	// KeySpace tells installations apart in idempotency keys (the platform base domain).
	KeySpace string
	// StorefrontTeamID's outbox is the storefront mailer's; 0 under none.
	StorefrontTeamID int64
	// SignInTTL is how long a sign-in letter may wait in the outbox (the login challenge's TTL).
	SignInTTL                  time.Duration
	WorkerInterval             time.Duration
	MaxSendAttempts            int
	RetryBaseInterval          time.Duration
	RetryMaxInterval           time.Duration
	ResendRequestsPerSecond    float64
	ResendBurst                int
	TransactionalReserveTokens int
	// State is the authoritative shop state (required); the cached identity is only for rendering.
	State ShopStateReader
	// Owner, optional, records each accepted letter's team for the bounce route.
	Owner ShopOwnerRecorder
}

// ShopMailer queues and sends team shops' customer letters.
type ShopMailer struct {
	inner   *Mailer // templates, catalog and the Resend client; its own worker never runs
	repo    dependency.Mail
	c       ShopConfig
	addr    string
	resolve ShopResolver
	loop    ShopTeamLoop
	acquire func(context.Context) error
	tracker health.Tracker

	mu    sync.Mutex
	holds map[int64]shopHold
	// cools: a team whose send got a 429 waits out the Retry-After on its own; the other shops and
	// the storefront's letters go on.
	cools map[int64]time.Time
	// owed: a team with letter owners not recorded yet (sweepOwners); swept: its last sweep.
	owed  map[int64]bool
	swept map[int64]time.Time

	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup
}

// shopHold is one team's backoff after failed ticks; other teams go on.
type shopHold struct {
	failures int
	until    time.Time
}

// NewShop builds the shop lane. storefront is the storefront's own mailer, nil under none: when it
// sends over the same Resend key the lanes share its budget, the storefront first (TryShared).
func NewShop(c ShopConfig, repo dependency.Mail, resolve ShopResolver, loop ShopTeamLoop, storefront dependency.Mailer) (*ShopMailer, error) {
	if strings.TrimSpace(c.APIKey) == "" {
		return nil, fmt.Errorf("shop mail: no Resend key")
	}
	from, err := netmail.ParseAddress(strings.TrimSpace(c.From))
	if err != nil || from.Address == "" {
		return nil, fmt.Errorf("shop mail: PLATFORM_MAIL_FROM is not an address")
	}
	if repo == nil || resolve == nil || loop == nil || c.State == nil {
		return nil, fmt.Errorf("shop mail: incomplete wiring")
	}
	if c.SignInTTL <= 0 {
		c.SignInTTL = defaultShopSignInTTL
	}
	inner, err := new(&Config{
		APIKey:                     c.APIKey,
		FromEmail:                  from.Address,
		FromName:                   "lekalo",
		WorkerInterval:             c.WorkerInterval,
		MaxSendAttempts:            c.MaxSendAttempts,
		RetryBaseInterval:          c.RetryBaseInterval,
		RetryMaxInterval:           c.RetryMaxInterval,
		ResendRequestsPerSecond:    c.ResendRequestsPerSecond,
		ResendBurst:                c.ResendBurst,
		TransactionalReserveTokens: c.TransactionalReserveTokens,
		// English only: a recipient's language lives in a team database the render must not read.
		LocalizationEnabled: false,
	}, repo, nil)
	if err != nil {
		return nil, fmt.Errorf("shop mail: %w", err)
	}
	s := &ShopMailer{
		inner:   inner,
		repo:    repo,
		c:       c,
		addr:    from.Address,
		resolve: resolve,
		loop:    loop,
		holds:   map[int64]shopHold{},
		cools:   map[int64]time.Time{},
		owed:    map[int64]bool{},
		swept:   map[int64]time.Time{},
	}
	s.c.WorkerInterval = inner.c.WorkerInterval
	if s.c.WorkerInterval <= 0 {
		s.c.WorkerInterval = defaultShopWorkerInterval
	}
	s.c.MaxSendAttempts = inner.c.MaxSendAttempts
	s.c.RetryBaseInterval = inner.c.RetryBaseInterval
	s.c.RetryMaxInterval = inner.c.RetryMaxInterval
	// A shop's 429 cools that shop only (coolTeam); the storefront's own cooldowns still stop the
	// shared lane (TryShared refuses during them).
	if sf, ok := storefront.(*Mailer); ok && sf != nil && sf.c.APIKey == c.APIKey {
		s.acquire = sharedAcquire(sf.budget)
	} else {
		s.acquire = inner.budget.WaitTransactional
	}
	return s, nil
}

// sharedAcquire takes a token from the storefront's budget only while no storefront send waits and
// the transactional reserve stays whole; it polls until the team's deadline, then gives up (the row
// goes back to the queue, no attempt counted).
func sharedAcquire(b *ResendBudget) func(context.Context) error {
	return func(ctx context.Context) error {
		for {
			if b.TryShared() {
				return nil
			}
			t := time.NewTimer(shopSharedPoll)
			select {
			case <-ctx.Done():
				t.Stop()
				return ctx.Err()
			case <-t.C:
			}
		}
	}
}

// Name implements health.Reporter.
func (s *ShopMailer) Name() string { return "shopmail" }

// LastSuccess implements health.Reporter.
func (s *ShopMailer) LastSuccess() time.Time { return s.tracker.LastSuccess() }

// queue renders a shop letter and writes it to the team's outbox. The worker sends it.
func (s *ShopMailer) queue(ctx context.Context, rep dependency.Repository, to string, tn templateName, data any, kind string) error {
	team, ok := tenant.From(ctx)
	if !ok || team.ID <= 0 || team.ID == s.c.StorefrontTeamID {
		return ErrShopMailNoTeam
	}
	id, err := s.resolve(ctx, team.ID)
	if err != nil {
		return fmt.Errorf("shop mail: team identity: %w", err)
	}
	if !id.Open {
		return ErrShopMailClosed
	}
	req, err := s.inner.buildRequest(to, tn, data, newShopBrand(id, s.addr))
	if err != nil {
		return fmt.Errorf("shop mail: build %s: %w", tn, err)
	}
	row, err := dto.ResendSendEmailRequestToEntity(req)
	if err != nil {
		return fmt.Errorf("shop mail: %w", err)
	}
	row.Kind = sql.NullString{String: kind, Valid: true}
	if kind == entity.MailKindShopSignIn {
		row.ExpiresAt = sql.NullTime{Time: time.Now().UTC().Add(s.c.SignInTTL), Valid: true}
	}
	if _, err := rep.Mail().AddMail(ctx, row); err != nil {
		return fmt.Errorf("shop mail: queue: %w", err)
	}
	return nil
}

// QueueAccountLogin queues a shop's sign-in letter; it expires with the login challenge.
func (s *ShopMailer) QueueAccountLogin(ctx context.Context, rep dependency.Repository, to, otpCode, magicLinkURL string) error {
	data := &struct {
		Preheader    string
		EmailB64     string
		OTPCode      string
		MagicLinkURL string
	}{Preheader: "", EmailB64: " ", OTPCode: otpCode, MagicLinkURL: magicLinkURL}
	return s.queue(ctx, rep, to, AccountLogin, data, entity.MailKindShopSignIn)
}

func (s *ShopMailer) QueueOrderConfirmation(ctx context.Context, rep dependency.Repository, to string, d *dto.OrderConfirmed) error {
	if d == nil || d.OrderUUID == "" {
		return fmt.Errorf("incomplete order details")
	}
	return s.queue(ctx, rep, to, OrderConfirmed, d, entity.MailKindShopOrder)
}

func (s *ShopMailer) SendOrderCancellation(ctx context.Context, rep dependency.Repository, to string, d *dto.OrderCancelled) error {
	if d == nil || d.OrderUUID == "" {
		return fmt.Errorf("incomplete order details")
	}
	return s.queue(ctx, rep, to, OrderCancelled, d, entity.MailKindShopOrder)
}

func (s *ShopMailer) SendOrderShipped(ctx context.Context, rep dependency.Repository, to string, d *dto.OrderShipment) error {
	if d == nil || d.OrderUUID == "" {
		return fmt.Errorf("incomplete shipment details")
	}
	return s.queue(ctx, rep, to, OrderShipped, d, entity.MailKindShopOrder)
}

func (s *ShopMailer) SendOrderDelivered(ctx context.Context, rep dependency.Repository, to string, d *dto.OrderDelivered) error {
	if d == nil || d.OrderUUID == "" {
		return fmt.Errorf("incomplete delivery details")
	}
	return s.queue(ctx, rep, to, OrderDelivered, d, entity.MailKindShopOrder)
}

func (s *ShopMailer) SendRefundInitiated(ctx context.Context, rep dependency.Repository, to string, d *dto.OrderRefundInitiated) error {
	if d == nil || d.OrderUUID == "" {
		return fmt.Errorf("incomplete refund details")
	}
	return s.queue(ctx, rep, to, OrderRefundInitiated, d, entity.MailKindShopOrder)
}

func (s *ShopMailer) SendPendingReturn(ctx context.Context, rep dependency.Repository, to string, d *dto.OrderPendingReturn) error {
	if d == nil || d.OrderUUID == "" {
		return fmt.Errorf("incomplete return details")
	}
	return s.queue(ctx, rep, to, OrderPendingReturn, d, entity.MailKindShopOrder)
}

// Start runs the worker; call it once the team directory exists.
func (s *ShopMailer) Start(ctx context.Context) error {
	if s.cancel != nil {
		return fmt.Errorf("shop mailer already started")
	}
	s.ctx, s.cancel = context.WithCancel(ctx)
	s.wg.Go(func() {
		t := time.NewTicker(s.c.WorkerInterval)
		defer t.Stop()
		for {
			select {
			case <-t.C:
				s.runOnce(s.ctx)
			case <-s.ctx.Done():
				return
			}
		}
	})
	return nil
}

// Stop ends the worker and waits for it.
func (s *ShopMailer) Stop() error {
	if s.cancel == nil {
		return fmt.Errorf("shop mailer not started")
	}
	s.cancel()
	s.cancel = nil
	s.wg.Wait()
	return nil
}

// runOnce visits every active team once. A team's failure holds that team only; the tick counts as
// a success when every team it visited went through.
func (s *ShopMailer) runOnce(ctx context.Context) bool {
	defer saferun.Recover(ctx, "shopmail")
	var mu sync.Mutex
	var firstErr error
	ran := s.loop(ctx, func(teamCtx context.Context) bool {
		if err := s.teamTick(teamCtx, time.Now()); err != nil {
			mu.Lock()
			if firstErr == nil {
				firstErr = err
			}
			mu.Unlock()
		}
		return true // a failing team is held on its own; it never fails the other teams' tick
	})
	switch {
	case firstErr != nil:
		s.tracker.MarkError(firstErr)
		return false
	case ran:
		s.tracker.MarkSuccess()
		return true
	}
	return false
}

// teamTick drains one team's outbox unless the team is the storefront, held, cooling after a 429,
// or has no open shop.
func (s *ShopMailer) teamTick(ctx context.Context, now time.Time) error {
	team, ok := tenant.From(ctx)
	if !ok || team.ID <= 0 || team.ID == s.c.StorefrontTeamID {
		return nil
	}
	if s.held(team.ID, now) || s.cooling(team.ID, now) {
		return nil
	}
	err := s.drainTeam(ctx, team.ID)
	if err != nil && ctx.Err() != nil && errors.Is(ctx.Err(), context.Canceled) {
		return nil // shutdown
	}
	s.settle(team.ID, err, time.Now())
	if err != nil {
		slog.Default().ErrorContext(ctx, "shop mail: team tick failed; team held",
			slog.Int64("tid", team.ID), slog.String("err", err.Error()))
	}
	return err
}

// drainTeam quarantines what the cutover forbids, then sends what is due, under the team's own
// deadline. The shop state is read fresh, not from the identity cache.
func (s *ShopMailer) drainTeam(ctx context.Context, teamID int64) error {
	s.sweepOwners(ctx, teamID, time.Now())
	st, err := s.c.State(ctx, teamID)
	if err != nil {
		return fmt.Errorf("shop state: %w", err)
	}
	if !st.Open || st.OpenedAt.IsZero() {
		return nil // no open shop: its outbox waits, and the next opening quarantines it
	}
	tctx, cancel := context.WithTimeout(ctx, shopTeamTimeout)
	defer cancel()
	n, err := s.repo.QuarantineOutside(tctx, entity.MailCutover{
		Kinds:    shopKinds(),
		OpenedAt: st.OpenedAt,
		Now:      time.Now().UTC(),
		Reason:   "shop cutover: not a shop letter, expired, or queued before the shop opened",
	})
	if err != nil {
		return fmt.Errorf("quarantine: %w", err)
	}
	if n > 0 {
		slog.Default().WarnContext(ctx, "shop mail: outbox rows quarantined",
			slog.Int64("tid", teamID), slog.Int64("rows", n))
	}
	return s.lane(teamID, st.OpenedAt).drain(tctx)
}

func (s *ShopMailer) lane(teamID int64, openedAt time.Time) *outboxLane {
	return &outboxLane{
		name:        "shopmail",
		repo:        s.repo,
		cli:         s.inner.cli,
		teamID:      teamID,
		keySpace:    outboxKeySpace("shop", s.c.KeySpace, s.addr),
		kinds:       shopKinds(),
		notBefore:   openedAt,
		batch:       shopClaimBatch,
		rounds:      1,
		maxAttempts: s.c.MaxSendAttempts,
		retryBase:   s.c.RetryBaseInterval,
		retryMax:    s.c.RetryMaxInterval,
		acquire:     s.acquire,
		cooldown:    func(d time.Duration) { s.coolTeam(teamID, d, time.Now()) },
		prepare:     s.prepareRow(teamID, openedAt),
		gate:        s.dispatchGate(teamID),
		accepted:    s.recordOwner(teamID),
	}
}

// dispatchGate reads the shop's state again right before the provider call: a shop closed meanwhile
// holds the row (back to the queue), and a row older than a reopening is quarantined.
func (s *ShopMailer) dispatchGate(teamID int64) rowGate {
	return func(ctx context.Context, row *entity.SendEmailRequest) (rowVerdict, string, error) {
		st, err := s.c.State(ctx, teamID)
		switch {
		case err != nil:
			return rowHold, "", err
		case !st.Open || st.OpenedAt.IsZero():
			return rowHold, "", nil
		case row.CreatedAt.Unix() < st.OpenedAt.Unix():
			return rowQuarantine, "queued before the shop opened", nil
		}
		return rowSend, "", nil
	}
}

// recordOwner remembers the team of an accepted letter before the row is settled sent. A failure
// is returned (the row is then settled sent with its owner pending) and marks the team, so its next
// drain records the owner (sweepOwners); the letter itself is never sent again for it.
func (s *ShopMailer) recordOwner(teamID int64) func(context.Context, *entity.SendEmailRequest, string) error {
	if s.c.Owner == nil {
		return nil
	}
	return func(ctx context.Context, row *entity.SendEmailRequest, providerID string) error {
		if err := s.ownerWrite(ctx, teamID, row.Id, providerID); err != nil {
			s.mu.Lock()
			s.owed[teamID] = true
			s.mu.Unlock()
			slog.Default().WarnContext(ctx, "shop mail: letter owner not recorded; kept for the next drain",
				slog.Int64("tid", teamID), slog.Int("mailId", row.Id), slog.String("err", err.Error()))
			return err
		}
		return nil
	}
}

// ownerWrite records one owner under its own deadline, past the tick's.
func (s *ShopMailer) ownerWrite(ctx context.Context, teamID int64, mailID int, providerID string) error {
	octx, cancel := context.WithTimeout(context.WithoutCancel(ctx), shopOwnerTimeout)
	defer cancel()
	return s.c.Owner(octx, teamID, mailID, providerID)
}

// sweepOwners records the owners a team still owes (rows settled sent with owner_pending_at): at
// once after a failure in this process, and every shopOwnerSweepEvery anyway (another instance's
// failure, a crash). A failure leaves the rows marked and never fails the team's tick: sending goes
// on, and the bounce route asks Resend to retry an event whose owner is not known yet.
func (s *ShopMailer) sweepOwners(ctx context.Context, teamID int64, now time.Time) {
	if s.c.Owner == nil {
		return
	}
	s.mu.Lock()
	due := s.owed[teamID] || !now.Before(s.swept[teamID].Add(shopOwnerSweepEvery))
	if due {
		s.swept[teamID] = now
	}
	s.mu.Unlock()
	if !due {
		return
	}
	ctx, cancel := context.WithTimeout(ctx, shopTeamTimeout)
	defer cancel()
	fail := func(err error) {
		s.mu.Lock()
		s.owed[teamID] = true
		s.mu.Unlock()
		slog.Default().WarnContext(ctx, "shop mail: owed letter owners not recorded yet",
			slog.Int64("tid", teamID), slog.String("err", err.Error()))
	}
	rows, err := s.repo.MailOwnersDue(ctx, shopClaimBatch)
	if err != nil {
		fail(err)
		return
	}
	for _, r := range rows {
		if err := s.ownerWrite(ctx, teamID, r.ID, r.ProviderID); err != nil {
			fail(err)
			return
		}
		if err := s.repo.ClearMailOwnerDue(ctx, r.ID); err != nil {
			fail(err)
			return
		}
	}
	s.mu.Lock()
	s.owed[teamID] = len(rows) >= shopClaimBatch // a full page: more may wait
	s.mu.Unlock()
}

// prepareRow checks a claimed row again before it reaches the provider: a shop kind, not expired, not
// older than the opening, the platform address under a "via lekalo" name that is not reserved, a
// valid recipient. Anything else is quarantined, never sent. No reply-to, no headers.
func (s *ShopMailer) prepareRow(teamID int64, openedAt time.Time) rowPrepare {
	return func(row *entity.SendEmailRequest) (resend.SendEmailRequest, rowVerdict, string, error) {
		now := time.Now().UTC()
		kind := row.Kind.String
		switch {
		case !row.Kind.Valid || (kind != entity.MailKindShopSignIn && kind != entity.MailKindShopOrder):
			return resend.SendEmailRequest{}, rowQuarantine, "not a shop letter", nil
		case row.ExpiresAt.Valid && !row.ExpiresAt.Time.After(now):
			return resend.SendEmailRequest{}, rowQuarantine, "expired", nil
		case kind == entity.MailKindShopSignIn && !row.ExpiresAt.Valid:
			return resend.SendEmailRequest{}, rowQuarantine, "sign-in letter without expiry", nil
		case row.CreatedAt.Unix() < openedAt.Unix():
			return resend.SendEmailRequest{}, rowQuarantine, "queued before the shop opened", nil
		}
		if !s.shopSenderOK(row.From) {
			return resend.SendEmailRequest{}, rowQuarantine, "sender is not the shop sender", nil
		}
		to, err := validateEmailAddress(row.To)
		if err != nil {
			return resend.SendEmailRequest{}, rowQuarantine, "invalid recipient", nil
		}
		html := row.Html
		text := htmlToText(html)
		lane, team := "lane", "team"
		laneV, teamV := "shop", fmt.Sprintf("t%d", teamID)
		return resend.SendEmailRequest{
			From:    row.From,
			To:      []string{to},
			Html:    &html,
			Text:    &text,
			Subject: row.Subject,
			Tags:    &[]resend.Tag{{Name: &lane, Value: &laneV}, {Name: &team, Value: &teamV}},
		}, rowSend, "", nil
	}
}

// shopSenderOK: the platform address, under "<name> via lekalo" where the name does not read as
// grbpwr or lekalo.
func (s *ShopMailer) shopSenderOK(from string) bool {
	a, err := netmail.ParseAddress(from)
	if err != nil || !strings.EqualFold(a.Address, s.addr) {
		return false
	}
	name, ok := strings.CutSuffix(a.Name, shopSenderSuffix)
	if !ok || strings.TrimSpace(name) == "" {
		return false
	}
	return !readsReserved(name)
}

// held reports whether a team is backing off after failed ticks.
func (s *ShopMailer) held(teamID int64, now time.Time) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	h, ok := s.holds[teamID]
	return ok && now.Before(h.until)
}

// cooling reports whether a team waits out a 429's Retry-After.
func (s *ShopMailer) cooling(teamID int64, now time.Time) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	until, ok := s.cools[teamID]
	if ok && !now.Before(until) {
		delete(s.cools, teamID)
		return false
	}
	return ok
}

// coolTeam holds one team for a 429's Retry-After (the longest wins).
func (s *ShopMailer) coolTeam(teamID int64, d time.Duration, now time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if until := now.Add(d); until.After(s.cools[teamID]) {
		s.cools[teamID] = until
	}
}

// settle records a team's tick: a success clears its hold, a failure extends it (30s doubling to 5m).
func (s *ShopMailer) settle(teamID int64, err error, now time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err == nil {
		delete(s.holds, teamID)
		return
	}
	h := s.holds[teamID]
	h.failures++
	h.until = now.Add(backoffDelay(h.failures))
	s.holds[teamID] = h
}
