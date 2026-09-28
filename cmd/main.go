package main

import (
	"fmt"
	"os"

	"log/slog"

	// ⚠ THE RUNTIME IMAGE (alpine, Dockerfile) SHIPS NO tzdata: without this embed time.LoadLocation("Europe/Warsaw")
	// fails on beta and prod, and every local-day computation (the design budget day, entity/budget_day.go; the
	// OpenRouter midnight diff, aiprov/reconcile) is either refused or silently UTC. Seen live on beta 28.09 (commit F).
	_ "time/tzdata"

	"github.com/spf13/cobra"
)

var (
	rootCmd = &cobra.Command{
		Use:   "grbpwr-products-manager",
		Short: "Service to handle grbpwr-products-manager ",
		RunE:  run,
	}

	versionCmd = &cobra.Command{
		Use:   "version",
		Short: "Print the grbpwr-products-manager service version",
		Run: func(cmd *cobra.Command, args []string) {
			fmt.Println(version)
		},
	}

	cfgFile    string
	version    string
	commitHash string
)

func main() {
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, nil)))
	rootCmd.PersistentFlags().StringVarP(&cfgFile, "config", "c", "", "path to configuration file (optional)")
	rootCmd.AddCommand(versionCmd)
	if err := rootCmd.Execute(); err != nil {
		slog.Default().Error("can't start the service",
			slog.String("err", err.Error()),
		)
		os.Exit(-1)
	}
}
