package store

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/stretchr/testify/require"
)

func boardIDs(boards []entity.TaskBoardEntry) []int {
	ids := make([]int, 0, len(boards))
	for i := range boards {
		ids = append(ids, boards[i].Id)
	}
	return ids
}

func TestTaskBoardsReorderRequiresExactSetAndPersistsPositions(t *testing.T) {
	taskRelationsGuard(t)

	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
	s, err := NewForTest(ctx, *testCfg)
	require.NoError(t, err)

	original, err := s.Tasks().ListTaskBoards(ctx)
	require.NoError(t, err)
	originalIDs := boardIDs(original)

	suffix := time.Now().UTC().UnixNano()
	first, err := s.Tasks().AddTaskBoard(ctx, fmt.Sprintf("reorder-a-%d", suffix), "board-reorder-test")
	require.NoError(t, err)
	second, err := s.Tasks().AddTaskBoard(ctx, fmt.Sprintf("reorder-b-%d", suffix), "board-reorder-test")
	require.NoError(t, err)
	t.Cleanup(func() {
		_ = s.Tasks().DeleteTaskBoard(context.Background(), first, 0)
		_ = s.Tasks().DeleteTaskBoard(context.Background(), second, 0)
		_, _ = s.Tasks().ReorderTaskBoards(context.Background(), originalIDs)
	})

	current, err := s.Tasks().ListTaskBoards(ctx)
	require.NoError(t, err)
	want := boardIDs(current)
	for left, right := 0, len(want)-1; left < right; left, right = left+1, right-1 {
		want[left], want[right] = want[right], want[left]
	}

	reordered, err := s.Tasks().ReorderTaskBoards(ctx, want)
	require.NoError(t, err)
	require.Equal(t, want, boardIDs(reordered))
	for i := range reordered {
		require.Equal(t, i, reordered[i].Position)
	}
	listed, err := s.Tasks().ListTaskBoards(ctx)
	require.NoError(t, err)
	require.Equal(t, want, boardIDs(listed), "ListTaskBoards must follow position")

	invalidSets := [][]int{
		append([]int(nil), want[:len(want)-1]...),
		append(append([]int(nil), want[:len(want)-1]...), want[0]),
		append(append([]int(nil), want[:len(want)-1]...), 2147483600),
	}
	for _, ids := range invalidSets {
		_, err = s.Tasks().ReorderTaskBoards(ctx, ids)
		require.ErrorIs(t, err, entity.ErrTaskBoardSetMismatch)
		listed, listErr := s.Tasks().ListTaskBoards(ctx)
		require.NoError(t, listErr)
		require.Equal(t, want, boardIDs(listed), "invalid reorder must roll back completely")
	}
}
