package entity

import (
	"fmt"
	"strings"
)

// НАПРАВЛЕНИЕ ТКАНИ as the раскладка sees it (Ф1): which cloth a marker is laid on, what direction
// governs that cloth when the scope owns several BOM lines, and which placements the answer forbids.
//
// fabric_direction has been on tech_card_bom_item since 0073 and until now fed nothing but the
// MATERIALS digest. It decides exactly one physical thing: may a piece be put on the cloth the wrong
// way UP. On ворс, twill or a directional print it may not — and «wrong way up» is a 180° turn AND a
// mirror (the `flipped` of schema 3), which is why the two are refused together and why 90° is not
// in this rule at all: cross-grain is a different question, and allow_cross_grain already owns it.
//
// NULL IS NOT A FOURTH VALUE. It is UNKNOWN, and it is what almost every stored line carries,
// because nothing ever made the field mandatory. Both readings of UNKNOWN are wrong: «flip allowed»
// makes the guard decorative on exactly the cards that need it, «flip forbidden» stops markers that
// reproduce perfectly well today. So UNKNOWN blocks the SAVE and says where to fix it — the field
// becomes required precisely where it decides something, and nowhere else.

// MarkerLayoutSchemaWithFlip is the first blob version whose placements can express a mirror, and
// therefore the first version the rotation policy may judge. See ValidateMarkerFabricDirection.
const MarkerLayoutSchemaWithFlip = 3

// FabricDirectionLine is one roll-goods BOM line as this rule reads it: both halves of the binding
// scope (line key + назначение, 0265/0267), the name a refusal has to say out loud so an operator
// knows which row to open, and the direction — where "" is the column's NULL, i.e. UNKNOWN.
type FabricDirectionLine struct {
	LineKey   string
	Purpose   string
	Name      string
	Direction string
}

// label names the line twice over, and on purpose: the NAME is what an operator scans the BOM tab
// for, the line_key is what a client needs to deep-link straight to that row. A refusal carrying
// only the name would make the fix a search; only the key would make it unreadable.
func (l FabricDirectionLine) label() string {
	name, key := strings.TrimSpace(l.Name), strings.TrimSpace(l.LineKey)
	switch {
	case name == "":
		return key
	case key == "":
		return fmt.Sprintf("%q", name)
	default:
		return fmt.Sprintf("%q (%s)", name, key)
	}
}

// MarkerLayoutFacts is what the API layer distils out of the marker's layout blob for this rule.
// The blob is opaque past that layer by design (0257: the store stores it and never parses it), so
// the two bits the store must DECIDE on travel as facts instead of as a second parser in the store.
type MarkerLayoutFacts struct {
	// SchemaVersion of the blob being saved, as normalised by the API layer (an unset 0 reads as 1).
	SchemaVersion int
	// HasHalfTurn: at least one placement sits at rot_deg 180.
	HasHalfTurn bool
	// HasFlip: at least one placement is mirrored (schema 3+).
	HasFlip bool
}

// MarkerFabricScope resolves the cloth a marker's bom_line_key addresses, through the ONE binding
// rule (ResolveFabricScope): the назначение of the named line where the card has been sorted, that
// line alone where it has not.
//
// A marker names a LINE and not a назначение, and that is right — its width and кромка come off the
// article that line pins (0259/0264). The DIRECTION question is nevertheless asked of the whole
// назначение, because that is the set of lines the same лекала get cut from, and 0267 moved the
// sheet↔cloth binding there. Nobody validates that the lines under one назначение agree.
func MarkerFabricScope(bomLineKey string, lines []FabricDirectionLine) FabricScope {
	key := strings.TrimSpace(bomLineKey)
	if key == "" {
		return FabricScope{}
	}
	rollGoods := make([]RollGoodsLine, 0, len(lines))
	purpose := ""
	for _, l := range lines {
		rollGoods = append(rollGoods, RollGoodsLine{LineKey: l.LineKey, Purpose: l.Purpose})
		if strings.EqualFold(strings.TrimSpace(l.LineKey), key) {
			purpose = l.Purpose
		}
	}
	return ResolveFabricScope(purpose, key, rollGoods)
}

// fabricDirectionStrictness orders the values by how much they claim about the cloth, so «strictest
// wins» is a max and not a chain of ifs. Only the top rank changes what is ALLOWED — two_way and any
// both permit the piece upside down — but two_way outranks any so the fold returns a truthful
// summary of the scope rather than «any unless somebody said one_way»: the engine config (Ф1.4) and
// the UI read this answer too, and «any» there would understate what the лаборант actually recorded.
func fabricDirectionStrictness(d TechCardFabricDirection) int {
	switch d {
	case FabricDirectionOneWay:
		return 2
	case FabricDirectionTwoWay:
		return 1
	default:
		return 0
	}
}

// ScopeFabricDirection answers which direction governs a whole scope. СТРОГОЕ ПОБЕЖДАЕТ: one
// one_way line forbids the flip for every line under the same назначение, because the marker is one
// piece of geometry and it will be cut on whichever of those articles the colourway pins.
//
// ok is false when ANY line of the scope is UNKNOWN, and the line it names is the first such in card
// order: one unset row makes the whole answer a guess, and a guess is what this rule refuses to make.
// A value outside the closed vocabulary counts as UNKNOWN too — the DB CHECK makes that unreachable
// through the app, and if it ever becomes reachable the fail-closed answer is the safe one.
func ScopeFabricDirection(scope FabricScope, lines []FabricDirectionLine) (TechCardFabricDirection, FabricDirectionLine, bool) {
	byKey := make(map[string]FabricDirectionLine, len(lines))
	for _, l := range lines {
		byKey[strings.ToLower(strings.TrimSpace(l.LineKey))] = l
	}
	strictest := FabricDirectionAny
	for _, key := range scope.LineKeys {
		l, ok := byKey[strings.ToLower(strings.TrimSpace(key))]
		if !ok {
			continue
		}
		dir := TechCardFabricDirection(strings.ToLower(strings.TrimSpace(l.Direction)))
		if !ValidTechCardFabricDirections[dir] {
			return "", l, false
		}
		if fabricDirectionStrictness(dir) > fabricDirectionStrictness(strictest) {
			strictest = dir
		}
	}
	return strictest, FabricDirectionLine{}, true
}

// ValidateMarkerFabricDirection is the whole marker-side rule in one place (Ф1.5 + Ф1.6): a marker
// may not be saved onto cloth whose direction nobody set, and a NEW layout may not put a piece
// upside down on cloth that is directional. lines are the card's roll-goods BOM lines; facts are the
// layout distilled by the API layer.
func ValidateMarkerFabricDirection(bomLineKey string, lines []FabricDirectionLine, facts MarkerLayoutFacts) error {
	scope := MarkerFabricScope(bomLineKey, lines)
	if !scope.Live() {
		// An UNLINKED marker — no bom_line_key at all — stays saveable, and must: it is geometry
		// with no cloth attached, and there is nothing to ask the direction of. A binding that
		// DANGLES reads the same way: its line was deleted or reclassified out of roll goods, which
		// is a UI state («слот удалён»), not an error, and refusing it here would strand markers on
		// an attribution their operator can no longer reach.
		return nil
	}
	dir, unknown, known := ScopeFabricDirection(scope, lines)
	if !known {
		reason := fmt.Sprintf("BOM line %s has no направление ткани", unknown.label())
		if scope.ByPurpose && !strings.EqualFold(strings.TrimSpace(unknown.LineKey), strings.TrimSpace(bomLineKey)) {
			// Said only when it is surprising: the operator is being sent to a row this раскладка
			// does not name, and without this clause that reads as a bug rather than as назначение
			// doing its job.
			reason += fmt.Sprintf(" — it shares назначение %q with the line this раскладка is bound to", scope.Key)
		}
		return NewFieldViolation("bom_items.fabric_direction", reason, "",
			"set направление ткани (any / one_way / two_way) on that line on the BOM tab — while it "+
				"is unknown the server cannot tell a harmless 180° from a ruined ворс, so the раскладка is not saved")
	}
	if facts.SchemaVersion < MarkerLayoutSchemaWithFlip {
		// GRANDFATHERING, and it is the whole reason the version is inspected here. Stored markers
		// legitimately carry rotations outside today's policy: the manual editor saves the rotation
		// a piece ACTUALLY has, so 90° at allow_cross_grain=false is on file, and 180° with it.
		// Judging an old blob by the new rule would refuse every one of those the moment its card
		// gets a направление — measurements nobody can re-take without re-nesting, invalidated
		// retroactively by a rule that did not exist when they were taken. Only a blob that can
		// express `flipped` came from a client that knows the policy, so only that one is held to it.
		return nil
	}
	if dir != FabricDirectionOneWay {
		// two_way and any both allow the piece upside down. The binary «directional ⇒ forbidden»
		// would be wrong here: two_way is exactly the cloth that permits the half-turn.
		return nil
	}
	if !facts.HasHalfTurn && !facts.HasFlip {
		return nil
	}
	return NewFieldViolation("layout.placements",
		fmt.Sprintf("%s on one_way cloth (%s)", offendingPlacements(facts), scopeLabel(scope)), "",
		"на направленной ткани деталь нельзя класть вверх ногами: пересоберите раскладку без 180° и "+
			"без зеркальных размещений, либо исправьте направление ткани на вкладке BOM, если ткань на самом деле не направленная")
}

// offendingPlacements names which half of the ban fired. «180° and mirrored» and «mirrored» send an
// operator to two different controls of the editor, so a single generic wording would cost a search.
func offendingPlacements(facts MarkerLayoutFacts) string {
	switch {
	case facts.HasHalfTurn && facts.HasFlip:
		return "placements turned 180° and mirrored placements"
	case facts.HasHalfTurn:
		return "placements turned 180°"
	default:
		return "mirrored placements"
	}
}

// scopeLabel says WHICH cloth answered, in the operator's vocabulary: a назначение when the card has
// been sorted — and then how many lines hang off it, because that is what explains a refusal caused
// by a row the раскладка does not name — the BOM line itself when it has not.
func scopeLabel(scope FabricScope) string {
	if scope.ByPurpose {
		if len(scope.LineKeys) > 1 {
			return fmt.Sprintf("назначение %q, %d BOM lines", scope.Key, len(scope.LineKeys))
		}
		return fmt.Sprintf("назначение %q", scope.Key)
	}
	return fmt.Sprintf("BOM line %s", scope.Key)
}
