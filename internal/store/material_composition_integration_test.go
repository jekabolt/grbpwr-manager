package store

import (
	"context"
	"testing"
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/require"
)

// TestMaterialComposition is the acceptance test for the structured material composition (S17/P0.4,
// material_composition, migration 0167): a material round-trips its fibre composition resolved with
// dictionary names and ordered by descending percent, an update full-replaces it (and an empty set
// unsets it), and a composition that does not sum to 100 or references an unknown fibre is rejected
// with a field-tagged error. Uses the fibres seeded by 0177 (COT/POL).
func TestMaterialComposition(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	cfg := *testCfg
	cfg.Automigrate = true
	s, err := NewForTest(ctx, cfg)
	require.NoError(t, err)
	defer s.Close()
	tc := s.TechCards()

	dec := func(v string) decimal.Decimal { return decimal.RequireFromString(v) }

	// Create with a valid composition; the lower-case fibre code is normalised.
	id, err := tc.CreateMaterial(ctx, &entity.MaterialInsert{
		Name: "Composition Fabric", Section: "fabric", MaterialClass: "fabric",
		CompositionEntries: []entity.CompositionEntry{
			{FiberCode: "COT", Percent: dec("60")},
			{FiberCode: "pol", Percent: dec("40")},
		},
	})
	require.NoError(t, err)
	t.Cleanup(func() { _, _ = testDB.ExecContext(ctx, `DELETE FROM material WHERE id = ?`, id) })

	// Read back: two entries, ordered by descending percent, resolved with dictionary names, sum 100.
	m, err := tc.GetMaterial(ctx, id)
	require.NoError(t, err)
	require.Len(t, m.CompositionEntries, 2)
	require.Equal(t, "COT", m.CompositionEntries[0].FiberCode)
	require.Equal(t, "Cotton", m.CompositionEntries[0].Name, "name resolved from the fibre dictionary")
	require.True(t, m.CompositionEntries[0].Percent.Equal(dec("60")))
	require.Equal(t, "POL", m.CompositionEntries[1].FiberCode, "code is normalised to upper-case on write")
	require.Equal(t, "Polyester", m.CompositionEntries[1].Name)
	require.True(t, m.CompositionEntries[1].Percent.Equal(dec("40")))
	total := m.CompositionEntries[0].Percent.Add(m.CompositionEntries[1].Percent)
	require.True(t, total.Equal(dec("100")), "composition sums to 100")

	// ListMaterials also carries the composition (batch read path).
	list, err := tc.ListMaterials(ctx, "fabric", false)
	require.NoError(t, err)
	var found bool
	for _, lm := range list {
		if lm.Id == id {
			found = true
			require.Len(t, lm.CompositionEntries, 2)
		}
	}
	require.True(t, found, "created material appears in the list with its composition")

	// Update full-replaces the composition (100% cotton now).
	require.NoError(t, tc.UpdateMaterial(ctx, id, &entity.MaterialInsert{
		Name: "Composition Fabric", Section: "fabric", MaterialClass: "fabric",
		CompositionEntries: []entity.CompositionEntry{{FiberCode: "COT", Percent: dec("100")}},
	}, 0))
	m, err = tc.GetMaterial(ctx, id)
	require.NoError(t, err)
	require.Len(t, m.CompositionEntries, 1, "update full-replaces the composition")
	require.Equal(t, "COT", m.CompositionEntries[0].FiberCode)
	require.True(t, m.CompositionEntries[0].Percent.Equal(dec("100")))

	// An empty composition unsets it (valid — not every material has a fibre breakdown).
	require.NoError(t, tc.UpdateMaterial(ctx, id, &entity.MaterialInsert{
		Name: "Composition Fabric", Section: "fabric", MaterialClass: "fabric",
	}, 1))
	m, err = tc.GetMaterial(ctx, id)
	require.NoError(t, err)
	require.Empty(t, m.CompositionEntries, "empty composition is unset")

	// A composition that does not sum to 100 is rejected (field-tagged validation).
	_, err = tc.CreateMaterial(ctx, &entity.MaterialInsert{
		Name: "Bad Sum", Section: "fabric", MaterialClass: "fabric",
		CompositionEntries: []entity.CompositionEntry{
			{FiberCode: "COT", Percent: dec("60")},
			{FiberCode: "POL", Percent: dec("30")},
		},
	})
	require.Error(t, err, "composition summing to 90 must be rejected")
	var ve *entity.ValidationError
	require.ErrorAs(t, err, &ve, "sum error is a field-tagged validation error")

	// A composition referencing a fibre absent from the dictionary is rejected before the FK.
	_, err = tc.CreateMaterial(ctx, &entity.MaterialInsert{
		Name: "Bad Fibre", Section: "fabric", MaterialClass: "fabric",
		CompositionEntries: []entity.CompositionEntry{{FiberCode: "ZZZ", Percent: dec("100")}},
	})
	require.Error(t, err, "unknown fibre code must be rejected")
	require.ErrorAs(t, err, &ve, "unknown-fibre error is a field-tagged validation error")
}
