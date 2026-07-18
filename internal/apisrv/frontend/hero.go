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
			Colors:                      cache.GetColors(),
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

// filterHeroForViewer returns a copy of the hero filtered for the requesting viewer. It resolves the
// viewer's un-spoofable tier (0 for guests) from the request, then delegates to heroForViewer.
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

	return heroForViewer(hero, authenticated, tier)
}

// heroForViewer returns a copy of the hero containing only the entities the viewer may see (per each
// entity's HeroAudience) AND, within every retained entity, only the embedded products the viewer is
// allowed to see. The two filters are independent: the audience filter is entity-level TARGETING
// (unchanged); the per-product filter is the leak-proofing this closes.
//
// The hero cache is built ONCE, tier-blind: buildHeroData loads embedded products via
// GetProductsByTag / GetProductsByIds / GetLowStockProducts, all of which gate on lifecycle_status
// only, never hidden_for_non_qualified. So a hidden, tier-gated product that merely shares a
// featured tag/id is embedded in the shared hero and would reach a non-qualifying viewer. This runs
// per-request against a copy; it MUST NOT mutate the shared cache (filterHeroEntityProducts copies
// every sub-struct/slice it rewrites).
func heroForViewer(hero *entity.HeroFullWithTranslations, authenticated bool, tier int16) *entity.HeroFullWithTranslations {
	out := &entity.HeroFullWithTranslations{
		NavFeatured: hero.NavFeatured,
		Entities:    make([]entity.HeroEntityWithTranslations, 0, len(hero.Entities)),
	}
	for _, e := range hero.Entities {
		if !heroEntityVisibleTo(e, authenticated, tier) {
			continue
		}
		filtered, keep := filterHeroEntityProducts(e, tier)
		if !keep {
			continue
		}
		out.Entities = append(out.Entities, filtered)
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

// heroVisibleProductsFor returns a NEW slice with every hidden_for_non_qualified colourway the viewer
// does not qualify to purchase removed — mirroring dto.storefrontColorwaysFromList. Non-hidden
// colourways, and gated colourways the viewer DOES qualify for, are retained (the latter as locked
// teasers). It never mutates or reorders the input slice.
func heroVisibleProductsFor(products []entity.Colorway, tier int16) []entity.Colorway {
	out := make([]entity.Colorway, 0, len(products))
	for i := range products {
		p := &products[i]
		if p.HiddenForNonQualified() && !entity.TierCanPurchase(tier, p.MinTier()) {
			continue
		}
		out = append(out, products[i])
	}
	return out
}

// filterHeroEntityProducts returns a copy of e whose embedded products have been filtered for the
// viewer's tier, plus whether the entity should be retained. Only product-carrying hero types are
// affected; every other type is returned unchanged (keep=true). Because the hero cache is shared and
// tier-blind, this copies any sub-struct/slice it rewrites and NEVER mutates the cached hero in
// place. A ProductSpotlight is a single-product block: if its one product is filtered out the whole
// block is dropped (keep=false) — an empty spotlight has nothing to show, and keeping its media could
// hint at the hidden product.
func filterHeroEntityProducts(e entity.HeroEntityWithTranslations, tier int16) (entity.HeroEntityWithTranslations, bool) {
	switch e.Type {
	case entity.HeroTypeFeaturedProducts:
		if e.FeaturedProducts != nil {
			cp := *e.FeaturedProducts
			cp.Products = heroVisibleProductsFor(cp.Products, tier)
			e.FeaturedProducts = &cp
		}
	case entity.HeroTypeFeaturedProductsTag:
		if e.FeaturedProductsTag != nil {
			cp := *e.FeaturedProductsTag
			cp.Products.Products = heroVisibleProductsFor(cp.Products.Products, tier)
			e.FeaturedProductsTag = &cp
		}
	case entity.HeroTypeSplit:
		if e.Split != nil {
			cp := *e.Split
			cp.Products = heroVisibleProductsFor(cp.Products, tier)
			e.Split = &cp
		}
	case entity.HeroTypeLastChance:
		if e.LastChance != nil {
			cp := *e.LastChance
			cp.Products = heroVisibleProductsFor(cp.Products, tier)
			e.LastChance = &cp
		}
	case entity.HeroTypeNewArrivals:
		if e.NewArrivals != nil {
			cp := *e.NewArrivals
			cp.Products = heroVisibleProductsFor(cp.Products, tier)
			e.NewArrivals = &cp
		}
	case entity.HeroTypeProductSpotlight:
		if e.ProductSpotlight != nil {
			p := &e.ProductSpotlight.Product
			if p.HiddenForNonQualified() && !entity.TierCanPurchase(tier, p.MinTier()) {
				return e, false
			}
		}
	}
	return e, true
}
