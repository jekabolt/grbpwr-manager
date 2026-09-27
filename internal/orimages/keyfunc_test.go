package orimages

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
)

// TestKeyFunc_SwapChangesTheNextRequestsAuthorization — see the openrouter twin: the image client
// reads its key through Config.KeyFunc per request, and Enabled() follows it.
//
// MUTATION: apiKey() returns cfg.APIKey even when KeyFunc is set → red.
func TestKeyFunc_SwapChangesTheNextRequestsAuthorization(t *testing.T) {
	var (
		mu    sync.Mutex
		auths []string
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		auths = append(auths, r.Header.Get("Authorization"))
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, okResponse("image/png"))
	}))
	defer srv.Close()

	var key atomic.Value
	key.Store("key-one")
	c := New(Config{APIKey: "env-key", BaseURL: srv.URL, KeyFunc: func() string { return key.Load().(string) }})
	call := func() error {
		_, err := c.Generate(context.Background(), Request{Prompt: "technical flat, front view", N: 1})
		return err
	}
	if err := call(); err != nil {
		t.Fatalf("first call: %v", err)
	}
	key.Store("key-two")
	if err := call(); err != nil {
		t.Fatalf("second call: %v", err)
	}
	mu.Lock()
	got := append([]string(nil), auths...)
	mu.Unlock()
	if len(got) != 2 || got[0] != "Bearer key-one" || got[1] != "Bearer key-two" {
		t.Fatalf("Authorization per request = %q, want [Bearer key-one Bearer key-two]", got)
	}

	key.Store("")
	if c.Enabled() {
		t.Fatal("Enabled() = true while the func answers \"\"")
	}
	if err := call(); !errors.Is(err, ErrNotConfigured) {
		t.Fatalf("call with no key = %v, want ErrNotConfigured", err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(auths) != 2 {
		t.Fatalf("a disabled client reached the wire: %d requests", len(auths))
	}
}
