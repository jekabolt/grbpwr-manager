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

	// auxiliary + linked products → rejected: moved to the Colorway RPCs (R1). product_ids left the
	// tech-card payload, and CreateColorway refuses to attach a colourway to an auxiliary style, so the
	// gate is enforced where the link is created — re-covered in track T-B step D.

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
