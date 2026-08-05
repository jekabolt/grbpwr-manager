package storeutil

import (
	"database/sql"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestResolvePatternName locks the presence-gated write semantics of a pattern's display
// name across a full-replace save: absent inherits the replaced row's name, present wins,
// present-empty clears.
func TestResolvePatternName(t *testing.T) {
	stored := sql.NullString{String: "перед", Valid: true}
	sent := sql.NullString{String: "спинка", Valid: true}
	empty := sql.NullString{String: "", Valid: true}
	absent := sql.NullString{}

	// A stale client omits the field — the stored name survives the full-replace.
	require.Equal(t, stored, ResolvePatternName(absent, stored))
	// Absent on a brand-new row — there is nothing to inherit.
	require.Equal(t, absent, ResolvePatternName(absent, absent))
	// A present name overwrites whatever was stored.
	require.Equal(t, sent, ResolvePatternName(sent, stored))
	require.Equal(t, sent, ResolvePatternName(sent, absent))
	// Present-empty is an explicit clear, normalised to NULL.
	require.Equal(t, absent, ResolvePatternName(empty, stored))
	require.Equal(t, absent, ResolvePatternName(empty, absent))
}
