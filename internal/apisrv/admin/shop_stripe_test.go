package admin

import (
	"context"
	"fmt"
	"testing"

	authsrv "github.com/jekabolt/grbpwr-manager/internal/apisrv/auth"
	"github.com/jekabolt/grbpwr-manager/internal/dependency"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/jekabolt/grbpwr-manager/internal/feature"
	"github.com/jekabolt/grbpwr-manager/internal/platformstore"
	"github.com/jekabolt/grbpwr-manager/internal/shop"
	"github.com/jekabolt/grbpwr-manager/internal/tenant"
	pb_admin "github.com/jekabolt/grbpwr-manager/proto/gen/admin"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type namedInvoicer struct {
	dependency.Invoicer
	name string
}

type fakePayments struct {
	asked   []int64
	procs   map[int64]dependency.Invoicer
	started []string
	done    []string
	keys    []string
	gone    []int64
	goneOp  []bool
	err     error
}

func (f *fakePayments) StartConnect(_ context.Context, t tenant.Team, _ int64, _ bool, ret string) (string, error) {
	if f.err != nil {
		return "", f.err
	}
	f.started = append(f.started, t.Slug+"|"+ret)
	return "https://connect.stripe.com/oauth/authorize?x=1", nil
}

func (f *fakePayments) FinishConnect(_ context.Context, t tenant.Team, userID int64, operator bool, token string) error {
	if f.err != nil {
		return f.err
	}
	f.done = append(f.done, fmt.Sprintf("%s|%d|%t|%s", t.Slug, userID, operator, token))
	return nil
}

func (f *fakePayments) SetKeys(_ context.Context, t tenant.Team, pk, sk, whsec string) error {
	if f.err != nil {
		return f.err
	}
	f.keys = append(f.keys, t.Slug+"|"+pk+"|"+sk+"|"+whsec)
	return nil
}

func (f *fakePayments) Disconnect(_ context.Context, t tenant.Team, operator bool) error {
	f.gone = append(f.gone, t.ID)
	f.goneOp = append(f.goneOp, operator)
	return f.err
}

func (f *fakePayments) WebhookURL(teamID int64) string {
	return "https://api.example.com/api/webhooks/stripe/team/tok" + string(rune('0'+teamID))
}

func (f *fakePayments) For(_ context.Context, t tenant.Team, _ entity.PaymentMethodName) (dependency.Invoicer, error) {
	f.asked = append(f.asked, t.ID)
	if p, ok := f.procs[t.ID]; ok {
		return p, nil
	}
	return nil, status.Error(codes.FailedPrecondition, msgShopPaymentsNotReady)
}

// TestTeamOrdersRefundThroughTheirOwnStripe: the admin's order paths (payment check, refund) use the
// storefront's processors for the storefront team only; another team gets its own or a refusal.
func TestTeamOrdersRefundThroughTheirOwnStripe(t *testing.T) {
	live, test := &namedInvoicer{name: "storefront live"}, &namedInvoicer{name: "storefront test"}
	team2 := &namedInvoicer{name: "acme"}
	pay := &fakePayments{procs: map[int64]dependency.Invoicer{2: team2}}
	s := &Server{stripePayment: live, stripePaymentTest: test}
	at := func(id int64) context.Context { return tenant.With(context.Background(), tenant.Team{ID: id}) }

	// Shops off (teams mode off): today's selector for everyone.
	h, err := s.getPaymentHandler(at(2), entity.CARD)
	require.NoError(t, err)
	require.Same(t, live, h)

	s.SetShops(ShopWiring{Domains: &shop.FakeDomains{}, Payments: pay})
	for pm, want := range map[entity.PaymentMethodName]*namedInvoicer{entity.CARD: live, entity.CARD_TEST: test} {
		h, err := s.getPaymentHandler(at(tenant.TeamOneID), pm)
		require.NoError(t, err)
		require.Same(t, want, h)
	}
	require.Empty(t, pay.asked, "the storefront team asked team payments")

	h, err = s.getPaymentHandler(at(2), entity.CARD_TEST)
	require.NoError(t, err)
	require.Same(t, team2, h)
	h, err = s.getPaymentHandler(at(3), entity.CARD)
	require.Equal(t, "FailedPrecondition shop_payments_not_ready", refusal(err))
	require.Nil(t, h)

	// Unwired team payments: a team never falls back to the storefront's Stripe.
	s.shopW.Payments = nil
	_, err = s.getPaymentHandler(at(2), entity.CARD)
	require.Equal(t, "FailedPrecondition shop_payments_not_ready", refusal(err))
	_, err = s.getPaymentHandler(at(2), "bank-invoice")
	require.Equal(t, codes.Unimplemented, status.Code(err))

	// Another storefront team: team #1 is then an ordinary team.
	s.shopW.Payments = pay
	s.platform.cfg = platformstore.Config{StorefrontTeamID: 5}
	_, err = s.getPaymentHandler(at(tenant.TeamOneID), entity.CARD)
	require.Equal(t, "FailedPrecondition shop_payments_not_ready", refusal(err))
	h, err = s.getPaymentHandler(at(5), entity.CARD)
	require.NoError(t, err)
	require.Same(t, live, h)
}

func TestShopWebhookURLShownUnlessConnected(t *testing.T) {
	s := &Server{}
	acme := tenant.Team{ID: 2}
	out := &pb_admin.ShopPayments{}
	s.shopWebhookURL(acme, out, platformstore.TeamStripe{}, false)
	require.Empty(t, out.WebhookUrl, "no shop wiring")
	s.SetShops(ShopWiring{Payments: &fakePayments{}})
	s.shopWebhookURL(acme, out, platformstore.TeamStripe{}, false)
	require.NotEmpty(t, out.WebhookUrl)
	out = &pb_admin.ShopPayments{}
	s.shopWebhookURL(acme, out, platformstore.TeamStripe{Kind: platformstore.StripeKindConnect}, true)
	require.Empty(t, out.WebhookUrl, "a connected team has no own webhook")
	s.shopWebhookURL(acme, out, platformstore.TeamStripe{Kind: platformstore.StripeKindKeys}, true)
	require.NotEmpty(t, out.WebhookUrl)
}

// storefrontOwner is an owner's call in the storefront team (grbpwr).
func storefrontOwner() context.Context {
	one := tenant.Team{ID: tenant.TeamOneID, Slug: "grbpwr", Status: tenant.StatusActive}
	ctx := authsrv.PutAdminUsername(context.Background(), "ann")
	ctx = authsrv.PutWorkspace(ctx, authsrv.Workspace{Team: one, Role: roleOwner, UserID: 101, Features: feature.DefaultOn()})
	return tenant.With(ctx, one)
}

// TestShopPaymentsChangedDropsTheTeamsCache: a payments change drops that team's cached shop answers;
// the storefront team (env keys) is never touched.
func TestShopPaymentsChangedDropsTheTeamsCache(t *testing.T) {
	var dropped []int64
	s := &Server{}
	s.ShopPaymentsChanged(context.Background(), 2) // no shop wiring: nothing to do
	s.SetShops(ShopWiring{Payments: &fakePayments{}, Invalidate: func(id int64) { dropped = append(dropped, id) }})
	s.ShopPaymentsChanged(context.Background(), 2) // a ctx without the team: the cache only
	s.ShopPaymentsChanged(tenant.With(context.Background(), tenant.Team{ID: 3}), 2)
	s.ShopPaymentsChanged(context.Background(), tenant.TeamOneID)
	require.Equal(t, []int64{2, 2}, dropped)
}
