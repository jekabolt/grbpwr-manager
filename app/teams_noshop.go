package app

import (
	"context"
	"errors"
	"log/slog"

	bq "github.com/jekabolt/grbpwr-manager/internal/analytics/bigquery"
	"github.com/jekabolt/grbpwr-manager/internal/dependency"
	"github.com/jekabolt/grbpwr-manager/internal/dto"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/jekabolt/grbpwr-manager/openapi/gen/resend"
)

// bootComponent is one background component App.Start may start. shop marks the ones that touch
// orders, customers, payments, shipments, marketing or the storefront: an installation without a
// shop (config.Platform.NoShop, TENANT_STOREFRONT_TEAM_ID=none) does not start them.
type bootComponent struct {
	name string
	shop bool
}

// bootComponents is every component Start passes through shopGate, in start order. A test reads
// app.go and checks that each shopGate call names a row here and each row is gated exactly once,
// so a new worker cannot slip past the switch.
func bootComponents() []bootComponent {
	return []bootComponent{
		{name: "marketingaggregate", shop: true},
		{name: "mail", shop: true}, // tenant mail outbox: order, customer and campaign letters
		{name: "campaigndispatch", shop: true},
		{name: "stockreserve", shop: true},
		{name: "storefrontcleanup", shop: true},
		{name: "tiermanagement", shop: true},
		{name: "taskarchive"},
		{name: "opexmaterialize", shop: true},
		{name: "acctposting", shop: true},
		{name: "fxsync"},
		{name: "aireconcile"},
		{name: "archivecleanup"},
		{name: "stripe", shop: true}, // both processors and their in-process payment monitors
		{name: "stripereconcile", shop: true},
		{name: "ordercleanup", shop: true},
		{name: "deliverysync", shop: true},
		{name: "revalidation", shop: true},
		{name: "ga4", shop: true},
		{name: "bigquery", shop: true},
		{name: "ga4sync", shop: true},
		{name: "ga4mp", shop: true},
		{name: "storefront", shop: true}, // FrontendService (gRPC + gateway)
		{name: "webhooks", shop: true},   // Resend, Stripe and AfterShip webhooks
	}
}

// shopPlan splits bootComponents into what Start starts (subject to each component's own config
// switch) and what it skips for the no-shop switch.
func shopPlan(noShop bool) (start, skip []string) {
	for _, c := range bootComponents() {
		if c.shop && noShop {
			skip = append(skip, c.name)
		} else {
			start = append(start, c.name)
		}
	}
	return start, skip
}

// shopGate reports whether Start may start the named component. On a no-shop installation a shop
// component is skipped with one INFO line `no_shop: skipped <name>`; its own config switch is not
// consulted, so the line prints even where the switch was already off.
func (a *App) shopGate(ctx context.Context, name string) bool {
	shop := false
	for _, c := range bootComponents() {
		if c.name == name {
			shop = c.shop
			break
		}
	}
	if shop && a.c.Platform.NoShop() {
		slog.Default().InfoContext(ctx, "no_shop: skipped "+name)
		a.noShopSkipped = append(a.noShopSkipped, name)
		return false
	}
	return true
}

// errNoShop answers every letter the no-shop mailer is asked to send or queue.
var errNoShop = errors.New("no shop on this installation: shop letters are not sent")

// noShopMailer stands in for the tenant mailer on a no-shop installation: the outbox is never
// written and no worker drains it. Admin sign-in letters are platform mail (platformmail), which
// this does not touch.
type noShopMailer struct{}

var _ dependency.Mailer = noShopMailer{}

func (noShopMailer) SendCampaignTest(context.Context, dependency.Repository, string, string, string, string) error {
	return errNoShop
}
func (noShopMailer) CampaignDispatchConfigured() error { return errNoShop }
func (noShopMailer) CampaignSendingDisabled() bool     { return true }
func (noShopMailer) CampaignEnvelope(*entity.EmailCampaignFull) (string, *string, error) {
	return "", nil, errNoShop
}
func (noShopMailer) CampaignUnsubscribeURL(entity.EmailCampaignTopic, string) (string, error) {
	return "", errNoShop
}
func (noShopMailer) CampaignFooterStrings(int, []entity.Language) entity.EmailFooterStrings {
	return entity.EmailFooterStrings{}
}
func (noShopMailer) BuildCampaignSendRequest(string, entity.EmailCampaignRenderSnapshot, string, string, string) (resend.SendEmailRequest, error) {
	return resend.SendEmailRequest{}, errNoShop
}
func (noShopMailer) SendCampaignBatch(context.Context, []resend.SendEmailRequest, string, func() error) ([]string, error) {
	return nil, errNoShop
}
func (noShopMailer) SendNewSubscriber(context.Context, dependency.Repository, string) error {
	return errNoShop
}
func (noShopMailer) QueueNewSubscriber(context.Context, dependency.Repository, string) error {
	return errNoShop
}
func (noShopMailer) SendOrderConfirmation(context.Context, dependency.Repository, string, *dto.OrderConfirmed) error {
	return errNoShop
}
func (noShopMailer) QueueOrderConfirmation(context.Context, dependency.Repository, string, *dto.OrderConfirmed) error {
	return errNoShop
}
func (noShopMailer) SendOrderCancellation(context.Context, dependency.Repository, string, *dto.OrderCancelled) error {
	return errNoShop
}
func (noShopMailer) SendOrderShipped(context.Context, dependency.Repository, string, *dto.OrderShipment) error {
	return errNoShop
}
func (noShopMailer) SendOrderDelivered(context.Context, dependency.Repository, string, *dto.OrderDelivered) error {
	return errNoShop
}
func (noShopMailer) SendRefundInitiated(context.Context, dependency.Repository, string, *dto.OrderRefundInitiated) error {
	return errNoShop
}
func (noShopMailer) SendPendingReturn(context.Context, dependency.Repository, string, *dto.OrderPendingReturn) error {
	return errNoShop
}
func (noShopMailer) SendPromoCode(context.Context, dependency.Repository, string, *dto.PromoCodeDetails) error {
	return errNoShop
}
func (noShopMailer) SendBackInStock(context.Context, dependency.Repository, string, *dto.BackInStock) error {
	return errNoShop
}
func (noShopMailer) QueueAccountLogin(context.Context, dependency.Repository, string, string, string) error {
	return errNoShop
}
func (noShopMailer) QueueTierUpgrade(context.Context, dependency.Repository, string, *dto.TierChangeEmail) error {
	return errNoShop
}
func (noShopMailer) QueueTierDowngrade(context.Context, dependency.Repository, string, *dto.TierChangeEmail) error {
	return errNoShop
}
func (noShopMailer) QueueDowngradeReminder(context.Context, dependency.Repository, string, *dto.TierChangeEmail) error {
	return errNoShop
}
func (noShopMailer) QueueTierRollback(context.Context, dependency.Repository, string, *dto.TierChangeEmail) error {
	return errNoShop
}
func (noShopMailer) QueueFirstPurchaseThanks(context.Context, dependency.Repository, string, *dto.TierChangeEmail) error {
	return errNoShop
}
func (noShopMailer) QueueUnsubscribeConfirmation(context.Context, dependency.Repository, string, *dto.UnsubscribeConfirmationEmail) error {
	return errNoShop
}
func (noShopMailer) QueueBirthdayGift(context.Context, dependency.Repository, string, *dto.BirthdayEmail) error {
	return errNoShop
}
func (noShopMailer) QueueEventInvite(context.Context, dependency.Repository, string, *dto.MemberCustomEmail) error {
	return errNoShop
}
func (noShopMailer) QueueHackerInvite(context.Context, dependency.Repository, string, *dto.HackerInviteEmail) error {
	return errNoShop
}
func (noShopMailer) Start(context.Context) error { return nil }
func (noShopMailer) Stop() error                 { return nil }

// bigQueryClient stores bq.NewClient's answer in the dependency.BQClient interface. NewClient answers
// (nil, nil) when BigQuery is not configured; that nil pointer must become a nil interface, or every
// `a.bqc != nil` check passes and /statusz, Stop and ga4sync call methods on a nil client.
func bigQueryClient(c *bq.Client) dependency.BQClient {
	if c == nil {
		return nil
	}
	return c
}
