package entity

import (
	"testing"

	"github.com/shopspring/decimal"
)

func carryTestRing(x0, x1 int64) TechCardPieceFusingSpline {
	return TechCardPieceFusingSpline{Points: []TechCardPieceFusingSplinePoint{
		{X: decimal.NewFromInt(x0), Y: decimal.NewFromInt(0), Corner: true},
		{X: decimal.NewFromInt(x1), Y: decimal.NewFromInt(0), Corner: true},
		{X: decimal.NewFromInt(x1), Y: decimal.NewFromInt(10), Corner: true},
	}}
}

// An older client echoes geometry it understands and drops the modes it has never heard of. Its
// save must not reset them; a client that states a mode, or a ring the old client reshaped, is taken
// at its word.
func TestCarryOmittedFusingModesFrom(t *testing.T) {
	storedSpline := carryTestRing(0, 20)
	storedSpline.GradeMode = PieceFusingGradeModeFollowLine
	storedSpline.Anchor = &TechCardPieceFusingAnchor{X: decimal.NewFromInt(4), Y: decimal.NewFromInt(4)}
	storedIsland := carryTestRing(30, 40)
	storedIsland.GradeMode = PieceFusingGradeModeGrow
	storedReshaped := carryTestRing(50, 60)
	storedReshaped.GradeMode = PieceFusingGradeModeGrow
	stored := &TechCardPiece{
		FusingZone: &TechCardPieceFusingZone{Spline: &storedSpline, Islands: []TechCardPieceFusingSpline{storedIsland, storedReshaped}},
		FusingEdges: []TechCardPieceFusingEdge{
			{Edge: 2, StartMode: PieceFusingEndModeNotch, EndMode: PieceFusingEndModeShare},
		},
	}

	oldSpline := carryTestRing(0, 20)
	oldSpline.GradeModeOmitted = true
	statedIsland := carryTestRing(30, 40) // a new client resets this ring to fixed
	reshaped := carryTestRing(50, 65)
	reshaped.GradeModeOmitted = true
	incoming := &TechCardPiece{
		FusingZone: &TechCardPieceFusingZone{Spline: &oldSpline, Islands: []TechCardPieceFusingSpline{statedIsland, reshaped}},
		FusingEdges: []TechCardPieceFusingEdge{
			{Edge: 2, StartModeOmitted: true},                       // old end kept, stated end reset to mm
			{Edge: 5, StartModeOmitted: true, EndModeOmitted: true}, // a new edge has nothing to keep
		},
	}
	incoming.CarryOmittedFusingModesFrom(stored)

	got := incoming.FusingZone.Spline
	if got.GradeMode != PieceFusingGradeModeFollowLine || got.Anchor == nil || !got.Anchor.X.Equal(decimal.NewFromInt(4)) {
		t.Fatalf("untouched ring lost its stored mode: %#v", got)
	}
	if got.Anchor == storedSpline.Anchor {
		t.Fatal("carried anchor aliases the stored one")
	}
	if mode := incoming.FusingZone.Islands[0].GradeMode; mode != PieceFusingGradeModeFixed {
		t.Fatalf("explicit fixed overwritten with %q", mode)
	}
	if mode := incoming.FusingZone.Islands[1].GradeMode; mode != PieceFusingGradeModeFixed {
		t.Fatalf("reshaped ring inherited %q, want fixed", mode)
	}
	if e := incoming.FusingEdges[0]; e.StartMode != PieceFusingEndModeNotch || e.EndMode != PieceFusingEndModeMm {
		t.Fatalf("edge modes = %q / %q, want notch / mm", e.StartMode, e.EndMode)
	}
	if e := incoming.FusingEdges[1]; e.StartMode != PieceFusingEndModeMm || e.EndMode != PieceFusingEndModeMm {
		t.Fatalf("new edge modes = %q / %q, want mm", e.StartMode, e.EndMode)
	}
	if storedSpline.GradeModeOmitted || oldSpline.GradeMode != PieceFusingGradeModeFixed {
		t.Fatal("carry wrote through to a struct it did not own")
	}
}
