package dto

import (
	"database/sql"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
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
		return pb_common.GenderEnum(0), fmt.Errorf("bad entity target gender %v", g)
	}
	return g, nil
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

	price, err := convertDecimal(pbProductBodyInsert.Price.Value)
	if err != nil {
		return nil, fmt.Errorf("failed to convert product price: %w", err)
	}

	salePercentage, err := convertDecimal(pbProductBodyInsert.SalePercentage.Value)
	if err != nil {
		return nil, fmt.Errorf("failed to convert product sale percentage: %w", err)
	}

	targetGender, err := ConvertPbGenderEnumToEntityGenderEnum(pbProductBodyInsert.TargetGender)
	if err != nil {
		return nil, err
	}

	pb := &entity.ProductBody{
		ProductBodyInsert: entity.ProductBodyInsert{
			Preorder:           sql.NullTime{Time: pbProductBodyInsert.Preorder.AsTime(), Valid: pbProductBodyInsert.Preorder.IsValid()},
			Brand:              pbProductBodyInsert.Brand,
			Color:              pbProductBodyInsert.Color,
			ColorHex:           pbProductBodyInsert.ColorHex,
			CountryOfOrigin:    pbProductBodyInsert.CountryOfOrigin,
			Price:              price,
			SalePercentage:     decimal.NullDecimal{Decimal: salePercentage, Valid: pbProductBodyInsert.SalePercentage.Value != ""},
			TopCategoryId:      int(pbProductBodyInsert.TopCategoryId),
			SubCategoryId:      sql.NullInt32{Int32: int32(pbProductBodyInsert.SubCategoryId), Valid: pbProductBodyInsert.SubCategoryId != 0},
			TypeId:             sql.NullInt32{Int32: int32(pbProductBodyInsert.TypeId), Valid: pbProductBodyInsert.TypeId != 0},
			ModelWearsHeightCm: sql.NullInt32{Int32: int32(pbProductBodyInsert.ModelWearsHeightCm), Valid: pbProductBodyInsert.ModelWearsHeightCm != 0},
			ModelWearsSizeId:   sql.NullInt32{Int32: int32(pbProductBodyInsert.ModelWearsSizeId), Valid: pbProductBodyInsert.ModelWearsSizeId != 0},
			Hidden:             sql.NullBool{Bool: pbProductBodyInsert.Hidden, Valid: true},
			TargetGender:       targetGender,
			CareInstructions:   sql.NullString{String: pbProductBodyInsert.CareInstructions, Valid: pbProductBodyInsert.CareInstructions != ""},
			Composition:        sql.NullString{String: pbProductBodyInsert.Composition, Valid: pbProductBodyInsert.Composition != ""},
			Version:            pbProductBodyInsert.Version,
			Collection:         pbProductBodyInsert.Collection,
			Fit:                sql.NullString{String: pbProductBodyInsert.Fit, Valid: pbProductBodyInsert.Fit != ""},
		},
		Translations: []entity.ProductTranslationInsert{}, // Will be set by caller if needed
	}

	if pbProductBodyInsert.Preorder.AsTime().Year() < time.Now().Year() {
		pb.ProductBodyInsert.Preorder.Valid = false
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

	return &entity.ProductInsert{
		ProductBodyInsert:         productBody.ProductBodyInsert,
		ThumbnailMediaID:          int(pbProductNew.ThumbnailMediaId),
		SecondaryThumbnailMediaID: secondaryThumbnailID,
		Translations:              translations,
	}, nil
}

func ConvertPbMeasurementsUpdateToEntity(mUpd []*pb_common.ProductMeasurementUpdate) ([]entity.ProductMeasurementUpdate, error) {
	if mUpd == nil {
		return nil, fmt.Errorf("input pbProductMeasurementUpdate is nil")
	}

	var measurements []entity.ProductMeasurementUpdate
	for _, pbMeasurement := range mUpd {
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

	// Create a ProductBody from ProductBodyInsert
	productBody, err := convertProductBodyInsertToProductBody(pbProductNew.Product.ProductBodyInsert)
	if err != nil {
		return nil, err
	}

	// Convert translations
	var translations []entity.ProductTranslationInsert
	for _, trans := range pbProductNew.Product.Translations {
		translations = append(translations, entity.ProductTranslationInsert{
			LanguageId:  int(trans.LanguageId),
			Name:        trans.Name,
			Description: trans.Description,
		})
	}

	// Set translations on the product body
	productBody.Translations = translations

	productInsert := &entity.ProductInsert{
		ProductBodyInsert: productBody.ProductBodyInsert,
		ThumbnailMediaID:  int(pbProductNew.Product.ThumbnailMediaId),
		SecondaryThumbnailMediaID: sql.NullInt32{
			Int32: pbProductNew.Product.SecondaryThumbnailMediaId,
			Valid: pbProductNew.Product.SecondaryThumbnailMediaId != 0,
		},
		Translations: translations,
	}

	sizeMeasurements, err := convertSizeMeasurements(pbProductNew.SizeMeasurements)
	if err != nil {
		return nil, err
	}

	mediaIds := convertMediaIds(pbProductNew.MediaIds)
	tags := convertTags(pbProductNew.Tags)

	return &entity.ProductNew{
		Product:          productInsert,
		SizeMeasurements: sizeMeasurements,
		MediaIds:         mediaIds,
		Tags:             tags,
	}, nil
}

func convertSizeMeasurements(pbSizeMeasurements []*pb_common.SizeWithMeasurementInsert) ([]entity.SizeWithMeasurementInsert, error) {
	var sizeMeasurements []entity.SizeWithMeasurementInsert
	for _, pbSizeMeasurement := range pbSizeMeasurements {
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

	tg, err := ConvertEntityGenderToPbGenderEnum(productBodyInsert.TargetGender)
	if err != nil {
		return nil, err
	}

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
				Preorder:           timestamppb.New(productBodyInsert.Preorder.Time),
				Brand:              productBodyInsert.Brand,
				Color:              productBodyInsert.Color,
				ColorHex:           productBodyInsert.ColorHex,
				CountryOfOrigin:    productBodyInsert.CountryOfOrigin,
				Price:              &pb_decimal.Decimal{Value: productBodyInsert.Price.String()},
				SalePercentage:     &pb_decimal.Decimal{Value: productBodyInsert.SalePercentage.Decimal.String()},
				TopCategoryId:      int32(productBodyInsert.TopCategoryId),
				SubCategoryId:      int32(productBodyInsert.SubCategoryId.Int32),
				TypeId:             int32(productBodyInsert.TypeId.Int32),
				ModelWearsHeightCm: int32(productBodyInsert.ModelWearsHeightCm.Int32),
				ModelWearsSizeId:   int32(productBodyInsert.ModelWearsSizeId.Int32),
				Hidden:             productBodyInsert.Hidden.Bool,
				TargetGender:       tg,
				CareInstructions:   productBodyInsert.CareInstructions.String,
				Composition:        productBodyInsert.Composition.String,
				Version:            productBodyInsert.Version,
				Collection:         productBodyInsert.Collection,
				Fit:                productBodyInsert.Fit.String,
			},
			Translations: pbTranslations,
		},
		Thumbnail:          ConvertEntityToCommonMedia(&e.Product.ProductDisplay.Thumbnail),
		SecondaryThumbnail: pbSecondaryThumbnail,
	}

	// Get first translation for slug generation (or empty strings if no translations)
	var firstTranslationName string
	if len(productBody.Translations) > 0 {
		firstTranslationName = productBody.Translations[0].Name
	}

	pbProduct := &pb_common.Product{
		Id:             int32(e.Product.Id),
		CreatedAt:      timestamppb.New(e.Product.CreatedAt),
		UpdatedAt:      timestamppb.New(e.Product.UpdatedAt),
		Slug:           GetProductSlug(e.Product.Id, productBodyInsert.Brand, firstTranslationName, productBodyInsert.TargetGender.String()),
		Sku:            e.Product.SKU,
		ProductDisplay: pbProductDisplay,
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

var reg = regexp.MustCompile("[^a-zA-Z0-9]+")

func GetProductSlug(id int, brand, name, gender string) string {
	clean := func(part string) string {
		// Replace all non-alphanumeric characters with an empty string
		return reg.ReplaceAllString(part, "")
	}

	// Use strings.Builder for efficient string concatenation
	var sb strings.Builder
	sb.WriteString("/product/")
	sb.WriteString(gender)
	sb.WriteString("/")
	sb.WriteString(clean(brand))
	sb.WriteString("/")
	sb.WriteString(clean(name))
	sb.WriteString("/")
	sb.WriteString(fmt.Sprint(id))

	return sb.String()
}

// ConvertEntityProductToCommon converts entity.Product to pb_common.Product
func ConvertEntityProductToCommon(e *entity.Product) (*pb_common.Product, error) {
	productBody := &e.ProductDisplay.ProductBody
	productBodyInsert := &productBody.ProductBodyInsert

	tg, err := ConvertEntityGenderToPbGenderEnum(productBodyInsert.TargetGender)
	if err != nil {
		return nil, err
	}

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

	pbProduct := &pb_common.Product{
		Id:        int32(e.Id),
		CreatedAt: timestamppb.New(e.CreatedAt),
		UpdatedAt: timestamppb.New(e.UpdatedAt),
		Slug:      GetProductSlug(e.Id, productBodyInsert.Brand, firstTranslationName, productBodyInsert.TargetGender.String()),
		Sku:       e.SKU,
		ProductDisplay: &pb_common.ProductDisplay{
			ProductBody: &pb_common.ProductBody{
				ProductBodyInsert: &pb_common.ProductBodyInsert{
					Preorder:           timestamppb.New(productBodyInsert.Preorder.Time),
					Brand:              productBodyInsert.Brand,
					Color:              productBodyInsert.Color,
					ColorHex:           productBodyInsert.ColorHex,
					CountryOfOrigin:    productBodyInsert.CountryOfOrigin,
					Price:              &pb_decimal.Decimal{Value: productBodyInsert.Price.String()},
					SalePercentage:     &pb_decimal.Decimal{Value: productBodyInsert.SalePercentage.Decimal.String()},
					TopCategoryId:      int32(productBodyInsert.TopCategoryId),
					SubCategoryId:      int32(productBodyInsert.SubCategoryId.Int32),
					TypeId:             int32(productBodyInsert.TypeId.Int32),
					ModelWearsHeightCm: int32(productBodyInsert.ModelWearsHeightCm.Int32),
					ModelWearsSizeId:   int32(productBodyInsert.ModelWearsSizeId.Int32),
					Hidden:             productBodyInsert.Hidden.Bool,
					TargetGender:       tg,
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
	}

	return pbProduct, nil
}
