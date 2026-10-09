package admin

import (
	"encoding/json"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/dependency/mocks"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/jekabolt/grbpwr-manager/internal/openrouter"
	pb_admin "github.com/jekabolt/grbpwr-manager/proto/gen/admin"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// ─── the call-level rig: a fake provider, a media mock, the real limiter ───

type ppRig struct {
	s     *Server
	media *mocks.MockMedia
	mu    sync.Mutex
	calls int
}

// ppReply — one completion: the content, 100 prompt + 40 completion tokens, and a cost (nil = the
// provider reported none).
func ppReply(content string, cost any) func(http.ResponseWriter) {
	return func(w http.ResponseWriter) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"choices": []any{map[string]any{"message": map[string]any{"role": "assistant", "content": content}}},
			"usage":   map[string]any{"prompt_tokens": 100, "completion_tokens": 40, "cost": cost},
		})
	}
}

// newPPRig serves the replies in order (the last one repeats); before each reply it runs gate (may
// be nil) — the concurrency test holds the provider there.
func newPPRig(t *testing.T, gate func(), replies ...func(http.ResponseWriter)) *ppRig {
	t.Helper()
	rig := &ppRig{}
	client, _ := newFakeOpenRouter(t, func(w http.ResponseWriter) {
		rig.mu.Lock()
		i := rig.calls
		rig.calls++
		rig.mu.Unlock()
		if gate != nil {
			gate()
		}
		if i >= len(replies) {
			i = len(replies) - 1
		}
		replies[i](w)
	})
	repo := mocks.NewMockRepository(t)
	rig.media = mocks.NewMockMedia(t)
	repo.EXPECT().Media().Return(rig.media).Maybe()
	rig.s = &Server{ai: newTestRouter(client), repo: repo, enhanceSem: make(chan struct{}, maxConcurrentEnhance)}
	return rig
}

func (r *ppRig) picturesOK() {
	r.media.EXPECT().GetMediaByIds(mock.Anything, []int{100, 101}).Return(map[int]entity.MediaFull{
		100: {Id: 100, MediaItem: entity.MediaItem{FullSizeMediaURL: "https://files.grbpwr.com/sheet.png"}},
		101: {Id: 101, MediaItem: entity.MediaItem{FullSizeMediaURL: "https://files.grbpwr.com/crop.png"}},
	}, nil).Maybe()
}

func (r *ppRig) providerCalls() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.calls
}

func (r *ppRig) cacheEntries() int {
	r.s.patternPiecesCache.mu.Lock()
	defer r.s.patternPiecesCache.mu.Unlock()
	return len(r.s.patternPiecesCache.entries)
}

// slotsUsed spends what is left of admin's hourly window and reports how much was already spent.
// Call it LAST in a test.
func (r *ppRig) slotsUsed(admin string) int {
	left := 0
	for left <= enhancePerAdminCalls && r.s.enhanceRuns.allow(admin) {
		left++
	}
	return enhancePerAdminCalls - left
}

func ppAnswer(marks ...int) string {
	parts := make([]string, 0, len(marks))
	for _, m := range marks {
		parts = append(parts, patternPiecesTestPiece(m, []string{"", "FP", "SL", "BP"}[m], ""))
	}
	return `{"pieces":[` + strings.Join(parts, ",") + `]}`
}

// invalid → valid: two provider calls, one cache entry, the tokens and the cost of BOTH attempts;
// the next identical press is a cache hit that spends no slot and no call.
func TestSuggestPatternPiecesRetriesAndSumsBothAttempts(t *testing.T) {
	rig := newPPRig(t, nil, ppReply("Sorry, here it is: {", 0.01), ppReply(ppAnswer(1, 2, 3), 0.02))
	rig.picturesOK()
	res, err := rig.s.SuggestPatternPieces(adminCtx("alice"), patternPiecesTestRequest())
	require.NoError(t, err)
	require.Equal(t, 2, rig.providerCalls())
	require.Equal(t, 1, rig.cacheEntries())
	require.EqualValues(t, 200, res.PromptTokens)
	require.EqualValues(t, 80, res.CompletionTokens)
	require.Equal(t, "0.03", res.CostUsd)
	require.False(t, res.Cached)
	require.Len(t, res.Suggestions, 3)

	hit, err := rig.s.SuggestPatternPieces(adminCtx("alice"), patternPiecesTestRequest())
	require.NoError(t, err)
	require.True(t, hit.Cached)
	require.Zero(t, hit.PromptTokens)
	require.Equal(t, 2, rig.providerCalls(), "a cache hit calls nobody")
	require.Equal(t, 1, rig.slotsUsed("alice"), "one flight = one slot; the cache hit spends none")
}

// An attempt without a reported cost: the known sum is kept and the gap is named.
func TestSuggestPatternPiecesWarnsAboutAnUnpricedAttempt(t *testing.T) {
	rig := newPPRig(t, nil, ppReply("{}", nil), ppReply(ppAnswer(1, 2, 3), 0.02))
	rig.picturesOK()
	res, err := rig.s.SuggestPatternPieces(adminCtx("alice"), patternPiecesTestRequest())
	require.NoError(t, err)
	require.Equal(t, "0.02", res.CostUsd)
	require.EqualValues(t, 200, res.PromptTokens)
	require.Contains(t, strings.Join(res.Warnings, "\n"), "attempt 1: the provider reported no cost")
}

// A valid but partial answer: one call, returned with the gap named, never cached.
func TestSuggestPatternPiecesNeverCachesAPartialAnswer(t *testing.T) {
	rig := newPPRig(t, nil, ppReply(ppAnswer(1, 2), 0.01))
	rig.picturesOK()
	res, err := rig.s.SuggestPatternPieces(adminCtx("alice"), patternPiecesTestRequest())
	require.NoError(t, err)
	require.Equal(t, 1, rig.providerCalls(), "a structurally valid answer is not asked again")
	require.Equal(t, 0, rig.cacheEntries())
	require.Contains(t, strings.Join(res.Warnings, "\n"), "mark 3: not named by the model")

	_, err = rig.s.SuggestPatternPieces(adminCtx("alice"), patternPiecesTestRequest())
	require.NoError(t, err)
	require.Equal(t, 2, rig.providerCalls(), "the next press asks again")
}

// invalid × 2: two calls, the unusable refusal, nothing cached, one slot.
func TestSuggestPatternPiecesTwoInvalidAnswers(t *testing.T) {
	rig := newPPRig(t, nil, ppReply(`{"pieces":[{"mark":"1"}]}`, 0.01))
	rig.picturesOK()
	_, err := rig.s.SuggestPatternPieces(adminCtx("alice"), patternPiecesTestRequest())
	require.Equal(t, codes.Internal, status.Code(err))
	require.Equal(t, designPartsUnusableMsg, status.Convert(err).Message())
	require.Equal(t, 2, rig.providerCalls())
	require.Equal(t, 0, rig.cacheEntries())
	require.Equal(t, 1, rig.slotsUsed("alice"))
}

// A refused picture spends no slot and calls nobody.
func TestSuggestPatternPiecesRefusedMediaSpendsNothing(t *testing.T) {
	rig := newPPRig(t, nil, ppReply(ppAnswer(1, 2, 3), 0.01))
	rig.media.EXPECT().GetMediaByIds(mock.Anything, []int{100, 101}).Return(map[int]entity.MediaFull{
		100: {Id: 100, MediaItem: entity.MediaItem{FullSizeMediaURL: "https://files.grbpwr.com/sheet.glb"}},
		101: {Id: 101, MediaItem: entity.MediaItem{FullSizeMediaURL: "https://files.grbpwr.com/crop.png"}},
	}, nil).Once()
	_, err := rig.s.SuggestPatternPieces(adminCtx("alice"), patternPiecesTestRequest())
	require.Equal(t, codes.FailedPrecondition, status.Code(err), "%v", err)
	require.Equal(t, 0, rig.providerCalls())
	require.Equal(t, 0, rig.slotsUsed("alice"))

	// A missing file too.
	rig2 := newPPRig(t, nil, ppReply(ppAnswer(1, 2, 3), 0.01))
	rig2.media.EXPECT().GetMediaByIds(mock.Anything, []int{100, 101}).Return(map[int]entity.MediaFull{
		100: {Id: 100, MediaItem: entity.MediaItem{FullSizeMediaURL: "https://files.grbpwr.com/sheet.png"}},
	}, nil).Once()
	_, err = rig2.s.SuggestPatternPieces(adminCtx("alice"), patternPiecesTestRequest())
	require.Equal(t, codes.InvalidArgument, status.Code(err))
	require.Equal(t, 0, rig2.slotsUsed("alice"))
}

// A busy semaphore spends no slot.
func TestSuggestPatternPiecesBusySemaphoreSpendsNothing(t *testing.T) {
	rig := newPPRig(t, nil, ppReply(ppAnswer(1, 2, 3), 0.01))
	rig.picturesOK()
	for i := 0; i < cap(rig.s.enhanceSem); i++ {
		rig.s.enhanceSem <- struct{}{}
	}
	_, err := rig.s.SuggestPatternPieces(adminCtx("alice"), patternPiecesTestRequest())
	require.Equal(t, codes.ResourceExhausted, status.Code(err))
	require.Equal(t, 0, rig.providerCalls())
	require.Equal(t, 0, rig.slotsUsed("alice"))
}

// Two identical presses at once: one provider call, one slot.
func TestSuggestPatternPiecesConcurrentPressesPayOnce(t *testing.T) {
	entered, release := make(chan struct{}, 4), make(chan struct{})
	rig := newPPRig(t, func() { entered <- struct{}{}; <-release }, ppReply(ppAnswer(1, 2, 3), 0.01))
	rig.picturesOK()
	var wg sync.WaitGroup
	errs := make([]error, 2)
	press := func(i int) {
		defer wg.Done()
		_, errs[i] = rig.s.SuggestPatternPieces(adminCtx("alice"), patternPiecesTestRequest())
	}
	wg.Add(2)
	go press(0)
	<-entered // the first press is at the provider
	go press(1)
	time.Sleep(100 * time.Millisecond) // let the second press join the flight
	close(release)
	wg.Wait()
	require.NoError(t, errs[0])
	require.NoError(t, errs[1])
	require.Equal(t, 1, rig.providerCalls())
	require.Equal(t, 1, rig.slotsUsed("alice"))
}

// The prompt ceiling is the first door after the arguments: before the purpose check (a router with
// no key), the cache, the limiter, the media lookup (no repo at all) and the provider.
func TestSuggestPatternPiecesPromptCeilingComesFirst(t *testing.T) {
	big := patternPiecesTestRequest()
	big.Pieces, big.Crops = nil, nil
	for m := 1; m <= patternPiecesMaxPieces; m++ {
		texts := make([]string, patternPiecesMaxTextItems)
		for i := range texts {
			texts[i] = strings.Repeat("ж", patternPiecesMaxTextRunes-3) + string(rune('a'+i))
		}
		big.Pieces = append(big.Pieces, &pb_admin.PatternPieceEvidence{Mark: int32(m), TextInside: texts})
	}

	// A router whose purpose is NOT callable: the ceiling still answers first.
	off := &Server{ai: newTestRouter(openrouter.New(openrouter.Config{})), enhanceSem: make(chan struct{}, maxConcurrentEnhance)}
	_, err := off.SuggestPatternPieces(adminCtx("alice"), big)
	require.Equal(t, codes.InvalidArgument, status.Code(err), "%v", err)
	require.Contains(t, err.Error(), "too large for one call")
	// Negative control: the ordinary request on that server is refused by the purpose door.
	_, err = off.SuggestPatternPieces(adminCtx("alice"), patternPiecesTestRequest())
	require.NotEqual(t, codes.InvalidArgument, status.Code(err))

	// A live router and no repo: nothing past the ceiling is touched.
	rig := newPPRig(t, nil, ppReply(ppAnswer(1, 2, 3), 0.01))
	rig.s.repo = nil
	_, err = rig.s.SuggestPatternPieces(adminCtx("alice"), big)
	require.Equal(t, codes.InvalidArgument, status.Code(err))
	require.Equal(t, 0, rig.providerCalls())
	require.Equal(t, 0, rig.cacheEntries())
	require.Equal(t, 0, rig.slotsUsed("alice"))
}
