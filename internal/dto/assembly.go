package dto

import (
	"fmt"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	pb_admin "github.com/jekabolt/grbpwr-manager/proto/gen/admin"
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
		qty, err := parseNonNegDecimal(it.Qty, fmt.Sprintf("items[%d].qty", i))
		if err != nil {
			return nil, err
		}
		if !qty.IsPositive() {
			return nil, fmt.Errorf("items[%d].qty must be > 0", i)
		}
		out = append(out, entity.StyleAssemblyInsert{
			ComponentTechCardId: int(it.ComponentTechCardId),
			SizeId:              nullInt32FromPb(it.SizeId),
			Qty:                 qty,
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
