// Package task implements internal team kanban (task manager) storage.
package task

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/jekabolt/grbpwr-manager/internal/dependency"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/jekabolt/grbpwr-manager/internal/store/storeutil"
)

// Pagination bounds for ListTasks. A board fetch wants the whole column set, so
// the default is generous; the cap still guards against an unbounded scan.
const (
	defaultPageLimit = 200
	maxPageLimit     = 1000
)

// TxFunc executes f within a transaction.
type TxFunc func(ctx context.Context, f func(context.Context, dependency.Repository) error) error

// Store implements dependency.Tasks.
type Store struct {
	storeutil.Base
	txFunc TxFunc
}

// New creates a new task store.
func New(base storeutil.Base, txFunc TxFunc) *Store {
	return &Store{Base: base, txFunc: txFunc}
}

// AddTask inserts a task (content + placement) with its labels and media,
// appending it to the end of its (board,status) column. Returns the new id.
func (s *Store) AddTask(ctx context.Context, t *entity.Task) (int, error) {
	var id int
	err := s.txFunc(ctx, func(ctx context.Context, rep dependency.Repository) error {
		// Append to the end of the target column. Archived tasks are excluded from
		// position accounting (they are outside the visible sequence).
		pos, err := storeutil.QueryCountNamed(ctx, rep.DB(),
			`SELECT COALESCE(MAX(position)+1, 0) FROM task WHERE board = :board AND status = :status AND archived_at IS NULL`,
			map[string]any{"board": string(t.Board), "status": string(t.Status)})
		if err != nil {
			return fmt.Errorf("failed to compute task position: %w", err)
		}
		params := taskContentParams(&t.TaskInsert)
		params["board"] = string(t.Board)
		params["status"] = string(t.Status)
		params["position"] = pos
		params["createdBy"] = t.CreatedBy
		id, err = storeutil.ExecNamedLastId(ctx, rep.DB(), `
			INSERT INTO task (title, description, board, status, position, assignee, priority, due_date, start_date, created_by, tech_card_id, product_id, order_uuid, archive_id, fitting_id, production_run_id, sample_id, started_at)
			VALUES (:title, :description, :board, :status, :position, :assignee, :priority, :dueDate, :startDate, :createdBy, :techCardId, :productId, :orderUuid, :archiveId, :fittingId, :productionRunId, :sampleId,
				CASE WHEN :status = 'in_progress' THEN UTC_TIMESTAMP() ELSE NULL END)`,
			params)
		if err != nil {
			return fmt.Errorf("failed to insert task: %w", err)
		}
		if err := insertTaskLabels(ctx, rep.DB(), id, t.Labels); err != nil {
			return err
		}
		return insertTaskMedia(ctx, rep.DB(), id, t.MediaIds)
	})
	if err != nil {
		return 0, fmt.Errorf("can't add task: %w", err)
	}
	return id, nil
}

// UpdateTask replaces a task's CONTENT and its labels/media. Placement
// (board/status/position) and created_by are left untouched. Returns
// sql.ErrNoRows when no task with the given id exists.
func (s *Store) UpdateTask(ctx context.Context, id int, t *entity.TaskInsert) error {
	err := s.txFunc(ctx, func(ctx context.Context, rep dependency.Repository) error {
		exists, err := storeutil.QueryCountNamed(ctx, rep.DB(),
			`SELECT COUNT(*) FROM task WHERE id = :id`, map[string]any{"id": id})
		if err != nil {
			return fmt.Errorf("failed to check task existence: %w", err)
		}
		if exists == 0 {
			return sql.ErrNoRows
		}
		params := taskContentParams(t)
		params["id"] = id
		if err := storeutil.ExecNamed(ctx, rep.DB(), `
			UPDATE task SET
				title = :title,
				description = :description,
				assignee = :assignee,
				priority = :priority,
				due_date = :dueDate,
				start_date = :startDate,
				tech_card_id = :techCardId,
				product_id = :productId,
				order_uuid = :orderUuid,
				archive_id = :archiveId,
				fitting_id = :fittingId,
				production_run_id = :productionRunId,
				sample_id = :sampleId
			WHERE id = :id`, params); err != nil {
			return fmt.Errorf("failed to update task: %w", err)
		}
		if err := storeutil.ExecNamed(ctx, rep.DB(),
			`DELETE FROM task_label WHERE task_id = :id`, map[string]any{"id": id}); err != nil {
			return fmt.Errorf("failed to clear task labels: %w", err)
		}
		if err := storeutil.ExecNamed(ctx, rep.DB(),
			`DELETE FROM task_media WHERE task_id = :id`, map[string]any{"id": id}); err != nil {
			return fmt.Errorf("failed to clear task media: %w", err)
		}
		if err := insertTaskLabels(ctx, rep.DB(), id, t.Labels); err != nil {
			return err
		}
		return insertTaskMedia(ctx, rep.DB(), id, t.MediaIds)
	})
	if err != nil {
		return fmt.Errorf("can't update task: %w", err)
	}
	return nil
}

// MoveTask changes a task's placement: board (empty = keep current), column
// (status), and position within that column, re-sequencing sibling positions so
// each column stays a gap-free 0..n-1 sequence. Returns sql.ErrNoRows when no
// task with the given id exists.
func (s *Store) MoveTask(ctx context.Context, id int, board entity.TaskBoard, status entity.TaskStatus, position int) error {
	err := s.txFunc(ctx, func(ctx context.Context, rep dependency.Repository) error {
		// Only active tasks have a meaningful position in the gap-free sequence.
		// An archived task carries a frozen, out-of-band position, so moving it
		// would corrupt the active column — require it be active (NotFound
		// otherwise; unarchive first).
		cur, err := storeutil.QueryNamedOne[taskPlacement](ctx, rep.DB(),
			`SELECT board, status, position FROM task WHERE id = :id AND archived_at IS NULL`, map[string]any{"id": id})
		if err != nil {
			return err // wraps sql.ErrNoRows when missing or archived
		}
		if board == "" {
			board = cur.Board
		}

		// 1) Close the gap left in the source column. Archived tasks are outside
		// the position sequence, so they are excluded here and below.
		if err := storeutil.ExecNamed(ctx, rep.DB(), `
			UPDATE task SET position = position - 1
			WHERE board = :board AND status = :status AND archived_at IS NULL AND position > :pos`,
			map[string]any{"board": string(cur.Board), "status": string(cur.Status), "pos": cur.Position}); err != nil {
			return fmt.Errorf("failed to compact source column: %w", err)
		}

		// 2) Clamp the target position to the target column's current size
		// (excluding this task, which is not yet placed there).
		n, err := storeutil.QueryCountNamed(ctx, rep.DB(), `
			SELECT COUNT(*) FROM task WHERE board = :board AND status = :status AND archived_at IS NULL AND id != :id`,
			map[string]any{"board": string(board), "status": string(status), "id": id})
		if err != nil {
			return fmt.Errorf("failed to size target column: %w", err)
		}
		if position < 0 {
			position = 0
		}
		if position > n {
			position = n
		}

		// 3) Open a slot in the target column.
		if err := storeutil.ExecNamed(ctx, rep.DB(), `
			UPDATE task SET position = position + 1
			WHERE board = :board AND status = :status AND archived_at IS NULL AND id != :id AND position >= :pos`,
			map[string]any{"board": string(board), "status": string(status), "id": id, "pos": position}); err != nil {
			return fmt.Errorf("failed to open target slot: %w", err)
		}

		// 4) Place the task.
		if err := storeutil.ExecNamed(ctx, rep.DB(), `
			UPDATE task SET board = :board, status = :status, position = :pos WHERE id = :id`,
			map[string]any{"board": string(board), "status": string(status), "pos": position, "id": id}); err != nil {
			return fmt.Errorf("failed to place task: %w", err)
		}

		// 5) Stamp the actual start the FIRST time the card enters in_progress. The
		// `started_at IS NULL` guard makes it idempotent — a later re-entry keeps the
		// original start.
		if status == entity.TaskStatusInProgress {
			if err := storeutil.ExecNamed(ctx, rep.DB(), `
				UPDATE task SET started_at = UTC_TIMESTAMP() WHERE id = :id AND started_at IS NULL`,
				map[string]any{"id": id}); err != nil {
				return fmt.Errorf("failed to stamp task start: %w", err)
			}
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("can't move task: %w", err)
	}
	return nil
}

// DeleteTask deletes a task by id (labels, media, comments, checklist cascade).
func (s *Store) DeleteTask(ctx context.Context, id int) error {
	if err := storeutil.ExecNamed(ctx, s.DB,
		`DELETE FROM task WHERE id = :id`, map[string]any{"id": id}); err != nil {
		return fmt.Errorf("failed to delete task: %w", err)
	}
	return nil
}

// ArchiveTask soft-archives an active task (hidden from the board, restorable) and
// compacts its former (board,status) column so active positions stay gap-free.
// Returns sql.ErrNoRows when no active task with the given id exists.
func (s *Store) ArchiveTask(ctx context.Context, id int) error {
	err := s.txFunc(ctx, func(ctx context.Context, rep dependency.Repository) error {
		cur, err := storeutil.QueryNamedOne[taskPlacement](ctx, rep.DB(),
			`SELECT board, status, position FROM task WHERE id = :id AND archived_at IS NULL`,
			map[string]any{"id": id})
		if err != nil {
			return err // wraps sql.ErrNoRows when missing or already archived
		}
		// Mark archived first, removing it from the active position sequence, then
		// compact the gap it left behind (the archived row is now excluded).
		if err := storeutil.ExecNamed(ctx, rep.DB(),
			`UPDATE task SET archived_at = UTC_TIMESTAMP() WHERE id = :id`, map[string]any{"id": id}); err != nil {
			return fmt.Errorf("failed to archive task: %w", err)
		}
		if err := storeutil.ExecNamed(ctx, rep.DB(), `
			UPDATE task SET position = position - 1
			WHERE board = :board AND status = :status AND archived_at IS NULL AND position > :pos`,
			map[string]any{"board": string(cur.Board), "status": string(cur.Status), "pos": cur.Position}); err != nil {
			return fmt.Errorf("failed to compact column after archive: %w", err)
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("can't archive task: %w", err)
	}
	return nil
}

// UnarchiveTask restores an archived task, appending it to the end of its
// (board,status) column. Returns sql.ErrNoRows when no archived task with the id
// exists.
func (s *Store) UnarchiveTask(ctx context.Context, id int) error {
	err := s.txFunc(ctx, func(ctx context.Context, rep dependency.Repository) error {
		cur, err := storeutil.QueryNamedOne[taskPlacement](ctx, rep.DB(),
			`SELECT board, status, position FROM task WHERE id = :id AND archived_at IS NOT NULL`,
			map[string]any{"id": id})
		if err != nil {
			return err // wraps sql.ErrNoRows when missing or not archived
		}
		pos, err := storeutil.QueryCountNamed(ctx, rep.DB(),
			`SELECT COALESCE(MAX(position)+1, 0) FROM task WHERE board = :board AND status = :status AND archived_at IS NULL`,
			map[string]any{"board": string(cur.Board), "status": string(cur.Status)})
		if err != nil {
			return fmt.Errorf("failed to compute unarchive position: %w", err)
		}
		if err := storeutil.ExecNamed(ctx, rep.DB(),
			`UPDATE task SET archived_at = NULL, position = :pos WHERE id = :id`,
			map[string]any{"pos": pos, "id": id}); err != nil {
			return fmt.Errorf("failed to unarchive task: %w", err)
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("can't unarchive task: %w", err)
	}
	return nil
}

// AddTaskChecklistItem appends a checklist item to a task, returning the new id.
// Returns sql.ErrNoRows when no task with the given id exists.
func (s *Store) AddTaskChecklistItem(ctx context.Context, taskID int, content string) (int, error) {
	var id int
	err := s.txFunc(ctx, func(ctx context.Context, rep dependency.Repository) error {
		exists, err := storeutil.QueryCountNamed(ctx, rep.DB(),
			`SELECT COUNT(*) FROM task WHERE id = :id`, map[string]any{"id": taskID})
		if err != nil {
			return fmt.Errorf("failed to check task existence: %w", err)
		}
		if exists == 0 {
			return sql.ErrNoRows
		}
		pos, err := storeutil.QueryCountNamed(ctx, rep.DB(),
			`SELECT COALESCE(MAX(position)+1, 0) FROM task_checklist_item WHERE task_id = :id`,
			map[string]any{"id": taskID})
		if err != nil {
			return fmt.Errorf("failed to compute checklist position: %w", err)
		}
		id, err = storeutil.ExecNamedLastId(ctx, rep.DB(),
			`INSERT INTO task_checklist_item (task_id, content, position) VALUES (:taskId, :content, :position)`,
			map[string]any{"taskId": taskID, "content": content, "position": pos})
		if err != nil {
			return fmt.Errorf("failed to insert checklist item: %w", err)
		}
		return nil
	})
	if err != nil {
		return 0, fmt.Errorf("can't add task checklist item: %w", err)
	}
	return id, nil
}

// SetTaskChecklistItemDone sets a checklist item's done flag. Returns
// sql.ErrNoRows when no item with the given id exists.
func (s *Store) SetTaskChecklistItemDone(ctx context.Context, id int, done bool) error {
	err := s.txFunc(ctx, func(ctx context.Context, rep dependency.Repository) error {
		exists, err := storeutil.QueryCountNamed(ctx, rep.DB(),
			`SELECT COUNT(*) FROM task_checklist_item WHERE id = :id`, map[string]any{"id": id})
		if err != nil {
			return fmt.Errorf("failed to check checklist item existence: %w", err)
		}
		if exists == 0 {
			return sql.ErrNoRows
		}
		return storeutil.ExecNamed(ctx, rep.DB(),
			`UPDATE task_checklist_item SET is_done = :done WHERE id = :id`,
			map[string]any{"done": done, "id": id})
	})
	if err != nil {
		return fmt.Errorf("can't set task checklist item done: %w", err)
	}
	return nil
}

// DeleteTaskChecklistItem removes a checklist item by id (idempotent).
func (s *Store) DeleteTaskChecklistItem(ctx context.Context, id int) error {
	if err := storeutil.ExecNamed(ctx, s.DB,
		`DELETE FROM task_checklist_item WHERE id = :id`, map[string]any{"id": id}); err != nil {
		return fmt.Errorf("failed to delete task checklist item: %w", err)
	}
	return nil
}

// GetTaskById returns a task with its labels and resolved media.
func (s *Store) GetTaskById(ctx context.Context, id int) (*entity.Task, error) {
	t, err := storeutil.QueryNamedOne[entity.Task](ctx, s.DB,
		`SELECT * FROM task WHERE id = :id`, map[string]any{"id": id})
	if err != nil {
		return nil, fmt.Errorf("failed to get task: %w", err)
	}
	labels, err := s.labelsByTaskIds(ctx, []int{id})
	if err != nil {
		return nil, err
	}
	media, err := s.mediaByTaskIds(ctx, []int{id})
	if err != nil {
		return nil, err
	}
	checklist, err := s.checklistByTaskIds(ctx, []int{id})
	if err != nil {
		return nil, err
	}
	t.Labels = labels[id]
	t.Media = media[id]
	t.Checklist = checklist[id]
	return &t, nil
}

// ListTasks returns a paged, optionally filtered list of tasks (with labels and
// resolved media) plus the total number of matching tasks (ignoring pagination).
// Rows are clustered by (board,status) and ordered by position so a board renders
// directly.
func (s *Store) ListTasks(ctx context.Context, f entity.TaskListFilter) ([]entity.Task, int, error) {
	limit, offset := clampPagination(f.Limit, f.Offset)

	filterParams := map[string]any{}
	where := ""
	if f.Board != "" {
		where += " AND board = :board"
		filterParams["board"] = string(f.Board)
	}
	if f.Status != "" {
		where += " AND status = :status"
		filterParams["status"] = string(f.Status)
	}
	if f.Assignee != "" {
		where += " AND assignee = :assignee"
		filterParams["assignee"] = f.Assignee
	}
	if f.TechCardId != 0 {
		where += " AND tech_card_id = :techCardId"
		filterParams["techCardId"] = f.TechCardId
	}
	if f.ProductId != 0 {
		where += " AND product_id = :productId"
		filterParams["productId"] = f.ProductId
	}
	// Active-only by default: archived tasks are hidden from the board unless
	// explicitly requested.
	if !f.IncludeArchived {
		where += " AND archived_at IS NULL"
	}

	total, err := storeutil.QueryCountNamed(ctx, s.DB,
		fmt.Sprintf(`SELECT COUNT(*) FROM task WHERE 1=1%s`, where), filterParams)
	if err != nil {
		return nil, 0, fmt.Errorf("can't count tasks: %w", err)
	}

	filterParams["limit"] = limit
	filterParams["offset"] = offset
	query := fmt.Sprintf(`
		SELECT * FROM task
		WHERE 1=1%s
		ORDER BY board, status, position %s, id %s
		LIMIT :limit OFFSET :offset`, where, f.OrderFactor.String(), f.OrderFactor.String())

	tasks, err := storeutil.QueryListNamed[entity.Task](ctx, s.DB, query, filterParams)
	if err != nil {
		return nil, 0, fmt.Errorf("can't list tasks: %w", err)
	}
	if len(tasks) == 0 {
		return tasks, total, nil
	}
	ids := make([]int, 0, len(tasks))
	for _, t := range tasks {
		ids = append(ids, t.Id)
	}
	labels, err := s.labelsByTaskIds(ctx, ids)
	if err != nil {
		return nil, 0, err
	}
	media, err := s.mediaByTaskIds(ctx, ids)
	if err != nil {
		return nil, 0, err
	}
	checklist, err := s.checklistByTaskIds(ctx, ids)
	if err != nil {
		return nil, 0, err
	}
	for i := range tasks {
		tasks[i].Labels = labels[tasks[i].Id]
		tasks[i].Media = media[tasks[i].Id]
		tasks[i].Checklist = checklist[tasks[i].Id]
	}
	return tasks, total, nil
}

// AddTaskComment appends a comment to a task with the given author, returning the
// new id. Returns sql.ErrNoRows when no task with the comment's task_id exists.
func (s *Store) AddTaskComment(ctx context.Context, c *entity.TaskCommentInsert, author string) (int, error) {
	var id int
	err := s.txFunc(ctx, func(ctx context.Context, rep dependency.Repository) error {
		exists, err := storeutil.QueryCountNamed(ctx, rep.DB(),
			`SELECT COUNT(*) FROM task WHERE id = :id`, map[string]any{"id": c.TaskId})
		if err != nil {
			return fmt.Errorf("failed to check task existence: %w", err)
		}
		if exists == 0 {
			return sql.ErrNoRows
		}
		id, err = storeutil.ExecNamedLastId(ctx, rep.DB(),
			`INSERT INTO task_comment (task_id, author, body) VALUES (:taskId, :author, :body)`,
			map[string]any{"taskId": c.TaskId, "author": author, "body": c.Body})
		if err != nil {
			return fmt.Errorf("failed to insert task comment: %w", err)
		}
		return nil
	})
	if err != nil {
		return 0, fmt.Errorf("can't add task comment: %w", err)
	}
	return id, nil
}

// ListTaskComments returns a task's comments, oldest first.
func (s *Store) ListTaskComments(ctx context.Context, taskID int) ([]entity.TaskComment, error) {
	comments, err := storeutil.QueryListNamed[entity.TaskComment](ctx, s.DB,
		`SELECT id, task_id, author, body, created_at FROM task_comment WHERE task_id = :taskId ORDER BY created_at, id`,
		map[string]any{"taskId": taskID})
	if err != nil {
		return nil, fmt.Errorf("can't list task comments: %w", err)
	}
	return comments, nil
}

// taskPlacement is the placement subset of a task row, read for a move.
type taskPlacement struct {
	Board    entity.TaskBoard  `db:"board"`
	Status   entity.TaskStatus `db:"status"`
	Position int               `db:"position"`
}

func clampPagination(limit, offset int) (int, int) {
	if limit <= 0 {
		limit = defaultPageLimit
	}
	if limit > maxPageLimit {
		limit = maxPageLimit
	}
	if offset < 0 {
		offset = 0
	}
	return limit, offset
}

func taskContentParams(t *entity.TaskInsert) map[string]any {
	return map[string]any{
		"title":           t.Title,
		"description":     t.Description,
		"assignee":        t.Assignee,
		"priority":        string(t.Priority),
		"dueDate":         t.DueDate,
		"startDate":       t.StartDate,
		"techCardId":      t.TechCardId,
		"productId":       t.ProductId,
		"orderUuid":       t.OrderUuid,
		"archiveId":       t.ArchiveId,
		"fittingId":       t.FittingId,
		"productionRunId": t.ProductionRunId,
		"sampleId":        t.SampleId,
	}
}

func insertTaskLabels(ctx context.Context, db dependency.DB, taskID int, labels []string) error {
	if len(labels) == 0 {
		return nil
	}
	rows := make([]map[string]any, 0, len(labels))
	for i, l := range labels {
		rows = append(rows, map[string]any{
			"task_id":       taskID,
			"label":         l,
			"display_order": i,
		})
	}
	if err := storeutil.BulkInsert(ctx, db, "task_label", rows); err != nil {
		return fmt.Errorf("failed to insert task labels: %w", err)
	}
	return nil
}

func insertTaskMedia(ctx context.Context, db dependency.DB, taskID int, mediaIDs []int) error {
	if len(mediaIDs) == 0 {
		return nil
	}
	rows := make([]map[string]any, 0, len(mediaIDs))
	for i, mid := range mediaIDs {
		rows = append(rows, map[string]any{
			"task_id":       taskID,
			"media_id":      mid,
			"display_order": i,
		})
	}
	if err := storeutil.BulkInsert(ctx, db, "task_media", rows); err != nil {
		return fmt.Errorf("failed to insert task media: %w", err)
	}
	return nil
}

type taskLabelRow struct {
	TaskID int    `db:"task_id"`
	Label  string `db:"label"`
}

func (s *Store) labelsByTaskIds(ctx context.Context, ids []int) (map[int][]string, error) {
	if len(ids) == 0 {
		return map[int][]string{}, nil
	}
	rows, err := storeutil.QueryListNamed[taskLabelRow](ctx, s.DB, `
		SELECT task_id, label
		FROM task_label
		WHERE task_id IN (:ids)
		ORDER BY task_id, display_order`, map[string]any{"ids": ids})
	if err != nil {
		return nil, fmt.Errorf("can't load task labels: %w", err)
	}
	out := make(map[int][]string, len(ids))
	for _, r := range rows {
		out[r.TaskID] = append(out[r.TaskID], r.Label)
	}
	return out, nil
}

type taskMediaRow struct {
	TaskID int `db:"task_id"`
	entity.MediaFull
}

func (s *Store) mediaByTaskIds(ctx context.Context, ids []int) (map[int][]entity.MediaFull, error) {
	if len(ids) == 0 {
		return map[int][]entity.MediaFull{}, nil
	}
	rows, err := storeutil.QueryListNamed[taskMediaRow](ctx, s.DB, `
		SELECT tm.task_id, m.*
		FROM task_media tm
		JOIN media m ON m.id = tm.media_id
		WHERE tm.task_id IN (:ids)
		ORDER BY tm.task_id, tm.display_order`, map[string]any{"ids": ids})
	if err != nil {
		return nil, fmt.Errorf("can't load task media: %w", err)
	}
	out := make(map[int][]entity.MediaFull, len(ids))
	for _, r := range rows {
		out[r.TaskID] = append(out[r.TaskID], r.MediaFull)
	}
	return out, nil
}

func (s *Store) checklistByTaskIds(ctx context.Context, ids []int) (map[int][]entity.TaskChecklistItem, error) {
	if len(ids) == 0 {
		return map[int][]entity.TaskChecklistItem{}, nil
	}
	rows, err := storeutil.QueryListNamed[entity.TaskChecklistItem](ctx, s.DB, `
		SELECT id, task_id, content, is_done, position, created_at
		FROM task_checklist_item
		WHERE task_id IN (:ids)
		ORDER BY task_id, position, id`, map[string]any{"ids": ids})
	if err != nil {
		return nil, fmt.Errorf("can't load task checklist: %w", err)
	}
	out := make(map[int][]entity.TaskChecklistItem, len(ids))
	for i := range rows {
		out[rows[i].TaskId] = append(out[rows[i].TaskId], rows[i])
	}
	return out, nil
}
