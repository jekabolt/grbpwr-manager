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

const taskFKViolationMsg = "tech_card_id, product_id, archive_id, or media_id does not reference an existing record"

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
	return &pb_admin.GetTaskResponse{Task: dto.ConvertEntityTaskToPb(t)}, nil
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

// ListTasks lists tasks with optional board/status/assignee/tech-card/product filters.
func (s *Server) ListTasks(ctx context.Context, req *pb_admin.ListTasksRequest) (*pb_admin.ListTasksResponse, error) {
	filter := entity.TaskListFilter{
		Assignee:    req.Assignee,
		TechCardId:  int(req.TechCardId),
		ProductId:   int(req.ProductId),
		Limit:       int(req.Limit),
		Offset:      int(req.Offset),
		OrderFactor: dto.ConvertPBCommonOrderFactorToEntity(req.OrderFactor),
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
