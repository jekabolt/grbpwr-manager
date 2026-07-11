package admin

import (
	"context"
	"database/sql"
	"errors"
	"testing"

	authsrv "github.com/jekabolt/grbpwr-manager/internal/apisrv/auth"
	mocks "github.com/jekabolt/grbpwr-manager/internal/dependency/mocks"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	pb_admin "github.com/jekabolt/grbpwr-manager/proto/gen/admin"
	pb_common "github.com/jekabolt/grbpwr-manager/proto/gen/common"
	"github.com/stretchr/testify/mock"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// AddTask stamps created_by from the JWT, maps the board, and defaults the column
// to TODO when the request leaves status unset.
func TestAddTaskStampsIdentityAndDefaults(t *testing.T) {
	repo := mocks.NewMockRepository(t)
	tasks := mocks.NewMockTasks(t)
	repo.EXPECT().Tasks().Return(tasks)

	var captured *entity.Task
	tasks.EXPECT().AddTask(mock.Anything, mock.MatchedBy(func(tk *entity.Task) bool {
		captured = tk
		return true
	})).Return(7, nil)

	s := &Server{repo: repo}
	ctx := authsrv.PutAdminUsername(context.Background(), "olya")
	resp, err := s.AddTask(ctx, &pb_admin.AddTaskRequest{
		Task:  &pb_common.TaskInsert{Title: "sew sample"},
		Board: pb_common.TaskBoard_TASK_BOARD_DESIGN,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.Id != 7 {
		t.Errorf("id mismatch: %d", resp.Id)
	}
	if captured.CreatedBy != "olya" {
		t.Errorf("created_by not stamped from JWT: %q", captured.CreatedBy)
	}
	if captured.Board != entity.TaskBoardDesign {
		t.Errorf("board mismatch: %v", captured.Board)
	}
	if captured.Status != entity.TaskStatusTodo {
		t.Errorf("status should default to todo, got %v", captured.Status)
	}
}

// AddTask rejects a missing board without touching the store.
func TestAddTaskBoardRequired(t *testing.T) {
	repo := mocks.NewMockRepository(t)
	s := &Server{repo: repo}
	_, err := s.AddTask(context.Background(), &pb_admin.AddTaskRequest{
		Task: &pb_common.TaskInsert{Title: "x"},
	})
	if status.Code(err) != codes.InvalidArgument {
		t.Errorf("want InvalidArgument, got %v", err)
	}
}

// A foreign-key violation on add (bad deep-link / media id) maps to InvalidArgument.
func TestAddTaskForeignKeyViolation(t *testing.T) {
	repo := mocks.NewMockRepository(t)
	tasks := mocks.NewMockTasks(t)
	repo.EXPECT().Tasks().Return(tasks)
	tasks.EXPECT().AddTask(mock.Anything, mock.Anything).Return(0, errors.New("fk"))
	repo.EXPECT().IsErrForeignKeyViolation(mock.Anything).Return(true)

	s := &Server{repo: repo}
	_, err := s.AddTask(context.Background(), &pb_admin.AddTaskRequest{
		Task:  &pb_common.TaskInsert{Title: "x", ProductId: 999999},
		Board: pb_common.TaskBoard_TASK_BOARD_DEVELOPMENT,
	})
	if status.Code(err) != codes.InvalidArgument {
		t.Errorf("want InvalidArgument, got %v", err)
	}
}

// MoveTask treats an unset board as "keep current" (passes an empty board to the
// store) and maps a missing task to NotFound.
func TestMoveTaskKeepCurrentBoardAndNotFound(t *testing.T) {
	repo := mocks.NewMockRepository(t)
	tasks := mocks.NewMockTasks(t)
	repo.EXPECT().Tasks().Return(tasks)
	tasks.EXPECT().MoveTask(mock.Anything, 5, entity.TaskBoard(""), entity.TaskStatusReview, 3).
		Return(sql.ErrNoRows)

	s := &Server{repo: repo}
	_, err := s.MoveTask(context.Background(), &pb_admin.MoveTaskRequest{
		Id:       5,
		Status:   pb_common.TaskStatus_TASK_STATUS_REVIEW,
		Position: 3,
	})
	if status.Code(err) != codes.NotFound {
		t.Errorf("want NotFound, got %v", err)
	}
}

// MoveTask requires a target status.
func TestMoveTaskStatusRequired(t *testing.T) {
	repo := mocks.NewMockRepository(t)
	s := &Server{repo: repo}
	_, err := s.MoveTask(context.Background(), &pb_admin.MoveTaskRequest{Id: 5})
	if status.Code(err) != codes.InvalidArgument {
		t.Errorf("want InvalidArgument, got %v", err)
	}
}

// ArchiveTask requires an id and maps a missing/already-archived task to NotFound.
func TestArchiveTaskValidationAndNotFound(t *testing.T) {
	repo := mocks.NewMockRepository(t)
	s := &Server{repo: repo}
	if _, err := s.ArchiveTask(context.Background(), &pb_admin.ArchiveTaskRequest{Id: 0}); status.Code(err) != codes.InvalidArgument {
		t.Errorf("id required: want InvalidArgument, got %v", err)
	}

	repo2 := mocks.NewMockRepository(t)
	tasks := mocks.NewMockTasks(t)
	repo2.EXPECT().Tasks().Return(tasks)
	tasks.EXPECT().ArchiveTask(mock.Anything, 5).Return(sql.ErrNoRows)
	s2 := &Server{repo: repo2}
	if _, err := s2.ArchiveTask(context.Background(), &pb_admin.ArchiveTaskRequest{Id: 5}); status.Code(err) != codes.NotFound {
		t.Errorf("want NotFound, got %v", err)
	}
}

// AddTaskChecklistItem validates content, requires a task_id, and maps a missing
// task to NotFound.
func TestAddTaskChecklistItemValidatesAndMapsNotFound(t *testing.T) {
	repo := mocks.NewMockRepository(t)
	s := &Server{repo: repo}
	if _, err := s.AddTaskChecklistItem(context.Background(), &pb_admin.AddTaskChecklistItemRequest{TaskId: 1, Content: "   "}); status.Code(err) != codes.InvalidArgument {
		t.Errorf("empty content: want InvalidArgument, got %v", err)
	}
	if _, err := s.AddTaskChecklistItem(context.Background(), &pb_admin.AddTaskChecklistItemRequest{TaskId: 0, Content: "x"}); status.Code(err) != codes.InvalidArgument {
		t.Errorf("missing task_id: want InvalidArgument, got %v", err)
	}

	repo2 := mocks.NewMockRepository(t)
	tasks := mocks.NewMockTasks(t)
	repo2.EXPECT().Tasks().Return(tasks)
	tasks.EXPECT().AddTaskChecklistItem(mock.Anything, 1, "pack it").Return(0, sql.ErrNoRows)
	s2 := &Server{repo: repo2}
	if _, err := s2.AddTaskChecklistItem(context.Background(), &pb_admin.AddTaskChecklistItemRequest{TaskId: 1, Content: "  pack it  "}); status.Code(err) != codes.NotFound {
		t.Errorf("want NotFound, got %v", err)
	}
}

// SetTaskChecklistItemDone maps a missing item to NotFound.
func TestSetTaskChecklistItemDoneNotFound(t *testing.T) {
	repo := mocks.NewMockRepository(t)
	tasks := mocks.NewMockTasks(t)
	repo.EXPECT().Tasks().Return(tasks)
	tasks.EXPECT().SetTaskChecklistItemDone(mock.Anything, 9, true).Return(sql.ErrNoRows)
	s := &Server{repo: repo}
	if _, err := s.SetTaskChecklistItemDone(context.Background(), &pb_admin.SetTaskChecklistItemDoneRequest{Id: 9, IsDone: true}); status.Code(err) != codes.NotFound {
		t.Errorf("want NotFound, got %v", err)
	}
}

// AddTaskComment stamps the author from the JWT.
func TestAddTaskCommentStampsAuthor(t *testing.T) {
	repo := mocks.NewMockRepository(t)
	tasks := mocks.NewMockTasks(t)
	repo.EXPECT().Tasks().Return(tasks)
	tasks.EXPECT().AddTaskComment(mock.Anything, mock.Anything, "max").Return(3, nil)

	s := &Server{repo: repo}
	ctx := authsrv.PutAdminUsername(context.Background(), "max")
	resp, err := s.AddTaskComment(ctx, &pb_admin.AddTaskCommentRequest{
		Comment: &pb_common.TaskCommentInsert{TaskId: 1, Body: "looks good"},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.Id != 3 {
		t.Errorf("id mismatch: %d", resp.Id)
	}
}
