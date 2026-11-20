package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"log/slog"

	"github.com/jekabolt/grbpwr-manager/app"
	"github.com/jekabolt/grbpwr-manager/config"
	"github.com/jekabolt/grbpwr-manager/internal/observability"
	"github.com/spf13/cobra"
)

// migrate-fleet (X3) runs the team migrations with the NEW binary before a deploy (a DO App Platform
// PRE_DEPLOY job), so teams already fit the new code when it starts serving. Exit 0: the run
// happened (failed teams are listed in the summary and keep their old version; the running app keeps
// retrying them). Exit 1: it could not run (config, team #1, control plane). --strict: exit 2 when
// a team failed or is still behind.
var (
	fleetTimeout time.Duration
	fleetStrict  bool

	migrateFleetCmd = &cobra.Command{
		Use:          "migrate-fleet",
		Short:        "Migrate team #1 and every team database, then exit (pre-deploy job)",
		SilenceUsage: true,
		RunE:         runMigrateFleet,
	}
)

func init() {
	migrateFleetCmd.Flags().DurationVar(&fleetTimeout, "timeout", 10*time.Minute, "stop starting new teams after this long")
	migrateFleetCmd.Flags().BoolVar(&fleetStrict, "strict", false, "exit 2 when a team failed or is still behind")
	rootCmd.AddCommand(migrateFleetCmd)
}

func runMigrateFleet(cmd *cobra.Command, _ []string) error {
	cfg, err := config.LoadConfig(cfgFile)
	if err != nil {
		return fmt.Errorf("cannot load a config %v", err.Error())
	}
	_, _ = observability.Init(cfg.Sentry, commitHash)
	defer observability.Flush(2 * time.Second)
	logger := slog.New(observability.NewSlogHandler(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{
		Level: slog.Level(cfg.Logger.Level),
	})))
	slog.SetDefault(logger)
	app.SetCommitHash(commitHash)

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	ctx, cancel := context.WithTimeout(ctx, fleetTimeout)
	defer cancel()

	start := time.Now()
	run, err := app.MigrateFleet(ctx, cfg)
	if err != nil {
		logger.Error("migrate-fleet: cannot run", slog.String("err", err.Error()))
		return err
	}
	line := fleetSummary(run, time.Since(start))
	fmt.Println(line)
	if fleetStrict && (len(run.Failed) > 0 || run.Behind > 0) {
		os.Exit(2)
	}
	return nil
}

// fleetSummary is the one line the job prints (team slugs, never ids).
func fleetSummary(run app.FleetRun, took time.Duration) string {
	if !run.TeamsMode {
		return fmt.Sprintf("migrate-fleet: team #1 migrated; teams mode off (%s)", took.Round(time.Second))
	}
	failed := "none"
	if len(run.Failed) > 0 {
		failed = strings.Join(run.Failed, ",")
	}
	return fmt.Sprintf("migrate-fleet: version %s; migrated %d; failed %s; still behind %d; passes %d (%s)",
		run.Latest, len(run.Migrated), failed, run.Behind, run.Passes, took.Round(time.Second))
}
