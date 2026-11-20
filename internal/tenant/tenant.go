// Package tenant carries the team a piece of work belongs to.
//
// Every team has its own MySQL database. The team travels in the context: the edge (gRPC
// interceptors, chi middleware) or a worker puts it there with With, and the tenant-routing pool
// (internal/store/tenantdb) switches each connection to that team's database before it runs a
// statement. A context without a team is refused in teams mode (ErrNoTenant): nothing falls back to
// "some" database silently.
package tenant

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
)

// Status is a team's lifecycle state (control-plane column team.status).
type Status string

const (
	StatusPending Status = "pending"
	// StatusReview is a confirmed sign-up waiting for an operator: listed to its owner, never routed.
	StatusReview       Status = "review"
	StatusProvisioning Status = "provisioning"
	StatusActive       Status = "active"
	// StatusSuspended is read-only: the team can sign in and read, every write is refused.
	StatusSuspended Status = "suspended"
	StatusDeleted   Status = "deleted"
)

// Team is one workspace: its own database, its own file prefix, its own members.
type Team struct {
	ID                                   int64
	Slug, Name, DBName, FileKey, Profile string
	Status                               Status
	SchemaVersion                        string
	// SchemaMinCode is the newest breaking migration applied to the team's database ("" = none):
	// only a binary that embeds it may serve the team (X3, internal/schemacompat).
	SchemaMinCode string
}

// Team #1 is today's grbpwr database, adopted in place.
const (
	TeamOneID       int64 = 1
	TeamOneSlug           = "grbpwr"
	TeamOneName           = "grbpwr"
	ProfileAll            = "all"
	ProfileStandard       = "standard"
)

// TeamOne describes team #1 on the given database. It is the same row EnsureTeamOne writes to the
// control plane, so the static directory (teams mode off) and the boot ctx agree with it.
func TeamOne(dbName string) Team {
	return Team{
		ID:      TeamOneID,
		Slug:    TeamOneSlug,
		Name:    TeamOneName,
		DBName:  dbName,
		Profile: ProfileAll,
		Status:  StatusActive,
	}
}

var (
	// ErrNoTenant: a database call arrived with no team in its context while teams mode is on.
	ErrNoTenant = errors.New("tenant: no team in context")
	// ErrNotFound: no team with that id or slug.
	ErrNotFound = errors.New("tenant: team not found")
)

type ctxKey struct{}

// With returns ctx carrying t. The routing pool switches to t.DBName on the next statement.
func With(ctx context.Context, t Team) context.Context {
	return context.WithValue(ctx, ctxKey{}, t)
}

// From returns the team in ctx. ok is false when there is none (a zero ID never counts as a team).
func From(ctx context.Context) (Team, bool) {
	if ctx == nil {
		return Team{}, false
	}
	t, ok := ctx.Value(ctxKey{}).(Team)
	if !ok || t.ID <= 0 {
		return Team{}, false
	}
	return t, true
}

// Directory resolves teams. In teams mode it reads the control plane (with a short cache); with
// teams mode off it is a static one-team directory (team #1 only).
type Directory interface {
	ByID(ctx context.Context, id int64) (Team, error)
	// BySlug follows team_slug_history: a retired slug still in its redirect window resolves to its team.
	BySlug(ctx context.Context, slug string) (Team, error)
	// ListActive returns active and suspended teams (suspended = read-only, still alive).
	ListActive(ctx context.Context) ([]Team, error)
	// Invalidate drops the cached entry of one team (after a status / feature / rename change).
	Invalidate(teamID int64)
}

// Each runs fn once per active or suspended team, sequentially, with the team in ctx. A team whose fn
// fails is logged and skipped; the loop goes on. It returns the listing error, the ctx error if ctx
// ended mid-way, or every team's failure joined (nil when all succeeded).
func Each(ctx context.Context, dir Directory, fn func(ctx context.Context, t Team) error) error {
	teams, err := dir.ListActive(ctx)
	if err != nil {
		return fmt.Errorf("tenant: list teams: %w", err)
	}
	var errs []error
	for _, t := range teams {
		if err := ctx.Err(); err != nil {
			errs = append(errs, err)
			break
		}
		if err := fn(With(ctx, t), t); err != nil {
			slog.Default().ErrorContext(ctx, "tenant: per-team run failed; continuing with the next team",
				slog.Int64("tid", t.ID),
				slog.String("slug", t.Slug),
				slog.String("err", err.Error()),
			)
			errs = append(errs, fmt.Errorf("team %s: %w", t.Slug, err))
		}
	}
	return errors.Join(errs...)
}

// Pinned returns ctx carrying team teamID, whatever team ctx carried before. Used where a call must
// read one fixed team (the AI registry reads team #1's config whoever asks).
func Pinned(ctx context.Context, dir Directory, teamID int64) (context.Context, error) {
	t, err := dir.ByID(ctx, teamID)
	if err != nil {
		return ctx, fmt.Errorf("tenant: pin team %d: %w", teamID, err)
	}
	return With(ctx, t), nil
}
