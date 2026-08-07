package dto

import (
	"database/sql"
	"errors"
	"strings"
	"testing"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	pb_common "github.com/jekabolt/grbpwr-manager/proto/gen/common"
	"github.com/shopspring/decimal"
)

// ------------------------------------------------------------------ fixtures

// cm builds a plain decimal for the widths and lengths; `dec` in this package is already the pb
// helper and `nd` the NullDecimal one (both reused below).
func cm(s string) decimal.Decimal { return decimal.RequireFromString(s) }

// unsetDec is a NULL column — «не настроено» / «не замерено», never zero.
var unsetDec = decimal.NullDecimal{}

func nullInt(i int64) sql.NullInt64 { return sql.NullInt64{Int64: i, Valid: true} }

// noInt is a NULL foreign key: the SET NULL of fk_tcm_bom (0257:63) and fk_tcm_colorway (0264:45).
var noInt = sql.NullInt64{}

const (
	testRunID    = 77
	testCardID   = 12
	testColorway = 501
	testSlotID   = 909
)

// healthyLay is the identity every fitness test starts from: a настил of run 77, card 12, colourway
// 501, slot 909. Each test then breaks exactly one thing.
func healthyLay() LayIdentity {
	return LayIdentity{
		RunId:      testRunID,
		TechCardId: testCardID,
		ColorwayId: testColorway,
		BomItemId:  nullInt(testSlotID),
		BomLineKey: "01HZZSLOTMAIN0000000000001",
		Name:       "основная 40-42",
	}
}

// healthyMarker is a раскройный маркер of that same run, on that same slot, for that colourway.
func healthyMarker() LayMarkerFacts {
	return LayMarkerFacts{
		Id:            31,
		Name:          "основная 40-42",
		TechCardId:    testCardID,
		RunId:         testRunID,
		BomItemId:     nullInt(testSlotID),
		ColorwayId:    nullInt(testColorway),
		FabricWidthCm: cm("145"),
		UsedLengthCm:  cm("620"),
	}
}

func section(key string, plies int, m LayMarkerFacts) LayCheckSection {
	return LayCheckSection{SectionKey: key, Plies: plies, Marker: m}
}

// mirroredYield builds a parsed blob carrying ONE piece with the given chirality split, at the given
// schema version — the only three facts lay_mirror_expansion reads.
func mirroredYield(t *testing.T, schema int, asDrawn, mirrored int) *MarkerYield {
	t.Helper()
	l := &pb_common.TechCardMarkerLayout{
		SchemaVersion: int32(schema),
		Pieces:        []*pb_common.TechCardMarkerPiece{piece(1, "полочка", "PIECE_FRONT", 0, 1)},
		Placements:    placements(1, asDrawn, mirrored),
	}
	if schema >= 4 {
		l.Composition = comp([2]int32{10, 1})
	}
	y, err := MarkerYieldFromBlob(layoutBlob(t, l))
	if err != nil {
		t.Fatalf("fixture blob does not distil: %v", err)
	}
	return &y
}

// findCheck returns the check with that key, failing if it is missing — the aggregates promise a
// fixed set, and a silently absent check is a check that stopped running.
func findCheck(t *testing.T, checks []LayCheck, key string) LayCheck {
	t.Helper()
	for _, c := range checks {
		if c.Key == key {
			return c
		}
	}
	t.Fatalf("check %q not returned; got %d checks", key, len(checks))
	return LayCheck{}
}

// ------------------------------------------------------------ the ladder itself

// TestLayCheckLadderIsNotNumericOrder is the mutation guard for RULE ONE: the severity ladder
// (OK < WARNING < UNKNOWN < BLOCKER) is NOT the constants' numeric order (UNKNOWN=0 < OK=1), so
// folding with `max` answers OK for a pair containing an UNKNOWN — a silent approval of a check that
// never ran. Anybody who "simplifies" WorstLayCheckStatus into a max fails here.
func TestLayCheckLadderIsNotNumericOrder(t *testing.T) {
	if !(LayCheckStatusUnknown < LayCheckStatusOK) {
		t.Fatalf("the zero value must be UNKNOWN and therefore numerically below OK")
	}
	if got := WorstLayCheckStatus(LayCheckStatusOK, LayCheckStatusUnknown); got != LayCheckStatusUnknown {
		t.Errorf("OK folded with UNKNOWN = %v, want UNKNOWN (max() would answer OK here)", got)
	}
	if got := WorstLayCheckStatus(LayCheckStatusUnknown, LayCheckStatusBlocker); got != LayCheckStatusBlocker {
		t.Errorf("UNKNOWN folded with BLOCKER = %v, want BLOCKER", got)
	}
	if got := WorstLayCheckStatus(LayCheckStatusWarning, LayCheckStatusUnknown); got != LayCheckStatusUnknown {
		t.Errorf("WARNING folded with UNKNOWN = %v, want UNKNOWN: a known-and-legal fact must not "+
			"outrank «проверить было нечем»", got)
	}
	if got := WorstLayCheckStatus(LayCheckStatusOK, LayCheckStatusWarning); got != LayCheckStatusWarning {
		t.Errorf("OK folded with WARNING = %v, want WARNING", got)
	}
	if got := WorstLayCheckStatus(); got != LayCheckStatusUnknown {
		t.Errorf("folding nothing = %v, want UNKNOWN: an empty fold proved nothing", got)
	}
	// The fold must agree with the coverage ladder on the three values they share — one ladder, two
	// vocabularies. If WorstCoverageStatus ever changes, this catches the divergence.
	pairs := []struct {
		lay LayCheckStatus
		cov CoverageStatus
	}{
		{LayCheckStatusOK, CoverageStatusOK},
		{LayCheckStatusUnknown, CoverageStatusUnknown},
		{LayCheckStatusBlocker, CoverageStatusBlocker},
	}
	for _, a := range pairs {
		for _, b := range pairs {
			gotLay := WorstLayCheckStatus(a.lay, b.lay)
			gotCov := WorstCoverageStatus(a.cov, b.cov)
			if gotLay.coverageOf() != gotCov {
				t.Errorf("fold(%v,%v)=%v disagrees with coverage fold(%v,%v)=%v",
					a.lay, b.lay, gotLay, a.cov, b.cov, gotCov)
			}
		}
	}
	if LayCheckStatusWarning.Blocks() || LayCheckStatusUnknown.Blocks() || LayCheckStatusOK.Blocks() {
		t.Errorf("only BLOCKER may block")
	}
	if !LayCheckStatusBlocker.Blocks() {
		t.Errorf("BLOCKER must block")
	}
}

// TestLayCheckStatusPbIsNotACast pins the wire projection. `pb_common.ProductionLayCheckStatus(s)` —
// the cast a handler would reach for — maps OK, WARNING and BLOCKER correctly and turns UNKNOWN into
// UNSPECIFIED, i.e. the client's own zero value, for the one status this whole file exists to make
// visible. This test fails the moment somebody replaces Pb() with that cast.
func TestLayCheckStatusPbIsNotACast(t *testing.T) {
	want := map[LayCheckStatus]pb_common.ProductionLayCheckStatus{
		LayCheckStatusUnknown: pb_common.ProductionLayCheckStatus_PRODUCTION_LAY_CHECK_STATUS_UNKNOWN,
		LayCheckStatusOK:      pb_common.ProductionLayCheckStatus_PRODUCTION_LAY_CHECK_STATUS_OK,
		LayCheckStatusWarning: pb_common.ProductionLayCheckStatus_PRODUCTION_LAY_CHECK_STATUS_WARNING,
		LayCheckStatusBlocker: pb_common.ProductionLayCheckStatus_PRODUCTION_LAY_CHECK_STATUS_BLOCKER,
	}
	casts := 0
	for s, w := range want {
		if got := s.Pb(); got != w {
			t.Errorf("%v.Pb() = %v, want %v", s, got, w)
		}
		if pb_common.ProductionLayCheckStatus(s) == w {
			casts++
		}
	}
	if casts == len(want) {
		t.Errorf("a plain cast would agree on every value — the numbering no longer diverges, so " +
			"this guard has stopped guarding anything")
	}
	if got := LayCheckStatus(99).Pb(); got != pb_common.ProductionLayCheckStatus_PRODUCTION_LAY_CHECK_STATUS_UNSPECIFIED {
		t.Errorf("an unreadable status = %v, want UNSPECIFIED (never OK)", got)
	}

	pb := LayCheck{Key: "k", Status: LayCheckStatusUnknown, Label: "l", Detail: "d", MarkerId: 7, PieceLineKey: "P"}.Pb()
	if pb.GetStatus() != pb_common.ProductionLayCheckStatus_PRODUCTION_LAY_CHECK_STATUS_UNKNOWN ||
		pb.GetKey() != "k" || pb.GetMarkerId() != 7 || pb.GetPieceLineKey() != "P" || pb.GetDetail() != "d" {
		t.Errorf("LayCheck.Pb() lost a field: %+v", pb)
	}
	if list := LayChecksPb([]LayCheck{{Key: "a"}, {Key: "b"}}); len(list) != 2 || list[0].GetKey() != "a" {
		t.Errorf("LayChecksPb did not preserve the list or its order: %+v", list)
	}
}

// ------------------------------------------------------- §13 п.11 — чётность слоёв

// TestModeParityRefusesOddPliesFaceToFace is acceptance probe §13 п.11 in both directions: seven
// plies face to face never save, and a настил flipped INTO face-to-face saves only when EVERY
// section is even — the mode is a property of the настил, so every section is re-asked.
func TestModeParityRefusesOddPliesFaceToFace(t *testing.T) {
	m := healthyMarker()

	t.Run("plies = 7 face to face does not save", func(t *testing.T) {
		in := LayCheckInput{
			Lay: healthyLay(), Mode: LayFaceModeFaceToFace,
			Sections: []LayCheckSection{section("S1", 7, m)},
		}
		c := LayModeParityCheck(in.Mode, in.Sections)
		if c.Status != LayCheckStatusBlocker {
			t.Fatalf("status = %v, want BLOCKER", c.Status)
		}
		err := ValidateLayForSave(in)
		if err == nil {
			t.Fatalf("SaveLay would have accepted 7 plies face to face")
		}
		if !errors.Is(err, ErrLayModeParity) {
			t.Errorf("refusal does not carry ErrLayModeParity: %v", err)
		}
	})

	t.Run("the same 7 plies face UP are fine", func(t *testing.T) {
		in := LayCheckInput{
			Lay: healthyLay(), Mode: LayFaceModeFaceUp,
			Sections: []LayCheckSection{section("S1", 7, m)},
		}
		if c := LayModeParityCheck(in.Mode, in.Sections); c.Status != LayCheckStatusOK {
			t.Fatalf("status = %v (%s), want OK", c.Status, c.Detail)
		}
		if err := ValidateLayForSave(in); err != nil {
			t.Fatalf("face-up lay refused: %v", err)
		}
	})

	t.Run("saved lay turned to face-to-face: all sections even saves", func(t *testing.T) {
		in := LayCheckInput{
			Lay: healthyLay(), Mode: LayFaceModeFaceToFace,
			Sections: []LayCheckSection{section("S1", 20, m), section("S2", 6, m)},
		}
		if err := ValidateLayForSave(in); err != nil {
			t.Fatalf("all-even lay refused: %v", err)
		}
	})

	t.Run("saved lay turned to face-to-face: one odd section refuses, and NAMES it", func(t *testing.T) {
		in := LayCheckInput{
			Lay: healthyLay(), Mode: LayFaceModeFaceToFace,
			Sections: []LayCheckSection{section("S1", 20, m), section("S2", 5, m)},
		}
		err := ValidateLayForSave(in)
		if err == nil {
			t.Fatalf("a lay with an odd section was accepted into face-to-face")
		}
		if !strings.Contains(err.Error(), "S2") {
			t.Errorf("refusal does not name the offending section: %v", err)
		}
		if strings.Contains(err.Error(), "секция S1") {
			t.Errorf("refusal blames the even section too: %v", err)
		}
	})

	t.Run("a mode outside the dictionary blocks rather than passes", func(t *testing.T) {
		c := LayModeParityCheck(LayFaceMode("Face_Up"), []LayCheckSection{section("S1", 4, m)})
		if c.Status != LayCheckStatusBlocker {
			t.Errorf("status = %v, want BLOCKER: chk_prlay_mode is case-closed (0281), so a "+
				"mis-cased mode is a corrupt row, not a shrug", c.Status)
		}
	})
}

// -------------------------------------------- §13 п.13 — высота стопки, три ответа

// TestStackHeightNeverGuesses is acceptance probe §13 п.13 plus its sibling: an article with no
// measured thickness answers UNKNOWN AND WITHHOLDS THE NUMBER, and a workshop with no configured
// limit answers UNKNOWN too. «0 см, влезает» must be unreachable by any input.
func TestStackHeightNeverGuesses(t *testing.T) {
	const plies = 40

	t.Run("article without thickness: UNKNOWN and NO height at all", func(t *testing.T) {
		v := LayStackHeightVerdict(plies, unsetDec, nd("15"))
		if v.Status != LayCheckStatusUnknown {
			t.Fatalf("status = %v, want UNKNOWN", v.Status)
		}
		if v.HeightCm.Valid {
			t.Fatalf("stack_height_cm = %s was returned for an unmeasured article; §11 says it is "+
				"not returned AT ALL — a zero reads as «влезает»", v.HeightCm.Decimal.String())
		}
		if !strings.Contains(v.Detail, "толщина ткани не задана") {
			t.Errorf("detail does not tell the operator to measure: %q", v.Detail)
		}
		c := LayStackHeightCheck(plies, unsetDec, nd("15"))
		if c.Status != LayCheckStatusUnknown || c.Detail == "" {
			t.Errorf("check = %v / %q, want UNKNOWN with a detail", c.Status, c.Detail)
		}
	})

	t.Run("workshop limit not configured: UNKNOWN, and the height still shown", func(t *testing.T) {
		v := LayStackHeightVerdict(plies, nd("0.3"), unsetDec)
		if v.Status != LayCheckStatusUnknown {
			t.Fatalf("status = %v, want UNKNOWN: an unset limit is «не настроено», not «сколько угодно»", v.Status)
		}
		if v.LimitCm.Valid {
			t.Errorf("an unset limit came back as a number: %s", v.LimitCm.Decimal.String())
		}
		// The height is measurable here — thickness is known — and showing it is the point of
		// separating the two NULLs.
		if !v.HeightCm.Valid || v.HeightCm.Decimal.String() != "1.2" {
			t.Errorf("height = %v, want 1.2 cm (40 слоёв × 0.3 мм / 10)", v.HeightCm)
		}
		if !strings.Contains(v.Detail, "предел стопки не настроен") {
			t.Errorf("detail does not point at the workshop settings: %q", v.Detail)
		}
	})

	t.Run("neither configured: UNKNOWN naming both gaps", func(t *testing.T) {
		v := LayStackHeightVerdict(plies, unsetDec, unsetDec)
		if v.Status != LayCheckStatusUnknown || v.HeightCm.Valid || v.LimitCm.Valid {
			t.Fatalf("verdict = %+v, want UNKNOWN with neither number", v)
		}
		if !strings.Contains(v.Detail, "толщина") || !strings.Contains(v.Detail, "предел") {
			t.Errorf("detail names only one of the two gaps: %q", v.Detail)
		}
	})

	t.Run("both configured: OK and FAIL with numbers", func(t *testing.T) {
		ok := LayStackHeightVerdict(plies, nd("0.3"), nd("15"))
		if ok.Status != LayCheckStatusOK {
			t.Errorf("1.2 cm under a 15 cm limit = %v (%s), want OK", ok.Status, ok.Detail)
		}
		if ok.Detail != "" {
			t.Errorf("an OK verdict carries a detail: %q", ok.Detail)
		}
		fail := LayStackHeightVerdict(plies, nd("4"), nd("15"))
		if fail.Status != LayCheckStatusBlocker {
			t.Fatalf("16 cm over a 15 cm limit = %v, want BLOCKER", fail.Status)
		}
		if !fail.HeightCm.Valid || fail.HeightCm.Decimal.String() != "16" {
			t.Errorf("height = %v, want 16 cm (40 слоёв × 4 мм / 10)", fail.HeightCm)
		}
		for _, want := range []string{"16", "40", "4", "15"} {
			if !strings.Contains(fail.Detail, want) {
				t.Errorf("detail %q does not carry %s", fail.Detail, want)
			}
		}
		// Равно пределу — влезает: предел это потолок, а не строгое неравенство.
		if eq := LayStackHeightVerdict(plies, nd("3.75"), nd("15")); eq.Status != LayCheckStatusOK {
			t.Errorf("exactly at the limit (15 cm) = %v, want OK", eq.Status)
		}
	})

	t.Run("chiffon and drape at the same ply count land on opposite verdicts", func(t *testing.T) {
		// 00-model.md:44-46 — почему предел в САНТИМЕТРАХ, а не в слоях: одно и то же число слоёв
		// даёт разную стопку. Числа взяты по формуле §11 (подсказка диапазонов оттуда же: шифон
		// 0.1–0.2 мм, драп 1.5–2.5 мм), не по иллюстрации модельного документа — см. отчёт о
		// расхождении.
		if v := LayStackHeightVerdict(30, nd("0.15"), nd("5")); v.Status != LayCheckStatusOK {
			t.Errorf("30 слоёв шифона (0.45 см) = %v, want OK", v.Status)
		}
		if v := LayStackHeightVerdict(30, nd("2"), nd("5")); v.Status != LayCheckStatusBlocker {
			t.Errorf("30 слоёв драпа (6 см) = %v, want BLOCKER", v.Status)
		}
	})

	t.Run("a lay with no plies is UNKNOWN, not a zero-height stack that fits", func(t *testing.T) {
		v := LayStackHeightVerdict(0, nd("0.3"), nd("15"))
		if v.Status != LayCheckStatusUnknown {
			t.Fatalf("status = %v, want UNKNOWN", v.Status)
		}
		if v.HeightCm.Valid {
			t.Errorf("height %s returned for a lay with no plies", v.HeightCm.Decimal.String())
		}
	})

	t.Run("a stored non-positive limit is «не настроено», never «предел 0 см»", func(t *testing.T) {
		v := LayStackHeightVerdict(plies, nd("0.3"), nd("0"))
		if v.Status != LayCheckStatusUnknown || v.LimitCm.Valid {
			t.Errorf("verdict = %+v, want UNKNOWN with no limit", v)
		}
	})
}

// ------------------------------------------ §14 п.6 — маркер, потерявший слот, на ЧТЕНИИ

// TestDetachedMarkerIsCaughtOnRead is the trap of §14 п.6: fk_tcm_bom is SET NULL, so an edit to the
// CARD's BOM detaches a marker from its slot without touching the настил at all. The fitness verdict
// must therefore be recomputed on READ — a save-time-only check would leave the настил rendering
// green until somebody edited it, which may be never.
func TestDetachedMarkerIsCaughtOnRead(t *testing.T) {
	lay := healthyLay()
	detached := healthyMarker()
	detached.BomItemId = noInt // чужая правка BOM, ни одной записи в настил

	in := LayCheckInput{
		Lay: lay, Mode: LayFaceModeFaceUp,
		Sections: []LayCheckSection{section("S1", 20, detached)},
		Article:  LayArticleFacts{Name: "ВЕЛЬВЕТ", NominalUsableWidthCm: nd("150")},
	}

	// НА ЧТЕНИИ.
	c := findCheck(t, ProductionLaySectionChecks(in, in.Sections[0]), LayCheckKeyMarkerScope)
	if c.Status != LayCheckStatusBlocker {
		t.Fatalf("read path status = %v, want BLOCKER: a marker that lost its slot is not this lay's marker", c.Status)
	}
	if c.MarkerId != detached.Id {
		t.Errorf("finding does not name the marker (%d)", c.MarkerId)
	}
	if !strings.Contains(c.Detail, "потерял слот") {
		t.Errorf("detail does not say what happened: %q", c.Detail)
	}

	// И на записи тоже — один предикат, два места применения.
	if err := ValidateLayForSave(in); err == nil {
		t.Errorf("SaveLay would have accepted a section whose marker lost its slot")
	} else if !errors.Is(err, ErrLayMarkerScope) {
		t.Errorf("refusal does not carry ErrLayMarkerScope: %v", err)
	}

	t.Run("healthy marker passes both paths", func(t *testing.T) {
		ok := LayCheckInput{
			Lay: lay, Mode: LayFaceModeFaceUp,
			Sections: []LayCheckSection{section("S1", 20, healthyMarker())},
		}
		if c := LayMarkerScopeCheck(ok.Lay, ok.Sections[0].Marker); c.Status != LayCheckStatusOK {
			t.Fatalf("status = %v (%s), want OK", c.Status, c.Detail)
		}
		if err := ValidateLayForSave(ok); err != nil {
			t.Fatalf("healthy lay refused: %v", err)
		}
	})

	t.Run("the LAY's own lost slot is a fault, not a match against the marker's", func(t *testing.T) {
		broken := healthyLay()
		broken.BomItemId = noInt
		c := LayMarkerScopeCheck(broken, detached)
		if c.Status != LayCheckStatusBlocker {
			t.Fatalf("two NULL slots were read as an agreement: %v", c.Status)
		}
		if !strings.Contains(c.Detail, broken.BomLineKey) {
			t.Errorf("detail does not name the vanished slot: %q", c.Detail)
		}
		// И отдельная находка про сам настил.
		if d := LaySlotDetachedCheck(broken); d.Status != LayCheckStatusBlocker || d.Detail == "" {
			t.Errorf("lay_slot_detached = %v / %q, want BLOCKER with a detail", d.Status, d.Detail)
		}
		if d := LaySlotDetachedCheck(healthyLay()); d.Status != LayCheckStatusOK {
			t.Errorf("a live slot = %v, want OK", d.Status)
		}
	})

	t.Run("a card marker (run_id = 0) can never be a section", func(t *testing.T) {
		norm := healthyMarker()
		norm.RunId = 0
		c := LayMarkerScopeCheck(lay, norm)
		if c.Status != LayCheckStatusBlocker {
			t.Fatalf("status = %v, want BLOCKER (Р2: секция ссылается только на СВОЮ копию)", c.Status)
		}
		if !strings.Contains(c.Detail, "КАРТОЧНЫЙ") {
			t.Errorf("detail does not explain what is wrong: %q", c.Detail)
		}
	})

	t.Run("another run's marker, and another card's marker", func(t *testing.T) {
		other := healthyMarker()
		other.RunId = testRunID + 1
		if c := LayMarkerScopeCheck(lay, other); c.Status != LayCheckStatusBlocker {
			t.Errorf("another run's marker = %v, want BLOCKER", c.Status)
		}
		foreignCard := healthyMarker()
		foreignCard.TechCardId = testCardID + 1
		if c := LayMarkerScopeCheck(lay, foreignCard); c.Status != LayCheckStatusBlocker {
			t.Errorf("another card's marker = %v, want BLOCKER", c.Status)
		}
		otherSlot := healthyMarker()
		otherSlot.BomItemId = nullInt(testSlotID + 1)
		if c := LayMarkerScopeCheck(lay, otherSlot); c.Status != LayCheckStatusBlocker {
			t.Errorf("another slot's marker = %v, want BLOCKER", c.Status)
		}
	})
}

// ------------------------------------------- §14 п.7 — colorway_id = 0 значит «общая»

// TestMarkerWithoutColorwayReadsAsGeneral is the named trade of 0264:45 (§14 п.7). A marker that lost
// its colourway becomes ОБЩАЯ — usable by any colourway — and it must never be read as «this one».
// The distinction is invisible to an int comparison and decides which colour a раскладка is offered
// for, so it exists exactly once, in ColorwayBindingOf.
func TestMarkerWithoutColorwayReadsAsGeneral(t *testing.T) {
	cases := []struct {
		name       string
		marker     sql.NullInt64
		layColour  int
		want       MarkerColorwayBinding
		wantInLay  LayCheckStatus
		wantDetail string
	}{
		{"NULL = общая", noInt, testColorway, MarkerColorwayGeneral, LayCheckStatusOK, ""},
		{"0 = общая (SET NULL прочитан как ноль)", nullInt(0), testColorway, MarkerColorwayGeneral, LayCheckStatusOK, ""},
		{"тот же колорвей", nullInt(testColorway), testColorway, MarkerColorwayMatches, LayCheckStatusOK, ""},
		{"другой колорвей", nullInt(testColorway + 1), testColorway, MarkerColorwayForeign, LayCheckStatusBlocker, "колорвею"},
		// Ноль с ОБЕИХ сторон: production_run_lay.colorway_id is NOT NULL, so a zero here is an
		// unfilled input. Answering MATCHES would pair two absences and call the pair an agreement.
		{"общий маркер против незаполненного колорвея настила", nullInt(0), 0, MarkerColorwayGeneral, LayCheckStatusOK, ""},
		{"привязанный маркер против незаполненного колорвея настила", nullInt(9), 0, MarkerColorwayForeign, LayCheckStatusBlocker, "колорвею"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := ColorwayBindingOf(tc.marker, tc.layColour); got != tc.want {
				t.Fatalf("binding = %v, want %v", got, tc.want)
			}
			lay := healthyLay()
			lay.ColorwayId = tc.layColour
			m := healthyMarker()
			m.ColorwayId = tc.marker
			c := LayMarkerScopeCheck(lay, m)
			if c.Status != tc.wantInLay {
				t.Fatalf("lay_marker_scope = %v (%s), want %v", c.Status, c.Detail, tc.wantInLay)
			}
			if tc.wantDetail != "" && !strings.Contains(c.Detail, tc.wantDetail) {
				t.Errorf("detail %q does not mention %q", c.Detail, tc.wantDetail)
			}
		})
	}

	if MarkerColorwayBinding(0).String() == MarkerColorwayGeneral.String() {
		t.Errorf("the zero value must not be one of the three answers — nobody may get «общая» by forgetting to ask")
	}
}

// --------------------------------------- §8.1 — направление ткани против режима настилания

// TestDirectionModeUsesPhase1Resolver pins that lay_direction_mode делает ровно то, что обещает §8.1:
// scope through entity.MarkerFabricScope, fold through entity.ScopeFabricDirection with СТРОГОЕ
// ПОБЕЖДАЕТ, and one_way + face-to-face is the only refusal.
func TestDirectionModeUsesPhase1Resolver(t *testing.T) {
	const mainKey, secondKey, sampleKey = "LINE_MAIN", "LINE_MAIN_2", "LINE_SAMPLE"
	lines := func(dirs ...string) []entity.FabricDirectionLine {
		return []entity.FabricDirectionLine{
			{Index: 0, LineKey: mainKey, Purpose: "main", Name: "ВЕЛЬВЕТ", Direction: dirs[0]},
			{Index: 1, LineKey: secondKey, Purpose: "main", Name: "ВЕЛЬВЕТ-2", Direction: dirs[1]},
			{Index: 2, LineKey: sampleKey, Purpose: "main", Name: "ОБРАЗЕЦ", IsSample: true, Direction: dirs[2]},
		}
	}

	t.Run("two_way cloth: both modes fine", func(t *testing.T) {
		l := lines("two_way", "two_way", "one_way")
		for _, mode := range []LayFaceMode{LayFaceModeFaceUp, LayFaceModeFaceToFace} {
			c := LayDirectionModeCheck(mode, mainKey, l)
			if c.Status != LayCheckStatusOK {
				t.Errorf("mode %s = %v (%s), want OK — семпловая ярдажа в скоуп не входит (§8.1)",
					mode, c.Status, c.Detail)
			}
		}
	})

	t.Run("one_way cloth: face up fine, face to face refused", func(t *testing.T) {
		l := lines("one_way", "any", "any")
		if c := LayDirectionModeCheck(LayFaceModeFaceUp, mainKey, l); c.Status != LayCheckStatusOK {
			t.Errorf("face up on one_way = %v (%s), want OK", c.Status, c.Detail)
		}
		c := LayDirectionModeCheck(LayFaceModeFaceToFace, mainKey, l)
		if c.Status != LayCheckStatusBlocker {
			t.Fatalf("face to face on one_way = %v, want BLOCKER", c.Status)
		}
		if c.Detail == "" {
			t.Errorf("a blocker with no detail")
		}
	})

	t.Run("СТРОЖАЙШЕЕ ПОБЕЖДАЕТ по назначению: one_way на СОСЕДНЕЙ строке решает за скоуп", func(t *testing.T) {
		// The marker names LINE_MAIN, which is `any`; LINE_MAIN_2 shares the назначение and is
		// one_way. The лекала are cut from whichever article the colourway pins, so the strict one
		// governs. This is the rule Ф1 owns — a second implementation here would have answered OK.
		l := lines("any", "one_way", "any")
		if c := LayDirectionModeCheck(LayFaceModeFaceToFace, mainKey, l); c.Status != LayCheckStatusBlocker {
			t.Errorf("status = %v, want BLOCKER: строжайшее по назначению побеждает", c.Status)
		}
	})

	t.Run("направление NULL хоть на одной строке скоупа ⇒ UNKNOWN, не OK", func(t *testing.T) {
		l := lines("one_way", "", "any")
		c := LayDirectionModeCheck(LayFaceModeFaceUp, mainKey, l)
		if c.Status != LayCheckStatusUnknown {
			t.Fatalf("status = %v, want UNKNOWN: на бете направление NULL почти везде до кампании Д1", c.Status)
		}
		if !strings.Contains(c.Detail, secondKey) {
			t.Errorf("detail does not name the row to fix: %q", c.Detail)
		}
	})

	t.Run("семпловая строка судится своим скоупом", func(t *testing.T) {
		// The sample bolt is a different bolt: a one_way production line must not govern a sample
		// marker, and vice versa (entity.MarkerFabricScope states it in as many words).
		l := lines("one_way", "one_way", "any")
		if c := LayDirectionModeCheck(LayFaceModeFaceToFace, sampleKey, l); c.Status != LayCheckStatusOK {
			t.Errorf("sample scope = %v (%s), want OK", c.Status, c.Detail)
		}
	})

	t.Run("скоуп, который ничего не резолвит, — UNKNOWN, а НЕ «any ⇒ OK»", func(t *testing.T) {
		// The trap: ScopeFabricDirection over an EMPTY scope truthfully answers `any` — it folded
		// nothing — and a caller that passed that straight through would approve directional cloth on
		// evidence that does not exist. This is the state of a настил whose slot was deleted.
		l := lines("one_way", "one_way", "one_way")
		c := LayDirectionModeCheck(LayFaceModeFaceToFace, "LINE_GONE", l)
		if c.Status != LayCheckStatusUnknown {
			t.Fatalf("status = %v, want UNKNOWN for a dangling scope", c.Status)
		}
		if c2 := LayDirectionModeCheck(LayFaceModeFaceToFace, "", nil); c2.Status != LayCheckStatusUnknown {
			t.Errorf("empty key with no lines = %v, want UNKNOWN", c2.Status)
		}
	})
}

// ------------------------------------------------- §8 — развёртка зеркальных деталей

// TestMirrorExpansionIsThreeValued walks lay_mirror_expansion through its OK, FAIL and all three
// UNKNOWN doors (§8's table), because the UNKNOWNs are what stops this check from firing on every
// legacy marker in the portfolio at once.
func TestMirrorExpansionIsThreeValued(t *testing.T) {
	sym := map[string]sql.NullString{"PIECE_FRONT": marked(entity.PieceCutSymmetryMirrored)}
	markerWith := func(y *MarkerYield) LayMarkerFacts {
		m := healthyMarker()
		m.Yield = y
		return m
	}

	t.Run("face up: обе руки поровну ⇒ OK", func(t *testing.T) {
		c := LayMirrorExpansionCheck(LayFaceModeFaceUp, markerWith(mirroredYield(t, 4, 3, 3)), sym)
		if c.Status != LayCheckStatusOK || c.Detail != "" {
			t.Fatalf("status = %v / %q, want OK with no detail", c.Status, c.Detail)
		}
	})

	t.Run("face up: 44 левых и 0 правых ⇒ BLOCKER с числами", func(t *testing.T) {
		c := LayMirrorExpansionCheck(LayFaceModeFaceUp, markerWith(mirroredYield(t, 4, 44, 0)), sym)
		if c.Status != LayCheckStatusBlocker {
			t.Fatalf("status = %v, want BLOCKER", c.Status)
		}
		if !strings.Contains(c.Detail, "44") {
			t.Errorf("detail does not carry the count: %q", c.Detail)
		}
		if c.PieceLineKey != "PIECE_FRONT" {
			t.Errorf("a single faulty piece must be named on the finding, got %q", c.PieceLineKey)
		}
	})

	t.Run("face to face: отражённых нет ⇒ OK, есть ⇒ BLOCKER", func(t *testing.T) {
		if c := LayMirrorExpansionCheck(LayFaceModeFaceToFace, markerWith(mirroredYield(t, 4, 6, 0)), sym); c.Status != LayCheckStatusOK {
			t.Errorf("status = %v (%s), want OK", c.Status, c.Detail)
		}
		c := LayMirrorExpansionCheck(LayFaceModeFaceToFace, markerWith(mirroredYield(t, 4, 3, 3)), sym)
		if c.Status != LayCheckStatusBlocker {
			t.Fatalf("status = %v, want BLOCKER: маркер разложен под другой режим", c.Status)
		}
	})

	t.Run("UNKNOWN: схема < 3 не различает руки", func(t *testing.T) {
		c := LayMirrorExpansionCheck(LayFaceModeFaceUp, markerWith(mirroredYield(t, 2, 44, 0)), sym)
		if c.Status != LayCheckStatusUnknown {
			t.Fatalf("status = %v, want UNKNOWN: до схемы 3 «ноль правых» — отсутствие доказательства, "+
				"и BLOCKER здесь загорелся бы на КАЖДОМ легаси-маркере", c.Status)
		}
	})

	t.Run("UNKNOWN: cut_symmetry не размечено", func(t *testing.T) {
		c := LayMirrorExpansionCheck(LayFaceModeFaceUp, markerWith(mirroredYield(t, 4, 44, 0)),
			map[string]sql.NullString{"PIECE_FRONT": unmarked})
		if c.Status != LayCheckStatusUnknown {
			t.Fatalf("status = %v, want UNKNOWN (0275: NULL это «НЕ РАЗМЕЧЕНО», а не «обычная»)", c.Status)
		}
		// И полное отсутствие ключа — то же самое утверждение.
		if c2 := LayMirrorExpansionCheck(LayFaceModeFaceUp, markerWith(mirroredYield(t, 4, 44, 0)), nil); c2.Status != LayCheckStatusUnknown {
			t.Errorf("missing key = %v, want UNKNOWN", c2.Status)
		}
	})

	t.Run("UNKNOWN: деталь неатрибутируема (piece_line_key пуст)", func(t *testing.T) {
		l := &pb_common.TechCardMarkerLayout{
			SchemaVersion: 3,
			Pieces:        []*pb_common.TechCardMarkerPiece{piece(1, "полочка", "", 0, 1)},
			Placements:    placements(1, 44, 0),
		}
		y, err := MarkerYieldFromBlob(layoutBlob(t, l))
		if err != nil {
			t.Fatalf("fixture: %v", err)
		}
		if c := LayMirrorExpansionCheck(LayFaceModeFaceUp, markerWith(&y), sym); c.Status != LayCheckStatusUnknown {
			t.Errorf("status = %v, want UNKNOWN", c.Status)
		}
	})

	t.Run("UNKNOWN: блоб не прочитан вовсе", func(t *testing.T) {
		if c := LayMirrorExpansionCheck(LayFaceModeFaceUp, healthyMarker(), sym); c.Status != LayCheckStatusUnknown {
			t.Errorf("nil yield = %v, want UNKNOWN", c.Status)
		}
	})

	t.Run("identical и fold зеркальную пару не образуют ⇒ OK", func(t *testing.T) {
		for _, s := range []entity.TechCardPieceCutSymmetry{entity.PieceCutSymmetryIdentical, entity.PieceCutSymmetryFold} {
			c := LayMirrorExpansionCheck(LayFaceModeFaceUp, markerWith(mirroredYield(t, 4, 5, 1)),
				map[string]sql.NullString{"PIECE_FRONT": marked(s)})
			if c.Status != LayCheckStatusOK {
				t.Errorf("%s = %v (%s), want OK", s, c.Status, c.Detail)
			}
		}
	})

	t.Run("BLOCKER бьёт UNKNOWN, а не наоборот", func(t *testing.T) {
		// Один маркер, две детали: у одной доказанная негодность, у другой нечем судить.
		l := &pb_common.TechCardMarkerLayout{
			SchemaVersion: 4,
			Composition:   comp([2]int32{10, 1}),
			Pieces: []*pb_common.TechCardMarkerPiece{
				piece(1, "полочка", "PIECE_FRONT", 0, 1),
				piece(2, "рукав", "PIECE_SLEEVE", 0, 1),
			},
			Placements: concat(placements(1, 44, 0), placements(2, 2, 2)),
		}
		y, err := MarkerYieldFromBlob(layoutBlob(t, l))
		if err != nil {
			t.Fatalf("fixture: %v", err)
		}
		c := LayMirrorExpansionCheck(LayFaceModeFaceUp, markerWith(&y), map[string]sql.NullString{
			"PIECE_FRONT":  marked(entity.PieceCutSymmetryMirrored),
			"PIECE_SLEEVE": unmarked,
		})
		if c.Status != LayCheckStatusBlocker {
			t.Fatalf("status = %v, want BLOCKER: доказанная негодность не смягчается чужим UNKNOWN", c.Status)
		}
	})
}

// ------------------------------------------------------- §8.2 — перекрой как предупреждение

// TestOvercutIsAWarningWithTheSpecArithmetic reproduces §8.2's worked example number for number: a
// face-to-face настил of состав {M:1} with p plies cuts полочку exactly, карман with p to waste and
// спинку with p/2 to waste — and NONE of it is a refusal.
func TestOvercutIsAWarningWithTheSpecArithmetic(t *testing.T) {
	const p = 20
	covered := LayCoveredQty{Qty: p / 2, Known: true}

	front := LayPieceCut{ // mirrored, n=2, одно размещение на слой ⇒ p экземпляров
		PieceLineKey: "PIECE_FRONT", PieceName: "полочка",
		Cut: MarkerPieceCounts{AsDrawn: p / 2, Mirrored: p / 2}, Symmetry: marked(entity.PieceCutSymmetryMirrored),
		PiecesPerGarment: 2, ChiralityKnown: true,
	}
	pocket := LayPieceCut{ // identical, n=2, два размещения на слой ⇒ 2p экземпляров, p в отход
		PieceLineKey: "PIECE_POCKET", PieceName: "карман",
		Cut: MarkerPieceCounts{AsDrawn: p, Mirrored: p}, Symmetry: marked(entity.PieceCutSymmetryIdentical),
		PiecesPerGarment: 2, ChiralityKnown: true,
	}
	back := LayPieceCut{ // fold, n=1, одно размещение на слой ⇒ p экземпляров, годны все, нужно p/2
		PieceLineKey: "PIECE_BACK", PieceName: "спинка",
		Cut: MarkerPieceCounts{AsDrawn: p}, Symmetry: marked(entity.PieceCutSymmetryFold),
		PiecesPerGarment: 1, ChiralityKnown: true,
	}

	t.Run("полочка кроится ровно ⇒ OK", func(t *testing.T) {
		if c := LayOvercutCheck([]LayPieceCut{front}, covered); c.Status != LayCheckStatusOK || c.Detail != "" {
			t.Fatalf("status = %v / %q, want OK with no detail", c.Status, c.Detail)
		}
	})

	t.Run("карман: перекрой ровно p, и это ПРЕДУПРЕЖДЕНИЕ", func(t *testing.T) {
		c := LayOvercutCheck([]LayPieceCut{pocket}, covered)
		if c.Status != LayCheckStatusWarning {
			t.Fatalf("status = %v, want WARNING: перекрой законен (§8.2), запрет был бы догадкой о цехе", c.Status)
		}
		if c.Status.Blocks() {
			t.Fatalf("перекрой не имеет права блокировать прогон")
		}
		if !strings.Contains(c.Detail, "перекрой 20") {
			t.Errorf("detail does not carry the §8.2 number (p = %d): %q", p, c.Detail)
		}
		if c.PieceLineKey != "PIECE_POCKET" {
			t.Errorf("the single guilty piece must be named, got %q", c.PieceLineKey)
		}
	})

	t.Run("спинка: перекрой p/2", func(t *testing.T) {
		c := LayOvercutCheck([]LayPieceCut{back}, covered)
		if c.Status != LayCheckStatusWarning || !strings.Contains(c.Detail, "перекрой 10") {
			t.Fatalf("status = %v, detail = %q, want WARNING with перекрой 10", c.Status, c.Detail)
		}
	})

	t.Run("две детали с перекроем — обе названы, ни одна не выделена", func(t *testing.T) {
		c := LayOvercutCheck([]LayPieceCut{pocket, back}, covered)
		if c.Status != LayCheckStatusWarning {
			t.Fatalf("status = %v, want WARNING", c.Status)
		}
		if !strings.Contains(c.Detail, "карман") || !strings.Contains(c.Detail, "спинка") {
			t.Errorf("detail names only some of the pieces: %q", c.Detail)
		}
		if c.PieceLineKey != "" {
			t.Errorf("with two guilty pieces no single one may be highlighted, got %q", c.PieceLineKey)
		}
	})

	t.Run("деталь без cut_symmetry ⇒ UNKNOWN, и UNKNOWN бьёт предупреждение", func(t *testing.T) {
		unmarkedPiece := pocket
		unmarkedPiece.Symmetry = unmarked
		c := LayOvercutCheck([]LayPieceCut{unmarkedPiece}, covered)
		if c.Status != LayCheckStatusUnknown {
			t.Fatalf("status = %v, want UNKNOWN", c.Status)
		}
		mixed := LayOvercutCheck([]LayPieceCut{unmarkedPiece, back}, covered)
		if mixed.Status != LayCheckStatusUnknown {
			t.Errorf("status = %v, want UNKNOWN: «не смогли посчитать» не схлопывается в предупреждение", mixed.Status)
		}
		if !strings.Contains(mixed.Detail, "спинка") {
			t.Errorf("the known overcut disappeared from the detail: %q", mixed.Detail)
		}
	})

	t.Run("покрытие не определено ⇒ UNKNOWN, а не «всё выкроенное — перекрой»", func(t *testing.T) {
		c := LayOvercutCheck([]LayPieceCut{pocket}, LayCoveredQty{})
		if c.Status != LayCheckStatusUnknown {
			t.Fatalf("status = %v, want UNKNOWN", c.Status)
		}
	})

	t.Run("зеркальная деталь на легаси-блобе ⇒ UNKNOWN", func(t *testing.T) {
		legacy := front
		legacy.ChiralityKnown = false
		if c := LayOvercutCheck([]LayPieceCut{legacy}, covered); c.Status != LayCheckStatusUnknown {
			t.Errorf("status = %v, want UNKNOWN", c.Status)
		}
	})
}

// ------------------------------------------- остальные предикаты §8 и их UNKNOWN-двери

func TestTableLengthIsAWarningAndUnknownWhenUnset(t *testing.T) {
	short, long := healthyMarker(), healthyMarker()
	short.UsedLengthCm = cm("300")
	long.Id, long.Name, long.UsedLengthCm = 32, "основная 44-46", cm("910")
	secs := []LayCheckSection{section("S1", 10, short), section("S2", 10, long)}

	t.Run("стол не настроен ⇒ UNKNOWN", func(t *testing.T) {
		c := LayTableLengthCheck(secs, unsetDec)
		if c.Status != LayCheckStatusUnknown || c.Detail == "" {
			t.Fatalf("status = %v / %q, want UNKNOWN with a detail", c.Status, c.Detail)
		}
	})
	t.Run("влезает ⇒ OK", func(t *testing.T) {
		if c := LayTableLengthCheck(secs, nd("1000")); c.Status != LayCheckStatusOK {
			t.Fatalf("status = %v (%s), want OK", c.Status, c.Detail)
		}
	})
	t.Run("не влезает ⇒ WARNING на САМОМ ДЛИННОМ маркере, не отказ", func(t *testing.T) {
		c := LayTableLengthCheck(secs, nd("800"))
		if c.Status != LayCheckStatusWarning || c.Status.Blocks() {
			t.Fatalf("status = %v, want WARNING: настил длиннее стола просто делится на проходы", c.Status)
		}
		if c.MarkerId != long.Id {
			t.Errorf("finding blames marker %d, want the longest one (%d)", c.MarkerId, long.Id)
		}
	})
	t.Run("нет секций ⇒ UNKNOWN, не OK", func(t *testing.T) {
		if c := LayTableLengthCheck(nil, nd("800")); c.Status != LayCheckStatusUnknown {
			t.Errorf("status = %v, want UNKNOWN", c.Status)
		}
	})
}

func TestMarkerWidthDelegatesToPhase6Predicate(t *testing.T) {
	m := healthyMarker() // 145 см раскройной ширины

	t.Run("нет ни каталожной ширины, ни измеренного лота ⇒ UNKNOWN", func(t *testing.T) {
		c := LayMarkerWidthCheck(m, LayArticleFacts{Name: "ВЕЛЬВЕТ"})
		if c.Status != LayCheckStatusUnknown || c.Detail == "" {
			t.Fatalf("status = %v / %q, want UNKNOWN with a detail", c.Status, c.Detail)
		}
	})
	t.Run("каталожная ширина шире ⇒ OK", func(t *testing.T) {
		c := LayMarkerWidthCheck(m, LayArticleFacts{Name: "ВЕЛЬВЕТ", NominalUsableWidthCm: nd("150")})
		if c.Status != LayCheckStatusOK || c.Detail != "" {
			t.Fatalf("status = %v / %q, want OK with no detail", c.Status, c.Detail)
		}
	})
	t.Run("измеренный лот уже маркера ⇒ BLOCKER, и кромка вычтена один раз", func(t *testing.T) {
		// 148 пришло, кромка 2 см с каждого края ⇒ 144 раскройных < 145 маркера.
		c := LayMarkerWidthCheck(m, LayArticleFacts{
			Name: "ВЕЛЬВЕТ", NominalUsableWidthCm: nd("150"),
			NarrowestMeasuredLotCm: nd("148"), SelvedgeCm: cm("2"),
		})
		if c.Status != LayCheckStatusBlocker {
			t.Fatalf("status = %v (%s), want BLOCKER", c.Status, c.Detail)
		}
		if !strings.Contains(c.Detail, "144") {
			t.Errorf("detail does not show today's cutting width: %q", c.Detail)
		}
	})
}

func TestQuantitiesStaleIsAWarningAndOrderInsensitive(t *testing.T) {
	snap := []LayQtyEntry{{SizeId: 10, Qty: 20}, {SizeId: 11, Qty: 30}}

	t.Run("порядок записей значения не имеет", func(t *testing.T) {
		now := []LayQtyEntry{{SizeId: 11, Qty: 30}, {SizeId: 10, Qty: 20}}
		if c := LayQuantitiesStaleCheck(snap, now); c.Status != LayCheckStatusOK {
			t.Fatalf("status = %v (%s), want OK", c.Status, c.Detail)
		}
	})
	t.Run("нулевое количество и отсутствие размера — одно утверждение", func(t *testing.T) {
		c := LayQuantitiesStaleCheck(
			[]LayQtyEntry{{SizeId: 10, Qty: 20}, {SizeId: 12, Qty: 0}},
			[]LayQtyEntry{{SizeId: 10, Qty: 20}})
		if c.Status != LayCheckStatusOK {
			t.Fatalf("status = %v (%s), want OK", c.Status, c.Detail)
		}
	})
	t.Run("количества изменились ⇒ WARNING с числами", func(t *testing.T) {
		c := LayQuantitiesStaleCheck(snap, []LayQtyEntry{{SizeId: 10, Qty: 20}, {SizeId: 11, Qty: 45}})
		if c.Status != LayCheckStatusWarning || c.Status.Blocks() {
			t.Fatalf("status = %v, want WARNING", c.Status)
		}
		if !strings.Contains(c.Detail, "было 30, стало 45") {
			t.Errorf("detail does not carry the change: %q", c.Detail)
		}
	})
	t.Run("размер исчез из прогона", func(t *testing.T) {
		c := LayQuantitiesStaleCheck(snap, []LayQtyEntry{{SizeId: 10, Qty: 20}})
		if c.Status != LayCheckStatusWarning || !strings.Contains(c.Detail, "стало 0") {
			t.Fatalf("status = %v, detail = %q", c.Status, c.Detail)
		}
	})
}

// ------------------------------------------------------------------- сборки и инварианты

// TestAggregatesReturnTheWholeTableOfSection8 pins the SET of checks each aggregate returns. A check
// that quietly stops running is indistinguishable on screen from a check that passes.
func TestAggregatesReturnTheWholeTableOfSection8(t *testing.T) {
	in := LayCheckInput{
		Lay: healthyLay(), Mode: LayFaceModeFaceUp,
		Sections: []LayCheckSection{section("S1", 20, healthyMarker())},
	}
	layKeys := []string{
		LayCheckKeyModeParity, LayCheckKeyDirectionMode, LayCheckKeyStackHeight,
		LayCheckKeyTableLength, LayCheckKeySlotDetached, LayCheckKeyQuantitiesStale, LayCheckKeyOvercut,
	}
	got := ProductionLayChecks(in)
	if len(got) != len(layKeys) {
		t.Fatalf("lay-level checks = %d, want %d", len(got), len(layKeys))
	}
	for i, key := range layKeys {
		if got[i].Key != key {
			t.Errorf("check %d = %q, want %q (order is part of the contract)", i, got[i].Key, key)
		}
	}
	sectionKeys := []string{LayCheckKeyMarkerScope, LayCheckKeyMarkerWidth, LayCheckKeyMirrorExpansion}
	gotSec := ProductionLaySectionChecks(in, in.Sections[0])
	if len(gotSec) != len(sectionKeys) {
		t.Fatalf("section checks = %d, want %d", len(gotSec), len(sectionKeys))
	}
	for i, key := range sectionKeys {
		if gotSec[i].Key != key {
			t.Errorf("section check %d = %q, want %q", i, gotSec[i].Key, key)
		}
	}
	// Ключи глобально уникальны — по обеим сборкам сразу.
	seen := map[string]bool{}
	for _, c := range append(append([]LayCheck{}, got...), gotSec...) {
		if seen[c.Key] {
			t.Errorf("duplicate check key %q", c.Key)
		}
		seen[c.Key] = true
		if c.Label == "" {
			t.Errorf("check %q has no label", c.Key)
		}
	}
}

// TestEveryCheckDetailsUnlessOK is the invariant the proto states («detail ПУСТО только у OK») and
// the one a new predicate is most likely to break: a BLOCKER with no detail is a red badge nobody can
// act on, and an OK carrying one reads as a failure that was let through.
func TestEveryCheckDetailsUnlessOK(t *testing.T) {
	yield := mirroredYield(t, 4, 3, 3)
	healthyIn := func() LayCheckInput {
		m := healthyMarker()
		m.Yield = yield
		return LayCheckInput{
			Lay: healthyLay(), Mode: LayFaceModeFaceUp,
			Sections: []LayCheckSection{section("S1", 20, m)},
			Article: LayArticleFacts{
				Name: "ВЕЛЬВЕТ", NominalUsableWidthCm: nd("150"), FabricThicknessMm: nd("0.3"),
			},
			Limits:        LayWorkshopLimits{MaxStackHeightCm: nd("15"), CuttingTableLengthCm: nd("1000")},
			BomLines:      []entity.FabricDirectionLine{{LineKey: healthyLay().BomLineKey, Purpose: "main", Name: "ВЕЛЬВЕТ", Direction: "two_way"}},
			PieceSymmetry: map[string]sql.NullString{"PIECE_FRONT": marked(entity.PieceCutSymmetryMirrored)},
			QtySnapshot:   []LayQtyEntry{{SizeId: 10, Qty: 20}},
			QtyCurrent:    []LayQtyEntry{{SizeId: 10, Qty: 20}},
			PieceCuts: []LayPieceCut{{
				PieceLineKey: "PIECE_FRONT", PieceName: "полочка",
				Cut: MarkerPieceCounts{AsDrawn: 20, Mirrored: 20}, Symmetry: marked(entity.PieceCutSymmetryMirrored),
				PiecesPerGarment: 2, ChiralityKnown: true,
			}},
			Covered: LayCoveredQty{Qty: 20, Known: true},
		}
	}

	// A настил on which literally everything is right: every check must be OK, or the healthy path
	// itself is broken and every UNKNOWN below would be meaningless.
	for _, c := range append(ProductionLayChecks(healthyIn()), ProductionLaySectionChecks(healthyIn(), healthyIn().Sections[0])...) {
		if c.Status != LayCheckStatusOK {
			t.Errorf("healthy lay: check %q = %v (%s), want OK", c.Key, c.Status, c.Detail)
		}
	}

	// И теперь ломаем по одному входу, проверяя дисциплину detail.
	broken := []struct {
		name  string
		mutbr func(*LayCheckInput)
	}{
		{"odd plies face to face", func(in *LayCheckInput) { in.Mode = LayFaceModeFaceToFace; in.Sections[0].Plies = 7 }},
		{"direction unset", func(in *LayCheckInput) { in.BomLines[0].Direction = "" }},
		{"thickness unset", func(in *LayCheckInput) { in.Article.FabricThicknessMm = unsetDec }},
		{"stack limit unset", func(in *LayCheckInput) { in.Limits.MaxStackHeightCm = unsetDec }},
		{"table unset", func(in *LayCheckInput) { in.Limits.CuttingTableLengthCm = unsetDec }},
		{"table too short", func(in *LayCheckInput) { in.Limits.CuttingTableLengthCm = nd("100") }},
		{"slot detached", func(in *LayCheckInput) { in.Lay.BomItemId = noInt }},
		{"quantities moved", func(in *LayCheckInput) { in.QtyCurrent = []LayQtyEntry{{SizeId: 10, Qty: 25}} }},
		{"coverage unknown", func(in *LayCheckInput) { in.Covered = LayCoveredQty{} }},
		{"marker off scope", func(in *LayCheckInput) { in.Sections[0].Marker.RunId = 0 }},
		{"width unknown", func(in *LayCheckInput) { in.Article.NominalUsableWidthCm = unsetDec }},
		{"symmetry unmarked", func(in *LayCheckInput) { in.PieceSymmetry = nil }},
		{"blob unread", func(in *LayCheckInput) { in.Sections[0].Marker.Yield = nil }},
	}
	for _, b := range broken {
		t.Run(b.name, func(t *testing.T) {
			in := healthyIn()
			b.mutbr(&in)
			all := append(ProductionLayChecks(in), ProductionLaySectionChecks(in, in.Sections[0])...)
			degraded := 0
			for _, c := range all {
				switch {
				case c.Status == LayCheckStatusOK && c.Detail != "":
					t.Errorf("check %q is OK but carries a detail: %q", c.Key, c.Detail)
				case c.Status != LayCheckStatusOK:
					degraded++
					if c.Detail == "" {
						t.Errorf("check %q is %v with an EMPTY detail — a badge nobody can act on", c.Key, c.Status)
					}
				}
			}
			if degraded == 0 {
				t.Errorf("breaking %q degraded nothing — the input is not wired to any check", b.name)
			}
		})
	}
}

// TestUnknownNeverCollapsesIntoOK is the mutation guard the orchestrator asked for by name. Every
// UNKNOWN door of every predicate is walked, and NONE of them may answer OK. This is the test that
// fails first if somebody replaces a WorstLayCheckStatus fold with `max`, drops a `!Valid` guard, or
// "simplifies" an unset column to a zero.
func TestUnknownNeverCollapsesIntoOK(t *testing.T) {
	unknowns := []struct {
		name string
		got  LayCheck
	}{
		{"толщина ткани не замерена", LayStackHeightCheck(20, unsetDec, nd("15"))},
		{"предел стопки не настроен", LayStackHeightCheck(20, nd("0.3"), unsetDec)},
		{"ни толщины, ни предела", LayStackHeightCheck(20, unsetDec, unsetDec)},
		{"в настиле нет слоёв", LayStackHeightCheck(0, nd("0.3"), nd("15"))},
		{"длина стола не настроена", LayTableLengthCheck([]LayCheckSection{section("S1", 4, healthyMarker())}, unsetDec)},
		{"ширины артикула нет", LayMarkerWidthCheck(healthyMarker(), LayArticleFacts{Name: "X"})},
		{"направление не заполнено", LayDirectionModeCheck(LayFaceModeFaceUp, "K",
			[]entity.FabricDirectionLine{{LineKey: "K", Direction: ""}})},
		{"скоуп не резолвится", LayDirectionModeCheck(LayFaceModeFaceUp, "GONE",
			[]entity.FabricDirectionLine{{LineKey: "K", Direction: "one_way"}})},
		{"блоб не прочитан", LayMirrorExpansionCheck(LayFaceModeFaceUp, healthyMarker(), nil)},
		{"cut_symmetry не размечено", LayMirrorExpansionCheck(LayFaceModeFaceUp,
			func() LayMarkerFacts { m := healthyMarker(); m.Yield = mirroredYield(t, 4, 3, 3); return m }(),
			map[string]sql.NullString{"PIECE_FRONT": unmarked})},
		{"покрытие не определено", LayOvercutCheck([]LayPieceCut{{
			PieceLineKey: "P", Cut: MarkerPieceCounts{AsDrawn: 4},
			Symmetry: marked(entity.PieceCutSymmetryFold), PiecesPerGarment: 1, ChiralityKnown: true,
		}}, LayCoveredQty{})},
		{"деталей для перекроя нет", LayOvercutCheck(nil, LayCoveredQty{Qty: 4, Known: true})},
	}
	for _, u := range unknowns {
		if u.got.Status != LayCheckStatusUnknown {
			t.Errorf("%s: %q = %v, want UNKNOWN — «проверить было нечем» никогда не «проверили, всё хорошо»",
				u.name, u.got.Key, u.got.Status)
		}
		if u.got.Status.Blocks() {
			t.Errorf("%s: an UNKNOWN must not block either", u.name)
		}
		if u.got.Detail == "" {
			t.Errorf("%s: an UNKNOWN with no explanation is indistinguishable from a bug", u.name)
		}
	}
}
