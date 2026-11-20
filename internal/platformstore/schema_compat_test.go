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

	if _, err := st.DB().Exec(`INSERT INTO platform_migrations (id, applied_at) VALUES ('0009_zz_diverged.sql', NOW())`); err != nil {
		t.Fatal(err)
	}
	if again, err := platformstore.Open(ctx, d); err == nil {
		again.Close()
		t.Fatal("a diverged history must still refuse")
	}
	if _, err := st.DB().Exec(`DELETE FROM platform_migrations WHERE id = '0009_zz_diverged.sql'`); err != nil {
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
