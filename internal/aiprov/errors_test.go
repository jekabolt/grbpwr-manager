package aiprov

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"
)

var errTransport = errors.New("upstream said no")

// TestCallErrorUnwrapAndAs — the wrapper keeps the transport's sentinel reachable and is itself
// findable through any number of %w layers.
//
// MUTATION: Unwrap returns nil → the errors.Is assertions go red; AsCallError drops its errors.As
// and type-asserts the top error only → the wrapped lookup goes red.
func TestCallErrorUnwrapAndAs(t *testing.T) {
	ce := &CallError{Provider: "openai", Code: "key_rejected", HTTPStatus: 401, Err: errTransport}

	require.ErrorIs(t, ce, errTransport)
	require.Same(t, errTransport, ce.Unwrap())

	wrapped := fmt.Errorf("draft idea: %w", ce)
	require.ErrorIs(t, wrapped, errTransport, "the sentinel survives two layers")

	got, ok := AsCallError(wrapped)
	require.True(t, ok)
	require.Same(t, ce, got)
	require.Equal(t, 401, got.HTTPStatus)

	_, ok = AsCallError(errTransport)
	require.False(t, ok)
	_, ok = AsCallError(nil)
	require.False(t, ok)
	var typedNil *CallError
	_, ok = AsCallError(fmt.Errorf("x: %w", typedNil))
	require.False(t, ok, "a typed-nil CallError is not an answer")
	require.Nil(t, typedNil.Unwrap(), "Unwrap is nil-safe")

	// The text keeps the transport's sentence verbatim and names provider, code and status.
	require.Equal(t, "openai [key_rejected, HTTP 401]: upstream said no", ce.Error())
	require.Equal(t, "ai: call failed", (&CallError{}).Error())
}

// TestEngagedCallError — the transport's flag is the answer, through wrapping and joining.
//
// MUTATION: Engaged returns openrouter.ProviderEngaged(err) only (ignores CallError) → red.
// MUTATION: anyEngagedCallError stops at the first CallError (errors.As semantics) → the
// "outer not engaged, inner engaged" case goes red.
func TestEngagedCallError(t *testing.T) {
	require.False(t, Engaged(nil))
	require.False(t, Engaged(errTransport))

	engaged := &CallError{Provider: "anthropic", Engaged: true, Err: context.DeadlineExceeded}
	require.True(t, Engaged(engaged))
	require.True(t, Engaged(fmt.Errorf("chat: %w", engaged)))

	gate := &CallError{Provider: "openai", HTTPStatus: 404, Code: "model_unknown", Err: errTransport}
	require.False(t, Engaged(gate), "a refusal at the gate moved no money")

	// A router wrapping a candidate's engaged failure in its own non-engaged error, or joining the
	// errors of every candidate, must still read "engaged".
	outer := &CallError{Provider: "router", Err: engaged}
	require.True(t, Engaged(outer))
	joined := errors.Join(ErrAllCandidatesFailed, gate, fmt.Errorf("second: %w", engaged))
	require.True(t, Engaged(joined))
	require.False(t, Engaged(errors.Join(ErrAllCandidatesFailed, gate)))
}
