package entity

import (
	"database/sql"
	"errors"
	"time"
)

// Отказы, у которых есть ФРАЗА, а не код нарушения ключа. Оба про отношения между двумя живыми
// карточками, поэтому живут здесь, а не рядом с ErrTaskNeedsProjectTopic (та — про семью «проектное
// свойство бывает только у проекта»).
//
// Хендлер обязан проверять их ПОСЛЕ sql.ErrNoRows: «задачи нет» и «так связывать нельзя» — разные
// ответы на разные ошибки человека, и порядок здесь несущий (тот же порядок, что в UpdateTask).
var (
	// ErrTaskParentCycle — предлагаемый родитель является потомком самой задачи (или ею самой).
	// Ловится подъёмом по цепочке parent_task_id ВНУТРИ пишущей SERIALIZABLE-транзакции: только там
	// чтение цепочки запирает строки и закрывает гонку двух встречных SetTaskParent.
	ErrTaskParentCycle = errors.New("a task cannot become a subtask of its own descendant")
	// ErrTaskReverseBlock — обратная связь blocks уже существует (B блокирует A, просят A блокирует
	// B). Это ошибка ВСЕГДА и проверяется одним SELECT. Длинные циклы (A→B→C→A) наоборот терпятся:
	// блокер здесь совет, а не замок, и отказ «эта связь замыкает цикл через 4 задачи» ценой обхода
	// графа на каждую вставку путал бы сильнее самого цикла.
	ErrTaskReverseBlock = errors.New("these tasks already block each other the other way round")
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
	// Assignees — ИСПОЛНИТЕЛИ КАРТОЧКИ, список без «главного» (task_assignee, 0337). Порядок = порядок
	// показа. Заменяются целиком при сохранении, как Labels.
	//
	// Поля Assignee здесь БОЛЬШЕ НЕТ, и колонка task.assignee этим кодом не читается и не пишется.
	// Она осиротела и ждёт отдельной миграции на снос — internal/store/sql/README-pending-drops.md.
	// На проводе поле assignee живёт ещё один релиз алиасом совместимости (см. dto).
	Assignees []string     `db:"-"`
	Priority  TaskPriority `db:"priority"`
	DueDate         sql.NullTime   `db:"due_date"`
	StartDate       sql.NullTime   `db:"start_date"` // planned start (manual); actual start is Task.StartedAt
	TechCardId      sql.NullInt32  `db:"tech_card_id"`
	ProductId       sql.NullInt32  `db:"product_id"`
	OrderUuid       sql.NullString `db:"order_uuid"`
	ArchiveId       sql.NullInt32  `db:"archive_id"`
	FittingId       sql.NullInt32  `db:"fitting_id"`
	ProductionRunId sql.NullInt32  `db:"production_run_id"`
	SampleId        sql.NullInt32  `db:"sample_id"`
	// ProjectTopicId — ВОСЬМАЯ глубокая ссылка (0322): проект библиотеки файлов, в котором
	// происходит эта работа. Колонка, а не вторая таблица связи: задача — единица работы, и работа
	// происходит в ОДНОМ контексте, в отличие от связи «проект ↔ стиль» (0321), где множественны
	// обе стороны.
	//
	// Указывать можно только тему kind='project'. Проверяется в сторе внутри пишущей транзакции —
	// не CHECK'ом (он проверяет всю историю и останавливает старт прода) и не внешним ключом (тема
	// СУЩЕСТВУЕТ, она просто ярлык, и ключ на это не срабатывает вовсе).
	ProjectTopicId sql.NullInt32 `db:"project_topic_id"`
	Labels         []string      `db:"-"`
	MediaIds       []int         `db:"-"`
	// FileIds are library files attached to the card. Separate from MediaIds
	// because the two live in different buckets with opposite privacy: media is
	// public-read on the CDN (it ships to the storefront), library files are
	// private and only ever leave through a short-lived presigned url. The UI
	// merges both into one "attachments" list — the split is a storage fact, not
	// something a person should have to think about.
	FileIds []int `db:"-"`
	// MediaAnnotations — указания, нарисованные на прикреплённых картинках (0313). Живут рядом с
	// MediaIds и заменяются вместе с ними: набор без своей картинки нельзя ни увидеть, ни убрать,
	// поэтому dto отбрасывает такие на входе, а стор пишет наборы строками той же полной замены.
	MediaAnnotations []TaskMediaAnnotations `db:"-"`
}

// TaskMediaAnnotations — указания одной прикреплённой картинки карточки.
//
// ТОТ ЖЕ ПРИМИТИВ, ЧТО НА ЭСКИЗЕ ТЕХ-КАРТЫ И НА СНИМКЕ ШАГА СБОРКИ: TechCardAnnotation, вместе с её
// видами, точками, цветом, пунктиром и штриховкой. Имя типа историческое — указание в системе ОДНО
// (довод 0308/0309), и второй набор видов ради задачи развёл бы их на первой же правке.
//
// PieceLineKey/PieceLineKeys здесь ВСЕГДА пусты: деталей кроя у карточки канбана нет — ни выбрать,
// ни показать, — и dto очищает их на входе, чтобы висящий ключ чужой тех-карты однажды не напечатали.
type TaskMediaAnnotations struct {
	MediaId     int
	Annotations []TechCardAnnotation
}

// Task is a stored kanban card: content (TaskInsert) + placement + resolved media
// + server-stamped identity/timestamps.
type Task struct {
	Id int `db:"id"`
	TaskInsert
	Board    TaskBoard   `db:"board"`
	Status   TaskStatus  `db:"status"`
	Position int         `db:"position"`
	Media    []MediaFull `db:"-"`
	// Files are the resolved library attachments. Resolved by the handler rather
	// than the task store, so the task store never has to know the files store.
	Files     []LibraryFile `db:"-"`
	CreatedBy string        `db:"created_by"`
	CreatedAt time.Time     `db:"created_at"`
	UpdatedAt time.Time     `db:"updated_at"`
	// ArchivedAt is the soft-archive marker: Valid = archived (hidden from the
	// board and default list, but restorable); invalid/NULL = active.
	ArchivedAt sql.NullTime `db:"archived_at"`
	// StartedAt is the actual start: server-stamped the first time the card enters
	// in_progress, never cleared afterwards. Invalid/NULL = not started yet.
	StartedAt sql.NullTime        `db:"started_at"`
	Checklist []TaskChecklistItem `db:"-"`
	// ParentTaskId — родитель-сабтаска (0338). Invalid/NULL = верхний уровень. Меняется ТОЛЬКО через
	// SetTaskParent (и задаётся при AddTask), никогда не приезжает внутри TaskInsert.
	ParentTaskId sql.NullInt32 `db:"parent_task_id"`
	// Links — все связи карточки, обе стороны blocks плюс relates, со ВТОРЫМ КОНЦОМ уже разрешённым.
	Links []TaskLink `db:"-"`
	// SubtaskTotal/SubtaskDone — свёртка АКТИВНЫХ (незаархивированных) детей для карточки на доске.
	SubtaskTotal int `db:"-"`
	SubtaskDone  int `db:"-"`
}

// TaskLinkKind — вид связи В ХРАНИЛИЩЕ. Их ДВА, и это не сокращение контракта: BLOCKED_BY на проводе
// это blocks, прочитанный с другого конца, а не третий вид строки. Одна строка на факт — иначе пара
// могла бы полусуществовать.
type TaskLinkKind string

const (
	TaskLinkKindBlocks  TaskLinkKind = "blocks"
	TaskLinkKindRelates TaskLinkKind = "relates"
)

// ValidTaskLinkKinds is the set of accepted stored link kinds (mirrors chk_task_link_kind).
var ValidTaskLinkKinds = map[TaskLinkKind]bool{
	TaskLinkKindBlocks:  true,
	TaskLinkKindRelates: true,
}

// TaskLinkRole — вид связи С ТОЧКИ ЗРЕНИЯ карточки, в чьём списке эта строка едет. Отличается от
// TaskLinkKind ровно на BlockedBy: он и есть blocks, прочитанный со стороны блокируемой задачи.
type TaskLinkRole string

const (
	TaskLinkRoleBlocks    TaskLinkRole = "blocks"
	TaskLinkRoleBlockedBy TaskLinkRole = "blocked_by"
	TaskLinkRoleRelates   TaskLinkRole = "relates"
)

// TaskLink — одна связь, как её рисует карточка: второй конец уже разрешён в заголовок/статус/доску.
type TaskLink struct {
	// OwnerTaskId — карточка, в чьём списке едет строка (для группировки батч-чтения).
	OwnerTaskId int `db:"owner_task_id"`
	// TaskId — ВТОРОЙ конец связи.
	TaskId   int          `db:"task_id"`
	Role     TaskLinkRole `db:"-"`
	Title    string       `db:"title"`
	Status   TaskStatus   `db:"status"`
	Board    TaskBoard    `db:"board"`
	Archived bool         `db:"archived"`
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

// LibraryFileTask is one task row AS THE FILE CARD DRAWS IT: the #id pill, the title, the column,
// who is on it and when it is due.
//
// НЕ Task, И ЭТО НЕ ЭКОНОМИЯ ПОЛЕЙ. Task несёт содержимое, чек-лист, разрешённые медиа и СВОИ
// вложения — то есть на каждую задачу, к которой прицеплен файл, пришлось бы резолвить ещё один
// список файлов. Карточка файла рисует строку, а не карточку задачи, и читать надо ровно строку.
type LibraryFileTask struct {
	TaskId int        `db:"id"`
	Title  string     `db:"title"`
	Status TaskStatus `db:"status"`
	// Assignees — все, кто на задаче; пусто = её никто не взял (это состояние, а не пропуск).
	// Дозаполняется батч-запросом по собранным id, как и у доски: колонки task.assignee этот код
	// больше не читает (см. README-pending-drops.md).
	Assignees []string `db:"-"`
	// DueDate invalid/NULL = срока нет. У строки тогда просто нет даты — не «сегодня».
	DueDate sql.NullTime `db:"due_date"`
	// Board is the department lane, чтобы строка говорила, ГДЕ живёт работа: один и тот же файл
	// висит на задачах из разных досок одновременно.
	Board TaskBoard `db:"board"`
}

// TaskListFilter narrows a ListTasks query. Zero-value fields are "no filter".
type TaskListFilter struct {
	Board  TaskBoard  // "" = all boards
	Status TaskStatus // "" = all columns
	// Assignee — ЧЛЕНСТВО в списке исполнителей, а не равенство одному полю: «мои» находит и
	// задачи, где я второй. "" = any assignee.
	Assignee        string
	TechCardId      int // 0 = no filter
	ProductId       int        // 0 = no filter
	OrderUuid       string     // "" = no filter
	ArchiveId       int        // 0 = no filter
	FittingId       int        // 0 = no filter
	ProductionRunId int        // 0 = no filter
	SampleId        int        // 0 = no filter
	// ProjectTopicId отвечает на ОБРАТНЫЙ вопрос фазы 0322 — «какие задачи у этого проекта».
	// Фильтр существующего списка, а не отдельный RPC: это тот же список задач, просто суженный, и
	// второй путь к тем же данным завёл бы вторые права, которым нечем помешать разойтись.
	ProjectTopicId int // 0 = no filter
	// ParentTaskId — «сабтаски этой задачи». Фильтр того же списка, а не отдельный RPC (доктрина
	// 0322: не заводить второй путь к тем же данным). 0 = no filter.
	ParentTaskId    int
	IncludeArchived bool // false = active only (default); true = include archived
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
	Id     int    `db:"id"`
	TaskId int    `db:"task_id"`
	Author string `db:"author"` // username НА МОМЕНТ ПИСЬМА: строка-факт, переживает удаление аккаунта
	// AuthorId — ЖИВАЯ ссылка на аккаунт автора (0339). Invalid/NULL = аккаунта больше нет.
	//
	// Право «удалить свою реплику» требует ИМЕННО ПАРЫ «имя совпало И ссылка жива»: UNIQUE на
	// admins.username освобождает имя при удалении аккаунта, и новый однофамилец совпал бы по строке
	// со всей перепиской прежнего. Тот же гейт и тот же довод, что у LibraryFileComment (0316).
	AuthorId  sql.NullInt32 `db:"author_id"`
	Body      string        `db:"body"`
	CreatedAt time.Time     `db:"created_at"`
}
