package platformstore_test

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/platformstore"
)

// proofFake proves the "team:host" pairs it names.
type proofFake map[string]bool

func (p proofFake) Record(teamID int64, host string) platformstore.ShopRecord {
	return platformstore.ShopRecord{Type: "TXT", Name: "_lekalo." + host, Value: "proof"}
}

func (p proofFake) Proven(_ context.Context, teamID int64, host string) (bool, error) {
	return p[proofKey(teamID, host)], nil
}

func proofKey(teamID int64, host string) string {
	return string(rune('0'+teamID)) + ":" + host
}

func hostStatus(t *testing.T, st *platformstore.Store, teamID int64, host string) platformstore.ShopHost {
	t.Helper()
	h, err := st.ShopHost(context.Background(), teamID, host)
	if err != nil {
		t.Fatalf("host %s: %v", host, err)
	}
	return h
}

// TestShopHostTakeoverGuards (X18 review, major 1): a custom host goes live only with its team's
// proof, and only live hosts route; a pending host frees its name after 7 days; a let-go host is
// queued for removal at the provider, retried until it goes, and an attach again cancels it.
func TestShopHostTakeoverGuards(t *testing.T) {
	st := openFresh(t)
	ctx := context.Background()
	ann, _, _ := seedConsole(t, st)
	now := time.Now().UTC()
	if err := st.SetShopEnabled(ctx, 2, true, "acme.shop.beta.lekalo.studio", ann, now); err != nil {
		t.Fatal(err)
	}

	// No proof installed: the provider's «live» is not enough.
	if err := st.AttachShopHost(ctx, 2, "acme.com", platformstore.ShopHostLive, nil, now); err != nil {
		t.Fatal(err)
	}
	if h := hostStatus(t, st, 2, "acme.com"); h.Status != platformstore.ShopHostVerifying || h.VerifiedAt != nil {
		t.Fatalf("live without a proof: %+v", h)
	}
	if _, ok, _ := st.ShopHostTeam(ctx, "acme.com"); ok {
		t.Fatal("a host that is not live routes")
	}
	if open, _ := st.OpenShopHosts(ctx); len(open) != 1 {
		t.Fatalf("a host that is not live is a CORS origin: %v", open)
	}

	// A proof of another team (or of nobody) keeps it off; the team's own puts it live.
	proofs := proofFake{proofKey(3, "acme.com"): true}
	st.SetShopHostProof(proofs)
	if _, err := st.SetShopHostStatus(ctx, 2, "acme.com", platformstore.ShopHostLive, []platformstore.ShopRecord{{Type: "A", Name: "@", Value: "76.76.21.21", Found: true}}, now); err != nil {
		t.Fatal(err)
	}
	h := hostStatus(t, st, 2, "acme.com")
	if h.Status != platformstore.ShopHostVerifying || len(h.Records) != 2 || h.Records[0].Type != "TXT" || h.Records[0].Found {
		t.Fatalf("another team's proof: %+v", h)
	}
	proofs[proofKey(2, "acme.com")] = true
	if _, err := st.SetShopHostStatus(ctx, 2, "acme.com", platformstore.ShopHostLive, nil, now); err != nil {
		t.Fatal(err)
	}
	if h := hostStatus(t, st, 2, "acme.com"); h.Status != platformstore.ShopHostLive || !h.Records[0].Found || h.VerifiedAt == nil {
		t.Fatalf("proven: %+v", h)
	}
	if id, ok, _ := st.ShopHostTeam(ctx, "acme.com"); !ok || id != 2 {
		t.Fatalf("proven host routes to %d %v", id, ok)
	}

	// A pending host holds its name for 7 days, then the next team may take it; a live one never.
	old := now.Add(-8 * 24 * time.Hour)
	if err := st.AttachShopHost(ctx, 3, "pending.com", platformstore.ShopHostRecordsNeeded, nil, now.Add(-24*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if err := st.ShopHostFree(ctx, 2, "pending.com", now); !errors.Is(err, platformstore.ErrShopHostTaken) {
		t.Fatalf("a day-old pending host: %v", err)
	}
	if err := st.AttachShopHost(ctx, 2, "pending.com", platformstore.ShopHostRecordsNeeded, nil, now); !errors.Is(err, platformstore.ErrShopHostTaken) {
		t.Fatalf("attach a day-old pending host: %v", err)
	}
	if _, err := st.DB().ExecContext(ctx, `UPDATE shop_host SET created_at = ? WHERE host = 'pending.com'`, old); err != nil {
		t.Fatal(err)
	}
	if err := st.ShopHostFree(ctx, 2, "pending.com", now); err != nil {
		t.Fatalf("an expired pending host: %v", err)
	}
	if err := st.AttachShopHost(ctx, 2, "pending.com", platformstore.ShopHostRecordsNeeded, nil, now); err != nil {
		t.Fatalf("take an expired pending host: %v", err)
	}
	if _, err := st.ShopHost(ctx, 3, "pending.com"); !errors.Is(err, platformstore.ErrShopHostNotFound) {
		t.Fatalf("the old team keeps the host: %v", err)
	}
	if _, err := st.DB().ExecContext(ctx, `UPDATE shop_host SET created_at = ? WHERE host = 'acme.com'`, old); err != nil {
		t.Fatal(err)
	}
	if err := st.AttachShopHost(ctx, 3, "acme.com", platformstore.ShopHostLive, nil, now); !errors.Is(err, platformstore.ErrShopHostTaken) {
		t.Fatalf("an old live host: %v", err)
	}
	if err := st.ShopHostFree(ctx, 2, "acme.shop.beta.lekalo.studio", now); !errors.Is(err, platformstore.ErrShopHostManaged) {
		t.Fatalf("own platform address: %v", err)
	}

	// Removal queues the provider drop; a failure backs off; success clears it.
	if err := st.RemoveShopHost(ctx, 2, "acme.com"); err != nil {
		t.Fatal(err)
	}
	if n := countRows(t, st, `SELECT COUNT(*) FROM shop_domain_drop WHERE host = 'acme.com'`); n != 1 {
		t.Fatalf("queued drops = %d", n)
	}
	var calls []string
	fail := func(_ context.Context, h string) error { calls = append(calls, h); return errors.New("provider down") }
	ok := func(_ context.Context, h string) error { calls = append(calls, h); return nil }
	at := time.Now().UTC().Add(time.Second) // queued at the removal's clock
	if n, err := st.DrainShopDomainDrops(ctx, at, fail); err == nil || n != 0 {
		t.Fatalf("failed drain %d %v", n, err)
	}
	if n := countRows(t, st, `SELECT COUNT(*) FROM shop_domain_drop WHERE host = 'acme.com' AND attempts = 1 AND last_error = 'provider down'`); n != 1 {
		t.Fatal("a failed removal was not recorded")
	}
	if n, err := st.DrainShopDomainDrops(ctx, at, ok); err != nil || n != 0 || len(calls) != 1 {
		t.Fatalf("drain inside the backoff %d %v %v", n, err, calls)
	}
	if n, err := st.DrainShopDomainDrops(ctx, at.Add(2*time.Minute), ok); err != nil || n != 1 || len(calls) != 2 {
		t.Fatalf("drain after the backoff %d %v %v", n, err, calls)
	}
	if n := countRows(t, st, `SELECT COUNT(*) FROM shop_domain_drop`); n != 0 {
		t.Fatalf("drops left = %d", n)
	}

	// Removed then attached again: the queued drop is cancelled, never run.
	if err := st.RemoveShopHost(ctx, 2, "pending.com"); err != nil {
		t.Fatal(err)
	}
	if err := st.AttachShopHost(ctx, 2, "pending.com", platformstore.ShopHostRecordsNeeded, nil, now); err != nil {
		t.Fatal(err)
	}
	if n := countRows(t, st, `SELECT COUNT(*) FROM shop_domain_drop`); n != 0 {
		t.Fatalf("attach again kept the drop: %d", n)
	}

	// DropShopDomainNow runs one at once; a host attached again (by its row) is not removed.
	if err := st.RemoveShopHost(ctx, 2, "pending.com"); err != nil {
		t.Fatal(err)
	}
	if removed, err := st.DropShopDomainNow(ctx, "pending.com", now, ok); err != nil || !removed {
		t.Fatalf("drop now %v %v", removed, err)
	}
	if removed, err := st.DropShopDomainNow(ctx, "pending.com", now, ok); err != nil || removed {
		t.Fatalf("drop now with nothing queued %v %v", removed, err)
	}

	// The wipe queues every custom host of the team.
	if err := st.AttachShopHost(ctx, 2, "wiped.com", platformstore.ShopHostLive, nil, now); err != nil {
		t.Fatal(err)
	}
	tx, err := st.DB().Beginx()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := platformstore.DeleteTeamShopTx(ctx, tx, 2); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if n := countRows(t, st, `SELECT COUNT(*) FROM shop_domain_drop WHERE host = 'wiped.com'`); n != 1 {
		t.Fatalf("wipe queued %d drops", n)
	}
	if n := countRows(t, st, `SELECT COUNT(*) FROM shop_domain_drop WHERE host LIKE '%lekalo.studio'`); n != 0 {
		t.Fatal("a platform address was queued for the provider")
	}
}

// TestLegacyShopHosts (X18 review, major 2): the storefront team's own hosts are seeded at boot as
// live legacy rows, idempotently, and taken back from a team that attached one.
func TestLegacyShopHosts(t *testing.T) {
	st := openFresh(t)
	ctx := context.Background()
	ann, _, _ := seedConsole(t, st)
	now := time.Now().UTC()
	st.SetShopHostProof(proofFake{proofKey(2, "www.grbpwr.com"): true})
	if err := st.SetShopEnabled(ctx, 2, true, "acme.shop.beta.lekalo.studio", ann, now); err != nil {
		t.Fatal(err)
	}
	if err := st.AttachShopHost(ctx, 2, "www.grbpwr.com", platformstore.ShopHostLive, nil, now); err != nil {
		t.Fatal(err)
	}
	if err := st.SetShopPrimary(ctx, 2, "www.grbpwr.com"); err != nil {
		t.Fatal(err)
	}
	taken, err := st.EnsureLegacyShopHosts(ctx, 1, []string{"www.grbpwr.com", "grbpwr.com"}, now)
	if err != nil || len(taken) != 1 || taken[0] != "www.grbpwr.com" {
		t.Fatalf("seed %v %v", taken, err)
	}
	for _, h := range []string{"www.grbpwr.com", "grbpwr.com"} {
		if id, ok, err := st.ShopHostTeam(ctx, h); err != nil || !ok || id != 1 {
			t.Fatalf("%s routes to %d %v %v", h, id, ok, err)
		}
		if lh := hostStatus(t, st, 1, h); lh.Kind != platformstore.ShopHostLegacy || !lh.Managed || lh.Primary {
			t.Fatalf("legacy row %+v", lh)
		}
	}
	if h := hostStatus(t, st, 2, "acme.shop.beta.lekalo.studio"); !h.Primary {
		t.Fatal("the team that lost the host has no main address")
	}
	if taken, err := st.EnsureLegacyShopHosts(ctx, 1, []string{"www.grbpwr.com", "grbpwr.com"}, now); err != nil || len(taken) != 0 {
		t.Fatalf("second seed %v %v", taken, err)
	}
	if n := countRows(t, st, `SELECT COUNT(*) FROM shop_host WHERE team_id = 1`); n != 2 {
		t.Fatalf("legacy rows = %d", n)
	}
	if err := st.AttachShopHost(ctx, 2, "grbpwr.com", platformstore.ShopHostLive, nil, now); !errors.Is(err, platformstore.ErrShopHostTaken) {
		t.Fatalf("attach a legacy host: %v", err)
	}
}

// TestPruneLegacyShopHosts (X18 v3 review): only the configured storefront hosts stay legacy rows of
// the storefront team; a host an older boot seeded from the allowed origins goes, a pruned main
// address leaves no main address, and other kinds and other teams' rows are never touched.
func TestPruneLegacyShopHosts(t *testing.T) {
	st := openFresh(t)
	ctx := context.Background()
	ann, _, _ := seedConsole(t, st)
	now := time.Now().UTC()
	if err := st.SetShopEnabled(ctx, 2, true, "acme.shop.beta.lekalo.studio", ann, now); err != nil {
		t.Fatal(err)
	}
	if _, err := st.EnsureLegacyShopHosts(ctx, 1, []string{"grbpwr.com", "www.grbpwr.com", "jekabolt.github.io", "admin.grbpwr.com"}, now); err != nil {
		t.Fatal(err)
	}
	if err := st.SetShopPrimary(ctx, 1, "jekabolt.github.io"); err != nil {
		t.Fatal(err)
	}
	removed, err := st.PruneLegacyShopHosts(ctx, 1, []string{"grbpwr.com", "www.grbpwr.com"})
	if err != nil || len(removed) != 2 || removed[0] != "admin.grbpwr.com" || removed[1] != "jekabolt.github.io" {
		t.Fatalf("prune: %v %v", removed, err)
	}
	for _, h := range []string{"jekabolt.github.io", "admin.grbpwr.com"} {
		if _, ok, err := st.ShopHostTeam(ctx, h); err != nil || ok {
			t.Fatalf("%s still routes: %v %v", h, ok, err)
		}
	}
	if n := countRows(t, st, `SELECT COUNT(*) FROM shop_host WHERE team_id = 1 AND is_primary = 1`); n != 0 {
		t.Fatalf("a pruned main address stayed: %d", n)
	}
	if n := countRows(t, st, `SELECT COUNT(*) FROM shop_host WHERE team_id = 1`); n != 2 {
		t.Fatalf("storefront rows = %d, want 2", n)
	}
	if h := hostStatus(t, st, 2, "acme.shop.beta.lekalo.studio"); !h.Primary || h.Kind != platformstore.ShopHostSubdomain {
		t.Fatalf("another team's row changed: %+v", h)
	}
	if removed, err := st.PruneLegacyShopHosts(ctx, 1, []string{"grbpwr.com", "www.grbpwr.com"}); err != nil || len(removed) != 0 {
		t.Fatalf("second prune: %v %v", removed, err)
	}
	// Team 2's platform address is never a legacy row: a prune of team 2 with nothing to keep leaves it.
	if removed, err := st.PruneLegacyShopHosts(ctx, 2, nil); err != nil || len(removed) != 0 {
		t.Fatalf("prune of a team without legacy rows: %v %v", removed, err)
	}
	if h := hostStatus(t, st, 2, "acme.shop.beta.lekalo.studio"); !h.Primary {
		t.Fatal("prune removed a platform address")
	}
}

// TestShopHostOnceLiveStaysTheTeams (X18 re-review): a host its team verified once is never free for
// another team, however old and whatever its status now; a lapsed proof keeps its status, only the
// provider demotes it.
func TestShopHostOnceLiveStaysTheTeams(t *testing.T) {
	st := openFresh(t)
	ctx := context.Background()
	ann, _, _ := seedConsole(t, st)
	now := time.Now().UTC()
	if err := st.SetShopEnabled(ctx, 2, true, "acme.shop.beta.lekalo.studio", ann, now); err != nil {
		t.Fatal(err)
	}
	proofs := proofFake{proofKey(2, "victim.com"): true}
	st.SetShopHostProof(proofs)
	if err := st.AttachShopHost(ctx, 2, "victim.com", platformstore.ShopHostLive, nil, now); err != nil {
		t.Fatal(err)
	}
	if err := st.SetShopPrimary(ctx, 2, "victim.com"); err != nil {
		t.Fatal(err)
	}
	if _, err := st.DB().ExecContext(ctx, `UPDATE shop_host SET created_at = ? WHERE host = 'victim.com'`, now.Add(-30*24*time.Hour)); err != nil {
		t.Fatal(err)
	}
	stays := func(what string) {
		t.Helper()
		if err := st.ShopHostFree(ctx, 3, "victim.com", now); !errors.Is(err, platformstore.ErrShopHostTaken) {
			t.Fatalf("%s: free for another team: %v", what, err)
		}
		if err := st.AttachShopHost(ctx, 3, "victim.com", platformstore.ShopHostRecordsNeeded, nil, now); !errors.Is(err, platformstore.ErrShopHostTaken) {
			t.Fatalf("%s: another team attached it: %v", what, err)
		}
		if _, err := st.ShopHost(ctx, 2, "victim.com"); err != nil {
			t.Fatalf("%s: the team lost its host: %v", what, err)
		}
	}

	// The TXT is gone (or the resolver failed) and the provider still says live: nothing changes.
	proofs[proofKey(2, "victim.com")] = false
	if _, err := st.SetShopHostStatus(ctx, 2, "victim.com", platformstore.ShopHostLive, nil, now); err != nil {
		t.Fatal(err)
	}
	if h := hostStatus(t, st, 2, "victim.com"); h.Status != platformstore.ShopHostLive || !h.Primary || h.VerifiedAt == nil {
		t.Fatalf("a lapsed proof demoted a verified host: %+v", h)
	}
	stays("lapsed proof")

	// The provider says it is not live: demoted, the platform address is main, still the team's.
	if _, err := st.SetShopHostStatus(ctx, 2, "victim.com", platformstore.ShopHostFailed, nil, now); err != nil {
		t.Fatal(err)
	}
	if h := hostStatus(t, st, 2, "victim.com"); h.Status != platformstore.ShopHostFailed || h.Primary {
		t.Fatalf("provider failure: %+v", h)
	}
	if h := hostStatus(t, st, 2, "acme.shop.beta.lekalo.studio"); !h.Primary {
		t.Fatal("the platform address did not take the main address")
	}
	stays("failed")

	// Live again at the provider without the proof: keeps its status; with the proof: live.
	if err := st.AttachShopHost(ctx, 2, "victim.com", platformstore.ShopHostLive, nil, now); err != nil {
		t.Fatal(err)
	}
	if h := hostStatus(t, st, 2, "victim.com"); h.Status != platformstore.ShopHostFailed {
		t.Fatalf("live without the proof: %+v", h)
	}
	proofs[proofKey(2, "victim.com")] = true
	if _, err := st.SetShopHostStatus(ctx, 2, "victim.com", platformstore.ShopHostLive, nil, now); err != nil {
		t.Fatal(err)
	}
	if h := hostStatus(t, st, 2, "victim.com"); h.Status != platformstore.ShopHostLive {
		t.Fatalf("proven again: %+v", h)
	}

	// A refused attach keeps a queued provider removal.
	if err := st.AttachShopHost(ctx, 3, "gone.com", platformstore.ShopHostRecordsNeeded, nil, now); err != nil {
		t.Fatal(err)
	}
	if err := st.RemoveShopHost(ctx, 3, "gone.com"); err != nil {
		t.Fatal(err)
	}
	for i := range platformstore.ShopHostMax {
		if err := st.AttachShopHost(ctx, 2, fmt.Sprintf("fill%d.com", i), platformstore.ShopHostRecordsNeeded, nil, now); err != nil && !errors.Is(err, platformstore.ErrShopHostLimit) {
			t.Fatal(err)
		}
	}
	if err := st.AttachShopHost(ctx, 2, "gone.com", platformstore.ShopHostRecordsNeeded, nil, now); !errors.Is(err, platformstore.ErrShopHostLimit) {
		t.Fatalf("attach over the limit: %v", err)
	}
	if n := countRows(t, st, `SELECT COUNT(*) FROM shop_domain_drop WHERE host = 'gone.com'`); n != 1 {
		t.Fatalf("a refused attach dropped the queued removal: %d", n)
	}

	// A team's platform address is never seeded as a legacy host: the boot fails.
	if _, err := st.EnsureLegacyShopHosts(ctx, 1, []string{"acme.shop.beta.lekalo.studio"}, now); !errors.Is(err, platformstore.ErrShopHostManaged) {
		t.Fatalf("legacy seed over a platform address: %v", err)
	}
	if h := hostStatus(t, st, 2, "acme.shop.beta.lekalo.studio"); h.Kind != platformstore.ShopHostSubdomain {
		t.Fatalf("platform address changed: %+v", h)
	}
}
