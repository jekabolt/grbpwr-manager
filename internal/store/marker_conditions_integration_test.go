package store

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/require"
)

// УСЛОВИЯ СЪЁМКИ end to end (Ф3, migrations 0276/0277). What this covers, and why each part is here
// rather than trusted:
//
//	(a) the five conditions round-trip through the columns, INCLUDING the two distinctions that only
//	    a database can lose: a RECORDED zero vs an unrecorded allowance, and an EMPTY grain layer vs
//	    an absent one;
//	(b) the piece-set fingerprint is written by the SAVE, in its transaction, off the card's rows —
//	    never from the payload — and editing the card's pieces afterwards flips the status to CHANGED
//	    while an unrecorded fingerprint stays UNKNOWN;
//	(c) SetMarkerNorm clears the previous norm of the SAME cloth and leaves another cloth's alone;
//	    N designations in a row leave EXACTLY ONE flag standing;
//	(d) re-saving geometry neither seizes nor loses the norm — is_norm is not in the save's SET list;
//	(e) THE 1761 REGRESSION: deleting the BOM line a norm was measured against must NOT fail. This is
//	    the whole reason exclusivity is transactional rather than a UNIQUE index, and the failure it
//	    guards against lands on the save of the WHOLE CARD, not on the marker;
//	(f) a scope that somehow holds two norms is REPORTED (norm_conflict) and resolved identically by
//	    every reader — the card list and the single-marker read must name the same winner.
func TestTechCardMarkerConditions(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 180*time.Second)
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
	d := func(v string) decimal.Decimal { return decimal.RequireFromString(v) }
	nd := func(v string) decimal.NullDecimal {
		return decimal.NullDecimal{Decimal: decimal.RequireFromString(v), Valid: true}
	}

	main := entity.TechCardBomItem{
		LineKey: "01MRKCOND0000000000000MAIN", Section: entity.BomSectionFabric, Name: "Основная",
		FabricDirection: ns("any"),
	}
	lining := entity.TechCardBomItem{
		LineKey: "01MRKCOND0000000000000LINE", Section: entity.BomSectionLining, Name: "Подкладка",
		FabricDirection: ns("any"),
	}
	pieceFront := entity.TechCardPiece{LineKey: "01MRKCONDPIECE0000000FRNT", Name: "полочка", PiecesPerGarment: 2, Grainline: "lengthwise"}
	pieceBack := entity.TechCardPiece{LineKey: "01MRKCONDPIECE0000000BACK", Name: "спинка", PiecesPerGarment: 1, Grainline: "lengthwise"}

	card := func(pieces []entity.TechCardPiece) *entity.TechCardInsert {
		return &entity.TechCardInsert{
			Name: "Conditions Style", Stage: entity.TechCardStageProto, StyleNumber: ns("MCOND-1"),
			MeasurementUnit: entity.TechCardUnitMm, ApprovalState: entity.TechCardApprovalDraft,
			SizeIds:  []int{szA},
			BomItems: []entity.TechCardBomItem{main, lining},
			Pieces:   pieces,
			// Ф3.2's card-level override travels on the header and must survive the round trip.
			RequiredSeamAllowanceMm: nd("0"),
		}
	}
	tcID, err := T.AddTechCard(ctx, card([]entity.TechCardPiece{pieceFront, pieceBack}))
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = testDB.ExecContext(context.Background(), "DELETE FROM tech_card WHERE id = ?", tcID)
	})

	base := func(name, lineKey string) entity.TechCardMarkerInsert {
		m := entity.TechCardMarkerInsert{
			Name: name, Source: entity.MarkerSourceAuto, BomLineKey: lineKey,
			FabricWidthCm: d("140"), GapCm: d("0.5"), EdgeMarginCm: d("1"),
			UsedLengthCm: d("900"), PlacedCount: 8, TotalCount: 8,
			Layout: markerLayoutV1, LayoutFacts: markerLayoutFacts(t, markerLayoutV1),
		}
		markerSizing(&m, szA, 4)
		return m
	}
	markerByName := func(name string) entity.TechCardMarkerSummary {
		t.Helper()
		c, err := T.GetTechCardById(ctx, tcID)
		require.NoError(t, err)
		for _, m := range c.Markers {
			if m.Name == name {
				return m
			}
		}
		t.Fatalf("no marker %q on card %d", name, tcID)
		return entity.TechCardMarkerSummary{}
	}

	// --- (a) the conditions round-trip, with every distinction intact -----------------------------

	measured := base("замеренная", main.LineKey)
	measured.SeamAllowanceMm = nd("1")
	measured.ContourAllowanceMm = nd("0") // the file was measured: the laid contour IS the seam line
	measured.ContourLayer = sql.NullString{String: "14", Valid: true}
	measured.GrainLayer = sql.NullString{String: "", Valid: true} // «не разворачивать» — a DECISION
	measured.AllowFlip = sql.NullBool{Bool: false, Valid: true}
	measuredID, err := T.SaveMarker(ctx, tcID, 0, measured, "tester")
	require.NoError(t, err)

	got := markerByName("замеренная")
	require.Equal(t, "1", got.SeamAllowanceMm.Decimal.String())
	require.True(t, got.ContourAllowanceMm.Valid)
	require.True(t, got.ContourAllowanceMm.Decimal.IsZero(),
		"a RECORDED zero must come back as a value, not as «не записано»")
	require.Equal(t, "14", got.ContourLayer.String)
	require.True(t, got.GrainLayer.Valid, "an EMPTY grain layer is «не разворачивать»")
	require.Equal(t, "", got.GrainLayer.String)
	require.True(t, got.AllowFlip.Valid)
	require.False(t, got.AllowFlip.Bool)
	require.False(t, got.IsLegacyNorm(), "a раскладка that states its allowance is not «старая норма»")
	require.True(t, got.Allowance().Confirmed)
	require.Equal(t, "1", got.Allowance().Cm.String())

	// A stale bundle sends none of them; the row is stored and honestly becomes «старая норма».
	legacyID, err := T.SaveMarker(ctx, tcID, 0, base("старая", main.LineKey), "tester")
	require.NoError(t, err)
	legacy := markerByName("старая")
	require.False(t, legacy.SeamAllowanceMm.Valid)
	require.True(t, legacy.IsLegacyNorm())
	require.False(t, legacy.Allowance().Recorded)

	t.Run("the card's required allowance round-trips and a recorded zero is not «unset»", func(t *testing.T) {
		c, err := T.GetTechCardById(ctx, tcID)
		require.NoError(t, err)
		require.True(t, c.RequiredSeamAllowanceMm.Valid,
			"an explicit 0 is a REQUIREMENT («our выкройки carry the cut line»), not an absent one")
		require.True(t, c.RequiredSeamAllowanceMm.Decimal.IsZero())
		// And it takes precedence over the workshop default, which is what the gate will read.
		ws := decimal.NullDecimal{Decimal: decimal.RequireFromString("1"), Valid: true}
		require.True(t, entity.RequiredSeamAllowanceMm(c.RequiredSeamAllowanceMm, ws).Decimal.IsZero())
	})

	// --- (b) the fingerprint is the store's, and the comparison is against the card TODAY ----------

	t.Run("the fingerprint is computed server-side and matches at save time", func(t *testing.T) {
		require.True(t, got.PieceSetFp.Valid, "the save must record the card's piece set")
		require.Equal(t, entity.MarkerPieceSetMatches, got.PieceSetStatus())
		// It is the fingerprint of the card's OWN rows, not of anything the payload carried — the
		// insert has no wire field for it at all.
		want, ok := entity.PieceSetFingerprint([]entity.PieceSetEntry{
			{LineKey: pieceFront.LineKey, PiecesPerGarment: pieceFront.PiecesPerGarment},
			{LineKey: pieceBack.LineKey, PiecesPerGarment: pieceBack.PiecesPerGarment},
		})
		require.True(t, ok)
		require.Equal(t, want, got.PieceSetFp.String)
	})

	t.Run("an unrecorded fingerprint reads UNKNOWN, never CHANGED", func(t *testing.T) {
		// The state every раскладка taken before Ф3 is in. Reading it as CHANGED would badge the whole
		// stored estate at once — noise where a signal is needed.
		_, err := testDB.ExecContext(ctx,
			"UPDATE tech_card_marker SET piece_set_fp = NULL WHERE id = ?", legacyID)
		require.NoError(t, err)
		require.Equal(t, entity.MarkerPieceSetUnknown, markerByName("старая").PieceSetStatus())
	})

	t.Run("editing the card's pieces flips the status to CHANGED", func(t *testing.T) {
		lv, err := T.GetTechCardLockVersion(ctx, tcID)
		require.NoError(t, err)
		grown := card([]entity.TechCardPiece{pieceFront, pieceBack,
			{LineKey: "01MRKCONDPIECE0000000SLEV", Name: "рукав", PiecesPerGarment: 2, Grainline: "lengthwise"}})
		require.NoError(t, T.UpdateTechCard(ctx, tcID, grown, lv))
		require.Equal(t, entity.MarkerPieceSetChanged, markerByName("замеренная").PieceSetStatus(),
			"the раскладка was measured against a set the card no longer cuts")
		// The single-marker read must agree with the list — a verdict that depends on which RPC you
		// asked is a verdict nobody can act on.
		one, err := T.GetMarker(ctx, measuredID)
		require.NoError(t, err)
		require.Equal(t, entity.MarkerPieceSetChanged, one.PieceSetStatus())

		// …and restoring the set restores MATCHES: the fingerprint is over the SET, not over a version.
		lv, err = T.GetTechCardLockVersion(ctx, tcID)
		require.NoError(t, err)
		require.NoError(t, T.UpdateTechCard(ctx, tcID,
			card([]entity.TechCardPiece{pieceBack, pieceFront}), lv))
		require.Equal(t, entity.MarkerPieceSetMatches, markerByName("замеренная").PieceSetStatus(),
			"reordering and restoring the same set is not a change of set")
	})

	// --- (c)(d) the norm ---------------------------------------------------------------------------

	liningID, err := T.SaveMarker(ctx, tcID, 0, base("подкладочная", lining.LineKey), "tester")
	require.NoError(t, err)

	t.Run("designation is exclusive per CLOTH, not per card", func(t *testing.T) {
		prev, err := T.SetMarkerNorm(ctx, measuredID, true, "tester")
		require.NoError(t, err)
		require.Equal(t, 0, prev, "there was no previous norm on this cloth")
		require.True(t, markerByName("замеренная").IsNorm)

		// Another CLOTH's norm is its own: a garment is not cut until ALL its cloths are, and one norm
		// per card would let the lining's displace the main fabric's with the gate none the wiser.
		prev, err = T.SetMarkerNorm(ctx, liningID, true, "tester")
		require.NoError(t, err)
		require.Equal(t, 0, prev)
		require.True(t, markerByName("замеренная").IsNorm, "the main fabric keeps its norm")
		require.True(t, markerByName("подкладочная").IsNorm)

		// Designating a sibling of the SAME cloth takes the flag off the previous one and says which.
		prev, err = T.SetMarkerNorm(ctx, legacyID, true, "tester")
		require.NoError(t, err)
		require.Equal(t, measuredID, prev)
		require.False(t, markerByName("замеренная").IsNorm)
		require.True(t, markerByName("старая").IsNorm)
		require.True(t, markerByName("подкладочная").IsNorm, "another cloth is untouched")
	})

	t.Run("N designations leave exactly one flag standing", func(t *testing.T) {
		for _, id := range []int{measuredID, legacyID, measuredID, legacyID, measuredID} {
			_, err := T.SetMarkerNorm(ctx, id, true, "tester")
			require.NoError(t, err)
		}
		var n int
		require.NoError(t, testDB.QueryRowContext(ctx,
			`SELECT COUNT(*) FROM tech_card_marker WHERE tech_card_id = ? AND is_norm = TRUE
			   AND bom_item_id = (SELECT id FROM tech_card_bom_item WHERE tech_card_id = ? AND line_key = ?)`,
			tcID, tcID, main.LineKey).Scan(&n))
		require.Equal(t, 1, n, "exclusivity is held by the transaction; without it this is where it shows")
		require.True(t, markerByName("замеренная").IsNorm)
	})

	t.Run("clearing promotes nobody", func(t *testing.T) {
		prev, err := T.SetMarkerNorm(ctx, measuredID, false, "tester")
		require.NoError(t, err)
		require.Equal(t, 0, prev)
		require.False(t, markerByName("замеренная").IsNorm)
		require.False(t, markerByName("старая").IsNorm,
			"«this is no longer the norm» says nothing about who is; quietly promoting a neighbour would invent a decision")
		// Put it back for the tests below.
		_, err = T.SetMarkerNorm(ctx, measuredID, true, "tester")
		require.NoError(t, err)
	})

	t.Run("re-saving geometry neither seizes nor loses the norm", func(t *testing.T) {
		require.True(t, markerByName("замеренная").IsNorm)
		again := measured
		again.UsedLengthCm = d("880")
		_, err := T.SaveMarker(ctx, tcID, measuredID, again, "tester")
		require.NoError(t, err)
		require.True(t, markerByName("замеренная").IsNorm, "the save does not list is_norm")

		// …and a save of a NON-norm sibling cannot take it either.
		_, err = T.SaveMarker(ctx, tcID, legacyID, base("старая", main.LineKey), "tester")
		require.NoError(t, err)
		require.False(t, markerByName("старая").IsNorm)
		require.True(t, markerByName("замеренная").IsNorm)
	})

	t.Run("переезд нормы на ДРУГУЮ ткань снимает признак", func(t *testing.T) {
		// Эксклюзивность скоупится парой (карточка, bom_item_id) — «одна норма на ткань». Значит
		// сохранение, сменившее ткань, уносит признак В ЧУЖОЙ СКОУП, и обе развязки назначал бы не
		// человек, а случайность: у подкладки норма уже есть — станет две, и свежий updated_at
		// отдаст победу переехавшему; нормы там нет — подкладка приобретёт её молча, а основная
		// молча потеряет. Поэтому переезд СНИМАЕТ признак: назначить норму заново — одно осознанное
		// действие, а отобрать её у другой ткани молча нельзя ничем.
		//
		// Проверяется здесь, а не рассуждением, ещё и потому, что защита ЛЕГКО СТАНОВИТСЯ
		// ДЕКОРАТИВНОЙ: MySQL вычисляет список SET слева направо и видит уже обновлённые колонки,
		// так что то же самое присваивание ПОСЛЕ `bom_item_id = :bom_item_id` сравнивало бы новое
		// значение с самим собой — всегда истина, признак не снимался бы никогда, а тест на ту же
		// ткань (выше) продолжал бы проходить.
		require.True(t, markerByName("замеренная").IsNorm)
		moved := measured
		moved.BomLineKey = lining.LineKey
		_, err := T.SaveMarker(ctx, tcID, measuredID, moved, "tester")
		require.NoError(t, err)
		require.False(t, markerByName("замеренная").IsNorm,
			"признак уехал бы в скоуп подкладки, где его никто не назначал")

		// Вернуть как было — тесты ниже ждут норму на основной ткани.
		_, err = T.SaveMarker(ctx, tcID, measuredID, measured, "tester")
		require.NoError(t, err)
		require.False(t, markerByName("замеренная").IsNorm,
			"возврат на прежнюю ткань — тоже переезд: признак не воскресает сам")
		_, err = T.SetMarkerNorm(ctx, measuredID, true, "tester")
		require.NoError(t, err)
		require.True(t, markerByName("замеренная").IsNorm)
	})

	// --- (e) the 1761 regression -------------------------------------------------------------------

	t.Run("deleting the BOM line a norm was measured against does not fail the card save", func(t *testing.T) {
		// THIS is why exclusivity is transactional. With a UNIQUE index over the norm scope, fk_tcm_bom's
		// ON DELETE SET NULL would move this norm into the «no cloth» scope, collide with whatever lives
		// there, and MySQL would refuse the DELETE with ERROR 1761 — landing on UpdateTechCard, which
		// diffs the BOM, in an error naming neither a norm nor a раскладка.
		//
		// The unlinked marker below is what makes the collision reachable at all: without it the «no
		// cloth» scope is empty and the index would have passed.
		unlinkedID, err := T.SaveMarker(ctx, tcID, 0, base("несвязанная", ""), "tester")
		require.NoError(t, err)
		_, err = T.SetMarkerNorm(ctx, unlinkedID, true, "tester")
		require.NoError(t, err)
		require.True(t, markerByName("замеренная").IsNorm)
		require.True(t, markerByName("несвязанная").IsNorm)

		lv, err := T.GetTechCardLockVersion(ctx, tcID)
		require.NoError(t, err)
		trimmed := card([]entity.TechCardPiece{pieceBack, pieceFront})
		trimmed.BomItems = []entity.TechCardBomItem{lining} // the main fabric slot is deleted
		require.NoError(t, T.UpdateTechCard(ctx, tcID, trimmed, lv),
			"a norm must never be able to block the save of the card it belongs to")

		// The раскладка survives as valid geometry with a dangling attribution — the 0257 contract —
		// and it is now a second norm in the «no cloth» scope, which is the state (f) exercises.
		orphan := markerByName("замеренная")
		require.False(t, orphan.BomItemId.Valid)
		require.True(t, orphan.IsNorm)
	})

	// --- (f) two norms in one scope: reported, and resolved identically everywhere ------------------

	t.Run("a scope with two norms is reported and every reader picks the same winner", func(t *testing.T) {
		c, err := T.GetTechCardById(ctx, tcID)
		require.NoError(t, err)
		var flagged []entity.TechCardMarkerSummary
		for _, m := range c.Markers {
			if m.IsNorm && !m.BomItemId.Valid {
				flagged = append(flagged, m)
			}
		}
		require.Len(t, flagged, 2, "the SET NULL above left two norms in the «no cloth» scope")
		for _, m := range flagged {
			require.NotEmpty(t, m.NormConflict,
				"a state the schema cannot prevent must at least not be invisible")
		}
		winner, contenders, ok := entity.SelectNorm(entity.NormPeersOf(c.Markers), entity.NormScope{})
		require.True(t, ok)
		require.Len(t, contenders, 2)

		// The single-marker read resolves it the same way — the whole point of the tiebreak.
		for _, m := range flagged {
			one, err := T.GetMarker(ctx, m.Id)
			require.NoError(t, err)
			require.NotEmpty(t, one.NormConflict)
			require.Contains(t, one.NormConflict, winner.Name,
				"the list and the single read must name the same effective norm")
		}

		// Re-designating repairs the scope in one write: the clear takes ALL of them, not just the one
		// it reported.
		_, err = T.SetMarkerNorm(ctx, winner.Id, true, "tester")
		require.NoError(t, err)
		c, err = T.GetTechCardById(ctx, tcID)
		require.NoError(t, err)
		n := 0
		for _, m := range c.Markers {
			if m.IsNorm && !m.BomItemId.Valid {
				n++
				require.Empty(t, m.NormConflict)
			}
		}
		require.Equal(t, 1, n)
	})

	t.Run("deleting a marker takes its norm with it", func(t *testing.T) {
		require.True(t, markerByName("подкладочная").IsNorm)
		require.NoError(t, T.DeleteMarker(ctx, liningID))
		c, err := T.GetTechCardById(ctx, tcID)
		require.NoError(t, err)
		for _, m := range c.Markers {
			require.NotEqual(t, liningID, m.Id)
		}
	})
}

// The workshop's second tenant (Ф3.2). The one thing worth an integration test is the difference from
// the first: a stored ZERO must survive as a value, because it is a real setting («our выкройки carry
// the cut line») and not the absence of one.
func TestWorkshopDefaultSeamAllowance(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	cfg := *testCfg
	cfg.Automigrate = true
	s, err := NewForTest(ctx, cfg)
	require.NoError(t, err)
	defer s.Close()
	W := s.Workshop()

	before, err := W.GetSettings(ctx)
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = W.UpdateSettings(context.Background(), entity.WorkshopSettingsPatch{
			CuttingTableLengthCm:   &before.CuttingTableLengthCm,
			DefaultSeamAllowanceMm: &before.DefaultSeamAllowanceMm,
		}, "cleanup")
	})

	zero := decimal.NullDecimal{Decimal: decimal.Zero, Valid: true}
	out, err := W.UpdateSettings(ctx, entity.WorkshopSettingsPatch{DefaultSeamAllowanceMm: &zero}, "tester")
	require.NoError(t, err)
	require.True(t, out.DefaultSeamAllowanceMm.Valid, "a configured 0 is a SETTING, not «not configured»")
	require.True(t, out.DefaultSeamAllowanceMm.Decimal.IsZero())

	// An omitted setting carries the stored value forward — a workshop screen shipped before this
	// tenant landed must not be able to wipe it.
	table := decimal.NullDecimal{Decimal: decimal.RequireFromString("600"), Valid: true}
	out, err = W.UpdateSettings(ctx, entity.WorkshopSettingsPatch{CuttingTableLengthCm: &table}, "tester")
	require.NoError(t, err)
	require.True(t, out.DefaultSeamAllowanceMm.Valid)

	// Clearing is its own state, distinct from zero.
	cleared := decimal.NullDecimal{}
	out, err = W.UpdateSettings(ctx, entity.WorkshopSettingsPatch{DefaultSeamAllowanceMm: &cleared}, "tester")
	require.NoError(t, err)
	require.False(t, out.DefaultSeamAllowanceMm.Valid)

	// The plausibility band is refused in Go, with a field, before the CHECK answers 3819 with none.
	tooWide := decimal.NullDecimal{Decimal: decimal.RequireFromString("25"), Valid: true}
	_, err = W.UpdateSettings(ctx, entity.WorkshopSettingsPatch{DefaultSeamAllowanceMm: &tooWide}, "tester")
	require.Error(t, err)
	var ve *entity.ValidationError
	require.ErrorAs(t, err, &ve)
	require.Equal(t, "default_seam_allowance_mm", ve.Field)
}
