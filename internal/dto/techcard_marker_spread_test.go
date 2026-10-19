package dto

import (
	"math"
	"testing"

	pb_common "github.com/jekabolt/grbpwr-manager/proto/gen/common"
	"google.golang.org/protobuf/encoding/protojson"
)

// TestMarkerSpreadBounds pins the save-path gate on layout.spread: absent is legal, every bound is
// inclusive, and an unknown mode number is refused.
func TestMarkerSpreadBounds(t *testing.T) {
	ok := func(sp *pb_common.TechCardMarkerSpread) {
		t.Helper()
		if err := validateMarkerSpread(sp); err != nil {
			t.Fatalf("%v: unexpected refusal: %v", sp, err)
		}
	}
	bad := func(sp *pb_common.TechCardMarkerSpread) {
		t.Helper()
		if err := validateMarkerSpread(sp); err == nil {
			t.Fatalf("%v: expected a refusal", sp)
		}
	}
	ok(nil)
	ok(&pb_common.TechCardMarkerSpread{Plies: 1})
	ok(&pb_common.TechCardMarkerSpread{Plies: 1000, EndLossCm: 100, TableLengthCm: 5000,
		Mode: pb_common.TechCardMarkerSpreadMode_TECH_CARD_MARKER_SPREAD_MODE_FACE_TO_FACE})
	ok(&pb_common.TechCardMarkerSpread{Plies: 40, EndLossCm: 2.5, TableLengthCm: 50,
		Mode: pb_common.TechCardMarkerSpreadMode_TECH_CARD_MARKER_SPREAD_MODE_FACE_UP})
	bad(&pb_common.TechCardMarkerSpread{Plies: 0})
	bad(&pb_common.TechCardMarkerSpread{Plies: 1001})
	bad(&pb_common.TechCardMarkerSpread{Plies: 1, EndLossCm: -0.1})
	bad(&pb_common.TechCardMarkerSpread{Plies: 1, EndLossCm: 100.5})
	bad(&pb_common.TechCardMarkerSpread{Plies: 1, EndLossCm: math.NaN()})
	bad(&pb_common.TechCardMarkerSpread{Plies: 1, TableLengthCm: 49.9})
	bad(&pb_common.TechCardMarkerSpread{Plies: 1, TableLengthCm: 5000.1})
	bad(&pb_common.TechCardMarkerSpread{Plies: 1, TableLengthCm: -100})
	bad(&pb_common.TechCardMarkerSpread{Plies: 1, Mode: 7})
}

// TestMarkerSpreadSurvivesBlob: the spread rides in the layout blob and reads back unchanged.
func TestMarkerSpreadSurvivesBlob(t *testing.T) {
	in := &pb_common.TechCardMarkerLayout{SchemaVersion: 4, Spread: &pb_common.TechCardMarkerSpread{
		Plies: 24, EndLossCm: 1.5, TableLengthCm: 1200,
		Mode: pb_common.TechCardMarkerSpreadMode_TECH_CARD_MARKER_SPREAD_MODE_FACE_TO_FACE}}
	blob, err := protojson.Marshal(in)
	if err != nil {
		t.Fatal(err)
	}
	var out pb_common.TechCardMarkerLayout
	if err := (protojson.UnmarshalOptions{DiscardUnknown: true}).Unmarshal(blob, &out); err != nil {
		t.Fatal(err)
	}
	sp := out.GetSpread()
	if sp.GetPlies() != 24 || sp.GetEndLossCm() != 1.5 || sp.GetTableLengthCm() != 1200 ||
		sp.GetMode() != pb_common.TechCardMarkerSpreadMode_TECH_CARD_MARKER_SPREAD_MODE_FACE_TO_FACE {
		t.Fatalf("spread did not round-trip: %v", sp)
	}
}
