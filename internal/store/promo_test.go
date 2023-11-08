package store

import (
	"context"
	"testing"
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/assert"
)

var promoFreeShip = &entity.PromoCodeInsert{
	Code:         "freeShip",
	FreeShipping: true,
	Discount:     decimal.NewFromInt(0),
	Expiration:   time.Now().Add(time.Hour * 24),
	Allowed:      true,
}

var promoSale = &entity.PromoCodeInsert{
	Code:         "10",
	FreeShipping: false,
	Discount:     decimal.NewFromInt(10),
	Expiration:   time.Now().Add(time.Hour * 24),
	Allowed:      true,
}

var promoDisabled = &entity.PromoCodeInsert{
	Code:         "disabled",
	FreeShipping: false,
	Discount:     decimal.NewFromInt(10),
	Expiration:   time.Now().Add(time.Hour * 24),
	Allowed:      false,
}

var promoExpired = &entity.PromoCodeInsert{
	Code:         "expired",
	FreeShipping: false,
	Discount:     decimal.NewFromInt(10),
	Expiration:   time.Now().Add(time.Hour * -24),
	Allowed:      false,
}

func TestPromo(t *testing.T) {

	db := newTestDB(t)
	ps := db.Promo()
	ctx := context.Background()

	t.Run("AddPromo", func(t *testing.T) {
		err := ps.AddPromo(ctx, promoFreeShip)
		assert.NoError(t, err)

		err = ps.AddPromo(ctx, promoSale)
		assert.NoError(t, err)

		err = ps.AddPromo(ctx, promoDisabled)
		assert.NoError(t, err)

		err = ps.AddPromo(ctx, promoExpired)
		assert.NoError(t, err)
	})

	t.Run("GetAllPromoCodes", func(t *testing.T) {
		promos, err := ps.ListPromos(ctx)
		assert.NoError(t, err)
		assert.Len(t, promos, 4)
	})

	t.Run("DeletePromoCode", func(t *testing.T) {
		err := ps.DeletePromoCode(ctx, promoExpired.Code)
		assert.NoError(t, err)

		promos, err := ps.ListPromos(ctx)
		assert.NoError(t, err)
		assert.Len(t, promos, 3)
	})

	t.Run("DisablePromoCode", func(t *testing.T) {
		err := ps.DisablePromoCode(ctx, promoFreeShip.Code)
		assert.NoError(t, err)

		promo, ok := db.cache.GetPromoByName(promoFreeShip.Code)
		assert.False(t, ok)
		assert.Equal(t, promo.Code, promoFreeShip.Code)
		assert.False(t, promo.Allowed)

	})

}
