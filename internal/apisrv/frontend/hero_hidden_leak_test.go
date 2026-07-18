package frontend

import (
	"context"
	"fmt"
	"math/rand"
	"testing"
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/stretchr/testify/require"
)

// findTagBlock returns the FeaturedProductsTag block for `tag`, or nil.
func findTagBlock(h *entity.HeroFullWithTranslations, tag string) *entity.HeroFeaturedProductsTagWithTranslations {
	for i := range h.Entities {
		e := h.Entities[i]
		if e.Type == entity.HeroTypeFeaturedProductsTag && e.FeaturedProductsTag != nil && e.FeaturedProductsTag.Tag == tag {
			return e.FeaturedProductsTag
		}
	}
	return nil
}

func containsSKU(prds []entity.Colorway, sku string) bool {
	for i := range prds {
		if prds[i].SKU == sku {
			return true
		}
	}
	return false
}

// TestHeroHiddenProductLeak is the regression guard for the hero hidden-product leak. An admin
// FeaturedProductsTag block (Audience=ALL) auto-pulls every ACTIVE product sharing its tag via the
// tier-blind GetProductsByTag, so a hidden_for_non_qualified=TRUE, min_tier=99 product that merely
// shares the tag is embedded in the shared, tier-blind hero cache. Serving that hero must filter the
// embedded products per-viewer: the hidden min_tier=99 product must NOT reach a guest (tier 0) or a
// plus (tier 1) viewer, but MUST reach a hacker (tier 99). Removing the per-product filter in
// heroForViewer makes the guest/plus assertions fail (the leak), which is what this guards.
func TestHeroHiddenProductLeak(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	st, db := tierGateBackends(t)

	token := fmt.Sprintf("%d%04d", time.Now().UnixNano(), rand.Intn(10000))
	tag := "HEROLEAK-" + token
	skuNormal := "HL0-" + token
	skuHidden := "HLH-" + token

	mediaID := seedTestMedia(ctx, t, st)
	sizeID := seedTestSize(ctx, t, db)

	// P0: normal (min_tier=0). PH: hidden_for_non_qualified=TRUE, min_tier=99. Both ACTIVE, both tagged.
	seedTierProduct(ctx, t, db, mediaID, sizeID, skuNormal, 0, false, tag)
	seedTierProduct(ctx, t, db, mediaID, sizeID, skuHidden, 99, true, tag)

	// Store an ALL-audience FeaturedProductsTag hero block on `tag`, then resolve it (tier-blind, shared).
	hfi := entity.HeroFullInsert{
		Entities: []entity.HeroEntityInsert{{
			Type:                entity.HeroTypeFeaturedProductsTag,
			FeaturedProductsTag: entity.HeroFeaturedProductsTagInsert{Tag: tag},
			Audience:            entity.HeroAudienceAll,
		}},
	}
	require.NoError(t, st.Hero().SetHero(ctx, hfi))
	t.Cleanup(func() { _, _ = db.ExecContext(context.Background(), "DELETE FROM hero") })

	resolved, err := st.Hero().GetHero(ctx)
	require.NoError(t, err)

	shared := findTagBlock(resolved, tag)
	require.NotNil(t, shared, "featured-products-tag block must resolve")

	// Leak vector: the tier-blind resolution embeds BOTH products, including the hidden one.
	require.True(t, containsSKU(shared.Products.Products, skuNormal), "resolved hero must embed the normal product")
	require.True(t, containsSKU(shared.Products.Products, skuHidden),
		"resolved (tier-blind) hero DOES embed the hidden min_tier=99 product — this is the leak source the per-viewer filter must close")

	// GUEST (tier 0): hidden product dropped, normal product retained.
	guest := findTagBlock(heroForViewer(resolved, false, entity.TierCodeMember), tag)
	require.NotNil(t, guest)
	require.False(t, containsSKU(guest.Products.Products, skuHidden),
		"hidden min_tier=99 product MUST NOT be served to a guest")
	require.True(t, containsSKU(guest.Products.Products, skuNormal),
		"the normal product must still be served to a guest")

	// PLUS (tier 1): still does not qualify for min_tier=99, so the hidden product stays dropped.
	plus := findTagBlock(heroForViewer(resolved, true, entity.TierCodePlus), tag)
	require.NotNil(t, plus)
	require.False(t, containsSKU(plus.Products.Products, skuHidden),
		"hidden min_tier=99 product MUST NOT be served to a plus viewer (does not qualify)")
	require.True(t, containsSKU(plus.Products.Products, skuNormal))

	// HACKER (tier 99): qualifies for min_tier=99, so the hidden product IS served.
	hacker := findTagBlock(heroForViewer(resolved, true, entity.TierCodeHacker), tag)
	require.NotNil(t, hacker)
	require.True(t, containsSKU(hacker.Products.Products, skuHidden),
		"hidden min_tier=99 product IS served to a hacker (qualifies)")

	// Per-viewer filtering must never mutate the shared, cached hero: the hidden product is still there.
	require.True(t, containsSKU(shared.Products.Products, skuHidden),
		"per-viewer filtering must not mutate the shared resolved/cached hero")
	require.Len(t, shared.Products.Products, 2, "shared hero block must retain both products after filtering copies")
}
