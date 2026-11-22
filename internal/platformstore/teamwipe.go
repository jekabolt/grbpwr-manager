package platformstore

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/tenant"
	"github.com/jmoiron/sqlx"
)

// DAY-30 WIPE OF A DELETED TEAM (teams D3, lanes/D3-design.md).
//
// The team row stays as a tombstone; everything behind it goes. The point of no return is one
// control-plane transaction under the team row lock (ClaimTeamWipe): purge_at, db_name, file_key and
// closed_with_user_id go to NULL and a team_wipe row records what was taken. From then on nothing can
// restore the team (RestoreTeam / restoreTx refuse purge_at NULL), route to it (the pool refuses an
// empty database name), migrate it (SchemaTeams skips it) or write a file for it (the bucket refuses
// an empty file key). The worker (internal/teamwipe) then deletes the files, drops the database and
// finishes the control-plane rows (FinishTeamWipe). Team #1 is refused at every step, in SQL and in Go.

// ErrTeamOne: a wipe step was asked for team #1. Never wiped, whatever its row says.
var ErrTeamOne = errors.New("platformstore: team #1 is never wiped")

// ErrWipeNotReady: FinishTeamWipe was called before the files and the database were done.
var ErrWipeNotReady = errors.New("platformstore: team wipe steps not done")

// WipeSlugHold is how long the address of a wiped team stays held (owner, 20.11: 90 days, as a
// renamed address): a team_slug_history row for it redirecting until finalize + WipeSlugHold.
const WipeSlugHold = SlugRedirect

// wipeErrMax is the width of team_wipe.last_error.
const wipeErrMax = 500

// TeamWipe is one team_wipe row: a claimed team and how far its wipe got.
type TeamWipe struct {
	TeamID         int64          `db:"team_id"`
	Slug           string         `db:"slug"`
	DBName         sql.NullString `db:"db_name"`
	FileKey        sql.NullString `db:"file_key"`
	PurgeAt        time.Time      `db:"purge_at"`
	StartedAt      time.Time      `db:"started_at"`
	FilesDoneAt    sql.NullTime   `db:"files_done_at"`
	DBDroppedAt    sql.NullTime   `db:"db_dropped_at"`
	FinishedAt     sql.NullTime   `db:"finished_at"`
	Attempts       int            `db:"attempts"`
	NextAttemptAt  time.Time      `db:"next_attempt_at"`
	LastError      sql.NullString `db:"last_error"`
	ObjectsDeleted int64          `db:"objects_deleted"`
}

// FilesPending: the team had a file key and its prefix is not yet empty.
func (w TeamWipe) FilesPending() bool {
	return w.FileKey.Valid && w.FileKey.String != "" && !w.FilesDoneAt.Valid
}

// DBPending: the team had a database and it is not yet dropped.
func (w TeamWipe) DBPending() bool {
	return w.DBName.Valid && w.DBName.String != "" && !w.DBDroppedAt.Valid
}

const teamWipeColumns = `team_id, slug, db_name, file_key, purge_at, started_at, files_done_at, db_dropped_at,
finished_at, attempts, next_attempt_at, last_error, objects_deleted`

// DueTeamWipes lists up to limit teams whose wipe is due and not yet claimed: deleted, purge_at
// passed on the database clock, never team #1. Unlocked: ClaimTeamWipe re-checks under the row lock.
func (s *Store) DueTeamWipes(ctx context.Context, limit int) ([]int64, error) {
	var ids []int64
	if err := s.db.SelectContext(ctx, &ids, `SELECT id FROM team
WHERE status = 'deleted' AND purge_at IS NOT NULL AND purge_at <= UTC_TIMESTAMP(3) AND id <> ?
ORDER BY purge_at, id LIMIT ?`, tenant.TeamOneID, limit); err != nil {
		return nil, fmt.Errorf("platformstore: due team wipes: %w", err)
	}
	out := ids[:0]
	for _, id := range ids {
		if id != tenant.TeamOneID {
			out = append(out, id)
		}
	}
	return out, nil
}

// OpenTeamWipes lists up to limit claimed, unfinished wipes whose next attempt is due, oldest first.
func (s *Store) OpenTeamWipes(ctx context.Context, limit int) ([]TeamWipe, error) {
	var out []TeamWipe
	if err := s.db.SelectContext(ctx, &out, `SELECT `+teamWipeColumns+` FROM team_wipe
WHERE finished_at IS NULL AND next_attempt_at <= UTC_TIMESTAMP(3) AND team_id <> ?
ORDER BY started_at, team_id LIMIT ?`, tenant.TeamOneID, limit); err != nil {
		return nil, fmt.Errorf("platformstore: open team wipes: %w", err)
	}
	return out, nil
}

// TeamWipeState reads one team_wipe row (ok false when the team was never claimed).
func (s *Store) TeamWipeState(ctx context.Context, teamID int64) (TeamWipe, bool, error) {
	var w TeamWipe
	if err := s.db.GetContext(ctx, &w, `SELECT `+teamWipeColumns+` FROM team_wipe WHERE team_id = ?`, teamID); err != nil {
		if isNoRows(err) {
			return TeamWipe{}, false, nil
		}
		return TeamWipe{}, false, fmt.Errorf("platformstore: team wipe state: %w", err)
	}
	return w, true, nil
}

// ClaimTeamWipe is the point of no return of one team's wipe. One transaction in lock order: the
// team row FOR UPDATE → re-check (not team #1, status deleted, purge_at set and passed on the
// database clock) → when the team closed with an account, that account FOR SHARE: the claim waits
// (claimed false) while the account is deleted and not yet purged, so a team and the account it
// closed with never split → INSERT team_wipe (already claimed: claimed false, nothing written) →
// the team row's purge_at, db_name, file_key and closed_with_user_id to NULL → the claimed spare_db
// row of its database deleted (ReferenceSpare must never name a dropped database) → the DeleteTeam
// revokes again (sessions, invites, hand-offs; normally no-ops) → audit team_wipe_start. The
// directory entry is dropped. claimed false with a nil error: not due, waiting, or claimed before.
func (s *Store) ClaimTeamWipe(ctx context.Context, teamID int64, now time.Time) (TeamWipe, bool, error) {
	if teamID == tenant.TeamOneID {
		return TeamWipe{}, false, ErrTeamOne
	}
	now = now.UTC()
	var (
		w       TeamWipe
		claimed bool
	)
	err := s.inTx(ctx, func(tx *sqlx.Tx) error {
		var cur struct {
			ID         int64          `db:"id"`
			Slug       string         `db:"slug"`
			Status     string         `db:"status"`
			DBName     sql.NullString `db:"db_name"`
			FileKey    sql.NullString `db:"file_key"`
			PurgeAt    sql.NullTime   `db:"purge_at"`
			Due        sql.NullBool   `db:"due"`
			ClosedWith sql.NullInt64  `db:"closed_with_user_id"`
		}
		if err := tx.GetContext(ctx, &cur, `SELECT id, slug, status, db_name, file_key, purge_at,
purge_at <= UTC_TIMESTAMP(3) AS due, closed_with_user_id FROM team WHERE id = ? FOR UPDATE`, teamID); err != nil {
			if isNoRows(err) {
				return nil
			}
			return fmt.Errorf("platformstore: claim wipe: lock team: %w", err)
		}
		if cur.ID == tenant.TeamOneID {
			return ErrTeamOne
		}
		if cur.Status != "deleted" || !cur.PurgeAt.Valid || !cur.Due.Valid || !cur.Due.Bool {
			return nil
		}
		if cur.ClosedWith.Valid {
			var acct struct {
				Status   string       `db:"status"`
				PurgedAt sql.NullTime `db:"purged_at"`
			}
			err := tx.GetContext(ctx, &acct, `SELECT status, purged_at FROM platform_user WHERE id = ? FOR SHARE`, cur.ClosedWith.Int64)
			switch {
			case err == nil:
				if acct.Status == UserDeleted && !acct.PurgedAt.Valid {
					return nil // the account purge goes first; the next pass claims the team
				}
			case isNoRows(err):
			default:
				return fmt.Errorf("platformstore: claim wipe: closing account: %w", err)
			}
		}
		w = TeamWipe{TeamID: teamID, Slug: cur.Slug, DBName: cur.DBName, FileKey: cur.FileKey,
			PurgeAt: cur.PurgeAt.Time.UTC(), StartedAt: now, NextAttemptAt: now}
		if !w.DBPending() {
			w.DBDroppedAt = sql.NullTime{Time: now, Valid: true}
		}
		if !w.FilesPending() {
			w.FilesDoneAt = sql.NullTime{Time: now, Valid: true}
		}
		res, err := tx.ExecContext(ctx, `INSERT IGNORE INTO team_wipe (team_id, slug, db_name, file_key, purge_at, started_at,
files_done_at, db_dropped_at, next_attempt_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			w.TeamID, w.Slug, w.DBName, w.FileKey, w.PurgeAt, w.StartedAt, w.FilesDoneAt, w.DBDroppedAt, w.NextAttemptAt)
		if err != nil {
			return fmt.Errorf("platformstore: claim wipe: insert: %w", err)
		}
		if n, err := res.RowsAffected(); err != nil || n != 1 {
			return err // claimed before (a crash after commit can never leave purge_at set: one tx)
		}
		if _, err := tx.ExecContext(ctx, `UPDATE team SET purge_at = NULL, db_name = NULL, file_key = NULL,
closed_with_user_id = NULL WHERE id = ? AND id <> ? AND status = 'deleted'`, teamID, tenant.TeamOneID); err != nil {
			return fmt.Errorf("platformstore: claim wipe: team row: %w", err)
		}
		if w.DBName.Valid {
			if _, err := tx.ExecContext(ctx, `DELETE FROM spare_db WHERE db_name = ? AND claimed_team_id = ?`,
				w.DBName.String, teamID); err != nil {
				return fmt.Errorf("platformstore: claim wipe: spare row: %w", err)
			}
		}
		if _, err := revokeAllTeamSessionsTx(ctx, tx, teamID, now); err != nil {
			return err
		}
		if err := revokeTeamInvitesTx(ctx, tx, teamID, now); err != nil {
			return err
		}
		if err := expireTeamHandoffsTx(ctx, tx, teamID, now); err != nil {
			return err
		}
		detail, _ := json.Marshal(map[string]any{"slug": w.Slug, "db_name": w.DBName.String,
			"files": w.FileKey.Valid, "purge_at": w.PurgeAt.Format(time.RFC3339)})
		if _, err := tx.ExecContext(ctx, `INSERT INTO platform_audit (actor_user_id, team_id, action, detail, created_at)
VALUES (NULL, ?, 'team_wipe_start', ?, ?)`, teamID, string(detail), now); err != nil {
			return fmt.Errorf("platformstore: claim wipe: audit: %w", err)
		}
		claimed = true
		return nil
	})
	s.dir.Invalidate(teamID)
	if err != nil {
		return TeamWipe{}, false, lockConflict(err)
	}
	return w, claimed, nil
}

// MarkWipeFilesDone records that the team's file prefix listed empty; objects is what this step
// deleted in all (added to objects_deleted).
func (s *Store) MarkWipeFilesDone(ctx context.Context, teamID, objects int64, now time.Time) error {
	if teamID == tenant.TeamOneID {
		return ErrTeamOne
	}
	if _, err := s.db.ExecContext(ctx, `UPDATE team_wipe SET files_done_at = COALESCE(files_done_at, ?),
objects_deleted = objects_deleted + ?, last_error = NULL WHERE team_id = ? AND finished_at IS NULL`, now.UTC(), objects, teamID); err != nil {
		return fmt.Errorf("platformstore: wipe files done: %w", err)
	}
	return nil
}

// AddWipeObjects adds objects deleted by a file step that has not finished yet.
func (s *Store) AddWipeObjects(ctx context.Context, teamID, objects int64) error {
	if objects == 0 {
		return nil
	}
	if _, err := s.db.ExecContext(ctx, `UPDATE team_wipe SET objects_deleted = objects_deleted + ?
WHERE team_id = ? AND finished_at IS NULL`, objects, teamID); err != nil {
		return fmt.Errorf("platformstore: wipe objects: %w", err)
	}
	return nil
}

// MarkWipeDBDropped records that the team's database is gone.
func (s *Store) MarkWipeDBDropped(ctx context.Context, teamID int64, now time.Time) error {
	if teamID == tenant.TeamOneID {
		return ErrTeamOne
	}
	if _, err := s.db.ExecContext(ctx, `UPDATE team_wipe SET db_dropped_at = COALESCE(db_dropped_at, ?), last_error = NULL
WHERE team_id = ? AND finished_at IS NULL`, now.UTC(), teamID); err != nil {
		return fmt.Errorf("platformstore: wipe db dropped: %w", err)
	}
	return nil
}

// wipeBackoff is the wait before attempt n+1 after n failed attempts: 1 m, 5 m, 30 m, 2 h, then 6 h.
func wipeBackoff(attempts int) time.Duration {
	switch {
	case attempts <= 1:
		return time.Minute
	case attempts == 2:
		return 5 * time.Minute
	case attempts == 3:
		return 30 * time.Minute
	case attempts == 4:
		return 2 * time.Hour
	}
	return 6 * time.Hour
}

// MarkWipeFailed records a failed step: with count, attempts + 1 and the next attempt after the
// backoff; without (a busy database lock), only the next attempt in a minute. reason must carry no
// secret and no file key. It returns the attempts so far.
func (s *Store) MarkWipeFailed(ctx context.Context, teamID int64, reason string, count bool, now time.Time) (int, error) {
	if len(reason) > wipeErrMax {
		reason = reason[:wipeErrMax]
	}
	var attempts int
	err := s.inTx(ctx, func(tx *sqlx.Tx) error {
		if err := tx.GetContext(ctx, &attempts, `SELECT attempts FROM team_wipe WHERE team_id = ? AND finished_at IS NULL FOR UPDATE`, teamID); err != nil {
			if isNoRows(err) {
				return nil
			}
			return err
		}
		wait := time.Minute
		if count {
			attempts++
			wait = wipeBackoff(attempts)
		}
		_, err := tx.ExecContext(ctx, `UPDATE team_wipe SET attempts = ?, last_error = ?, next_attempt_at = ? WHERE team_id = ?`,
			attempts, reason, now.UTC().Add(wait), teamID)
		return err
	})
	if err != nil {
		return 0, fmt.Errorf("platformstore: wipe failed: %w", err)
	}
	return attempts, nil
}

// DBNameReferenced reports whether any team row or spare_db row still names dbName. After the claim
// a wiped team's database is named by neither: a database someone still holds is never dropped.
func (s *Store) DBNameReferenced(ctx context.Context, dbName string) (bool, error) {
	var n int
	if err := s.db.GetContext(ctx, &n, `SELECT (SELECT COUNT(*) FROM team WHERE db_name = ?)
 + (SELECT COUNT(*) FROM spare_db WHERE db_name = ?)`, dbName, dbName); err != nil {
		return true, fmt.Errorf("platformstore: db name referenced: %w", err)
	}
	return n > 0, nil
}

// WipedTeam is what a finished wipe deleted from the control plane.
type WipedTeam struct {
	Slug string
	// Rows: control-plane rows deleted, by table.
	Rows map[string]int64
	// Finished is false when the wipe had finished before (a rerun: nothing written).
	Finished bool
}

// wipeTeamTables are the team's control-plane rows a finished wipe deletes, in lock order (logo and
// brand right after the team row, members, sessions, invites, tokens), then the rows outside the
// order. Audit rows, AI spend and slug history stay.
func wipeTeamTables() []string {
	return []string{
		"team_logo", "team_brand", "team_member", "user_session", "team_invite", "email_token",
		"team_feature", "team_join_link", "team_join_request", "platform_join_intent",
		"team_storage", "platform_email",
	}
}

// FinishTeamWipe finishes a claimed wipe. One transaction: the team row FOR UPDATE → the team_wipe
// row FOR UPDATE, which must have its files and database done and no finished_at (finished before:
// Finished false, nothing written; steps open: ErrWipeNotReady) → every control-plane row of the
// team (wipeTeamTables) deleted, frozen memberships too, so a later account restore never revives a
// membership in a dead team → the address held WipeSlugHold in team_slug_history → the row a
// tombstone (slug _w<id>, which no address check or host can ever produce; name empty) → finished_at →
// audit team_wipe. The directory entry is dropped.
func (s *Store) FinishTeamWipe(ctx context.Context, teamID int64, now time.Time) (WipedTeam, error) {
	if teamID == tenant.TeamOneID {
		return WipedTeam{}, ErrTeamOne
	}
	now = now.UTC()
	res := WipedTeam{Rows: map[string]int64{}}
	err := s.inTx(ctx, func(tx *sqlx.Tx) error {
		var cur struct {
			Slug    string         `db:"slug"`
			Status  string         `db:"status"`
			PurgeAt sql.NullTime   `db:"purge_at"`
			DBName  sql.NullString `db:"db_name"`
			FileKey sql.NullString `db:"file_key"`
		}
		if err := tx.GetContext(ctx, &cur, `SELECT slug, status, purge_at, db_name, file_key FROM team WHERE id = ? FOR UPDATE`, teamID); err != nil {
			if isNoRows(err) {
				return ErrTeamNotFound
			}
			return fmt.Errorf("platformstore: finish wipe: lock team: %w", err)
		}
		var w TeamWipe
		if err := tx.GetContext(ctx, &w, `SELECT `+teamWipeColumns+` FROM team_wipe WHERE team_id = ? FOR UPDATE`, teamID); err != nil {
			if isNoRows(err) {
				return ErrWipeNotReady
			}
			return fmt.Errorf("platformstore: finish wipe: state: %w", err)
		}
		res.Slug = w.Slug
		if w.FinishedAt.Valid {
			return nil
		}
		// The claim left the row deleted with nothing to route, restore or migrate; anything else
		// means someone changed it by hand — stop rather than erase a live team.
		if cur.Status != "deleted" || cur.PurgeAt.Valid || cur.DBName.Valid || cur.FileKey.Valid {
			return fmt.Errorf("platformstore: finish wipe: team row is not a claimed tombstone (status %s)", cur.Status)
		}
		if w.FilesPending() || w.DBPending() {
			return ErrWipeNotReady
		}
		for _, table := range wipeTeamTables() {
			r, err := tx.ExecContext(ctx, `DELETE FROM `+table+` WHERE team_id = ?`, teamID)
			if err != nil {
				return fmt.Errorf("platformstore: finish wipe: %s: %w", table, err)
			}
			if n, _ := r.RowsAffected(); n > 0 {
				res.Rows[table] = n
			}
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO team_slug_history (slug, team_id, retired_at, redirect_until)
VALUES (?, ?, ?, ?) AS n ON DUPLICATE KEY UPDATE team_id = n.team_id, retired_at = n.retired_at,
redirect_until = GREATEST(team_slug_history.redirect_until, n.redirect_until)`,
			w.Slug, teamID, now, now.Add(WipeSlugHold)); err != nil {
			return fmt.Errorf("platformstore: finish wipe: hold address: %w", err)
		}
		if _, err := tx.ExecContext(ctx, `UPDATE team SET slug = ?, name = '' WHERE id = ? AND id <> ?`,
			tombstoneSlug(teamID), teamID, tenant.TeamOneID); err != nil {
			return fmt.Errorf("platformstore: finish wipe: tombstone: %w", err)
		}
		if _, err := tx.ExecContext(ctx, `UPDATE team_wipe SET finished_at = ?, last_error = NULL WHERE team_id = ?`, now, teamID); err != nil {
			return fmt.Errorf("platformstore: finish wipe: state: %w", err)
		}
		detail, _ := json.Marshal(map[string]any{"slug": w.Slug, "db_name": w.DBName.String,
			"objects_deleted": w.ObjectsDeleted, "rows": res.Rows})
		if _, err := tx.ExecContext(ctx, `INSERT INTO platform_audit (actor_user_id, team_id, action, detail, created_at)
VALUES (NULL, ?, 'team_wipe', ?, ?)`, teamID, string(detail), now); err != nil {
			return fmt.Errorf("platformstore: finish wipe: audit: %w", err)
		}
		res.Finished = true
		return nil
	})
	s.dir.Invalidate(teamID)
	if err != nil {
		return WipedTeam{}, lockConflict(err)
	}
	return res, nil
}

// tombstoneSlug is the slug of a wiped team's row: '_' fails ValidSlug and every host check.
func tombstoneSlug(teamID int64) string { return "_w" + strconv.FormatInt(teamID, 10) }

// TeamWipePreview is what a wipe of one due team would erase (the dry run's plan).
type TeamWipePreview struct {
	TeamID  int64
	Slug    string
	PurgeAt time.Time
	DBName  string
	FileKey string
	// ClosedWithPending: the team closed with an account that is not purged yet; it waits for it.
	ClosedWithPending bool
	// Rows: control-plane rows the finalize would delete, by table.
	Rows map[string]int64
}

// PreviewTeamWipe reads, without a lock and without writing, what the wipe of a due team would take.
func (s *Store) PreviewTeamWipe(ctx context.Context, teamID int64) (TeamWipePreview, error) {
	if teamID == tenant.TeamOneID {
		return TeamWipePreview{}, ErrTeamOne
	}
	var cur struct {
		Slug       string         `db:"slug"`
		DBName     sql.NullString `db:"db_name"`
		FileKey    sql.NullString `db:"file_key"`
		PurgeAt    sql.NullTime   `db:"purge_at"`
		ClosedWith sql.NullInt64  `db:"closed_with_user_id"`
	}
	if err := s.db.GetContext(ctx, &cur, `SELECT slug, db_name, file_key, purge_at, closed_with_user_id FROM team
WHERE id = ? AND id <> ? AND status = 'deleted'`, teamID, tenant.TeamOneID); err != nil {
		if isNoRows(err) {
			return TeamWipePreview{}, ErrTeamNotFound
		}
		return TeamWipePreview{}, fmt.Errorf("platformstore: preview wipe: %w", err)
	}
	p := TeamWipePreview{TeamID: teamID, Slug: cur.Slug, PurgeAt: cur.PurgeAt.Time, DBName: cur.DBName.String,
		FileKey: cur.FileKey.String, Rows: map[string]int64{}}
	if cur.ClosedWith.Valid {
		var n int
		if err := s.db.GetContext(ctx, &n, `SELECT COUNT(*) FROM platform_user WHERE id = ? AND status = 'deleted' AND purged_at IS NULL`,
			cur.ClosedWith.Int64); err != nil {
			return TeamWipePreview{}, fmt.Errorf("platformstore: preview wipe: account: %w", err)
		}
		p.ClosedWithPending = n > 0
	}
	for _, table := range wipeTeamTables() {
		var n int64
		if err := s.db.GetContext(ctx, &n, `SELECT COUNT(*) FROM `+table+` WHERE team_id = ?`, teamID); err != nil {
			return TeamWipePreview{}, fmt.Errorf("platformstore: preview wipe: %s: %w", table, err)
		}
		if n > 0 {
			p.Rows[table] = n
		}
	}
	return p, nil
}
