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
	zone := pbSplineZone(
		pbSplinePoint("0", "0", true), pbSplinePoint("20.5", "0", false),
		pbSplinePoint("20", "10", true), pbSplinePoint("0", "10", false),
	)
	zone.Holes = []*pb_common.TechCardPieceFusingSpline{{Points: []*pb_common.TechCardPieceFusingSplinePoint{
		pbSplinePoint("2", "2", true), pbSplinePoint("5", "2", true),
		pbSplinePoint("5", "5", true), pbSplinePoint("2", "5", true),
	}}}
	in := baseTechCardWithPieces([]*pb_common.TechCardPiece{{
		Name: "Body", Fused: true,
		FusingMode: pb_common.TechCardPieceFusingMode_TECH_CARD_PIECE_FUSING_MODE_ZONE.Enum(),
		FusingZone: zone,
	}})
	got, err := ConvertPbTechCardInsertToEntity(in)
	if err != nil {
		t.Fatalf("parse spline: %v", err)
	}
	points := got.Pieces[0].FusingZone.Spline.Points
	if len(points) != 4 || points[1].X.String() != "20.5" || points[1].Corner || !points[2].Corner {
		t.Fatalf("entity spline mismatch: %#v", points)
	}
	holes := got.Pieces[0].FusingZone.Holes
	if len(holes) != 1 || len(holes[0].Points) != 4 || holes[0].Points[2].X.String() != "5" {
		t.Fatalf("entity holes mismatch: %#v", holes)
	}

	out := ConvertEntityTechCardToPb(&entity.TechCard{TechCardInsert: *got}, CostingFx{})
	outZone := out.TechCard.Pieces[0].GetFusingZone()
	roundTrip := outZone.GetSpline().GetPoints()
	if len(roundTrip) != 4 || roundTrip[1].GetX().GetValue() != "20.5" || roundTrip[1].GetCorner() || !roundTrip[2].GetCorner() {
		t.Fatalf("wire spline mismatch: %#v", roundTrip)
	}
	if len(outZone.GetHoles()) != 1 || len(outZone.GetHoles()[0].GetPoints()) != 4 ||
		outZone.GetHoles()[0].GetPoints()[2].GetX().GetValue() != "5" {
		t.Fatalf("wire holes mismatch: %#v", outZone.GetHoles())
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
	piece.FusingZone.Holes = []entity.TechCardPieceFusingSpline{{Points: []entity.TechCardPieceFusingSplinePoint{
		{X: decimal.NewFromInt(1), Y: decimal.NewFromInt(1), Corner: true},
		{X: decimal.NewFromInt(2), Y: decimal.NewFromInt(1), Corner: true},
		{X: decimal.NewFromInt(1), Y: decimal.NewFromInt(2), Corner: true},
	}}}
	withHole := TechCardSectionDigests(fusingCard(piece))[entity.SignoffConstruction]
	if after == withHole {
		t.Fatal("adding a spline hole did not move the construction digest")
	}
	piece.FusingZone.Holes[0].Points[1].X = decimal.NewFromInt(3)
	changedHole := TechCardSectionDigests(fusingCard(piece))[entity.SignoffConstruction]
	if withHole == changedHole {
		t.Fatal("changing a spline hole did not move the construction digest")
	}
}
