package cache

import (
	"context"
	"database/sql"
	"testing"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/stretchr/testify/require"
)

// TestRefreshDictionaryLoadsTags locks A3 at the cache layer: RefreshDictionary loads the controlled
// tag dictionary so GetTags reflects it. This is the in-memory half of "a created tag shows up in
// GetDictionary immediately" — the store half (getTags reading the `tag` table) feeds this.
func TestRefreshDictionaryLoadsTags(t *testing.T) {
	tags := []entity.TagDict{
		{ID: 1, Code: "sale", Name: "Sale"},
		{ID: 2, Code: "new", Name: "New", ArchivedAt: sql.NullTime{Valid: true}},
	}
	resetForTest(t)
	ctx := context.Background()
	RefreshDictionary(ctx, &entity.DictionaryInfo{Tags: tags})

	got := GetTags(ctx)
	require.Len(t, got, 2)
	require.Equal(t, "sale", got[0].Code)
	require.Equal(t, "new", got[1].Code)
	require.True(t, got[1].ArchivedAt.Valid)
}
