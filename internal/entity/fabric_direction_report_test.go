package entity

import "testing"

// UNSET has to mean the same thing to the report and to the marker rule. The rule refuses on
// anything outside the closed vocabulary (after trim+lower), so the report must count exactly the
// same rows as outstanding — one row where they disagree is one save that fails on a card the owner
// was told was finished.
func TestFabricDirectionIsUnknown(t *testing.T) {
	known := []string{"any", "one_way", "two_way", " ONE_WAY ", "Two_Way"}
	for _, v := range known {
		if FabricDirectionIsUnknown(v) {
			t.Errorf("direction %q must count as known", v)
		}
	}
	unknown := []string{"", "   ", "unknown", "lengthwise", "1", "one way"}
	for _, v := range unknown {
		if !FabricDirectionIsUnknown(v) {
			t.Errorf("direction %q must count as unknown", v)
		}
	}
	// The vocabulary is not restated here: whatever the map holds is what counts as known.
	for d := range ValidTechCardFabricDirections {
		if FabricDirectionIsUnknown(string(d)) {
			t.Errorf("vocabulary value %q must count as known", d)
		}
	}
}

// gapCard builds a card with `lines` unset lines, `blocked` раскладки bound to the first of them,
// and — unless overridden — a LinkedMarkerCount consistent with that (blocked markers are linked
// markers). The tier reads LinkedMarkerCount, so tests that care about the tier set it explicitly.
func gapCard(id int, state string, blocked int, lines int) FabricDirectionGapCard {
	c := FabricDirectionGapCard{TechCardID: id, ApprovalState: state, LinkedMarkerCount: blocked}
	for i := 0; i < lines; i++ {
		l := FabricDirectionGapLine{BomItemID: int64(id*100 + i)}
		if i == 0 {
			l.BlockedMarkerCount = blocked
		}
		c.Lines = append(c.Lines, l)
	}
	return c
}

func ids(cards []FabricDirectionGapCard) []int {
	out := make([]int, 0, len(cards))
	for _, c := range cards {
		out = append(out, c.TechCardID)
	}
	return out
}

func equalInts(a, b []int) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// The default worklist defers RELEASED cards only — and PRICES what it deferred, always. A filter
// nobody can see is a report that lies by omission, and this one gets read as a release condition.
func TestBuildFabricDirectionGapReportDefersReleasedButCountsThem(t *testing.T) {
	cards := []FabricDirectionGapCard{
		gapCard(1, string(TechCardApprovalDraft), 0, 2),
		gapCard(2, string(TechCardApprovalReleased), 0, 3),
		gapCard(3, string(TechCardApprovalApproved), 0, 1),
		gapCard(4, string(TechCardApprovalObsolete), 0, 4),
		gapCard(5, string(TechCardApprovalInReview), 0, 1),
	}
	r := BuildFabricDirectionGapReport(cards, false)
	if got := ids(r.Cards); !equalInts(got, []int{1, 3, 4, 5}) {
		t.Fatalf("worklist = %v, want [1 3 4 5]", got)
	}
	if r.TotalCards != 4 || r.TotalLines != 8 {
		t.Fatalf("totals = %d cards / %d lines, want 4 / 8", r.TotalCards, r.TotalLines)
	}
	if r.ExcludedCards != 1 || r.ExcludedLines != 3 {
		t.Fatalf("excluded = %d cards / %d lines, want 1 / 3", r.ExcludedCards, r.ExcludedLines)
	}
	if len(r.Excluded) != 1 || r.Excluded[0].ApprovalState != string(TechCardApprovalReleased) {
		t.Fatalf("excluded breakdown = %+v, want released only", r.Excluded)
	}
}

// OBSOLETE IS NOT DEFERRED, and this is the regression that matters most in this file.
//
// RequireMutableTechCard refuses only `released`. An obsolete card is fully mutable, so a save on it
// reaches the direction rule and is refused TODAY, with no state change and no warning. Holding it
// back made the default total_lines read «finished» over exactly the failure this report exists to
// prevent — a card nobody expected to break, breaking.
func TestBuildFabricDirectionGapReportKeepsObsoleteInTheWorklist(t *testing.T) {
	r := BuildFabricDirectionGapReport([]FabricDirectionGapCard{
		gapCard(7, string(TechCardApprovalObsolete), 0, 2),
	}, false)
	if len(r.Cards) != 1 || r.TotalLines != 2 {
		t.Fatalf("obsolete must be worked like any live card: %+v", r)
	}
	if r.ExcludedLines != 0 || len(r.Excluded) != 0 {
		t.Fatalf("obsolete must not be counted as deferred: %+v", r.Excluded)
	}
}

// include_inactive folds the deferred rows back in and empties the breakdown — the same numbers, all
// on one side of the fence. This is the RELEASE GATE form of the call: the go/no-go is
// TotalLines + ExcludedLines == 0, which is scope-independent, and this pins that identity.
func TestBuildFabricDirectionGapReportIncludeInactiveIsTheGoNoGo(t *testing.T) {
	cards := []FabricDirectionGapCard{
		gapCard(1, string(TechCardApprovalDraft), 0, 2),
		gapCard(2, string(TechCardApprovalReleased), 0, 3),
		gapCard(4, string(TechCardApprovalObsolete), 0, 4),
	}
	deferred := BuildFabricDirectionGapReport(cards, false)
	r := BuildFabricDirectionGapReport(cards, true)
	if got := ids(r.Cards); !equalInts(got, []int{1, 2, 4}) {
		t.Fatalf("worklist = %v, want [1 2 4]", got)
	}
	if r.TotalLines != 9 || r.ExcludedCards != 0 || r.ExcludedLines != 0 || len(r.Excluded) != 0 {
		t.Fatalf("include_inactive must fold everything in: %+v", r)
	}
	if deferred.TotalLines+deferred.ExcludedLines != r.TotalLines {
		t.Fatalf("go/no-go identity broken: %d + %d != %d",
			deferred.TotalLines, deferred.ExcludedLines, r.TotalLines)
	}
}

// Cards carrying a bound раскладка come first; everything under that tier keeps the store's id
// order. The owner works this list over days, so a second load must put the same card in the same
// place — and the tier must not be a proxy sort that reshuffles on every save.
func TestBuildFabricDirectionGapReportOrdersUrgentFirstThenById(t *testing.T) {
	cards := []FabricDirectionGapCard{
		gapCard(1, string(TechCardApprovalDraft), 0, 1),
		gapCard(2, string(TechCardApprovalDraft), 2, 1),
		gapCard(3, string(TechCardApprovalDraft), 0, 5), // more lines, still not urgent
		gapCard(4, string(TechCardApprovalDraft), 1, 1),
	}
	r := BuildFabricDirectionGapReport(cards, false)
	if got := ids(r.Cards); !equalInts(got, []int{2, 4, 1, 3}) {
		t.Fatalf("order = %v, want [2 4 1 3]", got)
	}
	// Idempotent: feeding the output back in must not move anything.
	again := BuildFabricDirectionGapReport(r.Cards, false)
	if got := ids(again.Cards); !equalInts(got, []int{2, 4, 1, 3}) {
		t.Fatalf("second pass reordered the list: %v", got)
	}
}

// THE SIBLING CASE, which is why the tier reads LinkedMarkerCount and not BlockedMarkerCount.
//
// An unset line X and an ANSWERED line Y share a назначение; the markers hang off Y. Those markers
// resolve a scope containing X, so they are precisely the ones that break — while X reports zero
// blocked markers. On the sharper count this card sinks below the fold; on the sound one it sorts
// first, which is the error direction a triage tier is allowed to have.
func TestBuildFabricDirectionGapReportTierCatchesSiblingRefusals(t *testing.T) {
	sibling := FabricDirectionGapCard{
		TechCardID: 9, ApprovalState: string(TechCardApprovalDraft),
		LinkedMarkerCount: 3, // all bound to the ANSWERED sibling, which is not reported
		Lines:             []FabricDirectionGapLine{{BomItemID: 901, BlockedMarkerCount: 0}},
	}
	calm := gapCard(10, string(TechCardApprovalDraft), 0, 1)
	if sibling.BlockedMarkerCount() != 0 {
		t.Fatal("fixture must have no directly-bound markers, or it proves nothing")
	}
	r := BuildFabricDirectionGapReport([]FabricDirectionGapCard{calm, sibling}, false)
	if got := ids(r.Cards); !equalInts(got, []int{9, 10}) {
		t.Fatalf("order = %v, want [9 10] — the sibling-refused card must not sink", got)
	}
}

// BlockedMarkerCount is informational and sums every line, not just the first.
func TestFabricDirectionGapCardBlockedMarkerCountSumsLines(t *testing.T) {
	c := FabricDirectionGapCard{Lines: []FabricDirectionGapLine{
		{BlockedMarkerCount: 0}, {BlockedMarkerCount: 3}, {BlockedMarkerCount: 2},
	}}
	if got := c.BlockedMarkerCount(); got != 5 {
		t.Fatalf("blocked = %d, want 5", got)
	}
}

// MarkerSavePossible is about an INSTANT, not about the campaign. Only `released` refuses every
// marker write outright (RequireMutableTechCard); obsolete does not, which is why obsolete is worked
// like any other card and released is merely deferred-and-counted rather than dismissed.
func TestFabricDirectionGapCardMarkerSavePossible(t *testing.T) {
	for state, want := range map[TechCardApprovalState]bool{
		TechCardApprovalDraft:    true,
		TechCardApprovalInReview: true,
		TechCardApprovalApproved: true,
		TechCardApprovalObsolete: true,
		TechCardApprovalReleased: false,
	} {
		c := FabricDirectionGapCard{ApprovalState: string(state)}
		if got := c.MarkerSavePossible(); got != want {
			t.Errorf("state %q: marker save possible = %v, want %v", state, got, want)
		}
	}
}

// A card the store handed over with no unset lines is not a card with an empty worklist — it is not
// on the worklist at all, and it must not inflate total_cards.
func TestBuildFabricDirectionGapReportSkipsLinelessCards(t *testing.T) {
	r := BuildFabricDirectionGapReport([]FabricDirectionGapCard{
		{TechCardID: 1, ApprovalState: string(TechCardApprovalDraft)},
		gapCard(2, string(TechCardApprovalDraft), 0, 1),
	}, false)
	if r.TotalCards != 1 || len(r.Cards) != 1 || r.Cards[0].TechCardID != 2 {
		t.Fatalf("lineless card must not appear: %+v", r.Cards)
	}
}
