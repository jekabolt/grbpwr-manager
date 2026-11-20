package oauthflow

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/platformstore"
	"github.com/jekabolt/grbpwr-manager/internal/tenant"
)

// fakeExpires is the expiry the fake gives a state row (the store sets the real one).
var fakeExpires = time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)

const acmeProfile = "https://acme.beta.lekalo.studio/profile"

type linkCall struct {
	userID    int64
	startedAt time.Time
	in        platformstore.ProviderSignIn
}

func (f *fakeFlow) ConsumeLinkTicket(_ context.Context, ticket string) (int64, tenant.Team, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.linkErr != nil {
		return 0, tenant.Team{}, f.linkErr
	}
	if ticket != f.linkTicket || f.ticketsUse > 0 {
		return 0, tenant.Team{}, platformstore.ErrTokenInvalid
	}
	f.ticketsUse++
	return f.linkUID, f.linkTeam, nil
}

func (f *fakeFlow) CompleteGoogleLink(_ context.Context, userID int64, startedAt time.Time, in platformstore.ProviderSignIn) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.links = append(f.links, linkCall{userID: userID, startedAt: startedAt, in: in})
	return f.linkKey
}

func wantProfile(t *testing.T, rec interface {
	Result() *http.Response
}, profile, key string) {
	t.Helper()
	res := rec.Result()
	if res.StatusCode != http.StatusSeeOther {
		t.Fatalf("status %d, want 303 to the profile", res.StatusCode)
	}
	if got := res.Header.Get("Location"); got != profile+"?google="+key {
		t.Fatalf("Location %q, want %s?google=%s", got, profile, key)
	}
}

// A Google connect from a team host: the ticket is spent at the start, the state row carries the
// account and the return host, the callback connects and lands on that team's profile.
func TestLinkFlow(t *testing.T) {
	r := newRig(t, true, true)
	rec, cookie := r.start(t, "google", "target=link&ticket=link-ticket-value", acmeOrigin)
	if rec.Code != http.StatusSeeOther || cookie == nil {
		t.Fatalf("start = %d (cookie %v), want 303 with the binder", rec.Code, cookie != nil)
	}
	row := r.flow.lastCreated(t)
	if row.Target != platformstore.OAuthTargetLink || row.LinkUserID != 42 || row.ReturnSlug != "acme" || row.Provider != "google" {
		t.Fatalf("state row %+v", row)
	}
	code, state := r.authorize(t, rec.Header().Get("Location"))
	cb := r.serve(googleCallback(code, state, cookie))
	wantProfile(t, cb, acmeProfile, "linked")
	if len(r.flow.links) != 1 || len(r.flow.signIns) != 0 || len(r.flow.accepts) != 0 {
		t.Fatalf("completions: %d links, %d sign-ins, %d invites", len(r.flow.links), len(r.flow.signIns), len(r.flow.accepts))
	}
	l := r.flow.links[0]
	if l.userID != 42 || l.in.Subject != "subject-1" || l.in.Provider != "google" ||
		!l.startedAt.Equal(fakeExpires.Add(-platformstore.OAuthStateTTL)) {
		t.Fatalf("link completion %+v", l)
	}
	if raw := setCookieHeader(cb); !strings.Contains(raw, CookieName+"=;") {
		t.Errorf("the cookie was not cleared: %q", raw)
	}
	// The ticket is single use.
	again := r.serve(formReq(http.MethodPost, "/google/start", "target=link&ticket=link-ticket-value", acmeOrigin))
	wantResult(t, again, "error=expired")
	// Replay of the callback: the row is spent; the target is unknown, so the result page answers.
	wantResult(t, r.serve(googleCallback(code, state, cookie)), "error=expired")
	if len(r.flow.links) != 1 {
		t.Fatal("a replayed callback connected twice")
	}
}

// Every completion key reaches the profile; one outside the set is try_again.
func TestLinkCompletionKeys(t *testing.T) {
	r := newRig(t, true, false)
	for _, key := range []string{"linked", "taken", "already_linked", "unverified", "expired", "try_again", "bogus"} {
		r.flow.ticketsUse, r.flow.linkKey = 0, key
		rec, cookie := r.start(t, "google", "target=link&ticket=link-ticket-value", acmeOrigin)
		code, state := r.authorize(t, rec.Header().Get("Location"))
		want := key
		if key == "bogus" {
			want = "try_again"
		}
		wantProfile(t, r.serve(googleCallback(code, state, cookie)), acmeProfile, want)
	}
}

// A root token's ticket returns to the root profile and is started from the root host only; a team
// ticket only from its own team host.
func TestLinkStartOrigin(t *testing.T) {
	cases := []struct {
		name   string
		team   tenant.Team
		origin string
		want   int
	}{
		{"team ticket from its host", tenant.Team{ID: 7, Slug: "acme"}, acmeOrigin, http.StatusSeeOther},
		{"team ticket from root", tenant.Team{ID: 7, Slug: "acme"}, rootOrigin, http.StatusForbidden},
		{"team ticket from another team", tenant.Team{ID: 7, Slug: "acme"}, "https://other.beta.lekalo.studio", http.StatusForbidden},
		{"team ticket from elsewhere", tenant.Team{ID: 7, Slug: "acme"}, "https://evil.example", http.StatusForbidden},
		{"root ticket from root", tenant.Team{}, rootOrigin, http.StatusSeeOther},
		{"root ticket from a team host", tenant.Team{}, acmeOrigin, http.StatusForbidden},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := newRig(t, true, true)
			r.flow.linkTeam = c.team
			rec := r.serve(formReq(http.MethodPost, "/google/start", "target=link&ticket=link-ticket-value", c.origin))
			if rec.Code != c.want {
				t.Fatalf("start = %d, want %d", rec.Code, c.want)
			}
			if c.want != http.StatusSeeOther && len(r.flow.created) != 0 {
				t.Fatal("a refused origin wrote a state row")
			}
		})
	}
	r := newRig(t, true, false)
	r.flow.linkTeam = tenant.Team{}
	rec, cookie := r.start(t, "google", "target=link&ticket=link-ticket-value", rootOrigin)
	code, state := r.authorize(t, rec.Header().Get("Location"))
	wantProfile(t, r.serve(googleCallback(code, state, cookie)), "https://beta.lekalo.studio/profile", "linked")
}

// Client mistakes answer 4xx without touching the ticket; a dead ticket lands on the result page.
func TestLinkStartRefusals(t *testing.T) {
	r := newRig(t, true, true)
	for _, c := range []struct {
		name, provider, body string
		want                 int
	}{
		{"no ticket", "google", "target=link", http.StatusBadRequest},
		{"long ticket", "google", "target=link&ticket=" + strings.Repeat("t", maxTokenLen+1), http.StatusBadRequest},
		{"apple", "apple", "target=link&ticket=link-ticket-value", http.StatusBadRequest},
	} {
		if rec := r.serve(formReq(http.MethodPost, "/"+c.provider+"/start", c.body, acmeOrigin)); rec.Code != c.want {
			t.Errorf("%s: start = %d, want %d", c.name, rec.Code, c.want)
		}
	}
	if r.flow.ticketsUse != 0 || len(r.flow.created) != 0 {
		t.Fatal("a refused start spent the ticket or wrote a row")
	}
	wantResult(t, r.serve(formReq(http.MethodPost, "/google/start", "target=link&ticket=other", acmeOrigin)), "error=expired")
	r.flow.linkErr = errors.New("store down")
	wantResult(t, r.serve(formReq(http.MethodPost, "/google/start", "target=link&ticket=link-ticket-value", acmeOrigin)), "error=try_again")
}

// Once the row is known to be a connect, every refusal returns to its profile with the key.
func TestLinkCallbackRefusalsReturnToProfile(t *testing.T) {
	r := newRig(t, true, false)
	start := func() (string, string, *http.Cookie) {
		r.flow.ticketsUse = 0
		rec, cookie := r.start(t, "google", "target=link&ticket=link-ticket-value", acmeOrigin)
		code, state := r.authorize(t, rec.Header().Get("Location"))
		return code, state, cookie
	}
	// No binder cookie.
	code, state, _ := start()
	wantProfile(t, r.serve(googleCallback(code, state, nil)), acmeProfile, "expired")
	// The person said no at Google.
	_, state, cookie := start()
	q := url.Values{"state": {state}, "error": {"access_denied"}}
	req := googleCallback("", "", cookie)
	req.URL.RawQuery = q.Encode()
	wantProfile(t, r.serve(req), acmeProfile, "cancelled")
	// The token endpoint fails.
	code, state, cookie = start()
	r.idp.SetTokenResponse(http.StatusBadGateway, []byte(`oops`))
	wantProfile(t, r.serve(googleCallback(code, state, cookie)), acmeProfile, "try_again")
	r.idp.ResetTokenResponse()
	if len(r.flow.links) != 0 {
		t.Fatal("a refused callback reached the completion")
	}
}

func TestProfileURL(t *testing.T) {
	h := New(Deps{BaseDomain: "Beta.Lekalo.Studio."})
	defer h.Close()
	for slug, want := range map[string]string{
		"acme":        "https://acme.beta.lekalo.studio/profile",
		"a-1":         "https://a-1.beta.lekalo.studio/profile",
		"":            "https://beta.lekalo.studio/profile",
		"evil.com/x":  "https://beta.lekalo.studio/profile",
		"-bad":        "https://beta.lekalo.studio/profile",
		"UPPER":       "https://beta.lekalo.studio/profile",
		"a@b":         "https://beta.lekalo.studio/profile",
		"x.evil.test": "https://beta.lekalo.studio/profile",
	} {
		if got := h.profileURL(slug); got != want {
			t.Errorf("profileURL(%q) = %q, want %q", slug, got, want)
		}
	}
}
