package entity

import (
	"sort"
	"strings"
)

// НАПРАВЛЕНИЕ ТКАНИ, seen the other way round (Ф1.8): not «may this раскладка be saved» but «which
// cloth lines have nobody answered for yet».
//
// fabric_direction has been on tech_card_bom_item since 0073 and until Ф1 fed nothing but the
// MATERIALS digest, so it is unset on almost every stored line. Ф1 makes an unset direction refuse
// the save of a раскладка whose geometry actually needs judging — including the re-save of a marker
// that saves fine today — which turns «fill the field in» from housekeeping into a release condition
// (кампания Д1). This file is the pure half of the report that campaign is worked from: the scope
// decision and the ordering, with no SQL and no transport in them, so both are arguable in a test.
//
// The report and the blocker MUST agree about what «unset» means and about which lines can even
// have a direction. Both are shared rather than restated: the families come from the store's single
// rollGoodsSectionList, and the value test is FabricDirectionIsUnknown below, over the same
// ValidTechCardFabricDirections map the marker rule reads. A report that answered «done» while
// saves still failed would be worse than no report at all — somebody would deploy the block onto
// data they believed was clean.
//
// WHAT THIS REPORT IS EXACT ABOUT, AND WHAT IT ONLY BOUNDS. Per LINE it is exact and it is what the
// campaign is measured by: a line reported here is a line whose направление nobody set, and no line
// the rule can refuse on is missing. Per MARKER it only bounds, because the refusal needs a second
// fact the store cannot see — the rule waves a layout through before it ever asks about the cloth
// when nothing in it is upside down, and only the layout blob knows that (0257: the store does not
// parse it). So every marker count here is a triage aid with its error direction written next to
// it, and none of them is the campaign's measure.

// FabricDirectionIsUnknown reports whether a stored fabric_direction says nothing about the cloth.
//
// UNKNOWN is not a fourth value: it is NULL (which arrives here as ""), and also anything outside
// the closed vocabulary. The DB CHECK makes the second case unreachable through the app, and it is
// still tested for, because the fail-closed answer is the safe one in both directions — the marker
// rule counts such a value as unknown and refuses, so the report has to count it as outstanding work
// or the two would disagree about a row that actually blocks.
func FabricDirectionIsUnknown(direction string) bool {
	return !ValidTechCardFabricDirections[TechCardFabricDirection(strings.ToLower(strings.TrimSpace(direction)))]
}

// FabricDirectionGapLine is one roll-goods BOM line still missing its направление.
type FabricDirectionGapLine struct {
	BomItemID int64
	LineKey   string
	// Name resolved through the catalogue the way the BOM tab resolves it (COALESCE over the line's
	// own name and the linked article's) — the report has to speak the vocabulary of the screen it
	// sends an operator to, or it prints a ULID at somebody looking for «ВЕЛЬВЕТ ИЗ КАТАЛОГА».
	Name    string
	Section string
	Purpose string
	// IsSample marks семпловая ярдажа (0265). Reported, never filtered: the marker rule asks the
	// direction of sample cloth for SAMPLE раскладки, so a report that dropped these rows would read
	// «done» while a sample раскладка still refused to save.
	IsSample bool
	// BlockedMarkerCount — раскладки bound to THIS line: an UPPER bound on what the rule refuses
	// here, never a count of refusals. ValidateMarkerFabricDirection lets a layout through BEFORE it
	// asks about the cloth when that layout carries neither a 180° nor a mirror — nothing upside
	// down means no direction can change the verdict — so a marker counted here may keep saving
	// exactly as it does today. Separating the two needs the layout blob, and the store does not
	// parse it by design (0257); an over-count is the honest thing to publish, provided it says so.
	BlockedMarkerCount int
}

// FabricDirectionGapCard is one tech card with at least one unset cloth line, plus the facts an
// owner triages by. Everything here is a COUNT or a state — the report deliberately stops short of
// deciding which раскладка breaks, because that answer belongs to the marker rule and a second copy
// of it here would be free to drift.
type FabricDirectionGapCard struct {
	TechCardID    int
	StyleNumber   string
	Name          string
	Stage         string
	ApprovalState string
	// LinkedMarkerCount — раскладки bound to any BOM line of this card: every marker a gap on THIS
	// card could possibly refuse, because a marker's scope is built out of the card's own lines and
	// an unlinked раскладка has no cloth at all. Over-inclusive by construction and chosen for it —
	// this is the only count here with NO FALSE NEGATIVES, which is what a triage tier needs. See
	// BuildFabricDirectionGapReport for why the tier is drawn on it rather than on the sharper
	// BlockedMarkerCount.
	LinkedMarkerCount int
	HasPatterns       bool
	Lines             []FabricDirectionGapLine
}

// BlockedMarkerCount sums the per-line counts. Informational only — it is neither an upper nor a
// lower bound on refusals (it over-counts compliant geometry, and misses markers refused through a
// sibling line under the same назначение), so it decides nothing about scope or order.
func (c FabricDirectionGapCard) BlockedMarkerCount() int {
	n := 0
	for _, l := range c.Lines {
		n += l.BlockedMarkerCount
	}
	return n
}

// MarkerSavePossible reports whether a раскладка can be written to this card RIGHT NOW. A RELEASED
// card refuses every marker write (RequireMutableTechCard) before направление is ever consulted; no
// other state does, obsolete included.
//
// «Right now» is the whole caveat. This is a statement about an instant, not about the campaign:
// re-opening a released card to draft is a sanctioned, ordinary edit, and the moment it happens the
// same unset lines are live again. So it justifies DEFERRING released cards out of the default
// worklist and pricing them — never dismissing them from the release condition.
func (c FabricDirectionGapCard) MarkerSavePossible() bool {
	return TechCardApprovalState(c.ApprovalState) != TechCardApprovalReleased
}

// FabricDirectionGapExclusion is one reason rows were withheld, priced in cards and lines.
type FabricDirectionGapExclusion struct {
	ApprovalState string
	Cards         int
	Lines         int
}

// FabricDirectionGapReport is the assembled answer.
type FabricDirectionGapReport struct {
	Cards      []FabricDirectionGapCard
	TotalCards int
	// TotalLines — lines still missing a направление IN THE SCOPE THAT WAS ASKED FOR. On a default
	// call zero means «nothing refuses today»; it is not on its own the release condition, because a
	// deferred card is one ordinary edit away from being live again.
	//
	// THE GO/NO-GO IS TotalLines + ExcludedLines == 0, i.e. TotalLines == 0 with includeInactive.
	TotalLines    int
	Excluded      []FabricDirectionGapExclusion
	ExcludedCards int
	ExcludedLines int
}

// fabricDirectionGapDeferredStates are the approval states the default worklist holds back.
//
// THE TEST FOR MEMBERSHIP IS «CAN THE RULE REFUSE THIS CARD», NOT «WILL ANYBODY TOUCH IT». That
// distinction is the whole content of this variable, and getting it wrong once already cost
// something: OBSOLETE used to be in here on the reasoning that nobody nests a retired card. But
// RequireMutableTechCard refuses only RELEASED — an obsolete card is fully mutable, so a save on it
// reaches the direction rule and is refused TODAY, with no state change and no warning. Deferring it
// made the default total_lines read «finished» over exactly the failure this report exists to
// prevent. It is now in the worklist like any other live card.
//
// RELEASED stays deferred, and on an honestly weaker claim than the one first written here. It is
// NOT a proof that these lines can never refuse: moving a card back to draft is a sanctioned edit,
// and one of them puts the same unset lines back in play. It is a claim about ORDER OF WORK — a
// released card cannot refuse until somebody deliberately re-opens it, so it does not belong in the
// list being worked through today. Which is why it is priced in `excluded` on every response and why
// the release condition is the INCLUSIVE one: total_lines + excluded_lines == 0.
//
// Deliberately NOT here, though both were candidates:
//
//   - «no раскладки yet» — a card with no markers is precisely the card whose FIRST раскладка gets
//     refused. Filtering on it would make the report answer «done» for a card where the very next
//     нестинг fails. It rides along as LinkedMarkerCount instead, so the ordering can prioritise
//     without the totals lying.
//   - «no выкройки yet» — a card gains DXF sheets in one upload. That is a state that changes in
//     minutes, and a worklist worked over days must not be filtered by it. It rides along as
//     HasPatterns.
var fabricDirectionGapDeferredStates = map[TechCardApprovalState]bool{
	TechCardApprovalReleased: true,
}

// BuildFabricDirectionGapReport applies the worklist scope and the ordering to every card the store
// found, and prices what it held back.
//
// ORDER: cards carrying at least one BOUND раскладка first, then by tech_card_id.
//
// The tier is drawn on LinkedMarkerCount and not on the sharper-looking BlockedMarkerCount, because
// a triage tier must have no false negatives and BlockedMarkerCount has them. Let an unset line X
// and an ANSWERED line Y share a назначение, with the markers hanging off Y: those markers resolve a
// scope containing X, so they are the ones that break — while X reports zero blocked markers and the
// card would sink below the fold. Catching that exactly means re-deriving назначение here, i.e. a
// second copy of the marker rule free to disagree with the first. A sound over-approximation is the
// cheaper honest answer: every marker a gap on this card can refuse is bound to SOME line of this
// card, so LinkedMarkerCount > 0 can over-include but can never miss.
//
// A card leaves the tier only by being FIXED (its lines drop out and it leaves the report entirely)
// or by gaining its first bound marker — which is news, not churn. Everything else is id order, so
// two loads days apart show the same list in the same places. Sorting by «most unset lines» would
// have looked helpful and reshuffled the page under an operator on every save.
func BuildFabricDirectionGapReport(cards []FabricDirectionGapCard, includeInactive bool) FabricDirectionGapReport {
	var out FabricDirectionGapReport
	var urgent, rest []FabricDirectionGapCard
	excluded := make(map[TechCardApprovalState]*FabricDirectionGapExclusion)
	for _, c := range cards {
		if len(c.Lines) == 0 {
			continue
		}
		state := TechCardApprovalState(c.ApprovalState)
		if !includeInactive && fabricDirectionGapDeferredStates[state] {
			e := excluded[state]
			if e == nil {
				e = &FabricDirectionGapExclusion{ApprovalState: c.ApprovalState}
				excluded[state] = e
			}
			e.Cards++
			e.Lines += len(c.Lines)
			out.ExcludedCards++
			out.ExcludedLines += len(c.Lines)
			continue
		}
		if c.LinkedMarkerCount > 0 {
			urgent = append(urgent, c)
		} else {
			rest = append(rest, c)
		}
		out.TotalCards++
		out.TotalLines += len(c.Lines)
	}
	// A stable two-way partition rather than a sort: the input already carries the total order the
	// store promised (tech_card_id, then display_order, id within a card), appends preserve it inside
	// each tier, and there is no comparator to get subtly wrong.
	out.Cards = append(append(make([]FabricDirectionGapCard, 0, len(urgent)+len(rest)), urgent...), rest...)
	out.Excluded = make([]FabricDirectionGapExclusion, 0, len(excluded))
	for _, e := range excluded {
		out.Excluded = append(out.Excluded, *e)
	}
	sort.Slice(out.Excluded, func(i, j int) bool {
		return out.Excluded[i].ApprovalState < out.Excluded[j].ApprovalState
	})
	return out
}
