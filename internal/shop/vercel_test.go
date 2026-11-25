package shop

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

const testToken = "tok-secret-123"

// fakeVercel is a tiny stand-in for the Vercel domains API of one project.
type fakeVercel struct {
	t         *testing.T
	mu        sync.Mutex
	domains   map[string]*fakeDomain // on our project
	elsewhere map[string]bool        // on another project
	throttle  int                    // answer 429 to this many next calls
	fail5xx   int                    // answer 503 to this many next calls
	calls     []string
}

type fakeDomain struct {
	redirect   string // the redirect target ("" = serves the project)
	redirectSC int
	verified   bool
	txt        string // verification TXT value while not verified
	routed     bool   // DNS points at Vercel
	verifyOKAt int    // verify succeeds from this verify call on (0 = never by itself)
	verifies   int
}

func newFakeVercel(t *testing.T) (*fakeVercel, *Vercel) {
	f := &fakeVercel{t: t, domains: map[string]*fakeDomain{}, elsewhere: map[string]bool{}}
	srv := httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(srv.Close)
	v := NewVercel(VercelConfig{Token: testToken, ProjectID: "lekalo-shop", TeamID: "team_x"}, srv.URL, srv.Client())
	v.sleep = func(context.Context, time.Duration) error { return nil }
	return f, v
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func vErr(w http.ResponseWriter, code int, c, msg string) {
	writeJSON(w, code, map[string]any{"error": map[string]string{"code": c, "message": msg}})
}

func (f *fakeVercel) pd(name string, d *fakeDomain) map[string]any {
	labels := strings.Split(name, ".")
	out := map[string]any{"name": name, "apexName": strings.Join(labels[len(labels)-2:], "."), "verified": d.verified}
	if d.redirect != "" {
		out["redirect"], out["redirectStatusCode"] = d.redirect, d.redirectSC
	}
	if !d.verified && d.txt != "" {
		out["verification"] = []map[string]string{{"type": "TXT", "domain": "_vercel." + out["apexName"].(string), "value": d.txt, "reason": "pending_domain_verification"}}
	}
	return out
}

func (f *fakeVercel) serve(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, r.Method+" "+r.URL.Path)
	if r.Header.Get("Authorization") != "Bearer "+testToken {
		vErr(w, 403, "forbidden", "bad token")
		return
	}
	if r.URL.Query().Get("teamId") != "team_x" {
		vErr(w, 403, "forbidden", "no team")
		return
	}
	if f.throttle > 0 {
		f.throttle--
		w.Header().Set("Retry-After", "1")
		vErr(w, 429, "rate_limited", "slow down")
		return
	}
	if f.fail5xx > 0 {
		f.fail5xx--
		vErr(w, 503, "internal", "oops")
		return
	}
	p := r.URL.Path
	switch {
	case r.Method == http.MethodPost && p == "/v10/projects/lekalo-shop/domains":
		var body struct {
			Name               string
			Redirect           string
			RedirectStatusCode int
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		if f.elsewhere[body.Name] {
			vErr(w, 409, "domain_already_in_use", "in use by another project")
			return
		}
		if _, ok := f.domains[body.Name]; ok {
			vErr(w, 409, "domain_already_in_use", "already assigned")
			return
		}
		if strings.Contains(body.Name, "_") {
			vErr(w, 400, "invalid_domain", "bad")
			return
		}
		if body.Redirect != "" && f.domains[body.Redirect] == nil {
			vErr(w, 400, "redirect_target_not_found", "no such target")
			return
		}
		d := &fakeDomain{redirect: body.Redirect, redirectSC: body.RedirectStatusCode}
		f.domains[body.Name] = d
		writeJSON(w, 200, f.pd(body.Name, d))
	case strings.HasPrefix(p, "/v9/projects/lekalo-shop/domains/"):
		rest := strings.TrimPrefix(p, "/v9/projects/lekalo-shop/domains/")
		name, verify := strings.CutSuffix(rest, "/verify")
		d, ok := f.domains[name]
		if !ok {
			vErr(w, 404, "not_found", "no such domain")
			return
		}
		switch {
		case verify && r.Method == http.MethodPost:
			d.verifies++
			if d.verifyOKAt > 0 && d.verifies >= d.verifyOKAt {
				d.verified = true
			}
			if !d.verified {
				vErr(w, 400, "missing_txt_record", "TXT not found")
				return
			}
			writeJSON(w, 200, f.pd(name, d))
		case r.Method == http.MethodGet:
			writeJSON(w, 200, f.pd(name, d))
		case r.Method == http.MethodPatch:
			var body struct {
				Redirect           string
				RedirectStatusCode int
			}
			_ = json.NewDecoder(r.Body).Decode(&body)
			d.redirect, d.redirectSC = body.Redirect, body.RedirectStatusCode
			writeJSON(w, 200, f.pd(name, d))
		case r.Method == http.MethodDelete:
			delete(f.domains, name)
			writeJSON(w, 200, map[string]any{})
		default:
			vErr(w, 405, "method", "no")
		}
	case r.Method == http.MethodGet && strings.HasPrefix(p, "/v6/domains/") && strings.HasSuffix(p, "/config"):
		if r.URL.Query().Get("projectIdOrName") != "lekalo-shop" {
			vErr(w, 400, "bad_request", "project")
			return
		}
		name := strings.TrimSuffix(strings.TrimPrefix(p, "/v6/domains/"), "/config")
		d := f.domains[name]
		routed := d != nil && d.routed
		var by any
		if routed {
			by = "A"
		}
		writeJSON(w, 200, map[string]any{
			"configuredBy":     by,
			"misconfigured":    !routed,
			"recommendedIPv4":  []map[string]any{{"rank": 2, "value": []string{"76.76.21.99"}}, {"rank": 1, "value": []string{"216.198.79.1"}}},
			"recommendedCNAME": []map[string]any{{"rank": 1, "value": "abc123.vercel-dns-017.com."}},
		})
	default:
		vErr(w, 404, "not_found", p)
	}
}

func TestVercelAttachApexHappyPath(t *testing.T) {
	f, v := newFakeVercel(t)
	st, err := v.Attach(context.Background(), "brand.com")
	if err != nil {
		t.Fatal(err)
	}
	if st.Status != "records_needed" {
		t.Fatalf("status = %q", st.Status)
	}
	if len(st.Records) != 1 || st.Records[0] != (Record{Type: "A", Name: "@", Value: "216.198.79.1"}) {
		t.Fatalf("records = %+v", st.Records)
	}
	if _, ok := f.domains["brand.com"]; !ok {
		t.Fatal("domain not on project")
	}
}

func TestVercelAttachSubdomainCNAME(t *testing.T) {
	_, v := newFakeVercel(t)
	st, err := v.Attach(context.Background(), "shop.brand.co")
	if err != nil {
		t.Fatal(err)
	}
	if len(st.Records) != 1 || st.Records[0] != (Record{Type: "CNAME", Name: "shop", Value: "abc123.vercel-dns-017.com"}) {
		t.Fatalf("records = %+v", st.Records)
	}
}

func TestVercelAttachAlreadyOnProjectIsIdempotent(t *testing.T) {
	f, v := newFakeVercel(t)
	f.domains["brand.com"] = &fakeDomain{verified: true, routed: true}
	st, err := v.Attach(context.Background(), "brand.com")
	if err != nil {
		t.Fatal(err)
	}
	if st.Status != "live" || !st.Records[0].Found {
		t.Fatalf("state = %+v", st)
	}
}

func TestVercelAttachTakenByAnotherProject(t *testing.T) {
	f, v := newFakeVercel(t)
	f.elsewhere["brand.com"] = true
	_, err := v.Attach(context.Background(), "brand.com")
	if !errors.Is(err, ErrDomainTaken) {
		t.Fatalf("err = %v, want ErrDomainTaken", err)
	}
}

func TestVercelAttachInvalidIsBadHost(t *testing.T) {
	_, v := newFakeVercel(t)
	_, err := v.Attach(context.Background(), "bad_name.com")
	if !errors.Is(err, ErrBadHost) {
		t.Fatalf("err = %v, want ErrBadHost", err)
	}
}

func TestVercelRetriesOn429And5xx(t *testing.T) {
	f, v := newFakeVercel(t)
	f.throttle = 2
	f.fail5xx = 1
	var waits []time.Duration
	v.sleep = func(_ context.Context, d time.Duration) error { waits = append(waits, d); return nil }
	if _, err := v.Attach(context.Background(), "brand.com"); err != nil {
		t.Fatal(err)
	}
	if len(waits) != 3 || waits[0] != time.Second || waits[1] != time.Second {
		t.Fatalf("waits = %v (Retry-After must be honoured)", waits)
	}
}

func TestVercelGivesUpAfterAttemptsWithoutLeakingToken(t *testing.T) {
	f, v := newFakeVercel(t)
	f.fail5xx = 100
	_, err := v.Attach(context.Background(), "brand.com")
	if err == nil {
		t.Fatal("want error")
	}
	if statusOf(err) != 503 {
		t.Fatalf("err = %v", err)
	}
	if strings.Contains(err.Error(), testToken) || strings.Contains(err.Error(), "team_x") {
		t.Fatalf("error leaks a secret or query: %v", err)
	}
	if n := len(f.calls); n != vercelAttempts {
		t.Fatalf("calls = %d, want %d", n, vercelAttempts)
	}
}

func TestVercelNoRetryOn4xx(t *testing.T) {
	f, v := newFakeVercel(t)
	v.token = "wrong"
	if _, err := v.Attach(context.Background(), "brand.com"); statusOf(err) != 403 {
		t.Fatalf("err = %v", err)
	}
	if len(f.calls) != 1 {
		t.Fatalf("calls = %d", len(f.calls))
	}
}

func TestVercelVerificationPendingThenLive(t *testing.T) {
	f, v := newFakeVercel(t)
	f.domains["brand.com"] = &fakeDomain{txt: "vc-domain-verify=brand.com,abc", verifyOKAt: 3}
	ctx := context.Background()

	st, err := v.Check(ctx, "brand.com")
	if err != nil {
		t.Fatal(err)
	}
	if st.Status != "records_needed" || len(st.Records) != 2 {
		t.Fatalf("first check = %+v", st)
	}
	if st.Records[1] != (Record{Type: "TXT", Name: "_vercel", Value: "vc-domain-verify=brand.com,abc"}) {
		t.Fatalf("txt = %+v", st.Records[1])
	}

	f.domains["brand.com"].routed = true // the team added the A record
	if st, err = v.Check(ctx, "brand.com"); err != nil || st.Status != "verifying" || !st.Records[0].Found {
		t.Fatalf("second check = %+v, %v", st, err)
	}

	// The TXT propagates: verify passes.
	if st, err = v.Check(ctx, "brand.com"); err != nil || st.Status != "live" || len(st.Records) != 1 {
		t.Fatalf("third check = %+v, %v", st, err)
	}
	// Live: no verify call any more.
	before := f.domains["brand.com"].verifies
	if st, err = v.Check(ctx, "brand.com"); err != nil || st.Status != "live" {
		t.Fatalf("fourth check = %+v, %v", st, err)
	}
	if f.domains["brand.com"].verifies != before {
		t.Fatal("verified domain verified again")
	}
}

func TestVercelCheckMissingDoesNotReattach(t *testing.T) {
	f, v := newFakeVercel(t)
	st, err := v.Check(context.Background(), "gone.com")
	if err != nil || st.Status != "failed" {
		t.Fatalf("state = %+v, %v", st, err)
	}
	if _, ok := f.domains["gone.com"]; ok {
		t.Fatal("check re-attached a removed domain")
	}
}

func TestVercelRemoveIdempotent(t *testing.T) {
	f, v := newFakeVercel(t)
	f.domains["brand.com"] = &fakeDomain{}
	ctx := context.Background()
	if err := v.Remove(ctx, "brand.com"); err != nil {
		t.Fatal(err)
	}
	if _, ok := f.domains["brand.com"]; ok {
		t.Fatal("still attached")
	}
	if err := v.Remove(ctx, "brand.com"); err != nil {
		t.Fatalf("second remove: %v", err)
	}
}

func TestVercelConfigEnabled(t *testing.T) {
	if (VercelConfig{Token: "x"}).Enabled() || (VercelConfig{ProjectID: "p"}).Enabled() {
		t.Fatal("half a config must stay fake")
	}
	if !(VercelConfig{Token: "x", ProjectID: "p"}).Enabled() {
		t.Fatal("full config must be enabled")
	}
}

// A www sibling is added as a permanent redirect to its bare domain; again is idempotent; a sibling
// the project has with another target gets the redirect; one on another project is taken.
func TestVercelAttachRedirect(t *testing.T) {
	f, v := newFakeVercel(t)
	ctx := context.Background()
	if _, err := v.Attach(ctx, "brand.com"); err != nil {
		t.Fatal(err)
	}
	if _, err := v.AttachRedirect(ctx, "www.brand.com", "brand.com"); err != nil {
		t.Fatal(err)
	}
	d := f.domains["www.brand.com"]
	if d == nil || d.redirect != "brand.com" || d.redirectSC != http.StatusPermanentRedirect {
		t.Fatalf("redirect: %+v", d)
	}
	f.calls = nil
	if _, err := v.AttachRedirect(ctx, "www.brand.com", "brand.com"); err != nil {
		t.Fatal(err)
	}
	for _, c := range f.calls {
		if strings.HasPrefix(c, http.MethodPatch) {
			t.Fatalf("an unchanged redirect was patched: %v", f.calls)
		}
	}
	d.redirect = ""
	if _, err := v.AttachRedirect(ctx, "www.brand.com", "brand.com"); err != nil {
		t.Fatal(err)
	}
	if d.redirect != "brand.com" || d.redirectSC != http.StatusPermanentRedirect {
		t.Fatalf("patched: %+v", d)
	}
	f.elsewhere["www.other.com"] = true
	if _, err := v.AttachRedirect(ctx, "www.other.com", "brand.com"); !errors.Is(err, ErrDomainTaken) {
		t.Fatalf("elsewhere: %v", err)
	}
	if _, err := v.AttachRedirect(ctx, "www_bad.brand.com", "brand.com"); !errors.Is(err, ErrBadHost) {
		t.Fatalf("bad host: %v", err)
	}
}
