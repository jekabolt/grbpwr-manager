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

func createTestMedia(ctx context.Context, store *MYSQLStore, count int) ([]int, error) {
	var mediaIds []int
	for i := 0; i < count; i++ {
		media := &entity.MediaItem{
			FullSizeMediaURL:   "test/full/path.jpg",
			FullSizeWidth:      1920,
			FullSizeHeight:     1080,
			ThumbnailMediaURL:  "test/thumb/path.jpg",
			ThumbnailWidth:     300,
			ThumbnailHeight:    200,
			CompressedMediaURL: "test/compressed/path.jpg",
			CompressedWidth:    800,
			CompressedHeight:   600,
			BlurHash:           sql.NullString{String: "test-blur-hash", Valid: true},
		}
		id, err := store.Media().AddMedia(ctx, media)
		if err != nil {
			return nil, err
		}
		mediaIds = append(mediaIds, id)
	}
	return mediaIds, nil
}

func cleanupArchives(ctx context.Context, store *MYSQLStore, archiveIds []int) error {
	for _, id := range archiveIds {
		if err := store.Archive().DeleteArchiveById(ctx, id); err != nil {
			return err
		}
	}
	return nil
}

func cleanupMedia(ctx context.Context, store *MYSQLStore, mediaIds []int) error {
	// First, delete all archive items that reference these media
	query := `DELETE FROM archive_item WHERE media_id IN (:mediaIds)`
	if err := ExecNamed(ctx, store.DB(), query, map[string]any{
		"mediaIds": mediaIds,
	}); err != nil {
		return err
	}

	// Then delete the media
	for _, id := range mediaIds {
		if err := store.Media().DeleteMediaById(ctx, id); err != nil {
			return err
		}
	}
	return nil
}

func TestArchiveCRUD(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// Initialize store with migrations
	cfg := *testCfg
	cfg.Automigrate = true
	store, err := NewForTest(ctx, cfg)
	require.NoError(t, err)
	defer store.Close()

	// Create test media first
	mediaIds, err := createTestMedia(ctx, store, 11) // We need 11 media items for all our tests
	require.NoError(t, err)

	// Initialize hero data
	err = store.Hero().SetHero(ctx, entity.HeroFullInsert{
		Entities: []entity.HeroEntityInsert{
			{
				Type: entity.HeroTypeSingle,
				Single: entity.HeroSingleInsert{
					MediaPortraitId:  mediaIds[0],
					MediaLandscapeId: mediaIds[1],
					Headline:         "Test Hero",
					ExploreLink:      "/test",
					ExploreText:      "Explore",
				},
			},
		},
		NavFeatured: entity.NavFeaturedInsert{
			Men: entity.NavFeaturedEntityInsert{
				MediaId:     mediaIds[2],
				ExploreText: "Men's Collection",
				FeaturedTag: "men",
			},
			Women: entity.NavFeaturedEntityInsert{
				MediaId:     mediaIds[3],
				ExploreText: "Women's Collection",
				FeaturedTag: "women",
			},
		},
	})
	require.NoError(t, err)

	var archiveIds []int
	defer func() {
		// Cleanup in correct order: first archives, then media
		err := cleanupArchives(ctx, store, archiveIds)
		assert.NoError(t, err)
		err = cleanupMedia(ctx, store, mediaIds)
		assert.NoError(t, err)
	}()

	// Test Create (Add)
	t.Run("create", func(t *testing.T) {
		// Try to create with empty media ids (should fail)
		emptyArchive := &entity.ArchiveInsert{
			Heading:     "Empty Archive",
			Description: "Empty Description",
			Tag:         "empty",
			MediaIds:    []int{},
		}
		_, err := store.Archive().AddArchive(ctx, emptyArchive)
		assert.Error(t, err, "should fail with empty media ids")

		// Create valid archive
		archive := &entity.ArchiveInsert{
			Heading:     "Test Archive",
			Description: "Test Description",
			Tag:         "test",
			MediaIds:    mediaIds[0:3],
			VideoId:     sql.NullInt32{Int32: int32(mediaIds[3]), Valid: true},
		}
		id, err := store.Archive().AddArchive(ctx, archive)
		require.NoError(t, err)
		assert.Greater(t, id, 0)
		archiveIds = append(archiveIds, id)

		// Test Read (Get by ID)
		t.Run("read", func(t *testing.T) {
			// Get non-existent archive
			_, err := store.Archive().GetArchiveById(ctx, 99999)
			assert.Error(t, err, "should fail with non-existent id")

			// Get created archive
			result, err := store.Archive().GetArchiveById(ctx, id)
			require.NoError(t, err)
			// Only verify the core fields, not media-related fields
			assert.Equal(t, archive.Heading, result.Heading)
			assert.Equal(t, archive.Description, result.Description)
			assert.Equal(t, archive.Tag, result.Tag)

			// Test Read (Get Paged)
			t.Run("read_paged", func(t *testing.T) {
				// Create additional archives for pagination test
				archives := []*entity.ArchiveInsert{
					{
						Heading:     "Archive 2",
						Description: "Description 2",
						Tag:         "tag2",
						MediaIds:    mediaIds[4:6],
					},
					{
						Heading:     "Archive 3",
						Description: "Description 3",
						Tag:         "tag3",
						MediaIds:    mediaIds[6:8],
					},
				}

				for _, a := range archives {
					newId, err := store.Archive().AddArchive(ctx, a)
					require.NoError(t, err)
					archiveIds = append(archiveIds, newId)
				}

				// Test invalid pagination
				_, _, err = store.Archive().GetArchivesPaged(ctx, 0, 0, entity.Descending)
				assert.Error(t, err, "should fail with invalid limit")

				_, _, err = store.Archive().GetArchivesPaged(ctx, 10, -1, entity.Descending)
				assert.Error(t, err, "should fail with invalid offset")

				// Test valid pagination
				results, count, err := store.Archive().GetArchivesPaged(ctx, 2, 0, entity.Descending)
				require.NoError(t, err)
				assert.Equal(t, 3, count) // Total count should be 3 (1 original + 2 additional)
				assert.Len(t, results, 2) // Should return 2 items due to limit

				// Test Update
				t.Run("update", func(t *testing.T) {
					// Update non-existent archive
					err := store.Archive().UpdateArchive(ctx, 99999, archive)
					assert.Error(t, err, "should fail with non-existent id")

					// Update existing archive
					updateArchive := &entity.ArchiveInsert{
						Heading:     "Updated Archive",
						Description: "Updated Description",
						Tag:         "updated",
						MediaIds:    mediaIds[8:10],
						VideoId:     sql.NullInt32{Int32: int32(mediaIds[10]), Valid: true},
					}
					err = store.Archive().UpdateArchive(ctx, id, updateArchive)
					require.NoError(t, err)

					// Verify update
					updated, err := store.Archive().GetArchiveById(ctx, id)
					require.NoError(t, err)
					// Only verify the core fields, not media-related fields
					assert.Equal(t, updateArchive.Heading, updated.Heading)
					assert.Equal(t, updateArchive.Description, updated.Description)
					assert.Equal(t, updateArchive.Tag, updated.Tag)

					// Test Delete
					t.Run("delete", func(t *testing.T) {
						// Delete non-existent archive
						err := store.Archive().DeleteArchiveById(ctx, 99999)
						assert.Error(t, err, "should fail with non-existent id")

						// Delete all created archives
						for _, aid := range archiveIds {
							err = store.Archive().DeleteArchiveById(ctx, aid)
							require.NoError(t, err)
						}
						archiveIds = nil // Clear the slice since we've deleted all archives

						// Verify deletion
						_, err = store.Archive().GetArchiveById(ctx, id)
						assert.Error(t, err, "should fail to get deleted archive")

						// Verify all archives are deleted
						results, count, err := store.Archive().GetArchivesPaged(ctx, 10, 0, entity.Descending)
						require.NoError(t, err)
						assert.Equal(t, 0, count)
						assert.Len(t, results, 0)
					})
				})
			})
		})
	})
}
