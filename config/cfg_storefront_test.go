package config

import (
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const testPlatformDSN = "u:p@tcp(h:3306)/lekalo_platform?parseTime=true&tls=custom"

// noShopDefaultOn are the shop switches that default to true when unset: a no-shop installation
// must turn each off explicitly.
var noShopDefaultOn = []string{
	"STRIPE_PAYMENT_ENABLED", "STRIPE_PAYMENT_TEST_ENABLED", "STRIPE_RECONCILE_ENABLED", "DELIVERY_SYNC_ENABLED",
}

// noShopForbidden is every other shop setting TENANT_STOREFRONT_TEAM_ID=none refuses, with a value
// that turns it on.
var noShopForbidden = map[string]string{
	"GA4_ENABLED":                        "true",
	"GA4MP_ENABLED":                      "true",
	"ACCOUNTING_ENABLED":                 "true",
	"BIGQUERY_PROJECT_ID":                "p",
	"BIGQUERY_DATASET_ID":                "d",
	"STRIPE_PAYMENT_SECRET_KEY":          "sk",
	"STRIPE_PAYMENT_PUB_KEY":             "pk",
	"STRIPE_PAYMENT_WEBHOOK_SECRET":      "wh",
	"STRIPE_PAYMENT_TEST_SECRET_KEY":     "sk",
	"STRIPE_PAYMENT_TEST_PUB_KEY":        "pk",
	"STRIPE_PAYMENT_TEST_WEBHOOK_SECRET": "wh",
	"AFTERSHIP_API_KEY":                  "k",
	"AFTERSHIP_WEBHOOK_SECRET":           "s",
	"SENDCLOUD_PUBLIC_KEY":               "k",
	"SENDCLOUD_SECRET_KEY":               "s",
	"GA4_CREDENTIALS_JSON":               "{}",
	"GA4MP_API_SECRET":                   "s",
	"BIGQUERY_CREDENTIALS_JSON":          "{}",
	"MAILER_WEBHOOK_SECRET":              "s",
}

// storefrontEnv gives the test a clean slate: no DSN parts, no shop settings, teams mode as asked.
// MYSQL_DSN keeps the value the test process was started with, so `MYSQL_DSN=...tls=custom go
// test` runs the whole table against the beta DSN shape too.
func storefrontEnv(t *testing.T, teams bool) {
	t.Helper()
	dsn := os.Getenv("MYSQL_DSN")
	clearDBParts(t)
	if dsn != "" {
		t.Setenv("MYSQL_DSN", dsn)
	}
	for _, k := range noShopDefaultOn {
		t.Setenv(k, "")
	}
	for k := range noShopForbidden {
		t.Setenv(k, "")
	}
	t.Setenv("TENANT_STOREFRONT_TEAM_ID", "")
	t.Setenv("TENANT_TEAM1_PROFILE", "")
	t.Setenv("PLATFORM_MYSQL_DSN", "")
	if teams {
		t.Setenv("PLATFORM_MYSQL_DSN", testPlatformDSN)
	}
}

// noShopClean switches off the four default-true shop switches.
func noShopClean(t *testing.T) {
	t.Helper()
	for _, k := range noShopDefaultOn {
		t.Setenv(k, "false")
	}
}

// TestStorefrontTeamTable — TENANT_STOREFRONT_TEAM_ID in both modes. Mode OFF keeps today's
// answers (unset/0 → team 1, any integer as given, a non-integer fails to load) and refuses none;
// mode ON adds none (no shop) and refuses a negative id.
func TestStorefrontTeamTable(t *testing.T) {
	type want struct {
		id     int64
		noShop bool
		err    string
	}
	cases := []struct {
		raw     string
		off, on want
	}{
		{"", want{id: 1}, want{id: 1}},
		{"0", want{id: 1}, want{id: 1}},
		{"1", want{id: 1}, want{id: 1}},
		{"7", want{id: 7}, want{id: 7}},
		{" 7 ", want{id: 7}, want{id: 7}},
		{"-3", want{id: -3}, want{err: "want a positive team id or none"}},
		{"none", want{err: "needs teams mode"}, want{noShop: true}},
		{"None", want{err: "needs teams mode"}, want{noShop: true}},
		{"nope", want{err: "want a positive team id or none"}, want{err: "want a positive team id or none"}},
	}
	for _, c := range cases {
		for _, teams := range []bool{false, true} {
			w := c.off
			mode := "OFF"
			if teams {
				w, mode = c.on, "ON"
			}
			t.Run(mode+"/"+c.raw, func(t *testing.T) {
				storefrontEnv(t, teams)
				noShopClean(t) // so a none case tests the value alone, not the shop switches
				t.Setenv("TENANT_STOREFRONT_TEAM_ID", c.raw)
				cfg, err := LoadConfig("")
				if w.err != "" {
					require.ErrorContains(t, err, w.err)
					return
				}
				require.NoError(t, err)
				assert.Equal(t, w.id, cfg.Platform.StorefrontTeamID)
				assert.Equal(t, w.noShop, cfg.Platform.NoShop())
				assert.Equal(t, w.noShop, cfg.Platform.StorefrontNone)
			})
		}
	}
}

// TestNoShopRefusesEachShopSetting — none boots only with every shop setting off; each one alone
// is refused by name, and the four default-true switches tell the operator to write =false.
func TestNoShopRefusesEachShopSetting(t *testing.T) {
	for _, k := range noShopDefaultOn {
		t.Run(k, func(t *testing.T) {
			storefrontEnv(t, true)
			noShopClean(t)
			t.Setenv(k, "") // unset → its default true
			t.Setenv("TENANT_STOREFRONT_TEAM_ID", "none")
			_, err := LoadConfig("")
			require.ErrorContains(t, err, k+" (defaults to true when unset: set "+k+"=false)")
		})
	}
	for k, v := range noShopForbidden {
		t.Run(k, func(t *testing.T) {
			storefrontEnv(t, true)
			noShopClean(t)
			t.Setenv(k, v)
			t.Setenv("TENANT_STOREFRONT_TEAM_ID", "none")
			_, err := LoadConfig("")
			require.ErrorContains(t, err, "TENANT_STOREFRONT_TEAM_ID=none (no shop)")
			require.ErrorContains(t, err, k)
			assert.Equal(t, 1, strings.Count(err.Error(), ";")+1, "only %s is on, so only it is named: %v", k, err)
		})
	}
	t.Run("all at once names every one", func(t *testing.T) {
		storefrontEnv(t, true)
		for k, v := range noShopForbidden {
			t.Setenv(k, v)
		}
		t.Setenv("TENANT_STOREFRONT_TEAM_ID", "none")
		_, err := LoadConfig("")
		require.Error(t, err)
		for _, k := range noShopDefaultOn {
			assert.ErrorContains(t, err, k)
		}
		for k := range noShopForbidden {
			assert.ErrorContains(t, err, k)
		}
	})
	t.Run("the platform mailer key is not a shop secret", func(t *testing.T) {
		storefrontEnv(t, true)
		noShopClean(t)
		t.Setenv("MAILER_SENDGRID_API_KEY", "re_x") // admin sign-in letters (platform mail) need it
		t.Setenv("TENANT_STOREFRONT_TEAM_ID", "none")
		cfg, err := LoadConfig("")
		require.NoError(t, err)
		assert.True(t, cfg.Platform.NoShop())
	})
}

// TestNumericStorefrontIsAShop — team id 1 in teams mode keeps the shop: NoShop is false even with
// every shop switch at its default.
func TestNumericStorefrontIsAShop(t *testing.T) {
	storefrontEnv(t, true)
	t.Setenv("TENANT_STOREFRONT_TEAM_ID", "1")
	cfg, err := LoadConfig("")
	require.NoError(t, err)
	assert.False(t, cfg.Platform.NoShop())
	assert.Equal(t, int64(1), cfg.Platform.StorefrontTeamID)
}

// TestTeam1Profile — TENANT_TEAM1_PROFILE: all (default) or production; anything else refuses
// boot in teams mode. Mode OFF never reads it.
func TestTeam1Profile(t *testing.T) {
	for _, c := range []struct{ raw, want, err string }{
		{"", "all", ""},
		{"all", "all", ""},
		{" Production ", "production", ""},
		{"production", "production", ""},
		{"standard", "", "TENANT_TEAM1_PROFILE"},
	} {
		t.Run("ON/"+c.raw, func(t *testing.T) {
			storefrontEnv(t, true)
			t.Setenv("TENANT_TEAM1_PROFILE", c.raw)
			cfg, err := LoadConfig("")
			if c.err != "" {
				require.ErrorContains(t, err, c.err)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, c.want, cfg.Platform.Team1Profile)
		})
	}
	t.Run("OFF ignores it", func(t *testing.T) {
		storefrontEnv(t, false)
		t.Setenv("TENANT_TEAM1_PROFILE", "standard")
		_, err := LoadConfig("")
		require.NoError(t, err)
	})
}
