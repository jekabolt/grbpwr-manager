package techcard

import (
	"errors"
	"testing"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/stretchr/testify/require"
)

func TestMemoizedTechCardSnapshotID(t *testing.T) {
	t.Run("implicit writer without a blob keeps the old no-snapshot behaviour", func(t *testing.T) {
		calls := 0
		getSnapshotID := memoizedTechCardSnapshotID(&entity.TechCardInsert{}, 7, func(string) (int64, error) {
			calls++
			return 0, errors.New("must not be called")
		})

		id, err := getSnapshotID("pattern removed")
		require.NoError(t, err)
		require.Zero(t, id)
		require.Zero(t, calls)
	})

	t.Run("a stale pre-update blob is refused before insertion", func(t *testing.T) {
		calls := 0
		getSnapshotID := memoizedTechCardSnapshotID(&entity.TechCardInsert{
			SnapshotJSON:        `{}`,
			SnapshotLockVersion: 6,
		}, 7, func(string) (int64, error) {
			calls++
			return 41, nil
		})

		id, err := getSnapshotID("pattern replaced")
		require.ErrorIs(t, err, entity.ErrTechCardConflict)
		require.Zero(t, id)
		require.Zero(t, calls)
	})

	t.Run("the first archive inserts once and every later archive shares its id", func(t *testing.T) {
		var reasons []string
		getSnapshotID := memoizedTechCardSnapshotID(&entity.TechCardInsert{
			SnapshotJSON:        `{}`,
			SnapshotLockVersion: 7,
		}, 7, func(reason string) (int64, error) {
			reasons = append(reasons, reason)
			return 41, nil
		})

		first, err := getSnapshotID("pattern replaced")
		require.NoError(t, err)
		second, err := getSnapshotID("pattern removed")
		require.NoError(t, err)
		require.EqualValues(t, 41, first)
		require.Equal(t, first, second)
		require.Equal(t, []string{"pattern replaced"}, reasons,
			"one pre-save card is shared by every row archived in the save")
	})

	t.Run("an explicit request without its blob still calls the validating insert", func(t *testing.T) {
		wantErr := errors.New("snapshot reason was provided without a pre-update card snapshot")
		calls := 0
		getSnapshotID := memoizedTechCardSnapshotID(&entity.TechCardInsert{
			SnapshotReason:      "dxf-reupload",
			SnapshotLockVersion: 7,
		}, 7, func(reason string) (int64, error) {
			calls++
			require.Equal(t, "dxf-reupload", reason)
			return 0, wantErr
		})

		id, err := getSnapshotID("dxf-reupload")
		require.ErrorIs(t, err, wantErr)
		require.Zero(t, id)
		require.Equal(t, 1, calls)
	})
}
