package dto

import (
	"testing"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	pb_common "github.com/jekabolt/grbpwr-manager/proto/gen/common"
)

func TestConvertPbModelInsertToEntity(t *testing.T) {
	valid := &pb_common.ModelInsert{
		Name:                "Anna",
		Comment:             "lookbook",
		Gender:              pb_common.GenderEnum_GENDER_ENUM_FEMALE,
		DefaultSampleSizeId: 4,
		Measurements: []*pb_common.ModelMeasurement{
			{Name: pb_common.BodyMeasurementName_BODY_MEASUREMENT_NAME_CHEST, ValueMm: 880},
			{Name: pb_common.BodyMeasurementName_BODY_MEASUREMENT_NAME_WAIST, ValueMm: 640},
		},
	}

	got, err := ConvertPbModelInsertToEntity(valid)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.Name != "Anna" || !got.Comment.Valid || got.Comment.String != "lookbook" {
		t.Errorf("name/comment mismatch: %+v", got)
	}
	if !got.Gender.Valid || got.Gender.String != string(entity.Female) {
		t.Errorf("gender mismatch: %+v", got.Gender)
	}
	if !got.DefaultSampleSizeId.Valid || got.DefaultSampleSizeId.Int32 != 4 {
		t.Errorf("default size mismatch: %+v", got.DefaultSampleSizeId)
	}
	if len(got.Measurements) != 2 || got.Measurements[0].Name != entity.BodyChest || got.Measurements[0].ValueMM != 880 {
		t.Errorf("measurements mismatch: %+v", got.Measurements)
	}

	// invalid cases
	bad := map[string]*pb_common.ModelInsert{
		"nil":           nil,
		"empty name":    {Name: ""},
		"unknown name":  {Name: "x", Measurements: []*pb_common.ModelMeasurement{{Name: pb_common.BodyMeasurementName(999), ValueMm: 100}}},
		"negative size": {Name: "x", DefaultSampleSizeId: -1},
		"value zero":    {Name: "x", Measurements: []*pb_common.ModelMeasurement{{Name: pb_common.BodyMeasurementName_BODY_MEASUREMENT_NAME_CHEST, ValueMm: 0}}},
		"value too big": {Name: "x", Measurements: []*pb_common.ModelMeasurement{{Name: pb_common.BodyMeasurementName_BODY_MEASUREMENT_NAME_CHEST, ValueMm: 99999}}},
		"duplicate name": {Name: "x", Measurements: []*pb_common.ModelMeasurement{
			{Name: pb_common.BodyMeasurementName_BODY_MEASUREMENT_NAME_CHEST, ValueMm: 100},
			{Name: pb_common.BodyMeasurementName_BODY_MEASUREMENT_NAME_CHEST, ValueMm: 200},
		}},
	}
	for name, in := range bad {
		if _, err := ConvertPbModelInsertToEntity(in); err == nil {
			t.Errorf("case %q: expected error, got nil", name)
		}
	}
}

func TestConvertEntityModelToPbRoundTrip(t *testing.T) {
	in := &pb_common.ModelInsert{
		Name:                "Max",
		Gender:              pb_common.GenderEnum_GENDER_ENUM_MALE,
		DefaultSampleSizeId: 0, // unset
		Measurements: []*pb_common.ModelMeasurement{
			{Name: pb_common.BodyMeasurementName_BODY_MEASUREMENT_NAME_INSEAM, ValueMm: 820},
		},
	}
	ent, err := ConvertPbModelInsertToEntity(in)
	if err != nil {
		t.Fatalf("to entity: %v", err)
	}
	pb := ConvertEntityModelToPb(&entity.Model{Id: 7, ModelInsert: *ent})
	if pb.Id != 7 || pb.Model.Name != "Max" {
		t.Errorf("round-trip id/name mismatch: %+v", pb)
	}
	if pb.Model.Gender != pb_common.GenderEnum_GENDER_ENUM_MALE {
		t.Errorf("round-trip gender mismatch: %v", pb.Model.Gender)
	}
	if pb.Model.DefaultSampleSizeId != 0 {
		t.Errorf("unset size should be 0, got %d", pb.Model.DefaultSampleSizeId)
	}
	if len(pb.Model.Measurements) != 1 ||
		pb.Model.Measurements[0].Name != pb_common.BodyMeasurementName_BODY_MEASUREMENT_NAME_INSEAM ||
		pb.Model.Measurements[0].ValueMm != 820 {
		t.Errorf("round-trip measurement mismatch: %+v", pb.Model.Measurements)
	}
}
