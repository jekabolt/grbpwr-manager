package admin

import (
	"context"
	"log/slog"

	"github.com/jekabolt/grbpwr-manager/internal/dto"
	pb_admin "github.com/jekabolt/grbpwr-manager/proto/gen/admin"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// AddTechCardDevExpense appends one development-cost row to a tech card's journal (task 14),
// folding amount into base currency via the costing FX, and returns the stored row.
func (s *Server) AddTechCardDevExpense(ctx context.Context, req *pb_admin.AddTechCardDevExpenseRequest) (*pb_admin.AddTechCardDevExpenseResponse, error) {
	// A dev-expense row is entirely confidential cost data (kind + amount), so it is gated by
	// costing:*, not just tech_cards (task 19), the same as material prices and the BOM costing block.
	if _, write := s.costingAccess(ctx); !write {
		return nil, status.Error(codes.PermissionDenied, "costing:write is required to record a development expense")
	}
	e, err := dto.ConvertPbDevExpenseInsertToEntity(req.GetExpense())
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, err.Error())
	}
	dto.FoldTechCardDevExpenseToBase(&e, s.costingFx(ctx))
	saved, err := s.repo.TechCards().AddTechCardDevExpense(ctx, e)
	if err != nil {
		slog.Default().ErrorContext(ctx, "can't add tech card dev expense", slog.String("err", err.Error()))
		return nil, status.Error(codes.Internal, "can't add tech card dev expense")
	}
	return &pb_admin.AddTechCardDevExpenseResponse{Expense: dto.ConvertEntityDevExpenseToPb(saved)}, nil
}

// DeleteTechCardDevExpense removes a single development-cost row.
func (s *Server) DeleteTechCardDevExpense(ctx context.Context, req *pb_admin.DeleteTechCardDevExpenseRequest) (*pb_admin.DeleteTechCardDevExpenseResponse, error) {
	if _, write := s.costingAccess(ctx); !write {
		return nil, status.Error(codes.PermissionDenied, "costing:write is required to delete a development expense")
	}
	if req.GetId() <= 0 {
		return nil, status.Error(codes.InvalidArgument, "id is required")
	}
	if err := s.repo.TechCards().DeleteTechCardDevExpense(ctx, int(req.GetId())); err != nil {
		slog.Default().ErrorContext(ctx, "can't delete tech card dev expense", slog.String("err", err.Error()))
		return nil, status.Error(codes.Internal, "can't delete tech card dev expense")
	}
	return &pb_admin.DeleteTechCardDevExpenseResponse{}, nil
}

// ListTechCardDevExpenses returns a tech card's development-cost journal (newest first) plus the
// computed roll-up (total in base, per-kind, amortized unit_cost_with_dev).
func (s *Server) ListTechCardDevExpenses(ctx context.Context, req *pb_admin.ListTechCardDevExpensesRequest) (*pb_admin.ListTechCardDevExpensesResponse, error) {
	tcID := int(req.GetTechCardId())
	if tcID <= 0 {
		return nil, status.Error(codes.InvalidArgument, "tech_card_id is required")
	}
	// The whole journal is confidential cost data; without costing:read there is nothing non-cost
	// to return, so shape it to an empty response (task 19), like ListMaterialPrices.
	if read, _ := s.costingAccess(ctx); !read {
		return &pb_admin.ListTechCardDevExpensesResponse{}, nil
	}
	expenses, err := s.repo.TechCards().ListTechCardDevExpenses(ctx, tcID)
	if err != nil {
		slog.Default().ErrorContext(ctx, "can't list tech card dev expenses", slog.String("err", err.Error()))
		return nil, status.Error(codes.Internal, "can't list tech card dev expenses")
	}
	// Load the card for the amortization basis (order qty + production unit cost). A load failure
	// degrades to a summary without the amortized figure rather than failing the list.
	card, err := s.repo.TechCards().GetTechCardById(ctx, tcID)
	if err != nil {
		slog.Default().WarnContext(ctx, "can't load tech card for dev-cost summary; amortization omitted",
			slog.Int("tech_card_id", tcID), slog.String("err", err.Error()))
		card = nil
	}
	return &pb_admin.ListTechCardDevExpensesResponse{
		Expenses: dto.ConvertEntityDevExpensesToPb(expenses),
		Summary:  dto.ComputeTechCardDevCostSummary(card, expenses, s.costingFx(ctx)),
	}, nil
}
