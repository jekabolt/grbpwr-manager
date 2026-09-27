package fal

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// TestKeyFunc_SwapChangesTheNextRequestsAuthorization — the fal client reads its key through
// Config.KeyFunc per request (fal's own `Key` scheme kept), and Enabled() follows it: a provider
// switched off in the registry refuses at the client with ErrNotConfigured, before any submit.
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
		_ = json.NewEncoder(w).Encode(map[string]any{
			"request_id": "req-42",
			"status_url": srvStatusURL(r.Host, "hitem3d/hi3d", "req-42"),
		})
	}))
	defer srv.Close()

	var key atomic.Value
	key.Store("key-one")
	c := New(Config{
		APIKey:       "env-key",
		BaseURL:      srv.URL,
		HTTPTimeout:  2 * time.Second,
		PollInterval: 5 * time.Millisecond,
		PollTimeout:  2 * time.Second,
		KeyFunc:      func() string { return key.Load().(string) },
	})
	req := Request3D{
		Model:    hitem3dModel,
		FrontURL: "https://cdn.example/front.png",
		BackURL:  "https://cdn.example/back.png",
		LeftURL:  "https://cdn.example/left.png",
		RightURL: "https://cdn.example/right.png",
	}
	_, err := c.Submit(context.Background(), req)
	require.NoError(t, err)
	key.Store("key-two")
	_, err = c.Submit(context.Background(), req)
	require.NoError(t, err)

	mu.Lock()
	require.Equal(t, []string{"Key key-one", "Key key-two"}, auths)
	mu.Unlock()

	require.True(t, c.Enabled())
	key.Store("")
	require.False(t, c.Enabled(), "Enabled() follows the func")
	_, err = c.Submit(context.Background(), req)
	require.ErrorIs(t, err, ErrNotConfigured)
	mu.Lock()
	defer mu.Unlock()
	require.Len(t, auths, 2, "a disabled client must not submit — every fal POST is a payment")
}
