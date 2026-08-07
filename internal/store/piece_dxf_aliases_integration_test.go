package store

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/stretchr/testify/require"
)

// TestPieceDxfAliases covers the DXF block → cut-piece alias child (§2.2, fabric-scope amendment):
// round-trip with piece_line_key resolution, the fabric-scope collision freedom (same block name on
// two slots → two pieces), the CI collapse within a slot, presence semantics (absent set = stored
// aliases untouched; present-empty = clear), the grandfather rule for a dangling slot, piece
// deletion cascading its aliases, and the refusals (unknown piece, dead slot on a NEW pair).
func TestPieceDxfAliases(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	cfg := *testCfg
	cfg.Automigrate = true
	s, err := NewForTest(ctx, cfg)
	require.NoError(t, err)
	defer s.Close()
	T := s.TechCards()

	var szA int
	require.NoError(t, testDB.QueryRowContext(ctx, "SELECT MIN(id) FROM size").Scan(&szA))

	ns := func(v string) sql.NullString { return sql.NullString{String: v, Valid: true} }
	const (
		slotMain   = "01ALSFABRICMAIN00000000K01"
		slotLining = "01ALSFABRICLINING000000K02"
		pcFront    = "01ALSPIECEFRONT00000000P01"
		pcPocket   = "01ALSPIECEPOCKET0000000P02"
	)
	fabricMain := entity.TechCardBomItem{LineKey: slotMain, Section: entity.BomSectionFabric, Name: "Основная"}
	fabricLining := entity.TechCardBomItem{LineKey: slotLining, Section: entity.BomSectionFabric, Name: "Подклад"}
	pieces := func() []entity.TechCardPiece {
		return []entity.TechCardPiece{
			{LineKey: pcFront, Name: "перед", PiecesPerGarment: 1, Grainline: "lengthwise"},
			{LineKey: pcPocket, Name: "мешковина кармана", PiecesPerGarment: 2, Grainline: "lengthwise"},
		}
	}
	card := func(aliasSet bool, aliases []entity.TechCardPieceDxfAlias, items ...entity.TechCardBomItem) *entity.TechCardInsert {
		return &entity.TechCardInsert{
			Name: "Alias Style", Stage: entity.TechCardStageProto, StyleNumber: ns("ALS-1"),
			MeasurementUnit: entity.TechCardUnitMm, ApprovalState: entity.TechCardApprovalDraft,
			SizeIds:            []int{szA},
			BomItems:           items,
			Pieces:             pieces(),
			PieceDxfAliases:    aliases,
			PieceDxfAliasesSet: aliasSet,
		}
	}

	tcID, err := T.AddTechCard(ctx, card(false, nil, fabricMain, fabricLining))
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = testDB.ExecContext(context.Background(), "DELETE FROM tech_card WHERE id = ?", tcID)
	})
	lock := 0
	resave := func(aliasSet bool, aliases []entity.TechCardPieceDxfAlias, items ...entity.TechCardBomItem) error {
		err := T.UpdateTechCard(ctx, tcID, card(aliasSet, aliases, items...), lock)
		if err == nil {
			lock++
		}
		return err
	}
	read := func() []entity.TechCardPieceDxfAlias {
		c, err := T.GetTechCardById(ctx, tcID)
		require.NoError(t, err)
		return c.PieceDxfAliases
	}

	// Round-trip + fabric-scope freedom: the SAME generic block name on two slots maps to two
	// different pieces — the owner's collision case, impossible under a card-level unique.
	require.NoError(t, resave(true, []entity.TechCardPieceDxfAlias{
		{BomLineKey: slotMain, BlockName: "деталь_1", PieceLineKey: pcFront},
		{BomLineKey: slotLining, BlockName: "деталь_1", PieceLineKey: pcPocket},
	}, fabricMain, fabricLining))
	got := read()
	require.Len(t, got, 2)
	by := func(slot string) entity.TechCardPieceDxfAlias {
		for _, a := range got {
			if a.BomLineKey == slot {
				return a
			}
		}
		t.Fatalf("no alias for slot %s", slot)
		return entity.TechCardPieceDxfAlias{}
	}
	require.Equal(t, pcFront, by(slotMain).PieceLineKey)
	require.Equal(t, pcPocket, by(slotLining).PieceLineKey)

	// Legacy payload (set=false) leaves the mappings untouched.
	require.NoError(t, resave(false, nil, fabricMain, fabricLining))
	require.Len(t, read(), 2)

	// CI collapse within a slot: «ДЕТАЛЬ_1» addresses the stored «деталь_1» row (update, not 1062),
	// re-pointing it to another piece.
	require.NoError(t, resave(true, []entity.TechCardPieceDxfAlias{
		{BomLineKey: slotMain, BlockName: "ДЕТАЛЬ_1", PieceLineKey: pcPocket},
		{BomLineKey: slotLining, BlockName: "деталь_1", PieceLineKey: pcPocket},
	}, fabricMain, fabricLining))
	require.Equal(t, pcPocket, func() string {
		for _, a := range read() {
			if a.BomLineKey == slotMain {
				return a.PieceLineKey
			}
		}
		return ""
	}())

	// Unknown piece → field violation, nothing changed.
	err = resave(true, []entity.TechCardPieceDxfAlias{
		{BomLineKey: slotMain, BlockName: "x", PieceLineKey: "01ALSNOSUCHPIECE0000000P99"},
	}, fabricMain, fabricLining)
	require.Error(t, err)
	require.Len(t, read(), 2)

	// Dead slot: dropping the lining slot from the BOM keeps its existing alias readable and
	// re-savable (grandfathered), but a NEW pair on the dead slot is refused.
	require.NoError(t, resave(true, []entity.TechCardPieceDxfAlias{
		{BomLineKey: slotMain, BlockName: "ДЕТАЛЬ_1", PieceLineKey: pcPocket},
		{BomLineKey: slotLining, BlockName: "деталь_1", PieceLineKey: pcPocket},
	}, fabricMain))
	require.Len(t, read(), 2, "existing pair on a removed slot survives the save")
	err = resave(true, []entity.TechCardPieceDxfAlias{
		{BomLineKey: slotLining, BlockName: "новый блок", PieceLineKey: pcFront},
	}, fabricMain)
	require.Error(t, err, "a NEW pair may not bind a slot that is not on the BOM")

	// Present-empty clears everything.
	require.NoError(t, resave(true, nil, fabricMain, fabricLining))
	require.Empty(t, read())

	// Piece deletion cascades its aliases: map a block to the pocket piece, then re-save the card
	// without that piece (no usages hold it) — the alias disappears with it.
	require.NoError(t, resave(true, []entity.TechCardPieceDxfAlias{
		{BomLineKey: slotMain, BlockName: "карман", PieceLineKey: pcPocket},
	}, fabricMain, fabricLining))
	require.Len(t, read(), 1)
	one := card(false, nil, fabricMain, fabricLining)
	one.Pieces = one.Pieces[:1] // drop the pocket piece
	require.NoError(t, T.UpdateTechCard(ctx, tcID, one, lock))
	lock++
	require.Empty(t, read(), "FK CASCADE dropped the alias with its piece")
}
