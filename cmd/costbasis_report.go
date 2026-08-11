package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/jekabolt/grbpwr-manager/config"
	"github.com/jekabolt/grbpwr-manager/internal/cache"
	"github.com/jekabolt/grbpwr-manager/internal/costbasisreport"
	"github.com/jekabolt/grbpwr-manager/internal/store"
	"github.com/spf13/cobra"
)

// costbasis-report is the before/after instrument for a costing-basis change. Current comparison
// (v2, the T6 range-average change): it prices every tech card BOTH ways — the outgoing basis
// (the norm on the style's base sample size) and the incoming one (the simple average over the
// declared size range) — and reports the difference per card × colourway × расход, plus which
// styles stop having a computable cost at all.
//
// IT WRITES NOTHING, and that is a property of how it connects, not a promise in a comment: it
// builds the repository with store.NewForTest and Automigrate forced OFF. The normal store.New
// would apply pending migrations AND run the SKU backfill — two writes — against whatever database
// the config points at, which for this tool is usually production.
var (
	costBasisJSON        bool
	costBasisOnlyChanged bool
	costBasisCards       string
	costBasisPageSize    int

	costBasisCmd = &cobra.Command{
		Use:   "costbasis-report",
		Short: "READ-ONLY before/after report for the range-average costing basis (T6)",
		Long: "Prices every tech card on the outgoing basis (the norm on the style's base sample " +
			"size) and on the incoming one (the simple average over the declared size range), and " +
			"prints the per-card, per-colourway and per-usage difference, which styles become " +
			"uncosted, whose product.cost_price may be overwritten, and which costing sign-offs go " +
			"stale. Opens the database read-only (no migrations, no SKU backfill, no writes).",
		RunE: runCostBasisReport,
	}
)

func init() {
	costBasisCmd.Flags().BoolVar(&costBasisJSON, "json", false, "emit the machine-readable document instead of the table")
	costBasisCmd.Flags().BoolVar(&costBasisOnlyChanged, "only-changed", false, "list only cards whose cost actually moves")
	costBasisCmd.Flags().StringVar(&costBasisCards, "cards", "", "comma list of tech card ids (default: every card)")
	costBasisCmd.Flags().IntVar(&costBasisPageSize, "page-size", 100, "tech-card list page size (store caps it at 100)")
	rootCmd.AddCommand(costBasisCmd)
}

func runCostBasisReport(cmd *cobra.Command, args []string) error {
	// Diagnostics go to stderr; stdout carries the report and nothing else. main() points the
	// default logger at stdout, which is right for the serving binaries (App Platform collects
	// it) and wrong here: the store logs the CA-certificate path when it connects, and that one
	// line ahead of the document makes `--json | jq` fail with "extra data" — the flag exists
	// precisely to be piped. Scoped to this command rather than changed in main for that reason.
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stderr, nil)))

	// The read-only loader: this command opens the database and prints. Requiring the
	// serving secrets (JWT key, pattern-token pepper) would only force production keys
	// into the shell of whoever runs the report.
	cfg, err := config.LoadConfigForReadOnlyTooling(cfgFile)
	if err != nil {
		return fmt.Errorf("cannot load a config: %w", err)
	}

	ids, err := parseCardIDs(costBasisCards)
	if err != nil {
		return err
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
	defer cancel()

	dbCfg := cfg.DB
	// Forced off regardless of what the environment says. Beta and prod both set
	// MYSQL_AUTOMIGRATE=true, and a reporting tool must never be the thing that applies a migration.
	dbCfg.Automigrate = false

	repo, err := store.NewForTest(ctx, dbCfg)
	if err != nil {
		return fmt.Errorf("cannot open the database: %w", err)
	}
	defer repo.Close()

	// The base currency is normally set during app boot (app.App.Start). It is not cosmetic here:
	// the material-catalog read path picks each material's LatestPrice by comparing against it, so
	// leaving it at the package default would price pinned articles from the wrong currency's quote.
	baseCcy := strings.ToUpper(strings.TrimSpace(cfg.Rates.BaseCurrency))
	if baseCcy == "" {
		return fmt.Errorf("rates.base_currency (RATES_BASE_CURRENCY) is not configured; " +
			"the report will not guess the base currency the costs are folded into")
	}
	cache.SetDefaultCurrency(baseCcy)

	rep, err := costbasisreport.Run(ctx, repo, baseCcy, costbasisreport.Options{
		CardIDs:     ids,
		OnlyChanged: costBasisOnlyChanged,
		JSON:        costBasisJSON,
		PageSize:    costBasisPageSize,
	})
	if err != nil {
		return err
	}
	return rep.Write(os.Stdout, costbasisreport.Options{JSON: costBasisJSON})
}

func parseCardIDs(s string) ([]int, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil, nil
	}
	var out []int
	for _, part := range strings.Split(s, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		id, err := strconv.Atoi(part)
		if err != nil || id <= 0 {
			return nil, fmt.Errorf("--cards: %q is not a tech card id", part)
		}
		out = append(out, id)
	}
	return out, nil
}
