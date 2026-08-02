package techcard

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/stretchr/testify/require"
)

// The purpose guard has independent arms and a card normally trips exactly one. Reporting all of
// them ("runs, products, or assembly component") made an operator whose card has zero runs read a
// correct refusal as a bug, so the message must name the arm that actually fired — and only that one.
func TestPurposeLockReason(t *testing.T) {
	t.Run("nothing references the card", func(t *testing.T) {
		require.Empty(t, purposeLockReason(0, 0, 0, 0))
	})

	t.Run("only the arm that fired is named", func(t *testing.T) {
		live := purposeLockReason(0, 2, 0, 0)
		require.Contains(t, live, "2 live colourways")
		require.Contains(t, live, "archive them first")
		require.NotContains(t, live, "production run")
		require.NotContains(t, live, "assembly")

		assemblies := purposeLockReason(0, 0, 0, 1)
		require.Contains(t, assemblies, "1 style assembly")
		require.NotContains(t, assemblies, "colourway")

		runs := purposeLockReason(3, 0, 0, 0)
		require.Contains(t, runs, "3 production runs")
		require.NotContains(t, runs, "colourway")
	})

	// A sold colourway is the one arm with no way out. It has to lead the message so it does not read
	// as one more chore next to the clearable ones.
	t.Run("a sold colourway leads and says the lock is final", func(t *testing.T) {
		reason := purposeLockReason(0, 1, 1, 0)
		require.True(t, len(reason) > 0)
		require.Contains(t, reason, "1 colourway already sold")
		require.Less(t, strings.Index(reason, "already sold"), strings.Index(reason, "live colourway"))
	})

	t.Run("several arms are listed together", func(t *testing.T) {
		reason := purposeLockReason(1, 1, 0, 2)
		require.Contains(t, reason, "1 production run")
		require.Contains(t, reason, "1 live colourway")
		require.Contains(t, reason, "2 style assemblies")
	})

	// The apisrv layer returns err.Error() to the operator and matches with errors.Is; wrapping has
	// to keep both working or the client either loses the detail or loses the FailedPrecondition code.
	t.Run("wrapped error keeps the sentinel and carries the detail", func(t *testing.T) {
		err := fmt.Errorf("%w: %s", entity.ErrTechCardPurposeLocked, purposeLockReason(0, 2, 0, 0))
		require.True(t, errors.Is(err, entity.ErrTechCardPurposeLocked))
		require.Contains(t, err.Error(), "2 live colourways")
	})
}
