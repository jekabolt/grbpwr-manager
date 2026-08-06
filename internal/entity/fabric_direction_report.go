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
// the save of any раскладка cut from that cloth — including the re-save of a marker that saves fine
// today — which turns «fill the field in» from housekeeping into a release condition (кампания Д1).
// This file is the pure half of the report that campaign is worked from: the scope decision and the
// ordering, with no SQL and no transport in them, so both are arguable in a test.
//
// The report and the blocker MUST agree about what «unset» means and about which lines can even
// have a direction. Both are shared rather than restated: the families come from the store's single
// rollGoodsSectionList, and the value test is FabricDirectionIsUnknown below, over the same
// ValidTechCardFabricDirections map the marker rule reads. A report that answered «done» while
// saves still failed would be worse than no report at all — somebody would deploy the block onto
// data they believed was clean.

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
	// BlockedMarkerCount — раскладки bound to THIS line. They are provably refused once the blocking
	// half ships: a marker's scope always contains the line it names, whether that scope resolves
	// through назначение or through the line itself, so no resolution rule can excuse them.
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
	// LinkedMarkerCount — раскладки bound to any BOM line at all, i.e. the population the rule
	// judges at all (an unlinked раскладка has no cloth attached and is exempt from it).
	LinkedMarkerCount int
	HasPatterns       bool
	Lines             []FabricDirectionGapLine
}

// BlockedMarkerCount is the card's provably-refused раскладки: the sum over its unset lines. Two
// markers cannot double-count — a marker names one line.
func (c FabricDirectionGapCard) BlockedMarkerCount() int {
	n := 0
	for _, l := range c.Lines {
		n += l.BlockedMarkerCount
	}
	return n
}

// MarkerSavePossible reports whether a раскладка can be written to this card AT ALL. A RELEASED card
// refuses every marker write (RequireMutableTechCard) before направление is ever consulted, so an
// unset line on one blocks nothing — which is the whole justification for leaving those cards out of
// the default worklist, and it is derived from the write path rather than asserted.
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
	// TotalLines is THE campaign number — lines still missing a направление in the reported scope.
	TotalLines    int
	Excluded      []FabricDirectionGapExclusion
	ExcludedCards int
	ExcludedLines int
}

// fabricDirectionGapInactiveStates are the approval states the default worklist leaves out, each for
// its own reason — and the two reasons are NOT of the same kind, which is why they are listed with
// the argument attached rather than folded into one «inactive» idea:
//
//   - RELEASED is a PROOF. RequireMutableTechCard refuses every marker save on a released card, so
//     no unset line on one can ever refuse anything. Including these rows would inflate the campaign
//     with work that changes nothing.
//   - OBSOLETE is a JUDGEMENT. A retired card is still mutable and its markers would still be
//     refused; nobody is nesting it. This is the exclusion that could be wrong, so it is the one the
//     always-reported count exists for.
//
// Deliberately NOT here, though both were candidates:
//
//   - «no раскладки yet» — a card with no markers is precisely the card whose FIRST раскладка gets
//     refused. Filtering on it would make the report answer «done» for a card where the very next
//     нестинг fails, which is the exact failure this report exists to prevent. It rides along as
//     LinkedMarkerCount/BlockedMarkerCount instead, so the ordering can prioritise without the
//     totals lying.
//   - «no выкройки yet» — a card gains DXF sheets in one upload. That is a state that changes in
//     minutes, and a worklist worked over days must not be filtered by it. It rides along as
//     HasPatterns.
var fabricDirectionGapInactiveStates = map[TechCardApprovalState]bool{
	TechCardApprovalReleased: true,
	TechCardApprovalObsolete: true,
}

// BuildFabricDirectionGapReport applies the worklist scope and the ordering to every card the store
// found, and prices what it withheld.
//
// ORDER: cards carrying a provably-refused раскладка first, then by tech_card_id. The tier is the
// only thing that outranks identity, and a card leaves it only by being FIXED (its lines drop out
// and the card leaves the report entirely) or by gaining its first bound marker — which is news, not
// churn. Everything else is id order, so two loads days apart show the same list in the same places.
// Sorting by «most unset lines» would have looked helpful and reshuffled the page under an operator
// on every save.
func BuildFabricDirectionGapReport(cards []FabricDirectionGapCard, includeInactive bool) FabricDirectionGapReport {
	var out FabricDirectionGapReport
	var urgent, rest []FabricDirectionGapCard
	excluded := make(map[TechCardApprovalState]*FabricDirectionGapExclusion)
	for _, c := range cards {
		if len(c.Lines) == 0 {
			continue
		}
		state := TechCardApprovalState(c.ApprovalState)
		if !includeInactive && fabricDirectionGapInactiveStates[state] {
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
		if c.BlockedMarkerCount() > 0 {
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
