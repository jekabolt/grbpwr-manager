package admin

import (
	"context"
	"log/slog"

	"github.com/jekabolt/grbpwr-manager/internal/apisrv/apierr"
	authsrv "github.com/jekabolt/grbpwr-manager/internal/apisrv/auth"
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
		return nil, status.Error(codes.InvalidArgument, err.Error())
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
	styleIDs := make([]int, 0, len(products))
	seenStyle := map[int]bool{}
	for i := range products {
		styleByProduct[products[i].Id] = products[i].StyleId
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
			Quantity:    it.Quantity,
			Assembly:    assemblyForSize(assemblyByStyle[styleID], it.SizeId),
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

// assemblyForSize keeps the assembly lines that apply to a given garment size: the all-sizes lines
// (SizeId NULL) plus any line scoped to exactly that size.
func assemblyForSize(all []entity.StyleAssembly, sizeID int) []entity.StyleAssembly {
	out := make([]entity.StyleAssembly, 0, len(all))
	for _, a := range all {
		if !a.SizeId.Valid || int(a.SizeId.Int32) == sizeID {
			out = append(out, a)
		}
	}
	return out
}
