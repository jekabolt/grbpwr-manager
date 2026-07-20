package admin

import (
	"context"

	"github.com/jekabolt/grbpwr-manager/internal/dependency"
	"github.com/jekabolt/grbpwr-manager/internal/dto"
	pb_admin "github.com/jekabolt/grbpwr-manager/proto/gen/admin"
	"github.com/shopspring/decimal"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// Fixed-asset register + depreciation + corporation-tax accrual (statutory-exports track). The two
// posting actions run in s.repo.Tx like the other multi-row accounting writes, and are idempotent at
// the store (per asset-month / per period), so a re-run only fills gaps.

// CreateFixedAsset adds an asset to the register (drives straight-line depreciation).
func (s *Server) CreateFixedAsset(ctx context.Context, req *pb_admin.CreateFixedAssetRequest) (*pb_admin.CreateFixedAssetResponse, error) {
	in, err := dto.ConvertCreateFixedAssetReq(req)
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, err.Error())
	}
	var id int
	err = s.repo.Tx(ctx, func(ctx context.Context, rep dependency.Repository) error {
		var e error
		id, e = rep.Accounting().CreateFixedAsset(ctx, in)
		return e
	})
	if err != nil {
		return nil, mapAcctErr(ctx, "create fixed asset", err)
	}
	return &pb_admin.CreateFixedAssetResponse{Id: int32(id)}, nil
}

// ListFixedAssets returns the register.
func (s *Server) ListFixedAssets(ctx context.Context, _ *pb_admin.ListFixedAssetsRequest) (*pb_admin.ListFixedAssetsResponse, error) {
	assets, err := s.repo.Accounting().ListFixedAssets(ctx)
	if err != nil {
		return nil, mapAcctErr(ctx, "list fixed assets", err)
	}
	return &pb_admin.ListFixedAssetsResponse{Assets: dto.ConvertFixedAssetsToPb(assets)}, nil
}

// PostDepreciation posts every un-posted monthly depreciation charge up to the given month.
func (s *Server) PostDepreciation(ctx context.Context, req *pb_admin.PostDepreciationRequest) (*pb_admin.PostDepreciationResponse, error) {
	upTo, err := dto.ParseAcctMonth(req.GetUpTo())
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, err.Error())
	}
	var posted int
	err = s.repo.Tx(ctx, func(ctx context.Context, rep dependency.Repository) error {
		var e error
		posted, e = rep.Accounting().PostDepreciationDue(ctx, upTo)
		return e
	})
	if err != nil {
		return nil, mapAcctErr(ctx, "post depreciation", err)
	}
	return &pb_admin.PostDepreciationResponse{Posted: int32(posted)}, nil
}

// AccrueCorporationTax posts a corporation-tax accrual on the period's pre-tax profit.
func (s *Server) AccrueCorporationTax(ctx context.Context, req *pb_admin.AccrueCorporationTaxRequest) (*pb_admin.AccrueCorporationTaxResponse, error) {
	from, to, err := dto.ParseAcctDateRange(req.GetFrom(), req.GetTo())
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, err.Error())
	}
	rate, err := dto.RequiredRateFromPb(req.GetRatePct())
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, err.Error())
	}
	var ct decimal.Decimal
	var already bool
	err = s.repo.Tx(ctx, func(ctx context.Context, rep dependency.Repository) error {
		var e error
		ct, already, e = rep.Accounting().AccrueCorporationTax(ctx, from, to, rate)
		return e
	})
	if err != nil {
		return nil, mapAcctErr(ctx, "accrue corporation tax", err)
	}
	return dto.ConvertAccrueCorpTaxResp(ct, already), nil
}
