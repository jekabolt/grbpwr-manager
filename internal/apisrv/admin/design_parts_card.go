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
//
// Ф1 (flat-consistency B1): the side rule "the left side view shows the left parts" is gone — the
// flank of a side view comes from the drawing; regions with no cloth of their own are OPENINGS
// (seen through to the far side's inside → that part, or the `opening` group when nothing is
// behind); no invented pieces. Tested on card 38: back-8 / no-invent 3/3 (tmp/plans/flat-consistency
// 10-RESEARCH.md B1). f3 (2026-10-06): openings are never a binding (bindings are thin strips
// only), every separately cut piece and every inner layer is its own part, a strap is named by the
// neck point it starts at, and the viewer → wearer side mapping of every view is spelled out. Any change here: bump the client's parts rev (PARTS_ALGO_REV) so cached rows
// of the old text are not applied.
const designPartsCardSystemPrompt = `These are technical fashion flats of ONE garment, one picture per view (front, back and possibly the left and right side views). Each picture's line drawing has been cut into numbered regions (each region is tinted and carries a red number); the numbering restarts at 1 on every picture. Some regions are only fragments of one garment part: strips between pleat/fold lines, topstitching channels, fringe bits, a pocket split from its flap, etc.

List the PHYSICAL parts of the garment a designer could make from different materials (body panels, yoke, sleeves, cuffs, collar, collar stand, placket, pocket, pocket flap, waistband, hem band, straps, bodice cups, skirt, trims...). For every part give its region numbers on EVERY view where it is visible.

Rules:
- One physical part is ONE entry, even when it is visible on several views: the collar seen on the front, the back and the sides is one "collar" entry with regions on each of those views. Never repeat a part once per view.
- Left and right are the WEARER'S left and right, never the viewer's. On the FRONT view the wearer's left is picture-RIGHT; on the BACK view the wearer's left is picture-LEFT; on a side view the flank comes from the drawing (the garment's front facing picture-left = the wearer's LEFT flank, facing picture-right = the wearer's RIGHT flank). Left and right sleeves, cuffs, pockets, front panels are separate parts.
- A STRAP is named by the shoulder where it STARTS at the neck point (the wearer's side): a strap that starts at the wearer's left neck point is the "left strap" on every view, even where it crosses to the right side of the body or ends there. One strap keeps one name on all views.
- Every region number of every view must appear in exactly one part. A region that is noise goes into the part it sits on.
- Labels are lowercase, at most 3 words, standard garment part names ("left sleeve", "collar", "left front body", "back yoke", "left pocket flap"); each label is used once.
- If ONE region clearly spans two parts whose seam line is missing in the drawing, put it in the part it mostly belongs to and also list it in "split_needed" with its view.
- Use only the view keys named in the message.

Construction rules (they override any habit of naming the usual pieces):
- OPENINGS ARE NOT CLOTH. A region bounded by straps, bindings or the edge of a cut-out — an open back, a keyhole, a cut-out, an armhole seen from the side, the gap between crossed straps — is an OPENING. A flat has no body inside it, so through an opening you see the INSIDE (reverse side) of the cloth on the far side of the garment: through an open back, the inside of the front panels; through an armhole seen from the side, the inside of the opposite side. Put such a region in the part whose inside it shows (so painting that part paints it too) AND list it in "seen_through" with that part's label. An opening with no cloth behind it (empty space, background seen through), or one where you cannot tell what is behind it, goes into ONE part labelled "opening" — never a garment part.
- NO CLOTH IS NEVER A BINDING. An area with no cloth of its own bounded by straps or edges (e.g. the triangle between a strap and the armhole edge on an open back) is an "opening", or the inside of the piece seen through it (seen_through) — never a binding or a band. Bindings and bands are THIN strips along an edge only.
- EVERY SEPARATELY CUT PIECE IS ITS OWN PART: a neck band, each binding (neck, armhole, the edge of an open back), each strap, and every inner layer the construction lists. Never fold a band, binding, strap or layer into the body panel next to it. An inner layer seen through a sheer outer layer is its own part (its own label, the same on every view), not the outer body.
- NEVER INVENT A PIECE. Name a part only when the drawing gives it cloth bounded by its own seams/edges. In particular a back view of an open-back garment has NO upper back / back yoke / back bodice above the opening: the area inside the straps is an opening, not a panel. A part must be consistent across the views and with the construction notes in the message: before answering, check every part against every other view — if the other views show no such piece and the region can be explained as an opening, it is an opening.
- THE FLANK OF A SIDE VIEW COMES FROM THE DRAWING, NOT FROM ITS NAME. Find which way the garment's front faces (neckline, bust, front edge). If the front faces the RIGHT edge of the picture, the flank you see is the wearer's RIGHT side; if it faces the LEFT edge, the wearer's LEFT side. Every left/right part on that view belongs to that flank only — never mix a left armhole with a right back panel on one side view — and it must be the same entry (same part) as the matching piece on the front and back views.
- A seam that runs down the middle of a side view is the side seam: the front of the garment is on one side of it, the back on the other.

Answer with JSON only:
{"parts":[{"label":"left sleeve","regions":{"front":[4],"back":[2],"side_l":[3]}}, ...], "seen_through":[{"view":"back","region":8,"label":"front body"}], "split_needed":[{"view":"back","region":5,"why":"yoke and back body share it"}]}`

// designPartsOpening — the part_key and label of every region with no cloth of its own (Ф1): the
// client never paints it.
const designPartsOpening = "opening"

// designPartsInsideSuffix — the label of a region that shows the INSIDE of a part through an
// opening: the same part_key, the part's label + this.
const designPartsInsideSuffix = " · inside"

// designPartsCardMaxNoteRunes — the garment note travels at most this long (the card's own ceiling
// is designMaxGarmentNoteRunes; the labeller needs the construction words, not an essay).
const designPartsCardMaxNoteRunes = 1500

// designPartsCardView — one side of the call, as the doors cleaned it.
type designPartsCardView struct {
	View  string
	Base  int
	Marks int
	Count int
}

// designPartsCardUserPrompt numbers the pictures in the order they travel. `note` is the card's
// garment note, `construction` the confirmed construction (designPartsCardConstruction); either may
// be empty. No photos travel (B1: they make the labelling worse).
func designPartsCardUserPrompt(views []designPartsCardView, note, construction string, confirmed bool) string {
	var b strings.Builder
	if c := strings.TrimSpace(construction); c != "" {
		// Codex b3: «confirmed by the designer» only when a designer did confirm this list.
		if confirmed {
			b.WriteString("CONSTRUCTION of this garment (confirmed by the designer; trust it over habit — never name a part it does not have):\n")
		} else {
			b.WriteString("CONSTRUCTION of this garment (suggested construction (unconfirmed): read from the photos by a model, not yet checked by the designer; prefer it over habit, but where the drawing plainly disagrees, the drawing wins):\n")
		}
		b.WriteString(c)
		b.WriteString("\n\n")
	}
	if n := designPartsTrim(note, designPartsCardMaxNoteRunes); n != "" {
		b.WriteString("The designer's note on the garment (construction only):\n")
		b.WriteString(n)
		b.WriteString("\n\n")
	}
	fmt.Fprintf(&b, "The garment has %d views; the pictures follow in this order:\n", len(views))
	for i, v := range views {
		fmt.Fprintf(&b, "image %d: %s flat (key %q), regions 1..%d\n", i+1, designPartsViewWords[v.View], v.View, v.Count)
	}
	b.WriteString("List the garment's physical parts, each with its region numbers on every view where it is visible.")
	return b.String()
}

// designPartsCardNote — the card's garment note (empty when the card has none or cannot be read: the
// note helps, it never blocks the call).
func (s *Server) designPartsCardNote(ctx context.Context, cardID int) string {
	card, err := s.repo.TechCards().GetTechCardById(ctx, cardID)
	if err != nil || card == nil {
		if err != nil {
			slog.Default().WarnContext(ctx, "design parts card: the garment note is not read",
				slog.Int("tech_card_id", cardID), slog.String("err", err.Error()))
		}
		return ""
	}
	return card.GarmentDescription.String
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
	case strings.Contains(algoRev, designPartsServerTagSep):
		return nil, status.Errorf(codes.InvalidArgument, "algo_rev must not contain %q", designPartsServerTagSep)
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
	// Codex b4: the cache and the flight are keyed on the client's cut, the server's prompt revision
	// and the join list's rev — a list created, corrected or confirmed after the parts were named
	// names them again. The list read here is the one the prompt reads (one read, one rev).
	joins, err := s.repo.Design().GetJoins(ctx, cardID)
	if err != nil {
		return nil, designError(ctx, "failed to read the join list", err, nil)
	}
	clientRev := algoRev
	algoRev = designPartsCacheRev(clientRev, designPartsJoinsRev(joins))
	if len(algoRev) > entity.DesignPartsMaxAlgoRev {
		return nil, status.Errorf(codes.InvalidArgument, "algo_rev must be 1..%d characters", entity.DesignPartsMaxAlgoRev-(len(algoRev)-len(clientRev)))
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
		return s.designPartsCardCall(fctx, cardID, views, algoRev, ordered, force, joins)
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

// designPartsPromptRev — the server half of the parts cache key: BUMP IT on every change to what the
// labeller is told (the system prompt, the user prompt, the construction block, the vocabulary), so a
// card's cached parts are named again under the new words. 2 = 06.10 (unconfirmed construction is
// called «suggested construction (unconfirmed)»; the cache learns the joins rev).
const designPartsPromptRev = 2

// designPartsServerTagSep — where the server's half of a stored algo_rev starts. The client's own
// revision never contains it (the door refuses one that does), and the wire never shows it.
const designPartsServerTagSep = "@s"

// designPartsCacheRev — the algo_rev a card-wide answer is cached under: the client's cut, the
// prompt revision, the join list's rev («regions.v4+parts.f3@s2.j7»). Fits the 32-character column
// for any client revision up to ~22 characters.
func designPartsCacheRev(clientRev string, joinsRev int) string {
	return clientRev + designPartsServerTag(joinsRev)
}

// designPartsServerTag — «@s2.j7».
func designPartsServerTag(joinsRev int) string {
	return designPartsServerTagSep + strconv.Itoa(designPartsPromptRev) + ".j" + strconv.Itoa(joinsRev)
}

// designPartsClientRev — the client's half of a stored algo_rev (the wire value).
func designPartsClientRev(stored string) string {
	if i := strings.Index(stored, designPartsServerTagSep); i >= 0 {
		return stored[:i]
	}
	return stored
}

// designPartsJoinsRev — the rev of the card's join list; 0 when there is none.
func designPartsJoinsRev(j *entity.DesignJoins) int {
	if j == nil {
		return 0
	}
	return j.Rev
}

// designPartsCurrentRows — the band's rows that answer for TODAY's prompt and join list: tagged with
// this server's prompt revision and the list's current rev. An untagged row (named before the tag, or
// by the per-side call) and a row named under another prompt or list are not shown, so the client
// asks again (and the server answers from the cache when it can).
func designPartsCurrentRows(rows []entity.DesignPartsSuggestion, joins *entity.DesignJoins) []entity.DesignPartsSuggestion {
	tag := designPartsServerTag(designPartsJoinsRev(joins))
	out := make([]entity.DesignPartsSuggestion, 0, len(rows))
	for _, r := range rows {
		if strings.HasSuffix(r.AlgoRev, tag) && strings.Index(r.AlgoRev, designPartsServerTagSep) == len(r.AlgoRev)-len(tag) {
			out = append(out, r)
		}
	}
	return out
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
func (s *Server) designPartsCardCall(ctx context.Context, cardID int, views []designPartsCardView, algoRev string, marksURLs []string, force bool, joins *entity.DesignJoins) (designPartsCardFlightAnswer, error) {
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

	construction, confirmed := designPartsCardConstructionOf(joins)
	user := designPartsCardUserPrompt(views, s.designPartsCardNote(ctx, cardID), construction, confirmed)
	started := time.Now()
	res, err := s.ai.Chat(ctx, purpose, aiprov.ChatRequest{
		System: designPartsCardSystemPrompt, User: user, ImageURLs: marksURLs,
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
	SeenThrough []struct {
		View   string          `json:"view"`
		Region json.RawMessage `json:"region"`
		Label  string          `json:"label"`
	} `json:"seen_through"`
}

// designPartsIsOpening — a label that names no cloth: "opening", "openings", "opening (back)".
func designPartsIsOpening(label string) bool {
	return label == designPartsOpening || strings.HasPrefix(label, designPartsOpening+"s") ||
		strings.HasPrefix(label, designPartsOpening+" ")
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
//
// Ф1: every part labelled "opening…" is ONE group per side, part_key and label "opening". A
// "seen_through" region (an opening showing the INSIDE of a part) leaves wherever it was and joins
// that part as its own group: the part's part_key, label "<part> · inside". A seen_through naming
// no kept part (or "opening") changes nothing.
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
	openings := map[string][]int{}
	keyOf := map[string]string{} // kept label → its part_key (seen_through names parts by label)
	kept := 0
	for _, p := range in.Parts {
		if kept == entity.DesignPartsMaxParts {
			break
		}
		label := strings.ToLower(designPartsTrim(p.Label, entity.DesignPartsMaxLabelRunes))
		if label == "" || label == entity.DesignPartsUnnamed {
			continue
		}
		opening := designPartsIsOpening(label)
		if opening {
			label = designPartsOpening
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
		if opening {
			// Every opening is one group per side, kept after the parts (they are not a part).
			if !used[designPartsOpening] {
				used[designPartsOpening] = true
				kept++
			}
			for v, regions := range claimed {
				openings[v] = append(openings[v], regions...)
			}
			continue
		}
		key := designPartsSlug(label)
		for i := 2; used[key]; i++ {
			key = designPartsSlug(label) + "-" + strconv.Itoa(i)
		}
		used[key] = true
		keyOf[label] = key
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
		if regions := openings[v.View]; len(regions) > 0 {
			sort.Ints(regions)
			perView[v.View] = append(perView[v.View], entity.DesignPartGroup{Label: designPartsOpening, Regions: regions, PartKey: designPartsOpening})
		}
	}
	designPartsCardSeenThrough(perView, owner, countOf, keyOf, in)

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

// designPartsCardSeenThrough moves every "seen_through" region into an inside group of the part it
// shows (same part_key, label "<part> · inside"), out of whatever group held it; a group left empty
// goes. Regions the model left out are taken too (owner marks them). The ≤ 40 groups per side cap
// holds: an inside group that would be the 41st is not made.
func designPartsCardSeenThrough(perView map[string][]entity.DesignPartGroup, owner map[string]map[int]bool, countOf map[string]int, keyOf map[string]string, in designPartsCardRaw) {
	for _, st := range in.SeenThrough {
		view := designPartsCardViewKey(st.View)
		count, asked := countOf[view]
		n := designPartsRegion(st.Region)
		label := strings.ToLower(designPartsTrim(st.Label, entity.DesignPartsMaxLabelRunes))
		label = strings.TrimSpace(strings.TrimSuffix(label, strings.TrimSpace(designPartsInsideSuffix)))
		key, named := keyOf[label]
		if !asked || n < 1 || n > count || !named {
			continue
		}
		groups := perView[view]
		inside := label + designPartsInsideSuffix
		at := -1
		for i, g := range groups {
			if g.PartKey == key && g.Label == inside {
				at = i
			}
		}
		if at < 0 && len(groups) >= entity.DesignPartsMaxParts {
			continue
		}
		// Out of the group that holds it (if any).
		kept := groups[:0]
		for _, g := range groups {
			if i := sort.SearchInts(g.Regions, n); i < len(g.Regions) && g.Regions[i] == n {
				if g.Label == inside && g.PartKey == key {
					kept = append(kept, g)
					continue
				}
				g.Regions = append(append([]int{}, g.Regions[:i]...), g.Regions[i+1:]...)
				if len(g.Regions) == 0 {
					continue
				}
			}
			kept = append(kept, g)
		}
		groups = kept
		at = -1
		for i, g := range groups {
			if g.PartKey == key && g.Label == inside {
				at = i
			}
		}
		if at < 0 {
			groups = append(groups, entity.DesignPartGroup{Label: inside, PartKey: key})
			at = len(groups) - 1
		}
		if i := sort.SearchInts(groups[at].Regions, n); i == len(groups[at].Regions) || groups[at].Regions[i] != n {
			groups[at].Regions = append(groups[at].Regions, n)
			sort.Ints(groups[at].Regions)
		}
		owner[view][n] = true
		perView[view] = groups
	}
}
