package store

import (
	"context"
	"testing"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/stretchr/testify/assert"
)

func TestSetHero(t *testing.T) {
	db := newTestDB(t)

	ps := db.Products()
	hs := db.Hero()
	ctx := context.Background()

	main := entity.HeroInsert{
		ContentLink: "example.com/content-left",
		ContentType: "text/html",
		ExploreLink: "example.com/explore-left",
		ExploreText: "Explore more on left",
	}

	ads := []entity.HeroInsert{
		{
			ContentLink: "example.com/content-1",
			ContentType: "text/html",
			ExploreLink: "example.com/explore-1",
			ExploreText: "Explore more on 1",
		},
		{
			ContentLink: "example.com/content-2",
			ContentType: "text/html",
			ExploreLink: "example.com/explore-2",
			ExploreText: "Explore more on 2",
		},
	}

	np, err := randomProductInsert(db, 1)
	assert.NoError(t, err)

	// Insert new product
	newPrd, err := ps.AddProduct(ctx, np)
	assert.NoError(t, err)

	err = hs.SetHero(ctx, main, ads, []int{newPrd.Product.ID})
	assert.NoError(t, err)

	hero, err := db.GetHero(ctx)
	assert.NoError(t, err)

	assert.Equal(t, hero.Main.ContentLink, main.ContentLink)
	assert.Equal(t, hero.Main.ContentType, main.ContentType)
	assert.Equal(t, hero.Main.ExploreLink, main.ExploreLink)
	assert.Equal(t, hero.Main.ExploreText, main.ExploreText)

	assert.Len(t, hero.ProductsFeatured, 1)

	prdIds := []int{newPrd.Product.ID}
	for i := 0; i < 10; i++ {
		np, err := randomProductInsert(db, i)
		assert.NoError(t, err)

		// Insert new product
		newPrd, err := ps.AddProduct(ctx, np)
		assert.NoError(t, err)

		prdIds = append(prdIds, newPrd.Product.ID)
	}

	err = hs.SetHero(ctx, main, ads, prdIds)
	assert.NoError(t, err)

	hero, err = db.GetHero(ctx)
	assert.NoError(t, err)
	assert.Len(t, hero.ProductsFeatured, 11)

}
