package platformstore_test

import (
	"context"
	"fmt"
	"testing"
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
