package app

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/jekabolt/grbpwr-manager/config"
	"github.com/jekabolt/grbpwr-manager/internal/dependency"
	mocks "github.com/jekabolt/grbpwr-manager/internal/dependency/mocks"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/jekabolt/grbpwr-manager/internal/mail"
	"github.com/jekabolt/grbpwr-manager/internal/platformmail"
	"github.com/jekabolt/grbpwr-manager/internal/platformstore"
	"github.com/jekabolt/grbpwr-manager/internal/tenant"
)

type fakeShopPlatform struct {
	shops     map[int64]platformstore.TeamShop
	hosts     map[int64][]platformstore.ShopHost
	names     []platformstore.TeamMailName
	err       error
	shopCalls int
	nameCalls int
}

func (f *fakeShopPlatform) TeamShop(_ context.Context, id int64) (platformstore.TeamShop, error) {
	f.shopCalls++
	if f.err != nil {
		return platformstore.TeamShop{}, f.err
	}
	return f.shops[id], nil
}

func (f *fakeShopPlatform) ShopHosts(_ context.Context, id int64) ([]platformstore.ShopHost, error) {
	return f.hosts[id], nil
}

func (f *fakeShopPlatform) TeamMailNames(context.Context) ([]platformstore.TeamMailName, error) {
	f.nameCalls++
	return f.names, nil
}

func TestShopIdentitiesResolve(t *testing.T) {
	opened := time.Date(2026, 11, 20, 10, 0, 0, 0, time.FixedZone("x", 3600))
	p := &fakeShopPlatform{
		shops: map[int64]platformstore.TeamShop{
			2: {TeamID: 2, Enabled: true, EnabledAt: &opened},
			3: {TeamID: 3, Enabled: true, EnabledAt: &opened, ClosedByOperator: true},
			4: {TeamID: 4, Enabled: false},
			5: {TeamID: 5, Enabled: true, EnabledAt: &opened},
		},
		hosts: map[int64][]platformstore.ShopHost{
			2: {
				{Host: "acme.lekalo.test", Status: platformstore.ShopHostLive},
				{Host: "shop.acme.test", Primary: true, Status: "pending"},
			},
			5: {
				{Host: "old.lekalo.test", Status: platformstore.ShopHostLive, RedirectTo: "north.test"},
				{Host: "north.test", Primary: true, Status: platformstore.ShopHostLive},
			},
		},
		names: []platformstore.TeamMailName{
			{TeamID: 1, Name: "grbpwr"},
			{TeamID: 2, Name: "Acme", BrandName: "ACME STUDIO"},
			{TeamID: 5, Name: "North"},
		},
	}
	now := time.Date(2026, 11, 24, 12, 0, 0, 0, time.UTC)
	s := newShopIdentities(p)
	s.now = func() time.Time { return now }

	id, err := s.resolve(context.Background(), 2)
	if err != nil {
		t.Fatal(err)
	}
	if !id.Open || id.Name != "ACME STUDIO" || id.Host != "acme.lekalo.test" || !id.OpenedAt.Equal(opened) || id.OpenedAt.Location() != time.UTC {
		t.Fatalf("team 2: %+v", id)
	}
	if strings.Join(id.Taken, ",") != "grbpwr,North" {
		t.Fatalf("team 2 taken: %v", id.Taken)
	}
	id, _ = s.resolve(context.Background(), 5)
	if id.Name != "North" || id.Host != "north.test" {
		t.Fatalf("team 5: %+v", id)
	}
	for _, closed := range []int64{3, 4, 9} {
		if id, _ := s.resolve(context.Background(), closed); id.Open || !id.OpenedAt.IsZero() {
			t.Fatalf("team %d should be closed: %+v", closed, id)
		}
	}

	// Cached: open for 30s, closed for 5s, names for a minute.
	calls := p.shopCalls
	_, _ = s.resolve(context.Background(), 2)
	_, _ = s.resolve(context.Background(), 4)
	if p.shopCalls != calls {
		t.Fatalf("cache missed: %d calls", p.shopCalls-calls)
	}
	now = now.Add(6 * time.Second)
	_, _ = s.resolve(context.Background(), 2)
	_, _ = s.resolve(context.Background(), 4)
	if p.shopCalls != calls+1 {
		t.Fatalf("closed shop should be re-read after 5s, open one kept: %d calls", p.shopCalls-calls)
	}
	if p.nameCalls != 1 {
		t.Fatalf("names read %d times", p.nameCalls)
	}
	now = now.Add(time.Minute)
	_, _ = s.resolve(context.Background(), 2)
	if p.nameCalls != 2 {
		t.Fatalf("names should be re-read after a minute: %d", p.nameCalls)
	}

	// Errors are not cached.
	p.err = errors.New("platform down")
	if _, err := s.resolve(context.Background(), 7); err == nil {
		t.Fatal("want error")
	}
	p.err = nil
	if _, err := s.resolve(context.Background(), 7); err != nil {
		t.Fatal(err)
	}
}

func TestLoginChallengeTTL(t *testing.T) {
	for in, want := range map[string]time.Duration{"": defaultLoginTTL, "15m": 15 * time.Minute, "junk": defaultLoginTTL, "-1m": defaultLoginTTL, " 2m ": 2 * time.Minute} {
		if got := loginChallengeTTL(in); got != want {
			t.Fatalf("%q: %s, want %s", in, got, want)
		}
	}
}

// TestShopMailMissingConfigIsAProblemNotABootFailure: each missing piece leaves the lane off with
// one /statusz problem; the router still wraps the mailer so team shops are refused, never sent
// as the storefront.
func TestShopMailMissingConfigIsAProblemNotABootFailure(t *testing.T) {
	for name, tc := range map[string]struct {
		platform platformstore.Config
		mailer   mail.Config
		want     string
	}{
		"off":     {platformstore.Config{MailFrom: "lekalo <a@b.test>"}, mail.Config{APIKey: "k"}, "PLATFORM_MAILER_ENABLED"},
		"no key":  {platformstore.Config{MailerEnabled: true, MailFrom: "lekalo <a@b.test>"}, mail.Config{}, "no Resend key"},
		"no from": {platformstore.Config{MailerEnabled: true, ResendAPIKey: "k"}, mail.Config{}, "PLATFORM_MAIL_FROM"},
		"bad from": {platformstore.Config{MailerEnabled: true, ResendAPIKey: "k", MailFrom: "nonsense"},
			mail.Config{}, "PLATFORM_MAIL_FROM"},
	} {
		t.Run(name, func(t *testing.T) {
			a := &App{c: &config.Config{Platform: tc.platform, Mailer: tc.mailer}, platform: &platformstore.Store{}, db: shopTestRepo(t)}
			got := a.withShopMail(context.Background(), noShopMailer{})
			r, ok := got.(*mail.ShopRouter)
			if !ok {
				t.Fatalf("want the router, got %T", got)
			}
			if r.Storefront() != (noShopMailer{}) {
				t.Fatal("router lost the storefront mailer")
			}
			if a.shopMail != nil {
				t.Fatal("lane should be off")
			}
			if len(a.problems) != 1 || !strings.Contains(a.problems[0], tc.want) || !strings.HasPrefix(a.problems[0], "team-shop mail off: ") {
				t.Fatalf("problems: %v", a.problems)
			}
		})
	}
	a := &App{c: &config.Config{}}
	if got := a.withShopMail(context.Background(), noShopMailer{}); got != (noShopMailer{}) {
		t.Fatalf("teams mode off: the mailer stays as it is, got %T", got)
	}
}

func TestShopMailBuildsTheLaneOnTheMailerKey(t *testing.T) {
	a := &App{
		c: &config.Config{
			Platform: platformstore.Config{MailerEnabled: true, MailFrom: "lekalo <no-reply@lekalo.test>", BaseDomain: "lekalo.test"},
			Mailer:   mail.Config{APIKey: "k"},
		},
		platform: &platformstore.Store{},
		db:       shopTestRepo(t),
	}
	got := a.withShopMail(context.Background(), noShopMailer{})
	if _, ok := got.(*mail.ShopRouter); !ok {
		t.Fatalf("want the router, got %T", got)
	}
	if a.shopMail == nil || len(a.problems) != 0 {
		t.Fatalf("lane should be on without problems: %v", a.problems)
	}
}

func shopTestRepo(t *testing.T) *mocks.MockRepository {
	repo := mocks.NewMockRepository(t)
	repo.EXPECT().Mail().Return(mocks.NewMockMail(t)).Maybe()
	return repo
}

func TestShopResendKey(t *testing.T) {
	for _, c := range []struct{ shop, platform, mailer, want string }{
		{"s", "p", "m", "s"}, {" ", "p", "m", "p"}, {"", "", " m ", "m"}, {"", "", "", ""},
	} {
		if got := shopResendKey(c.shop, c.platform, c.mailer); got != c.want {
			t.Fatalf("%+v: %q", c, got)
		}
	}
}

// TestShopIdentitiesStateIsFresh: the worker's state read goes to the control plane every time (no
// cache), and a shop found closed drops its cached open identity so queueing stops at once.
func TestShopIdentitiesStateIsFresh(t *testing.T) {
	opened := time.Date(2026, 11, 20, 10, 0, 0, 0, time.UTC)
	p := &fakeShopPlatform{shops: map[int64]platformstore.TeamShop{2: {TeamID: 2, Enabled: true, EnabledAt: &opened}}}
	s := newShopIdentities(p)
	ctx := context.Background()
	if id, err := s.resolve(ctx, 2); err != nil || !id.Open {
		t.Fatalf("resolve %+v %v", id, err)
	}
	calls := p.shopCalls
	for range 2 {
		st, err := s.state(ctx, 2)
		if err != nil || !st.Open || !st.OpenedAt.Equal(opened) {
			t.Fatalf("state %+v %v", st, err)
		}
	}
	if p.shopCalls != calls+2 {
		t.Fatalf("state reads %d, want 2 fresh reads", p.shopCalls-calls)
	}
	p.shops[2] = platformstore.TeamShop{TeamID: 2, Enabled: true, EnabledAt: &opened, ClosedByOperator: true}
	if st, err := s.state(ctx, 2); err != nil || st.Open {
		t.Fatalf("closed shop state %+v %v", st, err)
	}
	if id, err := s.resolve(ctx, 2); err != nil || id.Open {
		t.Fatalf("the cached open identity outlived the close: %+v %v", id, err)
	}
	p.err = errors.New("platform down")
	if _, err := s.state(ctx, 2); err == nil {
		t.Fatal("state hid a read error")
	}
}

type fakeShopOwners struct {
	owners map[string][2]int64
	err    error
	calls  int
}

func (f *fakeShopOwners) ShopMailOwner(_ context.Context, pid string) (int64, int64, bool, error) {
	f.calls++
	if f.err != nil {
		return 0, 0, false, f.err
	}
	o, ok := f.owners[pid]
	return o[0], o[1], ok, nil
}

type fakeShopTeams struct {
	tenant.Directory
	teams map[int64]tenant.Team
}

func (d fakeShopTeams) ByID(_ context.Context, id int64) (tenant.Team, error) {
	t, ok := d.teams[id]
	if !ok {
		return tenant.Team{}, tenant.ErrNotFound
	}
	return t, nil
}

// fakeTeamMail is one tenant mail store per team, picked by the team in ctx.
type fakeTeamMail struct {
	dependency.Mail
	rows       map[int64]map[int][2]string // team -> row -> {provider id, recipient}
	suppressed map[int64][]string
}

func (f *fakeTeamMail) ProviderMailRecipient(ctx context.Context, id int, pid string) (string, error) {
	team, ok := tenant.From(ctx)
	if !ok {
		return "", tenant.ErrNoTenant
	}
	r := f.rows[team.ID][id]
	if r[0] != pid {
		return "", nil
	}
	return r[1], nil
}

func (f *fakeTeamMail) AddSuppression(ctx context.Context, email string, reason entity.SuppressionReason) error {
	team, ok := tenant.From(ctx)
	if !ok {
		return tenant.ErrNoTenant
	}
	f.suppressed[team.ID] = append(f.suppressed[team.ID], email+":"+string(reason))
	return nil
}

// TestShopMailBounces: a hard bounce or complaint of a shop letter lands on the sending team's own
// list, only for a letter the lane sent to that recipient, only for a live team; anything else
// changes nothing.
func TestShopMailBounces(t *testing.T) {
	owners := &fakeShopOwners{owners: map[string][2]int64{"re_a": {2, 7}, "re_b": {3, 1}, "re_gone": {9, 1}, "re_susp": {4, 1}, "re_other": {2, 8}}}
	store := &fakeTeamMail{
		rows: map[int64]map[int][2]string{
			2: {7: {"re_a", "Buyer@x.test"}, 8: {"re_elsewhere", "c@x.test"}},
			3: {1: {"re_b", "b@x.test"}},
			4: {1: {"re_susp", "s@x.test"}},
		},
		suppressed: map[int64][]string{},
	}
	dir := fakeShopTeams{teams: map[int64]tenant.Team{
		2: {ID: 2, Status: tenant.StatusActive},
		3: {ID: 3, Status: tenant.StatusActive},
		4: {ID: 4, Status: tenant.StatusSuspended},
	}}
	b := &shopMailBounces{owners: owners, teams: func() tenant.Directory { return dir }, mail: func() dependency.Mail { return store }}
	ctx := context.Background()
	ev := func(typ, id, to, bounce string) platformmail.Event {
		return platformmail.Event{Type: typ, EmailID: id, To: []string{to}, Bounce: bounce}
	}
	for _, e := range []platformmail.Event{
		ev("email.bounced", "re_a", "buyer@x.test", "Transient"), // soft: nothing, not even a lookup
		ev("email.delivered", "re_a", "buyer@x.test", ""),
	} {
		if err := b.apply(ctx, e); err != nil {
			t.Fatal(err)
		}
	}
	if owners.calls != 0 {
		t.Fatalf("soft events looked up %d owners", owners.calls)
	}
	for _, e := range []platformmail.Event{
		ev("email.complained", "re_unknown", "buyer@x.test", ""), // not a shop letter
		ev("email.complained", "re_gone", "buyer@x.test", ""),    // wiped team
		ev("email.complained", "re_susp", "s@x.test", ""),        // suspended team
		ev("email.complained", "re_a", "someone@x.test", ""),     // another recipient
		ev("email.complained", "re_other", "c@x.test", ""),       // the row was sent under another id
	} {
		if err := b.apply(ctx, e); err != nil {
			t.Fatalf("%+v: %v", e, err)
		}
	}
	if len(store.suppressed) != 0 {
		t.Fatalf("suppressed %v", store.suppressed)
	}
	if err := b.apply(ctx, ev("email.bounced", "re_a", "BUYER@x.test", "Permanent")); err != nil {
		t.Fatal(err)
	}
	if err := b.apply(ctx, ev("email.complained", "re_b", "b@x.test", "")); err != nil {
		t.Fatal(err)
	}
	if err := b.apply(ctx, ev("email.suppressed", "re_b", "b@x.test", "")); err != nil {
		t.Fatal(err)
	}
	want := map[int64][]string{2: {"Buyer@x.test:bounce"}, 3: {"b@x.test:complaint", "b@x.test:bounce"}}
	if fmt.Sprint(store.suppressed) != fmt.Sprint(want) {
		t.Fatalf("suppressed %v, want %v", store.suppressed, want)
	}

	owners.err = errors.New("platform down")
	if err := b.apply(ctx, ev("email.complained", "re_a", "buyer@x.test", "")); err == nil {
		t.Fatal("an owner read error must make Resend retry")
	}
	owners.err = nil
	b.teams = func() tenant.Directory { return nil }
	if err := b.apply(ctx, ev("email.complained", "re_a", "buyer@x.test", "")); err == nil {
		t.Fatal("no directory yet must make Resend retry")
	}
}
