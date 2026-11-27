package platformstore_test

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"testing"
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/platformstore"
)

// TestShopRedirectSibling (X18 v3): a "www." sibling is a redirect row of the team proven by its
// bare domain's proof, never a main address, not counted against ShopHostMax, and removed (and
// dropped at the provider) with its bare domain.
func TestShopRedirectSibling(t *testing.T) {
	st := openFresh(t)
	ctx := context.Background()
	ann, _, _ := seedConsole(t, st)
	now := time.Now().UTC()
	if err := st.SetShopEnabled(ctx, 2, true, "acme.shop.beta.lekalo.studio", ann, now); err != nil {
		t.Fatal(err)
	}
	proofs := proofFake{proofKey(2, "brand.com"): true}
	st.SetShopHostProof(proofs)
	if err := st.AttachShopHost(ctx, 2, "brand.com", platformstore.ShopHostLive, nil, now); err != nil {
		t.Fatal(err)
	}
	if err := st.AttachShopRedirect(ctx, 2, "www.brand.com", "brand.com", platformstore.ShopHostLive, nil, now); err != nil {
		t.Fatal(err)
	}
	h := hostStatus(t, st, 2, "www.brand.com")
	if h.Status != platformstore.ShopHostLive || h.RedirectTo != "brand.com" || h.Kind != platformstore.ShopHostCustom || h.Managed || h.Primary {
		t.Fatalf("redirect row: %+v", h)
	}
	for _, r := range h.Records {
		if r.Type == "TXT" {
			t.Fatalf("a redirect carries its own proof record: %+v", h.Records)
		}
	}
	// Again: refreshed, still one row.
	if err := st.AttachShopRedirect(ctx, 2, "www.brand.com", "brand.com", platformstore.ShopHostLive, nil, now); err != nil {
		t.Fatal(err)
	}
	if n := countRows(t, st, `SELECT COUNT(*) FROM shop_host WHERE host = 'www.brand.com'`); n != 1 {
		t.Fatalf("rows = %d", n)
	}

	// Refusals: itself as target, a target the team lacks, the redirect as a plain host or a main
	// address, the name for another team.
	if err := st.AttachShopRedirect(ctx, 2, "brand.com", "brand.com", platformstore.ShopHostLive, nil, now); !errors.Is(err, platformstore.ErrShopHostRedirect) {
		t.Fatalf("self target: %v", err)
	}
	if err := st.AttachShopRedirect(ctx, 2, "www.other.com", "other.com", platformstore.ShopHostLive, nil, now); !errors.Is(err, platformstore.ErrShopHostNotFound) {
		t.Fatalf("missing target: %v", err)
	}
	if err := st.AttachShopRedirect(ctx, 2, "www.acme.shop.beta.lekalo.studio", "acme.shop.beta.lekalo.studio", platformstore.ShopHostLive, nil, now); !errors.Is(err, platformstore.ErrShopHostNotFound) {
		t.Fatalf("platform address as target: %v", err)
	}
	if err := st.AttachShopHost(ctx, 2, "www.brand.com", platformstore.ShopHostLive, nil, now); !errors.Is(err, platformstore.ErrShopHostRedirect) {
		t.Fatalf("redirect attached as a host: %v", err)
	}
	if err := st.AttachShopRedirect(ctx, 2, "brand.com", "www.brand.com", platformstore.ShopHostLive, nil, now); !errors.Is(err, platformstore.ErrShopHostNotFound) {
		t.Fatalf("a redirect as a target: %v", err)
	}
	if err := st.SetShopPrimary(ctx, 2, "www.brand.com"); !errors.Is(err, platformstore.ErrShopHostNotLive) {
		t.Fatalf("redirect as main address: %v", err)
	}
	if err := st.ShopHostFree(ctx, 3, "www.brand.com", now); !errors.Is(err, platformstore.ErrShopHostTaken) {
		t.Fatalf("another team: %v", err)
	}
	if id, ok, _ := st.ShopHostTeam(ctx, "www.brand.com"); !ok || id != 2 {
		t.Fatalf("a live redirect routes to %d %v", id, ok)
	}

	// Unproven bare domain: its sibling never goes live; once proven, a check puts it live.
	if err := st.AttachShopHost(ctx, 2, "two.com", platformstore.ShopHostRecordsNeeded, nil, now); err != nil {
		t.Fatal(err)
	}
	if err := st.AttachShopRedirect(ctx, 2, "www.two.com", "two.com", platformstore.ShopHostLive, nil, now); err != nil {
		t.Fatal(err)
	}
	if h := hostStatus(t, st, 2, "www.two.com"); h.Status != platformstore.ShopHostVerifying || h.VerifiedAt != nil {
		t.Fatalf("unproven sibling: %+v", h)
	}
	proofs[proofKey(2, "two.com")] = true
	if got, err := st.SetShopHostStatus(ctx, 2, "www.two.com", platformstore.ShopHostLive, nil, now); err != nil || got != platformstore.ShopHostLive {
		t.Fatalf("proven sibling: %q %v", got, err)
	}

	// The limit counts bare hosts only: brand.com and two.com plus 8 more fill it with two redirects
	// standing.
	for i := 0; i < platformstore.ShopHostMax-2; i++ {
		if err := st.AttachShopHost(ctx, 2, fmt.Sprintf("h%d.com", i), platformstore.ShopHostRecordsNeeded, nil, now); err != nil {
			t.Fatalf("host %d: %v", i, err)
		}
	}
	if err := st.AttachShopHost(ctx, 2, "over.com", platformstore.ShopHostRecordsNeeded, nil, now); !errors.Is(err, platformstore.ErrShopHostLimit) {
		t.Fatalf("over the limit: %v", err)
	}

	// Removing the sibling alone keeps its bare domain; removing the bare domain takes its sibling.
	gone, err := st.RemoveShopHostAll(ctx, 2, "www.two.com")
	if err != nil || !slices.Equal(gone, []string{"www.two.com"}) {
		t.Fatalf("remove sibling: %v %v", gone, err)
	}
	hostStatus(t, st, 2, "two.com")
	gone, err = st.RemoveShopHostAll(ctx, 2, "brand.com")
	if err != nil || !slices.Equal(gone, []string{"brand.com", "www.brand.com"}) {
		t.Fatalf("remove bare domain: %v %v", gone, err)
	}
	if n := countRows(t, st, `SELECT COUNT(*) FROM shop_host WHERE host IN ('brand.com', 'www.brand.com')`); n != 0 {
		t.Fatalf("rows left = %d", n)
	}
	if n := countRows(t, st, `SELECT COUNT(*) FROM shop_domain_drop WHERE host IN ('brand.com', 'www.brand.com', 'www.two.com')`); n != 3 {
		t.Fatalf("queued drops = %d", n)
	}
	// Attached again: the queued drop of the sibling is cancelled.
	if err := st.AttachShopRedirect(ctx, 2, "www.two.com", "two.com", platformstore.ShopHostLive, nil, now); err != nil {
		t.Fatal(err)
	}
	if n := countRows(t, st, `SELECT COUNT(*) FROM shop_domain_drop WHERE host = 'www.two.com'`); n != 0 {
		t.Fatal("a re-attached sibling stays queued for removal")
	}
}

// TestStorefrontPrimaryOnLegacy (X18 v3): the storefront team's main address may be one of its
// legacy hosts, moved between them and cleared again.
func TestStorefrontPrimaryOnLegacy(t *testing.T) {
	st := openFresh(t)
	ctx := context.Background()
	seedConsole(t, st)
	now := time.Now().UTC()
	st.SetLegacyShopHosts(1, []string{"www.grbpwr.com", "grbpwr.com"})
	if _, err := st.EnsureLegacyShopHosts(ctx, 1, []string{"www.grbpwr.com", "grbpwr.com"}, now); err != nil {
		t.Fatal(err)
	}
	primary := func() string {
		t.Helper()
		hosts, err := st.ShopHosts(ctx, 1)
		if err != nil {
			t.Fatal(err)
		}
		p := ""
		for _, h := range hosts {
			if h.Primary {
				if p != "" {
					t.Fatalf("two main addresses: %s and %s", p, h.Host)
				}
				p = h.Host
			}
		}
		return p
	}
	if p := primary(); p != "" {
		t.Fatalf("legacy hosts came with a main address: %s", p)
	}
	if err := st.SetShopPrimary(ctx, 1, "grbpwr.com"); err != nil {
		t.Fatal(err)
	}
	if p := primary(); p != "grbpwr.com" {
		t.Fatalf("main address %q", p)
	}
	if err := st.SetShopPrimary(ctx, 1, "www.grbpwr.com"); err != nil {
		t.Fatal(err)
	}
	if p := primary(); p != "www.grbpwr.com" {
		t.Fatalf("moved main address %q", p)
	}
	// Seeding again at the next boot keeps it.
	if _, err := st.EnsureLegacyShopHosts(ctx, 1, []string{"www.grbpwr.com", "grbpwr.com"}, now); err != nil {
		t.Fatal(err)
	}
	if p := primary(); p != "www.grbpwr.com" {
		t.Fatalf("after a reseed %q", p)
	}
	if err := st.ClearShopPrimary(ctx, 1); err != nil {
		t.Fatal(err)
	}
	if p := primary(); p != "" {
		t.Fatalf("cleared main address %q", p)
	}
	if err := st.ClearShopPrimary(ctx, 1); err != nil {
		t.Fatalf("clear again: %v", err)
	}
}
