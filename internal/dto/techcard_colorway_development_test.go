package dto

import (
	"database/sql"
	"testing"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
)

// TestColorwayRefEmitsDevelopmentBlock locks audit #30: the colourway development block — the
// colour's internal code and label, the development comment, the pantone reference and its book,
// the screen hex and the approved swatch — is persisted by the Colorway write path and used to be
// returned by NO read path at all. The tech-card read now mirrors it beside the lab-dip scalars it
// belongs with, so the colourways tab can show which pantone a lab dip is chasing.
func TestColorwayRefEmitsDevelopmentBlock(t *testing.T) {
	tc := &entity.TechCard{}
	tc.Name = "Coat"
	tc.Colorways = []entity.TechCardColorway{{
		Id:            101,
		ColorCode:     "BLK",
		Code:          sql.NullString{String: "CW-BLK-01", Valid: true},
		Name:          "Raven Black",
		Comment:       sql.NullString{String: "third dip, warmer", Valid: true},
		Pantone:       sql.NullString{String: "19-4005", Valid: true},
		PantoneSystem: sql.NullString{String: "TCX", Valid: true},
		Hex:           sql.NullString{String: "#1B1B1B", Valid: true},
		SwatchMediaId: sql.NullInt32{Int32: 77, Valid: true},
	}}

	pb := ConvertEntityTechCardToPb(tc, CostingFx{})
	if len(pb.Colorways) != 1 {
		t.Fatalf("expected 1 colourway ref, got %d", len(pb.Colorways))
	}
	ref := pb.Colorways[0]
	if ref.DevCode != "CW-BLK-01" || ref.DevName != "Raven Black" || ref.DevComment != "third dip, warmer" ||
		ref.Pantone != "19-4005" || ref.PantoneSystem != "TCX" || ref.DevHex != "#1B1B1B" || ref.SwatchMediaId != 77 {
		t.Errorf("development block not emitted on the colourway ref: %+v", ref)
	}
	// The identity fields the ref already carried must keep their shape.
	if ref.ColorwayId != 101 || ref.ColorCode != "BLK" {
		t.Errorf("existing ref identity changed: %+v", ref)
	}
}

// TestColorwayRefDevelopmentBlockUnsetStaysEmpty checks a colourway with no development data emits
// zero values rather than anything invented — NULL columns are "not developed yet", and the tab
// distinguishes that from a real value.
func TestColorwayRefDevelopmentBlockUnsetStaysEmpty(t *testing.T) {
	tc := &entity.TechCard{}
	tc.Colorways = []entity.TechCardColorway{{Id: 102, ColorCode: "WHT"}}

	pb := ConvertEntityTechCardToPb(tc, CostingFx{})
	if len(pb.Colorways) != 1 {
		t.Fatalf("expected 1 colourway ref, got %d", len(pb.Colorways))
	}
	ref := pb.Colorways[0]
	if ref.DevCode != "" || ref.DevName != "" || ref.DevComment != "" || ref.Pantone != "" ||
		ref.PantoneSystem != "" || ref.DevHex != "" || ref.SwatchMediaId != 0 {
		t.Errorf("undeveloped colourway must emit an empty development block: %+v", ref)
	}
}
