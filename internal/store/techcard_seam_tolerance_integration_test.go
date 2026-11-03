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

// A step's seam tolerance survives a save, keeps 0 apart from unset, and a later save carrying the
// read card back (what the client and every server path do) changes nothing.
func TestTechCardOperationSeamToleranceRoundTrip(t *testing.T) {
	if os.Getenv("CI") == "" &&
		!strings.Contains(testCfg.DSN, "127.0.0.1") &&
		!strings.Contains(testCfg.DSN, "localhost") {
		t.Skip("skipping outside CI unless the DSN targets a local container (avoids the configured prod DB)")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 180*time.Second)
	defer cancel()
	cfg := *testCfg
	cfg.Automigrate = true
	st, err := NewForTest(ctx, cfg)
	require.NoError(t, err)
	defer st.Close()
	T := st.TechCards()

	ns := func(v string) sql.NullString { return sql.NullString{String: v, Valid: true} }
	step := func(note string, tol decimal.NullDecimal) entity.TechCardOperation {
		return entity.TechCardOperation{OperationType: entity.OpTypeMachine, MachineType: ns("lockstitch"),
			Zone: entity.ZoneOuter, Note: ns(note), SeamToleranceMm: tol}
	}
	mm := func(v string) decimal.NullDecimal {
		return decimal.NullDecimal{Decimal: decimal.RequireFromString(v), Valid: true}
	}
	cardID, err := T.AddTechCard(ctx, &entity.TechCardInsert{
		Name: "SEAM TOL", Stage: entity.TechCardStageProto, StyleNumber: ns("TOL-1"),
		MeasurementUnit: entity.TechCardUnitMm, ApprovalState: entity.TechCardApprovalDraft,
		Operations: []entity.TechCardOperation{
			step("unset", decimal.NullDecimal{}), step("zero", mm("0")), step("two", mm("2.5")),
		},
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = T.DeleteTechCard(context.Background(), cardID) })

	check := func() *entity.TechCard {
		t.Helper()
		card, err := T.GetTechCardById(ctx, cardID)
		require.NoError(t, err)
		got := map[string]decimal.NullDecimal{}
		for _, op := range card.Operations {
			got[op.Note.String] = op.SeamToleranceMm
		}
		require.False(t, got["unset"].Valid)
		require.True(t, got["zero"].Valid)
		require.True(t, got["zero"].Decimal.IsZero())
		require.True(t, got["two"].Decimal.Equal(decimal.RequireFromString("2.5")))
		return card
	}
	card := check()
	edit := card.TechCardInsert
	edit.Operations = append([]entity.TechCardOperation(nil), card.Operations...)
	require.NoError(t, T.UpdateTechCard(ctx, cardID, &edit, card.LockVersion))
	check()
}
