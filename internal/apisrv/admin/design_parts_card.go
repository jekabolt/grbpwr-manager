package admin

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/jekabolt/grbpwr-manager/internal/aiprov"
	authsrv "github.com/jekabolt/grbpwr-manager/internal/apisrv/auth"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	pb_admin "github.com/jekabolt/grbpwr-manager/proto/gen/admin"
	"golang.org/x/sync/singleflight"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// ─────────────── AUTO PARTS · TOPOLOGY (2026-10-04, Ф2.1) ───────────────
//
// The per-side call names each side on its own, so the collar of the front and the collar of the
// back are two parts that only share a word. Here ONE call sees every side's marks picture at once
// and lists the garment's PHYSICAL parts, each with its regions on every side it is visible on; the
// cleaned answer is cut back into the per-side cache rows of 0390 (one row per side, same key), and
// every group carries a part_key shared across the sides — the client paints one part on all sides
// with one gesture by that key.
//
// The doors are the per-side call's, for EVERY side: the arguments → the card and each side's flat
// → the cache (a hit only when every side has a card-shaped row) → the band flag → the purpose is
// callable → the marks pictures → one flight per (card, sides+flats, cut) → the shared fences.

const designPartsCardMaxTokens = 6000

// designPartsCardSystemPrompt — fixed text; nothing of the request reaches the system role.
const designPartsCardSystemPrompt = `These are technical fashion flats of ONE garment, one picture per view (front, back and possibly the left and right side views). Each picture's line drawing has been cut into numbered regions (each region is tinted and carries a red number); the numbering restarts at 1 on every picture. Some regions are only fragments of one garment part: strips between pleat/fold lines, topstitching channels, fringe bits, a pocket split from its flap, etc.

List the PHYSICAL parts of the garment a designer could make from different materials (body panels, yoke, sleeves, cuffs, collar, collar stand, placket, pocket, pocket flap, waistband, hem band, straps, bodice cups, skirt, trims...). For every part give its region numbers on EVERY view where it is visible.

Rules:
- One physical part is ONE entry, even when it is visible on several views: the collar seen on the front, the back and the sides is one "collar" entry with regions on each of those views. Never repeat a part once per view.
- Left and right are the WEARER'S left and right, never the viewer's: on a front view the wearer's left sleeve is on the right side of the picture, on a back view it is on the left side. Left and right sleeves, cuffs, pockets, front panels are separate parts.
- A side view shows the parts of THAT side: the left side view shows the wearer's left sleeve and the left edges of the front and back; the right side view shows the right ones.
- Every region number of every view must appear in exactly one part. A region that is noise goes into the part it sits on.
- Labels are lowercase, at most 3 words, standard garment part names ("left sleeve", "collar", "left front body", "back yoke", "left pocket flap"); each label is used once.
- If ONE region clearly spans two parts whose seam line is missing in the drawing, put it in the part it mostly belongs to and also list it in "split_needed" with its view.
- Use only the view keys named in the message.

Answer with JSON only:
{"parts":[{"label":"collar","regions":{"front":[1,2],"back":[1],"side_l":[1],"side_r":[1]}}, ...], "split_needed":[{"view":"back","region":5,"why":"yoke and back body share it"}]}`

// designPartsCardView — one side of the call, as the doors cleaned it.
type designPartsCardView struct {
	View  string
	Base  int
	Marks int
	Count int
}

// designPartsCardUserPrompt numbers the pictures in the order they travel.
func designPartsCardUserPrompt(views []designPartsCardView) string {
	var b strings.Builder
	fmt.Fprintf(&b, "The garment has %d views; the pictures follow in this order:\n", len(views))
	for i, v := range views {
		fmt.Fprintf(&b, "image %d: %s flat (key %q), regions 1..%d\n", i+1, designPartsViewWords[v.View], v.View, v.Count)
	}
	b.WriteString("List the garment's physical parts, each with its region numbers on every view where it is visible.")
	return b.String()
}

// designPartsCardFlightAnswer — what one flight hands every press that waited on it.
type designPartsCardFlightAnswer struct {
	suggestions []entity.DesignPartsSuggestion
	cached      bool
}

// SuggestDesignPartsCard names the parts of every requested side in one call (cached per side).
func (s *Server) SuggestDesignPartsCard(ctx context.Context, req *pb_admin.SuggestDesignPartsCardRequest) (*pb_admin.SuggestDesignPartsCardResponse, error) {
	cardID := int(req.GetTechCardId())
	algoRev := strings.TrimSpace(req.GetAlgoRev())
	switch {
	case cardID <= 0:
		return nil, status.Error(codes.InvalidArgument, "tech_card_id is required")
	case algoRev == "" || len(algoRev) > entity.DesignPartsMaxAlgoRev:
		return nil, status.Errorf(codes.InvalidArgument, "algo_rev must be 1..%d characters", entity.DesignPartsMaxAlgoRev)
	case len(req.GetViews()) == 0 || len(req.GetViews()) > entity.DesignPartsMaxViews:
		return nil, status.Errorf(codes.InvalidArgument, "views must name 1..%d sides", entity.DesignPartsMaxViews)
	}
	views := make([]designPartsCardView, 0, len(req.GetViews()))
	seen := map[string]bool{}
	for _, in := range req.GetViews() {
		v := designPartsCardView{View: in.GetView(), Base: int(in.GetBaseMediaId()), Marks: int(in.GetMarksMediaId()), Count: int(in.GetRegionCount())}
		switch {
		case !entity.IsDesignCardinalView(v.View):
			return nil, status.Errorf(codes.InvalidArgument, "view %q is not a side of the bench", v.View)
		case seen[v.View]:
			return nil, status.Errorf(codes.InvalidArgument, "view %q is named twice", v.View)
		case v.Base <= 0:
			return nil, status.Errorf(codes.InvalidArgument, "base_media_id is required (%s)", v.View)
		case v.Marks <= 0:
			return nil, status.Errorf(codes.InvalidArgument, "marks_media_id is required (%s)", v.View)
		case v.Count < entity.DesignPartsMinRegions || v.Count > entity.DesignPartsMaxRegions:
			return nil, status.Error(codes.FailedPrecondition, designPartsTooManyMsg)
		}
		seen[v.View] = true
		views = append(views, v)
	}

	flats, err := s.repo.Design().FlatBenchMedia(ctx, cardID)
	if err != nil {
		return nil, designError(ctx, "failed to read the flat bench", err, map[string]string{"tech_card_id": strconv.Itoa(cardID)})
	}
	for _, v := range views {
		if flats[v.View] != v.Base {
			return nil, status.Error(codes.FailedPrecondition, designPartsFlatChangedMsg)
		}
	}
	force := req.GetForce()
	if !force {
		hits, err := s.designPartsCardCached(ctx, cardID, views, algoRev)
		if err != nil {
			return nil, err
		}
		if hits != nil {
			return designPartsCardResponse(hits, true), nil
		}
	}

	if err := s.designGenerationGate(); err != nil {
		return nil, err
	}
	const purpose = entity.AIPurposeDesignParts
	if !s.ai.Enabled(purpose) {
		return nil, s.aiOffRefusal(purpose, designPartsNotConfiguredMsg)
	}

	// The marks pictures: ours, files, pictures the provider can read — in the order of the sides.
	marks := make([]int, 0, len(views))
	for _, v := range views {
		marks = append(marks, v.Marks)
	}
	if err := s.repo.Design().AssertMediaNotForeign(ctx, cardID, marks); err != nil {
		return nil, designError(ctx, "the marks picture is refused", err, nil)
	}
	urls, attached, err := s.designBoardPictureURLs(ctx, marks)
	if err != nil {
		slog.Default().ErrorContext(ctx, "design parts card: cannot resolve the marks pictures",
			slog.Int("tech_card_id", cardID), slog.String("err", err.Error()))
		return nil, status.Error(codes.Internal, "cannot read the marks picture")
	}
	urlOf := make(map[int]string, len(attached))
	for i, id := range attached {
		urlOf[id] = urls[i]
	}
	ordered := make([]string, 0, len(views))
	refs := make([]designInputMediaRef, 0, len(views))
	for _, v := range views {
		u, ok := urlOf[v.Marks]
		if !ok {
			return nil, status.Errorf(codes.InvalidArgument, "marks_media_id %d has no file", v.Marks)
		}
		ordered = append(ordered, u)
		refs = append(refs, designInputMediaRef{ID: v.Marks, URL: u, Where: "the marks of the " + designPartsViewWords[v.View]})
	}
	if ref, ct, bad := designFirstNonPictureInput(refs); bad {
		return nil, designNonPictureRefusal(ref, ct)
	}

	// ⚠ ONE FLIGHT PER (card, sides+flats, cut): a double press pays once. Detached from the leader's
	// cancellation under its own budget, like the per-side call.
	ch := s.partsCardFlight.DoChan(designPartsCardFlightKey(cardID, views, algoRev), func() (any, error) {
		fctx, cancel := context.WithTimeout(context.WithoutCancel(ctx),
			s.ai.ChainBudget(purpose, designPartsCardMaxTokens)+designPartsFlightMargin)
		defer cancel()
		return s.designPartsCardCall(fctx, cardID, views, algoRev, ordered, force)
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
	ans := res.Val.(designPartsCardFlightAnswer)
	return designPartsCardResponse(ans.suggestions, ans.cached), nil
}

// designPartsCardFlightKey — card, the sides with their flats sorted, the cut.
func designPartsCardFlightKey(cardID int, views []designPartsCardView, algoRev string) string {
	sides := make([]string, 0, len(views))
	for _, v := range views {
		sides = append(sides, fmt.Sprintf("%s:%d", v.View, v.Base))
	}
	sort.Strings(sides)
	return fmt.Sprintf("%d|%s|%s", cardID, strings.Join(sides, ","), algoRev)
}

// designPartsCardCached returns the rows of every side when ALL of them are cached as card-wide
// answers (every group carries a part_key — a per-side row never does), nil otherwise.
func (s *Server) designPartsCardCached(ctx context.Context, cardID int, views []designPartsCardView, algoRev string) ([]entity.DesignPartsSuggestion, error) {
	out := make([]entity.DesignPartsSuggestion, 0, len(views))
	for _, v := range views {
		hit, err := s.repo.Design().GetPartsSuggestion(ctx, cardID, v.View, v.Base, algoRev)
		if err != nil {
			return nil, designError(ctx, "failed to read the parts suggestion", err, nil)
		}
		if hit == nil || !designPartsCardShaped(*hit) {
			return nil, nil
		}
		out = append(out, *hit)
	}
	return out, nil
}

func designPartsCardShaped(in entity.DesignPartsSuggestion) bool {
	if len(in.Parts) == 0 {
		return false
	}
	for _, p := range in.Parts {
		if p.PartKey == "" {
			return false
		}
	}
	return true
}

// designPartsCardCall — the fences and the ONE provider call (the flight leader's work).
func (s *Server) designPartsCardCall(ctx context.Context, cardID int, views []designPartsCardView, algoRev string, marksURLs []string, force bool) (designPartsCardFlightAnswer, error) {
	const purpose = entity.AIPurposeDesignParts
	// A flight that finished just before this one already paid: read the cache again.
	if !force {
		hits, err := s.designPartsCardCached(ctx, cardID, views, algoRev)
		if err != nil {
			return designPartsCardFlightAnswer{}, err
		}
		if hits != nil {
			return designPartsCardFlightAnswer{suggestions: hits, cached: true}, nil
		}
	}

	// The fences of EnhanceText, THE SAME ONES: one semaphore, one hourly window.
	select {
	case s.enhanceSem <- struct{}{}:
		defer func() { <-s.enhanceSem }()
	default:
		return designPartsCardFlightAnswer{}, status.Error(codes.ResourceExhausted, "the assistant is busy right now — try again in a moment")
	}
	if !s.enhanceRuns.allow(authsrv.GetAdminUsername(ctx)) {
		return designPartsCardFlightAnswer{}, status.Errorf(codes.ResourceExhausted,
			"this account has used the assistant %d times in the last hour (ideas, text improvements, the quiz and the parts share the limit); every call spends the AI key — try again later",
			enhancePerAdminCalls)
	}

	started := time.Now()
	res, err := s.ai.Chat(ctx, purpose, aiprov.ChatRequest{
		System: designPartsCardSystemPrompt, User: designPartsCardUserPrompt(views), ImageURLs: marksURLs,
		UserAsParts: true, JSONMode: true, MaxTokens: designPartsCardMaxTokens, Effort: designPartsEffort,
	})
	var (
		raw, finishReason string
		usage             aiprov.TokenUsage
	)
	if res != nil {
		raw, finishReason, usage = res.Text, res.FinishReason, res.Usage
	}
	answered := s.aiModelOf(purpose, res)
	sides := make([]string, 0, len(views))
	for _, v := range views {
		sides = append(sides, fmt.Sprintf("%s:%d", v.View, v.Count))
	}
	logAttrs := []any{
		slog.Int("tech_card_id", cardID), slog.String("views", strings.Join(sides, ",")),
		slog.String("algo_rev", algoRev), slog.String("model", answered),
		slog.Duration("took", time.Since(started)), slog.String("finish_reason", enhanceLogFinishReason(finishReason)),
		slog.Int("prompt_tokens", usage.Prompt), slog.Int("completion_tokens", usage.Completion),
	}
	if err != nil {
		return designPartsCardFlightAnswer{}, s.designPartsChatFailure(ctx, res, err, logAttrs)
	}

	parts, splits, ok := parseDesignPartsCard(raw, views)
	if !ok {
		slog.Default().ErrorContext(ctx, "design parts card: the answer is not the promised JSON", logAttrs...)
		return designPartsCardFlightAnswer{}, status.Error(codes.Internal, designPartsUnusableMsg)
	}
	out := make([]entity.DesignPartsSuggestion, 0, len(views))
	keys := map[string]bool{}
	for _, v := range views {
		saved, err := s.repo.Design().SavePartsSuggestion(ctx, entity.DesignPartsSuggestion{
			TechCardId: cardID, View: v.View, BaseMediaId: v.Base, AlgoRev: algoRev,
			Parts: parts[v.View], SplitNeeded: splits[v.View], Model: answered, CreatedBy: designActor(ctx),
		})
		if err != nil {
			return designPartsCardFlightAnswer{}, designError(ctx, "failed to save the parts suggestion", err, nil)
		}
		for _, p := range saved.Parts {
			keys[p.PartKey] = true
		}
		out = append(out, *saved)
	}
	slog.Default().InfoContext(ctx, "design parts card", append(logAttrs, slog.Int("part_keys", len(keys)))...)
	return designPartsCardFlightAnswer{suggestions: out}, nil
}

func designPartsCardResponse(in []entity.DesignPartsSuggestion, cached bool) *pb_admin.SuggestDesignPartsCardResponse {
	return &pb_admin.SuggestDesignPartsCardResponse{Suggestions: designPartsSuggestionsToPb(in), Cached: cached}
}

// ─── parsing ───

type designPartsCardRaw struct {
	Parts []struct {
		Label   string          `json:"label"`
		Regions json.RawMessage `json:"regions"`
	} `json:"parts"`
	SplitNeeded []struct {
		View   string          `json:"view"`
		Region json.RawMessage `json:"region"`
		Why    string          `json:"why"`
	} `json:"split_needed"`
}

// designPartsCardExtract finds the {"parts":…} object: bare, fenced or wrapped in prose.
func designPartsCardExtract(raw string) (designPartsCardRaw, bool) {
	try := func(s string) (designPartsCardRaw, bool) {
		var probe map[string]json.RawMessage
		if json.Unmarshal([]byte(s), &probe) != nil {
			return designPartsCardRaw{}, false
		}
		if _, has := probe["parts"]; !has {
			return designPartsCardRaw{}, false
		}
		var out designPartsCardRaw
		if json.Unmarshal([]byte(s), &out) != nil {
			return designPartsCardRaw{}, false
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
	return designPartsCardRaw{}, false
}

// designPartsCardViewKey — the model's spelling of a side, folded to ours ("side-l" → "side_l").
func designPartsCardViewKey(s string) string {
	return strings.NewReplacer("-", "_", " ", "_").Replace(strings.ToLower(strings.TrimSpace(s)))
}

// designPartsCardRegions reads one part's {view: [n…]} object; a bare number stands for [n].
// Not an object → nothing.
func designPartsCardRegions(raw json.RawMessage) map[string][]json.RawMessage {
	var byView map[string]json.RawMessage
	if json.Unmarshal(raw, &byView) != nil {
		return nil
	}
	out := make(map[string][]json.RawMessage, len(byView))
	for k, v := range byView {
		var list []json.RawMessage
		if json.Unmarshal(v, &list) != nil {
			list = []json.RawMessage{v}
		}
		out[designPartsCardViewKey(k)] = append(out[designPartsCardViewKey(k)], list...)
	}
	return out
}

// designPartsSlug — the label as a key: letters and digits, runs of anything else as one "-".
func designPartsSlug(label string) string {
	var b strings.Builder
	dash := false
	for _, r := range strings.ToLower(label) {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			if dash && b.Len() > 0 {
				b.WriteByte('-')
			}
			dash = false
			b.WriteRune(r)
			continue
		}
		dash = true
	}
	if b.Len() == 0 {
		return "part"
	}
	return b.String()
}

// designPartsUnnamedKey — the key of what the model left out on one side.
func designPartsUnnamedKey(view string) string {
	return entity.DesignPartsUnnamed + "-" + view
}

// parseDesignPartsCard cleans the card-wide answer against the sides it was asked about and cuts it
// into per-side groups. Per side, region numbers outside 1..count are dropped and a region claimed
// twice stays with the first part; sides not asked about are ignored. A part keeps one part_key on
// every side it has regions on (slug of the label, -2, -3… when taken). A part with no label (or
// labelled "unnamed") is not a part: its regions fall with the ones the model left out into the
// side's "unnamed" group (part_key "unnamed-<view>"). Labels are lowercase ≤ 40 runes, ≤ 40 parts
// per answer and per side. ok=false when no JSON of that shape is there or no part survives.
func parseDesignPartsCard(raw string, views []designPartsCardView) (map[string][]entity.DesignPartGroup, map[string][]entity.DesignPartSplit, bool) {
	in, ok := designPartsCardExtract(raw)
	if !ok {
		return nil, nil, false
	}
	countOf := make(map[string]int, len(views))
	used := map[string]bool{}
	owner := make(map[string]map[int]bool, len(views))
	for _, v := range views {
		countOf[v.View] = v.Count
		owner[v.View] = map[int]bool{}
		used[designPartsUnnamedKey(v.View)] = true
	}
	perView := make(map[string][]entity.DesignPartGroup, len(views))
	kept := 0
	for _, p := range in.Parts {
		if kept == entity.DesignPartsMaxParts {
			break
		}
		label := strings.ToLower(designPartsTrim(p.Label, entity.DesignPartsMaxLabelRunes))
		if label == "" || label == entity.DesignPartsUnnamed {
			continue
		}
		byView := designPartsCardRegions(p.Regions)
		claimed := map[string][]int{}
		for _, v := range views {
			for _, r := range byView[v.View] {
				n := designPartsRegion(r)
				if n < 1 || n > v.Count || owner[v.View][n] {
					continue
				}
				owner[v.View][n] = true
				claimed[v.View] = append(claimed[v.View], n)
			}
		}
		if len(claimed) == 0 {
			continue
		}
		key := designPartsSlug(label)
		for i := 2; used[key]; i++ {
			key = designPartsSlug(label) + "-" + strconv.Itoa(i)
		}
		used[key] = true
		for _, v := range views {
			if regions := claimed[v.View]; len(regions) > 0 {
				sort.Ints(regions)
				perView[v.View] = append(perView[v.View], entity.DesignPartGroup{Label: label, Regions: regions, PartKey: key})
			}
		}
		kept++
	}
	if kept == 0 {
		return nil, nil, false
	}

	for _, v := range views {
		parts := perView[v.View]
		lost := func() []int {
			var out []int
			for n := 1; n <= v.Count; n++ {
				if !owner[v.View][n] {
					out = append(out, n)
				}
			}
			return out
		}
		missing := lost()
		if len(missing) > 0 && len(parts) == entity.DesignPartsMaxParts {
			// No room for one more group on this side: the last one's regions join the unnamed ones.
			for _, n := range parts[len(parts)-1].Regions {
				owner[v.View][n] = false
			}
			parts = parts[:len(parts)-1]
			missing = lost()
		}
		if len(missing) > 0 {
			parts = append(parts, entity.DesignPartGroup{Label: entity.DesignPartsUnnamed, Regions: missing, PartKey: designPartsUnnamedKey(v.View)})
		}
		perView[v.View] = parts
	}

	splits := map[string][]entity.DesignPartSplit{}
	seen := map[string]bool{}
	for _, sp := range in.SplitNeeded {
		view := designPartsCardViewKey(sp.View)
		count, asked := countOf[view]
		n := designPartsRegion(sp.Region)
		at := view + "|" + strconv.Itoa(n)
		if !asked || n < 1 || n > count || seen[at] {
			continue
		}
		seen[at] = true
		splits[view] = append(splits[view], entity.DesignPartSplit{Region: n, Why: designPartsTrim(sp.Why, entity.DesignPartsMaxWhyRunes)})
	}
	return perView, splits, true
}
