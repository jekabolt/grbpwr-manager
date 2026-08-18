package admin

import (
	"context"
	"database/sql"
	"errors"
	"log/slog"

	authsrv "github.com/jekabolt/grbpwr-manager/internal/apisrv/auth"
	"github.com/jekabolt/grbpwr-manager/internal/dto"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	pb_admin "github.com/jekabolt/grbpwr-manager/proto/gen/admin"
	pb_common "github.com/jekabolt/grbpwr-manager/proto/gen/common"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// taskFKViolationMsg перечисляет ВСЕ поля карточки, у которых есть внешний ключ и которые поэтому
// могут доехать сюда как 1452. Перечень неполный хуже, чем никакого: человек шлёт мёртвый sample_id,
// читает список, не находит в нём своего поля и ищет ошибку не там. order_uuid здесь намеренно нет —
// у него внешнего ключа не было никогда (0090: «best-effort (no FK)»), и назвать его значило бы
// пообещать проверку, которой не существует.
const taskFKViolationMsg = "tech_card_id, product_id, archive_id, fitting_id, production_run_id, sample_id, project_topic_id, media_id, or file_id does not reference an existing record"

// AddTask creates a new kanban task from its content + placement. created_by is
// stamped from the caller's JWT; the card is appended to its (board,status) column.
func (s *Server) AddTask(ctx context.Context, req *pb_admin.AddTaskRequest) (*pb_admin.AddTaskResponse, error) {
	ti, err := dto.ConvertPbTaskInsertToEntity(req.Task)
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "%v", err)
	}
	board, err := dto.ConvertPbTaskBoardToEntity(req.Board)
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, "task board is required")
	}
	// The initial column defaults to TODO when unset.
	taskStatus := entity.TaskStatusTodo
	if req.Status != pb_common.TaskStatus_TASK_STATUS_UNKNOWN {
		st, err := dto.ConvertPbTaskStatusToEntity(req.Status)
		if err != nil {
			return nil, status.Errorf(codes.InvalidArgument, "%v", err)
		}
		taskStatus = st
	}

	t := &entity.Task{
		TaskInsert: *ti,
		Board:      board,
		Status:     taskStatus,
		CreatedBy:  authsrv.GetAdminUsername(ctx),
	}
	id, err := s.repo.Tasks().AddTask(ctx, t)
	if err != nil {
		// ССЫЛКА НА ТЕМУ, КОТОРАЯ НЕ ПРОЕКТ (0322), ОТВЕЧАЕТ ФРАЗОЙ, А НЕ КОДОМ КЛЮЧА. Внешний
		// ключ здесь и не срабатывает — тема существует, она просто ярлык, — а сообщение про
		// «does not reference an existing record» было бы прямым враньём: id существует.
		if errors.Is(err, entity.ErrTaskNeedsProjectTopic) {
			return nil, status.Errorf(codes.InvalidArgument, "%v", err)
		}
		if s.repo.IsErrForeignKeyViolation(err) {
			return nil, status.Error(codes.InvalidArgument, taskFKViolationMsg)
		}
		slog.Default().ErrorContext(ctx, "can't add task", slog.String("err", err.Error()))
		return nil, status.Errorf(codes.Internal, "can't add task")
	}
	return &pb_admin.AddTaskResponse{Id: int32(id)}, nil
}

// GetTask returns a task by id.
func (s *Server) GetTask(ctx context.Context, req *pb_admin.GetTaskRequest) (*pb_admin.GetTaskResponse, error) {
	if req.Id <= 0 {
		return nil, status.Error(codes.InvalidArgument, "task id is required")
	}
	t, err := s.repo.Tasks().GetTaskById(ctx, int(req.Id))
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, status.Errorf(codes.NotFound, "task not found")
		}
		slog.Default().ErrorContext(ctx, "can't get task by id", slog.String("err", err.Error()))
		return nil, status.Errorf(codes.Internal, "can't get task")
	}
	// Library attachments are resolved here rather than in the task store, so the
	// task store never has to know the files store — and because the resolved form
	// carries presigned urls, which only make sense for the life of one response.
	//
	// Deliberately gated on tasks:read, not files:read: an attachment is part of
	// the card's content, exactly as its media already is. The files section gates
	// the library itself, not what someone chose to pin to a task.
	var files []*pb_admin.LibraryFile
	if len(t.FileIds) > 0 {
		resolved, err := s.repo.Files().ListFilesByIds(ctx, t.FileIds)
		if err != nil {
			// A card that cannot resolve its attachments is still a card worth
			// showing; losing the whole task over a file lookup would be worse.
			slog.Default().ErrorContext(ctx, "can't resolve task files",
				slog.Int("task_id", t.Id), slog.String("err", err.Error()))
		} else {
			files = s.libraryFilesToPb(ctx, resolved)
		}
	}
	return &pb_admin.GetTaskResponse{
		Task:  dto.ConvertEntityTaskToPb(t),
		Files: files,
	}, nil
}

// UpdateTask replaces a task's content. Placement is not touched here (see MoveTask).
func (s *Server) UpdateTask(ctx context.Context, req *pb_admin.UpdateTaskRequest) (*pb_admin.UpdateTaskResponse, error) {
	if req.Id <= 0 {
		return nil, status.Error(codes.InvalidArgument, "task id is required")
	}
	ti, err := dto.ConvertPbTaskInsertToEntity(req.Task)
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "%v", err)
	}
	if err := s.repo.Tasks().UpdateTask(ctx, int(req.Id), ti); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, status.Errorf(codes.NotFound, "task not found")
		}
		// Проверяется ПОСЛЕ ErrNoRows и до внешнего ключа — см. довод в AddTask. Порядок здесь
		// несущий: «задачи нет» и «тема не проект» — разные ответы на разные ошибки человека.
		if errors.Is(err, entity.ErrTaskNeedsProjectTopic) {
			return nil, status.Errorf(codes.InvalidArgument, "%v", err)
		}
		if s.repo.IsErrForeignKeyViolation(err) {
			return nil, status.Error(codes.InvalidArgument, taskFKViolationMsg)
		}
		slog.Default().ErrorContext(ctx, "can't update task", slog.String("err", err.Error()))
		return nil, status.Errorf(codes.Internal, "can't update task")
	}
	return &pb_admin.UpdateTaskResponse{}, nil
}

// MoveTask changes a task's placement (board/column/position), the drag-and-drop
// endpoint. An unset board keeps the current one; the target column is required.
func (s *Server) MoveTask(ctx context.Context, req *pb_admin.MoveTaskRequest) (*pb_admin.MoveTaskResponse, error) {
	if req.Id <= 0 {
		return nil, status.Error(codes.InvalidArgument, "task id is required")
	}
	// Board is optional: UNKNOWN = keep the task's current board.
	var board entity.TaskBoard
	if req.Board != pb_common.TaskBoard_TASK_BOARD_UNKNOWN {
		b, err := dto.ConvertPbTaskBoardToEntity(req.Board)
		if err != nil {
			return nil, status.Errorf(codes.InvalidArgument, "%v", err)
		}
		board = b
	}
	taskStatus, err := dto.ConvertPbTaskStatusToEntity(req.Status)
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, "target status is required")
	}
	if err := s.repo.Tasks().MoveTask(ctx, int(req.Id), board, taskStatus, int(req.Position)); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, status.Errorf(codes.NotFound, "task not found")
		}
		slog.Default().ErrorContext(ctx, "can't move task", slog.String("err", err.Error()))
		return nil, status.Errorf(codes.Internal, "can't move task")
	}
	return &pb_admin.MoveTaskResponse{}, nil
}

// DeleteTask deletes a task by id (labels, media, comments cascade).
func (s *Server) DeleteTask(ctx context.Context, req *pb_admin.DeleteTaskRequest) (*pb_admin.DeleteTaskResponse, error) {
	if req.Id <= 0 {
		return nil, status.Error(codes.InvalidArgument, "task id is required")
	}
	if err := s.repo.Tasks().DeleteTask(ctx, int(req.Id)); err != nil {
		slog.Default().ErrorContext(ctx, "can't delete task", slog.String("err", err.Error()))
		return nil, status.Errorf(codes.Internal, "can't delete task")
	}
	return &pb_admin.DeleteTaskResponse{}, nil
}

// AddTaskComment appends a comment to a task. author is stamped from the JWT.
func (s *Server) AddTaskComment(ctx context.Context, req *pb_admin.AddTaskCommentRequest) (*pb_admin.AddTaskCommentResponse, error) {
	ci, err := dto.ConvertPbTaskCommentInsertToEntity(req.Comment)
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "%v", err)
	}
	id, err := s.repo.Tasks().AddTaskComment(ctx, ci, authsrv.GetAdminUsername(ctx))
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, status.Errorf(codes.NotFound, "task not found")
		}
		if s.repo.IsErrForeignKeyViolation(err) {
			return nil, status.Error(codes.InvalidArgument, "task_id does not reference an existing task")
		}
		slog.Default().ErrorContext(ctx, "can't add task comment", slog.String("err", err.Error()))
		return nil, status.Errorf(codes.Internal, "can't add task comment")
	}
	return &pb_admin.AddTaskCommentResponse{Id: int32(id)}, nil
}

// ListTaskComments returns a task's comments, oldest first.
func (s *Server) ListTaskComments(ctx context.Context, req *pb_admin.ListTaskCommentsRequest) (*pb_admin.ListTaskCommentsResponse, error) {
	if req.TaskId <= 0 {
		return nil, status.Error(codes.InvalidArgument, "task_id is required")
	}
	comments, err := s.repo.Tasks().ListTaskComments(ctx, int(req.TaskId))
	if err != nil {
		slog.Default().ErrorContext(ctx, "can't list task comments", slog.String("err", err.Error()))
		return nil, status.Errorf(codes.Internal, "can't list task comments")
	}
	pbComments := make([]*pb_common.TaskComment, 0, len(comments))
	for i := range comments {
		pbComments = append(pbComments, dto.ConvertEntityTaskCommentToPb(&comments[i]))
	}
	return &pb_admin.ListTaskCommentsResponse{Comments: pbComments}, nil
}

// ListTasks lists tasks with optional placement and linked-entity filters.
func (s *Server) ListTasks(ctx context.Context, req *pb_admin.ListTasksRequest) (*pb_admin.ListTasksResponse, error) {
	filter := entity.TaskListFilter{
		Assignee:        req.Assignee,
		TechCardId:      int(req.TechCardId),
		ProductId:       int(req.ProductId),
		OrderUuid:       req.OrderUuid,
		ArchiveId:       int(req.ArchiveId),
		FittingId:       int(req.FittingId),
		ProductionRunId: int(req.ProductionRunId),
		SampleId:        int(req.SampleId),
		// ЗАДАЧИ ПРОЕКТА ИДУТ ПОД ПРАВАМИ ЗАДАЧ (0322). Здесь для этого не сделано НИЧЕГО, и это
		// главное свойство решения: фильтр живёт на уже существующем ListTasks, у которого в
		// rbac.go стоит rd(tasks). Отдельный RPC пришлось бы классифицировать заново, и там
		// проект однажды стал бы боковым каналом к задачам, которых человек иначе не видит.
		ProjectTopicId:  int(req.ProjectTopicId),
		IncludeArchived: req.IncludeArchived,
		Limit:           int(req.Limit),
		Offset:          int(req.Offset),
		OrderFactor:     dto.ConvertPBCommonOrderFactorToEntity(req.OrderFactor),
	}
	if req.Board != pb_common.TaskBoard_TASK_BOARD_UNKNOWN {
		b, err := dto.ConvertPbTaskBoardToEntity(req.Board)
		if err != nil {
			return nil, status.Errorf(codes.InvalidArgument, "%v", err)
		}
		filter.Board = b
	}
	if req.Status != pb_common.TaskStatus_TASK_STATUS_UNKNOWN {
		st, err := dto.ConvertPbTaskStatusToEntity(req.Status)
		if err != nil {
			return nil, status.Errorf(codes.InvalidArgument, "%v", err)
		}
		filter.Status = st
	}

	tasks, total, err := s.repo.Tasks().ListTasks(ctx, filter)
	if err != nil {
		slog.Default().ErrorContext(ctx, "can't list tasks", slog.String("err", err.Error()))
		return nil, status.Errorf(codes.Internal, "can't list tasks")
	}
	pbTasks := make([]*pb_common.Task, 0, len(tasks))
	for i := range tasks {
		pbTasks = append(pbTasks, dto.ConvertEntityTaskToPb(&tasks[i]))
	}
	return &pb_admin.ListTasksResponse{Tasks: pbTasks, Total: int32(total)}, nil
}

// ArchiveTask soft-archives a task (hidden from the board, restorable).
func (s *Server) ArchiveTask(ctx context.Context, req *pb_admin.ArchiveTaskRequest) (*pb_admin.ArchiveTaskResponse, error) {
	if req.Id <= 0 {
		return nil, status.Error(codes.InvalidArgument, "task id is required")
	}
	if err := s.repo.Tasks().ArchiveTask(ctx, int(req.Id)); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, status.Errorf(codes.NotFound, "task not found or already archived")
		}
		slog.Default().ErrorContext(ctx, "can't archive task", slog.String("err", err.Error()))
		return nil, status.Errorf(codes.Internal, "can't archive task")
	}
	return &pb_admin.ArchiveTaskResponse{}, nil
}

// UnarchiveTask restores an archived task to the end of its column.
func (s *Server) UnarchiveTask(ctx context.Context, req *pb_admin.UnarchiveTaskRequest) (*pb_admin.UnarchiveTaskResponse, error) {
	if req.Id <= 0 {
		return nil, status.Error(codes.InvalidArgument, "task id is required")
	}
	if err := s.repo.Tasks().UnarchiveTask(ctx, int(req.Id)); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, status.Errorf(codes.NotFound, "task not found or not archived")
		}
		slog.Default().ErrorContext(ctx, "can't unarchive task", slog.String("err", err.Error()))
		return nil, status.Errorf(codes.Internal, "can't unarchive task")
	}
	return &pb_admin.UnarchiveTaskResponse{}, nil
}

// AddTaskChecklistItem appends a checklist item (subtask) to a task.
func (s *Server) AddTaskChecklistItem(ctx context.Context, req *pb_admin.AddTaskChecklistItemRequest) (*pb_admin.AddTaskChecklistItemResponse, error) {
	if req.TaskId <= 0 {
		return nil, status.Error(codes.InvalidArgument, "task_id is required")
	}
	content, err := dto.ValidateChecklistContent(req.Content)
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "%v", err)
	}
	id, err := s.repo.Tasks().AddTaskChecklistItem(ctx, int(req.TaskId), content)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, status.Errorf(codes.NotFound, "task not found")
		}
		slog.Default().ErrorContext(ctx, "can't add task checklist item", slog.String("err", err.Error()))
		return nil, status.Errorf(codes.Internal, "can't add task checklist item")
	}
	return &pb_admin.AddTaskChecklistItemResponse{Id: int32(id)}, nil
}

// SetTaskChecklistItemDone sets a checklist item's done flag.
func (s *Server) SetTaskChecklistItemDone(ctx context.Context, req *pb_admin.SetTaskChecklistItemDoneRequest) (*pb_admin.SetTaskChecklistItemDoneResponse, error) {
	if req.Id <= 0 {
		return nil, status.Error(codes.InvalidArgument, "checklist item id is required")
	}
	if err := s.repo.Tasks().SetTaskChecklistItemDone(ctx, int(req.Id), req.IsDone); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, status.Errorf(codes.NotFound, "checklist item not found")
		}
		slog.Default().ErrorContext(ctx, "can't set task checklist item done", slog.String("err", err.Error()))
		return nil, status.Errorf(codes.Internal, "can't set task checklist item done")
	}
	return &pb_admin.SetTaskChecklistItemDoneResponse{}, nil
}

// DeleteTaskChecklistItem removes a checklist item.
func (s *Server) DeleteTaskChecklistItem(ctx context.Context, req *pb_admin.DeleteTaskChecklistItemRequest) (*pb_admin.DeleteTaskChecklistItemResponse, error) {
	if req.Id <= 0 {
		return nil, status.Error(codes.InvalidArgument, "checklist item id is required")
	}
	if err := s.repo.Tasks().DeleteTaskChecklistItem(ctx, int(req.Id)); err != nil {
		slog.Default().ErrorContext(ctx, "can't delete task checklist item", slog.String("err", err.Error()))
		return nil, status.Errorf(codes.Internal, "can't delete task checklist item")
	}
	return &pb_admin.DeleteTaskChecklistItemResponse{}, nil
}
