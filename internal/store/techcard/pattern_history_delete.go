package techcard

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/jekabolt/grbpwr-manager/internal/dependency"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/jekabolt/grbpwr-manager/internal/patternrefs"
	"github.com/jekabolt/grbpwr-manager/internal/store/storeutil"
)

type patternRevisionDeleteRow struct {
	URL             string         `db:"url"`
	SnapshotID      sql.NullInt64  `db:"snapshot_id"`
	SnapshotReason  sql.NullString `db:"snapshot_reason"`
	LockVersion     int            `db:"lock_version"`
	ApprovalState   string         `db:"approval_state"`
	CreateRequestID sql.NullString `db:"create_request_id"`
}

// DeleteTechCardPatternRevisionAndListOrphanedPatternURLs deletes one history row under the same
// lifecycle and optimistic-lock rules as UpdateTechCard. Snapshot retention, the journal entry and
// reference accounting are part of the transaction; external object deletion belongs to the caller
// after commit.
func (s *Store) DeleteTechCardPatternRevisionAndListOrphanedPatternURLs(
	ctx context.Context,
	techCardID int,
	historyID int64,
	expectedLockVersion int,
	actor string,
) ([]string, error) {
	var orphanedPatternURLs []string
	err := s.txFunc(ctx, func(ctx context.Context, rep dependency.Repository) error {
		orphanedPatternURLs = nil
		row, err := storeutil.QueryNamedOne[patternRevisionDeleteRow](ctx, rep.DB(), `
			SELECT h.url, h.snapshot_id, snap.reason AS snapshot_reason,
			       tc.lock_version, tc.approval_state, tc.create_request_id
			FROM tech_card_size_pattern_history h
			JOIN tech_card tc ON tc.id = h.tech_card_id
			LEFT JOIN tech_card_snapshot snap
			       ON snap.id = h.snapshot_id AND snap.tech_card_id = h.tech_card_id
			WHERE h.id = :history_id AND h.tech_card_id = :tech_card_id
			FOR UPDATE`, map[string]any{"history_id": historyID, "tech_card_id": techCardID})
		if err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return sql.ErrNoRows
			}
			return fmt.Errorf("load tech card pattern revision for delete: %w", err)
		}
		if entity.IsTechCardSystemKey(row.CreateRequestID.String) {
			return entity.ErrTechCardSystem
		}
		if row.ApprovalState == string(entity.TechCardApprovalReleased) {
			return entity.ErrTechCardReleased
		}
		if row.LockVersion != expectedLockVersion {
			return entity.ErrTechCardConflict
		}

		liveRows, err := storeutil.QueryListNamed[struct {
			URL string `db:"url"`
		}](ctx, rep.DB(), `
			SELECT url FROM tech_card_size_pattern WHERE tech_card_id = :tech_card_id`,
			map[string]any{"tech_card_id": techCardID})
		if err != nil {
			return fmt.Errorf("load current tech card pattern urls: %w", err)
		}
		liveURLs := make([]string, 0, len(liveRows))
		for _, live := range liveRows {
			liveURLs = append(liveURLs, live.URL)
		}
		if patternrefs.ObjectURLReferenced(row.URL, liveURLs) {
			return entity.ErrTechCardPatternRevisionCurrent
		}

		updated, err := storeutil.ExecNamedRows(ctx, rep.DB(), `
			UPDATE tech_card
			SET lock_version = lock_version + 1, updated_by = :updated_by
			WHERE id = :tech_card_id AND lock_version = :expected_lock_version`, map[string]any{
			"tech_card_id": techCardID, "expected_lock_version": expectedLockVersion,
			"updated_by": actor,
		})
		if err != nil {
			return fmt.Errorf("bump tech card after pattern history delete: %w", err)
		}
		if updated == 0 {
			return entity.ErrTechCardConflict
		}

		deleted, err := storeutil.ExecNamedRows(ctx, rep.DB(), `
			DELETE FROM tech_card_size_pattern_history
			WHERE id = :history_id AND tech_card_id = :tech_card_id`,
			map[string]any{"history_id": historyID, "tech_card_id": techCardID})
		if err != nil {
			return fmt.Errorf("delete tech card pattern revision: %w", err)
		}
		if deleted == 0 {
			return sql.ErrNoRows
		}

		if row.SnapshotID.Valid && row.SnapshotID.Int64 > 0 && row.SnapshotReason.Valid {
			remainingRefs, err := storeutil.QueryCountNamed(ctx, rep.DB(), `
				SELECT COUNT(*) FROM tech_card_size_pattern_history WHERE snapshot_id = :snapshot_id`,
				map[string]any{"snapshot_id": row.SnapshotID.Int64})
			if err != nil {
				return fmt.Errorf("count remaining pattern history snapshot references: %w", err)
			}
			if patternrefs.SnapshotCanBeDeleted(row.SnapshotReason.String, remainingRefs) {
				if err := storeutil.ExecNamed(ctx, rep.DB(), `
					DELETE FROM tech_card_snapshot
					WHERE id = :snapshot_id AND tech_card_id = :tech_card_id`, map[string]any{
					"snapshot_id": row.SnapshotID.Int64, "tech_card_id": techCardID,
				}); err != nil {
					return fmt.Errorf("delete unreferenced tech card snapshot: %w", err)
				}
			}
		}

		if err := appendTechCardRevision(ctx, rep.DB(), techCardID, actor, "pieces", "updated",
			fmt.Sprintf("pattern history revision %d deleted", historyID)); err != nil {
			return err
		}
		orphanedPatternURLs, err = storeutil.UnreferencedPatternObjectURLs(ctx, rep.DB(), []string{row.URL})
		return err
	})
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) || errors.Is(err, entity.ErrTechCardConflict) ||
			errors.Is(err, entity.ErrTechCardReleased) || errors.Is(err, entity.ErrTechCardSystem) ||
			errors.Is(err, entity.ErrTechCardPatternRevisionCurrent) {
			return nil, err
		}
		return nil, fmt.Errorf("can't delete tech card pattern revision: %w", err)
	}
	return orphanedPatternURLs, nil
}
