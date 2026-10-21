package store

import (
	"context"
	"database/sql"
	"fmt"
	"strconv"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/stretchr/testify/require"
)

func addSearchTask(
	ctx context.Context,
	t *testing.T,
	s *MYSQLStore,
	title string,
	description string,
	board entity.TaskBoard,
) int {
	t.Helper()

	id, err := s.Tasks().AddTask(ctx, &entity.Task{
		TaskInsert: entity.TaskInsert{
			Title:       title,
			Description: sql.NullString{String: description, Valid: description != ""},
			Priority:    entity.TaskPriorityUnknown,
		},
		Board:     board,
		Status:    entity.TaskStatusTodo,
		CreatedBy: "task-search-test",
	})
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = testDB.ExecContext(context.Background(), `DELETE FROM task WHERE id = ?`, id)
	})
	return id
}

func taskIDs(tasks []entity.Task) []int {
	ids := make([]int, 0, len(tasks))
	for i := range tasks {
		ids = append(ids, tasks[i].Id)
	}
	return ids
}

func TestTaskSearchAcrossArchiveCommentsChecklistIDAndLiterals(t *testing.T) {
	taskRelationsGuard(t)

	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	s, err := NewForTest(ctx, *testCfg)
	require.NoError(t, err)

	archivedID := addSearchTask(ctx, t, s, "Archived marketing card", "unrelated archive notes", entity.TaskBoardMarketing)
	_, err = s.Tasks().AddTaskComment(ctx, &entity.TaskCommentInsert{
		TaskId: archivedID,
		Body:   "The ArchivedCommentNeedle appears only in this comment. RankNeedle too.",
	}, "task-search-test")
	require.NoError(t, err)
	require.NoError(t, s.Tasks().ArchiveTask(ctx, archivedID))

	checklistID := addSearchTask(ctx, t, s, "Active checklist card", "unrelated active notes", entity.TaskBoardDesign)
	_, err = s.Tasks().AddTaskChecklistItem(ctx, checklistID, "ChecklistOnlyNeedle and RankNeedle live only here")
	require.NoError(t, err)

	literalID := addSearchTask(ctx, t, s, "Campaign 50%_off", "literal discount", entity.TaskBoardContent)
	decoyID := addSearchTask(ctx, t, s, "Campaign 50XXYoff", "wildcard decoy", entity.TaskBoardContent)
	titleRankID := addSearchTask(ctx, t, s, "RankNeedle title hit", "ranking proof", entity.TaskBoardProduction)

	// TASK_BOARD_UNKNOWN is represented by the zero-value board. It must search every board and,
	// when requested, the archive as well.
	found, total, err := s.Tasks().ListTasks(ctx, entity.TaskListFilter{
		Query:           "  archivedcommentneedle  ",
		IncludeArchived: true,
	})
	require.NoError(t, err)
	require.Equal(t, 1, total)
	require.Len(t, found, 1)
	require.Equal(t, archivedID, found[0].Id)
	require.Equal(t, entity.TaskBoardMarketing, found[0].Board)
	require.True(t, found[0].ArchivedAt.Valid)
	require.NotNil(t, found[0].Match)
	require.Equal(t, "comment", found[0].Match.Field)
	require.Contains(t, strings.ToLower(found[0].Match.Snippet), "archivedcommentneedle")

	_, total, err = s.Tasks().ListTasks(ctx, entity.TaskListFilter{Query: "ArchivedCommentNeedle"})
	require.NoError(t, err)
	require.Zero(t, total, "the same comment hit must stay hidden when include_archived is false")

	found, total, err = s.Tasks().ListTasks(ctx, entity.TaskListFilter{Query: "CHECKLISTONLYNEEDLE"})
	require.NoError(t, err)
	require.Equal(t, 1, total)
	require.Equal(t, []int{checklistID}, taskIDs(found))
	require.Equal(t, "checklist", found[0].Match.Field)
	require.Contains(t, strings.ToLower(found[0].Match.Snippet), "checklistonlyneedle")

	found, _, err = s.Tasks().ListTasks(ctx, entity.TaskListFilter{Query: fmt.Sprintf("#%d", checklistID)})
	require.NoError(t, err)
	require.Contains(t, taskIDs(found), checklistID)
	for i := range found {
		if found[i].Id == checklistID {
			require.Equal(t, "id", found[i].Match.Field)
			require.Empty(t, found[i].Match.Snippet)
		}
	}
	found, _, err = s.Tasks().ListTasks(ctx, entity.TaskListFilter{Query: strconv.Itoa(checklistID)})
	require.NoError(t, err)
	require.Contains(t, taskIDs(found), checklistID)

	found, total, err = s.Tasks().ListTasks(ctx, entity.TaskListFilter{Query: "50%_off"})
	require.NoError(t, err)
	require.Equal(t, 1, total, "% and _ must be literals, not LIKE wildcards")
	require.Equal(t, []int{literalID}, taskIDs(found))
	require.NotContains(t, taskIDs(found), decoyID)
	require.Equal(t, "title", found[0].Match.Field)
	require.Empty(t, found[0].Match.Snippet)

	descriptionID := addSearchTask(ctx, t, s, "Cyrillic description card",
		strings.Repeat("я", 48)+" до\nИГОЛКА\tпосле "+strings.Repeat("ю", 48), entity.TaskBoardDevelopment)
	found, total, err = s.Tasks().ListTasks(ctx, entity.TaskListFilter{Query: "иголка"})
	require.NoError(t, err)
	require.Equal(t, 1, total)
	require.Equal(t, []int{descriptionID}, taskIDs(found))
	require.Equal(t, "description", found[0].Match.Field)
	require.True(t, utf8.ValidString(found[0].Match.Snippet))
	require.NotContains(t, found[0].Match.Snippet, "\n")
	require.NotContains(t, found[0].Match.Snippet, "\t")
	require.True(t, strings.HasPrefix(found[0].Match.Snippet, "…"))
	require.True(t, strings.HasSuffix(found[0].Match.Snippet, "…"))

	// Query ordering is active first, then title hits, then id descending. These three rows hit the
	// title, checklist, and archived comment respectively, so each ranking tier is observable.
	found, total, err = s.Tasks().ListTasks(ctx, entity.TaskListFilter{Query: "rankneedle", IncludeArchived: true})
	require.NoError(t, err)
	require.Equal(t, 3, total)
	require.Equal(t, []int{titleRankID, checklistID, archivedID}, taskIDs(found))
	require.Equal(t, []string{"title", "checklist", "comment"}, []string{
		found[0].Match.Field, found[1].Match.Field, found[2].Match.Field,
	})

	// Whitespace-only input must take the exact legacy path, including its board/status/position
	// ordering and hydrated response.
	legacy, legacyTotal, err := s.Tasks().ListTasks(ctx, entity.TaskListFilter{})
	require.NoError(t, err)
	blank, blankTotal, err := s.Tasks().ListTasks(ctx, entity.TaskListFilter{Query: " \t\n "})
	require.NoError(t, err)
	require.Equal(t, legacyTotal, blankTotal)
	require.Equal(t, legacy, blank)
	for i := range blank {
		require.Nil(t, blank[i].Match)
	}
}
