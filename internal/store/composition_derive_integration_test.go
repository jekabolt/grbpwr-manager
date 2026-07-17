package store

import (
	"context"
	"database/sql"
	"strings"
	"testing"
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/stretchr/testify/require"
)

// styleCompositionRow is the raw-SQL assertion shape for a persisted style_composition row.
type styleCompositionRow struct {
	FiberCode string
	Percent   string
	Source    string
}

func readStyleComposition(t *testing.T, ctx context.Context, techCardID int) []styleCompositionRow {
	t.Helper()
	rows, err := testDB.QueryContext(ctx,
		"SELECT fiber_code, percent, source FROM style_composition WHERE tech_card_id = ? ORDER BY fiber_code",
		techCardID)
	require.NoError(t, err)
	defer rows.Close()
	var out []styleCompositionRow
	for rows.Next() {
		var r styleCompositionRow
		require.NoError(t, rows.Scan(&r.FiberCode, &r.Percent, &r.Source))
		out = append(out, r)
	}
	require.NoError(t, rows.Err())
	return out
}

// TestReconcileStyleCompositionOnUpdateStyle is the acceptance test for P4-flyover M1 / acceptance
// flow C.11 (99-VERIFICATION.md, flow C step 11): a single shell-fabric BOM line's composition derives
// at 100%, two shell-fabric lines divide equally, and a manual override survives a re-save (auto never
// overwrites manual, S17 defect 2). Exercises the real wiring path — UpdateStyle calling
// product.ReconcileStyleCompositionTx — end to end against bom_item_composition, not the pure
// entity.DeriveStyleComposition unit tests alone.
func TestReconcileStyleCompositionOnUpdateStyle(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	cfg := *testCfg
	cfg.Automigrate = true
	s, err := NewForTest(ctx, cfg)
	require.NoError(t, err)
	defer s.Close()
	T := s.TechCards()
	P := s.Products()
	ns := func(v string) sql.NullString { return sql.NullString{String: v, Valid: true} }

	tcID, err := T.AddTechCard(ctx, &entity.TechCardInsert{
		Name: "Composition Style", Stage: entity.TechCardStageProto, StyleNumber: ns("C11-COMP-1"),
		MeasurementUnit: entity.TechCardUnitMm, ApprovalState: entity.TechCardApprovalDraft,
		BomItems: []entity.TechCardBomItem{
			{LineKey: "FAB1", Section: entity.BomSectionFabric, Name: "Main Fabric"},
			{LineKey: "FAB2", Section: entity.BomSectionFabric, Name: "Contrast Fabric"},
		},
	})
	require.NoError(t, err)
	t.Cleanup(func() {
		bg := context.Background()
		_, _ = testDB.ExecContext(bg, "DELETE FROM style_composition WHERE tech_card_id = ?", tcID)
		_, _ = testDB.ExecContext(bg, "DELETE FROM tech_card WHERE id = ?", tcID)
	})

	card, err := T.GetTechCardById(ctx, tcID)
	require.NoError(t, err)
	require.Len(t, card.BomItems, 2)
	var fab1ID, fab2ID int
	for _, b := range card.BomItems {
		switch b.LineKey {
		case "FAB1":
			fab1ID = b.Id
		case "FAB2":
			fab2ID = b.Id
		}
	}
	require.NotZero(t, fab1ID)
	require.NotZero(t, fab2ID)

	// tech_card carries CHECK constraints on target_gender (tech_card_chk_1) and the season triple
	// (tech_card_season_atomic) — NULL or a fully-set, valid combination, never a bare "" — so the
	// patch must supply valid values for both, not just the fields this test cares about.
	patch := entity.StylePatch{TopCategoryId: 1, Season: entity.SeasonEnum("SS"), TargetGender: entity.GenderEnum("unisex")}

	// --- Step 1: one shell-fabric line with a known composition -> derives at 100%. FAB2 has no
	// composition data anywhere yet, so it is excluded from the derive input rather than counted as a
	// defined-but-empty fabric (see product.ReconcileStyleCompositionTx).
	_, err = testDB.ExecContext(ctx,
		"INSERT INTO bom_item_composition (bom_item_id, fiber_code, percent) VALUES (?, 'COT', 100.00)", fab1ID)
	require.NoError(t, err)

	newVer, err := P.UpdateStyle(ctx, tcID, card.LockVersion, patch)
	require.NoError(t, err)

	rows := readStyleComposition(t, ctx, tcID)
	require.Len(t, rows, 1, "single fabric -> single fibre row")
	require.Equal(t, "COT", rows[0].FiberCode)
	require.Equal(t, "100.00", rows[0].Percent)
	require.Equal(t, entity.CompositionSourceAuto, rows[0].Source)

	// The tech-card read model surfaces the structured composition (falls back to legacy free-text
	// otherwise) — smoke-check the read wiring, not the exact JSON shape.
	cardAfter, err := T.GetTechCardById(ctx, tcID)
	require.NoError(t, err)
	require.True(t, cardAfter.Composition.Valid)
	require.Contains(t, cardAfter.Composition.String, "COT")

	// --- Step 2: a second shell-fabric line gets its own composition -> the style re-derives to an
	// equal 50/50 split on the next save (re-derived every save, not cached).
	_, err = testDB.ExecContext(ctx,
		"INSERT INTO bom_item_composition (bom_item_id, fiber_code, percent) VALUES (?, 'POL', 100.00)", fab2ID)
	require.NoError(t, err)

	newVer, err = P.UpdateStyle(ctx, tcID, newVer, patch)
	require.NoError(t, err)

	rows = readStyleComposition(t, ctx, tcID)
	require.Len(t, rows, 2, "two fabrics -> two fibre rows")
	byCode := map[string]styleCompositionRow{}
	for _, r := range rows {
		byCode[r.FiberCode] = r
		require.Equal(t, entity.CompositionSourceAuto, r.Source)
	}
	require.Equal(t, "50.00", byCode["COT"].Percent)
	require.Equal(t, "50.00", byCode["POL"].Percent)

	// --- Step 3: a manual override is never overwritten by the auto re-derive (S17 defect 2). Seeded
	// directly (no CRUD/RPC exists yet for authoring source=manual — 04-MAZE-FLYOVER.md M1 follow-up).
	_, err = testDB.ExecContext(ctx, "DELETE FROM style_composition WHERE tech_card_id = ?", tcID)
	require.NoError(t, err)
	_, err = testDB.ExecContext(ctx,
		"INSERT INTO style_composition (tech_card_id, fiber_code, percent, source) VALUES (?, 'SLK', 100.00, 'manual')", tcID)
	require.NoError(t, err)

	_, err = P.UpdateStyle(ctx, tcID, newVer, patch)
	require.NoError(t, err)

	rows = readStyleComposition(t, ctx, tcID)
	require.Len(t, rows, 1, "manual override is not replaced by the auto-derived set")
	require.Equal(t, "SLK", rows[0].FiberCode)
	require.Equal(t, "100.00", rows[0].Percent)
	require.Equal(t, entity.CompositionSourceManual, rows[0].Source)

	cardManual, err := T.GetTechCardById(ctx, tcID)
	require.NoError(t, err)
	require.True(t, cardManual.Composition.Valid)
	require.True(t, strings.Contains(cardManual.Composition.String, "SLK"))
}
