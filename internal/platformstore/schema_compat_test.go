package platformstore_test

import (
	"context"
	"testing"

	"github.com/jekabolt/grbpwr-manager/internal/platformstore"
)

// X3: a rollback binary boots on a control plane that a newer release migrated further with
// additive migrations; an unknown migration in the middle of its history still refuses.
func TestControlPlaneAheadByAdditiveMigrationsStillOpens(t *testing.T) {
	st := openFresh(t)
	d := dsn(t, cpDB)
	ctx := context.Background()
	if _, err := st.DB().Exec(`INSERT INTO platform_migrations (id, applied_at) VALUES ('9999_from_a_newer_release.sql', NOW())`); err != nil {
		t.Fatal(err)
	}
	again, err := platformstore.Open(ctx, d)
	if err != nil {
		t.Fatalf("a control plane ahead by an additive migration must open: %v", err)
	}
	again.Close()

	if _, err := st.DB().Exec(`INSERT INTO platform_migrations (id, applied_at) VALUES ('0005_zz_diverged.sql', NOW())`); err != nil {
		t.Fatal(err)
	}
	if again, err := platformstore.Open(ctx, d); err == nil {
		again.Close()
		t.Fatal("a diverged history must still refuse")
	}
	if _, err := st.DB().Exec(`DELETE FROM platform_migrations WHERE id = '0005_zz_diverged.sql'`); err != nil {
		t.Fatal(err)
	}
	if _, err := st.DB().Exec(`INSERT INTO platform_migrations (id, applied_at) VALUES ('9999_breaking_drop.sql', NOW())`); err != nil {
		t.Fatal(err)
	}
	if again, err := platformstore.Open(ctx, d); err == nil {
		again.Close()
		t.Fatal("a newer breaking control-plane migration must refuse")
	}
}

func TestRaiseTeamMinCodeOnlyRaises(t *testing.T) {
	st := openFresh(t)
	ctx := context.Background()
	res, err := st.DB().Exec(`INSERT INTO team (slug, name, db_name, status) VALUES ('x3mincode', 'x3', 'grbpwr_t_x3mincode', 'active')`)
	if err != nil {
		t.Fatal(err)
	}
	id, _ := res.LastInsertId()
	get := func() string {
		var v *string
		if err := st.DB().Get(&v, `SELECT schema_min_code FROM team WHERE id = ?`, id); err != nil {
			t.Fatal(err)
		}
		if v == nil {
			return ""
		}
		return *v
	}
	for _, step := range []struct{ raise, want string }{
		{"", ""},
		{"0490_breaking_a.sql", "0490_breaking_a.sql"},
		{"0488_breaking_old.sql", "0490_breaking_a.sql"}, // never lowered
		{"1000_breaking_b.sql", "1000_breaking_b.sql"},   // numeric, not string, order
		{"0999_breaking_zzz.sql", "1000_breaking_b.sql"},
	} {
		if err := st.RaiseTeamMinCode(ctx, id, step.raise); err != nil {
			t.Fatal(err)
		}
		if got := get(); got != step.want {
			t.Fatalf("after raise %q: %q, want %q", step.raise, got, step.want)
		}
	}
}
