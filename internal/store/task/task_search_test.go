package task

import (
	"database/sql"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/stretchr/testify/require"
)

func TestTaskMatchSnippetIsRuneSafeAndOneLine(t *testing.T) {
	value := strings.Repeat("я", 48) + "  до\nИГОЛКА\tпосле  " + strings.Repeat("ю", 48)
	snippet, ok := taskMatchSnippet(value, "иголка")
	require.True(t, ok)
	require.True(t, utf8.ValidString(snippet))
	require.Contains(t, snippet, "ИГОЛКА")
	require.NotContains(t, snippet, "\n")
	require.NotContains(t, snippet, "\t")
	require.NotContains(t, snippet, "  ")
	require.True(t, strings.HasPrefix(snippet, "…"), snippet)
	require.True(t, strings.HasSuffix(snippet, "…"), snippet)
	require.LessOrEqual(t, utf8.RuneCountInString(snippet), taskMatchSnippetRunes+2)
}

func TestTaskMatchFieldPriority(t *testing.T) {
	task := entity.Task{
		Id: 42,
		TaskInsert: entity.TaskInsert{
			Title:       "plain title",
			Description: sql.NullString{String: "description needle", Valid: true},
		},
	}
	rows := []taskMatchTextRow{
		{TaskId: 42, Field: "comment", MatchText: "comment needle"},
		{TaskId: 42, Field: "checklist", MatchText: "checklist needle"},
	}

	require.Equal(t, "id", taskMatchForTask(&task, rows, "42", 42).Field)
	require.Equal(t, "description", taskMatchForTask(&task, rows, "needle", 0).Field)
	task.Description = sql.NullString{}
	require.Equal(t, "comment", taskMatchForTask(&task, rows, "needle", 0).Field)
	task.Title = "NEEDLE in title"
	match := taskMatchForTask(&task, rows, "needle", 0)
	require.Equal(t, "title", match.Field)
	require.Empty(t, match.Snippet)
}

func TestTaskSearchRuneSpanMatchesMySQLDefaultCaseAndAccentRules(t *testing.T) {
	start, end, ok := taskSearchRuneSpan("Пример CAFÉ", "cafe")
	require.True(t, ok)
	require.Equal(t, "CAFÉ", string([]rune("Пример CAFÉ")[start:end]))
}
