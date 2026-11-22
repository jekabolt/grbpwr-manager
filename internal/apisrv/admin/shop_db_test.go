package admin

import (
	"testing"

	"github.com/jekabolt/grbpwr-manager/internal/shop"
	pb_admin "github.com/jekabolt/grbpwr-manager/proto/gen/admin"
	"github.com/stretchr/testify/require"
)

// The shop screen against a real control plane (X18): acme (team 2), ann owner, bob member.
func TestShopScreenFlow(t *testing.T) {
	w := newWorkspaceWorld(t)
	ann, bob := w.as("ann", 101, "owner"), w.as("bob", 102, "member")

	// Not wired = teams off.
	_, err := w.s.GetShop(ann, &pb_admin.GetShopRequest{})
	require.Error(t, err)

	domains := &shop.FakeDomains{Live: map[string]bool{}}
	invalidated := 0
	w.s.SetShops(ShopWiring{Domains: domains, Invalidate: func(int64) { invalidated++ }})

	got, err := w.s.GetShop(bob, &pb_admin.GetShopRequest{})
	require.NoError(t, err)
	require.False(t, got.Shop.Enabled)
	require.Equal(t, "off", got.Shop.Status)
	require.Equal(t, "none", got.Shop.Payments.Method)

	// Writes are the owner's.
	_, err = w.s.SetShopEnabled(bob, &pb_admin.SetShopEnabledRequest{Enabled: true})
	require.Contains(t, refusal(err), "PermissionDenied")

	on, err := w.s.SetShopEnabled(ann, &pb_admin.SetShopEnabledRequest{Enabled: true})
	require.NoError(t, err)
	require.True(t, on.Shop.Enabled)
	require.Equal(t, "acme.shop.beta.lekalo.studio", on.Shop.Address)
	require.Equal(t, "acme.shop.beta.lekalo.studio", on.Shop.PrimaryHost)
	require.Equal(t, 1, invalidated)

	// A custom domain: stored, with the records to add, not yet the main address.
	_, err = w.s.AttachShopDomain(ann, &pb_admin.AttachShopDomainRequest{Host: "x.vercel.app"})
	require.Equal(t, "InvalidArgument bad_host", refusal(err))
	_, err = w.s.AttachShopDomain(ann, &pb_admin.AttachShopDomainRequest{Host: "bolt.shop.beta.lekalo.studio"})
	require.Equal(t, "InvalidArgument bad_host", refusal(err))
	at, err := w.s.AttachShopDomain(ann, &pb_admin.AttachShopDomainRequest{Host: "Shop.Acme.io"})
	require.NoError(t, err)
	require.Equal(t, "shop.acme.io", at.Domain.Host)
	require.Equal(t, "records_needed", at.Domain.Status)
	require.Len(t, at.Domain.Records, 1)
	require.Equal(t, "CNAME", at.Domain.Records[0].Type)
	require.Len(t, at.Shop.Domains, 2)
	_, err = w.s.SetShopPrimaryHost(ann, &pb_admin.SetShopPrimaryHostRequest{Host: "shop.acme.io"})
	require.Equal(t, "FailedPrecondition host_not_live", refusal(err))

	domains.Live["shop.acme.io"] = true
	ck, err := w.s.CheckShopDomain(ann, &pb_admin.CheckShopDomainRequest{Host: "shop.acme.io"})
	require.NoError(t, err)
	require.Equal(t, "live", ck.Domain.Status)
	pr, err := w.s.SetShopPrimaryHost(ann, &pb_admin.SetShopPrimaryHostRequest{Host: "shop.acme.io"})
	require.NoError(t, err)
	require.Equal(t, "shop.acme.io", pr.Shop.PrimaryHost)

	// Mail sender.
	_, err = w.s.SetShopMail(ann, &pb_admin.SetShopMailRequest{FromName: "Acme\r\nBcc: x"})
	require.Equal(t, "InvalidArgument bad_from_name", refusal(err))
	_, err = w.s.SetShopMail(ann, &pb_admin.SetShopMailRequest{ReplyTo: "nope"})
	require.Equal(t, "InvalidArgument bad_reply_to", refusal(err))
	ml, err := w.s.SetShopMail(ann, &pb_admin.SetShopMailRequest{FromName: "Acme", ReplyTo: "hi@acme.io"})
	require.NoError(t, err)
	require.Equal(t, "Acme", ml.Shop.Mail.FromName)

	// Removing the main domain falls back to the platform address.
	rm, err := w.s.RemoveShopDomain(ann, &pb_admin.RemoveShopDomainRequest{Host: "shop.acme.io"})
	require.NoError(t, err)
	require.Equal(t, "acme.shop.beta.lekalo.studio", rm.Shop.PrimaryHost)
	_, err = w.s.RemoveShopDomain(ann, &pb_admin.RemoveShopDomainRequest{Host: "acme.shop.beta.lekalo.studio"})
	require.Contains(t, refusal(err), "host_managed")

	// Stripe is lane L3's.
	_, err = w.s.StartStripeConnect(ann, &pb_admin.StartStripeConnectRequest{})
	require.Equal(t, "Unimplemented stripe_not_ready", refusal(err))

	require.Equal(t, 5, w.count(t, `SELECT COUNT(*) FROM platform_audit WHERE team_id = 2 AND action LIKE 'shop.%'`))
}
