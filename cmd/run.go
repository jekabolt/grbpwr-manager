package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	"log/slog"

	"github.com/jekabolt/grbpwr-manager/app"
	"github.com/jekabolt/grbpwr-manager/config"
	"github.com/jekabolt/grbpwr-manager/internal/observability"
	"github.com/spf13/cobra"
)

func run(cmd *cobra.Command, args []string) error {
	cfg, err := config.LoadConfig(cfgFile)
	if err != nil {
		return fmt.Errorf("cannot load a config %v", err.Error())
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Sentry first, so the logger below can forward Error records. SENTRY_DSN unset: Init is a
	// no-op and the logger is the plain JSON handler, exactly as before.
	sentryOn, sentryErr := observability.Init(cfg.Sentry, commitHash)
	defer observability.Flush(2 * time.Second)

	logger := slog.New(observability.NewSlogHandler(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{
		Level:     slog.Level(cfg.Logger.Level),
		AddSource: cfg.Logger.AddSource,
	})))
	slog.SetDefault(logger)
	if sentryErr != nil {
		// A bad DSN must not keep the service down; it only means no error reporting.
		logger.Warn("sentry disabled: cannot init", slog.String("err", sentryErr.Error()))
	} else if sentryOn {
		logger.Info("sentry enabled", slog.String("environment", observability.EnvironmentOf(cfg.Sentry)))
	}

	app.SetCommitHash(commitHash)
	a := app.New(cfg)
	if err := a.Start(ctx); err != nil {
		// Start wires resources incrementally (DB, mailer, workers, HTTP). On a
		// mid-sequence failure the already-started pieces — worker goroutines, a
		// live DB pool, Stripe monitors — would otherwise leak until process exit.
		// Stop is idempotent and partial-init safe, so tear down what came up.
		a.Stop(ctx)
		return fmt.Errorf("cannot start the application %v", err.Error())
	}

	sigCh := make(chan os.Signal, 1)
	// The service has no config hot-reload, so a stray SIGHUP (controlling-terminal
	// close, some process managers) must be a genuine no-op. It has to be ignored
	// *explicitly*: merely leaving it out of signal.Notify reverts it to the OS default
	// disposition, which for SIGHUP is to terminate the process abruptly — no graceful
	// drain, worse than the shutdown we're trying to avoid. DO App Platform terminates
	// instances with SIGTERM, which we do handle below.
	signal.Ignore(syscall.SIGHUP)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	select {
	case s := <-sigCh:
		logger.With("signal", s.String()).Warn("signal received, exiting")
		a.Stop(ctx)
		logger.Info("application exited")
		return nil
	case <-a.Done():
		// Reached only when the app tore itself down without an OS signal — the
		// HTTP listener exited unexpectedly and the crash bridge invoked Stop.
		// Return an error so the process exits non-zero and the platform restarts it.
		logger.Error("application exited unexpectedly")
		return fmt.Errorf("application exited unexpectedly")
	}
}
