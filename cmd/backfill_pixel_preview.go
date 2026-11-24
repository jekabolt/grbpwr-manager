package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/jekabolt/grbpwr-manager/config"
	"github.com/jekabolt/grbpwr-manager/internal/pixelbackfill"
	"github.com/jekabolt/grbpwr-manager/internal/platformstore"
	"github.com/jekabolt/grbpwr-manager/internal/store"
	"github.com/jekabolt/grbpwr-manager/internal/tenant"
	"github.com/spf13/cobra"
)

// backfill-pixel-preview fills media.pixel_preview (0488) for rows uploaded before the preview was
// generated at upload: it downloads each raster thumbnail, computes the 6 px colour preview with the
// upload path's own helper and writes it only where the column is still NULL. Idempotent; batches of
// --batch rows; every team database in teams mode (or --team). Video/SVG/GLB rows are skipped.
//
// It never migrates: Automigrate is forced off, so the 0488 column must already exist (a deploy of
// this binary applies it). The running server keeps its in-memory caches (products, hero) until
// they refresh; the new previews appear there after the next cache refresh or restart.
var (
	pixelBackfillDryRun bool
	pixelBackfillLimit  int
	pixelBackfillBatch  int
	pixelBackfillTeam   string

	pixelBackfillCmd = &cobra.Command{
		Use:          "backfill-pixel-preview",
		Short:        "Fill media.pixel_preview for old images (idempotent, batched)",
		SilenceUsage: true,
		RunE:         runPixelBackfill,
	}
)

func init() {
	pixelBackfillCmd.Flags().BoolVar(&pixelBackfillDryRun, "dry-run", false, "download and compute, write nothing")
	pixelBackfillCmd.Flags().IntVar(&pixelBackfillLimit, "limit", 0, "stop after this many rows per team (0 = all)")
	pixelBackfillCmd.Flags().IntVar(&pixelBackfillBatch, "batch", 50, "rows per batch")
	pixelBackfillCmd.Flags().StringVar(&pixelBackfillTeam, "team", "", "only this team slug (teams mode); default every active team")
	rootCmd.AddCommand(pixelBackfillCmd)
}

func runPixelBackfill(cmd *cobra.Command, _ []string) error {
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stderr, nil)))
	cfg, err := config.LoadConfigForReadOnlyTooling(cfgFile)
	if err != nil {
		return fmt.Errorf("cannot load a config: %w", err)
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	dbCfg := cfg.DB
	dbCfg.Automigrate = false
	teamOne := dbCfg.Tenant.Team1
	if teamOne.DBName == "" {
		return errors.New("team #1 database unknown (MYSQL_DSN names no database)")
	}

	repo, err := store.NewForTest(tenant.With(ctx, teamOne), dbCfg)
	if err != nil {
		return fmt.Errorf("open database: %w", err)
	}
	defer repo.Close()

	teams := []tenant.Team{teamOne}
	if cfg.Platform.Enabled() {
		pf, err := platformstore.Open(ctx, cfg.Platform.DSN)
		if err != nil {
			return fmt.Errorf("control plane: %w", err)
		}
		listed, err := pf.Directory().ListActive(ctx)
		_ = pf.Close()
		if err != nil {
			return err
		}
		for _, t := range listed {
			if t.ID != teamOne.ID {
				teams = append(teams, t)
			}
		}
	}
	if pixelBackfillTeam != "" {
		var only []tenant.Team
		for _, t := range teams {
			if t.Slug == pixelBackfillTeam {
				only = append(only, t)
			}
		}
		if len(only) == 0 {
			return fmt.Errorf("team %q not found among active teams", pixelBackfillTeam)
		}
		teams = only
	}

	out := cmd.OutOrStdout()
	fetch := pixelbackfill.HTTPFetcher(30 * time.Second)
	var total pixelbackfill.Stats
	for _, t := range teams {
		st, err := pixelbackfill.Run(tenant.With(ctx, t), repo.Media(), fetch, pixelbackfill.Options{
			Batch:  pixelBackfillBatch,
			Limit:  pixelBackfillLimit,
			DryRun: pixelBackfillDryRun,
			Log: func(format string, args ...any) {
				fmt.Fprintf(os.Stderr, "team %s: "+format+"\n", append([]any{t.Slug}, args...)...)
			},
		})
		fmt.Fprintf(out, "team %s: %s\n", t.Slug, st)
		total.Seen += st.Seen
		total.Written += st.Written
		total.Skipped += st.Skipped
		total.Failed += st.Failed
		if err != nil {
			return fmt.Errorf("team %s: %w", t.Slug, err)
		}
	}
	mode := ""
	if pixelBackfillDryRun {
		mode = " (dry-run: nothing written)"
	}
	fmt.Fprintf(out, "total: %s%s\n", total, mode)
	return nil
}
