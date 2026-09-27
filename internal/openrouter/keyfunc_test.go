package openrouter

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

// TestKeyFunc_SwapChangesTheNextRequestsAuthorization — the AI providers registry swaps the key
// behind Config.KeyFunc; the NEXT request carries the new key, no client rebuild, and Enabled()
// follows the func (a provider switched off answers "" and the client refuses before the wire).
//
// MUTATION: apiKey() returns cfg.APIKey even when KeyFunc is set → red (the env key goes out).
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
		io.WriteString(w, `{"model":"stub-model","choices":[{"message":{"role":"assistant","content":"{\"ok\":true}"}}]}`)
	}))
	defer srv.Close()

	var key atomic.Value
	key.Store("key-one")
	c := New(Config{APIKey: "env-key", BaseURL: srv.URL, KeyFunc: func() string { return key.Load().(string) }})

	call := func() error {
		_, err := c.Complete(context.Background(), "sys", "assemble it", true)
		return err
	}
	if err := call(); err != nil {
		t.Fatalf("first call: %v", err)
	}
	key.Store("  key-two  ")
	if err := call(); err != nil {
		t.Fatalf("second call: %v", err)
	}
	mu.Lock()
	got := append([]string(nil), auths...)
	mu.Unlock()
	if len(got) != 2 || got[0] != "Bearer key-one" || got[1] != "Bearer key-two" {
		t.Fatalf("Authorization per request = %q, want [Bearer key-one Bearer key-two]", got)
	}

	if !c.Enabled() {
		t.Fatal("Enabled() = false with a key behind the func")
	}
	key.Store("")
	if c.Enabled() {
		t.Fatal("Enabled() = true while the func answers \"\" — enabled=0 must win over the env key")
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
