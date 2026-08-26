package task

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/jekabolt/grbpwr-manager/internal/dependency"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/jekabolt/grbpwr-manager/internal/store/storeutil"
)

// СВЯЗИ ЗАДАЧ — ДВА МЕХАНИЗМА, А НЕ ОДИН (0338).
//
// РОДИТЕЛЬ — КОЛОНКА task.parent_task_id: кардинальность 1, два родителя у карточки невозможны
// физически. СВЯЗЬ — строка task_link(from, to, kind): равноправные карточки, многие-ко-многим.
// Смешать их значило бы потерять кардинальность 1 и завести второй способ записать иерархию;
// подробный довод — в шапке миграции.
//
// ГДЕ ЧТО НОРМАЛИЗУЕТСЯ. Перспектива (BLOCKED_BY → перевёрнутый blocks) снимается в dto чистой
// функцией — это свойство КОНТРАКТА. Нормализация relates (min,max) и проверка перевёрнутого blocks
// живут ЗДЕСЬ, рядом с инвариантом хранилища, который их и закрепляет CHECK'ом.

// maxParentChainWalk ограничивает подъём по цепочке родителей. Это защита от ИСПОРЧЕННЫХ данных
// (цикл, каким-то образом уже лежащий в таблице), а не предел глубины сабтасок: искусственный
// «не глубже двух» пришлось бы объяснять человеку, которому понадобилось три.
const maxParentChainWalk = 100

// SetTaskParent делает задачу сабтаской другой; parentID 0 = снять родителя.
//
// ЦИКЛ-ЧЕК ВНУТРИ ПИШУЩЕЙ ТРАНЗАКЦИИ, А НЕ ПЕРЕД НЕЙ. Транзакции стора идут в SERIALIZABLE, поэтому
// подъём по цепочке ЗАПИРАЕТ прочитанные строки, и два встречных SetTaskParent («A под B» и «B под
// A») не могут проскочить между проверкой и записью. Снаружи транзакции проверка была бы фикцией —
// тот же довод, что у ensureProjectTopic.
//
// СЕБЯ РОДИТЕЛЕМ — ТОТ ЖЕ ОТКАЗ, что и цикл: это его вырожденный случай, и различать их отдельной
// фразой значило бы обещать разницу, которой нет.
//
// НЕСУЩЕСТВУЮЩИЙ РОДИТЕЛЬ доезжает внешним ключом (1452) — так же, как восемь глубоких ссылок
// карточки, и хендлер переводит его в тот же InvalidArgument. Возвращать отсюда sql.ErrNoRows было
// бы нельзя: он уже занят смыслом «задачи, которую двигаем, нет».
func (s *Store) SetTaskParent(ctx context.Context, taskID, parentID int) error {
	err := s.txFunc(ctx, func(ctx context.Context, rep dependency.Repository) error {
		exists, err := storeutil.QueryCountNamed(ctx, rep.DB(),
			`SELECT COUNT(*) FROM task WHERE id = :id`, map[string]any{"id": taskID})
		if err != nil {
			return fmt.Errorf("failed to check task existence: %w", err)
		}
		if exists == 0 {
			return sql.ErrNoRows
		}
		if parentID > 0 {
			if parentID == taskID {
				return entity.ErrTaskParentCycle
			}
			if err := ensureNoParentCycle(ctx, rep.DB(), taskID, parentID); err != nil {
				return err
			}
		}
		var parent any
		if parentID > 0 {
			parent = parentID
		}
		if err := storeutil.ExecNamed(ctx, rep.DB(),
			`UPDATE task SET parent_task_id = :parent WHERE id = :id`,
			map[string]any{"parent": parent, "id": taskID}); err != nil {
			return fmt.Errorf("failed to set task parent: %w", err)
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("can't set task parent: %w", err)
	}
	return nil
}

// ensureNoParentCycle поднимается от ПРЕДЛАГАЕМОГО родителя вверх по цепочке. Встретили саму задачу —
// значит предлагаемый родитель является её потомком, и связь замкнула бы кольцо.
//
// Превышение потолка шагов — тоже отказ, и той же фразой: цепочка длиной в сотню уровней в канбане на
// сотни карточек означает уже существующее кольцо в данных, а молча разрешить в него дописать хуже,
// чем отказать.
func ensureNoParentCycle(ctx context.Context, db dependency.DB, taskID, parentID int) error {
	cur := parentID
	for i := 0; i < maxParentChainWalk; i++ {
		if cur == taskID {
			return entity.ErrTaskParentCycle
		}
		row, err := storeutil.QueryNamedOne[struct {
			Parent sql.NullInt32 `db:"parent_task_id"`
		}](ctx, db, `SELECT parent_task_id FROM task WHERE id = :id`, map[string]any{"id": cur})
		if err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				// Родителя не существует: пусть отвечает внешний ключ на самой записи — тем же
				// InvalidArgument, что и остальные глубокие ссылки. Здесь молчим.
				return nil
			}
			return fmt.Errorf("failed to walk task parent chain: %w", err)
		}
		if !row.Parent.Valid {
			return nil
		}
		cur = int(row.Parent.Int32)
	}
	return entity.ErrTaskParentCycle
}

// AddTaskLink записывает связь. ИДЕМПОТЕНТНО: существующая пара — no-op, а не 1062.
//
// kind приезжает УЖЕ развёрнутым в сторону хранилища (BLOCKED_BY снят в dto перестановкой концов),
// поэтому здесь остаются ровно два инварианта самой таблицы:
//   - relates нормализуется в (min, max) — это то, что закрепляет chk_task_link_relates_ordered;
//   - прямая обратная пара blocks отвергается, потому что «A блокирует B» и «B блокирует A»
//     одновременно — всегда ошибка, и стоит она одного SELECT.
//
// Длинные циклы (A→B→C→A) НЕ проверяются намеренно: блокер — совет, а не замок, а обход графа на
// каждую вставку ради отказа «эта связь замыкает цикл через 4 задачи» путал бы сильнее самого цикла.
func (s *Store) AddTaskLink(ctx context.Context, fromTaskID, toTaskID int, kind entity.TaskLinkKind, createdBy string) error {
	if !entity.ValidTaskLinkKinds[kind] {
		return fmt.Errorf("can't add task link: unknown link kind %q", kind)
	}
	from, to := fromTaskID, toTaskID
	if kind == entity.TaskLinkKindRelates && from > to {
		from, to = to, from
	}
	err := s.txFunc(ctx, func(ctx context.Context, rep dependency.Repository) error {
		if kind == entity.TaskLinkKindBlocks {
			reverse, err := storeutil.QueryCountNamed(ctx, rep.DB(), `
				SELECT COUNT(*) FROM task_link
				WHERE from_task_id = :to AND to_task_id = :from AND kind = 'blocks'`,
				map[string]any{"from": from, "to": to})
			if err != nil {
				return fmt.Errorf("failed to check reverse task link: %w", err)
			}
			if reverse > 0 {
				return entity.ErrTaskReverseBlock
			}
		}
		// ON DUPLICATE KEY UPDATE, а не INSERT IGNORE — по доводу AttachFileToTask: IGNORE глушит и
		// нарушение внешнего ключа (1452), и связь с несуществующей задачей прошла бы «успешно»,
		// ничего не записав. ON DUPLICATE даёт ту же идемпотентность по uniq_task_link, но оставляет
		// ключ работать. Автора первой записи повтор НЕ переписывает: факт «кто связал» принадлежит
		// первому нажатию.
		if err := storeutil.ExecNamed(ctx, rep.DB(), `
			INSERT INTO task_link (from_task_id, to_task_id, kind, created_by)
			VALUES (:from, :to, :kind, :createdBy)
			ON DUPLICATE KEY UPDATE created_by = created_by`,
			map[string]any{"from": from, "to": to, "kind": string(kind), "createdBy": createdBy}); err != nil {
			return fmt.Errorf("failed to insert task link: %w", err)
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("can't add task link: %w", err)
	}
	return nil
}

// DeleteTaskLink снимает связь. ИДЕМПОТЕНТНО и молча: обе кнопки описывают ЖЕЛАЕМОЕ состояние («пусть
// эти две задачи не связаны»), и двое, снимающие одну и ту же связь, оба обязаны услышать «её больше
// нет», а не один из них — «её никогда и не было». Тот же довод, что у DetachFileFromTask.
func (s *Store) DeleteTaskLink(ctx context.Context, fromTaskID, toTaskID int, kind entity.TaskLinkKind) error {
	if !entity.ValidTaskLinkKinds[kind] {
		return fmt.Errorf("can't delete task link: unknown link kind %q", kind)
	}
	from, to := fromTaskID, toTaskID
	if kind == entity.TaskLinkKindRelates && from > to {
		from, to = to, from
	}
	if err := storeutil.ExecNamed(ctx, s.DB, `
		DELETE FROM task_link
		WHERE from_task_id = :from AND to_task_id = :to AND kind = :kind`,
		map[string]any{"from": from, "to": to, "kind": string(kind)}); err != nil {
		return fmt.Errorf("can't delete task link: %w", err)
	}
	return nil
}

// taskLinkRow — строка обеих половин чтения. `role` вычисляется в SQL, потому что от того, с какой
// стороны UNION'а строка приехала, зависит и он, и то, какой конец считать «вторым».
type taskLinkRow struct {
	OwnerTaskID int    `db:"owner_task_id"`
	TaskID      int    `db:"task_id"`
	Role        string `db:"role"`
	Title       string `db:"title"`
	Status      string `db:"status"`
	Board       string `db:"board"`
	Archived    bool   `db:"archived"`
}

// linksByTaskIds — батч-чтение связей страницы ОДНИМ запросом (UNION ALL двух половин), со вторым
// концом, уже разрешённым в заголовок/статус/доску.
//
// ДВЕ ПОЛОВИНЫ, А НЕ ДВА ЗАПРОСА: исходящие (эта задача — from) и входящие (эта задача — to). У
// relates обе половины дают роль relates, поэтому одна нормализованная строка честно видна с обоих
// концов; у blocks исходящая половина — BLOCKS, входящая — BLOCKED_BY.
func (s *Store) linksByTaskIds(ctx context.Context, ids []int) (map[int][]entity.TaskLink, error) {
	if len(ids) == 0 {
		return map[int][]entity.TaskLink{}, nil
	}
	rows, err := storeutil.QueryListNamed[taskLinkRow](ctx, s.DB, `
		SELECT l.from_task_id AS owner_task_id, l.to_task_id AS task_id,
		       IF(l.kind = 'blocks', 'blocks', 'relates') AS role,
		       t.title AS title, t.status AS status, t.board AS board,
		       (t.archived_at IS NOT NULL) AS archived
		FROM task_link l
		JOIN task t ON t.id = l.to_task_id
		WHERE l.from_task_id IN (:ids)
		UNION ALL
		SELECT l.to_task_id AS owner_task_id, l.from_task_id AS task_id,
		       IF(l.kind = 'blocks', 'blocked_by', 'relates') AS role,
		       t.title AS title, t.status AS status, t.board AS board,
		       (t.archived_at IS NOT NULL) AS archived
		FROM task_link l
		JOIN task t ON t.id = l.from_task_id
		WHERE l.to_task_id IN (:ids)
		ORDER BY owner_task_id, role, task_id`, map[string]any{"ids": ids})
	if err != nil {
		return nil, fmt.Errorf("can't load task links: %w", err)
	}
	out := make(map[int][]entity.TaskLink, len(ids))
	for _, r := range rows {
		out[r.OwnerTaskID] = append(out[r.OwnerTaskID], entity.TaskLink{
			OwnerTaskId: r.OwnerTaskID,
			TaskId:      r.TaskID,
			Role:        entity.TaskLinkRole(r.Role),
			Title:       r.Title,
			Status:      entity.TaskStatus(r.Status),
			Board:       entity.TaskBoard(r.Board),
			Archived:    r.Archived,
		})
	}
	return out, nil
}

// SubtaskCounts — свёртка детей одной карточки.
type SubtaskCounts struct {
	Total int `db:"total"`
	Done  int `db:"done"`
}

type subtaskCountRow struct {
	ParentTaskID int `db:"parent_task_id"`
	Total        int `db:"total"`
	Done         int `db:"done"`
}

// subtaskCountsByTaskIds — счётчики сабтасок страницы одним GROUP BY.
//
// ТОЛЬКО АКТИВНЫЕ ДЕТИ. Счётчик на карточке отвечает на вопрос «сколько ещё делать», а
// заархивированная сабтаска с доски снята — считать её значило бы вечно показывать «1 из 3» там, где
// работы больше нет.
func (s *Store) subtaskCountsByTaskIds(ctx context.Context, ids []int) (map[int]SubtaskCounts, error) {
	if len(ids) == 0 {
		return map[int]SubtaskCounts{}, nil
	}
	rows, err := storeutil.QueryListNamed[subtaskCountRow](ctx, s.DB, `
		SELECT parent_task_id, COUNT(*) AS total, SUM(status = 'done') AS done
		FROM task
		WHERE parent_task_id IN (:ids) AND archived_at IS NULL
		GROUP BY parent_task_id`, map[string]any{"ids": ids})
	if err != nil {
		return nil, fmt.Errorf("can't load task subtask counts: %w", err)
	}
	out := make(map[int]SubtaskCounts, len(ids))
	for _, r := range rows {
		out[r.ParentTaskID] = SubtaskCounts{Total: r.Total, Done: r.Done}
	}
	return out, nil
}
