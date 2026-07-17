package dto

import (
	"testing"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	pb_common "github.com/jekabolt/grbpwr-manager/proto/gen/common"
)

// TestGenderEnumNoDrift asserts every non-UNKNOWN proto GenderEnum value has an entity mapping to a
// valid gender, and that the entity/proto sets are the same size. It fails if a value is added to
// the proto enum (or entity set) without updating the other — the "edit in 3-4 places" drift the
// enum single-sourcing (PR5-F) is meant to catch.
func TestGenderEnumNoDrift(t *testing.T) {
	protoValues := 0
	for v, name := range pb_common.GenderEnum_name {
		if pb_common.GenderEnum(v) == pb_common.GenderEnum_GENDER_ENUM_UNKNOWN {
			continue
		}
		protoValues++
		g, ok := genderPbEntityMap[pb_common.GenderEnum(v)]
		if !ok {
			t.Errorf("proto GenderEnum %s has no entity mapping", name)
			continue
		}
		if !entity.IsValidTargetGender(g) {
			t.Errorf("proto GenderEnum %s maps to invalid entity gender %q", name, g)
		}
	}
	if protoValues != len(genderPbEntityMap) {
		t.Errorf("proto gender values (%d) != entity mapping size (%d)", protoValues, len(genderPbEntityMap))
	}
	if protoValues != len(entity.ValidProductTargetGenders) {
		t.Errorf("proto gender values (%d) != entity.ValidProductTargetGenders (%d)", protoValues, len(entity.ValidProductTargetGenders))
	}
}

// TestSeasonEnumNoDrift is the same guard for SeasonEnum.
func TestSeasonEnumNoDrift(t *testing.T) {
	protoValues := 0
	for v, name := range pb_common.SeasonEnum_name {
		if pb_common.SeasonEnum(v) == pb_common.SeasonEnum_SEASON_ENUM_UNKNOWN {
			continue
		}
		protoValues++
		s, ok := seasonPbEntityMap[pb_common.SeasonEnum(v)]
		if !ok {
			t.Errorf("proto SeasonEnum %s has no entity mapping", name)
			continue
		}
		if !entity.IsValidSeason(s) {
			t.Errorf("proto SeasonEnum %s maps to invalid entity season %q", name, s)
		}
	}
	if protoValues != len(seasonPbEntityMap) {
		t.Errorf("proto season values (%d) != entity mapping size (%d)", protoValues, len(seasonPbEntityMap))
	}
	if protoValues != len(entity.ValidSeasons) {
		t.Errorf("proto season values (%d) != entity.ValidSeasons (%d)", protoValues, len(entity.ValidSeasons))
	}
}

// TestMaterialClassEnumNoDrift is the same guard for the S15 MaterialClass enum: every non-UNKNOWN
// proto value maps (via materialClassPbToEntity) to a valid entity class and the sets match in size.
func TestMaterialClassEnumNoDrift(t *testing.T) {
	protoValues := 0
	for v, name := range pb_common.MaterialClass_name {
		if pb_common.MaterialClass(v) == pb_common.MaterialClass_MATERIAL_CLASS_UNKNOWN {
			continue
		}
		protoValues++
		c, ok := materialClassPbToEntity[pb_common.MaterialClass(v)]
		if !ok {
			t.Errorf("proto MaterialClass %s has no entity mapping", name)
			continue
		}
		if !entity.ValidMaterialClasses[c] {
			t.Errorf("proto MaterialClass %s maps to invalid entity class %q", name, c)
		}
	}
	if protoValues != len(materialClassPbToEntity) {
		t.Errorf("proto material class values (%d) != entity mapping size (%d)", protoValues, len(materialClassPbToEntity))
	}
	if protoValues != len(entity.ValidMaterialClasses) {
		t.Errorf("proto material class values (%d) != entity.ValidMaterialClasses (%d)", protoValues, len(entity.ValidMaterialClasses))
	}
}

// TestTechCardPurposeEnumNoDrift is the same guard for the R6 TechCardPurpose enum: every non-UNKNOWN
// proto value maps (via techCardPurposeFromPb) to a valid entity purpose and the sets match in size.
// Closes the entity<->proto leg the T-C purpose drift work left for T-B (handoff item 3).
func TestTechCardPurposeEnumNoDrift(t *testing.T) {
	protoValues := 0
	for v, name := range pb_common.TechCardPurpose_name {
		if pb_common.TechCardPurpose(v) == pb_common.TechCardPurpose_TECH_CARD_PURPOSE_UNKNOWN {
			continue
		}
		protoValues++
		p := techCardPurposeFromPb(pb_common.TechCardPurpose(v))
		if !entity.ValidTechCardPurposes[p] {
			t.Errorf("proto TechCardPurpose %s maps to invalid entity purpose %q", name, p)
		}
	}
	if protoValues != len(entity.ValidTechCardPurposes) {
		t.Errorf("proto purpose values (%d) != entity.ValidTechCardPurposes (%d)", protoValues, len(entity.ValidTechCardPurposes))
	}
}
