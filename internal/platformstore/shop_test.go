package platformstore_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/platformstore"
)

// TestShopHosts: turning a shop on gives it its platform address as the main one; custom hosts
// are one team's; only a live host becomes the main address; losing it falls back to the platform
// address; a rename replaces the platform address; the console summary and the open-host list
// follow; a hard delete takes every shop row.
func TestShopHosts(t *testing.T) {
	st := openFresh(t)
	ctx := context.Background()
	ann, _, _ := seedConsole(t, st)
	now := time.Now().UTC()
	const sub = "acme.shop.beta.lekalo.studio"
	st.SetShopHostProof(proofFake{"2:shop.acme.com": true})

	if ts, err := st.TeamShop(ctx, 2); err != nil || ts.Enabled {
		t.Fatalf("no row: %+v %v", ts, err)
	}
	if err := st.SetShopEnabled(ctx, 2, true, sub, ann, now); err != nil {
		t.Fatal(err)
	}
	if id, ok, err := st.ShopHostTeam(ctx, sub); err != nil || !ok || id != 2 {
		t.Fatalf("subdomain owner %d %v %v", id, ok, err)
	}
	hosts, err := st.ShopHosts(ctx, 2)
	if err != nil || len(hosts) != 1 || !hosts[0].Primary || hosts[0].Status != platformstore.ShopHostLive || !hosts[0].Managed {
		t.Fatalf("hosts %+v %v", hosts, err)
	}
	if open, err := st.OpenShopHosts(ctx); err != nil || len(open) != 1 || open[0] != sub {
		t.Fatalf("open hosts %v %v", open, err)
	}

	// A custom host: records needed, then live, then main.
	recs := []platformstore.ShopRecord{{Type: "CNAME", Name: "shop", Value: "cname.vercel-dns.com"}}
	if err := st.AttachShopHost(ctx, 2, "shop.acme.com", platformstore.ShopHostRecordsNeeded, recs, now); err != nil {
		t.Fatal(err)
	}
	if err := st.AttachShopHost(ctx, 3, "shop.acme.com", platformstore.ShopHostRecordsNeeded, nil, now); !errors.Is(err, platformstore.ErrShopHostTaken) {
		t.Fatalf("another team's host: %v", err)
	}
	if err := st.AttachShopHost(ctx, 2, sub, platformstore.ShopHostLive, nil, now); !errors.Is(err, platformstore.ErrShopHostManaged) {
		t.Fatalf("attach the platform address: %v", err)
	}
	if err := st.SetShopPrimary(ctx, 2, "shop.acme.com"); !errors.Is(err, platformstore.ErrShopHostNotLive) {
		t.Fatalf("main before live: %v", err)
	}
	h, err := st.ShopHost(ctx, 2, "shop.acme.com")
	if err != nil || len(h.Records) != 2 || h.Records[0].Type != "TXT" || h.Records[1].Value != "cname.vercel-dns.com" || h.VerifiedAt != nil {
		t.Fatalf("custom host %+v %v", h, err)
	}
	recs[0].Found = true
	if err := st.SetShopHostStatus(ctx, 2, "shop.acme.com", platformstore.ShopHostLive, recs, now); err != nil {
		t.Fatal(err)
	}
	if err := st.SetShopPrimary(ctx, 2, "shop.acme.com"); err != nil {
		t.Fatal(err)
	}
	if n := countRows(t, st, `SELECT COUNT(*) FROM shop_host WHERE team_id = 2 AND is_primary = 1`); n != 1 {
		t.Fatalf("main addresses = %d", n)
	}
	sums, err := st.ShopSummaries(ctx, []int64{2, 3})
	if err != nil || sums[2].PrimaryHost != "shop.acme.com" || !sums[2].Enabled || sums[2].Payable {
		t.Fatalf("summary %+v %v", sums, err)
	}
	if _, ok := sums[3]; ok {
		t.Fatal("a team without a shop has a summary")
	}

	// The custom host fails its check: the platform address is main again.
	if err := st.SetShopHostStatus(ctx, 2, "shop.acme.com", platformstore.ShopHostFailed, recs, now); err != nil {
		t.Fatal(err)
	}
	if h, _ := st.ShopHost(ctx, 2, sub); !h.Primary {
		t.Fatal("the platform address did not take the main address back")
	}
	if err := st.RemoveShopHost(ctx, 2, sub); !errors.Is(err, platformstore.ErrShopHostManaged) {
		t.Fatalf("remove the platform address: %v", err)
	}

	// A rename: the new platform address replaces the old one and stays main.
	const sub2 = "acme2.shop.beta.lekalo.studio"
	if err := st.EnsureShopSubdomain(ctx, 2, sub2, now); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := st.ShopHostTeam(ctx, sub); ok {
		t.Fatal("the old platform address still answers")
	}
	if h, err := st.ShopHost(ctx, 2, sub2); err != nil || !h.Primary {
		t.Fatalf("new platform address %+v %v", h, err)
	}

	// Mail, operator close, switch off.
	if err := st.SetShopMail(ctx, 2, "Acme", "hi@acme.com", ann); err != nil {
		t.Fatal(err)
	}
	if err := st.SetShopClosed(ctx, 2, true); err != nil {
		t.Fatal(err)
	}
	ts, err := st.TeamShop(ctx, 2)
	if err != nil || !ts.Enabled || !ts.ClosedByOperator || ts.MailFromName != "Acme" || ts.MailReplyTo != "hi@acme.com" {
		t.Fatalf("shop %+v %v", ts, err)
	}
	if open, _ := st.OpenShopHosts(ctx); len(open) != 0 {
		t.Fatalf("a closed shop's hosts are open: %v", open)
	}
	if err := st.SetShopEnabled(ctx, 2, false, sub2, ann, now); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := st.TeamStripe(ctx, 2); ok {
		t.Fatal("stripe row from nowhere")
	}

	// The wipe's finish takes every shop row.
	tx, err := st.DB().Beginx()
	if err != nil {
		t.Fatal(err)
	}
	if rows, err := platformstore.DeleteTeamShopTx(ctx, tx, 2); err != nil || rows["team_shop"] != 1 {
		t.Fatalf("delete shop rows %v %v", rows, err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{`SELECT COUNT(*) FROM shop_host WHERE team_id = 2`, `SELECT COUNT(*) FROM team_shop WHERE team_id = 2`} {
		if n := countRows(t, st, q); n != 0 {
			t.Fatalf("%s = %d", q, n)
		}
	}
}
