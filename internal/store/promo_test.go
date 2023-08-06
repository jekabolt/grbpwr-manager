package store

import (
	"context"
	"testing"
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/dto"
	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/assert"
)

var promoFreeShip = &dto.PromoCode{
	Code:         "freeShip",
	FreeShipping: true,
	Sale:         decimal.NewFromInt(0),
	Expiration:   time.Now().Add(time.Hour * 24),
	Allowed:      true,
}

var promoSale = &dto.PromoCode{
	Code:         "10",
	FreeShipping: false,
	Sale:         decimal.NewFromInt(10),
	Expiration:   time.Now().Add(time.Hour * 24),
	Allowed:      true,
}

var promoDisabled = &dto.PromoCode{
	Code:         "disabled",
	FreeShipping: false,
	Sale:         decimal.NewFromInt(10),
	Expiration:   time.Now().Add(time.Hour * 24),
	Allowed:      false,
}

var promoExpired = &dto.PromoCode{
	Code:         "expired",
	FreeShipping: false,
	Sale:         decimal.NewFromInt(10),
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
		promos, err := ps.GetAllPromoCodes(ctx)
		assert.NoError(t, err)
		assert.Len(t, promos, 4)
	})

	t.Run("DeletePromoCode", func(t *testing.T) {
		err := ps.DeletePromoCode(ctx, promoExpired.Code)
		assert.NoError(t, err)

		promos, err := ps.GetAllPromoCodes(ctx)
		assert.NoError(t, err)
		assert.Len(t, promos, 3)
	})

	t.Run("GetPromoByCode", func(t *testing.T) {
		promo, err := ps.GetPromoByCode(ctx, promoFreeShip.Code)
		assert.NoError(t, err)

		assert.Equal(t, promo.Code, promoFreeShip.Code)
		assert.Equal(t, promo.Allowed, promoFreeShip.Allowed)
		assert.Equal(t, promo.Expiration.Day(), promoFreeShip.Expiration.Day())
		assert.Equal(t, promo.Expiration.Month(), promoFreeShip.Expiration.Month())
		assert.Equal(t, promo.Expiration.Year(), promoFreeShip.Expiration.Year())
		assert.Equal(t, promo.FreeShipping, promoFreeShip.FreeShipping)
		assert.Equal(t, promo.Sale, promoFreeShip.Sale)
	})

	t.Run("DisablePromoCode", func(t *testing.T) {
		promo, err := ps.GetPromoByCode(ctx, promoFreeShip.Code)
		assert.NoError(t, err)

		err = ps.DisablePromoCode(ctx, promo.Code)
		assert.NoError(t, err)

		promo, err = ps.GetPromoByCode(ctx, promoFreeShip.Code)
		assert.NoError(t, err)
		assert.False(t, promo.Allowed)

	})

}
