package dto

import (
	"database/sql"
	"fmt"
	"strconv"
	"strings"

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

func ConvertPbProductInsertToEntity(pbProductNew *pb_common.ProductInsert) (*entity.ProductInsert, error) {
	if pbProductNew == nil {
		return nil, fmt.Errorf("input pbProductNew is nil")
	}

	// Convert ProductInsert
	price, err := decimal.NewFromString(pbProductNew.Price.Value)
	if err != nil {
		return nil, fmt.Errorf("failed to convert product price: %w", err)
	}
	salePercentage, err := decimal.NewFromString(pbProductNew.SalePercentage.Value)
	if err != nil {
		if pbProductNew.SalePercentage.Value == "" {
			salePercentage = decimal.Zero
		} else {
			return nil, fmt.Errorf("failed to convert product sale percentage: %w", err)
		}
	}
	targetGender, err := ConvertPbGenderEnumToEntityGenderEnum(pbProductNew.TargetGender)
	if err != nil {
		return nil, err
	}

	return &entity.ProductInsert{
		Preorder:        sql.NullString{String: pbProductNew.Preorder, Valid: pbProductNew.Preorder != ""},
		Name:            pbProductNew.Name,
		Brand:           pbProductNew.Brand,
		SKU:             pbProductNew.Sku,
		Color:           pbProductNew.Color,
		ColorHex:        pbProductNew.ColorHex,
		CountryOfOrigin: pbProductNew.CountryOfOrigin,
		Thumbnail:       pbProductNew.Thumbnail,
		Price:           price,
		SalePercentage:  decimal.NullDecimal{Decimal: salePercentage, Valid: pbProductNew.SalePercentage.Value != ""},
		CategoryID:      int(pbProductNew.CategoryId),
		Description:     pbProductNew.Description,
		Hidden:          sql.NullBool{Bool: pbProductNew.Hidden, Valid: true},
		TargetGender:    targetGender,
	}, nil

}

func ConvertPbMeasurementsUpdateToEntity(mUpd []*pb_common.ProductMeasurementUpdate) ([]entity.ProductMeasurementUpdate, error) {
	if mUpd == nil {
		return nil, fmt.Errorf("input pbProductMeasurementUpdate is nil")
	}

	var measurements []entity.ProductMeasurementUpdate

	for _, pbMeasurement := range mUpd {
		measurementValue, err := decimal.NewFromString(pbMeasurement.MeasurementValue.Value)
		if err != nil {
			return nil, fmt.Errorf("failed to convert product measurement value: %w", err)
		}

		measurement := entity.ProductMeasurementUpdate{
			SizeId:            int(pbMeasurement.SizeId),
			MeasurementNameId: int(pbMeasurement.MeasurementNameId),
			MeasurementValue:  measurementValue,
		}

		measurements = append(measurements, measurement)
	}

	return measurements, nil
}

func ConvertCommonProductToEntity(pbProductNew *pb_common.ProductNew) (*entity.ProductNew, error) {
	if pbProductNew == nil {
		return nil, fmt.Errorf("input pbProductNew is nil")
	}

	// Convert ProductInsert
	price, err := decimal.NewFromString(pbProductNew.Product.Price.Value)
	if err != nil {
		return nil, fmt.Errorf("failed to convert product price: %w", err)
	}
	salePercentage, err := decimal.NewFromString(pbProductNew.Product.SalePercentage.Value)
	if err != nil {
		if pbProductNew.Product.SalePercentage.Value == "" {
			salePercentage = decimal.Zero
		} else {
			return nil, fmt.Errorf("failed to convert product sale percentage: %w", err)
		}
	}
	targetGender, err := ConvertPbGenderEnumToEntityGenderEnum(pbProductNew.Product.TargetGender)
	if err != nil {
		return nil, err
	}

	productInsert := &entity.ProductInsert{
		Preorder:        sql.NullString{String: pbProductNew.Product.Preorder, Valid: pbProductNew.Product.Preorder != ""},
		Name:            pbProductNew.Product.Name,
		Brand:           pbProductNew.Product.Brand,
		SKU:             pbProductNew.Product.Sku,
		Color:           pbProductNew.Product.Color,
		ColorHex:        pbProductNew.Product.ColorHex,
		CountryOfOrigin: pbProductNew.Product.CountryOfOrigin,
		Thumbnail:       pbProductNew.Product.Thumbnail,
		Price:           price,
		SalePercentage:  decimal.NullDecimal{Decimal: salePercentage, Valid: pbProductNew.Product.SalePercentage.Value != ""},
		CategoryID:      int(pbProductNew.Product.CategoryId),
		Description:     pbProductNew.Product.Description,
		Hidden:          sql.NullBool{Bool: pbProductNew.Product.Hidden, Valid: true},
		TargetGender:    targetGender,
	}

	// Convert SizeMeasurements
	var sizeMeasurements []entity.SizeWithMeasurementInsert
	for _, pbSizeMeasurement := range pbProductNew.SizeMeasurements {
		quantity, err := decimal.NewFromString(pbSizeMeasurement.ProductSize.Quantity.Value)
		if err != nil {
			return nil, fmt.Errorf("failed to convert product size quantity: %w for size id  %v", err, pbSizeMeasurement.ProductSize.SizeId)
		}

		productSize := &entity.ProductSizeInsert{
			Quantity: quantity,
			SizeID:   int(pbSizeMeasurement.ProductSize.SizeId),
		}

		var measurements []entity.ProductMeasurementInsert
		for _, pbMeasurement := range pbSizeMeasurement.Measurements {
			measurementValue, err := decimal.NewFromString(pbMeasurement.MeasurementValue.Value)
			if err != nil {
				return nil, fmt.Errorf("failed to convert product measurement value: %w for measurement name id %v", err, pbMeasurement.MeasurementNameId)
			}

			measurement := entity.ProductMeasurementInsert{
				MeasurementNameID: int(pbMeasurement.MeasurementNameId),
				MeasurementValue:  measurementValue,
			}

			measurements = append(measurements, measurement)
		}

		sizeMeasurement := entity.SizeWithMeasurementInsert{
			ProductSize:  *productSize,
			Measurements: measurements,
		}

		sizeMeasurements = append(sizeMeasurements, sizeMeasurement)
	}

	// Convert Media
	var media []entity.ProductMediaInsert
	for _, pbMedia := range pbProductNew.Media {
		mediaInsert := entity.ProductMediaInsert{
			FullSize:   pbMedia.FullSize,
			Thumbnail:  pbMedia.Thumbnail,
			Compressed: pbMedia.Compressed,
		}
		media = append(media, mediaInsert)
	}

	// Convert Tags
	var tags []entity.ProductTagInsert
	for _, pbTag := range pbProductNew.Tags {
		tagInsert := entity.ProductTagInsert{
			Tag: pbTag.Tag,
		}
		tags = append(tags, tagInsert)
	}

	return &entity.ProductNew{
		Product:          productInsert,
		SizeMeasurements: sizeMeasurements,
		Media:            media,
		Tags:             tags,
	}, nil
}

func ConvertToPbProductFull(e *entity.ProductFull) (*pb_common.ProductFull, error) {
	if e == nil {
		return nil, nil
	}
	tg, err := ConvertEntityGenderToPbGenderEnum(e.Product.TargetGender)
	if err != nil {
		return nil, err
	}

	pbProductInsert := &pb_common.ProductInsert{
		Preorder:        e.Product.Preorder.String,
		Name:            e.Product.Name,
		Brand:           e.Product.Brand,
		Sku:             e.Product.SKU,
		Color:           e.Product.Color,
		ColorHex:        e.Product.ColorHex,
		CountryOfOrigin: e.Product.CountryOfOrigin,
		Thumbnail:       e.Product.Thumbnail,
		Price:           &pb_decimal.Decimal{Value: e.Product.Price.String()},
		SalePercentage:  &pb_decimal.Decimal{Value: e.Product.SalePercentage.Decimal.String()},
		CategoryId:      int32(e.Product.CategoryID),
		Description:     e.Product.Description,
		Hidden:          e.Product.Hidden.Bool,
		TargetGender:    tg,
	}

	pbProduct := &pb_common.Product{
		Id:            int32(e.Product.ID),
		CreatedAt:     timestamppb.New(e.Product.CreatedAt),
		UpdatedAt:     timestamppb.New(e.Product.UpdatedAt),
		Slug:          GetSlug(e.Product),
		ProductInsert: pbProductInsert,
	}

	var pbSizes []*pb_common.ProductSize
	for _, size := range e.Sizes {
		pbSizes = append(pbSizes, &pb_common.ProductSize{
			Id: int32(size.ID),
			Quantity: &pb_decimal.Decimal{
				Value: size.Quantity.String(),
			},
			ProductId: int32(size.ProductID),
			SizeId:    int32(size.SizeID),
		})
	}

	var pbMeasurements []*pb_common.ProductMeasurement
	for _, measurement := range e.Measurements {
		pbMeasurements = append(pbMeasurements, &pb_common.ProductMeasurement{
			Id:                int32(measurement.ID),
			ProductId:         int32(measurement.ProductID),
			ProductSizeId:     int32(measurement.ProductSizeID),
			MeasurementNameId: int32(measurement.MeasurementNameID),
			MeasurementValue: &pb_decimal.Decimal{
				Value: measurement.MeasurementValue.String(),
			},
		})
	}

	var pbMedia []*pb_common.ProductMedia
	for _, media := range e.Media {

		pbMedia = append(pbMedia, &pb_common.ProductMedia{
			Id:        int32(media.ID),
			CreatedAt: timestamppb.New(media.CreatedAt),
			ProductId: int32(media.ProductID),
			ProductMediaInsert: &pb_common.ProductMediaInsert{
				FullSize:   media.FullSize,
				Thumbnail:  media.Thumbnail,
				Compressed: media.Compressed,
			},
		})
	}

	var pbTags []*pb_common.ProductTag
	for _, tag := range e.Tags {
		pbTags = append(pbTags, &pb_common.ProductTag{
			Id: int32(tag.ID),
			ProductTagInsert: &pb_common.ProductTagInsert{
				Tag: tag.Tag,
			},
		})
	}

	return &pb_common.ProductFull{
		Product:      pbProduct,
		Sizes:        pbSizes,
		Measurements: pbMeasurements,
		Media:        pbMedia,
		Tags:         pbTags,
	}, nil
}

func GetSlug(ePrd *entity.Product) string {
	return strings.ReplaceAll(strings.ToLower(fmt.Sprintf("%d_%d_%s_%s_%s", ePrd.ID, ePrd.CategoryID, ePrd.Brand, ePrd.Color, ePrd.Name)), " ", "-")
}

// returns product id + name or error
func ParseSlug(slug string) (int, string, error) {
	parts := strings.Split(slug, "_")
	if len(parts) < 5 {
		return 0, "", fmt.Errorf("invalid slug format")
	}

	idStr := parts[0]
	productID, err := strconv.Atoi(idStr)
	if err != nil {
		return 0, "", fmt.Errorf("invalid product ID: %v", err)
	}

	// Reassemble the name part in case it contains underscores
	nameParts := parts[4:]
	name := strings.Join(nameParts, "_")
	name = strings.ReplaceAll(name, "-", " ")

	return productID, name, nil
}

// ConvertEntityProductToCommon converts entity.Product to pb_common.Product
func ConvertEntityProductToCommon(entityProduct *entity.Product) (*pb_common.Product, error) {
	tg, err := ConvertEntityGenderToPbGenderEnum(entityProduct.TargetGender)
	if err != nil {
		return nil, err
	}
	pbProduct := &pb_common.Product{
		Id:        int32(entityProduct.ID),
		CreatedAt: timestamppb.New(entityProduct.CreatedAt),
		UpdatedAt: timestamppb.New(entityProduct.UpdatedAt),
		Slug:      GetSlug(entityProduct),
		ProductInsert: &pb_common.ProductInsert{
			Preorder:        entityProduct.Preorder.String,
			Name:            entityProduct.Name,
			Brand:           entityProduct.Brand,
			Sku:             entityProduct.SKU,
			Color:           entityProduct.Color,
			ColorHex:        entityProduct.ColorHex,
			CountryOfOrigin: entityProduct.CountryOfOrigin,
			Thumbnail:       entityProduct.Thumbnail,
			Price: &pb_decimal.Decimal{
				Value: entityProduct.Price.String(),
			},
			SalePercentage: &pb_decimal.Decimal{Value: entityProduct.SalePercentage.Decimal.String()},
			CategoryId:     int32(entityProduct.CategoryID),
			Description:    entityProduct.Description,
			Hidden:         entityProduct.Hidden.Bool,
			TargetGender:   tg,
		},
	}

	return pbProduct, nil
}
