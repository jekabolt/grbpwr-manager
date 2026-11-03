package store

import (
	"context"
	"database/sql"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/jekabolt/grbpwr-manager/internal/fusingops"
	"github.com/stretchr/testify/require"
)

// TestTechCardOperationLayerOrder drives tech_card_operation.layer_order (0445) through the real store:
// write and read, a save that keeps it, a save that clears it, the column staying NULL for a step with
// none, the season clone carrying it, and the FUSING rebuild not touching it on the steps it keeps.
//
// SAFE ONLY against a local container DSN or CI — see mysql_test.go.
func TestTechCardOperationLayerOrder(t *testing.T) {
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

	const (
		pieceBack   = "01OPLAYERPIECEBACK00000001"
		piecePocket = "01OPLAYERPIECEPOCKET000002"
		pieceCuff   = "01OPLAYERPIECECUFF00000003"
	)
	ns := func(v string) sql.NullString { return sql.NullString{String: v, Valid: true} }
	piece := func(key, name string, fused bool) entity.TechCardPiece {
		return entity.TechCardPiece{LineKey: key, Name: name, PiecesPerGarment: 1, Grainline: "lengthwise", Fused: fused}
	}
	step := func(note string, pieces ...string) entity.TechCardOperation {
		op := entity.TechCardOperation{OperationType: entity.OpTypeMachine, MachineType: ns("lockstitch"),
			Zone: entity.ZoneOuter, Note: ns(note)}
		for _, p := range pieces {
			op.AssemblyInputs = append(op.AssemblyInputs, entity.OperationInput{Kind: entity.AssemblyInputPiece, Key: p})
			op.InputKeys = append(op.InputKeys, p)
			op.PieceLineKeys = append(op.PieceLineKeys, p)
		}
		return op
	}
	ordered := step("ordered", pieceBack, piecePocket)
	ordered.LayerOrder = entity.TechCardLayerOrder{piecePocket, pieceBack}
	cardID, err := T.AddTechCard(ctx, &entity.TechCardInsert{
		Name: "OP LAYERS", Stage: entity.TechCardStageProto, StyleNumber: ns("OPL-1"),
		MeasurementUnit: entity.TechCardUnitMm, ApprovalState: entity.TechCardApprovalDraft,
		Pieces:     []entity.TechCardPiece{piece(pieceBack, "back", false), piece(piecePocket, "pocket", false)},
		Operations: []entity.TechCardOperation{ordered, step("plain", pieceBack)},
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = T.DeleteTechCard(context.Background(), cardID) })

	read := func(id int) (*entity.TechCard, map[string]entity.TechCardLayerOrder) {
		t.Helper()
		card, err := T.GetTechCardById(ctx, id)
		require.NoError(t, err)
		byNote := make(map[string]entity.TechCardLayerOrder, len(card.Operations))
		for _, op := range card.Operations {
			byNote[op.Note.String] = op.LayerOrder
		}
		return card, byNote
	}
	want := entity.TechCardLayerOrder{piecePocket, pieceBack}
	stored := func(note string) sql.NullString {
		t.Helper()
		var raw sql.NullString
		require.NoError(t, testDB.QueryRowContext(ctx, `
			SELECT layer_order FROM tech_card_operation WHERE tech_card_id = ? AND note = ?`, cardID, note).Scan(&raw))
		return raw
	}

	// ── write and read; a step with no order stays NULL, not an empty array ──
	card, orders := read(cardID)
	require.Equal(t, want, orders["ordered"])
	require.Empty(t, orders["plain"])
	require.True(t, stored("ordered").Valid)
	require.False(t, stored("plain").Valid, "no order is NULL")

	// ── a full replace that carries it keeps it, on the new operation rows ──
	edit := card.TechCardInsert
	edit.Operations = append([]entity.TechCardOperation(nil), card.Operations...)
	require.NoError(t, T.UpdateTechCard(ctx, cardID, &edit, card.LockVersion))
	card, orders = read(cardID)
	require.Equal(t, want, orders["ordered"])

	// ── a stale key is not the store's business: the inputs change, the order stays as written ──
	edit = card.TechCardInsert
	edit.Operations = append([]entity.TechCardOperation(nil), card.Operations...)
	for i := range edit.Operations {
		if edit.Operations[i].Note.String == "plain" {
			edit.Operations[i].LayerOrder = entity.TechCardLayerOrder{"NOT_AN_INPUT", pieceBack}
		}
	}
	require.NoError(t, T.UpdateTechCard(ctx, cardID, &edit, card.LockVersion))
	card, orders = read(cardID)
	require.Equal(t, entity.TechCardLayerOrder{"NOT_AN_INPUT", pieceBack}, orders["plain"])

	// ── the season clone carries it ──
	clone := card.TechCardInsert
	clone.StyleNumber = ns("OPL-2")
	clone.Operations = append([]entity.TechCardOperation(nil), card.Operations...)
	cloneID, err := T.CloneTechCardForSeason(ctx, cardID, card.LockVersion, &clone)
	require.NoError(t, err)
	t.Cleanup(func() { _ = T.DeleteTechCard(context.Background(), cloneID) })
	_, clonedOrders := read(cloneID)
	require.Equal(t, want, clonedOrders["ordered"])

	// ── the FUSING rebuild adds a step and leaves the order on the steps it keeps ──
	card, _ = read(cardID)
	edit = card.TechCardInsert
	edit.Pieces = append(append([]entity.TechCardPiece(nil), card.Pieces...), piece(pieceCuff, "cuff", true))
	edit.Operations = append([]entity.TechCardOperation(nil), card.Operations...)
	require.Nil(t, fusingops.ReconcileTechCard(&edit))
	require.NoError(t, T.UpdateTechCard(ctx, cardID, &edit, card.LockVersion))
	rebuilt, orders := read(cardID)
	require.Greater(t, len(rebuilt.Operations), 2, "the rebuild added the cuff's step")
	require.Equal(t, want, orders["ordered"])

	// ── a save that says nothing clears it: the client that knows the field meant that; the API
	// refuses a client that does not know it (operationLayerOrderStoredGate) ──
	edit = rebuilt.TechCardInsert
	edit.Operations = append([]entity.TechCardOperation(nil), rebuilt.Operations...)
	for i := range edit.Operations {
		edit.Operations[i].LayerOrder = nil
	}
	require.NoError(t, T.UpdateTechCard(ctx, cardID, &edit, rebuilt.LockVersion))
	_, orders = read(cardID)
	require.Empty(t, orders["ordered"])
	require.False(t, stored("ordered").Valid, "cleared is NULL again")
}

// TestTechCardOperationLayerOrderMigrationIsIdempotent re-runs 0445 under ANSI_QUOTES, the session mode
// of the cluster. SAFE ONLY against a local container DSN or CI.
func TestTechCardOperationLayerOrderMigrationIsIdempotent(t *testing.T) {
	if os.Getenv("CI") == "" &&
		!strings.Contains(testCfg.DSN, "127.0.0.1") &&
		!strings.Contains(testCfg.DSN, "localhost") {
		t.Skip("skipping outside CI unless the DSN targets a local container (avoids the configured prod DB)")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	conn, err := testDB.Conn(ctx)
	require.NoError(t, err)
	defer conn.Close()
	_, err = conn.ExecContext(ctx, "SET SESSION sql_mode = CONCAT(@@SESSION.sql_mode, ',ANSI_QUOTES')")
	require.NoError(t, err)
	up := migrationUpBody(t, "0445_tech_card_operation_layer_order.sql")
	for i := 0; i < 2; i++ {
		_, err = conn.ExecContext(ctx, up)
		require.NoError(t, err, "re-run %d", i+1)
	}
	var n int
	require.NoError(t, conn.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM information_schema.COLUMNS
		WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'tech_card_operation'
		  AND COLUMN_NAME = 'layer_order' AND IS_NULLABLE = 'YES'`).Scan(&n))
	require.Equal(t, 1, n)
}
