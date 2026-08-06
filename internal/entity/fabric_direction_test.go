package entity

import (
	"errors"
	"strings"
	"testing"
)

// The direction rule decides four separate things and each of them has a way to be wrong: WHICH
// lines are asked (a назначение owns several — 0267), WHAT the answer is when they disagree (строгое
// побеждает), WHETHER a given blob may be judged at all (only schema 3 — the trap), and whether the
// blob is telling the truth about its own version (a mirror cannot be legacy). Every case below is
// one of those four.
func TestValidateMarkerFabricDirection(t *testing.T) {
	const (
		shellA  = "01FDLINESHELLA0000000000A1" // основная ткань, article 1
		shellB  = "01FDLINESHELLB0000000000A2" // основная ткань, article 2 — same назначение
		lining  = "01FDLINELINING0000000000L1"
		unsized = "01FDLINEUNSORTED00000000U1" // no назначение: an unsorted line, the pre-0265 state
	)
	line := func(idx int, key, purpose, name, dir string) FabricDirectionLine {
		return FabricDirectionLine{Index: idx, LineKey: key, Purpose: purpose, Name: name, Direction: dir}
	}
	v3 := func(halfTurn, flip bool) MarkerLayoutFacts {
		return MarkerLayoutFacts{SchemaVersion: 3, HasHalfTurn: halfTurn, HasFlip: flip}
	}

	t.Run("one_way refuses a v3 layout with a 180° placement", func(t *testing.T) {
		lines := []FabricDirectionLine{line(2, shellA, "main", "Вельвет", "one_way")}
		ve := requireFieldViolation(t, ValidateMarkerFabricDirection(shellA, lines, v3(true, false)),
			"layout.placements", ReasonFlipOnOneWay)
		// FIX 4: the blocker is named, not printed as a bare ULID.
		if !strings.Contains(ve.HowToFix, "Вельвет") || !strings.Contains(ve.HowToFix, "180") {
			t.Errorf("how-to-fix must name the cloth and what fired: %q", ve.HowToFix)
		}
		if ve.Conflicting != shellA {
			t.Errorf("conflicting = %q, want the blocking line_key %q", ve.Conflicting, shellA)
		}
	})

	t.Run("one_way refuses a v3 layout with a mirrored placement", func(t *testing.T) {
		lines := []FabricDirectionLine{line(0, shellA, "main", "Вельвет", "one_way")}
		ve := requireFieldViolation(t, ValidateMarkerFabricDirection(shellA, lines, v3(false, true)),
			"layout.placements", ReasonFlipOnOneWay)
		if !strings.Contains(ve.HowToFix, "зеркальные") {
			t.Errorf("how-to-fix must say a mirror fired: %q", ve.HowToFix)
		}
	})

	// THE REGRESSION GUARD. A stored marker legitimately carries rotations outside today's policy —
	// the manual editor saves the rotation a piece actually has, so 90° at allow_cross_grain=false
	// is on file and 180° with it. 90° never reaches this rule at all (cross-grain is
	// allow_cross_grain's question), so the sharp form of the trap is the one asserted here: a v2
	// blob carrying a half-turn on one_way cloth still saves, because it predates the policy.
	t.Run("legacy schema is grandfathered on one_way cloth", func(t *testing.T) {
		lines := []FabricDirectionLine{line(0, shellA, "main", "Вельвет", "one_way")}
		for _, v := range []int{1, 2} {
			legacy := MarkerLayoutFacts{SchemaVersion: v, HasHalfTurn: true}
			if err := ValidateMarkerFabricDirection(shellA, lines, legacy); err != nil {
				t.Fatalf("schema_version %d must save unchanged, got %v", v, err)
			}
		}
	})

	// …and the exact half of that exemption which is NOT legitimate. `flipped` did not exist before
	// schema 3, so no stored blob can carry one: a legacy version declaring a mirror is a forgery or
	// a client bug, and grandfathering it would make the exemption claimable by writing a smaller
	// number.
	t.Run("a mirror cannot be legacy", func(t *testing.T) {
		lines := []FabricDirectionLine{line(0, shellA, "main", "Вельвет", "one_way")}
		for _, v := range []int{1, 2} {
			ve := requireFieldViolation(t,
				ValidateMarkerFabricDirection(shellA, lines, MarkerLayoutFacts{SchemaVersion: v, HasFlip: true}),
				"layout.placements", ReasonFlipInLegacySchema)
			if !strings.Contains(ve.HowToFix, "flipped") {
				t.Errorf("how-to-fix must name the impossible field: %q", ve.HowToFix)
			}
		}
		// The same forgery is refused on cloth that would otherwise permit everything: it is a
		// statement about the payload, not about the fabric.
		any := []FabricDirectionLine{line(0, shellA, "main", "Джерси", "any")}
		requireFieldViolation(t,
			ValidateMarkerFabricDirection(shellA, any, MarkerLayoutFacts{SchemaVersion: 1, HasFlip: true}),
			"layout.placements", ReasonFlipInLegacySchema)
	})

	t.Run("unknown direction blocks the save and names the row", func(t *testing.T) {
		lines := []FabricDirectionLine{line(4, shellA, "main", "Твил 320", "")}
		err := ValidateMarkerFabricDirection(shellA, lines, MarkerLayoutFacts{SchemaVersion: 1})
		ve := requireFieldViolation(t, err, "bom_items[4].fabric_direction", ReasonFabricDirectionUnknown)
		// The refusal has to be actionable in both registers: a form path a client can pin, the
		// line_key in the machine-readable slot, the name in the prose.
		if ve.Conflicting != shellA {
			t.Errorf("conflicting = %q, want the line_key %q", ve.Conflicting, shellA)
		}
		if !strings.Contains(ve.HowToFix, "Твил 320") || !strings.Contains(ve.HowToFix, "BOM") {
			t.Errorf("how-to-fix must name the row and the tab: %q", ve.HowToFix)
		}
		if strings.Contains(ve.HowToFix, "назначение") {
			t.Errorf("the line the раскладка names needs no назначение explanation: %q", ve.HowToFix)
		}
		// And it blocks the save regardless of blob version — UNKNOWN is not grandfathered, it is
		// the whole point of Ф1.5: the field becomes required where it decides something.
		if err := ValidateMarkerFabricDirection(shellA, lines, v3(false, false)); err == nil {
			t.Error("a v3 layout on unknown cloth must be refused too")
		}
	})

	// The fix is a mass fill (кампания Д1). Naming one row per round-trip would make a three-row
	// card three saves; the refusal carries them all.
	t.Run("every unset row is named at once", func(t *testing.T) {
		lines := []FabricDirectionLine{
			line(1, shellA, "main", "Твил 320", ""),
			line(3, shellB, "main", "Вельвет", ""),
			line(5, lining, "lining", "Купра", ""),
		}
		ve := requireFieldViolation(t, ValidateMarkerFabricDirection(shellA, lines, v3(false, false)),
			"bom_items[1].fabric_direction", ReasonFabricDirectionUnknown)
		if ve.Conflicting != shellA+", "+shellB {
			t.Errorf("conflicting = %q, want both keys of the scope in order", ve.Conflicting)
		}
		if !strings.Contains(ve.HowToFix, "Твил 320") || !strings.Contains(ve.HowToFix, "Вельвет") {
			t.Errorf("how-to-fix must list every unset row: %q", ve.HowToFix)
		}
		// The lining is a different назначение and is not swept in.
		if strings.Contains(ve.HowToFix, "Купра") {
			t.Errorf("another назначение must stay out of this refusal: %q", ve.HowToFix)
		}
	})

	t.Run("two_way and any allow the piece upside down", func(t *testing.T) {
		for _, dir := range []string{"two_way", "any"} {
			lines := []FabricDirectionLine{line(0, shellA, "main", "Джерси", dir)}
			if err := ValidateMarkerFabricDirection(shellA, lines, v3(true, true)); err != nil {
				t.Errorf("%s must permit 180°/mirror, got %v", dir, err)
			}
		}
	})

	// СТРОГОЕ ПОБЕЖДАЕТ. The marker names ONE line, but a назначение owns several articles and the
	// same geometry will be cut on whichever one the colourway pins — so one directional article
	// forbids the flip for the whole назначение, including through the line the marker names.
	t.Run("strictest wins across a назначение", func(t *testing.T) {
		lines := []FabricDirectionLine{
			line(0, shellA, "main", "Твил гладкий", "any"),
			line(1, shellB, "main", "Вельвет", "one_way"),
			line(2, lining, "lining", "Купра", "any"),
		}
		ve := requireFieldViolation(t, ValidateMarkerFabricDirection(shellA, lines, v3(true, false)),
			"layout.placements", ReasonFlipOnOneWay)
		// Being blocked by a row the marker does not name has to be explained, or it reads as a bug.
		if !strings.Contains(ve.HowToFix, "назначение") || !strings.Contains(ve.HowToFix, "Вельвет") {
			t.Errorf("how-to-fix must name the blocking article and why it applies: %q", ve.HowToFix)
		}
		if ve.Conflicting != shellB {
			t.Errorf("conflicting = %q, want the one_way line %q", ve.Conflicting, shellB)
		}
		// The lining is a different назначение and is not dragged in by the strict shell.
		if err := ValidateMarkerFabricDirection(lining, lines, v3(true, false)); err != nil {
			t.Errorf("another назначение must stay unaffected, got %v", err)
		}
	})

	t.Run("unknown on a sibling line of the назначение blocks it too", func(t *testing.T) {
		lines := []FabricDirectionLine{
			line(0, shellA, "main", "Твил гладкий", "any"),
			line(7, shellB, "main", "Вельвет", ""),
		}
		ve := requireFieldViolation(t, ValidateMarkerFabricDirection(shellA, lines, MarkerLayoutFacts{SchemaVersion: 1}),
			"bom_items[7].fabric_direction", ReasonFabricDirectionUnknown)
		if !strings.Contains(ve.HowToFix, "назначение") {
			t.Errorf("how-to-fix %q must explain why a line the marker does not name is blocking it", ve.HowToFix)
		}
	})

	t.Run("an unsorted line answers for itself alone", func(t *testing.T) {
		// Pre-0265 state: no назначение anywhere, so the scope is the named line and the one_way
		// neighbour is irrelevant.
		lines := []FabricDirectionLine{
			line(0, unsized, "", "Твил гладкий", "any"),
			line(1, shellB, "", "Вельвет", "one_way"),
		}
		if err := ValidateMarkerFabricDirection(unsized, lines, v3(true, false)); err != nil {
			t.Errorf("an unsorted card must resolve line-by-line, got %v", err)
		}
	})

	t.Run("an unlinked marker stays saveable", func(t *testing.T) {
		lines := []FabricDirectionLine{line(0, shellA, "main", "Вельвет", "")}
		if err := ValidateMarkerFabricDirection("", lines, v3(true, true)); err != nil {
			t.Errorf("no bom_line_key means no cloth to ask about, got %v", err)
		}
	})

	t.Run("a dangling binding stays saveable", func(t *testing.T) {
		// The line was deleted or reclassified out of roll goods after the marker was measured.
		// «Слот удалён» is a UI state, not a reason to strand a stored measurement.
		lines := []FabricDirectionLine{line(0, shellB, "main", "Вельвет", "one_way")}
		if err := ValidateMarkerFabricDirection("01FDGONELINE000000000000X1", lines, v3(true, true)); err != nil {
			t.Errorf("a dangling binding must not block the save, got %v", err)
		}
	})

	t.Run("a value outside the vocabulary reads as unknown", func(t *testing.T) {
		// Unreachable through the app (chk on 0073), and if it ever becomes reachable the
		// fail-closed answer is the safe one: ask the operator rather than assume «flip allowed».
		lines := []FabricDirectionLine{line(0, shellA, "main", "Вельвет", "diagonal")}
		requireFieldViolation(t, ValidateMarkerFabricDirection(shellA, lines, v3(false, false)),
			"bom_items[0].fabric_direction", ReasonFabricDirectionUnknown)
	})

	// The zero value of MarkerLayoutFacts is the one that would sail through every check, so it must
	// not be reachable by forgetting to distil the blob. The API normalises an absent version to 1;
	// a zero here can only mean nobody filled the struct, and that is a server bug, not the
	// operator's — so it is NOT a field violation.
	t.Run("undistilled facts are refused, not skipped", func(t *testing.T) {
		lines := []FabricDirectionLine{line(0, shellA, "main", "Вельвет", "one_way")}
		err := ValidateMarkerFabricDirection(shellA, lines, MarkerLayoutFacts{})
		if err == nil {
			t.Fatal("a linked marker must not be judged on facts nobody filled")
		}
		var ve *ValidationError
		if errors.As(err, &ve) {
			t.Errorf("a wiring failure must not read as a field violation: %v", ve)
		}
	})
}

// TestScopeFabricDirectionStrictest pins the fold itself: the answer is the strictest member, and
// order of appearance must not change it.
func TestScopeFabricDirectionStrictest(t *testing.T) {
	lines := []FabricDirectionLine{
		{LineKey: "A", Purpose: "main", Direction: "any"},
		{LineKey: "B", Purpose: "main", Direction: "one_way"},
		{LineKey: "C", Purpose: "main", Direction: "two_way"},
	}
	for _, order := range [][]string{{"A", "B", "C"}, {"C", "B", "A"}, {"B", "A", "C"}} {
		dir, unknown, ok := ScopeFabricDirection(FabricScope{Key: "main", ByPurpose: true, LineKeys: order}, lines)
		if !ok || dir != FabricDirectionOneWay || len(unknown) != 0 {
			t.Fatalf("order %v: dir = %q ok = %v unknown = %v, want one_way", order, dir, ok, unknown)
		}
	}
	dir, _, ok := ScopeFabricDirection(
		FabricScope{Key: "main", ByPurpose: true, LineKeys: []string{"A", "C"}}, lines)
	if !ok || dir != FabricDirectionTwoWay {
		t.Fatalf("without a one_way member: dir = %q ok = %v, want two_way", dir, ok)
	}
}

func requireFieldViolation(t *testing.T, err error, field, reason string) *ValidationError {
	t.Helper()
	var ve *ValidationError
	if !errors.As(err, &ve) {
		t.Fatalf("want a field-tagged ValidationError, got %v", err)
	}
	if ve.Field != field {
		t.Errorf("field = %q, want %q", ve.Field, field)
	}
	// Reason is the stable machine-readable code a client switches on (entity/order.go), never
	// prose: apierr copies it verbatim into the BadRequest description.
	if ve.Reason != reason {
		t.Errorf("reason = %q, want the code %q", ve.Reason, reason)
	}
	return ve
}
