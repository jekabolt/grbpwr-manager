package revalidation

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// A revalidation post carries a secret: the client never follows a redirect.
func TestClientNeverFollowsRedirects(t *testing.T) {
	hit := false
	elsewhere := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { hit = true }))
	defer elsewhere.Close()
	shopSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, elsewhere.URL, http.StatusTemporaryRedirect)
	}))
	defer shopSrv.Close()
	v, err := New(context.Background(), &Config{})
	if err != nil {
		t.Fatal(err)
	}
	resp, err := v.client.Post(shopSrv.URL, "application/json", strings.NewReader(`{"secret":"s"}`))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if hit || resp.StatusCode != http.StatusTemporaryRedirect {
		t.Fatalf("redirect followed: hit=%v status=%d", hit, resp.StatusCode)
	}
}
