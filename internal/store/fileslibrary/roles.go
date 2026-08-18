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

// СЛОВАРЬ РОЛЕЙ И ТИП ТЕМЫ (0320).
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
// Все счётчики здесь считаются ПОД ПРЕДИКАТОМ ВИДИМОСТИ, тем же билдером Viewer.Where, что и
// рельс тем. Одинаковое у всех число означало бы «в этой роли есть что-то, чего тебе не
// показывают», то есть ту же утечку, только выраженную числом.

// ListRoles returns the role vocabulary with CROSS-PROJECT counts.
//
// Предикат стоит в ON внешнего соединения, а не в WHERE, ровно по той же причине, что в
// ListTopics: в WHERE он превратил бы LEFT JOIN в INNER и выкинул ПУСТЫЕ роли. А пустая роль —
// это половина ценности страницы проекта: «готовое — пусто» говорит, что съёмка не сдана.
// Отсутствие рисуется только тогда, когда известно, чего может не хватать.
//
// Архив фильтруется в WHERE по самой роли — это её собственное свойство, а не свойство файлов.
func (s *Store) ListRoles(ctx context.Context, includeArchived bool) ([]entity.FileRoleWithCount, error) {
	v, err := s.viewer(ctx)
	if err != nil {
		return nil, err
	}
	archived := "fr.archived_at IS NULL"
	if includeArchived {
		archived = "1 = 1"
	}
	params := map[string]any{}
	roles, err := storeutil.QueryListNamed[entity.FileRoleWithCount](ctx, s.DB, `
		SELECT fr.*, COUNT(lf.id) AS files_count
		FROM file_role fr
		LEFT JOIN library_file_topic lft ON lft.role_id = fr.id
		LEFT JOIN library_file lf ON lf.id = lft.file_id AND `+v.Where("lf", params)+`
		WHERE `+archived+`
		GROUP BY fr.id
		ORDER BY fr.sort_order ASC, fr.name ASC`, params)
	if err != nil {
		return nil, fmt.Errorf("failed to list file roles: %w", err)
	}
	return roles, nil
}

// UpsertRole creates a role (id = 0) or edits the existing one.
//
// ЕДИНСТВЕННАЯ ТОЧКА СОЗДАНИЯ РОЛИ во всём репозитории — в этом и состоит «закрытый словарь»
// механически. Совпадение имени НЕ схлопывается в существующую роль (в отличие от upsertTopic,
// который так делает намеренно: тему создают на лету двое сразу). Роль заводят руками на экране
// словаря, и «создал, а получил чужую» там читается как молчаливая потеря; отказ по уникальному
// ключу честнее.
func (s *Store) UpsertRole(ctx context.Context, r entity.FileRoleUpsert) (int, error) {
	name := strings.TrimSpace(r.Name)
	if name == "" {
		return 0, fmt.Errorf("role name is empty")
	}
	if r.Id <= 0 {
		id, err := storeutil.ExecNamedLastId(ctx, s.DB, `
			INSERT INTO file_role (name, sort_order, archived_at)
			VALUES (:name, :sortOrder, IF(:archived, CURRENT_TIMESTAMP, NULL))`,
			map[string]any{"name": name, "sortOrder": r.SortOrder, "archived": r.Archived})
		if err != nil {
			return 0, err // 1062 доезжает до хендлера нетронутым
		}
		return id, nil
	}
	exists, err := storeutil.QueryCountNamed(ctx, s.DB,
		`SELECT COUNT(*) FROM file_role WHERE id = :id`, map[string]any{"id": r.Id})
	if err != nil {
		return 0, fmt.Errorf("failed to check file role existence: %w", err)
	}
	if exists == 0 {
		return 0, sql.ErrNoRows
	}
	// COALESCE держит МОМЕНТ архивации: повторное сохранение уже заархивированной роли не
	// переписывает дату, иначе «когда убрали» врало бы после любой правки имени.
	if err := storeutil.ExecNamed(ctx, s.DB, `
		UPDATE file_role
		SET name = :name, sort_order = :sortOrder,
			archived_at = IF(:archived, COALESCE(archived_at, CURRENT_TIMESTAMP), NULL)
		WHERE id = :id`,
		map[string]any{"id": r.Id, "name": name, "sortOrder": r.SortOrder, "archived": r.Archived}); err != nil {
		return 0, err // 1062 доезжает до хендлера нетронутым
	}
	return r.Id, nil
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
		found, err := storeutil.QueryCountNamed(ctx, rep.DB(),
			`SELECT COUNT(*) FROM file_role WHERE id IN (:ids)`,
			map[string]any{"ids": []int{sourceID, targetID}})
		if err != nil {
			return fmt.Errorf("failed to check file roles existence: %w", err)
		}
		if found != 2 {
			return sql.ErrNoRows
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
// the demotion took away: how many VISIBLE link rows lost their role, and how many
// styles lost their link to the project.
//
// ПОНИЖЕНИЕ ПРОЕКТА СНИМАЕТ ЕГО ПРОЕКТНЫЕ СВОЙСТВА В ТОЙ ЖЕ ТРАНЗАКЦИИ И ГОВОРИТ, СКОЛЬКО СНЯЛО.
// Оставить роли значило бы завести строки, чья роль указывает в тему, проектом не являющуюся, —
// состояние, которого стор больше нигде не допускает. Оставить привязки стилей — то же самое
// одним уровнем выше: связь «проект ↔ стиль» существует только у проекта, и связь, висящая на
// ярлыке, невыразима ни в одном экране. Снять молча — хуже обоих вариантов: обратное повышение
// воскресило бы разметку, которой никто не ставил, а карточка вещи потеряла бы ответ на «каким
// файлом меня сделали» в день, когда кто-то переключил тип темы.
//
// ДВА ЧИСЛА СЧИТАЮТСЯ ПО-РАЗНОМУ, И ЭТО НЕ НЕБРЕЖНОСТЬ. Роли считаются ПОД предикатом видимости
// (а обнуляются все) — та же асимметрия, что у слияний: число читает человек, и «снято 7» в
// проекте, где ему видно два файла, само рассказало бы, что от него что-то закрыто. Привязки
// стилей считаются ТОЧНО: стиль — не файл библиотеки, он живёт под собственным RBAC секции
// techcards, и прятать его от того, кто и так правит проект, значило бы придумать границу,
// которой в системе нет.
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
	var cleared, unlinked int
	err = s.txFunc(ctx, func(ctx context.Context, rep dependency.Repository) error {
		exists, err := storeutil.QueryCountNamed(ctx, rep.DB(),
			`SELECT COUNT(*) FROM file_topic WHERE id = :id`, map[string]any{"id": m.TopicId})
		if err != nil {
			return fmt.Errorf("failed to check file topic existence: %w", err)
		}
		if exists == 0 {
			return sql.ErrNoRows
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
		return nil
	})
	if err != nil {
		return res, err // sql.ErrNoRows passes through untouched
	}
	res.ClearedRoles = cleared
	res.ClearedStyles = unlinked
	return res, nil
}
