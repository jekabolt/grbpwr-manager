package dto

import (
	"testing"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	pb_common "github.com/jekabolt/grbpwr-manager/proto/gen/common"
	"github.com/shopspring/decimal"
)

func pbSplinePoint(x, y string, corner bool) *pb_common.TechCardPieceFusingSplinePoint {
	return &pb_common.TechCardPieceFusingSplinePoint{X: dec(x), Y: dec(y), Corner: corner}
}

func pbSplineZone(points ...*pb_common.TechCardPieceFusingSplinePoint) *pb_common.TechCardPieceFusingZone {
	return &pb_common.TechCardPieceFusingZone{Shape: &pb_common.TechCardPieceFusingZone_Spline{
		Spline: &pb_common.TechCardPieceFusingSpline{Points: points},
	}}
}

func TestTechCardPieceFusingSplineRoundTrip(t *testing.T) {
	in := baseTechCardWithPieces([]*pb_common.TechCardPiece{{
		Name: "Body", Fused: true,
		FusingMode: pb_common.TechCardPieceFusingMode_TECH_CARD_PIECE_FUSING_MODE_ZONE.Enum(),
		FusingZone: pbSplineZone(
			pbSplinePoint("0", "0", true), pbSplinePoint("20.5", "0", false),
			pbSplinePoint("20", "10", true), pbSplinePoint("0", "10", false),
		),
	}})
	got, err := ConvertPbTechCardInsertToEntity(in)
	if err != nil {
		t.Fatalf("parse spline: %v", err)
	}
	points := got.Pieces[0].FusingZone.Spline.Points
	if len(points) != 4 || points[1].X.String() != "20.5" || points[1].Corner || !points[2].Corner {
		t.Fatalf("entity spline mismatch: %#v", points)
	}

	out := ConvertEntityTechCardToPb(&entity.TechCard{TechCardInsert: *got}, CostingFx{})
	roundTrip := out.TechCard.Pieces[0].GetFusingZone().GetSpline().GetPoints()
	if len(roundTrip) != 4 || roundTrip[1].GetX().GetValue() != "20.5" || roundTrip[1].GetCorner() || !roundTrip[2].GetCorner() {
		t.Fatalf("wire spline mismatch: %#v", roundTrip)
	}
}

func TestTechCardPieceFusingSplineRejectsNonNumericPoint(t *testing.T) {
	in := baseTechCardWithPieces([]*pb_common.TechCardPiece{{
		Name: "Body", Fused: true,
		FusingMode: pb_common.TechCardPieceFusingMode_TECH_CARD_PIECE_FUSING_MODE_ZONE.Enum(),
		FusingZone: pbSplineZone(
			pbSplinePoint("NaN", "0", true), pbSplinePoint("10", "0", true), pbSplinePoint("0", "10", true)),
	}})
	if _, err := ConvertPbTechCardInsertToEntity(in); err == nil {
		t.Fatal("non-numeric spline coordinate accepted")
	}
}

func TestFusingSplineMovesConstructionDigest(t *testing.T) {
	piece := fusingPiece()
	piece.FusingMode.Valid = true
	piece.FusingMode.String = string(entity.PieceFusingModeZone)
	piece.FusingZone = &entity.TechCardPieceFusingZone{Spline: &entity.TechCardPieceFusingSpline{Points: []entity.TechCardPieceFusingSplinePoint{
		{X: decimal.RequireFromString("0"), Y: decimal.RequireFromString("0"), Corner: true},
		{X: decimal.RequireFromString("10"), Y: decimal.RequireFromString("0"), Corner: true},
		{X: decimal.RequireFromString("0"), Y: decimal.RequireFromString("10"), Corner: true},
	}}}
	before := TechCardSectionDigests(fusingCard(piece))[entity.SignoffConstruction]
	piece.FusingZone.Spline.Points[1].Corner = false
	after := TechCardSectionDigests(fusingCard(piece))[entity.SignoffConstruction]
	if before == after {
		t.Fatal("changing a spline corner did not move the construction digest")
	}
}
