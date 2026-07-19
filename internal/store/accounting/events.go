package accounting

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/jekabolt/grbpwr-manager/internal/store/storeutil"
)

// EnqueueEvent appends an order outbox event, marshalling ev.Payload (any → JSON) itself so hot-path
// producers never touch json.RawMessage. A duplicate (event_type, source_key) is a no-op
// (ON DUPLICATE KEY UPDATE id = id) — a retried producer is safe. A marshal error is returned so a
// producer inside a payment transaction propagates it (the Tx rolls back rather than losing the
// event silently).
func (s *Store) EnqueueEvent(ctx context.Context, ev entity.AcctEventInsert) error {
	payload, err := json.Marshal(ev.Payload)
	if err != nil {
		return fmt.Errorf("accounting: marshal event payload (%s/%s): %w", ev.EventType, ev.SourceKey, err)
	}
	if err := storeutil.ExecNamed(ctx, s.DB, `
		INSERT INTO acct_event (event_type, source_key, payload, occurred_at)
		VALUES (:event_type, :source_key, :payload, :occurred_at)
		ON DUPLICATE KEY UPDATE id = id`,
		map[string]any{
			"event_type":  string(ev.EventType),
			"source_key":  ev.SourceKey,
			"payload":     string(payload),
			"occurred_at": ev.OccurredAt.UTC(),
		}); err != nil {
		return fmt.Errorf("accounting: enqueue event %s/%s: %w", ev.EventType, ev.SourceKey, err)
	}
	return nil
}

// ListPendingEvents returns due, unprocessed events (processed_at IS NULL AND the retry backoff has
// elapsed), oldest first, up to limit.
func (s *Store) ListPendingEvents(ctx context.Context, limit int) ([]entity.AcctEvent, error) {
	if limit <= 0 {
		limit = defaultListLimit
	}
	events, err := storeutil.QueryListNamed[entity.AcctEvent](ctx, s.DB, `
		SELECT id, event_type, source_key, payload, occurred_at, created_at,
		       processed_at, attempts, next_retry_at, last_error
		FROM acct_event
		WHERE processed_at IS NULL
		  AND (next_retry_at IS NULL OR next_retry_at <= NOW())
		ORDER BY id
		LIMIT :limit`, map[string]any{"limit": limit})
	if err != nil {
		return nil, fmt.Errorf("accounting: list pending events: %w", err)
	}
	return events, nil
}

// MarkEventProcessed marks an event done. It does not clear last_error, so a skip reason recorded by
// a prior MarkEventFailed (e.g. "manual entry required") survives as the disposition note.
func (s *Store) MarkEventProcessed(ctx context.Context, id int64) error {
	if err := storeutil.ExecNamed(ctx, s.DB,
		`UPDATE acct_event SET processed_at = NOW() WHERE id = :id`,
		map[string]any{"id": id}); err != nil {
		return fmt.Errorf("accounting: mark event %d processed: %w", id, err)
	}
	return nil
}

// MarkEventFailed bumps attempts, records errMsg, and schedules the next retry at NOW() + retryAfter
// (explicit backoff — the event returns to the pending window once it elapses).
func (s *Store) MarkEventFailed(ctx context.Context, id int64, errMsg string, retryAfter time.Duration) error {
	secs := int64(retryAfter / time.Second)
	if secs < 0 {
		secs = 0
	}
	if err := storeutil.ExecNamed(ctx, s.DB, `
		UPDATE acct_event
		SET attempts = attempts + 1,
		    last_error = :err,
		    next_retry_at = DATE_ADD(NOW(), INTERVAL :secs SECOND)
		WHERE id = :id`,
		map[string]any{"id": id, "err": errMsg, "secs": secs}); err != nil {
		return fmt.Errorf("accounting: mark event %d failed: %w", id, err)
	}
	return nil
}
