package fusingops

import (
	"testing"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/shopspring/decimal"
)

func fusingSplineCard(corner bool) *entity.TechCardInsert {
	return &entity.TechCardInsert{Pieces: []entity.TechCardPiece{{
		LineKey: "FRONT", Fused: true,
		FusingZone: &entity.TechCardPieceFusingZone{Spline: &entity.TechCardPieceFusingSpline{
			Points: []entity.TechCardPieceFusingSplinePoint{
				{X: decimal.Zero, Y: decimal.Zero, Corner: true},
				{X: decimal.NewFromInt(10), Y: decimal.Zero, Corner: corner},
				{X: decimal.Zero, Y: decimal.NewFromInt(10), Corner: true},
			},
		}},
	}}}
}

func TestFusingStateChangedIncludesSpline(t *testing.T) {
	stored := fusingSplineCard(true)
	same := fusingSplineCard(true)
	if FusingStateChanged(stored, same) {
		t.Fatal("identical splines changed fusing state")
	}
	changed := fusingSplineCard(false)
	if !FusingStateChanged(stored, changed) {
		t.Fatal("spline corner change was absent from fusing state")
	}
	changed = fusingSplineCard(true)
	changed.Pieces[0].FusingZone.Spline.Points[1].X = decimal.NewFromInt(11)
	if !FusingStateChanged(stored, changed) {
		t.Fatal("spline coordinate change was absent from fusing state")
	}
}
