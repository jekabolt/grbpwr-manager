package mail

import (
	"context"
	"database/sql"
	"errors"
	netmail "net/mail"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/dependency"
	"github.com/jekabolt/grbpwr-manager/internal/dto"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/jekabolt/grbpwr-manager/internal/tenant"
	"github.com/stretchr/testify/require"
)

const testPlatformAddr = "no-reply@lekalo.test"

// shopFixture is a shop lane over an in-memory outbox and a scripted provider.
type shopFixture struct {
	s    *ShopMailer
	repo *fakeOutbox
	cli  *fakeSender

	mu       sync.Mutex
	ids      map[int64]ShopIdentity
	idErr    map[int64]error
	resolves map[int64]int
}

func newShopFixture(t *testing.T, storefrontTeamID int64) *shopFixture {
	t.Helper()
	f := &shopFixture{
		repo:     newFakeOutbox(),
		cli:      &fakeSender{},
		ids:      map[int64]ShopIdentity{},
		idErr:    map[int64]error{},
		resolves: map[int64]int{},
	}
	s, err := NewShop(ShopConfig{
		APIKey:           "platform-key",
		From:             "lekalo <" + testPlatformAddr + ">",
		KeySpace:         "lekalo.test",
		StorefrontTeamID: storefrontTeamID,
		SignInTTL:        10 * time.Minute,
	}, f.repo, f.resolve, func(context.Context, func(context.Context) bool) bool { return true }, nil)
	require.NoError(t, err)
	s.inner.cli = f.cli
	s.acquire = func(context.Context) error { return nil }
	f.s = s
	return f
}

func (f *shopFixture) resolve(_ context.Context, teamID int64) (ShopIdentity, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.resolves[teamID]++
	if err := f.idErr[teamID]; err != nil {
		return ShopIdentity{}, err
	}
	id, ok := f.ids[teamID]
	if !ok {
		return ShopIdentity{TeamID: teamID}, nil
	}
	return id, nil
}

func (f *shopFixture) open(teamID int64, name, host string, openedAt time.Time, taken ...string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.ids[teamID] = ShopIdentity{TeamID: teamID, Open: true, Name: name, Host: host, OpenedAt: openedAt, Taken: taken}
}

func (f *shopFixture) resolveCount(teamID int64) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.resolves[teamID]
}

func teamCtx(id int64) context.Context {
	return tenant.With(context.Background(), tenant.Team{ID: id, Slug: "t", Status: tenant.StatusActive})
}

func sampleData(t *testing.T, tn templateName) any {
	t.Helper()
	for _, s := range emailSamples() {
		if s.tn == tn {
			return s.data
		}
	}
	t.Fatalf("no sample for %s", tn)
	return nil
}

// shopLetters calls every letter the shop lane queues, with the shared template samples.
func shopLetterCalls(t *testing.T, s *ShopMailer) map[string]func(context.Context, dependency.Repository) error {
	return map[string]func(context.Context, dependency.Repository) error{
		"sign-in": func(ctx context.Context, rep dependency.Repository) error {
			return s.QueueAccountLogin(ctx, rep, "customer@example.com", "482915", "https://acme.lekalo.test/account/verify?token=abc")
		},
		"confirmed": func(ctx context.Context, rep dependency.Repository) error {
			return s.QueueOrderConfirmation(ctx, rep, "customer@example.com", sampleData(t, OrderConfirmed).(*dto.OrderConfirmed))
		},
		"shipped": func(ctx context.Context, rep dependency.Repository) error {
			return s.SendOrderShipped(ctx, rep, "customer@example.com", sampleData(t, OrderShipped).(*dto.OrderShipment))
		},
		"delivered": func(ctx context.Context, rep dependency.Repository) error {
			return s.SendOrderDelivered(ctx, rep, "customer@example.com", sampleData(t, OrderDelivered).(*dto.OrderDelivered))
		},
		"cancelled": func(ctx context.Context, rep dependency.Repository) error {
			return s.SendOrderCancellation(ctx, rep, "customer@example.com", sampleData(t, OrderCancelled).(*dto.OrderCancelled))
		},
		"refund": func(ctx context.Context, rep dependency.Repository) error {
			return s.SendRefundInitiated(ctx, rep, "customer@example.com", sampleData(t, OrderRefundInitiated).(*dto.OrderRefundInitiated))
		},
		"return": func(ctx context.Context, rep dependency.Repository) error {
			return s.SendPendingReturn(ctx, rep, "customer@example.com", sampleData(t, OrderPendingReturn).(*dto.OrderPendingReturn))
		},
	}
}

// TestShopLettersCarryTheShopNotGRBPWR: every shop letter, queued and then sent, names the shop,
// links to the shop host, comes from "<shop> via lekalo" at the platform address, and never says
// grbpwr anywhere (subject, html, text, sender). No reply-to, no extra headers.
func TestShopLettersCarryTheShopNotGRBPWR(t *testing.T) {
	f := newShopFixture(t, 0)
	f.open(5, "Acme Studio", "acme.lekalo.test", time.Now().Add(-time.Hour), "grbpwr", "Other")
	rep := fakeRepo{mail: f.repo}
	for name, call := range shopLetterCalls(t, f.s) {
		t.Run(name, func(t *testing.T) {
			f.repo = newFakeOutbox()
			f.s.repo = f.repo
			f.cli.calls = nil
			rep.mail = f.repo
			require.NoError(t, call(teamCtx(5), rep))
			require.Len(t, f.repo.rows, 1)
			row := f.repo.row(1)
			require.Empty(t, row.ReplyTo)
			require.True(t, row.Kind.Valid)

			require.NoError(t, f.s.drainTeam(teamCtx(5), 5))
			sent := f.cli.sent()
			require.Len(t, sent, 1, "the queued letter goes out on the next drain")
			req := sent[0].req
			a, err := netmail.ParseAddress(req.From)
			require.NoError(t, err)
			require.Equal(t, testPlatformAddr, a.Address)
			require.Equal(t, "Acme Studio via lekalo", a.Name)
			require.Nil(t, req.ReplyTo)
			require.Nil(t, req.Headers)
			require.NotNil(t, req.Text)
			require.NotEmpty(t, *req.Text)
			for part, v := range map[string]string{"subject": req.Subject, "html": *req.Html, "text": *req.Text, "from": req.From} {
				require.NotContains(t, strings.ToLower(v), "grbpwr", "%s of the %s letter", part, name)
			}
			require.Contains(t, strings.ToUpper(*req.Html), "ACME STUDIO")
			require.NotContains(t, *req.Html, "grbpwr.com")
			if name != "sign-in" && name != "cancelled" && name != "refund" && name != "return" {
				require.Contains(t, *req.Html, "https://acme.lekalo.test", "%s links to the shop", name)
			}
			require.Equal(t, []string{"customer@example.com"}, req.To)
			require.NotNil(t, req.Tags)
			require.True(t, row.IdempotencyKey.Valid)
			require.Equal(t, row.IdempotencyKey.String, sent[0].key, "the row's own idempotency key")
			require.True(t, f.repo.row(1).Sent)
		})
	}
}

func TestShopLetterWithReservedNameFallsBackToShop(t *testing.T) {
	f := newShopFixture(t, 0)
	f.open(5, "GRBPWR", "", time.Now().Add(-time.Hour))
	require.NoError(t, f.s.QueueAccountLogin(teamCtx(5), fakeRepo{mail: f.repo}, "c@example.com", "111111", "https://x.test/v"))
	row := f.repo.row(1)
	a, err := netmail.ParseAddress(row.From)
	require.NoError(t, err)
	require.Equal(t, "shop via lekalo", a.Name)
	require.NotContains(t, strings.ToLower(row.Html+row.Subject), "grbpwr")
}

func TestShopQueueRefuses(t *testing.T) {
	f := newShopFixture(t, 1)
	f.open(1, "Grbpwr", "", time.Now().Add(-time.Hour))
	f.open(5, "Acme", "", time.Now().Add(-time.Hour))
	f.idErr[7] = errors.New("platform down")
	rep := fakeRepo{mail: f.repo}
	send := func(ctx context.Context) error {
		return f.s.QueueAccountLogin(ctx, rep, "c@example.com", "111111", "https://x.test/v")
	}
	require.ErrorIs(t, send(context.Background()), ErrShopMailNoTeam)
	require.ErrorIs(t, send(teamCtx(1)), ErrShopMailNoTeam, "the storefront team's letters are not the shop lane's")
	require.ErrorIs(t, send(teamCtx(6)), ErrShopMailClosed)
	require.ErrorContains(t, send(teamCtx(7)), "platform down")
	require.Empty(t, f.repo.rows, "nothing queued")
	require.NoError(t, send(teamCtx(5)))
	require.Len(t, f.repo.rows, 1)
}

func TestShopQueueSetsKindAndExpiry(t *testing.T) {
	f := newShopFixture(t, 0)
	f.open(5, "Acme", "", time.Now().Add(-time.Hour))
	rep := fakeRepo{mail: f.repo}
	before := time.Now().UTC()
	require.NoError(t, f.s.QueueAccountLogin(teamCtx(5), rep, "c@example.com", "111111", "https://x.test/v"))
	require.NoError(t, f.s.SendOrderShipped(teamCtx(5), rep, "c@example.com", sampleData(t, OrderShipped).(*dto.OrderShipment)))

	signin, order := f.repo.row(1), f.repo.row(2)
	require.Equal(t, entity.MailKindShopSignIn, signin.Kind.String)
	require.True(t, signin.ExpiresAt.Valid)
	require.WithinDuration(t, before.Add(10*time.Minute), signin.ExpiresAt.Time, 5*time.Second)
	require.Equal(t, entity.MailKindShopOrder, order.Kind.String)
	require.False(t, order.ExpiresAt.Valid)
	require.ErrorContains(t, f.s.SendOrderShipped(teamCtx(5), rep, "c@example.com", nil), "incomplete")
}

// TestShopDrainCutover: under the shop lane only shop letters queued after the shop opened and not
// expired go out; everything else is quarantined, never sent.
func TestShopDrainCutover(t *testing.T) {
	f := newShopFixture(t, 0)
	opened := time.Now().UTC().Add(-time.Hour).Truncate(time.Second)
	f.open(5, "Acme", "acme.lekalo.test", opened)
	from := shopSender("Acme", testPlatformAddr)
	kind := func(k string) sql.NullString { return sql.NullString{String: k, Valid: true} }
	future := sql.NullTime{Time: time.Now().UTC().Add(5 * time.Minute), Valid: true}
	after := opened.Add(time.Minute)

	legacy := f.repo.put(entity.SendEmailRequest{From: "GRBPWR <info@grbpwr.test>", To: "c@example.com", Subject: "promo", Html: "x", CreatedAt: after})
	marketing := f.repo.put(entity.SendEmailRequest{From: from, To: "c@example.com", Subject: "promo", Html: "x", CreatedAt: after, Kind: kind("campaign")})
	expired := f.repo.put(entity.SendEmailRequest{From: from, To: "c@example.com", Subject: "code", Html: "x", CreatedAt: after,
		Kind: kind(entity.MailKindShopSignIn), ExpiresAt: sql.NullTime{Time: time.Now().UTC().Add(-time.Second), Valid: true}})
	beforeOpen := f.repo.put(entity.SendEmailRequest{From: from, To: "c@example.com", Subject: "order", Html: "x",
		CreatedAt: opened.Add(-time.Second), Kind: kind(entity.MailKindShopOrder)})
	noExpiry := f.repo.put(entity.SendEmailRequest{From: from, To: "c@example.com", Subject: "code", Html: "x", CreatedAt: after,
		Kind: kind(entity.MailKindShopSignIn)})
	foreignSender := f.repo.put(entity.SendEmailRequest{From: "GRBPWR <info@grbpwr.test>", To: "c@example.com", Subject: "order", Html: "x",
		CreatedAt: after, Kind: kind(entity.MailKindShopOrder)})
	reservedSender := f.repo.put(entity.SendEmailRequest{From: shopSender("GRBPWR", testPlatformAddr), To: "c@example.com", Subject: "order", Html: "x",
		CreatedAt: after, Kind: kind(entity.MailKindShopOrder)})
	badRecipient := f.repo.put(entity.SendEmailRequest{From: from, To: "not an address", Subject: "order", Html: "x",
		CreatedAt: after, Kind: kind(entity.MailKindShopOrder)})
	order := f.repo.put(entity.SendEmailRequest{From: from, To: "c@example.com", Subject: "order", Html: "<p>x</p>", CreatedAt: after, Kind: kind(entity.MailKindShopOrder)})
	code := f.repo.put(entity.SendEmailRequest{From: from, To: "d@example.com", Subject: "code", Html: "<p>y</p>", CreatedAt: after,
		Kind: kind(entity.MailKindShopSignIn), ExpiresAt: future})

	require.NoError(t, f.s.drainTeam(teamCtx(5), 5))

	sent := f.cli.sent()
	require.Len(t, sent, 2)
	require.True(t, f.repo.row(order).Sent)
	require.True(t, f.repo.row(code).Sent)
	for _, id := range []int{legacy, marketing, expired, beforeOpen, noExpiry, foreignSender, reservedSender, badRecipient} {
		r := f.repo.row(id)
		require.False(t, r.Sent, "row %d sent", id)
		require.NotEmpty(t, r.quarantined, "row %d not quarantined", id)
	}
	require.Len(t, f.repo.cutovers, 1)
	require.Equal(t, opened, f.repo.cutovers[0].OpenedAt)
	require.ElementsMatch(t, shopKinds(), f.repo.cutovers[0].Kinds)
	require.Equal(t, shopClaimBatch, f.repo.claims[0].Limit)
	require.ElementsMatch(t, shopKinds(), f.repo.claims[0].Kinds)
	require.Equal(t, opened, f.repo.claims[0].NotBefore)
}

func TestShopDrainLeavesClosedShopAlone(t *testing.T) {
	f := newShopFixture(t, 0)
	f.repo.put(entity.SendEmailRequest{From: "x", To: "c@example.com", Subject: "s", Html: "h"})
	require.NoError(t, f.s.drainTeam(teamCtx(5), 5))
	require.Empty(t, f.repo.claims)
	require.Empty(t, f.repo.cutovers)
	require.Empty(t, f.repo.row(1).quarantined)
}

func TestShopTeamTickSkipsTheStorefrontTeam(t *testing.T) {
	f := newShopFixture(t, 1)
	require.NoError(t, f.s.teamTick(teamCtx(1), time.Now()))
	require.NoError(t, f.s.teamTick(context.Background(), time.Now()))
	require.Zero(t, f.resolveCount(1))
}

// TestShopTeamBackoffIsPerTeam: a failing team is held on its own; the other team keeps draining,
// the tick reports the failure, and a success clears the hold.
func TestShopTeamBackoffIsPerTeam(t *testing.T) {
	f := newShopFixture(t, 0)
	f.open(6, "Good", "", time.Now().Add(-time.Hour))
	f.idErr[5] = errors.New("platform down")
	f.s.loop = func(ctx context.Context, tick func(context.Context) bool) bool {
		tick(tenant.With(ctx, tenant.Team{ID: 5}))
		tick(tenant.With(ctx, tenant.Team{ID: 6}))
		return true
	}
	require.False(t, f.s.runOnce(context.Background()), "a failing team marks the tick")
	require.Equal(t, 1, f.resolveCount(5))
	require.Equal(t, 1, f.resolveCount(6))
	require.True(t, f.s.held(5, time.Now()))
	require.False(t, f.s.held(6, time.Now()))

	f.s.runOnce(context.Background())
	require.Equal(t, 1, f.resolveCount(5), "a held team is not visited")
	require.Equal(t, 2, f.resolveCount(6), "the other team goes on")

	f.s.settle(5, nil, time.Now())
	require.False(t, f.s.held(5, time.Now()))
	f.s.settle(5, errors.New("x"), time.Now())
	first := f.s.holds[5].until
	f.s.settle(5, errors.New("x"), time.Now())
	require.True(t, f.s.holds[5].until.After(first), "the hold grows with each failure")
}

func TestShopRunOnceHealthy(t *testing.T) {
	f := newShopFixture(t, 0)
	f.open(6, "Good", "", time.Now().Add(-time.Hour))
	f.s.loop = func(ctx context.Context, tick func(context.Context) bool) bool {
		tick(tenant.With(ctx, tenant.Team{ID: 6}))
		return true
	}
	require.True(t, f.s.runOnce(context.Background()))
	require.False(t, f.s.LastSuccess().IsZero())
}

func TestNewShopConfig(t *testing.T) {
	loop := func(context.Context, func(context.Context) bool) bool { return true }
	resolve := func(context.Context, int64) (ShopIdentity, error) { return ShopIdentity{}, nil }
	repo := newFakeOutbox()
	_, err := NewShop(ShopConfig{From: "a@b.test"}, repo, resolve, loop, nil)
	require.ErrorContains(t, err, "no Resend key")
	_, err = NewShop(ShopConfig{APIKey: "k", From: "not an address"}, repo, resolve, loop, nil)
	require.ErrorContains(t, err, "PLATFORM_MAIL_FROM")
	_, err = NewShop(ShopConfig{APIKey: "k", From: "a@b.test"}, repo, nil, loop, nil)
	require.ErrorContains(t, err, "wiring")

	s, err := NewShop(ShopConfig{APIKey: "k", From: "lekalo <a@b.test>"}, repo, resolve, loop, nil)
	require.NoError(t, err)
	require.Equal(t, defaultShopWorkerInterval, s.c.WorkerInterval)
	require.Equal(t, defaultShopSignInTTL, s.c.SignInTTL)
	require.Positive(t, s.c.MaxSendAttempts)
	require.Equal(t, "a@b.test", s.addr)
	require.NoError(t, s.Start(context.Background()))
	require.Error(t, s.Start(context.Background()))
	require.NoError(t, s.Stop())
}

// TestShopLaneSharesTheStorefrontBudgetSecond: over the storefront's own Resend key the shop lane
// takes a token only above the transactional reserve, so storefront letters keep priority.
func TestShopLaneSharesTheStorefrontBudgetSecond(t *testing.T) {
	sf, err := new(&Config{APIKey: "same", FromEmail: "info@shop.test", FromName: "GRBPWR",
		ResendRequestsPerSecond: 0.0001, ResendBurst: 2, TransactionalReserveTokens: 1}, newFakeOutbox(), nil)
	require.NoError(t, err)
	loop := func(context.Context, func(context.Context) bool) bool { return true }
	resolve := func(context.Context, int64) (ShopIdentity, error) { return ShopIdentity{}, nil }
	s, err := NewShop(ShopConfig{APIKey: "same", From: "a@b.test"}, newFakeOutbox(), resolve, loop, sf)
	require.NoError(t, err)

	require.NoError(t, s.acquire(context.Background()), "a token above the reserve")
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Millisecond)
	defer cancel()
	require.ErrorIs(t, s.acquire(ctx), context.DeadlineExceeded, "the reserve is the storefront's")
	quick, cancel2 := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel2()
	require.NoError(t, sf.budget.WaitTransactional(quick), "the storefront still gets its reserved token")

	other, err := NewShop(ShopConfig{APIKey: "platform", From: "a@b.test"}, newFakeOutbox(), resolve, loop, sf)
	require.NoError(t, err)
	require.NoError(t, other.acquire(context.Background()), "another key has its own budget")
}

func TestResendBudgetTrySharedYields(t *testing.T) {
	b := NewResendBudget(0.0001, 3, 1)
	require.True(t, b.TryShared())
	require.True(t, b.TryShared())
	require.False(t, b.TryShared(), "the reserve token is not shared")

	b = NewResendBudget(0.0001, 3, 1)
	b.CooldownGlobal(time.Minute)
	require.False(t, b.TryShared(), "a global cooldown stops the shared lane")

	b = NewResendBudget(0.0001, 3, 1)
	b.mu.Lock()
	b.transactionalWaiters = 1
	b.mu.Unlock()
	require.False(t, b.TryShared(), "a waiting storefront letter goes first")
}

func TestShopSenderOK(t *testing.T) {
	f := newShopFixture(t, 0)
	require.True(t, f.s.shopSenderOK(shopSender("Acme", testPlatformAddr)))
	require.False(t, f.s.shopSenderOK(shopSender("Acme", "other@lekalo.test")))
	require.False(t, f.s.shopSenderOK(`"Acme" <`+testPlatformAddr+`>`))
	require.False(t, f.s.shopSenderOK(shopSender("lekalo", testPlatformAddr)))
	require.False(t, f.s.shopSenderOK(shopSender("Grbpwr!", testPlatformAddr)))
	require.False(t, f.s.shopSenderOK("garbage"))
}
