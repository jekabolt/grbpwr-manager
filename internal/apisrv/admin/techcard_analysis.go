package admin

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	authsrv "github.com/jekabolt/grbpwr-manager/internal/apisrv/auth"
	"github.com/jekabolt/grbpwr-manager/internal/dto"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/jekabolt/grbpwr-manager/internal/openrouter"
	"github.com/jekabolt/grbpwr-manager/internal/ratelimit"
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

// ── LLM LAYER (design §4) ───────────────────────────────────────────────────────────────────────

// ai_status — the six words §4 puts on the wire. They are the ONLY channel by which an AI failure
// reaches the caller: this RPC answers 200 whatever happens to the model, because the machine half
// of the review is already drawn on the screen and blanking the tab over a retired model slug would
// hide a working report behind a broken one.
const (
	// aiStatusOK — the model answered and the answer survived verification. An empty findings list
	// under this status is a real verdict: the model found nothing.
	aiStatusOK = "ok"
	// aiStatusNotConfigured — no OPENROUTER_API_KEY on this deployment. Nothing was called, nothing
	// was spent. The button is meant to be disabled by ai_enabled long before this is reached.
	aiStatusNotConfigured = "not_configured"
	// aiStatusModelUnavailable — the provider does not serve the slug (HTTP 404). A CONFIGURATION
	// fault: the panel names the slug and does not invite a retry, because nothing about it is
	// transient — this is the failure that already happened once, in production, dressed as weather.
	aiStatusModelUnavailable = "model_unavailable"
	// aiStatusFailed — timeout or transport. This one IS weather and a retry is honest.
	aiStatusFailed = "failed"
	// aiStatusInvalidOutput — the answer was cut by the token ceiling, was not analysis JSON, or
	// lost so many findings to verification that the run cannot be trusted (§8 п.5). No auto-retry:
	// paying twice for the same fault without a diagnosis is the same fault twice.
	aiStatusInvalidOutput = "invalid_output"
	// aiStatusSkipped — the card carries no assembly fact at all, so there is nothing to analyse and
	// the key is not spent (BuildUserPrompt's second value, §1/§7).
	aiStatusSkipped = "skipped"
)

// analysisMaxTokens caps the completion of one run (§5: ~1.5–2.5k tokens of output on a real card).
//
// IT IS A CAP, NOT A TARGET, and hitting it is a FAILED run rather than a shortened one: a truncated
// answer comes back with finish_reason=length, and the verifier refuses it outright instead of
// serving half a review as a whole one.
const analysisMaxTokens = 2500

// The three belts of §12. They are not three spellings of one idea: an in-flight map stops the
// double click, the per-card interval stops the human drumming the button on one card, and the
// per-admin window stops the same human moving to the next card — «лимит раздражения ≠ лимит
// расхода, нужны оба». A card is not a free multiplier of the key.
const (
	analysisMinInterval    = 15 * time.Second
	analysisPerAdminWindow = time.Hour
	analysisPerAdminRuns   = 20
)

// analysisRunKey is (who, which card). The admin is part of it because two people reviewing the
// same card at the same time are two reviews, not a double click; the card is part of it because a
// person analysing two cards in parallel is doing ordinary work.
type analysisRunKey struct {
	admin  string
	cardID int
}

// analysisRunState is what the guard remembers about one key: whether a run is in the air, and when
// the last one landed. An entry whose run is over and whose stamp is older than the interval carries
// no information at all — that is what makes pruning it safe rather than merely tidy.
type analysisRunState struct {
	running  bool
	finished time.Time
}

// analysisRunGuard is the money fence in front of the model call.
//
// IT IS A VALUE FIELD OF Server WITH A LAZY MAP, ON PURPOSE. Tests all over this package build a
// Server as a bare struct literal, and a fence that only exists when New() built it would be absent
// exactly where nobody is looking. noteFormatSem makes the opposite choice for the opposite reason:
// a missing semaphore must be loud because it bounds concurrency, while a missing fence must be
// impossible because it bounds spend.
type analysisRunGuard struct {
	mu   sync.Mutex
	runs map[analysisRunKey]*analysisRunState
	// hourly is the per-ADMIN sliding window, built once on first use. It is keyed by admin alone —
	// the whole point of the third belt is that the card must not multiply it.
	hourly *ratelimit.Limiter
	// nowFn is the clock, injectable so a test can prove the interval EXPIRES. A guard that never
	// released a card would look identical to a correct one in every test that only presses twice.
	nowFn func() time.Time
}

// analysisRunsPruneAt is when the guard bothers to sweep. Below it the map is a rounding error; the
// sweep itself is O(n) and only drops entries that are provably indistinguishable from absent.
const analysisRunsPruneAt = 512

func (g *analysisRunGuard) now() time.Time {
	if g.nowFn != nil {
		return g.nowFn()
	}
	return time.Now()
}

// begin takes all three belts, in the order that makes a refused run cost nothing: the in-flight
// check first (it is the one a double click trips, and a double click must not eat an hourly token),
// then the per-card interval, and only then the hourly window — which is the only belt that SPENDS
// something by being asked.
//
// The returned release stamps the finish. Every path out of the handler runs it, including the ones
// that never reach the model: the belt bounds how often the button may be PRESSED on a card, and a
// press that turned out to be refused by a later gate is still a press.
func (g *analysisRunGuard) begin(key analysisRunKey) (func(), error) {
	g.mu.Lock()
	if g.runs == nil {
		g.runs = make(map[analysisRunKey]*analysisRunState, 8)
	}
	now := g.now()
	if len(g.runs) >= analysisRunsPruneAt {
		g.pruneLocked(now)
	}
	st := g.runs[key]
	if st != nil {
		if st.running {
			g.mu.Unlock()
			return nil, fmt.Errorf("an analysis of this tech card is already running")
		}
		if wait := analysisMinInterval - now.Sub(st.finished); !st.finished.IsZero() && wait > 0 {
			g.mu.Unlock()
			return nil, fmt.Errorf("this tech card was analysed less than %s ago; try again in %s",
				analysisMinInterval, wait.Round(time.Second))
		}
	} else {
		st = &analysisRunState{}
		g.runs[key] = st
	}
	st.running = true
	if g.hourly == nil {
		g.hourly = ratelimit.NewLimiter(analysisPerAdminWindow, analysisPerAdminRuns)
	}
	limiter := g.hourly
	g.mu.Unlock()

	if !limiter.Allow(key.admin) {
		// The hourly window said no, so this run never happens — unmark it, and leave the previous
		// finish stamp alone. Stamping a run that did not occur would push the per-card interval
		// forward for a press that was refused before it cost anything.
		g.mu.Lock()
		st.running = false
		g.mu.Unlock()
		return nil, fmt.Errorf("this account has run %d analyses in the last hour; the limit is there because every run spends the AI key",
			analysisPerAdminRuns)
	}

	return func() {
		g.mu.Lock()
		st.running = false
		st.finished = g.now()
		g.mu.Unlock()
	}, nil
}

// pruneLocked drops the entries that no longer say anything: not running, and last finished longer
// ago than the interval they exist to enforce. Such an entry and a missing entry produce the same
// decision for every possible future call, which is exactly why dropping it changes no behaviour.
func (g *analysisRunGuard) pruneLocked(now time.Time) {
	for k, st := range g.runs {
		if !st.running && now.Sub(st.finished) >= analysisMinInterval {
			delete(g.runs, k)
		}
	}
}

// analysisRun is one pass of the LLM layer, carried from wherever it ended to the single exit.
//
// EVERY FIELD IS FILLED WHETHER THE RUN SUCCEEDED OR NOT, because the log line of a failed run is
// the whole diagnosis: model + base_url separate "the slug is gone" from "the base URL points
// nowhere", and usage says whether a failed run was also a paid one.
type analysisRun struct {
	cardID  int
	status  string
	model   string
	baseURL string

	findings     []techcardanalysis.Finding
	notChecked   []string
	summary      string
	fingerprints map[int32]string

	stats techcardanalysis.VerifyStats
	usage openrouter.Usage
	took  time.Duration
	// err is the failure verbatim, for the log only. It never reaches the client: ai_status is what
	// the panel renders, and a provider's English sentence in a UI field is not a status.
	err error
}

// AnalyzeTechCardConstruction runs the MODEL layer of the CONSTRUCTION review over a saved card.
//
// It returns model findings only. The machine findings are already on the screen from
// GetTechCardConstructionAudit and repeating them here would double every one of them.
//
// THE ORDER OF THIS FUNCTION IS ITS CONTRACT: the three spend belts stand before the card is even
// read, the input gate before the analyser, and the model call last. Anything that can refuse the
// run refuses it before the key is spent.
func (s *Server) AnalyzeTechCardConstruction(ctx context.Context, req *pb_admin.AnalyzeTechCardConstructionRequest) (*pb_admin.AnalyzeTechCardConstructionResponse, error) {
	if req.GetTechCardId() <= 0 {
		return nil, status.Error(codes.InvalidArgument, "tech_card_id is required")
	}
	cardID := int(req.GetTechCardId())

	// THE BELTS COME FIRST, BEFORE THE READ. A press that is refused here must cost nothing at all —
	// not the key, and not a full hydrated tech-card read either, which is the most expensive query
	// on this path.
	release, err := s.analysisRuns.begin(analysisRunKey{admin: authsrv.GetAdminUsername(ctx), cardID: cardID})
	if err != nil {
		return nil, status.Error(codes.ResourceExhausted, err.Error())
	}
	defer release()

	run := analysisRun{cardID: cardID, model: s.aiOps.AnalysisModel(), baseURL: s.aiOps.BaseURL()}

	card, err := s.repo.TechCards().GetTechCardById(ctx, cardID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, status.Error(codes.NotFound, "tech card not found")
		}
		slog.Default().ErrorContext(ctx, "construction analysis: can't load tech card",
			slog.Int("tech_card_id", cardID), slog.String("err", err.Error()))
		return nil, status.Error(codes.Internal, "can't load tech card")
	}
	if card == nil {
		return nil, status.Error(codes.NotFound, "tech card not found")
	}

	// The same input gate as the audit, and for the same reason: an analysis of the first 200 steps
	// of a 260-step route would be a confident verdict about a route it never saw the end of.
	if len(card.Operations) > techcardanalysis.MaxAnalysisOperations {
		return nil, status.Errorf(codes.InvalidArgument,
			"tech card has %d operations; the construction analysis handles at most %d",
			len(card.Operations), techcardanalysis.MaxAnalysisOperations)
	}

	// The machine layer is recomputed and DOES NOT travel in this response (§4). It is not waste: it
	// is the VERIFIED FACTS block of the prompt, the FILED block the model must not repeat, and the
	// set of anchors the verifier deduplicates against.
	fx := s.analysisFx(ctx)
	audit := techcardanalysis.RunAudit(card, fx)
	run.fingerprints = audit.Fingerprints

	if !s.aiOps.Enabled() {
		run.status = aiStatusNotConfigured
		run.err = openrouter.ErrNotConfigured
		return s.finishAnalysis(ctx, run)
	}

	prompt, ok := techcardanalysis.BuildUserPrompt(techcardanalysis.PromptInput{
		Card: card, Audit: audit, GarmentType: s.resolveCategoryName(ctx, card.CategoryId),
	})
	if !ok {
		// No producing step anywhere: the card has no assembly to judge. Sending it would buy a
		// paragraph of the model guessing at an empty route.
		run.status = aiStatusSkipped
		run.err = fmt.Errorf("the card carries no assembly fact: nothing to analyse")
		return s.finishAnalysis(ctx, run)
	}

	started := time.Now()
	raw, finishReason, usage, err := s.aiOps.CompleteWithMeta(
		ctx, techcardanalysis.AnalysisSystemPrompt(), prompt, true, analysisMaxTokens)
	run.took = time.Since(started)
	run.usage = usage
	if err != nil {
		run.err = err
		// 404 is a configuration fault and everything else on this path is weather. The split is by
		// SENTINEL, never by reading the provider's prose — see openrouter.ErrModelUnavailable.
		run.status = aiStatusFailed
		if errors.Is(err, openrouter.ErrModelUnavailable) {
			run.status = aiStatusModelUnavailable
		}
		return s.finishAnalysis(ctx, run)
	}

	findings, notChecked, summary, stats, err := techcardanalysis.VerifyModelRun(
		raw, finishReason, card, fx, audit.Findings)
	run.stats = stats
	if err != nil {
		run.err = err
		run.status = aiStatusInvalidOutput
		return s.finishAnalysis(ctx, run)
	}

	run.status = aiStatusOK
	run.findings, run.notChecked, run.summary = findings, notChecked, summary
	return s.finishAnalysis(ctx, run)
}

// finishAnalysis is the ONE exit of the LLM layer: it logs the run and puts it on the wire.
//
// THERE IS EXACTLY ONE OF IT SO THAT THE COSTING REDACTION CANNOT BE BYPASSED. Six statuses reach
// this function; a handler that returned a response from any of them directly would be one edit away
// from shipping a model finding that quotes a purchase price to an account that may not see prices.
// Making the un-redacted return unrepresentable is cheaper than remembering to redact six times.
func (s *Server) finishAnalysis(ctx context.Context, run analysisRun) (*pb_admin.AnalyzeTechCardConstructionResponse, error) {
	logAnalysisRun(ctx, run)

	findings, notChecked, summary := run.findings, run.notChecked, run.summary
	if read, _ := s.costingAccess(ctx); !read {
		summary, notChecked = withholdModelProse(summary, notChecked)
		findings, notChecked = redactMoneyFindings(findings, notChecked)
	}

	return &pb_admin.AnalyzeTechCardConstructionResponse{
		Findings:   dto.TechCardAnalysisFindingsToPb(findings),
		AiStatus:   run.status,
		Model:      run.model,
		NotChecked: notChecked,
		Summary:    summary,
		// The counters travel even on a failed run: zero-of-zero and "the run never got that far"
		// are different facts, and ai_status is what tells them apart.
		DroppedBadRef:         int32(run.stats.DroppedBadRef),
		DroppedContradiction:  int32(run.stats.DroppedContradiction),
		OperationFingerprints: run.fingerprints,
	}, nil
}

// analysisProseWithheldMsg replaces the model's summary for an account without costing access.
//
// IT IS A SENTENCE, NOT AN EMPTY STRING, for the same reason auditNoCostingAccessLine exists: a
// blank field reads as "the model had nothing to say", which is a claim nobody made.
const analysisProseWithheldMsg = "the model's summary and its not-checked notes are withheld from this " +
	"account (no costing access): they are free prose, and prose can quote a purchase price or a line " +
	"currency in a sentence no field-level filter can reach"

// withholdModelProse suppresses the two model outputs that CANNOT carry a Money flag.
//
// THE HOLE IT CLOSES. Finding.Money is set next to the check that discloses money, and
// redactMoneyFindings drops flagged findings whole. `summary` and the model's `not_checked` lines
// are neither findings nor fields — there is nowhere on them to put a flag. A model that writes
// «the pocketing is dearer per metre than the shell» into the summary would ship exactly the RATIO
// that redaction exists to hide, while the findings half of the same response was correctly cleaned.
//
// SUPPRESSION, NOT SCREENING, AND THAT IS A DECISION. Re-deriving «does this sentence disclose
// money» here would be a SECOND copy of the rule that lives beside the checks — and a second copy
// drifts, always toward leaking, because a rule that under-flags produces no visible symptom. The
// verifier exposes no screening helper, and exporting its internals to borrow one would spread the
// rule instead of keeping it in one place. So the whole paragraph goes, and the reader is told.
//
// EVERY not_checked LINE HERE IS THE MODEL'S. The machine layer's own list travels with the audit
// response, never with this one, so dropping the list wholesale costs no machine-produced line.
func withholdModelProse(summary string, modelNotChecked []string) (string, []string) {
	if strings.TrimSpace(summary) != "" {
		summary = analysisProseWithheldMsg
	}
	// The list is read only to be dropped, and it is dropped whether or not it holds anything: the
	// sentence above already tells the reader it is gone, and a list that survived "sometimes" would
	// leak by exactly the door the always-present line closes.
	_ = modelNotChecked
	return summary, nil
}

// logAnalysisRun writes ONE record per run — §8 п.9 and §12, which ask for the run, the drops and
// the money in the same place.
//
// ONE RECORD, NOT SEVERAL, AND ITS LEVEL IS THE VERDICT. A non-ok run is an Error and there is
// exactly one of them, so «how often did the analysis fail last week» is a count and not a join. An
// ok run that dropped findings is a Warn: the run worked, the model partly did not. A clean run is
// an Info that exists for one reason — usage. A per-press call to a paid API whose cost never
// reaches the log is a bill nobody can see (§12).
//
// EVERY DROP IS PRINTED VERBATIM, with the model's own title and the note saying what did not
// resolve. A count alone would say that the verifier is dropping things and never say what, which is
// precisely the information needed to tell a broken prompt from a broken model.
func logAnalysisRun(ctx context.Context, run analysisRun) {
	attrs := []any{
		slog.Int("tech_card_id", run.cardID),
		slog.String("ai_status", run.status),
		slog.String("model", run.model),
		slog.String("base_url", run.baseURL),
		slog.Int("emitted", run.stats.Emitted),
		slog.Int("dropped_bad_ref", run.stats.DroppedBadRef),
		slog.Int("dropped_contradiction", run.stats.DroppedContradiction),
		slog.Int("truncated", run.stats.Truncated),
		slog.Int("prompt_tokens", run.usage.Prompt),
		slog.Int("completion_tokens", run.usage.Completion),
		slog.Int("total_tokens", run.usage.Total),
		slog.Duration("took", run.took),
	}
	if run.err != nil {
		attrs = append(attrs, slog.String("err", run.err.Error()))
	}
	if len(run.stats.Drops) > 0 {
		attrs = append(attrs, slog.Any("drops", analysisDropLines(run.stats.Drops)))
	}
	if len(run.stats.Coercions) > 0 {
		attrs = append(attrs, slog.Any("coercions", run.stats.Coercions))
	}

	switch {
	case run.status != aiStatusOK:
		slog.Default().ErrorContext(ctx, "tech-card construction analysis did not complete", attrs...)
	case len(run.stats.Drops) > 0 || run.stats.Truncated > 0:
		slog.Default().WarnContext(ctx, "tech-card construction analysis dropped model findings", attrs...)
	default:
		slog.Default().InfoContext(ctx, "tech-card construction analysis", attrs...)
	}
}

// analysisDropLines renders each discarded finding as one line: why it went, what the model called
// it, and which anchors it offered. The model's own words, not our summary of them.
func analysisDropLines(drops []techcardanalysis.Drop) []string {
	out := make([]string, 0, len(drops))
	for _, d := range drops {
		out = append(out, fmt.Sprintf("%s: %q (%s) refs=%v", d.Reason, d.Title, d.Note, d.Refs))
	}
	return out
}

// ── FILING AN ISSUE ─────────────────────────────────────────────────────────────────────────────

// analysisIssueSeverities maps the wire tokens onto the column's vocabulary (0072:
// `severity REGEXP '^(low|medium|high)$'`). The wire is upper-case because that is how the request
// message spells it; the column is lower-case because that is what the CHECK constraint accepts, and
// sending the wire spelling straight through would be a 3819 on every single call.
var analysisIssueSeverities = map[string]entity.TechCardIssueSeverity{
	"HIGH":   entity.IssueSeverityHigh,
	"MEDIUM": entity.IssueSeverityMedium,
	"LOW":    entity.IssueSeverityLow,
}

// maxIssueDescriptionRunes bounds what one call may write. The column is TEXT and would take far
// more; the bound is here so a paste of an entire document cannot become a row nobody can read in a
// list. It is generous on purpose — the whole point of the gesture is to write down what happened.
const maxIssueDescriptionRunes = 4000

// AddTechCardIssue files ONE issue against a tech card.
//
// IT WORKS ON A FROZEN CARD, AND THAT IS ITS REASON TO EXIST. UpdateTechCard refuses a released
// card, and «this cannot be sewn as specified» is exactly the kind of thing found after release.
// Issues are outside the CONSTRUCTION digest, so this call cannot disturb a signature — the narrow
// path is safe, not merely convenient, and it does NOT go anywhere near the full-write machinery.
//
// raised_by IS STAMPED FROM THE TOKEN, NEVER TAKEN FROM THE REQUEST. UpdateTechCard is not the model
// to copy here: its dto passes the client's raised_by through untouched, so on that path the field
// is whatever the caller typed. On a filing gesture that would make the author of a complaint an
// input field.
func (s *Server) AddTechCardIssue(ctx context.Context, req *pb_admin.AddTechCardIssueRequest) (*pb_admin.AddTechCardIssueResponse, error) {
	if req.GetTechCardId() <= 0 {
		return nil, status.Error(codes.InvalidArgument, "tech_card_id is required")
	}
	description := strings.TrimSpace(req.GetDescription())
	if description == "" {
		return nil, status.Error(codes.InvalidArgument, "description is required: an issue with no text is a row nobody can act on")
	}
	if len([]rune(description)) > maxIssueDescriptionRunes {
		return nil, status.Errorf(codes.InvalidArgument, "description is longer than %d characters", maxIssueDescriptionRunes)
	}
	severity, ok := analysisIssueSeverities[strings.ToUpper(strings.TrimSpace(req.GetSeverity()))]
	if !ok {
		return nil, status.Error(codes.InvalidArgument, "severity must be one of HIGH, MEDIUM, LOW")
	}
	if req.GetOperationNumber() < 0 {
		return nil, status.Error(codes.InvalidArgument, "operation_number cannot be negative")
	}

	issue := entity.TechCardIssue{
		// 0 is «about the card as a whole» and is stored as NO LINK, not as step zero: a row
		// pointing at operation 0 would be a link to a step that cannot exist, and every reader
		// joining on the number would have to know that one number is a lie.
		OperationNumber: sql.NullInt32{Int32: req.GetOperationNumber(), Valid: req.GetOperationNumber() > 0},
		Severity:        severity,
		// A newly filed issue is open by definition; there is no wire field for it and there should
		// not be one.
		Status:      entity.IssueStatusOpen,
		Description: description,
	}
	if username := authsrv.GetAdminUsername(ctx); username != "" {
		issue.RaisedBy = sql.NullString{String: username, Valid: true}
	}

	id, err := s.repo.TechCards().AddTechCardIssue(ctx, int(req.GetTechCardId()), issue)
	if err != nil {
		// tech_card_id is the ONLY foreign key on this row, so a violation says exactly one thing and
		// NotFound says it. (task.go answers InvalidArgument for its FK failures because that row has
		// nine of them and the caller has to be told which family of ids to look at; here there is
		// nothing to disambiguate, and «that card is gone» is the same sentence the audit gives.)
		if s.repo.IsErrForeignKeyViolation(err) {
			return nil, status.Error(codes.NotFound, "tech card not found")
		}
		slog.Default().ErrorContext(ctx, "can't add tech card issue",
			slog.Int("tech_card_id", int(req.GetTechCardId())), slog.String("err", err.Error()))
		return nil, status.Error(codes.Internal, "can't add tech card issue")
	}
	return &pb_admin.AddTechCardIssueResponse{IssueId: int32(id)}, nil
}
