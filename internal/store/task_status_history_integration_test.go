package store

import (
	"context"
	"testing"
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/stretchr/testify/require"
)

// TestTaskStatusHistoryRoundTrip covers both write paths and proves board/position-only moves are
// not audit events. It uses the guarded shared-store harness and must not be run against production.
func TestTaskStatusHistoryRoundTrip(t *testing.T) {
	taskRelationsGuard(t)

	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	s, err := NewForTest(ctx, *testCfg)
	require.NoError(t, err)

	taskID := addRelationsTask(ctx, t, s, "status history "+time.Now().UTC().Format(time.RFC3339Nano), nil)
	got, err := s.Tasks().GetTaskById(ctx, taskID)
	require.NoError(t, err)
	require.Len(t, got.StatusHistory, 1)
	require.False(t, got.StatusHistory[0].FromStatus.Valid)
	require.Equal(t, entity.TaskStatusTodo, got.StatusHistory[0].ToStatus)
	require.Equal(t, "test-task-relations", got.StatusHistory[0].ChangedBy)

	// Same status with a different board/position is placement-only and writes no history row.
	require.NoError(t, s.Tasks().MoveTask(ctx, taskID, entity.TaskBoardRef{Slug: entity.TaskBoardDevelopment}, entity.TaskStatusTodo, 0, "anna"))
	got, err = s.Tasks().GetTaskById(ctx, taskID)
	require.NoError(t, err)
	require.Len(t, got.StatusHistory, 1)

	require.NoError(t, s.Tasks().MoveTask(ctx, taskID, entity.TaskBoardRef{}, entity.TaskStatusReview, 0, "anna"))
	got, err = s.Tasks().GetTaskById(ctx, taskID)
	require.NoError(t, err)
	require.Len(t, got.StatusHistory, 2)
	require.Equal(t, string(entity.TaskStatusTodo), got.StatusHistory[0].FromStatus.String)
	require.Equal(t, entity.TaskStatusReview, got.StatusHistory[0].ToStatus)
	require.Equal(t, "anna", got.StatusHistory[0].ChangedBy)

	// Simulate a pre-0418 task by removing only its initial row. The real post-deploy transition
	// stays newest and GetTaskById appends a status-less creation fact from the task row.
	_, err = testDB.ExecContext(ctx,
		`DELETE FROM task_status_history WHERE task_id = ? AND from_status IS NULL`, taskID)
	require.NoError(t, err)
	got, err = s.Tasks().GetTaskById(ctx, taskID)
	require.NoError(t, err)
	require.Len(t, got.StatusHistory, 2)
	require.Equal(t, entity.TaskStatusReview, got.StatusHistory[0].ToStatus)
	require.False(t, got.StatusHistory[1].FromStatus.Valid)
	require.Empty(t, got.StatusHistory[1].ToStatus)
	require.Equal(t, "test-task-relations", got.StatusHistory[1].ChangedBy)
}
