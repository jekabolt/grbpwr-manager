package frontend

import (
	"context"
	"database/sql"
	"errors"
	"log/slog"
	"slices"

	"github.com/jekabolt/grbpwr-manager/internal/dto"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	pb_common "github.com/jekabolt/grbpwr-manager/proto/gen/common"
	pb_frontend "github.com/jekabolt/grbpwr-manager/proto/gen/frontend"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func (s *Server) GetProduct(ctx context.Context, req *pb_frontend.GetProductRequest) (*pb_frontend.GetProductResponse, error) {

	pf, err := s.repo.Products().GetProductByIdNoHidden(ctx, int(req.Id))
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, status.Errorf(codes.NotFound, "product not found")
		}
		slog.Default().ErrorContext(ctx, "can't get product by id",
			slog.String("err", err.Error()),
		)
		return nil, status.Errorf(codes.Internal, "failed to get product")
	}

	pbPrd, err := dto.ConvertToPbProductFull(pf)
	if err != nil {
		slog.Default().ErrorContext(ctx, "can't convert dto product to proto product",
			slog.String("err", err.Error()),
		)
		return nil, status.Errorf(codes.Internal, "can't convert dto product to proto product")
	}

	return &pb_frontend.GetProductResponse{
		Product: pbPrd,
	}, nil
}

func (s *Server) GetProductsPaged(ctx context.Context, req *pb_frontend.GetProductsPagedRequest) (*pb_frontend.GetProductsPagedResponse, error) {
	sfs := make([]entity.SortFactor, 0, len(req.SortFactors))
	for _, sf := range req.SortFactors {
		sfs = append(sfs, dto.ConvertPBCommonSortFactorToEntity(sf))
	}

	// remove duplicates
	sfs = slices.Compact(sfs)

	of := dto.ConvertPBCommonOrderFactorToEntity(req.OrderFactor)

	fc := dto.ConvertPBCommonFilterConditionsToEntity(req.FilterConditions)

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

	prds, count, err := s.repo.Products().GetProductsPaged(ctx, int(req.Limit), int(req.Offset), sfs, of, fc, false)
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

	prdsPb := make([]*pb_common.Product, 0, len(prds))
	for _, prd := range prds {
		pbPrd, err := dto.ConvertEntityProductToCommon(&prd)
		if err != nil {
			slog.Default().ErrorContext(ctx, "can't convert dto product to proto product",
				slog.String("err", err.Error()),
			)
			return nil, status.Errorf(codes.Internal, "can't convert dto product to proto product")
		}
		prdsPb = append(prdsPb, pbPrd)
	}

	return &pb_frontend.GetProductsPagedResponse{
		Products: prdsPb,
		Total:    int32(count),
	}, nil
}
