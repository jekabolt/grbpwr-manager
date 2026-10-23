package fusingops

import (
	"database/sql"
	"testing"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
)

func TestCascadeRemovedInterliningClearsEveryOwnedReference(t *testing.T) {
	interlining := entity.TechCardBomItem{LineKey: "FUSE-A", Section: entity.BomSectionInterlining}
	fabric := entity.TechCardBomItem{LineKey: "FABRIC-A", Section: entity.BomSectionFabric}
	auto := entity.TechCardOperation{
		OperationNumber:    sql.NullInt32{Int32: 10, Valid: true},
		OperationType:      entity.OpTypeFusing,
		Draft:              false,
		Review:             sql.NullString{String: "remapped", Valid: true},
		AutoFusingPieceKey: sql.NullString{String: "FRONT", Valid: true},
		PieceLineKeys:      []string{"FRONT"},
		BomLineKeys:        []string{"FUSE-A"},
	}
	manual := entity.TechCardOperation{
		OperationNumber: sql.NullInt32{Int32: 20, Valid: true},
		OperationType:   entity.OpTypeFusing,
		Note:            sql.NullString{String: "press by hand", Valid: true},
		PieceLineKeys:   []string{"BACK"},
		BomLineKeys:     []string{"FUSE-A"},
	}
	stored := &entity.TechCardInsert{
		BomItems: []entity.TechCardBomItem{fabric, interlining},
		Pieces: []entity.TechCardPiece{{
			LineKey:              "FRONT",
			Fused:                true,
			FusingMode:           sql.NullString{String: string(entity.PieceFusingModeZone), Valid: true},
			FusingBomLineKey:     "FUSE-A",
			FusingBomItemId:      sql.NullInt64{Int64: 9, Valid: true},
			FusingZone:           &entity.TechCardPieceFusingZone{DxfPieceKey: "FUSE-ZONE"},
			FusingZoneOmitted:    true,
			FusingBomOmitted:     true,
			FusingEdgesOmitted:   true,
			FusingGrainOmitted:   true,
			FusingDartsOmitted:   true,
			FusingInsetMmOmitted: true,
			Materials: []entity.TechCardPieceMaterial{{
				ColorwayID:         7,
				FusingBomLineKey:   "FUSE-A",
				FusingBomItemId:    sql.NullInt64{Int64: 9, Valid: true},
				FusingBomItemIndex: sql.NullInt32{Int32: 1, Valid: true},
			}},
		}},
		Operations: []entity.TechCardOperation{auto, manual},
		Issues: []entity.TechCardIssue{
			{OperationNumber: sql.NullInt32{Int32: 10, Valid: true}, Description: "automatic"},
			{OperationNumber: sql.NullInt32{Int32: 20, Valid: true}, Description: "manual"},
		},
		Patterns: []entity.TechCardSizePattern{{
			LineKey:    "FUSE-SHEET",
			URL:        "fuse.dxf",
			Name:       sql.NullString{String: "own fusing DXF", Valid: true},
			BomLineKey: sql.NullString{String: "FUSE-A", Valid: true},
		}},
		PieceDxfAliases: []entity.TechCardPieceDxfAlias{
			{BomLineKey: "FUSE-A", BlockName: "FRONT-FUSE", PieceLineKey: "FRONT"},
			{BomLineKey: "FABRIC-A", BlockName: "FRONT", PieceLineKey: "FRONT"},
		},
		PieceDxfAliasesSet: true,
	}
	incomingAuto := auto
	incomingAuto.AutoFusingPieceKey = sql.NullString{} // private on the wire
	incoming := &entity.TechCardInsert{
		BomItems:   []entity.TechCardBomItem{fabric},
		Pieces:     append([]entity.TechCardPiece(nil), stored.Pieces...),
		Operations: []entity.TechCardOperation{incomingAuto, manual},
		Issues:     append([]entity.TechCardIssue(nil), stored.Issues...),
		Patterns:   append([]entity.TechCardSizePattern(nil), stored.Patterns...),
		// Simulate a stale client: aliases were absent, so the cascade must carry the unrelated one.
		PieceDxfAliasesSet: false,
	}

	if !CascadeRemovedInterlining(stored, incoming) {
		t.Fatal("CascadeRemovedInterlining() reported no change")
	}
	piece := incoming.Pieces[0]
	if piece.Fused || piece.FusingMode.Valid || piece.FusingBomLineKey != "" || piece.FusingZone != nil {
		t.Fatalf("piece fusing survived: %#v", piece)
	}
	if got := piece.Materials[0]; got.FusingBomLineKey != "" || got.FusingBomItemId.Valid || got.FusingBomItemIndex.Valid {
		t.Fatalf("piece material fusing survived: %#v", got)
	}
	if len(incoming.Operations) != 1 || incoming.Operations[0].Note.String != "press by hand" ||
		len(incoming.Operations[0].BomLineKeys) != 0 {
		t.Fatalf("operations = %#v, want the unlinked manual step only", incoming.Operations)
	}
	if incoming.Issues[0].OperationNumber.Valid || !incoming.Issues[1].OperationNumber.Valid {
		t.Fatalf("issue links = %#v", incoming.Issues)
	}
	if len(incoming.Patterns) != 0 {
		t.Fatalf("bound fusing sheets survived: %#v", incoming.Patterns)
	}
	if !incoming.PieceDxfAliasesSet || len(incoming.PieceDxfAliases) != 1 ||
		incoming.PieceDxfAliases[0].BomLineKey != "FABRIC-A" {
		t.Fatalf("aliases = %#v", incoming.PieceDxfAliases)
	}
}

func TestDropDanglingFusingRefsRepairsOldReadModel(t *testing.T) {
	tc := &entity.TechCardInsert{
		BomItems: []entity.TechCardBomItem{{LineKey: "FABRIC-A", Section: entity.BomSectionFabric}},
		Pieces:   []entity.TechCardPiece{{LineKey: "FRONT", Fused: true, FusingBomLineKey: "GONE"}},
		Operations: []entity.TechCardOperation{{
			OperationNumber:    sql.NullInt32{Int32: 10, Valid: true},
			OperationType:      entity.OpTypeFusing,
			AutoFusingPieceKey: sql.NullString{String: "FRONT", Valid: true},
			BomLineKeys:        []string{"GONE"},
		}},
		Patterns: []entity.TechCardSizePattern{{
			Name:       sql.NullString{String: generatedFusingPatternPrefix + "front", Valid: true},
			BomLineKey: sql.NullString{String: "GONE", Valid: true},
		}},
		PieceDxfAliases: []entity.TechCardPieceDxfAlias{{BomLineKey: "GONE", BlockName: "FRONT"}},
	}
	if !DropDanglingFusingRefs(tc) {
		t.Fatal("DropDanglingFusingRefs() reported no change")
	}
	if tc.Pieces[0].Fused || tc.Pieces[0].FusingBomLineKey != "" || len(tc.Operations) != 0 ||
		len(tc.Patterns) != 0 || len(tc.PieceDxfAliases) != 0 {
		t.Fatalf("dangling read model survived: %#v", tc)
	}
}

func TestCascadeRemovedInterliningDeletesReviewedLegacyAutoAfterClientUnlinksIt(t *testing.T) {
	line := entity.TechCardBomItem{LineKey: "FUSE-A", Section: entity.BomSectionInterlining}
	legacy := entity.TechCardOperation{
		OperationNumber:   sql.NullInt32{Int32: 10, Valid: true},
		OperationType:     entity.OpTypeFusing,
		Draft:             false,
		Review:            sql.NullString{String: "remapped", Valid: true},
		AutoFusingLineKey: sql.NullString{String: "FUSE-A", Valid: true},
		PieceLineKeys:     []string{"FRONT"},
		BomLineKeys:       []string{"FUSE-A"},
	}
	stored := &entity.TechCardInsert{BomItems: []entity.TechCardBomItem{line}, Operations: []entity.TechCardOperation{legacy}}
	echo := legacy
	echo.AutoFusingLineKey = sql.NullString{}
	echo.BomLineKeys = nil // the admin releases the visible reference before submitting the delete
	incoming := &entity.TechCardInsert{Operations: []entity.TechCardOperation{echo}}

	CascadeRemovedInterlining(stored, incoming)
	if len(incoming.Operations) != 0 {
		t.Fatalf("legacy automatic operation survived: %#v", incoming.Operations)
	}
}
