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

// Boards are rows of task_board (migration 0420). The six former enum boards are seeded with ids
// 1..6 and keep their enum string as slug; task.board_id is the placement, task.board the legacy
// string kept for one deploy (see entity.TaskBoardLegacyValue).

// boardRow is the identity of a board needed to place a task on it.
type boardRow struct {
	Id   int            `db:"id"`
	Slug sql.NullString `db:"slug"`
}

func (b boardRow) legacy() entity.TaskBoard { return entity.TaskBoardLegacyValue(b.Id, b.Slug) }

// resolveTaskBoard reads the board a ref names: by id when set, else by legacy slug. Inside a
// SERIALIZABLE write transaction the read share-locks the row, so a concurrent DeleteTaskBoard
// cannot remove it under the caller. entity.ErrTaskBoardNotFound when it names nothing.
func resolveTaskBoard(ctx context.Context, db dependency.DB, ref entity.TaskBoardRef) (boardRow, error) {
	var (
		b   boardRow
		err error
	)
	switch {
	case ref.Id > 0:
		b, err = storeutil.QueryNamedOne[boardRow](ctx, db,
			`SELECT id, slug FROM task_board WHERE id = :id`, map[string]any{"id": ref.Id})
	case ref.Slug != "":
		b, err = storeutil.QueryNamedOne[boardRow](ctx, db,
			`SELECT id, slug FROM task_board WHERE slug = :slug`, map[string]any{"slug": string(ref.Slug)})
	default:
		return boardRow{}, entity.ErrTaskBoardNotFound
	}
	if errors.Is(err, sql.ErrNoRows) {
		return boardRow{}, entity.ErrTaskBoardNotFound
	}
	if err != nil {
		return boardRow{}, fmt.Errorf("failed to resolve task board: %w", err)
	}
	return b, nil
}

const taskBoardSelect = `
	SELECT b.id, b.name, b.slug, b.position, b.created_by, b.created_at, b.updated_at,
		CAST(COALESCE(SUM(t.id IS NOT NULL AND t.archived_at IS NULL), 0) AS SIGNED) AS task_count,
		CAST(COALESCE(SUM(t.archived_at IS NOT NULL), 0) AS SIGNED) AS archived_task_count
	FROM task_board b
	LEFT JOIN task t ON t.board_id = b.id`

// ListTaskBoards returns every board in rail order with its active and archived task counts.
func (s *Store) ListTaskBoards(ctx context.Context) ([]entity.TaskBoardEntry, error) {
	boards, err := listTaskBoards(ctx, s.DB)
	if err != nil {
		return nil, fmt.Errorf("can't list task boards: %w", err)
	}
	return boards, nil
}

func listTaskBoards(ctx context.Context, db dependency.DB) ([]entity.TaskBoardEntry, error) {
	return storeutil.QueryListNamed[entity.TaskBoardEntry](ctx, db,
		taskBoardSelect+`
	GROUP BY b.id
	ORDER BY b.position, b.id`, map[string]any{})
}

// GetTaskBoardById returns one board with its counts; sql.ErrNoRows (wrapped) when missing.
func (s *Store) GetTaskBoardById(ctx context.Context, id int) (*entity.TaskBoardEntry, error) {
	b, err := storeutil.QueryNamedOne[entity.TaskBoardEntry](ctx, s.DB,
		taskBoardSelect+`
	WHERE b.id = :id
	GROUP BY b.id`, map[string]any{"id": id})
	if err != nil {
		return nil, fmt.Errorf("can't get task board: %w", err)
	}
	return &b, nil
}

// AddTaskBoard creates a custom board (slug NULL) at the end of the rail and returns its id. A
// name taken case-insensitively surfaces as a unique violation (uniq_task_board_name).
func (s *Store) AddTaskBoard(ctx context.Context, name, createdBy string) (int, error) {
	var id int
	err := s.txFunc(ctx, func(ctx context.Context, rep dependency.Repository) error {
		pos, err := storeutil.QueryCountNamed(ctx, rep.DB(),
			`SELECT COALESCE(MAX(position)+1, 0) FROM task_board`, map[string]any{})
		if err != nil {
			return fmt.Errorf("failed to compute task board position: %w", err)
		}
		id, err = storeutil.ExecNamedLastId(ctx, rep.DB(),
			`INSERT INTO task_board (name, position, created_by) VALUES (:name, :position, :createdBy)`,
			map[string]any{"name": name, "position": pos, "createdBy": createdBy})
		if err != nil {
			return fmt.Errorf("failed to insert task board: %w", err)
		}
		return nil
	})
	if err != nil {
		return 0, fmt.Errorf("can't add task board: %w", err)
	}
	return id, nil
}

// ReorderTaskBoards replaces the complete rail order in one transaction. Locking and comparing
// the current id set before any UPDATE makes a stale/partial request a no-op instead of silently
// leaving new boards at an arbitrary position.
func (s *Store) ReorderTaskBoards(ctx context.Context, ids []int) ([]entity.TaskBoardEntry, error) {
	var boards []entity.TaskBoardEntry
	err := s.txFunc(ctx, func(ctx context.Context, rep dependency.Repository) error {
		db := rep.DB()
		existing, err := storeutil.QueryScalarListNamed[int](ctx, db,
			`SELECT id FROM task_board ORDER BY id FOR UPDATE`, map[string]any{})
		if err != nil {
			return fmt.Errorf("failed to lock task boards: %w", err)
		}
		if len(ids) != len(existing) {
			return entity.ErrTaskBoardSetMismatch
		}
		current := make(map[int]struct{}, len(existing))
		for _, id := range existing {
			current[id] = struct{}{}
		}
		seen := make(map[int]struct{}, len(ids))
		for _, id := range ids {
			if id <= 0 {
				return entity.ErrTaskBoardSetMismatch
			}
			if _, ok := current[id]; !ok {
				return entity.ErrTaskBoardSetMismatch
			}
			if _, duplicate := seen[id]; duplicate {
				return entity.ErrTaskBoardSetMismatch
			}
			seen[id] = struct{}{}
		}
		for position, id := range ids {
			if err := storeutil.ExecNamed(ctx, db,
				`UPDATE task_board SET position = :position WHERE id = :id`,
				map[string]any{"position": position, "id": id}); err != nil {
				return fmt.Errorf("failed to set task board %d position: %w", id, err)
			}
		}
		boards, err = listTaskBoards(ctx, db)
		if err != nil {
			return fmt.Errorf("failed to read reordered task boards: %w", err)
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("can't reorder task boards: %w", err)
	}
	return boards, nil
}

// RenameTaskBoard renames any board, seeded ones included (their slug stays). sql.ErrNoRows when the
// board is missing; existence is read first because renaming to the same name affects 0 rows.
func (s *Store) RenameTaskBoard(ctx context.Context, id int, name string) error {
	err := s.txFunc(ctx, func(ctx context.Context, rep dependency.Repository) error {
		n, err := storeutil.QueryCountNamed(ctx, rep.DB(),
			`SELECT COUNT(*) FROM task_board WHERE id = :id`, map[string]any{"id": id})
		if err != nil {
			return fmt.Errorf("failed to check task board existence: %w", err)
		}
		if n == 0 {
			return sql.ErrNoRows
		}
		return storeutil.ExecNamed(ctx, rep.DB(),
			`UPDATE task_board SET name = :name WHERE id = :id`, map[string]any{"name": name, "id": id})
	})
	if err != nil {
		return fmt.Errorf("can't rename task board: %w", err)
	}
	return nil
}

// boardMoveRow is one active task of a board being deleted.
type boardMoveRow struct {
	Id       int               `db:"id"`
	Status   entity.TaskStatus `db:"status"`
	Position int               `db:"position"`
}

// boardColumnEnd is the next free position of one target column.
type boardColumnEnd struct {
	Status entity.TaskStatus `db:"status"`
	Next   int               `db:"next_pos"`
}

// DeleteTaskBoard deletes a board in one transaction. A board with tasks (archived included) needs
// moveToId: its active tasks are appended to the END of the same-status column of the target, in
// their current order; archived tasks change board only (their position is frozen out of band).
//
// Errors: entity.ErrTaskBoardNotFound (no such board), entity.ErrTaskBoardLast (it is the only
// board), entity.ErrTaskBoardNotEmpty (tasks and no moveToId), entity.ErrTaskBoardMoveTarget
// (moveToId is the board itself or does not exist). moveToId is ignored for an empty board.
func (s *Store) DeleteTaskBoard(ctx context.Context, id, moveToId int) error {
	err := s.txFunc(ctx, func(ctx context.Context, rep dependency.Repository) error {
		db := rep.DB()
		exists, err := storeutil.QueryCountNamed(ctx, db,
			`SELECT COUNT(*) FROM task_board WHERE id = :id`, map[string]any{"id": id})
		if err != nil {
			return fmt.Errorf("failed to check task board existence: %w", err)
		}
		if exists == 0 {
			return entity.ErrTaskBoardNotFound
		}
		total, err := storeutil.QueryCountNamed(ctx, db, `SELECT COUNT(*) FROM task_board`, map[string]any{})
		if err != nil {
			return fmt.Errorf("failed to count task boards: %w", err)
		}
		if total <= 1 {
			return entity.ErrTaskBoardLast
		}
		tasks, err := storeutil.QueryCountNamed(ctx, db,
			`SELECT COUNT(*) FROM task WHERE board_id = :id`, map[string]any{"id": id})
		if err != nil {
			return fmt.Errorf("failed to count board tasks: %w", err)
		}
		if tasks > 0 {
			if moveToId <= 0 {
				return entity.ErrTaskBoardNotEmpty
			}
			if moveToId == id {
				return entity.ErrTaskBoardMoveTarget
			}
			target, err := resolveTaskBoard(ctx, db, entity.TaskBoardRef{Id: moveToId})
			if errors.Is(err, entity.ErrTaskBoardNotFound) {
				return entity.ErrTaskBoardMoveTarget
			}
			if err != nil {
				return err
			}
			if err := moveBoardTasks(ctx, db, id, target); err != nil {
				return err
			}
		}
		if err := storeutil.ExecNamed(ctx, db,
			`DELETE FROM task_board WHERE id = :id`, map[string]any{"id": id}); err != nil {
			return fmt.Errorf("failed to delete task board: %w", err)
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("can't delete task board: %w", err)
	}
	return nil
}

// moveBoardTasks re-homes every task of board `from` onto `target`: active ones appended per status
// column (order kept), archived ones relabelled only.
func moveBoardTasks(ctx context.Context, db dependency.DB, from int, target boardRow) error {
	ends, err := storeutil.QueryListNamed[boardColumnEnd](ctx, db, `
		SELECT status, MAX(position) + 1 AS next_pos
		FROM task
		WHERE board_id = :to AND archived_at IS NULL
		GROUP BY status`, map[string]any{"to": target.Id})
	if err != nil {
		return fmt.Errorf("failed to read target board columns: %w", err)
	}
	next := make(map[entity.TaskStatus]int, len(ends))
	for _, e := range ends {
		next[e.Status] = e.Next
	}
	active, err := storeutil.QueryListNamed[boardMoveRow](ctx, db, `
		SELECT id, status, position
		FROM task
		WHERE board_id = :from AND archived_at IS NULL
		ORDER BY status, position, id`, map[string]any{"from": from})
	if err != nil {
		return fmt.Errorf("failed to read board tasks: %w", err)
	}
	legacy := string(target.legacy())
	for _, t := range active {
		pos := next[t.Status]
		next[t.Status] = pos + 1
		if err := storeutil.ExecNamed(ctx, db,
			`UPDATE task SET board_id = :to, board = :board, position = :pos WHERE id = :id`,
			map[string]any{"to": target.Id, "board": legacy, "pos": pos, "id": t.Id}); err != nil {
			return fmt.Errorf("failed to move task %d: %w", t.Id, err)
		}
	}
	if err := storeutil.ExecNamed(ctx, db, `
		UPDATE task SET board_id = :to, board = :board
		WHERE board_id = :from AND archived_at IS NOT NULL`,
		map[string]any{"to": target.Id, "board": legacy, "from": from}); err != nil {
		return fmt.Errorf("failed to move archived board tasks: %w", err)
	}
	return nil
}

// HealTaskBoardIds realigns board_id with task.board for rows whose board is a seeded slug and
// disagrees — the drift a pre-0420 binary leaves when its MoveTask rewrites only `board` (deploy
// overlap, rollback). updated_at is kept: this is a repair, not an edit. Returns rows fixed.
func (s *Store) HealTaskBoardIds(ctx context.Context) (int64, error) {
	n, err := storeutil.ExecNamedRows(ctx, s.DB, `
		UPDATE task t
		JOIN task_board b ON b.slug = t.board
		SET t.board_id = b.id, t.updated_at = t.updated_at
		WHERE t.board_id <> b.id`, map[string]any{})
	if err != nil {
		return 0, fmt.Errorf("can't heal task board ids: %w", err)
	}
	return n, nil
}
