package mail

import (
	"context"
	"testing"

	"github.com/jekabolt/grbpwr-manager/internal/dependency"
	mocks "github.com/jekabolt/grbpwr-manager/internal/dependency/mocks"
	"github.com/jekabolt/grbpwr-manager/internal/dto"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

// recordingShop records which shop letter the router handed over.
type recordingShop struct{ got []string }

func (r *recordingShop) QueueAccountLogin(context.Context, dependency.Repository, string, string, string) error {
	r.got = append(r.got, "login")
	return nil
}
func (r *recordingShop) QueueOrderConfirmation(context.Context, dependency.Repository, string, *dto.OrderConfirmed) error {
	r.got = append(r.got, "confirmed")
	return nil
}
func (r *recordingShop) SendOrderCancellation(context.Context, dependency.Repository, string, *dto.OrderCancelled) error {
	r.got = append(r.got, "cancelled")
	return nil
}
func (r *recordingShop) SendOrderShipped(context.Context, dependency.Repository, string, *dto.OrderShipment) error {
	r.got = append(r.got, "shipped")
	return nil
}
func (r *recordingShop) SendOrderDelivered(context.Context, dependency.Repository, string, *dto.OrderDelivered) error {
	r.got = append(r.got, "delivered")
	return nil
}
func (r *recordingShop) SendRefundInitiated(context.Context, dependency.Repository, string, *dto.OrderRefundInitiated) error {
	r.got = append(r.got, "refund")
	return nil
}
func (r *recordingShop) SendPendingReturn(context.Context, dependency.Repository, string, *dto.OrderPendingReturn) error {
	r.got = append(r.got, "return")
	return nil
}

func TestShopRouterSendsShopLettersToTheShopLane(t *testing.T) {
	sf := mocks.NewMockMailer(t) // any storefront call fails the test
	shop := &recordingShop{}
	r := &ShopRouter{storefront: sf, shop: shop, storefrontTeamID: 1}
	ctx := teamCtx(5)
	require.NoError(t, r.QueueAccountLogin(ctx, nil, "c@x.test", "1", "u"))
	require.NoError(t, r.SendOrderConfirmation(ctx, nil, "c@x.test", &dto.OrderConfirmed{}))
	require.NoError(t, r.QueueOrderConfirmation(ctx, nil, "c@x.test", &dto.OrderConfirmed{}))
	require.NoError(t, r.SendOrderCancellation(ctx, nil, "c@x.test", &dto.OrderCancelled{}))
	require.NoError(t, r.SendOrderShipped(ctx, nil, "c@x.test", &dto.OrderShipment{}))
	require.NoError(t, r.SendOrderDelivered(ctx, nil, "c@x.test", &dto.OrderDelivered{}))
	require.NoError(t, r.SendRefundInitiated(ctx, nil, "c@x.test", &dto.OrderRefundInitiated{}))
	require.NoError(t, r.SendPendingReturn(ctx, nil, "c@x.test", &dto.OrderPendingReturn{}))
	require.Equal(t, []string{"login", "confirmed", "confirmed", "cancelled", "shipped", "delivered", "refund", "return"}, shop.got)
}

func TestShopRouterRefusesShopMarketing(t *testing.T) {
	sf := mocks.NewMockMailer(t)
	r := &ShopRouter{storefront: sf, shop: &recordingShop{}, storefrontTeamID: 1}
	ctx := teamCtx(5)
	require.ErrorIs(t, r.SendPromoCode(ctx, nil, "c@x.test", &dto.PromoCodeDetails{}), ErrShopMailMarketing)
	require.ErrorIs(t, r.SendBackInStock(ctx, nil, "c@x.test", &dto.BackInStock{}), ErrShopMailMarketing)
	require.ErrorIs(t, r.QueueTierUpgrade(ctx, nil, "c@x.test", &dto.TierChangeEmail{}), ErrShopMailMarketing)
	require.ErrorIs(t, r.QueueBirthdayGift(ctx, nil, "c@x.test", &dto.BirthdayEmail{}), ErrShopMailMarketing)
	require.ErrorIs(t, r.SendCampaignTest(ctx, nil, "c@x.test", "s", "h", "t"), ErrShopMailMarketing)
	_, err := r.SendCampaignBatch(ctx, nil, "k", nil)
	require.ErrorIs(t, err, ErrShopMailMarketing)
	// The welcome letter is skipped without failing the opt-in.
	require.NoError(t, r.SendNewSubscriber(ctx, nil, "c@x.test"))
	require.NoError(t, r.QueueNewSubscriber(ctx, nil, "c@x.test"))
}

func TestShopRouterWithoutLaneRefusesShopLetters(t *testing.T) {
	r := NewShopRouter(mocks.NewMockMailer(t), nil, 0)
	require.Nil(t, r.shop, "a nil lane stays a nil interface")
	require.ErrorIs(t, r.QueueAccountLogin(teamCtx(5), nil, "c@x.test", "1", "u"), ErrShopMailOff)
	require.ErrorIs(t, r.SendOrderShipped(teamCtx(1), nil, "c@x.test", &dto.OrderShipment{}), ErrShopMailOff,
		"under none (no storefront team) team #1 is a team shop too")
}

func TestShopRouterKeepsTheStorefront(t *testing.T) {
	sf := mocks.NewMockMailer(t)
	shop := &recordingShop{}
	r := &ShopRouter{storefront: sf, shop: shop, storefrontTeamID: 1}
	sf.EXPECT().QueueAccountLogin(mock.Anything, mock.Anything, "c@x.test", "1", "u").Return(nil).Twice()
	sf.EXPECT().SendPromoCode(mock.Anything, mock.Anything, "c@x.test", mock.Anything).Return(nil).Once()
	sf.EXPECT().Stop().Return(nil).Once()
	require.NoError(t, r.QueueAccountLogin(teamCtx(1), nil, "c@x.test", "1", "u"))
	require.NoError(t, r.QueueAccountLogin(context.Background(), nil, "c@x.test", "1", "u"), "no team: the storefront")
	require.NoError(t, r.SendPromoCode(teamCtx(1), nil, "c@x.test", &dto.PromoCodeDetails{}))
	require.NoError(t, r.Stop())
	require.Empty(t, shop.got)
	require.Equal(t, sf, r.Storefront())
}
