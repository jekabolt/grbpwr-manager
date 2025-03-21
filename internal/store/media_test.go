package store

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// cleanupAllMedia removes all media entries from the database
func cleanupAllMedia(ctx context.Context, store *MYSQLStore) error {
	return ExecNamed(ctx, store.DB(), "DELETE FROM media", nil)
}

func TestMedia(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// Initialize store with migrations
	cfg := *testCfg
	cfg.Automigrate = true
	store, err := NewForTest(ctx, cfg)
	require.NoError(t, err)
	defer store.Close()

	// Create initial hero data to avoid errors
	err = store.Hero().SetHero(ctx, entity.HeroFullInsert{
		Entities:    []entity.HeroEntityInsert{},
		NavFeatured: entity.NavFeaturedInsert{},
	})
	require.NoError(t, err)

	t.Run("AddMedia", func(t *testing.T) {
		// Clean up before test
		err := cleanupAllMedia(ctx, store)
		require.NoError(t, err)

		tests := []struct {
			name    string
			media   *entity.MediaItem
			wantErr bool
		}{
			{
				name: "valid media",
				media: &entity.MediaItem{
					FullSizeMediaURL:   "https://example.com/full.jpg",
					FullSizeWidth:      1920,
					FullSizeHeight:     1080,
					CompressedMediaURL: "https://example.com/compressed.jpg",
					CompressedWidth:    960,
					CompressedHeight:   540,
					ThumbnailMediaURL:  "https://example.com/thumb.jpg",
					ThumbnailWidth:     320,
					ThumbnailHeight:    180,
					BlurHash:           sql.NullString{String: "L9AS}j^-0#NGaJt79FD%", Valid: true},
				},
				wantErr: false,
			},
			{
				name: "empty media",
				media: &entity.MediaItem{
					FullSizeMediaURL: "",
					BlurHash:         sql.NullString{String: "", Valid: false},
				},
				wantErr: false, // MySQL will accept empty strings
			},
		}

		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				id, err := store.Media().AddMedia(ctx, tt.media)
				if tt.wantErr {
					assert.Error(t, err)
					return
				}
				assert.NoError(t, err)
				assert.Greater(t, id, 0)
			})
		}
	})

	t.Run("GetMediaById", func(t *testing.T) {
		// Clean up before test
		err := cleanupAllMedia(ctx, store)
		require.NoError(t, err)

		// First, add a media item
		media := &entity.MediaItem{
			FullSizeMediaURL:   "https://example.com/test.jpg",
			FullSizeWidth:      1920,
			FullSizeHeight:     1080,
			CompressedMediaURL: "https://example.com/test_compressed.jpg",
			CompressedWidth:    960,
			CompressedHeight:   540,
			ThumbnailMediaURL:  "https://example.com/test_thumb.jpg",
			ThumbnailWidth:     320,
			ThumbnailHeight:    180,
			BlurHash:           sql.NullString{String: "L9AS}j^-0#NGaJt79FD%", Valid: true},
		}

		id, err := store.Media().AddMedia(ctx, media)
		require.NoError(t, err)

		tests := []struct {
			name    string
			id      int
			wantErr bool
		}{
			{
				name:    "existing media",
				id:      id,
				wantErr: false,
			},
			{
				name:    "non-existing media",
				id:      999999,
				wantErr: true,
			},
		}

		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				result, err := store.Media().GetMediaById(ctx, tt.id)
				if tt.wantErr {
					assert.Error(t, err)
					return
				}
				assert.NoError(t, err)
				assert.NotNil(t, result)
				assert.Equal(t, media.FullSizeMediaURL, result.FullSizeMediaURL)
				assert.Equal(t, media.BlurHash.String, result.BlurHash.String)
			})
		}
	})

	t.Run("DeleteMediaById", func(t *testing.T) {
		// Clean up before test
		err := cleanupAllMedia(ctx, store)
		require.NoError(t, err)

		// First, add a media item
		media := &entity.MediaItem{
			FullSizeMediaURL: "https://example.com/delete_test.jpg",
			FullSizeWidth:    1920,
			FullSizeHeight:   1080,
		}

		id, err := store.Media().AddMedia(ctx, media)
		require.NoError(t, err)

		tests := []struct {
			name    string
			id      int
			wantErr bool
		}{
			{
				name:    "existing media",
				id:      id,
				wantErr: false,
			},
			{
				name:    "non-existing media",
				id:      999999,
				wantErr: false, // MySQL DELETE doesn't error on non-existing rows
			},
		}

		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				err := store.Media().DeleteMediaById(ctx, tt.id)
				if tt.wantErr {
					assert.Error(t, err)
					return
				}
				assert.NoError(t, err)

				// Verify deletion
				_, err = store.Media().GetMediaById(ctx, tt.id)
				assert.Error(t, err) // Should error as media is deleted
			})
		}
	})

	t.Run("ListMediaPaged", func(t *testing.T) {
		// Clean up before test
		err := cleanupAllMedia(ctx, store)
		require.NoError(t, err)

		// Add multiple media items
		mediaItems := []*entity.MediaItem{
			{FullSizeMediaURL: "https://example.com/1.jpg"},
			{FullSizeMediaURL: "https://example.com/2.jpg"},
			{FullSizeMediaURL: "https://example.com/3.jpg"},
			{FullSizeMediaURL: "https://example.com/4.jpg"},
			{FullSizeMediaURL: "https://example.com/5.jpg"},
		}

		for _, item := range mediaItems {
			_, err := store.Media().AddMedia(ctx, item)
			require.NoError(t, err)
		}

		tests := []struct {
			name        string
			limit       int
			offset      int
			orderFactor entity.OrderFactor
			wantCount   int
			wantErr     bool
		}{
			{
				name:        "valid page",
				limit:       3,
				offset:      0,
				orderFactor: entity.Ascending,
				wantCount:   3,
				wantErr:     false,
			},
			{
				name:        "second page",
				limit:       3,
				offset:      3,
				orderFactor: entity.Ascending,
				wantCount:   2,
				wantErr:     false,
			},
			{
				name:        "invalid limit",
				limit:       0,
				offset:      0,
				orderFactor: entity.Ascending,
				wantCount:   0,
				wantErr:     true,
			},
			{
				name:        "invalid offset",
				limit:       10,
				offset:      -1,
				orderFactor: entity.Ascending,
				wantCount:   0,
				wantErr:     true,
			},
			{
				name:        "descending order",
				limit:       5,
				offset:      0,
				orderFactor: entity.Descending,
				wantCount:   5,
				wantErr:     false,
			},
		}

		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				result, err := store.Media().ListMediaPaged(ctx, tt.limit, tt.offset, tt.orderFactor)
				if tt.wantErr {
					assert.Error(t, err)
					return
				}
				assert.NoError(t, err)
				t.Logf("Got %d items for limit=%d, offset=%d", len(result), tt.limit, tt.offset)
				for i, item := range result {
					t.Logf("Item %d: ID=%d, URL=%s", i, item.Id, item.FullSizeMediaURL)
				}
				assert.Len(t, result, tt.wantCount)

				if len(result) > 1 && tt.orderFactor == entity.Ascending {
					assert.Less(t, result[0].Id, result[1].Id)
				} else if len(result) > 1 && tt.orderFactor == entity.Descending {
					assert.Greater(t, result[0].Id, result[1].Id)
				}
			})
		}
	})
}
