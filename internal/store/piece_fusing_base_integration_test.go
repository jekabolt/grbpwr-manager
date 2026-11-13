package store

import (
	"context"
	"database/sql"
	"os"
	"testing"
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/rubenv/sql-migrate/sqlparse"
	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/require"
)

// TestMigration0468UpDownUp runs the key's migration under ANSI_QUOTES: the suite starts migrated,
// Down drops both columns, Up adds them back with their shapes, and a second Up is a no-op.
func TestMigration0468UpDownUp(t *testing.T) {
	requireGarmentLabelGeometryDB(t)
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
	cfg := *testCfg
	cfg.Automigrate = true
	st, err := NewForTest(ctx, cfg)
	require.NoError(t, err)
	defer st.Close()
	f, err := os.Open("sql/0468_tech_card_piece_fusing_base.sql")
	require.NoError(t, err)
	parsed, err := sqlparse.ParseMigration(f)
	require.NoError(t, f.Close())
	require.NoError(t, err)
	conn, err := testDB.Conn(ctx)
	require.NoError(t, err)
	defer conn.Close()
	_, err = conn.ExecContext(ctx, `SET SESSION sql_mode = CONCAT(@@SESSION.sql_mode, ',ANSI_QUOTES')`)
	require.NoError(t, err)
	run := func(name string, statements []string) {
		t.Helper()
		for i, statement := range statements {
			_, err := conn.ExecContext(ctx, statement)
			require.NoErrorf(t, err, "%s statement %d", name, i)
		}
	}
	// Whatever fails below, leave the schema migrated for the rest of the suite.
	t.Cleanup(func() {
		for _, statement := range parsed.UpStatements {
			_, _ = testDB.ExecContext(context.Background(), statement)
		}
	})
	columns := func() map[string]string {
		t.Helper()
		rows, err := conn.QueryContext(ctx, `
			SELECT COLUMN_NAME, CONCAT(COLUMN_TYPE, ' ', IS_NULLABLE, ' ', COALESCE(COLLATION_NAME, '-'))
			FROM information_schema.COLUMNS
			WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'tech_card_piece' AND COLUMN_NAME LIKE 'fusing\_base\_%'`)
		require.NoError(t, err)
		defer rows.Close()
		out := map[string]string{}
		for rows.Next() {
			var name, shape string
			require.NoError(t, rows.Scan(&name, &shape))
			out[name] = shape
		}
		require.NoError(t, rows.Err())
		return out
	}
	want := map[string]string{
		"fusing_base_size_id":   "int YES -",
		"fusing_base_signature": "varchar(64) YES utf8mb4_bin",
	}

	require.Equal(t, want, columns(), "the suite starts migrated")
	run("down", parsed.DownStatements)
	require.Empty(t, columns())
	run("down retry", parsed.DownStatements)
	run("up", parsed.UpStatements)
	require.Equal(t, want, columns())
	run("up retry", parsed.UpStatements)
	require.Equal(t, want, columns())
}

// TestPieceFusingBaseRoundTrip proves the staleness key of a piece's fusing markup (0468) on a real
// write and read: it is stored with markup and never without, an omitted key keeps the stored one,
// an explicit key or clear wins, and the key goes with its markup even when the write omitted it.
func TestPieceFusingBaseRoundTrip(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	cfg := *testCfg
	cfg.Automigrate = true
	s, err := NewForTest(ctx, cfg)
	require.NoError(t, err)
	defer s.Close()
	T := s.TechCards()
	ns := func(v string) sql.NullString { return sql.NullString{String: v, Valid: true} }
	d := decimal.RequireFromString
	zone := func() *entity.TechCardPieceFusingZone {
		return &entity.TechCardPieceFusingZone{Spline: &entity.TechCardPieceFusingSpline{Points: []entity.TechCardPieceFusingSplinePoint{
			{X: d("0"), Y: d("0"), Corner: true}, {X: d("20"), Y: d("0"), Corner: true},
			{X: d("20"), Y: d("10"), Corner: true}, {X: d("0"), Y: d("10"), Corner: true},
		}}}
	}
	withKey := func(p entity.TechCardPiece, sizeID int32, signature string) entity.TechCardPiece {
		p.FusingBaseSizeId = sql.NullInt32{Int32: sizeID, Valid: true}
		p.FusingBaseSignature = ns(signature)
		return p
	}
	zonePiece := func(key string, sizeID int32, signature string) entity.TechCardPiece {
		return withKey(entity.TechCardPiece{
			LineKey: key, Name: key, PiecesPerGarment: 1, Grainline: "lengthwise", Fused: true,
			FusingMode: ns(string(entity.PieceFusingModeZone)), FusingZone: zone(),
		}, sizeID, signature)
	}
	stripPiece := func(key string, sizeID int32, signature string) entity.TechCardPiece {
		return withKey(entity.TechCardPiece{
			LineKey: key, Name: key, PiecesPerGarment: 1, Grainline: "lengthwise", Fused: true,
			FusingMode:  ns(string(entity.PieceFusingModeStrip)),
			FusingEdges: []entity.TechCardPieceFusingEdge{{Edge: 0, StartMm: d("10"), EndMm: d("10")}},
		}, sizeID, signature)
	}
	full := withKey(entity.TechCardPiece{
		LineKey: "BARE", Name: "BARE", PiecesPerGarment: 1, Grainline: "lengthwise", Fused: true,
		FusingMode: ns(string(entity.PieceFusingModeFull)),
	}, 4, "k-bare")
	newOmitted := zonePiece("NEWOMIT", 4, "k-newomit")
	newOmitted.FusingZone, newOmitted.FusingZoneOmitted = nil, true

	card := &entity.TechCardInsert{
		Name: "Fusing Base Style", Stage: entity.TechCardStageProto, StyleNumber: ns("FBASE-1"),
		Purpose:         entity.TechCardPurposeSellable,
		MeasurementUnit: entity.TechCardUnitMm, ApprovalState: entity.TechCardApprovalDraft,
		Pieces: []entity.TechCardPiece{
			zonePiece("KEEP", 4, "k-keep"),
			zonePiece("SWAP", 4, "k-swap"),
			zonePiece("CLEAR", 4, "k-clear"),
			stripPiece("UNFUSE", 5, "k-unfuse"),
			zonePiece("FULL", 4, "k-full"),
			zonePiece("ZONEGONE", 4, "k-zonegone"),
			zonePiece("ZONEOMIT", 4, "k-zoneomit"),
			stripPiece("EDGESGONE", 5, "k-edgesgone"),
			full,
			newOmitted,
		},
	}
	tcID, err := T.AddTechCard(ctx, card)
	require.NoError(t, err)
	t.Cleanup(func() { _, _ = testDB.ExecContext(context.Background(), "DELETE FROM tech_card WHERE id = ?", tcID) })

	type key struct {
		SizeID    any
		Signature any
	}
	none := key{nil, nil}
	read := func() (*entity.TechCard, map[string]key) {
		t.Helper()
		got, err := T.GetTechCardById(ctx, tcID)
		require.NoError(t, err)
		keys := map[string]key{}
		for _, p := range got.Pieces {
			k := none
			if p.FusingBaseSizeId.Valid {
				k.SizeID = p.FusingBaseSizeId.Int32
			}
			if p.FusingBaseSignature.Valid {
				k.Signature = p.FusingBaseSignature.String
			}
			keys[p.LineKey] = k
		}
		return got, keys
	}

	stored, keys := read()
	for line, want := range map[string]key{
		"KEEP": {int32(4), "k-keep"}, "SWAP": {int32(4), "k-swap"}, "CLEAR": {int32(4), "k-clear"},
		"UNFUSE": {int32(5), "k-unfuse"}, "FULL": {int32(4), "k-full"}, "ZONEGONE": {int32(4), "k-zonegone"},
		"ZONEOMIT": {int32(4), "k-zoneomit"}, "EDGESGONE": {int32(5), "k-edgesgone"},
		"BARE":    none, // whole-piece fusing has no markup to key
		"NEWOMIT": none, // a new row with its zone omitted has no zone, so no key either
	} {
		require.Equal(t, want, keys[line], "after insert: %s", line)
	}

	// One save as an older client would send it: every key omitted (its values unknown, so zero),
	// except where the edit says otherwise.
	write := stored.TechCardInsert
	write.Pieces = append([]entity.TechCardPiece(nil), stored.Pieces...)
	for i := range write.Pieces {
		p := &write.Pieces[i]
		p.FusingBaseSizeId, p.FusingBaseSignature, p.FusingBaseOmitted = sql.NullInt32{}, sql.NullString{}, true
		switch p.LineKey {
		case "SWAP":
			*p = withKey(*p, 7, "k-swapped")
			p.FusingBaseOmitted = false
		case "CLEAR":
			p.FusingBaseOmitted = false
		case "UNFUSE":
			p.Fused, p.FusingMode, p.FusingEdges = false, sql.NullString{}, nil
		case "FULL":
			p.FusingMode, p.FusingZone = ns(string(entity.PieceFusingModeFull)), nil
		case "ZONEGONE":
			p.FusingZone = nil
		case "ZONEOMIT":
			p.FusingZone, p.FusingZoneOmitted = nil, true
		case "EDGESGONE":
			p.FusingEdges = nil
		}
	}
	require.NoError(t, T.UpdateTechCard(ctx, tcID, &write, stored.LockVersion))

	after, keys := read()
	for line, want := range map[string]key{
		"KEEP":      {int32(4), "k-keep"},     // omitted keeps the stored key
		"SWAP":      {int32(7), "k-swapped"},  // an explicit key replaces it
		"CLEAR":     none,                     // an explicit empty key clears it
		"UNFUSE":    none,                     // unfused: the markup and its key go, even omitted
		"FULL":      none,                     // whole piece: no edges or zone remain
		"ZONEGONE":  none,                     // the zone was cleared in the same save
		"ZONEOMIT":  {int32(4), "k-zoneomit"}, // the zone was omitted, so it and its key stay
		"EDGESGONE": none,                     // the strip edges were cleared in the same save
	} {
		require.Equal(t, want, keys[line], "after update: %s", line)
	}
	for _, p := range after.Pieces {
		if p.LineKey == "ZONEOMIT" {
			require.NotNil(t, p.FusingZone, "an omitted zone is kept")
		}
	}

	// The pair is never half-stored, whatever the path.
	var half int
	require.NoError(t, testDB.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM tech_card_piece
		WHERE tech_card_id = ? AND (fusing_base_size_id IS NULL) <> (fusing_base_signature IS NULL)`, tcID).Scan(&half))
	require.Zero(t, half)
}
