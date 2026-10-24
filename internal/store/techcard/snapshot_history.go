package techcard

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/dependency"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/jekabolt/grbpwr-manager/internal/store/storeutil"
)

type autosaveSnapshotRow struct {
	Id           int64          `db:"id"`
	ChangedPaths sql.NullString `db:"changed_paths"`
	Pinned       bool           `db:"pinned"`
}

// appendOrMergeAutosaveSnapshot creates this bucket's immutable pre-image, or only merges the
// later saves' section labels into the already-existing row. UpdateTechCard calls it after its
// optimistic header UPDATE has acquired the card row, so two writers cannot both create the same
// logical (card, session, bucket) row even though the additive migration intentionally uses a
// non-unique index.
func appendOrMergeAutosaveSnapshot(
	ctx context.Context,
	db dependency.DB,
	id, lockVersion int,
	now time.Time,
	tc *entity.TechCardInsert,
) (int64, error) {
	if tc == nil || strings.TrimSpace(tc.SnapshotSaveSessionId) == "" {
		return 0, fmt.Errorf("autosave snapshot requires a save session id")
	}
	if tc.SnapshotJSON == "" {
		return 0, fmt.Errorf("autosave snapshot requires a pre-update card snapshot")
	}
	bucket := techCardSnapshotBucket(now)
	row, err := storeutil.QueryNamedOne[autosaveSnapshotRow](ctx, db, `
		SELECT id, CAST(changed_paths AS CHAR) AS changed_paths, pinned
		FROM tech_card_snapshot
		WHERE tech_card_id = :tech_card_id
		  AND save_session_id = :save_session_id
		  AND bucket_start = :bucket_start
		ORDER BY id DESC
		LIMIT 1`, map[string]any{
		"tech_card_id": id, "save_session_id": tc.SnapshotSaveSessionId, "bucket_start": bucket,
	})
	if err == nil {
		existing, parseErr := decodeSnapshotChanges(row.ChangedPaths)
		if parseErr != nil {
			return 0, fmt.Errorf("decode autosave snapshot %d changed paths: %w", row.Id, parseErr)
		}
		merged := mergeSnapshotChanges(existing, tc.SnapshotChangedPaths)
		changedPaths, marshalErr := json.Marshal(merged)
		if marshalErr != nil {
			return 0, fmt.Errorf("encode autosave snapshot changed paths: %w", marshalErr)
		}
		if err := storeutil.ExecNamed(ctx, db, `
			UPDATE tech_card_snapshot
			SET changed_paths = :changed_paths,
			    pinned = pinned OR :pinned
			WHERE id = :snapshot_id AND tech_card_id = :tech_card_id`, map[string]any{
			"changed_paths": string(changedPaths), "pinned": tc.SnapshotPinned,
			"snapshot_id": row.Id, "tech_card_id": id,
		}); err != nil {
			return 0, fmt.Errorf("merge autosave snapshot: %w", err)
		}
		return row.Id, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return 0, fmt.Errorf("find autosave snapshot bucket: %w", err)
	}

	changedPaths, err := json.Marshal(mergeSnapshotChanges(nil, tc.SnapshotChangedPaths))
	if err != nil {
		return 0, fmt.Errorf("encode autosave snapshot changed paths: %w", err)
	}
	snapshotID, err := storeutil.ExecNamedLastId(ctx, db, `
		INSERT INTO tech_card_snapshot
			(tech_card_id, lock_version, reason, snapshot, created_by,
			 save_session_id, bucket_start, changed_paths, pinned)
		VALUES
			(:tech_card_id, :lock_version, 'autosave', :snapshot, :created_by,
			 :save_session_id, :bucket_start, :changed_paths, :pinned)`, map[string]any{
		"tech_card_id": id, "lock_version": lockVersion, "snapshot": tc.SnapshotJSON,
		"created_by":      sql.NullString{String: tc.UpdatedBy, Valid: tc.UpdatedBy != ""},
		"save_session_id": tc.SnapshotSaveSessionId, "bucket_start": bucket,
		"changed_paths": string(changedPaths), "pinned": tc.SnapshotPinned,
	})
	if err != nil {
		return 0, fmt.Errorf("append autosave tech card snapshot: %w", err)
	}
	if snapshotID <= 0 {
		return 0, fmt.Errorf("autosave tech card snapshot insert returned no id")
	}
	if err := pruneTechCardAutosaveSnapshots(ctx, db, id); err != nil {
		return 0, err
	}
	return int64(snapshotID), nil
}

func pruneTechCardAutosaveSnapshots(ctx context.Context, db dependency.DB, techCardID int) error {
	// Keep the union of both retention windows: the newest 100 unpinned autosaves, plus every
	// unpinned autosave younger than 180 days. A pattern-history reference is a hard exemption even
	// after both windows expire. Explicit and restore snapshots are outside this DELETE by reason.
	if err := storeutil.ExecNamed(ctx, db, `
		DELETE snap
		FROM tech_card_snapshot snap
		LEFT JOIN tech_card_size_pattern_history history ON history.snapshot_id = snap.id
		LEFT JOIN (
			SELECT id FROM (
				SELECT id
				FROM tech_card_snapshot
				WHERE tech_card_id = :tech_card_id
				  AND reason = 'autosave'
				  AND pinned = FALSE
				ORDER BY created_at DESC, id DESC
				LIMIT 100
			) newest_autosave_ids
		) keep_snapshot ON keep_snapshot.id = snap.id
		WHERE snap.tech_card_id = :tech_card_id
		  AND snap.reason = 'autosave'
		  AND snap.pinned = FALSE
		  AND snap.created_at < UTC_TIMESTAMP(6) - INTERVAL 180 DAY
		  AND history.snapshot_id IS NULL
		  AND keep_snapshot.id IS NULL`, map[string]any{"tech_card_id": techCardID}); err != nil {
		return fmt.Errorf("prune tech card autosave snapshots: %w", err)
	}
	return nil
}

func decodeSnapshotChanges(raw sql.NullString) ([]string, error) {
	if !raw.Valid || strings.TrimSpace(raw.String) == "" || strings.TrimSpace(raw.String) == "null" {
		return nil, nil
	}
	var changes []string
	if err := json.Unmarshal([]byte(raw.String), &changes); err != nil {
		return nil, err
	}
	return mergeSnapshotChanges(nil, changes), nil
}

type techCardSnapshotListRow struct {
	Id           int64          `db:"id"`
	TechCardId   int            `db:"tech_card_id"`
	LockVersion  int            `db:"lock_version"`
	Reason       string         `db:"reason"`
	CreatedBy    sql.NullString `db:"created_by"`
	ChangedPaths sql.NullString `db:"changed_paths"`
	Pinned       bool           `db:"pinned"`
	CreatedAt    time.Time      `db:"created_at"`
}

// ListTechCardSnapshots returns history metadata only; snapshot JSON is intentionally absent from
// both the SELECT and the entity rows so opening the save chip stays cheap for large cards.
func (s *Store) ListTechCardSnapshots(
	ctx context.Context,
	techCardID int,
	beforeSnapshotID int64,
	limit int,
) ([]entity.TechCardSnapshot, error) {
	if _, err := storeutil.QueryNamedOne[struct {
		Id int `db:"id"`
	}](ctx, s.DB, `SELECT id FROM tech_card WHERE id = :tech_card_id`,
		map[string]any{"tech_card_id": techCardID}); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, sql.ErrNoRows
		}
		return nil, fmt.Errorf("check tech card before listing snapshots: %w", err)
	}
	if limit <= 0 {
		limit = techCardSnapshotKeepCount
	}
	if limit > techCardSnapshotKeepCount+1 {
		limit = techCardSnapshotKeepCount + 1
	}
	rows, err := storeutil.QueryListNamed[techCardSnapshotListRow](ctx, s.DB, `
		SELECT id, tech_card_id, lock_version, reason, created_by,
		       CAST(changed_paths AS CHAR) AS changed_paths, pinned, created_at
		FROM tech_card_snapshot
		WHERE tech_card_id = :tech_card_id
		  AND (:before_snapshot_id <= 0 OR id < :before_snapshot_id)
		ORDER BY id DESC
		LIMIT :limit`, map[string]any{
		"tech_card_id": techCardID, "before_snapshot_id": beforeSnapshotID, "limit": limit,
	})
	if err != nil {
		return nil, fmt.Errorf("list tech card snapshots: %w", err)
	}
	out := make([]entity.TechCardSnapshot, 0, len(rows))
	for _, row := range rows {
		changes, err := decodeSnapshotChanges(row.ChangedPaths)
		if err != nil {
			return nil, fmt.Errorf("decode tech card snapshot %d changed paths: %w", row.Id, err)
		}
		out = append(out, entity.TechCardSnapshot{
			Id: row.Id, TechCardId: row.TechCardId, LockVersion: row.LockVersion,
			Reason: row.Reason, CreatedBy: row.CreatedBy, ChangedPaths: changes,
			// Restore snapshots created before 0432 predate the pinned column but have always been
			// durable by reason. Surface that existing invariant honestly in the light list.
			Pinned:    row.Pinned || strings.HasPrefix(strings.TrimSpace(row.Reason), "restore:"),
			CreatedAt: row.CreatedAt,
		})
	}
	return out, nil
}
