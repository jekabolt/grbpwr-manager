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

	hi := entity.HeroInsert{
		ContentLink: "example.com/content-left",
		ContentType: "text/html",
		ExploreLink: "example.com/explore-left",
		ExploreText: "Explore more on left",
	}

	np, err := randomProductInsert(db, 1)
	assert.NoError(t, err)

	// Insert new product
	newPrd, err := ps.AddProduct(ctx, np)
	assert.NoError(t, err)

	err = hs.SetHero(ctx, hi, []int{newPrd.Product.ID})
	assert.NoError(t, err)

	hero, err := db.GetHero(ctx)
	assert.NoError(t, err)

	assert.Equal(t, hi.ContentLink, hero.ContentLink)
	assert.Equal(t, hi.ContentType, hero.ContentType)
	assert.Equal(t, hi.ExploreLink, hero.ExploreLink)
	assert.Equal(t, hi.ExploreText, hero.ExploreText)

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

	err = hs.SetHero(ctx, hi, prdIds)
	assert.NoError(t, err)

	hero, err = db.GetHero(ctx)
	assert.NoError(t, err)
	assert.Len(t, hero.ProductsFeatured, 11)

}
