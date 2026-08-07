package dto

import (
	"testing"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	pb_common "github.com/jekabolt/grbpwr-manager/proto/gen/common"
)

func gapReportFixture() entity.FabricDirectionGapReport {
	return entity.FabricDirectionGapReport{
		Cards: []entity.FabricDirectionGapCard{{
			TechCardID: 3, StyleNumber: "SS26-001", Name: "Coat",
			Stage: string(entity.TechCardStageProto), ApprovalState: string(entity.TechCardApprovalDraft),
			LinkedMarkerCount: 2,
			Lines: []entity.FabricDirectionGapLine{{
				BomItemID: 11, LineKey: "01K", Name: "ВЕЛЬВЕТ ИЗ КАТАЛОГА",
				Section: string(entity.BomSectionFabric), Purpose: string(entity.BomPurposeMain),
				BlockedMarkerCount: 2,
			}},
		}},
		TotalCards: 1, TotalLines: 1,
		Excluded: []entity.FabricDirectionGapExclusion{
			{ApprovalState: string(entity.TechCardApprovalReleased), Cards: 4, Lines: 9},
		},
		ExcludedCards: 4, ExcludedLines: 9,
	}
}

// The rows travel as the SAME enums the BOM tab reads, and the deferred-rows breakdown travels
// whatever else happens — a client must never be handed a filtered list with the filter missing.
func TestFabricDirectionGapReportToPb(t *testing.T) {
	out := FabricDirectionGapReportToPb(gapReportFixture(), false)
	if len(out.GetCards()) != 1 || len(out.GetCards()[0].GetLines()) != 1 {
		t.Fatalf("rows lost in conversion: %+v", out.GetCards())
	}
	line := out.GetCards()[0].GetLines()[0]
	if line.GetSection() != pb_common.TechCardBomSection_TECH_CARD_BOM_SECTION_FABRIC {
		t.Errorf("section = %v", line.GetSection())
	}
	if line.GetPurpose() != pb_common.TechCardBomPurpose_TECH_CARD_BOM_PURPOSE_MAIN {
		t.Errorf("purpose = %v", line.GetPurpose())
	}
	if line.GetName() != "ВЕЛЬВЕТ ИЗ КАТАЛОГА" {
		t.Errorf("name = %q", line.GetName())
	}
	card := out.GetCards()[0]
	if !card.GetMarkerSavePossible() || card.GetBlockedMarkerCount() != 2 || card.GetLinkedMarkerCount() != 2 {
		t.Errorf("card facts = %+v", card)
	}
	if len(out.GetExcluded()) != 1 ||
		out.GetExcluded()[0].GetApprovalState() != pb_common.TechCardApprovalState_TECH_CARD_APPROVAL_STATE_RELEASED {
		t.Errorf("excluded breakdown = %+v", out.GetExcluded())
	}
}

// counts_only is the release-gate form: constant-size, same numbers. The one thing it must NOT drop
// is the deferred-rows breakdown — that is the part of the answer saying the report is filtering,
// and the gate is exactly the caller who must not miss it.
func TestFabricDirectionGapReportToPbCountsOnly(t *testing.T) {
	r := gapReportFixture()
	full := FabricDirectionGapReportToPb(r, false)
	counts := FabricDirectionGapReportToPb(r, true)

	if len(counts.GetCards()) != 0 {
		t.Fatalf("counts_only must not carry rows, got %d", len(counts.GetCards()))
	}
	if counts.GetTotalCards() != full.GetTotalCards() ||
		counts.GetTotalLines() != full.GetTotalLines() ||
		counts.GetExcludedCards() != full.GetExcludedCards() ||
		counts.GetExcludedLines() != full.GetExcludedLines() {
		t.Fatalf("counts_only changed the numbers: %+v vs %+v", counts, full)
	}
	if len(counts.GetExcluded()) != len(full.GetExcluded()) {
		t.Fatalf("counts_only dropped the exclusion breakdown: %+v", counts.GetExcluded())
	}
	// The gate reads one predicate off this response; pin that it is answerable without rows.
	if counts.GetTotalLines()+counts.GetExcludedLines() == 0 {
		t.Fatal("fixture should be non-zero, or the gate assertion proves nothing")
	}
}
