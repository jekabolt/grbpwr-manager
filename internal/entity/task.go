package entity

import (
	"database/sql"
	"time"
)

// TaskBoard is the department lane a kanban task lives in. Stored verbatim.
type TaskBoard string

const (
	TaskBoardDevelopment TaskBoard = "development"
	TaskBoardDesign      TaskBoard = "design"
	TaskBoardMarketing   TaskBoard = "marketing"
	TaskBoardProduction  TaskBoard = "production"
	TaskBoardSourcing    TaskBoard = "sourcing"
	TaskBoardContent     TaskBoard = "content"
)

// ValidTaskBoards is the set of accepted task boards.
var ValidTaskBoards = map[TaskBoard]bool{
	TaskBoardDevelopment: true,
	TaskBoardDesign:      true,
	TaskBoardMarketing:   true,
	TaskBoardProduction:  true,
	TaskBoardSourcing:    true,
	TaskBoardContent:     true,
}

// TaskStatus is the kanban column a task sits in.
type TaskStatus string

const (
	TaskStatusBacklog    TaskStatus = "backlog"
	TaskStatusTodo       TaskStatus = "todo"
	TaskStatusInProgress TaskStatus = "in_progress"
	TaskStatusReview     TaskStatus = "review"
	TaskStatusDone       TaskStatus = "done"
)

// ValidTaskStatuses is the set of accepted task statuses.
var ValidTaskStatuses = map[TaskStatus]bool{
	TaskStatusBacklog:    true,
	TaskStatusTodo:       true,
	TaskStatusInProgress: true,
	TaskStatusReview:     true,
	TaskStatusDone:       true,
}

// TaskPriority is a task's priority; unknown = unset.
type TaskPriority string

const (
	TaskPriorityUnknown TaskPriority = "unknown"
	TaskPriorityLow     TaskPriority = "low"
	TaskPriorityMedium  TaskPriority = "medium"
	TaskPriorityHigh    TaskPriority = "high"
	TaskPriorityUrgent  TaskPriority = "urgent"
)

// ValidTaskPriorities is the set of accepted task priorities.
var ValidTaskPriorities = map[TaskPriority]bool{
	TaskPriorityUnknown: true,
	TaskPriorityLow:     true,
	TaskPriorityMedium:  true,
	TaskPriorityHigh:    true,
	TaskPriorityUrgent:  true,
}

// TaskInsert is the writable CONTENT of a task. Placement (board/status/position)
// and server-stamped fields (id, created_by, timestamps) live on Task, not here.
type TaskInsert struct {
	Title       string         `db:"title"`
	Description sql.NullString `db:"description"`
	Assignee    string         `db:"assignee"`
	Priority    TaskPriority   `db:"priority"`
	DueDate     sql.NullTime   `db:"due_date"`
	TechCardId  sql.NullInt32  `db:"tech_card_id"`
	ProductId   sql.NullInt32  `db:"product_id"`
	OrderUuid   sql.NullString `db:"order_uuid"`
	ArchiveId   sql.NullInt32  `db:"archive_id"`
	Labels      []string       `db:"-"`
	MediaIds    []int          `db:"-"`
}

// Task is a stored kanban card: content (TaskInsert) + placement + resolved media
// + server-stamped identity/timestamps.
type Task struct {
	Id int `db:"id"`
	TaskInsert
	Board     TaskBoard   `db:"board"`
	Status    TaskStatus  `db:"status"`
	Position  int         `db:"position"`
	Media     []MediaFull `db:"-"`
	CreatedBy string      `db:"created_by"`
	CreatedAt time.Time   `db:"created_at"`
	UpdatedAt time.Time   `db:"updated_at"`
	// ArchivedAt is the soft-archive marker: Valid = archived (hidden from the
	// board and default list, but restorable); invalid/NULL = active.
	ArchivedAt sql.NullTime        `db:"archived_at"`
	Checklist  []TaskChecklistItem `db:"-"`
}

// TaskChecklistItem is one row of a task's checklist — a lightweight subtask with
// a done flag. Managed by dedicated add/toggle/delete operations, never wiped by a
// content edit.
type TaskChecklistItem struct {
	Id        int       `db:"id"`
	TaskId    int       `db:"task_id"`
	Content   string    `db:"content"`
	IsDone    bool      `db:"is_done"`
	Position  int       `db:"position"`
	CreatedAt time.Time `db:"created_at"`
}

// TaskListFilter narrows a ListTasks query. Zero-value fields are "no filter".
type TaskListFilter struct {
	Board           TaskBoard  // "" = all boards
	Status          TaskStatus // "" = all columns
	Assignee        string     // "" = any assignee
	TechCardId      int        // 0 = no filter
	ProductId       int        // 0 = no filter
	IncludeArchived bool       // false = active only (default); true = include archived
	Limit           int
	Offset          int
	OrderFactor     OrderFactor
}

// TaskCommentInsert is the writable payload for a task comment. Author is stamped
// server-side from the caller's JWT, not carried here.
type TaskCommentInsert struct {
	TaskId int    `db:"task_id"`
	Body   string `db:"body"`
}

// TaskComment is a stored comment on a task.
type TaskComment struct {
	Id        int       `db:"id"`
	TaskId    int       `db:"task_id"`
	Author    string    `db:"author"`
	Body      string    `db:"body"`
	CreatedAt time.Time `db:"created_at"`
}
