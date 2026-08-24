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

	// COSTING REDACTION. The audit is classified rd(tech_cards), so its audience is WIDER than
	// costing's: a content manager holding tech_cards:read reaches it. Some findings quote purchase
	// prices and line currencies in prose — the very fields stripTechCardCosting blanks out of
	// GetTechCard for exactly that account. Without this the audit would be a side channel to them.
	findings := result.Findings
	notChecked := result.NotChecked
	if read, _ := s.costingAccess(ctx); !read {
		findings, notChecked = redactMoneyFindings(findings, notChecked)
	}

	return &pb_admin.GetTechCardConstructionAuditResponse{
		Findings:              dto.TechCardAnalysisFindingsToPb(findings),
		OperationFingerprints: result.Fingerprints,
		NotChecked:            notChecked,
		// Enabled() is nil-safe, so an unconfigured deployment answers false rather than panicking.
		// NEITHER THIS NOR THE FINGERPRINTS ARE REDACTED: a fingerprint is a hash of an assembly
		// shape and this is a deployment fact. Neither is money.
		AiEnabled: s.aiOps.Enabled(),
	}, nil
}

// auditNoCostingAccessLine is what the caller is told INSTEAD of the money findings.
//
// Saying it is the whole point. Dropping the findings silently would tell a reader that the card is
// clean on money, which is a claim nobody made and which this very layer exists to refuse: the
// audit already reports what it did not check rather than letting silence pass for a verdict. Here
// the reason is the reader's own rights, and that is still a reason to name.
const auditNoCostingAccessLine = "price checks were not run (no costing access): findings that quote " +
	"purchase prices or line currencies are withheld from this account"

// redactMoneyFindings drops every finding flagged Money and says so in not_checked.
//
// It filters on techcardanalysis.Finding.Money — the flag the CHECK sets next to itself — and not on
// a list of check names kept here. A list here is a place a newly written money check never reaches:
// it would leak by default, quietly. The flag fails the other way round, hiding a finding that is
// visibly missing.
//
// WHOLE FINDINGS, NOT THEIR NUMBERS. "Show the finding without the figures" was considered and
// rejected: «the pocketing costs more per metre than the main fabric» still discloses the RATIO,
// and costing redaction hides the price itself, not merely its digits. A half-redaction is a leak
// that looks like a solved problem.
func redactMoneyFindings(findings []techcardanalysis.Finding, notChecked []string) ([]techcardanalysis.Finding, []string) {
	kept := make([]techcardanalysis.Finding, 0, len(findings))
	withheld := 0
	for _, f := range findings {
		if f.Money {
			withheld++
			continue
		}
		kept = append(kept, f)
	}
	// The line goes in whether or not anything was actually withheld: "no money findings on this
	// card" and "money findings you may not see" must not be distinguishable by their absence,
	// or the line itself becomes the leak it was added to close.
	out := make([]string, 0, len(notChecked)+1)
	out = append(out, notChecked...)
	out = append(out, auditNoCostingAccessLine)
	return kept, out
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
