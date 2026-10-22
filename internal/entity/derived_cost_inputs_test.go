package entity

import "testing"

func TestDerivedCostInputsDigestIncludesPieceFusingLine(t *testing.T) {
	base := []PieceFabricAssignment{{
		ColorwayKey: 1, PieceKey: "FRONT", BomKey: "SHELL", PiecesPerGarment: 1,
	}}
	a := append([]PieceFabricAssignment(nil), base...)
	a[0].FusingBomKey = "FUSE-A"
	b := append([]PieceFabricAssignment(nil), base...)
	b[0].FusingBomKey = "FUSE-B"

	digestA := DerivedCostInputsDigest(nil, a)
	digestB := DerivedCostInputsDigest(nil, b)
	if digestA == "" || digestB == "" || digestA == digestB {
		t.Fatalf("fusing line did not move derived-cost digest: A=%q B=%q", digestA, digestB)
	}
	// Line-key spelling is case-insensitive everywhere else; the digest must not create a false
	// approval change when a legacy row differs only in case.
	a[0].FusingBomKey = "fuse-a"
	if got := DerivedCostInputsDigest(nil, a); got != digestA {
		t.Fatalf("case-only fusing line change moved digest: got %q want %q", got, digestA)
	}
}
