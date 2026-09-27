package admin

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/apisrv/apierr"
	"github.com/jekabolt/grbpwr-manager/internal/dto"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	pb_admin "github.com/jekabolt/grbpwr-manager/proto/gen/admin"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// aiSpendMaxDays bounds one report: a leap year, counted inclusively. The presets (this month, last
// month, last 7 days) are the client's (04-DECISIONS D-04); the bound only keeps a custom range from
// asking the ledger for its whole history in one GROUP BY.
const aiSpendMaxDays = 366

// GetAiSpendReport returns the AI spend over the calendar days [from_day, to_day], counted in the org
// timezone: our number from the ledger per provider and per actor × purpose × provider × model, the
// provider's own number beside ours where its cost API reports one. SuperOnly (rbac): it is money.
//
// The period is checked here, before the store is asked anything; the store repeats the day checks
// for its other callers.
func (s *Server) GetAiSpendReport(ctx context.Context, req *pb_admin.GetAiSpendReportRequest) (*pb_admin.GetAiSpendReportResponse, error) {
	if ve := validateAiSpendPeriod(req.GetFromDay(), req.GetToDay()); ve != nil {
		return nil, apierr.Invalid(ve)
	}
	rep, err := s.repo.AI().SpendReport(ctx, req.GetFromDay(), req.GetToDay())
	if err != nil {
		var ve *entity.ValidationError
		if errors.As(err, &ve) {
			return nil, apierr.Invalid(ve)
		}
		slog.Default().ErrorContext(ctx, "can't report ai spend",
			slog.String("from_day", req.GetFromDay()), slog.String("to_day", req.GetToDay()),
			slog.String("err", err.Error()))
		return nil, status.Error(codes.Internal, "can't read the AI spend; try again")
	}
	return dto.AISpendReportToPb(rep), nil
}

// validateAiSpendPeriod refuses a period that is not two YYYY-MM-DD days, runs backwards, or spans
// more than aiSpendMaxDays days counting both ends.
func validateAiSpendPeriod(fromDay, toDay string) *entity.ValidationError {
	from, err := time.Parse(time.DateOnly, fromDay)
	if err != nil {
		return entity.NewFieldViolation("from_day", "bad_day", "", "a calendar day, YYYY-MM-DD")
	}
	to, err := time.Parse(time.DateOnly, toDay)
	if err != nil {
		return entity.NewFieldViolation("to_day", "bad_day", "", "a calendar day, YYYY-MM-DD")
	}
	if to.Before(from) {
		return entity.NewFieldViolation("to_day", "range_reversed", "", "the last day must not precede the first")
	}
	// Both parsed as UTC midnights, so the difference is a whole number of days. A span beyond
	// time.Duration's ~292 years saturates, which still reads as too long.
	if days := int(to.Sub(from)/(24*time.Hour)) + 1; days > aiSpendMaxDays {
		return entity.NewFieldViolation("to_day", "range_too_long", "",
			fmt.Sprintf("at most %d days counting both ends; this period is %d", aiSpendMaxDays, days))
	}
	return nil
}
