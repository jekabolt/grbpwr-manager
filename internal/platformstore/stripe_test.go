package platformstore_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/platformstore"
)

// TestTeamStripe: Connect and own keys write one row per team; an account is one team's; a team that
// is not active cannot connect; sync and a deauthorization touch only the account the row still holds;
// a disconnect frees the account and wipes the secrets; the fee survives a reconnect.
func TestTeamStripe(t *testing.T) {
	st := openFresh(t)
	ctx := context.Background()
	seedConsole(t, st)
	now := time.Now().UTC()
	acctA := platformstore.StripeAccountState{AccountID: "acct_A", Name: "Acme", Mode: "test", DetailsSubmitted: true}

	if err := st.ConnectTeamStripe(ctx, 2, acctA, now); err != nil {
		t.Fatal(err)
	}
	row, ok, err := st.TeamStripe(ctx, 2)
	if err != nil || !ok || row.Kind != platformstore.StripeKindConnect || row.AccountID == nil || *row.AccountID != "acct_A" ||
		row.ChargesEnabled || !row.DetailsSubmitted || row.ConnectedAt == nil || row.DisconnectedAt != nil {
		t.Fatalf("connected row %+v %v %v", row, ok, err)
	}
	if err := st.ConnectTeamStripe(ctx, 1, acctA, now); !errors.Is(err, platformstore.ErrStripeAccountTaken) {
		t.Fatalf("another team's account: %v", err)
	}
	if err := st.ConnectTeamStripe(ctx, 3, platformstore.StripeAccountState{AccountID: "acct_W", Mode: "test"}, now); !errors.Is(err, platformstore.ErrWrongTeamStatus) {
		t.Fatalf("team in review: %v", err)
	}
	if got, ok, err := st.TeamStripeByAccount(ctx, "acct_A"); err != nil || !ok || got.TeamID != 2 {
		t.Fatalf("by account %+v %v %v", got, ok, err)
	}

	// Sync matches only the account the row holds.
	acctA.ChargesEnabled, acctA.PayoutsEnabled = true, true
	if ok, err := st.SyncTeamStripe(ctx, 2, acctA); err != nil || !ok {
		t.Fatalf("sync %v %v", ok, err)
	}
	if ok, err := st.SyncTeamStripe(ctx, 2, platformstore.StripeAccountState{AccountID: "acct_B", ChargesEnabled: true}); err != nil || ok {
		t.Fatalf("sync of another account %v %v", ok, err)
	}
	if row, _, _ := st.TeamStripe(ctx, 2); !row.ChargesEnabled || !row.PayoutsEnabled {
		t.Fatalf("synced row %+v", row)
	}

	// Own keys replace Connect: the account is freed for another team; the fee stays.
	if _, err := st.DB().Exec(`UPDATE team_stripe SET fee_bps = 150 WHERE team_id = 2`); err != nil {
		t.Fatal(err)
	}
	keys := platformstore.TeamStripeKeys{
		StripeAccountState: platformstore.StripeAccountState{AccountID: "acct_K", Name: "Acme own", Mode: "test", ChargesEnabled: true},
		PublishableKey:     "pk_test_x", SecretKeySealed: []byte{1, 2, 3}, WebhookSecretSealed: []byte{4, 5}, KeyLast4: "abcd",
	}
	if err := st.SetTeamStripeKeys(ctx, 2, keys, now); err != nil {
		t.Fatal(err)
	}
	row, _, _ = st.TeamStripe(ctx, 2)
	if row.Kind != platformstore.StripeKindKeys || row.AccountID == nil || *row.AccountID != "acct_K" || row.FeeBps != 150 ||
		row.PublishableKey != "pk_test_x" || len(row.SecretKeySealed) != 3 || len(row.WebhookSecretSealed) != 2 || row.KeyLast4 != "abcd" {
		t.Fatalf("keys row %+v", row)
	}
	if _, ok, _ := st.TeamStripeByAccount(ctx, "acct_K"); ok {
		t.Fatal("own keys found as a connect account")
	}
	if err := st.ConnectTeamStripe(ctx, 1, acctA, now); err != nil {
		t.Fatalf("freed account: %v", err)
	}
	if ids, err := st.LiveStripeTeams(ctx, platformstore.StripeKindKeys); err != nil || len(ids) != 1 || ids[0] != 2 {
		t.Fatalf("keys teams %v %v", ids, err)
	}
	if ids, err := st.LiveStripeTeams(ctx, ""); err != nil || len(ids) != 2 {
		t.Fatalf("live teams %v %v", ids, err)
	}

	// Connect over own keys wipes the secrets, keeps the fee.
	if err := st.ConnectTeamStripe(ctx, 2, platformstore.StripeAccountState{AccountID: "acct_C", Mode: "test"}, now); err != nil {
		t.Fatal(err)
	}
	row, _, _ = st.TeamStripe(ctx, 2)
	if row.Kind != platformstore.StripeKindConnect || row.SecretKeySealed != nil || row.WebhookSecretSealed != nil ||
		row.PublishableKey != "" || row.KeyLast4 != "" || row.FeeBps != 150 {
		t.Fatalf("connect over keys %+v", row)
	}

	// A late deauthorization of an older account changes nothing; the right one disconnects.
	if ok, err := st.DisconnectTeamStripe(ctx, 1, "acct_OLD", now); err != nil || ok {
		t.Fatalf("old account disconnect %v %v", ok, err)
	}
	if ok, err := st.DisconnectTeamStripe(ctx, 1, "acct_A", now); err != nil || !ok {
		t.Fatalf("disconnect %v %v", ok, err)
	}
	row, _, _ = st.TeamStripe(ctx, 1)
	if row.AccountID != nil || row.ChargesEnabled || row.DisconnectedAt == nil || row.AccountName != "Acme" {
		t.Fatalf("disconnected row %+v", row)
	}
	if _, ok, _ := st.TeamStripeByAccount(ctx, "acct_A"); ok {
		t.Fatal("a disconnected account still resolves")
	}
	if ok, err := st.DisconnectTeamStripe(ctx, 1, "", now); err != nil || ok {
		t.Fatalf("second disconnect %v %v", ok, err)
	}
	if ok, err := st.SyncTeamStripe(ctx, 1, platformstore.StripeAccountState{ChargesEnabled: true}); err != nil || ok {
		t.Fatalf("sync after disconnect %v %v", ok, err)
	}

	// Own keys whose restricted key cannot read the account: no account id, sync by "".
	keys.AccountID = ""
	if err := st.SetTeamStripeKeys(ctx, 1, keys, now); err != nil {
		t.Fatal(err)
	}
	if row, _, _ := st.TeamStripe(ctx, 1); row.AccountID != nil || row.DisconnectedAt != nil {
		t.Fatalf("keys without account %+v", row)
	}
	if ok, err := st.SyncTeamStripe(ctx, 1, platformstore.StripeAccountState{Name: "x", ChargesEnabled: true}); err != nil || !ok {
		t.Fatalf("sync without account %v %v", ok, err)
	}
	if err := st.SetTeamStripeKeys(ctx, 1, platformstore.TeamStripeKeys{}, now); err == nil {
		t.Fatal("keys without sealed secrets stored")
	}
}
