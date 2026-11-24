package shoppay

import (
	"context"
	"errors"
	"net/url"
	"slices"
	"testing"

	"github.com/jekabolt/grbpwr-manager/internal/platformstore"
)

// A live row saved with a restricted key has no account id: a rotation keeps the publishable key, so
// it is the same account and goes through while card orders are in flight; another publishable key
// still waits for them.
func TestRestrictedKeyRotationWhileBusy(t *testing.T) {
	r := newRig(t)
	ctx := context.Background()
	a := r.team(teamA)
	r.stripe.own["rk_test_one"] = platformstore.StripeAccountState{ChargesEnabled: true}
	r.stripe.own["rk_test_two"] = platformstore.StripeAccountState{ChargesEnabled: true}
	r.stripe.own["rk_test_three"] = platformstore.StripeAccountState{ChargesEnabled: true}
	if err := r.svc.SetKeys(ctx, a, "pk_test_R", "rk_test_one", "whsec_1"); err != nil {
		t.Fatal(err)
	}
	if row := r.st.row(teamA); row.AccountID != nil {
		t.Fatalf("restricted key row has an account: %+v", row)
	}
	r.orders.set(teamA, 1)
	if err := r.svc.SetKeys(ctx, a, "pk_test_R", "rk_test_two", "whsec_2"); err != nil {
		t.Fatalf("rotation with the same publishable key while busy: %v", err)
	}
	if row := r.st.row(teamA); row.KeyLast4 != "_two" {
		t.Fatalf("rotation not stored: %+v", row)
	}
	if err := r.svc.SetKeys(ctx, a, "pk_test_X", "rk_test_three", "whsec_3"); refusal(err) != "FailedPrecondition shop_payments_busy" {
		t.Fatalf("another publishable key while busy: %v", err)
	}
	if row := r.st.row(teamA); row.PublishableKey != "pk_test_R" || row.KeyLast4 != "_two" {
		t.Fatalf("keys switched while busy: %+v", row)
	}
}

// A row with a known account rotated to a restricted key (same publishable key) keeps its account.
func TestRotationToRestrictedKeyKeepsAccount(t *testing.T) {
	r := newRig(t)
	ctx := context.Background()
	a := r.team(teamA)
	r.stripe.own["sk_test_alpha"] = platformstore.StripeAccountState{AccountID: "acct_A", ChargesEnabled: true}
	r.stripe.own["rk_test_alpha"] = platformstore.StripeAccountState{ChargesEnabled: true}
	if err := r.svc.SetKeys(ctx, a, "pk_test_A", "sk_test_alpha", "whsec_1"); err != nil {
		t.Fatal(err)
	}
	r.orders.set(teamA, 1)
	if err := r.svc.SetKeys(ctx, a, "pk_test_A", "rk_test_alpha", "whsec_2"); err != nil {
		t.Fatalf("rotation to a restricted key while busy: %v", err)
	}
	if row := r.st.row(teamA); row.AccountID == nil || *row.AccountID != "acct_A" || row.KeyLast4 != "lpha" {
		t.Fatalf("rotated row %+v", row)
	}
	if err := r.svc.SetKeys(ctx, a, "pk_test_B", "rk_test_alpha", "whsec_3"); refusal(err) != "FailedPrecondition shop_payments_busy" {
		t.Fatalf("restricted key with another publishable key while busy: %v", err)
	}
}

func TestSameOwnAccount(t *testing.T) {
	acct := "acct_A"
	keys := platformstore.TeamStripe{Kind: platformstore.StripeKindKeys, AccountID: &acct, PublishableKey: "pk_test_A"}
	for _, c := range []struct {
		row     platformstore.TeamStripe
		account string
		pk      string
		want    bool
	}{
		{keys, "acct_A", "pk_test_other", true}, // the account opens: the ids decide
		{keys, "acct_B", "pk_test_A", false},    // two known accounts differ
		{keys, "", "pk_test_A", true},           // restricted key, same publishable key
		{keys, "", "pk_test_B", false},          // restricted key, another publishable key
		{platformstore.TeamStripe{Kind: platformstore.StripeKindKeys, PublishableKey: "pk_test_A"}, "acct_A", "pk_test_A", true},
		{platformstore.TeamStripe{Kind: platformstore.StripeKindKeys}, "", "", false},
		{platformstore.TeamStripe{Kind: platformstore.StripeKindConnect, AccountID: &acct}, "acct_A", "pk_test_A", false},
	} {
		if got, _ := sameOwnAccount(c.row, c.account, c.pk); got != c.want {
			t.Fatalf("sameOwnAccount(%+v, %q, %q) = %v", c.row, c.account, c.pk, got)
		}
	}
}

// Every refusal after the OAuth exchange lets the just-authorized account go at Stripe, unless that
// account is live on a team (this one or another): one authorization serves both.
func TestRefusedFinishDeauthorizes(t *testing.T) {
	r := newRig(t)
	ctx := context.Background()
	a := r.team(teamA)
	// States are started up front: a team with a live Connect gets the dashboard, not a new state.
	var states []string
	for i := 0; i < 5; i++ {
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

	// Wrong mode: refused, deauthorized.
	if err := finish("code-live", "acct_L", true); refusal(err) != "FailedPrecondition stripe_wrong_mode" {
		t.Fatalf("wrong mode: %v", err)
	}
	if !slices.Equal(r.stripe.deauth, []string{"acct_L"}) {
		t.Fatalf("wrong mode deauth %v", r.stripe.deauth)
	}

	// Taken by team B: refused, B's authorization stays.
	if err := r.st.ConnectTeamStripe(ctx, teamB, platformstore.StripeAccountState{AccountID: "acct_B", Mode: ModeTest}, r.now); err != nil {
		t.Fatal(err)
	}
	if err := finish("code-taken", "acct_B", false); refusal(err) != "AlreadyExists stripe_account_taken" {
		t.Fatalf("taken: %v", err)
	}
	if !slices.Equal(r.stripe.deauth, []string{"acct_L"}) {
		t.Fatalf("taken account deauthorized: %v", r.stripe.deauth)
	}

	// Busy switch: refused, the new account goes, the live one stays.
	if err := finish("code-a1", "acct_A1", false); err != nil {
		t.Fatal(err)
	}
	r.orders.set(teamA, 1)
	if err := finish("code-a2", "acct_A2", false); refusal(err) != "FailedPrecondition shop_payments_busy" {
		t.Fatalf("busy: %v", err)
	}
	if !slices.Equal(r.stripe.deauth, []string{"acct_L", "acct_A2"}) {
		t.Fatalf("busy deauth %v", r.stripe.deauth)
	}

	// A store failure on reconnecting the live account: refused, the live account is never let go.
	r.st.failConnect = errors.New("db down")
	if err := finish("code-a1b", "acct_A1", false); err == nil {
		t.Fatal("store failure connected")
	}
	r.st.failConnect = nil
	if !slices.Equal(r.stripe.deauth, []string{"acct_L", "acct_A2"}) {
		t.Fatalf("live account deauthorized: %v", r.stripe.deauth)
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
