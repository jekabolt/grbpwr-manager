package dto

import (
	"fmt"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	pb_admin "github.com/jekabolt/grbpwr-manager/proto/gen/admin"
	pb_common "github.com/jekabolt/grbpwr-manager/proto/gen/common"
)

const (
	// style_assembly.qty is DECIMAL(12,3) (0174): at most 3 fraction digits and 9 integer digits.
	assemblyQtyMaxFrac = 3
	assemblyQtyLimit   = 1_000_000_000
)

// ConvertPbStyleAssemblyToEntity validates and converts writable assembly lines (WS7, §2.8). qty must be
// > 0; size_id is optional (0 = all sizes). Duplicate (component, size) pairs are rejected here for a
// clean InvalidArgument; the store re-checks and also enforces the auxiliary-component invariant.
func ConvertPbStyleAssemblyToEntity(items []*pb_admin.StyleAssemblyItem) ([]entity.StyleAssemblyInsert, error) {
	out := make([]entity.StyleAssemblyInsert, 0, len(items))
	seen := map[[2]int32]bool{}
	for i, it := range items {
		if it == nil {
			continue
		}
		if it.ComponentTechCardId <= 0 {
			return nil, fmt.Errorf("items[%d]: component_tech_card_id is required", i)
		}
		key := [2]int32{it.ComponentTechCardId, it.SizeId}
		if seen[key] {
			return nil, fmt.Errorf("items[%d]: duplicate component_tech_card_id %d for the same size", i, it.ComponentTechCardId)
		}
		seen[key] = true
		// Parsed WITHOUT the rounding parseNonNegDecimal applies: style_assembly.qty is
		// DECIMAL(12,3), so a finer value has to be rejected rather than quietly rounded into the
		// column (0.0005 → 0.001 is a 2× error on a per-garment component count).
		qty, err := nullDecimalFromPb(it.Qty)
		if err != nil {
			return nil, entity.NewFieldViolation(fmt.Sprintf("items[%d].qty", i), "not_a_number", "", "enter a number")
		}
		if !qty.Valid || !qty.Decimal.IsPositive() {
			return nil, fmt.Errorf("items[%d].qty must be > 0", i)
		}
		if err := validateDecimalFits(fmt.Sprintf("items[%d].qty", i), qty.Decimal,
			assemblyQtyMaxFrac, assemblyQtyLimit, false); err != nil {
			return nil, err
		}
		out = append(out, entity.StyleAssemblyInsert{
			ComponentTechCardId: int(it.ComponentTechCardId),
			SizeId:              nullInt32FromPb(it.SizeId),
			Qty:                 qty.Decimal,
			PrintNote:           nullStringFromPb(it.PrintNote),
			PositionNote:        nullStringFromPb(it.PositionNote),
			Active:              it.Active,
		})
	}
	return out, nil
}

// StyleAssemblyLineToPb converts a resolved stored assembly line to protobuf.
func StyleAssemblyLineToPb(a entity.StyleAssembly) *pb_admin.StyleAssemblyLine {
	pb := &pb_admin.StyleAssemblyLine{
		Id:                  int32(a.Id),
		StyleId:             int32(a.StyleId),
		ComponentTechCardId: int32(a.ComponentTechCardId),
		ComponentName:       a.ComponentName,
		ComponentAuxSubtype: techCardAuxSubtypeToPb(a.ComponentAuxSubtype),
		Qty:                 pbDecimalFromDecimal(a.Qty),
		PrintNote:           a.PrintNote.String,
		PositionNote:        a.PositionNote.String,
		Active:              a.Active,
		OutputMaterialName:  a.OutputMaterialName.String,
		OutputVariantCount:  int32(a.OutputVariantCount),
		// Zero for a bill read on its own (ListStyleAssembly never resolves a colour); the packing spec
		// fills these in per order item, and `unresolved` false there means "no colour dimension", not
		// "resolution not attempted".
		ResolvedColorCode:    a.ResolvedColorCode,
		ResolvedColorName:    a.ResolvedColorName,
		ResolvedMaterialId:   int32(a.ResolvedMaterialId),
		ResolvedMaterialName: a.ResolvedMaterialName,
		Unresolved:           a.Unresolved,
		ResolutionBasis:      assemblyResolutionBasisToPb(a.Basis),
	}
	if a.SizeId.Valid {
		pb.SizeId = a.SizeId.Int32
		pb.SizeName = a.SizeName.String
	}
	if a.OutputMaterialId.Valid {
		pb.OutputMaterialId = a.OutputMaterialId.Int32
	}
	return pb
}

// assemblyResolutionBasisPbByEntity is the single entity<->proto mapping table for the packing spec's
// resolution discriminator; the drift test walks it in both directions.
var assemblyResolutionBasisPbByEntity = map[entity.AssemblyResolutionBasis]pb_common.AssemblyResolutionBasis{
	entity.AssemblyResolutionColorMatch:       pb_common.AssemblyResolutionBasis_ASSEMBLY_RESOLUTION_BASIS_COLOR_MATCH,
	entity.AssemblyResolutionSoleVariant:      pb_common.AssemblyResolutionBasis_ASSEMBLY_RESOLUTION_BASIS_SOLE_VARIANT,
	entity.AssemblyResolutionLegacyOutput:     pb_common.AssemblyResolutionBasis_ASSEMBLY_RESOLUTION_BASIS_LEGACY_OUTPUT,
	entity.AssemblyResolutionRetiredColor:     pb_common.AssemblyResolutionBasis_ASSEMBLY_RESOLUTION_BASIS_RETIRED_COLOR,
	entity.AssemblyResolutionNoColorMatch:     pb_common.AssemblyResolutionBasis_ASSEMBLY_RESOLUTION_BASIS_NO_COLOR_MATCH,
	entity.AssemblyResolutionArchivedMaterial: pb_common.AssemblyResolutionBasis_ASSEMBLY_RESOLUTION_BASIS_ARCHIVED_MATERIAL,
	entity.AssemblyResolutionNoOutput:         pb_common.AssemblyResolutionBasis_ASSEMBLY_RESOLUTION_BASIS_NO_OUTPUT,
}

// assemblyResolutionBasisToPb maps the read-model basis to the proto enum; an unset basis (a bill read
// on its own, never resolved against an order item) is UNKNOWN.
func assemblyResolutionBasisToPb(b entity.AssemblyResolutionBasis) pb_common.AssemblyResolutionBasis {
	if v, ok := assemblyResolutionBasisPbByEntity[b]; ok {
		return v
	}
	return pb_common.AssemblyResolutionBasis_ASSEMBLY_RESOLUTION_BASIS_UNKNOWN
}

// assemblyResolutionBasisFromPb is the reverse leg, for the drift guard ("" for UNKNOWN).
func assemblyResolutionBasisFromPb(p pb_common.AssemblyResolutionBasis) entity.AssemblyResolutionBasis {
	for ent, pb := range assemblyResolutionBasisPbByEntity {
		if pb == p {
			return ent
		}
	}
	return entity.AssemblyResolutionNotAttempted
}

// StyleAssemblyListToPb converts resolved assembly lines to protobuf.
func StyleAssemblyListToPb(items []entity.StyleAssembly) []*pb_admin.StyleAssemblyLine {
	out := make([]*pb_admin.StyleAssemblyLine, 0, len(items))
	for _, it := range items {
		out = append(out, StyleAssemblyLineToPb(it))
	}
	return out
}

// OrderPackingSpecToPb converts the read-only packing spec projection to protobuf (WS7 scope 3).
func OrderPackingSpecToPb(spec entity.OrderPackingSpec) *pb_admin.GetOrderPackingSpecResponse {
	resp := &pb_admin.GetOrderPackingSpecResponse{
		OrderUuid: spec.OrderUUID,
		Items:     make([]*pb_admin.OrderPackingSpecItem, 0, len(spec.Items)),
		Packaging: make([]*pb_admin.OrderPackingSpecPackaging, 0, len(spec.Packaging)),
	}
	for _, it := range spec.Items {
		resp.Items = append(resp.Items, &pb_admin.OrderPackingSpecItem{
			OrderItemId: int32(it.OrderItemId),
			ProductId:   int32(it.ProductId),
			VariantId:   int32(it.VariantId),
			StyleId:     int32(it.StyleId),
			StyleName:   it.StyleName,
			Sku:         it.SKU,
			SizeName:    it.SizeName,
			ColorCode:   it.ColorCode,
			ColorName:   it.ColorName,
			Quantity:    pbDecimalFromDecimal(it.Quantity),
			Assembly:    StyleAssemblyListToPb(it.Assembly),
		})
	}
	for _, p := range spec.Packaging {
		resp.Packaging = append(resp.Packaging, &pb_admin.OrderPackingSpecPackaging{
			MaterialId:   int32(p.MaterialId),
			MaterialName: p.MaterialName,
			MaterialUnit: p.MaterialUnit.String,
			Qty:          pbDecimalFromDecimal(p.Qty),
		})
	}
	return resp
}
