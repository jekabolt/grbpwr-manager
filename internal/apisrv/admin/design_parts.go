package admin

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"math"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jekabolt/grbpwr-manager/internal/aiprov"
	authsrv "github.com/jekabolt/grbpwr-manager/internal/apisrv/auth"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	pb_admin "github.com/jekabolt/grbpwr-manager/proto/gen/admin"
	"golang.org/x/sync/singleflight"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// ─────────────── AUTO PARTS (2026-10-04, Ф2) ───────────────
//
// The client cuts a side's flat into numbered regions (its own deterministic cut, algo_rev), draws
// the numbers on a picture (marks) and asks here: the model groups the numbers into garment parts
// and names them (Set-of-Mark, F0-FINDINGS §3). One sync vision+JSON call, the GenerateDesignQuiz
// skeleton; the cleaned answer is cached per (card, view, flat media, algo_rev) in 0390.
//
// Doors, in order, all before money: the arguments → the card and the side's flat (the base must
// still stand in the slot) → the cache (a hit answers without a call) → the band flag → the purpose
// is callable → the marks picture → one flight per key → the shared fences (enhanceSem + the
// hourly window shared with EnhanceText, the ideas door and the quiz).

const (
	designPartsMaxTokens    = 4000
	designPartsEffort       = "low"
	designPartsFlightMargin = 10 * time.Second

	designPartsNotConfiguredMsg = "naming the parts is not configured: " + openRouterNoKeyMsg
	designPartsModelUnavailMsg  = "naming the parts is misconfigured: " + modelUnavailableAdviceMsg
	designPartsFlatChangedMsg   = "the flat changed · reopen"
	designPartsTooManyMsg       = "too many pieces to name · paint with the pen"
	designPartsUnusableMsg      = "the assistant answered nothing usable"
)

// designPartsSystemPrompt — fixed text (f0/som.py PROMPT plus the naming rules the FRONT↔BACK
// transfer needs). Nothing of the request reaches the system role.
const designPartsSystemPrompt = `This is a technical fashion flat of a garment. Its line drawing has been cut into numbered regions (each region is tinted and carries a red number). Some regions are only fragments of one garment part: strips between pleat/fold lines, topstitching channels, fringe bits, a pocket split from its flap, etc.

Group the region numbers into the real garment PARTS a designer could make from different materials (body panels, yoke, sleeves, cuffs, collar, collar stand, placket, pocket, pocket flap, waistband, hem band, straps, bodice cups, skirt, trims...). Left and right sleeves/cuffs are separate parts.

Rules:
- Every region number must appear in exactly one part. A region that is noise goes into the part it sits on.
- Left and right are the WEARER'S left and right, never the viewer's: on a front view the wearer's left sleeve is on the right side of the picture, on a back view it is on the left side.
- Labels are lowercase, at most 3 words, standard garment part names ("left sleeve", "collar", "front body", "back yoke", "left pocket flap").
- The same part seen on the front and on the back view gets the same label ("left sleeve", "collar", "waistband"), so the two views can be matched by name.
- If ONE region clearly spans two parts whose seam line is missing in the drawing, put it in the part it mostly belongs to and also list it in "split_needed".

Answer with JSON only:
{"parts":[{"label":"left sleeve","regions":[3,7]}, ...], "split_needed":[{"region":5,"why":"yoke and back body share it"}]}`

// designPartsViewWords — the user turn's name of each side.
var designPartsViewWords = map[string]string{
	entity.DesignViewFront: "front view",
	entity.DesignViewBack:  "back view",
	entity.DesignViewSideL: "left side view",
	entity.DesignViewSideR: "right side view",
}

// designPartsUserPrompt — the side and the region count; the picture travels beside it.
func designPartsUserPrompt(view string, regionCount int) string {
	return fmt.Sprintf("This is the %s flat of the garment, cut into %d numbered regions (1..%d). Group every region number into named parts.",
		designPartsViewWords[view], regionCount, regionCount)
}

// designPartsFlightAnswer — what one flight hands every press that waited on it.
type designPartsFlightAnswer struct {
	suggestion *entity.DesignPartsSuggestion
	cached     bool
}

// SuggestDesignParts names the parts of one cut of a side's flat (cached).
func (s *Server) SuggestDesignParts(ctx context.Context, req *pb_admin.SuggestDesignPartsRequest) (*pb_admin.SuggestDesignPartsResponse, error) {
	cardID, view := int(req.GetTechCardId()), req.GetView()
	base, marks, count := int(req.GetBaseMediaId()), int(req.GetMarksMediaId()), int(req.GetRegionCount())
	algoRev := strings.TrimSpace(req.GetAlgoRev())
	switch {
	case cardID <= 0:
		return nil, status.Error(codes.InvalidArgument, "tech_card_id is required")
	case !entity.IsDesignCardinalView(view):
		return nil, status.Errorf(codes.InvalidArgument, "view %q is not a side of the bench", view)
	case base <= 0:
		return nil, status.Error(codes.InvalidArgument, "base_media_id is required")
	case marks <= 0:
		return nil, status.Error(codes.InvalidArgument, "marks_media_id is required")
	case algoRev == "" || len(algoRev) > entity.DesignPartsMaxAlgoRev:
		return nil, status.Errorf(codes.InvalidArgument, "algo_rev must be 1..%d characters", entity.DesignPartsMaxAlgoRev)
	case count < entity.DesignPartsMinRegions || count > entity.DesignPartsMaxRegions:
		return nil, status.Error(codes.FailedPrecondition, designPartsTooManyMsg)
	}

	flats, err := s.repo.Design().FlatBenchMedia(ctx, cardID)
	if err != nil {
		return nil, designError(ctx, "failed to read the flat bench", err, map[string]string{"tech_card_id": strconv.Itoa(cardID)})
	}
	if flats[view] != base {
		return nil, status.Error(codes.FailedPrecondition, designPartsFlatChangedMsg)
	}
	if !req.GetForce() {
		hit, err := s.repo.Design().GetPartsSuggestion(ctx, cardID, view, base, algoRev)
		if err != nil {
			return nil, designError(ctx, "failed to read the parts suggestion", err, nil)
		}
		if hit != nil {
			return &pb_admin.SuggestDesignPartsResponse{Suggestion: designPartsSuggestionToPb(*hit), Cached: true}, nil
		}
	}

	if err := s.designGenerationGate(); err != nil {
		return nil, err
	}
	const purpose = entity.AIPurposeDesignParts
	if !s.ai.Enabled(purpose) {
		return nil, s.aiOffRefusal(purpose, designPartsNotConfiguredMsg)
	}

	// The marks picture: ours, a file, a picture the provider can read.
	if err := s.repo.Design().AssertMediaNotForeign(ctx, cardID, []int{marks}); err != nil {
		return nil, designError(ctx, "the marks picture is refused", err, nil)
	}
	urls, attached, err := s.designBoardPictureURLs(ctx, []int{marks})
	if err != nil {
		slog.Default().ErrorContext(ctx, "design parts: cannot resolve the marks picture",
			slog.Int("tech_card_id", cardID), slog.String("err", err.Error()))
		return nil, status.Error(codes.Internal, "cannot read the marks picture")
	}
	if len(urls) != 1 {
		return nil, status.Errorf(codes.InvalidArgument, "marks_media_id %d has no file", marks)
	}
	if ref, ct, bad := designFirstNonPictureInput([]designInputMediaRef{{ID: attached[0], URL: urls[0], Where: "the marks of this side"}}); bad {
		return nil, designNonPictureRefusal(ref, ct)
	}

	// ⚠ ONE FLIGHT PER (card, view, flat, cut): a double press pays once. Detached from the leader's
	// cancellation under its own budget, like the quiz.
	key := fmt.Sprintf("%d|%s|%d|%s", cardID, view, base, algoRev)
	force := req.GetForce()
	ch := s.partsFlight.DoChan(key, func() (any, error) {
		fctx, cancel := context.WithTimeout(context.WithoutCancel(ctx),
			s.ai.ChainBudget(purpose, designPartsMaxTokens)+designPartsFlightMargin)
		defer cancel()
		return s.designPartsCall(fctx, cardID, view, base, algoRev, count, urls[0], force)
	})
	var res singleflight.Result
	select {
	case res = <-ch:
	case <-ctx.Done():
		return nil, status.FromContextError(ctx.Err()).Err()
	}
	if res.Err != nil {
		return nil, res.Err
	}
	ans := res.Val.(designPartsFlightAnswer)
	return &pb_admin.SuggestDesignPartsResponse{Suggestion: designPartsSuggestionToPb(*ans.suggestion), Cached: ans.cached}, nil
}

// designPartsCall — the fences and the ONE provider call (the flight leader's work).
func (s *Server) designPartsCall(ctx context.Context, cardID int, view string, base int, algoRev string, count int, marksURL string, force bool) (designPartsFlightAnswer, error) {
	const purpose = entity.AIPurposeDesignParts
	// A flight that finished just before this one already paid: read the cache again.
	if !force {
		hit, err := s.repo.Design().GetPartsSuggestion(ctx, cardID, view, base, algoRev)
		if err != nil {
			return designPartsFlightAnswer{}, designError(ctx, "failed to read the parts suggestion", err, nil)
		}
		if hit != nil {
			return designPartsFlightAnswer{suggestion: hit, cached: true}, nil
		}
	}

	// The fences of EnhanceText, THE SAME ONES: one semaphore, one hourly window.
	select {
	case s.enhanceSem <- struct{}{}:
		defer func() { <-s.enhanceSem }()
	default:
		return designPartsFlightAnswer{}, status.Error(codes.ResourceExhausted, "the assistant is busy right now — try again in a moment")
	}
	if !s.enhanceRuns.allow(authsrv.GetAdminUsername(ctx)) {
		return designPartsFlightAnswer{}, status.Errorf(codes.ResourceExhausted,
			"this account has used the assistant %d times in the last hour (ideas, text improvements, the quiz and the parts share the limit); every call spends the AI key — try again later",
			enhancePerAdminCalls)
	}

	started := time.Now()
	res, err := s.ai.Chat(ctx, purpose, aiprov.ChatRequest{
		System: designPartsSystemPrompt, User: designPartsUserPrompt(view, count), ImageURLs: []string{marksURL},
		UserAsParts: true, JSONMode: true, MaxTokens: designPartsMaxTokens, Effort: designPartsEffort,
	})
	var (
		raw, finishReason string
		usage             aiprov.TokenUsage
	)
	if res != nil {
		raw, finishReason, usage = res.Text, res.FinishReason, res.Usage
	}
	answered := s.aiModelOf(purpose, res)
	logAttrs := []any{
		slog.Int("tech_card_id", cardID), slog.String("view", view), slog.Int("base_media_id", base),
		slog.String("algo_rev", algoRev), slog.Int("regions", count), slog.String("model", answered),
		slog.Duration("took", time.Since(started)), slog.String("finish_reason", enhanceLogFinishReason(finishReason)),
		slog.Int("prompt_tokens", usage.Prompt), slog.Int("completion_tokens", usage.Completion),
	}
	if err != nil {
		return designPartsFlightAnswer{}, s.designPartsChatFailure(ctx, res, err, logAttrs)
	}

	parts, splits, ok := parseDesignParts(raw, count)
	if !ok {
		slog.Default().ErrorContext(ctx, "design parts: the answer is not the promised JSON", logAttrs...)
		return designPartsFlightAnswer{}, status.Error(codes.Internal, designPartsUnusableMsg)
	}
	saved, err := s.repo.Design().SavePartsSuggestion(ctx, entity.DesignPartsSuggestion{
		TechCardId: cardID, View: view, BaseMediaId: base, AlgoRev: algoRev,
		Parts: parts, SplitNeeded: splits, Model: answered, CreatedBy: designActor(ctx),
	})
	if err != nil {
		return designPartsFlightAnswer{}, designError(ctx, "failed to save the parts suggestion", err, nil)
	}
	slog.Default().InfoContext(ctx, "design parts", append(logAttrs, slog.Int("parts", len(parts)),
		slog.Int("split_needed", len(splits)))...)
	return designPartsFlightAnswer{suggestion: saved}, nil
}

// designPartsChatFailure maps a failed chat.design_parts call to its refusal (and logs it) — the
// per-side and the card-wide call fail the same way.
func (s *Server) designPartsChatFailure(ctx context.Context, res *aiprov.ChatResult, err error, logAttrs []any) error {
	const purpose = entity.AIPurposeDesignParts
	// NEVER err.Error(): the provider may echo the request. A fixed class only.
	class := enhanceErrClass(err)
	if refusal, ok := aiUncalledRefusal(err, designPartsNotConfiguredMsg); ok {
		return refusal
	}
	if class == enhanceErrNotConfigured {
		return aiRefusal(aiReasonNotConfigured, designPartsNotConfiguredMsg, nil)
	}
	provider := s.aiProviderOf(purpose, res)
	failAttrs := append(logAttrs, slog.String("err_class", class),
		slog.Bool("provider_engaged", aiprov.Engaged(err)),
		slog.String("provider", provider), slog.String("base_url", s.ai.BaseURL(provider)))
	if class == enhanceErrProviderHTTP {
		failAttrs = append(failAttrs, slog.Int("http_status", providerHTTPStatus(err)))
	}
	slog.Default().ErrorContext(ctx, "design parts failed", failAttrs...)
	switch class {
	case enhanceErrModelUnavailable:
		return aiModelRefusal(designPartsModelUnavailMsg, s.ai.PrimaryModel(purpose))
	case enhanceErrBudgetExhausted, enhanceErrEmptyAnswer:
		return status.Error(codes.Internal, designPartsUnusableMsg)
	}
	return status.Error(codes.Unavailable, "the assistant is unavailable right now — try again in a moment")
}

// ─── parsing ───

type designPartsRaw struct {
	Parts []struct {
		Label   string            `json:"label"`
		Regions []json.RawMessage `json:"regions"`
	} `json:"parts"`
	SplitNeeded []struct {
		Region json.RawMessage `json:"region"`
		Why    string          `json:"why"`
	} `json:"split_needed"`
}

// designPartsExtract finds the {"parts":…} object: bare, fenced or wrapped in prose.
func designPartsExtract(raw string) (designPartsRaw, bool) {
	try := func(s string) (designPartsRaw, bool) {
		var probe map[string]json.RawMessage
		if json.Unmarshal([]byte(s), &probe) != nil {
			return designPartsRaw{}, false
		}
		if _, has := probe["parts"]; !has {
			return designPartsRaw{}, false
		}
		var out designPartsRaw
		if json.Unmarshal([]byte(s), &out) != nil {
			return designPartsRaw{}, false
		}
		return out, true
	}
	body := strings.TrimSpace(raw)
	if out, ok := try(body); ok {
		return out, true
	}
	if i, j := strings.Index(body, "{"), strings.LastIndex(body, "}"); i >= 0 && j > i {
		return try(body[i : j+1])
	}
	return designPartsRaw{}, false
}

// designPartsRegion reads one region number: an integer, or a string of digits. 0 = not one.
func designPartsRegion(raw json.RawMessage) int {
	var f float64
	if json.Unmarshal(raw, &f) == nil {
		if f != math.Trunc(f) || f < 1 || f > math.MaxInt32 {
			return 0
		}
		return int(f)
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		if n, err := strconv.Atoi(strings.TrimSpace(s)); err == nil && n > 0 {
			return n
		}
	}
	return 0
}

// designPartsTrim — trimmed, single-spaced, cut to max runes.
func designPartsTrim(s string, max int) string {
	s = strings.Join(strings.Fields(s), " ")
	if utf8.RuneCountInString(s) > max {
		s = strings.TrimSpace(string([]rune(s)[:max]))
	}
	return s
}

// parseDesignParts cleans the model's answer against the cut it was asked about: region numbers
// outside 1..regionCount are dropped, a region claimed twice stays with the first part, every
// region left out goes into "unnamed", labels are lowercase ≤ 40 runes, ≤ 40 parts, why ≤ 120
// runes. ok=false when no JSON of that shape is there or no part of the model's survives.
func parseDesignParts(raw string, regionCount int) ([]entity.DesignPartGroup, []entity.DesignPartSplit, bool) {
	in, ok := designPartsExtract(raw)
	if !ok {
		return nil, nil, false
	}
	owner := map[int]int{} // region → index of its part
	var parts []entity.DesignPartGroup
	for _, p := range in.Parts {
		if len(parts) == entity.DesignPartsMaxParts {
			break
		}
		label := strings.ToLower(designPartsTrim(p.Label, entity.DesignPartsMaxLabelRunes))
		if label == "" {
			label = entity.DesignPartsUnnamed
		}
		var regions []int
		for _, r := range p.Regions {
			n := designPartsRegion(r)
			if n < 1 || n > regionCount {
				continue
			}
			if _, taken := owner[n]; taken {
				continue
			}
			owner[n] = len(parts)
			regions = append(regions, n)
		}
		if len(regions) == 0 {
			continue
		}
		sort.Ints(regions)
		parts = append(parts, entity.DesignPartGroup{Label: label, Regions: regions})
	}
	if len(parts) == 0 {
		return nil, nil, false
	}

	lost := func() []int {
		var out []int
		for n := 1; n <= regionCount; n++ {
			if _, ok := owner[n]; !ok {
				out = append(out, n)
			}
		}
		return out
	}
	if missing := lost(); len(missing) > 0 {
		at := -1
		for i, p := range parts {
			if p.Label == entity.DesignPartsUnnamed {
				at = i
				break
			}
		}
		if at < 0 && len(parts) == entity.DesignPartsMaxParts {
			// No room for one more part: the last part's regions join the unnamed ones.
			for _, n := range parts[len(parts)-1].Regions {
				delete(owner, n)
			}
			parts = parts[:len(parts)-1]
			missing = lost()
		}
		if at < 0 {
			parts = append(parts, entity.DesignPartGroup{Label: entity.DesignPartsUnnamed})
			at = len(parts) - 1
		}
		parts[at].Regions = append(parts[at].Regions, missing...)
		sort.Ints(parts[at].Regions)
	}

	var splits []entity.DesignPartSplit
	seen := map[int]bool{}
	for _, sp := range in.SplitNeeded {
		n := designPartsRegion(sp.Region)
		if n < 1 || n > regionCount || seen[n] {
			continue
		}
		seen[n] = true
		splits = append(splits, entity.DesignPartSplit{Region: n, Why: designPartsTrim(sp.Why, entity.DesignPartsMaxWhyRunes)})
	}
	return parts, splits, true
}

// ─── the wire ───

func designPartsSuggestionToPb(in entity.DesignPartsSuggestion) *pb_admin.DesignPartsSuggestion {
	out := &pb_admin.DesignPartsSuggestion{
		View: in.View, BaseMediaId: int32(in.BaseMediaId), AlgoRev: in.AlgoRev, Model: in.Model,
		CreatedAt: timestamppb.New(in.CreatedAt),
	}
	for _, p := range in.Parts {
		g := &pb_admin.DesignPartGroup{Label: p.Label, PartKey: p.PartKey}
		for _, r := range p.Regions {
			g.Regions = append(g.Regions, int32(r))
		}
		out.Parts = append(out.Parts, g)
	}
	for _, sp := range in.SplitNeeded {
		out.SplitNeeded = append(out.SplitNeeded, &pb_admin.DesignPartSplit{Region: int32(sp.Region), Why: sp.Why})
	}
	return out
}

func designPartsSuggestionsToPb(in []entity.DesignPartsSuggestion) []*pb_admin.DesignPartsSuggestion {
	out := make([]*pb_admin.DesignPartsSuggestion, 0, len(in))
	for _, s := range in {
		out = append(out, designPartsSuggestionToPb(s))
	}
	return out
}
