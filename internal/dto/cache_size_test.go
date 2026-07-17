package dto

import (
	"testing"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	pb_common "github.com/jekabolt/grbpwr-manager/proto/gen/common"
	"github.com/stretchr/testify/require"
)

func TestDictionarySizeSKUSystemIsTyped(t *testing.T) {
	dict := ConvertToCommonDictionary(Dict{Sizes: []entity.Size{
		{Id: 1, Name: "m", SkuOrd: 25, SkuSystem: entity.SizeSKUSystemApparel},
		{Id: 2, Name: "42", SkuOrd: 64, SkuSystem: entity.SizeSKUSystemShoe},
		{Id: 3, Name: "m_48ta_m", SkuOrd: 55, SkuSystem: entity.SizeSKUSystemCompositeTA},
		{Id: 4, Name: "m_32bo_m", SkuOrd: 55, SkuSystem: entity.SizeSKUSystemCompositeBO},
		{Id: 5, Name: "bad", SkuOrd: 1, SkuSystem: entity.SizeSKUSystem("free-text")},
	}})

	require.Len(t, dict.Sizes, 5)
	require.Equal(t, pb_common.SizeSkuSystem_SIZE_SKU_SYSTEM_APPAREL, dict.Sizes[0].SkuSystem)
	require.Equal(t, pb_common.SizeSkuSystem_SIZE_SKU_SYSTEM_SHOE, dict.Sizes[1].SkuSystem)
	require.Equal(t, pb_common.SizeSkuSystem_SIZE_SKU_SYSTEM_COMPOSITE_TA, dict.Sizes[2].SkuSystem)
	require.Equal(t, pb_common.SizeSkuSystem_SIZE_SKU_SYSTEM_COMPOSITE_BO, dict.Sizes[3].SkuSystem)
	require.Equal(t, pb_common.SizeSkuSystem_SIZE_SKU_SYSTEM_UNKNOWN, dict.Sizes[4].SkuSystem)
}
