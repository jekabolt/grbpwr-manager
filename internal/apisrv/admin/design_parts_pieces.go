package admin

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jekabolt/grbpwr-manager/internal/aiprov"
	authsrv "github.com/jekabolt/grbpwr-manager/internal/apisrv/auth"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	pb_admin "github.com/jekabolt/grbpwr-manager/proto/gen/admin"
	pb_common "github.com/jekabolt/grbpwr-manager/proto/gen/common"
	"golang.org/x/sync/singleflight"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// ═══ PARTS · THE PIECES LIST (M6, tmp/plans/flat-consistency 107) ═══
//
// The parts labeller names regions only from a CLOSED list of pieces. That list used to be derived
// from the join list (design_joins), which was read from the PHOTOS and is made of edges, not pieces:
// on card 38 «inner front v» from the list landed on the front's armhole panels, «right armhole» was
// an edge. Now the list is read ONCE per FRONT/BACK pair from the ACCEPTED flats themselves — the
// plates of the FLAT bench slots, the very drawings the designer paints — by one vision call, from a
// fixed garment vocabulary (designPiecesNouns / designPiecesQualifiers); the designer edits it
// (SetDesignPartsPieces) and a later read never writes over that (the store holds it as a proposal).
// Nothing here reads photos or joins, and nothing here reaches image generation.

const (
	designPiecesMaxTokens = 2000
	designPiecesMaxWords  = 3 // words of a read name, past an optional left/right
)

// designPiecesSystemPrompt — fixed text; nothing of the request reaches the system role. Any change:
// bump designPartsPromptRev only if the LABELLER's words change; a read is redone when the plates do.
const designPiecesSystemPrompt = `These are the ACCEPTED technical flats of ONE garment: black line drawings of the empty garment lying flat, the FRONT view and the BACK view (the message says which image is which; one may be missing).

List the garment's PIECES — every separately cut piece of cloth a designer could make from its own material — and its OPENINGS (areas inside the outline with no cloth at all).

Rules:
- Read only what is drawn. A piece is cloth bounded by its own seam lines or edges in the drawing. Never add a piece the drawing does not show. Never split one panel because of a dart, a fold, a drape or shading line, gathers or a dashed topstitch row.
- A thin strip along an edge is a piece only when it is drawn as its own band sewn on (a neckband, a waistband, a hem band, a cuff, a binding along the neck or along the edge of an open back). AN ARMHOLE HAS NO PIECE OF ITS OWN: the strip and the area along an armhole are the body panel they lie on (or the strap that runs on into it) — never list an armhole binding, an armhole panel or anything named after the armhole. A plain turned hem is no piece.
- An inner layer seen through or under an opening of the outer layer is a piece ("inner front layer").
- Name every piece with ONE word from NOUNS as its LAST word, preceded by at most two words from QUALIFIERS or NOUNS: "front body", "back yoke", "collar stand", "pocket flap", "inner front layer". Never write left or right yourself: a piece cut twice as a mirror pair (two sleeves, two cuffs, two straps, two front panels either side of a centre-front opening or seam, two back panels either side of a centre-back seam, a pocket on each side) is ONE entry with "pair": true. Only a piece that exists on one side only starts with "left" or "right" (the WEARER's side: on the front view the wearer's left is picture-right).
- Bodies: one "front body" and one "back body", unless a centre seam or a centre-front opening splits that body into two (then it is one entry with "pair": true). A garment with no back cloth has no back body.
- "views": the views ("front", "back") on which the piece shows.
- "openings": at most 4, two or three words each ("open back", "keyhole", "gap between straps"). The neck hole, the armholes and the hem opening of an ordinary garment are not listed.

NOUNS: %s
QUALIFIERS: %s

Answer with JSON only:
{"pieces":[{"name":"front body","pair":false,"views":["front"]},{"name":"sleeve","pair":true,"views":["front","back"]}],"openings":["open back"]}`

// designPiecesNouns — the last word of every piece a read may name: the fixed garment vocabulary.
var designPiecesNouns = []string{
	"body", "bodice", "panel", "yoke", "sleeve", "cuff", "collar", "stand", "lapel", "neckband", "band",
	"binding", "placket", "pocket", "flap", "welt", "waistband", "strap", "hood", "cup", "skirt", "leg",
	"fly", "gusset", "belt", "loop", "tab", "lining", "layer", "trim", "ruffle", "frill", "peplum", "tie",
	"drawcord", "epaulette", "gore", "godet", "insert", "inset", "overlay", "facing", "cape", "patch", "bib",
	"flounce", "drape", "cowl", "sash", "bow", "piping", "tape", "casing", "channel",
}

// designPiecesQualifiers — the words that may stand before the noun (nouns may too: «collar stand»).
var designPiecesQualifiers = []string{
	"front", "back", "upper", "lower", "inner", "outer", "centre", "center", "side", "top", "bottom",
	"under", "mid", "chest", "breast", "hip", "shoulder", "neck", "hem", "waist", "elbow", "knee", "seat",
	"coin", "cross", "wrap", "rib",
}

var (
	designPiecesNounSet      = designPiecesSet(designPiecesNouns)
	designPiecesQualifierSet = designPiecesSet(designPiecesQualifiers)
	designPiecesSystem       = fmt.Sprintf(designPiecesSystemPrompt,
		strings.Join(designPiecesNouns, ", "), strings.Join(designPiecesQualifiers, ", "))
)

func designPiecesSet(words []string) map[string]bool {
	out := make(map[string]bool, len(words))
	for _, w := range words {
		out[w] = true
	}
	return out
}

// designPiecesUserPrompt — which image is which view.
func designPiecesUserPrompt(views []string) string {
	var b strings.Builder
	for i, v := range views {
		fmt.Fprintf(&b, "image %d: the %s flat\n", i+1, strings.ToUpper(v))
	}
	if len(views) == 1 {
		fmt.Fprintf(&b, "There is no %s flat: list the pieces this view shows.\n", strings.ToUpper(designPiecesOtherView(views[0])))
	}
	b.WriteString("List the garment's pieces and openings.")
	return b.String()
}

func designPiecesOtherView(v string) string {
	if v == entity.DesignViewFront {
		return entity.DesignViewBack
	}
	return entity.DesignViewFront
}

// designPiecesNameOK — a read name in the vocabulary: 1..designPiecesMaxWords words past an optional
// left/right, the last a noun, the others nouns or qualifiers; nothing named after the armhole.
func designPiecesNameOK(name string) bool {
	words := strings.Fields(name)
	if len(words) > 0 && (words[0] == "left" || words[0] == "right") {
		words = words[1:]
	}
	if len(words) == 0 || len(words) > designPiecesMaxWords || !designPiecesNounSet[words[len(words)-1]] {
		return false
	}
	for _, w := range words[:len(words)-1] {
		if !designPiecesNounSet[w] && !designPiecesQualifierSet[w] {
			return false
		}
	}
	return true
}

type designPiecesRaw struct {
	Pieces   []json.RawMessage `json:"pieces"`
	Openings []json.RawMessage `json:"openings"`
}

type designPiecesRawPiece struct {
	Name  string          `json:"name"`
	Pair  json.RawMessage `json:"pair"`
	Views []string        `json:"views"`
}

// designPiecesTruthy — true, "true", "yes", 1.
func designPiecesTruthy(raw json.RawMessage) bool {
	switch strings.ToLower(strings.Trim(strings.TrimSpace(string(raw)), `"`)) {
	case "true", "yes", "1":
		return true
	}
	return false
}

// parseDesignPieces cleans a read's answer into the stored list. A piece outside the vocabulary
// (designPiecesNameOK) is dropped, as is a repeat; a pair becomes «left X» and «right X» (a «left X»
// the model wrote itself, with «right X» beside it, is the same pair); views are front/back only;
// openings are short cleaned words. ok=false when no JSON of that shape is there or no piece survives.
// `dropped` — what the vocabulary refused (for the log).
func parseDesignPieces(raw string) (doc entity.DesignPartsPiecesDoc, dropped []string, ok bool) {
	body := strings.TrimSpace(raw)
	var in designPiecesRaw
	if json.Unmarshal([]byte(body), &in) != nil || in.Pieces == nil {
		i, j := strings.Index(body, "{"), strings.LastIndex(body, "}")
		if i < 0 || j <= i || json.Unmarshal([]byte(body[i:j+1]), &in) != nil || in.Pieces == nil {
			return entity.DesignPartsPiecesDoc{}, nil, false
		}
	}
	seen := map[string]bool{}
	add := func(name string, views []string) {
		if seen[name] || len(doc.Pieces) == entity.DesignPartsPiecesMax {
			return
		}
		seen[name] = true
		doc.Pieces = append(doc.Pieces, entity.DesignPartsPiece{Name: name, Views: views})
	}
	for _, r := range in.Pieces {
		var p designPiecesRawPiece
		if json.Unmarshal(r, &p) != nil {
			var s string
			if json.Unmarshal(r, &s) != nil {
				continue
			}
			p.Name = s
		}
		name := entity.DesignPartsPieceNameOf(p.Name)
		if name == "" {
			continue
		}
		if !designPiecesNameOK(name) || entity.DesignPartsPieceReserved(name) ||
			utf8.RuneCountInString(name) > entity.DesignPartsPieceMaxRunes-len("right ") {
			dropped = append(dropped, name)
			continue
		}
		var views []string
		vs := map[string]bool{}
		for _, v := range p.Views {
			v = strings.ToLower(strings.TrimSpace(v))
			if (v == entity.DesignViewFront || v == entity.DesignViewBack) && !vs[v] {
				vs[v] = true
				views = append(views, v)
			}
		}
		pair := designPiecesTruthy(p.Pair)
		if base, cut := strings.CutPrefix(name, "left "); cut && pair {
			name = base
		} else if base, cut := strings.CutPrefix(name, "right "); cut && pair {
			name = base
		}
		if pair {
			add("left "+name, views)
			add("right "+name, views)
			continue
		}
		add(name, views)
	}
	if len(doc.Pieces) == 0 {
		return entity.DesignPartsPiecesDoc{}, dropped, false
	}
	for _, r := range in.Openings {
		var s string
		if json.Unmarshal(r, &s) != nil {
			var o struct {
				Name string `json:"name"`
			}
			if json.Unmarshal(r, &o) != nil {
				continue
			}
			s = o.Name
		}
		s = entity.DesignPartsPieceNameOf(s)
		if s == "" || len(strings.Fields(s)) > 5 || utf8.RuneCountInString(s) > entity.DesignPartsOpeningMaxRunes {
			continue
		}
		if len(doc.Openings) < entity.DesignPartsOpeningsMax {
			doc.Openings = append(doc.Openings, s)
		}
	}
	return doc, dropped, true
}

// designPiecesFlightAnswer — what one read hands every press that waited on it.
type designPiecesFlightAnswer struct {
	pieces *entity.DesignPartsPieces
}

// designPartsPiecesFor — the card's pieces list for the FRONT/BACK plates on the bench NOW, read when
// there is none or the plates moved since the newest read. A failed read leaves an older list
// standing (logged); with no list at all the failure is the caller's. nil, nil = no FRONT/BACK plate
// to read from and no list (the labeller then names freely).
func (s *Server) designPartsPiecesFor(ctx context.Context, cardID int, flats map[string]int) (*entity.DesignPartsPieces, error) {
	cur, err := s.repo.Design().GetPartsPieces(ctx, cardID)
	if err != nil {
		return nil, designError(ctx, "failed to read the pieces list", err, map[string]string{"tech_card_id": strconv.Itoa(cardID)})
	}
	front, back := flats[entity.DesignViewFront], flats[entity.DesignViewBack]
	if front == 0 && back == 0 {
		return cur, nil
	}
	if cur != nil {
		if f, b := cur.ReadFrom(); f == front && b == back {
			return cur, nil
		}
	}
	seen := 0
	if cur != nil {
		seen = cur.Rev
	}
	fallback := func(err error) (*entity.DesignPartsPieces, error) {
		if cur != nil && len(cur.Doc.Pieces) > 0 {
			slog.Default().WarnContext(ctx, "design parts pieces: the flats moved but were not read; the older list stands",
				slog.Int("tech_card_id", cardID), slog.Int("front", front), slog.Int("back", back),
				slog.String("err", status.Convert(err).Message()))
			return cur, nil
		}
		return nil, err
	}
	if err := s.designGenerationGate(); err != nil {
		return fallback(err)
	}
	const purpose = entity.AIPurposeDesignParts
	if !s.ai.Enabled(purpose) {
		return fallback(s.aiOffRefusal(purpose, designPartsNotConfiguredMsg))
	}
	budget := s.ai.ChainBudget(purpose, designPiecesMaxTokens)
	key := fmt.Sprintf("%d|%d|%d|%d", cardID, front, back, seen)
	ch := s.partsPiecesFlight.DoChan(key, func() (any, error) {
		fctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), designPartsCardAttempts*budget+designPartsFlightMargin)
		defer cancel()
		return s.designPartsPiecesRead(fctx, cardID, front, back, seen, budget)
	})
	var res singleflight.Result
	select {
	case res = <-ch:
	case <-ctx.Done():
		return nil, status.FromContextError(ctx.Err()).Err()
	}
	if res.Err != nil {
		return fallback(res.Err)
	}
	return res.Val.(designPiecesFlightAnswer).pieces, nil
}

// designPartsPiecesRead — the fences and the ONE read of the FRONT/BACK plates (the flight leader's
// work); the answer is stored (SavePartsPiecesRead decides list or proposal) and the row returned.
func (s *Server) designPartsPiecesRead(ctx context.Context, cardID, front, back, seenRev int, budget time.Duration) (designPiecesFlightAnswer, error) {
	const purpose = entity.AIPurposeDesignParts
	var views []string
	var ids []int
	for _, v := range []struct {
		view string
		id   int
	}{{entity.DesignViewFront, front}, {entity.DesignViewBack, back}} {
		if v.id > 0 {
			views, ids = append(views, v.view), append(ids, v.id)
		}
	}
	if err := s.repo.Design().AssertMediaNotForeign(ctx, cardID, ids); err != nil {
		return designPiecesFlightAnswer{}, designError(ctx, "the flat picture is refused", err, nil)
	}
	urls, attached, err := s.designBoardPictureURLs(ctx, ids)
	if err != nil {
		slog.Default().ErrorContext(ctx, "design parts pieces: cannot resolve the flat pictures",
			slog.Int("tech_card_id", cardID), slog.String("err", err.Error()))
		return designPiecesFlightAnswer{}, status.Error(codes.Internal, "cannot read the flat picture")
	}
	urlOf := make(map[int]string, len(attached))
	for i, id := range attached {
		urlOf[id] = urls[i]
	}
	ordered := make([]string, 0, len(ids))
	refs := make([]designInputMediaRef, 0, len(ids))
	for i, id := range ids {
		u, ok := urlOf[id]
		if !ok {
			return designPiecesFlightAnswer{}, status.Errorf(codes.FailedPrecondition, "the %s flat has no file", views[i])
		}
		ordered = append(ordered, u)
		refs = append(refs, designInputMediaRef{ID: id, URL: u, Where: "the " + views[i] + " flat"})
	}
	if ref, ct, bad := designFirstNonPictureInput(refs); bad {
		return designPiecesFlightAnswer{}, designNonPictureRefusal(ref, ct)
	}

	// The fences of EnhanceText, THE SAME ONES as the labeller's: one semaphore, one hourly window.
	select {
	case s.enhanceSem <- struct{}{}:
		defer func() { <-s.enhanceSem }()
	default:
		return designPiecesFlightAnswer{}, status.Error(codes.ResourceExhausted, "the assistant is busy right now — try again in a moment")
	}
	if !s.enhanceRuns.allow(authsrv.GetAdminUsername(ctx)) {
		return designPiecesFlightAnswer{}, status.Errorf(codes.ResourceExhausted,
			"this account has used the assistant %d times in the last hour (ideas, text improvements, the quiz and the parts share the limit); every call spends the AI key — try again later",
			enhancePerAdminCalls)
	}

	user := designPiecesUserPrompt(views)
	var (
		doc      entity.DesignPartsPiecesDoc
		answered string
		logAttrs []any
	)
	err = designPartsRetryUnusable(ctx, budget, func(actx context.Context, attempt int) error {
		started := time.Now()
		res, err := s.ai.Chat(actx, purpose, aiprov.ChatRequest{
			System: designPiecesSystem, User: user, ImageURLs: ordered,
			UserAsParts: true, JSONMode: true, MaxTokens: designPiecesMaxTokens, Effort: designPartsEffort,
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
			slog.Int("tech_card_id", cardID), slog.Int("front", front), slog.Int("back", back),
			slog.String("model", answered), slog.Int("attempt", attempt),
			slog.Duration("took", time.Since(started)), slog.String("finish_reason", enhanceLogFinishReason(finishReason)),
			slog.Int("prompt_tokens", usage.Prompt), slog.Int("completion_tokens", usage.Completion),
		}
		if err != nil {
			return s.designPartsChatFailure(ctx, res, err, logAttrs)
		}
		var dropped []string
		var ok bool
		doc, dropped, ok = parseDesignPieces(raw)
		if len(dropped) > 0 {
			logAttrs = append(logAttrs, slog.String("dropped", strings.Join(dropped, "; ")))
		}
		if !ok {
			slog.Default().ErrorContext(ctx, "design parts pieces: the answer has no usable piece", logAttrs...)
			return status.Error(codes.Internal, designPartsUnusableMsg)
		}
		return nil
	})
	if err != nil {
		return designPiecesFlightAnswer{}, err
	}
	saved, err := s.repo.Design().SavePartsPiecesRead(ctx, entity.DesignPartsPiecesRead{
		TechCardId: cardID, ExpectedRev: seenRev, Doc: doc, Front: front, Back: back, Model: answered,
	})
	if err != nil {
		return designPiecesFlightAnswer{}, designError(ctx, "failed to save the pieces list", err, nil)
	}
	slog.Default().InfoContext(ctx, "design parts pieces read", append(logAttrs,
		slog.String("pieces", strings.Join(doc.Names(), "; ")), slog.String("openings", strings.Join(doc.Openings, "; ")),
		slog.Bool("proposed", saved != nil && saved.Proposal != nil && saved.Edited()))...)
	return designPiecesFlightAnswer{pieces: saved}, nil
}

// SetDesignPartsPieces saves the designer's pieces list (CAS on rev). Spends no key.
func (s *Server) SetDesignPartsPieces(ctx context.Context, req *pb_admin.SetDesignPartsPiecesRequest) (*pb_admin.SetDesignPartsPiecesResponse, error) {
	cardID := int(req.GetTechCardId())
	switch {
	case cardID <= 0:
		return nil, status.Error(codes.InvalidArgument, "tech_card_id is required")
	case req.GetExpectedRev() < 0:
		return nil, status.Error(codes.InvalidArgument, "expected_rev must be 0 or more")
	case len(req.GetNames()) > 2*entity.DesignPartsPiecesMax:
		return nil, status.Errorf(codes.InvalidArgument, "the list holds %d names; the ceiling is %d",
			len(req.GetNames()), entity.DesignPartsPiecesMax)
	}
	names, err := entity.CleanDesignPartsPieceNames(req.GetNames())
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, err.Error())
	}
	saved, err := s.repo.Design().SetPartsPieces(ctx, entity.DesignPartsPiecesSave{
		TechCardId: cardID, ExpectedRev: int(req.GetExpectedRev()), Names: names,
		SettleProposal: req.GetSettleProposal(), Actor: designActor(ctx),
	})
	if err != nil {
		return nil, designError(ctx, "failed to save the pieces list", err, map[string]string{"tech_card_id": strconv.Itoa(cardID)})
	}
	flats, err := s.repo.Design().FlatBenchMedia(ctx, cardID)
	if err != nil {
		return nil, designError(ctx, "failed to read the flat bench", err, nil)
	}
	return &pb_admin.SetDesignPartsPiecesResponse{Pieces: designPartsPiecesToPb(saved, flats)}, nil
}

// designPartsPiecesStale — the FRONT/BACK plates on the bench are not the ones the newest read saw.
func designPartsPiecesStale(p *entity.DesignPartsPieces, flats map[string]int) bool {
	front, back := flats[entity.DesignViewFront], flats[entity.DesignViewBack]
	if p == nil || front == 0 && back == 0 {
		return false
	}
	f, b := p.ReadFrom()
	return f != front || b != back
}

func designPartsPiecesOfPb(in []entity.DesignPartsPiece) []*pb_common.DesignPartsPiece {
	out := make([]*pb_common.DesignPartsPiece, 0, len(in))
	for _, p := range in {
		out = append(out, &pb_common.DesignPartsPiece{Name: p.Name, Views: append([]string{}, p.Views...)})
	}
	return out
}

func designPartsPiecesToPb(p *entity.DesignPartsPieces, flats map[string]int) *pb_common.DesignPartsPieces {
	if p == nil {
		return nil
	}
	out := &pb_common.DesignPartsPieces{
		Rev: int32(p.Rev), Pieces: designPartsPiecesOfPb(p.Doc.Pieces), Openings: append([]string{}, p.Doc.Openings...),
		Model: p.Model, Edited: p.Edited(), FrontMediaId: int32(p.Front), BackMediaId: int32(p.Back),
		Stale: designPartsPiecesStale(p, flats),
	}
	if p.EditedAt.Valid {
		out.EditedAt = timestamppb.New(p.EditedAt.Time)
	}
	if pr := p.Proposal; pr != nil {
		out.Proposal = &pb_common.DesignPartsPiecesProposal{
			Pieces: designPartsPiecesOfPb(pr.Doc.Pieces), Openings: append([]string{}, pr.Doc.Openings...),
			Model: pr.Model, FrontMediaId: int32(pr.Front), BackMediaId: int32(pr.Back), ReadAt: timestamppb.New(pr.ReadAt),
		}
	}
	return out
}

// designPartsPiecesBlock — the labeller's PIECES block (M6): the closed names, the openings, and the
// rules that keep a name where it belongs. "" when there is no list.
func designPartsPiecesBlock(p *entity.DesignPartsPieces) string {
	if p == nil || len(p.Doc.Pieces) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("PIECES of this garment — the CLOSED list, read from its accepted front and back flats")
	if p.Edited() {
		b.WriteString(" and corrected by the designer")
	}
	b.WriteString(". Label every region with exactly one of these names, or \"opening\":\n")
	// The read's views are NOT said: they vary from read to read (card 38: a strap «on front and back»,
	// then «on the back»), and «seen on the front» pulled the front's armhole strips into the strap —
	// the owner's «there is no piece there». The labeller reads the views from the drawing.
	for _, pc := range p.Doc.Pieces {
		b.WriteString("- ")
		b.WriteString(pc.Name)
		b.WriteString("\n")
	}
	if len(p.Doc.Openings) > 0 {
		b.WriteString("Openings (no cloth): ")
		b.WriteString(strings.Join(p.Doc.Openings, ", "))
		b.WriteString(".\n")
	}
	b.WriteString("A side view shows only pieces of this list — never a piece of its own. A piece need not have a region on every view: where its lines are not closed it has no region there, and its name never goes to another region instead.")
	return b.String()
}

// designPartsPiecesRev — the rev the labeller's answers are tagged with; 0 = no list.
func designPartsPiecesRev(p *entity.DesignPartsPieces) int {
	if p == nil {
		return 0
	}
	return p.Rev
}

// designPartsPiecesVocab — the closed names (nil = no list: labels stay free).
func designPartsPiecesVocab(p *entity.DesignPartsPieces) []string {
	if p == nil || len(p.Doc.Pieces) == 0 {
		return nil
	}
	return p.Doc.Names()
}
