package admin

import (
	"context"
	"database/sql"
	"testing"
	"time"

	mocks "github.com/jekabolt/grbpwr-manager/internal/dependency/mocks"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	pb_admin "github.com/jekabolt/grbpwr-manager/proto/gen/admin"
	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
	pb_decimal "google.golang.org/genproto/googleapis/type/decimal"
)

// TestUpsertOpexLinesHandler: with full access (no scoped authz in ctx) the handler validates the
// lines, folds each amount into base currency via the costing FX (USD 60 → EUR 54 at 0.9), and
// hands them to the store. Mirrors the dev-expense fold path.
func TestUpsertOpexLinesHandler(t *testing.T) {
	repo := mocks.NewMockRepository(t)
	mtr := mocks.NewMockMetrics(t)
	tc := mocks.NewMockTechCards(t)
	repo.EXPECT().Metrics().Return(mtr)
	repo.EXPECT().TechCards().Return(tc)
	tc.EXPECT().GetCostingFxRatesToBase(mock.Anything).Return(
		map[string]decimal.Decimal{"USD": decimal.RequireFromString("0.9")}, nil)

	var captured []entity.OpexLineInsert
	mtr.EXPECT().UpsertOpexLines(mock.Anything, mock.Anything).
		Run(func(_ context.Context, rows []entity.OpexLineInsert) { captured = rows }).
		Return(nil)

	s := &Server{repo: repo}
	_, err := s.UpsertOpexLines(context.Background(), &pb_admin.UpsertOpexLinesRequest{
		Lines: []*pb_admin.OpexLineInsert{
			{Month: "2029-06-15", Category: "software", Label: "Adobe", Amount: &pb_decimal.Decimal{Value: "60"}, Currency: "USD"},
			{Month: "2029-06-01", Category: "software", Label: "Figma", Amount: &pb_decimal.Decimal{Value: "15"}, Currency: "GBP"},
		},
	})
	require.NoError(t, err)
	require.Len(t, captured, 2)

	byLabel := map[string]entity.OpexLineInsert{}
	for _, r := range captured {
		byLabel[r.Label] = r
	}
	// month normalised to the 1st.
	require.Equal(t, time.Date(2029, 6, 1, 0, 0, 0, 0, time.UTC), byLabel["Adobe"].Month)
	require.True(t, byLabel["Adobe"].AmountBase.Valid, "USD folded via rate")
	require.Equal(t, "54", byLabel["Adobe"].AmountBase.Decimal.String())
	require.False(t, byLabel["Figma"].AmountBase.Valid, "GBP has no rate → uncosted")
}

// TestUpsertOpexLinesHandler_Validation: a bad category is rejected before any store/FX call.
func TestUpsertOpexLinesHandler_Validation(t *testing.T) {
	repo := mocks.NewMockRepository(t)
	s := &Server{repo: repo}
	_, err := s.UpsertOpexLines(context.Background(), &pb_admin.UpsertOpexLinesRequest{
		Lines: []*pb_admin.OpexLineInsert{
			{Month: "2029-06-01", Category: "not_a_category", Label: "x", Amount: &pb_decimal.Decimal{Value: "1"}},
		},
	})
	require.Error(t, err)
}

// TestListOpexLinesHandler maps stored lines to protobuf, including the costed flag and recurring id.
func TestListOpexLinesHandler(t *testing.T) {
	repo := mocks.NewMockRepository(t)
	mtr := mocks.NewMockMetrics(t)
	repo.EXPECT().Metrics().Return(mtr)
	mtr.EXPECT().ListOpexLines(mock.Anything, mock.Anything).Return([]entity.OpexLine{
		{
			Id: 1, OpexLineInsert: entity.OpexLineInsert{
				Month: time.Date(2029, 6, 1, 0, 0, 0, 0, time.UTC), Category: "salaries", Label: "Maria",
				Amount: decimal.RequireFromString("1000"), Currency: "USD",
				AmountBase: decimal.NullDecimal{Decimal: decimal.RequireFromString("900"), Valid: true},
				RecurringId: sql.NullInt32{Int32: 7, Valid: true},
			},
		},
		{
			Id: 2, OpexLineInsert: entity.OpexLineInsert{
				Month: time.Date(2029, 6, 1, 0, 0, 0, 0, time.UTC), Category: "software", Label: "Figma",
				Amount: decimal.RequireFromString("15"), Currency: "GBP", // uncosted
			},
		},
	}, nil)

	s := &Server{repo: repo}
	resp, err := s.ListOpexLines(context.Background(), &pb_admin.ListOpexLinesRequest{
		MonthFrom: "2029-06-01", MonthTo: "2029-06-01",
	})
	require.NoError(t, err)
	require.Len(t, resp.Lines, 2)

	byLabel := map[string]*pb_admin.OpexLine{}
	for _, l := range resp.Lines {
		byLabel[l.Label] = l
	}
	require.True(t, byLabel["Maria"].Costed)
	require.Equal(t, "900", byLabel["Maria"].AmountBase.GetValue())
	require.Equal(t, int32(7), byLabel["Maria"].RecurringId)
	require.False(t, byLabel["Figma"].Costed, "no amount_base → not costed")
	require.Equal(t, int32(0), byLabel["Figma"].RecurringId)
}

// TestUpsertOpexRecurringHandler passes the template through and returns the id.
func TestUpsertOpexRecurringHandler(t *testing.T) {
	repo := mocks.NewMockRepository(t)
	mtr := mocks.NewMockMetrics(t)
	repo.EXPECT().Metrics().Return(mtr)
	mtr.EXPECT().UpsertOpexRecurring(mock.Anything, mock.Anything, 0).Return(42, nil)

	s := &Server{repo: repo}
	resp, err := s.UpsertOpexRecurring(context.Background(), &pb_admin.UpsertOpexRecurringRequest{
		Recurring: &pb_admin.OpexRecurringInsert{
			Label: "Maria", Category: "salaries", Amount: &pb_decimal.Decimal{Value: "1000"},
			Currency: "USD", ActiveFrom: "2029-01-01",
		},
	})
	require.NoError(t, err)
	require.Equal(t, int32(42), resp.Id)
}
