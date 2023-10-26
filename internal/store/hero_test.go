package store

// import (
// 	"context"
// 	"testing"

// 	"github.com/jekabolt/grbpwr-manager/internal/dto"
// 	"github.com/stretchr/testify/assert"
// )

// func TestSetHero(t *testing.T) {
// 	db := newTestDB(t)
// 	defer db.Close()

// 	left := dto.HeroElement{
// 		ContentLink: "example.com/content-left",
// 		ContentType: "text/html",
// 		ExploreLink: "example.com/explore-left",
// 		ExploreText: "Explore more on left",
// 	}

// 	right := dto.HeroElement{
// 		ContentLink: "example.com/content-right",
// 		ContentType: "text/html",
// 		ExploreLink: "example.com/explore-right",
// 		ExploreText: "Explore more on right",
// 	}

// 	err := db.SetHero(context.Background(), left, right)
// 	assert.NoError(t, err)

// 	hero, err := db.GetHero(context.Background())
// 	assert.NoError(t, err)

// 	assert.Equal(t, left.ContentLink, hero.HeroLeft.ContentLink)
// 	assert.Equal(t, left.ContentType, hero.HeroLeft.ContentType)
// 	assert.Equal(t, left.ExploreLink, hero.HeroLeft.ExploreLink)
// 	assert.Equal(t, left.ExploreText, hero.HeroLeft.ExploreText)

// 	assert.Equal(t, right.ContentLink, hero.HeroRight.ContentLink)
// 	assert.Equal(t, right.ContentType, hero.HeroRight.ContentType)
// 	assert.Equal(t, right.ExploreLink, hero.HeroRight.ExploreLink)
// 	assert.Equal(t, right.ExploreText, hero.HeroRight.ExploreText)
// }
