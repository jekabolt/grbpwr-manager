package entity

import (
	"database/sql"
	"testing"

	"github.com/stretchr/testify/require"
)

// T14: an edit of a multi-view sheet is the same sheet and must keep its composite_views
// (and its ghost_view), or SPLIT disappears from the edit that heads the bench chain.
func TestDesignFlattenInheritedViews(t *testing.T) {
	t.Run("composite sheet keeps its views", func(t *testing.T) {
		parent := &DesignPicture{CompositeViews: RawJSON(`["front","back","side_left"]`)}
		ghost, composite := DesignFlattenInheritedViews(parent)
		require.False(t, ghost.Valid)
		require.JSONEq(t, `["front","back","side_left"]`, string(composite))

		// A copy, not an alias of the parent's buffer.
		parent.CompositeViews[2] = 'X'
		require.JSONEq(t, `["front","back","side_left"]`, string(composite))
	})

	t.Run("single view keeps its ghost view", func(t *testing.T) {
		ghost, composite := DesignFlattenInheritedViews(&DesignPicture{
			GhostView: sql.NullString{String: "back", Valid: true},
		})
		require.Equal(t, sql.NullString{String: "back", Valid: true}, ghost)
		require.Nil(t, composite)
	})

	t.Run("empty composite forms inherit nothing", func(t *testing.T) {
		for _, raw := range []string{"", "null", "[]"} {
			_, composite := DesignFlattenInheritedViews(&DesignPicture{CompositeViews: RawJSON(raw)})
			require.Nil(t, composite, "raw %q", raw)
		}
	})

	t.Run("no base inherits nothing", func(t *testing.T) {
		ghost, composite := DesignFlattenInheritedViews(nil)
		require.False(t, ghost.Valid)
		require.Nil(t, composite)
	})
}
