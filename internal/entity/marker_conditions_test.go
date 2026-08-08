package entity

import (
	"strings"
	"testing"
	"time"

	"github.com/shopspring/decimal"
)

func dec(s string) decimal.NullDecimal {
	d, err := decimal.NewFromString(s)
	if err != nil {
		panic(err)
	}
	return decimal.NullDecimal{Decimal: d, Valid: true}
}

// --- отпечаток набора деталей -----------------------------------------------------------------

// The point of the fingerprint is that it moves for changes to the SET and stands still for
// everything else. Both halves are asserted, because a fingerprint that never moves and one that
// always moves are equally useless — and each fails in its own direction: the first feeds a
// readiness gate a silent «не менялось», the second buries the operator in badges.
func TestPieceSetFingerprintIsStableAgainstIrrelevantEdits(t *testing.T) {
	base := []PieceSetEntry{
		{LineKey: "01HZZZAAAA0000000000000001", PiecesPerGarment: 2},
		{LineKey: "01HZZZAAAA0000000000000002", PiecesPerGarment: 1},
		{LineKey: "LEGACY00000000000000000007", PiecesPerGarment: 4},
	}
	want, ok := PieceSetFingerprint(base)
	if !ok {
		t.Fatal("the base set must be fingerprintable")
	}

	t.Run("reordering does not change it", func(t *testing.T) {
		// The card read orders by display_order, which an operator drags around; the fingerprint
		// sorts by line_key precisely so that dragging is not a change of set.
		shuffled := []PieceSetEntry{base[2], base[0], base[1]}
		got, ok := PieceSetFingerprint(shuffled)
		if !ok || got != want {
			t.Fatalf("reordering moved the fingerprint: %q vs %q", got, want)
		}
	})

	t.Run("case and surrounding space in the key do not change it", func(t *testing.T) {
		folded := []PieceSetEntry{
			{LineKey: " 01hzzzaaaa0000000000000001 ", PiecesPerGarment: 2},
			{LineKey: "01HZZZAAAA0000000000000002", PiecesPerGarment: 1},
			{LineKey: "legacy00000000000000000007", PiecesPerGarment: 4},
		}
		got, ok := PieceSetFingerprint(folded)
		if !ok || got != want {
			t.Fatalf("a collation-shaped difference moved the fingerprint: %q vs %q", got, want)
		}
	})

	t.Run("adding a piece changes it", func(t *testing.T) {
		grown := append(append([]PieceSetEntry{}, base...),
			PieceSetEntry{LineKey: "01HZZZAAAA0000000000000009", PiecesPerGarment: 1})
		got, _ := PieceSetFingerprint(grown)
		if got == want {
			t.Fatal("adding a cut-piece must move the fingerprint")
		}
	})

	t.Run("removing a piece changes it", func(t *testing.T) {
		got, _ := PieceSetFingerprint(base[:2])
		if got == want {
			t.Fatal("removing a cut-piece must move the fingerprint")
		}
	})

	t.Run("changing pieces_per_garment changes it", func(t *testing.T) {
		bumped := append([]PieceSetEntry{}, base...)
		bumped[0].PiecesPerGarment = 3
		got, _ := PieceSetFingerprint(bumped)
		if got == want {
			t.Fatal("cutting a piece a different number of times must move the fingerprint")
		}
	})
}

// An empty set is a real state and must hash — a card with no cut-pieces has to stay distinguishable
// from a marker whose fingerprint was never recorded, or the two collapse into one silence.
func TestPieceSetFingerprintOfEmptySetIsStableAndDistinct(t *testing.T) {
	empty, ok := PieceSetFingerprint(nil)
	if !ok || empty == "" {
		t.Fatalf("the empty set must be fingerprintable, got %q ok=%v", empty, ok)
	}
	again, _ := PieceSetFingerprint([]PieceSetEntry{})
	if again != empty {
		t.Fatal("the empty set must hash to one value")
	}
	first, _ := PieceSetFingerprint([]PieceSetEntry{{LineKey: "X", PiecesPerGarment: 1}})
	if first == empty {
		t.Fatal("adding the FIRST piece must move the fingerprint off the empty hash")
	}
	if got := PieceSetFingerprintNull(nil); !got.Valid {
		t.Fatal("the empty set must reach the column as a value, not as NULL")
	}
}

// Hashing "" would fold two keyless pieces into one and then report «unchanged» for a set one of them
// was deleted from. The branch is unreachable in production, which is exactly why it must fail safe.
func TestPieceSetFingerprintRefusesAKeylessPiece(t *testing.T) {
	for _, key := range []string{"", "   "} {
		if _, ok := PieceSetFingerprint([]PieceSetEntry{
			{LineKey: "01HZZZAAAA0000000000000001", PiecesPerGarment: 1},
			{LineKey: key, PiecesPerGarment: 1},
		}); ok {
			t.Fatalf("a piece with line_key %q must make the set unfingerprintable", key)
		}
	}
	if got := PieceSetFingerprintNull([]PieceSetEntry{{LineKey: "", PiecesPerGarment: 1}}); got.Valid {
		t.Fatal("an unfingerprintable set must reach the column as NULL")
	}
}

// The read and write sides must agree by CONSTRUCTION, not by discipline: both project onto
// PieceSetEntry and both call PieceSetFingerprint. This asserts the projection helper the read side
// uses agrees with the entries the write side selects.
func TestPieceSetEntriesOfMatchesTheWriteSideProjection(t *testing.T) {
	pieces := []TechCardPiece{
		{LineKey: "01HZZZAAAA0000000000000002", PiecesPerGarment: 1, Name: "рукав"},
		{LineKey: "01HZZZAAAA0000000000000001", PiecesPerGarment: 2, Name: "полочка"},
	}
	fromRead, ok := PieceSetFingerprint(PieceSetEntriesOf(pieces))
	if !ok {
		t.Fatal("the read-side projection must be fingerprintable")
	}
	fromWrite, _ := PieceSetFingerprint([]PieceSetEntry{
		{LineKey: "01HZZZAAAA0000000000000001", PiecesPerGarment: 2},
		{LineKey: "01HZZZAAAA0000000000000002", PiecesPerGarment: 1},
	})
	if fromRead != fromWrite {
		t.Fatalf("the two sides disagree: read %q, write %q", fromRead, fromWrite)
	}
}

// --- статус набора ------------------------------------------------------------------------------

// UNKNOWN is never «changed», and that is a requirement rather than a nicety: every раскладка taken
// before Ф3 carries no fingerprint, and reading them as changed would badge the whole estate at once.
func TestMarkerPieceSetStatus(t *testing.T) {
	cases := []struct {
		name   string
		stored string
		card   string
		want   MarkerPieceSetStatus
	}{
		{"no fingerprint recorded", "", "abc", MarkerPieceSetUnknown},
		{"card unfingerprintable today", "abc", "", MarkerPieceSetUnknown},
		{"same set", "abc", "abc", MarkerPieceSetMatches},
		{"set moved", "abc", "def", MarkerPieceSetChanged},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var m TechCardMarkerSummary
			if c.stored != "" {
				m.PieceSetFp.String, m.PieceSetFp.Valid = c.stored, true
			}
			if c.card != "" {
				m.CardPieceSetFp.String, m.CardPieceSetFp.Valid = c.card, true
			}
			if got := m.PieceSetStatus(); got != c.want {
				t.Fatalf("status = %v, want %v", got, c.want)
			}
		})
	}
}

// --- припуск ------------------------------------------------------------------------------------

func TestMarkerAllowanceOf(t *testing.T) {
	t.Run("nothing recorded is старая норма", func(t *testing.T) {
		a := MarkerAllowanceOf(decimal.NullDecimal{}, dec("1.00"))
		if a.Recorded {
			t.Fatal("an unrecorded offset must not read as a recorded allowance")
		}
	})
	t.Run("offset without a file measurement is a floor", func(t *testing.T) {
		a := MarkerAllowanceOf(dec("1.00"), decimal.NullDecimal{})
		if !a.Recorded || a.Confirmed {
			t.Fatalf("want recorded-but-unconfirmed, got %+v", a)
		}
		if !a.Mm.Equal(decimal.RequireFromString("1.00")) {
			t.Fatalf("Mm = %s, want 1.00", a.Mm)
		}
	})
	t.Run("both halves sum", func(t *testing.T) {
		a := MarkerAllowanceOf(dec("0"), dec("1.00"))
		if !a.Recorded || !a.Confirmed || !a.Mm.Equal(decimal.RequireFromString("1.00")) {
			t.Fatalf("want confirmed 1.00, got %+v", a)
		}
	})
	t.Run("a recorded zero is a measurement, not an absence", func(t *testing.T) {
		a := MarkerAllowanceOf(dec("0"), dec("0"))
		if !a.Recorded || !a.Confirmed || !a.Mm.IsZero() {
			t.Fatalf("want a confirmed zero, got %+v", a)
		}
	})
}

// The refusal has to fire on exactly one combination and accept the four honest ones — a refusal that
// also fired on «замерить было нечем» would turn an unmeasurable file into an unsaveable раскладка.
func TestMarkerAllowanceRefusal(t *testing.T) {
	cases := []struct {
		name          string
		seam, contour decimal.NullDecimal
		wantRefusal   bool
	}{
		{"double allowance", dec("1.00"), dec("1.00"), true},
		{"seam line laid, offset added", dec("1.00"), dec("0"), false},
		{"cut line laid, no offset", dec("0"), dec("1.00"), false},
		{"honest ignorance: nothing to measure against", dec("1.00"), decimal.NullDecimal{}, false},
		{"nothing recorded at all", decimal.NullDecimal{}, decimal.NullDecimal{}, false},
		{"cut line laid, offset unrecorded", decimal.NullDecimal{}, dec("1.00"), false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			ve := MarkerAllowanceRefusal(c.seam, c.contour, "14")
			if (ve != nil) != c.wantRefusal {
				t.Fatalf("refusal = %v, want %v", ve, c.wantRefusal)
			}
			if ve == nil {
				return
			}
			if ve.Field != "seam_allowance_mm" || ve.Reason != ReasonDoubleSeamAllowance {
				t.Fatalf("violation = %+v, want the seam field and the stable reason code", ve)
			}
			// The prose has to carry the ARITHMETIC, not just the verdict: an operator who is told
			// «двойной припуск» and not by how much has no way to judge whether it matters.
			if !strings.Contains(ve.HowToFix, "2") {
				t.Fatalf("the refusal must state the doubled total, got %q", ve.HowToFix)
			}
		})
	}
}

func TestValidateSeamAllowanceStandardMm(t *testing.T) {
	t.Run("zero is a legal standard", func(t *testing.T) {
		// The whole reason this validator is not ValidateCuttingTableLengthCm: 0 cm of table is
		// nonsense, 0 mm of allowance is «our выкройки carry the cut line».
		if err := ValidateSeamAllowanceStandardMm("f", dec("0")); err != nil {
			t.Fatalf("zero must be accepted: %v", err)
		}
	})
	t.Run("unset is legal", func(t *testing.T) {
		if err := ValidateSeamAllowanceStandardMm("f", decimal.NullDecimal{}); err != nil {
			t.Fatalf("clearing the standard must be accepted: %v", err)
		}
	})
	// 0.5 is the CENTIMETRE mistake the mm switch introduced: it looks like a plausible allowance to
	// whoever types it and means half a millimetre to everything downstream, so it is refused by its
	// own rule rather than by the ceiling.
	for _, bad := range []string{"-0.5", "0.5", "100.01", "250"} {
		t.Run("rejects "+bad, func(t *testing.T) {
			if err := ValidateSeamAllowanceStandardMm("f", dec(bad)); err == nil {
				t.Fatalf("%s must be refused", bad)
			}
		})
	}
	t.Run("rejects a scale the column would truncate", func(t *testing.T) {
		if err := ValidateSeamAllowanceStandardMm("f", dec("10.05")); err == nil {
			t.Fatal("a second decimal place must be refused rather than silently lost")
		}
	})
}

// The RECORDED allowance is judged by different rules than the standard, and both differences are
// deliberate. This is the validator that replaced a CHECK constraint: the ceiling could not stay in
// the schema because ADD CONSTRAINT validates history, and a раскладка recorded before the rule
// existed would have taken the whole deploy down with it (0291).
func TestValidateMarkerAllowanceMm(t *testing.T) {
	t.Run("zero is a measured value, not an absence", func(t *testing.T) {
		if err := ValidateMarkerAllowanceMm("f", dec("0")); err != nil {
			t.Fatalf("zero must be accepted: %v", err)
		}
	})
	t.Run("unset is legal — nothing was measured", func(t *testing.T) {
		if err := ValidateMarkerAllowanceMm("f", decimal.NullDecimal{}); err != nil {
			t.Fatalf("an unrecorded allowance must be accepted: %v", err)
		}
	})
	// NO «implausibly narrow» RULE, unlike the standard: the contour half is measured off the
	// drawing and the measurement's own floor is half a millimetre, so 0.5 is an honest reading of a
	// file whose two lines nearly coincide — not a centimetre typed by mistake.
	t.Run("a sub-millimetre reading is accepted", func(t *testing.T) {
		if err := ValidateMarkerAllowanceMm("f", dec("0.5")); err != nil {
			t.Fatalf("a measured 0.5 mm must be accepted: %v", err)
		}
	})
	for _, bad := range []string{"-1", "100.1", "250"} {
		t.Run("rejects "+bad, func(t *testing.T) {
			if err := ValidateMarkerAllowanceMm("f", dec(bad)); err == nil {
				t.Fatalf("%s must be refused", bad)
			}
		})
	}
	// The columns are DECIMAL(6,1) since 0290. Two decimal places used to be rounded here and
	// rounded AGAIN by MySQL on the way in, so 7.55 became 7.6 with nothing saying so.
	t.Run("rejects a scale the column would truncate", func(t *testing.T) {
		if err := ValidateMarkerAllowanceMm("f", dec("7.55")); err == nil {
			t.Fatal("a second decimal place must be refused rather than silently lost")
		}
	})
}

// The card wins, and an unset pair yields «no standard» rather than zero — substituting zero would
// declare every раскладка with a 1 cm allowance in breach of a standard nobody set.
func TestRequiredSeamAllowanceMm(t *testing.T) {
	if got := RequiredSeamAllowanceMm(dec("0"), dec("1.00")); !got.Valid || !got.Decimal.IsZero() {
		t.Fatalf("the card's explicit zero must win over the workshop default, got %+v", got)
	}
	if got := RequiredSeamAllowanceMm(decimal.NullDecimal{}, dec("1.00")); !got.Valid ||
		!got.Decimal.Equal(decimal.RequireFromString("1.00")) {
		t.Fatalf("an unset card must fall back to the workshop default, got %+v", got)
	}
	if got := RequiredSeamAllowanceMm(decimal.NullDecimal{}, decimal.NullDecimal{}); got.Valid {
		t.Fatal("with neither configured there is NO standard; it must not degrade to zero")
	}
}

// --- норма --------------------------------------------------------------------------------------

func normPeer(id int, name string, bom int64, bound bool, at time.Time) NormPeer {
	return NormPeer{Id: id, Name: name, Scope: NormScope{BomItemId: bom, Bound: bound}, UpdatedAt: at}
}

// The tiebreak is what the missing UNIQUE index is traded for: readers must all pick the SAME row
// even in a state the schema failed to prevent, because readers disagreeing is worse than two norms.
func TestSelectNormIsDeterministic(t *testing.T) {
	t0 := time.Date(2026, 8, 1, 10, 0, 0, 0, time.UTC)
	scope := NormScope{BomItemId: 7, Bound: true}
	peers := []NormPeer{
		normPeer(3, "старая", 7, true, t0),
		normPeer(9, "свежая", 7, true, t0.Add(time.Hour)),
		normPeer(4, "подкладочная", 8, true, t0.Add(2*time.Hour)),
		normPeer(5, "несвязанная", 0, false, t0.Add(3*time.Hour)),
	}
	winner, contenders, ok := SelectNorm(peers, scope)
	if !ok || winner.Id != 9 {
		t.Fatalf("newest updated_at must win, got %+v ok=%v", winner, ok)
	}
	if len(contenders) != 2 {
		t.Fatalf("the scope holds 2 contenders, got %d", len(contenders))
	}

	t.Run("id breaks a timestamp tie", func(t *testing.T) {
		// tech_card_marker.updated_at is a second-resolution TIMESTAMP, so ties are ordinary, not
		// exotic: two designations inside one second would otherwise be resolved by InnoDB's mood.
		tied := []NormPeer{normPeer(11, "a", 7, true, t0), normPeer(12, "b", 7, true, t0)}
		w, _, _ := SelectNorm(tied, scope)
		if w.Id != 12 {
			t.Fatalf("the higher id must break the tie, got %d", w.Id)
		}
		reversed := []NormPeer{tied[1], tied[0]}
		w2, _, _ := SelectNorm(reversed, scope)
		if w2.Id != w.Id {
			t.Fatalf("the answer must not depend on input order: %d vs %d", w2.Id, w.Id)
		}
	})

	t.Run("the unbound scope is its own", func(t *testing.T) {
		w, _, ok := SelectNorm(peers, NormScope{})
		if !ok || w.Id != 5 {
			t.Fatalf("an unlinked раскладка falls into the «no cloth» scope, got %+v ok=%v", w, ok)
		}
	})

	t.Run("a scope with no norm has no winner", func(t *testing.T) {
		if _, _, ok := SelectNorm(peers, NormScope{BomItemId: 99, Bound: true}); ok {
			t.Fatal("a cloth nobody designated must report no norm")
		}
	})
}

// Р2's second obligation: the conflict is REPORTED, not silently resolved. Without a UNIQUE index the
// state is reachable, and the one thing that must not happen is for it to be invisible.
func TestNormConflictReport(t *testing.T) {
	t0 := time.Date(2026, 8, 1, 10, 0, 0, 0, time.UTC)
	scope := NormScope{BomItemId: 7, Bound: true}
	one := []NormPeer{normPeer(3, "основная", 7, true, t0)}
	if got := NormConflictReport(one, scope); got != "" {
		t.Fatalf("a single norm is not a conflict, got %q", got)
	}
	if got := NormConflictReport(one, NormScope{BomItemId: 8, Bound: true}); got != "" {
		t.Fatalf("another cloth's norm is not this cloth's conflict, got %q", got)
	}
	two := append(one, normPeer(9, "вторая", 7, true, t0.Add(time.Hour)))
	got := NormConflictReport(two, scope)
	if got == "" {
		t.Fatal("two norms on one cloth must be reported")
	}
	// The report must name the row that ACTUALLY rules, or the screen and the response describe two
	// different раскладки.
	if !strings.Contains(got, "вторая") {
		t.Fatalf("the report must name the effective norm, got %q", got)
	}
}

func TestNormPeersOfTakesOnlyDesignatedMarkers(t *testing.T) {
	markers := []TechCardMarkerSummary{
		{Id: 1, Name: "a", IsNorm: true},
		{Id: 2, Name: "b"},
	}
	markers[0].BomItemId.Int64, markers[0].BomItemId.Valid = 7, true
	peers := NormPeersOf(markers)
	if len(peers) != 1 || peers[0].Id != 1 {
		t.Fatalf("only designated markers are peers, got %+v", peers)
	}
	if !peers[0].Scope.Bound || peers[0].Scope.BomItemId != 7 {
		t.Fatalf("the scope must come off bom_item_id, got %+v", peers[0].Scope)
	}
}

func TestIsLegacyNorm(t *testing.T) {
	var m TechCardMarkerSummary
	if !m.IsLegacyNorm() {
		t.Fatal("a раскладка with no recorded allowance is «старая норма»")
	}
	m.SeamAllowanceMm = dec("0")
	if m.IsLegacyNorm() {
		t.Fatal("a RECORDED zero is a measurement — the row is not legacy")
	}
}
