package recraft

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
)

// TestKeyFunc_SwapChangesTheNextRequestsAuthorization — the direct Recraft transport reads its key
// through DirectConfig.KeyFunc per request, and Enabled() — the transport's and the service's —
// follows it.
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
		fmt.Fprintf(w, `{"data":[{"b64_json":%q,"image_id":"abc"}],"credits":80}`,
			base64.StdEncoding.EncodeToString([]byte(sampleSVG)))
	}))
	defer srv.Close()

	var key atomic.Value
	key.Store("key-one")
	svc := New(Config{
		Route:  "direct",
		Direct: DirectConfig{APIKey: "env-key", BaseURL: srv.URL, KeyFunc: func() string { return key.Load().(string) }},
	}, nil)

	if _, err := svc.ImageToImage(context.Background(), redrawRequest()); err != nil {
		t.Fatalf("first call: %v", err)
	}
	key.Store("key-two")
	if _, err := svc.ImageToImage(context.Background(), redrawRequest()); err != nil {
		t.Fatalf("second call: %v", err)
	}
	mu.Lock()
	got := append([]string(nil), auths...)
	mu.Unlock()
	if len(got) != 2 || got[0] != "Bearer key-one" || got[1] != "Bearer key-two" {
		t.Fatalf("Authorization per request = %q, want [Bearer key-one Bearer key-two]", got)
	}

	if !svc.Enabled() {
		t.Fatal("Enabled() = false with a key behind the func")
	}
	key.Store("")
	if svc.Enabled() {
		t.Fatal("Enabled() = true while the func answers \"\"")
	}
	if _, err := svc.ImageToImage(context.Background(), redrawRequest()); !errors.Is(err, ErrNotConfigured) {
		t.Fatalf("call with no key = %v, want ErrNotConfigured", err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(auths) != 2 {
		t.Fatalf("a disabled transport reached the wire: %d requests", len(auths))
	}
}
