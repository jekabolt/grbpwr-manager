package admin

import (
	"context"
	"database/sql"
	"errors"
	"log/slog"
	"strings"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	pb_admin "github.com/jekabolt/grbpwr-manager/proto/gen/admin"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// GetProductCustoms returns a product's international-shipping customs data.
func (s *Server) GetProductCustoms(ctx context.Context, req *pb_admin.GetProductCustomsRequest) (*pb_admin.GetProductCustomsResponse, error) {
	if req.ProductId <= 0 {
		return nil, status.Error(codes.InvalidArgument, "product_id is required")
	}
	c, err := s.repo.Products().GetProductCustoms(ctx, int(req.ProductId))
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, status.Error(codes.NotFound, "product not found")
		}
		slog.Default().ErrorContext(ctx, "can't get product customs", slog.String("err", err.Error()))
		return nil, status.Error(codes.Internal, "can't get product customs")
	}
	return &pb_admin.GetProductCustomsResponse{
		Customs: &pb_admin.ColorwayCustoms{
			HsCode:             c.HSCode.String,
			CountryOfOrigin:    c.CountryOfOrigin.String,
			CustomsDescription: c.CustomsDescription.String,
		},
	}, nil
}

// SetProductCustoms sets a product's customs data (HS code + declared description). Empty fields
// clear the stored value. country_of_origin is ignored here: it is a core product field set via the
// product form (and reused as the Sendcloud origin_country); the customs path never writes it.
func (s *Server) SetProductCustoms(ctx context.Context, req *pb_admin.SetProductCustomsRequest) (*pb_admin.SetProductCustomsResponse, error) {
	if req.ProductId <= 0 {
		return nil, status.Error(codes.InvalidArgument, "product_id is required")
	}
	c := req.Customs
	if c == nil {
		c = &pb_admin.ColorwayCustoms{}
	}

	hs := strings.TrimSpace(c.HsCode)
	descr := strings.TrimSpace(c.CustomsDescription)

	err := s.repo.Products().SetProductCustoms(ctx, int(req.ProductId), entity.ColorwayCustoms{
		HSCode:             sql.NullString{String: hs, Valid: hs != ""},
		CustomsDescription: sql.NullString{String: descr, Valid: descr != ""},
	})
	if err != nil {
		slog.Default().ErrorContext(ctx, "can't set product customs", slog.String("err", err.Error()))
		return nil, status.Error(codes.Internal, "can't set product customs")
	}
	return &pb_admin.SetProductCustomsResponse{}, nil
}
