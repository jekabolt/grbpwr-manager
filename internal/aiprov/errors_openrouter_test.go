package aiprov_test

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/jekabolt/grbpwr-manager/internal/aiprov"
	"github.com/jekabolt/grbpwr-manager/internal/openrouter"
)

// TestEngagedOpenRouterError — an error the existing chat client marks as engaged (a 2xx whose
// envelope carries no choices: accepted, therefore billed) is engaged for aiprov too, through the
// EngagedMarker interface; its gate refusal (401) is not. Lives in an EXTERNAL test package on
// purpose: aiprov must not import openrouter, and once openrouter imports aiprov (commit C) an
// internal test that imported it would be an import cycle.
//
// MUTATION: drop the EngagedMarker branch of aiprov.Engaged (return false after the CallError walk)
// → red. MUTATION: remove engagedError.ProviderEngaged from openrouter → red.
func TestEngagedOpenRouterError(t *testing.T) {
	ok2xx := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[]}`))
	}))
	defer ok2xx.Close()
	c := openrouter.New(openrouter.Config{APIKey: "k", BaseURL: ok2xx.URL, HTTPTimeout: 2 * time.Second})
	_, _, _, err := c.CompleteWithImages(context.Background(), "sys", "user", nil, false, 0)
	require.Error(t, err)
	require.True(t, openrouter.ProviderEngaged(err), "precondition: the old helper calls it engaged")
	require.True(t, aiprov.Engaged(err))
	require.True(t, aiprov.Engaged(fmt.Errorf("draft: %w", err)))
	_, isCallErr := aiprov.AsCallError(err)
	require.False(t, isCallErr, "the old client does not speak CallError yet — that is the point")

	refused := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":{"message":"no auth"}}`))
	}))
	defer refused.Close()
	c = openrouter.New(openrouter.Config{APIKey: "k", BaseURL: refused.URL, HTTPTimeout: 2 * time.Second})
	_, _, _, err = c.CompleteWithImages(context.Background(), "sys", "user", nil, false, 0)
	require.Error(t, err)
	require.False(t, aiprov.Engaged(err), "a 401 is a refusal at the gate, not a charge")
}
