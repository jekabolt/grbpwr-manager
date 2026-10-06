package admin

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/aiprov"
	authsrv "github.com/jekabolt/grbpwr-manager/internal/apisrv/auth"
	"github.com/jekabolt/grbpwr-manager/internal/designgen"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	pb_admin "github.com/jekabolt/grbpwr-manager/proto/gen/admin"
	pb_common "github.com/jekabolt/grbpwr-manager/proto/gen/common"
	"golang.org/x/sync/singleflight"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// ─────────────── FLAT ROUTE · THE JOIN LIST (2026-10-05, 0397) ───────────────
//
// GenerateDesignJoins: ONE sync vision+JSON call (chat.design_joins, opus-5.5) reads the card's
// reference photos with their roles and the garment note and writes the garment's construction on
// the fixed landmark ruler (tmp/plans/flat-consistency r5.py JSYS + layers + the photos verdict). The
// answer is cleaned by entity.SanitizeDesignJoinsDoc and stored as the card's current list.
// SetDesignJoins: the designer's correction, under CAS, cleaned by the same function.
//
// The doors are the parts call's: the arguments → the card → the cache (same photos and note, or a
// designer's edit, unless force) → the generation gate → the purpose is callable → the photos are
// ours and pictures → one flight per (card, source) → the shared fences. Logs never carry err.Error()
// of a provider answer — only its class.

const (
	designJoinsMaxTokens = 9000
	designJoinsEffort    = "medium"

	designJoinsNotConfiguredMsg = "writing the join list is not configured: " + openRouterNoKeyMsg
	designJoinsModelUnavailMsg  = "writing the join list is misconfigured: " + modelUnavailableAdviceMsg
	designJoinsNothingToReadMsg = "no reference photo and no garment note · add one first"
	designJoinsUnusableMsg      = "the assistant answered nothing usable"
)

// designJoinsSystemPrompt — fixed text; nothing of the request reaches the system role.
var designJoinsSystemPrompt = `You are a senior pattern maker. Describe ONE garment as a STRUCTURED JOIN LIST on a fixed landmark ruler, so that code can draw a schematic of it and an illustrator can draw technical flats whose joins are right.

` + entity.DesignJoinLandmarkHelp + `

List EVERY edge and line of the garment as items (paths through landmarks, in order):
- kind "edge": a raw or finished outer edge of the garment (side outlines, hem, armhole edges, neck edges, edges of openings);
- kind "seam": a seam line (side seam, shoulder seam, yoke seam, panel/bib seams, armhole seam of a set-in sleeve);
- kind "binding"/"band"/"strap"/"collar"/"stand"/"placket"/"cuff"/"waistband": a band of its own width ("width": "narrow"|"wide"), with its path from where it STARTS to where it ENDS — e.g. a binding that runs only across the front from NP_R via CFN to NP_L, or a strap from NP_L over the back (via UB_C) to MB_R;
- kind "sleeve": the sleeve outline (e.g. SP_L, ELB_OUT_L, WRIST_OUT_L, WRIST_IN_L, UA_L);
- kind "closure": "type" (buttons|zip|hook), path along its line, "count";
- the item that draws the FRONT neckline (its path passes CFN, CFN_LOW or BREAK) gets "type": crew|v|scoop|halter|boat|square|mock|turtle — the neckline's shape;
- kind "pocket": "anchor" landmark (on the correct WEARER side), "type" (patch|flap|welt|kangaroo|jeans-front-scoop|back-patch), "size" (half-width in ruler units, e.g. 0.045), or a "path" polygon through landmarks for a shaped pocket;
- kind "opening": "bounded_by" item ids (no path needed) — an area with no cloth.
Each item: {"id","kind","path":[...],"closed":false,"width":..., "sharp":["landmarks in the path that are CORNERS (e.g. the point of a V, hem corners); every other point is passed through smoothly"], "continues_into":["other item id at its ends"], "note":"short"}.
Paths go DIRECTLY through the landmarks the line really passes; do not route a diagonal line through extra landmarks it does not touch. Use ONLY the landmark names of the ruler.
Also list explicit ABSENCES that illustrators tend to invent ("no back neckline", "no back neck binding", "no sleeves", "no pocket on the right"...). Every absence starts with "no"; a positive remark about the garment (an edge is raw, a seam is topstitched) belongs in the note of its item, never in the absences.
Garment silhouette: the front and back outlines must be closed by edges (neck, shoulders/straps, armholes, sides, hem).

Reference consistency: say whether all photos show the SAME garment; if not, group the photos by garment and say which photos to keep ("keep"); write the list from those only.

LAYERS. Garments can have several cloth layers one over another (a sheer outer layer over an inner layer, a lining, a bib/modesty panel, a double-layer front). Decide from the photos:
- is any cloth SHEER (skin tone, underwear or an inner layer's edge visible through it)?
- how many layers are there over each face (front/back)? For each layer: its own edges (where they END — a free hanging edge, or caught into a seam/binding with another layer), and which other layers it is sewn to.
Cues: an edge that shows as a soft, light or shadowy line WITHOUT a stitched/bound finish and that does not change the silhouette is usually the edge of a layer BEHIND the outer cloth (visibility "through"); several parallel bound edges stacking at a neck/shoulder/armhole mean several layers each with its own edge finish; a bound/stitched edge that changes the outline is the outer layer's edge.
Represent layers in the JSON: "layers":[{"index":0,"name":"outer front","sheer":true,"face":"front","note":"..."}], index 0 = outermost. Every item gets "layer": <index> and "visibility": "visible" (on the outside) | "through" (behind sheer cloth, seen as a shadow) | "hidden" (not seen on any view but needed for construction). A layer's edge that is caught into another item gets "caught_into": ["item id"]; a free hanging edge gets "free_edge": true. Do not merge two layers' edges into one item.
A layer is a DEPTH level per face, not a panel: 0 = everything outermost (the back panel of a single-layer back is layer 0 too), 1 = the cloth directly behind layer 0. "face" is front, back or both.
State in "uncertain" whatever you cannot tell from the photos.

Also give "fit": {"ease": "slim|regular|relaxed|oversized", "waist": "fitted|straight"} for the body block.

Answer with JSON only:
{"fit":{...},"consistency":{"consistent":true,"note":"...","groups":[{"images":[1,2],"what":"..."}],"keep":[1,2]},"layers":[...],"items":[...],"absences":["no ..."],"uncertain":["..."]}`

// designJoinsPhoto — one reference photo as the call reads it.
type designJoinsPhoto struct {
	MediaID int
	Role    string
	Note    string
	URL     string
}

var designJoinsRoleWords = map[string]string{
	entity.DesignViewFront:  "front",
	entity.DesignViewBack:   "back",
	entity.DesignViewSideL:  "left side (shows the wearer's LEFT flank)",
	entity.DesignViewSideR:  "right side (shows the wearer's RIGHT flank)",
	entity.DesignViewDetail: "detail close-up",
	"three_quarter_l":       "three-quarter view from the wearer's left",
	"three_quarter_r":       "three-quarter view from the wearer's right",
}

// designJoinsUserPrompt numbers the photos in the order they travel.
func designJoinsUserPrompt(photos []designJoinsPhoto, note string) string {
	var b strings.Builder
	for i, p := range photos {
		role := designJoinsRoleWords[p.Role]
		if role == "" {
			role = strings.ReplaceAll(p.Role, "_", " ")
		}
		fmt.Fprintf(&b, "image %d: %s photo", i+1, role)
		if n := strings.TrimSpace(p.Note); n != "" {
			fmt.Fprintf(&b, " — %s", oneLineWords(n))
		}
		b.WriteString("\n")
	}
	if n := strings.TrimSpace(note); n != "" {
		fmt.Fprintf(&b, "garment note: %s\n", n)
	}
	b.WriteString("Write the join list.")
	return b.String()
}

func oneLineWords(s string) string { return strings.Join(strings.Fields(s), " ") }

// designJoinsFingerprint — what the list was written from: the photos (id, role, note) and the note.
func designJoinsFingerprint(photos []designJoinsPhoto, note string) string {
	h := sha256.New()
	for _, p := range photos {
		fmt.Fprintf(h, "%d|%s|%s\n", p.MediaID, p.Role, oneLineWords(p.Note))
	}
	fmt.Fprintf(h, "note|%s", oneLineWords(note))
	return hex.EncodeToString(h.Sum(nil))[:32]
}

// designJoinsNote — the card's garment note as the join call reads it (trimmed, capped).
func designJoinsNote(card *entity.TechCard) string {
	if card == nil {
		return ""
	}
	note := strings.TrimSpace(card.GarmentDescription.String)
	if r := []rune(note); len(r) > designMaxGarmentNoteRunes {
		note = string(r[:designMaxGarmentNoteRunes])
	}
	return note
}

// designJoinsSourceFP — the card's CURRENT source fingerprint: the photos a join call would read (with
// their roles and notes) and the garment note, exactly as GenerateDesignJoins computes it.
func designJoinsSourceFP(card *entity.TechCard, refs []entity.DesignReference) string {
	return designJoinsFingerprint(designJoinsPhotos(refs), designJoinsNote(card))
}

// designJoinsCacheHit — a stored list answers without a call when it was written from the same
// source, or a designer edited it (a regeneration would throw the edit away: that is `force`).
func designJoinsCacheHit(j *entity.DesignJoins, fp string) bool {
	return j != nil && (j.SourceFingerprint == fp || j.EditedAt.Valid)
}

type designJoinsFlightAnswer struct {
	joins  *entity.DesignJoins
	cached bool
}

// GenerateDesignJoins writes the card's join list from its reference photos (cached by source).
func (s *Server) GenerateDesignJoins(ctx context.Context, req *pb_admin.GenerateDesignJoinsRequest) (*pb_admin.GenerateDesignJoinsResponse, error) {
	cardID := int(req.GetTechCardId())
	if cardID <= 0 {
		return nil, status.Error(codes.InvalidArgument, "tech_card_id is required")
	}
	card, err := s.repo.TechCards().GetTechCardById(ctx, cardID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, status.Error(codes.NotFound, "tech card not found")
		}
		slog.Default().ErrorContext(ctx, "design joins: cannot load the tech card",
			slog.Int("tech_card_id", cardID), slog.String("err", err.Error()))
		return nil, status.Error(codes.Internal, "cannot load the tech card")
	}
	note := designJoinsNote(card)
	band, err := s.repo.Design().GetBand(ctx, cardID, 1)
	if err != nil {
		return nil, designError(ctx, "failed to read the design band", err, nil)
	}
	photos := designJoinsPhotos(designRunRefs(card, band.References))
	if len(photos) == 0 && note == "" {
		return nil, status.Error(codes.FailedPrecondition, designJoinsNothingToReadMsg)
	}
	fp := designJoinsFingerprint(photos, note)
	force := req.GetForce()
	// The rev this press saw: the model's answer is saved ONLY over it (CAS), so a designer's edit
	// saved while the model was thinking is never overwritten — the edit wins, the answer is dropped.
	seenRev := 0
	if band.Joins != nil {
		seenRev = band.Joins.Rev
	}
	if !force && designJoinsCacheHit(band.Joins, fp) {
		return &pb_admin.GenerateDesignJoinsResponse{Joins: designJoinsToPb(band.Joins), Cached: true}, nil
	}
	// A NON-FORCE regeneration reads only the photos the last verdict kept (the designer's «photos
	// disagree · pick»); `force` re-reads every photo and re-judges them. The fingerprint stays the
	// full set's, so the cache keeps answering for the same source.
	if !force {
		photos = designJoinsKeptPhotos(photos, band.Joins)
	}

	if err := s.designGenerationGate(); err != nil {
		return nil, err
	}
	const purpose = entity.AIPurposeDesignJoins
	if !s.ai.Enabled(purpose) {
		return nil, s.aiOffRefusal(purpose, designJoinsNotConfiguredMsg)
	}

	// The photos: ours, files, pictures the provider can read — in role order.
	if len(photos) > 0 {
		ids := make([]int, 0, len(photos))
		for _, p := range photos {
			ids = append(ids, p.MediaID)
		}
		if err := s.repo.Design().AssertMediaNotForeign(ctx, cardID, ids); err != nil {
			return nil, designError(ctx, "a reference photo is refused", err, nil)
		}
		urls, attached, err := s.designBoardPictureURLs(ctx, ids)
		if err != nil {
			slog.Default().ErrorContext(ctx, "design joins: cannot resolve the reference photos",
				slog.Int("tech_card_id", cardID), slog.String("err", err.Error()))
			return nil, status.Error(codes.Internal, "cannot read the reference photos")
		}
		urlOf := make(map[int]string, len(attached))
		for i, id := range attached {
			urlOf[id] = urls[i]
		}
		kept := photos[:0]
		refs := make([]designInputMediaRef, 0, len(photos))
		for _, p := range photos {
			u, ok := urlOf[p.MediaID]
			if !ok {
				continue // a reference without a file: the call reads the others
			}
			p.URL = u
			kept = append(kept, p)
			refs = append(refs, designInputMediaRef{ID: p.MediaID, URL: u, Where: "the " + p.Role + " reference"})
		}
		photos = kept
		if ref, ct, bad := designFirstNonPictureInput(refs); bad {
			return nil, designNonPictureRefusal(ref, ct)
		}
		if len(photos) == 0 && note == "" {
			return nil, status.Error(codes.FailedPrecondition, designJoinsNothingToReadMsg)
		}
	}

	// ⚠ ONE FLIGHT PER (card, source): a double press pays once. Detached from the leader's
	// cancellation under its own budget, like the parts call.
	ch := s.joinsFlight.DoChan(designJoinsFlightKey(cardID, fp, force, photos), func() (any, error) {
		fctx, cancel := context.WithTimeout(context.WithoutCancel(ctx),
			s.ai.ChainBudget(purpose, designJoinsMaxTokens)+designPartsFlightMargin)
		defer cancel()
		return s.designJoinsCall(fctx, cardID, photos, note, fp, force, seenRev)
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
	ans := res.Val.(designJoinsFlightAnswer)
	return &pb_admin.GenerateDesignJoinsResponse{Joins: designJoinsToPb(ans.joins), Cached: ans.cached}, nil
}

// designJoinsFlightKey — one flight per (card, source, force, the photos actually read): a forced
// re-read of every photo never joins a non-force flight that reads only the kept ones and would save
// its narrowed answer under the full source (Codex b2).
func designJoinsFlightKey(cardID int, fp string, force bool, photos []designJoinsPhoto) string {
	ids := make([]int, 0, len(photos))
	for _, p := range photos {
		ids = append(ids, p.MediaID)
	}
	sort.Ints(ids)
	parts := make([]string, len(ids))
	for i, id := range ids {
		parts[i] = strconv.Itoa(id)
	}
	return fmt.Sprintf("%d|%s|force=%t|%s", cardID, fp, force, strings.Join(parts, ","))
}

// designJoinsPhotos — the reference photos the call reads: every reference with a role, in role order
// (front, back, sides, three-quarters, details), then ordinal; at most entity.DesignJoinsMaxPhotos.
func designJoinsPhotos(refs []entity.DesignReference) []designJoinsPhoto {
	rank := map[string]int{
		entity.DesignViewFront: 0, entity.DesignViewBack: 1, entity.DesignViewSideL: 2, entity.DesignViewSideR: 3,
		"three_quarter_l": 4, "three_quarter_r": 5, entity.DesignViewDetail: 6,
	}
	sorted := append([]entity.DesignReference(nil), refs...)
	sort.SliceStable(sorted, func(i, j int) bool {
		ri, ok := rank[sorted[i].Role]
		if !ok {
			ri = 7
		}
		rj, ok := rank[sorted[j].Role]
		if !ok {
			rj = 7
		}
		if ri != rj {
			return ri < rj
		}
		return sorted[i].Ordinal < sorted[j].Ordinal
	})
	var out []designJoinsPhoto
	seen := map[int]bool{}
	for _, r := range sorted {
		if r.MediaId <= 0 || strings.TrimSpace(r.Role) == "" || seen[r.MediaId] {
			continue
		}
		seen[r.MediaId] = true
		out = append(out, designJoinsPhoto{MediaID: r.MediaId, Role: r.Role, Note: r.Note.String})
		if len(out) == entity.DesignJoinsMaxPhotos {
			break
		}
	}
	return out
}

// designJoinsKeptPhotos — photos narrowed to the stored verdict's keep_media_ids; unchanged when
// there is no verdict, no keep list, or none of the kept photos is still a reference.
func designJoinsKeptPhotos(photos []designJoinsPhoto, j *entity.DesignJoins) []designJoinsPhoto {
	keep := designJoinsKeepSet(j)
	if keep == nil {
		return photos
	}
	var out []designJoinsPhoto
	for _, p := range photos {
		if keep[p.MediaID] {
			out = append(out, p)
		}
	}
	if len(out) == 0 {
		return photos
	}
	return out
}

// designJoinsKeepSet — the card's keep_media_ids as a set; nil when there is none.
func designJoinsKeepSet(j *entity.DesignJoins) map[int]bool {
	if j == nil || len(j.Consistency.KeepMediaIDs) == 0 {
		return nil
	}
	keep := make(map[int]bool, len(j.Consistency.KeepMediaIDs))
	for _, id := range j.Consistency.KeepMediaIDs {
		keep[id] = true
	}
	return keep
}

// designKeptReferences — a FLAT run reads only the reference photos the join list's verdict kept
// (the photos that show THIS garment); the roles of the others stay on the card untouched. Any other
// kind, no verdict or no keep list — the card's references as they are. When none of the kept photos
// is still a reference, nothing is filtered (an empty reference set would be a different run).
func designKeptReferences(kind string, refs []entity.DesignReference, j *entity.DesignJoins) []entity.DesignReference {
	keep := designJoinsKeepSet(j)
	// WAVE 10 (live check, card 51 run 176): with the join list out of the flat prompt its verdict must
	// not choose the photos either — a list read when the card had one photo kept that one photo and
	// silently dropped the front and side added later. The filter comes back with the switch.
	if kind != entity.DesignRunKindFlat || keep == nil || !designgen.FlatPromptCarriesConstruction {
		return refs
	}
	var out []entity.DesignReference
	for _, r := range refs {
		if keep[r.MediaId] {
			out = append(out, r)
		}
	}
	if len(out) == 0 {
		return refs
	}
	return out
}

// designJoinsCall — the fences and the ONE provider call (the flight leader's work).
func (s *Server) designJoinsCall(ctx context.Context, cardID int, photos []designJoinsPhoto, note, fp string, force bool, seenRev int) (designJoinsFlightAnswer, error) {
	const purpose = entity.AIPurposeDesignJoins
	// A flight that finished just before this one already paid: read the row again.
	if !force {
		j, err := s.repo.Design().GetJoins(ctx, cardID)
		if err != nil {
			return designJoinsFlightAnswer{}, designError(ctx, "failed to read the join list", err, nil)
		}
		if designJoinsCacheHit(j, fp) {
			return designJoinsFlightAnswer{joins: j, cached: true}, nil
		}
	}

	// The fences of EnhanceText, THE SAME ONES: one semaphore, one hourly window.
	select {
	case s.enhanceSem <- struct{}{}:
		defer func() { <-s.enhanceSem }()
	default:
		return designJoinsFlightAnswer{}, status.Error(codes.ResourceExhausted, "the assistant is busy right now — try again in a moment")
	}
	if !s.enhanceRuns.allow(authsrv.GetAdminUsername(ctx)) {
		return designJoinsFlightAnswer{}, status.Errorf(codes.ResourceExhausted,
			"this account has used the assistant %d times in the last hour (ideas, text improvements, the quiz, the parts and the joins share the limit); every call spends the AI key — try again later",
			enhancePerAdminCalls)
	}

	urls := make([]string, 0, len(photos))
	mediaIDs := make([]int, 0, len(photos))
	for _, p := range photos {
		urls = append(urls, p.URL)
		mediaIDs = append(mediaIDs, p.MediaID)
	}
	started := time.Now()
	res, err := s.ai.Chat(ctx, purpose, aiprov.ChatRequest{
		System: designJoinsSystemPrompt, User: designJoinsUserPrompt(photos, note), ImageURLs: urls,
		UserAsParts: true, JSONMode: true, MaxTokens: designJoinsMaxTokens, Effort: designJoinsEffort,
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
		slog.Int("tech_card_id", cardID), slog.Int("photos", len(photos)), slog.Bool("note", note != ""),
		slog.String("model", answered), slog.Duration("took", time.Since(started)),
		slog.String("finish_reason", enhanceLogFinishReason(finishReason)),
		slog.Int("prompt_tokens", usage.Prompt), slog.Int("completion_tokens", usage.Completion),
	}
	if err != nil {
		return designJoinsFlightAnswer{}, s.designJoinsChatFailure(ctx, res, err, logAttrs)
	}

	ans, ok := entity.ParseDesignJoinsAnswer(raw)
	if !ok {
		slog.Default().ErrorContext(ctx, "design joins: the answer is not the promised JSON", logAttrs...)
		return designJoinsFlightAnswer{}, status.Error(codes.Internal, designJoinsUnusableMsg)
	}
	doc := ans.Doc()
	if len(doc.Items) == 0 {
		slog.Default().ErrorContext(ctx, "design joins: the answer has no usable item", logAttrs...)
		return designJoinsFlightAnswer{}, status.Error(codes.Internal, designJoinsUnusableMsg)
	}
	saved, err := s.repo.Design().SaveJoins(ctx, entity.DesignJoinsSave{
		TechCardId: cardID, ExpectedRev: seenRev, Doc: doc, Consistency: ans.ConsistencyFor(mediaIDs),
		Model: answered, SourceFingerprint: fp, Actor: designActor(ctx),
	})
	if errors.Is(err, entity.ErrDesignJoinsRevMismatch) {
		// Someone saved the list while the model was answering: their row stands, the answer goes.
		cur, rerr := s.repo.Design().GetJoins(ctx, cardID)
		if rerr != nil || cur == nil {
			return designJoinsFlightAnswer{}, designError(ctx, "failed to save the join list", err, nil)
		}
		slog.Default().WarnContext(ctx, "design joins: the list changed during the call; the answer is dropped",
			append(logAttrs, slog.Int("seen_rev", seenRev), slog.Int("rev", cur.Rev))...)
		return designJoinsFlightAnswer{joins: cur, cached: true}, nil
	}
	if err != nil {
		return designJoinsFlightAnswer{}, designError(ctx, "failed to save the join list", err, nil)
	}
	slog.Default().InfoContext(ctx, "design joins", append(logAttrs, slog.Int("items", len(doc.Items)),
		slog.Int("absences", len(doc.Absences)), slog.Int("layers", len(doc.Layers)),
		slog.Bool("consistent", saved.Consistency.Consistent))...)
	return designJoinsFlightAnswer{joins: saved}, nil
}

// designJoinsChatFailure — the parts call's mapping, under this purpose's words.
func (s *Server) designJoinsChatFailure(ctx context.Context, res *aiprov.ChatResult, err error, logAttrs []any) error {
	const purpose = entity.AIPurposeDesignJoins
	class := enhanceErrClass(err)
	if refusal, ok := aiUncalledRefusal(err, designJoinsNotConfiguredMsg); ok {
		return refusal
	}
	if class == enhanceErrNotConfigured {
		return aiRefusal(aiReasonNotConfigured, designJoinsNotConfiguredMsg, nil)
	}
	provider := s.aiProviderOf(purpose, res)
	failAttrs := append(logAttrs, slog.String("err_class", class),
		slog.Bool("provider_engaged", aiprov.Engaged(err)),
		slog.String("provider", provider), slog.String("base_url", s.ai.BaseURL(provider)))
	if class == enhanceErrProviderHTTP {
		failAttrs = append(failAttrs, slog.Int("http_status", providerHTTPStatus(err)))
	}
	slog.Default().ErrorContext(ctx, "design joins failed", failAttrs...)
	switch class {
	case enhanceErrModelUnavailable:
		return aiModelRefusal(designJoinsModelUnavailMsg, s.ai.PrimaryModel(purpose))
	case enhanceErrBudgetExhausted, enhanceErrEmptyAnswer:
		return status.Error(codes.Internal, designJoinsUnusableMsg)
	}
	return status.Error(codes.Unavailable, "the assistant is unavailable right now — try again in a moment")
}

// SetDesignJoins saves the designer's list (CAS on rev).
func (s *Server) SetDesignJoins(ctx context.Context, req *pb_admin.SetDesignJoinsRequest) (*pb_admin.SetDesignJoinsResponse, error) {
	cardID := int(req.GetTechCardId())
	switch {
	case cardID <= 0:
		return nil, status.Error(codes.InvalidArgument, "tech_card_id is required")
	case req.GetExpectedRev() < 0:
		return nil, status.Error(codes.InvalidArgument, "expected_rev must be 0 or more")
	case req.GetJoins() == nil:
		return nil, status.Error(codes.InvalidArgument, "joins is required")
	case len(req.GetJoins().GetItems()) > 2*entity.DesignJoinsMaxItems:
		return nil, status.Errorf(codes.InvalidArgument, "joins carries %d items; the ceiling is %d",
			len(req.GetJoins().GetItems()), entity.DesignJoinsMaxItems)
	}
	before, err := s.repo.Design().GetJoins(ctx, cardID)
	if err != nil {
		return nil, designError(ctx, "failed to read the join list", err, nil)
	}
	in := req.GetJoins()
	doc := entity.SanitizeDesignJoinsDoc(designJoinsDocFromPb(in))
	// What the designer changed is said to the flat model verbatim (81-FINAL-MODES type 6); the mark is
	// computed here against the stored list, never read off the wire.
	var prev *entity.DesignJoinsDoc
	if before != nil {
		prev = &before.Doc
	}
	designJoinsMarkEdits(&doc, prev)
	// The confirmation belongs to THIS save's rev; a save without it clears it (the doc is written whole).
	// It records the card's source fingerprint at this moment: a confirmation is of the list AGAINST
	// these photos and this note. Data only since M7 (07.10): the straps door that refused a stale
	// confirmation is retired with the mode, and nothing in the flat run path reads it.
	doc.Confirmed = req.GetConfirm()
	if doc.Confirmed {
		cfp, err := s.designJoinsCurrentSource(ctx, cardID)
		if err != nil {
			return nil, err
		}
		doc.ConfirmedSource = cfp
	}
	// The photos verdict is the model's; the designer may only narrow which photos to keep.
	cons := entity.DesignJoinsConsistency{Consistent: true}
	model, fp := "", ""
	if before != nil {
		cons, model, fp = before.Consistency, before.Model, before.SourceFingerprint
	}
	if keep := in.GetConsistency().GetKeepMediaIds(); len(keep) > 0 {
		allowed := map[int]bool{}
		for _, id := range cons.KeepMediaIDs {
			allowed[id] = true
		}
		for _, g := range cons.Groups {
			for _, id := range g.MediaIDs {
				allowed[id] = true
			}
		}
		var ids []int
		for _, id := range keep {
			if allowed[int(id)] {
				ids = append(ids, int(id))
			}
		}
		if len(ids) > 0 {
			cons.KeepMediaIDs = ids
		}
	}
	saved, err := s.repo.Design().SaveJoins(ctx, entity.DesignJoinsSave{
		TechCardId: cardID, ExpectedRev: int(req.GetExpectedRev()), Doc: doc, Consistency: cons,
		Model: model, SourceFingerprint: fp, Actor: designActor(ctx), Edited: true,
	})
	if err != nil {
		return nil, designError(ctx, "failed to save the join list", err, map[string]string{"tech_card_id": strconv.Itoa(cardID)})
	}
	return &pb_admin.SetDesignJoinsResponse{Joins: designJoinsToPb(saved)}, nil
}

// designJoinsCurrentSource — the card's current source fingerprint, read fresh (card + references).
func (s *Server) designJoinsCurrentSource(ctx context.Context, cardID int) (string, error) {
	card, err := s.repo.TechCards().GetTechCardById(ctx, cardID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return "", status.Error(codes.NotFound, "tech card not found")
		}
		return "", designError(ctx, "failed to read the tech card", err, nil)
	}
	band, err := s.repo.Design().GetBand(ctx, cardID, 1)
	if err != nil {
		return "", designError(ctx, "failed to read the design band", err, nil)
	}
	return designJoinsSourceFP(card, designRunRefs(card, band.References)), nil
}

// ─── the wire ───

func designJoinsToPb(j *entity.DesignJoins) *pb_common.DesignJoins {
	if j == nil {
		return nil
	}
	out := &pb_common.DesignJoins{
		Rev: int32(j.Rev), Absences: append([]string{}, j.Doc.Absences...), Model: j.Model,
		Edited: j.EditedAt.Valid, CreatedAt: timestamppb.New(j.CreatedAt), Uncertain: append([]string{}, j.Doc.Uncertain...),
		Items: make([]*pb_common.DesignJoinItem, 0, len(j.Doc.Items)),
	}
	if j.EditedAt.Valid {
		out.EditedAt = timestamppb.New(j.EditedAt.Time)
	}
	for _, l := range j.Doc.Layers {
		out.Layers = append(out.Layers, &pb_common.DesignJoinLayer{Index: int32(l.Index), Name: l.Name, Sheer: l.Sheer, Note: l.Note, Face: l.Face})
	}
	for _, it := range j.Doc.Items {
		out.Items = append(out.Items, &pb_common.DesignJoinItem{
			Kind: it.Kind, From: it.From, To: it.To, View: it.View, Side: it.Side, Text: it.Text, Id: it.ID,
			Via: it.Via, Width: it.Width, Closed: it.Closed, Type: it.Type, Count: int32(it.Count),
			BoundedBy: it.BoundedBy, ContinuesInto: it.ContinuesInto, Layer: int32(it.Layer), Visibility: it.Visibility,
			CaughtInto: it.CaughtInto, FreeEdge: it.FreeEdge, Sharp: it.Sharp, Size: it.Size, Edited: it.Edited,
		})
	}
	c := &pb_common.DesignJoinsConsistency{Consistent: j.Consistency.Consistent, Note: j.Consistency.Note}
	for _, id := range j.Consistency.KeepMediaIDs {
		c.KeepMediaIds = append(c.KeepMediaIds, int32(id))
	}
	for _, g := range j.Consistency.Groups {
		pg := &pb_common.DesignJoinsConsistencyGroup{What: g.What}
		for _, id := range g.MediaIDs {
			pg.MediaIds = append(pg.MediaIds, int32(id))
		}
		c.Groups = append(c.Groups, pg)
	}
	out.Consistency = c
	if f := j.Doc.Fit; f != nil {
		out.Fit = &pb_common.DesignJoinsFit{Ease: f.Ease, Waist: f.Waist}
	}
	out.Confirmed = j.Doc.Confirmed
	return out
}

// designJoinsMarkEdits — marks every item a designer added or changed (against the stored list; an item
// already marked keeps its mark while it lives) and every absence they added.
func designJoinsMarkEdits(doc *entity.DesignJoinsDoc, prev *entity.DesignJoinsDoc) {
	old := map[string]entity.DesignJoinItem{}
	oldAbs := map[string]bool{}
	oldEditedAbs := map[string]bool{}
	if prev != nil {
		for _, it := range prev.Items {
			old[it.ID] = it
		}
		for _, a := range prev.Absences {
			oldAbs[strings.ToLower(a)] = true
		}
		for _, a := range prev.EditedAbsences {
			oldEditedAbs[strings.ToLower(a)] = true
		}
	}
	for i, it := range doc.Items {
		o, ok := old[it.ID]
		if !ok {
			doc.Items[i].Edited = true
			continue
		}
		was := o.Edited
		o.Edited, it.Edited = false, false
		doc.Items[i].Edited = was || !reflect.DeepEqual(designJoinsComparable(o), designJoinsComparable(it))
	}
	doc.EditedAbsences = nil
	for _, a := range doc.Absences {
		k := strings.ToLower(a)
		if !oldAbs[k] || oldEditedAbs[k] {
			doc.EditedAbsences = append(doc.EditedAbsences, a)
		}
	}
}

// designJoinsComparable — an item with nil and empty slices made equal (the wire and the store differ).
func designJoinsComparable(it entity.DesignJoinItem) entity.DesignJoinItem {
	norm := func(s []string) []string {
		if len(s) == 0 {
			return nil
		}
		return s
	}
	it.Via, it.BoundedBy, it.ContinuesInto, it.CaughtInto, it.Sharp = norm(it.Via), norm(it.BoundedBy), norm(it.ContinuesInto), norm(it.CaughtInto), norm(it.Sharp)
	return it
}

func designJoinsDocFromPb(in *pb_common.DesignJoins) entity.DesignJoinsDoc {
	var d entity.DesignJoinsDoc
	for _, l := range in.GetLayers() {
		d.Layers = append(d.Layers, entity.DesignJoinLayer{Index: int(l.GetIndex()), Name: l.GetName(), Sheer: l.GetSheer(), Note: l.GetNote(), Face: l.GetFace()})
	}
	for i, it := range in.GetItems() {
		if i == entity.DesignJoinsMaxItems {
			break
		}
		d.Items = append(d.Items, entity.DesignJoinItem{
			ID: it.GetId(), Kind: it.GetKind(), From: it.GetFrom(), To: it.GetTo(), Via: it.GetVia(),
			View: it.GetView(), Side: it.GetSide(), Text: it.GetText(), Width: it.GetWidth(),
			Closed: it.GetClosed(), Type: it.GetType(), Count: int(it.GetCount()), BoundedBy: it.GetBoundedBy(),
			ContinuesInto: it.GetContinuesInto(), Layer: int(it.GetLayer()), Visibility: it.GetVisibility(),
			CaughtInto: it.GetCaughtInto(), FreeEdge: it.GetFreeEdge(), Sharp: it.GetSharp(), Size: it.GetSize(),
		})
	}
	d.Absences = append(d.Absences, in.GetAbsences()...)
	d.Uncertain = append(d.Uncertain, in.GetUncertain()...)
	if f := in.GetFit(); f != nil {
		d.Fit = &entity.DesignJoinsFit{Ease: f.GetEase(), Waist: f.GetWaist()}
	}
	return d
}

// ─── the door of a flat run ───

// designRunJoins — the join list a flat run freezes into its snapshot (`inputs.joins`): a rerun
// carries its parent's copy; a new garment flat reads the card's current list off the band the door
// already holds (GetBand reads it in the same snapshot as the bench); anything else none.
func designRunJoins(kind string, params *pb_common.DesignRunParams, band *entity.DesignBand, parent *entity.DesignRun) *entity.DesignJoinsDoc {
	// hand_flat redraws the designer's flats: no list (81-FINAL-MODES).
	if mode, _ := designFlatModeOf(params); mode == designgen.FlatModeHandFlat {
		return nil
	}
	if kind != entity.DesignRunKindFlat || !designgen.FlatIsGarmentSheet(params.GetViews(), designLayoutOne) {
		return nil
	}
	if parent != nil {
		return designParentJoins(parent)
	}
	if band == nil || band.Joins == nil {
		return nil
	}
	j := band.Joins
	if len(j.Doc.Items) == 0 && len(j.Doc.Absences) == 0 {
		return nil
	}
	doc := j.Doc
	return &doc
}

// designParentJoins — the parent's frozen list, raw (protojson parsing of the snapshot drops it).
func designParentJoins(parent *entity.DesignRun) *entity.DesignJoinsDoc {
	if parent == nil || len(parent.Inputs) == 0 {
		return nil
	}
	var in struct {
		Joins *entity.DesignJoinsDoc `json:"joins"`
	}
	if err := json.Unmarshal(parent.Inputs, &in); err != nil {
		return nil
	}
	return in.Joins
}

// designFreezeFlatModel — a NEW flat press that names no engine is drawn by
// designgen.FlatDefaultEngine, frozen into params.image.model so the run says so for ever. Only when
// this deployment's engine table lists the row (a custom default empties the table: then nothing is
// frozen and the run keeps the deployment's own slug, as before).
//
// A RERUN keeps what its parent effectively used: the parent's frozen model when it had one, else
// nothing (the deployment default it was drawn by) — a flat from before the flat route is never
// silently redrawn by another model. A FIX keeps today's behaviour too.
func (s *Server) designFreezeFlatModel(kind string, params *pb_common.DesignRunParams, parent *entity.DesignRun) {
	if kind != entity.DesignRunKindFlat || params == nil || strings.TrimSpace(params.GetImage().GetModel()) != "" {
		return
	}
	if parent != nil {
		var pp struct {
			Image struct {
				Model string `json:"model"`
			} `json:"image"`
		}
		if len(parent.Params) > 0 && json.Unmarshal(parent.Params, &pp) == nil && strings.TrimSpace(pp.Image.Model) != "" {
			if params.Image == nil {
				params.Image = &pb_common.DesignImageOptions{}
			}
			params.Image.Model = strings.TrimSpace(pp.Image.Model)
		}
		return
	}
	if designFlatIsFix(params) {
		return
	}
	mode, _ := designFlatModeOf(params)
	slug := designgen.FlatModelFor(mode)
	if _, ok := designgen.FindEngine(s.designEngineTable(), slug); !ok {
		return
	}
	if params.Image == nil {
		params.Image = &pb_common.DesignImageOptions{}
	}
	params.Image.Model = slug
}

// designFlatIsFix — a flat that corrects named views (fix_target(s) / fix_slot_ids): one picture.
func designFlatIsFix(params *pb_common.DesignRunParams) bool {
	return len(params.GetFixTargets()) > 0 || strings.TrimSpace(params.GetFixTarget()) != "" || len(params.GetFixSlotIds()) > 0
}
