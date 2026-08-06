package entity

import (
	"errors"
	"strings"
	"testing"
)

// The direction rule decides three separate things and each of them has a way to be wrong:
// WHICH lines are asked (a назначение owns several — 0267), WHAT the answer is when they disagree
// (строгое побеждает), and WHETHER a given blob may be judged at all (only schema 3 — the trap).
// Every case below is one of those three.
func TestValidateMarkerFabricDirection(t *testing.T) {
	const (
		shellA  = "01FDLINESHELLA0000000000A1" // основная ткань, article 1
		shellB  = "01FDLINESHELLB0000000000A2" // основная ткань, article 2 — same назначение
		lining  = "01FDLINELINING0000000000L1"
		unsized = "01FDLINEUNSORTED00000000U1" // no назначение: an unsorted line, the pre-0265 state
	)
	line := func(key, purpose, name, dir string) FabricDirectionLine {
		return FabricDirectionLine{LineKey: key, Purpose: purpose, Name: name, Direction: dir}
	}
	v3 := func(halfTurn, flip bool) MarkerLayoutFacts {
		return MarkerLayoutFacts{SchemaVersion: 3, HasHalfTurn: halfTurn, HasFlip: flip}
	}

	t.Run("one_way refuses a v3 layout with a 180° placement", func(t *testing.T) {
		lines := []FabricDirectionLine{line(shellA, "main", "Вельвет", "one_way")}
		err := ValidateMarkerFabricDirection(shellA, lines, v3(true, false))
		requireFieldViolation(t, err, "layout.placements", "180")
	})

	t.Run("one_way refuses a v3 layout with a mirrored placement", func(t *testing.T) {
		lines := []FabricDirectionLine{line(shellA, "main", "Вельвет", "one_way")}
		err := ValidateMarkerFabricDirection(shellA, lines, v3(false, true))
		requireFieldViolation(t, err, "layout.placements", "mirrored")
	})

	// THE REGRESSION GUARD. A stored marker legitimately carries rotations outside today's policy —
	// the manual editor saves the rotation a piece actually has, so 90° at allow_cross_grain=false
	// is on file and 180° with it. 90° never reaches this rule at all (cross-grain is
	// allow_cross_grain's question), so the sharp form of the trap is the one asserted here: a v2
	// blob carrying BOTH forbidden things on one_way cloth still saves, because it predates the
	// policy. Judging it would retro-invalidate every measurement on file.
	t.Run("legacy schema is grandfathered on one_way cloth", func(t *testing.T) {
		lines := []FabricDirectionLine{line(shellA, "main", "Вельвет", "one_way")}
		for _, v := range []int{1, 2} {
			legacy := MarkerLayoutFacts{SchemaVersion: v, HasHalfTurn: true, HasFlip: true}
			if err := ValidateMarkerFabricDirection(shellA, lines, legacy); err != nil {
				t.Fatalf("schema_version %d must save unchanged, got %v", v, err)
			}
		}
	})

	t.Run("unknown direction blocks the save and names the line", func(t *testing.T) {
		lines := []FabricDirectionLine{line(shellA, "main", "Твил 320", "")}
		err := ValidateMarkerFabricDirection(shellA, lines, MarkerLayoutFacts{SchemaVersion: 1})
		requireFieldViolation(t, err, "bom_items.fabric_direction", "Твил 320")
		// The refusal has to be actionable: it names the control, not just the problem, and it
		// carries the line_key a client needs to deep-link to that row.
		var ve *ValidationError
		_ = errors.As(err, &ve)
		if !strings.Contains(ve.HowToFix, "BOM") {
			t.Errorf("how-to-fix must send the operator to the BOM tab, got %q", ve.HowToFix)
		}
		if !strings.Contains(ve.Message, shellA) {
			t.Errorf("message %q must carry the line_key for the deep link", ve.Message)
		}
		if strings.Contains(ve.Message, "назначение") {
			t.Errorf("the line the раскладка names needs no назначение explanation: %q", ve.Message)
		}
		// And it blocks the save regardless of blob version — UNKNOWN is not grandfathered, it is
		// the whole point of Ф1.5: the field becomes required where it decides something.
		if err := ValidateMarkerFabricDirection(shellA, lines, v3(false, false)); err == nil {
			t.Error("a v3 layout on unknown cloth must be refused too")
		}
	})

	t.Run("two_way and any allow the piece upside down", func(t *testing.T) {
		for _, dir := range []string{"two_way", "any"} {
			lines := []FabricDirectionLine{line(shellA, "main", "Джерси", dir)}
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
			line(shellA, "main", "Твил гладкий", "any"),
			line(shellB, "main", "Вельвет", "one_way"),
			line(lining, "lining", "Купра", "any"),
		}
		if err := ValidateMarkerFabricDirection(shellA, lines, v3(true, false)); err == nil {
			t.Error("a one_way article under the same назначение must forbid the half-turn")
		}
		// The lining is a different назначение and is not dragged in by the strict shell.
		if err := ValidateMarkerFabricDirection(lining, lines, v3(true, false)); err != nil {
			t.Errorf("another назначение must stay unaffected, got %v", err)
		}
	})

	t.Run("unknown on a sibling line of the назначение blocks it too", func(t *testing.T) {
		lines := []FabricDirectionLine{
			line(shellA, "main", "Твил гладкий", "any"),
			line(shellB, "main", "Вельвет", ""),
		}
		err := ValidateMarkerFabricDirection(shellA, lines, MarkerLayoutFacts{SchemaVersion: 1})
		requireFieldViolation(t, err, "bom_items.fabric_direction", "Вельвет")
		// Being sent to a row the раскладка does not name reads as a bug unless the refusal says
		// why — so here, and only here, it names the назначение.
		var ve *ValidationError
		_ = errors.As(err, &ve)
		if !strings.Contains(ve.Message, "назначение") {
			t.Errorf("message %q must explain why a line the marker does not name is blocking it", ve.Message)
		}
	})

	t.Run("an unsorted line answers for itself alone", func(t *testing.T) {
		// Pre-0265 state: no назначение anywhere, so the scope is the named line and the one_way
		// neighbour is irrelevant.
		lines := []FabricDirectionLine{
			line(unsized, "", "Твил гладкий", "any"),
			line(shellB, "", "Вельвет", "one_way"),
		}
		if err := ValidateMarkerFabricDirection(unsized, lines, v3(true, false)); err != nil {
			t.Errorf("an unsorted card must resolve line-by-line, got %v", err)
		}
	})

	t.Run("an unlinked marker stays saveable", func(t *testing.T) {
		lines := []FabricDirectionLine{line(shellA, "main", "Вельвет", "")}
		if err := ValidateMarkerFabricDirection("", lines, v3(true, true)); err != nil {
			t.Errorf("no bom_line_key means no cloth to ask about, got %v", err)
		}
	})

	t.Run("a dangling binding stays saveable", func(t *testing.T) {
		// The line was deleted or reclassified out of roll goods after the marker was measured.
		// «Слот удалён» is a UI state, not a reason to strand a stored measurement.
		lines := []FabricDirectionLine{line(shellB, "main", "Вельвет", "one_way")}
		if err := ValidateMarkerFabricDirection("01FDGONELINE000000000000X1", lines, v3(true, true)); err != nil {
			t.Errorf("a dangling binding must not block the save, got %v", err)
		}
	})

	t.Run("a value outside the vocabulary reads as unknown", func(t *testing.T) {
		// Unreachable through the app (chk on 0073), and if it ever becomes reachable the
		// fail-closed answer is the safe one: ask the operator rather than assume «flip allowed».
		lines := []FabricDirectionLine{line(shellA, "main", "Вельвет", "diagonal")}
		requireFieldViolation(t, ValidateMarkerFabricDirection(shellA, lines, v3(false, false)),
			"bom_items.fabric_direction", "Вельвет")
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
		dir, _, ok := ScopeFabricDirection(FabricScope{Key: "main", ByPurpose: true, LineKeys: order}, lines)
		if !ok || dir != FabricDirectionOneWay {
			t.Fatalf("order %v: dir = %q ok = %v, want one_way", order, dir, ok)
		}
	}
	dir, _, ok := ScopeFabricDirection(
		FabricScope{Key: "main", ByPurpose: true, LineKeys: []string{"A", "C"}}, lines)
	if !ok || dir != FabricDirectionTwoWay {
		t.Fatalf("without a one_way member: dir = %q ok = %v, want two_way", dir, ok)
	}
}

func requireFieldViolation(t *testing.T, err error, field, mentions string) {
	t.Helper()
	var ve *ValidationError
	if !errors.As(err, &ve) {
		t.Fatalf("want a field-tagged ValidationError, got %v", err)
	}
	if ve.Field != field {
		t.Errorf("field = %q, want %q", ve.Field, field)
	}
	if !strings.Contains(ve.Message, mentions) {
		t.Errorf("message %q does not mention %q", ve.Message, mentions)
	}
}
