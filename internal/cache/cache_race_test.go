package cache

import (
	"context"
	"sync"
	"testing"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
)

// TestCacheConcurrentAccess hammers the runtime-mutable dictionary and
// payment-method state from many goroutines at once. Run with -race: before the
// cacheMu guard was added this panicked with "concurrent map read and map write"
// (sizeById / paymentMethodsById). It must now complete cleanly.
func TestCacheConcurrentAccess(t *testing.T) {
	resetForTest(t)
	ctx := context.Background()
	const goroutines = 16
	const iterations = 500

	dict := func(n int) *entity.DictionaryInfo {
		return &entity.DictionaryInfo{
			Categories:  []entity.Category{},
			Collections: []entity.Collection{},
			Sizes: []entity.Size{
				{Id: 1, Name: "S"},
				{Id: 2, Name: "M"},
				{Id: n%3 + 3, Name: "L"},
			},
		}
	}

	var wg sync.WaitGroup

	// Writers: replace the dictionary maps/slices.
	for range goroutines {
		wg.Go(func() {
			for i := range iterations {
				RefreshDictionary(ctx, dict(i))
				UpdatePaymentMethodAllowance(ctx, entity.CARD, i%2 == 0)
				RefreshEntityPaymentMethods(ctx)
				_ = SetSiteAvailability(ctx, i%2 == 0)
				_ = SetMaxOrderItems(ctx, i)
				_ = SetOrderExpirationSeconds(ctx, i)
				UpdateHero(ctx, &entity.HeroFullWithTranslations{})
			}
		})
	}

	// Readers: read everything concurrently.
	for range goroutines {
		wg.Go(func() {
			for range iterations {
				_ = GetSizes(ctx)
				_, _ = GetSizeById(1)
				_ = GetCategories(ctx)
				_ = GetCollections(ctx)
				_, _ = GetPaymentMethodByName(ctx, entity.CARD)
				_, _ = GetPaymentMethodById(ctx, 1)
				_ = GetPaymentMethods(ctx)
				_ = GetPaymentMethodsFilteredByIsProd(ctx)
				_ = GetSiteAvailability(ctx)
				_ = GetMaxOrderItems(ctx)
				_ = GetOrderExpirationSeconds(ctx)
				_ = GetHero(ctx)
			}
		})
	}

	wg.Wait()
}
