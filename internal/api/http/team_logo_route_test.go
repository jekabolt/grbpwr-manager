package httpapi

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/jekabolt/grbpwr-manager/internal/apisrv/auth"
	pb_auth "github.com/jekabolt/grbpwr-manager/proto/gen/auth"
	"google.golang.org/protobuf/encoding/protojson"
)

// The public team logo is a plain route next to the AuthService gateway mount: chi must hand
// GET and HEAD /api/auth/team-logo/{slug} to it, not to the mount's catch-all.
func TestTeamLogoRouteBeatsAuthMount(t *testing.T) {
	s := New(&Config{Address: "127.0.0.1", Port: "8081"})
	hit := 0
	s.SetTeamLogoHandler(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hit++
		w.WriteHeader(http.StatusTeapot)
	}))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	h, err := s.setupHTTPAPI(ctx, &auth.Server{})
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range []string{http.MethodGet, http.MethodHead} {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(m, "/api/auth/team-logo/acme?v=0123456789ab", nil))
		if rec.Code != http.StatusTeapot {
			t.Errorf("%s /api/auth/team-logo/acme = %d, want the logo handler", m, rec.Code)
		}
	}
	if hit != 2 {
		t.Errorf("logo handler hit %d times, want 2", hit)
	}
}

// The three requests that carry a logo (SignUp, CreateTeam, SetTeamLogo) ride the /api/auth mount:
// its body limit must take a 1 MiB logo as gateway JSON (base64, ~1.4 MB) with room for the other
// fields, and stay a limit (O1: "accept ~1.4 MB base64, cap ~2 MB" — the mount's 4 MB covers it).
func TestAuthBodyLimitTakesALogo(t *testing.T) {
	if maxJSONBodyBytes < 2<<20 || maxJSONBodyBytes > 8<<20 {
		t.Fatalf("maxJSONBodyBytes = %d, want between 2 MiB and 8 MiB", maxJSONBodyBytes)
	}
	logo := bytes.Repeat([]byte{0xA5}, 1<<20)
	body, err := protojson.Marshal(&pb_auth.CreateTeamRequest{TeamName: "acme", TeamSlug: "acme", Logo: logo, TermsAccepted: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(body) < 1_398_000 {
		t.Fatalf("gateway JSON of a 1 MiB logo is %d bytes, expected base64 size", len(body))
	}
	read := func(b []byte) error {
		var got error
		h := limitBody(maxJSONBodyBytes)(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
			raw, err := io.ReadAll(r.Body)
			if err != nil {
				got = err
				return
			}
			var req pb_auth.CreateTeamRequest
			got = protojson.Unmarshal(raw, &req)
			if got == nil && !bytes.Equal(req.GetLogo(), logo) {
				t.Error("logo bytes changed on the way through")
			}
		}))
		h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodPost, "/api/auth/v1/teams", bytes.NewReader(b)))
		return got
	}
	if err := read(body); err != nil {
		t.Fatalf("a 1 MiB logo did not pass the /api/auth body limit: %v", err)
	}
	huge := []byte(`{"teamName":"` + strings.Repeat("a", int(maxJSONBodyBytes)) + `"}`)
	if err := read(huge); err == nil {
		t.Fatal("a body above maxJSONBodyBytes passed")
	}
}
