package dto

import (
	"database/sql"
	"fmt"
	"strings"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	pb_common "github.com/jekabolt/grbpwr-manager/proto/gen/common"
	"github.com/shopspring/decimal"
	"google.golang.org/protobuf/types/known/timestamppb"
)

func ConvertPbGenderEnumToEntityGenderEnum(pbGenderEnum pb_common.GenderEnum) (entity.GenderEnum, error) {
	tg, ok := pb_common.GenderEnum_value[strings.ToUpper(string(pbGenderEnum))]
	if !ok {
		return "", fmt.Errorf("bad target gender")
	}
	return entity.GenderEnum(tg), nil
}

func ConvertFromPbToEntity(pbProductNew *pb_common.ProductNew) (*entity.ProductNew, error) {
	if pbProductNew == nil {
		return nil, fmt.Errorf("input pbProductNew is nil")
	}

	// Convert ProductInsert
	price, err := decimal.NewFromString(pbProductNew.Product.Price)
	if err != nil {
		return nil, err
	}
	if pbProductNew.Product.SalePercentage == "" {
		pbProductNew.Product.SalePercentage = "0"
	}
	salePercentage, err := decimal.NewFromString(pbProductNew.Product.SalePercentage)
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
		SalePercentage:  decimal.NullDecimal{Decimal: salePercentage, Valid: pbProductNew.Product.SalePercentage != ""},
		CategoryID:      int(pbProductNew.Product.CategoryId),
		Description:     pbProductNew.Product.Description,
		Hidden:          sql.NullBool{Bool: pbProductNew.Product.Hidden, Valid: true},
		TargetGender:    entity.GenderEnum(pbProductNew.Product.TargetGender.String()),
	}

	// Convert SizeMeasurements
	var sizeMeasurements []entity.SizeWithMeasurementInsert
	for _, pbSizeMeasurement := range pbProductNew.SizeMeasurements {
		quantity, err := decimal.NewFromString(pbSizeMeasurement.ProductSize.Quantity)
		if err != nil {
			return nil, err
		}

		productSize := &entity.ProductSizeInsert{
			Quantity: quantity,
			SizeID:   int(pbSizeMeasurement.ProductSize.SizeId),
		}

		var measurements []entity.ProductMeasurementInsert
		for _, pbMeasurement := range pbSizeMeasurement.Measurements {
			measurementValue, err := decimal.NewFromString(pbMeasurement.MeasurementValue)
			if err != nil {
				return nil, err
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
	tg, ok := pb_common.GenderEnum_value[strings.ToUpper(string(e.Product.TargetGender))]
	if !ok {
		return nil, fmt.Errorf("bad target gender")
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
		Price:           e.Product.Price.String(),
		SalePercentage:  e.Product.SalePercentage.Decimal.String(),
		CategoryId:      int32(e.Product.CategoryID),
		Description:     e.Product.Description,
		Hidden:          e.Product.Hidden.Bool,
		TargetGender:    pb_common.GenderEnum(tg),
	}

	pbProduct := &pb_common.Product{
		Id:            int32(e.Product.ID),
		CreatedAt:     timestamppb.New(e.Product.CreatedAt),
		UpdatedAt:     timestamppb.New(e.Product.UpdatedAt),
		ProductInsert: pbProductInsert,
	}

	var pbSizes []*pb_common.ProductSize
	for _, size := range e.Sizes {
		pbSizes = append(pbSizes, &pb_common.ProductSize{
			Id:        int32(size.ID),
			Quantity:  size.Quantity.String(),
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
			MeasurementValue:  measurement.MeasurementValue.String(),
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

// ConvertEntityProductToPb converts entity.Product to pb_common.Product
func ConvertEntityProductToPb(entityProduct *entity.Product) (*pb_common.Product, error) {
	tg, ok := pb_common.GenderEnum_value[strings.ToUpper(string(entityProduct.TargetGender))]
	if !ok {
		return nil, fmt.Errorf("bad target gender")
	}
	pbProduct := &pb_common.Product{
		Id:        int32(entityProduct.ID),
		CreatedAt: timestamppb.New(entityProduct.CreatedAt),
		UpdatedAt: timestamppb.New(entityProduct.UpdatedAt),
		ProductInsert: &pb_common.ProductInsert{
			Preorder:        entityProduct.Preorder.String,
			Name:            entityProduct.Name,
			Brand:           entityProduct.Brand,
			Sku:             entityProduct.SKU,
			Color:           entityProduct.Color,
			ColorHex:        entityProduct.ColorHex,
			CountryOfOrigin: entityProduct.CountryOfOrigin,
			Thumbnail:       entityProduct.Thumbnail,
			Price:           entityProduct.Price.String(),
			SalePercentage:  entityProduct.SalePercentage.Decimal.String(),
			CategoryId:      int32(entityProduct.CategoryID),
			Description:     entityProduct.Description,
			Hidden:          entityProduct.Hidden.Bool,
			TargetGender:    pb_common.GenderEnum(tg),
		},
	}

	return pbProduct, nil
}
