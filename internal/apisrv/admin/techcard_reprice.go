package admin

import (
	"context"
	"database/sql"
	"errors"
	"log/slog"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	pb_admin "github.com/jekabolt/grbpwr-manager/proto/gen/admin"
	pb_decimal "google.golang.org/genproto/googleapis/type/decimal"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// RepriceTechCardBom re-pulls the current material-catalog price into every catalog-linked BOM line
// of a MUTABLE (non-released) card (production-costing Phase 3, plan 11). The store applies the estimate's own
// CATALOG_LATEST resolution (costing currency → base currency → sole unambiguous currency), so the
// repriced document equals what the estimate already reported as the catalog fallback. Repricing
// rewrites BOM money, so beyond the interceptor's tech-card write it demands costing:write — the
// same rule UpdateTechCard applies to a payload carrying BOM prices. A changed price legitimately
// stales an approved MATERIALS sign-off (the document's content moved).
func (s *Server) RepriceTechCardBom(ctx context.Context, req *pb_admin.RepriceTechCardBomRequest) (*pb_admin.RepriceTechCardBomResponse, error) {
	if req.GetTechCardId() <= 0 {
		return nil, status.Error(codes.InvalidArgument, "tech_card_id is required")
	}
	if _, canWrite := s.costingAccess(ctx); !canWrite {
		return nil, status.Error(codes.PermissionDenied, "costing:write is required to reprice BOM lines")
	}

	fx := s.costingFx(ctx)
	lines, skippedUnlinked, err := s.repo.TechCards().RepriceTechCardBom(ctx, int(req.GetTechCardId()), fx.Base)
	if err != nil {
		switch {
		case errors.Is(err, sql.ErrNoRows):
			return nil, status.Error(codes.NotFound, "tech card not found")
		case errors.Is(err, entity.ErrTechCardReleased):
			// Only RELEASED blocks (same rule as every content write); in_review/approved reprice fine.
			return nil, status.Error(codes.FailedPrecondition,
				"tech card is released and frozen; re-releasing it is how a frozen card reprices")
		}
		slog.Default().ErrorContext(ctx, "can't reprice tech card bom",
			slog.Int("tech_card_id", int(req.GetTechCardId())), slog.String("err", err.Error()))
		return nil, status.Error(codes.Internal, "can't reprice tech card bom")
	}

	resp := &pb_admin.RepriceTechCardBomResponse{SkippedUnlinked: int32(skippedUnlinked)}
	for _, l := range lines {
		pb := &pb_admin.RepricedBomLine{
			LineKey:     l.LineKey,
			Name:        l.Name,
			Section:     string(l.Section),
			OldCurrency: l.OldCurrency,
			NewCurrency: l.NewCurrency,
			Changed:     l.Changed,
		}
		if l.OldPrice.Valid {
			pb.OldPrice = &pb_decimal.Decimal{Value: l.OldPrice.Decimal.String()}
		}
		if l.NewPrice.Valid {
			pb.NewPrice = &pb_decimal.Decimal{Value: l.NewPrice.Decimal.String()}
		}
		resp.Lines = append(resp.Lines, pb)
	}
	return resp, nil
}

// ListCostingMigrationExceptions reads the Phase 2 scalar→BOM migration's exception report — the
// hardware/packaging money migration 0237 refused to move mechanically, waiting for a manual
// transfer into the BOM. The interceptor grants tech-card read; the AMOUNTS are confidential cost
// data, so without costing:read the rows come back with the money stripped (structure only), the
// same rule every cost surface follows.
func (s *Server) ListCostingMigrationExceptions(ctx context.Context, req *pb_admin.ListCostingMigrationExceptionsRequest) (*pb_admin.ListCostingMigrationExceptionsResponse, error) {
	rows, err := s.repo.TechCards().ListCostingMigrationExceptions(ctx, int(req.GetTechCardId()))
	if err != nil {
		slog.Default().ErrorContext(ctx, "can't list costing migration exceptions", slog.String("err", err.Error()))
		return nil, status.Error(codes.Internal, "can't list costing migration exceptions")
	}
	canReadCosting, _ := s.costingAccess(ctx)

	resp := &pb_admin.ListCostingMigrationExceptionsResponse{}
	for _, r := range rows {
		pb := &pb_admin.CostingMigrationException{
			TechCardId:    int32(r.TechCardId),
			StyleNumber:   r.StyleNumber.String,
			TechCardName:  r.TechCardName,
			Article:       r.Article,
			Kind:          r.Kind,
			Currency:      r.Currency.String,
			ApprovalState: r.ApprovalState,
			CreatedAt:     timestamppb.New(r.CreatedAt),
		}
		if canReadCosting {
			pb.Amount = &pb_decimal.Decimal{Value: r.Amount.String()}
		}
		resp.Exceptions = append(resp.Exceptions, pb)
	}
	return resp, nil
}
