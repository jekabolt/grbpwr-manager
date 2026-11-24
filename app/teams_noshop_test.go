package app

import (
	"context"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"strconv"
	"strings"
	"testing"

	"github.com/jekabolt/grbpwr-manager/config"
	bq "github.com/jekabolt/grbpwr-manager/internal/analytics/bigquery"
	"github.com/jekabolt/grbpwr-manager/internal/analytics/ga4"
	"github.com/jekabolt/grbpwr-manager/internal/platformstore"
)

// The components a no-shop installation does not start, and the ones it keeps (team shops: the
// storefront API, checkout holds and per-shop revalidation run without a storefront team).
var (
	wantSkipped = []string{
		"marketingaggregate", "mail", "campaigndispatch", "storefrontcleanup",
		"tiermanagement", "opexmaterialize", "acctposting", "stripe", "stripereconcile",
		"ordercleanup", "deliverysync", "ga4", "bigquery", "ga4sync", "ga4mp", "webhooks",
	}
	wantKept = []string{"stockreserve", "taskarchive", "fxsync", "aireconcile", "archivecleanup", "revalidation", "storefront"}
)

func appWith(p platformstore.Config) *App {
	return &App{c: &config.Config{Platform: p}}
}

var (
	noShopPlatform  = platformstore.Config{DSN: "u:p@tcp(h:3306)/lekalo", StorefrontNone: true}
	teamOnePlatform = platformstore.Config{DSN: "u:p@tcp(h:3306)/lekalo", StorefrontTeamID: 1}
)

// TestShopPlan — the one seam App.Start asks: what a no-shop installation skips and keeps, and that
// every installation with a shop (mode OFF, or teams mode with a storefront id) starts everything.
func TestShopPlan(t *testing.T) {
	start, skip := shopPlan(true)
	if fmt.Sprint(skip) != fmt.Sprint(wantSkipped) || fmt.Sprint(start) != fmt.Sprint(wantKept) {
		t.Fatalf("no shop:\n skip %v\nstart %v", skip, start)
	}
	start, skip = shopPlan(false)
	if len(skip) != 0 || len(start) != len(bootComponents()) {
		t.Fatalf("shop: skip %v start %v", skip, start)
	}

	ctx := context.Background()
	for name, p := range map[string]platformstore.Config{
		"mode OFF":         {},
		"mode OFF + none":  {StorefrontNone: true}, // impossible after config, still a shop
		"teams, team id 1": teamOnePlatform,
		"teams, team id 7": {DSN: "x", StorefrontTeamID: 7},
	} {
		a := appWith(p)
		for _, c := range bootComponents() {
			if !a.shopGate(ctx, c.name) {
				t.Errorf("%s: %s skipped", name, c.name)
			}
		}
		if len(a.noShopSkipped) != 0 {
			t.Errorf("%s: skipped %v", name, a.noShopSkipped)
		}
	}

	a := appWith(noShopPlatform)
	for _, c := range bootComponents() {
		if got := a.shopGate(ctx, c.name); got != !c.shop {
			t.Errorf("no shop: %s started=%v", c.name, got)
		}
	}
	if fmt.Sprint(a.noShopSkipped) != fmt.Sprint(wantSkipped) {
		t.Fatalf("no shop recorded %v", a.noShopSkipped)
	}
}

// TestEveryShopGateIsInTheTable — app.go asks shopGate only for names in bootComponents, each row
// is asked exactly once, so a new worker cannot slip past the no-shop switch and a renamed one
// cannot leave a dead row.
func TestEveryShopGateIsInTheTable(t *testing.T) {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "app.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]int{}
	ast.Inspect(f, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok || sel.Sel.Name != "shopGate" {
			return true
		}
		if len(call.Args) != 2 {
			t.Errorf("%s: shopGate with %d args", fset.Position(call.Pos()), len(call.Args))
			return true
		}
		lit, ok := call.Args[1].(*ast.BasicLit)
		if !ok || lit.Kind != token.STRING {
			t.Errorf("%s: shopGate name must be a string literal", fset.Position(call.Pos()))
			return true
		}
		name, _ := strconv.Unquote(lit.Value)
		seen[name]++
		return true
	})
	rows := map[string]bool{}
	for _, c := range bootComponents() {
		rows[c.name] = true
		if seen[c.name] != 1 {
			t.Errorf("bootComponents row %q gated %d times in app.go, want 1", c.name, seen[c.name])
		}
	}
	for name := range seen {
		if !rows[name] {
			t.Errorf("app.go gates %q, which bootComponents does not list", name)
		}
	}
}

// TestHealthRegistryAnalyticsOff — /statusz with analytics off (review item 7). bq.NewClient
// answers (nil, nil); a.bqc must stay a nil interface, so the registry lists no BigQuery breaker.
// The getters are nil-safe too, so even the old typed-nil wiring cannot panic.
func TestHealthRegistryAnalyticsOff(t *testing.T) {
	c, err := bq.NewClient(context.Background(), &bq.Config{})
	if err != nil || c != nil {
		t.Fatalf("unconfigured BigQuery = %v, %v; want nil, nil", c, err)
	}
	a := appWith(platformstore.Config{})
	a.bqc = bigQueryClient(c)
	if a.bqc != nil {
		t.Fatal("a nil *bq.Client became a non-nil dependency.BQClient")
	}
	if reg := a.buildHealthRegistry(nil); len(reg.Breakers) != 0 {
		t.Fatalf("analytics off: breakers %v", reg.Breakers)
	}

	// The old wiring: a typed nil in the interface. Every breaker must still answer.
	var nilBQ *bq.Client
	var nilGA4 *ga4.Client
	a.bqc = nilBQ
	reg := a.buildHealthRegistry(nilGA4)
	for _, b := range reg.Breakers {
		_ = b.StateFunc() // panicked before the getter was nil-safe
	}
	_ = nilGA4.CircuitBreakerState()
}

// A boot fault that leaves a feature off (here team shops without their customer master) is logged
// and listed in GET /statusz, never a silently healthy server.
func TestBootProblemsReachStatusz(t *testing.T) {
	a := appWith(platformstore.Config{})
	a.noteProblem(context.Background(), "shops: STOREFRONT_AUTH_SHOP_MASTER is empty; team shop customers cannot sign in")
	reg := a.buildHealthRegistry(nil)
	if len(reg.Problems) != 1 || !strings.Contains(reg.Problems[0], "STOREFRONT_AUTH_SHOP_MASTER") {
		t.Fatalf("problems = %v", reg.Problems)
	}
}
