package shoppay

import (
	"context"
	"errors"
	"net/url"
	"slices"
	"testing"

	"github.com/jekabolt/grbpwr-manager/internal/platformstore"
)

// A rotation while card orders are in flight goes through only when Stripe proves the new key opens
// the same account. A restricted key that may not read its account proves nothing (the publishable
// key is public), so it waits like any other switch, even with the same publishable key.
func TestKeyRotationWhileBusy(t *testing.T) {
	r := newRig(t)
	ctx := context.Background()
	a := r.team(teamA)
	r.stripe.own["sk_test_alpha"] = platformstore.StripeAccountState{AccountID: "acct_A", ChargesEnabled: true}
	r.stripe.own["sk_test_alpha2"] = platformstore.StripeAccountState{AccountID: "acct_A", ChargesEnabled: true}
	r.stripe.own["rk_test_alpha"] = platformstore.StripeAccountState{ChargesEnabled: true}
	r.stripe.own["sk_test_beta"] = platformstore.StripeAccountState{AccountID: "acct_B", ChargesEnabled: true}
	if err := r.svc.SetKeys(ctx, a, "pk_test_A", "sk_test_alpha", "whsec_1"); err != nil {
		t.Fatal(err)
	}
	r.orders.set(teamA, 1)
	if err := r.svc.SetKeys(ctx, a, "pk_test_A", "sk_test_alpha2", "whsec_2"); err != nil {
		t.Fatalf("rotation proven by Stripe while busy: %v", err)
	}
	if row := r.st.row(teamA); row.KeyLast4 != "pha2" {
		t.Fatalf("rotation not stored: %+v", row)
	}
	if err := r.svc.SetKeys(ctx, a, "pk_test_A", "rk_test_alpha", "whsec_3"); refusal(err) != "FailedPrecondition shop_payments_busy" {
		t.Fatalf("restricted key, same publishable key, while busy: %v", err)
	}
	if err := r.svc.SetKeys(ctx, a, "pk_test_B", "sk_test_beta", "whsec_4"); refusal(err) != "FailedPrecondition shop_payments_busy" {
		t.Fatalf("another account while busy: %v", err)
	}
	if row := r.st.row(teamA); row.KeyLast4 != "pha2" {
		t.Fatalf("keys switched while busy: %+v", row)
	}
	// Idle: the restricted key goes through.
	r.orders.set(teamA, 0)
	if err := r.svc.SetKeys(ctx, a, "pk_test_A", "rk_test_alpha", "whsec_3"); err != nil {
		t.Fatalf("restricted key while idle: %v", err)
	}
}

func TestSameOwnAccount(t *testing.T) {
	acct, empty := "acct_A", ""
	keys := platformstore.TeamStripe{Kind: platformstore.StripeKindKeys, AccountID: &acct, PublishableKey: "pk_test_A"}
	for _, c := range []struct {
		row     platformstore.TeamStripe
		account string
		want    bool
	}{
		{keys, "acct_A", true},
		{keys, "acct_B", false},
		{keys, "", false}, // restricted key: unproven
		{platformstore.TeamStripe{Kind: platformstore.StripeKindKeys, PublishableKey: "pk_test_A"}, "acct_A", false},
		{platformstore.TeamStripe{Kind: platformstore.StripeKindKeys, AccountID: &empty}, "", false},
		{platformstore.TeamStripe{Kind: platformstore.StripeKindConnect, AccountID: &acct}, "acct_A", false},
	} {
		if got := sameOwnAccount(c.row, c.account); got != c.want {
			t.Fatalf("sameOwnAccount(%+v, %q) = %v", c.row, c.account, got)
		}
	}
}

// A refusal after the OAuth exchange never deauthorizes: the authorization is per platform and
// account, and another team's finish for the same account may already hold it.
func TestRefusedFinishKeepsAuthorization(t *testing.T) {
	r := newRig(t)
	ctx := context.Background()
	a := r.team(teamA)
	var states []string
	for i := 0; i < 3; i++ {
		raw, err := r.svc.StartConnect(ctx, a, ownerA, false, "")
		if err != nil {
			t.Fatal(err)
		}
		_, q := stateOf(t, raw)
		states = append(states, q.Get("state"))
	}
	finish := func(code, acct string, live bool) error {
		t.Helper()
		state := states[0]
		states = states[1:]
		r.stripe.codes[code] = codeGrant{acct: acct, live: live}
		tok := finishTokenOf(t, callback(r, url.Values{"state": {state}, "code": {code}}), "https://alpha.example.com/shop?stripe=finish#", code)
		return r.svc.FinishConnect(ctx, a, ownerA, false, tok)
	}
	if err := finish("code-live", "acct_L", true); refusal(err) != "FailedPrecondition stripe_wrong_mode" {
		t.Fatalf("wrong mode: %v", err)
	}
	if err := finish("code-a1", "acct_A1", false); err != nil {
		t.Fatal(err)
	}
	r.orders.set(teamA, 1)
	if err := finish("code-a2", "acct_A2", false); refusal(err) != "FailedPrecondition shop_payments_busy" {
		t.Fatalf("busy: %v", err)
	}
	if len(r.stripe.deauth) != 0 {
		t.Fatalf("a refused finish deauthorized: %v", r.stripe.deauth)
	}
	if row := r.st.row(teamA); row.DisconnectedAt != nil || *row.AccountID != "acct_A1" {
		t.Fatalf("live row %+v", row)
	}
}

// Own keys saved before the finish: refused before the code is exchanged, so nothing is authorized.
func TestFinishOverKeysRefusesBeforeExchange(t *testing.T) {
	r := newRig(t)
	ctx := context.Background()
	a := r.team(teamA)
	raw, err := r.svc.StartConnect(ctx, a, ownerA, false, "")
	if err != nil {
		t.Fatal(err)
	}
	_, q := stateOf(t, raw)
	r.stripe.codes["code-k"] = codeGrant{acct: "acct_K"}
	tok := finishTokenOf(t, callback(r, url.Values{"state": {q.Get("state")}, "code": {"code-k"}}), "https://alpha.example.com/shop?stripe=finish#", "code-k")
	r.stripe.own["sk_test_alpha"] = platformstore.StripeAccountState{AccountID: "acct_A", ChargesEnabled: true}
	if err := r.svc.SetKeys(ctx, a, "pk_test_A", "sk_test_alpha", "whsec_1"); err != nil {
		t.Fatal(err)
	}
	if err := r.svc.FinishConnect(ctx, a, ownerA, false, tok); refusal(err) != "FailedPrecondition stripe_keys_set" {
		t.Fatalf("finish over keys: %v", err)
	}
	if _, ok := r.stripe.codes["code-k"]; !ok {
		t.Fatal("the code was exchanged")
	}
	if len(r.stripe.deauth) != 0 {
		t.Fatalf("deauth %v", r.stripe.deauth)
	}
}

// An operator cuts a busy shop's Stripe off; the owner waits.
func TestOperatorDisconnectsBusyShop(t *testing.T) {
	r := newRig(t)
	ctx := context.Background()
	a := r.team(teamA)
	if err := r.st.ConnectTeamStripe(ctx, teamA, platformstore.StripeAccountState{AccountID: "acct_A", Mode: ModeTest}, r.now); err != nil {
		t.Fatal(err)
	}
	r.orders.set(teamA, 3)
	if err := r.svc.Disconnect(ctx, a, false); refusal(err) != "FailedPrecondition shop_payments_busy" {
		t.Fatalf("owner busy disconnect: %v", err)
	}
	if row := r.st.row(teamA); row.DisconnectedAt != nil {
		t.Fatalf("owner disconnected a busy shop: %+v", row)
	}
	if err := r.svc.Disconnect(ctx, a, true); err != nil {
		t.Fatalf("operator disconnect: %v", err)
	}
	if row := r.st.row(teamA); row.DisconnectedAt == nil || !slices.Equal(r.stripe.deauth, []string{"acct_A"}) {
		t.Fatalf("operator disconnect: %+v %v", row, r.stripe.deauth)
	}
	// An orders read that fails still refuses, operator or not.
	if err := r.st.ConnectTeamStripe(ctx, teamA, platformstore.StripeAccountState{AccountID: "acct_A", Mode: ModeTest}, r.now); err != nil {
		t.Fatal(err)
	}
	r.orders.err = errors.New("db down")
	if err := r.svc.Disconnect(ctx, a, true); err == nil {
		t.Fatal("disconnect without the orders answer")
	}
}
