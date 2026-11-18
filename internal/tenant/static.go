package tenant

import (
	"context"
	"fmt"
	"strings"
)

// staticDirectory is the directory of teams mode OFF: exactly one team, team #1.
type staticDirectory struct {
	team Team
}

// NewStaticDirectory returns the one-team directory used while PLATFORM_MYSQL_DSN is empty.
// team must be team #1 with its database name.
func NewStaticDirectory(team Team) (Directory, error) {
	if team.ID != TeamOneID {
		return nil, fmt.Errorf("tenant: static directory holds team #1 only, got team %d", team.ID)
	}
	if team.DBName == "" {
		return nil, fmt.Errorf("tenant: static directory: team #1 has no database name")
	}
	return staticDirectory{team: team}, nil
}

func (d staticDirectory) ByID(_ context.Context, id int64) (Team, error) {
	if id != d.team.ID {
		return Team{}, ErrNotFound
	}
	return d.team, nil
}

func (d staticDirectory) BySlug(_ context.Context, slug string) (Team, error) {
	if !strings.EqualFold(strings.TrimSpace(slug), d.team.Slug) {
		return Team{}, ErrNotFound
	}
	return d.team, nil
}

func (d staticDirectory) ListActive(context.Context) ([]Team, error) {
	return []Team{d.team}, nil
}

func (staticDirectory) Invalidate(int64) {}
