package dto

import (
	"database/sql"
	"testing"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/stretchr/testify/require"
)

// TestConvertToCommonDictionaryTags locks A3 at the DTO layer: the controlled tag dictionary
// (Dict.Tags, sourced from the `tag` table) is projected onto the wire Dictionary.Tags field — the
// wiring that was previously missing, leaving a freshly created tag invisible in GetDictionary.
func TestConvertToCommonDictionaryTags(t *testing.T) {
	common := ConvertToCommonDictionary(Dict{
		Tags: []entity.TagDict{
			{ID: 1, Code: "sale", Name: "Sale"},
			{ID: 2, Code: "archived-tag", Name: "Archived", ArchivedAt: sql.NullTime{Valid: true}},
		},
	})

	require.Len(t, common.Tags, 2)
	require.Equal(t, int32(1), common.Tags[0].Id)
	require.Equal(t, "sale", common.Tags[0].Code)
	require.Equal(t, "Sale", common.Tags[0].Name)
	require.False(t, common.Tags[0].Archived)
	require.True(t, common.Tags[1].Archived, "archived_at set -> Archived true")
}
