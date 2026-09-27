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

// TestEngagedOpenRouterError — the openrouter client SPEAKS CallError since B-11 (its chat calls go
// through oaichat): a 2xx whose envelope carries no choices (accepted, therefore billed) comes back as
// a *CallError whose Engaged FIELD says so, and the old helper openrouter.ProviderEngaged agrees because
// it is aiprov.Engaged now; its gate refusal (401) is a CallError that is NOT engaged. Lives in an
// EXTERNAL test package on purpose: openrouter imports aiprov, so an internal test that imported it
// would be an import cycle.
//
// MUTATION: oaichat marks the "no choices" branch not engaged → red. MUTATION: openrouter.send wraps
// the transport's error in a plain fmt.Errorf("%v") (the chain lost) → AsCallError red. MUTATION:
// openrouter.ProviderEngaged returns false → the "old helper" line red.
func TestEngagedOpenRouterError(t *testing.T) {
	ok2xx := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[]}`))
	}))
	defer ok2xx.Close()
	c := openrouter.New(openrouter.Config{APIKey: "k", BaseURL: ok2xx.URL, HTTPTimeout: 2 * time.Second})
	_, _, _, err := c.CompleteWithImages(context.Background(), "sys", "user", nil, false, 0)
	require.Error(t, err)
	ce, isCallErr := aiprov.AsCallError(err)
	require.True(t, isCallErr, "the openrouter client speaks CallError — that is the point of B-11")
	require.True(t, ce.Engaged, "a 2xx was accepted and billed: the transport's own field says so")
	require.False(t, ce.Retryable)
	require.Equal(t, "openrouter", ce.Provider)
	require.Equal(t, aiprov.CodeEmptyAnswer, ce.Code)
	require.Equal(t, http.StatusOK, ce.HTTPStatus)
	require.True(t, aiprov.Engaged(err))
	require.True(t, aiprov.Engaged(fmt.Errorf("draft: %w", err)))
	require.True(t, openrouter.ProviderEngaged(err), "the old helper still answers — it is aiprov.Engaged")
	require.Equal(t, "openrouter: API response contained no choices", err.Error(),
		"CallError.Error() is the transport's sentence, verbatim")

	refused := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":{"message":"no auth"}}`))
	}))
	defer refused.Close()
	c = openrouter.New(openrouter.Config{APIKey: "k", BaseURL: refused.URL, HTTPTimeout: 2 * time.Second})
	_, _, _, err = c.CompleteWithImages(context.Background(), "sys", "user", nil, false, 0)
	require.Error(t, err)
	ce, isCallErr = aiprov.AsCallError(err)
	require.True(t, isCallErr)
	require.False(t, ce.Engaged, "a 401 is a refusal at the gate, not a charge")
	require.False(t, aiprov.Engaged(err))
	require.Equal(t, aiprov.CodeKeyRejected, ce.Code)
	require.Equal(t, http.StatusUnauthorized, ce.HTTPStatus)
	require.Equal(t, "openrouter: API error (HTTP 401): no auth", err.Error())
}
