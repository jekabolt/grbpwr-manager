package frontend

import (
	"context"
	"database/sql"
	"errors"
	"log/slog"
	"slices"

	"github.com/jekabolt/grbpwr-manager/internal/dto"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	pb_frontend "github.com/jekabolt/grbpwr-manager/proto/gen/frontend"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func (s *Server) GetColorway(ctx context.Context, req *pb_frontend.GetColorwayRequest) (*pb_frontend.GetColorwayResponse, error) {
	if req.BaseSku == "" {
		return nil, status.Errorf(codes.InvalidArgument, "base_sku is required")
	}

	pf, err := s.repo.Products().GetProductBySKU(ctx, req.BaseSku)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, status.Errorf(codes.NotFound, "product not found")
		}
		slog.Default().ErrorContext(ctx, "can't get product by sku",
			slog.String("err", err.Error()),
		)
		return nil, status.Errorf(codes.Internal, "failed to get product")
	}

	// R3: the storefront gets a projection with no catalogue PKs (base_sku/variant_sku identity only).
	return &pb_frontend.GetColorwayResponse{
		Colorway: dto.StorefrontColorwayFromFull(pf),
	}, nil
}

// viewerTier resolves the loyalty tier code of the requesting customer from the
// optional bearer token, returning 0 (member/guest) when unauthenticated.
func (s *Server) viewerTier(ctx context.Context) int16 {
	email, err := s.storefrontEmailFromAccess(ctx)
	if err != nil || email == "" {
		return entity.TierCodeMember
	}
	acc, err := s.repo.StorefrontAccount().GetAccountByEmail(ctx, email)
	if err != nil {
		return entity.TierCodeMember
	}
	return entity.TierCode(acc.Tier())
}

func (s *Server) GetColorwaysPaged(ctx context.Context, req *pb_frontend.GetColorwaysPagedRequest) (*pb_frontend.GetColorwaysPagedResponse, error) {
	sfs := make([]entity.SortFactor, 0, len(req.SortFactors))
	for _, sf := range req.SortFactors {
		sfs = append(sfs, dto.ConvertPBCommonSortFactorToEntity(sf))
	}

	// remove duplicates
	sfs = slices.Compact(sfs)

	of := dto.ConvertPBCommonOrderFactorToEntity(req.OrderFactor)

	fc, err := dto.ConvertPBCommonFilterConditionsToEntity(req.FilterConditions)
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, err.Error())
	}

	// Validate: price sorting requires currency to be specified
	var priceSortRequested bool
	for _, sf := range sfs {
		if sf == entity.Price {
			priceSortRequested = true
			break
		}
	}

	if priceSortRequested && (fc == nil || fc.Currency == "") {
		slog.Default().WarnContext(ctx, "price sorting requires currency",
			slog.String("err", "price sorting requires currency to be specified in filter conditions"),
		)
		return nil, status.Errorf(codes.InvalidArgument, "price sorting requires currency to be specified in filter conditions")
	}

	// Tier gating: resolve the viewer's tier (0 for guests / unauthenticated)
	// so the catalog query hides higher-tier-only and hacker-only products.
	if fc == nil {
		fc = &entity.FilterConditions{}
	}
	fc.ViewerTier = s.viewerTier(ctx)

	limit, offset := clampPagination(int(req.Limit), int(req.Offset), 30, 100)

	prds, count, err := s.repo.Products().GetProductsPaged(ctx, limit, offset, sfs, of, fc, false)
	if err != nil {
		// Check if it's a validation error (should return 4xx, not 5xx)
		if err.Error() == "price sorting requires currency to be specified in filter conditions" {
			slog.Default().WarnContext(ctx, "price sorting requires currency",
				slog.String("err", err.Error()),
			)
			return nil, status.Error(codes.InvalidArgument, err.Error())
		}
		slog.Default().ErrorContext(ctx, "can't get products paged",
			slog.String("err", err.Error()),
		)
		return nil, status.Errorf(codes.Internal, "can't get products paged")
	}

	prdsPb := make([]*pb_frontend.StorefrontColorway, 0, len(prds))
	for i := range prds {
		prdsPb = append(prdsPb, dto.StorefrontColorwayFromColorway(&prds[i]))
	}

	return &pb_frontend.GetColorwaysPagedResponse{
		Colorways: prdsPb,
		Total:     int32(count),
	}, nil
}
