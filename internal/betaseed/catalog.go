package betaseed

import (
	"context"
	"fmt"
	"time"

	decimal "google.golang.org/genproto/googleapis/type/decimal"

	admin "github.com/jekabolt/grbpwr-manager/proto/gen/admin"
	common "github.com/jekabolt/grbpwr-manager/proto/gen/common"
	frontend "github.com/jekabolt/grbpwr-manager/proto/gen/frontend"
)

// VariantResult identifies one published variant of a seeded colourway.
type VariantResult struct {
	VariantID int32  // stable internal variant id (product_size.id); admin custom orders + stock address this
	Sku       string // public variant SKU minted by publish (e.g. SS26-00021-BLK-04)
	SizeID    int32
	SizeName  string
}

// CatalogResult is the published-product handle downstream phases (orders/analytics)
// reference. One per seeded style.
type CatalogResult struct {
	Index       int
	StyleID     int32
	StyleNumber string
	ColorwayID  int32
	BaseSku     string
	Slug        string
	Status      string // ColorwayLifecycleStatus enum name; expect COLORWAY_LIFECYCLE_STATUS_ACTIVE
	ColorCode   string
	MediaIDs    []int32 // the 3 media uploaded for this style (thumb, secondary, extra)
	Variants    []VariantResult
}

// SeedCatalog runs the full catalog + storefront flow count(Vol) times, then adds the
// singleton hero, one archive, one storefront order and one admin custom order overall.
// Returns a CatalogResult per published style. Idempotent: each run mints fresh
// style_numbers (Run+index) so re-running never collides.
func (s *Seeder) SeedCatalog(ctx context.Context) ([]CatalogResult, error) {
	mID, err := s.SizeIDByName("m")
	if err != nil {
		return nil, err
	}
	lID, err := s.SizeIDByName("l")
	if err != nil {
		return nil, err
	}
	top, sub, typ, leaf, err := s.CategoryChain()
	if err != nil {
		return nil, err
	}
	meas, err := s.MeasurementIDs(2)
	if err != nil {
		return nil, err
	}
	country := s.CountryCode()
	colorCode, _, err := s.ColorByCode("BLK")
	if err != nil {
		return nil, err
	}

	n := count(s.Vol)
	s.logf("SeedCatalog: volume=%d styles, run=%s, lang=%d, color=%s, country=%s, categories(top=%d sub=%d type=%d leaf=%d), sizes(m=%d l=%d)",
		n, s.Run, s.LangID, colorCode, country, top, sub, typ, leaf, mID, lID)

	results := make([]CatalogResult, 0, n)
	for i := 0; i < n; i++ {
		res, err := s.seedOneStyle(ctx, i, styleParams{
			mID: mID, lID: lID, top: top, sub: sub, typ: typ, leaf: leaf,
			meas: meas, country: country, colorCode: colorCode,
		})
		if err != nil {
			return results, fmt.Errorf("style %d/%d: %w", i+1, n, err)
		}
		results = append(results, res)
		s.logf("  [style %d/%d] style_id=%d colorway_id=%d base_sku=%s status=%s variants=m:%s l:%s",
			i+1, n, res.StyleID, res.ColorwayID, res.BaseSku, res.Status,
			res.Variants[0].Sku, res.Variants[1].Sku)
	}
	if len(results) == 0 {
		return nil, fmt.Errorf("no styles seeded")
	}

	if err := s.seedHero(ctx, results[0]); err != nil {
		return results, fmt.Errorf("hero: %w", err)
	}
	if err := s.seedArchive(ctx, results[0]); err != nil {
		return results, fmt.Errorf("archive: %w", err)
	}
	if err := s.seedOrders(ctx, results[0]); err != nil {
		return results, fmt.Errorf("orders: %w", err)
	}
	return results, nil
}

type styleParams struct {
	mID, lID            int32
	top, sub, typ, leaf int32
	meas                []int32
	country, colorCode  string
}

// seedOneStyle drives steps 2-7S for a single style: media → tech card → style facts →
// colourway (DRAFT) → 2 variants → size chart → publish (ACTIVE) → stock (post-publish).
func (s *Seeder) seedOneStyle(ctx context.Context, idx int, p styleParams) (CatalogResult, error) {
	var zero CatalogResult
	styleNumber := fmt.Sprintf("BSEED-%s-%02d", s.Run, idx+1)

	// [2] media: 3 real JPEGs.
	media := make([]int32, 0, 3)
	for j := 0; j < 3; j++ {
		id, err := s.UploadJPEG(ctx, fmt.Sprintf("%s-%d", styleNumber, j))
		if err != nil {
			return zero, err
		}
		media = append(media, id)
	}

	// [3] style: CreateTechCard.
	tcResp, err := s.C.CreateTechCard(ctx, &admin.CreateTechCardRequest{
		TechCard: &common.TechCardInsert{
			StyleNumber:  styleNumber,
			Name:         fmt.Sprintf("Beta Seed %s", styleNumber),
			Brand:        "grbpwr",
			Collection:   "beta-seed-collection",
			CategoryId:   p.leaf,
			TargetGender: common.GenderEnum_GENDER_ENUM_UNISEX,
			SkuSeason:    &common.SkuSeason{Code: common.SeasonEnum_SEASON_ENUM_SS, Year: 2026},
			SizeIds:      []int32{p.mID, p.lID},
		},
	})
	if err != nil {
		return zero, fmt.Errorf("CreateTechCard: %w", err)
	}
	styleID := tcResp.GetId()
	if styleID == 0 {
		return zero, fmt.Errorf("CreateTechCard returned style_id=0")
	}

	// [3b] UpdateStyle: seed categories + merch facts (top_category_id must be non-null
	// or the admin projection / publish response 500s).
	lv, err := s.lockVersion(ctx, styleID)
	if err != nil {
		return zero, err
	}
	if _, err := s.C.UpdateStyle(ctx, &admin.UpdateStyleRequest{
		StyleId:             int64(styleID),
		ExpectedLockVersion: lv,
		Patch: &admin.StylePatch{
			Brand:              "grbpwr",
			Season:             common.SeasonEnum_SEASON_ENUM_SS,
			Collection:         "beta-seed-collection",
			TargetGender:       common.GenderEnum_GENDER_ENUM_UNISEX,
			Fit:                "regular",
			Composition:        `["100% cotton"]`,
			CareInstructions:   "Machine wash cold at 30",
			ModelWearsHeightCm: 180,
			ModelWearsSizeId:   p.mID,
			TopCategoryId:      p.top,
			SubCategoryId:      p.sub,
			TypeId:             p.typ,
		},
	}); err != nil {
		return zero, fmt.Errorf("UpdateStyle: %w", err)
	}

	// [4] colourway (DRAFT) with ALL required-currency prices (PLN fix).
	cwResp, err := s.C.CreateColorway(ctx, &admin.CreateColorwayRequest{
		StyleId: styleID,
		Merchandising: &common.ColorwayMerchandisingInsert{
			ColorCode:   p.colorCode,
			CountryCode: p.country,
			MinTier:     0,
		},
		ThumbnailMediaId:          media[0],
		SecondaryThumbnailMediaId: media[1],
		MediaIds:                  media,
		Tags:                      []*common.ColorwayTagInsert{{Tag: seedTag}},
		Prices:                    s.Prices(),
		Translations: []*common.ColorwayInsertTranslation{{
			LanguageId:  s.LangID,
			Name:        fmt.Sprintf("Beta Seed %s %s", styleNumber, p.colorCode),
			Description: "Seed colourway for the beta environment.",
		}},
		CountryCode: p.country,
	})
	if err != nil {
		return zero, fmt.Errorf("CreateColorway: %w", err)
	}
	colorwayID := cwResp.GetColorwayId()
	if colorwayID == 0 {
		return zero, fmt.Errorf("CreateColorway returned colorway_id=0")
	}

	// [5] variants (m, l). SKUs stay NULL until publish mints them.
	varM, err := s.C.CreateVariant(ctx, &admin.CreateVariantRequest{ColorwayId: int64(colorwayID), SizeId: p.mID})
	if err != nil {
		return zero, fmt.Errorf("CreateVariant(m): %w", err)
	}
	varL, err := s.C.CreateVariant(ctx, &admin.CreateVariantRequest{ColorwayId: int64(colorwayID), SizeId: p.lID})
	if err != nil {
		return zero, fmt.Errorf("CreateVariant(l): %w", err)
	}
	varMID := varM.GetVariant().GetVariantId()
	varLID := varL.GetVariant().GetVariantId()

	// [6] size chart (full replace).
	lv, err = s.lockVersion(ctx, styleID)
	if err != nil {
		return zero, err
	}
	if _, err := s.C.UpdateStyleSizeChart(ctx, &admin.UpdateStyleSizeChartRequest{
		StyleId:             styleID,
		ExpectedLockVersion: lv,
		Cells: []*common.StyleSizeChartCell{
			{SizeId: p.mID, MeasurementNameId: p.meas[0], Value: &decimal.Decimal{Value: "50"}},
			{SizeId: p.lID, MeasurementNameId: p.meas[0], Value: &decimal.Decimal{Value: "54"}},
			{SizeId: p.mID, MeasurementNameId: p.meas[1], Value: &decimal.Decimal{Value: "48"}},
			{SizeId: p.lID, MeasurementNameId: p.meas[1], Value: &decimal.Decimal{Value: "52"}},
		},
	}); err != nil {
		return zero, fmt.Errorf("UpdateStyleSizeChart: %w", err)
	}

	// [7] publish → ACTIVE; mints base + variant SKUs itself.
	lv, err = s.lockVersion(ctx, styleID)
	if err != nil {
		return zero, err
	}
	pubResp, err := s.C.PublishColorway(ctx, &admin.PublishColorwayRequest{
		ColorwayId:      colorwayID,
		ExpectedVersion: lv,
	})
	if err != nil {
		return zero, fmt.Errorf("PublishColorway: %w", err)
	}
	pub := pubResp.GetColorway()
	baseSku := pub.GetBaseSku()
	slug := pub.GetSlug()
	status := pub.GetStatus().String()
	if status != common.ColorwayLifecycleStatus_COLORWAY_LIFECYCLE_STATUS_ACTIVE.String() {
		return zero, fmt.Errorf("PublishColorway: colourway not ACTIVE (status=%s base_sku=%q)", status, baseSku)
	}

	// Read back the minted variant SKUs (publish alone must have minted them).
	full, err := s.C.GetColorwayByID(ctx, &admin.GetColorwayByIDRequest{ColorwayId: colorwayID})
	if err != nil {
		return zero, fmt.Errorf("GetColorwayByID post-publish: %w", err)
	}
	mSku := variantSKU(full.GetColorway().GetVariants(), varMID)
	lSku := variantSKU(full.GetColorway().GetVariants(), varLID)
	if mSku == "" || lSku == "" {
		return zero, fmt.Errorf("variant SKUs still NULL after publish (m=%q l=%q)", mSku, lSku)
	}

	// [7S] stock: set=5 per variant — MUST run after publish (ensureVariantSKU needs a base SKU).
	for _, vid := range []int32{varMID, varLID} {
		if _, err := s.C.UpdateVariantStock(ctx, &admin.UpdateVariantStockRequest{
			Mode:      common.StockAdjustmentMode_STOCK_ADJUSTMENT_MODE_SET,
			Quantity:  5,
			Reason:    common.StockChangeReason_STOCK_CHANGE_REASON_STOCK_COUNT,
			VariantId: int64(vid),
		}); err != nil {
			return zero, fmt.Errorf("UpdateVariantStock(variant=%d): %w", vid, err)
		}
	}

	return CatalogResult{
		Index:       idx,
		StyleID:     styleID,
		StyleNumber: styleNumber,
		ColorwayID:  colorwayID,
		BaseSku:     baseSku,
		Slug:        slug,
		Status:      status,
		ColorCode:   p.colorCode,
		MediaIDs:    media,
		Variants: []VariantResult{
			{VariantID: varMID, Sku: mSku, SizeID: p.mID, SizeName: "m"},
			{VariantID: varLID, Sku: lSku, SizeID: p.lID, SizeName: "l"},
		},
	}, nil
}

// variantSKU finds the minted SKU for variantID within vs.
func variantSKU(vs []*common.Variant, variantID int32) string {
	for _, v := range vs {
		if v.GetVariantId() == variantID {
			return v.GetVariantSku()
		}
	}
	return ""
}

// seedHero sets the singleton hero: MAIN + FEATURED_PRODUCTS + nav_featured(men/women).
// featured_products.product_ids carries COLOURWAY ids (R1 legacy naming); nav featured_tag
// references the seed tag every colourway carries.
func (s *Seeder) seedHero(ctx context.Context, r CatalogResult) error {
	portrait, landscape, nav := r.MediaIDs[0], r.MediaIDs[1], r.MediaIDs[2]
	_, err := s.C.AddHero(ctx, &admin.AddHeroRequest{
		Hero: &common.HeroFullInsert{
			Entities: []*common.HeroEntityInsert{
				{
					Type: common.HeroType_HERO_TYPE_MAIN,
					Main: &common.HeroMainInsert{
						Media:       &common.HeroMedia{PortraitId: portrait, LandscapeId: landscape},
						ExploreLink: "/catalog",
						Translations: []*common.HeroCopyTranslation{{
							LanguageId:  s.LangID,
							Tag:         "seed",
							Headline:    "Beta Seed Hero",
							Body:        "Seed hero block for the beta environment.",
							ExploreText: "Explore",
						}},
					},
				},
				{
					Type: common.HeroType_HERO_TYPE_FEATURED_PRODUCTS,
					FeaturedProducts: &common.HeroFeaturedProductsInsert{
						ProductIds:  []int32{r.ColorwayID},
						ExploreLink: "/catalog",
						Translations: []*common.HeroCopyTranslation{{
							LanguageId:  s.LangID,
							Headline:    "Featured",
							ExploreText: "Shop the drop",
						}},
					},
				},
			},
			NavFeatured: &common.NavFeaturedInsert{
				Men: &common.NavFeaturedEntityInsert{
					MediaId:      nav,
					FeaturedTag:  seedTag,
					Translations: []*common.NavFeaturedEntityInsertTranslation{{LanguageId: s.LangID, ExploreText: "Shop men"}},
				},
				Women: &common.NavFeaturedEntityInsert{
					MediaId:      nav,
					FeaturedTag:  seedTag,
					Translations: []*common.NavFeaturedEntityInsertTranslation{{LanguageId: s.LangID, ExploreText: "Shop women"}},
				},
			},
		},
	})
	if err != nil {
		return fmt.Errorf("AddHero: %w", err)
	}
	s.logf("  [hero] set (MAIN + FEATURED_PRODUCTS[colorway=%d] + nav_featured tag=%s)", r.ColorwayID, seedTag)
	return nil
}

// seedArchive adds one archive with a MAIN_MEDIA block and a PRODUCT block linked to the
// seeded colourway, then resolves its public AR.. code.
func (s *Seeder) seedArchive(ctx context.Context, r CatalogResult) error {
	resp, err := s.C.AddArchive(ctx, &admin.AddArchiveRequest{
		ArchiveInsert: &common.ArchiveInsert{
			Tag:          "beta-seed-archive",
			ThumbnailId:  r.MediaIDs[2],
			Translations: []*common.ArchiveInsertTranslation{{LanguageId: s.LangID, Heading: "Beta Seed Archive"}},
			Items: []*common.ArchiveItemInsert{
				{
					Type:      common.ArchiveItemType_ARCHIVE_ITEM_TYPE_MAIN_MEDIA,
					MainMedia: &common.ArchiveMainMediaInsert{MediaId: r.MediaIDs[0], AspectRatio: common.ArchiveMediaAspectRatio_ARCHIVE_MEDIA_ASPECT_RATIO_16X9},
				},
				{
					Type: common.ArchiveItemType_ARCHIVE_ITEM_TYPE_PRODUCT,
					Product: &common.ArchiveProductInsert{
						ColorwayId:   r.ColorwayID,
						Translations: []*common.ArchiveItemTranslation{{LanguageId: s.LangID, Caption: "Seed product"}},
					},
				},
			},
		},
	})
	if err != nil {
		return fmt.Errorf("AddArchive: %w", err)
	}
	archiveID := resp.GetId()
	if archiveID == 0 {
		return fmt.Errorf("AddArchive returned id=0")
	}
	code := ""
	if a, err := s.C.GetArchiveByID(ctx, &admin.GetArchiveByIDRequest{Id: archiveID}); err == nil {
		code = a.GetArchive().GetArchiveList().GetCode()
	}
	s.logf("  [archive] id=%d code=%s", archiveID, code)
	return nil
}

// seedOrders places one storefront customer order (card-test → AWAITING_PAYMENT) and one
// admin custom order (bank invoice, addressing the variant by internal id).
func (s *Seeder) seedOrders(ctx context.Context, r CatalogResult) error {
	carrier := s.carrierID()
	email := fmt.Sprintf("beta-seed-%d@grbpwr.com", time.Now().UnixNano())
	mSku := r.Variants[0].Sku
	lVariantID := r.Variants[1].VariantID

	// [10] storefront order: validate (mints PaymentIntent) → submit. Try CARD_TEST then CARD.
	var orderUUID string
	for _, pm := range []common.PaymentMethodNameEnum{
		common.PaymentMethodNameEnum_PAYMENT_METHOD_NAME_ENUM_CARD_TEST,
		common.PaymentMethodNameEnum_PAYMENT_METHOD_NAME_ENUM_CARD,
	} {
		vr, err := s.C.SFValidateOrderItemsInsert(ctx, &frontend.ValidateOrderItemsInsertRequest{
			Items:             []*common.OrderItemInsert{{Quantity: 1, VariantSku: mSku}},
			ShipmentCarrierId: carrier,
			Country:           "DE",
			PaymentMethod:     pm,
			Currency:          "eur",
		})
		if err != nil {
			s.logf("  [order] validate-items(%s) failed: %v", pm.String(), err)
			continue
		}
		piid := vr.GetPaymentIntentId()
		if piid == "" {
			s.logf("  [order] validate-items(%s): no paymentIntentId", pm.String())
			continue
		}
		sr, err := s.C.SFSubmitOrder(ctx, &frontend.SubmitOrderRequest{
			Order: &common.OrderNew{
				Items:             []*common.OrderItemInsert{{Quantity: 1, VariantSku: mSku}},
				ShippingAddress:   seedAddress(),
				BillingAddress:    seedAddress(),
				Buyer:             &common.BuyerInsert{FirstName: "Beta", LastName: "Seed", Email: email, Phone: "+49301234567"},
				PaymentMethod:     pm,
				ShipmentCarrierId: carrier,
				Currency:          "eur",
			},
			PaymentIntentId: piid,
		})
		if err != nil {
			s.logf("  [order] submit(%s) failed: %v", pm.String(), err)
			continue
		}
		orderUUID = sr.GetOrderUuid()
		s.logf("  [order] uuid=%s status=%s payment=%s", orderUUID, sr.GetOrderStatus().String(), pm.String())
		break
	}
	if orderUUID == "" {
		return fmt.Errorf("storefront order failed for both CARD_TEST and CARD")
	}

	// [11] admin custom order via internal variant id (bank invoice; custom_price required).
	cr, err := s.C.CreateCustomOrder(ctx, &admin.CreateCustomOrderRequest{
		Items: []*common.CustomOrderItemInsert{{
			Quantity:    1,
			VariantId:   int64(lVariantID),
			CustomPrice: &decimal.Decimal{Value: "99.00"},
		}},
		ShippingAddress:   seedAddress(),
		BillingAddress:    seedAddress(),
		Buyer:             &common.BuyerInsert{FirstName: "Beta", LastName: "Custom", Email: email, Phone: "+49301234599"},
		PaymentMethod:     common.PaymentMethodNameEnum_PAYMENT_METHOD_NAME_ENUM_BANK_INVOICE,
		ShipmentCarrierId: carrier,
		Currency:          "eur",
	})
	if err != nil {
		return fmt.Errorf("CreateCustomOrder: %w", err)
	}
	s.logf("  [custom order] uuid=%s (variant_id=%d, bank_invoice, custom_price=99.00 eur)", cr.GetOrder().GetUuid(), lVariantID)
	return nil
}

func seedAddress() *common.AddressInsert {
	return &common.AddressInsert{
		Country:        "DE",
		State:          "Berlin",
		City:           "Berlin",
		AddressLineOne: "Teststrasse 1",
		PostalCode:     "10115",
	}
}
