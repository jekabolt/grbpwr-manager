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

// РОЛИ ПРИНАДЛЕЖАТ ПРОЕКТУ (0323) — ПРИЁМКА.
//
// 0320 сделала роль ЗАКРЫТЫМ ОБЩИМ словарём; заказ владельца прямо противоположен: «они для
// каждого проекта могут быть разными и не должны быть на уровне всех файлов». Отсюда новый
// инвариант, ради которого написан этот файл:
//
//	РОЛЬ НА СТРОКЕ СВЯЗИ ОБЯЗАНА БЫТЬ РОЛЬЮ ТОГО ЖЕ ПРОЕКТА, ЧТО И САМА СТРОКА.
//
// Держится он ДВУМЯ слоями, и проверять надо ОБА по отдельности:
//
//  1. код в SetFileRoles отвечает ФРАЗОЙ (entity.ErrFileRoleForeignProject);
//  2. составной внешний ключ (topic_id, role_id) → file_role (project_topic_id, id) отвечает
//     нечитаемым 1452 и ловит то, что код пропустил.
//
// ЛОВУШКА, ВПИСАННАЯ СЮДА ЗАРАНЕЕ: проба, утверждающая ФАКТ падения, а не его ТЕКСТ, останется
// зелёной, если кодовую проверку выкинуть — вместо неё упадёт ключ. Поэтому подтест инварианта
// сверяет именно ошибку и отдельно требует, чтобы в тексте НЕ было номера отказа СУБД. Тот же
// класс тавтологии на этой волне уже ловился.
func TestFileRolesPerProject(t *testing.T) {
	filesGuard(t)

	ctx, cancel := context.WithTimeout(context.Background(), 240*time.Second)
	defer cancel()

	s, err := NewForTest(ctx, *testCfg)
	require.NoError(t, err)
	admin := superCtx(ctx)

	_, pasha := insertAdminFixture(ctx, t, "test-rp-pasha")
	sha := fmt.Sprintf("%064d", time.Now().UnixNano()%1e10)

	rolesOf := func(t *testing.T, project int) map[string]int {
		t.Helper()
		rows, err := testDB.QueryContext(ctx,
			`SELECT name, id FROM file_role WHERE project_topic_id = ?`, project)
		require.NoError(t, err)
		defer rows.Close()
		out := map[string]int{}
		for rows.Next() {
			var name string
			var id int
			require.NoError(t, rows.Scan(&name, &id))
			out[name] = id
		}
		require.NoError(t, rows.Err())
		return out
	}
	// roleOnRow reads what a link row ACTUALLY carries, straight from the schema: подтест
	// сверяет строку, а не рассказ того же кода, который её и писал.
	roleOnRow := func(t *testing.T, fileID, topicID int) (string, bool) {
		t.Helper()
		var name sql.NullString
		err := testDB.QueryRowContext(ctx, `
			SELECT fr.name FROM library_file_topic lft
			LEFT JOIN file_role fr ON fr.id = lft.role_id
			WHERE lft.file_id = ? AND lft.topic_id = ?`, fileID, topicID).Scan(&name)
		if err == sql.ErrNoRows {
			return "", false
		}
		require.NoError(t, err)
		return name.String, true
	}
	requireInvariant := func(t *testing.T) {
		t.Helper()
		bad, err := storeScalarInt(ctx, `
			SELECT COUNT(*) FROM library_file_topic lft
			WHERE lft.role_id IS NOT NULL
			  AND NOT EXISTS (SELECT 1 FROM file_role fr
				WHERE fr.id = lft.role_id AND fr.project_topic_id = lft.topic_id)`)
		require.NoError(t, err)
		require.Zero(t, bad,
			"ни одна строка связи не имеет права нести роль чужого проекта: это и есть весь инвариант 0323")
	}

	t.Run("повышение темы засевает словарь, а повторное — нет", func(t *testing.T) {
		// Довод из 0312 дословно: раздел, открывшийся пустым, заставляет придумывать структуру на
		// месте, а придумывать её никто не будет. Новый проект без единой роли — это страница без
		// разбивки, то есть ровно тот пустой рейл.
		topic := insertFileTopicFixture(ctx, t, "test-rp-seed")
		require.Empty(t, rolesOf(t, topic), "фикстура: обычный ярлык словаря не имеет")

		_, err := s.Files().UpdateTopicMeta(admin, entity.FileTopicMetaUpdate{
			TopicId: topic, Kind: entity.FileTopicKindProject,
		})
		require.NoError(t, err)
		seeded := rolesOf(t, topic)
		require.Len(t, seeded, 4, "повышение обязано завести стандартный набор")
		for _, name := range []string{"исходники", "обработанные", "идея", "планирование"} {
			require.Contains(t, seeded, name)
		}

		// Затравка НЕ ДОБИРАЕТ недостающее: словарь правит человек, и «удалил лишнюю роль, а она
		// вернулась после сохранения дат» читается как потеря правки.
		idea := rolesOf(t, topic)["идея"]
		require.NotZero(t, idea)
		_, err = testDB.ExecContext(ctx, `DELETE FROM file_role WHERE id = ?`, idea)
		require.NoError(t, err)
		_, err = s.Files().UpdateTopicMeta(admin, entity.FileTopicMetaUpdate{
			TopicId:  topic,
			Kind:     entity.FileTopicKindProject,
			StartsAt: sql.NullTime{Time: time.Date(2026, 9, 12, 0, 0, 0, 0, time.UTC), Valid: true},
		})
		require.NoError(t, err)
		require.Len(t, rolesOf(t, topic), 3,
			"правка дат уже-проекта не имеет права восстанавливать удалённое человеком")

		// Понижение словарь НЕ трогает (роли снимаются со СТРОК СВЯЗИ, строки словаря остаются
		// спать), поэтому обратное повышение находит выстраданный набор и не кладёт поверх него
		// четыре чужих слова. Мутант «снять zero-guard» краснеет ровно здесь.
		_, err = s.Files().UpdateTopicMeta(admin, entity.FileTopicMetaUpdate{
			TopicId: topic, Kind: entity.FileTopicKindPlain,
		})
		require.NoError(t, err)
		require.Len(t, rolesOf(t, topic), 3, "понижение снимает разметку файлов, а не словарь проекта")

		_, err = s.Files().UpdateTopicMeta(admin, entity.FileTopicMetaUpdate{
			TopicId: topic, Kind: entity.FileTopicKindProject,
		})
		require.NoError(t, err)
		require.Len(t, rolesOf(t, topic), 3,
			"повторное повышение обязано найти спящий словарь и НЕ сеять поверх него")

		// ПУСТОЙ СЛОВАРЬ У УЖЕ-ПРОЕКТА — И ЗАТРАВКА ВСЁ РАВНО МОЛЧИТ. Это единственное место, где
		// видно, что сеется ПЕРЕХОД, а не итоговый тип: пока в словаре хоть что-то есть, обе
		// формулировки ведут себя одинаково, и zero-guard маскирует разницу. Мутант «сеять по
		// итоговому типу» краснеет ровно здесь и больше нигде.
		_, err = testDB.ExecContext(ctx, `DELETE FROM file_role WHERE project_topic_id = ?`, topic)
		require.NoError(t, err)
		_, err = s.Files().UpdateTopicMeta(admin, entity.FileTopicMetaUpdate{
			TopicId:  topic,
			Kind:     entity.FileTopicKindProject,
			StartsAt: sql.NullTime{Time: time.Date(2026, 9, 13, 0, 0, 0, 0, time.UTC), Valid: true},
		})
		require.NoError(t, err)
		require.Empty(t, rolesOf(t, topic),
			"правка уже-проекта — не переход: словарь, вычищенный человеком, обязан остаться пустым")

		// А вот ПЕРЕХОД на пустом словаре сеет заново, и это названный краевой случай: владелец,
		// удаливший все роли и прогнавший понижение-повышение, получит стандартные четыре.
		_, err = s.Files().UpdateTopicMeta(admin, entity.FileTopicMetaUpdate{
			TopicId: topic, Kind: entity.FileTopicKindPlain,
		})
		require.NoError(t, err)
		_, err = s.Files().UpdateTopicMeta(admin, entity.FileTopicMetaUpdate{
			TopicId: topic, Kind: entity.FileTopicKindProject,
		})
		require.NoError(t, err)
		require.Len(t, rolesOf(t, topic), 4,
			"переход plain → project на пустом словаре сеет: раздел, открывшийся пустым, заставляет придумывать структуру на месте")
	})

	t.Run("роль чужого проекта отвергается ФРАЗОЙ, а не отказом ключа", func(t *testing.T) {
		mine := insertProjectTopicFixture(ctx, t, s, "test-rp-mine")
		alien := insertProjectTopicFixture(ctx, t, s, "test-rp-alien")
		alienRole := insertFileRoleFixture(ctx, t, alien, "test-rp-alien-role")
		file := insertLibraryFileWithSha(ctx, t, "rp-foreign.pdf", pasha, sha)

		_, err := s.Files().SetFileRoles(admin, []int{file}, mine, alienRole)
		require.ErrorIs(t, err, entity.ErrFileRoleForeignProject,
			"кодовая проверка обязана стоять ПЕРВОЙ и отвечать фразой")
		// ЭТО И ЕСТЬ ЛОВУШКА МУТАНТА. Выкинутая проверка в SetFileRoles оставила бы подтест
		// зелёным, если бы он утверждал только «упало»: вместо неё упал бы составной ключ. Здесь
		// утверждается ТЕКСТ — номера отказа СУБД в нём быть не должно.
		require.NotContains(t, err.Error(), "1452",
			"человек читает фразу, а не номер отказа внешнего ключа: если сюда доехал 1452, значит кодовой проверки больше нет")
		require.NotErrorIs(t, err, sql.ErrNoRows,
			"роль существует — это не тройной NotFound, а осмысленный выбор не той роли")

		// Своя роль того же проекта при этом ставится: иначе отказ доказывал бы лишь, что сломана
		// простановка вообще.
		ownRole := insertFileRoleFixture(ctx, t, mine, "test-rp-own-role")
		updated, err := s.Files().SetFileRoles(admin, []int{file}, mine, ownRole)
		require.NoError(t, err)
		require.Equal(t, 1, updated)
		requireInvariant(t)
	})

	t.Run("составной ключ — последний рубеж: сырой UPDATE не проходит", func(t *testing.T) {
		// Отдельная проба САМОГО рубежа. Без неё «инвариант держится» означало бы только «стор
		// проверяет», и любой второй путь записи (миграция, ручная правка, новый метод) прошёл бы
		// мимо молча.
		mine := insertProjectTopicFixture(ctx, t, s, "test-rp-key-mine")
		alien := insertProjectTopicFixture(ctx, t, s, "test-rp-key-alien")
		alienRole := insertFileRoleFixture(ctx, t, alien, "test-rp-key-alien-role")
		file := insertLibraryFileWithSha(ctx, t, "rp-key.pdf", pasha, sha)
		_, err := s.Files().SetFileRoles(admin, []int{file}, mine, 0)
		require.NoError(t, err)

		_, err = testDB.ExecContext(ctx,
			`UPDATE library_file_topic SET role_id = ? WHERE file_id = ? AND topic_id = ?`,
			alienRole, file, mine)
		require.Error(t, err, "схема обязана отвергать чужую роль сама, без участия стора")
		require.Contains(t, err.Error(), "fk_library_file_topic_role_project",
			"и отвергать её обязан именно составной ключ, а не что-нибудь ещё")
		requireInvariant(t)
	})

	t.Run("слияние проектов: матрица имён", func(t *testing.T) {
		// ПОТЕРЯ СВЯЗЕЙ ПРИ СЛИЯНИИ ТЕМ ЛОВИЛАСЬ НА ЭТОЙ ВОЛНЕ ТРИЖДЫ — сначала у ролей (0320),
		// потом у стилей (0321), потом у задач (0322). Поэтому матрица здесь обязательна, а не
		// желательна: слияние обязано разобрать ЧЕТЫРЕ разных случая, и три из них выглядят
		// одинаково зелёными, если проверять только один.
		source := insertProjectTopicFixture(ctx, t, s, "test-rp-merge-src")
		target := insertProjectTopicFixture(ctx, t, s, "test-rp-merge-dst")

		srcShared := insertNamedFileRoleFixture(ctx, t, source, "test-rp-общая")
		dstShared := insertNamedFileRoleFixture(ctx, t, target, "test-rp-общая")
		srcOnly := insertNamedFileRoleFixture(ctx, t, source, "test-rp-только-источник")
		dstOnly := insertNamedFileRoleFixture(ctx, t, target, "test-rp-только-цель")

		shared := insertLibraryFileWithSha(ctx, t, "rp-merge-shared.pdf", pasha, sha)
		onlySrc := insertLibraryFileWithSha(ctx, t, "rp-merge-src-only.pdf", pasha, sha)
		inBoth := insertLibraryFileWithSha(ctx, t, "rp-merge-both.pdf", pasha, sha)
		bare := insertLibraryFileWithSha(ctx, t, "rp-merge-bare.pdf", pasha, sha)

		set := func(fileID, topicID, roleID int) {
			_, err := s.Files().SetFileRoles(admin, []int{fileID}, topicID, roleID)
			require.NoError(t, err)
		}
		set(shared, source, srcShared)
		set(onlySrc, source, srcOnly)
		set(inBoth, source, srcShared)
		set(inBoth, target, dstOnly)
		set(bare, source, 0)

		_, err := s.Files().MergeTopics(admin, source, target)
		require.NoError(t, err,
			"слияние обязано ПРОЙТИ: проекция, оставляющая role_id источника, упирается в составной ключ и роняет всю операцию")

		after := rolesOf(t, target)
		require.Equal(t, dstShared, after["test-rp-общая"],
			"своя роль цели СТАРШЕ приезжей: столкновение имён гасит парный UNIQUE, и переживает его строка цели")
		require.Contains(t, after, "test-rp-только-источник",
			"роль, которой в цели не было, обязана доехать по имени — иначе разметку источника переносить некуда")
		require.NotEqual(t, srcOnly, after["test-rp-только-источник"],
			"доезжает КОПИЯ: роль принадлежит проекту и между проектами не переезжает")
		require.Equal(t, dstOnly, after["test-rp-только-цель"], "роль, бывшая только в цели, не трогается")

		name, ok := roleOnRow(t, shared, target)
		require.True(t, ok, "файл источника обязан оказаться в цели")
		require.Equal(t, "test-rp-общая", name, "и нести роль С ТЕМ ЖЕ ИМЕНЕМ, что была у него в источнике")

		name, ok = roleOnRow(t, onlySrc, target)
		require.True(t, ok)
		require.Equal(t, "test-rp-только-источник", name,
			"роль, которой в цели не было, обязана доехать ВМЕСТЕ С ФАЙЛОМ: без клонирования словаря он приехал бы без роли, и потеря была бы молчаливой")

		name, ok = roleOnRow(t, inBoth, target)
		require.True(t, ok)
		require.Equal(t, "test-rp-только-цель", name,
			"файл, уже лежавший в цели, сохраняет СВОЮ роль: приезжая её не перебивает")

		name, ok = roleOnRow(t, bare, target)
		require.True(t, ok, "файл без роли обязан переехать так же свободно, как размеченный")
		require.Empty(t, name, "и остаться без роли: приёмник проекта — законное состояние")

		requireInvariant(t)
	})

	t.Run("слияние ролей разных проектов отказывает", func(t *testing.T) {
		one := insertProjectTopicFixture(ctx, t, s, "test-rp-mr-one")
		two := insertProjectTopicFixture(ctx, t, s, "test-rp-mr-two")
		roleOne := insertFileRoleFixture(ctx, t, one, "test-rp-mr-role-one")
		roleTwo := insertFileRoleFixture(ctx, t, two, "test-rp-mr-role-two")

		_, err := s.Files().MergeRoles(admin, roleOne, roleTwo)
		require.ErrorIs(t, err, entity.ErrFileRoleProjectMismatch,
			"строки связи источника живут в его проекте: подстановка им роли чужого проекта — то самое состояние, которое запрещает инвариант")

		// Внутри одного проекта слияние работает — иначе отказ доказывал бы лишь, что сломано
		// слияние вообще.
		sibling := insertFileRoleFixture(ctx, t, one, "test-rp-mr-sibling")
		_, err = s.Files().MergeRoles(admin, sibling, roleOne)
		require.NoError(t, err)
	})

	t.Run("создание роли требует проекта, имя уникально в проекте", func(t *testing.T) {
		project := insertProjectTopicFixture(ctx, t, s, "test-rp-upsert")
		other := insertProjectTopicFixture(ctx, t, s, "test-rp-upsert-other")
		plain := insertFileTopicFixture(ctx, t, "test-rp-upsert-plain")

		_, err := s.Files().UpsertRole(admin, entity.FileRoleUpsert{Name: "test-rp-loose"})
		require.ErrorIs(t, err, entity.ErrFileRoleNeedsProject,
			"роль без проекта — сирота, которую потом не найдёт ни один экран")

		_, err = s.Files().UpsertRole(admin, entity.FileRoleUpsert{
			ProjectTopicId: plain, Name: "test-rp-loose",
		})
		require.ErrorIs(t, err, entity.ErrFileRoleNeedsProject,
			"ярлык — не проект: «это исходник ничего» не значит ничего и на создании тоже")

		id, err := s.Files().UpsertRole(admin, entity.FileRoleUpsert{
			ProjectTopicId: project, Name: "test-rp-имя", SortOrder: 7,
		})
		require.NoError(t, err)
		require.NotZero(t, id)
		t.Cleanup(func() { _, _ = testDB.Exec(`DELETE FROM file_role WHERE id = ?`, id) })

		_, err = s.Files().UpsertRole(admin, entity.FileRoleUpsert{
			ProjectTopicId: project, Name: "test-rp-имя",
		})
		require.Error(t, err, "дважды одно имя в ОДНОМ проекте — отказ")
		require.True(t, s.IsErrUniqueViolation(err), "и отказ обязан быть 1062, чтобы хендлер сказал про имя")

		twin, err := s.Files().UpsertRole(admin, entity.FileRoleUpsert{
			ProjectTopicId: other, Name: "test-rp-имя",
		})
		require.NoError(t, err,
			"а то же имя в ДРУГОМ проекте законно — ради этого вся волна: «исходники» съёмки и «исходники» лукбука разные сущности")
		t.Cleanup(func() { _, _ = testDB.Exec(`DELETE FROM file_role WHERE id = ?`, twin) })

		// Правка проект НЕ двигает: молчаливый переезд утащил бы за собой разбивку целого
		// проекта, оставив его строки связи указывать в чужую роль.
		_, err = s.Files().UpsertRole(admin, entity.FileRoleUpsert{
			Id: id, ProjectTopicId: other, Name: "test-rp-имя",
		})
		require.ErrorIs(t, err, entity.ErrFileRoleProjectImmutable)

		// Ноль в правке значит «не трогать» — так шлёт клиент, не знающий про владельца.
		_, err = s.Files().UpsertRole(admin, entity.FileRoleUpsert{Id: id, Name: "test-rp-имя-2"})
		require.NoError(t, err)
		var owner sql.NullInt64
		require.NoError(t, testDB.QueryRowContext(ctx,
			`SELECT project_topic_id FROM file_role WHERE id = ?`, id).Scan(&owner))
		require.True(t, owner.Valid)
		require.Equal(t, project, int(owner.Int64), "переименование не имеет права менять владельца")
	})

	t.Run("словарь ключуется проектом, ноль отдаёт всё с владельцами", func(t *testing.T) {
		mine := insertProjectTopicFixture(ctx, t, s, "test-rp-list-mine")
		alien := insertProjectTopicFixture(ctx, t, s, "test-rp-list-alien")
		mineRole := insertFileRoleFixture(ctx, t, mine, "test-rp-list-mine-role")
		alienRole := insertFileRoleFixture(ctx, t, alien, "test-rp-list-alien-role")

		has := func(roles []entity.FileRoleWithCount, id int) bool {
			for _, r := range roles {
				if r.Id == id {
					return true
				}
			}
			return false
		}
		scoped, err := s.Files().ListRoles(admin, false, mine)
		require.NoError(t, err)
		require.True(t, has(scoped, mineRole), "словарь проекта обязан содержать его роли")
		require.False(t, has(scoped, alienRole),
			"и НЕ содержать чужих: иначе пикер снова предлагал бы роль, которую сервер отвергнет")
		for _, r := range scoped {
			require.True(t, r.ProjectTopicId.Valid)
			require.Equal(t, mine, int(r.ProjectTopicId.Int64))
		}

		all, err := s.Files().ListRoles(admin, false, 0)
		require.NoError(t, err)
		require.True(t, has(all, mineRole))
		require.True(t, has(all, alienRole),
			"ноль — индекс для разрешения старой ссылки и экрана тем: он обязан отдавать роли всех проектов")
	})
}

// TestFileRolesPerProjectMigrationIsRerunnable re-applies 0323 over a schema it has ALREADY been
// applied to, with LIVE data standing on it — exactly what a mid-file failure leaves behind: MySQL
// auto-commits DDL, so the half-applied schema keeps no gorp_migrations row and the next boot runs
// the file again from the top.
//
// ЭТО НЕ ДУБЛИКАТ migrationlint И НЕ ДУБЛИКАТ ПРОГОНА НА ЧИСТОЙ БАЗЕ. Линт читает ТЕКСТ; чистая
// база доказывает лишь, что файл применяется. Здесь проверяется ГЛАВНОЕ: повтор не трогает уже
// перенесённые данные — роли остаются у своих проектов, строки связи не теряют role_id, id ролей
// не меняются (INSERT IGNORE обязан быть no-op, а не пересозданием), а ключей и индексов ровно
// столько, сколько должно быть.
//
// Перенос ЖИВОЙ глобальной роли в три проекта проверяется отдельно, прогоном на слепке «до»
// (tmp/migcheck-roles): здесь схема уже мигрирована, и глобальных ролей на ней нет по построению.
func TestFileRolesPerProjectMigrationIsRerunnable(t *testing.T) {
	filesGuard(t)

	ctx, cancel := context.WithTimeout(context.Background(), 180*time.Second)
	defer cancel()

	s, err := NewForTest(ctx, *testCfg)
	require.NoError(t, err)
	admin := superCtx(ctx)

	_, pasha := insertAdminFixture(ctx, t, "test-rr-pasha")
	sha := fmt.Sprintf("%064d", time.Now().UnixNano()%1e10)

	project := insertProjectTopicFixture(ctx, t, s, "test-rr-project")
	role := insertFileRoleFixture(ctx, t, project, "test-rr-role")
	file := insertLibraryFileWithSha(ctx, t, "rr-file.pdf", pasha, sha)
	_, err = s.Files().SetFileRoles(admin, []int{file}, project, role)
	require.NoError(t, err)

	for i := range 3 {
		_, err := testDB.ExecContext(ctx,
			`DELETE FROM gorp_migrations WHERE id = '0323_file_role_per_project.sql'`)
		require.NoError(t, err)
		require.NoError(t, Migrate(testDB), "re-applying 0323 over an applied schema (pass %d)", i+1)

		var owner sql.NullInt64
		require.NoError(t, testDB.QueryRowContext(ctx,
			`SELECT project_topic_id FROM file_role WHERE id = ?`, role).Scan(&owner))
		require.True(t, owner.Valid, "повтор обязан быть no-op ПО СУЩЕСТВУ: роль не имеет права осиротеть")
		require.Equal(t, project, int(owner.Int64), "и остаться у СВОЕГО проекта")

		carried, err := storeScalarInt(ctx,
			`SELECT COUNT(*) FROM library_file_topic WHERE file_id = ? AND topic_id = ? AND role_id = ?`,
			file, project, role)
		require.NoError(t, err)
		require.Equal(t, 1, carried,
			"строка связи обязана нести ТУ ЖЕ роль: пересозданная строка словаря сменила бы id и оставила разметку указывать в никуда")

		seeded, err := storeScalarInt(ctx,
			`SELECT COUNT(*) FROM file_role WHERE project_topic_id = ?`, project)
		require.NoError(t, err)
		require.Equal(t, 5, seeded,
			"затравка обязана молчать на проекте, у которого словарь уже есть: 4 засеянных при повышении + заведённая тестом")

		idx, err := storeScalarInt(ctx, `
			SELECT COUNT(DISTINCT INDEX_NAME) FROM information_schema.STATISTICS
			WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'file_role'
			  AND INDEX_NAME IN ('uniq_file_role_project_name', 'uniq_file_role_project_id')`)
		require.NoError(t, err)
		require.Equal(t, 2, idx,
			"оба парных индекса обязаны стоять после повтора: на них держатся идемпотентность клонирования и составной ключ")

		gone, err := storeScalarInt(ctx, `
			SELECT COUNT(*) FROM information_schema.STATISTICS
			WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'file_role'
			  AND INDEX_NAME = 'uniq_file_role_name'`)
		require.NoError(t, err)
		require.Zero(t, gone,
			"глобальный UNIQUE(name) обязан остаться снятым: с ним «исходники» были бы законны ровно в одном проекте")

		fks, err := storeScalarInt(ctx, `
			SELECT COUNT(*) FROM information_schema.TABLE_CONSTRAINTS
			WHERE CONSTRAINT_SCHEMA = DATABASE() AND TABLE_NAME = 'library_file_topic'
			  AND CONSTRAINT_TYPE = 'FOREIGN KEY'`)
		require.NoError(t, err)
		require.Equal(t, 3, fks,
			"три ключа: файл, тема и СОСТАВНОЙ на роль — одноколоночный обязан быть снят, иначе рубеж инварианта отсутствует")
	}
}
