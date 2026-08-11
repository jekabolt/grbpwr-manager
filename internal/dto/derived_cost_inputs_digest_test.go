package dto

import (
	"database/sql"
	"testing"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/shopspring/decimal"
)

func costingCard() *entity.TechCardInsert {
	return &entity.TechCardInsert{
		SizeIds: []int{4, 5},
		Costing: &entity.TechCardCosting{
			CmtCost:  decimal.NullDecimal{Decimal: decimal.RequireFromString("12.50"), Valid: true},
			Currency: sql.NullString{String: "EUR", Valid: true},
		},
	}
}

// TestCostingDigestShapeIsATailNotASlot locks the SHAPE of the projection, and only the shape.
//
// It deliberately does not exercise the store: what it guards is that the derived-inputs element is
// APPENDED and not written unconditionally. The set-level questions (does a legacy row count, does
// an archived colourway count, does read agree with write) live where the sets are built and cannot
// be answered here.
//
// The new cost inputs (measured piece areas, piece→fabric assignments) enter costingProjection as a
// TAIL — appended only when non-empty. If anybody ever makes that element unconditional, the
// fingerprint of EVERY card in the database moves and every approved COSTING sign-off is declared
// stale at the moment of deploy, before a human has touched anything. That failure is silent: the
// code compiles, the digests are internally consistent, and the damage shows up as a re-approval
// wave across the whole catalogue.
//
// The constant below is the digest of a card that has no areas and no piece-bound recipe rows. It is
// pinned deliberately: a change to it means the wire meaning of an existing signature changed, and
// that must be a conscious act with a rebase plan, never a side effect.
func TestCostingDigestShapeIsATailNotASlot(t *testing.T) {
	const pinned = "f97320b8f665524d44dae58c0a6fbfc52a59628dbf2272e19c4c6fad059fe40d"
	got := TechCardSectionDigests(costingCard())[entity.SignoffCosting]
	if got == "" {
		t.Fatal("no COSTING digest produced")
	}
	if got != pinned {
		t.Errorf("COSTING digest of a card WITHOUT derived cost inputs moved:\n got  %s\n want %s\n"+
			"If this was intentional, every approved COSTING sign-off in the database goes stale — "+
			"say so explicitly and re-pin. If it was not, the derived-inputs element stopped being a tail.", got, pinned)
	}
}

// TestCostingDigestMovesWithDerivedInputs is the other half: the tail must actually bite. A card
// whose areas or assignments changed MUST read as «changed since sign-off», because the number under
// that signature moved with them.
func TestCostingDigestMovesWithDerivedInputs(t *testing.T) {
	base := TechCardSectionDigests(costingCard())[entity.SignoffCosting]
	withDerived := costingCard()
	withDerived.DerivedCostInputsDigest = "abc123"
	got := TechCardSectionDigests(withDerived)[entity.SignoffCosting]
	if got == base {
		t.Fatal("COSTING digest ignored the derived cost inputs — a re-measured card would keep a green signature over a changed cost")
	}
	other := costingCard()
	other.DerivedCostInputsDigest = "def456"
	if TechCardSectionDigests(other)[entity.SignoffCosting] == got {
		t.Fatal("two different derived-input sets produced the same digest")
	}
}

// TestOtherSectionsIgnoreDerivedInputs: the areas and assignments are a COSTING fact. Folding them
// into DESIGN or LABELS would stale signatures over content those approvers never claimed.
func TestOtherSectionsIgnoreDerivedInputs(t *testing.T) {
	base := TechCardSectionDigests(costingCard())
	withDerived := costingCard()
	withDerived.DerivedCostInputsDigest = "abc123"
	got := TechCardSectionDigests(withDerived)
	for section, want := range base {
		if section == entity.SignoffCosting {
			continue
		}
		if got[section] != want {
			t.Errorf("section %v moved on a derived-cost-input change; only COSTING may", section)
		}
	}
}
