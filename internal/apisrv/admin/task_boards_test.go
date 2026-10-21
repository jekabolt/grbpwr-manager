package admin

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
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

// board_id wins over the legacy enum when a request carries both.
func TestAddTaskBoardIdWinsOverEnum(t *testing.T) {
	repo := mocks.NewMockRepository(t)
	tasks := mocks.NewMockTasks(t)
	repo.EXPECT().Tasks().Return(tasks)
	tasks.EXPECT().AddTask(mock.Anything, mock.MatchedBy(func(tk *entity.Task) bool {
		return tk.BoardRef == entity.TaskBoardRef{Id: 9}
	})).Return(1, nil)

	_, err := (&Server{repo: repo}).AddTask(context.Background(), &pb_admin.AddTaskRequest{
		Task:    &pb_common.TaskInsert{Title: "x"},
		Board:   pb_common.TaskBoard_TASK_BOARD_DESIGN,
		BoardId: 9,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

// A board id that names no board is the caller's mistake, not a server failure.
func TestAddTaskUnknownBoardIsInvalidArgument(t *testing.T) {
	repo := mocks.NewMockRepository(t)
	tasks := mocks.NewMockTasks(t)
	repo.EXPECT().Tasks().Return(tasks)
	tasks.EXPECT().AddTask(mock.Anything, mock.Anything).
		Return(0, fmt.Errorf("can't add task: %w", entity.ErrTaskBoardNotFound))

	_, err := (&Server{repo: repo}).AddTask(context.Background(), &pb_admin.AddTaskRequest{
		Task:    &pb_common.TaskInsert{Title: "x"},
		BoardId: 404,
	})
	if status.Code(err) != codes.InvalidArgument {
		t.Fatalf("want InvalidArgument, got %v", err)
	}
}

func TestAddTaskNegativeBoardIdRejected(t *testing.T) {
	repo := mocks.NewMockRepository(t)
	_, err := (&Server{repo: repo}).AddTask(context.Background(), &pb_admin.AddTaskRequest{
		Task:    &pb_common.TaskInsert{Title: "x"},
		BoardId: -1,
	})
	if status.Code(err) != codes.InvalidArgument {
		t.Fatalf("want InvalidArgument, got %v", err)
	}
}

// MoveTask: board_id wins over the enum; the legacy enum alone resolves to its slug.
func TestMoveTaskBoardRefResolution(t *testing.T) {
	cases := []struct {
		name string
		req  *pb_admin.MoveTaskRequest
		want entity.TaskBoardRef
	}{
		{"id wins", &pb_admin.MoveTaskRequest{Id: 3, BoardId: 12, Board: pb_common.TaskBoard_TASK_BOARD_CONTENT, Status: pb_common.TaskStatus_TASK_STATUS_TODO}, entity.TaskBoardRef{Id: 12}},
		{"legacy enum", &pb_admin.MoveTaskRequest{Id: 3, Board: pb_common.TaskBoard_TASK_BOARD_CONTENT, Status: pb_common.TaskStatus_TASK_STATUS_TODO}, entity.TaskBoardRef{Slug: entity.TaskBoardContent}},
		{"keep", &pb_admin.MoveTaskRequest{Id: 3, Status: pb_common.TaskStatus_TASK_STATUS_TODO}, entity.TaskBoardRef{}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			repo := mocks.NewMockRepository(t)
			tasks := mocks.NewMockTasks(t)
			repo.EXPECT().Tasks().Return(tasks)
			tasks.EXPECT().MoveTask(mock.Anything, 3, tc.want, entity.TaskStatusTodo, 0, "").Return(nil)
			if _, err := (&Server{repo: repo}).MoveTask(context.Background(), tc.req); err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
		})
	}
}

func TestMoveTaskUnknownBoardIsInvalidArgument(t *testing.T) {
	repo := mocks.NewMockRepository(t)
	tasks := mocks.NewMockTasks(t)
	repo.EXPECT().Tasks().Return(tasks)
	tasks.EXPECT().MoveTask(mock.Anything, 3, entity.TaskBoardRef{Id: 77}, entity.TaskStatusTodo, 0, "").
		Return(fmt.Errorf("can't move task: %w", entity.ErrTaskBoardNotFound))
	_, err := (&Server{repo: repo}).MoveTask(context.Background(), &pb_admin.MoveTaskRequest{
		Id: 3, BoardId: 77, Status: pb_common.TaskStatus_TASK_STATUS_TODO,
	})
	if status.Code(err) != codes.InvalidArgument {
		t.Fatalf("want InvalidArgument, got %v", err)
	}
}

// ListTasks: board_id filters by id and wins; the enum alone filters by its seeded slug.
func TestListTasksBoardFilter(t *testing.T) {
	cases := []struct {
		name     string
		req      *pb_admin.ListTasksRequest
		wantID   int
		wantSlug entity.TaskBoard
	}{
		{"id wins", &pb_admin.ListTasksRequest{BoardId: 8, Board: pb_common.TaskBoard_TASK_BOARD_DESIGN}, 8, ""},
		{"legacy enum", &pb_admin.ListTasksRequest{Board: pb_common.TaskBoard_TASK_BOARD_DESIGN}, 0, entity.TaskBoardDesign},
		{"all", &pb_admin.ListTasksRequest{}, 0, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			repo := mocks.NewMockRepository(t)
			tasks := mocks.NewMockTasks(t)
			repo.EXPECT().Tasks().Return(tasks)
			tasks.EXPECT().ListTasks(mock.Anything, mock.MatchedBy(func(f entity.TaskListFilter) bool {
				return f.BoardId == tc.wantID && f.Board == tc.wantSlug
			})).Return(nil, 0, nil)
			if _, err := (&Server{repo: repo}).ListTasks(context.Background(), tc.req); err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
		})
	}
}

func TestListTaskBoardsMapsLegacyEnumAndCounts(t *testing.T) {
	repo := mocks.NewMockRepository(t)
	tasks := mocks.NewMockTasks(t)
	repo.EXPECT().Tasks().Return(tasks)
	tasks.EXPECT().ListTaskBoards(mock.Anything).Return([]entity.TaskBoardEntry{
		{Id: 2, Name: "Design", Slug: sql.NullString{String: "design", Valid: true}, Position: 1, TaskCount: 4, ArchivedTaskCount: 1},
		{Id: 7, Name: "samples", Position: 6, CreatedBy: "olya"},
	}, nil)

	resp, err := (&Server{repo: repo}).ListTaskBoards(context.Background(), &pb_admin.ListTaskBoardsRequest{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(resp.Boards) != 2 {
		t.Fatalf("want 2 boards, got %d", len(resp.Boards))
	}
	d, c := resp.Boards[0], resp.Boards[1]
	if d.Id != 2 || d.Name != "Design" || d.LegacyBoard != pb_common.TaskBoard_TASK_BOARD_DESIGN || d.TaskCount != 4 || d.ArchivedTaskCount != 1 {
		t.Errorf("seeded board mapped wrong: %+v", d)
	}
	if c.Id != 7 || c.LegacyBoard != pb_common.TaskBoard_TASK_BOARD_UNKNOWN || c.CreatedBy != "olya" {
		t.Errorf("custom board mapped wrong: %+v", c)
	}
}

// AddTaskBoard trims the name, stamps created_by and answers the stored row.
func TestAddTaskBoardTrimsAndStamps(t *testing.T) {
	repo := mocks.NewMockRepository(t)
	tasks := mocks.NewMockTasks(t)
	repo.EXPECT().Tasks().Return(tasks)
	tasks.EXPECT().AddTaskBoard(mock.Anything, "samples", "olya").Return(7, nil)
	tasks.EXPECT().GetTaskBoardById(mock.Anything, 7).Return(&entity.TaskBoardEntry{Id: 7, Name: "samples", Position: 6}, nil)

	ctx := authsrv.PutAdminUsername(context.Background(), "olya")
	resp, err := (&Server{repo: repo}).AddTaskBoard(ctx, &pb_admin.AddTaskBoardRequest{Name: "  samples \n"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.Board.GetId() != 7 || resp.Board.GetPosition() != 6 {
		t.Errorf("unexpected board: %+v", resp.Board)
	}
}

func TestAddTaskBoardNameValidation(t *testing.T) {
	for _, name := range []string{"", "   ", strings.Repeat("я", entity.TaskBoardNameMaxLen+1)} {
		repo := mocks.NewMockRepository(t)
		_, err := (&Server{repo: repo}).AddTaskBoard(context.Background(), &pb_admin.AddTaskBoardRequest{Name: name})
		if status.Code(err) != codes.InvalidArgument {
			t.Errorf("name %q: want InvalidArgument, got %v", name, err)
		}
	}
	// Exactly the limit, counted in characters (not bytes), passes validation.
	repo := mocks.NewMockRepository(t)
	tasks := mocks.NewMockTasks(t)
	repo.EXPECT().Tasks().Return(tasks)
	long := strings.Repeat("я", entity.TaskBoardNameMaxLen)
	tasks.EXPECT().AddTaskBoard(mock.Anything, long, "").Return(8, nil)
	tasks.EXPECT().GetTaskBoardById(mock.Anything, 8).Return(&entity.TaskBoardEntry{Id: 8, Name: long}, nil)
	if _, err := (&Server{repo: repo}).AddTaskBoard(context.Background(), &pb_admin.AddTaskBoardRequest{Name: long}); err != nil {
		t.Errorf("64-character name must pass: %v", err)
	}
}

func TestAddTaskBoardDuplicateNameIsAlreadyExists(t *testing.T) {
	repo := mocks.NewMockRepository(t)
	tasks := mocks.NewMockTasks(t)
	repo.EXPECT().Tasks().Return(tasks)
	dup := errors.New("1062")
	tasks.EXPECT().AddTaskBoard(mock.Anything, "Design", "").Return(0, dup)
	repo.EXPECT().IsErrUniqueViolation(dup).Return(true)

	_, err := (&Server{repo: repo}).AddTaskBoard(context.Background(), &pb_admin.AddTaskBoardRequest{Name: "Design"})
	if status.Code(err) != codes.AlreadyExists {
		t.Fatalf("want AlreadyExists, got %v", err)
	}
}

func TestUpdateTaskBoardErrors(t *testing.T) {
	if _, err := (&Server{repo: mocks.NewMockRepository(t)}).UpdateTaskBoard(context.Background(),
		&pb_admin.UpdateTaskBoardRequest{Name: "x"}); status.Code(err) != codes.InvalidArgument {
		t.Errorf("missing id: want InvalidArgument, got %v", err)
	}

	repo := mocks.NewMockRepository(t)
	tasks := mocks.NewMockTasks(t)
	repo.EXPECT().Tasks().Return(tasks)
	tasks.EXPECT().RenameTaskBoard(mock.Anything, 5, "x").Return(fmt.Errorf("can't rename task board: %w", sql.ErrNoRows))
	if _, err := (&Server{repo: repo}).UpdateTaskBoard(context.Background(),
		&pb_admin.UpdateTaskBoardRequest{Id: 5, Name: "x"}); status.Code(err) != codes.NotFound {
		t.Errorf("missing board: want NotFound, got %v", err)
	}

	repo2 := mocks.NewMockRepository(t)
	tasks2 := mocks.NewMockTasks(t)
	repo2.EXPECT().Tasks().Return(tasks2)
	dup := errors.New("1062")
	tasks2.EXPECT().RenameTaskBoard(mock.Anything, 5, "content").Return(dup)
	repo2.EXPECT().IsErrUniqueViolation(dup).Return(true)
	if _, err := (&Server{repo: repo2}).UpdateTaskBoard(context.Background(),
		&pb_admin.UpdateTaskBoardRequest{Id: 5, Name: "content"}); status.Code(err) != codes.AlreadyExists {
		t.Errorf("taken name: want AlreadyExists, got %v", err)
	}
}

func TestUpdateTaskBoardReturnsRenamedRow(t *testing.T) {
	repo := mocks.NewMockRepository(t)
	tasks := mocks.NewMockTasks(t)
	repo.EXPECT().Tasks().Return(tasks)
	tasks.EXPECT().RenameTaskBoard(mock.Anything, 2, "Дизайн").Return(nil)
	tasks.EXPECT().GetTaskBoardById(mock.Anything, 2).Return(&entity.TaskBoardEntry{
		Id: 2, Name: "Дизайн", Slug: sql.NullString{String: "design", Valid: true},
	}, nil)
	resp, err := (&Server{repo: repo}).UpdateTaskBoard(context.Background(), &pb_admin.UpdateTaskBoardRequest{Id: 2, Name: "Дизайн"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Renaming a seeded board keeps its legacy enum: old clients still find their lane.
	if resp.Board.GetName() != "Дизайн" || resp.Board.GetLegacyBoard() != pb_common.TaskBoard_TASK_BOARD_DESIGN {
		t.Errorf("unexpected board: %+v", resp.Board)
	}
}

func TestDeleteTaskBoardErrorMapping(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want codes.Code
	}{
		{"ok", nil, codes.OK},
		{"missing", entity.ErrTaskBoardNotFound, codes.NotFound},
		{"last", entity.ErrTaskBoardLast, codes.FailedPrecondition},
		{"not empty", entity.ErrTaskBoardNotEmpty, codes.FailedPrecondition},
		{"bad target", entity.ErrTaskBoardMoveTarget, codes.InvalidArgument},
		{"db", errors.New("boom"), codes.Internal},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			repo := mocks.NewMockRepository(t)
			tasks := mocks.NewMockTasks(t)
			repo.EXPECT().Tasks().Return(tasks)
			var storeErr error
			if tc.err != nil {
				storeErr = fmt.Errorf("can't delete task board: %w", tc.err)
			}
			tasks.EXPECT().DeleteTaskBoard(mock.Anything, 3, 1).Return(storeErr)
			_, err := (&Server{repo: repo}).DeleteTaskBoard(context.Background(), &pb_admin.DeleteTaskBoardRequest{Id: 3, MoveToId: 1})
			if status.Code(err) != tc.want {
				t.Fatalf("want %v, got %v", tc.want, err)
			}
		})
	}
}

func TestDeleteTaskBoardValidation(t *testing.T) {
	s := &Server{repo: mocks.NewMockRepository(t)}
	if _, err := s.DeleteTaskBoard(context.Background(), &pb_admin.DeleteTaskBoardRequest{}); status.Code(err) != codes.InvalidArgument {
		t.Errorf("missing id: want InvalidArgument, got %v", err)
	}
	if _, err := s.DeleteTaskBoard(context.Background(), &pb_admin.DeleteTaskBoardRequest{Id: 3, MoveToId: -2}); status.Code(err) != codes.InvalidArgument {
		t.Errorf("negative move_to_id: want InvalidArgument, got %v", err)
	}
}
