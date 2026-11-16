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
	if roundTrip.Model.BodyMeasurementSchemaVersion != entity.BodyMeasurementSchemaVersionCurrent {
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
		"future writer version": func(in *pb_common.ModelInsert) { in.BodyMeasurementSchemaVersion = 3 },
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

func TestP1NeckBodyMeasurementContract(t *testing.T) {
	neck := pb_common.BodyMeasurementName_BODY_MEASUREMENT_NAME_NECK
	if neck == pb_common.BodyMeasurementName_BODY_MEASUREMENT_NAME_NECK_BASE {
		t.Fatal("NECK must be a separate enum value from NECK_BASE")
	}
	if int32(neck) != 35 {
		t.Fatalf("NECK wire number = %d, want 35 (append-only)", int32(neck))
	}
	if got := bodyMeasurementPbToEntity[neck]; got != entity.BodyNeck {
		t.Fatalf("NECK maps to %q, want %q", got, entity.BodyNeck)
	}
	if got := bodyMeasurementPbToEntity[pb_common.BodyMeasurementName_BODY_MEASUREMENT_NAME_NECK_BASE]; got != entity.BodyNeckBase {
		t.Fatalf("NECK_BASE maps to %q, want %q", got, entity.BodyNeckBase)
	}

	// Every named enum value has exactly one storage key and vice versa.
	for value, name := range pb_common.BodyMeasurementName_name {
		if value == 0 {
			continue
		}
		key, ok := bodyMeasurementPbToEntity[pb_common.BodyMeasurementName(value)]
		if !ok {
			t.Errorf("enum %s has no storage key", name)
			continue
		}
		if !entity.ValidBodyMeasurementNames[key] {
			t.Errorf("enum %s maps to %q, which is not an accepted key", name, key)
		}
	}
	if len(bodyMeasurementPbToEntity) != len(entity.ValidBodyMeasurementNames) ||
		len(bodyMeasurementEntityToPb) != len(bodyMeasurementPbToEntity) {
		t.Fatalf("pb map %d, reverse map %d, accepted keys %d must be equal",
			len(bodyMeasurementPbToEntity), len(bodyMeasurementEntityToPb), len(entity.ValidBodyMeasurementNames))
	}

	in := func(version uint32) *pb_common.ModelInsert {
		return &pb_common.ModelInsert{
			Name: "neck",
			Measurements: []*pb_common.ModelMeasurement{
				{Name: pb_common.BodyMeasurementName_BODY_MEASUREMENT_NAME_NECK_BASE, ValueMm: 400},
				{
					Name:    neck,
					ValueMm: 360,
					Metadata: &pb_common.BodyMeasurementMetadataV1{
						SchemaVersion: entity.BodyMeasurementMetadataSchemaVersionV1,
						Source:        pb_common.BodyMeasurementSource_BODY_MEASUREMENT_SOURCE_MANUAL_TAPE,
						ProtocolId:    "internal/tape-v1",
					},
				},
			},
			BodyMeasurementSchemaVersion: version,
		}
	}

	for _, version := range []uint32{0, entity.BodyMeasurementSchemaVersionP0, entity.BodyMeasurementSchemaVersionP1} {
		// Older writers may echo a stored neck back from a read; that must not fail the save.
		converted, err := ConvertPbModelInsertToEntity(in(version))
		if err != nil {
			t.Fatalf("writer version %d with NECK: %v", version, err)
		}
		if converted.BodyMeasurementSchemaVersion != version {
			t.Fatalf("writer version = %d, want %d", converted.BodyMeasurementSchemaVersion, version)
		}
		if len(converted.Measurements) != 2 ||
			converted.Measurements[0].Name != entity.BodyNeckBase || converted.Measurements[0].ValueMM != 400 ||
			converted.Measurements[1].Name != entity.BodyNeck || converted.Measurements[1].ValueMM != 360 {
			t.Fatalf("converted measurements = %+v", converted.Measurements)
		}
	}

	converted, err := ConvertPbModelInsertToEntity(in(entity.BodyMeasurementSchemaVersionP1))
	if err != nil {
		t.Fatalf("to entity: %v", err)
	}
	roundTrip := ConvertEntityModelToPb(&entity.Model{Id: 1, ModelInsert: *converted})
	if roundTrip.Model.BodyMeasurementSchemaVersion != entity.BodyMeasurementSchemaVersionP1 {
		t.Fatalf("emitted schema version = %d, want %d", roundTrip.Model.BodyMeasurementSchemaVersion, entity.BodyMeasurementSchemaVersionP1)
	}
	if len(roundTrip.Model.Measurements) != 2 ||
		roundTrip.Model.Measurements[0].Name != pb_common.BodyMeasurementName_BODY_MEASUREMENT_NAME_NECK_BASE ||
		roundTrip.Model.Measurements[0].ValueMm != 400 ||
		roundTrip.Model.Measurements[1].Name != neck ||
		roundTrip.Model.Measurements[1].ValueMm != 360 ||
		roundTrip.Model.Measurements[1].Metadata == nil ||
		roundTrip.Model.Measurements[1].Metadata.ProtocolId != "internal/tape-v1" {
		t.Fatalf("neck round-trip = %+v", roundTrip.Model.Measurements)
	}
}
