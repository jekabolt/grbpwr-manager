package dto

import (
	"database/sql"
	"fmt"
	"strings"
	"time"

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

func convertProductBodyInsertToProductBody(pbProductBodyInsert *pb_common.ProductBodyInsert) (*entity.ProductBody, error) {
	if pbProductBodyInsert == nil {
		return nil, fmt.Errorf("ProductBodyInsert is nil")
	}

	var salePercentage decimal.Decimal
	var salePercentageValid bool
	if pbProductBodyInsert.SalePercentage != nil {
		var err error
		salePercentage, err = convertDecimal(pbProductBodyInsert.SalePercentage.Value)
		if err != nil {
			return nil, fmt.Errorf("failed to convert product sale percentage: %w", err)
		}
		salePercentageValid = pbProductBodyInsert.SalePercentage.Value != ""
	}

	targetGender, err := ConvertPbGenderEnumToEntityGenderEnum(pbProductBodyInsert.TargetGender)
	if err != nil {
		return nil, err
	}

	season, err := ConvertPbSeasonEnumToEntitySeasonEnum(pbProductBodyInsert.Season)
	if err != nil {
		return nil, err
	}

	var preorderTime sql.NullTime
	if pbProductBodyInsert.Preorder != nil {
		preorderTime = sql.NullTime{
			Time:  pbProductBodyInsert.Preorder.AsTime(),
			Valid: pbProductBodyInsert.Preorder.IsValid(),
		}
		if preorderTime.Valid && preorderTime.Time.Year() < time.Now().UTC().Year() {
			preorderTime.Valid = false
		}
	}

	pb := &entity.ProductBody{
		ProductBodyInsert: entity.ProductBodyInsert{
			Preorder:           preorderTime,
			Brand:              pbProductBodyInsert.Brand,
			Color:              pbProductBodyInsert.Color,
			ColorCode:          sql.NullString{String: pbProductBodyInsert.ColorCode, Valid: pbProductBodyInsert.ColorCode != ""},
			ColorHex:           pbProductBodyInsert.ColorHex,
			CountryOfOrigin:    pbProductBodyInsert.CountryOfOrigin,
			SalePercentage:     decimal.NullDecimal{Decimal: salePercentage, Valid: salePercentageValid},
			TopCategoryId:      int(pbProductBodyInsert.TopCategoryId),
			SubCategoryId:      sql.NullInt32{Int32: int32(pbProductBodyInsert.SubCategoryId), Valid: pbProductBodyInsert.SubCategoryId != 0},
			TypeId:             sql.NullInt32{Int32: int32(pbProductBodyInsert.TypeId), Valid: pbProductBodyInsert.TypeId != 0},
			ModelWearsHeightCm: sql.NullInt32{Int32: int32(pbProductBodyInsert.ModelWearsHeightCm), Valid: pbProductBodyInsert.ModelWearsHeightCm != 0},
			ModelWearsSizeId:   sql.NullInt32{Int32: int32(pbProductBodyInsert.ModelWearsSizeId), Valid: pbProductBodyInsert.ModelWearsSizeId != 0},
			Hidden:             sql.NullBool{Bool: pbProductBodyInsert.Hidden, Valid: true},
			TargetGender:       targetGender,
			Season:             season,
			CareInstructions:   sql.NullString{String: pbProductBodyInsert.CareInstructions, Valid: pbProductBodyInsert.CareInstructions != ""},
			Composition:        sql.NullString{String: pbProductBodyInsert.Composition, Valid: pbProductBodyInsert.Composition != ""},
			Version:            pbProductBodyInsert.Version,
			Collection:         pbProductBodyInsert.Collection,
			Fit:                sql.NullString{String: pbProductBodyInsert.Fit, Valid: pbProductBodyInsert.Fit != ""},
			MinTier:            int16(pbProductBodyInsert.MinTier),
		},
		Translations: []entity.ProductTranslationInsert{},
	}

	return pb, nil
}

func ConvertPbProductInsertToEntity(pbProductNew *pb_common.ProductInsert) (*entity.ProductInsert, error) {
	if pbProductNew == nil {
		return nil, fmt.Errorf("input pbProductNew is nil")
	}

	// Create a ProductBody from ProductBodyInsert
	productBody, err := convertProductBodyInsertToProductBody(pbProductNew.ProductBodyInsert)
	if err != nil {
		return nil, err
	}

	// Convert translations
	var translations []entity.ProductTranslationInsert
	for _, trans := range pbProductNew.Translations {
		translations = append(translations, entity.ProductTranslationInsert{
			LanguageId:  int(trans.LanguageId),
			Name:        trans.Name,
			Description: trans.Description,
		})
	}

	// Set translations on the product body
	productBody.Translations = translations

	var secondaryThumbnailID sql.NullInt32
	if pbProductNew.SecondaryThumbnailMediaId != 0 {
		secondaryThumbnailID = sql.NullInt32{
			Int32: pbProductNew.SecondaryThumbnailMediaId,
			Valid: true,
		}
	}

	// Convert prices
	prices := convertPrices(pbProductNew.Prices)

	// cost_price is optional COGS in base currency. When absent/empty it stays invalid so
	// the store leaves the stored value unchanged (COALESCE on update). Negatives are
	// rejected (treated as unset) rather than persisted.
	costPrice, err := nullDecimalFromPb(pbProductNew.CostPrice)
	if err != nil {
		return nil, fmt.Errorf("invalid cost_price: %w", err)
	}
	if costPrice.Valid {
		if costPrice.Decimal.IsNegative() {
			costPrice = decimal.NullDecimal{}
		} else {
			costPrice.Decimal = roundMoney(costPrice.Decimal)
		}
	}

	return &entity.ProductInsert{
		ProductBodyInsert:         productBody.ProductBodyInsert,
		ThumbnailMediaID:          int(pbProductNew.ThumbnailMediaId),
		SecondaryThumbnailMediaID: secondaryThumbnailID,
		Translations:              translations,
		Prices:                    prices,
		CostPrice:                 costPrice,
	}, nil
}

func convertPrices(pbPrices []*pb_common.ProductPriceInsert) []entity.ProductPriceInsert {
	var prices []entity.ProductPriceInsert
	for _, pbPrice := range pbPrices {
		if pbPrice == nil || pbPrice.Price == nil {
			continue
		}
		priceVal, err := convertDecimal(pbPrice.Price.Value)
		if err != nil {
			continue
		}
		currency := strings.ToUpper(pbPrice.Currency)
		prices = append(prices, entity.ProductPriceInsert{
			Currency: currency,
			Price:    RoundForCurrency(priceVal, currency),
		})
	}
	return prices
}

func ConvertPbMeasurementsUpdateToEntity(mUpd []*pb_common.ProductMeasurementUpdate) ([]entity.ProductMeasurementUpdate, error) {
	if mUpd == nil {
		return nil, fmt.Errorf("input pbProductMeasurementUpdate is nil")
	}

	var measurements []entity.ProductMeasurementUpdate
	for _, pbMeasurement := range mUpd {
		if pbMeasurement == nil {
			continue
		}

		if pbMeasurement.MeasurementValue == nil {
			return nil, fmt.Errorf("MeasurementValue is nil for measurement name id %v", pbMeasurement.MeasurementNameId)
		}

		measurementValue, err := convertDecimal(pbMeasurement.MeasurementValue.Value)
		if err != nil {
			return nil, fmt.Errorf("failed to convert product measurement value: %w", err)
		}

		measurements = append(measurements, entity.ProductMeasurementUpdate{
			SizeId:            int(pbMeasurement.SizeId),
			MeasurementNameId: int(pbMeasurement.MeasurementNameId),
			MeasurementValue:  measurementValue,
		})
	}

	return measurements, nil
}

func ConvertCommonProductToEntity(pbProductNew *pb_common.ProductNew) (*entity.ProductNew, error) {
	if pbProductNew == nil {
		return nil, fmt.Errorf("input pbProductNew is nil")
	}

	if pbProductNew.Product == nil {
		return nil, fmt.Errorf("pbProductNew.Product is nil")
	}

	productBody, err := convertProductBodyInsertToProductBody(pbProductNew.Product.ProductBodyInsert)
	if err != nil {
		return nil, err
	}

	var translations []entity.ProductTranslationInsert
	for _, trans := range pbProductNew.Product.Translations {
		translations = append(translations, entity.ProductTranslationInsert{
			LanguageId:  int(trans.LanguageId),
			Name:        trans.Name,
			Description: trans.Description,
		})
	}

	productBody.Translations = translations

	productInsert := &entity.ProductInsert{
		ProductBodyInsert: productBody.ProductBodyInsert,
		ThumbnailMediaID:  int(pbProductNew.Product.ThumbnailMediaId),
		SecondaryThumbnailMediaID: sql.NullInt32{
			Int32: pbProductNew.Product.SecondaryThumbnailMediaId,
			Valid: pbProductNew.Product.SecondaryThumbnailMediaId != 0,
		},
		Translations: translations,
		Prices:       convertPrices(pbProductNew.Prices),
	}

	sizeMeasurements, err := convertSizeMeasurements(pbProductNew.SizeMeasurements)
	if err != nil {
		return nil, err
	}

	mediaIds := convertMediaIds(pbProductNew.MediaIds)
	tags := convertTags(pbProductNew.Tags)
	prices := convertPrices(pbProductNew.Prices)

	return &entity.ProductNew{
		Product:          productInsert,
		SizeMeasurements: sizeMeasurements,
		MediaIds:         mediaIds,
		Tags:             tags,
		Prices:           prices,
	}, nil
}

func convertSizeMeasurements(pbSizeMeasurements []*pb_common.SizeWithMeasurementInsert) ([]entity.SizeWithMeasurementInsert, error) {
	var sizeMeasurements []entity.SizeWithMeasurementInsert
	for _, pbSizeMeasurement := range pbSizeMeasurements {
		if pbSizeMeasurement == nil {
			continue
		}

		if pbSizeMeasurement.ProductSize == nil {
			return nil, fmt.Errorf("ProductSize is nil in SizeWithMeasurementInsert")
		}

		if pbSizeMeasurement.ProductSize.Quantity == nil {
			return nil, fmt.Errorf("ProductSize.Quantity is nil for size id %v", pbSizeMeasurement.ProductSize.SizeId)
		}

		quantity, err := convertDecimal(pbSizeMeasurement.ProductSize.Quantity.Value)
		if err != nil {
			return nil, fmt.Errorf("failed to convert product size quantity: %w for size id  %v", err, pbSizeMeasurement.ProductSize.SizeId)
		}

		productSize := &entity.ProductSizeInsert{
			Quantity: quantity.Round(0),
			SizeId:   int(pbSizeMeasurement.ProductSize.SizeId),
		}

		measurements, err := convertMeasurements(pbSizeMeasurement.Measurements)
		if err != nil {
			return nil, err
		}

		sizeMeasurements = append(sizeMeasurements, entity.SizeWithMeasurementInsert{
			ProductSize:  *productSize,
			Measurements: measurements,
		})
	}
	return sizeMeasurements, nil
}

func convertMeasurements(pbMeasurements []*pb_common.ProductMeasurementInsert) ([]entity.ProductMeasurementInsert, error) {
	var measurements []entity.ProductMeasurementInsert
	for _, pbMeasurement := range pbMeasurements {
		if pbMeasurement == nil {
			continue
		}

		if pbMeasurement.MeasurementValue == nil {
			return nil, fmt.Errorf("MeasurementValue is nil for measurement name id %v", pbMeasurement.MeasurementNameId)
		}

		measurementValue, err := convertDecimal(pbMeasurement.MeasurementValue.Value)
		if err != nil {
			return nil, fmt.Errorf("failed to convert product measurement value: %w for measurement name id %v", err, pbMeasurement.MeasurementNameId)
		}

		measurements = append(measurements, entity.ProductMeasurementInsert{
			MeasurementNameId: int(pbMeasurement.MeasurementNameId),
			MeasurementValue:  measurementValue,
		})
	}
	return measurements, nil
}

func convertMediaIds(pbMediaIds []int32) []int {
	var mediaIds []int
	for _, pbMediaId := range pbMediaIds {
		mediaIds = append(mediaIds, int(pbMediaId))
	}
	return mediaIds
}

func convertTags(pbTags []*pb_common.ProductTagInsert) []entity.ProductTagInsert {
	var tags []entity.ProductTagInsert
	for _, pbTag := range pbTags {
		tags = append(tags, entity.ProductTagInsert{
			Tag: pbTag.Tag,
		})
	}
	return tags
}

func ConvertToPbProductFull(e *entity.ProductFull) (*pb_common.ProductFull, error) {
	if e == nil {
		return nil, nil
	}

	productBody := &e.Product.ProductDisplay.ProductBody
	productBodyInsert := &productBody.ProductBodyInsert

	tg, _ := ConvertEntityGenderToPbGenderEnum(productBodyInsert.TargetGender)
	sn, _ := ConvertEntitySeasonToPbSeasonEnum(productBodyInsert.Season)

	// Convert translations to protobuf format
	var pbTranslations []*pb_common.ProductInsertTranslation
	for _, trans := range productBody.Translations {
		pbTranslations = append(pbTranslations, &pb_common.ProductInsertTranslation{
			LanguageId:  int32(trans.LanguageId),
			Name:        trans.Name,
			Description: trans.Description,
		})
	}

	var pbSecondaryThumbnail *pb_common.MediaFull
	if e.Product.ProductDisplay.SecondaryThumbnail != nil {
		pbSecondaryThumbnail = ConvertEntityToCommonMedia(e.Product.ProductDisplay.SecondaryThumbnail)
	}

	pbProductDisplay := &pb_common.ProductDisplay{
		ProductBody: &pb_common.ProductBody{
			ProductBodyInsert: &pb_common.ProductBodyInsert{
				Preorder:        timestamppb.New(productBodyInsert.Preorder.Time),
				Brand:           productBodyInsert.Brand,
				Color:           productBodyInsert.Color,
				ColorCode:       productBodyInsert.ColorCode.String,
				ColorHex:        productBodyInsert.ColorHex,
				CountryOfOrigin: productBodyInsert.CountryOfOrigin,

				SalePercentage:     &pb_decimal.Decimal{Value: productBodyInsert.SalePercentage.Decimal.String()},
				TopCategoryId:      int32(productBodyInsert.TopCategoryId),
				SubCategoryId:      int32(productBodyInsert.SubCategoryId.Int32),
				TypeId:             int32(productBodyInsert.TypeId.Int32),
				ModelWearsHeightCm: int32(productBodyInsert.ModelWearsHeightCm.Int32),
				ModelWearsSizeId:   int32(productBodyInsert.ModelWearsSizeId.Int32),
				Hidden:             productBodyInsert.Hidden.Bool,
				TargetGender:       tg,
				Season:             sn,
				CareInstructions:   productBodyInsert.CareInstructions.String,
				Composition:        productBodyInsert.Composition.String,
				Version:            productBodyInsert.Version,
				Collection:         productBodyInsert.Collection,
				Fit:                productBodyInsert.Fit.String,
				MinTier:            int32(productBodyInsert.MinTier),
			},
			Translations: pbTranslations,
		},
		Thumbnail:          ConvertEntityToCommonMedia(&e.Product.ProductDisplay.Thumbnail),
		SecondaryThumbnail: pbSecondaryThumbnail,
	}

	// Convert prices - place prices inside nested Product
	pbPrices := convertEntityPricesToPb(e.Prices)

	// Get first translation for slug generation (or empty strings if no translations)
	var firstTranslationName string
	if len(productBody.Translations) > 0 {
		firstTranslationName = productBody.Translations[0].Name
	}

	// sold_out is derived from the sizes' total stock — one shared definition (PR5-B).
	soldOut := entity.SoldOutFromSizes(e.Sizes)

	pbProduct := &pb_common.Product{
		Id:             int32(e.Product.Id),
		CreatedAt:      timestamppb.New(e.Product.CreatedAt),
		UpdatedAt:      timestamppb.New(e.Product.UpdatedAt),
		Slug:           slug.ProductPath(firstTranslationName, e.Product.SKU),
		Sku:            e.Product.SKU,
		ProductDisplay: pbProductDisplay,
		Prices:         pbPrices, // Prices are in nested Product
		SoldOut:        soldOut,
	}

	pbSizes := convertEntitySizesToPbSizes(e.Sizes)
	pbMeasurements := convertEntityMeasurementsToPbMeasurements(e.Measurements)
	pbMedia := ConvertEntityMediaListToPbMedia(e.Media)
	pbTags := convertEntityTagsToPbTags(e.Tags)

	return &pb_common.ProductFull{
		Product:      pbProduct,
		Sizes:        pbSizes,
		Measurements: pbMeasurements,
		Media:        pbMedia,
		Tags:         pbTags,
	}, nil
}

func convertEntityPricesToPb(prices []entity.ProductPrice) []*pb_common.ProductPrice {
	var pbPrices []*pb_common.ProductPrice
	for _, price := range prices {
		pbPrices = append(pbPrices, &pb_common.ProductPrice{
			Currency: price.Currency,
			Price:    &pb_decimal.Decimal{Value: price.Price.String()},
		})
	}
	return pbPrices
}

func convertEntitySizesToPbSizes(sizes []entity.ProductSize) []*pb_common.ProductSize {
	var pbSizes []*pb_common.ProductSize
	for _, size := range sizes {
		pbSizes = append(pbSizes, &pb_common.ProductSize{
			Id: int32(size.Id),
			Quantity: &pb_decimal.Decimal{
				Value: size.Quantity.String(),
			},
			ProductId: int32(size.ProductId),
			SizeId:    int32(size.SizeId),
			Sku:       size.SKU.String,
		})
	}
	return pbSizes
}

func convertEntityMeasurementsToPbMeasurements(measurements []entity.ProductMeasurement) []*pb_common.ProductMeasurement {
	var pbMeasurements []*pb_common.ProductMeasurement
	for _, measurement := range measurements {
		pbMeasurements = append(pbMeasurements, &pb_common.ProductMeasurement{
			Id:                int32(measurement.Id),
			ProductId:         int32(measurement.ProductId),
			ProductSizeId:     int32(measurement.ProductSizeId),
			MeasurementNameId: int32(measurement.MeasurementNameId),
			MeasurementValue: &pb_decimal.Decimal{
				Value: measurement.MeasurementValue.String(),
			},
		})
	}
	return pbMeasurements
}

func convertEntityTagsToPbTags(tags []entity.ProductTag) []*pb_common.ProductTag {
	var pbTags []*pb_common.ProductTag
	for _, tag := range tags {
		pbTags = append(pbTags, &pb_common.ProductTag{
			Id: int32(tag.Id),
			ProductTagInsert: &pb_common.ProductTagInsert{
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
		ProductId:      int32(e.ProductId),
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

// ConvertEntityProductToCommon converts entity.Product to pb_common.Product
func ConvertEntityProductToCommon(e *entity.Product) (*pb_common.Product, error) {
	productBody := &e.ProductDisplay.ProductBody
	productBodyInsert := &productBody.ProductBodyInsert

	tg, _ := ConvertEntityGenderToPbGenderEnum(productBodyInsert.TargetGender)
	sn, _ := ConvertEntitySeasonToPbSeasonEnum(productBodyInsert.Season)

	// Convert translations to protobuf format
	var pbTranslations []*pb_common.ProductInsertTranslation
	for _, trans := range productBody.Translations {
		pbTranslations = append(pbTranslations, &pb_common.ProductInsertTranslation{
			LanguageId:  int32(trans.LanguageId),
			Name:        trans.Name,
			Description: trans.Description,
		})
	}

	// Get first translation for slug generation (or empty strings if no translations)
	var firstTranslationName string
	if len(productBody.Translations) > 0 {
		firstTranslationName = productBody.Translations[0].Name
	}

	var pbSecondaryThumbnail *pb_common.MediaFull
	if e.ProductDisplay.SecondaryThumbnail != nil {
		pbSecondaryThumbnail = ConvertEntityToCommonMedia(e.ProductDisplay.SecondaryThumbnail)
	}

	// Convert prices
	pbPrices := convertEntityPricesToPb(e.Prices)

	pbProduct := &pb_common.Product{
		Id:        int32(e.Id),
		CreatedAt: timestamppb.New(e.CreatedAt),
		UpdatedAt: timestamppb.New(e.UpdatedAt),
		Slug:      slug.ProductPath(firstTranslationName, e.SKU),
		Sku:       e.SKU,
		ProductDisplay: &pb_common.ProductDisplay{
			ProductBody: &pb_common.ProductBody{
				ProductBodyInsert: &pb_common.ProductBodyInsert{
					Preorder:        timestamppb.New(productBodyInsert.Preorder.Time),
					Brand:           productBodyInsert.Brand,
					Color:           productBodyInsert.Color,
					ColorCode:       productBodyInsert.ColorCode.String,
					ColorHex:        productBodyInsert.ColorHex,
					CountryOfOrigin: productBodyInsert.CountryOfOrigin,

					SalePercentage:     &pb_decimal.Decimal{Value: productBodyInsert.SalePercentage.Decimal.String()},
					TopCategoryId:      int32(productBodyInsert.TopCategoryId),
					SubCategoryId:      int32(productBodyInsert.SubCategoryId.Int32),
					TypeId:             int32(productBodyInsert.TypeId.Int32),
					ModelWearsHeightCm: int32(productBodyInsert.ModelWearsHeightCm.Int32),
					ModelWearsSizeId:   int32(productBodyInsert.ModelWearsSizeId.Int32),
					Hidden:             productBodyInsert.Hidden.Bool,
					TargetGender:       tg,
					Season:             sn,
					CareInstructions:   productBodyInsert.CareInstructions.String,
					Composition:        productBodyInsert.Composition.String,
					Version:            productBodyInsert.Version,
					Collection:         productBodyInsert.Collection,
					Fit:                productBodyInsert.Fit.String,
				},
				Translations: pbTranslations,
			},
			Thumbnail:          ConvertEntityToCommonMedia(&e.ProductDisplay.Thumbnail),
			SecondaryThumbnail: pbSecondaryThumbnail,
		},
		Prices:  pbPrices,
		SoldOut: e.SoldOut,
	}

	return pbProduct, nil
}
