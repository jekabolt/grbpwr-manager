package design

import (
	"testing"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/stretchr/testify/require"
)

// B-m1: потолок полки считается нетто — строка, которую та же транзакция соберёт, места не занимает.
func TestShelfFullIsNetOfFreedRows(t *testing.T) {
	max := entity.MaxDesignAssetsPerCard
	require.False(t, shelfFull(max-1, 0))
	require.True(t, shelfFull(max, 0))
	require.False(t, shelfFull(max, 1), "замена на полной полке — строка за строку")
	require.True(t, shelfFull(max+1, 1))
	require.True(t, shelfFull(max, -1), "отрицательное освобождение не даёт места")
}

// B-m1/B-m2: предикат GC прежнего ассета пары совпадает с dropSupersededHardwareTx после переезда меток.
func TestSupersededCollectable(t *testing.T) {
	hw, fab := entity.DesignAssetKindHardware, entity.DesignAssetKindFabric
	for _, c := range []struct {
		name                          string
		kind                          string
		cwHeld, landHW                bool
		bindings, placements, derived int
		want                          bool
	}{
		{"сирота", hw, false, true, 1, 0, 0, true},
		{"метки переедут", hw, false, true, 1, 3, 0, true},
		{"метки остаются: садится не hardware", hw, false, false, 1, 2, 0, false},
		{"без меток, садится не hardware", hw, false, false, 1, 0, 0, true},
		{"носит другая пара", hw, false, true, 2, 0, 0, false},
		{"есть ребёнок", hw, false, true, 1, 0, 1, false},
		{"носит колорвей", hw, true, true, 1, 0, 0, false},
		{"ткань — библиотека", fab, false, true, 1, 0, 0, false},
	} {
		require.Equal(t, c.want, supersededCollectable(c.kind, c.cwHeld, c.landHW, c.bindings, c.placements, c.derived), c.name)
	}
}
