package store

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/stretchr/testify/require"
)

// ПРОЕКТ ↔ ЗАДАЧА (0322) — ПРИЁМКА ВОСЬМОЙ ГЛУБОКОЙ ССЫЛКИ.
//
// 0320 научила тему быть проектом, 0321 связала проект со стилями, здесь к проекту привязывается
// РАБОТА. Тест обязан быть контейнерным: каждое утверждение ниже — утверждение о схеме и о
// транзакции, и ни одно не видно из Go.
//
//  1. «ТОЛЬКО ПРОЕКТ» ДЕРЖИТСЯ КОДОМ, А НЕ КЛЮЧОМ. Внешний ключ на ярлыке НЕ срабатывает — тема
//     существует, — поэтому проверка либо стоит в сторе, либо не стоит нигде, и отличить эти два
//     случая можно только живой записью.
//  2. ТРИ ЧИСЛА ПОНИЖЕНИЯ НЕ ИМЕЮТ ПРАВА ПЕРЕПУТАТЬСЯ МЕСТАМИ. Подмена одного другим — рабочий
//     код, который просто отвечает не о том; на равных количествах она невидима, поэтому
//     количества здесь РАЗНЫЕ (1 / 2 / 3).
//  3. КАСКАДЫ И SET NULL. «Задача переживает удаление темы» — свойство ключа, а не Go.
//  4. СЛИЯНИЕ. Без переноса ссылки обнулились бы МОЛЧА: ключ SET NULL, отказать нечему. Ровно тот
//     дефект, который на этой волне уже находился дважды — у ролей и у стилей.

// addProjectTaskFixture creates one kanban card through the real store path (so the «only a
// project» check is actually exercised) and registers its removal.
func addProjectTaskFixture(ctx context.Context, t *testing.T, s *MYSQLStore, title string, projectID int) int {
	t.Helper()
	ins := entity.TaskInsert{Title: title, Priority: entity.TaskPriorityUnknown}
	if projectID > 0 {
		ins.ProjectTopicId = sql.NullInt32{Int32: int32(projectID), Valid: true}
	}
	id, err := s.Tasks().AddTask(ctx, &entity.Task{
		TaskInsert: ins,
		Board:      entity.TaskBoardContent,
		Status:     entity.TaskStatusTodo,
		CreatedBy:  "test-project-tasks",
	})
	require.NoError(t, err)
	t.Cleanup(func() { _, _ = testDB.Exec(`DELETE FROM task WHERE id = ?`, id) })
	return id
}

func TestTaskProjectTopicLink(t *testing.T) {
	filesGuard(t)

	ctx, cancel := context.WithTimeout(context.Background(), 240*time.Second)
	defer cancel()

	s, err := NewForTest(ctx, *testCfg)
	require.NoError(t, err)

	admin := superCtx(ctx)

	projectOf := func(t *testing.T, taskID int) (int, bool) {
		t.Helper()
		var v sql.NullInt32
		require.NoError(t, testDB.QueryRowContext(ctx,
			`SELECT project_topic_id FROM task WHERE id = ?`, taskID).Scan(&v))
		return int(v.Int32), v.Valid
	}
	taskIDs := func(tasks []entity.Task) []int {
		out := make([]int, 0, len(tasks))
		for _, tk := range tasks {
			out = append(out, tk.Id)
		}
		return out
	}

	t.Run("ссылка ставится и снимается", func(t *testing.T) {
		// Круговой рейс целиком: запись — чтение — снятие. Снятие проверяется отдельно от записи
		// потому, что UpdateTask ЗАМЕЩАЕТ содержимое, и пропущенная колонка в UPDATE выглядела бы
		// как «ссылка не снимается», а не как «ссылка не пишется».
		project := insertProjectTopicFixture(ctx, t, s, "test-pt-shoot")
		task := addProjectTaskFixture(ctx, t, s, "отретушировать отобранное", project)

		got, ok := projectOf(t, task)
		require.True(t, ok, "ссылка обязана доехать до колонки, а не остаться в структуре")
		require.Equal(t, project, got)

		read, err := s.Tasks().GetTaskById(ctx, task)
		require.NoError(t, err)
		require.True(t, read.ProjectTopicId.Valid, "и обязана вернуться чтением: без этого форма затрёт её первым же сохранением")
		require.Equal(t, int32(project), read.ProjectTopicId.Int32)

		require.NoError(t, s.Tasks().UpdateTask(ctx, task, &entity.TaskInsert{
			Title: "отретушировать отобранное", Priority: entity.TaskPriorityUnknown,
		}))
		_, ok = projectOf(t, task)
		require.False(t, ok, "пустая ссылка в присланном содержимом означает «проекта больше не указано»")
	})

	t.Run("ссылка на тему, которая не проект, отклоняется внятной ошибкой", func(t *testing.T) {
		// НЕ ОТКАЗОМ ВНЕШНЕГО КЛЮЧА: тема СУЩЕСТВУЕТ, она просто ярлык, и ключ на это не
		// срабатывает вовсе. Значит проверка либо стоит в сторе, либо не стоит нигде.
		plain := insertFileTopicFixture(ctx, t, "test-pt-plain")

		_, err := s.Tasks().AddTask(ctx, &entity.Task{
			TaskInsert: entity.TaskInsert{
				Title:          "не должна завестись",
				Priority:       entity.TaskPriorityUnknown,
				ProjectTopicId: sql.NullInt32{Int32: int32(plain), Valid: true},
			},
			Board: entity.TaskBoardContent, Status: entity.TaskStatusTodo,
			CreatedBy: "test-project-tasks",
		})
		require.ErrorIs(t, err, entity.ErrTaskNeedsProjectTopic,
			"хендлер переводит эту ошибку в InvalidArgument с фразой; 1452 с именем ключа сказал бы человеку не то и не о том")

		left, err := storeScalarInt(ctx, `SELECT COUNT(*) FROM task WHERE project_topic_id = ?`, plain)
		require.NoError(t, err)
		require.Zero(t, left, "отказ обязан быть ЦЕЛЫМ: транзакция откатывается вместе с карточкой")

		// То же на правке уже существующей карточки: второй путь записи не имеет права быть
		// дырой в том же правиле.
		project := insertProjectTopicFixture(ctx, t, s, "test-pt-plain-ok")
		task := addProjectTaskFixture(ctx, t, s, "живая карточка", project)
		err = s.Tasks().UpdateTask(ctx, task, &entity.TaskInsert{
			Title: "живая карточка", Priority: entity.TaskPriorityUnknown,
			ProjectTopicId: sql.NullInt32{Int32: int32(plain), Valid: true},
		})
		require.ErrorIs(t, err, entity.ErrTaskNeedsProjectTopic)

		got, ok := projectOf(t, task)
		require.True(t, ok, "прежняя ссылка обязана уцелеть: отвергнутая правка не имеет права отнять то, что было")
		require.Equal(t, project, got)
	})

	t.Run("несуществующая тема отвечает внешним ключом, а не фразой про ярлык", func(t *testing.T) {
		// РАЗНИЦА СУЩЕСТВЕННА, И ОНА ЗАКРЕПЛЕНА НАРОЧНО. Опечатка в id — одна и та же ошибка для
		// всех восьми глубоких ссылок, и отвечать на неё она обязана так же, как остальные семь.
		// А вернуть отсюда sql.ErrNoRows было бы прямым дефектом: в UpdateTask он уже значит
		// «задачи нет», и хендлер ответил бы «task not found» на живую задачу.
		missing, err := storeScalarInt(ctx, `SELECT COALESCE(MAX(id), 0) + 1000 FROM file_topic`)
		require.NoError(t, err)

		_, err = s.Tasks().AddTask(ctx, &entity.Task{
			TaskInsert: entity.TaskInsert{
				Title:          "тоже не должна завестись",
				Priority:       entity.TaskPriorityUnknown,
				ProjectTopicId: sql.NullInt32{Int32: int32(missing), Valid: true},
			},
			Board: entity.TaskBoardContent, Status: entity.TaskStatusTodo,
			CreatedBy: "test-project-tasks",
		})
		require.Error(t, err)
		require.NotErrorIs(t, err, entity.ErrTaskNeedsProjectTopic,
			"«тема не проект» здесь было бы враньём: темы нет вовсе")
		require.NotErrorIs(t, err, sql.ErrNoRows,
			"а sql.ErrNoRows хендлер прочёл бы как «задачи нет» и ответил бы NotFound на опечатку в id темы")
		require.True(t, s.IsErrForeignKeyViolation(err),
			"отвечает внешний ключ — ровно как на остальных семи ссылках")
	})

	t.Run("архивный проект ссылку ПРИНИМАЕТ", func(t *testing.T) {
		// АРХИВ — ЭТО КОРОБКА, ЕЁ ЗАКРЫВАЮТ, А НЕ ЗАПРЕЩАЮТ. Так же решено для стилей (0321) и
		// противоположно роли, чей архив выводит СЛОВО из оборота. Съёмку архивируют, когда она
		// отснята, а хвост задач по ней («доотдать ретушь») — нормальная жизнь, и требовать
		// разархивации ради записи правды значило бы сделать архив препятствием.
		project := insertProjectTopicFixture(ctx, t, s, "test-pt-archived")
		_, err := s.Files().UpdateTopicMeta(admin, entity.FileTopicMetaUpdate{
			TopicId: project, Kind: entity.FileTopicKindProject, Archived: true,
		})
		require.NoError(t, err)

		task := addProjectTaskFixture(ctx, t, s, "доотдать ретушь", project)
		got, ok := projectOf(t, task)
		require.True(t, ok)
		require.Equal(t, project, got)
	})

	t.Run("понижение проекта обнуляет ссылки задач и возвращает ИХ число", func(t *testing.T) {
		// ТЕСТ НА ПОДМЕНУ. Три проектных свойства — роли (0320), стили (0321), задачи (0322) — и
		// три числа в одном ответе. Количества РАЗНЫЕ нарочно: на равных подмена одного поля
		// другим прошла бы незамеченной, а именно она и есть самая правдоподобная ошибка в коде,
		// который перекладывает три int'а.
		project := insertProjectTopicFixture(ctx, t, s, "test-pt-demote")
		role := insertFileRoleFixture(ctx, t, "test-pt-demote-role")

		// 1 роль.
		file := insertLibraryFileFixture(ctx, t, "pt-demote.pdf", 10, "pasha")
		_, err := s.Files().SetFileRoles(admin, []int{file}, project, role)
		require.NoError(t, err)

		// 2 стиля.
		require.NoError(t, s.Files().LinkTopicStyle(admin, project, insertTechCardFixture(ctx, t, "test-pt-demote-s1")))
		require.NoError(t, s.Files().LinkTopicStyle(admin, project, insertTechCardFixture(ctx, t, "test-pt-demote-s2")))

		// 3 задачи.
		tasks := []int{
			addProjectTaskFixture(ctx, t, s, "снять", project),
			addProjectTaskFixture(ctx, t, s, "отобрать", project),
			addProjectTaskFixture(ctx, t, s, "отретушировать", project),
		}

		res, err := s.Files().UpdateTopicMeta(admin, entity.FileTopicMetaUpdate{
			TopicId: project, Kind: entity.FileTopicKindPlain,
		})
		require.NoError(t, err)
		require.Equal(t, 1, res.ClearedRoles, "роли — в ClearedRoles")
		require.Equal(t, 2, res.ClearedStyles, "стили — в ClearedStyles")
		require.Equal(t, 3, res.ClearedTasks, "а задачи — в ClearedTasks, и перепутать их местами нечем только потому, что числа разные")

		left, err := storeScalarInt(ctx, `SELECT COUNT(*) FROM task WHERE project_topic_id = ?`, project)
		require.NoError(t, err)
		require.Zero(t, left, "понижение обязано СНЯТЬ ссылки, а не только пересчитать их")

		// КАРТОЧКИ ЖИВЫ. Понижение снимает контекст, а не работу: уносить чей-то список дел вместе
		// с типом темы было бы куда худшим ответом, чем потерянная ссылка.
		alive, err := storeScalarInt(ctx, `SELECT COUNT(*) FROM task WHERE id IN (?, ?, ?)`,
			tasks[0], tasks[1], tasks[2])
		require.NoError(t, err)
		require.Equal(t, 3, alive)
	})

	t.Run("удаление темы обнуляет ссылки, возвращает число и оставляет задачи живыми", func(t *testing.T) {
		// Ключ у задачи SET NULL, а не каскадный, и это ровно та разница, ради которой число
		// возвращается отдельно от стилей: у вещи связь ИСЧЕЗАЕТ, у карточки — обнуляется.
		project := insertProjectTopicFixture(ctx, t, s, "test-pt-delete")
		require.NoError(t, s.Files().LinkTopicStyle(admin, project, insertTechCardFixture(ctx, t, "test-pt-delete-s")))
		first := addProjectTaskFixture(ctx, t, s, "первая", project)
		second := addProjectTaskFixture(ctx, t, s, "вторая", project)

		res, err := s.Files().DeleteTopic(admin, project)
		require.NoError(t, err)
		require.Equal(t, 1, res.UnlinkedStyles)
		require.Equal(t, 2, res.UnlinkedTasks,
			"удаление обязано НАЗВАТЬ, у скольких карточек пропал контекст: молчание развело бы это событие с обнаружением на месяц")

		for _, id := range []int{first, second} {
			_, ok := projectOf(t, id)
			require.False(t, ok, "SET NULL обязан обнулить ссылку")
		}
		alive, err := storeScalarInt(ctx, `SELECT COUNT(*) FROM task WHERE id IN (?, ?)`, first, second)
		require.NoError(t, err)
		require.Equal(t, 2, alive,
			"и обязан ОСТАВИТЬ карточки: они про работу, а не про съёмку, и каскад стёр бы чей-то список дел заодно с ярлыком")
	})

	t.Run("слияние тем переносит ссылки задач на цель", func(t *testing.T) {
		// БЕЗ ПЕРЕНОСА ССЫЛКИ ОБНУЛИЛИСЬ БЫ МОЛЧА: ключ стоит с ON DELETE SET NULL, поэтому DELETE
		// темы-источника внутри слияния увёл бы карточки в «проекта не указано» без единого
		// отказа. «Две съёмки оказались одной» — штатный сценарий, и он стирал бы контекст у всей
		// работы исходного проекта. Этот дефект на волне уже находился дважды — у ролей и у стилей.
		source := insertProjectTopicFixture(ctx, t, s, "test-pt-merge-src")
		target := insertProjectTopicFixture(ctx, t, s, "test-pt-merge-dst")
		moved := addProjectTaskFixture(ctx, t, s, "переезжает", source)
		stayed := addProjectTaskFixture(ctx, t, s, "уже в цели", target)

		_, err := s.Files().MergeTopics(admin, source, target)
		require.NoError(t, err)

		got, ok := projectOf(t, moved)
		require.True(t, ok, "ссылка обязана ПЕРЕЕХАТЬ, а не обнулиться")
		require.Equal(t, target, got)
		got, ok = projectOf(t, stayed)
		require.True(t, ok)
		require.Equal(t, target, got, "а карточка, уже стоявшая в цели, не имеет права никуда деться")

		tasks, _, err := s.Tasks().ListTasks(ctx, entity.TaskListFilter{ProjectTopicId: target})
		require.NoError(t, err)
		require.ElementsMatch(t, []int{moved, stayed}, taskIDs(tasks))
	})

	t.Run("фильтр по проекту отдаёт РОВНО задачи этого проекта", func(t *testing.T) {
		// Обратный вопрос фазы. Фильтр, а не отдельный RPC: это тот же список задач, просто
		// суженный, и он ходит под тем же rd(tasks), что и доска — второй путь к тем же данным
		// завёл бы вторые права, которым нечем помешать разойтись.
		mine := insertProjectTopicFixture(ctx, t, s, "test-pt-filter-mine")
		other := insertProjectTopicFixture(ctx, t, s, "test-pt-filter-other")

		a := addProjectTaskFixture(ctx, t, s, "моя первая", mine)
		b := addProjectTaskFixture(ctx, t, s, "моя вторая", mine)
		addProjectTaskFixture(ctx, t, s, "чужая", other)
		// Карточка БЕЗ проекта — контрольная: фильтр, потерявший условие, собрал бы и её.
		addProjectTaskFixture(ctx, t, s, "ничья", 0)

		tasks, total, err := s.Tasks().ListTasks(ctx, entity.TaskListFilter{ProjectTopicId: mine})
		require.NoError(t, err)
		require.ElementsMatch(t, []int{a, b}, taskIDs(tasks))
		require.Equal(t, 2, total, "total обязан считаться под тем же условием, что и выборка: иначе пагинация обещает страницы, которых нет")

		// Несуществующий проект — ПУСТОЙ список, а не отличимый отказ: различимость сделала бы
		// чтение задач оракулом по темам, ровно как у семи соседних фильтров.
		missing, err := storeScalarInt(ctx, `SELECT COALESCE(MAX(id), 0) + 1000 FROM file_topic`)
		require.NoError(t, err)
		tasks, total, err = s.Tasks().ListTasks(ctx, entity.TaskListFilter{ProjectTopicId: missing})
		require.NoError(t, err)
		require.Empty(t, tasks)
		require.Zero(t, total)

		// АРХИВНАЯ КАРТОЧКА ПО-ПРЕЖНЕМУ СКРЫТА ПО УМОЛЧАНИЮ. Фильтр по проекту — сужение, а не
		// новый режим выдачи, и он не имеет права протащить в ответ то, что доска прячет.
		require.NoError(t, s.Tasks().ArchiveTask(ctx, b))
		tasks, _, err = s.Tasks().ListTasks(ctx, entity.TaskListFilter{ProjectTopicId: mine})
		require.NoError(t, err)
		require.Equal(t, []int{a}, taskIDs(tasks))
		tasks, _, err = s.Tasks().ListTasks(ctx, entity.TaskListFilter{ProjectTopicId: mine, IncludeArchived: true})
		require.NoError(t, err)
		require.ElementsMatch(t, []int{a, b}, taskIDs(tasks))
	})
}

// TestTaskProjectTopicMigrationIsRerunnable re-applies 0322 over a schema it has ALREADY been
// applied to — exactly what a mid-file failure leaves behind: MySQL auto-commits DDL, so the
// half-applied schema keeps no gorp_migrations row and the next boot runs the file again from the
// top. A migration that cannot survive that does not fail in a test — it stops the process from
// starting, and DO then rolls the deploy back to an image that answers /readyz with 200 from the
// previous build.
//
// ЭТО НЕ ДУБЛИКАТ migrationlint: тот читает ТЕКСТ и умеет ровно два правила, а голый ALTER
// пропускает вовсе. Здесь файл применяется второй раз к реальной схеме, и проверяется, что повтор
// no-op ПО СУЩЕСТВУ: уже стоящая ссылка цела, индекс на колонке РОВНО ОДИН, а внешний ключ на месте
// и по-прежнему SET NULL.
//
// Про «ровно один индекс» стоит сказать прямо, чтобы утверждение не читалось сильнее, чем оно есть:
// перестановка шагов 2 и 3 в самой миграции его НЕ ломает — MySQL 8 убирает свой служебный индекс,
// когда появляется равнозначный пользовательский (замерено; см. шапку 0322). Ловит эта проверка
// другое: правку, которая заведёт ВТОРОЙ явный индекс по той же колонке, и незамеченный дрейф
// поведения самой MySQL.
func TestTaskProjectTopicMigrationIsRerunnable(t *testing.T) {
	filesGuard(t)

	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	s, err := NewForTest(ctx, *testCfg)
	require.NoError(t, err)

	project := insertProjectTopicFixture(ctx, t, s, "test-pt-rerun")
	task := addProjectTaskFixture(ctx, t, s, "переживает повтор", project)

	for i := range 2 {
		_, err := testDB.ExecContext(ctx,
			`DELETE FROM gorp_migrations WHERE id = '0322_task_project_topic.sql'`)
		require.NoError(t, err)
		require.NoError(t, Migrate(testDB), "re-applying 0322 over an applied schema (pass %d)", i+1)

		kept, err := storeScalarInt(ctx,
			`SELECT COUNT(*) FROM task WHERE id = ? AND project_topic_id = ?`, task, project)
		require.NoError(t, err)
		require.Equal(t, 1, kept,
			"повтор обязан быть no-op ПО СУЩЕСТВУ, а не только по коду возврата")

		idx, err := storeScalarInt(ctx, `
			SELECT COUNT(DISTINCT INDEX_NAME) FROM information_schema.STATISTICS
			WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'task'
			  AND COLUMN_NAME = 'project_topic_id'`)
		require.NoError(t, err)
		require.Equal(t, 1, idx,
			"индекс на колонке обязан остаться ОДНИМ: ловится правка, заводящая ВТОРОЙ явный индекс по той же колонке, и дрейф поведения самой MySQL — а НЕ перестановка шагов миграции, которая его не ломает (см. doc-комментарий выше и шапку 0322)")

		fks, err := storeScalarInt(ctx, `
			SELECT COUNT(*) FROM information_schema.REFERENTIAL_CONSTRAINTS
			WHERE CONSTRAINT_SCHEMA = DATABASE() AND CONSTRAINT_NAME = 'fk_task_project'
			  AND DELETE_RULE = 'SET NULL'`)
		require.NoError(t, err)
		require.Equal(t, 1, fks, "и ключ обязан остаться SET NULL: каскад унёс бы карточки вместе с темой")
	}
}
