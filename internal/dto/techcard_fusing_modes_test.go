package dto

import (
	"testing"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	pb_common "github.com/jekabolt/grbpwr-manager/proto/gen/common"
	"github.com/shopspring/decimal"
)

func TestTechCardPieceFusingRingModeRoundTrip(t *testing.T) {
	zone := pbSplineZone(
		pbSplinePoint("0", "0", true), pbSplinePoint("20", "0", true),
		pbSplinePoint("20", "10", true), pbSplinePoint("0", "10", true),
	)
	zone.GetSpline().GradeMode = pb_common.TechCardPieceFusingGradeMode_TECH_CARD_PIECE_FUSING_GRADE_MODE_FOLLOW_LINE
	zone.GetSpline().Anchor = &pb_common.TechCardPieceFusingAnchor{X: dec("12.5"), Y: dec("-3")}
	zone.Islands = []*pb_common.TechCardPieceFusingSpline{{
		Points: []*pb_common.TechCardPieceFusingSplinePoint{
			pbSplinePoint("30", "0", true), pbSplinePoint("40", "0", true),
			pbSplinePoint("40", "10", true), pbSplinePoint("30", "10", true),
		},
		GradeMode: pb_common.TechCardPieceFusingGradeMode_TECH_CARD_PIECE_FUSING_GRADE_MODE_GROW,
	}}
	in := baseTechCardWithPieces([]*pb_common.TechCardPiece{{
		Name: "Body", Fused: true,
		FusingMode: pb_common.TechCardPieceFusingMode_TECH_CARD_PIECE_FUSING_MODE_ZONE.Enum(),
		FusingZone: zone,
	}})
	got, err := ConvertPbTechCardInsertToEntity(in)
	if err != nil {
		t.Fatalf("parse ring modes: %v", err)
	}
	spline := got.Pieces[0].FusingZone.Spline
	if spline.GradeMode != entity.PieceFusingGradeModeFollowLine || spline.Anchor == nil ||
		spline.Anchor.X.String() != "12.5" || spline.Anchor.Y.String() != "-3" {
		t.Fatalf("entity spline mode mismatch: %#v", spline)
	}
	if mode := got.Pieces[0].FusingZone.Islands[0].GradeMode; mode != entity.PieceFusingGradeModeGrow {
		t.Fatalf("entity island mode = %q, want grow", mode)
	}

	out := ConvertEntityTechCardToPb(&entity.TechCard{TechCardInsert: *got}, CostingFx{})
	outZone := out.TechCard.Pieces[0].GetFusingZone()
	if outZone.GetSpline().GetGradeMode() != pb_common.TechCardPieceFusingGradeMode_TECH_CARD_PIECE_FUSING_GRADE_MODE_FOLLOW_LINE ||
		outZone.GetSpline().GetAnchor().GetX().GetValue() != "12.5" || outZone.GetSpline().GetAnchor().GetY().GetValue() != "-3" {
		t.Fatalf("wire spline mode mismatch: %#v", outZone.GetSpline())
	}
	if outZone.GetIslands()[0].GetGradeMode() != pb_common.TechCardPieceFusingGradeMode_TECH_CARD_PIECE_FUSING_GRADE_MODE_GROW ||
		outZone.GetIslands()[0].GetAnchor() != nil {
		t.Fatalf("wire island mode mismatch: %#v", outZone.GetIslands()[0])
	}
}

// A read always names the mode, so a client that echoes what it read states an explicit choice and
// UNKNOWN stays reserved for clients that predate the modes.
func TestTechCardPieceFusingDefaultModesReadExplicit(t *testing.T) {
	zone := pbSplineZone(
		pbSplinePoint("0", "0", true), pbSplinePoint("20", "0", true), pbSplinePoint("20", "10", true),
	)
	in := baseTechCardWithPieces([]*pb_common.TechCardPiece{{
		Name: "Body", Fused: true,
		FusingMode: pb_common.TechCardPieceFusingMode_TECH_CARD_PIECE_FUSING_MODE_ZONE.Enum(),
		FusingZone: zone,
	}, {
		Name: "Placket", Fused: true,
		FusingMode: pb_common.TechCardPieceFusingMode_TECH_CARD_PIECE_FUSING_MODE_STRIP.Enum(),
		FusingEdges: &pb_common.TechCardPieceFusingEdges{Items: []*pb_common.TechCardPieceFusingEdge{{
			Edge: 1, StartMm: dec("10"), EndMm: dec("10"),
			StartMode: pb_common.TechCardPieceFusingEndMode_TECH_CARD_PIECE_FUSING_END_MODE_MM,
		}}},
	}})
	got, err := ConvertPbTechCardInsertToEntity(in)
	if err != nil {
		t.Fatalf("parse default modes: %v", err)
	}
	spline := got.Pieces[0].FusingZone.Spline
	if spline.GradeMode != entity.PieceFusingGradeModeFixed || !spline.GradeModeOmitted {
		t.Fatalf("absent grade mode parsed as %q omitted=%v, want empty and omitted", spline.GradeMode, spline.GradeModeOmitted)
	}
	edge := got.Pieces[1].FusingEdges[0]
	if edge.StartMode != entity.PieceFusingEndModeMm || edge.StartModeOmitted {
		t.Fatalf("explicit MM parsed as %q omitted=%v, want empty and stated", edge.StartMode, edge.StartModeOmitted)
	}
	if edge.EndMode != entity.PieceFusingEndModeMm || !edge.EndModeOmitted {
		t.Fatalf("absent end mode parsed as %q omitted=%v, want empty and omitted", edge.EndMode, edge.EndModeOmitted)
	}
	out := ConvertEntityTechCardToPb(&entity.TechCard{TechCardInsert: *got}, CostingFx{})
	if mode := out.TechCard.Pieces[0].GetFusingZone().GetSpline().GetGradeMode(); mode != pb_common.TechCardPieceFusingGradeMode_TECH_CARD_PIECE_FUSING_GRADE_MODE_FIXED {
		t.Fatalf("fixed ring read back as %v, want FIXED", mode)
	}
	wire := out.TechCard.Pieces[1].GetFusingEdges().GetItems()[0]
	if wire.GetStartMode() != pb_common.TechCardPieceFusingEndMode_TECH_CARD_PIECE_FUSING_END_MODE_MM ||
		wire.GetEndMode() != pb_common.TechCardPieceFusingEndMode_TECH_CARD_PIECE_FUSING_END_MODE_MM {
		t.Fatalf("mm ends read back as %v / %v, want MM", wire.GetStartMode(), wire.GetEndMode())
	}
}

func TestTechCardPieceFusingEdgeModeRoundTrip(t *testing.T) {
	in := baseTechCardWithPieces([]*pb_common.TechCardPiece{{
		Name: "Placket", Fused: true,
		FusingMode: pb_common.TechCardPieceFusingMode_TECH_CARD_PIECE_FUSING_MODE_STRIP.Enum(),
		FusingEdges: &pb_common.TechCardPieceFusingEdges{Items: []*pb_common.TechCardPieceFusingEdge{{
			Edge: 2, StartMm: dec("10"), EndMm: dec("35.5"),
			StartMode: pb_common.TechCardPieceFusingEndMode_TECH_CARD_PIECE_FUSING_END_MODE_NOTCH,
			EndMode:   pb_common.TechCardPieceFusingEndMode_TECH_CARD_PIECE_FUSING_END_MODE_SHARE,
		}}},
	}})
	got, err := ConvertPbTechCardInsertToEntity(in)
	if err != nil {
		t.Fatalf("parse edge modes: %v", err)
	}
	edge := got.Pieces[0].FusingEdges[0]
	if edge.StartMode != entity.PieceFusingEndModeNotch || edge.EndMode != entity.PieceFusingEndModeShare {
		t.Fatalf("entity edge modes = %q / %q", edge.StartMode, edge.EndMode)
	}
	out := ConvertEntityTechCardToPb(&entity.TechCard{TechCardInsert: *got}, CostingFx{})
	wire := out.TechCard.Pieces[0].GetFusingEdges().GetItems()[0]
	if wire.GetStartMode() != pb_common.TechCardPieceFusingEndMode_TECH_CARD_PIECE_FUSING_END_MODE_NOTCH ||
		wire.GetEndMode() != pb_common.TechCardPieceFusingEndMode_TECH_CARD_PIECE_FUSING_END_MODE_SHARE ||
		wire.GetEndMm().GetValue() != "35.5" {
		t.Fatalf("wire edge mismatch: %#v", wire)
	}
}

func TestTechCardPieceFusingModesRejectBadInput(t *testing.T) {
	ring := func(mode pb_common.TechCardPieceFusingGradeMode, anchor *pb_common.TechCardPieceFusingAnchor) *pb_common.TechCardInsert {
		zone := pbSplineZone(
			pbSplinePoint("0", "0", true), pbSplinePoint("20", "0", true), pbSplinePoint("20", "10", true),
		)
		zone.GetSpline().GradeMode = mode
		zone.GetSpline().Anchor = anchor
		return baseTechCardWithPieces([]*pb_common.TechCardPiece{{
			Name: "Body", Fused: true,
			FusingMode: pb_common.TechCardPieceFusingMode_TECH_CARD_PIECE_FUSING_MODE_ZONE.Enum(),
			FusingZone: zone,
		}})
	}
	anchor := &pb_common.TechCardPieceFusingAnchor{X: dec("1"), Y: dec("1")}
	cases := map[string]*pb_common.TechCardInsert{
		"unknown ring mode":        ring(pb_common.TechCardPieceFusingGradeMode(99), nil),
		"follow line with no line": ring(pb_common.TechCardPieceFusingGradeMode_TECH_CARD_PIECE_FUSING_GRADE_MODE_FOLLOW_LINE, nil),
		"anchor on a fixed ring":   ring(pb_common.TechCardPieceFusingGradeMode_TECH_CARD_PIECE_FUSING_GRADE_MODE_UNKNOWN, anchor),
		"anchor on a growing ring": ring(pb_common.TechCardPieceFusingGradeMode_TECH_CARD_PIECE_FUSING_GRADE_MODE_GROW, anchor),
		"unknown end mode": baseTechCardWithPieces([]*pb_common.TechCardPiece{{
			Name: "Placket", Fused: true,
			FusingMode: pb_common.TechCardPieceFusingMode_TECH_CARD_PIECE_FUSING_MODE_STRIP.Enum(),
			FusingEdges: &pb_common.TechCardPieceFusingEdges{Items: []*pb_common.TechCardPieceFusingEdge{{
				Edge: 1, EndMode: pb_common.TechCardPieceFusingEndMode(42),
			}}},
		}}),
	}
	for name, in := range cases {
		if _, err := ConvertPbTechCardInsertToEntity(in); err == nil {
			t.Errorf("%s: accepted, want a field violation", name)
		}
	}
}

func TestFusingModesMoveConstructionDigestOnlyWhenSet(t *testing.T) {
	zonePiece := fusingPiece()
	zonePiece.FusingMode.Valid = true
	zonePiece.FusingMode.String = string(entity.PieceFusingModeZone)
	zonePiece.FusingZone = &entity.TechCardPieceFusingZone{Spline: &entity.TechCardPieceFusingSpline{Points: []entity.TechCardPieceFusingSplinePoint{
		{X: decimal.NewFromInt(0), Y: decimal.NewFromInt(0), Corner: true},
		{X: decimal.NewFromInt(10), Y: decimal.NewFromInt(0), Corner: true},
		{X: decimal.NewFromInt(0), Y: decimal.NewFromInt(10), Corner: true},
	}}}
	// Frozen before the modes existed: a fixed ring must keep hashing to exactly this.
	const fixedRingHex = "9908f0d64d56a16d4d7bbd6eeb87fa570a11287cff84acf81be9ac1f2544d4e8"
	fixed := TechCardSectionDigests(fusingCard(zonePiece))[entity.SignoffConstruction]
	if fixed != fixedRingHex {
		t.Fatalf("fixed ring digest moved: got %s, want %s", fixed, fixedRingHex)
	}
	zonePiece.FusingZone.Spline.GradeMode = entity.PieceFusingGradeModeGrow
	grow := TechCardSectionDigests(fusingCard(zonePiece))[entity.SignoffConstruction]
	if grow == fixed {
		t.Fatal("switching a ring to grow did not move the construction digest")
	}
	zonePiece.FusingZone.Spline.GradeMode = entity.PieceFusingGradeModeFollowLine
	zonePiece.FusingZone.Spline.Anchor = &entity.TechCardPieceFusingAnchor{X: decimal.NewFromInt(4), Y: decimal.NewFromInt(4)}
	follow := TechCardSectionDigests(fusingCard(zonePiece))[entity.SignoffConstruction]
	if follow == grow || follow == fixed {
		t.Fatal("switching a ring to follow line did not move the construction digest")
	}
	zonePiece.FusingZone.Spline.Anchor.X = decimal.NewFromInt(5)
	if moved := TechCardSectionDigests(fusingCard(zonePiece))[entity.SignoffConstruction]; moved == follow {
		t.Fatal("moving the anchor did not move the construction digest")
	}

	stripPiece := fusingPiece()
	stripPiece.FusingMode.Valid = true
	stripPiece.FusingMode.String = string(entity.PieceFusingModeStrip)
	stripPiece.FusingEdges = []entity.TechCardPieceFusingEdge{{Edge: 1, StartMm: decimal.NewFromInt(10), EndMm: decimal.NewFromInt(10)}}
	const mmEdgeHex = "bc1b9214cc2e61abeb3ca9727db0ff9cdb61b71cd7be1bdc7f1a9839fef1e950"
	mm := TechCardSectionDigests(fusingCard(stripPiece))[entity.SignoffConstruction]
	if mm != mmEdgeHex {
		t.Fatalf("mm edge digest moved: got %s, want %s", mm, mmEdgeHex)
	}
	stripPiece.FusingEdges[0].EndMode = entity.PieceFusingEndModeNotch
	notch := TechCardSectionDigests(fusingCard(stripPiece))[entity.SignoffConstruction]
	stripPiece.FusingEdges[0].EndMode = entity.PieceFusingEndModeMm
	stripPiece.FusingEdges[0].StartMode = entity.PieceFusingEndModeNotch
	startNotch := TechCardSectionDigests(fusingCard(stripPiece))[entity.SignoffConstruction]
	if notch == mm || startNotch == mm || notch == startNotch {
		t.Fatal("strip end modes did not move the construction digest distinctly")
	}
}
