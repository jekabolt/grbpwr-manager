package admin

import (
	"context"
	"database/sql"
	"errors"
	"log/slog"
	"strings"

	"github.com/jekabolt/grbpwr-manager/internal/dto"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	pb_admin "github.com/jekabolt/grbpwr-manager/proto/gen/admin"
	"github.com/shopspring/decimal"
	pb_decimal "google.golang.org/genproto/googleapis/type/decimal"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// styleEconomicsRunScan caps how many production runs are aggregated for one style. The store's
// maxPageLimit is 100 and a single style realistically has a handful of runs, so this is never a
// real bound (DB scale is small); it exists only to request a full page rather than the default 50.
const styleEconomicsRunScan = 100

// GetStyleEconomics assembles the "style as a business case" card (task 15 part C): one tech card's
// lifetime sales margin, its R&D development-cost roll-up, the number of fitting rounds, and a
// plan/fact production summary. It composes existing building blocks (GetStyleMargin,
// ListTechCardDevExpenses, ListFittings, ListProductionRuns) rather than one monster query. Cost and
// margin fields are stripped for accounts without costing:read (task 19).
func (s *Server) GetStyleEconomics(ctx context.Context, req *pb_admin.GetStyleEconomicsRequest) (*pb_admin.GetStyleEconomicsResponse, error) {
	tcID := int(req.GetTechCardId())
	if tcID <= 0 {
		return nil, status.Error(codes.InvalidArgument, "tech_card_id is required")
	}
	card, err := s.repo.TechCards().GetTechCardById(ctx, tcID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, status.Error(codes.NotFound, "tech card not found")
		}
		slog.Default().ErrorContext(ctx, "style economics: can't load tech card", slog.String("err", err.Error()))
		return nil, status.Error(codes.Internal, "can't load tech card")
	}

	econ := &pb_admin.StyleEconomics{
		TechCardId:  int32(tcID),
		StyleNumber: card.StyleNumber,
		Name:        card.Name,
	}

	// Sales: lifetime margin over the style's colourway SKUs. nil = no sales yet → a zero row that
	// still carries identity and has_cost=false.
	salesRow, err := s.repo.Metrics().GetStyleMargin(ctx, tcID)
	if err != nil {
		slog.Default().ErrorContext(ctx, "style economics: can't get style margin", slog.String("err", err.Error()))
		return nil, status.Error(codes.Internal, "can't compute style margin")
	}
	var grossMargin decimal.Decimal
	hasCost := false
	if salesRow != nil {
		if pb := dto.ConvertMarginByStyleToPb([]entity.MarginByStyleRow{*salesRow}); len(pb) > 0 {
			econ.Sales = pb[0]
		}
		grossMargin = salesRow.GrossMargin
		hasCost = salesRow.HasCost
	} else {
		econ.Sales = &pb_admin.MarginByStyleRow{TechCardId: int32(tcID), StyleNumber: card.StyleNumber, Name: card.Name}
	}

	// Development (R&D) journal roll-up.
	fx := s.costingFx(ctx)
	expenses, err := s.repo.TechCards().ListTechCardDevExpenses(ctx, tcID)
	if err != nil {
		slog.Default().ErrorContext(ctx, "style economics: can't list dev expenses", slog.String("err", err.Error()))
		return nil, status.Error(codes.Internal, "can't load development costs")
	}
	dev := dto.ComputeTechCardDevCostSummary(card, expenses, fx)
	econ.DevCost = dev

	// Fitting rounds: number of fittings recorded for the style (each fitting is a round). We only
	// need the total; passing limit 1 keeps the page cheap.
	_, rounds, err := s.repo.Fittings().ListFittings(ctx, 1, 0, entity.Descending, 0, 0, tcID)
	if err != nil {
		slog.Default().ErrorContext(ctx, "style economics: can't count fittings", slog.String("err", err.Error()))
		return nil, status.Error(codes.Internal, "can't count fittings")
	}
	econ.FittingRounds = int32(rounds)

	// Production plan/fact across the style's runs.
	runs, _, err := s.repo.ProductionRuns().ListProductionRuns(ctx, styleEconomicsRunScan, 0, entity.ProductionRunListFilter{TechCardId: tcID})
	if err != nil {
		slog.Default().ErrorContext(ctx, "style economics: can't list production runs", slog.String("err", err.Error()))
		return nil, status.Error(codes.Internal, "can't load production runs")
	}
	econ.Production = dto.ComputeStyleProductionSummary(runs)

	// Bottom line: net_after_dev = gross_margin − dev_total. Contribution-style, NOT net profit
	// (dev is a period R&D cost, deliberately never folded into unit COGS). Only computable when the
	// style has product cost (else gross_margin is N/A). Caveats surface partial/absent data.
	var caveats []string
	if hasCost {
		devTotal := decimal.Zero
		if dev != nil && dev.TotalBase != nil {
			devTotal, _ = decimal.NewFromString(dev.TotalBase.Value)
		}
		econ.NetAfterDev = &pb_decimal.Decimal{Value: grossMargin.Sub(devTotal).StringFixed(2)}
		if dev != nil && dev.HasUnconverted {
			caveats = append(caveats, "some development costs have no FX rate and are excluded from the total")
		}
	} else {
		caveats = append(caveats, "no product cost set for this style — margin and net result unavailable")
	}
	econ.Caveat = strings.Join(caveats, "; ")

	resp := &pb_admin.GetStyleEconomicsResponse{Economics: econ}
	// Redact confidential cost/margin for accounts without costing:read (task 19).
	if read, _ := s.costingAccess(ctx); !read {
		stripStyleEconomicsCosting(resp)
	}
	return resp, nil
}
