package admin

import (
	"context"
	"testing"
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/shop"
	pb_admin "github.com/jekabolt/grbpwr-manager/proto/gen/admin"
	"github.com/stretchr/testify/require"
)

// X18 v3: a bare domain brings its "www." sibling as a redirect (removed with it); the storefront
// team picks one of its configured storefront hosts as its main address (admin hosts and CORS-only
// origins neither listed nor accepted) and may clear it, while its switch, domains and mail stay
// refused.
func TestShopWWWSiblingAndStorefrontPrimary(t *testing.T) {
	w := newWorkspaceWorld(t)
	ctx := context.Background()
	ann := w.as("ann", 101, "owner")
	fake := &shop.FakeDomains{Live: map[string]bool{}}
	domains := &failingRemove{FakeDomains: fake}
	w.s.SetShops(ShopWiring{Domains: domains, Invalidate: func(int64) {},
		Reserved: []string{"www.grbpwr.com", "grbpwr.com"}, NoPrimary: []string{"admin.grbpwr.com"},
		StorefrontHosts: []string{"grbpwr.com", "www.grbpwr.com"}})
	proof, err := shop.NewTXTProof("test-pepper-for-the-host-proof")
	require.NoError(t, err)
	txt := map[string][]string{}
	proof.LookupTXT = func(_ context.Context, name string) ([]string, error) { return txt[name], nil }
	w.s.platform.store.SetShopHostProof(proof)
	_, err = w.s.SetShopEnabled(ann, &pb_admin.SetShopEnabledRequest{Enabled: true})
	require.NoError(t, err)

	// A bare domain: its www sibling comes along as a redirect, proven by the domain's TXT.
	at, err := w.s.AttachShopDomain(ann, &pb_admin.AttachShopDomainRequest{Host: "Brand.com"})
	require.NoError(t, err)
	require.Equal(t, "brand.com", at.Domain.Host)
	var sib *pb_admin.ShopDomain
	for _, d := range at.Shop.Domains {
		if d.Host == "www.brand.com" {
			sib = d
		}
	}
	require.NotNil(t, sib, "no www sibling")
	require.Equal(t, "brand.com", sib.RedirectTo)
	require.False(t, sib.Primary)
	require.Equal(t, 1, w.count(t, `SELECT COUNT(*) FROM platform_audit WHERE team_id = 2 AND action = 'shop.domain_attached' AND CAST(detail AS CHAR) LIKE '%redirect_to%'`))
	fake.Live["www.brand.com"], fake.Live["brand.com"] = true, true
	txt["_lekalo.brand.com"] = []string{at.Domain.Records[0].Value}
	ck, err := w.s.CheckShopDomain(ann, &pb_admin.CheckShopDomainRequest{Host: "www.brand.com"})
	require.NoError(t, err)
	require.Equal(t, "live", ck.Domain.Status)
	_, err = w.s.SetShopPrimaryHost(ann, &pb_admin.SetShopPrimaryHostRequest{Host: "www.brand.com"})
	require.Equal(t, "FailedPrecondition host_not_live", refusal(err))

	// Attaching the sibling itself is refused; attaching the domain again keeps one sibling.
	_, err = w.s.AttachShopDomain(ann, &pb_admin.AttachShopDomainRequest{Host: "www.brand.com"})
	require.Equal(t, "FailedPrecondition host_redirect", refusal(err))
	_, err = w.s.AttachShopDomain(ann, &pb_admin.AttachShopDomainRequest{Host: "brand.com"})
	require.NoError(t, err)
	require.Equal(t, 1, w.count(t, `SELECT COUNT(*) FROM shop_host WHERE host = 'www.brand.com' AND team_id = 2 AND redirect_to = 'brand.com'`))

	// A subdomain gets no sibling; a sibling another team holds stays theirs.
	_, err = w.s.AttachShopDomain(ann, &pb_admin.AttachShopDomainRequest{Host: "shop.other.io"})
	require.NoError(t, err)
	require.Equal(t, 0, w.count(t, `SELECT COUNT(*) FROM shop_host WHERE host = 'www.shop.other.io'`))
	_, err = w.st.DB().Exec(`INSERT INTO shop_host (host, team_id, kind, managed, is_primary, status, records, created_at, verified_at)
VALUES ('www.taken.io', 3, 'custom', 0, 0, 'live', '[]', ?, ?)`, time.Now().UTC(), time.Now().UTC())
	require.NoError(t, err)
	_, err = w.s.AttachShopDomain(ann, &pb_admin.AttachShopDomainRequest{Host: "taken.io"})
	require.NoError(t, err)
	require.Equal(t, 1, w.count(t, `SELECT COUNT(*) FROM shop_host WHERE host = 'www.taken.io' AND team_id = 3`))

	// Removing the domain removes its sibling, here and at the provider.
	_, err = w.s.RemoveShopDomain(ann, &pb_admin.RemoveShopDomainRequest{Host: "brand.com"})
	require.NoError(t, err)
	require.Equal(t, []string{"brand.com", "www.brand.com"}, domains.removed)
	require.Equal(t, 0, w.count(t, `SELECT COUNT(*) FROM shop_host WHERE host IN ('brand.com', 'www.brand.com')`))

	// The storefront team: its configured storefront hosts are its domains, an admin host and a
	// CORS-only origin (rows an older boot seeded) left out; one becomes its main address.
	sf := storefrontOwner()
	_, err = w.st.EnsureLegacyShopHosts(ctx, 1, []string{"grbpwr.com", "www.grbpwr.com", "admin.grbpwr.com", "jekabolt.github.io"}, time.Now())
	require.NoError(t, err)
	got, err := w.s.GetShop(sf, &pb_admin.GetShopRequest{})
	require.NoError(t, err)
	var listed []string
	for _, d := range got.Shop.Domains {
		listed = append(listed, d.Host)
	}
	require.ElementsMatch(t, []string{"grbpwr.com", "www.grbpwr.com"}, listed)
	require.Empty(t, got.Shop.PrimaryHost)
	_, err = w.s.SetShopPrimaryHost(sf, &pb_admin.SetShopPrimaryHostRequest{Host: "admin.grbpwr.com"})
	require.Equal(t, "FailedPrecondition host_admin", refusal(err))
	_, err = w.s.SetShopPrimaryHost(sf, &pb_admin.SetShopPrimaryHostRequest{Host: "jekabolt.github.io"})
	require.Equal(t, "FailedPrecondition host_not_storefront", refusal(err))
	require.Equal(t, 0, w.count(t, `SELECT COUNT(*) FROM shop_host WHERE team_id = 1 AND is_primary = 1`))
	pr, err := w.s.SetShopPrimaryHost(sf, &pb_admin.SetShopPrimaryHostRequest{Host: "GRBPWR.com"})
	require.NoError(t, err)
	require.Equal(t, "grbpwr.com", pr.Shop.PrimaryHost)
	pr, err = w.s.SetShopPrimaryHost(sf, &pb_admin.SetShopPrimaryHostRequest{Host: ""})
	require.NoError(t, err)
	require.Empty(t, pr.Shop.PrimaryHost)
	require.Equal(t, 1, w.count(t, `SELECT COUNT(*) FROM platform_audit WHERE team_id = 1 AND action = 'shop.primary_cleared'`))
	// Everything else of the storefront's shop stays env-configured.
	_, err = w.s.SetShopEnabled(sf, &pb_admin.SetShopEnabledRequest{Enabled: false})
	require.Equal(t, "FailedPrecondition shop_legacy", refusal(err))
	_, err = w.s.AttachShopDomain(sf, &pb_admin.AttachShopDomainRequest{Host: "grbpwr.io"})
	require.Equal(t, "FailedPrecondition shop_legacy", refusal(err))
	_, err = w.s.SetShopMail(sf, &pb_admin.SetShopMailRequest{FromName: "x"})
	require.Equal(t, "FailedPrecondition shop_legacy", refusal(err))
	// Any other team cannot clear its main address.
	_, err = w.s.SetShopPrimaryHost(ann, &pb_admin.SetShopPrimaryHostRequest{Host: ""})
	require.Error(t, err)
}
