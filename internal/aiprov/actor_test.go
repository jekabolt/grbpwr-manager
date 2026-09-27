package aiprov

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestActorRoundTrip — what the interceptor puts in is what the ledger reads out, id included.
//
// MUTATION: ActorFrom returns Actor{} instead of the stored value → red.
func TestActorRoundTrip(t *testing.T) {
	id := 42
	ctx := WithActor(context.Background(), Actor{Username: "im", AdminID: &id})

	got := ActorFrom(ctx)
	require.Equal(t, "im", got.Username)
	require.NotNil(t, got.AdminID)
	require.Equal(t, 42, *got.AdminID)

	// The id is copied on the way in: a caller reusing its variable cannot rewrite the actor of a
	// call already in flight.
	id = 7
	require.Equal(t, 42, *ActorFrom(ctx).AdminID)

	// And on the way out: a reader scribbling on its copy does not change the next reader's.
	*got.AdminID = 99
	require.Equal(t, 42, *ActorFrom(ctx).AdminID)

	sys := ActorFrom(WithActor(context.Background(), Actor{Username: ActorSystem}))
	require.Equal(t, ActorSystem, sys.Username)
	require.Nil(t, sys.AdminID)
}

// TestActorMissingIsUnknown — a path that forgot WithActor shows up in the report as "unknown",
// never as an empty name that reads like a filter.
//
// MUTATION: the missing-actor branch returns Actor{} → red (Username "" ≠ "unknown").
func TestActorMissingIsUnknown(t *testing.T) {
	require.Equal(t, Actor{Username: ActorUnknown}, ActorFrom(context.Background()))
	//nolint:staticcheck // a nil context is exactly the case under test
	require.Equal(t, Actor{Username: ActorUnknown}, ActorFrom(nil))

	id := 3
	blank := ActorFrom(WithActor(context.Background(), Actor{Username: "  ", AdminID: &id}))
	require.Equal(t, ActorUnknown, blank.Username)
	require.NotNil(t, blank.AdminID, "a blank name does not throw away a known id")
	require.Equal(t, 3, *blank.AdminID)
}

// TestRunRoundTrip — the pair WithRun puts in is the pair the ledger row is keyed by, as fresh
// pointers; a context without one (or with a pair that is not a run) books NULLs.
//
// MUTATION: RunFrom returns (nil, nil) always → red on the first pair.
// MUTATION: RunFrom drops the `< 1` guard → red on the (0, 1) context (a run_id the store refuses).
func TestRunRoundTrip(t *testing.T) {
	ctx := WithRun(context.Background(), 7, 2)
	id, no := RunFrom(ctx)
	require.NotNil(t, id)
	require.NotNil(t, no)
	require.Equal(t, 7, *id)
	require.Equal(t, 2, *no)

	// Fresh on every read: scribbling on one reader's copy changes no other row.
	*id, *no = 99, 99
	id2, no2 := RunFrom(ctx)
	require.Equal(t, 7, *id2)
	require.Equal(t, 2, *no2)

	// The actor and the run ride side by side.
	both := WithActor(ctx, Actor{Username: "im"})
	id3, _ := RunFrom(both)
	require.Equal(t, 7, *id3)
	require.Equal(t, "im", ActorFrom(both).Username)

	for name, c := range map[string]context.Context{
		"absent":     context.Background(),
		"zero run":   WithRun(context.Background(), 0, 1),
		"zero try":   WithRun(context.Background(), 7, 0),
		"negative":   WithRun(context.Background(), -1, -1),
		"nil parent": WithRun(nil, 0, 0), //nolint:staticcheck // a nil parent is part of the case
	} {
		id, no := RunFrom(c)
		require.Nil(t, id, name)
		require.Nil(t, no, name)
	}
	//nolint:staticcheck // a nil context is exactly the case under test
	id4, no4 := RunFrom(nil)
	require.Nil(t, id4)
	require.Nil(t, no4)
}
