package meshy

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
)

// TestKeyFunc_SwapChangesTheNextRequestsAuthorization — the Meshy client reads its key through
// Config.KeyFunc per request, and Enabled() follows it.
//
// MUTATION: apiKey() returns cfg.APIKey even when KeyFunc is set → red.
func TestKeyFunc_SwapChangesTheNextRequestsAuthorization(t *testing.T) {
	f := newFake(t, StatusSucceeded)
	var key atomic.Value
	key.Store("key-one")
	c := f.client(func(cfg *Config) {
		cfg.APIKey = "env-key"
		cfg.KeyFunc = func() string { return key.Load().(string) }
	})

	authOfSubmit := func() string {
		f.mu.Lock()
		defer f.mu.Unlock()
		return f.authSeen[multiImagePath]
	}
	if _, err := c.Submit(context.Background(), sampleRequest()); err != nil {
		t.Fatalf("first submit: %v", err)
	}
	if got := authOfSubmit(); got != "Bearer key-one" {
		t.Fatalf("first submit Authorization = %q, want %q", got, "Bearer key-one")
	}
	key.Store("key-two")
	if _, err := c.Submit(context.Background(), sampleRequest()); err != nil {
		t.Fatalf("second submit: %v", err)
	}
	if got := authOfSubmit(); got != "Bearer key-two" {
		t.Fatalf("second submit Authorization = %q, want %q", got, "Bearer key-two")
	}

	key.Store("")
	if c.Enabled() {
		t.Fatal("Enabled() = true while the func answers \"\"")
	}
	if _, err := c.Submit(context.Background(), sampleRequest()); !errors.Is(err, ErrNotConfigured) {
		t.Fatalf("submit with no key = %v, want ErrNotConfigured", err)
	}
	if n := len(f.snapshotCalls()); n != 2 {
		t.Fatalf("a disabled client reached the wire: %d calls", n)
	}
}
