package dto

import (
	"database/sql"
	"fmt"
	"strings"
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/cache"
	"github.com/jekabolt/grbpwr-manager/internal/canonical"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/jekabolt/grbpwr-manager/internal/slug"
	pb_admin "github.com/jekabolt/grbpwr-manager/proto/gen/admin"
	pb_common "github.com/jekabolt/grbpwr-manager/proto/gen/common"
	"github.com/shopspring/decimal"
	pb_decimal "google.golang.org/genproto/googleapis/type/decimal"
	"google.golang.org/protobuf/types/known/timestamppb"
)

var (
	genderEntityPbMap = map[entity.GenderEnum]pb_common.GenderEnum{
		entity.Male:   pb_common.GenderEnum_GENDER_ENUM_MALE,
		entity.Female: pb_common.GenderEnum_GENDER_ENUM_FEMALE,
		entity.Unisex: pb_common.GenderEnum_GENDER_ENUM_UNISEX,
	}
	genderPbEntityMap = map[pb_common.GenderEnum]entity.GenderEnum{
		pb_common.GenderEnum_GENDER_ENUM_MALE:   entity.Male,
		pb_common.GenderEnum_GENDER_ENUM_FEMALE: entity.Female,
		pb_common.GenderEnum_GENDER_ENUM_UNISEX: entity.Unisex,
	}
	seasonEntityPbMap = map[entity.SeasonEnum]pb_common.SeasonEnum{
		entity.SeasonSS: pb_common.SeasonEnum_SEASON_ENUM_SS,
		entity.SeasonFW: pb_common.SeasonEnum_SEASON_ENUM_FW,
		entity.SeasonPF: pb_common.SeasonEnum_SEASON_ENUM_PF,
		entity.SeasonRC: pb_common.SeasonEnum_SEASON_ENUM_RC,
	}
	seasonPbEntityMap = map[pb_common.SeasonEnum]entity.SeasonEnum{
		pb_common.SeasonEnum_SEASON_ENUM_SS: entity.SeasonSS,
		pb_common.SeasonEnum_SEASON_ENUM_FW: entity.SeasonFW,
		pb_common.SeasonEnum_SEASON_ENUM_PF: entity.SeasonPF,
		pb_common.SeasonEnum_SEASON_ENUM_RC: entity.SeasonRC,
	}
	stockChangeSourceToProto = map[string]pb_common.StockChangeSource{
		string(entity.StockChangeSourceAdminNewProduct):    pb_common.StockChangeSource_STOCK_CHANGE_SOURCE_ADMIN_NEW_PRODUCT,
		string(entity.StockChangeSourceManualAdjustment):   pb_common.StockChangeSource_STOCK_CHANGE_SOURCE_MANUAL_ADJUSTMENT,
		string(entity.StockChangeSourceOrderPaid):          pb_common.StockChangeSource_STOCK_CHANGE_SOURCE_ORDER_PAID,
		string(entity.StockChangeSourceOrderCustom):        pb_common.StockChangeSource_STOCK_CHANGE_SOURCE_ORDER_CUSTOM,
		string(entity.StockChangeSourceOrderReturned):      pb_common.StockChangeSource_STOCK_CHANGE_SOURCE_ORDER_RETURNED,
		string(entity.StockChangeSourceOrderCancelled):     pb_common.StockChangeSource_STOCK_CHANGE_SOURCE_ORDER_CANCELLED,
		string(entity.StockChangeSourceProductionReceived): pb_common.StockChangeSource_STOCK_CHANGE_SOURCE_PRODUCTION_RECEIVED,
	}
	stockChangeSourceToEntity = map[pb_common.StockChangeSource]string{
		pb_common.StockChangeSource_STOCK_CHANGE_SOURCE_ADMIN_NEW_PRODUCT:   string(entity.StockChangeSourceAdminNewProduct),
		pb_common.StockChangeSource_STOCK_CHANGE_SOURCE_MANUAL_ADJUSTMENT:   string(entity.StockChangeSourceManualAdjustment),
		pb_common.StockChangeSource_STOCK_CHANGE_SOURCE_ORDER_PAID:          string(entity.StockChangeSourceOrderPaid),
		pb_common.StockChangeSource_STOCK_CHANGE_SOURCE_ORDER_CUSTOM:        string(entity.StockChangeSourceOrderCustom),
		pb_common.StockChangeSource_STOCK_CHANGE_SOURCE_ORDER_RETURNED:      string(entity.StockChangeSourceOrderReturned),
		pb_common.StockChangeSource_STOCK_CHANGE_SOURCE_ORDER_CANCELLED:     string(entity.StockChangeSourceOrderCancelled),
		pb_common.StockChangeSource_STOCK_CHANGE_SOURCE_PRODUCTION_RECEIVED: string(entity.StockChangeSourceProductionReceived),
	}
	stockChangeReasonToProto = map[string]pb_common.StockChangeReason{
		string(entity.StockChangeReasonInitialStock):    pb_common.StockChangeReason_STOCK_CHANGE_REASON_INITIAL_STOCK,
		string(entity.StockChangeReasonStockCount):      pb_common.StockChangeReason_STOCK_CHANGE_REASON_STOCK_COUNT,
		string(entity.StockChangeReasonDamage):          pb_common.StockChangeReason_STOCK_CHANGE_REASON_DAMAGE,
		string(entity.StockChangeReasonLoss):            pb_common.StockChangeReason_STOCK_CHANGE_REASON_LOSS,
		string(entity.StockChangeReasonFound):           pb_common.StockChangeReason_STOCK_CHANGE_REASON_FOUND,
		string(entity.StockChangeReasonCorrection):      pb_common.StockChangeReason_STOCK_CHANGE_REASON_CORRECTION,
		string(entity.StockChangeReasonReservedRelease): pb_common.StockChangeReason_STOCK_CHANGE_REASON_RESERVED_RELEASE,
		string(entity.StockChangeReasonOther):           pb_common.StockChangeReason_STOCK_CHANGE_REASON_OTHER,
		string(entity.StockChangeReasonOrder):           pb_common.StockChangeReason_STOCK_CHANGE_REASON_ORDER,
		string(entity.StockChangeReasonCustomOrder):     pb_common.StockChangeReason_STOCK_CHANGE_REASON_CUSTOM_ORDER,
		string(entity.StockChangeReasonReturnToStock):   pb_common.StockChangeReason_STOCK_CHANGE_REASON_RETURN_TO_STOCK,
		string(entity.StockChangeReasonOrderCancelled):  pb_common.StockChangeReason_STOCK_CHANGE_REASON_ORDER_CANCELLED,
	}
	stockChangeReasonToEntity = map[pb_common.StockChangeReason]string{
		pb_common.StockChangeReason_STOCK_CHANGE_REASON_INITIAL_STOCK:    string(entity.StockChangeReasonInitialStock),
		pb_common.StockChangeReason_STOCK_CHANGE_REASON_STOCK_COUNT:      string(entity.StockChangeReasonStockCount),
		pb_common.StockChangeReason_STOCK_CHANGE_REASON_DAMAGE:           string(entity.StockChangeReasonDamage),
		pb_common.StockChangeReason_STOCK_CHANGE_REASON_LOSS:             string(entity.StockChangeReasonLoss),
		pb_common.StockChangeReason_STOCK_CHANGE_REASON_FOUND:            string(entity.StockChangeReasonFound),
		pb_common.StockChangeReason_STOCK_CHANGE_REASON_CORRECTION:       string(entity.StockChangeReasonCorrection),
		pb_common.StockChangeReason_STOCK_CHANGE_REASON_RESERVED_RELEASE: string(entity.StockChangeReasonReservedRelease),
		pb_common.StockChangeReason_STOCK_CHANGE_REASON_OTHER:            string(entity.StockChangeReasonOther),
		pb_common.StockChangeReason_STOCK_CHANGE_REASON_ORDER:            string(entity.StockChangeReasonOrder),
		pb_common.StockChangeReason_STOCK_CHANGE_REASON_CUSTOM_ORDER:     string(entity.StockChangeReasonCustomOrder),
		pb_common.StockChangeReason_STOCK_CHANGE_REASON_RETURN_TO_STOCK:  string(entity.StockChangeReasonReturnToStock),
		pb_common.StockChangeReason_STOCK_CHANGE_REASON_ORDER_CANCELLED:  string(entity.StockChangeReasonOrderCancelled),
	}
)

func ConvertPbGenderEnumToEntityGenderEnum(pbGenderEnum pb_common.GenderEnum) (entity.GenderEnum, error) {
	g, ok := genderPbEntityMap[pbGenderEnum]
	if !ok {
		return entity.GenderEnum(""), fmt.Errorf("bad pb target gender %v", pbGenderEnum)
	}
	return g, nil
}

func ConvertEntityGenderToPbGenderEnum(entityGenderEnum entity.GenderEnum) (pb_common.GenderEnum, error) {
	g, ok := genderEntityPbMap[entityGenderEnum]
	if !ok {
		// DB may have proto enum string (GENDER_ENUM_UNKNOWN), empty, or invalid - default to UNKNOWN
		return pb_common.GenderEnum_GENDER_ENUM_UNKNOWN, nil
	}
	return g, nil
}

func ConvertPbSeasonEnumToEntitySeasonEnum(pbSeasonEnum pb_common.SeasonEnum) (entity.SeasonEnum, error) {
	s, ok := seasonPbEntityMap[pbSeasonEnum]
	if !ok {
		return entity.SeasonEnum(""), fmt.Errorf("bad pb season %v", pbSeasonEnum)
	}
	return s, nil
}

func ConvertEntitySeasonToPbSeasonEnum(entitySeasonEnum entity.SeasonEnum) (pb_common.SeasonEnum, error) {
	s, ok := seasonEntityPbMap[entitySeasonEnum]
	if !ok {
		// DB may have proto enum string (SEASON_ENUM_UNKNOWN), empty, or invalid - default to UNKNOWN
		return pb_common.SeasonEnum_SEASON_ENUM_UNKNOWN, nil
	}
	return s, nil
}

func convertDecimal(value string) (decimal.Decimal, error) {
	if value == "" {
		return decimal.Zero, nil
	}
	return decimal.NewFromString(value)
}

// convertMerchInsertToEntity converts the colourway-owned merchandising write message (R2/R4/R8) into
// the colourway subset of entity.ColorwayBodyInsert. The style facts (brand/season/collection/gender/
// fit/composition/care/model-wears/categories) were stripped from this message (§1.5) — they are the
// Style's now and left zero here (written only through UpdateStyle). It validates color_code (3
// uppercase chars, in the dictionary) and the optional hex override. countryCode (ISO, R9) is carried
// into CountryOfOrigin, which the store resolves to the ISO country_code column.
func convertMerchInsertToEntity(m *pb_common.ColorwayMerchandisingInsert, countryCode string) (entity.ColorwayBodyInsert, error) {
	if m == nil {
		return entity.ColorwayBodyInsert{}, fmt.Errorf("merchandising is nil")
	}

	var salePercentage decimal.Decimal
	var salePercentageValid bool
	if m.SalePercentage != nil {
		var err error
		salePercentage, err = convertDecimal(m.SalePercentage.Value)
		if err != nil {
			return entity.ColorwayBodyInsert{}, fmt.Errorf("failed to convert sale percentage: %w", err)
		}
		salePercentageValid = m.SalePercentage.Value != ""
	}

	if len(m.ColorCode) != 3 ||
		m.ColorCode != strings.ToUpper(m.ColorCode) ||
		strings.TrimSpace(m.ColorCode) != m.ColorCode {
		return entity.ColorwayBodyInsert{}, fmt.Errorf("color_code must be exactly 3 uppercase characters")
	}
	dictionaryColor, ok := cache.GetColorByCode(m.ColorCode)
	if !ok {
		return entity.ColorwayBodyInsert{}, fmt.Errorf("color_code %q is not in the color dictionary", m.ColorCode)
	}
	var colorHexOverride sql.NullString
	if m.ColorHexOverride != nil {
		if !isHexColor(m.GetColorHexOverride()) {
			return entity.ColorwayBodyInsert{}, fmt.Errorf("color_hex_override must be #RRGGBB")
		}
		colorHexOverride = sql.NullString{String: m.GetColorHexOverride(), Valid: true}
	}

	var preorderTime sql.NullTime
	if m.Preorder != nil {
		preorderTime = sql.NullTime{Time: m.Preorder.AsTime(), Valid: m.Preorder.IsValid()}
		if preorderTime.Valid && preorderTime.Time.Year() < time.Now().UTC().Year() {
			preorderTime.Valid = false
		}
	}

	return entity.ColorwayBodyInsert{
		Preorder:         preorderTime,
		Color:            dictionaryColor.Name,
		ColorCode:        dictionaryColor.Code,
		ColorHexOverride: colorHexOverride,
		CountryOfOrigin:  countryCode,
		SalePercentage:   decimal.NullDecimal{Decimal: salePercentage, Valid: salePercentageValid},
		MinTier:          int16(m.MinTier),
	}, nil
}

// BuildColorwayInsertEntity assembles the colourway-owned write entity from the decomposed
// CreateColorway/UpdateColorway request fields (merchandising + thumbnails + merch translations +
// cost). Style facts are never part of it (R4). cost_price is optional COGS: absent/negative stays
// invalid so the store leaves the stored value unchanged (COALESCE on update).
func BuildColorwayInsertEntity(m *pb_common.ColorwayMerchandisingInsert, countryCode string, thumbnailMediaID, secondaryThumbnailMediaID int32, translations []*pb_common.ColorwayInsertTranslation, costPrice *pb_decimal.Decimal) (*entity.ColorwayInsert, error) {
	body, err := convertMerchInsertToEntity(m, countryCode)
	if err != nil {
		return nil, err
	}
	var secondaryThumbnailID sql.NullInt32
	if secondaryThumbnailMediaID != 0 {
		secondaryThumbnailID = sql.NullInt32{Int32: secondaryThumbnailMediaID, Valid: true}
	}
	var trans []entity.ColorwayTranslationInsert
	for _, t := range translations {
		if t == nil {
			continue
		}
		trans = append(trans, entity.ColorwayTranslationInsert{
			LanguageId:  int(t.LanguageId),
			Name:        t.Name,
			Description: t.Description,
		})
	}
	cost, err := nullDecimalFromPb(costPrice)
	if err != nil {
		return nil, fmt.Errorf("invalid cost_price: %w", err)
	}
	if cost.Valid {
		if cost.Decimal.IsNegative() {
			cost = decimal.NullDecimal{}
		} else {
			cost.Decimal = roundMoney(cost.Decimal)
		}
	}
	return &entity.ColorwayInsert{
		ProductBodyInsert:         body,
		ThumbnailMediaID:          int(thumbnailMediaID),
		SecondaryThumbnailMediaID: secondaryThumbnailID,
		Translations:              trans,
		CostPrice:                 cost,
	}, nil
}

// ConvertColorwayTags maps the tag write messages to entity tags.
func ConvertColorwayTags(pbTags []*pb_common.ColorwayTagInsert) []entity.ColorwayTagInsert {
	return convertTags(pbTags)
}

// ConvertColorwayPrices maps the price write messages to entity prices.
func ConvertColorwayPrices(pbPrices []*pb_common.ColorwayPriceInsert) []entity.ColorwayPriceInsert {
	return convertPrices(pbPrices)
}

// ConvertColorwayMediaIDs maps the media-id list to ints.
func ConvertColorwayMediaIDs(ids []int32) []int {
	return convertMediaIds(ids)
}

// ConvertPbStylePatchToEntity converts the admin StylePatch write message into entity.StylePatch — the
// catalogue-style facts owned solely by UpdateStyle (R4/§14.7).
func ConvertPbStylePatchToEntity(brand string, season pb_common.SeasonEnum, collection string, targetGender pb_common.GenderEnum, fit, composition, careInstructions string, modelWearsHeightCm, modelWearsSizeID, topCategoryID, subCategoryID, typeID int32) (entity.StylePatch, error) {
	tg, err := ConvertPbGenderEnumToEntityGenderEnum(targetGender)
	if err != nil {
		return entity.StylePatch{}, err
	}
	sn, err := ConvertPbSeasonEnumToEntitySeasonEnum(season)
	if err != nil {
		return entity.StylePatch{}, err
	}
	return entity.StylePatch{
		Brand:              brand,
		Season:             sn,
		Collection:         collection,
		TargetGender:       tg,
		Fit:                sql.NullString{String: fit, Valid: fit != ""},
		Composition:        sql.NullString{String: composition, Valid: composition != ""},
		CareInstructions:   sql.NullString{String: careInstructions, Valid: careInstructions != ""},
		ModelWearsHeightCm: sql.NullInt32{Int32: modelWearsHeightCm, Valid: modelWearsHeightCm != 0},
		ModelWearsSizeId:   sql.NullInt32{Int32: modelWearsSizeID, Valid: modelWearsSizeID != 0},
		TopCategoryId:      int(topCategoryID),
		SubCategoryId:      sql.NullInt32{Int32: subCategoryID, Valid: subCategoryID != 0},
		TypeId:             sql.NullInt32{Int32: typeID, Valid: typeID != 0},
	}, nil
}

func convertPrices(pbPrices []*pb_common.ColorwayPriceInsert) []entity.ColorwayPriceInsert {
	var prices []entity.ColorwayPriceInsert
	for _, pbPrice := range pbPrices {
		if pbPrice == nil || pbPrice.Price == nil {
			continue
		}
		priceVal, err := convertDecimal(pbPrice.Price.Value)
		if err != nil {
			continue
		}
		currency := strings.ToUpper(pbPrice.Currency)
		prices = append(prices, entity.ColorwayPriceInsert{
			Currency: currency,
			Price:    RoundForCurrency(priceVal, currency),
		})
	}
	return prices
}

func convertMediaIds(pbMediaIds []int32) []int {
	var mediaIds []int
	for _, pbMediaId := range pbMediaIds {
		mediaIds = append(mediaIds, int(pbMediaId))
	}
	return mediaIds
}

// canonicalProductName returns the name of the product's canonical translation — the default-language
// translation, or the smallest language id when none is default — for pretty-slug generation. It must
// not use Translations[0], whose position depends on SQL row order / insert order and would make the
// canonical URL unstable across reads (problem 030). The same policy is applied to archives.
func canonicalProductName(translations []entity.ColorwayTranslationInsert) string {
	name, ok := canonical.ProductName(translations, cache.GetLanguages())
	if !ok {
		return ""
	}
	return name
}

func convertTags(pbTags []*pb_common.ColorwayTagInsert) []entity.ColorwayTagInsert {
	var tags []entity.ColorwayTagInsert
	for _, pbTag := range pbTags {
		tags = append(tags, entity.ColorwayTagInsert{
			Tag: pbTag.Tag,
		})
	}
	return tags
}

// buildColorwayDisplayPb builds the admin/internal ColorwayDisplay read projection (R8): the resolved
// merchandising (colourway merch + style-resolved garment facts, output-only) plus merch translation
// overrides and thumbnails. The old product_body/ColorwayBody wrapper was removed with the write
// decomposition; the storefront uses its own StorefrontColorway (R3), never this admin projection.
func buildColorwayDisplayPb(display *entity.ColorwayDisplay) *pb_common.ColorwayDisplay {
	body := &display.ProductBody
	bi := &body.ProductBodyInsert
	tg, _ := ConvertEntityGenderToPbGenderEnum(bi.TargetGender)
	sn, _ := ConvertEntitySeasonToPbSeasonEnum(bi.Season)

	var pbTranslations []*pb_common.ColorwayInsertTranslation
	for _, trans := range body.Translations {
		pbTranslations = append(pbTranslations, &pb_common.ColorwayInsertTranslation{
			LanguageId:  int32(trans.LanguageId),
			Name:        trans.Name,
			Description: trans.Description,
		})
	}

	var pbSecondaryThumbnail *pb_common.MediaFull
	if display.SecondaryThumbnail != nil {
		pbSecondaryThumbnail = ConvertEntityToCommonMedia(display.SecondaryThumbnail)
	}

	return &pb_common.ColorwayDisplay{
		Thumbnail:          ConvertEntityToCommonMedia(&display.Thumbnail),
		SecondaryThumbnail: pbSecondaryThumbnail,
		Merchandising: &pb_common.ColorwayMerchandising{
			Preorder:           timestamppb.New(bi.Preorder.Time),
			Brand:              bi.Brand,
			ColorCode:          bi.ColorCode,
			DictionaryColor:    dictionaryColorToPb(bi.ColorCode),
			ColorHexOverride:   optionalStringFromNull(bi.ColorHexOverride),
			CountryOfOrigin:    bi.CountryOfOrigin,
			SalePercentage:     &pb_decimal.Decimal{Value: bi.SalePercentage.Decimal.String()},
			TopCategoryId:      int32(bi.TopCategoryId),
			SubCategoryId:      int32(bi.SubCategoryId.Int32),
			TypeId:             int32(bi.TypeId.Int32),
			ModelWearsHeightCm: int32(bi.ModelWearsHeightCm.Int32),
			ModelWearsSizeId:   int32(bi.ModelWearsSizeId.Int32),
			TargetGender:       tg,
			Season:             sn,
			CareInstructions:   bi.CareInstructions.String,
			Composition:        bi.Composition.String,
			Collection:         bi.Collection,
			Fit:                bi.Fit.String,
			MinTier:            int32(bi.MinTier),
		},
		Translations: pbTranslations,
	}
}

func ConvertToPbProductFull(e *entity.ColorwayFull) (*pb_common.ColorwayFull, error) {
	if e == nil {
		return nil, nil
	}

	pbProductDisplay := buildColorwayDisplayPb(&e.Product.ProductDisplay)

	// Convert prices - place prices inside nested Product
	pbPrices := convertEntityPricesToPb(e.Prices)

	// Canonical translation name for the pretty slug — deterministic (default language, else the
	// smallest language id), never the order-dependent Translations[0] (problem 030).
	firstTranslationName := canonicalProductName(e.Product.ProductDisplay.ProductBody.Translations)

	// sold_out is derived from the sizes' total stock — one shared definition (PR5-B).
	soldOut := entity.SoldOutFromSizes(e.Sizes)

	pbProduct := &pb_common.Colorway{
		Id:        int32(e.Product.Id),
		CreatedAt: timestamppb.New(e.Product.CreatedAt),
		UpdatedAt: timestamppb.New(e.Product.UpdatedAt),
		Slug:      slug.ProductPath(firstTranslationName, e.Product.SKU),
		BaseSku:   e.Product.SKU, // R8: renamed from Sku
		Display:   pbProductDisplay, // R8: renamed from ProductDisplay
		Prices:    pbPrices, // Prices are in nested Product
		SoldOut:   soldOut,
		Status:    pb_common.ColorwayLifecycleStatus(e.Product.LifecycleStatus),
		StyleId:   int32(e.Product.StyleId), // R4: the single style relation
		ColorCode: e.Product.ProductDisplay.ProductBody.ProductBodyInsert.ColorCode,
		// lock_version (tech_card.lock_version) and published_at need entity plumbing — left unset here.
	}

	pbSizes := convertEntitySizesToPbSizes(e.Sizes)
	pbMedia := ConvertEntityMediaListToPbMedia(e.Media)
	pbTags := convertEntityTagsToPbTags(e.Tags)

	// R5: the size chart is style-owned now (StyleSizeChart); ColorwayFull no longer carries per-colourway
	// measurements. e.Measurements is not serialised here.
	return &pb_common.ColorwayFull{
		Colorway: pbProduct, // R8: renamed from Product
		Variants: pbSizes, // R8: renamed from Sizes
		Media:    pbMedia,
		Tags:     pbTags,
	}, nil
}

func convertEntityPricesToPb(prices []entity.ColorwayPrice) []*pb_common.ColorwayPrice {
	var pbPrices []*pb_common.ColorwayPrice
	for _, price := range prices {
		pbPrices = append(pbPrices, &pb_common.ColorwayPrice{
			Currency: price.Currency,
			Price:    &pb_decimal.Decimal{Value: price.Price.String()},
		})
	}
	return pbPrices
}

// ConvertEntityVariantToPb projects a single variant (product_size) to the admin/common wire message,
// carrying its stored lifecycle status (R2). The variant SKU/size/colourway id are admin-facing here;
// the storefront never sees these internal ids (it gets a StorefrontVariant, R3).
func ConvertEntityVariantToPb(v entity.Variant) *pb_common.Variant {
	return &pb_common.Variant{
		VariantId: int32(v.Id), // R8: renamed from Id
		Quantity: &pb_decimal.Decimal{
			Value: v.Quantity.String(),
		},
		ColorwayId: int32(v.ProductId), // R8: renamed from ProductId
		SizeId:     int32(v.SizeId),
		VariantSku: v.SKU.String, // R8: renamed from Sku
		Status:     pb_common.VariantLifecycleStatus(v.Status),
	}
}

func convertEntitySizesToPbSizes(sizes []entity.Variant) []*pb_common.Variant {
	var pbSizes []*pb_common.Variant
	for _, size := range sizes {
		pbSizes = append(pbSizes, ConvertEntityVariantToPb(size))
	}
	return pbSizes
}

func convertEntityTagsToPbTags(tags []entity.ColorwayTag) []*pb_common.ColorwayTag {
	var pbTags []*pb_common.ColorwayTag
	for _, tag := range tags {
		pbTags = append(pbTags, &pb_common.ColorwayTag{
			Id: int32(tag.Id),
			TagInsert: &pb_common.ColorwayTagInsert{ // R8: renamed from ProductTagInsert
				Tag: tag.Tag,
			},
		})
	}
	return pbTags
}

// StockChangeSourceToFilterString converts proto StockChangeSource to DB filter string.
// Returns empty string for UNSPECIFIED (no filter).
func StockChangeSourceToFilterString(s pb_common.StockChangeSource) string {
	return stockChangeSourceToEntity[s]
}

// StockChangeToProto converts entity.StockChange to pb_common.StockChange.
func StockChangeToProto(e *entity.StockChange) *pb_common.StockChange {
	if e == nil {
		return nil
	}
	source := pb_common.StockChangeSource_STOCK_CHANGE_SOURCE_UNSPECIFIED
	if s, ok := stockChangeSourceToProto[e.Source]; ok {
		source = s
	}
	return &pb_common.StockChange{
		Id:             int32(e.Id),
		ColorwayId:     int32(e.ProductId), // R8: renamed from ProductId
		SizeId:         int32(e.SizeId),
		QuantityDelta:  &pb_decimal.Decimal{Value: e.QuantityDelta.String()},
		QuantityBefore: &pb_decimal.Decimal{Value: e.QuantityBefore.String()},
		QuantityAfter:  &pb_decimal.Decimal{Value: e.QuantityAfter.String()},
		Source:         source,
		OrderId:        int32(e.OrderId),
		OrderUuid:      e.OrderUUID,
		CreatedAt:      timestamppb.New(e.CreatedAt),
		AdminUsername:  e.AdminUsername,
	}
}

// MapStockChangeSourceToAPI maps internal source types to API-friendly names.
// Simplifies source categories for end-user consumption.
func MapStockChangeSourceToAPI(internalSource string) string {
	mapping := map[string]string{
		string(entity.StockChangeSourceAdminNewProduct):  "admin_new_product",
		string(entity.StockChangeSourceManualAdjustment): "manual_adjustment",
		string(entity.StockChangeSourceOrderPaid):        "order_paid",
		string(entity.StockChangeSourceOrderCustom):      "order_custom",
		string(entity.StockChangeSourceOrderReturned):    "order_returned",
		string(entity.StockChangeSourceOrderCancelled):   "order_cancelled",
	}

	if mapped, ok := mapping[internalSource]; ok {
		return mapped
	}
	return "other"
}

// FormatStockChangeReference builds reference string from available data.
// For order-related changes: returns the order_uuid directly (already in ORD-XXXXXXX format)
// For admin changes: "-"
func FormatStockChangeReference(referenceId, orderUUID, adminUsername string) string {
	if orderUUID != "" {
		// order_uuid is already in ORD-XXXXXXX format, return as-is
		return strings.ToUpper(orderUUID)
	}
	if referenceId != "" {
		return referenceId
	}
	// Admin changes show "-" as reference
	return "-"
}

// StockChangeReasonToString converts proto StockChangeReason to entity string.
func StockChangeReasonToString(r pb_common.StockChangeReason) string {
	return stockChangeReasonToEntity[r]
}

// StockChangeRowToProto converts entity.StockChangeRow to pb_admin.StockChangeRow.
func StockChangeRowToProto(e *entity.StockChangeRow) *pb_admin.StockChangeRow {
	if e == nil {
		return nil
	}

	// e.SKU is already the variant SKU (product_size.sku) from the stock-history query, or SHIPPING
	// for a shipping-only entry. No size suffix to append.
	formattedSKU := e.SKU

	// Map source to API-friendly name
	apiSource := MapStockChangeSourceToAPI(e.Source)

	// Format reference: for order-related sources use ORD-XXXXXXX, for admin use "-"
	reference := FormatStockChangeReference(e.ReferenceId, e.OrderUUID, e.AdminUsername)

	// Derive direction from amount_changed sign
	direction := pb_common.StockAdjustmentDirection_STOCK_ADJUSTMENT_DIRECTION_UNSPECIFIED
	if e.AmountChanged.IsPositive() {
		direction = pb_common.StockAdjustmentDirection_STOCK_ADJUSTMENT_DIRECTION_INCREASE
	} else if e.AmountChanged.IsNegative() {
		direction = pb_common.StockAdjustmentDirection_STOCK_ADJUSTMENT_DIRECTION_DECREASE
	}

	// Build proto message
	row := &pb_admin.StockChangeRow{
		Date:           timestamppb.New(e.Date),
		Sku:            formattedSKU,
		AmountChanged:  &pb_decimal.Decimal{Value: e.AmountChanged.Abs().String()},
		Direction:      direction,
		RemainingStock: &pb_decimal.Decimal{Value: e.RemainingStock.String()},
		Source:         apiSource,
		Reference:      reference,
	}

	// Add reason if present
	if e.Reason != "" {
		if reason, ok := stockChangeReasonToProto[e.Reason]; ok {
			row.Reason = &reason
		}
	}

	// Add comment: prefer order_comment for order-related entries, fall back to stock change comment
	comment := e.OrderComment
	if comment == "" {
		comment = e.Comment
	}
	if comment != "" {
		row.Comment = &comment
	}

	// Add financial fields if present (raw numeric values, currency separate)
	if e.PriceBeforeDiscount != "" {
		row.PriceBeforeDiscount = &e.PriceBeforeDiscount
	}
	if e.DiscountAmount != "" {
		row.DiscountAmount = &e.DiscountAmount
	}
	if e.PaidCurrency != "" {
		row.PaidCurrency = &e.PaidCurrency
	}
	if e.PaidAmount != "" {
		row.PaidAmount = &e.PaidAmount
	}
	if e.PayoutBaseAmount != "" && e.PayoutBaseCurrency != "" {
		row.PayoutBaseAmount = &e.PayoutBaseAmount
		row.PayoutBaseCurrency = &e.PayoutBaseCurrency
	}

	return row
}

// ConvertEntityProductToCommon converts entity.Colorway to pb_common.Colorway
func ConvertEntityProductToCommon(e *entity.Colorway) (*pb_common.Colorway, error) {
	// Canonical translation name for the pretty slug — deterministic (default language, else the
	// smallest language id), never the order-dependent Translations[0] (problem 030).
	firstTranslationName := canonicalProductName(e.ProductDisplay.ProductBody.Translations)

	pbProduct := &pb_common.Colorway{
		Id:        int32(e.Id),
		CreatedAt: timestamppb.New(e.CreatedAt),
		UpdatedAt: timestamppb.New(e.UpdatedAt),
		Slug:      slug.ProductPath(firstTranslationName, e.SKU),
		BaseSku:   e.SKU, // R8: renamed from Sku
		Display:   buildColorwayDisplayPb(&e.ProductDisplay), // R8: renamed from ProductDisplay
		Prices:    convertEntityPricesToPb(e.Prices),
		SoldOut:   e.SoldOut,
		Status:    pb_common.ColorwayLifecycleStatus(e.LifecycleStatus),
		StyleId:   int32(e.StyleId),
		ColorCode: e.ProductDisplay.ProductBody.ProductBodyInsert.ColorCode,
	}

	return pbProduct, nil
}
