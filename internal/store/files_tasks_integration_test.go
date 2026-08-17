package store

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/stretchr/testify/require"
)

// addTaskFixtureForFiles creates one kanban card through the real store path and registers its
// removal (task_file cascades with the card).
func addTaskFixtureForFiles(ctx context.Context, t *testing.T, s *MYSQLStore, title string,
	board entity.TaskBoard, taskStatus entity.TaskStatus, assignee string, due sql.NullTime) int {
	t.Helper()
	id, err := s.Tasks().AddTask(ctx, &entity.Task{
		TaskInsert: entity.TaskInsert{
			Title:    title,
			Assignee: assignee,
			DueDate:  due,
			// Приоритет — NOT NULL с CHECK по словарю: пустая строка роняет вставку 3819.
			Priority: entity.TaskPriorityUnknown,
		},
		Board:     board,
		Status:    taskStatus,
		CreatedBy: "test-files-tasks",
	})
	require.NoError(t, err)
	t.Cleanup(func() { _, _ = testDB.Exec(`DELETE FROM task WHERE id = ?`, id) })
	return id
}

// TestLibraryFileTaskLinks covers Ф4 on the store side: связь task_file, прочитанная и записанная
// СО СТОРОНЫ ФАЙЛА.
//
// Тест обязан быть контейнерным. Каждое утверждение здесь — утверждение о SQL и о правилах схемы:
// идемпотентность держится на uniq_task_file, отказ удаления — на RESTRICT у task_file.file_id, а
// главная проверка (несуществующий конец связи ОТКАЗЫВАЕТ) ловит ровно то, что INSERT IGNORE
// проглотил бы молча. Ни одно из этих утверждений не видно из Go.
func TestLibraryFileTaskLinks(t *testing.T) {
	filesGuard(t)

	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	s, err := NewForTest(ctx, *testCfg)
	require.NoError(t, err)

	countLinks := func(taskID, fileID int) int {
		var n int
		require.NoError(t, testDB.QueryRowContext(ctx,
			`SELECT COUNT(*) FROM task_file WHERE task_id = ? AND file_id = ?`, taskID, fileID).Scan(&n))
		return n
	}
	displayOrder := func(taskID, fileID int) int {
		var n int
		require.NoError(t, testDB.QueryRowContext(ctx,
			`SELECT display_order FROM task_file WHERE task_id = ? AND file_id = ?`, taskID, fileID).Scan(&n))
		return n
	}

	due := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)
	fileA := insertLibraryFileFixture(ctx, t, "brief.pdf", 10, "pasha")
	fileB := insertLibraryFileFixture(ctx, t, "spec.pdf", 20, "pasha")
	prodTask := addTaskFixtureForFiles(ctx, t, s, "отшить семпл", entity.TaskBoardProduction,
		entity.TaskStatusInProgress, "kirill", sql.NullTime{Time: due, Valid: true})

	t.Run("attaching twice leaves one row and does not renumber the task's attachments", func(t *testing.T) {
		require.NoError(t, s.Tasks().AttachFileToTask(ctx, fileA, prodTask))
		require.NoError(t, s.Tasks().AttachFileToTask(ctx, fileB, prodTask))
		require.Equal(t, 0, displayOrder(prodTask, fileA))
		require.Equal(t, 1, displayOrder(prodTask, fileB), "второй файл обязан встать в ХВОСТ порядка")

		// Повтор — совпадение с уже достигнутым состоянием, а не событие: ни новой строки, ни
		// перестановки порядка вложений, которых эта кнопка не касалась.
		require.NoError(t, s.Tasks().AttachFileToTask(ctx, fileA, prodTask))
		require.Equal(t, 1, countLinks(prodTask, fileA))
		require.Equal(t, 0, displayOrder(prodTask, fileA))
		require.Equal(t, 1, displayOrder(prodTask, fileB))
	})

	t.Run("detaching what is not attached is a no-op and touches nothing else", func(t *testing.T) {
		lonely := insertLibraryFileFixture(ctx, t, "never-attached.pdf", 5, "pasha")
		require.NoError(t, s.Tasks().DetachFileFromTask(ctx, lonely, prodTask))
		require.NoError(t, s.Tasks().DetachFileFromTask(ctx, lonely, prodTask))
		require.Equal(t, 0, countLinks(prodTask, lonely))
		// Соседние привязки той же задачи целы: повтор отвязки не имеет права быть широким.
		require.Equal(t, 1, countLinks(prodTask, fileA))
		require.Equal(t, 1, countLinks(prodTask, fileB))

		// Отвязка и повторная привязка: файл возвращается в ХВОСТ, а не на своё прежнее место —
		// порядок вложений принадлежит задаче и переигрывается по факту привязки.
		require.NoError(t, s.Tasks().DetachFileFromTask(ctx, fileA, prodTask))
		require.Equal(t, 0, countLinks(prodTask, fileA))
		require.NoError(t, s.Tasks().AttachFileToTask(ctx, fileA, prodTask))
		require.Equal(t, 2, displayOrder(prodTask, fileA))
	})

	t.Run("the file card reads the whole row, and an archived holder is not hidden", func(t *testing.T) {
		designTask := addTaskFixtureForFiles(ctx, t, s, "снять мерки", entity.TaskBoardDesign,
			entity.TaskStatusTodo, "", sql.NullTime{})
		archivedTask := addTaskFixtureForFiles(ctx, t, s, "старый бриф", entity.TaskBoardMarketing,
			entity.TaskStatusDone, "olya", sql.NullTime{})
		require.NoError(t, s.Tasks().AttachFileToTask(ctx, fileA, designTask))
		require.NoError(t, s.Tasks().AttachFileToTask(ctx, fileA, archivedTask))
		require.NoError(t, s.Tasks().ArchiveTask(ctx, archivedTask))

		rows, err := s.Tasks().ListTasksByFileId(ctx, fileA)
		require.NoError(t, err)
		require.Len(t, rows, 3)

		// Порядок: живые задачи первыми (свежие сверху), архивная — в хвосте. АРХИВНАЯ ВХОДИТ В
		// ОТВЕТ намеренно: DeleteFile называет держателями все строки task_file, и спрятанная здесь
		// задача превратила бы отказ удаления в тупик — «отцепите его выше» при пустом списке.
		require.Equal(t, designTask, rows[0].TaskId)
		require.Equal(t, prodTask, rows[1].TaskId)
		require.Equal(t, archivedTask, rows[2].TaskId)

		byID := map[int]entity.LibraryFileTask{}
		for _, r := range rows {
			byID[r.TaskId] = r
		}
		got := byID[prodTask]
		require.Equal(t, "отшить семпл", got.Title)
		require.Equal(t, entity.TaskStatusInProgress, got.Status)
		require.Equal(t, entity.TaskBoardProduction, got.Board)
		require.Equal(t, "kirill", got.Assignee)
		require.True(t, got.DueDate.Valid)
		require.True(t, due.Equal(got.DueDate.Time))

		// «Никто не взял» и «срока нет» — состояния, а не пропуски.
		require.Empty(t, byID[designTask].Assignee)
		require.False(t, byID[designTask].DueDate.Valid)
		require.Equal(t, entity.TaskBoardDesign, byID[designTask].Board)

		// Файл, которого никто не держит, отвечает пустым списком, а не ошибкой.
		free, err := s.Tasks().ListTasksByFileId(ctx, insertLibraryFileFixture(ctx, t, "free.pdf", 1, "pasha"))
		require.NoError(t, err)
		require.Empty(t, free)
	})

	t.Run("a missing end of the link FAILS as a foreign key instead of succeeding silently", func(t *testing.T) {
		// ГЛАВНАЯ ПРОВЕРКА ФАЗЫ. План называл INSERT IGNORE, но IGNORE в MySQL глушит и 1452:
		// привязка к несуществующей задаче вернула бы nil, ничего не записав, и на бете это
		// читалось бы как потерянная привязка. Оба конца проверяются, потому что оба — предмет
		// запроса.
		err := s.Tasks().AttachFileToTask(ctx, fileA, 2147483600)
		require.Error(t, err)
		require.True(t, s.IsErrForeignKeyViolation(err),
			"несуществующая задача обязана доехать до хендлера нарушением внешнего ключа")

		err = s.Tasks().AttachFileToTask(ctx, 2147483600, prodTask)
		require.Error(t, err)
		require.True(t, s.IsErrForeignKeyViolation(err),
			"несуществующий файл обязан доехать до хендлера нарушением внешнего ключа")

		var orphans int
		require.NoError(t, testDB.QueryRowContext(ctx,
			`SELECT COUNT(*) FROM task_file WHERE task_id = 2147483600 OR file_id = 2147483600`).Scan(&orphans))
		require.Zero(t, orphans)
	})

	t.Run("a held file refuses deletion by name and stops refusing once detached", func(t *testing.T) {
		held := insertLibraryFileFixture(ctx, t, "held-by-a-task.pdf", 30, "pasha")
		require.NoError(t, s.Tasks().AttachFileToTask(ctx, held, prodTask))

		_, err := s.Files().DeleteFile(ctx, held)
		require.ErrorIs(t, err, entity.ErrLibraryFileInUse,
			"RESTRICT на task_file.file_id обязан пережить Ф4")
		require.Contains(t, err.Error(), "#", "отказ обязан НАЗВАТЬ держателя")

		// Отвязка с карточки файла действительно освобождает файл — иначе кнопка «отцепить» была бы
		// украшением, а удаление продолжало бы отказывать без причины на экране.
		require.NoError(t, s.Tasks().DetachFileFromTask(ctx, held, prodTask))
		keys, err := s.Files().DeleteFile(ctx, held)
		require.NoError(t, err)
		require.NotEmpty(t, keys)
	})
}
