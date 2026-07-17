package dto

import (
	"testing"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	pb_common "github.com/jekabolt/grbpwr-manager/proto/gen/common"
	"github.com/stretchr/testify/require"
)

// NF-07 purpose rules: default sellable; auxiliary forbids products and carries an output material;
// sellable forbids an output material.
func TestTechCardPurposeValidation(t *testing.T) {
	// default (empty) purpose → sellable, no output material.
	def, err := ConvertPbTechCardInsertToEntity(&pb_common.TechCardInsert{StyleNumber: "PURP-1", Name: "Tee"})
	require.NoError(t, err)
	require.Equal(t, entity.TechCardPurposeSellable, def.Purpose)
	require.False(t, def.OutputMaterialId.Valid)

	// auxiliary with an output material, no products → ok.
	aux, err := ConvertPbTechCardInsertToEntity(&pb_common.TechCardInsert{
		StyleNumber: "PURP-2", Name: "Dust bag", Purpose: pb_common.TechCardPurpose_TECH_CARD_PURPOSE_AUXILIARY, OutputMaterialId: 42,
	})
	require.NoError(t, err)
	require.Equal(t, entity.TechCardPurposeAuxiliary, aux.Purpose)
	require.True(t, aux.OutputMaterialId.Valid)
	require.EqualValues(t, 42, aux.OutputMaterialId.Int64)

	// auxiliary + linked products → rejected.
	_, err = ConvertPbTechCardInsertToEntity(&pb_common.TechCardInsert{
		StyleNumber: "PURP-3", Name: "Dust bag", Purpose: pb_common.TechCardPurpose_TECH_CARD_PURPOSE_AUXILIARY, ProductIds: []int32{7},
	})
	require.Error(t, err, "auxiliary card cannot link products")

	// sellable + output material → rejected.
	_, err = ConvertPbTechCardInsertToEntity(&pb_common.TechCardInsert{
		StyleNumber: "PURP-4", Name: "Tee", OutputMaterialId: 42,
	})
	require.Error(t, err, "sellable card cannot carry an output material")

	// unknown purpose → rejected.
	_, err = ConvertPbTechCardInsertToEntity(&pb_common.TechCardInsert{
		StyleNumber: "PURP-5", Name: "Tee", Purpose: pb_common.TechCardPurpose(99),
	})
	require.Error(t, err)
}
