package main

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"
	"golang.org/x/exp/slog"
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

	cfgFile string
	version string
)

func main() {
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, nil)))
	rootCmd.PersistentFlags().StringVarP(&cfgFile, "config", "c", "", "path to configuration file (optional)")
	rootCmd.AddCommand(versionCmd)
	if err := rootCmd.Execute(); err != nil {
		slog.Default().With(err).Error("can't start the service ")
		os.Exit(-1)
	}
}
