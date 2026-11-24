package app

import (
	"context"
	"errors"
	"testing"

	"github.com/jekabolt/grbpwr-manager/internal/dependency"
	"github.com/jekabolt/grbpwr-manager/internal/dto"
	"github.com/jekabolt/grbpwr-manager/internal/mail"
	"github.com/jekabolt/grbpwr-manager/internal/platformstore"
	"github.com/jekabolt/grbpwr-manager/internal/tenant"
)

type fakeTeamShops map[int64]platformstore.TeamShop

func (f fakeTeamShops) TeamShop(_ context.Context, id int64) (platformstore.TeamShop, error) {
	return f[id], nil
}

// A team shop's letters carry its mail name (else its team name) and reply-to; the storefront team
// and a context without a team keep MAILER_*.
func TestShopMailSender(t *testing.T) {
	send := shopMailSenderFrom(1, fakeTeamShops{7: {MailFromName: " Acme ", MailReplyTo: "help@acme.test"}})
	at := func(team tenant.Team) (string, string) {
		return send(tenant.With(context.Background(), team))
	}
	if n, r := at(tenant.Team{ID: 7, Name: "acme-team"}); n != "Acme" || r != "help@acme.test" {
		t.Fatalf("shop with a mail name: %q %q", n, r)
	}
	if n, r := at(tenant.Team{ID: 9, Name: "bolt"}); n != "bolt" || r != "" {
		t.Fatalf("shop without a mail name: %q %q", n, r)
	}
	if n, _ := at(tenant.Team{ID: 1, Name: "grbpwr"}); n != "" {
		t.Fatalf("storefront team got %q", n)
	}
	if n, _ := send(context.Background()); n != "" {
		t.Fatalf("no team got %q", n)
	}
	// No storefront team: every team is a team shop.
	if n, _ := shopMailSenderFrom(0, nil)(tenant.With(context.Background(), tenant.Team{ID: 1, Name: "grbpwr"})); n != "grbpwr" {
		t.Fatalf("team 1 without a storefront team: %q", n)
	}
}

// Without a storefront team the mailer runs on the platform's Resend key and sender when MAILER_* is
// unset, and MAILER_* wins where it is set.
func TestTeamShopMailConfig(t *testing.T) {
	c := teamShopMailConfig(mail.Config{}, platformstore.Config{ResendAPIKey: "re_x", MailFrom: "lekalo <no-reply@lekalo.studio>"})
	if c.APIKey != "re_x" || c.FromEmail != "no-reply@lekalo.studio" || c.FromName != "lekalo" {
		t.Fatalf("platform fallback: %+v", c)
	}
	c = teamShopMailConfig(mail.Config{APIKey: "re_m", FromEmail: "m@x.test"}, platformstore.Config{ResendAPIKey: "re_x", MailFrom: "no-reply@lekalo.studio"})
	if c.APIKey != "re_m" || c.FromEmail != "m@x.test" || c.FromName != "shop" {
		t.Fatalf("MAILER_* first: %+v", c)
	}
}

type recordingMailer struct {
	dependency.Mailer
	logins, orders int
}

func (r *recordingMailer) QueueAccountLogin(context.Context, dependency.Repository, string, string, string) error {
	r.logins++
	return nil
}
func (r *recordingMailer) QueueOrderConfirmation(context.Context, dependency.Repository, string, *dto.OrderConfirmed) error {
	r.orders++
	return nil
}

// Without a storefront team, team shops' sign-in and order letters go through; campaign and
// marketing letters stay refused.
func TestTeamShopMailerSplitsTransactional(t *testing.T) {
	rec := &recordingMailer{}
	m := teamShopMailer{Mailer: rec}
	ctx := context.Background()
	if err := m.QueueAccountLogin(ctx, nil, "c@x.test", "123456", "https://acme.test/login"); err != nil || rec.logins != 1 {
		t.Fatalf("sign-in letter: %v %d", err, rec.logins)
	}
	if err := m.QueueOrderConfirmation(ctx, nil, "c@x.test", nil); err != nil || rec.orders != 1 {
		t.Fatalf("order letter: %v %d", err, rec.orders)
	}
	if err := m.CampaignDispatchConfigured(); !errors.Is(err, errNoShop) || !m.CampaignSendingDisabled() {
		t.Fatalf("campaigns must stay off: %v", err)
	}
	if err := m.SendCampaignTest(ctx, nil, "", "", "", ""); !errors.Is(err, errNoShop) {
		t.Fatalf("campaign test send: %v", err)
	}
	if err := m.QueueNewSubscriber(ctx, nil, "c@x.test"); !errors.Is(err, errNoShop) {
		t.Fatalf("newsletter letter: %v", err)
	}
}
