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

// TestMarkerFabricDirectionGuard covers Ф1.5/Ф1.6 against the real schema: the direction lives on
// tech_card_bom_item (0073) and the scope a marker falls into is resolved through назначение (0267),
// so the two facts this guard runs on are exactly the two the database owns. The pure rule is
// unit-tested in entity; what is asserted here is that the right rows reach it — including the
// row INDEX in the field path, which is a fact about the card's BOM order and nothing else.
//
// Every case drives the blob through the REAL distiller (dto.MarkerLayoutFactsFromPb). Handing the
// store a blob string and a facts struct as independent arguments would prove that legacy FACTS keep
// saving while saying nothing about legacy BLOBS — and the gap between those two is exactly where a
// forged version would live.
//
// The case that matters most is the legacy one: a stored marker carrying a rotation outside today's
// policy must keep saving. Refusals are easy to get right; a silent retro-invalidation of every
// measurement on file is what this test exists to prevent.
func TestMarkerFabricDirectionGuard(t *testing.T) {
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

	ns := func(v string) sql.NullString { return sql.NullString{String: v, Valid: v != ""} }
	const (
		lineThread  = "01FDIRTHREAD000000000000D0" // NOT roll goods: sits at index 0 of the BOM
		lineUnknown = "01FDIRUNKNOWN00000000000D1" // направление не задано — the state of almost every line
		lineOneWay  = "01FDIRONEWAY000000000000D2"
		lineTwoWay  = "01FDIRTWOWAY000000000000D3"
		lineMainAny = "01FDIRMAINANY00000000000D4" // назначение main, article 1 — any
		lineMainNap = "01FDIRMAINNAP00000000000D5" // назначение main, article 2 — one_way
		lineLiningP = "01FDIRLININGPROD00000000D6" // назначение lining, ПРОИЗВОДСТВЕННАЯ — any
		lineLiningS = "01FDIRLININGSAMP00000000D7" // назначение lining, СЕМПЛОВАЯ — one_way
		lineCatalog = "01FDIRCATALOGUE000000000D8" // имя пустое, приходит из каталога
	)
	// display_order is left unset, so the card read (and the guard) order by id — insertion order.
	// The thread line is FIRST on purpose: it is not roll goods, so it can never be the offending
	// row, but it does shift every cloth row's index by one. An index taken over roll goods alone
	// would be off by exactly that, and would pin the refusal on the thread.
	bom := []entity.TechCardBomItem{
		{LineKey: lineThread, Section: entity.BomSectionThread, Name: "Нитки"},
		{LineKey: lineUnknown, Section: entity.BomSectionFabric, Name: "Твил без направления"},
		{LineKey: lineOneWay, Section: entity.BomSectionFabric, Name: "Вельвет", FabricDirection: ns("one_way")},
		{LineKey: lineTwoWay, Section: entity.BomSectionFabric, Name: "Джерси", FabricDirection: ns("two_way")},
		{LineKey: lineMainAny, Section: entity.BomSectionFabric, Name: "Основная гладкая",
			Purpose: ns("main"), FabricDirection: ns("any")},
		{LineKey: lineMainNap, Section: entity.BomSectionFabric, Name: "Основная ворсовая",
			Purpose: ns("main"), FabricDirection: ns("one_way")},
		// Одно назначение, две ткани: производственная и семпловая. Семпловая — физически другой
		// рулон, и её ворс не должен управлять производственной раскладкой (0265 завёл is_sample
		// флагом именно потому, что обе живут под одним назначением).
		{LineKey: lineLiningP, Section: entity.BomSectionLining, Name: "Подкладка",
			Purpose: ns("lining"), FabricDirection: ns("any")},
		{LineKey: lineLiningS, Section: entity.BomSectionLining, Name: "Подкладка семпловая",
			Purpose: ns("lining"), IsSample: true, FabricDirection: ns("one_way")},
		// Имя строки пустое — его показывает КАТАЛОГ. Отказ обязан говорить словами экрана.
		{LineKey: lineCatalog, Section: entity.BomSectionFabric, Name: "будет очищено"},
	}
	card := func(items []entity.TechCardBomItem) *entity.TechCardInsert {
		return &entity.TechCardInsert{
			Name: "Direction Style", Stage: entity.TechCardStageProto, StyleNumber: ns("FDIR-1"),
			MeasurementUnit: entity.TechCardUnitMm, ApprovalState: entity.TechCardApprovalDraft,
			SizeIds:  []int{szA},
			BomItems: items,
		}
	}
	tcID, err := T.AddTechCard(ctx, card(bom))
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = testDB.ExecContext(context.Background(), "DELETE FROM tech_card WHERE id = ?", tcID)
	})

	// The catalogue line is put into its real-world state directly: own name empty, material linked.
	// parseTechCardBomItems documents that a linked line legitimately carries no name of its own and
	// shows the catalogue's — the guard has to resolve it the same way the BOM tab does.
	res, err := testDB.ExecContext(ctx, "INSERT INTO material (name, section) VALUES ('ВЕЛЬВЕТ ИЗ КАТАЛОГА', 'fabric')")
	require.NoError(t, err)
	matID, err := res.LastInsertId()
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = testDB.ExecContext(context.Background(), "DELETE FROM material WHERE id = ?", matID)
	})
	_, err = testDB.ExecContext(ctx,
		"UPDATE tech_card_bom_item SET name = '', material_id = ? WHERE tech_card_id = ? AND line_key = ?",
		matID, tcID, lineCatalog)
	require.NoError(t, err)

	d := func(v string) decimal.Decimal { return decimal.RequireFromString(v) }
	const (
		legacy90    = `{"schemaVersion":1,"pieces":[],"placements":[{"pieceId":1,"rotDeg":90}]}`
		legacy180   = `{"schemaVersion":1,"pieces":[],"placements":[{"pieceId":1,"rotDeg":180}]}`
		legacy180v2 = `{"schemaVersion":2,"pieces":[],"placements":[{"pieceId":1,"rotDeg":180}]}`
		legacyFlip  = `{"schemaVersion":1,"pieces":[],"placements":[{"pieceId":1,"flipped":true}]}`
		v3half      = `{"schemaVersion":3,"pieces":[],"placements":[{"pieceId":1,"rotDeg":180}]}`
		v3flip      = `{"schemaVersion":3,"pieces":[],"placements":[{"pieceId":1,"flipped":true}]}`
		v3clean     = `{"schemaVersion":3,"pieces":[],"placements":[{"pieceId":1}]}`
		v3crossOnly = `{"schemaVersion":3,"pieces":[],"placements":[{"pieceId":1,"rotDeg":90}]}`
	)
	ins := func(t *testing.T, name, lineKey, layout string) entity.TechCardMarkerInsert {
		return entity.TechCardMarkerInsert{
			SizeId: szA, Name: name, Source: entity.MarkerSourceAuto,
			BomLineKey:    lineKey,
			FabricWidthCm: d("140"), GapCm: d("0.5"), EdgeMarginCm: d("1"),
			// The legacy trap in its original form: the editor saved a cross-grain rotation even
			// though cross-grain was NOT permitted. That combination is on file and must survive.
			AllowCrossGrain: false,
			Sets:            1, UsedLengthCm: d("120"),
			PlacedCount: 1, TotalCount: 1,
			Layout: layout, LayoutFacts: markerLayoutFacts(t, layout),
		}
	}

	t.Run("unknown direction blocks the save and pins the card-order row", func(t *testing.T) {
		var ve *entity.ValidationError
		_, err := T.SaveMarker(ctx, tcID, 0, ins(t, "без направления", lineUnknown, v3clean), "tester")
		require.ErrorAs(t, err, &ve)
		// Index 1, not 0: the thread line is first in the card's BOM and this refusal must point at
		// the row the client actually renders.
		require.Equal(t, "bom_items[1].fabric_direction", ve.Field)
		require.Equal(t, entity.ReasonFabricDirectionUnknown, ve.Reason)
		require.Equal(t, lineUnknown, ve.Conflicting)
		require.Contains(t, ve.HowToFix, "Твил без направления")
	})

	t.Run("an unlinked marker on the same card still saves", func(t *testing.T) {
		// No bom_line_key, no cloth to ask about. This was legal before Ф1 and stays legal — the
		// geometry is just as valid without an attribution.
		id, err := T.SaveMarker(ctx, tcID, 0, ins(t, "без привязки", "", v3half), "tester")
		require.NoError(t, err)
		require.Positive(t, id)
	})

	t.Run("legacy blobs keep saving on one_way cloth", func(t *testing.T) {
		// 90° is not this rule's business at any version — cross-grain belongs to allow_cross_grain.
		id, err := T.SaveMarker(ctx, tcID, 0, ins(t, "легаси 90°", lineOneWay, legacy90), "tester")
		require.NoError(t, err)
		// Re-saving in place is the operator's real action (Ф5 adjustment) and must not start failing.
		_, err = T.SaveMarker(ctx, tcID, id, ins(t, "легаси 90°", lineOneWay, legacy90), "editor")
		require.NoError(t, err)

		// A row that PREDATES the policy: written when 180° on ворс was still saveable. It cannot be
		// created through SaveMarker any more, which is the point — so history is staged the only way
		// history exists, as a row already in the table.
		_, err = testDB.ExecContext(ctx,
			"UPDATE tech_card_marker SET layout = ?, layout_schema_version = 1 WHERE id = ?", legacy180, id)
		require.NoError(t, err)
		// The current client declares v3 unconditionally, so this arrives as v3 over a stored v1 and
		// must still save: renaming or re-linking an old раскладка cannot require re-nesting it.
		_, err = T.SaveMarker(ctx, tcID, id, ins(t, "легаси 180° переименован", lineOneWay, v3half), "editor")
		require.NoError(t, err)
	})

	// THE HOLE. The exemption used to key on the version the PAYLOAD declares, and 180° is
	// expressible in every version — so a brand-new marker opted out of the entire policy by writing
	// `schemaVersion: 1`. Proven against this very container before the fix: it saved.
	t.Run("a NEW marker cannot buy the exemption by declaring an old version", func(t *testing.T) {
		for _, blob := range []string{legacy180, legacy180v2} {
			var ve *entity.ValidationError
			_, err := T.SaveMarker(ctx, tcID, 0, ins(t, "подделка версии", lineOneWay, blob), "tester")
			require.ErrorAs(t, err, &ve, "blob %s", blob)
			require.Equal(t, entity.ReasonFlipOnOneWay, ve.Reason)
		}
	})

	// СЕМПЛОВАЯ ЯРДАЖА — другой рулон. Under one назначение the sample line's ворс must not reach the
	// production раскладка, and the production line's «any» must not exempt the sample one.
	t.Run("sample yardage does not govern a production marker", func(t *testing.T) {
		_, err := T.SaveMarker(ctx, tcID, 0, ins(t, "подкладка 180°", lineLiningP, v3half), "tester")
		require.NoError(t, err)

		var ve *entity.ValidationError
		_, err = T.SaveMarker(ctx, tcID, 0, ins(t, "семпловая 180°", lineLiningS, v3half), "tester")
		require.ErrorAs(t, err, &ve)
		require.Equal(t, entity.ReasonFlipOnOneWay, ve.Reason)
		require.Equal(t, lineLiningS, ve.Conflicting)
	})

	// The refusal has to speak the vocabulary of the screen it sends the operator to: a
	// catalogue-linked line shows the CATALOGUE's name on the BOM tab and has none of its own.
	t.Run("a catalogue-linked line is named, not printed as a ULID", func(t *testing.T) {
		var ve *entity.ValidationError
		_, err := T.SaveMarker(ctx, tcID, 0, ins(t, "из каталога", lineCatalog, v3clean), "tester")
		require.ErrorAs(t, err, &ve)
		require.Equal(t, entity.ReasonFabricDirectionUnknown, ve.Reason)
		require.Contains(t, ve.HowToFix, "ВЕЛЬВЕТ ИЗ КАТАЛОГА")
	})

	// The half of the exemption that is NOT legitimate, driven end to end: `flipped` did not exist
	// before schema 3, so a v1 blob carrying one is a forgery — and it is the one shape that would
	// otherwise buy the policy exemption by declaring a smaller number.
	t.Run("a mirror declaring a legacy schema is refused", func(t *testing.T) {
		var ve *entity.ValidationError
		_, err := T.SaveMarker(ctx, tcID, 0, ins(t, "поддельная схема", lineOneWay, legacyFlip), "tester")
		require.ErrorAs(t, err, &ve)
		require.Equal(t, entity.ReasonFlipInLegacySchema, ve.Reason)
		require.Equal(t, "layout.placements", ve.Field)
	})

	t.Run("one_way refuses a new blob turned 180°", func(t *testing.T) {
		var ve *entity.ValidationError
		_, err := T.SaveMarker(ctx, tcID, 0, ins(t, "ворс 180°", lineOneWay, v3half), "tester")
		require.ErrorAs(t, err, &ve)
		require.Equal(t, "layout.placements", ve.Field)
		require.Equal(t, entity.ReasonFlipOnOneWay, ve.Reason)
		require.Equal(t, lineOneWay, ve.Conflicting)
		require.Contains(t, ve.HowToFix, "Вельвет", "the refusal names the cloth, not a ULID")
	})

	t.Run("one_way refuses a new blob with a mirrored placement", func(t *testing.T) {
		var ve *entity.ValidationError
		_, err := T.SaveMarker(ctx, tcID, 0, ins(t, "ворс зеркало", lineOneWay, v3flip), "tester")
		require.ErrorAs(t, err, &ve)
		require.Equal(t, entity.ReasonFlipOnOneWay, ve.Reason)
	})

	t.Run("one_way still allows cross-grain and upright placements", func(t *testing.T) {
		// 90° is allow_cross_grain's question, not this rule's — a v3 blob must not be refused for it.
		_, err := T.SaveMarker(ctx, tcID, 0, ins(t, "ворс поперёк", lineOneWay, v3crossOnly), "tester")
		require.NoError(t, err)
	})

	t.Run("two_way accepts the half-turn", func(t *testing.T) {
		_, err := T.SaveMarker(ctx, tcID, 0, ins(t, "джерси 180°", lineTwoWay, v3half), "tester")
		require.NoError(t, err)
	})

	// СТРОГОЕ ПОБЕЖДАЕТ. The marker names the smooth article, but its назначение also owns a
	// ворсовый one — and the same geometry gets cut on whichever the colourway pins.
	t.Run("strictest line of the назначение wins", func(t *testing.T) {
		var ve *entity.ValidationError
		_, err := T.SaveMarker(ctx, tcID, 0, ins(t, "main 180°", lineMainAny, v3half), "tester")
		require.ErrorAs(t, err, &ve)
		require.Equal(t, entity.ReasonFlipOnOneWay, ve.Reason)
		require.Equal(t, lineMainNap, ve.Conflicting, "the blocker is the ворсовая line, not the named one")
		require.Contains(t, ve.HowToFix, "назначение")
		// Without the forbidden placements the same binding saves — the scope restricts the
		// GEOMETRY, it does not blacklist the line.
		_, err = T.SaveMarker(ctx, tcID, 0, ins(t, "main прямой", lineMainAny, v3clean), "tester")
		require.NoError(t, err)
	})

	t.Run("an unknown sibling under the назначение blocks the sorted line too", func(t *testing.T) {
		// Clearing the ворсовая line's direction leaves назначение main half-answered, and half an
		// answer is a guess — the save stops until somebody sets it.
		stored, err := T.GetTechCardById(ctx, tcID)
		require.NoError(t, err)
		cleared := make([]entity.TechCardBomItem, len(bom))
		copy(cleared, bom)
		for i := range cleared {
			if cleared[i].LineKey == lineMainNap {
				cleared[i].FabricDirection = sql.NullString{}
			}
		}
		require.NoError(t, T.UpdateTechCard(ctx, tcID, card(cleared), stored.LockVersion))

		var ve *entity.ValidationError
		_, err = T.SaveMarker(ctx, tcID, 0, ins(t, "main без соседа", lineMainAny, v3clean), "tester")
		require.ErrorAs(t, err, &ve)
		require.Equal(t, entity.ReasonFabricDirectionUnknown, ve.Reason)
		// Index 5: the ворсовая line is the sixth row of this card's BOM.
		require.Equal(t, "bom_items[5].fabric_direction", ve.Field)
		require.Equal(t, lineMainNap, ve.Conflicting)
		require.Contains(t, ve.HowToFix, "Основная ворсовая")
	})
}
