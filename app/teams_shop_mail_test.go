package app

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/jekabolt/grbpwr-manager/config"
	mocks "github.com/jekabolt/grbpwr-manager/internal/dependency/mocks"
	"github.com/jekabolt/grbpwr-manager/internal/mail"
	"github.com/jekabolt/grbpwr-manager/internal/platformstore"
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
