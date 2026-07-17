package dto

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/jekabolt/grbpwr-manager/internal/materialattr"
	pb_common "github.com/jekabolt/grbpwr-manager/proto/gen/common"
	pb_decimal "google.golang.org/genproto/googleapis/type/decimal"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// materialClassPbToEntity maps the proto MaterialClass enum to the entity class string. It is the
// entity<->proto leg of the single-source guard (the entity<->DB leg is migrationlint); a missing
// mapping surfaces in TestMaterialClassEnumNoDrift.
var materialClassPbToEntity = map[pb_common.MaterialClass]entity.MaterialClass{
	pb_common.MaterialClass_MATERIAL_CLASS_FABRIC:    entity.MaterialClassFabric,
	pb_common.MaterialClass_MATERIAL_CLASS_HARDWARE:  entity.MaterialClassHardware,
	pb_common.MaterialClass_MATERIAL_CLASS_THREAD:    entity.MaterialClassThread,
	pb_common.MaterialClass_MATERIAL_CLASS_PACKAGING: entity.MaterialClassPackaging,
	pb_common.MaterialClass_MATERIAL_CLASS_OTHER:     entity.MaterialClassOther,
}

func pbMaterialClass(c entity.MaterialClass) pb_common.MaterialClass {
	for k, v := range materialClassPbToEntity {
		if v == c {
			return k
		}
	}
	return pb_common.MaterialClass_MATERIAL_CLASS_UNKNOWN
}

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
	ins := &entity.MaterialInsert{
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
	}
	if err := applyPbMaterialAttrs(pb, ins); err != nil {
		return nil, err
	}
	entries, err := pbCompositionEntriesToEntity(pb.GetCompositionEntries())
	if err != nil {
		return nil, err
	}
	ins.CompositionEntries = entries
	// Fixture enum-validation (S15): reject a bad typed-attribute enum value at the app layer with a
	// field-tagged error, before the DB CHECK would.
	if err := materialattr.Validate(ins); err != nil {
		return nil, err
	}
	return ins, nil
}

// pbCompositionEntriesToEntity maps the wire composition_entries onto entity rows for a material write
// (S17): fibre code upper-cased/trimmed, percent parsed. The dictionary display name is a read-only
// projection and is ignored on write; structural validation (sum/range/duplicates) and dictionary
// existence are enforced by the store (entity.NormalizeMaterialComposition / checkFibersExist).
func pbCompositionEntriesToEntity(pbEntries []*pb_common.CompositionEntry) ([]entity.CompositionEntry, error) {
	if len(pbEntries) == 0 {
		return nil, nil
	}
	out := make([]entity.CompositionEntry, 0, len(pbEntries))
	for i, e := range pbEntries {
		pct, err := nullDecimalFromPb(e.GetPercent())
		if err != nil {
			return nil, fmt.Errorf("composition_entries[%d].percent: %w", i, err)
		}
		if !pct.Valid {
			return nil, entity.NewFieldViolation(fmt.Sprintf("composition_entries[%d].percent", i),
				"percent is required", "", "set each fibre's percent")
		}
		out = append(out, entity.CompositionEntry{
			FiberCode: strings.ToUpper(strings.TrimSpace(e.GetFiberCode())),
			Percent:   pct.Decimal,
		})
	}
	return out, nil
}

// applyPbMaterialAttrs maps the proto CTI class + typed attribute oneof (or the other_attrs JSON
// escape-hatch) onto the entity insert. An UNKNOWN class leaves MaterialClass empty (the store
// normalises it to 'other').
func applyPbMaterialAttrs(pb *pb_common.Material, ins *entity.MaterialInsert) error {
	if mc, ok := materialClassPbToEntity[pb.MaterialClass]; ok {
		ins.MaterialClass = string(mc)
	}
	switch pb.MaterialClass {
	case pb_common.MaterialClass_MATERIAL_CLASS_FABRIC:
		if a := pb.GetFabricAttrs(); a != nil {
			fa, err := pbFabricAttrs(a)
			if err != nil {
				return err
			}
			ins.FabricAttr = fa
		}
	case pb_common.MaterialClass_MATERIAL_CLASS_HARDWARE:
		if a := pb.GetHardwareAttrs(); a != nil {
			ha, err := pbHardwareAttrs(a)
			if err != nil {
				return err
			}
			ins.HardwareAttr = ha
		}
	case pb_common.MaterialClass_MATERIAL_CLASS_THREAD:
		if a := pb.GetThreadAttrs(); a != nil {
			ta, err := pbThreadAttrs(a)
			if err != nil {
				return err
			}
			ins.ThreadAttr = ta
		}
	case pb_common.MaterialClass_MATERIAL_CLASS_PACKAGING:
		if a := pb.GetPackagingAttrs(); a != nil {
			pa, err := pbPackagingAttrs(a)
			if err != nil {
				return err
			}
			ins.PackagingAttr = pa
		}
	case pb_common.MaterialClass_MATERIAL_CLASS_OTHER:
		if s := strings.TrimSpace(pb.GetOtherAttrs()); s != "" {
			if !json.Valid([]byte(s)) {
				return fmt.Errorf("material other_attrs must be a valid JSON object")
			}
			ins.OtherAttrs = []byte(s)
		}
	}
	return nil
}

func pbFabricAttrs(a *pb_common.MaterialFabricAttrs) (*entity.MaterialFabricAttr, error) {
	width, err := nullDecimalFromPb(a.WidthCm)
	if err != nil {
		return nil, fmt.Errorf("fabric_attrs.width_cm: %w", err)
	}
	gsm, err := nullDecimalFromPb(a.WeightGsm)
	if err != nil {
		return nil, fmt.Errorf("fabric_attrs.weight_gsm: %w", err)
	}
	shrink, err := nullDecimalFromPb(a.ShrinkagePct)
	if err != nil {
		return nil, fmt.Errorf("fabric_attrs.shrinkage_pct: %w", err)
	}
	roll, err := nullDecimalFromPb(a.RollLengthM)
	if err != nil {
		return nil, fmt.Errorf("fabric_attrs.roll_length_m: %w", err)
	}
	return &entity.MaterialFabricAttr{
		WidthCm: width, WeightGsm: gsm, FabricDirection: nullStringFromPb(a.FabricDirection),
		ShrinkagePct: shrink, RollLengthM: roll,
	}, nil
}

func pbHardwareAttrs(a *pb_common.MaterialHardwareAttrs) (*entity.MaterialHardwareAttr, error) {
	dia, err := nullDecimalFromPb(a.DiameterMm)
	if err != nil {
		return nil, fmt.Errorf("hardware_attrs.diameter_mm: %w", err)
	}
	wg, err := nullDecimalFromPb(a.WeightG)
	if err != nil {
		return nil, fmt.Errorf("hardware_attrs.weight_g: %w", err)
	}
	return &entity.MaterialHardwareAttr{
		DiameterMm: dia, Dimensions: nullStringFromPb(a.Dimensions), Finish: nullStringFromPb(a.Finish),
		BaseMaterial: nullStringFromPb(a.BaseMaterial), WeightG: wg,
	}, nil
}

func pbThreadAttrs(a *pb_common.MaterialThreadAttrs) (*entity.MaterialThreadAttr, error) {
	length, err := nullDecimalFromPb(a.LengthPerConeM)
	if err != nil {
		return nil, fmt.Errorf("thread_attrs.length_per_cone_m: %w", err)
	}
	return &entity.MaterialThreadAttr{
		TicketTex: nullStringFromPb(a.TicketTex), LengthPerConeM: length, NeedleReco: nullStringFromPb(a.NeedleReco),
	}, nil
}

func pbPackagingAttrs(a *pb_common.MaterialPackagingAttrs) (*entity.MaterialPackagingAttr, error) {
	gsm, err := nullDecimalFromPb(a.Gsm)
	if err != nil {
		return nil, fmt.Errorf("packaging_attrs.gsm: %w", err)
	}
	return &entity.MaterialPackagingAttr{
		Substrate: nullStringFromPb(a.Substrate), Dimensions: nullStringFromPb(a.Dimensions),
		Gsm: gsm, PrintMethod: nullStringFromPb(a.PrintMethod),
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
		LockVersion:     int32(m.LockVersion),
	}
	if m.LatestPrice != nil {
		out.LatestPrice = ConvertEntityMaterialPriceToPb(*m.LatestPrice)
	}
	out.CompositionEntries = compositionEntriesToPb(m.CompositionEntries)
	out.MaterialClass = pbMaterialClass(entity.MaterialClass(m.MaterialClass))
	switch entity.MaterialClass(m.MaterialClass) {
	case entity.MaterialClassFabric:
		if a := m.FabricAttr; a != nil {
			out.Attributes = &pb_common.Material_FabricAttrs{FabricAttrs: &pb_common.MaterialFabricAttrs{
				WidthCm: pbDecimalFromNull(a.WidthCm), WeightGsm: pbDecimalFromNull(a.WeightGsm),
				FabricDirection: pbStringFromNull(a.FabricDirection),
				ShrinkagePct:    pbDecimalFromNull(a.ShrinkagePct), RollLengthM: pbDecimalFromNull(a.RollLengthM),
			}}
		}
	case entity.MaterialClassHardware:
		if a := m.HardwareAttr; a != nil {
			out.Attributes = &pb_common.Material_HardwareAttrs{HardwareAttrs: &pb_common.MaterialHardwareAttrs{
				DiameterMm: pbDecimalFromNull(a.DiameterMm), Dimensions: pbStringFromNull(a.Dimensions),
				Finish: pbStringFromNull(a.Finish), BaseMaterial: pbStringFromNull(a.BaseMaterial),
				WeightG: pbDecimalFromNull(a.WeightG),
			}}
		}
	case entity.MaterialClassThread:
		if a := m.ThreadAttr; a != nil {
			out.Attributes = &pb_common.Material_ThreadAttrs{ThreadAttrs: &pb_common.MaterialThreadAttrs{
				TicketTex: pbStringFromNull(a.TicketTex), LengthPerConeM: pbDecimalFromNull(a.LengthPerConeM),
				NeedleReco: pbStringFromNull(a.NeedleReco),
			}}
		}
	case entity.MaterialClassPackaging:
		if a := m.PackagingAttr; a != nil {
			out.Attributes = &pb_common.Material_PackagingAttrs{PackagingAttrs: &pb_common.MaterialPackagingAttrs{
				Substrate: pbStringFromNull(a.Substrate), Dimensions: pbStringFromNull(a.Dimensions),
				Gsm: pbDecimalFromNull(a.Gsm), PrintMethod: pbStringFromNull(a.PrintMethod),
			}}
		}
	case entity.MaterialClassOther:
		out.OtherAttrs = string(m.OtherAttrs)
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
	if !entity.ValidMaterialPriceSources[source] {
		return entity.MaterialPrice{}, entity.NewFieldViolation("price.source",
			fmt.Sprintf("unknown price source %q", source), "",
			"use one of: manual, production_run, purchase")
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
