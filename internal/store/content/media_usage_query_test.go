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

// TestMediaRefRegistryCoversTheDesignWave pins BOTH halves of the DESIGN band's registration: the
// four columns that must be in the registry, and the two that must stay out of it.
//
// The four are the wave's owning references — each an ON DELETE RESTRICT into media(id) (0340,
// 0342, 0343). A missing one is not a cosmetic gap: GetMediaUsage is also what
// DeleteMediaByIdIfUnused asks, so an unregistered holder makes the library call a file free,
// offer the delete, and then meet a foreign key that refuses it with nothing to show for it.
//
// The two exclusions are decisions with consequences of their own. design_run.inputs is a JSON
// snapshot of what a model was shown; registering it would both freeze every moodboard picture
// forever and put a JSON scan of the whole run history into a query that is deliberately a
// relational UNION ALL. design_reference.media_id records a hint, carries no foreign key at all
// (0347), and registering it would turn an optional role into a lock on the delete.
//
// This test does not need a database. The live schema is diffed against the registry by
// TestMediaUsageRegistryCoversSchema, which does — this one states what that diff is expected to
// agree with, so a wrong registration is caught here rather than in a container.
func TestMediaRefRegistryCoversTheDesignWave(t *testing.T) {
	targets := map[string]bool{}
	for _, target := range MediaRefRegistryTargets() {
		targets[target] = true
	}

	for _, owning := range []string{
		"design_picture.media_id",
		"design_edit_layer.base_media_id",
		// A shelf row of the card (0354). Its media IS the asset — the texture, the pattern tile,
		// the hardware photograph — and the column is ON DELETE RESTRICT, so an unregistered entry
		// would let the library call the file free and hand the operator a raw foreign-key error.
		"design_asset.media_id",
	} {
		assert.True(t, targets[owning],
			"%s holds its media with ON DELETE RESTRICT; unregistered, the library reports the file as free", owning)
	}

	for _, excluded := range []string{"design_reference.media_id", "design_run.inputs"} {
		assert.False(t, targets[excluded],
			"%s is deliberately not a holder — registering it would make a hint refuse a delete", excluded)
	}

	// The kinds the client turns into routes. A registry entry that emits an unknown kind reaches
	// the operator as an unlabelled row.
	kinds := map[string]bool{}
	for _, src := range mediaRefRegistry {
		kinds[src.kind] = true
	}
	for _, kind := range []string{"design_picture", "design_edit_layer", "design_asset"} {
		assert.True(t, kinds[kind], "the DESIGN band must reach the client as kind %q", kind)
	}

	// The property this query is worth protecting for: no JSON scan, and no branch over the tables
	// whose columns were deliberately left out.
	assert.NotContains(t, mediaUsageQuery, "->>", "the usage query must stay purely relational")
	assert.NotContains(t, mediaUsageQuery, "design_run", "a run snapshot is provenance, not ownership")
	assert.NotContains(t, mediaUsageQuery, "design_reference", "a reference role is a hint, not ownership")
}
