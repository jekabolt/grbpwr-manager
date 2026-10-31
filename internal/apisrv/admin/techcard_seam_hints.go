package admin

import (
	"context"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/aiprov"
	"github.com/jekabolt/grbpwr-manager/internal/apisrv/apierr"
	authsrv "github.com/jekabolt/grbpwr-manager/internal/apisrv/auth"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	hints "github.com/jekabolt/grbpwr-manager/internal/techcardseamhints"
	pb_admin "github.com/jekabolt/grbpwr-manager/proto/gen/admin"
	"golang.org/x/sync/singleflight"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// ─────────────── SEAM HINTS · AI ONCE PER TECH CARD (unit-seams line F, SPEC-F §2–4) ───────────────
//
// SuggestTechCardSeamHints asks chat.techcard_seams ONCE per card input for a HINT: a role per piece
// and a joins map per join step. AI IS A HINT ONLY: nothing here writes a seam; the client shows the
// hint, the owner fixes it, and the fix (an override) survives every rerun.
//
// PRECEDENCE (the client applies it, the server only stores and tells the model):
//
//	owner fix for this card (override ok/fixed) > owner's code dictionary row > AI > static default.
//
// Doors, in order, all before money (the assembly_skeleton_ai.go / pattern_pieces.go recipe): the
// raw size of input_json → parse + bounds → the sketches are on the card's technical sheet and are
// pictures → the prompt's byte ceiling → the purpose is callable → the PERSISTENT cache by digest
// (tech_card_seam_hint_run; replaces the hour cache; force skips it, but a second force of the same
// digest within seamHintsForceCooldown returns the stored run) → one flight per (admin, digest) →
// [in the flight] the store again → the shared semaphore → ONE slot of the hourly window shared with
// EnhanceText and the labellers → provider (temperature 0, JSON mode). Tokens and cost are booked by
// the router into the AI ledger and summed into the answer.

const (
	seamHintsMaxPromptBytes = 128 << 10
	seamHintsMaxTokens      = 8000
	seamHintsEffort         = "medium"
	seamHintsAttempts       = 2
	seamHintsForceCooldown  = time.Minute

	seamHintsNotConfiguredMsg = "the AI seam hints are not configured: " + openRouterNoKeyMsg
	seamHintsModelUnavailMsg  = "the AI seam hints are misconfigured: " + modelUnavailableAdviceMsg
)

// seamHintsForceGuard remembers the last forced rerun per (admin, card, digest): a double press of
// «rerun» pays once. Zero value works.
type seamHintsForceGuard struct {
	mu   sync.Mutex
	last map[string]time.Time
}

func (g *seamHintsForceGuard) allow(key string, now time.Time) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.last == nil {
		g.last = map[string]time.Time{}
	}
	for k, t := range g.last {
		if now.Sub(t) >= seamHintsForceCooldown {
			delete(g.last, k)
		}
	}
	if t, ok := g.last[key]; ok && now.Sub(t) < seamHintsForceCooldown {
		return false
	}
	g.last[key] = now
	return true
}

// seamHintsPrep is a card input made ready: parsed, its sketches checked and fingerprinted, the
// matching dictionary rows, the digest.
type seamHintsPrep struct {
	in       hints.Input
	sketches []hints.MediaFact
	urls     []string // the sketches' file URLs, in sketch order
	dict     []hints.DictRow
	digest   string
}

// seamHintsInputStatus maps a refused input to InvalidArgument with the field named.
func seamHintsInputStatus(err error) error {
	var ie *hints.InputError
	if errors.As(err, &ie) {
		if st, ok := apierr.Status(entity.NewFieldViolation(ie.Field, ie.Reason, "", "")); ok {
			return st
		}
		return status.Error(codes.InvalidArgument, ie.Error())
	}
	return status.Error(codes.InvalidArgument, err.Error())
}

// seamHintsPrepare parses and checks an input and computes its digest. No provider call.
func (s *Server) seamHintsPrepare(ctx context.Context, cardID int, raw string) (seamHintsPrep, error) {
	var p seamHintsPrep
	if len(raw) > hints.MaxInputBytes { // the raw size, before any parse
		return p, seamHintsInputStatus(&hints.InputError{Field: "input_json",
			Reason: fmt.Sprintf("at most %d bytes, got %d", hints.MaxInputBytes, len(raw))})
	}
	in, err := hints.ParseInput([]byte(raw))
	if err != nil {
		return p, seamHintsInputStatus(err)
	}
	p.in = in

	// ⚠ ONLY THIS CARD'S TECHNICAL SHEET. A global media id would let a tech_cards:write account hand
	// any library file to an external model (the reason SuggestAssemblySkeleton lost its media_ids).
	owned, err := s.repo.TechCards().TechCardTechnicalMediaIDs(ctx, cardID, in.SketchMediaIDs)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return p, status.Error(codes.NotFound, "tech card not found")
		}
		slog.Default().ErrorContext(ctx, "seam hints: cannot read the card's technical media",
			slog.Int("tech_card_id", cardID), slog.String("err", err.Error()))
		return p, status.Error(codes.Internal, "cannot read the card's pictures")
	}
	ownedSet := make(map[int]bool, len(owned))
	for _, id := range owned {
		ownedSet[id] = true
	}
	for i, id := range in.SketchMediaIDs {
		if !ownedSet[id] {
			return p, seamHintsInputStatus(&hints.InputError{Field: fmt.Sprintf("sketch_media_ids[%d]", i),
				Reason: fmt.Sprintf("media %d is not on this card's technical sheet", id)})
		}
	}
	if len(in.SketchMediaIDs) > 0 {
		byID, err := s.repo.Media().GetMediaByIds(ctx, in.SketchMediaIDs)
		if err != nil {
			slog.Default().ErrorContext(ctx, "seam hints: cannot read the sketches",
				slog.Int("tech_card_id", cardID), slog.String("err", err.Error()))
			return p, status.Error(codes.Internal, "cannot read the card's pictures")
		}
		refs := make([]designInputMediaRef, 0, len(in.SketchMediaIDs))
		for _, id := range in.SketchMediaIDs {
			m, ok := byID[id]
			u := strings.TrimSpace(m.FullSizeMediaURL)
			if !ok || u == "" {
				return p, status.Errorf(codes.FailedPrecondition, "sketch media %d has no file", id)
			}
			h := "url:" + u // no stored hash (rows before 0336): the URL stands in
			if m.ContentHash.Valid && m.ContentHash.String != "" {
				h = m.ContentHash.String
			}
			p.sketches = append(p.sketches, hints.MediaFact{ID: id, Hash: h})
			p.urls = append(p.urls, u)
			refs = append(refs, designInputMediaRef{ID: id, URL: u, Where: "the seam hints' sketches"})
		}
		if ref, ct, bad := designFirstNonPictureInput(refs); bad {
			return p, designNonPictureRefusal(ref, ct)
		}
	}

	rows, err := s.repo.TechCards().ListPieceCodeDictionary(ctx)
	if err != nil {
		slog.Default().ErrorContext(ctx, "seam hints: cannot read the code dictionary", slog.String("err", err.Error()))
		return p, status.Error(codes.Internal, "cannot read the code dictionary")
	}
	all := make([]hints.DictRow, 0, len(rows))
	for _, r := range rows {
		all = append(all, hints.DictRow{Code: r.Code, Role: r.Role, Hand: r.Hand.String})
	}
	p.dict = hints.MatchDictionary(in, all)
	p.digest = hints.Digest(in, p.sketches, p.dict)
	return p, nil
}

// seamHintsPromptOverrides — the owner's facts for the prompt: ok / fixed with a value.
func seamHintsPromptOverrides(os []entity.TechCardSeamHintOverride) []hints.Override {
	var out []hints.Override
	for _, o := range os {
		if (o.State == "ok" || o.State == "fixed") && o.Value.Valid && o.Value.String != "" {
			out = append(out, hints.Override{Kind: o.Kind, Key: o.HintKey, Value: o.Value.String, State: o.State})
		}
	}
	return out
}

func seamHintsRunResponse(run entity.TechCardSeamHintRun, cached bool) *pb_admin.SuggestTechCardSeamHintsResponse {
	return &pb_admin.SuggestTechCardSeamHintsResponse{
		HintsJson: run.Hints, InputDigest: run.InputDigest, Model: run.Model,
		CreatedAt: timestamppb.New(run.CreatedAt), Cached: cached,
	}
}

// SuggestTechCardSeamHints gives the AI hint of a card input (stored per digest).
func (s *Server) SuggestTechCardSeamHints(ctx context.Context, req *pb_admin.SuggestTechCardSeamHintsRequest) (*pb_admin.SuggestTechCardSeamHintsResponse, error) {
	cardID := int(req.GetTechCardId())
	if cardID <= 0 {
		return nil, status.Error(codes.InvalidArgument, "tech_card_id is required")
	}
	p, err := s.seamHintsPrepare(ctx, cardID, req.GetInputJson())
	if err != nil {
		return nil, err
	}
	overrides, err := s.repo.TechCards().ListTechCardSeamHintOverrides(ctx, cardID)
	if err != nil {
		slog.Default().ErrorContext(ctx, "seam hints: cannot read the overrides",
			slog.Int("tech_card_id", cardID), slog.String("err", err.Error()))
		return nil, status.Error(codes.Internal, "cannot read the seam hint overrides")
	}
	atlas := p.in.AtlasDataURL()
	user := hints.UserPrompt(p.in, p.dict, seamHintsPromptOverrides(overrides), len(p.urls), atlas != "")
	if len(user) > seamHintsMaxPromptBytes {
		return nil, status.Errorf(codes.InvalidArgument,
			"the card is too large for one call: %d bytes of text, at most %d", len(user), seamHintsMaxPromptBytes)
	}
	const purpose = entity.AIPurposeTechCardSeams
	if !s.ai.Enabled(purpose) {
		return nil, s.aiOffRefusal(purpose, seamHintsNotConfiguredMsg)
	}

	admin := authsrv.GetAdminUsername(ctx)
	force := req.GetForce()
	if force && !s.seamHintsForced.allow(admin+"\x00"+strconv.Itoa(cardID)+"\x00"+p.digest, time.Now()) {
		force = false // a second «rerun» within the cooldown reads the stored run
	}
	if !force {
		run, ok, err := s.repo.TechCards().GetTechCardSeamHintRun(ctx, cardID, p.digest, hints.SchemaVersion)
		if err != nil {
			slog.Default().ErrorContext(ctx, "seam hints: cannot read the stored run",
				slog.Int("tech_card_id", cardID), slog.String("err", err.Error()))
			return nil, status.Error(codes.Internal, "cannot read the stored seam hints")
		}
		if ok {
			return seamHintsRunResponse(run, true), nil
		}
	}

	images := append(append([]string{}, p.urls...), nonEmpty(atlas)...)
	job := seamHintsJob{cardID: cardID, admin: admin, prep: p, user: user, images: images, force: force}
	key := admin + "\x00" + p.digest + "\x00" + strconv.FormatBool(force)
	ch := s.seamHintsFlight.DoChan(key, func() (any, error) {
		budget := s.ai.ChainBudget(purpose, seamHintsMaxTokens)
		fctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), seamHintsAttempts*budget+designPartsFlightMargin)
		defer cancel()
		return s.seamHintsCall(fctx, job, budget)
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
	return proto.Clone(res.Val.(*pb_admin.SuggestTechCardSeamHintsResponse)).(*pb_admin.SuggestTechCardSeamHintsResponse), nil
}

func nonEmpty(s string) []string {
	if s == "" {
		return nil
	}
	return []string{s}
}

type seamHintsJob struct {
	cardID int
	admin  string
	prep   seamHintsPrep
	user   string
	images []string
	force  bool
}

// seamHintsCall — the flight leader's work: the store once more (unless force), the fences, the call
// (an unparseable answer is asked once more), validation, the run stored.
func (s *Server) seamHintsCall(ctx context.Context, job seamHintsJob, budget time.Duration) (*pb_admin.SuggestTechCardSeamHintsResponse, error) {
	const purpose = entity.AIPurposeTechCardSeams
	if !job.force {
		if run, ok, err := s.repo.TechCards().GetTechCardSeamHintRun(ctx, job.cardID, job.prep.digest, hints.SchemaVersion); err == nil && ok {
			return seamHintsRunResponse(run, true), nil
		}
	}
	select {
	case s.enhanceSem <- struct{}{}:
		defer func() { <-s.enhanceSem }()
	default:
		return nil, status.Error(codes.ResourceExhausted, "the assistant is busy right now — try again in a moment")
	}
	if !s.enhanceRuns.allow(job.admin) {
		return nil, status.Errorf(codes.ResourceExhausted,
			"this account has used the assistant %d times in the last hour (the AI buttons share the limit); every call spends the AI key — try again later",
			enhancePerAdminCalls)
	}

	tally := &aiprov.CallTally{}
	ctx = aiprov.WithCallTally(ctx, tally)
	zero := 0.0
	var (
		out      hints.Hints
		answered string
	)
	err := designPartsRetryUnusable(ctx, budget, func(actx context.Context, attempt int) error {
		started := time.Now()
		res, err := s.ai.Chat(actx, purpose, aiprov.ChatRequest{
			System: hints.SystemPrompt, User: job.user, ImageURLs: job.images, UserAsParts: len(job.images) > 0,
			JSONMode: true, MaxTokens: seamHintsMaxTokens, Effort: seamHintsEffort, Temperature: &zero,
		})
		var (
			raw, finishReason string
			usage             aiprov.TokenUsage
		)
		if res != nil {
			raw, finishReason, usage = res.Text, res.FinishReason, res.Usage
		}
		answered = s.aiModelOf(purpose, res)
		logAttrs := []any{
			slog.Int("tech_card_id", job.cardID), slog.Int("pieces", len(job.prep.in.Pieces)),
			slog.Int("ops", len(job.prep.in.Ops)), slog.Int("images", len(job.images)),
			slog.String("model", answered), slog.Int("attempt", attempt),
			slog.Duration("took", time.Since(started)), slog.String("finish_reason", enhanceLogFinishReason(finishReason)),
			slog.Int("prompt_tokens", usage.Prompt), slog.Int("completion_tokens", usage.Completion),
		}
		if err != nil {
			return s.seamHintsChatFailure(ctx, res, err, logAttrs)
		}
		h, rep, verr := hints.Validate(raw, job.prep.in)
		if verr != nil {
			slog.Default().ErrorContext(ctx, "seam hints: the answer is not the promised JSON", logAttrs...)
			return status.Error(codes.Internal, designPartsUnusableMsg)
		}
		out = h
		slog.Default().InfoContext(ctx, "seam hints", append(logAttrs,
			slog.Int("roles", len(h.Roles)), slog.Int("joins", len(h.Joins)),
			slog.Int("not_inferred", len(h.NotInferred)), slog.Int("dropped", len(rep.Dropped)))...)
		return nil
	})
	spend := skeletonAISpendOf(tally.Calls())
	if err != nil {
		return nil, spend.attachTo(err)
	}
	run, err := s.repo.TechCards().SaveTechCardSeamHintRun(ctx, entity.TechCardSeamHintRun{
		TechCardId: job.cardID, InputDigest: job.prep.digest, SchemaVersion: hints.SchemaVersion,
		Model: answered, Hints: out.Marshal(), CreatedBy: job.admin,
	})
	if err != nil {
		if s.repo.IsErrForeignKeyViolation(err) {
			return nil, spend.attachTo(status.Error(codes.NotFound, "tech card not found"))
		}
		slog.Default().ErrorContext(ctx, "seam hints: cannot store the run",
			slog.Int("tech_card_id", job.cardID), slog.String("err", err.Error()))
		return nil, spend.attachTo(status.Error(codes.Internal, "cannot store the seam hints"))
	}
	resp := seamHintsRunResponse(run, false)
	resp.CostUsd, resp.Calls = spend.costString(), int32(spend.calls)
	return resp, nil
}

// seamHintsChatFailure maps a failed chat.techcard_seams call to its refusal (and logs it).
func (s *Server) seamHintsChatFailure(ctx context.Context, res *aiprov.ChatResult, err error, logAttrs []any) error {
	const purpose = entity.AIPurposeTechCardSeams
	class := enhanceErrClass(err) // NEVER err.Error(): the provider may echo the request
	if refusal, ok := aiUncalledRefusal(err, seamHintsNotConfiguredMsg); ok {
		return refusal
	}
	if class == enhanceErrNotConfigured {
		return aiRefusal(aiReasonNotConfigured, seamHintsNotConfiguredMsg, nil)
	}
	provider := s.aiProviderOf(purpose, res)
	failAttrs := append(logAttrs, slog.String("err_class", class),
		slog.Bool("provider_engaged", aiprov.Engaged(err)),
		slog.String("provider", provider), slog.String("base_url", s.ai.BaseURL(provider)))
	if class == enhanceErrProviderHTTP {
		failAttrs = append(failAttrs, slog.Int("http_status", providerHTTPStatus(err)))
	}
	slog.Default().ErrorContext(ctx, "seam hints failed", failAttrs...)
	switch class {
	case enhanceErrModelUnavailable:
		return aiModelRefusal(seamHintsModelUnavailMsg, s.ai.PrimaryModel(purpose))
	case enhanceErrBudgetExhausted, enhanceErrEmptyAnswer:
		return status.Error(codes.Internal, designPartsUnusableMsg)
	}
	return status.Error(codes.Unavailable, "the assistant is unavailable right now — try again in a moment")
}

func seamHintOverridesToPb(os []entity.TechCardSeamHintOverride) []*pb_admin.TechCardSeamHintOverride {
	out := make([]*pb_admin.TechCardSeamHintOverride, 0, len(os))
	for _, o := range os {
		out = append(out, &pb_admin.TechCardSeamHintOverride{
			Kind: o.Kind, HintKey: o.HintKey, ValueJson: o.Value.String, State: o.State,
			UpdatedAt: timestamppb.New(o.UpdatedAt), UpdatedBy: o.UpdatedBy,
		})
	}
	return out
}

func isDigestHex(d string) bool {
	if len(d) != 64 {
		return false
	}
	_, err := hex.DecodeString(d)
	return err == nil && strings.ToLower(d) == d
}

// GetTechCardSeamHints reads the stored hint and the overrides; never calls the model.
func (s *Server) GetTechCardSeamHints(ctx context.Context, req *pb_admin.GetTechCardSeamHintsRequest) (*pb_admin.GetTechCardSeamHintsResponse, error) {
	cardID := int(req.GetTechCardId())
	if cardID <= 0 {
		return nil, status.Error(codes.InvalidArgument, "tech_card_id is required")
	}
	digest := strings.TrimSpace(req.GetInputDigest())
	switch {
	case digest != "":
		if !isDigestHex(digest) {
			return nil, seamHintsInputStatus(&hints.InputError{Field: "input_digest", Reason: "64 lower-case hex characters"})
		}
	case req.GetInputJson() != "":
		p, err := s.seamHintsPrepare(ctx, cardID, req.GetInputJson())
		if err != nil {
			return nil, err
		}
		digest = p.digest
	}
	tc := s.repo.TechCards()
	resp := &pb_admin.GetTechCardSeamHintsResponse{InputDigest: digest}
	var (
		run entity.TechCardSeamHintRun
		ok  bool
		err error
	)
	if digest != "" {
		run, ok, err = tc.GetTechCardSeamHintRun(ctx, cardID, digest, hints.SchemaVersion)
	}
	if err == nil && !ok {
		run, ok, err = tc.LatestTechCardSeamHintRun(ctx, cardID, hints.SchemaVersion)
		resp.Stale = ok && digest != ""
	}
	if err != nil {
		slog.Default().ErrorContext(ctx, "seam hints: cannot read the stored run",
			slog.Int("tech_card_id", cardID), slog.String("err", err.Error()))
		return nil, status.Error(codes.Internal, "cannot read the stored seam hints")
	}
	if ok {
		resp.HintsJson, resp.RunDigest, resp.Model = run.Hints, run.InputDigest, run.Model
		resp.CreatedAt = timestamppb.New(run.CreatedAt)
	}
	overrides, err := tc.ListTechCardSeamHintOverrides(ctx, cardID)
	if err != nil {
		slog.Default().ErrorContext(ctx, "seam hints: cannot read the overrides",
			slog.Int("tech_card_id", cardID), slog.String("err", err.Error()))
		return nil, status.Error(codes.Internal, "cannot read the seam hint overrides")
	}
	resp.Overrides = seamHintOverridesToPb(overrides)
	return resp, nil
}

// SetTechCardSeamHintOverride writes (or, with an empty state, deletes) the owner's word on one hint.
// It is never touched by a run, so it survives every rerun.
func (s *Server) SetTechCardSeamHintOverride(ctx context.Context, req *pb_admin.SetTechCardSeamHintOverrideRequest) (*pb_admin.SetTechCardSeamHintOverrideResponse, error) {
	cardID := int(req.GetTechCardId())
	if cardID <= 0 {
		return nil, status.Error(codes.InvalidArgument, "tech_card_id is required")
	}
	o := entity.TechCardSeamHintOverride{TechCardId: cardID, Kind: strings.TrimSpace(req.GetKind()),
		State: strings.TrimSpace(req.GetState()), UpdatedBy: authsrv.GetAdminUsername(ctx)}
	if o.State == "" { // delete: only kind and key must be right
		key, _, err := hints.NormalizeOverride(o.Kind, req.GetHintKey(), "", "off")
		if err != nil {
			return nil, seamHintsInputStatus(err)
		}
		o.HintKey = key
	} else {
		key, value, err := hints.NormalizeOverride(o.Kind, req.GetHintKey(), req.GetValueJson(), o.State)
		if err != nil {
			return nil, seamHintsInputStatus(err)
		}
		o.HintKey = key
		o.Value = sql.NullString{String: value, Valid: value != ""}
	}
	list, err := s.repo.TechCards().SetTechCardSeamHintOverride(ctx, o)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) || s.repo.IsErrForeignKeyViolation(err) {
			return nil, status.Error(codes.NotFound, "tech card not found")
		}
		slog.Default().ErrorContext(ctx, "seam hints: cannot save an override",
			slog.Int("tech_card_id", cardID), slog.String("err", err.Error()))
		return nil, status.Error(codes.Internal, "cannot save the seam hint override")
	}
	return &pb_admin.SetTechCardSeamHintOverrideResponse{Overrides: seamHintOverridesToPb(list)}, nil
}

func pieceCodeEntryToPb(e entity.PieceCodeDictionaryEntry) *pb_admin.PieceCodeDictionaryEntry {
	return &pb_admin.PieceCodeDictionaryEntry{Code: e.Code, Role: e.Role, Hand: e.Hand.String,
		UpdatedAt: timestamppb.New(e.UpdatedAt), UpdatedBy: e.UpdatedBy}
}

// ListPieceCodeDictionary lists the owner's code dictionary («все карты»).
func (s *Server) ListPieceCodeDictionary(ctx context.Context, _ *pb_admin.ListPieceCodeDictionaryRequest) (*pb_admin.ListPieceCodeDictionaryResponse, error) {
	rows, err := s.repo.TechCards().ListPieceCodeDictionary(ctx)
	if err != nil {
		slog.Default().ErrorContext(ctx, "cannot list the piece code dictionary", slog.String("err", err.Error()))
		return nil, status.Error(codes.Internal, "cannot read the code dictionary")
	}
	out := &pb_admin.ListPieceCodeDictionaryResponse{Entries: make([]*pb_admin.PieceCodeDictionaryEntry, 0, len(rows))}
	for _, r := range rows {
		out.Entries = append(out.Entries, pieceCodeEntryToPb(r))
	}
	return out, nil
}

// SetPieceCodeDictionaryEntry upserts one dictionary row, or deletes it when role is empty. Never
// seeds FLP or anything else by itself: rows come only from the owner (or the client's one-time seed).
func (s *Server) SetPieceCodeDictionaryEntry(ctx context.Context, req *pb_admin.SetPieceCodeDictionaryEntryRequest) (*pb_admin.SetPieceCodeDictionaryEntryResponse, error) {
	code, ok := hints.NormalizeCode(req.GetCode())
	if !ok {
		return nil, seamHintsInputStatus(&hints.InputError{Field: "code", Reason: "1..32 of A-Z 0-9 _ -"})
	}
	role := strings.ToLower(strings.TrimSpace(req.GetRole()))
	hand := strings.ToUpper(strings.TrimSpace(req.GetHand()))
	if role != "" && !hints.Roles[role] {
		return nil, seamHintsInputStatus(&hints.InputError{Field: "role",
			Reason: "one of " + strings.Join(hints.SortedKeys(hints.Roles), ", ") + " (empty deletes)"})
	}
	if !hints.Hands[hand] {
		return nil, seamHintsInputStatus(&hints.InputError{Field: "hand", Reason: "L, R or empty"})
	}
	e, found, err := s.repo.TechCards().SetPieceCodeDictionaryEntry(ctx, entity.PieceCodeDictionaryEntry{
		Code: code, Role: role, Hand: sql.NullString{String: hand, Valid: hand != ""},
		UpdatedBy: authsrv.GetAdminUsername(ctx),
	})
	if err != nil {
		slog.Default().ErrorContext(ctx, "cannot save a piece code", slog.String("code", code), slog.String("err", err.Error()))
		return nil, status.Error(codes.Internal, "cannot save the code dictionary")
	}
	if !found {
		return &pb_admin.SetPieceCodeDictionaryEntryResponse{Deleted: true}, nil
	}
	return &pb_admin.SetPieceCodeDictionaryEntryResponse{Entry: pieceCodeEntryToPb(e)}, nil
}
