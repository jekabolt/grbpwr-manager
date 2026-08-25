package admin

import (
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/jekabolt/grbpwr-manager/internal/techcardarchive"
	pb_common "github.com/jekabolt/grbpwr-manager/proto/gen/common"
	"github.com/stretchr/testify/require"
	pbdecimal "google.golang.org/genproto/googleapis/type/decimal"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// ─────────────────────────────────────────────────────────────────────────────
// Ф2.3 — the OUTER half of card.json: style catalogue facts and measured piece areas.
//
// These live on the TechCard message rather than under TechCardInsert, which is the only reason
// they were ever a defect: the resolver's generic walk runs over the insert, so the two size FKs
// out here — model_wears_size_id (field 21) and piece_area_scopes[].areas[].size_id — were carried
// across bases WITH THE SOURCE'S NUMBERS.
//
// THE CASE THAT MATTERS IS THE ONE THE STORE CANNOT CATCH, and every test below is built on it.
// The store's guard asks only whether the id falls inside the imported card's size range
// (writeImportedStyleFacts / insertImportedPieceAreas → rng.Has). After the remap that range is
// made of TARGET ids, and both dictionaries are small integers, so a foreign id lands inside it far
// more often than not — and then the row imports SILENTLY UNDER THE WRONG SIZE. The fixture here
// therefore numbers the source base so that its ids collide with the target's: source 30 is «l»
// while target 30 is «s». An unremapped 30 passes every check downstream and means the wrong thing.
// ─────────────────────────────────────────────────────────────────────────────

// tcimpParsedAt is one fixed measurement moment. Fixed, because parsed_at is PROVENANCE: the test
// that proves it travels unchanged cannot use a clock that moves.
var tcimpParsedAt = time.Date(2026, 3, 12, 9, 30, 0, 0, time.UTC)

// tcimpCollidingArchive numbers the SOURCE base so that its size ids are ids of the TARGET base too,
// and mean different sizes there:
//
//	source 30 = "l"  → target 50
//	source 40 = "s"  → target 30
//
// The imported card's range is therefore {50, 30} — and the foreign id 30 is INSIDE it. That is the
// whole point: 30 written through unresolved is accepted by every guard between here and the
// database, and files the row under the target's "s".
func tcimpCollidingArchive() *tcimpArchive {
	a := tcimpNewArchive()
	a.manifest.IDMaps.Sizes = map[string]string{"30": "l", "40": "s"}
	a.insert.SizeIds = []int32{30, 40}
	return a
}

// tcimpAreaScope builds one measured scope with the conditions and provenance an export carries.
func tcimpAreaScope(scope string, areas ...*pb_common.TechCardPieceArea) *pb_common.TechCardPieceAreaScope {
	return &pb_common.TechCardPieceAreaScope{
		ScopeKey:        scope,
		Areas:           areas,
		Stale:           true,
		ContourLayer:    "14",
		SeamAllowanceMm: &pbdecimal.Decimal{Value: "10"},
		ParsedBy:        "constructor@source",
		ParsedAt:        timestamppb.New(tcimpParsedAt),
	}
}

// ────────────────────────────── 1. the defect itself ──────────────────────────────

// A size id of the SOURCE base that happens to be a valid id HERE must still be translated. This is
// the case the store's range check cannot see, and the only one where the failure is silent.
func TestResolveImportOuterSizesAreRemappedEvenWhenTheForeignIDIsInRange(t *testing.T) {
	s, _, _, _ := tcimpServer(t)
	a := tcimpCollidingArchive()
	a.outer = func(c *pb_common.TechCard) {
		c.ModelWearsSizeId = 30 // source "l"
		c.PieceAreaScopes = []*pb_common.TechCardPieceAreaScope{
			tcimpAreaScope("shell",
				&pb_common.TechCardPieceArea{
					PieceLineKey: "PIECE-FRONT", SizeId: 30, // source "l"
					AreaCm2: &pbdecimal.Decimal{Value: "1234.50"},
				},
				&pb_common.TechCardPieceArea{
					PieceLineKey: "PIECE-BACK", SizeId: 40, // source "s"
					AreaCm2: &pbdecimal.Decimal{Value: "980.25"},
				},
			),
		}
	}

	res, err := s.resolveTechCardImport(t.Context(), a.open(t))
	require.NoError(t, err)

	require.Equal(t, []int32{50, 30}, res.Insert.GetSizeIds(), "the card's own range is remapped, so 30 is a LOCAL id here")
	require.Empty(t, tcimpHoles(res, techcardarchive.ReasonSizeUnknown), "every size in this archive is in the target dictionary")

	// model_wears_size_id — field 21 of the OUTER message.
	require.True(t, res.StylePlan.ModelWearsSizeId.Valid, "the model-wears size resolved and must not read as unset")
	require.EqualValues(t, 50, res.StylePlan.ModelWearsSizeId.Int32,
		"source 30 is «l» and «l» is 50 here; 30 written through would be the target's «s» and pass every later check")

	// piece_area_scopes — field 27 of the OUTER message.
	require.Len(t, res.PieceAreaPlan, 2)
	byPiece := map[string]int64{}
	for _, row := range res.PieceAreaPlan {
		require.True(t, row.SizeId.Valid, "a graded area must not arrive as «does not grade»")
		byPiece[row.PieceLineKey] = row.SizeId.Int64
	}
	require.EqualValues(t, 50, byPiece["PIECE-FRONT"], "source 30 («l») must become 50, not stay 30 («s» here)")
	require.EqualValues(t, 30, byPiece["PIECE-BACK"], "source 40 («s») must become 30")
}

// ────────────────────────────── 2. a miss is a hole, never a silent zero ──────────────────────────────

// A model-wears size the target dictionary does not have lands as NULL — «not stated» — plus a line
// naming the size the operator would have to add. Never as the source's number, and never as 0 worn
// as a size.
func TestResolveImportModelWearsSizeMissIsAHoleAndNull(t *testing.T) {
	s, _, _, _ := tcimpServer(t)
	a := tcimpNewArchive()
	a.outer = func(c *pb_common.TechCard) { c.ModelWearsSizeId = 9 } // xxl, absent here

	res, err := s.resolveTechCardImport(t.Context(), a.open(t))
	require.NoError(t, err)

	require.False(t, res.StylePlan.ModelWearsSizeId.Valid,
		"an unplaceable size must clear the reference, not point at whichever local row shares the number")

	holes := tcimpHoles(res, techcardarchive.ReasonSizeUnknown)
	require.Len(t, holes, 1)
	require.Equal(t, techcardarchive.EntitySize, holes[0].Entity)
	require.Equal(t, "size_name=xxl", holes[0].Ref, "named by the dictionary entry to add, not by a number of somebody else's base")
	require.Equal(t, techcardarchive.StatusSkipped, holes[0].Status)
	require.Equal(t, 1, tcimpTally(t, res, techcardarchive.EntitySize).Skipped)

	// AND IT HAS TO REACH THE DRY RUN, which is the screen the operator decides on: the preview
	// builds its report from exactly these holes, so a hole recorded only on the commit would be a
	// preview that says «nothing to worry about» about a reference that is about to be dropped.
	rep := techcardarchive.BuildReport(techcardarchive.ReportInput{
		ImportID: "01J", StyleNumber: res.Insert.GetStyleNumber(), Stage: "draft",
		Counters: res.Counters, Holes: res.Holes,
	})
	var found bool
	for _, l := range rep.GetLines() {
		if l.GetReason() == string(techcardarchive.ReasonSizeUnknown) && l.GetRef() == "size_name=xxl" {
			found = true
		}
	}
	require.True(t, found, "the preview report must carry the line; an empty preview is the lie this feature exists to prevent")
}

// A measured area filed under a size this base does not have loses THAT ROW and nothing else. Not
// the scope, and above all not the size: NULL means «the piece does not grade and enters every
// size's set whole», so nulling it would multiply that contour into every size of the run.
func TestResolveImportPieceAreaSizeMissDropsTheRowNotTheScope(t *testing.T) {
	s, _, _, _ := tcimpServer(t)
	a := tcimpNewArchive()
	a.outer = func(c *pb_common.TechCard) {
		c.PieceAreaScopes = []*pb_common.TechCardPieceAreaScope{
			tcimpAreaScope("shell",
				&pb_common.TechCardPieceArea{PieceLineKey: "PIECE-FRONT", SizeId: 3, AreaCm2: &pbdecimal.Decimal{Value: "100"}},
				&pb_common.TechCardPieceArea{PieceLineKey: "PIECE-FRONT", SizeId: 9, AreaCm2: &pbdecimal.Decimal{Value: "120"}},
				&pb_common.TechCardPieceArea{PieceLineKey: "PIECE-TAPE", SizeId: 0, AreaCm2: &pbdecimal.Decimal{Value: "44"}},
			),
		}
	}

	res, err := s.resolveTechCardImport(t.Context(), a.open(t))
	require.NoError(t, err)

	require.Len(t, res.PieceAreaPlan, 2, "the unplaceable row goes; the rest of the scope imports")
	require.EqualValues(t, 30, res.PieceAreaPlan[0].SizeId.Int64)
	require.True(t, res.PieceAreaPlan[0].SizeId.Valid)
	require.Equal(t, "PIECE-TAPE", res.PieceAreaPlan[1].PieceLineKey)
	require.False(t, res.PieceAreaPlan[1].SizeId.Valid, "size 0 is «does not grade» and stays unset — it is not a miss and is never reported")

	for _, row := range res.PieceAreaPlan {
		require.NotEqualValues(t, 9, row.SizeId.Int64, "the source's number may not survive anywhere")
	}

	holes := tcimpHoles(res, techcardarchive.ReasonSizeUnknown)
	require.Len(t, holes, 1)
	require.Equal(t, "size_name=xxl", holes[0].Ref)
	require.Equal(t, 1, tcimpTally(t, res, techcardarchive.EntitySize).Skipped,
		"one missing size is ONE unresolved size, however many rows mention it")
}

// ────────────────────────────── 3. everything that is not an id travels as it stands ──────────────────────────────

// The catalogue facts are facts, not references: they cross verbatim, and «not stated» crosses as
// NULL rather than as an empty string the storefront would print.
func TestResolveImportStyleFactsTravelVerbatim(t *testing.T) {
	s, _, _, _ := tcimpServer(t)

	t.Run("stated", func(t *testing.T) {
		a := tcimpNewArchive()
		a.outer = func(c *pb_common.TechCard) {
			c.Fit = "oversized"
			c.Composition = "100% wool"
			c.CareInstructions = "W30,B0,I2"
			c.ModelWearsHeightCm = 184
			c.ModelWearsSizeId = 4
		}

		res, err := s.resolveTechCardImport(t.Context(), a.open(t))
		require.NoError(t, err)
		require.Equal(t, "oversized", res.StylePlan.Fit.String)
		require.Equal(t, "100% wool", res.StylePlan.Composition.String)
		require.Equal(t, "W30,B0,I2", res.StylePlan.CareInstructions.String)
		require.True(t, res.StylePlan.ModelWearsHeightCm.Valid)
		require.EqualValues(t, 184, res.StylePlan.ModelWearsHeightCm.Int32)
		require.EqualValues(t, 40, res.StylePlan.ModelWearsSizeId.Int32, "source 4 is «m», which is 40 here")
	})

	t.Run("unstated", func(t *testing.T) {
		a := tcimpNewArchive() // the outer message carries nothing at all

		res, err := s.resolveTechCardImport(t.Context(), a.open(t))
		require.NoError(t, err)
		require.False(t, res.StylePlan.Fit.Valid)
		require.False(t, res.StylePlan.Composition.Valid)
		require.False(t, res.StylePlan.CareInstructions.Valid)
		require.False(t, res.StylePlan.ModelWearsHeightCm.Valid, "0 cm is «unknown» on the wire and NULL in the column, never a height")
		require.False(t, res.StylePlan.ModelWearsSizeId.Valid)
		require.Empty(t, res.PieceAreaPlan, "a card with no measured areas plans none — and reports nothing, because nothing was lost")
		require.Empty(t, tcimpHoles(res, techcardarchive.ReasonCompositionNotDerived),
			"an archive that carries no fibre breakdown loses none, and a line about a loss that did "+
				"not happen is the same noise as a missing line about one that did")
	})
}

// The structured fibre breakdown (field 14) is the one thing on the outer message the import writes
// NOWHERE, and it used to go without a word.
//
// It is not written on purpose: style_composition's only writer re-derives the whole set from the
// card's own fabric lines against THIS catalogue on every save, so the archive's rows would state a
// breakdown of somebody else's catalogue as a fact about this base's BOM — and the imported card's
// first save would replace them in silence. The owner's rule allows the skip and forbids the
// silence, so the loss is a REPORTED one, in the dry run, where it is read before anybody commits.
//
// Two halves, and the second is the one that keeps this honest: the free-text composition (16) —
// which the store DOES write — must still travel, or «the breakdown is not imported» would have
// quietly become «the card says nothing about what it is made of».
func TestResolveImportReportsTheFibreBreakdownItDoesNotWrite(t *testing.T) {
	s, _, _, _ := tcimpServer(t)
	a := tcimpNewArchive()
	a.outer = func(c *pb_common.TechCard) {
		c.Composition = "80% wool, 20% pa"
		c.CompositionEntries = []*pb_common.CompositionEntry{
			{FiberCode: "WO", Name: "Wool", Percent: &pbdecimal.Decimal{Value: "80"}, Source: "auto"},
			{FiberCode: "PA", Name: "Polyamide", Percent: &pbdecimal.Decimal{Value: "20"}, Source: "auto"},
		}
	}

	res, err := s.resolveTechCardImport(t.Context(), a.open(t))
	require.NoError(t, err)

	holes := tcimpHoles(res, techcardarchive.ReasonCompositionNotDerived)
	require.Len(t, holes, 1, "the breakdown is lost once, as one fact of the card")
	h := holes[0]
	require.Equal(t, techcardarchive.EntityCard, h.Entity, "the CARD landed, one projection thinner")
	require.Equal(t, techcardarchive.StatusDegraded, h.Status)
	require.Equal(t, "composition_entries", h.Ref)
	// The detail is the only place on this side the archive's numbers exist at all, so it has to
	// carry them — a line saying «something was lost» is a line nobody can act on.
	require.Contains(t, h.Detail, "WO 80%")
	require.Contains(t, h.Detail, "PA 20%")
	require.NotEmpty(t, techcardarchive.ActionFor(h.Reason))

	// THE OTHER HALF. The legacy free-text composition is a different field with a different fate:
	// the store writes it, and it is what the card reads until somebody saves it here.
	require.Equal(t, "80% wool, 20% pa", res.StylePlan.Composition.String,
		"the free-text composition travels and IS written — without it the card would land silent "+
			"about its fibres, which is the loss this report line claims did NOT happen")
}

// A hostile archive can repeat composition_entries as many times as the card.json ceiling allows,
// and the detail is free text a human reads. It is bounded by construction rather than by the
// exporter's good manners — the same rule §6 states for every other channel of foreign prose.
func TestResolveImportCapsTheFibreBreakdownItSpellsOut(t *testing.T) {
	s, _, _, _ := tcimpServer(t)
	a := tcimpNewArchive()
	a.outer = func(c *pb_common.TechCard) {
		for i := 0; i < 400; i++ {
			c.CompositionEntries = append(c.CompositionEntries, &pb_common.CompositionEntry{
				FiberCode: "FIB", Percent: &pbdecimal.Decimal{Value: "0.25"},
			})
		}
	}

	res, err := s.resolveTechCardImport(t.Context(), a.open(t))
	require.NoError(t, err)

	holes := tcimpHoles(res, techcardarchive.ReasonCompositionNotDerived)
	require.Len(t, holes, 1)
	require.Less(t, len(holes[0].Detail), 400,
		"400 fibres may not become 400 fibres of report line: the detail is a sentence for a human")
	require.Contains(t, holes[0].Detail, "and 388 more",
		"a capped list has to say it was capped, or the report reads as a complete breakdown of twelve")
}

// THE CAP THAT MATTERS IS THE BYTE ONE, and the test above cannot see it: four hundred SHORT values
// prove only that the COUNT of spelled-out entries is bounded. `fiber_code` and `percent` are
// strings off somebody else's file and card.json may be 16 MiB, so TWELVE entries are enough to
// make a report line of megabytes — which is then written into the card's stored `report` column
// and returned by every read of the card afterwards.
//
// Twelve is exactly the number the producer spells out, so nothing here is saved by the entry cap:
// every one of these is inside it.
func TestResolveImportCapsTheFibreBreakdownInBytesNotEntries(t *testing.T) {
	s, _, _, _ := tcimpServer(t)
	a := tcimpNewArchive()
	const huge = 100_000
	a.outer = func(c *pb_common.TechCard) {
		for i := 0; i < 12; i++ {
			c.CompositionEntries = append(c.CompositionEntries, &pb_common.CompositionEntry{
				FiberCode: strings.Repeat("Ф", huge),
				Percent:   &pbdecimal.Decimal{Value: strings.Repeat("9", huge)},
			})
		}
	}

	res, err := s.resolveTechCardImport(t.Context(), a.open(t))
	require.NoError(t, err)

	holes := tcimpHoles(res, techcardarchive.ReasonCompositionNotDerived)
	require.Len(t, holes, 1)
	require.LessOrEqual(t, len(holes[0].Detail), techcardarchive.DetailLimit,
		"the detail is stored on the card and returned by every read of it: it is bounded in BYTES, "+
			"and twelve entries inside the entry cap were enough to blow past a bound that counts entries")
	require.True(t, utf8.ValidString(holes[0].Detail),
		"a clip landing inside a rune is invalid UTF-8, and protojson refuses to marshal the very "+
			"report the guard exists to keep small")

	// Cyrillic on purpose: a guard counting RUNES would let through twice the ceiling it names.
	require.Contains(t, holes[0].Detail, "Ф", "the fibre codes still reach the operator, clipped")
	require.NotContains(t, holes[0].Detail, strings.Repeat("9", 200),
		"one hostile value may cost its own slot and never the eleven others' room")
}

// The measurement's conditions and provenance are facts ABOUT THE MEASUREMENT and cross unchanged:
// re-stamping them with today's date and the importing operator's name would claim a measurement
// nobody took. The scope-level ones repeat onto every row, which is how the table stores them.
func TestResolveImportPieceAreaConditionsAndProvenanceTravelVerbatim(t *testing.T) {
	s, _, _, _ := tcimpServer(t)
	a := tcimpNewArchive()
	a.outer = func(c *pb_common.TechCard) {
		c.PieceAreaScopes = []*pb_common.TechCardPieceAreaScope{
			tcimpAreaScope("lining",
				&pb_common.TechCardPieceArea{
					PieceLineKey:  "PIECE-FRONT",
					SizeId:        4,
					AreaCm2:       &pbdecimal.Decimal{Value: "1234.50"},
					PerimeterCm:   &pbdecimal.Decimal{Value: "412.7"},
					Hulled:        true,
					AmbiguousPick: true,
				},
				// The measurement before 0305 carries no perimeter, and that is a permanent legal
				// state: it must arrive UNSET, so the edge-fusing estimate refuses instead of
				// inventing a strip width out of the area.
				&pb_common.TechCardPieceArea{
					PieceLineKey: "PIECE-BACK", SizeId: 4, AreaCm2: &pbdecimal.Decimal{Value: "900"},
				},
			),
		}
	}

	res, err := s.resolveTechCardImport(t.Context(), a.open(t))
	require.NoError(t, err)
	require.Len(t, res.PieceAreaPlan, 2)

	front := res.PieceAreaPlan[0]
	require.Equal(t, "lining", front.ScopeKey)
	require.Equal(t, "PIECE-FRONT", front.PieceLineKey)
	require.Equal(t, "1234.5", front.AreaCm2.String())
	require.Equal(t, "412.7", front.PerimeterCm.Decimal.String())
	require.True(t, front.PerimeterCm.Valid)
	require.Equal(t, "14", front.ContourLayer)
	require.Equal(t, "10", front.SeamAllowanceMm.String())
	require.True(t, front.Hulled)
	require.True(t, front.AmbiguousPick)
	require.Equal(t, "constructor@source", front.ParsedBy)
	require.True(t, tcimpParsedAt.Equal(front.ParsedAt), "the measurement's own date, not the import's")

	back := res.PieceAreaPlan[1]
	require.False(t, back.PerimeterCm.Valid, "«area measured, perimeter not» is a legal row and must stay legible as one")
	require.Equal(t, "10", back.SeamAllowanceMm.String(), "the conditions are per scope and repeat onto every row")
	require.True(t, tcimpParsedAt.Equal(back.ParsedAt))
}

// A scope with no measurement date at all: parsed_at lands in a TIMESTAMP NOT NULL column whose
// range starts one second after the Unix epoch, so the protobuf default (the epoch itself) is not
// storable and would fail the whole import with a bare 1292. The archive's own export time stands in
// — an upper bound on when the measurement was recorded — and when even that is missing the scope is
// dropped rather than stamped with a date nobody measured on.
func TestResolveImportPieceAreaWithoutAMeasurementDate(t *testing.T) {
	s, _, _, _ := tcimpServer(t)
	exported := time.Date(2026, 5, 1, 12, 0, 0, 0, time.UTC)

	undated := func(c *pb_common.TechCard) {
		c.PieceAreaScopes = []*pb_common.TechCardPieceAreaScope{{
			ScopeKey: "shell",
			Areas: []*pb_common.TechCardPieceArea{
				{PieceLineKey: "PIECE-FRONT", SizeId: 4, AreaCm2: &pbdecimal.Decimal{Value: "100"}},
			},
		}}
	}

	t.Run("falls back to the export time", func(t *testing.T) {
		a := tcimpNewArchive()
		a.manifest.ExportedAt = exported
		a.outer = undated

		res, err := s.resolveTechCardImport(t.Context(), a.open(t))
		require.NoError(t, err)
		require.Len(t, res.PieceAreaPlan, 1)
		require.True(t, exported.Equal(res.PieceAreaPlan[0].ParsedAt))
		require.EqualValues(t, 40, res.PieceAreaPlan[0].SizeId.Int64, "the size is still translated")
	})

	t.Run("dropped when there is no date anywhere", func(t *testing.T) {
		a := tcimpNewArchive()
		a.outer = undated

		res, err := s.resolveTechCardImport(t.Context(), a.open(t))
		require.NoError(t, err)
		require.Empty(t, res.PieceAreaPlan, "an unstorable timestamp must not reach the transaction")
	})
}

// ────────── 4. the money denylist covers the half this file made reachable ──────────

// READING THE OUTER MESSAGE IS WHAT MADE THIS REACHABLE. Until section 13 nobody looked at that half,
// so redacting only `insert` cost nothing; the moment the resolver reads it, a denylist that stops at
// the writable half is an import that redacts LESS than our own export does (buildArchiveCardJSON runs
// it over the whole message).
//
// And the outer half is where the money is: AdminColorwayRef carries cost_price / prices / net_prices,
// and its usages carry line_total / size_run_total. Our exporter nils that list whole
// (sanitizeCardForArchive) — which is exactly why the import may not rely on it. The manifest's
// money_policy is the archive's claim ABOUT ITSELF; a hand-made bundle types the flag and keeps the
// prices, and this is the check that flag is supposed to sit next to.
//
// SCANNED WITH THE EXPORT GATE'S OWN INSTRUMENT (amgProtoMoney) rather than by naming fields: a
// hand-listed assertion guards the names its author remembered, and the whole point of a denylist is
// the name nobody remembered.
func TestResolveImportRedactsMoneyOnTheOuterMessageToo(t *testing.T) {
	s, _, _, _ := tcimpServer(t)
	a := tcimpNewArchive()

	// The writable half's money, so this test also proves the widening did not LOSE the half that was
	// already covered: `costing` is a whole block and RedactFieldsDeep cuts it entire.
	a.insert.Costing = &pb_common.TechCardCosting{
		CmtCost:  &pbdecimal.Decimal{Value: "18.40"},
		Currency: "EUR",
	}
	// The outer half's money, on the message our exporter empties and a hand-made archive need not.
	a.outer = func(c *pb_common.TechCard) {
		c.Colorways = []*pb_common.AdminColorwayRef{{
			ColorwayId:         900,
			BaseSku:            "GRB-SS26-014-BLK",
			ColorCode:          "black",
			CostPrice:          &pbdecimal.Decimal{Value: "41.50"},
			CostPriceSource:    "production_run",
			CostPriceUpdatedAt: timestamppb.New(tcimpParsedAt),
			Prices: []*pb_common.ColorwayPrice{
				{Currency: "EUR", Price: &pbdecimal.Decimal{Value: "390"}},
			},
			NetPrices: []*pb_common.ColorwayPrice{
				{Currency: "EUR", Price: &pbdecimal.Decimal{Value: "319.67"}},
			},
			Usages: []*pb_common.TechCardColorwayUsage{{
				BomLineKey:   "B1",
				LineTotal:    &pbdecimal.Decimal{Value: "22.10"},
				SizeRunTotal: &pbdecimal.Decimal{Value: "1768.00"},
			}},
		}}
	}

	res, err := s.resolveTechCardImport(t.Context(), a.open(t))
	require.NoError(t, err)

	require.Empty(t, amgProtoMoney(res.Card.ProtoReflect(), "card"),
		"no money may survive anywhere in card.json — the outer half included, which is the half this resolver now reads")
	require.Nil(t, res.Insert.GetCosting(), "the writable half stays covered: one call over the parent covers both")

	// THE OTHER HALF OF THE PROOF: the scan has to be looking at something. A test that goes green
	// because the whole colourway vanished would guard nothing at all — `colorways` is not a money
	// name, so the row survives and only its figures go.
	require.Len(t, res.Card.GetColorways(), 1, "the colourway row itself is not money and must not be swept away with it")
	cw := res.Card.GetColorways()[0]
	require.Equal(t, "black", cw.GetColorCode())
	require.Len(t, cw.GetUsages(), 1, "the recipe line survives; only its totals go")
	require.Equal(t, "B1", cw.GetUsages()[0].GetBomLineKey())
}
