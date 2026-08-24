package techcardanalysis

import (
	"database/sql"
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
		if strings.Contains(f.Detail, " PLN") || strings.Contains(f.Detail, " EUR") {
			t.Errorf("finding %q is not flagged money, yet its detail names a currency: %q", f.Title, f.Detail)
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
