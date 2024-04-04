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

	a := app.New(cfg)
	if err := a.Start(ctx); err != nil {
		return fmt.Errorf("cannot start the application %v", err.Error())
	}

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM, syscall.SIGHUP)
	select {
	case s := <-sigCh:
		logger.With("signal", s.String()).Warn("signal received, exiting")
		a.Stop(ctx)
		logger.Info("application exited")
	case <-a.Done():
		logger.Error("application exited")
	}

	return nil
}
