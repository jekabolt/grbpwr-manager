package content

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

// TestMediaUsedIDsUnionShape pins the used-id set of the library's USED/FREE filter: one branch per
// registry row, no binds and nothing sqlx or the driver would misread.
func TestMediaUsedIDsUnionShape(t *testing.T) {
	q := mediaUsedIDsUnion
	assert.Equal(t, len(mediaRefRegistry)-1, strings.Count(q, "UNION ALL"), "one branch per registry row")
	assert.NotContains(t, q, ":", "the used set takes no binds; a ':' would be read as one")
	assert.NotContains(t, q, "--")
	assert.NotContains(t, q, "#")
	assert.NotContains(t, q, "?")
	for _, src := range mediaRefRegistry {
		assert.Contains(t, q, src.usedIDsSQL())
	}

	join := "LEFT JOIN (SELECT DISTINCT x.id FROM (" + q + ") x) " + MediaUsedJoinAlias + " ON " + MediaUsedJoinAlias + ".id = m.id"
	assert.NotContains(t, join, ":")
}

// TestMediaJSONUsedHolderQueryShape: the unfiltered twin of mediaJSONHolderQuery reads every holder,
// with no id prefilter and no binds, and leaves the prefiltered query as it was.
func TestMediaJSONUsedHolderQueryShape(t *testing.T) {
	q := mediaJSONUsedHolderQuery
	assert.Equal(t, mediaJSONHolderBranches-1, strings.Count(q, "UNION ALL"))
	assert.NotContains(t, q, ":")
	assert.NotContains(t, q, "JSON_OVERLAPS(")
	assert.Equal(t, mediaJSONHolderBranches, strings.Count(q, "IS NOT NULL"),
		"every branch drops the documents that name no media key at all")
	assert.NotContains(t, q, "--")
	assert.NotContains(t, q, "#")
	assert.NotContains(t, q, "?")
	assert.Equal(t, buildMediaJSONHolderUnion(jsonHolderBranch), mediaJSONHolderQuery,
		"the shared holder list must render the prefiltered query unchanged")
}
