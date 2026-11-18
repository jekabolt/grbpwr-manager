package platformstore

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/tenant"
	"github.com/jmoiron/sqlx"
)

// directoryTTL bounds how stale a cached team may be when nobody called Invalidate.
const directoryTTL = 15 * time.Second

type cachedTeam struct {
	team tenant.Team
	at   time.Time
}

// directory is tenant.Directory over the control plane.
type directory struct {
	db  *sqlx.DB
	now func() time.Time

	mu     sync.Mutex
	byID   map[int64]cachedTeam
	bySlug map[string]cachedTeam
}

var _ tenant.Directory = (*directory)(nil)

func newDirectory(db *sqlx.DB) *directory {
	return &directory{
		db:     db,
		now:    time.Now,
		byID:   map[int64]cachedTeam{},
		bySlug: map[string]cachedTeam{},
	}
}

type teamRow struct {
	ID            int64          `db:"id"`
	Slug          string         `db:"slug"`
	Name          string         `db:"name"`
	DBName        sql.NullString `db:"db_name"`
	FileKey       sql.NullString `db:"file_key"`
	Profile       string         `db:"profile"`
	Status        string         `db:"status"`
	SchemaVersion sql.NullString `db:"schema_version"`
}

func (r teamRow) team() tenant.Team {
	return tenant.Team{
		ID:            r.ID,
		Slug:          r.Slug,
		Name:          r.Name,
		DBName:        r.DBName.String,
		FileKey:       r.FileKey.String,
		Profile:       r.Profile,
		Status:        tenant.Status(r.Status),
		SchemaVersion: r.SchemaVersion.String,
	}
}

const teamColumns = `id, slug, name, db_name, file_key, profile, status, schema_version`

func (d *directory) cached(m map[int64]cachedTeam, id int64) (tenant.Team, bool) {
	d.mu.Lock()
	defer d.mu.Unlock()
	c, ok := m[id]
	if !ok || d.now().Sub(c.at) > directoryTTL {
		return tenant.Team{}, false
	}
	return c.team, true
}

func (d *directory) remember(slug string, t tenant.Team) {
	d.mu.Lock()
	defer d.mu.Unlock()
	c := cachedTeam{team: t, at: d.now()}
	d.byID[t.ID] = c
	d.bySlug[slug] = c
}

// ByID returns the team in any status; callers decide what a pending or deleted team may do.
func (d *directory) ByID(ctx context.Context, id int64) (tenant.Team, error) {
	if id <= 0 {
		return tenant.Team{}, tenant.ErrNotFound
	}
	if t, ok := d.cached(d.byID, id); ok {
		return t, nil
	}
	var r teamRow
	err := d.db.GetContext(ctx, &r, `SELECT `+teamColumns+` FROM team WHERE id = ?`, id)
	if err != nil {
		if isNoRows(err) {
			return tenant.Team{}, tenant.ErrNotFound
		}
		return tenant.Team{}, fmt.Errorf("platformstore: team by id: %w", err)
	}
	t := r.team()
	d.remember(t.Slug, t)
	return t, nil
}

// BySlug resolves a live slug, then a retired one still inside its redirect window. A deleted team
// is not found by slug.
func (d *directory) BySlug(ctx context.Context, slug string) (tenant.Team, error) {
	slug = strings.ToLower(strings.TrimSpace(slug))
	if slug == "" {
		return tenant.Team{}, tenant.ErrNotFound
	}
	d.mu.Lock()
	c, ok := d.bySlug[slug]
	d.mu.Unlock()
	if ok && d.now().Sub(c.at) <= directoryTTL {
		return c.team, nil
	}

	var r teamRow
	err := d.db.GetContext(ctx, &r,
		`SELECT `+teamColumns+` FROM team WHERE slug = ? AND status <> 'deleted'`, slug)
	if err == nil {
		t := r.team()
		d.remember(slug, t)
		return t, nil
	}
	if !isNoRows(err) {
		return tenant.Team{}, fmt.Errorf("platformstore: team by slug: %w", err)
	}

	var teamID int64
	err = d.db.GetContext(ctx, &teamID,
		`SELECT team_id FROM team_slug_history WHERE slug = ? AND redirect_until > UTC_TIMESTAMP(3)`, slug)
	if err != nil {
		if isNoRows(err) {
			return tenant.Team{}, tenant.ErrNotFound
		}
		return tenant.Team{}, fmt.Errorf("platformstore: slug history: %w", err)
	}
	t, err := d.ByID(ctx, teamID)
	if err != nil {
		return tenant.Team{}, err
	}
	if t.Status == tenant.StatusDeleted {
		return tenant.Team{}, tenant.ErrNotFound
	}
	d.remember(slug, t)
	return t, nil
}

// ListActive returns active and suspended teams, by id. Never cached: a worker loop must see a
// team suspended or deleted a second ago.
func (d *directory) ListActive(ctx context.Context) ([]tenant.Team, error) {
	var rows []teamRow
	err := d.db.SelectContext(ctx, &rows,
		`SELECT `+teamColumns+` FROM team WHERE status IN ('active','suspended') ORDER BY id`)
	if err != nil {
		return nil, fmt.Errorf("platformstore: list teams: %w", err)
	}
	out := make([]tenant.Team, 0, len(rows))
	for _, r := range rows {
		out = append(out, r.team())
	}
	return out, nil
}

// Invalidate drops every cached entry of the team (its id and every slug that pointed at it).
func (d *directory) Invalidate(teamID int64) {
	d.mu.Lock()
	defer d.mu.Unlock()
	delete(d.byID, teamID)
	for s, c := range d.bySlug {
		if c.team.ID == teamID {
			delete(d.bySlug, s)
		}
	}
}

func isNoRows(err error) bool { return errors.Is(err, sql.ErrNoRows) }
