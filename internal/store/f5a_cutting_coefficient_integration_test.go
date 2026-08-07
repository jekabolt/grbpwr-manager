package store

import (
	"context"
	"database/sql"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/require"
)

// TestMaterialCuttingCoefficientOmittedIsNotCleared is the acceptance test for Ф5а.2's write
// presence rule against the real UPDATE statement: the coefficient column is written through
// IF(:cutting_coefficient_omitted, …), so a save that does not carry the field leaves the stored
// value alone, while a save that carries an explicitly empty one clears it.
//
// It has to be an integration test: the whole defect lives in the SQL. A unit test on the DTO can
// only prove the flag is computed, not that the statement honours it — and the failure mode is a
// silent erase of a number an operator typed, on a table with neither a signed digest nor an edit
// journal to notice.
//
// SAFE ONLY against a local container DSN — see the guard and mysql_test.go / project memory.
func TestMaterialCuttingCoefficientOmittedIsNotCleared(t *testing.T) {
	// Only run in CI (which points MYSQL_* at a container) or when the DSN explicitly targets a local
	// container. Otherwise skip — a bare local `go test ./internal/store/...` uses config.toml's prod
	// DSN, this test runs Automigrate and DELETEs rows, and this suite's TestMain drops all tables on
	// cleanup (see mysql_test.go / project memory).
	if os.Getenv("CI") == "" &&
		!strings.Contains(testCfg.DSN, "127.0.0.1") &&
		!strings.Contains(testCfg.DSN, "localhost") {
		t.Skip("skipping outside CI unless the DSN targets a local container (avoids the configured prod DB)")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	cfg := *testCfg
	cfg.Automigrate = true
	s, err := NewForTest(ctx, cfg)
	require.NoError(t, err)
	defer s.Close()
	tc := s.TechCards()

	ns := func(v string) sql.NullString { return sql.NullString{String: v, Valid: true} }
	nd := func(v string) decimal.NullDecimal {
		return decimal.NullDecimal{Decimal: decimal.RequireFromString(v), Valid: true}
	}
	base := func() *entity.MaterialInsert {
		return &entity.MaterialInsert{Name: "F5A Coefficient Fabric", Section: "fabric", Unit: ns("m")}
	}

	id, err := tc.CreateMaterial(ctx, base())
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = testDB.ExecContext(context.Background(), "DELETE FROM material WHERE id = ?", id)
	})

	// The operator sets the dial.
	ins := base()
	ins.CuttingCoefficient = nd("1.06")
	require.NoError(t, tc.UpdateMaterial(ctx, id, ins, 0))
	m, err := tc.GetMaterial(ctx, id)
	require.NoError(t, err)
	require.True(t, m.CuttingCoefficient.Valid && m.CuttingCoefficient.Decimal.Equal(decimal.RequireFromString("1.06")),
		"the coefficient is stored, got %v", m.CuttingCoefficient)

	// A save from a bundle that does not know the field — the stale tab, or any client in the window
	// between the backend and client deploys. It must not erase what the operator set.
	omitted := base()
	omitted.Name = "F5A Coefficient Fabric v2"
	omitted.CuttingCoefficientOmitted = true
	require.NoError(t, tc.UpdateMaterial(ctx, id, omitted, m.LockVersion))
	m, err = tc.GetMaterial(ctx, id)
	require.NoError(t, err)
	require.Equal(t, "F5A Coefficient Fabric v2", m.Name, "the rest of the save still lands")
	require.True(t, m.CuttingCoefficient.Valid && m.CuttingCoefficient.Decimal.Equal(decimal.RequireFromString("1.06")),
		"an ABSENT cutting_coefficient must not clear the stored one, got %v", m.CuttingCoefficient)

	// An explicit clear (the field present with an empty value) still works — otherwise the operator
	// could set a coefficient and never take it off again.
	cleared := base()
	cleared.CuttingCoefficient = decimal.NullDecimal{}
	require.NoError(t, tc.UpdateMaterial(ctx, id, cleared, m.LockVersion))
	m, err = tc.GetMaterial(ctx, id)
	require.NoError(t, err)
	require.False(t, m.CuttingCoefficient.Valid, "an explicit clear must store NULL, got %v", m.CuttingCoefficient)

	// And a create never consults the flag: absent and empty are the same NULL on INSERT.
	otherID, err := tc.CreateMaterial(ctx, func() *entity.MaterialInsert {
		i := base()
		i.Name = "F5A Coefficient Fabric fresh"
		i.CuttingCoefficientOmitted = true
		return i
	}())
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = testDB.ExecContext(context.Background(), "DELETE FROM material WHERE id = ?", otherID)
	})
	fresh, err := tc.GetMaterial(ctx, otherID)
	require.NoError(t, err)
	require.False(t, fresh.CuttingCoefficient.Valid, "a fresh material has no coefficient, got %v", fresh.CuttingCoefficient)
}
