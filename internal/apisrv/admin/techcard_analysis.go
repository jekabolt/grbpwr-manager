package admin

import (
	"context"
	"database/sql"
	"errors"
	"log/slog"

	"github.com/jekabolt/grbpwr-manager/internal/dto"
	"github.com/jekabolt/grbpwr-manager/internal/techcardanalysis"
	pb_admin "github.com/jekabolt/grbpwr-manager/proto/gen/admin"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// GetTechCardConstructionAudit runs the MACHINE layer of the CONSTRUCTION review over a saved tech
// card and returns its findings, the operation fingerprints of that run, what the run did not check
// and whether this deployment has the LLM layer configured at all.
//
// It is free in every sense: no OpenRouter key, no rate limit, no in-flight map, no dismissals.
// The client calls it on tab open and after every save, and it is expected to.
//
// WHY IT IS THE CARRIER OF ai_enabled. The CONSTRUCTION tab needs to know whether to offer the
// Analyze button before anybody presses it, and the admin has no page-config channel. This is the
// one call the tab always makes, so the flag rides here — additive, and one round trip instead of
// a button that only reveals itself as dead after a click.
func (s *Server) GetTechCardConstructionAudit(ctx context.Context, req *pb_admin.GetTechCardConstructionAuditRequest) (*pb_admin.GetTechCardConstructionAuditResponse, error) {
	if req.GetTechCardId() <= 0 {
		return nil, status.Error(codes.InvalidArgument, "tech_card_id is required")
	}

	// The repository hydrates operations with their inputs — the same read UpdateTechCard lives on
	// — so the assembly graph the audit recomputes is the graph the card actually stores.
	card, err := s.repo.TechCards().GetTechCardById(ctx, int(req.GetTechCardId()))
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, status.Error(codes.NotFound, "tech card not found")
		}
		slog.Default().ErrorContext(ctx, "construction audit: can't load tech card",
			slog.Int("tech_card_id", int(req.GetTechCardId())), slog.String("err", err.Error()))
		return nil, status.Error(codes.Internal, "can't load tech card")
	}
	if card == nil {
		return nil, status.Error(codes.NotFound, "tech card not found")
	}

	// INPUT GATE. techcardanalysis.MaxAnalysisOperations is the ceiling, and it is read from the
	// analyzer rather than re-declared here precisely so there is only one of it. It is NOT
	// openrouter.maxOperations: that one silently slices the generator's OUTPUT, this one refuses an
	// oversized INPUT out loud. Refusing beats truncating — an audit of the first 200 steps of a
	// 260-step card would report "the route never packs" about a route that packs at step 240.
	if len(card.Operations) > techcardanalysis.MaxAnalysisOperations {
		return nil, status.Errorf(codes.InvalidArgument,
			"tech card has %d operations; the construction analysis handles at most %d",
			len(card.Operations), techcardanalysis.MaxAnalysisOperations)
	}

	result := techcardanalysis.RunAudit(card, s.analysisFx(ctx))

	return &pb_admin.GetTechCardConstructionAuditResponse{
		Findings:              dto.TechCardAnalysisFindingsToPb(result.Findings),
		OperationFingerprints: result.Fingerprints,
		NotChecked:            result.NotChecked,
		// Enabled() is nil-safe, so an unconfigured deployment answers false rather than panicking.
		AiEnabled: s.aiOps.Enabled(),
	}, nil
}

// analysisFx hands the analyzer the currency channel its money checks need: the manual rates to the
// base currency, and the base itself.
//
// THE ANALYZER CANNOT FETCH THESE ITSELF, and that is the point of the package: it never touches a
// database, so everything outside entity.TechCard arrives as an argument. Rates live in
// costing_fx_rate and the base in the boot cache, both of which are the handler's business.
//
// Built on s.costingFx so there is exactly one reader of the rate table on the tech-card path,
// including its failure mode: a load error degrades to NO rates, never to a request failure. That
// degradation is visible rather than silent — with no rates the money checks say "PLN has no rate
// to EUR: 3 lines drop out of the cost total" instead of quietly pretending a zloty is a euro.
//
// The margin/VAT fields of dto.CostingFx are dropped deliberately: the audit judges the card, not
// the price it should be sold at, and handing it a target margin would invite a check that reads
// like pricing advice.
func (s *Server) analysisFx(ctx context.Context) techcardanalysis.Fx {
	fx := s.costingFx(ctx)
	return techcardanalysis.Fx{ToBase: fx.ToBase, Base: fx.Base}
}
