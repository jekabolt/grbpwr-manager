package admin

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"slices"
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
//
// M5 (owner 06.10, flat-consistency 92 W5/W6): an opening is NEVER cloth — the Ф1 rule «an opening
// shows the inside of the far piece: give it to that part (seen_through)» is gone (the armhole hole
// of a side view was painted as «front body · inside»); an edge is never a part («right armhole»).
const designPartsCardSystemPrompt = `These are technical fashion flats of ONE garment, one picture per view (front, back and possibly the left and right side views). Each picture's line drawing has been cut into numbered regions (each region is tinted and carries a red number); the numbering restarts at 1 on every picture. Some regions are only fragments of one garment part: strips between pleat/fold lines, topstitching channels, fringe bits, a pocket split from its flap, etc.

List the PHYSICAL parts of the garment a designer could make from different materials (body panels, yoke, sleeves, cuffs, collar, collar stand, placket, pocket, pocket flap, waistband, hem band, straps, bodice cups, skirt, trims...). For every part give its region numbers on EVERY view where it is visible.

Rules:
- One physical part is ONE entry, even when it is visible on several views: the collar seen on the front, the back and the sides is one "collar" entry with regions on each of those views. Never repeat a part once per view.
- Left and right are the WEARER'S left and right, never the viewer's. On the FRONT view the wearer's left is picture-RIGHT; on the BACK view the wearer's left is picture-LEFT; on a side view the flank comes from the drawing (the garment's front facing picture-left = the wearer's LEFT flank, facing picture-right = the wearer's RIGHT flank). Left and right sleeves, cuffs, pockets, front panels are separate parts.
- A STRAP is named by the shoulder where it STARTS at the neck point (the wearer's side): a strap that starts at the wearer's left neck point is the "left strap" on every view, even where it crosses to the right side of the body or ends there. One strap keeps one name on all views.
- Every region number of every view must appear in exactly one part. A region that is noise goes into the part it sits on.
- Labels are lowercase, at most 3 words, standard garment part names ("left sleeve", "collar", "left front body", "back yoke", "left pocket flap"); each label is used once. When the message gives a PIECES list, every label is EXACTLY one of its names (or "opening") — no other name.
- If ONE region clearly spans two parts whose seam line is missing in the drawing, put it in the part it mostly belongs to and also list it in "split_needed" with its view.
- Use only the view keys named in the message.

Construction rules (they override any habit of naming the usual pieces):
- OPENINGS ARE NOT CLOTH. A region bounded by straps, bindings or the edge of a cut-out — an open back, a keyhole, a cut-out, the neck hole, the hole inside an armhole (on a side view too), the gap between crossed straps — is an OPENING: put every such region into ONE part labelled "opening". It is never painted, whatever you think shows through it (a flat is an empty garment): never give an opening to a garment part.
- NO CLOTH IS NEVER A BINDING. An area with no cloth of its own bounded by straps or edges (e.g. the triangle between a strap and the armhole edge on an open back) is an "opening" — never a binding or a band. Bindings and bands are THIN strips along an edge only.
- AN EDGE IS NOT A PART. Never name a part by an edge or a hole alone ("armhole", "right armhole", "neckline", "hem", "opening edge"): a thin strip along an edge is the binding or band the PIECES list names, or else the panel whose edge it finishes.
- AN ARMHOLE HAS NO PIECE OF ITS OWN. A strip or an area running along an armhole (also one between the armhole edge and a line from the shoulder down to the underarm) is the body panel it lies on — never a strap, a binding, a layer or any other piece of the list.
- EVERY PIECE OF THE LIST IS ITS OWN PART: a neck band, a binding, each strap, every inner layer. Never fold a band, binding, strap or layer into the body panel next to it. An inner layer seen through a sheer outer layer is its own part (its own label, the same on every view), not the outer body.
- NEVER INVENT A PIECE. Name a part only when the drawing gives it cloth bounded by its own seams/edges. In particular a back view of an open-back garment has NO upper back / back yoke / back bodice above the opening: the area inside the straps is an opening, not a panel. A part must be consistent across the views and with the PIECES list in the message: before answering, check every part against every other view — if the other views show no such piece and the region can be explained as an opening, it is an opening.
- THE FLANK OF A SIDE VIEW COMES FROM THE DRAWING, NOT FROM ITS NAME. Find which way the garment's front faces (neckline, bust, front edge). If the front faces the RIGHT edge of the picture, the flank you see is the wearer's RIGHT side; if it faces the LEFT edge, the wearer's LEFT side. Every left/right part on that view belongs to that flank only — never mix a left armhole with a right back panel on one side view — and it must be the same entry (same part) as the matching piece on the front and back views.
- A seam that runs down the middle of a side view is the side seam: the front of the garment is on one side of it, the back on the other.

Answer with JSON only:
{"parts":[{"label":"left sleeve","regions":{"front":[4],"back":[2],"side_l":[3]}}, ...], "split_needed":[{"view":"back","region":5,"why":"yoke and back body share it"}]}`

// designPartsOpening — the part_key and label of every region with no cloth of its own (Ф1): the
// client never paints it.
const designPartsOpening = "opening"

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
// garment note, `pieces` the PIECES block of the card's pieces list (designPartsPiecesBlock, M6 —
// read from the accepted front/back flats, never from the join list); either may be empty. No photos
// travel (B1: they make the labelling worse).
func designPartsCardUserPrompt(views []designPartsCardView, note, pieces string) string {
	var b strings.Builder
	if c := strings.TrimSpace(pieces); c != "" {
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
	// M6: the closed names come from the card's PIECES LIST, read from the FRONT/BACK plates on the
	// bench (now, if there is none or the plates moved) and edited by the designer — never from the
	// join list. The cache and the flight are keyed on the client's cut, the server's prompt revision
	// and the list's rev: a list edited after the parts were named names them again. The list read
	// here is the one the prompt reads (one read, one rev).
	pieces, err := s.designPartsPiecesFor(ctx, cardID, flats)
	if err != nil {
		return nil, err
	}
	clientRev := algoRev
	algoRev = designPartsCacheRev(clientRev, designPartsPiecesRev(pieces))
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
			return designPartsCardResponse(hits, true, pieces, flats), nil
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
		// One budget per attempt (an unusable answer is asked once more, designPartsCardAttempts).
		budget := s.ai.ChainBudget(purpose, designPartsCardMaxTokens)
		fctx, cancel := context.WithTimeout(context.WithoutCancel(ctx),
			designPartsCardAttempts*budget+designPartsFlightMargin)
		defer cancel()
		return s.designPartsCardCall(fctx, cardID, views, algoRev, ordered, force, pieces, budget)
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
	return designPartsCardResponse(ans.suggestions, ans.cached, pieces, flats), nil
}

// designPartsPromptRev — the server half of the parts cache key: BUMP IT on every change to what the
// labeller is told (the system prompt, the user prompt, the construction block, the vocabulary), so a
// card's cached parts are named again under the new words. 2 = 06.10 (unconfirmed construction is
// called «suggested construction (unconfirmed)»; the cache learns the joins rev). 3 = 07.10 (M5: an
// opening is never cloth, no seen_through; an edge is no part; no armhole binding; labels only from
// the vocabulary). 4 = 07.10 (M6: the closed names are the card's PIECES list read from the accepted
// front/back flats, no longer the join list's vocabulary; an armhole has no piece of its own; the
// cache learns the pieces list's rev instead of the join list's).
const designPartsPromptRev = 4

// designPartsServerTagSep — where the server's half of a stored algo_rev starts. The client's own
// revision never contains it (the door refuses one that does), and the wire never shows it.
const designPartsServerTagSep = "@s"

// designPartsCacheRev — the algo_rev a card-wide answer is cached under: the client's cut, the
// prompt revision, the pieces list's rev («regions.v6+parts.f6@s4.p3»). Fits the 32-character column
// for any client revision up to ~22 characters.
func designPartsCacheRev(clientRev string, piecesRev int) string {
	return clientRev + designPartsServerTag(piecesRev)
}

// designPartsServerTag — «@s4.p3» (M6: «p» = the pieces list's rev; «j» was the join list's).
func designPartsServerTag(piecesRev int) string {
	return designPartsServerTagSep + strconv.Itoa(designPartsPromptRev) + ".p" + strconv.Itoa(piecesRev)
}

// designPartsClientRev — the client's half of a stored algo_rev (the wire value).
func designPartsClientRev(stored string) string {
	if i := strings.Index(stored, designPartsServerTagSep); i >= 0 {
		return stored[:i]
	}
	return stored
}

// designPartsCurrentRows — the band's rows that answer for TODAY's prompt and pieces list: tagged with
// this server's prompt revision and the list's current rev. An untagged row (named before the tag, or
// by the per-side call) and a row named under another prompt or list are not shown, so the client
// asks again (and the server answers from the cache when it can).
func designPartsCurrentRows(rows []entity.DesignPartsSuggestion, pieces *entity.DesignPartsPieces) []entity.DesignPartsSuggestion {
	tag := designPartsServerTag(designPartsPiecesRev(pieces))
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
func (s *Server) designPartsCardCall(ctx context.Context, cardID int, views []designPartsCardView, algoRev string, marksURLs []string, force bool, pieces *entity.DesignPartsPieces, budget time.Duration) (designPartsCardFlightAnswer, error) {
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

	vocab := designPartsPiecesVocab(pieces)
	user := designPartsCardUserPrompt(views, s.designPartsCardNote(ctx, cardID), designPartsPiecesBlock(pieces))
	sides := make([]string, 0, len(views))
	for _, v := range views {
		sides = append(sides, fmt.Sprintf("%s:%d", v.View, v.Count))
	}
	var (
		parts    map[string][]entity.DesignPartGroup
		splits   map[string][]entity.DesignPartSplit
		answered string
		logAttrs []any
	)
	// An unusable answer (empty, cut at the token budget, not the promised JSON) is asked ONCE more
	// before the 500: the labeller fails like this now and then on a picture it names fine the next
	// time. Each attempt has its own chain budget; the flight's deadline covers both.
	err := designPartsRetryUnusable(ctx, budget, func(actx context.Context, attempt int) error {
		started := time.Now()
		res, err := s.ai.Chat(actx, purpose, aiprov.ChatRequest{
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
		answered = s.aiModelOf(purpose, res)
		logAttrs = []any{
			slog.Int("tech_card_id", cardID), slog.String("views", strings.Join(sides, ",")),
			slog.String("algo_rev", algoRev), slog.String("model", answered), slog.Int("attempt", attempt),
			slog.Duration("took", time.Since(started)), slog.String("finish_reason", enhanceLogFinishReason(finishReason)),
			slog.Int("prompt_tokens", usage.Prompt), slog.Int("completion_tokens", usage.Completion),
		}
		if err != nil {
			return s.designPartsChatFailure(ctx, res, err, logAttrs)
		}
		var ok bool
		parts, splits, ok = parseDesignPartsCard(raw, views, vocab...)
		if !ok {
			slog.Default().ErrorContext(ctx, "design parts card: the answer is not the promised JSON", logAttrs...)
			return status.Error(codes.Internal, designPartsUnusableMsg)
		}
		return nil
	})
	if err != nil {
		return designPartsCardFlightAnswer{}, err
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

// designPartsCardAttempts — the labeller is asked at most this many times per flight (1 retry).
const designPartsCardAttempts = 2

// designPartsUnusable — the refusal an unusable answer ends in (designPartsChatFailure's empty /
// budget-exhausted classes, and an answer that is not the promised JSON).
func designPartsUnusable(err error) bool {
	st, ok := status.FromError(err)
	return ok && st.Code() == codes.Internal && st.Message() == designPartsUnusableMsg
}

// designPartsRetryUnusable runs `try` once, and once more only when it ended unusable and the
// caller's deadline still leaves one full `budget` (never past ctx). Each attempt runs under its own
// `budget` deadline, so the total stays within designPartsCardAttempts × budget.
func designPartsRetryUnusable(ctx context.Context, budget time.Duration, try func(ctx context.Context, attempt int) error) error {
	var err error
	for attempt := 1; attempt <= designPartsCardAttempts; attempt++ {
		if attempt > 1 {
			if ctx.Err() != nil {
				return err
			}
			if dl, ok := ctx.Deadline(); ok && time.Until(dl) < budget {
				return err
			}
		}
		actx, cancel := context.WithTimeout(ctx, budget)
		err = try(actx, attempt)
		cancel()
		if err == nil || !designPartsUnusable(err) {
			return err
		}
	}
	return err
}

func designPartsCardResponse(in []entity.DesignPartsSuggestion, cached bool, pieces *entity.DesignPartsPieces, flats map[string]int) *pb_admin.SuggestDesignPartsCardResponse {
	return &pb_admin.SuggestDesignPartsCardResponse{
		Suggestions: designPartsSuggestionsToPb(in), Cached: cached, Pieces: designPartsPiecesToPb(pieces, flats),
	}
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
// Ф1: every part labelled "opening…" is ONE group per side, part_key and label "opening". M5: an
// opening is never cloth — a "seen_through" region (the Ф1 «inside of the far piece», no longer
// asked for) joins the side's opening, wherever the model had put it. An EDGE is no part: a label
// that names only an edge or a hole ("right armhole", "neckline") is no part (its regions are
// unnamed) unless it says a hole (→ opening). With a `vocab` (the construction's closed part list),
// a label is mapped onto it (designPartsVocabMatch) and a label it does not hold is no part; the
// labels mapped onto one name are one part.
func parseDesignPartsCard(raw string, views []designPartsCardView, vocab ...string) (map[string][]entity.DesignPartGroup, map[string][]entity.DesignPartSplit, bool) {
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
	keyOf := map[string]string{} // a vocabulary name → its part_key (the labels mapped onto it are one part)
	kept := 0
	for _, p := range in.Parts {
		label := strings.ToLower(designPartsTrim(p.Label, entity.DesignPartsMaxLabelRunes))
		if label == "" || label == entity.DesignPartsUnnamed {
			continue
		}
		// A name of the vocabulary itself is that part, whatever its words («opening placket»).
		exact, named := designPartsVocabExact(label, vocab)
		opening := !named && designPartsIsOpening(label)
		if !named && !opening {
			switch edge, hole := designPartsEdgeOnly(label); {
			case hole:
				opening = true
			case edge:
				continue // an edge is no part: its regions are unnamed
			}
		}
		if named {
			label = exact
		} else if opening {
			label = designPartsOpening
		} else if len(vocab) > 0 {
			name, ok := designPartsVocabMatch(label, vocab)
			if !ok {
				continue // a name the construction does not have is no part
			}
			label = name
		}
		key, merged := keyOf[label]
		if !opening && !merged && kept == entity.DesignPartsMaxParts {
			break
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
		if merged {
			for _, v := range views {
				regions := claimed[v.View]
				if len(regions) == 0 {
					continue
				}
				at := -1
				for i, g := range perView[v.View] {
					if g.PartKey == key {
						at = i
					}
				}
				if at < 0 {
					perView[v.View] = append(perView[v.View], entity.DesignPartGroup{Label: label, PartKey: key})
					at = len(perView[v.View]) - 1
				}
				g := &perView[v.View][at]
				g.Regions = append(g.Regions, regions...)
				sort.Ints(g.Regions)
			}
			continue
		}
		key = designPartsSlug(label)
		for i := 2; used[key]; i++ {
			key = designPartsSlug(label) + "-" + strconv.Itoa(i)
		}
		used[key] = true
		if len(vocab) > 0 {
			keyOf[label] = key
		}
		for _, v := range views {
			if regions := claimed[v.View]; len(regions) > 0 {
				sort.Ints(regions)
				perView[v.View] = append(perView[v.View], entity.DesignPartGroup{Label: label, Regions: regions, PartKey: key})
			}
		}
		kept++
	}
	// M5 · a region the model said shows something through it is an opening: out of any part.
	for _, st := range in.SeenThrough {
		view := designPartsCardViewKey(st.View)
		count, asked := countOf[view]
		n := designPartsRegion(st.Region)
		if !asked || n < 1 || n > count {
			continue
		}
		kept := perView[view][:0]
		for _, g := range perView[view] {
			if i := sort.SearchInts(g.Regions, n); i < len(g.Regions) && g.Regions[i] == n {
				g.Regions = append(append([]int{}, g.Regions[:i]...), g.Regions[i+1:]...)
				if len(g.Regions) == 0 {
					continue
				}
			}
			kept = append(kept, g)
		}
		perView[view] = kept
		if !slices.Contains(openings[view], n) {
			openings[view] = append(openings[view], n)
		}
		owner[view][n] = true
	}
	if kept == 0 && len(openings) == 0 {
		return nil, nil, false
	}
	for _, v := range views {
		if regions := openings[v.View]; len(regions) > 0 {
			sort.Ints(regions)
			perView[v.View] = append(perView[v.View], entity.DesignPartGroup{Label: designPartsOpening, Regions: regions, PartKey: designPartsOpening})
		}
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

// designPartsEdgeWords — words that name an edge or a place on the garment, never a cut piece;
// designPartsHoleWords — those of them that name a hole (no cloth: an opening). A region the model
// calls by a hole's edge alone («right armhole», «neckline») is that hole: an opening, never a part
// and never unnamed (an unnamed region is an error on the client).
var (
	designPartsEdgeWords = map[string]bool{
		"left": true, "right": true, "front": true, "back": true, "upper": true, "lower": true, "top": true,
		"bottom": true, "centre": true, "center": true, "side": true, "inner": true, "outer": true,
		"armhole": true, "armholes": true, "scye": true, "neckline": true, "neck": true, "hem": true,
		"edge": true, "edges": true, "seam": true, "line": true,
		"hole": true, "holes": true, "cutout": true, "cut": true, "out": true, "keyhole": true, "gap": true,
	}
	designPartsHoleWords = map[string]bool{
		"hole": true, "holes": true, "cutout": true, "keyhole": true, "gap": true, "out": true,
		"armhole": true, "armholes": true, "scye": true, "neckline": true, "neck": true,
	}
	designPartsPlaceWords = map[string]bool{
		"left": true, "right": true, "front": true, "back": true, "upper": true, "lower": true, "top": true,
		"bottom": true, "centre": true, "center": true, "side": true, "inner": true, "outer": true,
	}
)

// designPartsWords — a label as its words: lowercase letters and digits, every other run a break.
func designPartsWords(label string) []string {
	return strings.FieldsFunc(strings.ToLower(label), func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) })
}

// designPartsEdgeOnly — the label names only an edge or a place («right armhole», «neckline», «hem
// edge»): edge; and among those, a hole («armhole hole», «cut-out»): hole too. A label with any word
// of a piece («armhole binding», «neck band») is neither.
func designPartsEdgeOnly(label string) (edge, hole bool) {
	words := designPartsWords(label)
	place := true
	for _, w := range words {
		if !designPartsEdgeWords[w] {
			return false, false
		}
		if designPartsHoleWords[w] {
			hole = true
		}
		if !designPartsPlaceWords[w] {
			place = false
		}
	}
	if len(words) == 0 || place {
		return false, false // «front», «back left»: a place, not an edge — left to the vocabulary
	}
	return true, hole
}

// designPartsVocabExact — the vocabulary name the label IS: the same words in any order, or the
// same letters run together («neck band» is «neckband»).
func designPartsVocabExact(label string, vocab []string) (string, bool) {
	words := designPartsWords(label)
	if len(words) == 0 {
		return "", false
	}
	sorted := slices.Clone(words)
	slices.Sort(sorted)
	joined := strings.Join(words, "")
	for _, v := range vocab {
		vw := designPartsWords(v)
		vs := slices.Clone(vw)
		slices.Sort(vs)
		if slices.Equal(slices.Compact(vs), slices.Compact(slices.Clone(sorted))) || strings.Join(vw, "") == joined {
			return v, true
		}
	}
	return "", false
}

// designPartsVocabMatch maps a label onto the closed vocabulary: the same words in any order; else
// the ONE name whose words hold all of the label's («inner v panel» → «inner front v-panel»), or
// the ONE name all of whose words the label holds («left front body» → «front body»). A label two
// names would take («strap» of «left strap» and «right strap») matches none.
func designPartsVocabMatch(label string, vocab []string) (string, bool) {
	set := func(s string) map[string]bool {
		out := map[string]bool{}
		for _, w := range designPartsWords(s) {
			out[w] = true
		}
		return out
	}
	within := func(a, b map[string]bool) bool {
		for w := range a {
			if !b[w] {
				return false
			}
		}
		return true
	}
	l := set(label)
	if len(l) == 0 {
		return "", false
	}
	for _, v := range vocab {
		if vs := set(v); within(l, vs) && within(vs, l) {
			return v, true
		}
	}
	for _, rule := range []func(vs map[string]bool) bool{
		func(vs map[string]bool) bool { return within(l, vs) },
		func(vs map[string]bool) bool { return len(vs) > 0 && within(vs, l) },
	} {
		hit, n := "", 0
		for _, v := range vocab {
			if rule(set(v)) {
				hit, n = v, n+1
			}
		}
		if n == 1 {
			return hit, true
		}
		if n > 1 {
			return "", false
		}
	}
	return "", false
}
