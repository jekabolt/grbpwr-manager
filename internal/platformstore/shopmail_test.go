package platformstore_test

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"
)

// TestTeamMailNames: every team but a deleted one, with its brand name when it has a brand row
// (team #1 carries the seeded GRBPWR brand).
func TestTeamMailNames(t *testing.T) {
	st := openFresh(t)
	ctx := context.Background()
	seedConsole(t, st)
	for _, q := range []string{
		`INSERT INTO team (id, slug, name, db_name, status, created_at) VALUES (5, 'gone', 'Gone', NULL, 'deleted', '2026-11-05')`,
		`INSERT INTO team_brand (team_id, brand_name) VALUES (2, 'ACME STUDIO'), (5, 'GONE')`,
	} {
		if _, err := st.DB().Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	names, err := st.TeamMailNames(ctx)
	if err != nil {
		t.Fatal(err)
	}
	got := fmt.Sprint(names)
	want := "[{1 grbpwr GRBPWR} {2 Acme ACME STUDIO} {3 Wait } {4 Prov }]"
	if got != want {
		t.Fatalf("names\n got %s\nwant %s", got, want)
	}
}

// TestShopReopenIsAFreshOpening: every transition into open moves enabled_at (the shop mail
// cutover): the team switching it on, and the operator reopening a switched-on shop. Closing,
// reopening a switched-off shop, or a reopen of a shop that was not closed leave it alone.
func TestShopReopenIsAFreshOpening(t *testing.T) {
	st := openFresh(t)
	ctx := context.Background()
	seedConsole(t, st)
	opened := time.Date(2026, 11, 1, 9, 0, 0, 0, time.UTC)
	const sub = "acme.shop.lekalo.test"
	if err := st.SetShopEnabled(ctx, 2, true, sub, 0, opened); err != nil {
		t.Fatal(err)
	}
	at := func(step string) time.Time {
		t.Helper()
		ts, err := st.TeamShop(ctx, 2)
		if err != nil || ts.EnabledAt == nil {
			t.Fatalf("%s: shop %+v %v", step, ts, err)
		}
		return ts.EnabledAt.UTC()
	}
	if got := at("enabled"); !got.Equal(opened) {
		t.Fatalf("enabled_at %v, want %v", got, opened)
	}
	if err := st.SetShopClosed(ctx, 2, false); err != nil {
		t.Fatal(err)
	}
	if got := at("reopen of an open shop"); !got.Equal(opened) {
		t.Fatalf("a reopen of a shop that was not closed moved enabled_at to %v", got)
	}
	if err := st.SetShopClosed(ctx, 2, true); err != nil {
		t.Fatal(err)
	}
	if got := at("closed"); !got.Equal(opened) {
		t.Fatalf("closing moved enabled_at to %v", got)
	}
	before := time.Now().UTC().Add(-time.Second)
	if err := st.SetShopClosed(ctx, 2, false); err != nil {
		t.Fatal(err)
	}
	reopened := at("reopened")
	if reopened.Before(before) {
		t.Fatalf("the operator's reopen left enabled_at at %v", reopened)
	}
	if ts, _ := st.TeamShop(ctx, 2); ts.ClosedByOperator || !ts.Enabled {
		t.Fatalf("reopened shop %+v", ts)
	}

	// Closed, switched off, reopened by the operator: not open yet, so no new opening; the team's
	// switch-on is the opening.
	if err := st.SetShopClosed(ctx, 2, true); err != nil {
		t.Fatal(err)
	}
	if err := st.SetShopEnabled(ctx, 2, false, sub, 0, opened); err != nil {
		t.Fatal(err)
	}
	if err := st.SetShopClosed(ctx, 2, false); err != nil {
		t.Fatal(err)
	}
	if got := at("reopen of a switched-off shop"); !got.Equal(reopened) {
		t.Fatalf("a reopen of a switched-off shop moved enabled_at to %v", got)
	}
	later := time.Date(2026, 12, 1, 9, 0, 0, 0, time.UTC)
	if err := st.SetShopEnabled(ctx, 2, true, sub, 0, later); err != nil {
		t.Fatal(err)
	}
	if got := at("switched on again"); !got.Equal(later) {
		t.Fatalf("enabled_at %v, want %v", got, later)
	}
}

// TestShopMailOwner: an accepted shop letter's team, by Resend's id; the first owner stays, bad ids
// are refused, an unknown id is not found.
func TestShopMailOwner(t *testing.T) {
	st := openFresh(t)
	ctx := context.Background()
	if err := st.RecordShopMailOwner(ctx, "re_1", 2, 41); err != nil {
		t.Fatal(err)
	}
	if err := st.RecordShopMailOwner(ctx, "re_1", 3, 99); err != nil {
		t.Fatal(err)
	}
	team, mailID, ok, err := st.ShopMailOwner(ctx, " re_1 ")
	if err != nil || !ok || team != 2 || mailID != 41 {
		t.Fatalf("owner %d %d %v %v, want team 2 row 41", team, mailID, ok, err)
	}
	if _, _, ok, err := st.ShopMailOwner(ctx, "re_unknown"); ok || err != nil {
		t.Fatalf("unknown id found: %v %v", ok, err)
	}
	if _, _, ok, err := st.ShopMailOwner(ctx, ""); ok || err != nil {
		t.Fatalf("empty id found: %v %v", ok, err)
	}
	for _, bad := range []struct {
		pid        string
		team, mail int64
	}{{"", 2, 1}, {strings.Repeat("x", 65), 2, 1}, {"re_2", 0, 1}, {"re_2", 2, 0}} {
		if err := st.RecordShopMailOwner(ctx, bad.pid, bad.team, bad.mail); err == nil {
			t.Fatalf("recorded a bad owner %+v", bad)
		}
	}
}
