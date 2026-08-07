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
	// Index is the line's position in the card's bom_items AS THE CLIENT SEES THEM — the card read
	// orders by (display_order, id), so that is the order the form array is built in. It exists for
	// one reason: a field violation must be `bom_items[i].fabric_direction`, and an index taken over
	// roll goods alone would pin the error on whichever row happens to sit at that position in the
	// full list (a thread, a button) — confidently, and on the wrong control.
	Index   int
	LineKey string
	Purpose string
	Name    string
	// IsSample marks the yardage the ОБРАЗЕЦ is sewn from (0265). It is a flag beside назначение,
	// not a value of it, precisely because a card carries a production main fabric AND a sample main
	// fabric under the same purpose — which makes it this rule's business: see MarkerFabricScope.
	IsSample  bool
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
	// SchemaVersion of the blob being saved, as normalised by the API layer (an unset 0 reads as 1),
	// so a ZERO here never means «v1» — it means nobody distilled the blob at all. The rule refuses
	// on it rather than skipping: this struct's zero value grants the policy exemption, and a
	// default that exempts must not be reachable by forgetting a line of wiring.
	SchemaVersion int
	// HasHalfTurn: at least one placement sits at rot_deg 180 (after normalisation — -180 and 540
	// are the same half-turn and are counted as one).
	HasHalfTurn bool
	// HasFlip: at least one placement is mirrored (schema 3+).
	HasFlip bool
}

// FlipPredatesSchema reports a layout that carries a MIRRORED placement while declaring a version
// that could not express one. `flipped` is unforgeable: the field did not exist before schema 3, so
// no stored blob can contain it and no honest client can write one under an older version. Such a
// blob is a forgery or a client bug, and it must never be treated as legacy — the whole point of the
// version gate is that legacy geometry is EXEMPT from the rotation policy, and an exemption you can
// claim by writing a smaller number is not a gate.
//
// One predicate, two enforcement points: the API refuses it for every marker (an unlinked раскладка
// has no cloth to check but its blob must still not lie about its own format), and the rule below
// refuses it again before granting the exemption, so the exemption stays honest for any caller.
func FlipPredatesSchema(f MarkerLayoutFacts) bool {
	return f.HasFlip && f.SchemaVersion > 0 && f.SchemaVersion < MarkerLayoutSchemaWithFlip
}

// StoredMarkerFacts yields the facts of the layout this save REPLACES — the geometry already on
// file. It is a function, not a value, because reading it costs the stored blob and a parse, and the
// exemption below is the only thing that ever asks: a save that introduces nothing forbidden never
// needs to know what the old row looked like.
//
// ok=false means the stored geometry could not be read (no stored row, an unparsable blob, or a
// caller that did not wire the distiller). It is NOT «no forbidden geometry»; the two must not be
// confused, and the exemption treats it as «cannot forgive» — see ValidateMarkerFabricDirection.
type StoredMarkerFacts func() (MarkerLayoutFacts, bool)

// MarkerDirectionVerdict is what the rule tells its caller beyond «refused or not». Both fields feed
// exactly one decision — what generation the row should carry afterwards — and both are needed
// because «nothing was judged» and «judged and allowed» must not write the same thing.
type MarkerDirectionVerdict struct {
	// Judged: this geometry was actually held against the policy. False only when the marker has no
	// live cloth scope at all, i.e. nothing about направление could be decided either way.
	Judged bool
	// Exempted: the policy would have refused, and the row's pre-Ф1 pass forgave it — which it may
	// only do for geometry that was ALREADY THERE.
	Exempted bool
}

// introducesForbidden reports whether the incoming layout adds an upside-down property the stored
// layout did not already have. This is the whole scope of the exemption: it forgives what is on file,
// never what this very save is putting there.
//
// Property subset, not placement identity: «this row already had a half-turn» is what the pass is
// about, so swapping WHICH piece is turned stays forgiven. Narrowing that further would need the
// stored placements compared one by one, and would make a rename fail whenever the client re-emitted
// the blob in a different order — the exact cost the pass exists to avoid.
func introducesForbidden(incoming, stored MarkerLayoutFacts) bool {
	return (incoming.HasHalfTurn && !stored.HasHalfTurn) || (incoming.HasFlip && !stored.HasFlip)
}

// MarkerFabricScope resolves the cloth a marker's bom_line_key addresses, through the ONE binding
// rule (ResolveFabricScope): the назначение of the named line where the card has been sorted, that
// line alone where it has not.
//
// A marker names a LINE and not a назначение, and that is right — its width and кромка come off the
// article that line pins (0259/0264). The DIRECTION question is nevertheless asked of the whole
// назначение, because that is the set of lines the same лекала get cut from, and 0267 moved the
// sheet↔cloth binding there. Nobody validates that the lines under one назначение agree.
//
// СЕМПЛОВАЯ ЯРДАЖА В СКОУП НЕ ВХОДИТ, and this had to be decided rather than inherited: 0265's
// stated reason for is_sample being a flag rather than a purpose is that one card carries a
// production main fabric AND a sample main fabric under the SAME назначение. Left alone, an unset
// direction on the sample roll would have blocked the PRODUCTION marker, and a one_way sample roll
// would have imposed its policy on production geometry — of cloth that is physically a different
// bolt, bought for a different purpose and often not even the same quality.
//
// So the scope keeps only lines on the same side of that flag as the line the marker names:
// production markers ask production cloth, sample markers ask sample cloth. Symmetric on purpose —
// the argument «the sample bolt is a different bolt» reads identically in both directions, and an
// asymmetric rule would let a production roll's ворс govern a раскладка that will never touch it.
func MarkerFabricScope(bomLineKey string, lines []FabricDirectionLine) FabricScope {
	key := strings.TrimSpace(bomLineKey)
	if key == "" {
		return FabricScope{}
	}
	purpose, sample := "", false
	for _, l := range lines {
		if strings.EqualFold(strings.TrimSpace(l.LineKey), key) {
			purpose, sample = l.Purpose, l.IsSample
			break
		}
	}
	rollGoods := make([]RollGoodsLine, 0, len(lines))
	for _, l := range lines {
		if l.IsSample != sample {
			continue
		}
		rollGoods = append(rollGoods, RollGoodsLine{LineKey: l.LineKey, Purpose: l.Purpose})
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
// unknown collects EVERY line of the scope whose direction is unset, not the first one: the fix for
// this refusal is a mass fill (кампания Д1), and naming one row at a time would make a three-row
// card three round-trips. It is also the only deterministic shape — «the first» would depend on row
// order, and the loader's ORDER BY is a promise made in one query, not by the rule.
//
// A value outside the closed vocabulary counts as UNKNOWN too — the DB CHECK makes that unreachable
// through the app, and if it ever becomes reachable the fail-closed answer is the safe one.
func ScopeFabricDirection(scope FabricScope, lines []FabricDirectionLine) (TechCardFabricDirection, []FabricDirectionLine, bool) {
	byKey := make(map[string]FabricDirectionLine, len(lines))
	for _, l := range lines {
		byKey[strings.ToLower(strings.TrimSpace(l.LineKey))] = l
	}
	strictest := FabricDirectionAny
	var unknown []FabricDirectionLine
	for _, key := range scope.LineKeys {
		l, ok := byKey[strings.ToLower(strings.TrimSpace(key))]
		if !ok {
			// Unreachable today — scope keys are built out of these same lines — and it stays
			// unreachable only as long as that construction holds. A skip here would answer «any»
			// for a line nobody could look at, i.e. would allow everything on evidence it does not
			// have; the invalid-vocabulary branch below already chose the opposite default for the
			// same situation, and two branches of one function must not disagree about it.
			unknown = append(unknown, FabricDirectionLine{LineKey: key})
			continue
		}
		dir := TechCardFabricDirection(strings.ToLower(strings.TrimSpace(l.Direction)))
		if !ValidTechCardFabricDirections[dir] {
			unknown = append(unknown, l)
			continue
		}
		if fabricDirectionStrictness(dir) > fabricDirectionStrictness(strictest) {
			strictest = dir
		}
	}
	if len(unknown) > 0 {
		return "", unknown, false
	}
	return strictest, nil, true
}

// scopeLinesWith returns the lines of the scope carrying a given direction, in scope order. Used to
// NAME the blocker in a refusal: «this ткань is one_way» is actionable, «this scope resolved to
// one_way» is a fact about the server.
func scopeLinesWith(scope FabricScope, lines []FabricDirectionLine, want TechCardFabricDirection) []FabricDirectionLine {
	byKey := make(map[string]FabricDirectionLine, len(lines))
	for _, l := range lines {
		byKey[strings.ToLower(strings.TrimSpace(l.LineKey))] = l
	}
	var out []FabricDirectionLine
	for _, key := range scope.LineKeys {
		if l, ok := byKey[strings.ToLower(strings.TrimSpace(key))]; ok &&
			TechCardFabricDirection(strings.ToLower(strings.TrimSpace(l.Direction))) == want {
			out = append(out, l)
		}
	}
	return out
}

// labels / keys render a set of lines for the two halves of a violation: prose names them, the
// machine-readable Conflicting slot carries their line_keys.
func labelsOf(lines []FabricDirectionLine) string {
	out := make([]string, 0, len(lines))
	for _, l := range lines {
		out = append(out, l.label())
	}
	return strings.Join(out, ", ")
}

func keysOf(lines []FabricDirectionLine) string {
	out := make([]string, 0, len(lines))
	for _, l := range lines {
		out = append(out, strings.TrimSpace(l.LineKey))
	}
	return strings.Join(out, ", ")
}

// namesOtherThan reports whether any of these lines is NOT the one the раскладка names — the only
// case where a refusal has to explain назначение, because the operator is being sent to a row this
// marker does not mention and would otherwise read that as a bug.
func namesOtherThan(lines []FabricDirectionLine, bomLineKey string) bool {
	for _, l := range lines {
		if !strings.EqualFold(strings.TrimSpace(l.LineKey), strings.TrimSpace(bomLineKey)) {
			return true
		}
	}
	return false
}

// Stable machine-readable reason codes of the refusals below. A client switches on these; the prose
// beside them is for the human and may be reworded freely.
const (
	// ReasonFabricDirectionUnknown — the cloth this раскладка is cut from has no направление set.
	ReasonFabricDirectionUnknown = "direction_unknown"
	// ReasonFlipOnOneWay — a v3 layout puts a piece upside down (180° or mirrored) on one_way cloth.
	ReasonFlipOnOneWay = "flip_on_one_way"
	// ReasonFlipInLegacySchema — a mirrored placement in a blob declaring a version that predates
	// the field. Not a policy refusal: the payload is impossible.
	ReasonFlipInLegacySchema = "flip_in_legacy_schema"
)

// FabricLineNamer resolves display names for cloth lines that carry none of their own — a line linked
// to a catalogue material legitimately has an empty name and shows the material's on the BOM tab.
//
// It is a CALLBACK rather than a column of FabricDirectionLine because resolving it costs a join on
// `material`, and the marker save runs inside a SERIALIZABLE transaction where InnoDB promotes a
// plain SELECT to FOR SHARE: joining the catalogue on the hot path would let a concurrent material
// edit block — or deadlock — an unrelated раскладка save, surfacing as a 500 rather than as anything
// retryable. So it is called at most once, and only while a refusal is already being written, i.e.
// on a transaction that is about to roll back anyway. nil means «the lines already carry their
// names» (unit tests, and any caller that resolved them itself).
type FabricLineNamer func(lineKeys []string) map[string]string

// named fills in the display names of lines that have none, through the caller's resolver.
func named(lines []FabricDirectionLine, resolve FabricLineNamer) []FabricDirectionLine {
	if resolve == nil {
		return lines
	}
	var missing []string
	for _, l := range lines {
		if strings.TrimSpace(l.Name) == "" && strings.TrimSpace(l.LineKey) != "" {
			missing = append(missing, l.LineKey)
		}
	}
	if len(missing) == 0 {
		return lines
	}
	byKey := resolve(missing)
	out := make([]FabricDirectionLine, len(lines))
	copy(out, lines)
	for i := range out {
		if strings.TrimSpace(out[i].Name) == "" {
			out[i].Name = byKey[out[i].LineKey]
		}
	}
	return out
}

// ValidateMarkerFabricDirection is the whole marker-side rule in one place (Ф1.5 + Ф1.6): a layout
// may not put a piece upside down on cloth that is directional, and it may not do so on cloth whose
// direction nobody has set — because unknown is not an answer. lines are the card's roll-goods BOM
// lines IN CARD ORDER (their Index is used verbatim in the field path); facts are the incoming
// layout distilled by the API layer; storedGeneration is the POLICY GENERATION of the row being
// replaced (0 when this save creates one); storedFacts yields the geometry that row already carries.
//
// Order of the checks is deliberate, and two of them were in the wrong place before:
//
//  1. what is wrong with the PAYLOAD (undistilled facts, a mirror that cannot exist under its
//     declared version) — reversed, an operator would be sent to fill in the BOM tab to satisfy a
//     request that was malformed anyway;
//  2. GEOMETRY THAT DECIDES NOTHING passes here, before any question about the cloth. A layout with
//     no half-turn and no mirror is legal on every direction there is, so the direction cannot
//     change its verdict — and demanding the field anyway contradicted this file's own rule that it
//     becomes required «precisely where it decides something, and nowhere else». It also had a
//     price: almost every stored line has no direction, so routine saves (rename, re-link, change
//     the colourway) would have stopped dead on every legacy card, with a violation the client of
//     the day could not even act on. The safety property is untouched — an upside-down placement
//     still can never be stored on cloth of unknown direction, which is the case that reaches the
//     unknown check below;
//  3. only then the cloth: unknown, then the policy, then the exemption.
func ValidateMarkerFabricDirection(bomLineKey string, lines []FabricDirectionLine, facts MarkerLayoutFacts,
	storedGeneration int, storedFacts StoredMarkerFacts, resolveNames FabricLineNamer) (MarkerDirectionVerdict, error) {
	scope := MarkerFabricScope(bomLineKey, lines)
	if !scope.Live() {
		// An UNLINKED marker — no bom_line_key at all — stays saveable, and must: it is geometry
		// with no cloth attached, and there is nothing to ask the direction of. A binding that
		// DANGLES reads the same way: its line was deleted or reclassified out of roll goods, which
		// is a UI state («слот удалён»), not an error, and refusing it here would strand markers on
		// an attribution their operator can no longer reach.
		//
		// Judged stays FALSE, and that is not a formality: nothing here held this geometry against
		// any cloth, so the caller must not stamp the row as judged under the current policy. Before
		// the exemption was scoped to stored geometry that distinction was unaffordable — the
		// generation was the only defence, so it had to ratchet on every save that did not spend a
		// pass, and unlinking a legacy marker silently cost it one. Now the subset rule below stops
		// new forbidden geometry on its own, so the column is free to mean exactly what it says.
		return MarkerDirectionVerdict{}, nil
	}
	if facts.SchemaVersion <= 0 {
		// Not a validation failure — a wiring failure, and it must not read as the operator's
		// problem. The API layer normalises an absent version to 1, so a zero reaching here means
		// the blob was never distilled, and the zero value of MarkerLayoutFacts is precisely the one
		// that would sail through every check below. A default that exempts has to be unreachable.
		return MarkerDirectionVerdict{}, fmt.Errorf("marker layout facts were not distilled (schema_version 0) — "+
			"refusing to judge a раскладка linked to %s on inputs nobody filled", scope.Key)
	}
	if FlipPredatesSchema(facts) {
		return MarkerDirectionVerdict{Judged: true}, NewFieldViolation("layout.placements", ReasonFlipInLegacySchema, "",
			fmt.Sprintf("the layout declares schema_version %d but carries a mirrored placement, and "+
				"`flipped` only exists from version %d on — no stored раскладка can contain one, so this is a "+
				"client writing a version it does not actually speak; save it as version %d",
				facts.SchemaVersion, MarkerLayoutSchemaWithFlip, MarkerLayoutSchemaWithFlip))
	}
	if !facts.HasHalfTurn && !facts.HasFlip {
		// Nothing in this layout is upside down, so no direction — known, unknown or contradictory —
		// can refuse it. See (2) above: this is the check whose position is the difference between a
		// guard and a card-wide блокировка. Judged: the policy did look at this geometry and found
		// nothing to forbid, so the row has earned the current generation.
		return MarkerDirectionVerdict{Judged: true}, nil
	}
	dir, unknown, known := ScopeFabricDirection(scope, lines)
	if !known {
		unknown = named(unknown, resolveNames)
		howToFix := fmt.Sprintf("set направление ткани (any / one_way / two_way) on the BOM tab for %s", labelsOf(unknown))
		if scope.ByPurpose && namesOtherThan(unknown, bomLineKey) {
			// Said only when it is surprising: the operator is being sent to a row this раскладка
			// does not name, and without this clause that reads as a bug rather than as назначение
			// doing its job.
			howToFix += fmt.Sprintf(" (they hang off назначение %q together with the line this раскладка is bound to)", scope.Key)
		}
		howToFix += " — эта раскладка кладёт деталь вверх ногами, и пока направление неизвестно, " +
			"сервер не может отличить безобидные 180° от испорченного ворса"
		// The field pins the FIRST offending row so a form can focus something; the prose and the
		// conflicting keys carry all of them, because the fix is a mass fill, not one row.
		return MarkerDirectionVerdict{Judged: true}, NewFieldViolation(
			fmt.Sprintf("bom_items[%d].fabric_direction", unknown[0].Index),
			ReasonFabricDirectionUnknown, keysOf(unknown), howToFix)
	}
	if dir != FabricDirectionOneWay {
		// two_way and any both allow the piece upside down. The binary «directional ⇒ forbidden»
		// would be wrong here: two_way is exactly the cloth that permits the half-turn.
		return MarkerDirectionVerdict{Judged: true}, nil
	}
	// From here the policy REFUSES, and the pre-Ф1 pass is the only thing that can change that.
	if storedGeneration > 0 && storedGeneration < MarkerLayoutSchemaWithFlip && !introducesForbidden(facts, storedFactsOf(storedFacts)) {
		// GRANDFATHERING, and its scope is the point. Two facts have to hold together, and neither
		// is writable by the caller:
		//
		//   • the ROW predates the policy (its generation, written only by the server); and
		//   • this save INTRODUCES nothing forbidden that the stored geometry did not already carry.
		//
		// The second condition is what was missing. Keyed on the verdict alone, the pass fired
		// whenever the policy would have refused — including when the operator was adding the 180°
		// right then — and, because an exempted save deliberately holds the generation back, the row
		// stayed exemptible forever after. A legacy marker with perfectly compliant geometry could be
		// turned upside down on ворс in two clicks and keep the licence to do it again: the ratchet
		// defeated by the exact action it exists to prevent. The client made that trivial in
		// practice, offering 180° on every reopened marker regardless of the cloth.
		//
		// What the pass is FOR: geometry ALREADY ON FILE. The manual editor saved the rotation a
		// piece actually had, so 90° at allow_cross_grain=false is on file and 180° with it, and
		// refusing those the moment their card gets a направление would invalidate measurements
		// nobody can re-take without re-nesting. Such a row stays renameable and re-linkable
		// indefinitely — and now ONLY that.
		//
		// KNOWN AND DELIBERATE RESIDUAL, now much smaller: the comparison is by PROPERTY, so a row
		// that already carries a half-turn may have a different piece turned instead. Closing that
		// needs placement-by-placement comparison against the stored blob, which would fail a rename
		// whenever the client re-emitted the geometry in another order.
		return MarkerDirectionVerdict{Judged: true, Exempted: true}, nil
	}
	blockers := named(scopeLinesWith(scope, lines, FabricDirectionOneWay), resolveNames)
	howToFix := fmt.Sprintf("%s: %s помечена one_way", offendingPlacements(facts), labelsOf(blockers))
	if scope.ByPurpose && namesOtherThan(blockers, bomLineKey) {
		howToFix += fmt.Sprintf(" (через назначение %q)", scope.Key)
	}
	howToFix += " — на направленной ткани деталь нельзя класть вверх ногами: пересоберите раскладку без 180° и " +
		"без зеркальных размещений, либо исправьте направление ткани на вкладке BOM, если ткань на самом деле не направленная"
	return MarkerDirectionVerdict{Judged: true}, NewFieldViolation("layout.placements", ReasonFlipOnOneWay, keysOf(blockers), howToFix)
}

// storedFactsOf reads the geometry on file, and answers «everything is forbidden already» ONLY when
// it genuinely read it. A stored blob that cannot be distilled — unparsable, absent, or a caller that
// never wired the distiller — yields facts with nothing set, so introducesForbidden() sees every
// upside-down property in the payload as NEW and the pass is not granted.
//
// THAT DIRECTION IS DELIBERATE, and it is the opposite of what «be lenient with damaged data» would
// suggest. The read path degrades on an unreadable blob by design (GetTechCardMarker serves the
// summary plus a warning and NO geometry), which means a client cannot have loaded that marker's
// placements — so whatever upside-down geometry arrives in the payload cannot be a copy of what is on
// file. It is new by construction, and new geometry is exactly what the pass may not forgive.
// Refusing costs the operator only the forbidden save: renaming, re-linking or re-nesting the same
// marker with compliant geometry all still work, so nothing is stranded.
func storedFactsOf(storedFacts StoredMarkerFacts) MarkerLayoutFacts {
	if storedFacts == nil {
		return MarkerLayoutFacts{}
	}
	f, ok := storedFacts()
	if !ok {
		return MarkerLayoutFacts{}
	}
	return f
}

// offendingPlacements names which half of the ban fired. «180° and mirrored» and «mirrored» send an
// operator to two different controls of the editor, so a single generic wording would cost a search.
func offendingPlacements(facts MarkerLayoutFacts) string {
	switch {
	case facts.HasHalfTurn && facts.HasFlip:
		return "раскладка несёт размещения на 180° и зеркальные"
	case facts.HasHalfTurn:
		return "раскладка несёт размещения на 180°"
	default:
		return "раскладка несёт зеркальные размещения"
	}
}
