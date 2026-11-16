package store

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/stretchr/testify/require"
)

func storedMeasurementValues(t *testing.T, m *entity.Model) map[entity.BodyMeasurementName]int {
	t.Helper()
	out := make(map[entity.BodyMeasurementName]int, len(m.Measurements))
	for _, measurement := range m.Measurements {
		_, dup := out[measurement.Name]
		require.False(t, dup, "duplicate stored measurement %q", measurement.Name)
		out[measurement.Name] = measurement.ValueMM
	}
	return out
}

// TestModelNeckMeasurementSurvivesOlderWriters drives the real store: neck is
// saved and read back next to neck_base, an update from a neck-unaware (v1)
// or legacy (v0) writer keeps it, and only a neck-aware (v2) writer deletes it.
func TestModelNeckMeasurementSurvivesOlderWriters(t *testing.T) {
	requireLocalModelPoseDB(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cfg := *testCfg
	s, err := NewForTest(ctx, cfg)
	require.NoError(t, err)

	neckMetadata := &entity.BodyMeasurementMetadataV1{
		SchemaVersion: entity.BodyMeasurementMetadataSchemaVersionV1,
		Source:        entity.BodyMeasurementManualTape,
		ProtocolID:    "internal/tape-v1",
	}
	id, err := s.Models().AddModel(ctx, &entity.ModelInsert{
		Name: fmt.Sprintf("neck-%d", time.Now().UnixNano()),
		Measurements: []entity.ModelMeasurement{
			{Name: entity.BodyChest, ValueMM: 900},
			{Name: entity.BodyNeckBase, ValueMM: 400},
			{Name: entity.BodyNeck, ValueMM: 360, Metadata: neckMetadata},
		},
		BodyMeasurementSchemaVersion: entity.BodyMeasurementSchemaVersionP1,
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = s.Models().DeleteModel(context.Background(), id) })

	created, err := s.Models().GetModelById(ctx, id)
	require.NoError(t, err)
	require.Equal(t, map[entity.BodyMeasurementName]int{
		entity.BodyChest: 900, entity.BodyNeckBase: 400, entity.BodyNeck: 360,
	}, storedMeasurementValues(t, created))
	for _, m := range created.Measurements {
		if m.Name == entity.BodyNeck {
			require.NotNil(t, m.Metadata)
			require.Equal(t, "internal/tape-v1", m.Metadata.ProtocolID)
		}
	}

	// A v1 writer (knows 1..34, not neck) saves without neck: neck stays, neck_base change applies.
	require.NoError(t, s.Models().UpdateModel(ctx, id, &entity.ModelInsert{
		Name: created.Name,
		Measurements: []entity.ModelMeasurement{
			{Name: entity.BodyChest, ValueMM: 910},
			{Name: entity.BodyNeckBase, ValueMM: 410},
		},
		BodyMeasurementSchemaVersion: entity.BodyMeasurementSchemaVersionP0,
	}))
	afterV1, err := s.Models().GetModelById(ctx, id)
	require.NoError(t, err)
	require.Equal(t, map[entity.BodyMeasurementName]int{
		entity.BodyChest: 910, entity.BodyNeckBase: 410, entity.BodyNeck: 360,
	}, storedMeasurementValues(t, afterV1))

	// A legacy v0 writer saves without neck: neck stays.
	require.NoError(t, s.Models().UpdateModel(ctx, id, &entity.ModelInsert{
		Name:         created.Name,
		Measurements: []entity.ModelMeasurement{{Name: entity.BodyChest, ValueMM: 920}},
	}))
	afterV0, err := s.Models().GetModelById(ctx, id)
	require.NoError(t, err)
	require.Equal(t, map[entity.BodyMeasurementName]int{
		entity.BodyChest: 920, entity.BodyNeck: 360,
	}, storedMeasurementValues(t, afterV0))

	// A neck-aware v2 writer omits neck: that is an authoritative delete.
	require.NoError(t, s.Models().UpdateModel(ctx, id, &entity.ModelInsert{
		Name:                         created.Name,
		Measurements:                 []entity.ModelMeasurement{{Name: entity.BodyChest, ValueMM: 930}},
		BodyMeasurementSchemaVersion: entity.BodyMeasurementSchemaVersionP1,
	}))
	afterV2, err := s.Models().GetModelById(ctx, id)
	require.NoError(t, err)
	require.Equal(t, map[entity.BodyMeasurementName]int{
		entity.BodyChest: 930,
	}, storedMeasurementValues(t, afterV2))
}
