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
	// HalfTurnCount / FlipCount are HOW MANY placements are upside down, not merely whether any is.
	// The count is what the exemption compares, and the difference is not academic: with booleans, a
	// row storing ONE 180° licensed a replacement carrying forty of them — and SaveMarker replaces
	// the whole row, so the size, the colourway, the width and every piece could change with it. «It
	// already had a half-turn» is not a licence to add half-turns.
	//
	// Counting keeps the comparison ORDER-INSENSITIVE, which is the property that lets a rename work
	// at all: the client re-emits the geometry and the placements may come back in any order.
	//
	// Ф2 NOTE, deliberate and load bearing: a СОСТАВ multiplies placements, so re-nesting a legacy
	// раскладка as a composition raises these counts and therefore DENIES the exemption. That is
	// correct — the pass exists for renames and re-links, not for new geometry, and a re-nest is new
	// geometry. Normalising the counts per garment would hand the pass back to exactly the case it
	// must not cover. The operational consequence: putting an EXISTING card on Ф2 requires
	// fabric_direction to be filled in first (campaign Д1; Ф1.8's report finds the rows).
	HalfTurnCount int
	// FlipCount counts mirrored placements (schema 3+).
	FlipCount int
	// HasComposition / HasPieceSize: the blob carries a состав, and at least one piece is tagged with
	// a size (schema 4+, Ф2). BOOLEANS, not the состав itself, and that is a decision: the normalised
	// состав already travels on TechCardMarkerInsert.Composition, and a second copy here would be a
	// second answer to «how many garments does this marker cut» — precisely the two-copies question
	// §2.5 of the spec refuses to answer for the wire. These two exist only for
	// CompositionPredatesSchema, which asks whether the blob's FORMAT is honest.
	HasComposition bool
	HasPieceSize   bool
}

// HasHalfTurn / HasFlip are derived, never stored: one source of truth for «is there any», so a
// count and a flag can never drift apart.
func (f MarkerLayoutFacts) HasHalfTurn() bool { return f.HalfTurnCount > 0 }
func (f MarkerLayoutFacts) HasFlip() bool     { return f.FlipCount > 0 }

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
	return f.HasFlip() && f.SchemaVersion > 0 && f.SchemaVersion < MarkerLayoutSchemaWithFlip
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

// StoredMarkerRow is everything the ROW BEING REPLACED contributes to the decision. It is a struct
// so the property that matters is structural: not one field here can be written by the payload.
type StoredMarkerRow struct {
	// Generation is the policy generation stamped on the row (0 = this save creates one).
	Generation int
	// BindingUnchanged: this save leaves the marker on the same BOM line it was already attributed
	// to. The exemption requires it, because a pass forgives geometry ON ITS CLOTH — a 180° that was
	// always legal on two_way cloth is not grandfathered geometry at all, and re-linking that marker
	// to ворс in one request creates a (geometry, cloth) pairing that has never existed. That pairing
	// is exactly what shades a garment wrong. Nothing legitimate is lost: re-linking to two_way or
	// any needs no pass, and re-linking to cloth of unknown direction is refused outright.
	BindingUnchanged bool
	// Facts reads the geometry on file, lazily.
	Facts StoredMarkerFacts
}

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

// introducesForbidden reports whether the incoming layout puts MORE of a piece upside down than the
// stored one already does. This is the whole scope of the exemption: it forgives what is on file,
// never what this very save is adding to it.
//
// Counts, not flags. A boolean subset read «this row already has a half-turn» as a licence for any
// number of them: one stored 180° accepted a replacement with forty, on directional cloth, forever.
// Counting is still order-insensitive, so the re-emitted blob of a rename compares equal — which was
// the reason for not comparing placements one by one, and it survives intact.
func introducesForbidden(incoming, stored MarkerLayoutFacts) bool {
	return incoming.HalfTurnCount > stored.HalfTurnCount || incoming.FlipCount > stored.FlipCount
}

// newlyForbidden names the properties this save is ADDING, for a refusal that can tell an operator
// what to undo instead of what to re-nest.
func newlyForbidden(incoming, stored MarkerLayoutFacts) string {
	switch {
	case incoming.HalfTurnCount > stored.HalfTurnCount && incoming.FlipCount > stored.FlipCount:
		return fmt.Sprintf("размещений на 180° стало %d (было %d), зеркальных — %d (было %d)",
			incoming.HalfTurnCount, stored.HalfTurnCount, incoming.FlipCount, stored.FlipCount)
	case incoming.HalfTurnCount > stored.HalfTurnCount:
		return fmt.Sprintf("размещений на 180° стало %d (было %d)", incoming.HalfTurnCount, stored.HalfTurnCount)
	default:
		return fmt.Sprintf("зеркальных размещений стало %d (было %d)", incoming.FlipCount, stored.FlipCount)
	}
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
	// ReasonFlipIntroducedOnLegacy — the row predates the policy and keeps its pass, but THIS save
	// adds upside-down placements it did not have. A separate code from ReasonFlipOnOneWay because
	// the remedy is the opposite: restore what was saved, do not re-nest the geometry that was fine.
	ReasonFlipIntroducedOnLegacy = "flip_introduced_on_legacy"
	// ReasonStoredLayoutUnreadable — the row predates the policy but its stored geometry cannot be
	// read, so nothing can be established about what was already there and no pass can be granted.
	ReasonStoredLayoutUnreadable = "stored_layout_unreadable"
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
// layout distilled by the API layer; stored is what the row being replaced contributes — its policy
// generation, whether the binding is unchanged, and the geometry it already carries.
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
	stored StoredMarkerRow, resolveNames FabricLineNamer) (MarkerDirectionVerdict, error) {
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
	if !facts.HasHalfTurn() && !facts.HasFlip() {
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
		//
		// But TOLERATED IS NOT JUDGED. Upside-down geometry that this cloth merely permits has not
		// been held against the policy on its own merits — it passed because the fabric was
		// permissive, and the fabric is a field an operator corrects. Stamping the row as judged
		// under the current policy would brick it the day somebody fixes that line to one_way: the
		// pass would be gone and a rename would come back refused, on geometry nobody touched. Ф1.8
		// exists precisely to drive those corrections, so this is about to be routine.
		return MarkerDirectionVerdict{Judged: !facts.HasHalfTurn() && !facts.HasFlip()}, nil
	}
	// From here the policy REFUSES, and the row's pre-Ф1 pass is the only thing that can change that.
	// Three facts have to hold together, and not one of them is writable by the caller:
	//
	//   • the ROW predates the policy (its generation, written only by the server);
	//   • the marker stays on the SAME cloth (a pass forgives geometry on the fabric it was measured
	//     against — see StoredMarkerRow.BindingUnchanged);
	//   • this save ADDS no upside-down placement the stored geometry did not already carry.
	//
	// The third condition is the one that was missing at first, and the shape of its absence is worth
	// keeping in view: keyed on the verdict alone, the pass fired whenever the policy would have
	// refused — including when the operator was adding the 180° right then — and, because an exempted
	// save deliberately holds the generation back, the row stayed exemptible forever after. The
	// ratchet defeated by the exact action it exists to prevent.
	//
	// What the pass is FOR: geometry ALREADY ON FILE. The manual editor saved the rotation a piece
	// actually had, so 90° at allow_cross_grain=false is on file and 180° with it, and refusing those
	// the moment their card gets a направление would invalidate measurements nobody can re-take
	// without re-nesting. Such a row stays renameable and re-linkable indefinitely — and now ONLY
	// that.
	blockers := named(scopeLinesWith(scope, lines, FabricDirectionOneWay), resolveNames)
	refused := func(reason, howToFix string) (MarkerDirectionVerdict, error) {
		return MarkerDirectionVerdict{Judged: true},
			NewFieldViolation("layout.placements", reason, keysOf(blockers), howToFix)
	}
	if stored.Generation > 0 && stored.Generation < MarkerLayoutSchemaWithFlip && stored.BindingUnchanged {
		onFile, readable := storedFactsOf(stored.Facts)
		switch {
		case !readable:
			// Nothing can be established about what was already there. Said out loud rather than
			// folded into the generic refusal: its only other trace is a server log line, and an
			// operator told to «re-nest without 180°» would have no idea the reason is that their
			// stored geometry is unreadable — which is also why re-nesting is in fact the remedy.
			return refused(ReasonStoredLayoutUnreadable,
				fmt.Sprintf("%s: %s помечена one_way, а сохранённую геометрию этой раскладки прочитать "+
					"не удаётся — сервер не может отличить старый поворот от нового, поэтому пересоберите "+
					"раскладку заново без 180° и без зеркальных размещений", offendingPlacements(facts), labelsOf(blockers)))
		case !introducesForbidden(facts, onFile):
			// KNOWN AND DELIBERATE RESIDUAL, and now a narrow one: the comparison is by COUNT, so a
			// row that already carries a half-turn may have a DIFFERENT piece turned instead — the
			// count is unchanged, the geometry is not. Closing that needs placement-by-placement
			// comparison against the stored blob, which would fail a rename whenever the client
			// re-emitted the geometry in another order.
			return MarkerDirectionVerdict{Judged: true, Exempted: true}, nil
		default:
			// The row keeps its pass — this save just is not covered by it. Say WHICH property grew,
			// and ask for the saved placements back rather than for a re-nest: the geometry that was
			// on file is fine and must not be thrown away to satisfy this refusal. The «fix the
			// direction on the BOM tab» hint is deliberately absent here — on a row that is already
			// grandfathered it would read as an invitation to switch the guard off.
			return refused(ReasonFlipIntroducedOnLegacy,
				fmt.Sprintf("%s помечена one_way, а эта правка добавляет перевороты к сохранённым: %s — "+
					"верните сохранённые размещения (переименовать, перепривязать или сохранить раскладку "+
					"как она лежит по-прежнему можно)", labelsOf(blockers), newlyForbidden(facts, onFile)))
		}
	}
	howToFix := fmt.Sprintf("%s: %s помечена one_way", offendingPlacements(facts), labelsOf(blockers))
	if scope.ByPurpose && namesOtherThan(blockers, bomLineKey) {
		howToFix += fmt.Sprintf(" (через назначение %q)", scope.Key)
	}
	howToFix += " — на направленной ткани деталь нельзя класть вверх ногами: пересоберите раскладку без 180° и " +
		"без зеркальных размещений, либо исправьте направление ткани на вкладке BOM, если ткань на самом деле не направленная"
	return refused(ReasonFlipOnOneWay, howToFix)
}

// storedFactsOf reads the geometry on file, and reports whether it could be read AT ALL. The second
// return is the point: «nothing forbidden was on file» and «I could not look» must never collapse
// into the same answer, because one grants a pass and the other must withhold it.
//
// Withholding is the deliberate direction, and it is the opposite of what «be lenient with damaged
// data» would suggest. The read path degrades on an unreadable blob by design — GetTechCardMarker
// builds a FRESH empty layout and serves the summary plus a warning, with no placements — so a client
// cannot have loaded that marker's geometry to send it back. Whatever upside-down geometry arrives is
// new by construction, and new geometry is exactly what the pass may not forgive. It costs the
// operator only the forbidden save: renaming, re-linking and re-nesting with compliant geometry all
// still work, so nothing is stranded.
func storedFactsOf(storedFacts StoredMarkerFacts) (MarkerLayoutFacts, bool) {
	if storedFacts == nil {
		return MarkerLayoutFacts{}, false
	}
	return storedFacts()
}

// offendingPlacements names which half of the ban fired. «180° and mirrored» and «mirrored» send an
// operator to two different controls of the editor, so a single generic wording would cost a search.
func offendingPlacements(facts MarkerLayoutFacts) string {
	switch {
	case facts.HasHalfTurn() && facts.HasFlip():
		return "раскладка несёт размещения на 180° и зеркальные"
	case facts.HasHalfTurn():
		return "раскладка несёт размещения на 180°"
	default:
		return "раскладка несёт зеркальные размещения"
	}
}
