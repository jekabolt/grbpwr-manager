package admin

import (
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/aiprov"
	"github.com/jekabolt/grbpwr-manager/internal/aiprov/router"
	"github.com/jekabolt/grbpwr-manager/internal/dependency/mocks"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	hints "github.com/jekabolt/grbpwr-manager/internal/techcardseamhints"
	pb_admin "github.com/jekabolt/grbpwr-manager/proto/gen/admin"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// seamFakeStore — the seam-hint half of TechCards in memory (the rest stays the strict mock).
type seamFakeStore struct {
	*mocks.MockTechCards
	mu        sync.Mutex
	card      int
	technical map[int]bool
	runs      map[string]entity.TechCardSeamHintRun
	order     []string
	overrides map[string]entity.TechCardSeamHintOverride
	dict      map[string]entity.PieceCodeDictionaryEntry
	saves     int
}

func (f *seamFakeStore) TechCardTechnicalMediaIDs(_ context.Context, cardID int, ids []int) ([]int, error) {
	if cardID != f.card {
		return nil, sql.ErrNoRows
	}
	var out []int
	for _, id := range ids {
		if f.technical[id] {
			out = append(out, id)
		}
	}
	return out, nil
}

func (f *seamFakeStore) GetTechCardSeamHintRun(_ context.Context, cardID int, digest string, v int) (entity.TechCardSeamHintRun, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	r, ok := f.runs[digest]
	return r, ok && r.TechCardId == cardID && r.SchemaVersion == v, nil
}

func (f *seamFakeStore) LatestTechCardSeamHintRun(_ context.Context, cardID int, v int) (entity.TechCardSeamHintRun, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.order) == 0 {
		return entity.TechCardSeamHintRun{}, false, nil
	}
	return f.runs[f.order[len(f.order)-1]], true, nil
}

func (f *seamFakeStore) SaveTechCardSeamHintRun(_ context.Context, r entity.TechCardSeamHintRun) (entity.TechCardSeamHintRun, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.saves++
	r.Id = int64(f.saves)
	r.CreatedAt = time.Now()
	f.runs[r.InputDigest] = r
	f.order = append(f.order, r.InputDigest)
	return r, nil
}

func (f *seamFakeStore) ListTechCardSeamHintOverrides(_ context.Context, _ int) ([]entity.TechCardSeamHintOverride, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]entity.TechCardSeamHintOverride, 0, len(f.overrides))
	for _, o := range f.overrides {
		out = append(out, o)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Kind+out[i].HintKey < out[j].Kind+out[j].HintKey })
	return out, nil
}

func (f *seamFakeStore) SetTechCardSeamHintOverride(ctx context.Context, o entity.TechCardSeamHintOverride) ([]entity.TechCardSeamHintOverride, error) {
	if o.TechCardId != f.card {
		return nil, sql.ErrNoRows
	}
	f.mu.Lock()
	if o.State == "" {
		delete(f.overrides, o.Kind+"\x00"+o.HintKey)
	} else {
		o.UpdatedAt = time.Now()
		f.overrides[o.Kind+"\x00"+o.HintKey] = o
	}
	f.mu.Unlock()
	return f.ListTechCardSeamHintOverrides(ctx, o.TechCardId)
}

func (f *seamFakeStore) ListPieceCodeDictionary(_ context.Context) ([]entity.PieceCodeDictionaryEntry, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]entity.PieceCodeDictionaryEntry, 0, len(f.dict))
	for _, e := range f.dict {
		out = append(out, e)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Code < out[j].Code })
	return out, nil
}

func (f *seamFakeStore) SetPieceCodeDictionaryEntry(_ context.Context, e entity.PieceCodeDictionaryEntry) (entity.PieceCodeDictionaryEntry, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if e.Role == "" {
		delete(f.dict, e.Code)
		return entity.PieceCodeDictionaryEntry{}, false, nil
	}
	e.UpdatedAt = time.Now()
	f.dict[e.Code] = e
	return e, true, nil
}

// recordingChatter answers from a script and keeps every request.
type recordingChatter struct {
	mu   sync.Mutex
	reqs []aiprov.ChatRequest
	text []string // the last repeats
}

func (c *recordingChatter) Chat(_ context.Context, _ string, req aiprov.ChatRequest) (*aiprov.ChatResult, error) {
	c.mu.Lock()
	i := len(c.reqs)
	c.reqs = append(c.reqs, req)
	c.mu.Unlock()
	if i >= len(c.text) {
		i = len(c.text) - 1
	}
	return skAnswer(c.text[i], "0.01")()
}

func (c *recordingChatter) calls() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.reqs)
}

const seamTestCard = 6

// The model's answer: one good role, one role for a piece that does not exist, one good join, one join
// with a piece that is not an input of its step.
const seamTestAnswer = "```json\n" + `{"roles":[{"piece":"CLR_SECOND","role":"stand","hand":"","conf":"high","why":"stand under the collar"},
 {"piece":"GHOST","role":"front","hand":"","conf":"high"}],
 "joins":[{"op":720,"a":{"pieces":["PLCK_L"],"edge":"long"},"b":{"pieces":["P_L_MAIN"],"edge":"front_edge"},"relation":"boundary","conf":"high","why":"placket on the front"},
 {"op":870,"a":{"pieces":["NOPE"],"edge":"bottom"},"b":{"pieces":["CLR"],"edge":"neckline"},"relation":"boundary","conf":"high"},
 {"op":720,"a":{"pieces":["P_L_MAIN"],"edge":"hem"},"b":{"pieces":["PLCK_L"],"edge":"short"},"relation":"boundary","conf":"high"}],
 "not_inferred":["op:999","piece:FLP_R_MAIN"]}` + "\n```"

func seamTestInput(t *testing.T, extraPiece string) string {
	t.Helper()
	atlas := base64.StdEncoding.EncodeToString([]byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n', 1, 2, 3})
	pieces := []map[string]any{
		{"name": "P_L_MAIN", "purpose": "main", "dict_role": "front", "aspect": 2.8, "area": 180000.4},
		{"name": "PLCK_L", "purpose": "main", "fused": true, "dict_role": "placket"},
		{"name": "CLR", "purpose": "main", "dict_role": "collar"},
		{"name": "CLR_SECOND", "purpose": "main"},
		{"name": "FLP_R_MAIN", "purpose": "main", "fused": true},
	}
	if extraPiece != "" {
		pieces = append(pieces, map[string]any{"name": extraPiece, "purpose": "main"})
	}
	raw, err := json.Marshal(map[string]any{
		"pieces": pieces,
		"ops": []map[string]any{
			{"number": 720, "zone": "front", "work": "placket", "operation_type": "MACHINE", "inputs": []string{"PLCK_L", "P_L_MAIN"}, "output": "FRONT_L"},
			{"number": 860, "zone": "collar", "work": "collar", "operation_type": "MACHINE", "inputs": []string{"CLR"}, "output": "COLLAR_U"},
			{"number": 870, "zone": "collar", "work": "stand", "operation_type": "MACHINE", "inputs": []string{"CLR_SECOND", "COLLAR_U"}},
		},
		"sketch_media_ids": []int{5},
		"atlas_png":        "data:image/png;base64," + atlas,
	})
	require.NoError(t, err)
	return string(raw)
}

func newSeamRig(t *testing.T, chatter aiprov.Chatter) (*Server, *seamFakeStore) {
	t.Helper()
	store := &seamFakeStore{
		MockTechCards: mocks.NewMockTechCards(t), card: seamTestCard, technical: map[int]bool{5: true},
		runs: map[string]entity.TechCardSeamHintRun{}, overrides: map[string]entity.TechCardSeamHintOverride{},
		dict: map[string]entity.PieceCodeDictionaryEntry{},
	}
	media := mocks.NewMockMedia(t)
	media.EXPECT().GetMediaByIds(mock.Anything, mock.Anything).RunAndReturn(func(_ context.Context, ids []int) (map[int]entity.MediaFull, error) {
		out := map[int]entity.MediaFull{}
		for _, id := range ids {
			m := entity.MediaFull{Id: id}
			m.FullSizeMediaURL = "https://cdn.example/sketch.png"
			m.ContentHash = sql.NullString{String: "hash-5", Valid: true}
			out[id] = m
		}
		return out, nil
	}).Maybe()
	repo := mocks.NewMockRepository(t)
	repo.EXPECT().TechCards().Return(store).Maybe()
	repo.EXPECT().Media().Return(media).Maybe()
	var candidates []router.StaticCandidate
	if chatter != nil {
		candidates = []router.StaticCandidate{{ProviderKey: entity.AIProviderOpenRouter, Chatter: chatter, Model: "anthropic/claude-sonnet-5.5"}}
	}
	return &Server{ai: router.NewStatic(candidates), repo: repo, enhanceSem: make(chan struct{}, maxConcurrentEnhance)}, store
}

func seamSuggest(t *testing.T, s *Server, admin, input string, force bool) *pb_admin.SuggestTechCardSeamHintsResponse {
	t.Helper()
	res, err := s.SuggestTechCardSeamHints(adminCtx(admin), &pb_admin.SuggestTechCardSeamHintsRequest{
		TechCardId: seamTestCard, InputJson: input, Force: force,
	})
	require.NoError(t, err)
	return res
}

func TestSuggestTechCardSeamHintsCachesByDigestAndDropsInvalidRows(t *testing.T) {
	c := &recordingChatter{text: []string{seamTestAnswer}}
	s, store := newSeamRig(t, c)
	in := seamTestInput(t, "")

	first := seamSuggest(t, s, "alice", in, false)
	require.False(t, first.Cached)
	require.Equal(t, 1, c.calls())
	require.EqualValues(t, 1, first.Calls)
	require.Equal(t, "0.01", first.CostUsd)
	require.Len(t, first.InputDigest, 64)
	require.Equal(t, "anthropic/claude-sonnet-5.5", first.Model)

	// The call: temperature 0, JSON mode, the card's sketch then the atlas.
	req := c.reqs[0]
	require.NotNil(t, req.Temperature)
	require.Zero(t, *req.Temperature)
	require.True(t, req.JSONMode)
	require.Len(t, req.ImageURLs, 2)
	require.Equal(t, "https://cdn.example/sketch.png", req.ImageURLs[0])
	require.True(t, strings.HasPrefix(req.ImageURLs[1], "data:image/png;base64,"))
	require.Contains(t, req.System, "NEVER output edge indexes")

	// Invalid rows are dropped; the bad join's op is not_inferred; the first row per op is kept.
	var h hints.Hints
	require.NoError(t, json.Unmarshal([]byte(first.HintsJson), &h))
	require.Len(t, h.Roles, 1)
	require.Equal(t, "CLR_SECOND", h.Roles[0].Piece)
	require.Len(t, h.Joins, 1)
	require.Equal(t, 720, h.Joins[0].Op)
	require.Equal(t, "long", h.Joins[0].A.Edge)
	require.Equal(t, []string{"op:870", "piece:FLP_R_MAIN"}, h.NotInferred)
	require.NotContains(t, first.HintsJson, "GHOST")
	require.NotContains(t, first.HintsJson, "999")

	// Digest hit: no provider call, the stored run.
	again := seamSuggest(t, s, "bob", in, false)
	require.True(t, again.Cached)
	require.Equal(t, 1, c.calls())
	require.Equal(t, first.InputDigest, again.InputDigest)
	require.Equal(t, first.HintsJson, again.HintsJson)
	require.Equal(t, 1, store.saves)

	// Force calls again; a second force of the same digest within the cooldown pays nothing.
	forced := seamSuggest(t, s, "alice", in, true)
	require.False(t, forced.Cached)
	require.Equal(t, 2, c.calls())
	twice := seamSuggest(t, s, "alice", in, true)
	require.True(t, twice.Cached)
	require.Equal(t, 2, c.calls())
}

func TestSeamHintOverrideSurvivesARerunAndReachesThePrompt(t *testing.T) {
	c := &recordingChatter{text: []string{seamTestAnswer}}
	s, _ := newSeamRig(t, c)
	in := seamTestInput(t, "")
	first := seamSuggest(t, s, "alice", in, false)

	set, err := s.SetTechCardSeamHintOverride(adminCtx("alice"), &pb_admin.SetTechCardSeamHintOverrideRequest{
		TechCardId: seamTestCard, Kind: "join", HintKey: " 0870 ", State: "fixed",
		ValueJson: `{"a":{"pieces":["CLR_SECOND"],"edge":"bottom"},"b":{"pieces":["CLR"],"edge":"neckline"},"relation":"boundary"}`,
	})
	require.NoError(t, err)
	require.Len(t, set.Overrides, 1)
	require.Equal(t, "870", set.Overrides[0].HintKey)
	require.Equal(t, "alice", set.Overrides[0].UpdatedBy)
	_, err = s.SetTechCardSeamHintOverride(adminCtx("alice"), &pb_admin.SetTechCardSeamHintOverrideRequest{
		TechCardId: seamTestCard, Kind: "role", HintKey: "FLP_R_MAIN", State: "off",
	})
	require.NoError(t, err)

	// An override is not part of the digest: the same input reads the same run …
	got, err := s.GetTechCardSeamHints(adminCtx("alice"), &pb_admin.GetTechCardSeamHintsRequest{TechCardId: seamTestCard, InputJson: in})
	require.NoError(t, err)
	require.Equal(t, first.InputDigest, got.InputDigest)
	require.Equal(t, first.InputDigest, got.RunDigest)
	require.False(t, got.Stale)

	// … and a forced rerun keeps both overrides and tells the model the fixed one (never the off one).
	rerun := seamSuggest(t, s, "alice", in, true)
	require.False(t, rerun.Cached)
	require.Equal(t, 2, c.calls())
	require.Contains(t, c.reqs[1].User, "OWNER FACTS")
	require.Contains(t, c.reqs[1].User, "op 870:")
	require.NotContains(t, c.reqs[1].User, `piece "FLP_R_MAIN":`)
	got, err = s.GetTechCardSeamHints(adminCtx("alice"), &pb_admin.GetTechCardSeamHintsRequest{TechCardId: seamTestCard, InputDigest: rerun.InputDigest})
	require.NoError(t, err)
	require.Len(t, got.Overrides, 2)

	// An empty state deletes.
	del, err := s.SetTechCardSeamHintOverride(adminCtx("alice"), &pb_admin.SetTechCardSeamHintOverrideRequest{
		TechCardId: seamTestCard, Kind: "role", HintKey: "FLP_R_MAIN",
	})
	require.NoError(t, err)
	require.Len(t, del.Overrides, 1)
}

func TestSeamHintOverrideRefusals(t *testing.T) {
	s, _ := newSeamRig(t, nil)
	cases := map[string]*pb_admin.SetTechCardSeamHintOverrideRequest{
		"no card":     {Kind: "role", HintKey: "A", State: "ok", ValueJson: `{"role":"front","hand":""}`},
		"bad kind":    {TechCardId: seamTestCard, Kind: "seam", HintKey: "A", State: "ok", ValueJson: `{"role":"front"}`},
		"bad state":   {TechCardId: seamTestCard, Kind: "role", HintKey: "A", State: "maybe", ValueJson: `{"role":"front"}`},
		"bad role":    {TechCardId: seamTestCard, Kind: "role", HintKey: "A", State: "fixed", ValueJson: `{"role":"torso"}`},
		"no value":    {TechCardId: seamTestCard, Kind: "role", HintKey: "A", State: "fixed"},
		"bad op key":  {TechCardId: seamTestCard, Kind: "join", HintKey: "x1", State: "off"},
		"extra field": {TechCardId: seamTestCard, Kind: "role", HintKey: "A", State: "ok", ValueJson: `{"role":"front","mm":3}`},
	}
	for name, req := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := s.SetTechCardSeamHintOverride(adminCtx("alice"), req)
			require.Equal(t, codes.InvalidArgument, status.Code(err), "%v", err)
		})
	}
	_, err := s.SetTechCardSeamHintOverride(adminCtx("alice"), &pb_admin.SetTechCardSeamHintOverrideRequest{
		TechCardId: 99, Kind: "role", HintKey: "A", State: "off",
	})
	require.Equal(t, codes.NotFound, status.Code(err))
}

func TestSuggestTechCardSeamHintsDoorsBeforeMoney(t *testing.T) {
	// Purpose disabled: a clean refusal, nothing called, nothing stored.
	off, store := newSeamRig(t, nil)
	_, err := off.SuggestTechCardSeamHints(adminCtx("alice"), &pb_admin.SuggestTechCardSeamHintsRequest{
		TechCardId: seamTestCard, InputJson: seamTestInput(t, ""),
	})
	require.Error(t, err)
	require.NotEqual(t, codes.InvalidArgument, status.Code(err))
	require.Contains(t, err.Error(), "not configured")
	require.Zero(t, store.saves)

	c := &recordingChatter{text: []string{seamTestAnswer}}
	s, _ := newSeamRig(t, c)
	// A sketch that is not on this card's technical sheet.
	foreign := strings.Replace(seamTestInput(t, ""), `"sketch_media_ids":[5]`, `"sketch_media_ids":[5,77]`, 1)
	_, err = s.SuggestTechCardSeamHints(adminCtx("alice"), &pb_admin.SuggestTechCardSeamHintsRequest{TechCardId: seamTestCard, InputJson: foreign})
	require.Equal(t, codes.InvalidArgument, status.Code(err), "%v", err)
	require.Contains(t, err.Error(), "not on this card")
	// An unknown card.
	_, err = s.SuggestTechCardSeamHints(adminCtx("alice"), &pb_admin.SuggestTechCardSeamHintsRequest{TechCardId: 99, InputJson: seamTestInput(t, "")})
	require.Equal(t, codes.NotFound, status.Code(err), "%v", err)
	// A broken input.
	_, err = s.SuggestTechCardSeamHints(adminCtx("alice"), &pb_admin.SuggestTechCardSeamHintsRequest{TechCardId: seamTestCard, InputJson: `{"pieces":[]}`})
	require.Equal(t, codes.InvalidArgument, status.Code(err))
	_, err = s.SuggestTechCardSeamHints(adminCtx("alice"), &pb_admin.SuggestTechCardSeamHintsRequest{InputJson: seamTestInput(t, "")})
	require.Equal(t, codes.InvalidArgument, status.Code(err))
	require.Zero(t, c.calls())
}

func TestSuggestTechCardSeamHintsRetriesAnUnparseableAnswerOnce(t *testing.T) {
	c := &recordingChatter{text: []string{"Sure! {", seamTestAnswer}}
	s, _ := newSeamRig(t, c)
	res := seamSuggest(t, s, "alice", seamTestInput(t, ""), false)
	require.Equal(t, 2, c.calls())
	require.EqualValues(t, 2, res.Calls)

	bad := &recordingChatter{text: []string{"no json"}}
	s2, store := newSeamRig(t, bad)
	_, err := s2.SuggestTechCardSeamHints(adminCtx("alice"), &pb_admin.SuggestTechCardSeamHintsRequest{TechCardId: seamTestCard, InputJson: seamTestInput(t, "")})
	require.Equal(t, codes.Internal, status.Code(err))
	require.Equal(t, 2, bad.calls())
	require.Zero(t, store.saves, "an unusable answer is never stored")
}

func TestSeamHintsDictionaryIsInTheDigestAndThePrompt(t *testing.T) {
	c := &recordingChatter{text: []string{seamTestAnswer}}
	s, _ := newSeamRig(t, c)
	in := seamTestInput(t, "")
	before := seamSuggest(t, s, "alice", in, false)

	// The owner teaches FLP = flap: the digest moves, the stored run goes stale, the prompt says it.
	set, err := s.SetPieceCodeDictionaryEntry(adminCtx("alice"), &pb_admin.SetPieceCodeDictionaryEntryRequest{Code: " flp ", Role: "Flap", Hand: ""})
	require.NoError(t, err)
	require.Equal(t, "FLP", set.Entry.Code)
	require.Equal(t, "flap", set.Entry.Role)
	require.Equal(t, "alice", set.Entry.UpdatedBy)

	got, err := s.GetTechCardSeamHints(adminCtx("alice"), &pb_admin.GetTechCardSeamHintsRequest{TechCardId: seamTestCard, InputJson: in})
	require.NoError(t, err)
	require.NotEqual(t, before.InputDigest, got.InputDigest)
	require.True(t, got.Stale)
	require.Equal(t, before.InputDigest, got.RunDigest)
	require.Equal(t, before.HintsJson, got.HintsJson)

	after := seamSuggest(t, s, "alice", in, false)
	require.False(t, after.Cached)
	require.Equal(t, got.InputDigest, after.InputDigest)
	require.Contains(t, c.reqs[1].User, "OWNER'S CODE DICTIONARY")
	require.Contains(t, c.reqs[1].User, `"FLP" → flap`)

	// A code no piece carries does not move the digest.
	_, err = s.SetPieceCodeDictionaryEntry(adminCtx("alice"), &pb_admin.SetPieceCodeDictionaryEntryRequest{Code: "WB", Role: "waistband"})
	require.NoError(t, err)
	same, err := s.GetTechCardSeamHints(adminCtx("alice"), &pb_admin.GetTechCardSeamHintsRequest{TechCardId: seamTestCard, InputJson: in})
	require.NoError(t, err)
	require.Equal(t, after.InputDigest, same.InputDigest)
	require.False(t, same.Stale)

	list, err := s.ListPieceCodeDictionary(adminCtx("alice"), &pb_admin.ListPieceCodeDictionaryRequest{})
	require.NoError(t, err)
	require.Len(t, list.Entries, 2)

	// Empty role deletes; bad code / role / hand refuse.
	del, err := s.SetPieceCodeDictionaryEntry(adminCtx("alice"), &pb_admin.SetPieceCodeDictionaryEntryRequest{Code: "WB"})
	require.NoError(t, err)
	require.True(t, del.Deleted)
	for _, req := range []*pb_admin.SetPieceCodeDictionaryEntryRequest{
		{Code: "", Role: "flap"}, {Code: "A B", Role: "flap"}, {Code: "FLP", Role: "torso"}, {Code: "FLP", Role: "flap", Hand: "X"},
	} {
		_, err := s.SetPieceCodeDictionaryEntry(adminCtx("alice"), req)
		require.Equal(t, codes.InvalidArgument, status.Code(err), "%+v", req)
	}
}

func TestGetTechCardSeamHintsWithoutARun(t *testing.T) {
	s, _ := newSeamRig(t, nil)
	got, err := s.GetTechCardSeamHints(adminCtx("alice"), &pb_admin.GetTechCardSeamHintsRequest{TechCardId: seamTestCard})
	require.NoError(t, err)
	require.Empty(t, got.HintsJson)
	require.False(t, got.Stale)
	_, err = s.GetTechCardSeamHints(adminCtx("alice"), &pb_admin.GetTechCardSeamHintsRequest{TechCardId: seamTestCard, InputDigest: "zz"})
	require.Equal(t, codes.InvalidArgument, status.Code(err))
}
