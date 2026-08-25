package techcardanalysis

import (
	"database/sql"
	"fmt"
	"strings"
	"testing"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
)

// TestNoMoneyFindingIsReadiness pins THE INVARIANT that lets the handler redact money at all.
//
// A finding of class readiness is folded into the single "Not yet ready for release: …" finding on
// a draft card (§3.0), and that collapsed finding is built fresh — it carries Money=false. So a
// money finding that were ALSO readiness would, on every draft card, be laundered through the
// collapse and sail past a handler that filters on the flag.
//
// The check runs over the fixture in BOTH forms plus the money-heavy stand below, because the
// collapse only happens on a draft and the readiness class only expands off it.
func TestNoMoneyFindingIsReadiness(t *testing.T) {
	cards := map[string]*entity.TechCard{
		"card 8 (draft)":     card8(),
		"card 8 (in_review)": mtInReview(card8()),
		"money stand":        mtMoneyCard(),
		// B6 AND B7 MUST BE IN HERE. Every other fixture has Costing == nil, so neither branch of
		// the cmt_cost predicate ever fires in them — and B6 is the one check in the package that
		// carries CategoryReadiness, i.e. the only place the laundering this test exists to
		// prevent can actually happen. Without these two entries, flagging B6 money left the
		// package green while a draft card carried it past the handler inside the collapse.
		"costing stand, cmt unset": mtCostingCard(false),
		"costing stand, cmt set":   mtCostingCard(true),
		"aggregate money stand":    mtAggregateMoneyCard(),
	}
	for name, card := range cards {
		for _, f := range RunAudit(card, Fx{Base: "EUR"}).Findings {
			if f.Money && f.Category == CategoryReadiness {
				t.Errorf("%s: finding %q is BOTH money and readiness — on a draft it collapses into a "+
					"finding with Money=false and the handler's redaction cannot see it. Either it is not "+
					"money, or the collapse has to learn to carry the flag.", name, f.Title)
			}
			// The collapsed finding itself must never claim to be money either: it is built by
			// CollapseReadiness out of clauses, and a clause carrying a price would be the same hole
			// through a different door.
			if f.Money && strings.HasPrefix(f.Title, collapsedReadinessTitle) {
				t.Errorf("%s: the collapsed readiness finding is flagged money", name)
			}
		}
	}
}

// TestMoneyFlagIsOnExactlyThePriceQuotingFindings is the poimenny half: on a card built to fire the
// whole B5 family, the flagged set is EXACTLY the three checks that print a price, a currency or a
// ratio — no more (over-flagging hides real findings from a technologist) and no less (under-
// flagging leaks purchase prices to an account that GetTechCard redacts them from).
func TestMoneyFlagIsOnExactlyThePriceQuotingFindings(t *testing.T) {
	res := RunAudit(mtMoneyCard(), Fx{Base: "EUR"})

	wantMoney := map[string]bool{
		`Is "подкладка" priced or is that a placeholder?`:            true,
		`"Карманка" costs more per metre than the main fabric`:       true,
		"PLN has no rate to EUR: 2 lines drop out of the cost total": true,
	}
	gotMoney := map[string]bool{}
	for _, f := range res.Findings {
		if f.Money {
			gotMoney[f.Title] = true
		}
	}
	for want := range wantMoney {
		if !gotMoney[want] {
			t.Errorf("finding %q must be flagged money (it quotes a price or a currency); flagged: %v", want, mtKeys(gotMoney))
		}
	}
	for got := range gotMoney {
		if !wantMoney[got] {
			t.Errorf("finding %q is flagged money but is not expected to quote a value — over-flagging "+
				"hides a real finding from an account that is entitled to it", got)
		}
	}

	// And the other side: the non-money findings of the very same run stay unflagged. B6 is the
	// interesting one — it talks about cmt_cost and is deliberately NOT money.
	for _, f := range res.Findings {
		if f.Money {
			continue
		}
		// EVERY text field the client draws, not just Detail: a currency reaching Title,
		// Suggestion or Evidence is disclosed exactly as far as one in Detail is.
		for field, txt := range map[string]string{
			"title": f.Title, "detail": f.Detail, "suggestion": f.Suggestion,
			"evidence": strings.Join(f.Evidence, " | "),
		} {
			if strings.Contains(txt, " PLN") || strings.Contains(txt, " EUR") {
				t.Errorf("finding %q is not flagged money, yet its %s names a currency: %q",
					f.Title, field, txt)
			}
		}
	}
}

// TestB6AndB7ShareTheirMoneyClassification guards the pair argument written in bom.go: B6 and B7 are
// two branches of ONE predicate (cmt_cost is set / is not), so flagging one without the other would
// leak the very bit the flag pretends to hide — the reader would infer "cmt is set" from the silence
// of B6. They must agree, whichever way a future review decides.
func TestB6AndB7ShareTheirMoneyClassification(t *testing.T) {
	b6 := mtFindMoneyFlag(t, mtCostingCard(false), "CMT is not set")
	b7 := mtFindMoneyFlag(t, mtCostingCard(true), "CMT is quoted with SMV on")
	if b6 != b7 {
		t.Errorf("B6 money=%v but B7 money=%v — the two branches of cmt_cost must be classified "+
			"together, or the silence of one announces the other", b6, b7)
	}
}

// --- stands ---------------------------------------------------------------------------------------

func mtInReview(c *entity.TechCard) *entity.TechCard {
	c.ApprovalState = entity.TechCardApprovalInReview
	return c
}

func mtKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

// mtMoneyCard fires B5а (a lining at a token price), B5б (a pocketing dearer than the main fabric)
// and B5в (two PLN lines with no rate), on one card, off draft so nothing collapses.
func mtMoneyCard() *entity.TechCard {
	card := &entity.TechCard{Id: 91}
	card.ApprovalState = entity.TechCardApprovalInReview
	card.BomItems = []entity.TechCardBomItem{
		{
			Id: 1, LineKey: "F1", Name: "основная", Section: entity.BomSectionFabric,
			Unit: text("m"), UnitPrice: dec("55"), Currency: text("PLN"),
			Purpose: text("main"),
		},
		{
			Id: 2, LineKey: "F2", Name: "Карманка", Section: entity.BomSectionFabric,
			Unit: text("m"), UnitPrice: dec("60"), Currency: text("PLN"),
			Purpose: text("pocketing"),
		},
		{
			Id: 3, LineKey: "L1", Name: "подкладка", Section: entity.BomSectionLining,
			Unit: text("m"), UnitPrice: dec("1"), Currency: text("EUR"),
		},
	}
	return card
}

// mtCostingCard carries a costing row with (or without) a CMT figure and one unnormed step, so
// exactly one of B6 / B7 speaks.
func mtCostingCard(cmtSet bool) *entity.TechCard {
	card := &entity.TechCard{Id: 92}
	card.ApprovalState = entity.TechCardApprovalInReview
	card.Operations = []entity.TechCardOperation{{
		OperationNumber: sql.NullInt32{Int32: 10, Valid: true},
		OperationType:   entity.OpTypeMachine,
		Zone:            entity.ZoneOuter,
		MachineType:     text("lockstitch"),
	}}
	card.Costing = &entity.TechCardCosting{}
	if cmtSet {
		card.Costing.CmtCost = dec("12")
	}
	return card
}

func mtFindMoneyFlag(t *testing.T, card *entity.TechCard, titleSub string) bool {
	t.Helper()
	for _, f := range RunAudit(card, Fx{Base: "EUR"}).Findings {
		if strings.Contains(f.Title, titleSub) {
			return f.Money
		}
	}
	t.Fatalf("no finding whose title contains %q — the stand no longer fires the check it was built for", titleSub)
	return false
}

// mtAggregateMoneyCard trips the AGGREGATE branch of all three B5 checks at once. It exists because
// every other money fixture keeps each check under the §3.0 threshold (|M| <= 3), so the
// `len(missing) > 3` arm of B5а/B5б/B5в was never executed by any test: un-flagging Money on those
// three findings left the package green while a card with six half-priced fabric lines published
// the ratio to the dearest one in prose.
//
//   - four roll-goods lines priced at zero          -> B5а aggregate
//   - four pocketing/lining lines dearer than every main fabric -> B5б aggregate
//   - four accessory lines in four rateless currencies          -> B5в aggregate
func mtAggregateMoneyCard() *entity.TechCard {
	card := &entity.TechCard{Id: 93}
	card.ApprovalState = entity.TechCardApprovalInReview
	items := []entity.TechCardBomItem{{
		Id: 1, LineKey: "F0", Name: "основная", Section: entity.BomSectionFabric,
		Unit: text("m"), UnitPrice: dec("100"), Currency: text("PLN"), Purpose: text("main"),
	}}
	for i := 0; i < 4; i++ {
		items = append(items, entity.TechCardBomItem{
			Id: 10 + i, LineKey: fmt.Sprintf("Z%d", i), Name: fmt.Sprintf("нулевая %d", i),
			Section: entity.BomSectionFabric, Unit: text("m"),
			UnitPrice: dec("0"), Currency: text("PLN"), Purpose: text("main"),
		})
		items = append(items, entity.TechCardBomItem{
			Id: 20 + i, LineKey: fmt.Sprintf("P%d", i), Name: fmt.Sprintf("карманка %d", i),
			Section: entity.BomSectionFabric, Unit: text("m"),
			UnitPrice: dec("500"), Currency: text("PLN"), Purpose: text("pocketing"),
		})
	}
	for i, code := range []string{"GBP", "USD", "CHF", "SEK"} {
		items = append(items, entity.TechCardBomItem{
			Id: 30 + i, LineKey: fmt.Sprintf("A%d", i), Name: fmt.Sprintf("фурнитура %s", code),
			Section: entity.BomSectionHardware, Unit: text("pc"),
			UnitPrice: dec("7"), Currency: text(code),
		})
	}
	card.BomItems = items
	return card
}

// TestAggregateBranchOfTheMoneyChecksIsFlagged is the §3.0 half of the money classification. The
// per-operation form of B5а/B5б/B5в and their aggregate form are DIFFERENT literals in the source,
// and only the per-item ones were ever built by a test.
func TestAggregateBranchOfTheMoneyChecksIsFlagged(t *testing.T) {
	res := RunAudit(mtAggregateMoneyCard(), Fx{Base: "EUR"})

	// The fixture must actually reach the aggregate arm; a card that quietly stayed under the
	// threshold would make every assertion below vacuous.
	wantAggregates := []string{
		"roll-goods lines look priced with a placeholder",
		"lines cost more per metre than every main fabric",
		"BOM currencies have no rate to EUR",
	}
	for _, want := range wantAggregates {
		var found *Finding
		for i := range res.Findings {
			if strings.Contains(res.Findings[i].Title, want) {
				found = &res.Findings[i]
				break
			}
		}
		if found == nil {
			t.Errorf("the fixture did not reach the aggregate arm of %q — titles: %v",
				want, mtTitles(res.Findings))
			continue
		}
		if !found.Money {
			t.Errorf("aggregate finding %q is not flagged money, yet it states a ratio to the card's "+
				"dearest fabric line; an account without costing:read would read it", found.Title)
		}
	}
}

func mtTitles(fs []Finding) []string {
	out := make([]string, 0, len(fs))
	for _, f := range fs {
		out = append(out, f.Title)
	}
	return out
}
