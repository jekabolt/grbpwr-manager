package content

import (
	"strings"
	"testing"

	"github.com/jekabolt/grbpwr-manager/internal/store/storeutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestMediaUsageQueryShape guards the two ways this query can break without any test touching a
// database.
//
// First, the colon trap: sqlx's named-parameter scanner does not skip string literals or comments,
// so any ':' in the SQL text that is not a bind name binds as an empty name and the whole query
// fails at runtime with "could not find name  in map". That has silently shipped broken reads in
// this repo before (tech-card reads, JPK evidence, GetReceivables), and here it would take out the
// entire media library page rather than one row.
//
// Second, the branch count: every registry entry must contribute exactly one UNION branch with its
// own IN (...) predicate. A branch that loses its predicate would scan a whole table per lookup.
func TestMediaUsageQueryShape(t *testing.T) {
	q := mediaUsageQuery
	t.Log("\n" + q)

	// The only legal colon is the ':ids' bind, once per registry entry.
	assert.Equal(t, len(mediaRefRegistry), strings.Count(q, ":ids"),
		"every registry entry must bind the id list exactly once")
	assert.Equal(t, len(mediaRefRegistry), strings.Count(q, ":"),
		"no ':' may appear outside the ':ids' bind — sqlx would read it as an empty bind name")

	assert.Equal(t, len(mediaRefRegistry)-1, strings.Count(q, "UNION ALL"),
		"one UNION ALL between each pair of branches")
	assert.Equal(t, len(mediaRefRegistry), strings.Count(q, " IN (:ids)"),
		"each branch must filter on its own media column, never scan the table")

	// No SQL comments at all: '--' and '#' both survive into the scanner's input.
	assert.NotContains(t, q, "--", "SQL comments do not belong in a named query")
	assert.NotContains(t, q, "#", "'#' opens a MySQL comment")

	// The bind actually expands: sqlx.Named must resolve every occurrence, and sqlx.In must turn
	// each into one placeholder per id. This is the step that fails loudest when a colon slips in.
	expanded, args, err := storeutil.MakeQuery(q, map[string]any{"ids": []int{7, 8, 9}})
	require.NoError(t, err, "named-parameter expansion must succeed")
	assert.Len(t, args, 3*len(mediaRefRegistry))
	assert.Equal(t, 3*len(mediaRefRegistry), strings.Count(expanded, "?"))
}

// TestMediaRefRegistryTargetsAreDistinct keeps the registry from listing the same column twice,
// which would double every ref for that slot in the UI.
func TestMediaRefRegistryTargetsAreDistinct(t *testing.T) {
	seen := map[string]bool{}
	for _, target := range MediaRefRegistryTargets() {
		assert.False(t, seen[target], "%s listed twice", target)
		seen[target] = true
		assert.Contains(t, target, ".", "target must be table.column")
	}
	assert.Len(t, seen, len(mediaRefRegistry))
}

// TestMediaRefRegistryEntriesAreWellFormed catches a half-filled registry row at build time
// instead of as a SQL syntax error in production.
func TestMediaRefRegistryEntriesAreWellFormed(t *testing.T) {
	for _, src := range mediaRefRegistry {
		require.NotEmpty(t, src.kind)
		require.NotEmpty(t, src.table)
		require.NotEmpty(t, src.entityExpr)
		require.NotEmpty(t, src.labelExpr)
		assert.True(t, src.slot != "" || src.slotExpr != "",
			"%s must name the slot the media sits in", src.column)

		// The filter column must be alias-qualified and the alias must be one the FROM introduces,
		// otherwise the branch silently filters the wrong table.
		alias, _, found := strings.Cut(src.column, ".")
		require.True(t, found, "column %q must be alias-qualified", src.column)
		_, tableAlias, hasAlias := strings.Cut(src.table, " ")
		require.True(t, hasAlias, "table %q must declare an alias", src.table)
		assert.Equal(t, tableAlias, alias,
			"%s filters on alias %q but its own table is aliased %q", src.kind, alias, tableAlias)
	}
}
