package admin

import (
	"context"
	"log/slog"

	"github.com/jekabolt/grbpwr-manager/internal/cache"
	"github.com/jekabolt/grbpwr-manager/internal/dto"
	"github.com/jekabolt/grbpwr-manager/internal/tenant"
	pb_admin "github.com/jekabolt/grbpwr-manager/proto/gen/admin"
)

// GetDictionary answers from the caller's team cache. Team #1 (and the storefront team) are kept
// fresh by the background revision poller, exactly as before teams; any other team is not polled,
// so its revisions are checked here against its own database (one small read) before answering.
func (s *Server) GetDictionary(ctx context.Context, _ *pb_admin.GetDictionaryRequest) (*pb_admin.GetDictionaryResponse, error) {
	if t, ok := tenant.From(ctx); ok && t.ID != tenant.TeamOneID {
		if _, err := cache.EnsureDictionaryFresh(ctx, s.repo.Dictionary(), s.repo.Cache()); err != nil {
			slog.Default().WarnContext(ctx, "dictionary freshness check failed", slog.String("err", err.Error()))
		}
	}
	return &pb_admin.GetDictionaryResponse{
		Dictionary: dto.ConvertToCommonDictionary(dto.Dict{
			Categories:                  cache.GetCategories(ctx),
			Measurements:                cache.GetMeasurements(),
			OrderStatuses:               cache.GetOrderStatuses(),
			PaymentMethods:              cache.GetPaymentMethodsFilteredByIsProd(ctx),
			ShipmentCarriers:            cache.GetShipmentCarriers(ctx),
			Sizes:                       cache.GetSizes(ctx),
			Collections:                 cache.GetCollections(ctx),
			Languages:                   cache.GetLanguages(),
			Genders:                     cache.GetGenders(),
			SortFactors:                 cache.GetSortFactors(),
			OrderFactors:                cache.GetOrderFactors(),
			SiteEnabled:                 cache.GetSiteAvailability(ctx),
			MaxOrderItems:               cache.GetMaxOrderItems(ctx),
			BaseCurrency:                cache.GetBaseCurrency(),
			BigMenu:                     cache.GetBigMenu(ctx),
			Announce:                    cache.GetAnnounce(ctx),
			OrderExpirationSeconds:      cache.GetOrderExpirationSeconds(ctx),
			ComplimentaryShippingPrices: cache.GetComplimentaryShippingPrices(ctx),
			IsProd:                      cache.GetPaymentIsProd(ctx),
			BackgroundHeroColor:         cache.GetBackgroundHeroColor(ctx),
			ProductTags:                 cache.GetProductTags(ctx),
			Tags:                        cache.GetTags(ctx),
			Colors:                      cache.GetColors(ctx),
			CategorySizeSystems:         cache.GetCategorySizeSystems(),
			Fibers:                      cache.GetFibers(ctx),
			CareSymbols:                 cache.GetCareSymbols(),
			ModelPhotoTechCardID:        cache.GetModelPhotoTechCardID(ctx),
		}),
	}, nil
}
