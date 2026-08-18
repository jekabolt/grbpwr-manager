package store

import (
	"context"
	"database/sql"
	"fmt"
	"testing"
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/stretchr/testify/require"
)

// ГРУППИРОВКА «ПРОЕКТ × РОЛЬ» — ПРИЁМКА МОДЕЛИ.
//
// Главное утверждение здесь одно, и ради него написано всё остальное:
//
//	ФАЙЛ, ЛЕЖАЩИЙ В ДВУХ ПРОЕКТАХ С РАЗНЫМИ РОЛЯМИ, НЕ ДОЛЖЕН НАХОДИТЬСЯ ПО ПЕРЕКРЁСТНОЙ ПАРЕ.
//
// Снимок, который в съёмке «отобранное», а в лукбуке «референс», при плоских метках нёс бы на себе
// набор {съёмка, лукбук, отобранное, референс} — и запрос «съёмка × референс» находил бы его.
// Ошибка МОЛЧАЛИВАЯ: выдача выглядит правдоподобной, и проверить её нечем ничем, кроме такого
// теста. Роль на строке связи выражает пару точно, и перекрёстный запрос не находит ничего.
//
// Тест обязан быть контейнерным: всё различие живёт в сгенерированном SQL. Один EXISTS с двумя
// условиями и два независимых EXISTS — оба рабочие запросы, оба возвращают строки, и неправильный
// не выглядит сломанным — он просто отвечает на другой вопрос.

// insertProjectTopicFixture creates a topic and promotes it to a PROJECT through the
// real store path, so the test exercises the same promotion the topics screen does.
func insertProjectTopicFixture(ctx context.Context, t *testing.T, s *MYSQLStore, prefix string) int {
	t.Helper()
	id := insertFileTopicFixture(ctx, t, prefix)
	_, err := s.Files().UpdateTopicMeta(superCtx(ctx), entity.FileTopicMetaUpdate{
		TopicId: id, Kind: entity.FileTopicKindProject,
	})
	require.NoError(t, err)
	return id
}

// insertFileRoleFixture creates a uniquely named role IN A PROJECT and registers its
// removal. The cleanup NULLs the links first: the foreign key on the role has no
// cascade, so a role still carried by a link row cannot be deleted at all.
//
// ПРОЕКТ У ФИКСТУРЫ ОБЯЗАТЕЛЕН С 0323: роль принадлежит проекту, и роль-сирота с NULL-владельцем
// не проставляется ни на одну строку связи — её отвергает и стор, и составной внешний ключ.
func insertFileRoleFixture(ctx context.Context, t *testing.T, projectTopicID int, prefix string) int {
	t.Helper()
	var name string
	require.NoError(t, testDB.QueryRowContext(ctx, `SELECT CONCAT(?, '-', UUID_SHORT())`, prefix).Scan(&name))
	return insertNamedFileRoleFixture(ctx, t, projectTopicID, name)
}

// insertNamedFileRoleFixture creates a role with an EXACT name — the only way to put the SAME
// word into two projects, which is the whole point of per-project roles.
func insertNamedFileRoleFixture(ctx context.Context, t *testing.T, projectTopicID int, name string) int {
	t.Helper()
	res, err := testDB.ExecContext(ctx,
		`INSERT INTO file_role (project_topic_id, name, sort_order) VALUES (?, ?, 0)`, projectTopicID, name)
	require.NoError(t, err)
	id, err := res.LastInsertId()
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = testDB.Exec(`UPDATE library_file_topic SET role_id = NULL WHERE role_id = ?`, id)
		_, _ = testDB.Exec(`DELETE FROM file_role WHERE id = ?`, id)
	})
	return int(id)
}

func TestLibraryFilesProjectRoleGrouping(t *testing.T) {
	filesGuard(t)

	ctx, cancel := context.WithTimeout(context.Background(), 240*time.Second)
	defer cancel()

	s, err := NewForTest(ctx, *testCfg)
	require.NoError(t, err)

	admin := superCtx(ctx)

	_, pasha := insertAdminFixture(ctx, t, "test-gr-pasha")
	_, stranger := insertAdminFixture(ctx, t, "test-gr-stranger")

	shoot := insertProjectTopicFixture(ctx, t, s, "test-gr-shoot")
	lookbook := insertProjectTopicFixture(ctx, t, s, "test-gr-lookbook")

	// РОЛЬ ЖИВЁТ В ПРОЕКТЕ, ПОЭТОМУ «референс» ЗДЕСЬ ДВЕ СТРОКИ С ОДНИМ ИМЕНЕМ. Ровно это и
	// заказывали: «исходники» съёмки и «исходники» лукбука — разные сущности. Одинаковое имя взято
	// намеренно — на нём держится подтест про сквозной поиск по слову, единственный оставшийся
	// сквозной инструмент.
	picked := insertFileRoleFixture(ctx, t, shoot, "test-gr-picked")
	reference := insertFileRoleFixture(ctx, t, shoot, "test-gr-reference")
	var referenceName string
	require.NoError(t, testDB.QueryRowContext(ctx,
		`SELECT name FROM file_role WHERE id = ?`, reference).Scan(&referenceName))
	referenceLB := insertNamedFileRoleFixture(ctx, t, lookbook, referenceName)

	sha := fmt.Sprintf("%064d", time.Now().UnixNano()%1e10)

	// reused — тот самый переиспользованный снимок: в съёмке отобранный, в лукбуке референс.
	// Контрольные файлы нужны, чтобы «нашлось ровно одно» что-то значило: без них тот же ответ
	// давал бы фильтр, не отфильтровавший вообще ничего.
	reused := insertLibraryFileWithSha(ctx, t, "gr-reused.pdf", pasha, sha)
	shootOnly := insertLibraryFileWithSha(ctx, t, "gr-shoot-only.pdf", pasha, sha)
	bare := insertLibraryFileWithSha(ctx, t, "gr-bare.pdf", pasha, sha)

	setRole := func(t *testing.T, fileID, topicID, roleID int) {
		t.Helper()
		_, err := s.Files().SetFileRoles(admin, []int{fileID}, topicID, roleID)
		require.NoError(t, err)
	}
	setRole(t, reused, shoot, picked)
	setRole(t, reused, lookbook, referenceLB)
	setRole(t, shootOnly, shoot, reference)
	// bare лежит в съёмке БЕЗ роли — штатный приёмник «свалил и разберу потом».
	_, err = s.Files().SetFileRoles(admin, []int{bare}, shoot, 0)
	require.NoError(t, err)

	// list снимает И страницу, И счётчик одним вызовом: расхождение «N из M» иначе прошло бы мимо
	// любого утверждения о составе.
	list := func(ctx context.Context, t *testing.T, f entity.LibraryFileListFilter) ([]int, int) {
		t.Helper()
		if f.Limit == 0 {
			f.Limit = 1000
		}
		files, total, err := s.Files().ListFiles(ctx, f)
		require.NoError(t, err)
		return libraryFileIDs(files), total
	}
	requireSet := func(t *testing.T, want []int, ids []int, total int, msg string) {
		t.Helper()
		require.ElementsMatch(t, want, ids, msg)
		require.Equal(t, len(want), total,
			"total обязан считаться ТЕМ ЖЕ условием, что и страница: иначе «N из M» врёт, и человек листает за числом, которого нет")
	}

	t.Run("перекрёстная пара НЕ находит переиспользованный файл", func(t *testing.T) {
		// ЭТО ГЛАВНОЕ УТВЕРЖДЕНИЕ ВСЕЙ ФАЗЫ. Оно краснеет ровно тогда, когда модель возвращается
		// к плоской: два независимых EXISTS (один про проект, другой про роль) — и «съёмка ×
		// референс» находит reused, потому что референсом он был в лукбуке.
		ids, total := list(admin, t, entity.LibraryFileListFilter{
			ProjectTopicId: shoot, RoleId: reference,
		})
		requireSet(t, []int{shootOnly}, ids, total,
			"«съёмка × референс» обязана вернуть только тот файл, который является референсом ИМЕННО В СЪЁМКЕ")
		require.NotContains(t, ids, reused,
			"файл, который референс в ЛУКБУКЕ, не имеет права находиться по паре «съёмка × референс»: это и есть тот молчаливый дефект, ради которого роль переехала на строку связи")

		ids, total = list(admin, t, entity.LibraryFileListFilter{
			ProjectTopicId: lookbook, RoleId: picked,
		})
		requireSet(t, []int{}, ids, total,
			"обратная перекрёстная пара «лукбук × отобранное» тоже обязана быть пустой")
	})

	t.Run("обе настоящие пары находятся", func(t *testing.T) {
		// Без этого подтеста «перекрёстная пара пуста» доказывало бы только, что фильтр сломан.
		ids, total := list(admin, t, entity.LibraryFileListFilter{
			ProjectTopicId: shoot, RoleId: picked,
		})
		requireSet(t, []int{reused}, ids, total, "«съёмка × отобранное» обязана найти reused")

		ids, total = list(admin, t, entity.LibraryFileListFilter{
			ProjectTopicId: lookbook, RoleId: referenceLB,
		})
		requireSet(t, []int{reused}, ids, total, "«лукбук × референс» обязана найти reused")
	})

	t.Run("фильтр по роли не выходит за её проект, весь проект — целиком", func(t *testing.T) {
		// ЗДЕСЬ 0323 МЕНЯЕТ ОТВЕТ, И ЭТО НЕ РЕГРЕССИЯ, А ЗАКАЗ. До неё «референс» был одной
		// сущностью на всю библиотеку, и фильтр по нему без проекта собирал файлы всех съёмок.
		// Теперь роль принадлежит проекту: id роли съёмки не встречается ни на одной строке
		// лукбука по построению (составной внешний ключ), поэтому старый адрес `?frole=N` без
		// проекта точен сам по себе. Сквозной вопрос переехал в поиск по слову — подтест ниже.
		ids, total := list(admin, t, entity.LibraryFileListFilter{RoleId: reference})
		requireSet(t, []int{shootOnly}, ids, total,
			"роль съёмки обязана отдавать только файлы съёмки: одноимённая роль лукбука — другая сущность")
		require.NotContains(t, ids, reused,
			"иначе роль снова стала бы сквозной меткой, от которой уходила вся волна")

		ids, total = list(admin, t, entity.LibraryFileListFilter{ProjectTopicId: shoot})
		requireSet(t, []int{reused, shootOnly, bare}, ids, total,
			"проект без роли — это ВЕСЬ проект, включая файлы без роли")
	})

	t.Run("«без роли» — приёмник внутри проекта", func(t *testing.T) {
		ids, total := list(admin, t, entity.LibraryFileListFilter{
			ProjectTopicId: shoot, WithoutRole: true,
		})
		requireSet(t, []int{bare}, ids, total,
			"«в проекте, без роли» обязано отдать ровно приёмник")
	})

	t.Run("невозможные комбинации отвергаются, а не игнорируются", func(t *testing.T) {
		// Молча отброшенное плечо показало бы БОЛЬШЕ, чем просили, а лишняя строка здесь — это
		// чьё-то говорящее имя файла.
		_, _, err := s.Files().ListFiles(admin, entity.LibraryFileListFilter{WithoutRole: true})
		require.ErrorIs(t, err, entity.ErrLibraryFilterInvalid,
			"«без роли» без проекта означало бы почти всю библиотеку")

		_, _, err = s.Files().ListFiles(admin, entity.LibraryFileListFilter{
			Untopiced: true, ProjectTopicId: shoot,
		})
		require.ErrorIs(t, err, entity.ErrLibraryFilterInvalid,
			"файл в проекте несёт строку связи, значит в «разобрать» его нет по построению: пустая выдача читалась бы как «в проекте ничего нет»")

		_, _, err = s.Files().ListFiles(admin, entity.LibraryFileListFilter{
			Untopiced: true, RoleId: picked,
		})
		require.ErrorIs(t, err, entity.ErrLibraryFilterInvalid, "то же самое для роли")
	})

	t.Run("поиск находит по имени роли", func(t *testing.T) {
		// Роль печатается на плитке рядом с темой и выглядит таким же ярлыком. До 0320 такой
		// ярлык был темой и находился поиском даром; переезд роли в свою таблицу забрал бы это
		// свойство МОЛЧА — человек набрал бы «референс» и получил пустой экран.
		// С 0323 у этого подтеста появился ВТОРОЙ смысл, и он важнее первого: поиск по слову —
		// ЕДИНСТВЕННЫЙ оставшийся сквозной инструмент. «Все референсы по всем съёмкам» больше не
		// вопрос об одной сущности; он задаётся словом и собирает файлы всех проектов, где роль
		// ТАК НАЗВАНА. Здесь одно имя носят две разные роли двух проектов — и выдача обязана
		// содержать файлы обоих.
		ids, total := list(admin, t, entity.LibraryFileListFilter{Search: referenceName})
		requireSet(t, []int{reused, shootOnly}, ids, total,
			"строка поиска обязана находить файл по имени его роли — иначе ярлык, который человек видит на плитке, в поиске не работает, и сквозного вопроса не остаётся вовсе")
	})

	t.Run("файл несёт ПАРЫ, а не плоский список ролей", func(t *testing.T) {
		f, err := s.Files().GetFileById(admin, reused)
		require.NoError(t, err)
		require.Len(t, f.Roles, 2, "у переиспользованного файла две пары: по одной на проект")
		byProject := map[int]int{}
		for _, r := range f.Roles {
			byProject[r.ProjectTopicId] = r.RoleId
			require.NotEmpty(t, r.RoleName, "имя роли обязано приезжать вместе с id — плитка печатает его, а не число")
			require.NotEmpty(t, r.ProjectTopicName, "имя проекта тоже: иначе плитке нужен второй запрос в рельс тем")
		}
		require.Equal(t, picked, byProject[shoot])
		require.Equal(t, referenceLB, byProject[lookbook])

		// Файл БЕЗ роли не имеет права приносить пустую пару: «роль ничего» не значит ничего.
		b, err := s.Files().GetFileById(admin, bare)
		require.NoError(t, err)
		require.Empty(t, b.Roles, "у файла без роли список пар пуст")
		require.Len(t, b.Topics, 1, "но в проекте он лежит — темы приезжают как раньше")
	})

	t.Run("роль переживает переименование файла", func(t *testing.T) {
		// ПОЧИНКА T-0.2, ПЕРВАЯ ПОЛОВИНА. UpdateFile заменял темы через `DELETE ... WHERE
		// file_id` + вставку заново, и с появлением role_id на строке связи это стирало бы РОЛИ
		// молча, при любом сохранении карточки. Симптом «роли иногда пропадают сами» по коду не
		// ищется вовсе — только таким тестом.
		require.NoError(t, s.Files().UpdateFile(admin, reused, "gr-reused-renamed.pdf",
			[]int{shoot, lookbook}, nil))

		f, err := s.Files().GetFileById(admin, reused)
		require.NoError(t, err)
		require.Equal(t, "gr-reused-renamed.pdf", f.FileName, "фикстура: переименование обязано было пройти")
		require.Len(t, f.Roles, 2,
			"обе роли обязаны пережить переименование: замена тем считается РАЗНИЦЕЙ, уцелевшие строки не трогаются")

		ids, _ := list(admin, t, entity.LibraryFileListFilter{ProjectTopicId: shoot, RoleId: picked})
		require.Contains(t, ids, reused, "и пара обязана продолжать находиться")
	})

	t.Run("снятая тема уносит свою роль, оставшаяся — нет", func(t *testing.T) {
		// Разница обязана быть НАСТОЯЩЕЙ разницей, а не «ничего не удаляем». Снятие чипа проекта
		// в карточке законно удаляет строку связи вместе с ролью — и это правильное поведение,
		// но человек должен узнать о нём из интерфейса, а не опытным путём.
		require.NoError(t, s.Files().UpdateFile(admin, reused, "gr-reused-renamed.pdf",
			[]int{shoot}, nil))
		f, err := s.Files().GetFileById(admin, reused)
		require.NoError(t, err)
		require.Len(t, f.Topics, 1, "лукбук обязан был отцепиться")
		require.Len(t, f.Roles, 1, "вместе со своей ролью")
		require.Equal(t, picked, f.Roles[0].RoleId, "а роль в съёмке — остаться нетронутой")

		// Возвращаем состояние фикстуры для последующих подтестов.
		require.NoError(t, s.Files().UpdateFile(admin, reused, "gr-reused.pdf",
			[]int{shoot, lookbook}, nil))
		setRole(t, reused, lookbook, referenceLB)
	})

	t.Run("роль переживает слияние проектов", func(t *testing.T) {
		// ПОЧИНКА T-0.2, ВТОРАЯ ПОЛОВИНА. Проекция `INSERT IGNORE ... SELECT file_id, :target`
		// без role_id перевесила бы файлы на целевой проект, обнулив то, чем они в нём являются,
		// — и восстановить это было бы нечем: исходный проект в той же транзакции исчезает.
		source := insertProjectTopicFixture(ctx, t, s, "test-gr-merge-src")
		target := insertProjectTopicFixture(ctx, t, s, "test-gr-merge-dst")
		srcRole := insertFileRoleFixture(ctx, t, source, "test-gr-merge-role")
		var srcRoleName string
		require.NoError(t, testDB.QueryRowContext(ctx,
			`SELECT name FROM file_role WHERE id = ?`, srcRole).Scan(&srcRoleName))
		carried := insertLibraryFileWithSha(ctx, t, "gr-merge-carried.pdf", pasha, sha)
		setRole(t, carried, source, srcRole)

		moved, err := s.Files().MergeTopics(admin, source, target)
		require.NoError(t, err)
		require.Equal(t, 1, moved)

		// РОЛЬ ИЩЕТСЯ ПО ИМЕНИ В СЛОВАРЕ ЦЕЛИ, а не по id источника, и это прямое следствие
		// 0323: id роли источника в целевом проекте не значит ничего, а сама строка словаря
		// уходит каскадом вместе с исходным проектом.
		var movedRole int
		require.NoError(t, testDB.QueryRowContext(ctx,
			`SELECT id FROM file_role WHERE project_topic_id = ? AND name = ?`, target, srcRoleName).Scan(&movedRole),
			"словарь источника обязан доехать в цель по именам — иначе разметку некуда переносить")
		require.NotEqual(t, srcRole, movedRole, "это ДРУГАЯ строка: роль принадлежит проекту и между проектами не переезжает")

		ids, total := list(admin, t, entity.LibraryFileListFilter{
			ProjectTopicId: target, RoleId: movedRole,
		})
		requireSet(t, []int{carried}, ids, total,
			"роль обязана переехать вместе со связью: иначе слияние двух съёмок стирает всю разметку целевого проекта")
	})

	t.Run("при столкновении побеждает роль целевого проекта", func(t *testing.T) {
		// Файл уже лежал в цели со своей ролью — INSERT IGNORE не трогает существующую строку.
		// Выбор осознанный (целевой проект переживает слияние, его разметка старше приезжей), и
		// сказать о нём надо в диалоге слияния, а не оставить человеку выяснять опытом.
		source := insertProjectTopicFixture(ctx, t, s, "test-gr-clash-src")
		target := insertProjectTopicFixture(ctx, t, s, "test-gr-clash-dst")
		srcRole := insertFileRoleFixture(ctx, t, source, "test-gr-clash-src-role")
		dstRole := insertFileRoleFixture(ctx, t, target, "test-gr-clash-dst-role")
		clashing := insertLibraryFileWithSha(ctx, t, "gr-clash.pdf", pasha, sha)
		setRole(t, clashing, source, srcRole)
		setRole(t, clashing, target, dstRole)

		_, err := s.Files().MergeTopics(admin, source, target)
		require.NoError(t, err)

		ids, _ := list(admin, t, entity.LibraryFileListFilter{ProjectTopicId: target, RoleId: dstRole})
		require.Contains(t, ids, clashing, "роль, стоявшая в цели, обязана уцелеть")
		require.NotContains(t, ids, reused, "фикстура: в цели лежит только этот файл")
	})

	t.Run("слияние между типами отказывает", func(t *testing.T) {
		// Слить проект в обычный ярлык — бессмыслица: роли уехали бы на строки темы, которая
		// проектом не является, то есть в состояние, которого стор больше нигде не допускает.
		plain := insertFileTopicFixture(ctx, t, "test-gr-plain")
		project := insertProjectTopicFixture(ctx, t, s, "test-gr-typed")

		_, err := s.Files().MergeTopics(admin, project, plain)
		require.ErrorIs(t, err, entity.ErrFileTopicKindMismatch)
		_, err = s.Files().MergeTopics(admin, plain, project)
		require.ErrorIs(t, err, entity.ErrFileTopicKindMismatch)

		// Однотипное слияние при этом продолжает работать — иначе отказ доказывал бы только, что
		// сломано слияние вообще.
		otherPlain := insertFileTopicFixture(ctx, t, "test-gr-plain2")
		_, err = s.Files().MergeTopics(admin, otherPlain, plain)
		require.NoError(t, err)
	})

	t.Run("слияние ролей переносит разметку и убирает источник", func(t *testing.T) {
		project := insertProjectTopicFixture(ctx, t, s, "test-gr-role-merge")
		dup := insertFileRoleFixture(ctx, t, project, "test-gr-dup")
		keep := insertFileRoleFixture(ctx, t, project, "test-gr-keep")
		victim := insertLibraryFileWithSha(ctx, t, "gr-role-merge.pdf", pasha, sha)
		setRole(t, victim, project, dup)

		// Обе роли — ОДНОГО проекта: слияние ролей разных проектов отказывает (0323), и
		// проверяется это отдельно, в files_roles_project_integration_test.go.
		moved, err := s.Files().MergeRoles(admin, dup, keep)
		require.NoError(t, err)
		require.Equal(t, 1, moved)

		ids, _ := list(admin, t, entity.LibraryFileListFilter{ProjectTopicId: project, RoleId: keep})
		require.Equal(t, []int{victim}, ids, "файл обязан оказаться в целевой роли")

		gone, err := storeScalarInt(ctx, `SELECT COUNT(*) FROM file_role WHERE id = ?`, dup)
		require.NoError(t, err)
		require.Zero(t, gone, "источник обязан исчезнуть: слияние — это и есть способ убрать разъехавшуюся роль")
	})

	t.Run("понижение проекта обнуляет роли и говорит сколько", func(t *testing.T) {
		// Оставить роли значило бы завести строки, чья роль указывает в тему, проектом не
		// являющуюся. Обнулить МОЛЧА — ещё хуже: обратное повышение воскресило бы разметку,
		// которой никто не ставил.
		project := insertProjectTopicFixture(ctx, t, s, "test-gr-demote")
		firstRole := insertFileRoleFixture(ctx, t, project, "test-gr-demote-1")
		secondRole := insertFileRoleFixture(ctx, t, project, "test-gr-demote-2")
		one := insertLibraryFileWithSha(ctx, t, "gr-demote-1.pdf", pasha, sha)
		two := insertLibraryFileWithSha(ctx, t, "gr-demote-2.pdf", pasha, sha)
		setRole(t, one, project, firstRole)
		setRole(t, two, project, secondRole)

		res, err := s.Files().UpdateTopicMeta(admin, entity.FileTopicMetaUpdate{
			TopicId: project, Kind: entity.FileTopicKindPlain,
		})
		require.NoError(t, err)
		require.Equal(t, 2, res.ClearedRoles, "понижение обязано СКАЗАТЬ, сколько ролей оно сняло")

		left, err := storeScalarInt(ctx,
			`SELECT COUNT(*) FROM library_file_topic WHERE topic_id = ? AND role_id IS NOT NULL`, project)
		require.NoError(t, err)
		require.Zero(t, left, "и снять их все")

		// Файлы при этом остаются в теме — понижение это не разбор темы.
		ids, _ := list(admin, t, entity.LibraryFileListFilter{TopicIds: []int{project}})
		require.ElementsMatch(t, []int{one, two}, ids)
	})

	t.Run("роль вне проекта не ставится", func(t *testing.T) {
		plain := insertFileTopicFixture(ctx, t, "test-gr-not-a-project")
		// Проверка типа темы стоит РАНЬШЕ проверки владельца роли, поэтому здесь приезжает
		// именно «роль только внутри проекта», а не «роль чужого проекта»: порядок «частное
		// раньше общего» на экране читается фразой про то действие, которое человек совершил.
		_, err := s.Files().SetFileRoles(admin, []int{bare}, plain, picked)
		require.ErrorIs(t, err, entity.ErrRoleNeedsProjectTopic,
			"«это исходник ничего» не значит ничего: роль существует только внутри проекта")
	})

	t.Run("заархивированная роль не назначается, но снимается", func(t *testing.T) {
		// Иначе архив был бы пожеланием: роль пропала бы из пикеров и продолжила бы появляться
		// на файлах.
		project := insertProjectTopicFixture(ctx, t, s, "test-gr-archived-role")
		retired := insertFileRoleFixture(ctx, t, project, "test-gr-retired")
		file := insertLibraryFileWithSha(ctx, t, "gr-archived-role.pdf", pasha, sha)
		setRole(t, file, project, retired)

		var name string
		require.NoError(t, testDB.QueryRowContext(ctx,
			`SELECT name FROM file_role WHERE id = ?`, retired).Scan(&name))
		_, err := s.Files().UpsertRole(admin, entity.FileRoleUpsert{Id: retired, Name: name, Archived: true})
		require.NoError(t, err)

		other := insertLibraryFileWithSha(ctx, t, "gr-archived-role-2.pdf", pasha, sha)
		_, err = s.Files().SetFileRoles(admin, []int{other}, project, retired)
		require.ErrorIs(t, err, entity.ErrFileRoleArchived)

		// А снять — можно: иначе уже проставленную роль стало бы нечем убрать.
		_, err = s.Files().SetFileRoles(admin, []int{file}, project, 0)
		require.NoError(t, err)
	})

	t.Run("архив прячет тему из рельса, но не из экрана тем", func(t *testing.T) {
		project := insertProjectTopicFixture(ctx, t, s, "test-gr-archived-topic")
		_, err := s.Files().UpdateTopicMeta(admin, entity.FileTopicMetaUpdate{
			TopicId: project, Kind: entity.FileTopicKindProject, Archived: true,
		})
		require.NoError(t, err)

		hasTopic := func(topics []entity.FileTopicWithCount, id int) bool {
			for _, tp := range topics {
				if tp.Id == id {
					return true
				}
			}
			return false
		}
		topics, _, _, err := s.Files().ListTopics(admin, false)
		require.NoError(t, err)
		require.False(t, hasTopic(topics, project),
			"по умолчанию архив не приезжает: чипы и пикеры не имеют права его показывать")

		topics, _, _, err = s.Files().ListTopics(admin, true)
		require.NoError(t, err)
		require.True(t, hasTopic(topics, project),
			"экран тем обязан его показывать — архив, которого не видно, не архив")
	})

	t.Run("даты проекта доезжают и стираются", func(t *testing.T) {
		// sql.NullTime, переданный в драйвер СТРУКТУРОЙ, связался бы нулевым временем, и «не
		// задано» превратилось бы в «первый год» — молча, потому что колонка NULLable и никто не
		// упадёт. Поэтому дата снимается обратно из базы, а не из того, что вернул тот же код.
		project := insertProjectTopicFixture(ctx, t, s, "test-gr-dates")
		start := time.Date(2026, 9, 12, 0, 0, 0, 0, time.UTC)
		end := time.Date(2026, 9, 14, 0, 0, 0, 0, time.UTC)
		_, err := s.Files().UpdateTopicMeta(admin, entity.FileTopicMetaUpdate{
			TopicId:  project,
			Kind:     entity.FileTopicKindProject,
			StartsAt: sql.NullTime{Time: start, Valid: true},
			EndsAt:   sql.NullTime{Time: end, Valid: true},
		})
		require.NoError(t, err)

		var gotStart, gotEnd sql.NullTime
		require.NoError(t, testDB.QueryRowContext(ctx,
			`SELECT starts_at, ends_at FROM file_topic WHERE id = ?`, project).Scan(&gotStart, &gotEnd))
		require.True(t, gotStart.Valid)
		require.Equal(t, "2026-09-12", gotStart.Time.Format("2006-01-02"))
		require.Equal(t, "2026-09-14", gotEnd.Time.Format("2006-01-02"))

		// Пустая дата ОЧИЩАЕТ поле, а не оставляет прежнюю: форма приезжает целиком, и «стёр дату
		// в диалоге, а она вернулась» читается как потеря правки.
		_, err = s.Files().UpdateTopicMeta(admin, entity.FileTopicMetaUpdate{
			TopicId: project, Kind: entity.FileTopicKindProject,
		})
		require.NoError(t, err)
		require.NoError(t, testDB.QueryRowContext(ctx,
			`SELECT starts_at, ends_at FROM file_topic WHERE id = ?`, project).Scan(&gotStart, &gotEnd))
		require.False(t, gotStart.Valid, "пустая дата обязана записать NULL, а не нулевое время")
		require.False(t, gotEnd.Valid)
	})

	t.Run("предикат видимости накрывает и проект, и роль, и счётчики", func(t *testing.T) {
		// Фильтр по проекту и роли стоит ПОД предикатом, а не рядом с ним: имя файла в этой
		// библиотеке говорящее, и утечка здесь — это утечка имени, которую отказ на открытии уже
		// не закрывает.
		project := insertProjectTopicFixture(ctx, t, s, "test-gr-secret-project")
		role := insertFileRoleFixture(ctx, t, project, "test-gr-secret-role")
		secret := insertLibraryFileWithSha(ctx, t, "gr-secret.pdf", pasha, sha)
		open := insertLibraryFileWithSha(ctx, t, "gr-open.pdf", pasha, sha)
		setRole(t, secret, project, role)
		setRole(t, open, project, role)

		_, err := s.Files().SetFileAccess(admin, secret, entity.LibraryFileAccessUpdate{
			Level: entity.LibraryFileAccessPeople, Actor: pasha,
		})
		require.NoError(t, err)

		alien := viewerCtx(ctx, stranger)
		for _, c := range []struct {
			name string
			f    entity.LibraryFileListFilter
		}{
			{"весь проект", entity.LibraryFileListFilter{ProjectTopicId: project}},
			{"проект × роль", entity.LibraryFileListFilter{ProjectTopicId: project, RoleId: role}},
			{"сквозная роль", entity.LibraryFileListFilter{RoleId: role}},
		} {
			t.Run(c.name, func(t *testing.T) {
				ids, total := list(alien, t, c.f)
				requireSet(t, []int{open}, ids, total,
					"закрытый файл обязан ПРОПАСТЬ из выдачи, а счёт — идти под тем же условием: иначе число само рассказало бы, что от человека что-то закрыли")
			})
		}

		// Счётчик роли — тот же вопрос числом. Он персональный, и это принято сознательно.
		roleCount := func(ctx context.Context, t *testing.T) int {
			t.Helper()
			roles, err := s.Files().ListRoles(ctx, false, project)
			require.NoError(t, err)
			for _, r := range roles {
				if r.Id == role {
					return r.FilesCount
				}
			}
			t.Fatalf("роль %d обязана быть в словаре", role)
			return -1
		}
		require.Equal(t, 1, roleCount(alien, t),
			"чужой обязан видеть в счётчике роли только открытый файл")
		require.Equal(t, 2, roleCount(admin, t),
			"а тот, кому видно всё, — оба: иначе «пропало у чужого» доказывало бы лишь, что счётчик сломан")

		// Запись под предикатом: невидимый файл в пачке отказывает ВСЕЙ пачке, и отвечает
		// NotFound, а не PermissionDenied, — «нет прав» подтвердило бы существование.
		_, err = s.Files().SetFileRoles(alien, []int{open, secret}, project, role)
		require.ErrorIs(t, err, sql.ErrNoRows,
			"один невидимый id обязан отказать всей пачке: «проставилось 1 из 2» и есть подтверждение, что второй файл существует")
	})

	t.Run("пустая роль остаётся в словаре", func(t *testing.T) {
		// Половина ценности разбивки — в показе ОТСУТСТВИЯ: «готовое — пусто» говорит, что съёмка
		// не сдана. Предикат в WHERE вместо ON превратил бы LEFT JOIN в INNER и выкинул бы такие
		// роли из ответа вовсе.
		//
		// ПРОВЕРЯТЬ ЭТО ОБЯЗАТЕЛЬНО ОБЫЧНЫМ СОТРУДНИКОМ, А НЕ СУПЕРОМ. У супера предикат
		// вырождается в `1 = 1`, то есть в безобидное слагаемое, и перенос его из ON в WHERE не
		// меняет НИЧЕГО — подтест под супером остаётся зелёным на сломанном запросе. Это не
		// теория: ровно такая мутация здесь и не покраснела, пока строчки ниже не было.
		emptyProject := insertProjectTopicFixture(ctx, t, s, "test-gr-empty-project")
		empty := insertFileRoleFixture(ctx, t, emptyProject, "test-gr-empty-role")
		for _, seer := range []struct {
			name string
			ctx  context.Context
		}{
			{"супер", admin},
			{"обычный сотрудник", viewerCtx(ctx, stranger)},
		} {
			t.Run(seer.name, func(t *testing.T) {
				roles, err := s.Files().ListRoles(seer.ctx, false, emptyProject)
				require.NoError(t, err)
				found := false
				for _, r := range roles {
					if r.Id == empty {
						found = true
						require.Zero(t, r.FilesCount)
					}
				}
				require.True(t, found,
					"роль без единого файла обязана быть в словаре — в неё кладут новое, и «готовое: пусто» и есть половина ценности разбивки")
			})
		}
	})
}

// storeScalarInt — одна цифра прямо из базы, минуя стор: подтесты сверяют СХЕМУ (что строка
// исчезла, что колонка обнулилась), а не то, что об этом рассказывает тот же код, который её и
// менял.
func storeScalarInt(ctx context.Context, query string, args ...any) (int, error) {
	var n int
	if err := testDB.QueryRowContext(ctx, query, args...).Scan(&n); err != nil {
		return 0, err
	}
	return n, nil
}
