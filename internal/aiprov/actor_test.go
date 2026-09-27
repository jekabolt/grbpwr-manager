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
