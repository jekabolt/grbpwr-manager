package designgen

import (
	"context"
	"database/sql"
	"errors"
	"sync"
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
)

// The whole package is tested against these. NOT ONE TEST IN THIS FILE OPENS A DATABASE, and that
// is a rule rather than a convenience: outside CI the store package's TestMain reads the config
// file's DSN — production's — and drops every table in it.

type fakeStore struct {
	mu sync.Mutex

	claimReturn []entity.DesignRun
	claimErr    error
	claimedWith []string
	revived     int
	reviveErr   error

	getRun    *entity.DesignRun
	getRunErr error

	started    []entity.DesignAttemptStart
	startErr   error
	nextNo     int
	finished   []entity.DesignAttemptFinish
	finishErr  error
	completed  []entity.DesignRunComplete
	completeAs *entity.DesignRun
	completeEr error
	failed     []entity.DesignRunFail
	failErr    error

	// recordedPrompts is what RecordRunPrompt was handed; events is the ORDER the writing verbs
	// were called in — the record-then-spend probes assert on it.
	recordedPrompts []string
	recordErr       error
	events          []string
}

func (f *fakeStore) ClaimRuns(_ context.Context, _ int, _ time.Duration, token string) ([]entity.DesignRun, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.claimedWith = append(f.claimedWith, token)
	if f.claimErr != nil {
		return nil, f.claimErr
	}
	out := f.claimReturn
	f.claimReturn = nil
	return out, nil
}

func (f *fakeStore) ReviveExpiredRuns(context.Context) (int, error) { return f.revived, f.reviveErr }

func (f *fakeStore) GetRun(context.Context, int) (*entity.DesignRun, error) {
	return f.getRun, f.getRunErr
}

func (f *fakeStore) RecordRunPrompt(_ context.Context, _ int, _ string, prompt string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.events = append(f.events, "record_prompt")
	if f.recordErr != nil {
		return f.recordErr
	}
	f.recordedPrompts = append(f.recordedPrompts, prompt)
	return nil
}

func (f *fakeStore) StartAttempt(_ context.Context, req entity.DesignAttemptStart) (*entity.DesignRunAttempt, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.events = append(f.events, "start_attempt")
	f.started = append(f.started, req)
	if f.startErr != nil {
		return nil, f.startErr
	}
	f.nextNo++
	return &entity.DesignRunAttempt{RunId: req.RunId, AttemptNo: f.nextNo, Provider: req.Provider}, nil
}

func (f *fakeStore) FinishAttempt(_ context.Context, req entity.DesignAttemptFinish) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.finished = append(f.finished, req)
	return f.finishErr
}

func (f *fakeStore) CompleteRun(_ context.Context, req entity.DesignRunComplete) (*entity.DesignRun, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.completed = append(f.completed, req)
	if f.completeEr != nil {
		return nil, f.completeEr
	}
	if f.completeAs != nil {
		return f.completeAs, nil
	}
	// The ordinary case: the store adopts exactly what it was handed.
	out := &entity.DesignRun{Id: req.RunId, Status: entity.DesignRunDone}
	for _, o := range req.Outputs {
		out.Pictures = append(out.Pictures, entity.DesignPicture{MediaId: o.MediaId, Ordinal: o.Ordinal})
	}
	return out, nil
}

func (f *fakeStore) FailRun(_ context.Context, req entity.DesignRunFail) (*entity.DesignRun, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.failed = append(f.failed, req)
	if f.failErr != nil {
		return nil, f.failErr
	}
	return &entity.DesignRun{Id: req.RunId, Status: entity.DesignRunFailed}, nil
}

type fakeMedia struct {
	byID map[int]entity.MediaFull
	err  error
}

func (f fakeMedia) GetMediaByIds(_ context.Context, ids []int) (map[int]entity.MediaFull, error) {
	if f.err != nil {
		return nil, f.err
	}
	out := map[int]entity.MediaFull{}
	for _, id := range ids {
		if m, ok := f.byID[id]; ok {
			out[id] = m
		}
	}
	return out, nil
}

type fakeSink struct {
	mu sync.Mutex

	accepts  map[string]bool
	nextID   int
	put      []MintedMedia
	putTypes []string
	dropped  []int
	// failAfter makes the (failAfter+1)-th Put fail, to exercise a storage failure that lands
	// AFTER something was already minted.
	failAfter int
	failWith  error
}

func newFakeSink(types ...string) *fakeSink {
	s := &fakeSink{accepts: map[string]bool{}, failAfter: -1}
	for _, t := range types {
		s.accepts[t] = true
	}
	return s
}

func (f *fakeSink) Accepts(ct string) bool { return f.accepts[normalizeContentType(ct)] }

func (f *fakeSink) Put(_ context.Context, raw []byte, ct, _ string) (MintedMedia, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.failAfter >= 0 && len(f.put) == f.failAfter {
		if f.failWith != nil {
			return MintedMedia{}, f.failWith
		}
		return MintedMedia{}, errStorageFailed
	}
	f.nextID++
	m := MintedMedia{ID: f.nextID, URLs: []string{"https://cdn.example/o/" + ct}}
	f.put = append(f.put, m)
	f.putTypes = append(f.putTypes, ct)
	return m, nil
}

func (f *fakeSink) Drop(_ context.Context, m MintedMedia) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.dropped = append(f.dropped, m.ID)
}

func (f *fakeSink) mintedIDs() []int {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]int, 0, len(f.put))
	for _, m := range f.put {
		out = append(out, m.ID)
	}
	return out
}

type fakeProvider struct {
	name     string
	off      bool
	produces []string

	calls []Job
	out   *Outcome
	err   error
}

func (p *fakeProvider) Name() string { return p.name }

func (p *fakeProvider) Enabled() bool { return !p.off }

func (p *fakeProvider) Produces() []string {
	if p.produces == nil {
		return []string{ContentTypePNG}
	}
	return p.produces
}

func (p *fakeProvider) Execute(_ context.Context, job Job) (*Outcome, error) {
	p.calls = append(p.calls, job)
	return p.out, p.err
}

// fakeAsyncProvider is the shape of the 3D route: a paid submit and a free collect.
type fakeAsyncProvider struct {
	fakeProvider

	collectFor []string
	collectOut *Outcome
	collectErr error
}

func (p *fakeAsyncProvider) Collect(_ context.Context, _ Job, requestID string) (*Outcome, error) {
	p.collectFor = append(p.collectFor, requestID)
	return p.collectOut, p.collectErr
}

// testWorker wires a worker over the fakes with defaults applied.
func testWorker(store runStore, media mediaResolver, sink MediaSink, p Providers) *Worker {
	c := DefaultConfig()
	c.Enabled = true
	applyDefaults(&c)
	if media == nil {
		media = fakeMedia{}
	}
	return newWorker(&c, store, media, sink, p)
}

func testRun(id int, kind string) entity.DesignRun {
	return entity.DesignRun{
		Id: id, TechCardId: 7, Kind: kind, Status: entity.DesignRunRunning,
		RequestedOutputs: 1,
		ClaimToken:       sql.NullString{String: "tok", Valid: true},
	}
}

var errBoom = errors.New("boom")
