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

func gapCard(id int, state string, blocked int, lines int) FabricDirectionGapCard {
	c := FabricDirectionGapCard{TechCardID: id, ApprovalState: state}
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

// The default worklist drops released and obsolete cards — and PRICES what it dropped, always. A
// filter nobody can see is a report that lies by omission, and this one gets read as a release
// condition.
func TestBuildFabricDirectionGapReportExcludesInactiveButCountsThem(t *testing.T) {
	cards := []FabricDirectionGapCard{
		gapCard(1, string(TechCardApprovalDraft), 0, 2),
		gapCard(2, string(TechCardApprovalReleased), 0, 3),
		gapCard(3, string(TechCardApprovalApproved), 0, 1),
		gapCard(4, string(TechCardApprovalObsolete), 0, 4),
		gapCard(5, string(TechCardApprovalInReview), 0, 1),
	}
	r := BuildFabricDirectionGapReport(cards, false)
	if got := ids(r.Cards); !equalInts(got, []int{1, 3, 5}) {
		t.Fatalf("worklist = %v, want [1 3 5]", got)
	}
	if r.TotalCards != 3 || r.TotalLines != 4 {
		t.Fatalf("totals = %d cards / %d lines, want 3 / 4", r.TotalCards, r.TotalLines)
	}
	if r.ExcludedCards != 2 || r.ExcludedLines != 7 {
		t.Fatalf("excluded = %d cards / %d lines, want 2 / 7", r.ExcludedCards, r.ExcludedLines)
	}
	if len(r.Excluded) != 2 {
		t.Fatalf("excluded breakdown = %d reasons, want 2", len(r.Excluded))
	}
	// Deterministic breakdown order: obsolete before released, alphabetically.
	if r.Excluded[0].ApprovalState != string(TechCardApprovalObsolete) || r.Excluded[0].Lines != 4 {
		t.Fatalf("first exclusion = %+v, want obsolete / 4 lines", r.Excluded[0])
	}
	if r.Excluded[1].ApprovalState != string(TechCardApprovalReleased) || r.Excluded[1].Lines != 3 {
		t.Fatalf("second exclusion = %+v, want released / 3 lines", r.Excluded[1])
	}
}

// include_inactive folds the hidden rows back in and empties the breakdown — the same numbers, all
// on one side of the fence. Without this the owner could never audit what the default scope drops.
func TestBuildFabricDirectionGapReportIncludeInactive(t *testing.T) {
	cards := []FabricDirectionGapCard{
		gapCard(1, string(TechCardApprovalDraft), 0, 2),
		gapCard(2, string(TechCardApprovalReleased), 0, 3),
		gapCard(4, string(TechCardApprovalObsolete), 0, 4),
	}
	r := BuildFabricDirectionGapReport(cards, true)
	if got := ids(r.Cards); !equalInts(got, []int{1, 2, 4}) {
		t.Fatalf("worklist = %v, want [1 2 4]", got)
	}
	if r.TotalLines != 9 || r.ExcludedCards != 0 || r.ExcludedLines != 0 || len(r.Excluded) != 0 {
		t.Fatalf("include_inactive must fold everything in: %+v", r)
	}
}

// Cards with a provably-refused раскладка come first; everything under that tier keeps the store's
// id order. The owner works this list over days, so a second load must put the same card in the same
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

// A card whose blocked markers hang off a LATER line is still urgent — the tier is the card's sum,
// not its first row.
func TestFabricDirectionGapCardBlockedMarkerCountSumsLines(t *testing.T) {
	c := FabricDirectionGapCard{Lines: []FabricDirectionGapLine{
		{BlockedMarkerCount: 0}, {BlockedMarkerCount: 3}, {BlockedMarkerCount: 2},
	}}
	if got := c.BlockedMarkerCount(); got != 5 {
		t.Fatalf("blocked = %d, want 5", got)
	}
}

// The one exclusion that is a PROOF rather than a judgement: a released card refuses every marker
// write before направление is consulted, so its unset lines block nothing. Everything else can
// still be nested, obsolete included — which is why obsolete is excluded on judgement and counted.
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
