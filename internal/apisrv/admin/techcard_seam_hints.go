package admin

import (
	"context"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sort"
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

// ─────────────── SEAM HINTS · AI BY ITSELF, INCREMENTALLY (unit-seams line F, SPEC-F §2–5a) ───────────────
//
// SuggestTechCardSeamHints asks chat.techcard_seams for a HINT: a role per piece and a joins map per
// join step. AI IS A HINT ONLY: nothing here writes a seam; the client shows the hint, the owner fixes
// it, and the fix (an override) survives every rerun.
//
// PRECEDENCE (the client applies it, the server only stores and tells the model):
//
//	owner fix for this card (override ok/fixed) > owner's code dictionary row > AI > static default.
//
// THE SPLIT CACHE (§5a, owner «да» 31.10): the client fires Suggest by itself (DXF upload, operations
// save, archive import, the first open of a step with no run for the current fingerprint). The roles
// are stored per PIECES digest (tech_card_seam_hint_run, store version 2), the joins per (op number,
// OP digest = op fields + its input pieces' summaries + the roles digest) in tech_card_seam_hint_join.
// A non-forced Suggest asks the model only for what the store lacks — the roles when the pieces digest
// has no run, the joins of the ops with no row for their current digest — with the pieces, the known
// roles and the other ops' joins as context; when nothing is missing it answers from the store with no
// call and no purpose check. «rerun» = force = the roles and every op.
//
// Doors, in order, all before money (the assembly_skeleton_ai.go / pattern_pieces.go recipe): the
// raw size of input_json → parse + bounds → the sketches are on the card's technical sheet and are
// pictures → the store (nothing missing → the merged hint, no call) → the purpose is callable → one
// flight per (admin, full digest, force) → [in the flight] the store again → the prompt's byte ceiling
// → the shared semaphore → ONE slot of the hourly window shared with EnhanceText and the labellers →
// provider (temperature 0, JSON mode). A second force of the same input within seamHintsForceCooldown
// reads the store. Tokens and cost are booked by the router into the AI ledger and summed into the
// answer.

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
// matching dictionary rows, the digests.
type seamHintsPrep struct {
	in       hints.Input
	sketches []hints.MediaFact
	urls     []string // the sketches' file URLs, in sketch order
	dict     []hints.DictRow
	dg       hints.Digests
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
	p.dg = hints.ComputeDigests(in, p.sketches, p.dict)
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

func seamHintsLogStore(ctx context.Context, what string, cardID int, err error) error {
	slog.Default().ErrorContext(ctx, "seam hints: cannot "+what,
		slog.Int("tech_card_id", cardID), slog.String("err", err.Error()))
	return status.Error(codes.Internal, "cannot "+what)
}

// seamHintsState is what the store holds for one card input: the roles run of the current pieces
// digest (or the latest other one), and every join row of the card, newest first per op.
type seamHintsState struct {
	roles       entity.TechCardSeamHintRun
	rolesFound  bool // a roles run of the CURRENT pieces digest
	latest      entity.TechCardSeamHintRun
	latestFound bool // the newest roles run of any digest
	joins       map[int][]entity.TechCardSeamHintJoin
}

func (s *Server) seamHintsLoad(ctx context.Context, cardID int, rolesDigest string) (seamHintsState, error) {
	tc := s.repo.TechCards()
	var (
		st  seamHintsState
		err error
	)
	if rolesDigest != "" {
		st.roles, st.rolesFound, err = tc.GetTechCardSeamHintRun(ctx, cardID, rolesDigest, hints.StoreVersion)
		if err != nil {
			return st, seamHintsLogStore(ctx, "read the stored seam hints", cardID, err)
		}
	}
	if !st.rolesFound {
		st.latest, st.latestFound, err = tc.LatestTechCardSeamHintRun(ctx, cardID, hints.StoreVersion)
		if err != nil {
			return st, seamHintsLogStore(ctx, "read the stored seam hints", cardID, err)
		}
	}
	rows, err := tc.ListTechCardSeamHintJoins(ctx, cardID, hints.StoreVersion)
	if err != nil {
		return st, seamHintsLogStore(ctx, "read the stored seam hints", cardID, err)
	}
	st.joins = map[int][]entity.TechCardSeamHintJoin{}
	for _, r := range rows { // ordered by op, newest first
		st.joins[r.OpNumber] = append(st.joins[r.OpNumber], r)
	}
	return st, nil
}

// current returns the op's row of exactly digest.
func (st seamHintsState) current(op int, digest string) (entity.TechCardSeamHintJoin, bool) {
	for _, r := range st.joins[op] {
		if r.OpDigest == digest {
			return r, true
		}
	}
	return entity.TechCardSeamHintJoin{}, false
}

func parseStoredHints(run entity.TechCardSeamHintRun) hints.Hints {
	var h hints.Hints
	_ = json.Unmarshal([]byte(run.Hints), &h) // written by Hints.Marshal; a bad row reads as empty
	return h
}

func parseStoredJoin(r entity.TechCardSeamHintJoin) (hints.Join, bool) {
	if r.Outcome != entity.SeamHintJoinOutcomeJoin || !r.Hint.Valid {
		return hints.Join{}, false
	}
	var j hints.Join
	if json.Unmarshal([]byte(r.Hint.String), &j) != nil {
		return hints.Join{}, false
	}
	return j, true
}

// seamHintsMerged is the hint shown for one card input: the roles run + one join row per op.
type seamHintsMerged struct {
	h          hints.Hints
	rolesStale bool
	staleOps   []int32
	missingOps []int32
	model      string
	modelAt    time.Time
	createdAt  time.Time
	empty      bool
}

// saw notes one stored part shown: the newest part's time, the newest ANSWERED part's model (a row
// stored as none without asking has no model).
func (m *seamHintsMerged) saw(model string, at time.Time) {
	m.empty = false
	if at.After(m.createdAt) {
		m.createdAt = at
	}
	if model != "" && !at.Before(m.modelAt) {
		m.model, m.modelAt = model, at
	}
}

func (m seamHintsMerged) stale() bool {
	return m.rolesStale || len(m.staleOps) > 0 || len(m.missingOps) > 0
}

// seamHintsMerge builds the §4 hint of the CURRENT input from the store: the roles of the current
// pieces digest (else the latest run's, stale), and per op its row of the current op digest (else
// its newest row, stale, a join shown only while it still fits the op's pieces; else missing).
// Roles and pieces that left the input are not shown.
func seamHintsMerge(p seamHintsPrep, st seamHintsState) seamHintsMerged {
	m := seamHintsMerged{empty: true, h: hints.Hints{SchemaVersion: hints.SchemaVersion,
		Roles: []hints.Role{}, Joins: []hints.Join{}, NotInferred: []string{}}}
	isPiece := make(map[string]bool, len(p.in.Pieces))
	for _, pc := range p.in.Pieces {
		isPiece[pc.Name] = true
	}
	run, ok := st.roles, st.rolesFound
	if !ok && st.latestFound {
		run, ok, m.rolesStale = st.latest, true, true
	}
	var pieceNI []string
	if ok {
		m.saw(run.Model, run.CreatedAt)
		rh := parseStoredHints(run)
		for _, r := range rh.Roles {
			if isPiece[r.Piece] {
				m.h.Roles = append(m.h.Roles, r)
			}
		}
		for _, k := range rh.NotInferred {
			if strings.HasPrefix(k, "piece:") && isPiece[strings.TrimPrefix(k, "piece:")] {
				pieceNI = append(pieceNI, k)
			}
		}
	}
	var opNI []int
	for _, o := range p.in.Ops {
		r, cur := st.current(o.Number, p.dg.Ops[o.Number])
		if !cur {
			if len(st.joins[o.Number]) == 0 {
				m.missingOps = append(m.missingOps, int32(o.Number))
				continue
			}
			r = st.joins[o.Number][0]
			m.staleOps = append(m.staleOps, int32(o.Number))
		}
		m.saw(r.Model, r.CreatedAt)
		switch r.Outcome {
		case entity.SeamHintJoinOutcomeJoin:
			if j, ok := parseStoredJoin(r); ok && j.Op == o.Number && hints.JoinFits(p.in, j) {
				m.h.Joins = append(m.h.Joins, j)
			}
		case entity.SeamHintJoinOutcomeNotInferred:
			opNI = append(opNI, o.Number)
		}
	}
	sort.Slice(m.h.Joins, func(i, j int) bool { return m.h.Joins[i].Op < m.h.Joins[j].Op })
	sort.Ints(opNI)
	for _, n := range opNI {
		m.h.NotInferred = append(m.h.NotInferred, "op:"+strconv.Itoa(n))
	}
	sort.Strings(pieceNI)
	m.h.NotInferred = append(m.h.NotInferred, pieceNI...)
	return m
}

// seamHintsNeeds is the incremental task (SPEC-F §5a): the roles when the pieces digest has no run,
// the joins of the ops with no row for their current digest; force asks everything. An op whose
// inputs stand for fewer than two pieces cannot hold a join: it is stored as none, never asked.
type seamHintsNeeds struct {
	roles    bool
	ops      []int // asked, in card order
	autoNone []int // stored as none without asking
}

func (n seamHintsNeeds) any() bool { return n.roles || len(n.ops) > 0 || len(n.autoNone) > 0 }

func seamHintsNeedsOf(p seamHintsPrep, st seamHintsState, force bool) seamHintsNeeds {
	n := seamHintsNeeds{roles: force || !st.rolesFound}
	for _, o := range p.in.Ops {
		if _, cur := st.current(o.Number, p.dg.Ops[o.Number]); cur && !force {
			continue
		}
		if hints.Joinable(p.in, o) {
			n.ops = append(n.ops, o.Number)
		} else {
			n.autoNone = append(n.autoNone, o.Number)
		}
	}
	return n
}

func (s *Server) seamHintsSuggestResponse(p seamHintsPrep, m seamHintsMerged, cached bool) *pb_admin.SuggestTechCardSeamHintsResponse {
	resp := &pb_admin.SuggestTechCardSeamHintsResponse{
		HintsJson: m.h.Marshal(), InputDigest: p.dg.Full, Model: m.model, Cached: cached,
	}
	if !m.createdAt.IsZero() {
		resp.CreatedAt = timestamppb.New(m.createdAt)
	}
	return resp
}

// SuggestTechCardSeamHints gives the AI hint of a card input, asking the model only for what the
// store lacks (SPEC-F §5a). Fire-and-forget from the client: DXF upload, operations save, archive
// import, the first open of a step without a run for the current fingerprint; «rerun» = force.
func (s *Server) SuggestTechCardSeamHints(ctx context.Context, req *pb_admin.SuggestTechCardSeamHintsRequest) (*pb_admin.SuggestTechCardSeamHintsResponse, error) {
	cardID := int(req.GetTechCardId())
	if cardID <= 0 {
		return nil, status.Error(codes.InvalidArgument, "tech_card_id is required")
	}
	p, err := s.seamHintsPrepare(ctx, cardID, req.GetInputJson())
	if err != nil {
		return nil, err
	}
	admin := authsrv.GetAdminUsername(ctx)
	force := req.GetForce()
	if force && !s.seamHintsForced.allow(admin+"\x00"+strconv.Itoa(cardID)+"\x00"+p.dg.Full, time.Now()) {
		force = false // a second «rerun» within the cooldown reads the store
	}
	st, err := s.seamHintsLoad(ctx, cardID, p.dg.Roles)
	if err != nil {
		return nil, err
	}
	need := seamHintsNeedsOf(p, st, force)
	if !need.roles && len(need.ops) == 0 { // nothing to ask: no money, no purpose check
		if len(need.autoNone) > 0 {
			if err := s.seamHintsSaveJoins(ctx, cardID, admin, "", p, need.autoNone, hints.Hints{}); err != nil {
				return nil, err
			}
			if st, err = s.seamHintsLoad(ctx, cardID, p.dg.Roles); err != nil {
				return nil, err
			}
		}
		return s.seamHintsSuggestResponse(p, seamHintsMerge(p, st), true), nil
	}
	const purpose = entity.AIPurposeTechCardSeams
	if !s.ai.Enabled(purpose) {
		return nil, s.aiOffRefusal(purpose, seamHintsNotConfiguredMsg)
	}

	job := seamHintsJob{cardID: cardID, admin: admin, prep: p, force: force}
	key := admin + "\x00" + p.dg.Full + "\x00" + strconv.FormatBool(force)
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
	force  bool
}

// seamHintsPromptCtx builds the incremental task: known roles (dictionary first, then the run) when
// the roles are not asked, the asked ops, and the current joins of the other ops as context.
func seamHintsPromptCtx(p seamHintsPrep, st seamHintsState, need seamHintsNeeds, overrides []hints.Override) hints.PromptCtx {
	pc := hints.PromptCtx{Dict: p.dict, Overrides: overrides, Pictures: len(p.urls),
		Atlas: p.in.AtlasDataURL() != "", RolesKnown: !need.roles, AskOps: need.ops}
	if !need.roles {
		run := map[string]hints.Role{}
		for _, r := range parseStoredHints(st.roles).Roles {
			run[r.Piece] = r
		}
		for _, pc2 := range p.in.Pieces {
			switch r, ok := run[pc2.Name]; {
			case pc2.DictRole != "":
				pc.KnownRoles = append(pc.KnownRoles, hints.Role{Piece: pc2.Name, Role: pc2.DictRole})
			case ok:
				pc.KnownRoles = append(pc.KnownRoles, r)
			}
		}
	}
	asked := make(map[int]bool, len(need.ops))
	for _, n := range need.ops {
		asked[n] = true
	}
	for _, o := range p.in.Ops {
		if asked[o.Number] {
			continue
		}
		if r, ok := st.current(o.Number, p.dg.Ops[o.Number]); ok {
			if j, ok := parseStoredJoin(r); ok {
				pc.KnownJoins = append(pc.KnownJoins, j)
			}
		}
	}
	return pc
}

// seamHintsSaveJoins stores one row per op of ops (outcome from the validated answer h: its join row,
// else not_inferred when the model said so, else none).
func (s *Server) seamHintsSaveJoins(ctx context.Context, cardID int, admin, model string, p seamHintsPrep, ops []int, h hints.Hints) error {
	if len(ops) == 0 {
		return nil
	}
	byOp := make(map[int]hints.Join, len(h.Joins))
	for _, j := range h.Joins {
		byOp[j.Op] = j
	}
	ni := make(map[string]bool, len(h.NotInferred))
	for _, k := range h.NotInferred {
		ni[k] = true
	}
	rows := make([]entity.TechCardSeamHintJoin, 0, len(ops))
	for _, n := range ops {
		r := entity.TechCardSeamHintJoin{TechCardId: cardID, OpNumber: n, OpDigest: p.dg.Ops[n],
			SchemaVersion: hints.StoreVersion, Outcome: entity.SeamHintJoinOutcomeNone, Model: model, CreatedBy: admin}
		if j, ok := byOp[n]; ok {
			b, _ := json.Marshal(j)
			r.Outcome, r.Hint = entity.SeamHintJoinOutcomeJoin, sql.NullString{String: string(b), Valid: true}
		} else if ni["op:"+strconv.Itoa(n)] {
			r.Outcome = entity.SeamHintJoinOutcomeNotInferred
		}
		rows = append(rows, r)
	}
	if err := s.repo.TechCards().SaveTechCardSeamHintJoins(ctx, rows); err != nil {
		if s.repo.IsErrForeignKeyViolation(err) {
			return status.Error(codes.NotFound, "tech card not found")
		}
		return seamHintsLogStore(ctx, "store the seam hints", cardID, err)
	}
	return nil
}

// seamHintsCall — the flight leader's work: the store once more (what another flight filled is not
// asked again), the prompt and its ceiling, the fences, the call (an unparseable answer is asked
// once more), validation, the roles run and the join rows stored, the merged hint read back.
func (s *Server) seamHintsCall(ctx context.Context, job seamHintsJob, budget time.Duration) (*pb_admin.SuggestTechCardSeamHintsResponse, error) {
	const purpose = entity.AIPurposeTechCardSeams
	p := job.prep
	st, err := s.seamHintsLoad(ctx, job.cardID, p.dg.Roles)
	if err != nil {
		return nil, err
	}
	need := seamHintsNeedsOf(p, st, job.force)
	if !need.roles && len(need.ops) == 0 {
		if err := s.seamHintsSaveJoins(ctx, job.cardID, job.admin, "", p, need.autoNone, hints.Hints{}); err != nil {
			return nil, err
		}
		if st, err = s.seamHintsLoad(ctx, job.cardID, p.dg.Roles); err != nil {
			return nil, err
		}
		return s.seamHintsSuggestResponse(p, seamHintsMerge(p, st), true), nil
	}
	overrides, err := s.repo.TechCards().ListTechCardSeamHintOverrides(ctx, job.cardID)
	if err != nil {
		return nil, seamHintsLogStore(ctx, "read the seam hint overrides", job.cardID, err)
	}
	user := hints.UserPrompt(p.in, seamHintsPromptCtx(p, st, need, seamHintsPromptOverrides(overrides)))
	if len(user) > seamHintsMaxPromptBytes {
		return nil, status.Errorf(codes.InvalidArgument,
			"the card is too large for one call: %d bytes of text, at most %d", len(user), seamHintsMaxPromptBytes)
	}
	images := append(append([]string{}, p.urls...), nonEmpty(p.in.AtlasDataURL())...)

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
	err = designPartsRetryUnusable(ctx, budget, func(actx context.Context, attempt int) error {
		started := time.Now()
		res, err := s.ai.Chat(actx, purpose, aiprov.ChatRequest{
			System: hints.SystemPrompt, User: user, ImageURLs: images, UserAsParts: len(images) > 0,
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
			slog.Int("tech_card_id", job.cardID), slog.Int("pieces", len(p.in.Pieces)),
			slog.Int("ops", len(p.in.Ops)), slog.Bool("roles_asked", need.roles), slog.Int("ops_asked", len(need.ops)),
			slog.Int("images", len(images)), slog.String("model", answered), slog.Int("attempt", attempt),
			slog.Duration("took", time.Since(started)), slog.String("finish_reason", enhanceLogFinishReason(finishReason)),
			slog.Int("prompt_tokens", usage.Prompt), slog.Int("completion_tokens", usage.Completion),
		}
		if err != nil {
			return s.seamHintsChatFailure(ctx, res, err, logAttrs)
		}
		h, rep, verr := hints.Validate(raw, p.in)
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
	if need.roles { // the roles part: roles + the pieces' not_inferred; joins are never in a roles run
		rh := hints.Hints{SchemaVersion: hints.SchemaVersion, Roles: out.Roles, Joins: []hints.Join{}, NotInferred: []string{}}
		if rh.Roles == nil {
			rh.Roles = []hints.Role{}
		}
		for _, k := range out.NotInferred {
			if strings.HasPrefix(k, "piece:") {
				rh.NotInferred = append(rh.NotInferred, k)
			}
		}
		if _, err := s.repo.TechCards().SaveTechCardSeamHintRun(ctx, entity.TechCardSeamHintRun{
			TechCardId: job.cardID, InputDigest: p.dg.Roles, SchemaVersion: hints.StoreVersion,
			Model: answered, Hints: rh.Marshal(), CreatedBy: job.admin,
		}); err != nil {
			if s.repo.IsErrForeignKeyViolation(err) {
				return nil, spend.attachTo(status.Error(codes.NotFound, "tech card not found"))
			}
			return nil, spend.attachTo(seamHintsLogStore(ctx, "store the seam hints", job.cardID, err))
		}
	}
	if err := s.seamHintsSaveJoins(ctx, job.cardID, job.admin, answered, p, need.ops, out); err != nil {
		return nil, spend.attachTo(err)
	}
	if err := s.seamHintsSaveJoins(ctx, job.cardID, job.admin, "", p, need.autoNone, hints.Hints{}); err != nil {
		return nil, spend.attachTo(err)
	}
	if st, err = s.seamHintsLoad(ctx, job.cardID, p.dg.Roles); err != nil {
		return nil, spend.attachTo(err)
	}
	resp := s.seamHintsSuggestResponse(p, seamHintsMerge(p, st), false)
	resp.CostUsd, resp.Calls = spend.costString(), int32(spend.calls)
	resp.RolesAsked = need.roles
	for _, n := range need.ops {
		resp.AskedOps = append(resp.AskedOps, int32(n))
	}
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

// GetTechCardSeamHints reads the merged hint and the overrides; never calls the model. With
// input_json: the hint of exactly that input (roles of its pieces digest, the row of each op's
// current digest) with what is outdated named (roles_stale, stale_ops, missing_ops). Without it the
// server cannot judge freshness: the newest roles run and the newest row per op, stale when anything
// is shown.
func (s *Server) GetTechCardSeamHints(ctx context.Context, req *pb_admin.GetTechCardSeamHintsRequest) (*pb_admin.GetTechCardSeamHintsResponse, error) {
	cardID := int(req.GetTechCardId())
	if cardID <= 0 {
		return nil, status.Error(codes.InvalidArgument, "tech_card_id is required")
	}
	digest := strings.TrimSpace(req.GetInputDigest())
	if digest != "" && !isDigestHex(digest) {
		return nil, seamHintsInputStatus(&hints.InputError{Field: "input_digest", Reason: "64 lower-case hex characters"})
	}
	resp := &pb_admin.GetTechCardSeamHintsResponse{InputDigest: digest}
	var m seamHintsMerged
	if req.GetInputJson() != "" {
		p, err := s.seamHintsPrepare(ctx, cardID, req.GetInputJson())
		if err != nil {
			return nil, err
		}
		st, err := s.seamHintsLoad(ctx, cardID, p.dg.Roles)
		if err != nil {
			return nil, err
		}
		m = seamHintsMerge(p, st)
		resp.InputDigest = p.dg.Full
		resp.RolesStale, resp.StaleOps, resp.MissingOps = m.rolesStale, m.staleOps, m.missingOps
		resp.Stale = !m.empty && m.stale()
		if !m.empty && !m.stale() {
			resp.RunDigest = p.dg.Full
		}
	} else {
		st, err := s.seamHintsLoad(ctx, cardID, "")
		if err != nil {
			return nil, err
		}
		m = seamHintsMergeLatest(st)
		resp.Stale = !m.empty && digest != ""
	}
	if !m.empty {
		resp.HintsJson, resp.Model = m.h.Marshal(), m.model
		resp.CreatedAt = timestamppb.New(m.createdAt)
	}
	overrides, err := s.repo.TechCards().ListTechCardSeamHintOverrides(ctx, cardID)
	if err != nil {
		return nil, seamHintsLogStore(ctx, "read the seam hint overrides", cardID, err)
	}
	resp.Overrides = seamHintOverridesToPb(overrides)
	return resp, nil
}

// seamHintsMergeLatest — the newest roles run and the newest row of every op ever stored (no input
// to check them against).
func seamHintsMergeLatest(st seamHintsState) seamHintsMerged {
	m := seamHintsMerged{empty: true, h: hints.Hints{SchemaVersion: hints.SchemaVersion,
		Roles: []hints.Role{}, Joins: []hints.Join{}, NotInferred: []string{}}}
	var pieceNI []string
	if st.latestFound {
		m.saw(st.latest.Model, st.latest.CreatedAt)
		rh := parseStoredHints(st.latest)
		m.h.Roles = append(m.h.Roles, rh.Roles...)
		for _, k := range rh.NotInferred {
			if strings.HasPrefix(k, "piece:") {
				pieceNI = append(pieceNI, k)
			}
		}
	}
	ops := make([]int, 0, len(st.joins))
	for n := range st.joins {
		ops = append(ops, n)
	}
	sort.Ints(ops)
	for _, n := range ops {
		r := st.joins[n][0]
		m.saw(r.Model, r.CreatedAt)
		if j, ok := parseStoredJoin(r); ok {
			m.h.Joins = append(m.h.Joins, j)
		} else if r.Outcome == entity.SeamHintJoinOutcomeNotInferred {
			m.h.NotInferred = append(m.h.NotInferred, "op:"+strconv.Itoa(n))
		}
	}
	sort.Strings(pieceNI)
	m.h.NotInferred = append(m.h.NotInferred, pieceNI...)
	return m
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
