package store

import (
	"context"
	"testing"
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestHeroOperations(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// Initialize store with migrations
	cfg := *testCfg
	cfg.Automigrate = true
	store, err := NewForTest(ctx, cfg)
	require.NoError(t, err)
	defer store.Close()

	// Create test media first
	mediaIds, err := createTestMedia(ctx, store, 4) // We need 4 media items for our test
	require.NoError(t, err)
	defer func() {
		err = cleanupMedia(ctx, store, mediaIds)
		assert.NoError(t, err)
	}()

	heroStore := store.Hero()

	// Test error cases first
	t.Run("GetHero_NoData", func(t *testing.T) {
		// Clean up existing hero data
		err := deleteExistingHeroData(ctx, store)
		require.NoError(t, err)

		// Try to get hero when no data exists
		hero, err := heroStore.GetHero(ctx)
		assert.Error(t, err)
		assert.Nil(t, hero)
	})

	t.Run("SetHero_InvalidData", func(t *testing.T) {
		invalidHero := entity.HeroFullInsert{
			Entities: []entity.HeroEntityInsert{
				{
					Type: entity.HeroType(999), // Invalid type
					Main: entity.HeroMainInsert{},
				},
			},
		}

		err := heroStore.SetHero(ctx, invalidHero)
		assert.Error(t, err)
	})

	// Test data
	heroInsert := entity.HeroFullInsert{
		Entities: []entity.HeroEntityInsert{
			{
				Type: entity.HeroTypeMain,
				Main: entity.HeroMainInsert{
					Single: entity.HeroSingleInsert{
						MediaPortraitId:  mediaIds[0],
						MediaLandscapeId: mediaIds[1],
						ExploreLink:      "https://example.com",
						ExploreText:      "Explore Now",
						Headline:         "Test Headline",
					},
					Tag:         "test-tag",
					Description: "Test Description",
				},
			},
			{
				Type: entity.HeroTypeSingle,
				Single: entity.HeroSingleInsert{
					MediaPortraitId:  mediaIds[2],
					MediaLandscapeId: mediaIds[3],
					Headline:         "Single Test",
					ExploreLink:      "https://single.com",
					ExploreText:      "View Single",
				},
			},
		},
		NavFeatured: entity.NavFeaturedInsert{
			Men: entity.NavFeaturedEntityInsert{
				MediaId:           mediaIds[0],
				ExploreText:       "Men's Collection",
				FeaturedTag:       "men",
				FeaturedArchiveId: 1,
			},
			Women: entity.NavFeaturedEntityInsert{
				MediaId:           mediaIds[1],
				ExploreText:       "Women's Collection",
				FeaturedTag:       "women",
				FeaturedArchiveId: 2,
			},
		},
	}

	// Test SetHero
	t.Run("SetHero", func(t *testing.T) {
		err := heroStore.SetHero(ctx, heroInsert)
		require.NoError(t, err)
	})

	// Test GetHero
	t.Run("GetHero", func(t *testing.T) {
		hero, err := heroStore.GetHero(ctx)
		require.NoError(t, err)
		require.NotNil(t, hero)

		// Verify the data
		assert.Len(t, hero.Entities, 2)
		assert.Equal(t, entity.HeroTypeMain, hero.Entities[0].Type)
		assert.Equal(t, entity.HeroTypeSingle, hero.Entities[1].Type)

		// Check main entity
		mainEntity := hero.Entities[0]
		assert.Equal(t, "test-tag", mainEntity.Main.Tag)
		assert.Equal(t, "Test Description", mainEntity.Main.Description)
		assert.Equal(t, "Test Headline", mainEntity.Main.Single.Headline)

		// Check single entity
		singleEntity := hero.Entities[1]
		assert.Equal(t, "Single Test", singleEntity.Single.Headline)
		assert.Equal(t, "https://single.com", singleEntity.Single.ExploreLink)

		// Check NavFeatured
		assert.Equal(t, "Men's Collection", hero.NavFeatured.Men.ExploreText)
		assert.Equal(t, "men", hero.NavFeatured.Men.FeaturedTag)
		assert.Equal(t, "Women's Collection", hero.NavFeatured.Women.ExploreText)
		assert.Equal(t, "women", hero.NavFeatured.Women.FeaturedTag)
	})

	// Test RefreshHero
	t.Run("RefreshHero", func(t *testing.T) {
		err := heroStore.RefreshHero(ctx)
		require.NoError(t, err)

		// Verify the hero data is still accessible after refresh
		hero, err := heroStore.GetHero(ctx)
		require.NoError(t, err)
		require.NotNil(t, hero)
		assert.Len(t, hero.Entities, 2)
	})
}
