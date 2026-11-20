package platformauth

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"

	"github.com/jekabolt/grbpwr-manager/internal/platformstore"
)

const guardPepper = "0123456789abcdef0123456789abcdef-guard-test"

func testGuard(t *testing.T, cfg GuardConfig, now *time.Time,
	stats func(context.Context, time.Time) (platformstore.SignupLetterStats, error)) *signupGuard {
	t.Helper()
	g := newSignupGuard(cfg, guardPepper, func() time.Time { return *now }, stats)
	t.Cleanup(g.stop)
	return g
}

func defaultGuardCfg() GuardConfig {
	c, _ := GuardFromConfig(platformstore.Config{})
	c.PowMax = 2000 // fast to brute-force in a test
	return c
}

// solve brute-forces a challenge as the browser does and returns the header value.
func solve(t *testing.T, ch Challenge) string {
	t.Helper()
	for n := 0; n <= ch.MaxNumber; n++ {
		if powHash(ch.Salt, n) == ch.Challenge {
			b, _ := json.Marshal(map[string]any{"algorithm": ch.Algorithm, "challenge": ch.Challenge, "number": n,
				"salt": ch.Salt, "signature": ch.Signature, "took": 10})
			return base64.StdEncoding.EncodeToString(b)
		}
	}
	t.Fatal("challenge has no solution")
	return ""
}

func ctxWith(kv ...string) context.Context {
	return metadata.NewIncomingContext(context.Background(), metadata.Pairs(kv...))
}

func TestGuardFromConfig(t *testing.T) {
	g, warn := GuardFromConfig(platformstore.Config{})
	if g.BotCheck != BotCheckLog || g.PowMax != DefaultSignupPowMax || g.MinSubmit != DefaultSignupMinSubmit || !g.MXCheck ||
		g.LettersPerHour != DefaultSignupLettersPerHour || g.MaxBouncePct != DefaultSignupMaxBouncePct || len(warn) != 0 {
		t.Fatalf("defaults = %+v %v", g, warn)
	}
	g, warn = GuardFromConfig(platformstore.Config{SignupBotCheck: "Enforce", SignupPowMax: "50000", SignupMinSubmit: "0s",
		SignupMXCheck: "false", SignupBlockedDomains: " Spam.example , .x.io", SignupLettersPerHour: "0", SignupMaxBouncePct: "7"})
	if g.BotCheck != BotCheckEnforce || g.PowMax != 50000 || g.MinSubmit != 0 || g.MXCheck || !g.Blocked["spam.example"] ||
		!g.Blocked["x.io"] || g.LettersPerHour != 0 || g.MaxBouncePct != 7 || len(warn) != 0 {
		t.Fatalf("set = %+v %v", g, warn)
	}
	g, warn = GuardFromConfig(platformstore.Config{SignupBotCheck: "strict", SignupPowMax: "5", SignupMinSubmit: "soon",
		SignupMXCheck: "maybe", SignupLettersPerHour: "-1", SignupMaxBouncePct: "200"})
	if g.BotCheck != BotCheckLog || g.PowMax != DefaultSignupPowMax || len(warn) != 6 {
		t.Fatalf("bad values = %+v %v", g, warn)
	}
}

func TestProofOfWork(t *testing.T) {
	now := time.Now().UTC()
	g := testGuard(t, defaultGuardCfg(), &now, nil)
	ch, err := g.issue()
	if err != nil {
		t.Fatal(err)
	}
	if ch.Algorithm != "SHA-256" || ch.MaxNumber != 2000 || ch.Signature == "" {
		t.Fatalf("challenge = %+v", ch)
	}
	proof := solve(t, ch)

	if v, why := g.check(context.Background()); v != botNoProof || why != "no_proof" {
		t.Fatalf("no header = %v %s", v, why)
	}
	// Sent at once: a script.
	if v, why := g.check(ctxWith(MDSignupProof, proof)); v != botTrap || why != "too_fast" {
		t.Fatalf("instant = %v %s", v, why)
	}
	// The same proof again (even later) is spent.
	now = now.Add(5 * time.Second)
	if v, why := g.check(ctxWith(MDSignupProof, proof)); v != botNoProof || why != "reused" {
		t.Fatalf("reuse = %v %s", v, why)
	}
	// A fresh one after a human pause passes.
	ch2, _ := g.issue()
	p2 := solve(t, ch2)
	now = now.Add(3 * time.Second)
	if v, why := g.check(ctxWith(MDSignupProof, p2)); v != botOK {
		t.Fatalf("good proof = %v %s", v, why)
	}
	// The honeypot wins over a good proof.
	ch3, _ := g.issue()
	p3 := solve(t, ch3)
	now = now.Add(3 * time.Second)
	if v, why := g.check(ctxWith(MDSignupProof, p3, MDSignupTrap, "http://spam")); v != botTrap || why != "trap" {
		t.Fatalf("trap = %v %s", v, why)
	}
	// Expired.
	ch4, _ := g.issue()
	p4 := solve(t, ch4)
	now = now.Add(SignupChallengeTTL + time.Second)
	if v, why := g.check(ctxWith(MDSignupProof, p4)); v != botNoProof || why != "expired" {
		t.Fatalf("expired = %v %s", v, why)
	}
	// A challenge signed with another key, a changed salt, garbage.
	other := testGuard(t, defaultGuardCfg(), &now, nil)
	other.key = []byte("another key")
	ch5, _ := other.issue()
	if v, _ := g.check(ctxWith(MDSignupProof, solve(t, ch5))); v != botNoProof {
		t.Fatal("foreign signature accepted")
	}
	ch6, _ := g.issue()
	var p map[string]any
	raw, _ := base64.StdEncoding.DecodeString(solve(t, ch6))
	_ = json.Unmarshal(raw, &p)
	p["salt"] = "00?expires=" + strconv.FormatInt(now.Add(time.Hour).Unix(), 10) + "&issued=0"
	b, _ := json.Marshal(p)
	if v, _ := g.check(ctxWith(MDSignupProof, base64.StdEncoding.EncodeToString(b))); v != botNoProof {
		t.Fatal("changed salt accepted")
	}
	if v, _ := g.check(ctxWith(MDSignupProof, "!!not base64!!")); v != botNoProof {
		t.Fatal("garbage accepted")
	}
}

type fakeResolver struct {
	mx    map[string][]*net.MX
	hosts map[string][]string
	fail  map[string]bool
	calls int
}

func (f *fakeResolver) LookupMX(_ context.Context, name string) ([]*net.MX, error) {
	f.calls++
	if f.fail[name] {
		return nil, &net.DNSError{Err: "server misbehaving", Name: name, IsTemporary: true}
	}
	if m, ok := f.mx[name]; ok {
		return m, nil
	}
	return nil, &net.DNSError{Err: "no such host", Name: name, IsNotFound: true}
}

func (f *fakeResolver) LookupHost(_ context.Context, name string) ([]string, error) {
	if h, ok := f.hosts[name]; ok {
		return h, nil
	}
	return nil, &net.DNSError{Err: "no such host", Name: name, IsNotFound: true}
}

func TestCheckAddress(t *testing.T) {
	now := time.Now().UTC()
	cfg := defaultGuardCfg()
	cfg.Blocked = map[string]bool{"blocked.example": true}
	g := testGuard(t, cfg, &now, nil)
	r := &fakeResolver{
		mx:    map[string][]*net.MX{"good.example": {{Host: "mx.good.example.", Pref: 10}}, "nullmx.example": {{Host: "."}}},
		hosts: map[string][]string{"aonly.example": {"192.0.2.1"}},
		fail:  map[string]bool{"flaky.example": true},
	}
	g.resolver = r
	ctx := context.Background()
	for _, c := range []struct {
		email string
		ok    bool
	}{
		{"a@good.example", true},
		{"a@aonly.example", true},
		{"a@flaky.example", true}, // DNS trouble lets the address through
		{"a@nullmx.example", false},
		{"a@nothing.example", false},
		{"a@mailinator.com", false},
		{"a@eu.mailinator.com", false}, // a subdomain of a disposable service
		{"a@blocked.example", false},
		{"a@sub.blocked.example", false},
	} {
		err := g.checkAddress(ctx, c.email)
		if (err == nil) != c.ok {
			t.Errorf("%s: err = %v, want ok=%v", c.email, err, c.ok)
		}
	}
	// Cached: a second look at a definite answer asks no DNS.
	before := r.calls
	_ = g.checkAddress(ctx, "b@good.example")
	_ = g.checkAddress(ctx, "b@nothing.example")
	if r.calls != before {
		t.Fatalf("cache missed: %d → %d", before, r.calls)
	}
	// Not cached: a failure is asked again.
	_ = g.checkAddress(ctx, "b@flaky.example")
	if r.calls != before+1 {
		t.Fatalf("flaky cached: %d → %d", before, r.calls)
	}
	g.cfg.MXCheck = false
	if err := g.checkAddress(ctx, "a@nothing.example"); err != nil {
		t.Fatal("MX check off still refuses")
	}
}

func TestBreaker(t *testing.T) {
	now := time.Now().UTC()
	st := platformstore.SignupLetterStats{}
	var statErr error
	reads := 0
	g := testGuard(t, defaultGuardCfg(), &now, func(context.Context, time.Time) (platformstore.SignupLetterStats, error) {
		reads++
		return st, statErr
	})
	ctx := context.Background()
	step := func() (bool, bool) { now = now.Add(breakerRecheck); return g.breaker(ctx) }
	if busy, alert := g.breaker(ctx); busy || alert {
		t.Fatal("open on zero")
	}
	st.VerifyLastHour = DefaultSignupLettersPerHour
	if busy, _ := g.breaker(ctx); busy {
		t.Fatal("re-read before the recheck interval")
	}
	if busy, alert := step(); !busy || alert {
		t.Fatalf("hourly cap: busy=%v alert=%v", busy, alert)
	}
	// A read error keeps the states.
	statErr = errors.New("db down")
	if busy, _ := step(); !busy {
		t.Fatal("closed on a read error")
	}
	statErr = nil
	// The review's case: one bounce in 20 letters on a quiet day is not judged at all.
	st = platformstore.SignupLetterStats{VerifyLastHour: 1, Sent24h: 20, Bounced24h: 1}
	if busy, alert := step(); busy || alert {
		t.Fatalf("quiet day: busy=%v alert=%v", busy, alert)
	}
	st = platformstore.SignupLetterStats{VerifyLastHour: 1, Sent24h: DefaultSignupBounceMinSample - 1, Bounced24h: 40}
	if _, alert := step(); alert {
		t.Fatal("bounce share judged below the minimum sample")
	}
	// Over the share: an alert, never busy.
	st = platformstore.SignupLetterStats{VerifyLastHour: 1, Sent24h: 100, Bounced24h: 90}
	if busy, alert := step(); busy || !alert {
		t.Fatalf("bounce share: busy=%v alert=%v", busy, alert)
	}
	st.Bounced24h = 3
	if _, alert := step(); alert {
		t.Fatal("alert did not close")
	}
	g.cfg.LettersPerHour, g.cfg.MaxBouncePct = 0, 0
	n := reads
	st.VerifyLastHour = 1 << 20
	if busy, alert := step(); busy || alert || reads != n {
		t.Fatal("disabled breaker read or opened")
	}
}

func TestGuardLimits(t *testing.T) {
	now := time.Now().UTC()
	cfg := defaultGuardCfg()
	cfg.PerDomainHour = 4
	g := testGuard(t, cfg, &now, nil)
	// No alert: the per-domain limit only; the per-IP one lives in SignUp.
	for i := range 4 {
		if !g.limits("10.0.0."+strconv.Itoa(i), "a@one.example", false) {
			t.Fatalf("sign-up %d refused", i)
		}
	}
	if g.limits("10.0.0.9", "z@one.example", false) {
		t.Fatal("per-domain limit not applied")
	}
	// Alert: halved per-IP (5/2 = 2) and per-domain (4/2 = 2) limits.
	if !g.limits("10.1.0.1", "a@two.example", true) || !g.limits("10.1.0.1", "b@three.example", true) {
		t.Fatal("refused under the halved limits")
	}
	if g.limits("10.1.0.1", "c@four.example", true) {
		t.Fatal("halved per-IP limit not applied")
	}
	if !g.limits("10.2.0.1", "a@five.example", true) || !g.limits("10.2.0.2", "b@five.example", true) {
		t.Fatal("refused under the halved domain limit")
	}
	if g.limits("10.2.0.3", "c@five.example", true) {
		t.Fatal("halved per-domain limit not applied")
	}
	// The same counts without the alert pass: halved limits bite only while it is up.
	if !g.limits("10.1.0.1", "d@six.example", false) {
		t.Fatal("halved per-IP limit applied without the alert")
	}
	// Bot-check mode while the alert is up: only ever stricter.
	g.cfg.BotCheck, g.cfg.BotCheckAlert = BotCheckLog, BotCheckEnforce
	if g.botMode(false) != BotCheckLog || g.botMode(true) != BotCheckEnforce {
		t.Fatal("alert bot-check mode")
	}
	g.cfg.BotCheck, g.cfg.BotCheckAlert = BotCheckEnforce, BotCheckOff
	if g.botMode(true) != BotCheckEnforce {
		t.Fatal("alert mode loosened the bot check")
	}
}

// The bounce share never refuses a sign-up; only the hourly cap answers signup_busy.
func TestGuardSignUpBreaker(t *testing.T) {
	now := time.Now().UTC()
	cfg := defaultGuardCfg()
	cfg.BotCheck, cfg.MXCheck = BotCheckOff, false
	st := platformstore.SignupLetterStats{VerifyLastHour: 1, Sent24h: 1000, Bounced24h: 900}
	g := testGuard(t, cfg, &now, func(context.Context, time.Time) (platformstore.SignupLetterStats, error) { return st, nil })
	s := &Service{guard: g}
	if drop, err := s.guardSignUp(context.Background(), "anna@good.example"); drop || err != nil {
		t.Fatalf("bounce share refused: drop=%v err=%v", drop, err)
	}
	st = platformstore.SignupLetterStats{VerifyLastHour: DefaultSignupLettersPerHour}
	now = now.Add(breakerRecheck)
	if _, err := s.guardSignUp(context.Background(), "ola@good.example"); status.Code(err) != codes.Unavailable {
		t.Fatalf("hourly cap: %v", err)
	}
}

func TestChallengeHandler(t *testing.T) {
	now := time.Now().UTC()
	s := &Service{pepper: guardPepper, base: "beta.lekalo.studio"}
	s.guard = testGuard(t, defaultGuardCfg(), &now, nil)
	h := s.ChallengeHandler()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/auth/signup-challenge", nil))
	var ch Challenge
	if rec.Code != http.StatusOK || rec.Header().Get("Cache-Control") != "no-store" || json.Unmarshal(rec.Body.Bytes(), &ch) != nil ||
		ch.Signature == "" {
		t.Fatalf("challenge = %d %s", rec.Code, rec.Body.String())
	}
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/auth/signup-challenge", nil))
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("POST = %d", rec.Code)
	}
	for range MaxSignupChallengesPerIPMin {
		h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/api/auth/signup-challenge", nil))
	}
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/auth/signup-challenge", nil))
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("over the limit = %d", rec.Code)
	}
}
