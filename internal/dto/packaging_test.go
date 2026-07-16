package dto

import (
	"testing"

	pb_admin "github.com/jekabolt/grbpwr-manager/proto/gen/admin"
	"github.com/stretchr/testify/require"
)

// TestConvertPbPackagingBomToEntity covers the packaging-recipe write validation (gap-07 v2 B).
func TestConvertPbPackagingBomToEntity(t *testing.T) {
	ok := []*pb_admin.PackagingBomItem{
		{MaterialId: 10, QtyPerOrder: dec("1"), Active: true},  // box: per order
		{MaterialId: 11, QtyPerItem: dec("1.5"), Active: true}, // dust bag: per item
		{MaterialId: 12, QtyPerOrder: dec("1"), QtyPerItem: dec("2"), Active: false},
	}
	got, err := ConvertPbPackagingBomToEntity(ok)
	require.NoError(t, err)
	require.Len(t, got, 3)
	require.Equal(t, 10, got[0].MaterialId)
	require.Equal(t, "1", got[0].QtyPerOrder.String())
	require.True(t, got[0].QtyPerItem.IsZero())
	require.Equal(t, "1.5", got[1].QtyPerItem.String())
	require.False(t, got[2].Active)

	// failures.
	for name, in := range map[string][]*pb_admin.PackagingBomItem{
		"no material_id": {{QtyPerOrder: dec("1")}},
		"negative qty":   {{MaterialId: 10, QtyPerOrder: dec("-1")}},
		"zero total":     {{MaterialId: 10, QtyPerOrder: dec("0"), QtyPerItem: dec("0")}},
		"duplicate mat":  {{MaterialId: 10, QtyPerOrder: dec("1")}, {MaterialId: 10, QtyPerItem: dec("1")}},
		"bad number":     {{MaterialId: 10, QtyPerOrder: dec("abc")}},
	} {
		_, err := ConvertPbPackagingBomToEntity(in)
		require.Error(t, err, name)
	}

	// empty set is allowed (clears the recipe).
	empty, err := ConvertPbPackagingBomToEntity(nil)
	require.NoError(t, err)
	require.Empty(t, empty)
}
