package entity

import (
	"errors"
	"math"
	"testing"
)

func seamChangeAnchor(piece, hint string, from, to float64) TechCardSeamAnchor {
	return TechCardSeamAnchor{
		PieceLineKey: piece,
		Samples:      []TechCardSeamSample{{U: 0, V: 0}, {U: 0.25, V: 0.1}, {U: 0.5, V: 0.2}, {U: 0.75, V: 0.3}, {U: 1, V: 0.4}},
		PerimShare:   0.1, LenMm: 100, EdgeHint: hint, RangeFrom: from, RangeTo: to,
	}
}

func seamChangeRow(key string, a, b TechCardSeamAnchor) TechCardSeamInput {
	return TechCardSeamInput{
		SeamKey: key, Status: TechCardSeamConfirmed, Kind: TechCardSeamKindEdge,
		Direction: TechCardSeamReversed, Source: TechCardSeamSourceManual,
		SideA: []TechCardSeamAnchor{a}, SideB: []TechCardSeamAnchor{b},
	}
}

func TestTechCardSeamPairRangeSig(t *testing.T) {
	base := seamChangeRow("K", seamChangeAnchor("P1", "A#1", 0, 0), seamChangeAnchor("P2", "B#2", 0, 0))
	sig := TechCardSeamPairRangeSig(base)
	if sig != "edge|A#1@0-1~B#2@0-1" {
		t.Fatalf("sig = %q", sig)
	}
	// Sides swapped and the whole run sent as an explicit 0/1: the same pair, as the client reads it.
	swapped := seamChangeRow("N", seamChangeAnchor("P2", "B#2", 0, 1), seamChangeAnchor("P1", "A#1", math.Copysign(0, -1), 1))
	if got := TechCardSeamPairRangeSig(swapped); got != sig {
		t.Fatalf("swapped sides / explicit whole range: %q != %q", got, sig)
	}
	// A real sub-range is part of the pair: two windows of one edge are two rows.
	part := seamChangeRow("N", seamChangeAnchor("P1", "A#1", 0, 0.5), seamChangeAnchor("P2", "B#2", 0, 0))
	if got := TechCardSeamPairRangeSig(part); got == sig || got != "edge|A#1@0-0.5~B#2@0-1" {
		t.Fatalf("sub-range sig = %q", got)
	}
	// The kind is part of the pair.
	surface := base
	surface.Kind = TechCardSeamKindSurface
	if TechCardSeamPairRangeSig(surface) == sig {
		t.Fatal("kind must be part of the signature")
	}
	// Anchor order inside a side does not matter.
	multi := base
	multi.SideA = []TechCardSeamAnchor{seamChangeAnchor("P1", "A#3", 0, 0), seamChangeAnchor("P1", "A#1", 0, 0)}
	multi2 := multi
	multi2.SideA = []TechCardSeamAnchor{multi.SideA[1], multi.SideA[0]}
	if TechCardSeamPairRangeSig(multi) != TechCardSeamPairRangeSig(multi2) {
		t.Fatal("anchor order inside a side must not matter")
	}
}

func TestValidateTechCardSeamsChange(t *testing.T) {
	const k1, k2 = "01SEAMCHANGE00000000000001", "01seamchange00000000000002"
	v := 3
	row := func(key string) TechCardSeamInput {
		return seamChangeRow(key, seamChangeAnchor("P1", "A#1", 0, 0), seamChangeAnchor("P2", "B#2", 0, 0))
	}
	field := func(t *testing.T, err error) string {
		t.Helper()
		var ve *ValidationError
		if !errors.As(err, &ve) {
			t.Fatalf("want a field violation, got %v", err)
		}
		return ve.Field + " " + ve.Reason
	}

	t.Run("missing expected_lock_version", func(t *testing.T) {
		in := TechCardSeamsChange{TechCardId: 1, Delete: []string{k1}}
		if got := field(t, ValidateTechCardSeamsChange(&in)); got != "expected_lock_version required" {
			t.Fatal(got)
		}
	})
	t.Run("empty change", func(t *testing.T) {
		in := TechCardSeamsChange{TechCardId: 1, ExpectedLockVersion: &v}
		if got := field(t, ValidateTechCardSeamsChange(&in)); got != "upsert empty" {
			t.Fatal(got)
		}
	})
	t.Run("upsert errors name upsert[i]", func(t *testing.T) {
		in := TechCardSeamsChange{TechCardId: 1, ExpectedLockVersion: &v, Upsert: []TechCardSeamInput{row("bad")}}
		if got := field(t, ValidateTechCardSeamsChange(&in)); got != "upsert[0].seam_key invalid" {
			t.Fatal(got)
		}
	})
	t.Run("same pair in link and detach", func(t *testing.T) {
		in := TechCardSeamsChange{TechCardId: 1, ExpectedLockVersion: &v,
			Link:   []TechCardSeamLink{{OperationNumber: 10, SeamKey: k1}},
			Detach: []TechCardSeamLink{{OperationNumber: 20, SeamKey: k1}, {OperationNumber: 10, SeamKey: " " + k1 + " "}}}
		if got := field(t, ValidateTechCardSeamsChange(&in)); got != "detach[1] contradicts_link" {
			t.Fatal(got)
		}
	})
	t.Run("linked key deleted", func(t *testing.T) {
		in := TechCardSeamsChange{TechCardId: 1, ExpectedLockVersion: &v,
			Link: []TechCardSeamLink{{OperationNumber: 10, SeamKey: k2}}, Delete: []string{k2}}
		if got := field(t, ValidateTechCardSeamsChange(&in)); got != "delete[0] contradicts_link" {
			t.Fatal(got)
		}
	})
	t.Run("upserted key deleted", func(t *testing.T) {
		in := TechCardSeamsChange{TechCardId: 1, ExpectedLockVersion: &v,
			Upsert: []TechCardSeamInput{row(k1)}, Delete: []string{k1}}
		if got := field(t, ValidateTechCardSeamsChange(&in)); got != "delete[0] contradicts_upsert" {
			t.Fatal(got)
		}
	})
	t.Run("step number required", func(t *testing.T) {
		in := TechCardSeamsChange{TechCardId: 1, ExpectedLockVersion: &v,
			Link: []TechCardSeamLink{{OperationNumber: 0, SeamKey: k1}}}
		if got := field(t, ValidateTechCardSeamsChange(&in)); got != "link[0].operation_number required" {
			t.Fatal(got)
		}
	})
	t.Run("keys normalised, repeats kept", func(t *testing.T) {
		in := TechCardSeamsChange{TechCardId: 1, ExpectedLockVersion: &v,
			Link:   []TechCardSeamLink{{OperationNumber: 10, SeamKey: k2}, {OperationNumber: 10, SeamKey: k2}},
			Detach: []TechCardSeamLink{{OperationNumber: 20, SeamKey: k2}}}
		if err := ValidateTechCardSeamsChange(&in); err != nil {
			t.Fatal(err)
		}
		if len(in.Link) != 2 || in.Link[0].SeamKey != NormalizeTechCardSeamKey(k2) || in.Detach[0].SeamKey != in.Link[0].SeamKey {
			t.Fatalf("not normalised: %+v %+v", in.Link, in.Detach)
		}
	})
}
