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

// productionRunPageSize is the page listAllProductionRuns walks with — the store's own maxPageLimit,
// so a page is never silently clamped smaller than asked and the walk takes the fewest round trips.
// It is a page size, NOT a cap: see listAllProductionRuns.
const productionRunPageSize = 100

// styleEconomicsFittingScan is how many fitting rounds are scanned for the style card. A style has a
// handful of rounds; this only asks for a full page rather than the default. The ROUND COUNT printed
// on the card is the store's total and is unaffected by it.
const styleEconomicsFittingScan = 100

// listAllProductionRuns returns EVERY run matching filter, walking the store's pages until the list
// is exhausted, and is the ONLY way this package reads a style's runs.
//
// Both callers divide by these runs — R&D is amortised over Σ planned_qty and the plan/fact summary
// is aggregated over the same slice — so a single page was a silent ceiling on a DENOMINATOR: a
// style with 101 runs amortised its whole development spend over the first 100, and the R&D-per-unit
// it printed was too high by exactly the share of the batch it could not see. The returned total was
// discarded, so nothing anywhere said the number was partial.
//
// Paging rather than a new SQL aggregate: the summary needs the run ROWS (statuses, lines, costs),
// not just Σ planned_qty, so an aggregate would have had to live beside a still-truncated list — two
// reads answering the same question differently on the same card, which is the very thing the shared
// scan depth was introduced to prevent. Scale makes the loop free: a style's runs are counted in
// units, so this is one query in practice and the store's total ends it.
func (s *Server) listAllProductionRuns(ctx context.Context, filter entity.ProductionRunListFilter) ([]entity.ProductionRun, error) {
	var all []entity.ProductionRun
	for {
		// Offset is len(all), not a page counter: the store orders by id DESC, so the next page starts
		// exactly where the collected rows end even if a page came back short.
		page, total, err := s.repo.ProductionRuns().ListProductionRuns(ctx, productionRunPageSize, len(all), filter)
		if err != nil {
			return nil, err
		}
		all = append(all, page...)
		// An empty page is the definitive end (it also guarantees termination whatever total says);
		// reaching the store's own count is the normal one.
		if len(page) == 0 || len(all) >= total {
			return all, nil
		}
	}
}

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
		StyleNumber: card.StyleNumber.String,
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
		econ.Sales = &pb_admin.MarginByStyleRow{TechCardId: int32(tcID), StyleNumber: card.StyleNumber.String, Name: card.Name}
	}

	// Development (R&D) journal roll-up.
	fx := s.costingFx(ctx)
	expenses, err := s.repo.TechCards().ListTechCardDevExpenses(ctx, tcID)
	if err != nil {
		slog.Default().ErrorContext(ctx, "style economics: can't list dev expenses", slog.String("err", err.Error()))
		return nil, status.Error(codes.Internal, "can't load development costs")
	}

	// Fitting rounds: recorded fittings for the style (each fitting is a round). Fetch the rounds
	// themselves (not just a count) — they carry round_number/outcome/date that drive the dev-cost
	// round attribution and time-to-approval rollup (Q8/S20).
	fittings, rounds, err := s.repo.Fittings().ListFittings(ctx, styleEconomicsFittingScan, 0, entity.Descending, 0, 0, tcID)
	if err != nil {
		slog.Default().ErrorContext(ctx, "style economics: can't count fittings", slog.String("err", err.Error()))
		return nil, status.Error(codes.Internal, "can't count fittings")
	}
	econ.FittingRounds = int32(rounds)

	// The style's production runs — ALL of them (listAllProductionRuns explains why a page was not
	// enough) — loaded BEFORE the dev roll-up because that roll-up now amortises R&D over them
	// (Σ planned_qty), not over the card's declared typical run. This is the same slice the plan/fact
	// summary below aggregates, so the "planned quantity" behind the amortisation and the one printed
	// as planned_qty_total on the very same card are one number, not two reads that could answer
	// differently.
	runs, err := s.listAllProductionRuns(ctx, entity.ProductionRunListFilter{TechCardId: tcID})
	if err != nil {
		slog.Default().ErrorContext(ctx, "style economics: can't list production runs", slog.String("err", err.Error()))
		return nil, status.Error(codes.Internal, "can't load production runs")
	}

	dev := dto.ComputeTechCardDevCostSummary(card, expenses, fittings, runs, fx)
	econ.DevCost = dev

	// Production plan/fact across the style's runs. The material actuals issued from the warehouse
	// (net of returns, non-cancelled runs) fold into the run-level and now the style-level actual, so
	// fetch them first and pass them in (nf09-02) — the run detail and this roll-up must agree.
	matFromStock, err := s.repo.Metrics().GetStyleMaterialsFromStock(ctx, tcID)
	if err != nil {
		slog.Default().ErrorContext(ctx, "style economics: can't get materials from stock", slog.String("err", err.Error()))
		return nil, status.Error(codes.Internal, "can't get materials from stock")
	}
	hasStockMaterials := !matFromStock.Base.IsZero() || matFromStock.HasUncosted
	econ.Production = dto.ComputeStyleProductionSummary(runs, matFromStock.Base, hasStockMaterials)

	// Samples (NF-09): how many, and the warehouse material they consumed. Informational only — sample
	// material is R&D spend, deliberately NOT folded into net_after_dev.
	sampleSummary, err := s.repo.Metrics().GetStyleSampleSummary(ctx, tcID)
	if err != nil {
		slog.Default().ErrorContext(ctx, "style economics: can't summarise samples", slog.String("err", err.Error()))
		return nil, status.Error(codes.Internal, "can't summarise samples")
	}
	econ.SamplesCount = int32(sampleSummary.Count)
	econ.SamplesCostBase = &pb_decimal.Decimal{Value: sampleSummary.MaterialsCostBase.StringFixed(2)}

	materialsUncosted := sampleSummary.HasUncosted || matFromStock.HasUncosted

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
	} else if salesRow == nil {
		caveats = append(caveats, "no sales yet for this style — margin and net result unavailable")
	} else {
		caveats = append(caveats, "no cost snapshots on this style's sales (uncosted at sale time) — margin and net result unavailable")
	}
	if materialsUncosted {
		caveats = append(caveats, "some material issues have no unit cost — sample/production material figures understate")
	}
	// Samples are R&D: their warehouse material (samples_cost_base) is deliberately OUTSIDE
	// net_after_dev, and a manual kind=sample dev-expense covering the same fabric would overlap it —
	// spell that out so the operator doesn't eyeball-add the two (nf09-05).
	if sampleSummary.MaterialsCostBase.GreaterThan(decimal.Zero) {
		caveats = append(caveats, "sample materials from stock are not included in net_after_dev; a manual kind=sample dev expense may overlap this figure")
	}
	econ.Caveat = strings.Join(caveats, "; ")

	resp := &pb_admin.GetStyleEconomicsResponse{Economics: econ}
	// Redact confidential cost/margin for accounts without costing:read (task 19).
	if read, _ := s.costingAccess(ctx); !read {
		stripStyleEconomicsCosting(resp)
	}
	return resp, nil
}
