package mail

import (
	"context"
	"errors"
	"log/slog"

	"github.com/jekabolt/grbpwr-manager/internal/dependency"
	"github.com/jekabolt/grbpwr-manager/internal/dto"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/jekabolt/grbpwr-manager/internal/tenant"
	resend "github.com/jekabolt/grbpwr-manager/openapi/gen/resend"
)

var (
	// ErrShopMailOff: a team shop's letter while the shop lane is not configured (see /statusz).
	ErrShopMailOff = errors.New("mail: team-shop letters are off")
	// ErrShopMailMarketing: team shops get transactional letters only.
	ErrShopMailMarketing = errors.New("mail: no marketing letters for a team shop")
)

// shopLetters is the shop lane as the router uses it (ShopMailer).
type shopLetters interface {
	QueueAccountLogin(ctx context.Context, rep dependency.Repository, to, otpCode, magicLinkURL string) error
	QueueOrderConfirmation(ctx context.Context, rep dependency.Repository, to string, d *dto.OrderConfirmed) error
	SendOrderCancellation(ctx context.Context, rep dependency.Repository, to string, d *dto.OrderCancelled) error
	SendOrderShipped(ctx context.Context, rep dependency.Repository, to string, d *dto.OrderShipment) error
	SendOrderDelivered(ctx context.Context, rep dependency.Repository, to string, d *dto.OrderDelivered) error
	SendRefundInitiated(ctx context.Context, rep dependency.Repository, to string, d *dto.OrderRefundInitiated) error
	SendPendingReturn(ctx context.Context, rep dependency.Repository, to string, d *dto.OrderPendingReturn) error
}

// ShopRouter picks a letter's lane by the team in its context. The storefront team, and a context
// with no team, keep the storefront mailer (under none that is the no-shop mailer, which refuses).
// Any other team is a team shop: its sign-in and order letters go to the shop lane, and everything
// else (marketing, tiers, campaigns) is refused, never sent as the storefront.
type ShopRouter struct {
	storefront       dependency.Mailer
	shop             shopLetters // nil: the lane is off
	storefrontTeamID int64
}

var _ dependency.Mailer = (*ShopRouter)(nil)

// NewShopRouter wraps the storefront mailer. shop nil leaves team shops without letters (refused
// with ErrShopMailOff); storefrontTeamID is 0 under none.
func NewShopRouter(storefront dependency.Mailer, shop *ShopMailer, storefrontTeamID int64) *ShopRouter {
	r := &ShopRouter{storefront: storefront, storefrontTeamID: storefrontTeamID}
	if shop != nil {
		r.shop = shop
	}
	return r
}

// Storefront is the wrapped storefront mailer (its health report).
func (r *ShopRouter) Storefront() dependency.Mailer { return r.storefront }

// shopTeam reports whether the context's team is a team shop rather than the storefront.
func (r *ShopRouter) shopTeam(ctx context.Context) bool {
	t, ok := tenant.From(ctx)
	return ok && t.ID > 0 && t.ID != r.storefrontTeamID
}

func (r *ShopRouter) lane() (shopLetters, error) {
	if r.shop == nil {
		return nil, ErrShopMailOff
	}
	return r.shop, nil
}

// Transactional letters: the shop lane for a team shop.

func (r *ShopRouter) QueueAccountLogin(ctx context.Context, rep dependency.Repository, to, otpCode, magicLinkURL string) error {
	if !r.shopTeam(ctx) {
		return r.storefront.QueueAccountLogin(ctx, rep, to, otpCode, magicLinkURL)
	}
	l, err := r.lane()
	if err != nil {
		return err
	}
	return l.QueueAccountLogin(ctx, rep, to, otpCode, magicLinkURL)
}

func (r *ShopRouter) SendOrderConfirmation(ctx context.Context, rep dependency.Repository, to string, d *dto.OrderConfirmed) error {
	if !r.shopTeam(ctx) {
		return r.storefront.SendOrderConfirmation(ctx, rep, to, d)
	}
	l, err := r.lane()
	if err != nil {
		return err
	}
	return l.QueueOrderConfirmation(ctx, rep, to, d)
}

func (r *ShopRouter) QueueOrderConfirmation(ctx context.Context, rep dependency.Repository, to string, d *dto.OrderConfirmed) error {
	if !r.shopTeam(ctx) {
		return r.storefront.QueueOrderConfirmation(ctx, rep, to, d)
	}
	l, err := r.lane()
	if err != nil {
		return err
	}
	return l.QueueOrderConfirmation(ctx, rep, to, d)
}

func (r *ShopRouter) SendOrderCancellation(ctx context.Context, rep dependency.Repository, to string, d *dto.OrderCancelled) error {
	if !r.shopTeam(ctx) {
		return r.storefront.SendOrderCancellation(ctx, rep, to, d)
	}
	l, err := r.lane()
	if err != nil {
		return err
	}
	return l.SendOrderCancellation(ctx, rep, to, d)
}

func (r *ShopRouter) SendOrderShipped(ctx context.Context, rep dependency.Repository, to string, d *dto.OrderShipment) error {
	if !r.shopTeam(ctx) {
		return r.storefront.SendOrderShipped(ctx, rep, to, d)
	}
	l, err := r.lane()
	if err != nil {
		return err
	}
	return l.SendOrderShipped(ctx, rep, to, d)
}

func (r *ShopRouter) SendOrderDelivered(ctx context.Context, rep dependency.Repository, to string, d *dto.OrderDelivered) error {
	if !r.shopTeam(ctx) {
		return r.storefront.SendOrderDelivered(ctx, rep, to, d)
	}
	l, err := r.lane()
	if err != nil {
		return err
	}
	return l.SendOrderDelivered(ctx, rep, to, d)
}

func (r *ShopRouter) SendRefundInitiated(ctx context.Context, rep dependency.Repository, to string, d *dto.OrderRefundInitiated) error {
	if !r.shopTeam(ctx) {
		return r.storefront.SendRefundInitiated(ctx, rep, to, d)
	}
	l, err := r.lane()
	if err != nil {
		return err
	}
	return l.SendRefundInitiated(ctx, rep, to, d)
}

func (r *ShopRouter) SendPendingReturn(ctx context.Context, rep dependency.Repository, to string, d *dto.OrderPendingReturn) error {
	if !r.shopTeam(ctx) {
		return r.storefront.SendPendingReturn(ctx, rep, to, d)
	}
	l, err := r.lane()
	if err != nil {
		return err
	}
	return l.SendPendingReturn(ctx, rep, to, d)
}

// Everything else: the storefront's own, refused for a team shop.

func (r *ShopRouter) SendCampaignTest(ctx context.Context, rep dependency.Repository, to, subject, htmlBody, textBody string) error {
	if r.shopTeam(ctx) {
		return ErrShopMailMarketing
	}
	return r.storefront.SendCampaignTest(ctx, rep, to, subject, htmlBody, textBody)
}

func (r *ShopRouter) SendCampaignBatch(ctx context.Context, requests []resend.SendEmailRequest, idempotencyKey string, beforePost func() error) ([]string, error) {
	if r.shopTeam(ctx) {
		return nil, ErrShopMailMarketing
	}
	return r.storefront.SendCampaignBatch(ctx, requests, idempotencyKey, beforePost)
}

// The welcome letter after a newsletter opt-in is marketing: a team shop's is skipped without an
// error, so the opt-in itself (already saved by the caller) still answers OK.

func (r *ShopRouter) SendNewSubscriber(ctx context.Context, rep dependency.Repository, to string) error {
	if r.shopTeam(ctx) {
		skipShopWelcome(ctx)
		return nil
	}
	return r.storefront.SendNewSubscriber(ctx, rep, to)
}

func (r *ShopRouter) QueueNewSubscriber(ctx context.Context, rep dependency.Repository, to string) error {
	if r.shopTeam(ctx) {
		skipShopWelcome(ctx)
		return nil
	}
	return r.storefront.QueueNewSubscriber(ctx, rep, to)
}

func skipShopWelcome(ctx context.Context) {
	slog.Default().InfoContext(ctx, "mail: team-shop welcome letter skipped (no marketing for team shops)")
}

func (r *ShopRouter) SendPromoCode(ctx context.Context, rep dependency.Repository, to string, d *dto.PromoCodeDetails) error {
	if r.shopTeam(ctx) {
		return ErrShopMailMarketing
	}
	return r.storefront.SendPromoCode(ctx, rep, to, d)
}

func (r *ShopRouter) SendBackInStock(ctx context.Context, rep dependency.Repository, to string, d *dto.BackInStock) error {
	if r.shopTeam(ctx) {
		return ErrShopMailMarketing
	}
	return r.storefront.SendBackInStock(ctx, rep, to, d)
}

func (r *ShopRouter) QueueTierUpgrade(ctx context.Context, rep dependency.Repository, to string, d *dto.TierChangeEmail) error {
	if r.shopTeam(ctx) {
		return ErrShopMailMarketing
	}
	return r.storefront.QueueTierUpgrade(ctx, rep, to, d)
}

func (r *ShopRouter) QueueTierDowngrade(ctx context.Context, rep dependency.Repository, to string, d *dto.TierChangeEmail) error {
	if r.shopTeam(ctx) {
		return ErrShopMailMarketing
	}
	return r.storefront.QueueTierDowngrade(ctx, rep, to, d)
}

func (r *ShopRouter) QueueDowngradeReminder(ctx context.Context, rep dependency.Repository, to string, d *dto.TierChangeEmail) error {
	if r.shopTeam(ctx) {
		return ErrShopMailMarketing
	}
	return r.storefront.QueueDowngradeReminder(ctx, rep, to, d)
}

func (r *ShopRouter) QueueTierRollback(ctx context.Context, rep dependency.Repository, to string, d *dto.TierChangeEmail) error {
	if r.shopTeam(ctx) {
		return ErrShopMailMarketing
	}
	return r.storefront.QueueTierRollback(ctx, rep, to, d)
}

func (r *ShopRouter) QueueFirstPurchaseThanks(ctx context.Context, rep dependency.Repository, to string, d *dto.TierChangeEmail) error {
	if r.shopTeam(ctx) {
		return ErrShopMailMarketing
	}
	return r.storefront.QueueFirstPurchaseThanks(ctx, rep, to, d)
}

func (r *ShopRouter) QueueUnsubscribeConfirmation(ctx context.Context, rep dependency.Repository, to string, d *dto.UnsubscribeConfirmationEmail) error {
	if r.shopTeam(ctx) {
		return ErrShopMailMarketing
	}
	return r.storefront.QueueUnsubscribeConfirmation(ctx, rep, to, d)
}

func (r *ShopRouter) QueueBirthdayGift(ctx context.Context, rep dependency.Repository, to string, d *dto.BirthdayEmail) error {
	if r.shopTeam(ctx) {
		return ErrShopMailMarketing
	}
	return r.storefront.QueueBirthdayGift(ctx, rep, to, d)
}

func (r *ShopRouter) QueueEventInvite(ctx context.Context, rep dependency.Repository, to string, d *dto.MemberCustomEmail) error {
	if r.shopTeam(ctx) {
		return ErrShopMailMarketing
	}
	return r.storefront.QueueEventInvite(ctx, rep, to, d)
}

func (r *ShopRouter) QueueHackerInvite(ctx context.Context, rep dependency.Repository, to string, d *dto.HackerInviteEmail) error {
	if r.shopTeam(ctx) {
		return ErrShopMailMarketing
	}
	return r.storefront.QueueHackerInvite(ctx, rep, to, d)
}

// No context, no team: the storefront's configuration and campaign rendering.

func (r *ShopRouter) CampaignDispatchConfigured() error {
	return r.storefront.CampaignDispatchConfigured()
}
func (r *ShopRouter) CampaignSendingDisabled() bool { return r.storefront.CampaignSendingDisabled() }

func (r *ShopRouter) CampaignEnvelope(c *entity.EmailCampaignFull) (string, *string, error) {
	return r.storefront.CampaignEnvelope(c)
}

func (r *ShopRouter) CampaignUnsubscribeURL(topic entity.EmailCampaignTopic, email string) (string, error) {
	return r.storefront.CampaignUnsubscribeURL(topic, email)
}

func (r *ShopRouter) CampaignFooterStrings(languageID int, langs []entity.Language) entity.EmailFooterStrings {
	return r.storefront.CampaignFooterStrings(languageID, langs)
}

func (r *ShopRouter) BuildCampaignSendRequest(to string, snapshot entity.EmailCampaignRenderSnapshot, htmlBody, textBody, unsubscribeURL string) (resend.SendEmailRequest, error) {
	return r.storefront.BuildCampaignSendRequest(to, snapshot, htmlBody, textBody, unsubscribeURL)
}

// Start and Stop run the storefront mailer; the app starts and stops the shop lane itself (it needs
// the team directory).
func (r *ShopRouter) Start(ctx context.Context) error { return r.storefront.Start(ctx) }
func (r *ShopRouter) Stop() error                     { return r.storefront.Stop() }
