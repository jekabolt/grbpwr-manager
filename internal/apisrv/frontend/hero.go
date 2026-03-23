package frontend

import (
	"context"
	"log/slog"

	"github.com/jekabolt/grbpwr-manager/internal/cache"
	"github.com/jekabolt/grbpwr-manager/internal/dto"
	pb_frontend "github.com/jekabolt/grbpwr-manager/proto/gen/frontend"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func (s *Server) GetHero(ctx context.Context, req *pb_frontend.GetHeroRequest) (*pb_frontend.GetHeroResponse, error) {
	// Use cached hero for default language since GetHeroRequest doesn't have language field
	heroFull := cache.GetHero()

	h, err := dto.ConvertEntityHeroFullToCommonWithTranslations(heroFull)
	if err != nil {
		slog.Default().ErrorContext(ctx, "can't convert entity hero to pb hero",
			slog.String("err", err.Error()),
		)
		return nil, status.Errorf(codes.Internal, "can't convert entity hero to pb hero")
	}

	return &pb_frontend.GetHeroResponse{
		Hero: h,
		Dictionary: dto.ConvertToCommonDictionary(dto.Dict{
			Categories:                  cache.GetCategories(),
			Measurements:                cache.GetMeasurements(),
			OrderStatuses:               cache.GetOrderStatuses(),
			PaymentMethods:              cache.GetPaymentMethodsFilteredByIsProd(),
			ShipmentCarriers:            cache.GetShipmentCarriers(),
			Sizes:                       cache.GetSizes(),
			Collections:                 cache.GetCollections(),
			Genders:                     cache.GetGenders(),
			Languages:                   cache.GetLanguages(),
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
		}),
	}, nil
}
