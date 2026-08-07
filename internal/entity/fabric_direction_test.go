package entity

import (
	"errors"
	"strings"
	"testing"
)

// The rule decides five separate things and each has a way to be wrong: WHETHER the geometry asks a
// question of the cloth at all, WHICH lines are asked (a назначение owns several — 0267), WHAT the
// answer is when they disagree (строгое побеждает), whether the payload is telling the truth about
// its own version (a mirror cannot be legacy), and WHO may claim the grandfather pass. Every case
// below is one of those five.
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
	// gen(v) is the POLICY GENERATION of the row being replaced: 0 means this save creates a marker,
	// and a create has no history to grandfather. It is the only fact the exemption may key on, and
	// the server is the only writer of it.
	gen := func(v int) int { return v }
	// validate drops the name resolver — these lines already carry their names. Its own behaviour is
	// covered by TestValidateMarkerFabricDirectionNamesCatalogueLines.
	validate := func(key string, lines []FabricDirectionLine, facts MarkerLayoutFacts, storedGen int) (bool, error) {
		return ValidateMarkerFabricDirection(key, lines, facts, storedGen, nil)
	}

	t.Run("one_way refuses a v3 layout with a 180° placement", func(t *testing.T) {
		lines := []FabricDirectionLine{line(2, shellA, "main", "Вельвет", "one_way")}
		exempted, err := validate(shellA, lines, v3(true, false), gen(0))
		ve := requireFieldViolation(t, err, "layout.placements", ReasonFlipOnOneWay)
		if exempted {
			t.Error("a refusal cannot also be an exemption")
		}
		// FIX 4 of the review: the blocker is named, not printed as a bare ULID.
		if !strings.Contains(ve.HowToFix, "Вельвет") || !strings.Contains(ve.HowToFix, "180") {
			t.Errorf("how-to-fix must name the cloth and what fired: %q", ve.HowToFix)
		}
		if ve.Conflicting != shellA {
			t.Errorf("conflicting = %q, want the blocking line_key %q", ve.Conflicting, shellA)
		}
	})

	t.Run("one_way refuses a v3 layout with a mirrored placement", func(t *testing.T) {
		lines := []FabricDirectionLine{line(0, shellA, "main", "Вельвет", "one_way")}
		_, err := validate(shellA, lines, v3(false, true), gen(0))
		ve := requireFieldViolation(t, err, "layout.placements", ReasonFlipOnOneWay)
		if !strings.Contains(ve.HowToFix, "зеркальные") {
			t.Errorf("how-to-fix must say a mirror fired: %q", ve.HowToFix)
		}
	})

	// GEOMETRY THAT DECIDES NOTHING must not ask the cloth anything. Almost every stored line has no
	// направление, so demanding it for a layout that is legal on every fabric there is would have
	// stopped renames, re-links and colourway changes on every legacy card — for a field the client
	// of the day could not even set. The safety property is untouched: see the case below it.
	t.Run("a compliant layout saves on cloth of unknown direction", func(t *testing.T) {
		lines := []FabricDirectionLine{line(0, shellA, "main", "Твил 320", "")}
		for _, facts := range []MarkerLayoutFacts{v3(false, false), {SchemaVersion: 1}, {SchemaVersion: 2}} {
			exempted, err := validate(shellA, lines, facts, gen(0))
			if err != nil || exempted {
				t.Fatalf("nothing is upside down, so the direction decides nothing: err = %v exempted = %v", err, exempted)
			}
		}
	})

	t.Run("an upside-down layout still cannot be stored on cloth of unknown direction", func(t *testing.T) {
		lines := []FabricDirectionLine{line(4, shellA, "main", "Твил 320", "")}
		_, err := validate(shellA, lines, v3(true, false), gen(0))
		ve := requireFieldViolation(t, err, "bom_items[4].fabric_direction", ReasonFabricDirectionUnknown)
		// Actionable in both registers: a form path a client can pin, the line_key in the
		// machine-readable slot, the name in the prose.
		if ve.Conflicting != shellA {
			t.Errorf("conflicting = %q, want the line_key %q", ve.Conflicting, shellA)
		}
		if !strings.Contains(ve.HowToFix, "Твил 320") || !strings.Contains(ve.HowToFix, "BOM") {
			t.Errorf("how-to-fix must name the row and the tab: %q", ve.HowToFix)
		}
		if strings.Contains(ve.HowToFix, "назначение") {
			t.Errorf("the line the раскладка names needs no назначение explanation: %q", ve.HowToFix)
		}
		// A mirror is refused the same way, and an old declared version does not buy it through.
		if _, err := validate(shellA, lines, MarkerLayoutFacts{SchemaVersion: 3, HasFlip: true}, gen(0)); err == nil {
			t.Error("a mirrored placement on unknown cloth must be refused too")
		}
	})

	// THE EXEMPTION, and who may claim it. It is spent only where the policy would otherwise refuse,
	// so a legacy row whose geometry is compliant never claims it — and therefore ratchets forward.
	t.Run("only a legacy ROW with forbidden geometry is exempted", func(t *testing.T) {
		lines := []FabricDirectionLine{line(0, shellA, "main", "Вельвет", "one_way")}
		for _, storedGen := range []int{1, 2} {
			// The current client declares v3 unconditionally, so a re-save of a pre-Ф1 row arrives as
			// v3 over a stored 1. The ROW's generation decides, not the payload's.
			exempted, err := validate(shellA, lines, v3(true, false), gen(storedGen))
			if err != nil || !exempted {
				t.Fatalf("stored generation %d: err = %v exempted = %v, want an exemption", storedGen, err, exempted)
			}
		}
		// Compliant geometry on the same legacy row spends nothing: the save is allowed on its own
		// merits, so the caller is free to ratchet the row forward.
		exempted, err := validate(shellA, lines, v3(false, false), gen(1))
		if err != nil || exempted {
			t.Fatalf("compliant geometry must not claim a pass: err = %v exempted = %v", err, exempted)
		}
		// And a generation at or past the current one is no longer legacy.
		if _, err := validate(shellA, lines, v3(true, false), gen(MarkerLayoutSchemaWithFlip)); err == nil {
			t.Error("a row already judged under the current policy must not be exempted")
		}
	})

	// THE HOLE THIS REPLACED, in its original form. The exemption used to key on the version the
	// PAYLOAD declares, and a 180° is expressible in every version — so a brand-new marker opted out
	// of the whole policy by writing `schemaVersion: 1`.
	t.Run("a NEW marker gets no exemption whatever it declares", func(t *testing.T) {
		lines := []FabricDirectionLine{line(0, shellA, "main", "Вельвет", "one_way")}
		for _, v := range []int{1, 2, 3} {
			facts := MarkerLayoutFacts{SchemaVersion: v, HasHalfTurn: true}
			_, err := validate(shellA, lines, facts, gen(0))
			requireFieldViolation(t, err, "layout.placements", ReasonFlipOnOneWay)
		}
	})

	// `flipped` did not exist before schema 3, so a legacy version declaring a mirror is a forgery or
	// a client bug — and it must not be grandfathered by any generation.
	t.Run("a mirror cannot be legacy", func(t *testing.T) {
		lines := []FabricDirectionLine{line(0, shellA, "main", "Вельвет", "one_way")}
		for _, v := range []int{1, 2} {
			_, err := validate(shellA, lines, MarkerLayoutFacts{SchemaVersion: v, HasFlip: true}, gen(v))
			ve := requireFieldViolation(t, err, "layout.placements", ReasonFlipInLegacySchema)
			if !strings.Contains(ve.HowToFix, "flipped") {
				t.Errorf("how-to-fix must name the impossible field: %q", ve.HowToFix)
			}
		}
		// Refused on cloth that would otherwise permit everything: it is a statement about the
		// payload, not about the fabric.
		anyCloth := []FabricDirectionLine{line(0, shellA, "main", "Джерси", "any")}
		_, err := validate(shellA, anyCloth, MarkerLayoutFacts{SchemaVersion: 1, HasFlip: true}, gen(1))
		requireFieldViolation(t, err, "layout.placements", ReasonFlipInLegacySchema)
	})

	// The fix for an unset direction is a mass fill (кампания Д1). Naming one row per round-trip
	// would make a three-row card three saves; the refusal carries them all.
	t.Run("every unset row is named at once", func(t *testing.T) {
		lines := []FabricDirectionLine{
			line(1, shellA, "main", "Твил 320", ""),
			line(3, shellB, "main", "Вельвет", ""),
			line(5, lining, "lining", "Купра", ""),
		}
		_, err := validate(shellA, lines, v3(true, false), gen(0))
		ve := requireFieldViolation(t, err, "bom_items[1].fabric_direction", ReasonFabricDirectionUnknown)
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
			exempted, err := validate(shellA, lines, v3(true, true), gen(1))
			if err != nil {
				t.Errorf("%s must permit 180°/mirror, got %v", dir, err)
			}
			if exempted {
				t.Errorf("%s permits it outright — no pass is spent", dir)
			}
		}
	})

	// СТРОГОЕ ПОБЕЖДАЕТ. The marker names ONE line, but a назначение owns several articles and the
	// same geometry will be cut on whichever one the colourway pins.
	t.Run("strictest wins across a назначение", func(t *testing.T) {
		lines := []FabricDirectionLine{
			line(0, shellA, "main", "Твил гладкий", "any"),
			line(1, shellB, "main", "Вельвет", "one_way"),
			line(2, lining, "lining", "Купра", "any"),
		}
		_, err := validate(shellA, lines, v3(true, false), gen(0))
		ve := requireFieldViolation(t, err, "layout.placements", ReasonFlipOnOneWay)
		if !strings.Contains(ve.HowToFix, "назначение") || !strings.Contains(ve.HowToFix, "Вельвет") {
			t.Errorf("how-to-fix must name the blocking article and why it applies: %q", ve.HowToFix)
		}
		if ve.Conflicting != shellB {
			t.Errorf("conflicting = %q, want the one_way line %q", ve.Conflicting, shellB)
		}
		// The lining is a different назначение and is not dragged in by the strict shell.
		if _, err := validate(lining, lines, v3(true, false), gen(0)); err != nil {
			t.Errorf("another назначение must stay unaffected, got %v", err)
		}
	})

	t.Run("unknown on a sibling line of the назначение blocks it too", func(t *testing.T) {
		lines := []FabricDirectionLine{
			line(0, shellA, "main", "Твил гладкий", "any"),
			line(7, shellB, "main", "Вельвет", ""),
		}
		_, err := validate(shellA, lines, v3(true, false), gen(0))
		ve := requireFieldViolation(t, err, "bom_items[7].fabric_direction", ReasonFabricDirectionUnknown)
		if !strings.Contains(ve.HowToFix, "назначение") {
			t.Errorf("how-to-fix %q must explain why a line the marker does not name is blocking it", ve.HowToFix)
		}
	})

	// СЕМПЛОВАЯ ЯРДАЖА — другой рулон. 0265 made is_sample a flag beside назначение precisely because
	// both live under one purpose; left alone, the sample bolt's ворс would govern production
	// geometry it will never touch.
	t.Run("sample yardage does not govern a production marker", func(t *testing.T) {
		lines := []FabricDirectionLine{
			line(0, shellA, "main", "Твил гладкий", "any"),
			{Index: 1, LineKey: shellB, Purpose: "main", Name: "Твил семпловый", IsSample: true, Direction: "one_way"},
		}
		if _, err := validate(shellA, lines, v3(true, false), gen(0)); err != nil {
			t.Errorf("a one_way SAMPLE roll must not forbid production geometry, got %v", err)
		}
		// …nor does an unset direction on the sample roll block the production save.
		lines[1].Direction = ""
		if _, err := validate(shellA, lines, v3(true, false), gen(0)); err != nil {
			t.Errorf("an unset SAMPLE direction must not block a production marker, got %v", err)
		}
		// And symmetrically: a marker bound to the sample line asks the sample cloth.
		lines[1].Direction = "one_way"
		_, err := validate(shellB, lines, v3(true, false), gen(0))
		requireFieldViolation(t, err, "layout.placements", ReasonFlipOnOneWay)
	})

	t.Run("an unsorted line answers for itself alone", func(t *testing.T) {
		// Pre-0265 state: no назначение anywhere, so the scope is the named line and the one_way
		// neighbour is irrelevant.
		lines := []FabricDirectionLine{
			line(0, unsized, "", "Твил гладкий", "any"),
			line(1, shellB, "", "Вельвет", "one_way"),
		}
		if _, err := validate(unsized, lines, v3(true, false), gen(0)); err != nil {
			t.Errorf("an unsorted card must resolve line-by-line, got %v", err)
		}
	})

	t.Run("an unlinked marker stays saveable", func(t *testing.T) {
		lines := []FabricDirectionLine{line(0, shellA, "main", "Вельвет", "")}
		exempted, err := validate("", lines, v3(true, true), gen(1))
		if err != nil {
			t.Errorf("no bom_line_key means no cloth to ask about, got %v", err)
		}
		if exempted {
			t.Error("nothing was judged, so nothing was exempted")
		}
	})

	t.Run("a dangling binding stays saveable", func(t *testing.T) {
		// The line was deleted or reclassified out of roll goods after the marker was measured.
		// «Слот удалён» is a UI state, not a reason to strand a stored measurement.
		lines := []FabricDirectionLine{line(0, shellB, "main", "Вельвет", "one_way")}
		if _, err := validate("01FDGONELINE000000000000X1", lines, v3(true, true), gen(0)); err != nil {
			t.Errorf("a dangling binding must not block the save, got %v", err)
		}
	})

	t.Run("a value outside the vocabulary reads as unknown", func(t *testing.T) {
		// Unreachable through the app (chk on 0073), and if it ever becomes reachable the
		// fail-closed answer is the safe one: ask the operator rather than assume «flip allowed».
		lines := []FabricDirectionLine{line(0, shellA, "main", "Вельвет", "diagonal")}
		_, err := validate(shellA, lines, v3(true, false), gen(0))
		requireFieldViolation(t, err, "bom_items[0].fabric_direction", ReasonFabricDirectionUnknown)
	})

	// The zero value of MarkerLayoutFacts is the one that would sail through every check, so it must
	// not be reachable by forgetting to distil the blob. That is a server bug, not the operator's —
	// so it is NOT a field violation.
	t.Run("undistilled facts are refused, not skipped", func(t *testing.T) {
		lines := []FabricDirectionLine{line(0, shellA, "main", "Вельвет", "one_way")}
		_, err := validate(shellA, lines, MarkerLayoutFacts{}, gen(0))
		if err == nil {
			t.Fatal("a linked marker must not be judged on facts nobody filled")
		}
		var ve *ValidationError
		if errors.As(err, &ve) {
			t.Errorf("a wiring failure must not read as a field violation: %v", ve)
		}
	})
}

// A catalogue-linked BOM line legitimately carries no name of its own and shows the material's on the
// BOM tab. Resolving that costs a join the save path deliberately keeps out of its SERIALIZABLE
// transaction, so it happens through a callback — and only while a refusal is being built.
func TestValidateMarkerFabricDirectionNamesCatalogueLines(t *testing.T) {
	const key = "01FDCATALOGUELINE0000000C1"
	lines := []FabricDirectionLine{{Index: 3, LineKey: key, Purpose: "main", Direction: ""}}

	calls := 0
	namer := func(keys []string) map[string]string {
		calls++
		if len(keys) != 1 || keys[0] != key {
			t.Errorf("resolver asked for %v, want just the nameless line", keys)
		}
		return map[string]string{key: "ВЕЛЬВЕТ ИЗ КАТАЛОГА"}
	}

	// A save that is allowed must not touch the catalogue at all: the whole point of the callback is
	// that the join is paid for only by a request that is already failing.
	if _, err := ValidateMarkerFabricDirection(key, lines,
		MarkerLayoutFacts{SchemaVersion: 3}, 0, namer); err != nil {
		t.Fatalf("compliant geometry must save: %v", err)
	}
	if calls != 0 {
		t.Errorf("resolver ran %d times on a successful save, want 0", calls)
	}

	_, err := ValidateMarkerFabricDirection(key, lines,
		MarkerLayoutFacts{SchemaVersion: 3, HasHalfTurn: true}, 0, namer)
	ve := requireFieldViolation(t, err, "bom_items[3].fabric_direction", ReasonFabricDirectionUnknown)
	if !strings.Contains(ve.HowToFix, "ВЕЛЬВЕТ ИЗ КАТАЛОГА") {
		t.Errorf("the refusal must speak the name the BOM tab shows: %q", ve.HowToFix)
	}
	if calls != 1 {
		t.Errorf("resolver ran %d times, want exactly 1", calls)
	}

	// Without a resolver the refusal still happens; it falls back to the key rather than going quiet.
	_, err = ValidateMarkerFabricDirection(key, lines,
		MarkerLayoutFacts{SchemaVersion: 3, HasHalfTurn: true}, 0, nil)
	ve = requireFieldViolation(t, err, "bom_items[3].fabric_direction", ReasonFabricDirectionUnknown)
	if !strings.Contains(ve.HowToFix, key) {
		t.Errorf("with no resolver the key is the fallback label: %q", ve.HowToFix)
	}
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

	// A scope key with no line behind it is unreachable while scope keys are built out of these same
	// lines — and it must fail CLOSED anyway, like the invalid-vocabulary branch beside it. Skipping
	// it would answer «any» for a line nobody can look at: allow-everything on absent evidence.
	_, unknown, ok := ScopeFabricDirection(
		FabricScope{Key: "main", ByPurpose: true, LineKeys: []string{"A", "GHOST"}}, lines)
	if ok {
		t.Fatal("a scope key with no line must not resolve")
	}
	if len(unknown) != 1 || unknown[0].LineKey != "GHOST" {
		t.Fatalf("unknown = %v, want the unresolvable key named", unknown)
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
