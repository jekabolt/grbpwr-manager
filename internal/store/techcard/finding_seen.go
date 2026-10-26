package techcard

import (
	"context"
	"fmt"

	"github.com/jekabolt/grbpwr-manager/internal/dependency"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/jekabolt/grbpwr-manager/internal/store/storeutil"
)

// «Seen» marks on construction-audit findings (T9, table tech_card_finding_seen, 0433).
//
// NOT A LEG OF UpdateTechCard and not in its full-replace table list: a mark must survive every
// save of the card, and it must be settable on a released card — it is a note about the audit, not
// card content. The store keeps what the caller sends; whether a mark still matches the finding
// (same fingerprint) is decided on read, by techcardanalysis.ApplySeen.

// ListTechCardFindingSeen returns every mark of the card, stale ones included — at most
// entity.TechCardFindingSeenMaxPerCard, which MarkTechCardFindingSeen keeps.
func (s *Store) ListTechCardFindingSeen(ctx context.Context, techCardID int) ([]entity.TechCardFindingSeen, error) {
	rows, err := storeutil.QueryListNamed[entity.TechCardFindingSeen](ctx, s.DB, `
		SELECT tech_card_id, finding_key, fingerprint, seen_by, seen_at
		FROM tech_card_finding_seen
		WHERE tech_card_id = :tech_card_id
		ORDER BY finding_key`,
		map[string]any{"tech_card_id": techCardID})
	if err != nil {
		return nil, fmt.Errorf("failed to list finding seen marks of tech card %d: %w", techCardID, err)
	}
	return rows, nil
}

// MarkTechCardFindingSeen upserts the mark of one finding with the fingerprint the caller saw.
//
// IDEMPOTENT: marking again with the SAME fingerprint keeps the first seen_by/seen_at (a double
// click does not rewrite who saw it); a NEW fingerprint overwrites all three. MySQL evaluates the
// ON DUPLICATE KEY assignments left to right with already-updated values, so fingerprint is
// assigned LAST — the two IFs compare against the stored one.
//
// BOUNDED: a NEW key on a card that already keeps entity.TechCardFindingSeenMaxPerCard marks is
// refused with entity.ErrTechCardFindingSeenLimit; a key the card has is re-marked at any count.
// Count and insert are one decision: the card row is taken FOR UPDATE first, so two concurrent
// new marks at the ceiling queue on it and the second one counts the first (without it the
// SERIALIZABLE count would still hold, through gap-lock deadlocks and txFunc's retry).
//
// seen_by arrives as an argument: the store cannot know who is asking; the handler stamps it.
// An unknown tech card fails the lock with sql.ErrNoRows (callers map it to NotFound).
func (s *Store) MarkTechCardFindingSeen(ctx context.Context, techCardID int, findingKey, fingerprint, seenBy string) error {
	err := s.txFunc(ctx, func(ctx context.Context, rep dependency.Repository) error {
		db := rep.DB()
		if err := lockTechCardRow(ctx, db, techCardID); err != nil {
			return err
		}
		held, err := storeutil.QueryNamedOne[struct {
			Total int `db:"total"`
			Same  int `db:"same"`
		}](ctx, db, `
			SELECT COUNT(*) AS total, COUNT(CASE WHEN finding_key = :finding_key THEN 1 END) AS same
			FROM tech_card_finding_seen
			WHERE tech_card_id = :tech_card_id`,
			map[string]any{"tech_card_id": techCardID, "finding_key": findingKey})
		if err != nil {
			return err
		}
		if held.Same == 0 && held.Total >= entity.TechCardFindingSeenMaxPerCard {
			return entity.ErrTechCardFindingSeenLimit
		}
		return storeutil.ExecNamed(ctx, db, `
			INSERT INTO tech_card_finding_seen (tech_card_id, finding_key, fingerprint, seen_by, seen_at)
			VALUES (:tech_card_id, :finding_key, :fingerprint, :seen_by, CURRENT_TIMESTAMP(6))
			ON DUPLICATE KEY UPDATE
				seen_by = IF(fingerprint = VALUES(fingerprint), seen_by, VALUES(seen_by)),
				seen_at = IF(fingerprint = VALUES(fingerprint), seen_at, VALUES(seen_at)),
				fingerprint = VALUES(fingerprint)`,
			map[string]any{
				"tech_card_id": techCardID,
				"finding_key":  findingKey,
				"fingerprint":  fingerprint,
				"seen_by":      seenBy,
			})
	})
	if err != nil {
		return fmt.Errorf("failed to mark finding seen on tech card %d: %w", techCardID, err)
	}
	return nil
}

// UnmarkTechCardFindingSeen deletes the mark of one finding. Deleting a mark that is not there is
// not an error — unmark is idempotent like mark.
func (s *Store) UnmarkTechCardFindingSeen(ctx context.Context, techCardID int, findingKey string) error {
	err := storeutil.ExecNamed(ctx, s.DB, `
		DELETE FROM tech_card_finding_seen
		WHERE tech_card_id = :tech_card_id AND finding_key = :finding_key`,
		map[string]any{"tech_card_id": techCardID, "finding_key": findingKey})
	if err != nil {
		return fmt.Errorf("failed to unmark finding seen on tech card %d: %w", techCardID, err)
	}
	return nil
}
