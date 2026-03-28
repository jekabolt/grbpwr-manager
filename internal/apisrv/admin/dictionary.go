package admin

import (
	"context"

	"github.com/jekabolt/grbpwr-manager/internal/cache"
	"github.com/jekabolt/grbpwr-manager/internal/dto"
	pb_admin "github.com/jekabolt/grbpwr-manager/proto/gen/admin"
)

func (s *Server) GetDictionary(context.Context, *pb_admin.GetDictionaryRequest) (*pb_admin.GetDictionaryResponse, error) {
	return &pb_admin.GetDictionaryResponse{
		Dictionary: dto.ConvertToCommonDictionary(dto.Dict{
			Categories:                  cache.GetCategories(),
			Measurements:                cache.GetMeasurements(),
			OrderStatuses:               cache.GetOrderStatuses(),
			PaymentMethods:              cache.GetPaymentMethodsFilteredByIsProd(),
			ShipmentCarriers:            cache.GetShipmentCarriers(),
			Sizes:                       cache.GetSizes(),
			Collections:                 cache.GetCollections(),
			Languages:                   cache.GetLanguages(),
			Genders:                     cache.GetGenders(),
			SortFactors:                 cache.GetSortFactors(),
			OrderFactors:                cache.GetOrderFactors(),
			SiteEnabled:                 cache.GetSiteAvailability(),
			MaxOrderItems:               cache.GetMaxOrderItems(),
			BaseCurrency:                cache.GetBaseCurrency(),
			BigMenu:                     cache.GetBigMenu(),
			Announce:                    cache.GetAnnounce(),
			OrderExpirationSeconds:      cache.GetOrderExpirationSeconds(),
			ComplimentaryShippingPrices: cache.GetComplimentaryShippingPrices(),
			IsProd:                      cache.GetPaymentIsProd(),
			BackgroundHeroColor:         cache.GetBackgroundHeroColor(),
		}),
	}, nil
}
