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

func convertProductBody(pbProductBody *pb_common.ProductBody) (*entity.ProductBody, error) {
	price, err := convertDecimal(pbProductBody.Price.Value)
	if err != nil {
		return nil, fmt.Errorf("failed to convert product price: %w", err)
	}

	salePercentage, err := convertDecimal(pbProductBody.SalePercentage.Value)
	if err != nil {
		return nil, fmt.Errorf("failed to convert product sale percentage: %w", err)
	}

	targetGender, err := ConvertPbGenderEnumToEntityGenderEnum(pbProductBody.TargetGender)
	if err != nil {
		return nil, err
	}

	pb := &entity.ProductBody{
		Preorder:         sql.NullTime{Time: pbProductBody.Preorder.AsTime(), Valid: pbProductBody.Preorder.IsValid()},
		Name:             pbProductBody.Name,
		Brand:            pbProductBody.Brand,
		SKU:              pbProductBody.Sku,
		Color:            pbProductBody.Color,
		ColorHex:         pbProductBody.ColorHex,
		CountryOfOrigin:  pbProductBody.CountryOfOrigin,
		Price:            price,
		SalePercentage:   decimal.NullDecimal{Decimal: salePercentage, Valid: pbProductBody.SalePercentage.Value != ""},
		CategoryId:       int(pbProductBody.CategoryId),
		Description:      pbProductBody.Description,
		Hidden:           sql.NullBool{Bool: pbProductBody.Hidden, Valid: true},
		TargetGender:     targetGender,
		CareInstructions: sql.NullString{String: pbProductBody.CareInstructions, Valid: pbProductBody.CareInstructions != ""},
		Composition:      sql.NullString{String: pbProductBody.Composition, Valid: pbProductBody.Composition != ""},
	}

	if pbProductBody.Preorder.AsTime().Year() < time.Now().Year() {
		pb.Preorder.Valid = false
	}

	return pb, nil
}

func ConvertPbProductInsertToEntity(pbProductNew *pb_common.ProductInsert) (*entity.ProductInsert, error) {
	if pbProductNew == nil {
		return nil, fmt.Errorf("input pbProductNew is nil")
	}

	productBody, err := convertProductBody(pbProductNew.ProductBody)
	if err != nil {
		return nil, err
	}

	return &entity.ProductInsert{
		ProductBody:      *productBody,
		ThumbnailMediaID: int(pbProductNew.ThumbnailMediaId),
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

	productBody, err := convertProductBody(pbProductNew.Product.ProductBody)
	if err != nil {
		return nil, err
	}

	productInsert := &entity.ProductInsert{
		ProductBody:      *productBody,
		ThumbnailMediaID: int(pbProductNew.Product.ThumbnailMediaId),
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

	tg, err := ConvertEntityGenderToPbGenderEnum(e.Product.TargetGender)
	if err != nil {
		return nil, err
	}

	pbProductDisplay := &pb_common.ProductDisplay{
		ProductBody: &pb_common.ProductBody{
			Preorder:         timestamppb.New(e.Product.Preorder.Time),
			Name:             e.Product.Name,
			Brand:            e.Product.Brand,
			Sku:              e.Product.SKU,
			Color:            e.Product.Color,
			ColorHex:         e.Product.ColorHex,
			CountryOfOrigin:  e.Product.CountryOfOrigin,
			Price:            &pb_decimal.Decimal{Value: e.Product.Price.String()},
			SalePercentage:   &pb_decimal.Decimal{Value: e.Product.SalePercentage.Decimal.String()},
			CategoryId:       int32(e.Product.CategoryId),
			Description:      e.Product.Description,
			Hidden:           e.Product.Hidden.Bool,
			TargetGender:     tg,
			CareInstructions: e.Product.CareInstructions.String,
			Composition:      e.Product.Composition.String,
		},
		Thumbnail: ConvertEntityToCommonMedia(&e.Product.MediaFull),
	}

	pbProduct := &pb_common.Product{
		Id:             int32(e.Product.Id),
		CreatedAt:      timestamppb.New(e.Product.CreatedAt),
		UpdatedAt:      timestamppb.New(e.Product.UpdatedAt),
		Slug:           GetProductSlug(e.Product.Id, e.Product.Brand, e.Product.Name, e.Product.TargetGender.String()),
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
	tg, err := ConvertEntityGenderToPbGenderEnum(e.TargetGender)
	if err != nil {
		return nil, err
	}

	pbProduct := &pb_common.Product{
		Id:        int32(e.Id),
		CreatedAt: timestamppb.New(e.CreatedAt),
		UpdatedAt: timestamppb.New(e.UpdatedAt),
		Slug:      GetProductSlug(e.Id, e.Brand, e.Name, e.TargetGender.String()),
		ProductDisplay: &pb_common.ProductDisplay{
			ProductBody: &pb_common.ProductBody{
				Preorder:         timestamppb.New(e.Preorder.Time),
				Name:             e.Name,
				Brand:            e.Brand,
				Sku:              e.SKU,
				Color:            e.Color,
				ColorHex:         e.ColorHex,
				CountryOfOrigin:  e.CountryOfOrigin,
				Price:            &pb_decimal.Decimal{Value: e.Price.String()},
				SalePercentage:   &pb_decimal.Decimal{Value: e.SalePercentage.Decimal.String()},
				CategoryId:       int32(e.CategoryId),
				Description:      e.Description,
				Hidden:           e.Hidden.Bool,
				TargetGender:     tg,
				CareInstructions: e.CareInstructions.String,
				Composition:      e.Composition.String,
			},
			Thumbnail: ConvertEntityToCommonMedia(&e.MediaFull),
		},
	}

	return pbProduct, nil
}
