package dto

import (
	"errors"
	"testing"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	pb_common "github.com/jekabolt/grbpwr-manager/proto/gen/common"
	"github.com/stretchr/testify/require"
)

// The age group (0366) at the dto boundary: the write converter, the read converter, and the two
// read projections that carry it — the tech card (TechCard.age_group, 29) and the colourway's
// resolved merchandising (ColorwayMerchandising.age_group, 25).

// UNKNOWN is «no value», not a refusal — whether no value is acceptable depends on the update mask,
// which the converter does not see. A number the enum does not declare IS a refusal, field-tagged.
func TestConvertPbAgeGroupToEntity(t *testing.T) {
	got, err := ConvertPbAgeGroupToEntity(pb_common.AgeGroupEnum_AGE_GROUP_ENUM_UNKNOWN)
	require.NoError(t, err)
	require.Equal(t, entity.AgeGroupEnum(""), got)

	for pb, want := range map[pb_common.AgeGroupEnum]entity.AgeGroupEnum{
		pb_common.AgeGroupEnum_AGE_GROUP_ENUM_ADULT:   entity.AgeGroupAdult,
		pb_common.AgeGroupEnum_AGE_GROUP_ENUM_TEEN:    entity.AgeGroupTeen,
		pb_common.AgeGroupEnum_AGE_GROUP_ENUM_KIDS:    entity.AgeGroupKids,
		pb_common.AgeGroupEnum_AGE_GROUP_ENUM_TODDLER: entity.AgeGroupToddler,
		pb_common.AgeGroupEnum_AGE_GROUP_ENUM_BABY:    entity.AgeGroupBaby,
	} {
		got, err := ConvertPbAgeGroupToEntity(pb)
		require.NoError(t, err, pb.String())
		require.Equal(t, want, got, pb.String())
	}

	_, err = ConvertPbAgeGroupToEntity(pb_common.AgeGroupEnum(99))
	var ve *entity.ValidationError
	require.True(t, errors.As(err, &ve), "an undeclared number must be a field-tagged refusal, got %v", err)
	require.Equal(t, "age_group", ve.Field)
	require.Equal(t, "unknown_age_group", ve.Reason)
	require.Equal(t, "99", ve.Conflicting)
}

// A stored token this build cannot map reads as UNKNOWN — never as ADULT, which the editor would
// then show and the next full-replace save would write over the row.
func TestConvertEntityAgeGroupToPb(t *testing.T) {
	require.Equal(t, pb_common.AgeGroupEnum_AGE_GROUP_ENUM_KIDS, ConvertEntityAgeGroupToPb(entity.AgeGroupKids))
	require.Equal(t, pb_common.AgeGroupEnum_AGE_GROUP_ENUM_ADULT, ConvertEntityAgeGroupToPb(entity.AgeGroupAdult))
	require.Equal(t, pb_common.AgeGroupEnum_AGE_GROUP_ENUM_UNKNOWN, ConvertEntityAgeGroupToPb(""))
	require.Equal(t, pb_common.AgeGroupEnum_AGE_GROUP_ENUM_UNKNOWN, ConvertEntityAgeGroupToPb("senior"))
	require.Equal(t, pb_common.AgeGroupEnum_AGE_GROUP_ENUM_UNKNOWN, ConvertEntityAgeGroupToPb("KIDS"),
		"the stored vocabulary is lowercase; a differently-cased token is not a member")
}

func convertStylePatchWithAgeGroup(ag pb_common.AgeGroupEnum) (entity.StylePatch, error) {
	return ConvertPbStylePatchToEntity("grbpwr", pb_common.SeasonEnum_SEASON_ENUM_SS, 0, "",
		pb_common.GenderEnum_GENDER_ENUM_UNISEX, ag, "", "", "", 0, 0, 0, 0, 0)
}

func TestConvertPbStylePatchToEntityCarriesAgeGroup(t *testing.T) {
	patch, err := convertStylePatchWithAgeGroup(pb_common.AgeGroupEnum_AGE_GROUP_ENUM_TODDLER)
	require.NoError(t, err)
	require.Equal(t, entity.AgeGroupToddler, patch.AgeGroup)

	// UNKNOWN converts to "" without an error: the handler and the store decide, by the mask,
	// whether that means «keep the stored value» or a refusal.
	patch, err = convertStylePatchWithAgeGroup(pb_common.AgeGroupEnum_AGE_GROUP_ENUM_UNKNOWN)
	require.NoError(t, err)
	require.Equal(t, entity.AgeGroupEnum(""), patch.AgeGroup)

	_, err = convertStylePatchWithAgeGroup(pb_common.AgeGroupEnum(42))
	var ve *entity.ValidationError
	require.True(t, errors.As(err, &ve), "got %v", err)
	require.Equal(t, "age_group", ve.Field)
}

// TechCard.age_group (29) is the read-only projection of the style fact, next to fit.
func TestTechCardEmitsAgeGroup(t *testing.T) {
	tc := &entity.TechCard{}
	tc.AgeGroup = entity.AgeGroupKids
	require.Equal(t, pb_common.AgeGroupEnum_AGE_GROUP_ENUM_KIDS, ConvertEntityTechCardToPb(tc, CostingFx{}).GetAgeGroup())

	// A row not read through the column (or holding a token this build cannot map) is UNKNOWN.
	require.Equal(t, pb_common.AgeGroupEnum_AGE_GROUP_ENUM_UNKNOWN,
		ConvertEntityTechCardToPb(&entity.TechCard{}, CostingFx{}).GetAgeGroup())
}

// ColorwayMerchandising.age_group (25) carries the style's age group onto the colourway's admin read,
// resolved like target_gender.
func TestColorwayMerchandisingEmitsAgeGroup(t *testing.T) {
	display := &entity.ColorwayDisplay{}
	display.ProductBody.ProductBodyInsert.TargetGender = entity.Unisex
	display.ProductBody.ProductBodyInsert.AgeGroup = entity.AgeGroupTeen
	merch := buildColorwayDisplayPb(display).GetMerchandising()
	require.Equal(t, pb_common.AgeGroupEnum_AGE_GROUP_ENUM_TEEN, merch.GetAgeGroup())
	require.Equal(t, pb_common.GenderEnum_GENDER_ENUM_UNISEX, merch.GetTargetGender())

	require.Equal(t, pb_common.AgeGroupEnum_AGE_GROUP_ENUM_UNKNOWN,
		buildColorwayDisplayPb(&entity.ColorwayDisplay{}).GetMerchandising().GetAgeGroup())
}
