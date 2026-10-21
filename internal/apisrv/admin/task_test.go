package admin

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"

	authsrv "github.com/jekabolt/grbpwr-manager/internal/apisrv/auth"
	mocks "github.com/jekabolt/grbpwr-manager/internal/dependency/mocks"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	pb_admin "github.com/jekabolt/grbpwr-manager/proto/gen/admin"
	pb_common "github.com/jekabolt/grbpwr-manager/proto/gen/common"
	"github.com/stretchr/testify/mock"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"
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
	if captured.BoardRef != (entity.TaskBoardRef{Slug: entity.TaskBoardDesign}) {
		t.Errorf("legacy enum must resolve to its seeded slug, got %+v", captured.BoardRef)
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
	tasks.EXPECT().MoveTask(mock.Anything, 5, entity.TaskBoardRef{}, entity.TaskStatusReview, 3, "anna").
		Return(sql.ErrNoRows)

	s := &Server{repo: repo}
	ctx := authsrv.PutAdminUsername(context.Background(), "anna")
	_, err := s.MoveTask(ctx, &pb_admin.MoveTaskRequest{
		Id:       5,
		Status:   pb_common.TaskStatus_TASK_STATUS_REVIEW,
		Position: 3,
	})
	if status.Code(err) != codes.NotFound {
		t.Errorf("want NotFound, got %v", err)
	}
}

func TestGetTaskReturnsStatusHistory(t *testing.T) {
	repo := mocks.NewMockRepository(t)
	tasks := mocks.NewMockTasks(t)
	repo.EXPECT().Tasks().Return(tasks)

	changedAt := time.Date(2026, 10, 21, 12, 35, 0, 0, time.UTC)
	createdAt := changedAt.Add(-time.Hour)
	tasks.EXPECT().GetTaskById(mock.Anything, 5).Return(&entity.Task{
		Id:        5,
		Status:    entity.TaskStatusDone,
		CreatedAt: createdAt,
		StatusHistory: []entity.TaskStatusChange{{
			TaskId:     5,
			FromStatus: sql.NullString{String: string(entity.TaskStatusReview), Valid: true},
			ToStatus:   entity.TaskStatusDone,
			ChangedBy:  "jeka",
			ChangedAt:  changedAt,
		}, {
			TaskId:    5,
			ChangedBy: "olya",
			ChangedAt: createdAt,
		}},
	}, nil)

	s := &Server{repo: repo}
	resp, err := s.GetTask(context.Background(), &pb_admin.GetTaskRequest{Id: 5})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(resp.StatusHistory) != 2 {
		t.Fatalf("status_history length = %d, want 2", len(resp.StatusHistory))
	}
	got := resp.StatusHistory[0]
	if got.FromStatus != pb_common.TaskStatus_TASK_STATUS_REVIEW || got.ToStatus != pb_common.TaskStatus_TASK_STATUS_DONE {
		t.Errorf("statuses = %v -> %v, want REVIEW -> DONE", got.FromStatus, got.ToStatus)
	}
	if got.ChangedBy != "jeka" {
		t.Errorf("changed_by = %q, want jeka", got.ChangedBy)
	}
	if !got.ChangedAt.AsTime().Equal(changedAt) {
		t.Errorf("changed_at = %v, want %v", got.ChangedAt.AsTime(), changedAt)
	}
	legacy := resp.StatusHistory[1]
	if legacy.FromStatus != pb_common.TaskStatus_TASK_STATUS_UNKNOWN || legacy.ToStatus != pb_common.TaskStatus_TASK_STATUS_UNKNOWN {
		t.Errorf("legacy creation statuses = %v -> %v, want UNKNOWN -> UNKNOWN", legacy.FromStatus, legacy.ToStatus)
	}
	if legacy.ChangedBy != "olya" || !legacy.ChangedAt.AsTime().Equal(createdAt) {
		t.Errorf("legacy creation = %q at %v, want olya at %v", legacy.ChangedBy, legacy.ChangedAt.AsTime(), createdAt)
	}
}

func TestGetTaskExposesApproximateStatusHistory(t *testing.T) {
	repo := mocks.NewMockRepository(t)
	tasks := mocks.NewMockTasks(t)
	repo.EXPECT().Tasks().Return(tasks)

	changedAt := time.Date(2026, 10, 7, 9, 0, 0, 0, time.UTC)
	tasks.EXPECT().GetTaskById(mock.Anything, 8).Return(&entity.Task{
		Id:     8,
		Status: entity.TaskStatusDone,
		StatusHistory: []entity.TaskStatusChange{{
			TaskId:      8,
			ToStatus:    entity.TaskStatusDone,
			ChangedAt:   changedAt,
			Approximate: true,
		}},
	}, nil)

	resp, err := (&Server{repo: repo}).GetTask(context.Background(), &pb_admin.GetTaskRequest{Id: 8})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(resp.StatusHistory) != 1 {
		t.Fatalf("status_history length = %d, want 1", len(resp.StatusHistory))
	}
	got := resp.StatusHistory[0]
	if got.FromStatus != pb_common.TaskStatus_TASK_STATUS_UNKNOWN || got.ToStatus != pb_common.TaskStatus_TASK_STATUS_DONE {
		t.Errorf("statuses = %v -> %v, want UNKNOWN -> DONE", got.FromStatus, got.ToStatus)
	}
	if !got.Approximate {
		t.Error("approximate backfill row lost its marker")
	}
}

func TestListTasksClosedRangeReturnsBatchedClosingMetadata(t *testing.T) {
	repo := mocks.NewMockRepository(t)
	tasks := mocks.NewMockTasks(t)
	repo.EXPECT().Tasks().Return(tasks)

	from := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	to := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	closed7 := time.Date(2026, 10, 20, 10, 0, 0, 0, time.UTC)
	closed9 := time.Date(2026, 10, 19, 9, 0, 0, 0, time.UTC)
	reviewSince := time.Date(2026, 10, 18, 8, 0, 0, 0, time.UTC)

	tasks.EXPECT().ListTasks(mock.Anything, mock.MatchedBy(func(f entity.TaskListFilter) bool {
		return f.ClosedFrom.Valid && f.ClosedFrom.Time.Equal(from) &&
			f.ClosedTo.Valid && f.ClosedTo.Time.Equal(to) &&
			f.IncludeArchived && f.OrderFactor == entity.Descending
	})).Return([]entity.Task{
		{Id: 7, Status: entity.TaskStatusDone},
		{Id: 8, Status: entity.TaskStatusReview},
		{Id: 9, Status: entity.TaskStatusDone, ArchivedAt: sql.NullTime{Time: closed9.Add(time.Hour), Valid: true}},
	}, 3, nil)
	tasks.EXPECT().GetTaskClosedByIds(mock.Anything, []int{7, 9}).Return(map[int]entity.TaskClosed{
		7: {TaskId: 7, ClosedAt: closed7, ClosedBy: "anna"},
		9: {TaskId: 9, ClosedAt: closed9, Approximate: true},
	}, nil)
	tasks.EXPECT().GetTaskTimingByIds(mock.Anything, []int{7, 8, 9}).Return(map[int]entity.TaskTiming{
		7: {TaskId: 7, StatusSince: closed7},
		8: {TaskId: 8, StatusSince: reviewSince},
		9: {TaskId: 9, StatusSince: closed9, Approximate: true},
	}, nil)

	resp, err := (&Server{repo: repo}).ListTasks(context.Background(), &pb_admin.ListTasksRequest{
		IncludeArchived: true,
		ClosedFrom:      timestamppb.New(from),
		ClosedTo:        timestamppb.New(to),
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.Total != 3 || len(resp.Tasks) != 3 {
		t.Fatalf("tasks/total = %d/%d, want 3/3", len(resp.Tasks), resp.Total)
	}
	if len(resp.Closed) != 2 {
		t.Fatalf("closed length = %d, want 2", len(resp.Closed))
	}
	if got := resp.Closed[0]; got.TaskId != 7 || got.ClosedBy != "anna" || !got.ClosedAt.AsTime().Equal(closed7) || got.Approximate {
		t.Errorf("closed[0] = %+v, want exact task 7 closure", got)
	}
	if got := resp.Closed[1]; got.TaskId != 9 || !got.ClosedAt.AsTime().Equal(closed9) || !got.Approximate {
		t.Errorf("closed[1] = %+v, want approximate task 9 closure", got)
	}
	if len(resp.Timing) != 3 {
		t.Fatalf("timing length = %d, want one row per returned task", len(resp.Timing))
	}
	if got := resp.Timing[0]; got.TaskId != 7 || !got.StatusSince.AsTime().Equal(closed7) || got.Approximate {
		t.Errorf("timing[0] = %+v, want exact task 7 timing", got)
	}
	if got := resp.Timing[1]; got.TaskId != 8 || !got.StatusSince.AsTime().Equal(reviewSince) || got.Approximate {
		t.Errorf("timing[1] = %+v, want exact task 8 timing", got)
	}
	if got := resp.Timing[2]; got.TaskId != 9 || !got.StatusSince.AsTime().Equal(closed9) || !got.Approximate {
		t.Errorf("timing[2] = %+v, want approximate task 9 timing", got)
	}
}

func TestListTasksRejectsEmptyClosedRangeBeforeStore(t *testing.T) {
	repo := mocks.NewMockRepository(t)
	at := timestamppb.New(time.Date(2026, 10, 21, 0, 0, 0, 0, time.UTC))

	_, err := (&Server{repo: repo}).ListTasks(context.Background(), &pb_admin.ListTasksRequest{
		ClosedFrom: at,
		ClosedTo:   at,
	})
	if status.Code(err) != codes.InvalidArgument {
		t.Fatalf("want InvalidArgument, got %v", err)
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
