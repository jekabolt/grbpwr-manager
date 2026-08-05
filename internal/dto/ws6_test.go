package dto

import (
	"strings"
	"testing"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	pb_common "github.com/jekabolt/grbpwr-manager/proto/gen/common"
)

// TestConvertPbSampleInsert_RoundSpine covers the Q7 round-spine fields and the A2 pattern length
// guards (pattern_url/pattern_note previously had no validation at all).
func TestConvertPbSampleInsert_RoundSpine(t *testing.T) {
	ok, err := ConvertPbSampleInsertToEntity(&pb_common.SampleInsert{
		TechCardId:       7,
		RoundNumber:      2,
		SpecReleaseId:    5,
		PreviousSampleId: 3,
		PatternUrl:       "https://x/p.pdf",
		PatternNote:      "v2",
	})
	if err != nil {
		t.Fatalf("valid round-spine sample: %v", err)
	}
	if !ok.RoundNumber.Valid || ok.RoundNumber.Int32 != 2 ||
		!ok.SpecReleaseId.Valid || ok.SpecReleaseId.Int32 != 5 ||
		!ok.PreviousSampleId.Valid || ok.PreviousSampleId.Int32 != 3 {
		t.Errorf("round-spine fields not carried: %+v", ok)
	}

	// 0 => unset (NULL), not a real id.
	unset, err := ConvertPbSampleInsertToEntity(&pb_common.SampleInsert{TechCardId: 7})
	if err != nil {
		t.Fatalf("unset round-spine: %v", err)
	}
	if unset.RoundNumber.Valid || unset.SpecReleaseId.Valid || unset.PreviousSampleId.Valid {
		t.Errorf("zero round-spine ids must be NULL: %+v", unset)
	}

	bad := map[string]*pb_common.SampleInsert{
		"pattern_url too long":  {TechCardId: 7, PatternUrl: strings.Repeat("a", 513)},
		"pattern_note too long": {TechCardId: 7, PatternNote: strings.Repeat("a", 256)},
		"negative round":        {TechCardId: 7, RoundNumber: -1},
		"negative spec release": {TechCardId: 7, SpecReleaseId: -1},
	}
	for name, in := range bad {
		if _, err := ConvertPbSampleInsertToEntity(in); err == nil {
			t.Errorf("case %q: expected error, got nil", name)
		}
	}
}

// TestConvertPbSampleSubstitutionInsert covers the §2.7 substitution write payload.
func TestConvertPbSampleSubstitutionInsert(t *testing.T) {
	ok, err := ConvertPbSampleSubstitutionInsertToEntity(&pb_common.SampleSubstitutionInsert{
		SampleId:              10,
		BomItemId:             4,
		OriginalMaterialId:    1,
		SubstitutedMaterialId: 2,
		Reason:                "out of stock",
	})
	if err != nil {
		t.Fatalf("valid substitution: %v", err)
	}
	if ok.SampleId != 10 || !ok.BomItemId.Valid || ok.BomItemId.Int32 != 4 ||
		!ok.SubstitutedMaterialId.Valid || ok.SubstitutedMaterialId.Int32 != 2 {
		t.Errorf("substitution fields not carried: %+v", ok)
	}

	bad := map[string]*pb_common.SampleSubstitutionInsert{
		"nil":             nil,
		"no sample":       {SampleId: 0},
		"negative bom":    {SampleId: 1, BomItemId: -1},
		"reason too long": {SampleId: 1, Reason: strings.Repeat("a", 256)},
	}
	for name, in := range bad {
		if _, err := ConvertPbSampleSubstitutionInsertToEntity(in); err == nil {
			t.Errorf("case %q: expected error, got nil", name)
		}
	}
}

// TestConvertPbFittingChangeRequestInsert covers the S26 structured-remark write payload: target
// category, zone dictionary, status default/validation, and the location fields.
func TestConvertPbFittingChangeRequestInsert(t *testing.T) {
	ok, err := ConvertPbFittingChangeRequestInsertToEntity(&pb_common.FittingChangeRequestInsert{
		FittingId:     5,
		Target:        "pattern",
		Note:          "raise the hem",
		Zone:          "outer",
		PieceIds:      []int32{9, 4},
		CarriedFromId: 3,
		// status omitted -> defaults to open
	})
	if err != nil {
		t.Fatalf("valid change request: %v", err)
	}
	if ok.FittingId != 5 || ok.Target != "pattern" || ok.Status != entity.FittingChangeStatusOpen ||
		!ok.Zone.Valid || ok.Zone.String != "outer" ||
		len(ok.PieceIds) != 2 || ok.PieceIds[0] != 9 || ok.PieceIds[1] != 4 ||
		!ok.CarriedFromId.Valid || ok.CarriedFromId.Int32 != 3 {
		t.Errorf("change-request fields not carried / status default wrong: %+v", ok)
	}

	resolved, err := ConvertPbFittingChangeRequestInsertToEntity(&pb_common.FittingChangeRequestInsert{
		FittingId: 5, Target: "material", Note: "swap lining", Status: "resolved",
	})
	if err != nil {
		t.Fatalf("resolved change request: %v", err)
	}
	if resolved.Status != entity.FittingChangeStatusResolved {
		t.Errorf("status not carried: %+v", resolved)
	}

	// The garment areas added in 0256 — the whole point of the widened dictionary.
	for _, z := range []string{"sleeve", "collar", "neckline", "armhole", "shoulder", "chest",
		"waist", "hip", "hem", "pocket", "closure", "back", "front"} {
		got, err := ConvertPbFittingChangeRequestInsertToEntity(&pb_common.FittingChangeRequestInsert{
			FittingId: 5, Target: "pattern", Note: "x", Zone: z,
		})
		if err != nil {
			t.Errorf("zone %q rejected: %v", z, err)
		} else if got.Zone.String != z {
			t.Errorf("zone %q not carried: %+v", z, got.Zone)
		}
	}

	// An older admin sent the TECH_CARD_CONSTRUCTION_ZONE_* enum NAME in this string field, which is
	// what made AddFitting 400 before 0256. It normalises to the token instead of being rejected.
	norm := map[string]string{
		"TECH_CARD_CONSTRUCTION_ZONE_OTHER":   "other",
		"TECH_CARD_CONSTRUCTION_ZONE_LINING":  "lining",
		"TECH_CARD_CONSTRUCTION_ZONE_UNKNOWN": "", // the no-op token collapses to unset
		"  Outer  ":                           "outer",
		"unknown":                             "",
	}
	for in, want := range norm {
		got, err := ConvertPbFittingChangeRequestInsertToEntity(&pb_common.FittingChangeRequestInsert{
			FittingId: 5, Target: "pattern", Note: "x", Zone: in,
		})
		if err != nil {
			t.Errorf("zone %q rejected: %v", in, err)
			continue
		}
		if got.Zone.String != want || got.Zone.Valid != (want != "") {
			t.Errorf("zone %q normalised to %q (valid=%v), want %q", in, got.Zone.String, got.Zone.Valid, want)
		}
	}

	// piece_ids: zeros are skipped, duplicates collapse, selection order survives.
	pieces, err := ConvertPbFittingChangeRequestInsertToEntity(&pb_common.FittingChangeRequestInsert{
		FittingId: 5, Target: "pattern", Note: "x", PieceIds: []int32{7, 0, 7, 2},
	})
	if err != nil {
		t.Fatalf("piece set: %v", err)
	}
	if len(pieces.PieceIds) != 2 || pieces.PieceIds[0] != 7 || pieces.PieceIds[1] != 2 {
		t.Errorf("piece_ids not deduped/ordered: %+v", pieces.PieceIds)
	}

	// The deprecated single piece_id is honoured when piece_ids is empty (older client)...
	legacy, err := ConvertPbFittingChangeRequestInsertToEntity(&pb_common.FittingChangeRequestInsert{
		FittingId: 5, Target: "pattern", Note: "x", PieceId: 8,
	})
	if err != nil {
		t.Fatalf("legacy piece_id: %v", err)
	}
	if len(legacy.PieceIds) != 1 || legacy.PieceIds[0] != 8 {
		t.Errorf("legacy piece_id not promoted: %+v", legacy.PieceIds)
	}
	// ...and ignored when piece_ids is set, so a client sending both does not get a phantom piece.
	both, err := ConvertPbFittingChangeRequestInsertToEntity(&pb_common.FittingChangeRequestInsert{
		FittingId: 5, Target: "pattern", Note: "x", PieceId: 8, PieceIds: []int32{1, 2},
	})
	if err != nil {
		t.Fatalf("piece_id + piece_ids: %v", err)
	}
	if len(both.PieceIds) != 2 || both.PieceIds[0] != 1 || both.PieceIds[1] != 2 {
		t.Errorf("deprecated piece_id leaked into the set: %+v", both.PieceIds)
	}

	bad := map[string]*pb_common.FittingChangeRequestInsert{
		"nil":         nil,
		"bad target":  {FittingId: 5, Target: "sleeve", Note: "x"},
		"empty note":  {FittingId: 5, Target: "pattern", Note: "  "},
		"bad zone":    {FittingId: 5, Target: "pattern", Note: "x", Zone: "sleeve_left"},
		"bad status":  {FittingId: 5, Target: "pattern", Note: "x", Status: "carried"},
		"neg piece":   {FittingId: 5, Target: "pattern", Note: "x", PieceId: -1},
		"neg pieces":  {FittingId: 5, Target: "pattern", Note: "x", PieceIds: []int32{1, -2}},
		"neg carried": {FittingId: 5, Target: "pattern", Note: "x", CarriedFromId: -1},
	}
	for name, in := range bad {
		if _, err := ConvertPbFittingChangeRequestInsertToEntity(in); err == nil {
			t.Errorf("case %q: expected error, got nil", name)
		}
	}
}
