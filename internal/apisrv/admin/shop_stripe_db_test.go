package admin

import (
	"context"
	"testing"

	authsrv "github.com/jekabolt/grbpwr-manager/internal/apisrv/auth"
	"github.com/jekabolt/grbpwr-manager/internal/feature"
	"github.com/jekabolt/grbpwr-manager/internal/tenant"

	"github.com/jekabolt/grbpwr-manager/internal/shop"
	pb_admin "github.com/jekabolt/grbpwr-manager/proto/gen/admin"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// The Stripe RPCs of the shop screen against a real control plane (X18-L3).
func TestShopStripeRPCs(t *testing.T) {
	w := newWorkspaceWorld(t)
	ann, bob := w.as("ann", 101, "owner"), w.as("bob", 102, "member")
	pay := &fakePayments{}
	w.s.SetShops(ShopWiring{Domains: &shop.FakeDomains{}, Payments: pay})

	// Members read, owners change.
	got, err := w.s.GetShop(bob, &pb_admin.GetShopRequest{})
	require.NoError(t, err)
	require.Equal(t, "none", got.Shop.Payments.Method)
	require.NotEmpty(t, got.Shop.Payments.WebhookUrl, "the keys form needs the webhook address before the first save")
	_, err = w.s.StartStripeConnect(bob, &pb_admin.StartStripeConnectRequest{})
	require.Equal(t, "PermissionDenied owner_only", refusal(err))
	_, err = w.s.SetStripeKeys(bob, &pb_admin.SetStripeKeysRequest{PublishableKey: "pk_test_1", SecretKey: "sk_test_1", WebhookSecret: "whsec_1"})
	require.Equal(t, "PermissionDenied owner_only", refusal(err))
	_, err = w.s.DisconnectStripe(bob, &pb_admin.DisconnectStripeRequest{})
	require.Equal(t, "PermissionDenied owner_only", refusal(err))
	_, err = w.s.FinishStripeConnect(bob, &pb_admin.FinishStripeConnectRequest{Token: "tok"})
	require.Equal(t, "PermissionDenied owner_only", refusal(err))
	require.Empty(t, pay.started)
	require.Empty(t, pay.done)
	require.Empty(t, pay.keys)
	require.Empty(t, pay.gone)

	st, err := w.s.StartStripeConnect(ann, &pb_admin.StartStripeConnectRequest{ReturnPath: " /shop "})
	require.NoError(t, err)
	require.Equal(t, "https://connect.stripe.com/oauth/authorize?x=1", st.Url)
	require.NotNil(t, st.Shop)
	require.Equal(t, []string{"acme|/shop"}, pay.started)

	// The finish goes to the service with the signed-in caller: it alone may finish its own connect.
	fin, err := w.s.FinishStripeConnect(ann, &pb_admin.FinishStripeConnectRequest{Token: " tok "})
	require.NoError(t, err)
	require.NotNil(t, fin.Shop)
	require.Equal(t, []string{"acme|101|false|tok"}, pay.done)
	pay.err = status.Error(codes.PermissionDenied, "stripe_connect_not_yours")
	_, err = w.s.FinishStripeConnect(ann, &pb_admin.FinishStripeConnectRequest{Token: "tok"})
	require.Equal(t, "PermissionDenied stripe_connect_not_yours", refusal(err))
	pay.err = status.Error(codes.FailedPrecondition, "shop_payments_busy")
	_, err = w.s.DisconnectStripe(ann, &pb_admin.DisconnectStripeRequest{})
	require.Equal(t, "FailedPrecondition shop_payments_busy", refusal(err))
	pay.err, pay.gone, pay.goneOp = nil, nil, nil

	_, err = w.s.SetStripeKeys(ann, &pb_admin.SetStripeKeysRequest{PublishableKey: "pk_test_1", SecretKey: "sk_test_1", WebhookSecret: "whsec_1"})
	require.NoError(t, err)
	require.Equal(t, []string{"acme|pk_test_1|sk_test_1|whsec_1"}, pay.keys)

	_, err = w.s.DisconnectStripe(ann, &pb_admin.DisconnectStripeRequest{})
	require.NoError(t, err)
	require.Equal(t, []int64{2}, pay.gone)
	require.Equal(t, []bool{false}, pay.goneOp, "an owner's disconnect waits for the team's card orders")

	// An entered console operator's disconnect goes to the service as the operator's (fraud cut-off).
	op := authsrv.PutAdminUsername(context.Background(), "ann")
	op = authsrv.PutWorkspace(op, authsrv.Workspace{Team: annTeam(ann), Role: "owner", UserID: 101, Operator: true, Features: feature.DefaultOn()})
	op = tenant.With(op, annTeam(ann))
	_, err = w.s.DisconnectStripe(op, &pb_admin.DisconnectStripeRequest{})
	require.NoError(t, err)
	require.Equal(t, []bool{false, true}, pay.goneOp)
	require.Equal(t, 1, w.count(t, `SELECT COUNT(*) FROM platform_audit WHERE team_id = 2 AND action = 'shop.stripe_disconnect' AND CAST(detail AS CHAR) LIKE '%operator%'`))

	// The service's refusals reach the screen as they are.
	pay.err = status.Error(codes.AlreadyExists, "stripe_account_taken")
	_, err = w.s.SetStripeKeys(ann, &pb_admin.SetStripeKeysRequest{PublishableKey: "pk_test_1", SecretKey: "sk_test_1", WebhookSecret: "whsec_1"})
	require.Equal(t, "AlreadyExists stripe_account_taken", refusal(err))
	pay.err = nil

	// grbpwr's payments are env keys: not switchable here.
	_, err = w.s.StartStripeConnect(storefrontOwner(), &pb_admin.StartStripeConnectRequest{})
	require.Equal(t, "FailedPrecondition shop_legacy", refusal(err))
	_, err = w.s.SetStripeKeys(storefrontOwner(), &pb_admin.SetStripeKeysRequest{})
	require.Equal(t, "FailedPrecondition shop_legacy", refusal(err))

	// Not wired: stripe_not_ready.
	w.s.shopW.Payments = nil
	_, err = w.s.DisconnectStripe(ann, &pb_admin.DisconnectStripeRequest{})
	require.Equal(t, "Unimplemented stripe_not_ready", refusal(err))

	require.Equal(t, 4, w.count(t, `SELECT COUNT(*) FROM platform_audit WHERE team_id = 2 AND action LIKE 'shop.stripe_%'`))
	require.Equal(t, 0, w.count(t, `SELECT COUNT(*) FROM platform_audit WHERE CAST(detail AS CHAR) LIKE '%sk_test%' OR CAST(detail AS CHAR) LIKE '%whsec%'`))
}

// annTeam is the team of a workspace call (acme).
func annTeam(ctx context.Context) tenant.Team {
	t, _ := tenant.From(ctx)
	return t
}
