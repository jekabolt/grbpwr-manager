package platformstore

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/tenant"
)

// Spare database states (spare_db.status).
const (
	SpareCreating = "creating"
	SpareReady    = "ready"
	SpareFailed   = "failed"
	SpareClaimed  = "claimed"
)

// ErrNoSpare: no ready spare at the asked schema version.
var ErrNoSpare = errors.New("platformstore: no ready spare database")

// ErrNotProvisioning: the team left the provisioning state (or does not exist) meanwhile.
var ErrNotProvisioning = errors.New("platformstore: team is not provisioning")

// Spare is one spare_db row.
type Spare struct {
	DBName            string         `db:"db_name"`
	Status            string         `db:"status"`
	SchemaVersion     sql.NullString `db:"schema_version"`
	SchemaFingerprint sql.NullString `db:"schema_fingerprint"`
}

// ProvisioningTeam is a team waiting for its database / seed.
type ProvisioningTeam struct {
	ID     int64          `db:"id"`
	Slug   string         `db:"slug"`
	DBName sql.NullString `db:"db_name"`
	// OwnerHandle is the platform handle of the team's owner ("" when none is recorded yet).
	OwnerHandle string `db:"owner_handle"`
}

// SchemaTeam is a live team the fleet migrator keeps at the binary's migration level.
type SchemaTeam struct {
	ID            int64          `db:"id"`
	Slug          string         `db:"slug"`
	DBName        string         `db:"db_name"`
	Status        string         `db:"status"`
	SchemaVersion sql.NullString `db:"schema_version"`
}

// SpareCounts counts ready spares at one schema version, spares still being created (since a moment:
// a row left in creating by a crash stops counting after that), and spare databases created since
// that moment whatever their state (the provisioning cap is about CREATE DATABASE).
type SpareCounts struct {
	Ready, Creating, CreatedSince int
}

// CountSpares returns the ready and creating spares at version and the creations since since.
func (s *Store) CountSpares(ctx context.Context, version string, since time.Time) (SpareCounts, error) {
	var c SpareCounts
	err := s.db.QueryRowxContext(ctx, `SELECT
  COALESCE(SUM(status = 'ready' AND schema_version = ?), 0),
  COALESCE(SUM(status = 'creating' AND created_at >= ?), 0),
  COALESCE(SUM(created_at >= ?), 0)
FROM spare_db`, version, since.UTC(), since.UTC()).Scan(&c.Ready, &c.Creating, &c.CreatedSince)
	if err != nil {
		return c, fmt.Errorf("platformstore: count spares: %w", err)
	}
	return c, nil
}

// ErrSpareQuota: the hourly cap on new team databases is spent (every spare_db row created in the
// last hour counts, failed and claimed ones included).
var ErrSpareQuota = errors.New("platformstore: hourly database creation cap reached")

// ErrEnoughSpares: enough spares are ready or being created; the keeper has nothing to do.
var ErrEnoughSpares = errors.New("platformstore: enough spare databases")

// quotaRow is the provision_lease row every reservation locks (0003_provision_lease.sql).
const quotaRow = "spare-quota"

// ReserveSpare is the only way a spare database comes to exist: in one control-plane transaction,
// serialised on the quota row, it counts the spare_db rows created in the last hour (every state)
// and refuses with ErrSpareQuota at perHour; with keep > 0 (the keeper) it also refuses with
// ErrEnoughSpares when keep spares are ready at version or still being created; otherwise it
// records dbName in state creating. The row exists before CREATE DATABASE runs, so a crash leaves a
// visible row, never an unknown database, and the cap counts it.
func (s *Store) ReserveSpare(ctx context.Context, dbName, version string, perHour, keep int) error {
	if perHour <= 0 {
		return ErrSpareQuota
	}
	tx, err := s.db.BeginTxx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		return fmt.Errorf("platformstore: reserve spare: begin: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	// The quota row ships with migration 0003; recreate it if an operator removed it.
	if _, err := tx.ExecContext(ctx, `INSERT IGNORE INTO provision_lease (lease_key, token, expires_at) VALUES (?, '', '9999-12-31 00:00:00.000')`, quotaRow); err != nil {
		return fmt.Errorf("platformstore: reserve spare: quota row: %w", err)
	}
	var locked string
	if err := tx.GetContext(ctx, &locked, `SELECT lease_key FROM provision_lease WHERE lease_key = ? FOR UPDATE`, quotaRow); err != nil {
		return fmt.Errorf("platformstore: reserve spare: lock: %w", err)
	}
	var c SpareCounts
	if err := tx.QueryRowxContext(ctx, `SELECT
  COALESCE(SUM(status = 'ready' AND schema_version = ?), 0),
  COALESCE(SUM(status = 'creating' AND created_at >= UTC_TIMESTAMP(3) - INTERVAL 1 HOUR), 0),
  COALESCE(SUM(created_at >= UTC_TIMESTAMP(3) - INTERVAL 1 HOUR), 0)
FROM spare_db`, version).Scan(&c.Ready, &c.Creating, &c.CreatedSince); err != nil {
		return fmt.Errorf("platformstore: reserve spare: count: %w", err)
	}
	if keep > 0 && c.Ready+c.Creating >= keep {
		return ErrEnoughSpares
	}
	if c.CreatedSince >= perHour {
		return ErrSpareQuota
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO spare_db (db_name, status, created_at) VALUES (?, 'creating', UTC_TIMESTAMP(3))`, dbName); err != nil {
		return fmt.Errorf("platformstore: reserve spare %s: %w", dbName, err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("platformstore: reserve spare: commit: %w", err)
	}
	return nil
}

// MarkSpareReady records a migrated spare: version and fingerprint, state ready.
func (s *Store) MarkSpareReady(ctx context.Context, dbName, version, fingerprint string) error {
	res, err := s.db.ExecContext(ctx, `UPDATE spare_db SET status = 'ready', schema_version = ?, schema_fingerprint = ?, error = NULL
WHERE db_name = ? AND status = 'creating'`, version, fingerprint, dbName)
	if err != nil {
		return fmt.Errorf("platformstore: spare %s ready: %w", dbName, err)
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return fmt.Errorf("platformstore: spare %s ready: not in state creating", dbName)
	}
	return nil
}

// MarkSpareFailed records why a spare could not be created or migrated. A failed spare is never
// dropped automatically: an operator looks at it (02-DESIGN §19 risk 4).
func (s *Store) MarkSpareFailed(ctx context.Context, dbName, reason string) error {
	if len(reason) > 4000 {
		reason = reason[:4000]
	}
	if _, err := s.db.ExecContext(ctx, `UPDATE spare_db SET status = 'failed', error = ? WHERE db_name = ? AND status IN ('creating','ready')`, reason, dbName); err != nil {
		return fmt.Errorf("platformstore: spare %s failed: %w", dbName, err)
	}
	return nil
}

// SetSpareSchema records a ready spare's new migration level after the fleet migrator ran, only
// while token holds the lease DBLeaseKey(dbName) (ErrLeaseLost otherwise). A spare claimed
// meanwhile is left alone: its team records its own level.
func (s *Store) SetSpareSchema(ctx context.Context, dbName, token, version, fingerprint string) error {
	res, err := s.db.ExecContext(ctx, `UPDATE spare_db s
JOIN provision_lease l ON l.lease_key = ? AND l.token = ? AND l.expires_at > UTC_TIMESTAMP(3)
SET s.schema_version = ?, s.schema_fingerprint = ?
WHERE s.db_name = ? AND s.status = 'ready'`, DBLeaseKey(dbName), token, version, fingerprint, dbName)
	if err != nil {
		return fmt.Errorf("platformstore: spare %s schema: %w", dbName, err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return s.leaseHeld(ctx, DBLeaseKey(dbName), token)
	}
	return nil
}

// ReadySpares lists every ready spare (the fleet migrator brings those behind the binary forward).
func (s *Store) ReadySpares(ctx context.Context) ([]Spare, error) {
	var out []Spare
	if err := s.db.SelectContext(ctx, &out, `SELECT db_name, status, schema_version, schema_fingerprint
FROM spare_db WHERE status = 'ready' ORDER BY created_at, db_name`); err != nil {
		return nil, fmt.Errorf("platformstore: ready spares: %w", err)
	}
	return out, nil
}

// ProvisioningTeams lists teams in state provisioning, oldest first, with the owner's handle.
func (s *Store) ProvisioningTeams(ctx context.Context, limit int) ([]ProvisioningTeam, error) {
	var out []ProvisioningTeam
	err := s.db.SelectContext(ctx, &out, `SELECT t.id, t.slug, t.db_name,
  COALESCE((SELECT u.handle FROM team_member m JOIN platform_user u ON u.id = m.user_id
            WHERE m.team_id = t.id AND m.role = 'owner' AND m.status = 'active'
            ORDER BY m.created_at, m.user_id LIMIT 1),
           (SELECT u.handle FROM platform_user u WHERE u.id = t.created_by), '') AS owner_handle
FROM team t WHERE t.status = 'provisioning' ORDER BY t.created_at, t.id LIMIT ?`, limit)
	if err != nil {
		return nil, fmt.Errorf("platformstore: provisioning teams: %w", err)
	}
	return out, nil
}

// claimCandidates bounds how many ready spares one claim looks at before giving up.
const claimCandidates = 20

// ClaimSpare gives a provisioning team the oldest ready spare at version, in one control-plane
// transaction: the spare row is taken FOR UPDATE SKIP LOCKED (two workers never take the same
// spare), marked claimed, and the team gets db_name and a fresh 128-bit file_key; claimed is true.
// A team that already has its database gets it back unchanged with claimed false (a rerun after a
// crash). The team row is read under lock here, never from an earlier listing. ErrNoSpare:
// nothing ready at that version; ErrNotProvisioning: the team is gone or moved on.
func (s *Store) ClaimSpare(ctx context.Context, teamID int64, version string) (dbName string, claimed bool, err error) {
	tx, err := s.db.BeginTxx(ctx, nil)
	if err != nil {
		return "", false, fmt.Errorf("platformstore: claim spare: begin: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	var cur struct {
		Status string         `db:"status"`
		DBName sql.NullString `db:"db_name"`
	}
	if err := tx.GetContext(ctx, &cur, `SELECT status, db_name FROM team WHERE id = ? FOR UPDATE`, teamID); err != nil {
		if isNoRows(err) {
			return "", false, ErrNotProvisioning
		}
		return "", false, fmt.Errorf("platformstore: claim spare: read team: %w", err)
	}
	if cur.Status != string(tenant.StatusProvisioning) {
		return "", false, ErrNotProvisioning
	}
	if cur.DBName.Valid && cur.DBName.String != "" {
		return cur.DBName.String, false, nil
	}
	// Two steps: spare_db has no (status, schema_version, created_at) index, so a locking
	// "ORDER BY created_at LIMIT 1" sorts after reading and locks every candidate, and a second
	// worker's SKIP LOCKED then finds none. Read the oldest candidates without locks, then lock one
	// by primary key, re-checking it is still ready at this version.
	var candidates []string
	if err := tx.SelectContext(ctx, &candidates, `SELECT db_name FROM spare_db
WHERE status = 'ready' AND schema_version = ? ORDER BY created_at, db_name LIMIT ?`, version, claimCandidates); err != nil {
		return "", false, fmt.Errorf("platformstore: claim spare: candidates: %w", err)
	}
	for _, c := range candidates {
		err = tx.GetContext(ctx, &dbName, `SELECT db_name FROM spare_db
WHERE db_name = ? AND status = 'ready' AND schema_version = ? FOR UPDATE SKIP LOCKED`, c, version)
		if err == nil {
			break
		}
		if !isNoRows(err) {
			return "", false, fmt.Errorf("platformstore: claim spare: pick: %w", err)
		}
	}
	if dbName == "" {
		return "", false, ErrNoSpare
	}
	key, err := randomHex(16)
	if err != nil {
		return "", false, err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE spare_db SET status = 'claimed', claimed_team_id = ?, claimed_at = UTC_TIMESTAMP(3) WHERE db_name = ?`,
		teamID, dbName); err != nil {
		return "", false, fmt.Errorf("platformstore: claim spare: mark: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `UPDATE team SET db_name = ?, file_key = ? WHERE id = ?`, dbName, key, teamID); err != nil {
		return "", false, fmt.Errorf("platformstore: claim spare: attach: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return "", false, fmt.Errorf("platformstore: claim spare: commit: %w", err)
	}
	s.dir.Invalidate(teamID)
	return dbName, true, nil
}

// ActivateTeam ends provisioning: status active, activated_at, schema version and fingerprint, in
// one statement that also requires token to hold TeamLeaseKey(teamID). ErrLeaseLost: the lease
// expired or passed to another worker; ErrNotProvisioning: the team is no longer provisioning.
func (s *Store) ActivateTeam(ctx context.Context, teamID int64, token, version, fingerprint string) error {
	res, err := s.db.ExecContext(ctx, `UPDATE team t
JOIN provision_lease l ON l.lease_key = ? AND l.token = ? AND l.expires_at > UTC_TIMESTAMP(3)
SET t.status = 'active', t.activated_at = UTC_TIMESTAMP(3), t.schema_version = ?, t.schema_fingerprint = ?
WHERE t.id = ? AND t.status = 'provisioning' AND t.db_name IS NOT NULL`, TeamLeaseKey(teamID), token, version, fingerprint, teamID)
	if err != nil {
		return fmt.Errorf("platformstore: activate team %d: %w", teamID, err)
	}
	s.dir.Invalidate(teamID)
	if n, _ := res.RowsAffected(); n != 1 {
		if err := s.leaseHeld(ctx, TeamLeaseKey(teamID), token); err != nil {
			return err
		}
		return ErrNotProvisioning
	}
	return nil
}

// SetTeamSchema records a team's migration level and schema fingerprint ("" = unknown), only while
// token holds DBLeaseKey(dbName) and the team still points at dbName (ErrLeaseLost otherwise).
func (s *Store) SetTeamSchema(ctx context.Context, teamID int64, dbName, token, version, fingerprint string) error {
	var fp any
	if fingerprint != "" {
		fp = fingerprint
	}
	res, err := s.db.ExecContext(ctx, `UPDATE team t
JOIN provision_lease l ON l.lease_key = ? AND l.token = ? AND l.expires_at > UTC_TIMESTAMP(3)
SET t.schema_version = ?, t.schema_fingerprint = ?
WHERE t.id = ? AND t.db_name = ?`, DBLeaseKey(dbName), token, version, fp, teamID, dbName)
	if err != nil {
		return fmt.Errorf("platformstore: team %d schema: %w", teamID, err)
	}
	s.dir.Invalidate(teamID)
	if n, _ := res.RowsAffected(); n == 0 {
		// Nothing changed: either the values were already there (fine) or the lease is gone.
		return s.leaseHeld(ctx, DBLeaseKey(dbName), token)
	}
	return nil
}

// ─── leases ────────────────────────────────────────────────────────────────────────────────────

// ErrLeaseLost: the token no longer holds the lease (expired, released, or taken by another worker).
var ErrLeaseLost = errors.New("platformstore: provisioning lease not held")

// TeamLeaseKey is the lease a worker holds while it claims, seeds and activates one team.
func TeamLeaseKey(teamID int64) string { return fmt.Sprintf("team:%d", teamID) }

// DBLeaseKey is the lease a worker holds while it migrates (or seeds) one team or spare database.
func DBLeaseKey(dbName string) string { return "db:" + dbName }

// AcquireLease takes key for ttl in one statement: a new key is inserted, an expired one is taken
// over, a live one is left to its holder (ok false). The token returned proves ownership to the
// guarded writes, ExtendLease and ReleaseLease.
func (s *Store) AcquireLease(ctx context.Context, key string, ttl time.Duration) (token string, ok bool, err error) {
	if key == quotaRow {
		return "", false, fmt.Errorf("platformstore: %q is not a lease", key)
	}
	token, err = randomHex(16)
	if err != nil {
		return "", false, err
	}
	if _, err := s.db.ExecContext(ctx, `INSERT INTO provision_lease (lease_key, token, expires_at)
VALUES (?, ?, UTC_TIMESTAMP(3) + INTERVAL ? MICROSECOND) AS n
ON DUPLICATE KEY UPDATE
  token = IF(provision_lease.expires_at <= UTC_TIMESTAMP(3), n.token, provision_lease.token),
  expires_at = IF(provision_lease.expires_at <= UTC_TIMESTAMP(3), n.expires_at, provision_lease.expires_at)`,
		key, token, ttl.Microseconds()); err != nil {
		return "", false, fmt.Errorf("platformstore: acquire lease %s: %w", key, err)
	}
	var holder string
	if err := s.db.GetContext(ctx, &holder, `SELECT token FROM provision_lease WHERE lease_key = ?`, key); err != nil {
		return "", false, fmt.Errorf("platformstore: acquire lease %s: read: %w", key, err)
	}
	if holder != token {
		return "", false, nil
	}
	return token, true, nil
}

// ExtendLease pushes a held lease's expiry to now + ttl; ErrLeaseLost when token no longer holds it.
func (s *Store) ExtendLease(ctx context.Context, key, token string, ttl time.Duration) error {
	if _, err := s.db.ExecContext(ctx, `UPDATE provision_lease SET expires_at = UTC_TIMESTAMP(3) + INTERVAL ? MICROSECOND
WHERE lease_key = ? AND token = ? AND expires_at > UTC_TIMESTAMP(3)`, ttl.Microseconds(), key, token); err != nil {
		return fmt.Errorf("platformstore: extend lease %s: %w", key, err)
	}
	return s.leaseHeld(ctx, key, token)
}

// ReleaseLease gives a held lease back; a lease held by another token is left alone.
func (s *Store) ReleaseLease(ctx context.Context, key, token string) error {
	if _, err := s.db.ExecContext(ctx, `DELETE FROM provision_lease WHERE lease_key = ? AND token = ?`, key, token); err != nil {
		return fmt.Errorf("platformstore: release lease %s: %w", key, err)
	}
	return nil
}

// CheckProvisionLease is the seed's guard: nil only while token holds TeamLeaseKey(teamID), the
// team is provisioning and its database is dbName. ErrLeaseLost / ErrNotProvisioning otherwise.
func (s *Store) CheckProvisionLease(ctx context.Context, teamID int64, dbName, token string) error {
	if err := s.leaseHeld(ctx, TeamLeaseKey(teamID), token); err != nil {
		return err
	}
	var n int
	if err := s.db.GetContext(ctx, &n, `SELECT COUNT(*) FROM team WHERE id = ? AND status = 'provisioning' AND db_name = ?`, teamID, dbName); err != nil {
		return fmt.Errorf("platformstore: check team %d: %w", teamID, err)
	}
	if n != 1 {
		return ErrNotProvisioning
	}
	return nil
}

// leaseHeld is nil while token holds key unexpired, ErrLeaseLost otherwise.
func (s *Store) leaseHeld(ctx context.Context, key, token string) error {
	var n int
	if err := s.db.GetContext(ctx, &n, `SELECT COUNT(*) FROM provision_lease
WHERE lease_key = ? AND token = ? AND token <> '' AND expires_at > UTC_TIMESTAMP(3)`, key, token); err != nil {
		return fmt.Errorf("platformstore: lease %s: %w", key, err)
	}
	if n != 1 {
		return ErrLeaseLost
	}
	return nil
}

// SchemaTeams lists active and suspended teams other than team #1 that have a database: team #1 is
// migrated synchronously at boot, the rest by the fleet migrator.
func (s *Store) SchemaTeams(ctx context.Context) ([]SchemaTeam, error) {
	var out []SchemaTeam
	if err := s.db.SelectContext(ctx, &out, `SELECT id, slug, db_name, status, schema_version FROM team
WHERE status IN ('active','suspended') AND id <> ? AND db_name IS NOT NULL ORDER BY id`, tenant.TeamOneID); err != nil {
		return nil, fmt.Errorf("platformstore: schema teams: %w", err)
	}
	return out, nil
}

// ReferenceSpare returns a ready (preferred) or claimed spare at version with a fingerprint: the
// G8 reference schema, built from the migration chain alone on an empty database.
func (s *Store) ReferenceSpare(ctx context.Context, version string) (dbName, fingerprint string, ok bool, err error) {
	var r struct {
		DBName string `db:"db_name"`
		FP     string `db:"schema_fingerprint"`
	}
	err = s.db.GetContext(ctx, &r, `SELECT db_name, schema_fingerprint FROM spare_db
WHERE status IN ('ready','claimed') AND schema_version = ? AND schema_fingerprint IS NOT NULL
ORDER BY status = 'ready' DESC, created_at LIMIT 1`, version)
	if err != nil {
		if isNoRows(err) {
			return "", "", false, nil
		}
		return "", "", false, fmt.Errorf("platformstore: reference spare: %w", err)
	}
	return r.DBName, r.FP, true, nil
}

// ReferenceFingerprint is the schema fingerprint the binary's migrations produce at version, taken
// from a ready or claimed spare at that version (the console shows schema_ok against it).
func (s *Store) ReferenceFingerprint(ctx context.Context, version string) (string, bool, error) {
	_, fp, ok, err := s.ReferenceSpare(ctx, version)
	return fp, ok, err
}

// SweepPendingTeams deletes teams still pending past pending_expires_at (the slug is free again)
// with their memberships and tokens, and the users who were pending and belonged to nothing else.
// One audit row per team. Returns how many teams were removed.
func (s *Store) SweepPendingTeams(ctx context.Context, now time.Time, limit int) (int, error) {
	var ids []int64
	if err := s.db.SelectContext(ctx, &ids, `SELECT id FROM team
WHERE status = 'pending' AND pending_expires_at IS NOT NULL AND pending_expires_at < ? AND id <> ?
ORDER BY id LIMIT ?`, now.UTC(), tenant.TeamOneID, limit); err != nil {
		return 0, fmt.Errorf("platformstore: pending teams: %w", err)
	}
	n := 0
	for _, id := range ids {
		ok, err := s.sweepPendingTeam(ctx, id, now)
		if err != nil {
			return n, err
		}
		if ok {
			n++
		}
	}
	return n, nil
}

func (s *Store) sweepPendingTeam(ctx context.Context, teamID int64, now time.Time) (bool, error) {
	tx, err := s.db.BeginTxx(ctx, nil)
	if err != nil {
		return false, fmt.Errorf("platformstore: sweep: begin: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	var slug string
	err = tx.GetContext(ctx, &slug, `SELECT slug FROM team
WHERE id = ? AND status = 'pending' AND pending_expires_at < ? FOR UPDATE`, teamID, now.UTC())
	if err != nil {
		if isNoRows(err) {
			return false, nil // verified or replaced meanwhile
		}
		return false, fmt.Errorf("platformstore: sweep: read team: %w", err)
	}
	// Pending users of this team that belong to no other team.
	var users []int64
	if err := tx.SelectContext(ctx, &users, `SELECT u.id FROM team_member m JOIN platform_user u ON u.id = m.user_id
WHERE m.team_id = ? AND u.status = 'pending'
  AND NOT EXISTS (SELECT 1 FROM team_member o WHERE o.user_id = u.id AND o.team_id <> ?)
FOR UPDATE`, teamID, teamID); err != nil {
		return false, fmt.Errorf("platformstore: sweep: users: %w", err)
	}
	type stmt struct {
		q    string
		args []any
	}
	stmts := []stmt{
		{`DELETE FROM email_token WHERE team_id = ?`, []any{teamID}},
		{`DELETE FROM user_session WHERE team_id = ?`, []any{teamID}},
		{`DELETE FROM team_invite WHERE team_id = ?`, []any{teamID}},
		{`DELETE FROM team_feature WHERE team_id = ?`, []any{teamID}},
		{`DELETE FROM team_member WHERE team_id = ?`, []any{teamID}},
		{`DELETE FROM team WHERE id = ?`, []any{teamID}},
	}
	for _, uid := range users {
		stmts = append(stmts,
			stmt{`DELETE FROM email_token WHERE user_id = ?`, []any{uid}},
			stmt{`DELETE FROM user_session WHERE user_id = ?`, []any{uid}},
			stmt{`DELETE FROM platform_user WHERE id = ? AND status = 'pending'`, []any{uid}},
		)
	}
	for _, st := range stmts {
		if _, err := tx.ExecContext(ctx, st.q, st.args...); err != nil {
			return false, fmt.Errorf("platformstore: sweep team %d: %w", teamID, err)
		}
	}
	detail, _ := json.Marshal(map[string]any{"slug": slug, "users_removed": len(users)})
	if _, err := tx.ExecContext(ctx, `INSERT INTO platform_audit (actor_user_id, team_id, action, detail, created_at)
VALUES (NULL, ?, 'team_pending_expired', ?, UTC_TIMESTAMP(3))`, teamID, string(detail)); err != nil {
		return false, fmt.Errorf("platformstore: sweep: audit: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return false, fmt.Errorf("platformstore: sweep: commit: %w", err)
	}
	s.dir.Invalidate(teamID)
	return true, nil
}

func randomHex(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("platformstore: random: %w", err)
	}
	return hex.EncodeToString(b), nil
}
