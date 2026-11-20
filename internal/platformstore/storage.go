package platformstore

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/jmoiron/sqlx"
)

// TEAM STORAGE (lane X11): bytes in use per team and the team's cap (team_storage, migration 0014).
// cap_bytes NULL = the default cap of this installation (TEAM_STORAGE_CAP_GB), 0 = no cap.

// DefaultStorageCapGB is the cap of a team with no cap of its own when TEAM_STORAGE_CAP_GB is unset.
const DefaultStorageCapGB = 20

const gib = int64(1) << 30

// StorageCapDefault reads StorageCapGB (TEAM_STORAGE_CAP_GB) as bytes: a positive number of GB
// (decimals allowed), "0" / "off" = no cap; empty or unreadable = DefaultStorageCapGB (a typo never
// turns the cap off).
func (c Config) StorageCapDefault() int64 {
	v := strings.ToLower(strings.TrimSpace(c.StorageCapGB))
	if v == "off" || v == "0" {
		return 0
	}
	if f, err := strconv.ParseFloat(v, 64); err == nil && f > 0 && f < 1e6 {
		return int64(f * float64(gib))
	}
	return DefaultStorageCapGB * gib
}

// TeamStorage is one team's row: Used bytes, Cap as stored (NULL = the default).
type TeamStorage struct {
	Used int64
	Cap  sql.NullInt64
}

// EffectiveCap is the cap that applies: the team's own, else def; 0 = no cap.
func (t TeamStorage) EffectiveCap(def int64) int64 {
	if t.Cap.Valid {
		return t.Cap.Int64
	}
	return def
}

const reserveSQL = `UPDATE team_storage SET used_bytes = used_bytes + ?
WHERE team_id = ? AND (COALESCE(cap_bytes, ?) = 0 OR used_bytes + ? <= COALESCE(cap_bytes, ?))`

// ReserveStorage admits n more bytes for the team in one atomic statement (two parallel uploads
// cannot both pass the last free bytes). n == 0 (a stream, an empty file) is admitted while the team
// is strictly under its cap. false = refused; a team without a row gets one (used 0) first.
func (s *Store) ReserveStorage(ctx context.Context, teamID, n, defCap int64) (bool, error) {
	if n < 0 {
		return false, fmt.Errorf("platformstore: reserve storage: negative size %d", n)
	}
	for attempt := 0; attempt < 2; attempt++ {
		if n == 0 {
			var row struct {
				Used int64         `db:"used_bytes"`
				Cap  sql.NullInt64 `db:"cap_bytes"`
			}
			err := s.db.GetContext(ctx, &row, `SELECT used_bytes, cap_bytes FROM team_storage WHERE team_id = ?`, teamID)
			if err == nil {
				c := TeamStorage{Used: row.Used, Cap: row.Cap}.EffectiveCap(defCap)
				return c == 0 || row.Used < c, nil
			}
			if !errors.Is(err, sql.ErrNoRows) {
				return false, fmt.Errorf("platformstore: read storage: %w", err)
			}
		} else {
			res, err := s.db.ExecContext(ctx, reserveSQL, n, teamID, defCap, n, defCap)
			if err != nil {
				return false, fmt.Errorf("platformstore: reserve storage: %w", err)
			}
			if k, _ := res.RowsAffected(); k > 0 {
				return true, nil
			}
		}
		created, err := s.ensureStorageRow(ctx, teamID)
		if err != nil {
			return false, err
		}
		if !created {
			return false, nil // the row exists: the team is at its cap
		}
	}
	return false, nil
}

func (s *Store) ensureStorageRow(ctx context.Context, teamID int64) (bool, error) {
	res, err := s.db.ExecContext(ctx, `INSERT IGNORE INTO team_storage (team_id) VALUES (?)`, teamID)
	if err != nil {
		return false, fmt.Errorf("platformstore: storage row: %w", err)
	}
	k, _ := res.RowsAffected()
	return k > 0, nil
}

// AddStorage moves the team's count by delta, never below zero.
func (s *Store) AddStorage(ctx context.Context, teamID, delta int64) error {
	if delta == 0 {
		return nil
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO team_storage (team_id, used_bytes) VALUES (?, GREATEST(?, 0))
ON DUPLICATE KEY UPDATE used_bytes = GREATEST(used_bytes + ?, 0)`, teamID, delta, delta)
	if err != nil {
		return fmt.Errorf("platformstore: add storage: %w", err)
	}
	return nil
}

// SetStorageCap stores the team's own cap: capBytes > 0, or 0 = no cap.
func (s *Store) SetStorageCap(ctx context.Context, teamID, capBytes int64) error {
	if capBytes < 0 {
		return fmt.Errorf("platformstore: storage cap: negative %d", capBytes)
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO team_storage (team_id, cap_bytes) VALUES (?, ?)
ON DUPLICATE KEY UPDATE cap_bytes = ?`, teamID, capBytes, capBytes)
	if err != nil {
		return fmt.Errorf("platformstore: set storage cap: %w", err)
	}
	return nil
}

// ClearStorageCap puts the team back on the default cap (cap_bytes NULL = TEAM_STORAGE_CAP_GB).
func (s *Store) ClearStorageCap(ctx context.Context, teamID int64) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO team_storage (team_id, cap_bytes) VALUES (?, NULL)
ON DUPLICATE KEY UPDATE cap_bytes = NULL`, teamID)
	if err != nil {
		return fmt.Errorf("platformstore: clear storage cap: %w", err)
	}
	return nil
}

// StorageFor reads the given teams' rows; a team without a row is absent (used 0, default cap).
func (s *Store) StorageFor(ctx context.Context, teamIDs []int64) (map[int64]TeamStorage, error) {
	out := make(map[int64]TeamStorage, len(teamIDs))
	if len(teamIDs) == 0 {
		return out, nil
	}
	q, args, err := sqlx.In(`SELECT team_id, used_bytes, cap_bytes FROM team_storage WHERE team_id IN (?)`, teamIDs)
	if err != nil {
		return nil, err
	}
	var rows []struct {
		TeamID int64         `db:"team_id"`
		Used   int64         `db:"used_bytes"`
		Cap    sql.NullInt64 `db:"cap_bytes"`
	}
	if err := s.db.SelectContext(ctx, &rows, s.db.Rebind(q), args...); err != nil {
		return nil, fmt.Errorf("platformstore: read storage: %w", err)
	}
	for _, r := range rows {
		out[r.TeamID] = TeamStorage{Used: r.Used, Cap: r.Cap}
	}
	return out, nil
}

// StorageTeam is a team the recount attributes bucket keys to.
type StorageTeam struct {
	ID      int64          `db:"id"`
	FileKey sql.NullString `db:"file_key"`
}

// StorageTeams lists every team with its file key (deleted ones too: their files stay until purge).
func (s *Store) StorageTeams(ctx context.Context) ([]StorageTeam, error) {
	var out []StorageTeam
	if err := s.db.SelectContext(ctx, &out, `SELECT id, file_key FROM team ORDER BY id`); err != nil {
		return nil, fmt.Errorf("platformstore: storage teams: %w", err)
	}
	return out, nil
}

// SetRecountedStorage writes the recount: used_bytes of every given team, recounted_at = now.
func (s *Store) SetRecountedStorage(ctx context.Context, used map[int64]int64, now time.Time) error {
	for team, n := range used {
		if n < 0 {
			n = 0
		}
		if _, err := s.db.ExecContext(ctx, `INSERT INTO team_storage (team_id, used_bytes, recounted_at) VALUES (?, ?, ?)
ON DUPLICATE KEY UPDATE used_bytes = ?, recounted_at = ?`, team, n, now, n, now); err != nil {
			return fmt.Errorf("platformstore: recount storage: %w", err)
		}
	}
	return nil
}

// LockStorageRecount takes the recount's lock (GET_LOCK, named after this control-plane database:
// lock names are server-wide and beta / prod share a cluster). One holder across app instances; ok
// false = another instance is recounting. The lock lives on one pinned connection; release frees it.
func (s *Store) LockStorageRecount(ctx context.Context) (release func(), ok bool, err error) {
	conn, err := s.db.Conn(ctx)
	if err != nil {
		return nil, false, fmt.Errorf("platformstore: recount lock: %w", err)
	}
	var got sql.NullInt64
	if err := conn.QueryRowContext(ctx, `SELECT GET_LOCK(CONCAT(DATABASE(), ':team_storage_recount'), 0)`).Scan(&got); err != nil {
		_ = conn.Close()
		return nil, false, fmt.Errorf("platformstore: recount lock: %w", err)
	}
	if !got.Valid || got.Int64 != 1 {
		_ = conn.Close()
		return nil, false, nil
	}
	return func() {
		rctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
		defer cancel()
		_, _ = conn.ExecContext(rctx, `SELECT RELEASE_LOCK(CONCAT(DATABASE(), ':team_storage_recount'))`)
		_ = conn.Close() // a failed release still frees the lock with the session
	}, true, nil
}
