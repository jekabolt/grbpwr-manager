package admin

import (
	"context"
	"log/slog"
	"sort"

	"github.com/jekabolt/grbpwr-manager/internal/apisrv/apierr"
	authsrv "github.com/jekabolt/grbpwr-manager/internal/apisrv/auth"
	"github.com/jekabolt/grbpwr-manager/internal/cache"
	"github.com/jekabolt/grbpwr-manager/internal/dto"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	pb_admin "github.com/jekabolt/grbpwr-manager/proto/gen/admin"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// UpsertStyleAssembly full-replaces a garment style's assembly bill (WS7, §2.8): the auxiliary items
// (labels/tags) that physically go on/into it. Field-tagged errors (via apierr) surface a bad payload
// (non-auxiliary/duplicate/missing component) as InvalidArgument with the offending field.
func (s *Server) UpsertStyleAssembly(ctx context.Context, req *pb_admin.UpsertStyleAssemblyRequest) (*pb_admin.UpsertStyleAssemblyResponse, error) {
	if req.GetStyleId() <= 0 {
		return nil, status.Error(codes.InvalidArgument, "style_id is required")
	}
	items, err := dto.ConvertPbStyleAssemblyToEntity(req.GetItems())
	if err != nil {
		return nil, techCardConvertErr(err)
	}
	if err := s.repo.TechCards().UpsertStyleAssembly(ctx, int(req.GetStyleId()), items, authsrv.GetAdminUsername(ctx)); err != nil {
		if st, ok := apierr.Status(err); ok {
			return nil, st
		}
		slog.Default().ErrorContext(ctx, "can't upsert style assembly", slog.String("err", err.Error()))
		return nil, status.Error(codes.Internal, "can't upsert style assembly")
	}
	return &pb_admin.UpsertStyleAssemblyResponse{}, nil
}

// ListStyleAssembly returns a garment style's assembly bill, resolved for display.
func (s *Server) ListStyleAssembly(ctx context.Context, req *pb_admin.ListStyleAssemblyRequest) (*pb_admin.ListStyleAssemblyResponse, error) {
	if req.GetStyleId() <= 0 {
		return nil, status.Error(codes.InvalidArgument, "style_id is required")
	}
	items, err := s.repo.TechCards().ListStyleAssembly(ctx, int(req.GetStyleId()))
	if err != nil {
		slog.Default().ErrorContext(ctx, "can't list style assembly", slog.String("err", err.Error()))
		return nil, status.Error(codes.Internal, "can't list style assembly")
	}
	return &pb_admin.ListStyleAssemblyResponse{Items: dto.StyleAssemblyListToPb(items)}, nil
}

// GetOrderPackingSpec composes the packer/QC packing spec (WS7 scope 3): per order item the garment
// colourway/variant + its size-resolved on-garment assembly, plus the order's packaging requirement
// (WS2 resolution). Read-only; reserves/consumes nothing.
func (s *Server) GetOrderPackingSpec(ctx context.Context, req *pb_admin.GetOrderPackingSpecRequest) (*pb_admin.GetOrderPackingSpecResponse, error) {
	if req.GetOrderUuid() == "" {
		return nil, status.Error(codes.InvalidArgument, "order_uuid is required")
	}
	of, err := s.repo.Order().GetOrderFullByUUID(ctx, req.GetOrderUuid())
	if err != nil {
		if st, ok := apierr.Status(err); ok { // sql.ErrNoRows → NotFound
			return nil, st
		}
		slog.Default().ErrorContext(ctx, "can't load order for packing spec", slog.String("err", err.Error()))
		return nil, status.Error(codes.Internal, "can't load order")
	}

	// Resolve each item's colourway → style once, then style names + assembly bills once per style.
	productIDs := make([]int, 0, len(of.OrderItems))
	seenProduct := map[int]bool{}
	for _, it := range of.OrderItems {
		if it.ProductId > 0 && !seenProduct[it.ProductId] {
			seenProduct[it.ProductId] = true
			productIDs = append(productIDs, it.ProductId)
		}
	}
	products, err := s.repo.Products().GetProductsByIds(ctx, productIDs)
	if err != nil {
		slog.Default().ErrorContext(ctx, "can't load products for packing spec", slog.String("err", err.Error()))
		return nil, status.Error(codes.Internal, "can't load products")
	}
	styleByProduct := make(map[int]int, len(products))
	// The colourway's dictionary colour code is what an assembly component's colour is matched against
	// ("the black jacket ships the black dust bag"). Absent product ⇒ absent code ⇒ nothing matches, and
	// the resolution falls through to its no-guess branches.
	colorByProduct := make(map[int]string, len(products))
	colorNameByProduct := make(map[int]string, len(products))
	styleIDs := make([]int, 0, len(products))
	seenStyle := map[int]bool{}
	for i := range products {
		styleByProduct[products[i].Id] = products[i].StyleId
		body := products[i].ProductDisplay.ProductBody.ProductBodyInsert
		colorByProduct[products[i].Id] = body.ColorCode
		// The NAME comes from the same row as the code, never from the order line's snapshot: the packer
		// has to be shown the colour the matching actually used, not a label that may have drifted.
		colorNameByProduct[products[i].Id] = body.Color
		if !seenStyle[products[i].StyleId] {
			seenStyle[products[i].StyleId] = true
			styleIDs = append(styleIDs, products[i].StyleId)
		}
	}
	styleNames, err := s.repo.TechCards().GetTechCardNames(ctx, styleIDs)
	if err != nil {
		slog.Default().ErrorContext(ctx, "can't load style names for packing spec", slog.String("err", err.Error()))
		return nil, status.Error(codes.Internal, "can't load style names")
	}
	assemblyByStyle := make(map[int][]entity.StyleAssembly, len(styleIDs))
	for _, sid := range styleIDs {
		a, err := s.repo.TechCards().ListStyleAssembly(ctx, sid)
		if err != nil {
			slog.Default().ErrorContext(ctx, "can't load assembly for packing spec", slog.String("err", err.Error()))
			return nil, status.Error(codes.Internal, "can't load assembly")
		}
		assemblyByStyle[sid] = a
	}

	// ONE variant read for the whole order, over every distinct component card the bills mention (R11:
	// the per-style ListStyleAssembly loop above is already N queries; colour resolution must not make
	// it N×M). Every component is asked for, not only the ones ListStyleAssembly counted as varianted,
	// so that the count and the resolution below come from the SAME read and cannot disagree.
	componentIDs := make([]int, 0, len(styleIDs))
	seenComponent := map[int]bool{}
	for _, lines := range assemblyByStyle {
		for _, l := range lines {
			// Deactivated lines never reach a packer (assemblyForSize drops them), so their colours are
			// not worth reading.
			if !l.Active || l.ComponentTechCardId <= 0 || seenComponent[l.ComponentTechCardId] {
				continue
			}
			seenComponent[l.ComponentTechCardId] = true
			componentIDs = append(componentIDs, l.ComponentTechCardId)
		}
	}
	// assemblyByStyle is a map, so the collection order is not stable across calls; sort so one order
	// always produces the same query.
	sort.Ints(componentIDs)
	variantsByComponent, err := s.repo.TechCards().ListOutputVariantsByCardIds(ctx, componentIDs)
	if err != nil {
		slog.Default().ErrorContext(ctx, "can't load component colour variants for packing spec", slog.String("err", err.Error()))
		return nil, status.Error(codes.Internal, "can't load component colour variants")
	}

	spec := entity.OrderPackingSpec{OrderUUID: of.Order.UUID}
	for _, it := range of.OrderItems {
		styleID := styleByProduct[it.ProductId]
		spec.Items = append(spec.Items, entity.OrderPackingSpecItem{
			OrderItemId: it.Id,
			ProductId:   it.ProductId,
			VariantId:   it.VariantID,
			StyleId:     styleID,
			StyleName:   styleNames[styleID],
			SKU:         it.SKU,
			SizeName:    sizeName(it.SizeId),
			ColorCode:   colorByProduct[it.ProductId],
			ColorName:   colorNameByProduct[it.ProductId],
			Quantity:    it.Quantity,
			Assembly: resolveAssemblyColours(
				assemblyForSize(assemblyByStyle[styleID], it.SizeId),
				colorByProduct[it.ProductId], variantsByComponent),
		})
	}
	pkg, err := s.repo.MaterialStock().ResolveOrderPackaging(ctx, of.Order.Id)
	if err != nil {
		slog.Default().ErrorContext(ctx, "can't resolve packaging for packing spec", slog.String("err", err.Error()))
		return nil, status.Error(codes.Internal, "can't resolve packaging")
	}
	spec.Packaging = pkg
	return dto.OrderPackingSpecToPb(spec), nil
}

// sizeName resolves an order line's size to its display code from the size dictionary cache. Empty when
// the size is unknown — the packer sees a blank rather than a raw id.
func sizeName(sizeID int) string {
	if sz, ok := cache.GetSizeById(sizeID); ok {
		return sz.Name
	}
	return ""
}

// assemblyForSize keeps the assembly lines that apply to a given garment size: the ACTIVE all-sizes
// lines (SizeId NULL) plus any active line scoped to exactly that size. ListStyleAssembly deliberately
// returns deactivated lines too (the style editor has to show and re-enable them), but a deactivated
// care label / hangtag must never reach the packer's spec.
func assemblyForSize(all []entity.StyleAssembly, sizeID int) []entity.StyleAssembly {
	out := make([]entity.StyleAssembly, 0, len(all))
	for _, a := range all {
		if !a.Active {
			continue
		}
		if !a.SizeId.Valid || int(a.SizeId.Int32) == sizeID {
			out = append(out, a)
		}
	}
	return out
}

// resolveAssemblyColours names, per assembly line, the ONE warehouse bucket THIS order item consumes —
// the black jacket's black dust bag — from the component card's live colours and the item's colourway
// code. The rule itself is entity.ResolveAssemblyOutput; this is only the plumbing that feeds it and the
// place where the per-item answer is stamped onto the line.
//
// It mutates in place, which is safe because assemblyForSize hands back a fresh slice of COPIED lines
// per item: two items of the same style with different colourways must not overwrite each other's
// resolution, and they don't.
func resolveAssemblyColours(lines []entity.StyleAssembly, itemColorCode string, variantsByComponent map[int][]entity.TechCardOutputVariant) []entity.StyleAssembly {
	for i := range lines {
		variants := variantsByComponent[lines[i].ComponentTechCardId]
		// The badge counts LIVE colours only (unchanged semantics), while the rule below is handed the
		// full list — retired rows are three of its branches. Both are re-stated from the batched read
		// rather than kept from ListStyleAssembly's COUNT subquery, so "N colours" and the resolved
		// colour always describe the same set of rows.
		active := 0
		for _, v := range variants {
			if v.Active {
				active++
			}
		}
		lines[i].OutputVariantCount = active
		lines[i].AssemblyOutputResolution = entity.ResolveAssemblyOutput(itemColorCode, variants,
			entity.AssemblyLegacyOutput{
				MaterialId:   lines[i].OutputMaterialId,
				MaterialName: lines[i].OutputMaterialName,
				Archived:     lines[i].OutputMaterialArchived.Bool,
			})
	}
	return lines
}
