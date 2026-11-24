package shoppay

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/dependency"
	"github.com/jekabolt/grbpwr-manager/internal/platformstore"
	"github.com/jekabolt/grbpwr-manager/internal/tenant"
	"github.com/stripe/stripe-go/v79"
	"github.com/stripe/stripe-go/v79/webhook"
)

// ---- control plane ----

type fakeStore struct {
	mu       sync.Mutex
	rows     map[int64]platformstore.TeamStripe
	roles    map[[2]int64]string // (user, team) → role
	audits   []string
	inactive map[int64]bool // teams the store refuses (not active)
}

func newFakeStore() *fakeStore {
	return &fakeStore{rows: map[int64]platformstore.TeamStripe{}, roles: map[[2]int64]string{}, inactive: map[int64]bool{}}
}

func strp(s string) *string { return &s }

func (f *fakeStore) TeamStripe(_ context.Context, id int64) (platformstore.TeamStripe, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	r, ok := f.rows[id]
	return r, ok, nil
}

func (f *fakeStore) TeamStripeByAccount(_ context.Context, acct string) (platformstore.TeamStripe, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, r := range f.rows {
		if r.Kind == platformstore.StripeKindConnect && r.DisconnectedAt == nil && r.AccountID != nil && *r.AccountID == acct {
			return r, true, nil
		}
	}
	return platformstore.TeamStripe{}, false, nil
}

func (f *fakeStore) LiveStripeTeams(_ context.Context, kind string) ([]int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []int64
	for id, r := range f.rows {
		if r.DisconnectedAt == nil && (kind == "" || r.Kind == kind) {
			out = append(out, id)
		}
	}
	slices.Sort(out)
	return out, nil
}

func (f *fakeStore) claim(teamID int64, acct string) error {
	if f.inactive[teamID] {
		return platformstore.ErrWrongTeamStatus
	}
	if acct == "" {
		return nil
	}
	for id, r := range f.rows {
		if id != teamID && r.AccountID != nil && *r.AccountID == acct {
			return platformstore.ErrStripeAccountTaken
		}
	}
	return nil
}

func (f *fakeStore) ConnectTeamStripe(_ context.Context, teamID int64, a platformstore.StripeAccountState, now time.Time) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.claim(teamID, a.AccountID); err != nil {
		return err
	}
	f.rows[teamID] = platformstore.TeamStripe{TeamID: teamID, Kind: platformstore.StripeKindConnect, AccountID: strp(a.AccountID),
		AccountName: a.Name, Mode: a.Mode, ChargesEnabled: a.ChargesEnabled, PayoutsEnabled: a.PayoutsEnabled,
		DetailsSubmitted: a.DetailsSubmitted, FeeBps: f.rows[teamID].FeeBps, ConnectedAt: &now}
	return nil
}

func (f *fakeStore) SetTeamStripeKeys(_ context.Context, teamID int64, k platformstore.TeamStripeKeys, now time.Time) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.claim(teamID, k.AccountID); err != nil {
		return err
	}
	var acct *string
	if k.AccountID != "" {
		acct = strp(k.AccountID)
	}
	f.rows[teamID] = platformstore.TeamStripe{TeamID: teamID, Kind: platformstore.StripeKindKeys, AccountID: acct,
		AccountName: k.Name, Mode: k.Mode, ChargesEnabled: k.ChargesEnabled, PayoutsEnabled: k.PayoutsEnabled,
		DetailsSubmitted: k.DetailsSubmitted, PublishableKey: k.PublishableKey, SecretKeySealed: k.SecretKeySealed,
		WebhookSecretSealed: k.WebhookSecretSealed, KeyLast4: k.KeyLast4, ConnectedAt: &now}
	return nil
}

func (f *fakeStore) SyncTeamStripe(_ context.Context, teamID int64, a platformstore.StripeAccountState) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	r, ok := f.rows[teamID]
	cur := ""
	if ok && r.AccountID != nil {
		cur = *r.AccountID
	}
	if !ok || r.DisconnectedAt != nil || cur != a.AccountID {
		return false, nil
	}
	r.AccountName, r.ChargesEnabled, r.PayoutsEnabled, r.DetailsSubmitted = a.Name, a.ChargesEnabled, a.PayoutsEnabled, a.DetailsSubmitted
	f.rows[teamID] = r
	return true, nil
}

func (f *fakeStore) DisconnectTeamStripe(_ context.Context, teamID int64, only string, now time.Time) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	r, ok := f.rows[teamID]
	if !ok || r.DisconnectedAt != nil || (only != "" && (r.AccountID == nil || *r.AccountID != only)) {
		return false, nil
	}
	r.AccountID, r.ChargesEnabled, r.PayoutsEnabled, r.DetailsSubmitted = nil, false, false, false
	r.PublishableKey, r.SecretKeySealed, r.WebhookSecretSealed, r.DisconnectedAt = "", nil, nil, &now
	f.rows[teamID] = r
	return true, nil
}

func (f *fakeStore) Membership(_ context.Context, userID, teamID int64) (platformstore.MemberTeam, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	role, ok := f.roles[[2]int64{userID, teamID}]
	return platformstore.MemberTeam{TeamID: teamID, Role: role}, ok, nil
}

func (f *fakeStore) AuditAuth(_ context.Context, _, _ int64, action string, _ map[string]string, _ string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.audits = append(f.audits, action)
	return nil
}

func (f *fakeStore) row(id int64) platformstore.TeamStripe {
	r, _, _ := f.TeamStripe(context.Background(), id)
	return r
}

type fakeTeams struct {
	mu        sync.Mutex
	teams     map[int64]tenant.Team
	behind    map[int64]bool
	operators map[int64]bool
}

func (f *fakeTeams) TeamByID(_ context.Context, id int64) (tenant.Team, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	t, ok := f.teams[id]
	if !ok {
		return tenant.Team{}, errors.New("no team")
	}
	return t, nil
}

func (f *fakeTeams) SchemaBehind(t tenant.Team) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.behind[t.ID]
}

func (f *fakeTeams) Operator(_ context.Context, userID int64) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.operators[userID], nil
}

// ---- Stripe ----

type codeGrant struct {
	acct string
	live bool
}

type fakeStripeAPI struct {
	mu       sync.Mutex
	codes    map[string]codeGrant
	accounts map[string]platformstore.StripeAccountState // connected, by account
	own      map[string]platformstore.StripeAccountState // by secret key
	ownErr   error
	deauth   []string
}

func (f *fakeStripeAPI) ExchangeCode(_ context.Context, code string) (string, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	g, ok := f.codes[code]
	if !ok {
		return "", false, errors.New("invalid_grant")
	}
	delete(f.codes, code)
	return g.acct, g.live, nil
}

func (f *fakeStripeAPI) Deauthorize(_ context.Context, acct string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.deauth = append(f.deauth, acct)
	return nil
}

func (f *fakeStripeAPI) ConnectedAccount(_ context.Context, acct string) (platformstore.StripeAccountState, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	a, ok := f.accounts[acct]
	if !ok {
		return platformstore.StripeAccountState{}, errors.New("no such account")
	}
	return a, nil
}

func (f *fakeStripeAPI) OwnAccount(_ context.Context, sk string) (platformstore.StripeAccountState, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.ownErr != nil {
		return platformstore.StripeAccountState{}, f.ownErr
	}
	a, ok := f.own[sk]
	if !ok {
		return platformstore.StripeAccountState{}, ErrBadSecretKey
	}
	return a, nil
}

// ---- processors ----

// ledger is every team's orders: an event pays an order only when the team it runs under has it.
type ledger struct {
	mu     sync.Mutex
	orders map[int64]map[string]bool // team → order → paid
}

func (l *ledger) add(team int64, order string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.orders[team] == nil {
		l.orders[team] = map[string]bool{}
	}
	l.orders[team][order] = false
}

func (l *ledger) paid(team int64, order string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.orders[team][order]
}

type fakeProc struct {
	dependency.Invoicer
	spec    ProcessorSpec
	l       *ledger
	mu      sync.Mutex
	stopped bool
	events  int
}

func (p *fakeProc) DispatchEvent(ctx context.Context, ev stripe.Event) int {
	t, ok := tenant.From(ctx)
	if !ok || t.ID != p.spec.Team.ID {
		// A processor must only ever run under its own team.
		return http.StatusTeapot
	}
	p.mu.Lock()
	p.events++
	p.mu.Unlock()
	if ev.Type != stripe.EventTypePaymentIntentSucceeded {
		return http.StatusOK
	}
	var pi stripe.PaymentIntent
	if json.Unmarshal(ev.Data.Raw, &pi) != nil {
		return http.StatusBadRequest
	}
	order := pi.Metadata["order_id"]
	p.l.mu.Lock()
	defer p.l.mu.Unlock()
	if _, has := p.l.orders[t.ID][order]; has {
		p.l.orders[t.ID][order] = true
	}
	return http.StatusOK
}

func (p *fakeProc) StopAllMonitors(context.Context) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.stopped = true
}

func (p *fakeProc) isStopped() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.stopped
}

// ---- the rig ----

const (
	testMaster        = "a-test-master-secret-that-is-long-enough-0123456789"
	testPlatformKey   = "sk_test_platform"
	testConnectSecret = "whsec_connect_platform"
	teamA, teamB      = int64(2), int64(3)
	ownerA, ownerB    = int64(101), int64(102)
)

type rig struct {
	svc    *Service
	st     *fakeStore
	teams  *fakeTeams
	stripe *fakeStripeAPI
	l      *ledger
	mu     sync.Mutex
	built  []*fakeProc
	now    time.Time
}

func newRig(t *testing.T) *rig {
	t.Helper()
	r := &rig{
		st: newFakeStore(),
		teams: &fakeTeams{teams: map[int64]tenant.Team{
			teamA: {ID: teamA, Slug: "alpha", Status: tenant.StatusActive},
			teamB: {ID: teamB, Slug: "bravo", Status: tenant.StatusActive},
		}, behind: map[int64]bool{}, operators: map[int64]bool{}},
		stripe: &fakeStripeAPI{codes: map[string]codeGrant{}, accounts: map[string]platformstore.StripeAccountState{},
			own: map[string]platformstore.StripeAccountState{}},
		l:   &ledger{orders: map[int64]map[string]bool{}},
		now: time.Date(2026, 11, 24, 12, 0, 0, 0, time.UTC),
	}
	r.st.roles[[2]int64{ownerA, teamA}] = "owner"
	r.st.roles[[2]int64{ownerB, teamB}] = "owner"
	r.svc = New(context.Background(), Deps{
		Config:     Config{ConnectClientID: "ca_test", ConnectSecretKey: testPlatformKey, ConnectPublishableKey: "pk_test_platform", ConnectWebhookSecret: testConnectSecret, Master: testMaster},
		APIBase:    "https://api.example.com",
		BaseDomain: "example.com",
		Store:      r.st,
		Teams:      r.teams,
		Stripe:     r.stripe,
		Factory: func(ctx context.Context, spec ProcessorSpec) (Processor, error) {
			if tt, ok := tenant.From(ctx); !ok || tt.ID != spec.Team.ID {
				return nil, errors.New("build ctx without the team")
			}
			p := &fakeProc{spec: spec, l: r.l}
			r.mu.Lock()
			r.built = append(r.built, p)
			r.mu.Unlock()
			return p, nil
		},
		Now: func() time.Time { return r.now },
	})
	return r
}

func (r *rig) builds() []*fakeProc {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]*fakeProc(nil), r.built...)
}

func (r *rig) team(id int64) tenant.Team {
	t, _ := r.teams.TeamByID(context.Background(), id)
	return t
}

// ownKeys gives a team its own test keys (account acct, webhook secret whsec).
func (r *rig) ownKeys(t *testing.T, id int64, sk, acct, whsec string) {
	t.Helper()
	r.stripe.own[sk] = platformstore.StripeAccountState{AccountID: acct, Name: "shop " + acct, ChargesEnabled: true}
	if err := r.svc.SetKeys(context.Background(), r.team(id), "pk_test_"+acct, sk, whsec); err != nil {
		t.Fatalf("set keys: %v", err)
	}
}

func eventJSON(t *testing.T, typ stripe.EventType, account string, livemode bool, obj any) []byte {
	t.Helper()
	raw, err := json.Marshal(obj)
	if err != nil {
		t.Fatal(err)
	}
	b, err := json.Marshal(map[string]any{"id": "evt_test", "object": "event", "type": typ, "account": account,
		"livemode": livemode, "api_version": "2020-08-27", "data": map[string]any{"object": json.RawMessage(raw)}})
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func paidEvent(t *testing.T, account string, livemode bool, order string) []byte {
	return eventJSON(t, stripe.EventTypePaymentIntentSucceeded, account, livemode,
		map[string]any{"id": "pi_" + order, "object": "payment_intent", "amount_received": 1000, "currency": "eur",
			"metadata": map[string]string{"order_id": order}})
}

func sign(payload []byte, secret string) string {
	return webhook.GenerateTestSignedPayload(&webhook.UnsignedPayload{Payload: payload, Secret: secret}).Header
}
