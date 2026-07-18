package dto

import (
	"database/sql"
	"testing"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	pb_common "github.com/jekabolt/grbpwr-manager/proto/gen/common"
)

// TestMaterialPurposeEnumNoDrift asserts every non-UNKNOWN proto MaterialPurpose value maps (via
// materialPurposePbToEntity) to a valid entity purpose and the sets match in size (#40). Mirrors
// TestMaterialClassEnumNoDrift; the entity<->DB leg is TestMaterialPurposeDBCheckNoDrift.
func TestMaterialPurposeEnumNoDrift(t *testing.T) {
	protoValues := 0
	for v, name := range pb_common.MaterialPurpose_name {
		if pb_common.MaterialPurpose(v) == pb_common.MaterialPurpose_MATERIAL_PURPOSE_UNKNOWN {
			continue
		}
		protoValues++
		p, ok := materialPurposePbToEntity[pb_common.MaterialPurpose(v)]
		if !ok {
			t.Errorf("proto MaterialPurpose %s has no entity mapping", name)
			continue
		}
		if !entity.ValidMaterialPurposes[p] {
			t.Errorf("proto MaterialPurpose %s maps to invalid entity purpose %q", name, p)
		}
	}
	if protoValues != len(materialPurposePbToEntity) {
		t.Errorf("proto material purpose values (%d) != entity mapping size (%d)", protoValues, len(materialPurposePbToEntity))
	}
	if protoValues != len(entity.ValidMaterialPurposes) {
		t.Errorf("proto material purpose values (%d) != entity.ValidMaterialPurposes (%d)", protoValues, len(entity.ValidMaterialPurposes))
	}
}

// TestConvertMaterialImageAndPurpose covers the #39/#40 DTO legs: image_id + purpose round-trip, an
// UNKNOWN purpose maps to the empty entity value (the store defaults it to 'both'), a resolved image
// is emitted as MediaFull, and a negative image_id is rejected.
func TestConvertMaterialImageAndPurpose(t *testing.T) {
	// Produce a valid section enum via an entity->pb round-trip (avoids hard-coding the constant).
	base := ConvertEntityMaterialToPb(entity.MaterialWithPrice{Material: entity.Material{
		MaterialInsert: entity.MaterialInsert{Name: "img", Section: "fabric"},
	}})

	base.Purpose = pb_common.MaterialPurpose_MATERIAL_PURPOSE_SAMPLE
	base.ImageId = 7
	ins, err := ConvertPbMaterialToEntityInsert(base)
	if err != nil {
		t.Fatalf("convert: %v", err)
	}
	if ins.Purpose != string(entity.MaterialPurposeSample) {
		t.Errorf("purpose = %q, want sample", ins.Purpose)
	}
	if !ins.ImageId.Valid || ins.ImageId.Int32 != 7 {
		t.Errorf("image_id = %+v, want {7 true}", ins.ImageId)
	}

	// UNKNOWN purpose -> empty entity value (the store normalises it to 'both').
	base.Purpose = pb_common.MaterialPurpose_MATERIAL_PURPOSE_UNKNOWN
	ins, err = ConvertPbMaterialToEntityInsert(base)
	if err != nil {
		t.Fatalf("convert unknown: %v", err)
	}
	if ins.Purpose != "" {
		t.Errorf("unknown purpose = %q, want empty", ins.Purpose)
	}

	// A negative image_id is rejected.
	base.ImageId = -1
	if _, err := ConvertPbMaterialToEntityInsert(base); err == nil {
		t.Error("negative image_id should be rejected")
	}

	// Read side: a resolved image + purpose are emitted.
	out := ConvertEntityMaterialToPb(entity.MaterialWithPrice{Material: entity.Material{
		Id: 1,
		MaterialInsert: entity.MaterialInsert{
			Name: "img", Section: "fabric",
			ImageId: sql.NullInt32{Int32: 42, Valid: true},
			Purpose: string(entity.MaterialPurposeProduction),
		},
		Image: &entity.MediaFull{Id: 42},
	}})
	if out.Purpose != pb_common.MaterialPurpose_MATERIAL_PURPOSE_PRODUCTION {
		t.Errorf("out.Purpose = %v, want PRODUCTION", out.Purpose)
	}
	if out.ImageId != 42 {
		t.Errorf("out.ImageId = %d, want 42", out.ImageId)
	}
	if out.Image == nil || out.Image.Id != 42 {
		t.Errorf("out.Image = %+v, want id 42", out.Image)
	}
}
