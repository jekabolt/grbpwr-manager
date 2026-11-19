package platformstore

import (
	"context"
	"fmt"
	"time"

	"github.com/jmoiron/sqlx"
)

// TeamLogo is a team's stored logo (teams O §11): PNG bytes already checked and fitted by the
// caller, and the hex sha256 of exactly those bytes (the ETag and the ?v= of the logo URL).
type TeamLogo struct {
	PNG    []byte
	SHA256 string
}

// putTeamLogoTx stores or replaces a team's logo inside the caller's transaction, which already
// holds the team row (lock order at DeleteTeam: team_logo right after its team). nil = nothing.
func putTeamLogoTx(ctx context.Context, tx *sqlx.Tx, teamID int64, l *TeamLogo) error {
	if l == nil || len(l.PNG) == 0 {
		return nil
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO team_logo (team_id, png, sha256, updated_at) VALUES (?, ?, ?, UTC_TIMESTAMP(3)) AS nv
ON DUPLICATE KEY UPDATE png = nv.png, sha256 = nv.sha256, updated_at = nv.updated_at`, teamID, l.PNG, l.SHA256); err != nil {
		return fmt.Errorf("platformstore: put team logo: %w", err)
	}
	return nil
}

// deleteTeamLogoTx removes a team's logo inside the caller's transaction, which already holds the
// team row. Every statement that deletes a team row calls it first.
func deleteTeamLogoTx(ctx context.Context, tx *sqlx.Tx, teamID int64) error {
	if _, err := tx.ExecContext(ctx, `DELETE FROM team_logo WHERE team_id = ?`, teamID); err != nil {
		return fmt.Errorf("platformstore: delete team logo: %w", err)
	}
	return nil
}

// deletePendingTeamsTx deletes the teams matched by where (a condition on team that must keep
// status = 'pending') together with their logos and returns their ids: the rows are locked FOR
// UPDATE by the same condition the plain DELETE used, then each logo and team goes by id.
func deletePendingTeamsTx(ctx context.Context, tx *sqlx.Tx, where string, args ...any) ([]int64, error) {
	var ids []int64
	if err := tx.SelectContext(ctx, &ids, `SELECT id FROM team WHERE `+where+` FOR UPDATE`, args...); err != nil {
		return nil, fmt.Errorf("platformstore: lock pending teams: %w", err)
	}
	for _, id := range ids {
		if err := deleteTeamLogoTx(ctx, tx, id); err != nil {
			return nil, err
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM team WHERE id = ? AND status = 'pending'`, id); err != nil {
			return nil, fmt.Errorf("platformstore: delete pending team: %w", err)
		}
	}
	return ids, nil
}

// TeamLogoBySlug reads the logo of the team at this address for the public logo route. ok=false
// when the team has no logo, does not exist, or is deleted or pending — one answer for all of them.
func (s *Store) TeamLogoBySlug(ctx context.Context, slug string) (TeamLogo, time.Time, bool, error) {
	var rows []struct {
		PNG       []byte    `db:"png"`
		SHA256    string    `db:"sha256"`
		UpdatedAt time.Time `db:"updated_at"`
	}
	if err := s.db.SelectContext(ctx, &rows, `SELECT l.png, l.sha256, l.updated_at
FROM team t JOIN team_logo l ON l.team_id = t.id
WHERE t.slug = ? AND t.status NOT IN ('deleted', 'pending')`, slug); err != nil {
		return TeamLogo{}, time.Time{}, false, fmt.Errorf("platformstore: team logo: %w", err)
	}
	if len(rows) == 0 {
		return TeamLogo{}, time.Time{}, false, nil
	}
	return TeamLogo{PNG: rows[0].PNG, SHA256: rows[0].SHA256}, rows[0].UpdatedAt, true, nil
}

// TeamLogoSHA is the sha256 of a team's logo, "" when it has none (the logo URL is empty then).
func (s *Store) TeamLogoSHA(ctx context.Context, teamID int64) (string, error) {
	var shas []string
	if err := s.db.SelectContext(ctx, &shas, `SELECT sha256 FROM team_logo WHERE team_id = ?`, teamID); err != nil {
		return "", fmt.Errorf("platformstore: team logo sha: %w", err)
	}
	if len(shas) == 0 {
		return "", nil
	}
	return shas[0], nil
}

// TeamLogoSHAs is TeamLogoSHA for many teams at once: team id → sha256, teams without a logo absent.
func (s *Store) TeamLogoSHAs(ctx context.Context, teamIDs []int64) (map[int64]string, error) {
	out := make(map[int64]string, len(teamIDs))
	if len(teamIDs) == 0 {
		return out, nil
	}
	q, args, err := sqlx.In(`SELECT team_id, sha256 FROM team_logo WHERE team_id IN (?)`, teamIDs)
	if err != nil {
		return nil, fmt.Errorf("platformstore: team logo shas: %w", err)
	}
	var rows []struct {
		TeamID int64  `db:"team_id"`
		SHA256 string `db:"sha256"`
	}
	if err := s.db.SelectContext(ctx, &rows, s.db.Rebind(q), args...); err != nil {
		return nil, fmt.Errorf("platformstore: team logo shas: %w", err)
	}
	for _, r := range rows {
		out[r.TeamID] = r.SHA256
	}
	return out, nil
}

// SetTeamLogo sets (l non-nil) or removes (l nil) the logo of a usable team: the team row is locked
// FOR UPDATE first (the team itself changes, like a rename), then its logo row. ErrTeamNotFound
// when there is no such team; ErrWrongTeamStatus when it is deleted or pending.
func (s *Store) SetTeamLogo(ctx context.Context, teamID int64, l *TeamLogo) error {
	return s.inTx(ctx, func(tx *sqlx.Tx) error {
		var st string
		if err := tx.GetContext(ctx, &st, `SELECT status FROM team WHERE id = ? FOR UPDATE`, teamID); err != nil {
			if isNoRows(err) {
				return ErrTeamNotFound
			}
			return fmt.Errorf("platformstore: lock team: %w", err)
		}
		if st == "deleted" || st == "pending" {
			return ErrWrongTeamStatus
		}
		if l == nil || len(l.PNG) == 0 {
			return deleteTeamLogoTx(ctx, tx, teamID)
		}
		return putTeamLogoTx(ctx, tx, teamID, l)
	})
}
