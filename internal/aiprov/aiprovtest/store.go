// Package aiprovtest is an in-memory dependency.AI for tests.
//
// THE LEDGER HALF BEHAVES LIKE store/ai, NOT LIKE A RECORDER OF CALLS: the same refusals in
// BeginCall (a zero occurred_at, a malformed day, a key-less row), the same WHERE clauses
// (FinishCall only moves a row still `dispatching`, PriceAcceptedCall only one still `accepted`,
// SweepDispatching only `dispatching` rows older than the cut), the same COALESCE merge (a nil or
// empty field keeps what the row holds) and the same UNIQUE (run_id, attempt_no, call_no). A test
// that passes against this fake therefore exercises the store's rules, which is what a ledger bug
// would break. The config half is inert.
//
// It exists because no test in this repository may open a database: outside CI the store's
// TestMain reads the production DSN and drops every table.
package aiprovtest

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/dependency"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
)

var _ dependency.AI = (*Store)(nil)

// Row is one ai_usage_event as the fake holds it.
type Row struct {
	ID         int64
	Start      entity.AICallStart // CallNo already normalised (0 → 1)
	Status     string
	End        entity.AICallEnd // the merged finalisation; Status mirrors Row.Status
	FinishedAt time.Time
}

// Write is one ledger write the fake received, with the context it ran on — so a test can prove
// a write ran beyond the caller's cancellation and under a deadline.
type Write struct {
	Verb     string // begin | finish | price | sweep
	CtxErr   error  // ctx.Err() at the moment of the write
	Deadline time.Time
	HasDL    bool
}

// Store is the fake. The *Err fields make the matching verb fail (nothing is written).
type Store struct {
	mu     sync.Mutex
	rows   []*Row
	nextID int64
	writes []Write

	BeginErr  error
	FinishErr error
	PriceErr  error
	SweepErr  error
	// Now is the finished_at clock; nil = time.Now.
	Now func() time.Time
}

// Rows returns a copy of every row, in insertion order.
func (s *Store) Rows() []Row {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]Row, 0, len(s.rows))
	for _, r := range s.rows {
		out = append(out, *r)
	}
	return out
}

// Writes returns every write the fake received, in order.
func (s *Store) Writes() []Write {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]Write(nil), s.writes...)
}

// Seed inserts a row as it stands, bypassing BeginCall's clock (a sweep test needs an old row).
func (s *Store) Seed(r Row) int64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.nextID++
	r.ID = s.nextID
	if r.Start.CallNo == 0 {
		r.Start.CallNo = 1
	}
	s.rows = append(s.rows, &r)
	return r.ID
}

func (s *Store) note(ctx context.Context, verb string) {
	w := Write{Verb: verb, CtxErr: ctx.Err()}
	w.Deadline, w.HasDL = ctx.Deadline()
	s.writes = append(s.writes, w)
}

func (s *Store) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

// ───────────────────────── ledger ─────────────────────────

func (s *Store) BeginCall(ctx context.Context, st entity.AICallStart) (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.note(ctx, "begin")
	if s.BeginErr != nil {
		return 0, s.BeginErr
	}
	// store/ai beginCallParams' refusals, word for word in meaning.
	if st.OccurredAt.IsZero() {
		return 0, errors.New("ai ledger: occurred_at is required")
	}
	if _, err := time.Parse("2006-01-02", st.DayLocal); err != nil {
		return 0, fmt.Errorf("ai ledger: day_local %q is not YYYY-MM-DD", st.DayLocal)
	}
	if strings.TrimSpace(st.ProviderKey) == "" || strings.TrimSpace(st.Purpose) == "" {
		return 0, errors.New("ai ledger: provider_key and purpose are required")
	}
	if st.CallNo == 0 {
		st.CallNo = 1
	}
	if st.CallNo < 1 || st.CallNo > 255 {
		return 0, fmt.Errorf("ai ledger: call_no %d out of range", st.CallNo)
	}
	// UNIQUE KEY uq_ai_usage_call (run_id, attempt_no, call_no) — NULLs never collide.
	if st.RunID != nil && st.AttemptNo != nil {
		for _, r := range s.rows {
			if r.Start.RunID != nil && r.Start.AttemptNo != nil && *r.Start.RunID == *st.RunID &&
				*r.Start.AttemptNo == *st.AttemptNo && r.Start.CallNo == st.CallNo {
				return 0, fmt.Errorf("duplicate entry for uq_ai_usage_call (%d, %d, %d)",
					*st.RunID, *st.AttemptNo, st.CallNo)
			}
		}
	}
	s.nextID++
	s.rows = append(s.rows, &Row{ID: s.nextID, Start: st, Status: entity.AICallDispatching})
	return s.nextID, nil
}

func validEnd(end entity.AICallEnd) error {
	if !entity.IsAICallStatus(end.Status) || end.Status == entity.AICallDispatching {
		return fmt.Errorf("ai ledger: %q is not a status a call can end in", end.Status)
	}
	if end.CostSource != "" && !entity.IsAICostSource(end.CostSource) {
		return fmt.Errorf("ai ledger: unknown cost source %q", end.CostSource)
	}
	return nil
}

// merge is aiCallEndSet: status and finished_at always, everything else COALESCEd.
func (s *Store) merge(r *Row, e entity.AICallEnd) {
	m := r.End
	m.Status = e.Status
	if e.ErrorCode != "" {
		m.ErrorCode = e.ErrorCode
	}
	if e.HTTPStatus != nil {
		m.HTTPStatus = e.HTTPStatus
	}
	if e.Engaged != nil {
		m.Engaged = e.Engaged
	}
	if e.RequestID != "" {
		m.RequestID = e.RequestID
	}
	if e.ModelActual != "" {
		m.ModelActual = e.ModelActual
	}
	if e.PromptTokens != nil {
		m.PromptTokens = e.PromptTokens
	}
	if e.CompletionTokens != nil {
		m.CompletionTokens = e.CompletionTokens
	}
	if e.CachedTokens != nil {
		m.CachedTokens = e.CachedTokens
	}
	if e.ReasoningTokens != nil {
		m.ReasoningTokens = e.ReasoningTokens
	}
	if e.Units != nil {
		m.Units = e.Units
	}
	if e.Unit != "" {
		m.Unit = e.Unit
	}
	if e.CostUSD.Valid {
		m.CostUSD = e.CostUSD
	}
	if e.CostSource != "" {
		m.CostSource = e.CostSource
	}
	if e.PriceVersion != "" {
		m.PriceVersion = e.PriceVersion
	}
	if e.LatencyMs != nil {
		m.LatencyMs = e.LatencyMs
	}
	r.End = m
	r.Status = e.Status
	r.FinishedAt = s.now()
}

func (s *Store) FinishCall(ctx context.Context, id int64, end entity.AICallEnd) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.note(ctx, "finish")
	if s.FinishErr != nil {
		return s.FinishErr
	}
	if id < 1 {
		return fmt.Errorf("ai ledger: call id %d must be positive", id)
	}
	if err := validEnd(end); err != nil {
		return err
	}
	for _, r := range s.rows {
		if r.ID == id && r.Status == entity.AICallDispatching {
			s.merge(r, end)
		}
	}
	return nil
}

func (s *Store) PriceAcceptedCall(ctx context.Context, runID, attemptNo, callNo int, end entity.AICallEnd) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.note(ctx, "price")
	if s.PriceErr != nil {
		return s.PriceErr
	}
	if callNo == 0 {
		callNo = 1
	}
	if runID < 1 || attemptNo < 1 || callNo < 1 {
		return fmt.Errorf("ai ledger: run %d attempt %d call %d is not a design call", runID, attemptNo, callNo)
	}
	if end.Status == entity.AICallAccepted {
		return errors.New("ai ledger: pricing an accepted call must move it past accepted")
	}
	if err := validEnd(end); err != nil {
		return err
	}
	for _, r := range s.rows {
		if r.Start.RunID != nil && r.Start.AttemptNo != nil && *r.Start.RunID == runID &&
			*r.Start.AttemptNo == attemptNo && r.Start.CallNo == callNo && r.Status == entity.AICallAccepted {
			s.merge(r, end)
		}
	}
	return nil
}

func (s *Store) SweepDispatching(ctx context.Context, olderThan time.Time) (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.note(ctx, "sweep")
	if s.SweepErr != nil {
		return 0, s.SweepErr
	}
	var n int64
	for _, r := range s.rows {
		if r.Status == entity.AICallDispatching && r.Start.OccurredAt.Before(olderThan) {
			r.Status = entity.AICallUnknown
			r.End.Status = entity.AICallUnknown
			r.End.ErrorCode = entity.AICallErrorSweeper
			r.FinishedAt = s.now()
			n++
		}
	}
	return n, nil
}

// ───────────────────────── config (inert) ─────────────────────────

func (s *Store) GetConfig(context.Context) (*entity.AIConfig, error) { return &entity.AIConfig{}, nil }
func (s *Store) ConfigVersion(context.Context) (uint64, error)       { return 0, nil }
func (s *Store) UpdateProvider(context.Context, string, entity.AIProviderPatch, uint64, string) error {
	return nil
}
func (s *Store) SetProviderKey(context.Context, string, entity.AIKeyKind, []byte, string, string) error {
	return nil
}
func (s *Store) SetDefaults(context.Context, entity.AIDefaultsPatch, uint64, string) error {
	return nil
}
func (s *Store) SetRoute(context.Context, string, []entity.AIRouteCandidate, uint64, string) error {
	return nil
}
func (s *Store) UpsertModel(context.Context, entity.AIModel, string) error { return nil }
func (s *Store) SpendReport(_ context.Context, fromDay, toDay string) (*entity.AISpendReport, error) {
	return &entity.AISpendReport{FromDay: fromDay, ToDay: toDay}, nil
}
func (s *Store) UpsertCostDaily(context.Context, []entity.AICostDaily) error { return nil }
