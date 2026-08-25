package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/stretchr/testify/require"
)

// taskRelationsGuard keeps this test off the configured (production) database, exactly as the other
// store integration tests do: without CI set, TestMain builds its DSN from config.toml and the
// cleanup DROPS every table it finds.
func taskRelationsGuard(t *testing.T) {
	t.Helper()
	if os.Getenv("CI") == "" &&
		!strings.Contains(testCfg.DSN, "127.0.0.1") &&
		!strings.Contains(testCfg.DSN, "localhost") {
		t.Skip("skipping outside CI unless the DSN targets a local container (avoids the configured prod DB)")
	}
}

// addRelationsTask создаёт карточку через РЕАЛЬНЫЙ путь стора и регистрирует её удаление.
func addRelationsTask(ctx context.Context, t *testing.T, s *MYSQLStore, title string, assignees []string) int {
	t.Helper()
	id, err := s.Tasks().AddTask(ctx, &entity.Task{
		TaskInsert: entity.TaskInsert{
			Title:     title,
			Assignees: assignees,
			// Приоритет — NOT NULL с CHECK по словарю: пустая строка роняет вставку 3819.
			Priority: entity.TaskPriorityUnknown,
		},
		Board:     entity.TaskBoardDesign,
		Status:    entity.TaskStatusTodo,
		CreatedBy: "test-task-relations",
	})
	require.NoError(t, err)
	t.Cleanup(func() { _, _ = testDB.ExecContext(context.Background(), `DELETE FROM task WHERE id = ?`, id) })
	return id
}

// TestTaskAssigneesRoundTrip — мультиасайн (0337) по обоим путям чтения и по обоим путям записи.
//
// ОБА ПУТИ ЧТЕНИЯ АССЕРТЯТСЯ НАМЕРЕННО. Доска читает ListTasks, детальная страница — GetTaskById, и
// список, молча пропавший в одном из двух, — это ровно тот способ, которым рождается «форма
// сохранила карточку без исполнителей»: она шлёт то, что прочла.
func TestTaskAssigneesRoundTrip(t *testing.T) {
	taskRelationsGuard(t)

	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	s, err := NewForTest(ctx, *testCfg)
	require.NoError(t, err)

	suffix := fmt.Sprintf("%d", time.Now().UnixNano())
	a := "asg-a-" + suffix
	b := "asg-b-" + suffix
	c := "asg-c-" + suffix

	taskID := addRelationsTask(ctx, t, s, "мультиасайн "+suffix, []string{a, b, c})

	// --- создание: оба пути видят один и тот же список в одном и том же порядке ----------------
	got, err := s.Tasks().GetTaskById(ctx, taskID)
	require.NoError(t, err)
	require.Equal(t, []string{a, b, c}, got.Assignees, "GetTaskById обязан вернуть список в порядке записи")

	listed, total, err := s.Tasks().ListTasks(ctx, entity.TaskListFilter{Assignee: a})
	require.NoError(t, err)
	require.Equal(t, 1, total, "уникальный исполнитель обязан выбрать ровно эту карточку")
	require.Len(t, listed, 1)
	require.Equal(t, []string{a, b, c}, listed[0].Assignees, "ListTasks обязан нести тот же список, что GetTaskById")

	// --- сохранение: ПОЛНАЯ ЗАМЕНА, как у labels ----------------------------------------------
	require.NoError(t, s.Tasks().UpdateTask(ctx, taskID, &entity.TaskInsert{
		Title:     "мультиасайн сужен " + suffix,
		Assignees: []string{b},
		Priority:  entity.TaskPriorityUnknown,
	}))
	got, err = s.Tasks().GetTaskById(ctx, taskID)
	require.NoError(t, err)
	require.Equal(t, []string{b}, got.Assignees, "сохранение обязано ЗАМЕНИТЬ список целиком, а не дополнить его")

	// Снятый исполнитель больше не находит карточку — иначе «мои задачи» показывали бы чужую работу.
	_, total, err = s.Tasks().ListTasks(ctx, entity.TaskListFilter{Assignee: a})
	require.NoError(t, err)
	require.Equal(t, 0, total)

	// --- пустой список означает «задачу никто не взял» -----------------------------------------
	require.NoError(t, s.Tasks().UpdateTask(ctx, taskID, &entity.TaskInsert{
		Title:    "мультиасайн снят " + suffix,
		Priority: entity.TaskPriorityUnknown,
	}))
	got, err = s.Tasks().GetTaskById(ctx, taskID)
	require.NoError(t, err)
	require.Empty(t, got.Assignees)

	// Третий исполнитель исходного набора тоже отпущен: полная замена — это замена, а не сужение.
	_, total, err = s.Tasks().ListTasks(ctx, entity.TaskListFilter{Assignee: c})
	require.NoError(t, err)
	require.Equal(t, 0, total)
}

// TestTaskAssigneeFilterIsMembership — фильтр «мои задачи» обязан находить и те карточки, где я
// ВТОРОЙ. Это и есть та единственная функция, ради которой мультиасайн заводили: список из трёх
// исполнителей, у которого фильтр видит только первого, — мультиасайн только на вид.
func TestTaskAssigneeFilterIsMembership(t *testing.T) {
	taskRelationsGuard(t)

	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	s, err := NewForTest(ctx, *testCfg)
	require.NoError(t, err)

	suffix := fmt.Sprintf("%d", time.Now().UnixNano())
	olya := "olya-" + suffix
	pasha := "pasha-" + suffix

	taskA := addRelationsTask(ctx, t, s, "A "+suffix, []string{olya})
	taskB := addRelationsTask(ctx, t, s, "B "+suffix, []string{olya, pasha})
	addRelationsTask(ctx, t, s, "C "+suffix, nil) // ничья: встроенный отрицательный случай

	// pasha ВТОРОЙ в списке B — и обязан её найти.
	listed, total, err := s.Tasks().ListTasks(ctx, entity.TaskListFilter{Assignee: pasha})
	require.NoError(t, err)
	require.Equal(t, 1, total)
	require.Len(t, listed, 1)
	require.Equal(t, taskB, listed[0].Id, "фильтр обязан находить карточку по ВТОРОМУ исполнителю")

	// olya на обеих — и ни одна не задваивается (EXISTS, а не JOIN: JOIN вернул бы B дважды).
	listed, total, err = s.Tasks().ListTasks(ctx, entity.TaskListFilter{Assignee: olya})
	require.NoError(t, err)
	require.Equal(t, 2, total)
	require.Len(t, listed, 2)
	ids := map[int]bool{listed[0].Id: true, listed[1].Id: true}
	require.True(t, ids[taskA] && ids[taskB])

	// Никем не взятая карточка не попадает ни под один именной фильтр.
	_, total, err = s.Tasks().ListTasks(ctx, entity.TaskListFilter{Assignee: "nobody-" + suffix})
	require.NoError(t, err)
	require.Equal(t, 0, total)
}

// TestTaskParentCycleAndCascade — иерархия сабтасок (0338): запрет циклов и граница удаления.
func TestTaskParentCycleAndCascade(t *testing.T) {
	taskRelationsGuard(t)

	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	s, err := NewForTest(ctx, *testCfg)
	require.NoError(t, err)

	suffix := fmt.Sprintf("%d", time.Now().UnixNano())
	taskA := addRelationsTask(ctx, t, s, "A "+suffix, nil)
	taskB := addRelationsTask(ctx, t, s, "B "+suffix, nil)
	taskC := addRelationsTask(ctx, t, s, "C "+suffix, nil)

	// Цепочка A ← B ← C: B под A, C под B.
	require.NoError(t, s.Tasks().SetTaskParent(ctx, taskB, taskA))
	require.NoError(t, s.Tasks().SetTaskParent(ctx, taskC, taskB))

	// Замкнуть кольцо через ДВА уровня: A под C. Одного «не себе самому» здесь мало — нужен подъём.
	err = s.Tasks().SetTaskParent(ctx, taskA, taskC)
	require.ErrorIs(t, err, entity.ErrTaskParentCycle, "предок не может стать потомком")

	// Вырожденный случай — сам себе родитель.
	err = s.Tasks().SetTaskParent(ctx, taskA, taskA)
	require.ErrorIs(t, err, entity.ErrTaskParentCycle)

	// Отказ ничего не записал: цепочка цела.
	got, err := s.Tasks().GetTaskById(ctx, taskA)
	require.NoError(t, err)
	require.False(t, got.ParentTaskId.Valid, "отказ обязан оставить карточку верхнеуровневой")

	// Свёртка сабтасок на родителе: активных детей у A ровно один (B), сделанных — ноль.
	got, err = s.Tasks().GetTaskById(ctx, taskA)
	require.NoError(t, err)
	require.Equal(t, 1, got.SubtaskTotal)
	require.Equal(t, 0, got.SubtaskDone)

	// Доделанный ребёнок считается сделанным — иначе счётчик «1 из 3» не сдвинулся бы никогда.
	require.NoError(t, s.Tasks().MoveTask(ctx, taskB, entity.TaskBoardDesign, entity.TaskStatusDone, 0))
	got, err = s.Tasks().GetTaskById(ctx, taskA)
	require.NoError(t, err)
	require.Equal(t, 1, got.SubtaskTotal)
	require.Equal(t, 1, got.SubtaskDone)

	// АРХИВНЫЙ ребёнок из счётчика ВЫПАДАЕТ целиком: счётчик отвечает «сколько ещё делать», а
	// снятая с доски сабтаска вечно показывала бы «1 из 1» там, где работы больше нет.
	require.NoError(t, s.Tasks().ArchiveTask(ctx, taskB))
	got, err = s.Tasks().GetTaskById(ctx, taskA)
	require.NoError(t, err)
	require.Equal(t, 0, got.SubtaskTotal)
	require.Equal(t, 0, got.SubtaskDone)
	require.NoError(t, s.Tasks().UnarchiveTask(ctx, taskB))

	// Фильтр «сабтаски этой задачи».
	kids, total, err := s.Tasks().ListTasks(ctx, entity.TaskListFilter{ParentTaskId: taskA})
	require.NoError(t, err)
	require.Equal(t, 1, total)
	require.Len(t, kids, 1)
	require.Equal(t, taskB, kids[0].Id)

	// Снять родителя.
	require.NoError(t, s.Tasks().SetTaskParent(ctx, taskC, 0))
	got, err = s.Tasks().GetTaskById(ctx, taskC)
	require.NoError(t, err)
	require.False(t, got.ParentTaskId.Valid)

	// УДАЛЕНИЕ РОДИТЕЛЯ НЕ УНОСИТ ДЕТЕЙ. ON DELETE SET NULL, а не CASCADE: удаление одной карточки,
	// молча уносящее дерево чужой работы, — это история order_item ON DELETE CASCADE из 0001.
	require.NoError(t, s.Tasks().SetTaskParent(ctx, taskC, taskB))
	require.NoError(t, s.Tasks().DeleteTask(ctx, taskB))
	got, err = s.Tasks().GetTaskById(ctx, taskC)
	require.NoError(t, err, "ребёнок обязан пережить удаление родителя")
	require.False(t, got.ParentTaskId.Valid, "ссылка обязана занулиться, а не повиснуть")
}

// TestTaskLinksNormalizationAndCascade — связи (0338): направленность, нормализация, реверс-чек и
// каскад с обоих концов.
func TestTaskLinksNormalizationAndCascade(t *testing.T) {
	taskRelationsGuard(t)

	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	s, err := NewForTest(ctx, *testCfg)
	require.NoError(t, err)

	suffix := fmt.Sprintf("%d", time.Now().UnixNano())
	taskA := addRelationsTask(ctx, t, s, "блокер "+suffix, nil)
	taskB := addRelationsTask(ctx, t, s, "заблокированная "+suffix, nil)

	countLinks := func(from, to int, kind string) int {
		var n int
		require.NoError(t, testDB.QueryRowContext(ctx,
			`SELECT COUNT(*) FROM task_link WHERE from_task_id = ? AND to_task_id = ? AND kind = ?`,
			from, to, kind).Scan(&n))
		return n
	}

	// --- blocks: одна строка на факт, читается с обоих концов ---------------------------------
	require.NoError(t, s.Tasks().AddTaskLink(ctx, taskA, taskB, entity.TaskLinkKindBlocks, "tester"))
	// Повтор — совпадение с уже достигнутым состоянием, а не 1062.
	require.NoError(t, s.Tasks().AddTaskLink(ctx, taskA, taskB, entity.TaskLinkKindBlocks, "tester"))
	require.Equal(t, 1, countLinks(taskA, taskB, "blocks"))

	fromA, err := s.Tasks().GetTaskById(ctx, taskA)
	require.NoError(t, err)
	require.Len(t, fromA.Links, 1)
	require.Equal(t, entity.TaskLinkRoleBlocks, fromA.Links[0].Role)
	require.Equal(t, taskB, fromA.Links[0].TaskId)
	// Второй конец приезжает РАЗРЕШЁННЫМ — ради этого связь и вкладывается в карточку.
	require.Equal(t, "заблокированная "+suffix, fromA.Links[0].Title)

	fromB, err := s.Tasks().GetTaskById(ctx, taskB)
	require.NoError(t, err)
	require.Len(t, fromB.Links, 1)
	require.Equal(t, entity.TaskLinkRoleBlockedBy, fromB.Links[0].Role,
		"та же ОДНА строка с другого конца обязана читаться как blocked_by")
	require.Equal(t, taskA, fromB.Links[0].TaskId)

	// --- реверс-блок: A⇄B по blocks — ошибка всегда -------------------------------------------
	err = s.Tasks().AddTaskLink(ctx, taskB, taskA, entity.TaskLinkKindBlocks, "tester")
	require.ErrorIs(t, err, entity.ErrTaskReverseBlock)
	require.Equal(t, 0, countLinks(taskB, taskA, "blocks"), "отказ не имеет права оставить строку")

	// --- relates нормализуется: одна строка, from < to, с какого конца ни заводи ---------------
	hi, lo := taskA, taskB
	if hi < lo {
		hi, lo = lo, hi
	}
	require.NoError(t, s.Tasks().AddTaskLink(ctx, hi, lo, entity.TaskLinkKindRelates, "tester"))
	require.NoError(t, s.Tasks().AddTaskLink(ctx, lo, hi, entity.TaskLinkKindRelates, "tester"))
	require.Equal(t, 1, countLinks(lo, hi, "relates"), "симметричная связь — ОДНА строка, не две")
	require.Equal(t, 0, countLinks(hi, lo, "relates"), "ненормализованная пара невыразима CHECK'ом")

	// Снятие с ЛЮБОГО конца — по тому же нормализованному ключу.
	require.NoError(t, s.Tasks().DeleteTaskLink(ctx, hi, lo, entity.TaskLinkKindRelates))
	require.Equal(t, 0, countLinks(lo, hi, "relates"))
	// Снять несуществующую — no-op, не ошибка.
	require.NoError(t, s.Tasks().DeleteTaskLink(ctx, hi, lo, entity.TaskLinkKindRelates))

	// --- каскад: удалили один конец — связи не осталось ---------------------------------------
	require.NoError(t, s.Tasks().DeleteTask(ctx, taskB))
	require.Equal(t, 0, countLinks(taskA, taskB, "blocks"), "связь без конца бессодержательна")
}

// TestTaskCommentAuthorAndDeletion — авторство реплики (0339): живая ссылка выводится сервером, а
// удаление НЕ идемпотентно-молчаливо.
func TestTaskCommentAuthorAndDeletion(t *testing.T) {
	taskRelationsGuard(t)

	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	s, err := NewForTest(ctx, *testCfg)
	require.NoError(t, err)

	suffix := fmt.Sprintf("%d", time.Now().UnixNano())
	live := "live-" + suffix
	ghost := "ghost-" + suffix // аккаунта с таким именем НЕТ — встроенный отрицательный случай

	res, err := testDB.ExecContext(ctx,
		`INSERT INTO admins (username, password_hash) VALUES (?, 'x')`, live)
	require.NoError(t, err)
	adminID64, err := res.LastInsertId()
	require.NoError(t, err)
	t.Cleanup(func() { _, _ = testDB.ExecContext(context.Background(), `DELETE FROM admins WHERE id = ?`, adminID64) })

	taskID := addRelationsTask(ctx, t, s, "реплики "+suffix, nil)

	liveID, err := s.Tasks().AddTaskComment(ctx, &entity.TaskCommentInsert{TaskId: taskID, Body: "живой"}, live)
	require.NoError(t, err)
	ghostID, err := s.Tasks().AddTaskComment(ctx, &entity.TaskCommentInsert{TaskId: taskID, Body: "призрак"}, ghost)
	require.NoError(t, err)

	// Живая ссылка ВЫВОДИТСЯ стором из той же строки username — вторым параметром её не присылают,
	// иначе однажды приедет реплика, у которой строка говорит одно, а ссылка ведёт на другого.
	stored, err := s.Tasks().GetTaskCommentById(ctx, liveID)
	require.NoError(t, err)
	require.Equal(t, live, stored.Author)
	require.True(t, stored.AuthorId.Valid)
	require.Equal(t, int32(adminID64), stored.AuthorId.Int32)

	// Автора, которого нет среди аккаунтов, ссылка не выдумывает: NULL, а строка-факт остаётся.
	stored, err = s.Tasks().GetTaskCommentById(ctx, ghostID)
	require.NoError(t, err)
	require.Equal(t, ghost, stored.Author)
	require.False(t, stored.AuthorId.Valid, "ссылка в никуда хуже её отсутствия")

	// Лента несёт обе половины авторства.
	feed, err := s.Tasks().ListTaskComments(ctx, taskID)
	require.NoError(t, err)
	require.Len(t, feed, 2)
	require.True(t, feed[0].AuthorId.Valid)
	require.False(t, feed[1].AuthorId.Valid)

	// Удаление НЕ молчаливо-идемпотентно: второй раз — sql.ErrNoRows, потому что хендлеру нужен
	// NotFound. «Удалено» про то, чего нет, оставляет реплику на экране.
	require.NoError(t, s.Tasks().DeleteTaskComment(ctx, liveID))
	err = s.Tasks().DeleteTaskComment(ctx, liveID)
	require.True(t, errors.Is(err, sql.ErrNoRows), "второе удаление обязано сказать «нечего удалять», got %v", err)

	feed, err = s.Tasks().ListTaskComments(ctx, taskID)
	require.NoError(t, err)
	require.Len(t, feed, 1, "удаление одной реплики не имеет права задеть соседнюю")
}
