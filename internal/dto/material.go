package dto

import (
	"fmt"
	"strings"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	pb_common "github.com/jekabolt/grbpwr-manager/proto/gen/common"
	pb_decimal "google.golang.org/genproto/googleapis/type/decimal"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// ConvertPbMaterialToEntityInsert validates and converts a common.Material into the editable
// MaterialInsert (server-managed fields — id, archived, latest_price — are ignored).
func ConvertPbMaterialToEntityInsert(pb *pb_common.Material) (*entity.MaterialInsert, error) {
	if pb == nil {
		return nil, fmt.Errorf("material is required")
	}
	name := strings.TrimSpace(pb.Name)
	if name == "" {
		return nil, fmt.Errorf("material name is required")
	}
	if len(name) > maxVarchar255 {
		return nil, fmt.Errorf("material name must be at most %d characters", maxVarchar255)
	}
	section, ok := techCardBomSectionPbToEntity[pb.Section]
	if !ok {
		return nil, fmt.Errorf("material section is required and must be valid")
	}
	for _, c := range []struct {
		field, val string
		max        int
	}{
		{"supplier", pb.Supplier, maxVarchar255},
		{"supplier_ref", pb.SupplierRef, maxVarchar255},
		{"composition", pb.Composition, maxVarchar255},
		{"spec", pb.Spec, maxVarchar255},
		{"unit", pb.Unit, maxVarchar32},
	} {
		if len(c.val) > c.max {
			return nil, fmt.Errorf("material %s must be at most %d characters", c.field, c.max)
		}
	}
	fabricWidth, err := nullDecimalFromPb(pb.FabricWidth)
	if err != nil {
		return nil, fmt.Errorf("material fabric_width: %w", err)
	}
	fabricGsm, err := nullDecimalFromPb(pb.FabricWeightGsm)
	if err != nil {
		return nil, fmt.Errorf("material fabric_weight_gsm: %w", err)
	}
	if len(pb.Code) > maxVarchar64 {
		return nil, fmt.Errorf("material code must be at most %d characters", maxVarchar64)
	}
	if len(pb.Color) > maxVarchar64 || len(pb.Pantone) > maxVarchar32 {
		return nil, fmt.Errorf("material color/pantone too long")
	}
	minStock, err := nullDecimalFromPb(pb.MinStock)
	if err != nil {
		return nil, fmt.Errorf("material min_stock: %w", err)
	}
	if minStock.Valid && minStock.Decimal.IsNegative() {
		return nil, fmt.Errorf("material min_stock must be non-negative")
	}
	return &entity.MaterialInsert{
		Name:            name,
		Section:         string(section),
		Supplier:        nullStringFromPb(pb.Supplier),
		SupplierRef:     nullStringFromPb(pb.SupplierRef),
		Composition:     nullStringFromPb(pb.Composition),
		Spec:            nullStringFromPb(pb.Spec),
		Unit:            nullStringFromPb(pb.Unit),
		FabricWidth:     fabricWidth,
		FabricWeightGsm: fabricGsm,
		Code:            nullStringFromPb(pb.Code),
		Color:           nullStringFromPb(pb.Color),
		Pantone:         nullStringFromPb(pb.Pantone),
		MinStock:        minStock,
		Notes:           nullStringFromPb(pb.Notes),
	}, nil
}

// ConvertEntityMaterialToPb converts a catalog material (with its current price, if any) to pb.
func ConvertEntityMaterialToPb(m entity.MaterialWithPrice) *pb_common.Material {
	out := &pb_common.Material{
		Id:              int64(m.Id),
		Name:            m.Name,
		Section:         pbBomSection(entity.TechCardBomSection(m.Section)),
		Supplier:        pbStringFromNull(m.Supplier),
		SupplierRef:     pbStringFromNull(m.SupplierRef),
		Composition:     pbStringFromNull(m.Composition),
		Spec:            pbStringFromNull(m.Spec),
		Unit:            pbStringFromNull(m.Unit),
		FabricWidth:     pbDecimalFromNull(m.FabricWidth),
		FabricWeightGsm: pbDecimalFromNull(m.FabricWeightGsm),
		Archived:        m.Archived,
		Code:            pbStringFromNull(m.Code),
		Color:           pbStringFromNull(m.Color),
		Pantone:         pbStringFromNull(m.Pantone),
		MinStock:        pbDecimalFromNull(m.MinStock),
		Notes:           pbStringFromNull(m.Notes),
	}
	if m.LatestPrice != nil {
		out.LatestPrice = ConvertEntityMaterialPriceToPb(*m.LatestPrice)
	}
	return out
}

// ConvertPbMaterialPriceToEntity validates and converts a common.MaterialPrice for AddMaterialPrice.
func ConvertPbMaterialPriceToEntity(pb *pb_common.MaterialPrice) (entity.MaterialPrice, error) {
	if pb == nil {
		return entity.MaterialPrice{}, fmt.Errorf("price is required")
	}
	if pb.MaterialId <= 0 {
		return entity.MaterialPrice{}, fmt.Errorf("material_id is required")
	}
	price, err := nullDecimalFromPb(pb.Price)
	if err != nil {
		return entity.MaterialPrice{}, fmt.Errorf("price: %w", err)
	}
	if !price.Valid || price.Decimal.IsNegative() {
		return entity.MaterialPrice{}, fmt.Errorf("price must be a non-negative number")
	}
	currency := strings.ToUpper(strings.TrimSpace(pb.Currency))
	if len(currency) != maxCurrency {
		return entity.MaterialPrice{}, fmt.Errorf("currency must be a 3-letter ISO 4217 code")
	}
	if pb.ValidFrom == nil {
		return entity.MaterialPrice{}, fmt.Errorf("valid_from is required")
	}
	source := strings.TrimSpace(pb.Source)
	if source == "" {
		source = entity.MaterialPriceSourceManual
	}
	return entity.MaterialPrice{
		MaterialId: int(pb.MaterialId),
		Price:      price.Decimal,
		Currency:   currency,
		ValidFrom:  pb.ValidFrom.AsTime(),
		Source:     source,
		Note:       nullStringFromPb(pb.Note),
	}, nil
}

// ConvertEntityMaterialPriceToPb converts a price-history point to pb.
func ConvertEntityMaterialPriceToPb(p entity.MaterialPrice) *pb_common.MaterialPrice {
	return &pb_common.MaterialPrice{
		MaterialId: int64(p.MaterialId),
		Price:      &pb_decimal.Decimal{Value: p.Price.String()},
		Currency:   p.Currency,
		ValidFrom:  timestamppb.New(p.ValidFrom),
		Source:     p.Source,
		Note:       pbStringFromNull(p.Note),
	}
}
