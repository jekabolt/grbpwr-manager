package app

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/auth/pwhash"
	"github.com/jekabolt/grbpwr-manager/internal/bucket"
	"github.com/jekabolt/grbpwr-manager/internal/platformauth"
	"github.com/jekabolt/grbpwr-manager/internal/store"
	"github.com/jekabolt/grbpwr-manager/internal/teamwipe"
)

// Day-30 loop (lanes D3 + S4c): purge deleted accounts, then wipe deleted teams, whose 30 days
// passed. PLATFORM_PURGE_MODE off / dry (default) / on. Teams mode only.
const (
	day30First = time.Minute
	day30Every = 10 * time.Minute
)

// teamDatabases binds store.DropTeamDB / store.TeamDBSize to this installation.
type teamDatabases struct {
	baseDSN, prefix, platformDB string
}

func (d teamDatabases) Drop(ctx context.Context, name string) (store.DropResult, error) {
	return store.DropTeamDB(ctx, d.baseDSN, d.prefix, d.platformDB, name)
}

func (d teamDatabases) Size(ctx context.Context, name string) (bool, int64, error) {
	return store.TeamDBSize(ctx, d.baseDSN, d.prefix, d.platformDB, name)
}

// startDay30 starts the day-30 loop; nil stop when teams mode is off or PLATFORM_PURGE_MODE=off.
func (a *App) startDay30(ctx context.Context) (func(), error) {
	if a.platform == nil {
		return nil, nil
	}
	mode := teamwipe.ParseMode(a.c.Platform.PurgeMode)
	slog.Default().InfoContext(ctx, "teams: day-30 loop", slog.String("mode", string(mode)))
	if mode == teamwipe.ModeOff {
		return nil, nil
	}
	platformDB, err := store.DSNDatabase(a.c.Platform.DSN)
	if err != nil {
		return nil, fmt.Errorf("day-30: control plane database: %w", err)
	}
	if platformDB == "" {
		return nil, errors.New("day-30: PLATFORM_DSN names no database")
	}
	ph, err := pwhash.New(a.c.Auth.PasswordHasherSaltSize, a.c.Auth.PasswordHasherIterations)
	if err != nil {
		return nil, fmt.Errorf("day-30: hasher: %w", err)
	}
	var files teamwipe.Files
	if bk, ok := a.b.(*bucket.Bucket); ok {
		files = bk
	}
	dbs := teamDatabases{baseDSN: a.c.DB.DSN, prefix: a.c.Platform.DBPrefix, platformDB: platformDB}
	w := teamwipe.New(mode, a.platform, files, dbs, platformauth.UnusableHash(ph), day30First, day30Every)
	w.Start(ctx)
	return w.Stop, nil
}
