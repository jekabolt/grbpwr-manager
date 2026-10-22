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

func TestDerivedCostInputsDigestKeepsSingleInterliningCardByteIdentical(t *testing.T) {
	areas := map[string]PieceAreaScope{"main": {ScopeKey: "main"}}
	base := []PieceFabricAssignment{{
		ColorwayKey: 1, PieceKey: "FRONT", BomKey: "SHELL", PiecesPerGarment: 1,
	}}
	fusing := []PieceFabricAssignment{{
		PieceKey: "FRONT", FusingBomKey: "FUSE-A", PiecesPerGarment: 1,
	}}
	want := DerivedCostInputsDigest(areas, base)
	if got := DerivedCostInputsDigestWithFusing(areas, base, fusing, 1); got != want {
		t.Fatalf("one implicit interlining line moved the digest: got %q want %q", got, want)
	}
	if got := DerivedCostInputsDigestWithFusing(areas, base, fusing, 2); got == want {
		t.Fatal("a per-piece selection between multiple interlining lines did not move the digest")
	}
}
