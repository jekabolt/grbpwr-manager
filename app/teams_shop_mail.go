package app

import (
	"context"
	"log/slog"
	netmail "net/mail"
	"strings"

	"github.com/jekabolt/grbpwr-manager/internal/dependency"
	"github.com/jekabolt/grbpwr-manager/internal/dto"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/jekabolt/grbpwr-manager/internal/health"
	"github.com/jekabolt/grbpwr-manager/internal/mail"
	"github.com/jekabolt/grbpwr-manager/internal/platformstore"
	"github.com/jekabolt/grbpwr-manager/internal/tenant"
	"github.com/jekabolt/grbpwr-manager/openapi/gen/resend"
)

// Team-shop mail (X18). Every team's letters sit in its own database's outbox, so the worker drains
// each team in that team's context; a team shop's transactional letter (sign-in code, order letters)
// goes out under the shop's name (team_shop.mail_from_name, else the team's name) and reply-to over
// the platform sender address. Without a storefront team only those letters run: the storefront
// team's marketing and campaign letters stay off (teamShopMailer).

// teamMailer is what the tenant mailer offers for teams (internal/mail.Mailer).
type teamMailer interface {
	SetTeamLoop(func(context.Context, func(context.Context) bool) bool)
	SetSender(func(ctx context.Context) (name, replyTo string))
}

// startMailer builds and starts the tenant mailer into a.ma. With a storefront team a bad MAILER_*
// stops boot as before; without one it leaves team-shop letters off and lists the fault in /statusz.
func (a *App) startMailer(ctx context.Context) error {
	noShop := a.c.Platform.NoShop()
	cfg := &a.c.Mailer
	if noShop {
		c := teamShopMailConfig(a.c.Mailer, a.c.Platform)
		cfg = &c
	}
	m, err := mail.New(cfg, a.db.Mail(), a.db.StorefrontAccount())
	if err != nil {
		if noShop {
			a.noteProblem(ctx, "mail: team shop letters are off: "+err.Error())
			a.ma = noShopMailer{}
			return nil
		}
		slog.Default().ErrorContext(ctx, "couldn't connect to mailer", slog.String("err", err.Error()))
		return err
	}
	if tm, ok := m.(teamMailer); ok {
		tm.SetTeamLoop(a.workerTeamLoop("mail", activeTeams))
		tm.SetSender(shopMailSender(a.mailStorefrontID(), a.platform))
	}
	if err := m.Start(ctx); err != nil {
		slog.Default().ErrorContext(ctx, "couldn't start mailer worker", slog.String("err", err.Error()))
		return err
	}
	a.ma = m
	if noShop {
		r, _ := m.(health.Reporter)
		a.ma = teamShopMailer{Mailer: m, Reporter: r}
	}
	return nil
}

// mailStorefrontID is the team whose letters keep the configured sender (0 = none).
func (a *App) mailStorefrontID() int64 {
	sf := a.c.Platform.StorefrontTeamID
	if sf <= 0 && !a.c.Platform.NoShop() {
		sf = tenant.TeamOneID
	}
	return sf
}

// teamShopMailConfig is MAILER_* with the platform's Resend key and sender filling what is unset, so
// an installation without a storefront team needs no MAILER_* of its own.
func teamShopMailConfig(c mail.Config, p platformstore.Config) mail.Config {
	if strings.TrimSpace(c.APIKey) == "" {
		c.APIKey = strings.TrimSpace(p.ResendAPIKey)
	}
	if a, err := netmail.ParseAddress(strings.TrimSpace(p.MailFrom)); err == nil {
		if c.FromEmail == "" {
			c.FromEmail = a.Address
		}
		if c.FromName == "" {
			c.FromName = a.Name
		}
	}
	if c.FromName == "" {
		// Only the config's validity needs it: every team shop letter carries the shop's name.
		c.FromName = "shop"
	}
	return c
}

type teamShopStore interface {
	TeamShop(ctx context.Context, teamID int64) (platformstore.TeamShop, error)
}

// shopMailSender names a team shop's letters: team_shop.mail_from_name (else the team's name) and
// team_shop.mail_reply_to. The storefront team and a context without a team keep MAILER_*.
func shopMailSender(storefrontID int64, st *platformstore.Store) func(context.Context) (string, string) {
	var shops teamShopStore
	if st != nil {
		shops = st
	}
	return shopMailSenderFrom(storefrontID, shops)
}

func shopMailSenderFrom(storefrontID int64, shops teamShopStore) func(context.Context) (string, string) {
	return func(ctx context.Context) (string, string) {
		t, ok := tenant.From(ctx)
		if !ok || t.ID <= 0 || t.ID == storefrontID {
			return "", ""
		}
		name, reply := t.Name, ""
		if shops != nil {
			ts, err := shops.TeamShop(ctx, t.ID)
			if err != nil {
				slog.Default().WarnContext(ctx, "shop mail: sender", slog.String("err", err.Error()))
			} else {
				if n := strings.TrimSpace(ts.MailFromName); n != "" {
					name = n
				}
				reply = ts.MailReplyTo
			}
		}
		return name, reply
	}
}

// teamShopMailer is the tenant mailer of an installation without a storefront team: team shops'
// transactional letters (sign-in codes; order confirmation, cancellation, shipping, delivery, refund
// and return letters) go out; marketing, loyalty and campaign letters are refused as on noShopMailer.
type teamShopMailer struct {
	dependency.Mailer
	health.Reporter
}

var _ dependency.Mailer = teamShopMailer{}

func (teamShopMailer) SendCampaignTest(context.Context, dependency.Repository, string, string, string, string) error {
	return errNoShop
}
func (teamShopMailer) CampaignDispatchConfigured() error { return errNoShop }
func (teamShopMailer) CampaignSendingDisabled() bool     { return true }
func (teamShopMailer) CampaignEnvelope(*entity.EmailCampaignFull) (string, *string, error) {
	return "", nil, errNoShop
}
func (teamShopMailer) CampaignUnsubscribeURL(entity.EmailCampaignTopic, string) (string, error) {
	return "", errNoShop
}
func (teamShopMailer) CampaignFooterStrings(int, []entity.Language) entity.EmailFooterStrings {
	return entity.EmailFooterStrings{}
}
func (teamShopMailer) BuildCampaignSendRequest(string, entity.EmailCampaignRenderSnapshot, string, string, string) (resend.SendEmailRequest, error) {
	return resend.SendEmailRequest{}, errNoShop
}
func (teamShopMailer) SendCampaignBatch(context.Context, []resend.SendEmailRequest, string, func() error) ([]string, error) {
	return nil, errNoShop
}
func (teamShopMailer) SendNewSubscriber(context.Context, dependency.Repository, string) error {
	return errNoShop
}
func (teamShopMailer) QueueNewSubscriber(context.Context, dependency.Repository, string) error {
	return errNoShop
}
func (teamShopMailer) SendPromoCode(context.Context, dependency.Repository, string, *dto.PromoCodeDetails) error {
	return errNoShop
}
func (teamShopMailer) SendBackInStock(context.Context, dependency.Repository, string, *dto.BackInStock) error {
	return errNoShop
}
func (teamShopMailer) QueueTierUpgrade(context.Context, dependency.Repository, string, *dto.TierChangeEmail) error {
	return errNoShop
}
func (teamShopMailer) QueueTierDowngrade(context.Context, dependency.Repository, string, *dto.TierChangeEmail) error {
	return errNoShop
}
func (teamShopMailer) QueueDowngradeReminder(context.Context, dependency.Repository, string, *dto.TierChangeEmail) error {
	return errNoShop
}
func (teamShopMailer) QueueTierRollback(context.Context, dependency.Repository, string, *dto.TierChangeEmail) error {
	return errNoShop
}
func (teamShopMailer) QueueFirstPurchaseThanks(context.Context, dependency.Repository, string, *dto.TierChangeEmail) error {
	return errNoShop
}
func (teamShopMailer) QueueUnsubscribeConfirmation(context.Context, dependency.Repository, string, *dto.UnsubscribeConfirmationEmail) error {
	return errNoShop
}
func (teamShopMailer) QueueBirthdayGift(context.Context, dependency.Repository, string, *dto.BirthdayEmail) error {
	return errNoShop
}
func (teamShopMailer) QueueEventInvite(context.Context, dependency.Repository, string, *dto.MemberCustomEmail) error {
	return errNoShop
}
func (teamShopMailer) QueueHackerInvite(context.Context, dependency.Repository, string, *dto.HackerInviteEmail) error {
	return errNoShop
}
