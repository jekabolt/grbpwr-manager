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
		Customs: &pb_admin.ProductCustoms{
			HsCode:             c.HSCode.String,
			CountryOfOrigin:    c.CountryOfOrigin.String,
			CustomsDescription: c.CustomsDescription.String,
		},
	}, nil
}

// SetProductCustoms sets a product's customs data (HS code, ISO-2 country of origin, declared
// description). Empty fields clear the stored value. A provided country of origin must resolve to
// an ISO-2 code (Sendcloud origin_country) so the label call never sends an invalid origin.
func (s *Server) SetProductCustoms(ctx context.Context, req *pb_admin.SetProductCustomsRequest) (*pb_admin.SetProductCustomsResponse, error) {
	if req.ProductId <= 0 {
		return nil, status.Error(codes.InvalidArgument, "product_id is required")
	}
	c := req.Customs
	if c == nil {
		c = &pb_admin.ProductCustoms{}
	}

	origin := strings.TrimSpace(c.CountryOfOrigin)
	if origin != "" {
		iso2, ok := entity.ResolveCountryISO2(origin)
		if !ok {
			return nil, status.Errorf(codes.InvalidArgument, "country_of_origin %q is not a valid country", origin)
		}
		origin = iso2
	}
	hs := strings.TrimSpace(c.HsCode)
	descr := strings.TrimSpace(c.CustomsDescription)

	err := s.repo.Products().SetProductCustoms(ctx, int(req.ProductId), entity.ProductCustoms{
		HSCode:             sql.NullString{String: hs, Valid: hs != ""},
		CountryOfOrigin:    sql.NullString{String: origin, Valid: origin != ""},
		CustomsDescription: sql.NullString{String: descr, Valid: descr != ""},
	})
	if err != nil {
		slog.Default().ErrorContext(ctx, "can't set product customs", slog.String("err", err.Error()))
		return nil, status.Error(codes.Internal, "can't set product customs")
	}
	return &pb_admin.SetProductCustomsResponse{}, nil
}
