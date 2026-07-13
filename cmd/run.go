package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"log/slog"

	"github.com/jekabolt/grbpwr-manager/app"
	"github.com/jekabolt/grbpwr-manager/config"
	"github.com/spf13/cobra"
)

func run(cmd *cobra.Command, args []string) error {
	cfg, err := config.LoadConfig(cfgFile)
	if err != nil {
		return fmt.Errorf("cannot load a config %v", err.Error())
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{
		Level:     slog.Level(cfg.Logger.Level),
		AddSource: cfg.Logger.AddSource,
	}))
	slog.SetDefault(logger)

	app.SetCommitHash(commitHash)
	a := app.New(cfg)
	if err := a.Start(ctx); err != nil {
		return fmt.Errorf("cannot start the application %v", err.Error())
	}

	sigCh := make(chan os.Signal, 1)
	// SIGHUP is intentionally excluded: the service has no config hot-reload, so a
	// stray hangup (controlling-terminal close, some process managers) must not be
	// routed to a full shutdown. DO App Platform terminates instances with SIGTERM.
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
