// Package shoppay takes a team shop's card payments (X18-L3) through the team's own Stripe: Stripe
// Connect (a connected Standard account, direct charges, the platform's key plus Stripe-Account) or
// the team's own keys (the secret key and the webhook secret sealed at rest under SHOP_STRIPE_MASTER).
// The storefront team (grbpwr) never comes here: its processors stay the env-keyed ones in app.
//
// Pieces: the owner's admin actions (Service: StartConnect, SetKeys, Disconnect), the Connect OAuth
// callback, the two webhook endpoints (one for every connected account, one per own-keys team), and
// the Registry that builds a team's processor on demand and keeps its payment monitors.
//
// NOTHING HERE LOGS A KEY, a secret, a code or a state token.
package shoppay

import (
	"strings"
)

// Config is the platform's Stripe Connect application and the master of sealed team keys.
type Config struct {
	// ConnectClientID is the Connect application's client id (ca_…): STRIPE_CONNECT_CLIENT_ID.
	ConnectClientID string `mapstructure:"connect_client_id"`
	// ConnectSecretKey is the platform account's secret key (sk_live_… / sk_test_…), the account whose
	// publishable key the storefront loads Stripe.js with: STRIPE_CONNECT_SECRET_KEY. Its mode is the
	// mode of every connected team.
	ConnectSecretKey string `mapstructure:"connect_secret_key"`
	// ConnectPublishableKey is the same platform account's publishable key (pk_…, same mode): a
	// connected team's storefront loads Stripe.js with it and the team's account.
	// STRIPE_CONNECT_PUBLISHABLE_KEY.
	ConnectPublishableKey string `mapstructure:"connect_publishable_key"`
	// ConnectWebhookSecret signs the platform's Connect webhook endpoint (whsec_…):
	// STRIPE_CONNECT_WEBHOOK_SECRET. Empty = the Connect endpoint answers 404.
	ConnectWebhookSecret string `mapstructure:"connect_webhook_secret"`
	// Master seals the teams' own keys and signs the Connect state and the webhook addresses: at
	// least 32 bytes, SHOP_STRIPE_MASTER. Empty = own keys and Connect are both off.
	Master string `mapstructure:"master"`
}

// String never prints a secret.
func (c Config) String() string {
	set := func(s string) string {
		if strings.TrimSpace(s) == "" {
			return "unset"
		}
		return "set"
	}
	return "shoppay.Config{connect_client_id:" + set(c.ConnectClientID) + " connect_secret_key:" + set(c.ConnectSecretKey) +
		" connect_publishable_key:" + set(c.ConnectPublishableKey) +
		" connect_webhook_secret:" + set(c.ConnectWebhookSecret) + " master:" + set(c.Master) + "}"
}

// GoString — see String.
func (c Config) GoString() string { return c.String() }

// MinMasterBytes is the shortest SHOP_STRIPE_MASTER accepted.
const MinMasterBytes = 32

func (c Config) masterOK() bool { return len(strings.TrimSpace(c.Master)) >= MinMasterBytes }

// KeysReady: teams can store their own keys.
func (c Config) KeysReady() bool { return c.masterOK() }

// ConnectReady: teams can connect a Stripe account.
func (c Config) ConnectReady() bool {
	mode := keyMode(c.ConnectSecretKey)
	return c.masterOK() && strings.HasPrefix(strings.TrimSpace(c.ConnectClientID), "ca_") && mode != "" &&
		publishableMode(strings.TrimSpace(c.ConnectPublishableKey)) == mode
}

// ConnectMode is the mode of the platform key: live | test ("" when unset).
func (c Config) ConnectMode() string { return keyMode(c.ConnectSecretKey) }

// Stripe modes (team_stripe.mode).
const (
	ModeLive = "live"
	ModeTest = "test"
)

// keyMode is the mode of a secret or restricted key: live | test, "" when it is neither.
func keyMode(k string) string {
	k = strings.TrimSpace(k)
	switch {
	case strings.HasPrefix(k, "sk_live_"), strings.HasPrefix(k, "rk_live_"):
		return ModeLive
	case strings.HasPrefix(k, "sk_test_"), strings.HasPrefix(k, "rk_test_"):
		return ModeTest
	}
	return ""
}

// publishableMode is the mode of a publishable key, "" when it is not one.
func publishableMode(k string) string {
	switch {
	case strings.HasPrefix(k, "pk_live_"):
		return ModeLive
	case strings.HasPrefix(k, "pk_test_"):
		return ModeTest
	}
	return ""
}
