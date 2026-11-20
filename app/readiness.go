package app

import (
	"context"
	"fmt"
	"time"

	"github.com/jmoiron/sqlx"

	"github.com/jekabolt/grbpwr-manager/internal/platformstore"
)

// readinessPing is what /readyz checks. Single install: team #1's database. Teams mode: also the
// control database (people, teams, sessions) — without it nobody can sign in, so a 200 would lie.
// The two pings run side by side so the probe stays well inside its 1 s timeout.
func readinessPing(teamOne func(context.Context) error, plane *platformstore.Store) func(context.Context) error {
	if plane == nil {
		return teamOne
	}
	control := plane.DB()
	return func(ctx context.Context) error {
		ctrl := make(chan error, 1)
		go func() { ctrl <- pingControl(ctx, control) }()
		err := teamOne(ctx)
		if cerr := <-ctrl; cerr != nil && err == nil {
			err = cerr
		}
		return err
	}
}

func pingControl(ctx context.Context, db *sqlx.DB) error {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	var one int
	if err := db.QueryRowxContext(ctx, "SELECT 1").Scan(&one); err != nil {
		return fmt.Errorf("control database ping failed: %w", err)
	}
	return nil
}
