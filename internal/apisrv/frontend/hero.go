package frontend

import (
	"context"
	"log/slog"

	"github.com/jekabolt/grbpwr-manager/internal/cache"
	"github.com/jekabolt/grbpwr-manager/internal/dto"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	pb_frontend "github.com/jekabolt/grbpwr-manager/proto/gen/frontend"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func (s *Server) GetHero(ctx context.Context, req *pb_frontend.GetHeroRequest) (*pb_frontend.GetHeroResponse, error) {
	// Cached hero is the shared, resolved default-language hero. Filter a copy of
	// it for the requesting viewer (TARGETING modifier); never mutate the cache.
	heroFull := s.filterHeroForViewer(ctx, cache.GetHero())

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
			BackgroundHeroColor:         cache.GetBackgroundHeroColor(),
			ProductTags:                 cache.GetProductTags(),
		}),
	}, nil
}

// filterHeroForViewer returns a copy of the hero containing only the entities
// the requesting viewer is allowed to see, per each entity's HeroAudience.
func (s *Server) filterHeroForViewer(ctx context.Context, hero *entity.HeroFullWithTranslations) *entity.HeroFullWithTranslations {
	if hero == nil {
		return nil
	}

	email, err := s.storefrontEmailFromAccess(ctx)
	authenticated := err == nil && email != ""
	var tier int16 = entity.TierCodeMember
	if authenticated {
		tier = s.viewerTier(ctx)
	}

	out := &entity.HeroFullWithTranslations{
		NavFeatured: hero.NavFeatured,
		Entities:    make([]entity.HeroEntityWithTranslations, 0, len(hero.Entities)),
	}
	for _, e := range hero.Entities {
		if heroEntityVisibleTo(e, authenticated, tier) {
			out.Entities = append(out.Entities, e)
		}
	}
	return out
}

// heroEntityVisibleTo applies the TARGETING modifier for a single entity.
func heroEntityVisibleTo(e entity.HeroEntityWithTranslations, authenticated bool, tier int16) bool {
	switch e.Audience {
	case entity.HeroAudienceGuests:
		return !authenticated
	case entity.HeroAudienceMembers:
		return authenticated
	case entity.HeroAudienceTier:
		return authenticated && int(tier) >= e.MinTierId
	default: // HeroAudienceUnknown / HeroAudienceAll
		return true
	}
}
