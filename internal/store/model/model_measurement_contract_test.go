package model

import (
	"testing"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
)

func measurementMetadata(protocol string) *entity.BodyMeasurementMetadataV1 {
	return &entity.BodyMeasurementMetadataV1{
		SchemaVersion: entity.BodyMeasurementMetadataSchemaVersionV1,
		Source:        entity.BodyMeasurementManualTape,
		ProtocolID:    protocol,
	}
}

func TestMergeModelMeasurementsPreservesP0ForLegacyWriter(t *testing.T) {
	previous := []entity.ModelMeasurement{
		{Name: entity.BodyChest, ValueMM: 900, Metadata: measurementMetadata("tape/chest-v1")},
		{Name: entity.BodyHPSHeight, ValueMM: 1600, Metadata: measurementMetadata("tape/hps-v1")},
	}
	next := []entity.ModelMeasurement{{Name: entity.BodyChest, ValueMM: 910}}

	merged := mergeModelMeasurementsForUpdate(next, previous, 0)
	if len(merged) != 2 {
		t.Fatalf("legacy merge length = %d, want 2", len(merged))
	}
	if merged[0].Metadata == nil || merged[0].Metadata.ProtocolID != "tape/chest-v1" {
		t.Errorf("same-name metadata was not preserved: %+v", merged[0])
	}
	if merged[1].Name != entity.BodyHPSHeight || merged[1].Metadata == nil {
		t.Errorf("absent P0 field was not preserved: %+v", merged[1])
	}

	merged[1].Metadata.ProtocolID = "changed"
	if previous[1].Metadata.ProtocolID != "tape/hps-v1" {
		t.Fatal("merge returned an aliased metadata pointer")
	}
}

func TestMergeModelMeasurementsLetsP0AwareWriterDeleteAbsentP0(t *testing.T) {
	previous := []entity.ModelMeasurement{
		{Name: entity.BodyChest, ValueMM: 900},
		{Name: entity.BodyHPSHeight, ValueMM: 1600, Metadata: measurementMetadata("tape/hps-v1")},
	}
	next := []entity.ModelMeasurement{{Name: entity.BodyChest, ValueMM: 910}}

	merged := mergeModelMeasurementsForUpdate(next, previous, entity.BodyMeasurementSchemaVersionP0)
	if len(merged) != 1 || merged[0].Name != entity.BodyChest {
		t.Fatalf("P0-aware merge = %+v, want authoritative next list", merged)
	}
}

func TestMergeModelMeasurementsLetsP0AwareWriterClearMetadata(t *testing.T) {
	previous := []entity.ModelMeasurement{{
		Name: entity.BodyChest, ValueMM: 900, Metadata: measurementMetadata("tape/chest-v1"),
	}}
	next := []entity.ModelMeasurement{{Name: entity.BodyChest, ValueMM: 910}}

	merged := mergeModelMeasurementsForUpdate(next, previous, entity.BodyMeasurementSchemaVersionP0)
	if len(merged) != 1 || merged[0].Metadata != nil {
		t.Fatalf("P0-aware nil metadata must clear the stored tuple: %+v", merged)
	}
}

func TestMergeModelMeasurementsLetsP0AwareWriterReplaceMetadata(t *testing.T) {
	previous := []entity.ModelMeasurement{{
		Name: entity.BodyChest, ValueMM: 900, Metadata: measurementMetadata("tape/chest-v1"),
	}}
	replacement := &entity.BodyMeasurementMetadataV1{
		SchemaVersion: entity.BodyMeasurementMetadataSchemaVersionV1,
		Source:        entity.BodyMeasurementThreeDScan,
		ProtocolID:    "scan/chest-v2",
	}
	next := []entity.ModelMeasurement{{Name: entity.BodyChest, ValueMM: 910, Metadata: replacement}}

	merged := mergeModelMeasurementsForUpdate(next, previous, entity.BodyMeasurementSchemaVersionP0)
	if len(merged) != 1 || merged[0].Metadata == nil ||
		merged[0].Metadata.Source != entity.BodyMeasurementThreeDScan ||
		merged[0].Metadata.ProtocolID != "scan/chest-v2" {
		t.Fatalf("P0-aware replacement metadata was not authoritative: %+v", merged)
	}
	if merged[0].Metadata == replacement {
		t.Fatal("merge returned the caller-owned metadata pointer")
	}
	if previous[0].Metadata.ProtocolID != "tape/chest-v1" {
		t.Fatal("replacement mutated stored metadata")
	}
}
