package fileslibrary

import (
	"context"
	"database/sql"
	"fmt"
	"strings"

	"github.com/jekabolt/grbpwr-manager/internal/dependency"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/jekabolt/grbpwr-manager/internal/store/storeutil"
)

// СЛОВАРЬ РОЛЕЙ И ТИП ТЕМЫ (0320), РОЛИ ПРИНАДЛЕЖАТ ПРОЕКТУ (0323).
//
// Роль — это то, чем файл является В КОНКРЕТНОМ ПРОЕКТЕ, и хранится она на строке связи
// `library_file_topic.role_id`. Всё, что здесь написано, — следствия этого решения:
//
//   - «одна роль на файл в проекте» никем не проверяется: UNIQUE(file_id, topic_id) даёт одну
//     строку на пару, у строки одно поле роли. Две роли НЕВЫРАЗИМЫ;
//   - роль без проекта тоже невыразима, и это верное поведение — «исходник ничего» не значит
//     ничего;
//   - словарь ЗАКРЫТ формой данных, а не дисциплиной: роли лежат в своей таблице, и ни один из
//     путей, создающих темы на лету, туда не дотягивается.
//
// 0323 добавила к этому ВЛАДЕЛЬЦА У САМОЙ РОЛИ: словаря на всю библиотеку больше нет, «исходники»
// съёмки и «исходники» лукбука — разные строки разных проектов. Отсюда два новых правила, которые
// видно во всех четырёх функциях ниже:
//
//   - каждый читающий путь КЛЮЧУЕТСЯ ПРОЕКТОМ, каждый пишущий его ТРЕБУЕТ;
//   - инвариант «роль на строке — из проекта этой же строки» держится ДВУМЯ слоями: кодом здесь
//     (человеческая фраза) и составным внешним ключом (topic_id, role_id) →
//     file_role (project_topic_id, id) последним рубежом (нечитаемый 1452). Кодовые проверки
//     стоят ПЕРВЫМИ именно поэтому.
//
// Все счётчики здесь считаются ПОД ПРЕДИКАТОМ ВИДИМОСТИ, тем же билдером Viewer.Where, что и
// рельс тем. Одинаковое у всех число означало бы «в этой роли есть что-то, чего тебе не
// показывают», то есть ту же утечку, только выраженную числом.

// ListRoles returns the role vocabulary of ONE project (projectTopicID > 0) or, with 0, every
// role there is — each one carrying its owner.
//
// НОЛЬ — ЭТО НЕ «СЛОВАРЬ БИБЛИОТЕКИ», А ИНДЕКС ДЛЯ РАЗРЕШЕНИЯ. Экрану тем он нужен, чтобы
// показать словари всех проектов сразу, а старой ссылке `?frole=N` — чтобы найти, в каком проекте
// эта роль живёт, и дописать проект в адрес. Словарём же, из которого выбирают, служит только
// ответ с проектом: выбор из общего списка снова сделал бы возможной простановку чужой роли,
// которую сервер всё равно отвергнет.
//
// Предикат стоит в ON внешнего соединения, а не в WHERE, ровно по той же причине, что в
// ListTopics: в WHERE он превратил бы LEFT JOIN в INNER и выкинул ПУСТЫЕ роли. А пустая роль —
// это половина ценности страницы проекта: «готовое — пусто» говорит, что съёмка не сдана.
// Отсутствие рисуется только тогда, когда известно, чего может не хватать.
//
// Архив фильтруется в WHERE по самой роли — это её собственное свойство, а не свойство файлов.
//
// СЧЁТЧИК СТАЛ ВНУТРИПРОЕКТНЫМ ДАРОМ, БЕЗ ЕДИНОЙ ПРАВКИ ЗАПРОСА. Соединение идёт по role_id, а
// составной внешний ключ не даёт строке связи нести роль чужого проекта — значит все строки, по
// которым считается роль, лежат в её собственном проекте по построению. Дописывать сюда
// `AND lft.topic_id = fr.project_topic_id` было бы вторым выражением того же правила, и разошлись
// бы они молча.
func (s *Store) ListRoles(ctx context.Context, includeArchived bool, projectTopicID int) ([]entity.FileRoleWithCount, error) {
	v, err := s.viewer(ctx)
	if err != nil {
		return nil, err
	}
	archived := "fr.archived_at IS NULL"
	if includeArchived {
		archived = "1 = 1"
	}
	params := map[string]any{}
	scope := "1 = 1"
	if projectTopicID > 0 {
		scope = "fr.project_topic_id = :projectTopicId"
		params["projectTopicId"] = projectTopicID
	}
	roles, err := storeutil.QueryListNamed[entity.FileRoleWithCount](ctx, s.DB, `
		SELECT fr.*, COUNT(lf.id) AS files_count
		FROM file_role fr
		LEFT JOIN library_file_topic lft ON lft.role_id = fr.id
		LEFT JOIN library_file lf ON lf.id = lft.file_id AND `+v.Where("lf", params)+`
		WHERE `+archived+` AND `+scope+`
		GROUP BY fr.id
		ORDER BY fr.sort_order ASC, fr.name ASC`, params)
	if err != nil {
		return nil, fmt.Errorf("failed to list file roles: %w", err)
	}
	return roles, nil
}

// UpsertRole creates a role IN A PROJECT (id = 0) or edits the existing one.
//
// ЕДИНСТВЕННАЯ ТОЧКА СОЗДАНИЯ РОЛИ во всём репозитории — в этом и состоит «закрытый словарь»
// механически. Совпадение имени НЕ схлопывается в существующую роль (в отличие от upsertTopic,
// который так делает намеренно: тему создают на лету двое сразу). Роль заводят руками на экране
// словаря, и «создал, а получил чужую» там читается как молчаливая потеря; отказ по уникальному
// ключу честнее. Уникальность теперь ПАРНАЯ — (проект, имя), — поэтому «исходники» законны в
// двадцати проектах и незаконны дважды в одном.
//
// СОЗДАНИЕ ТРЕБУЕТ ПРОЕКТА, ПРАВКА ЕГО НЕ ДВИГАЕТ. Роль не переезжает между проектами никогда:
// её строки связи живут в её проекте, и переезд оставил бы их указывать в чужую роль — то самое
// состояние, которое запрещает инвариант. Ноль в правке значит «не трогать» (так шлёт клиент,
// не знающий про владельца), другой проект — отказ.
//
// ПРОВЕРКА kind ВНУТРИ ТРАНЗАКЦИИ, А НЕ ВНЕШНИМ КЛЮЧОМ: ключ подтверждает лишь существование
// темы, а ярлык вместо проекта — осмысленный выбор не той сущности, и о нём надо сказать фразой.
// Пишущие транзакции стора идут в SERIALIZABLE, поэтому чтение kind запирает строку темы и
// параллельное понижение не проскакивает между проверкой и вставкой.
func (s *Store) UpsertRole(ctx context.Context, r entity.FileRoleUpsert) (int, error) {
	name := strings.TrimSpace(r.Name)
	if name == "" {
		return 0, fmt.Errorf("role name is empty")
	}
	var id int
	err := s.txFunc(ctx, func(ctx context.Context, rep dependency.Repository) error {
		if r.Id <= 0 {
			if r.ProjectTopicId <= 0 {
				return entity.ErrFileRoleNeedsProject
			}
			kind, err := storeutil.QueryNamedOne[struct {
				Kind entity.FileTopicKind `db:"kind"`
			}](ctx, rep.DB(),
				`SELECT kind FROM file_topic WHERE id = :id`, map[string]any{"id": r.ProjectTopicId})
			if err != nil {
				return err // sql.ErrNoRows нетронутым - темы нет
			}
			if kind.Kind != entity.FileTopicKindProject {
				return entity.ErrFileRoleNeedsProject
			}
			newID, err := storeutil.ExecNamedLastId(ctx, rep.DB(), `
				INSERT INTO file_role (project_topic_id, name, sort_order, archived_at)
				VALUES (:projectTopicId, :name, :sortOrder, IF(:archived, CURRENT_TIMESTAMP, NULL))`,
				map[string]any{
					"projectTopicId": r.ProjectTopicId, "name": name,
					"sortOrder": r.SortOrder, "archived": r.Archived,
				})
			if err != nil {
				return err // 1062 доезжает до хендлера нетронутым
			}
			id = newID
			return nil
		}
		existing, err := storeutil.QueryNamedOne[entity.FileRole](ctx, rep.DB(),
			`SELECT * FROM file_role WHERE id = :id`, map[string]any{"id": r.Id})
		if err != nil {
			return err // sql.ErrNoRows нетронутым
		}
		if r.ProjectTopicId > 0 &&
			(!existing.ProjectTopicId.Valid || int(existing.ProjectTopicId.Int64) != r.ProjectTopicId) {
			return entity.ErrFileRoleProjectImmutable
		}
		// COALESCE держит МОМЕНТ архивации: повторное сохранение уже заархивированной роли не
		// переписывает дату, иначе «когда убрали» врало бы после любой правки имени.
		//
		// project_topic_id в SET НЕ ВХОДИТ, и это не забывчивость: колонка, которой нет в
		// присваивании, не может уехать ни от какой ошибки вызывающего.
		if err := storeutil.ExecNamed(ctx, rep.DB(), `
			UPDATE file_role
			SET name = :name, sort_order = :sortOrder,
				archived_at = IF(:archived, COALESCE(archived_at, CURRENT_TIMESTAMP), NULL)
			WHERE id = :id`,
			map[string]any{"id": r.Id, "name": name, "sortOrder": r.SortOrder, "archived": r.Archived}); err != nil {
			return err // 1062 доезжает до хендлера нетронутым
		}
		id = r.Id
		return nil
	})
	if err != nil {
		return 0, err // sql.ErrNoRows и 1062 проходят насквозь
	}
	return id, nil
}

// MergeRoles folds source into target and deletes source, returning how many
// VISIBLE link rows changed role.
//
// Слияние ролей ПРОЩЕ слияния тем, и это прямое следствие модели: роль — колонка, а не связь,
// поэтому дедупликации нет вовсе. Строка не может нести две роли, значит столкновения, которое у
// тем гасит INSERT IGNORE, здесь не существует.
//
// Число считается ПОД предикатом, а переезжает всё — та же асимметрия, что у MergeTopics, и по
// той же причине: «переехало 7» на роли, где спрашивающий видит два файла, само рассказало бы,
// что от него что-то закрыто.
//
// СЛИЯНИЕ ТОЛЬКО ВНУТРИ ОДНОГО ПРОЕКТА (0323). Строки связи источника лежат в его проекте, и
// подстановка им роли чужого проекта — ровно то состояние, которое инвариант запрещает; без
// проверки это молча ловил бы составной ключ нечитаемым 1452. Слияние — это «две роли оказались
// одной», а роль одного проекта и роль другого одной быть не могут по построению: их значения
// определяются проектом.
func (s *Store) MergeRoles(ctx context.Context, sourceID, targetID int) (int, error) {
	if sourceID == targetID {
		// Бэкстоп: хендлер отвечает на это InvalidArgument раньше. Молчаливый no-op был бы хуже
		// — слияние необратимо, и «готово» на бессмысленный запрос убеждает человека, что он
		// сделал то, чего не делал.
		return 0, fmt.Errorf("cannot merge a role into itself")
	}
	v, err := s.viewer(ctx)
	if err != nil {
		return 0, err
	}
	var moved int
	err = s.txFunc(ctx, func(ctx context.Context, rep dependency.Repository) error {
		// Существование проверяем ВНУТРИ транзакции: пишущие транзакции стора идут в
		// SERIALIZABLE, поэтому проверка реально закрывает гонку с параллельным удалением, а не
		// просто сужает окно.
		type roleOwnerRow struct {
			Id             int           `db:"id"`
			ProjectTopicId sql.NullInt64 `db:"project_topic_id"`
		}
		found, err := storeutil.QueryListNamed[roleOwnerRow](ctx, rep.DB(),
			`SELECT id, project_topic_id FROM file_role WHERE id IN (:ids)`,
			map[string]any{"ids": []int{sourceID, targetID}})
		if err != nil {
			return fmt.Errorf("failed to check file roles existence: %w", err)
		}
		if len(found) != 2 {
			return sql.ErrNoRows
		}
		// Сравниваются СТРУКТУРЫ, а не Int64: у двух легаси-строк с NULL-владельцем Int64 равны
		// нулю обе, и сравнение чисел объявило бы их одним проектом. sql.NullInt64 сравним, и
		// NULL совпадает только с NULL - что для мёртвых строк переноса и верно.
		if found[0].ProjectTopicId != found[1].ProjectTopicId {
			return entity.ErrFileRoleProjectMismatch
		}
		countParams := map[string]any{"source": sourceID}
		visibleMoved, err := storeutil.QueryCountNamed(ctx, rep.DB(), `
			SELECT COUNT(*) FROM library_file_topic lft
			JOIN library_file lf ON lf.id = lft.file_id
			WHERE lft.role_id = :source AND `+v.Where("lf", countParams), countParams)
		if err != nil {
			return fmt.Errorf("failed to count visible links moved between roles: %w", err)
		}
		if err := storeutil.ExecNamed(ctx, rep.DB(),
			`UPDATE library_file_topic SET role_id = :target WHERE role_id = :source`,
			map[string]any{"source": sourceID, "target": targetID}); err != nil {
			return fmt.Errorf("failed to move file role links: %w", err)
		}
		// Строки сняты ДО удаления самой роли: внешний ключ на роль стоит без каскада
		// (RESTRICT), иначе DELETE упал бы о собственные ссылки.
		if err := storeutil.ExecNamed(ctx, rep.DB(),
			`DELETE FROM file_role WHERE id = :source`, map[string]any{"source": sourceID}); err != nil {
			return fmt.Errorf("failed to delete source file role: %w", err)
		}
		moved = visibleMoved
		return nil
	})
	if err != nil {
		return 0, err // sql.ErrNoRows passes through untouched
	}
	return moved, nil
}

// SetFileRoles puts a batch of files into ONE project in ONE role, creating the
// link row for files that were not in the project yet. roleID = 0 clears the role
// while leaving the file in the project.
//
// ТОЧКА 10 ПРЕДИКАТА (запись), СЕМАНТИКА ПАЧКИ: ОДИН невидимый id отказывает ВСЕЙ пачке — ровно
// как у AssignTopics. Частичное применение отвечало бы на видимый и невидимый id по-разному, а
// «проставилось 4 из 5» и есть подтверждение, что пятый файл существует.
//
// Считается именно НЕВИДИМОЕ (условие под отрицанием), а не «сколько видимо»: файл, УДАЛЁННЫЙ
// между загрузкой сетки и нажатием кнопки, обязан остаться просто отсутствующей строкой, из-за
// которой пачка не падает.
func (s *Store) SetFileRoles(ctx context.Context, fileIDs []int, projectTopicID, roleID int) (int, error) {
	if len(fileIDs) == 0 {
		return 0, nil
	}
	if len(fileIDs) > maxPageLimit {
		return 0, fmt.Errorf("%w: at most %d files can be assigned a role in one call, got %d",
			entity.ErrLibraryBatchTooLarge, maxPageLimit, len(fileIDs))
	}
	if projectTopicID <= 0 {
		return 0, fmt.Errorf("%w: a project topic is required", entity.ErrRoleNeedsProjectTopic)
	}
	v, err := s.viewer(ctx)
	if err != nil {
		return 0, err
	}
	var updated int
	err = s.txFunc(ctx, func(ctx context.Context, rep dependency.Repository) error {
		invisibleParams := map[string]any{"fileIds": fileIDs}
		invisible, err := storeutil.QueryCountNamed(ctx, rep.DB(),
			`SELECT COUNT(*) FROM library_file lf WHERE lf.id IN (:fileIds) AND NOT (`+
				v.Where("lf", invisibleParams)+`)`, invisibleParams)
		if err != nil {
			return fmt.Errorf("failed to check library files visibility: %w", err)
		}
		if invisible > 0 {
			return sql.ErrNoRows
		}
		// РОЛЬ ТОЛЬКО ВНУТРИ ПРОЕКТА. Констрейнтом это не выражается без денормализации `kind` в
		// таблицу связи, а денормализация давала бы устаревающие строки при смене типа темы —
		// цена выше выгоды. Поэтому проверка стоит здесь, в единственном месте, где роль ставится.
		kind, err := storeutil.QueryNamedOne[struct {
			Kind entity.FileTopicKind `db:"kind"`
		}](ctx, rep.DB(),
			`SELECT kind FROM file_topic WHERE id = :id`, map[string]any{"id": projectTopicID})
		if err != nil {
			return err // sql.ErrNoRows нетронутым
		}
		if kind.Kind != entity.FileTopicKindProject {
			return entity.ErrRoleNeedsProjectTopic
		}
		if roleID > 0 {
			role, err := storeutil.QueryNamedOne[entity.FileRole](ctx, rep.DB(),
				`SELECT * FROM file_role WHERE id = :id`, map[string]any{"id": roleID})
			if err != nil {
				return err // sql.ErrNoRows нетронутым
			}
			// РОЛЬ ОБЯЗАНА БЫТЬ РОЛЬЮ ЭТОГО ЖЕ ПРОЕКТА (0323). Проверка стоит ПЕРВОЙ из двух:
			// последним рубежом ту же строку ловит составной внешний ключ, но отвечает он 1452 с
			// именем констрейнта, а на экране нужна фраза. Легаси-строка с NULL-владельцем сюда
			// тоже не проходит - Valid = false отвергается первым же условием.
			//
			// Отсюда прямое следствие для проб: утверждать надо ТЕКСТ отказа, а не факт падения.
			// Выкинутая отсюда проверка оставит тест зелёным, если он проверяет только «упало» -
			// потому что упадёт ключ.
			if !role.ProjectTopicId.Valid || int(role.ProjectTopicId.Int64) != projectTopicID {
				return entity.ErrFileRoleForeignProject
			}
			// Снять заархивированную роль можно, поставить — нет. Иначе архив был бы
			// пожеланием: роль пропала бы из пикеров и продолжила бы появляться на файлах.
			if role.ArchivedAt.Valid {
				return entity.ErrFileRoleArchived
			}
		}
		// Строка связи заводится, если файла в проекте ещё не было: операция читается как
		// «положить эти файлы в проект в роли R», и требовать предварительного проставления темы
		// значило бы делать одно действие двумя.
		//
		// Кросс-джойн по library_file, а не перечисление пар в VALUES: файл, удалённый между
		// загрузкой сетки и нажатием кнопки, просто не даёт строки, и пачка не падает целиком.
		if err := storeutil.ExecNamed(ctx, rep.DB(), `
			INSERT IGNORE INTO library_file_topic (file_id, topic_id)
			SELECT lf.id, :topicId FROM library_file lf WHERE lf.id IN (:fileIds)`,
			map[string]any{"topicId": projectTopicID, "fileIds": fileIDs}); err != nil {
			return fmt.Errorf("failed to link files to project: %w", err)
		}
		var role any
		if roleID > 0 {
			role = roleID
		}
		// RowsAffected у UPDATE считает РЕАЛЬНО ИЗМЕНЁННЫЕ строки, поэтому файл, уже несший эту
		// роль, в число не попадает — ровно то, что обещает контракт.
		rows, err := storeutil.ExecNamedRows(ctx, rep.DB(), `
			UPDATE library_file_topic SET role_id = :roleId
			WHERE topic_id = :topicId AND file_id IN (:fileIds)`,
			map[string]any{"roleId": role, "topicId": projectTopicID, "fileIds": fileIDs})
		if err != nil {
			return fmt.Errorf("failed to set file roles: %w", err)
		}
		updated = int(rows)
		return nil
	})
	if err != nil {
		return 0, err // sql.ErrNoRows passes through untouched
	}
	return updated, nil
}

// UpdateTopicMeta REPLACES a topic's kind, dates and archive flag, returning what
// the demotion took away: how many VISIBLE link rows lost their role, how many
// styles lost their link to the project, and how many kanban cards lost it too.
//
// ПОНИЖЕНИЕ ПРОЕКТА СНИМАЕТ ЕГО ПРОЕКТНЫЕ СВОЙСТВА В ТОЙ ЖЕ ТРАНЗАКЦИИ И ГОВОРИТ, СКОЛЬКО СНЯЛО.
// Оставить роли значило бы завести строки, чья роль указывает в тему, проектом не являющуюся, —
// состояние, которого стор больше нигде не допускает. Оставить привязки стилей — то же самое
// одним уровнем выше: связь «проект ↔ стиль» существует только у проекта, и связь, висящая на
// ярлыке, невыразима ни в одном экране. Снять молча — хуже обоих вариантов: обратное повышение
// воскресило бы разметку, которой никто не ставил, а карточка вещи потеряла бы ответ на «каким
// файлом меня сделали» в день, когда кто-то переключил тип темы.
//
// ТРИ ЧИСЛА СЧИТАЮТСЯ ПО-РАЗНОМУ, И ЭТО НЕ НЕБРЕЖНОСТЬ. Роли считаются ПОД предикатом видимости
// (а обнуляются все) — та же асимметрия, что у слияний: число читает человек, и «снято 7» в
// проекте, где ему видно два файла, само рассказало бы, что от него что-то закрыто. Привязки
// стилей и ссылки задач считаются ТОЧНО: ни стиль, ни карточка канбана не являются файлом
// библиотеки — они живут под собственным RBAC секций techcards и tasks, — и прятать их от того,
// кто и так правит проект, значило бы придумать границу, которой в системе нет.
//
// ЧИСЛА ПРИЕЗЖАЮТ В ИМЕНОВАННЫЕ ПОЛЯ, а не тройкой в возврате, именно потому, что их стало три:
// см. довод у entity.FileTopicMetaResult и тест, который ставит три РАЗНЫХ количества.
func (s *Store) UpdateTopicMeta(ctx context.Context, m entity.FileTopicMetaUpdate) (entity.FileTopicMetaResult, error) {
	var res entity.FileTopicMetaResult
	if m.TopicId <= 0 {
		return res, sql.ErrNoRows
	}
	if m.Kind != entity.FileTopicKindPlain && m.Kind != entity.FileTopicKindProject {
		return res, fmt.Errorf("%w: %q", entity.ErrFileTopicKindUnknown, m.Kind)
	}
	v, err := s.viewer(ctx)
	if err != nil {
		return res, err
	}
	var cleared, unlinked, clearedTasks int
	err = s.txFunc(ctx, func(ctx context.Context, rep dependency.Repository) error {
		// ПРЕЖНИЙ ТИП ЧИТАЕТСЯ ЗДЕСЬ ЖЕ, И ЭТО НЕ ЗАМЕНА ПРОВЕРКИ СУЩЕСТВОВАНИЯ, А ЕЁ РАСШИРЕНИЕ:
		// затравке (ниже) нужен именно ПЕРЕХОД plain → project, а не итоговый тип. Правка дат или
		// архива уже-проекта перехода не содержит и сеять не имеет права.
		prev, err := storeutil.QueryNamedOne[struct {
			Kind entity.FileTopicKind `db:"kind"`
		}](ctx, rep.DB(),
			`SELECT kind FROM file_topic WHERE id = :id`, map[string]any{"id": m.TopicId})
		if err != nil {
			return err // sql.ErrNoRows нетронутым
		}
		if m.Kind == entity.FileTopicKindPlain {
			countParams := map[string]any{"id": m.TopicId}
			visible, err := storeutil.QueryCountNamed(ctx, rep.DB(), `
				SELECT COUNT(*) FROM library_file_topic lft
				JOIN library_file lf ON lf.id = lft.file_id
				WHERE lft.topic_id = :id AND lft.role_id IS NOT NULL
				  AND `+v.Where("lf", countParams), countParams)
			if err != nil {
				return fmt.Errorf("failed to count roles cleared by demotion: %w", err)
			}
			if err := storeutil.ExecNamed(ctx, rep.DB(),
				`UPDATE library_file_topic SET role_id = NULL WHERE topic_id = :id AND role_id IS NOT NULL`,
				map[string]any{"id": m.TopicId}); err != nil {
				return fmt.Errorf("failed to clear roles of a demoted project: %w", err)
			}
			cleared = visible
			// ПРИВЯЗКИ СТИЛЕЙ УХОДЯТ ВМЕСТЕ С ТИПОМ (0321). Число снимается из RowsAffected
			// самого DELETE, а не отдельным COUNT перед ним: между двумя запросами в одной
			// SERIALIZABLE-транзакции разойтись они не могут, а лишний запрос — это лишнее
			// место, где условие можно однажды поправить только в одном из двух.
			rows, err := storeutil.ExecNamedRows(ctx, rep.DB(),
				`DELETE FROM file_topic_tech_card WHERE topic_id = :id`,
				map[string]any{"id": m.TopicId})
			if err != nil {
				return fmt.Errorf("failed to unlink styles of a demoted project: %w", err)
			}
			unlinked = int(rows)
			// ССЫЛКИ ЗАДАЧ УХОДЯТ ВМЕСТЕ С ТИПОМ (0322), ТРЕТЬИМ ПРОЕКТНЫМ СВОЙСТВОМ. Задача при
			// этом ОСТАЁТСЯ — она про работу, а не про съёмку, — и теряет только контекст.
			//
			// SQL ПО ЧУЖОЙ ТАБЛИЦЕ, А НЕ ВЫЗОВ СТОРА ЗАДАЧ, И ЭТО ВЫНУЖДЕННО ПО СУЩЕСТВУ. Понижение
			// обязано быть ОДНОЙ транзакцией с обнулением: между двумя транзакциями наблюдаемо
			// состояние «тема уже ярлык, а карточки всё ещё указывают в неё», то есть ровно то,
			// чего этот код не допускает. А вложенный вызов сюда не годится: sub-store внутри
			// транзакции несёт txFunc ВНЕШНЕГО стора (initSubStoresForTx в store/db.go), поэтому
			// rep.Tasks() открыл бы ВТОРУЮ транзакцию и упёрся бы в собственные локи.
			taskRows, err := storeutil.ExecNamedRows(ctx, rep.DB(),
				`UPDATE task SET project_topic_id = NULL WHERE project_topic_id = :id`,
				map[string]any{"id": m.TopicId})
			if err != nil {
				return fmt.Errorf("failed to clear project links of a demoted project: %w", err)
			}
			clearedTasks = int(taskRows)
		}
		// COALESCE держит МОМЕНТ архивации по тому же доводу, что у роли: правка дат уже
		// заархивированного проекта не имеет права переписывать «когда его убрали».
		if err := storeutil.ExecNamed(ctx, rep.DB(), `
			UPDATE file_topic
			SET kind = :kind, starts_at = :startsAt, ends_at = :endsAt,
				archived_at = IF(:archived, COALESCE(archived_at, CURRENT_TIMESTAMP), NULL)
			WHERE id = :id`,
			map[string]any{
				"id":   m.TopicId,
				"kind": string(m.Kind),
				// sql.NullTime уезжает СТРУКТУРОЙ, и разворачивать её вручную не нужно: она
				// реализует driver.Valuer и сама отдаёт NULL при Valid = false. Замерено
				// мутацией: ручная развёртка и прямая передача дают в базе одно и то же.
				"startsAt": m.StartsAt,
				"endsAt":   m.EndsAt,
				"archived": m.Archived,
			}); err != nil {
			return fmt.Errorf("failed to update file topic meta: %w", err)
		}
		// ЗАТРАВКА СЛОВАРЯ ПРИ ПОВЫШЕНИИ. Довод записан ещё в 0312 — раздел, открывшийся пустым,
		// заставляет придумывать структуру на месте, а придумывать её никто не будет. Со словарём
		// у проекта он применим дословно: новый проект без ролей это страница без разбивки.
		//
		// ZERO-GUARD, А НЕ «СЕЯТЬ ВСЕГДА». Понижение проекта словарь НЕ трогает (снимаются роли со
		// СТРОК СВЯЗИ, сами строки словаря остаются спать), поэтому обратное повышение находит
		// выстраданный набор и не имеет права класть поверх него четыре чужих слова. Краевой
		// случай назван вслух: владелец, удаливший все свои роли и прогнавший понижение-повышение,
		// получит стандартные четыре снова — редко и не больно.
		if prev.Kind != entity.FileTopicKindProject && m.Kind == entity.FileTopicKindProject {
			if err := seedProjectRoles(ctx, rep.DB(), m.TopicId); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return res, err // sql.ErrNoRows passes through untouched
	}
	res.ClearedRoles = cleared
	res.ClearedStyles = unlinked
	res.ClearedTasks = clearedTasks
	return res, nil
}

// defaultProjectRoles — ЗАТРАВКА СЛОВАРЯ НОВОГО ПРОЕКТА, один список на весь бэкенд.
//
// Тот же набор снимком повторён в миграции 0323 (шаг 6), которая сеет им проекты, заведённые до
// per-project словаря. Снимок в тексте миграции законен и не является вторым источником правды:
// миграции и есть снимки, они описывают ОДИН момент истории и после применения не меняются, а
// список для БУДУЩИХ проектов живёт здесь.
//
// Слова русские, а интерфейс раздела английский — так уже сеял 0320, и менять язык затравки
// заодно с моделью значило бы смешать два вопроса в одной волне.
var defaultProjectRoles = []struct {
	Name      string
	SortOrder int
}{
	{"исходники", 10},
	{"обработанные", 20},
	{"идея", 30},
	{"планирование", 40},
}

// seedProjectRoles fills a project's EMPTY vocabulary with the default set.
//
// INSERT IGNORE поверх zero-guard, а не вместо него: guard отвечает на «словарь уже есть» (и
// именно он делает повторное повышение безвредным), а IGNORE закрывает гонку двух повышений одной
// темы — парный UNIQUE (project_topic_id, name) превратил бы её в 1062 на ровном месте.
func seedProjectRoles(ctx context.Context, db dependency.DB, projectTopicID int) error {
	existing, err := storeutil.QueryCountNamed(ctx, db,
		`SELECT COUNT(*) FROM file_role WHERE project_topic_id = :id`,
		map[string]any{"id": projectTopicID})
	if err != nil {
		return fmt.Errorf("failed to count project roles before seeding: %w", err)
	}
	if existing > 0 {
		return nil
	}
	for _, r := range defaultProjectRoles {
		if err := storeutil.ExecNamed(ctx, db, `
			INSERT IGNORE INTO file_role (project_topic_id, name, sort_order)
			VALUES (:projectTopicId, :name, :sortOrder)`,
			map[string]any{"projectTopicId": projectTopicID, "name": r.Name, "sortOrder": r.SortOrder}); err != nil {
			return fmt.Errorf("failed to seed project roles: %w", err)
		}
	}
	return nil
}
