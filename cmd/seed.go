package main

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/betaseed"
	"github.com/spf13/cobra"
)

// seed populates the BETA environment end-to-end (catalog, PLM, empty admin
// sections, analytics + order volume) so every admin screen has representative
// data to look at. It talks ONLY to backend-beta.grbpwr.com (the betaseed client
// refuses any other host) — it can never touch prod.
//
// Throwaway beta bot credentials (NOT a real secret — a disposable test account
// on the beta env only). The admin account is bootstrapped on first run via the
// master password read from the DO beta app spec.
const (
	defaultSeedUser     = "beta-seed-bot"
	defaultSeedPassword = "Seedb0t-beta-2026!"
	defaultMasterYAML   = ".do/app-beta.yaml"
)

var (
	seedVolume     string
	seedOnly       string
	seedUser       string
	seedPassword   string
	seedMasterYAML string
	seedVerbose    bool

	seedCmd = &cobra.Command{
		Use:   "seed",
		Short: "Populate the BETA environment with representative data across every admin function",
		Long: "Seeds the beta backend (backend-beta.grbpwr.com ONLY) with a fully-populated catalog, " +
			"the full PLM flow, every otherwise-empty admin section, and enough settled orders + config " +
			"to light up the analytics dashboards. Re-runnable and idempotent.",
		RunE: runSeed,
	}
)

func init() {
	seedCmd.Flags().StringVar(&seedVolume, "volume", "dense", "data volume: single | moderate | dense")
	seedCmd.Flags().StringVar(&seedOnly, "only", "all", "comma list of phases to run: catalog,plm,extras,analytics,enrich,accounting (or all); 'verify' = read-only coverage")
	seedCmd.Flags().StringVar(&seedUser, "user", defaultSeedUser, "admin username to log in / bootstrap")
	seedCmd.Flags().StringVar(&seedPassword, "password", "", "admin password (default: throwaway beta bot pw, or $BETA_SEED_PASSWORD)")
	seedCmd.Flags().StringVar(&seedMasterYAML, "master-yaml", defaultMasterYAML, "DO app spec YAML to read AUTH_MASTER_PASSWORD from (for first-run bootstrap)")
	seedCmd.Flags().BoolVar(&seedVerbose, "verbose", false, "log every RPC (method, path, HTTP code)")
	rootCmd.AddCommand(seedCmd)
}

func parseVolume(s string) (betaseed.Volume, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "single":
		return betaseed.VolSingle, nil
	case "moderate":
		return betaseed.VolModerate, nil
	case "dense", "":
		return betaseed.VolDense, nil
	default:
		return 0, fmt.Errorf("unknown volume %q (want single|moderate|dense)", s)
	}
}

// phaseSet resolves --only into a membership set, expanding "all" and pulling in
// prerequisites (analytics needs catalog+plm data to order against).
func phaseSet(only string) map[string]bool {
	set := map[string]bool{}
	only = strings.ToLower(strings.TrimSpace(only))
	if only == "" || only == "all" {
		for _, p := range []string{"catalog", "plm", "extras", "analytics", "enrich", "accounting"} {
			set[p] = true
		}
		return set
	}
	for _, p := range strings.Split(only, ",") {
		set[strings.TrimSpace(p)] = true
	}
	if set["analytics"] { // analytics orders reference catalog + plm products
		set["catalog"] = true
		set["plm"] = true
	}
	if set["enrich"] { // enrichment needs catalog + plm entities in-memory; it resolves tasks/members/
		// invites/orders via List RPCs, so it does NOT require the (rate-limited) extras phase.
		set["catalog"] = true
		set["plm"] = true
	}
	return set
}

func runSeed(cmd *cobra.Command, args []string) error {
	vol, err := parseVolume(seedVolume)
	if err != nil {
		return err
	}
	phases := phaseSet(seedOnly)

	pw := seedPassword
	if pw == "" {
		pw = os.Getenv("BETA_SEED_PASSWORD")
	}
	if pw == "" {
		pw = defaultSeedPassword
	}

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Minute)
	defer cancel()

	c, err := betaseed.NewClient(betaseed.BetaBackend)
	if err != nil {
		return err
	}
	if seedVerbose {
		c.LogRPC = func(verb, path string, code int) { fmt.Printf("  → %-6s %-3d %s\n", verb, code, path) }
	}

	masterPw := ""
	if mp, err := betaseed.MasterPasswordFromYAML(seedMasterYAML); err == nil {
		masterPw = mp // best-effort: only needed if the account must be bootstrapped
	}
	if err := c.Authenticate(ctx, seedUser, pw, masterPw); err != nil {
		return fmt.Errorf("authenticate: %w", err)
	}
	fmt.Printf("== seed: authenticated as %s against %s (volume=%s) ==\n", seedUser, betaseed.BetaBackend, seedVolume)

	s, err := betaseed.NewSeeder(ctx, c, vol)
	if err != nil {
		return err
	}
	s.Log = func(format string, a ...any) { fmt.Printf(format+"\n", a...) }

	// verify-only: read-back coverage without seeding anything.
	if strings.EqualFold(strings.TrimSpace(seedOnly), "verify") {
		s.PrintCoverage(ctx)
		return nil
	}

	var cat []betaseed.CatalogResult
	var plm *betaseed.PLMResult

	if phases["catalog"] {
		fmt.Println("\n===== PHASE: catalog =====")
		cat, err = s.SeedCatalog(ctx)
		if err != nil {
			return fmt.Errorf("catalog: %w", err)
		}
		fmt.Printf("catalog: %d published styles\n", len(cat))
	}
	if phases["plm"] {
		fmt.Println("\n===== PHASE: plm =====")
		plm, err = s.SeedPLM(ctx)
		if err != nil {
			return fmt.Errorf("plm: %w", err)
		}
		fmt.Println("plm: full tech-card flow complete")
	}
	if phases["extras"] {
		fmt.Println("\n===== PHASE: extras =====")
		if _, err = s.SeedExtras(ctx); err != nil {
			return fmt.Errorf("extras: %w", err)
		}
		fmt.Println("extras: empty admin sections populated")
	}
	if phases["analytics"] {
		fmt.Println("\n===== PHASE: analytics =====")
		if _, err = s.SeedAnalytics(ctx, cat, plm); err != nil {
			return fmt.Errorf("analytics: %w", err)
		}
		fmt.Println("analytics: config + order volume seeded")
	}
	if phases["enrich"] {
		fmt.Println("\n===== PHASE: enrich =====")
		if _, err = s.SeedEnrichment(ctx, cat, plm); err != nil {
			return fmt.Errorf("enrich: %w", err)
		}
		fmt.Println("enrich: REST-seedable gaps filled (empty screens + archived/hidden/deleted tabs)")
	}
	if phases["accounting"] {
		fmt.Println("\n===== PHASE: accounting =====")
		// Self-contained (no catalog/plm deps): seeds deterministic manual journal entries + a reversal
		// + a period-lifecycle touch, then proves the ledger reads back balanced and non-empty.
		if _, err = s.SeedAccounting(ctx); err != nil {
			return fmt.Errorf("accounting: %w", err)
		}
		fmt.Println("accounting: manual journal entries + reversal + period lifecycle seeded")
	}

	fmt.Println("\n===== COVERAGE =====")
	s.PrintCoverage(ctx)

	fmt.Println("\n===== SEED COMPLETE =====")
	return nil
}
