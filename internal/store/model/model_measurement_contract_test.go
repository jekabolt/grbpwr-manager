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

func measurementNames(measurements []entity.ModelMeasurement) map[entity.BodyMeasurementName]int {
	out := make(map[entity.BodyMeasurementName]int, len(measurements))
	for _, m := range measurements {
		if _, dup := out[m.Name]; dup {
			out[m.Name] = -1
			continue
		}
		out[m.Name] = m.ValueMM
	}
	return out
}

func TestMergeModelMeasurementsPreservesNeckForOlderWriters(t *testing.T) {
	previous := []entity.ModelMeasurement{
		{Name: entity.BodyChest, ValueMM: 900},
		{Name: entity.BodyNeckBase, ValueMM: 400},
		{Name: entity.BodyHPSHeight, ValueMM: 1600},
		{Name: entity.BodyNeck, ValueMM: 360, Metadata: measurementMetadata("tape/neck-v1")},
	}
	next := []entity.ModelMeasurement{{Name: entity.BodyChest, ValueMM: 910}}

	// v1 (P0-aware, neck-unaware): absent P0 is deleted, absent neck survives.
	got := measurementNames(mergeModelMeasurementsForUpdate(next, previous, entity.BodyMeasurementSchemaVersionP0))
	want := map[entity.BodyMeasurementName]int{entity.BodyChest: 910, entity.BodyNeck: 360}
	if len(got) != len(want) || got[entity.BodyChest] != 910 || got[entity.BodyNeck] != 360 {
		t.Fatalf("v1 merge = %v, want %v", got, want)
	}

	// v0 (legacy): absent P0 and absent neck both survive; neck_base is base vocabulary.
	got = measurementNames(mergeModelMeasurementsForUpdate(next, previous, 0))
	if len(got) != 3 || got[entity.BodyChest] != 910 || got[entity.BodyHPSHeight] != 1600 || got[entity.BodyNeck] != 360 {
		t.Fatalf("v0 merge = %v, want chest 910, hps_height 1600, neck 360", got)
	}

	// v2 (neck-aware): the list is authoritative; omission deletes neck.
	got = measurementNames(mergeModelMeasurementsForUpdate(next, previous, entity.BodyMeasurementSchemaVersionP1))
	if len(got) != 1 || got[entity.BodyChest] != 910 {
		t.Fatalf("v2 merge = %v, want chest only", got)
	}
}

func TestMergeModelMeasurementsNeckEchoFromOlderWriterIsNotDuplicated(t *testing.T) {
	previous := []entity.ModelMeasurement{
		{Name: entity.BodyNeck, ValueMM: 360, Metadata: measurementMetadata("tape/neck-v1")},
	}
	// The admin form round-trips every stored measurement, so a v1 writer echoes neck.
	next := []entity.ModelMeasurement{{Name: entity.BodyNeck, ValueMM: 365, Metadata: measurementMetadata("tape/neck-v1")}}

	merged := mergeModelMeasurementsForUpdate(next, previous, entity.BodyMeasurementSchemaVersionP0)
	if len(merged) != 1 || merged[0].Name != entity.BodyNeck || merged[0].ValueMM != 365 {
		t.Fatalf("echoed neck merge = %+v, want one neck row at 365", merged)
	}
	if merged[0].Metadata == nil || merged[0].Metadata.ProtocolID != "tape/neck-v1" {
		t.Fatalf("echoed neck metadata = %+v", merged[0].Metadata)
	}

	merged[0].Metadata.ProtocolID = "changed"
	if previous[0].Metadata.ProtocolID != "tape/neck-v1" || next[0].Metadata.ProtocolID != "tape/neck-v1" {
		t.Fatal("merge returned an aliased metadata pointer")
	}
}

func TestMergeModelMeasurementsPreservedNeckIsCopied(t *testing.T) {
	previous := []entity.ModelMeasurement{
		{Name: entity.BodyNeck, ValueMM: 360, Metadata: measurementMetadata("tape/neck-v1")},
	}
	merged := mergeModelMeasurementsForUpdate(nil, previous, entity.BodyMeasurementSchemaVersionP0)
	if len(merged) != 1 || merged[0].Metadata == nil {
		t.Fatalf("preserved neck = %+v", merged)
	}
	merged[0].Metadata.ProtocolID = "changed"
	if previous[0].Metadata.ProtocolID != "tape/neck-v1" {
		t.Fatal("preserved neck aliases stored metadata")
	}
}
