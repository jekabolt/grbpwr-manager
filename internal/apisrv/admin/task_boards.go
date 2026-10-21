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
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// taskBoardNameTakenMsg — uniq_task_board_name is case-insensitive, so "Design" collides with "design".
const taskBoardNameTakenMsg = "a board with this name already exists"

// ListTaskBoards returns every board in rail order with its task counts.
func (s *Server) ListTaskBoards(ctx context.Context, _ *pb_admin.ListTaskBoardsRequest) (*pb_admin.ListTaskBoardsResponse, error) {
	boards, err := s.repo.Tasks().ListTaskBoards(ctx)
	if err != nil {
		slog.Default().ErrorContext(ctx, "can't list task boards", slog.String("err", err.Error()))
		return nil, status.Errorf(codes.Internal, "can't list task boards")
	}
	return &pb_admin.ListTaskBoardsResponse{Boards: dto.ConvertEntityTaskBoardsToPb(boards)}, nil
}

// ReorderTaskBoards replaces the complete rail order. The store validates the id set while its
// board rows are locked, so a stale client receives InvalidArgument and no partial positions.
func (s *Server) ReorderTaskBoards(ctx context.Context, req *pb_admin.ReorderTaskBoardsRequest) (*pb_admin.ReorderTaskBoardsResponse, error) {
	ids := make([]int, 0, len(req.Ids))
	for _, id := range req.Ids {
		ids = append(ids, int(id))
	}
	boards, err := s.repo.Tasks().ReorderTaskBoards(ctx, ids)
	if errors.Is(err, entity.ErrTaskBoardSetMismatch) {
		return nil, status.Error(codes.InvalidArgument, entity.ErrTaskBoardSetMismatch.Error())
	}
	if err != nil {
		slog.Default().ErrorContext(ctx, "can't reorder task boards", slog.String("err", err.Error()))
		return nil, status.Error(codes.Internal, "can't reorder task boards")
	}
	return &pb_admin.ReorderTaskBoardsResponse{Boards: dto.ConvertEntityTaskBoardsToPb(boards)}, nil
}

// AddTaskBoard creates a board at the end of the rail; created_by comes from the JWT.
func (s *Server) AddTaskBoard(ctx context.Context, req *pb_admin.AddTaskBoardRequest) (*pb_admin.AddTaskBoardResponse, error) {
	name, err := dto.ValidateTaskBoardName(req.Name)
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "%v", err)
	}
	tasks := s.repo.Tasks()
	id, err := tasks.AddTaskBoard(ctx, name, authsrv.GetAdminUsername(ctx))
	if err != nil {
		if s.repo.IsErrUniqueViolation(err) {
			return nil, status.Error(codes.AlreadyExists, taskBoardNameTakenMsg)
		}
		slog.Default().ErrorContext(ctx, "can't add task board", slog.String("err", err.Error()))
		return nil, status.Errorf(codes.Internal, "can't add task board")
	}
	b, err := tasks.GetTaskBoardById(ctx, id)
	if err != nil {
		slog.Default().ErrorContext(ctx, "can't read added task board", slog.Int("id", id), slog.String("err", err.Error()))
		return nil, status.Errorf(codes.Internal, "can't read added task board")
	}
	return &pb_admin.AddTaskBoardResponse{Board: dto.ConvertEntityTaskBoardToPb(b)}, nil
}

// UpdateTaskBoard renames a board (seeded ones included; their legacy slug does not change).
func (s *Server) UpdateTaskBoard(ctx context.Context, req *pb_admin.UpdateTaskBoardRequest) (*pb_admin.UpdateTaskBoardResponse, error) {
	if req.Id <= 0 {
		return nil, status.Error(codes.InvalidArgument, "board id is required")
	}
	name, err := dto.ValidateTaskBoardName(req.Name)
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "%v", err)
	}
	tasks := s.repo.Tasks()
	if err := tasks.RenameTaskBoard(ctx, int(req.Id), name); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, status.Error(codes.NotFound, "task board not found")
		}
		if s.repo.IsErrUniqueViolation(err) {
			return nil, status.Error(codes.AlreadyExists, taskBoardNameTakenMsg)
		}
		slog.Default().ErrorContext(ctx, "can't rename task board", slog.String("err", err.Error()))
		return nil, status.Errorf(codes.Internal, "can't rename task board")
	}
	b, err := tasks.GetTaskBoardById(ctx, int(req.Id))
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, status.Error(codes.NotFound, "task board not found")
		}
		slog.Default().ErrorContext(ctx, "can't read renamed task board", slog.String("err", err.Error()))
		return nil, status.Errorf(codes.Internal, "can't read renamed task board")
	}
	return &pb_admin.UpdateTaskBoardResponse{Board: dto.ConvertEntityTaskBoardToPb(b)}, nil
}

// DeleteTaskBoard deletes a board, moving its tasks (archived included) to move_to_id first.
func (s *Server) DeleteTaskBoard(ctx context.Context, req *pb_admin.DeleteTaskBoardRequest) (*pb_admin.DeleteTaskBoardResponse, error) {
	if req.Id <= 0 {
		return nil, status.Error(codes.InvalidArgument, "board id is required")
	}
	if req.MoveToId < 0 {
		return nil, status.Error(codes.InvalidArgument, "move_to_id must not be negative")
	}
	err := s.repo.Tasks().DeleteTaskBoard(ctx, int(req.Id), int(req.MoveToId))
	switch {
	case err == nil:
		return &pb_admin.DeleteTaskBoardResponse{}, nil
	case errors.Is(err, entity.ErrTaskBoardNotFound):
		return nil, status.Error(codes.NotFound, "task board not found")
	case errors.Is(err, entity.ErrTaskBoardLast):
		return nil, status.Error(codes.FailedPrecondition, entity.ErrTaskBoardLast.Error())
	case errors.Is(err, entity.ErrTaskBoardNotEmpty):
		return nil, status.Error(codes.FailedPrecondition, entity.ErrTaskBoardNotEmpty.Error())
	case errors.Is(err, entity.ErrTaskBoardMoveTarget):
		return nil, status.Error(codes.InvalidArgument, entity.ErrTaskBoardMoveTarget.Error())
	}
	slog.Default().ErrorContext(ctx, "can't delete task board", slog.String("err", err.Error()))
	return nil, status.Errorf(codes.Internal, "can't delete task board")
}
