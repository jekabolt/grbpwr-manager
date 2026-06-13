package cache

import (
	"sync"
	"testing"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
)

// TestCacheConcurrentAccess hammers the runtime-mutable dictionary and
// payment-method state from many goroutines at once. Run with -race: before the
// cacheMu guard was added this panicked with "concurrent map read and map write"
// (sizeById / paymentMethodsById). It must now complete cleanly.
func TestCacheConcurrentAccess(t *testing.T) {
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
				RefreshDictionary(dict(i))
				UpdatePaymentMethodAllowance(entity.CARD, i%2 == 0)
				RefreshEntityPaymentMethods()
				SetSiteAvailability(i%2 == 0)
				SetMaxOrderItems(i)
				SetOrderExpirationSeconds(i)
				UpdateHero(&entity.HeroFullWithTranslations{})
			}
		})
	}

	// Readers: read everything concurrently.
	for range goroutines {
		wg.Go(func() {
			for range iterations {
				_ = GetSizes()
				_, _ = GetSizeById(1)
				_ = GetCategories()
				_ = GetCollections()
				_, _ = GetPaymentMethodByName(entity.CARD)
				_, _ = GetPaymentMethodById(1)
				_ = GetPaymentMethods()
				_ = GetPaymentMethodsFilteredByIsProd()
				_ = GetSiteAvailability()
				_ = GetMaxOrderItems()
				_ = GetOrderExpirationSeconds()
				_ = GetHero()
			}
		})
	}

	wg.Wait()
}
