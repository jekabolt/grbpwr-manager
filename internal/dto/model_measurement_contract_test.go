package dto

import (
	"strings"
	"testing"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	pb_common "github.com/jekabolt/grbpwr-manager/proto/gen/common"
)

func TestP0BodyMeasurementContractRoundTrip(t *testing.T) {
	names := []pb_common.BodyMeasurementName{
		pb_common.BodyMeasurementName_BODY_MEASUREMENT_NAME_HPS_HEIGHT,
		pb_common.BodyMeasurementName_BODY_MEASUREMENT_NAME_CF_NECK_TO_WAIST,
		pb_common.BodyMeasurementName_BODY_MEASUREMENT_NAME_WAIST_HEIGHT,
		pb_common.BodyMeasurementName_BODY_MEASUREMENT_NAME_WAIST_TO_HIGH_HIP,
		pb_common.BodyMeasurementName_BODY_MEASUREMENT_NAME_WAIST_TO_HIP,
		pb_common.BodyMeasurementName_BODY_MEASUREMENT_NAME_CHEST_DEPTH,
		pb_common.BodyMeasurementName_BODY_MEASUREMENT_NAME_WAIST_DEPTH,
		pb_common.BodyMeasurementName_BODY_MEASUREMENT_NAME_HIP_DEPTH,
		pb_common.BodyMeasurementName_BODY_MEASUREMENT_NAME_SHOULDER_LENGTH,
		pb_common.BodyMeasurementName_BODY_MEASUREMENT_NAME_SHOULDER_DROP,
		pb_common.BodyMeasurementName_BODY_MEASUREMENT_NAME_ARMSCYE_DEPTH,
		pb_common.BodyMeasurementName_BODY_MEASUREMENT_NAME_ARMSCYE_CIRCUMFERENCE,
		pb_common.BodyMeasurementName_BODY_MEASUREMENT_NAME_BUST_APEX_TO_APEX,
		pb_common.BodyMeasurementName_BODY_MEASUREMENT_NAME_HPS_TO_BUST_APEX,
	}
	measurements := make([]*pb_common.ModelMeasurement, 0, len(names))
	for i, name := range names {
		measurements = append(measurements, &pb_common.ModelMeasurement{
			Name:    name,
			ValueMm: int32(100 + i),
			Metadata: &pb_common.BodyMeasurementMetadataV1{
				SchemaVersion: entity.BodyMeasurementMetadataSchemaVersionV1,
				Source:        pb_common.BodyMeasurementSource_BODY_MEASUREMENT_SOURCE_IMPORTED,
				ProtocolId:    "open-reference/p0-v1",
			},
		})
	}

	converted, err := ConvertPbModelInsertToEntity(&pb_common.ModelInsert{
		Name:                         "P0",
		Measurements:                 measurements,
		BodyMeasurementSchemaVersion: entity.BodyMeasurementSchemaVersionP0,
	})
	if err != nil {
		t.Fatalf("to entity: %v", err)
	}
	if len(converted.Measurements) != len(names) {
		t.Fatalf("entity measurements = %d, want %d", len(converted.Measurements), len(names))
	}

	roundTrip := ConvertEntityModelToPb(&entity.Model{Id: 1, ModelInsert: *converted})
	if roundTrip.Model.BodyMeasurementSchemaVersion != entity.BodyMeasurementSchemaVersionP0 {
		t.Fatalf("emitted schema version = %d", roundTrip.Model.BodyMeasurementSchemaVersion)
	}
	for i, measurement := range roundTrip.Model.Measurements {
		if measurement.Name != names[i] || measurement.ValueMm != int32(100+i) {
			t.Errorf("measurement %d round-trip = %+v", i, measurement)
		}
		if measurement.Metadata == nil ||
			measurement.Metadata.SchemaVersion != entity.BodyMeasurementMetadataSchemaVersionV1 ||
			measurement.Metadata.Source != pb_common.BodyMeasurementSource_BODY_MEASUREMENT_SOURCE_IMPORTED ||
			measurement.Metadata.ProtocolId != "open-reference/p0-v1" {
			t.Errorf("measurement %d metadata round-trip = %+v", i, measurement.Metadata)
		}
	}
}

func TestBodyMeasurementMetadataValidationAndLegacyWriterVersion(t *testing.T) {
	valid := func() *pb_common.ModelInsert {
		return &pb_common.ModelInsert{
			Name: "measurement",
			Measurements: []*pb_common.ModelMeasurement{{
				Name:    pb_common.BodyMeasurementName_BODY_MEASUREMENT_NAME_HPS_HEIGHT,
				ValueMm: 1600,
				Metadata: &pb_common.BodyMeasurementMetadataV1{
					SchemaVersion: entity.BodyMeasurementMetadataSchemaVersionV1,
					Source:        pb_common.BodyMeasurementSource_BODY_MEASUREMENT_SOURCE_MANUAL_TAPE,
					ProtocolId:    "internal/tape-v1",
				},
			}},
		}
	}

	legacy := valid()
	legacy.BodyMeasurementSchemaVersion = 0
	if _, err := ConvertPbModelInsertToEntity(legacy); err != nil {
		t.Fatalf("legacy writer version 0 must remain accepted: %v", err)
	}

	tests := map[string]func(*pb_common.ModelInsert){
		"null measurement":      func(in *pb_common.ModelInsert) { in.Measurements[0] = nil },
		"future writer version": func(in *pb_common.ModelInsert) { in.BodyMeasurementSchemaVersion = 2 },
		"metadata version zero": func(in *pb_common.ModelInsert) { in.Measurements[0].Metadata.SchemaVersion = 0 },
		"unknown source": func(in *pb_common.ModelInsert) {
			in.Measurements[0].Metadata.Source = pb_common.BodyMeasurementSource_BODY_MEASUREMENT_SOURCE_UNKNOWN
		},
		"blank protocol":     func(in *pb_common.ModelInsert) { in.Measurements[0].Metadata.ProtocolId = "" },
		"untrimmed protocol": func(in *pb_common.ModelInsert) { in.Measurements[0].Metadata.ProtocolId = " p0 " },
		"long protocol": func(in *pb_common.ModelInsert) {
			in.Measurements[0].Metadata.ProtocolId = strings.Repeat("x", maxBodyMeasurementProtocolIDChars+1)
		},
		"control protocol": func(in *pb_common.ModelInsert) { in.Measurements[0].Metadata.ProtocolId = "p0\nscan" },
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			in := valid()
			mutate(in)
			if _, err := ConvertPbModelInsertToEntity(in); err == nil {
				t.Fatal("expected validation error")
			}
		})
	}
}
