package techcard

import (
	"database/sql"
	"testing"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
)

func TestRefuseNewlyUnresolvedPatternsGrandfathersOnlyStoredOrphans(t *testing.T) {
	beforeLines := []entity.RollGoodsLine{
		{LineKey: "MAIN-A", Purpose: "main", Section: entity.BomSectionFabric},
	}
	before := []patternHistoryRow{
		{LineKey: "SHEET-LIVE", URL: "live.dxf", Name: sql.NullString{String: "live sheet", Valid: true},
			FabricPurpose: sql.NullString{String: "main", Valid: true}},
		{LineKey: "SHEET-LEGACY", URL: "legacy.dxf", Name: sql.NullString{String: "legacy orphan", Valid: true},
			FabricPurpose: sql.NullString{String: "mesh", Valid: true}},
	}

	if err := refuseNewlyUnresolvedPatterns(before, beforeLines, before, beforeLines); err != nil {
		t.Fatalf("stored orphan must remain saveable: %v", err)
	}
	after := append([]patternHistoryRow(nil), before...)
	after[0].FabricPurpose = sql.NullString{String: "main", Valid: true}
	err := refuseNewlyUnresolvedPatterns(before, beforeLines, after, nil)
	violation, ok := err.(*entity.ValidationError)
	if !ok {
		t.Fatalf("new orphan error = %v, want field violation", err)
	}
	if violation.Field != "patterns" || violation.Reason != "new_unresolved_sheets" ||
		violation.Conflicting != "live sheet" {
		t.Fatalf("violation = %#v, want the newly orphaned sheet named", violation)
	}
}
