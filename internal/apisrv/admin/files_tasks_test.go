package admin

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"

	mocks "github.com/jekabolt/grbpwr-manager/internal/dependency/mocks"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	pb_admin "github.com/jekabolt/grbpwr-manager/proto/gen/admin"
	pb_common "github.com/jekabolt/grbpwr-manager/proto/gen/common"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// TestListLibraryFileTasksDrawsTheRow проверяет то единственное, ради чего RPC существует: строка
// карточки файла доезжает целиком — pill, заголовок, колонка, исполнитель, срок и ДОСКА. Доска
// уезжала бы молча (её никто не смотрит глазами), а без неё строка не отвечает на вопрос «где живёт
// работа», ради которого её и добавили в контракт.
func TestListLibraryFileTasksDrawsTheRow(t *testing.T) {
	due := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)
	tasks := mocks.NewMockTasks(t)
	tasks.EXPECT().ListTasksByFileId(mock.Anything, 7).Return([]entity.LibraryFileTask{
		{
			TaskId:   42,
			Title:    "отшить семпл",
			Status:   entity.TaskStatusInProgress,
			Assignee: "kirill",
			DueDate:  sql.NullTime{Time: due, Valid: true},
			Board:    entity.TaskBoardProduction,
		},
		// Вторая строка — «задачу никто не взял и срока нет»: это СОСТОЯНИЕ, а не пропуск, и
		// пустой срок обязан приезжать отсутствующим полем, а не нулевым временем (иначе карточка
		// нарисует 1970 год как дедлайн).
		{TaskId: 43, Title: "снять мерки", Status: entity.TaskStatusTodo, Board: entity.TaskBoardDesign},
	}, nil)
	repo := mocks.NewMockRepository(t)
	repo.EXPECT().Tasks().Return(tasks)
	s := &Server{repo: repo}

	resp, err := s.ListLibraryFileTasks(context.Background(), &pb_admin.ListLibraryFileTasksRequest{Id: 7})
	require.NoError(t, err)
	require.Len(t, resp.Tasks, 2)

	got := resp.Tasks[0]
	require.Equal(t, int32(42), got.TaskId)
	require.Equal(t, "отшить семпл", got.Title)
	require.Equal(t, pb_common.TaskStatus_TASK_STATUS_IN_PROGRESS, got.Status)
	require.Equal(t, "kirill", got.Assignee)
	require.Equal(t, pb_common.TaskBoard_TASK_BOARD_PRODUCTION, got.Board)
	require.NotNil(t, got.DueDate)
	require.True(t, due.Equal(got.DueDate.AsTime()))

	require.Nil(t, resp.Tasks[1].DueDate, "срока нет — поля нет; ноль времени карточка покажет как дедлайн 1970 года")
	require.Empty(t, resp.Tasks[1].Assignee)
	require.Equal(t, pb_common.TaskBoard_TASK_BOARD_DESIGN, resp.Tasks[1].Board)
}

// Файл без задач отвечает пустым списком, а не ошибкой: «этот файл нигде не используется» —
// законченный ответ, на котором карточка рисует своё пустое состояние и разрешает удаление.
func TestListLibraryFileTasksEmptyIsAnAnswer(t *testing.T) {
	tasks := mocks.NewMockTasks(t)
	tasks.EXPECT().ListTasksByFileId(mock.Anything, 7).Return(nil, nil)
	repo := mocks.NewMockRepository(t)
	repo.EXPECT().Tasks().Return(tasks)
	s := &Server{repo: repo}

	resp, err := s.ListLibraryFileTasks(context.Background(), &pb_admin.ListLibraryFileTasksRequest{Id: 7})
	require.NoError(t, err)
	require.Empty(t, resp.Tasks)
}

// Нулевой id — отказ ДО похода в стор: у мока нет ожидания на ListTasksByFileId, и это и есть
// проверка, что запрос без предмета не превращается в чтение по id 0.
func TestLibraryFileTaskIdsAreRequired(t *testing.T) {
	s := &Server{repo: mocks.NewMockRepository(t)}

	_, err := s.ListLibraryFileTasks(context.Background(), &pb_admin.ListLibraryFileTasksRequest{})
	require.Equal(t, codes.InvalidArgument, status.Code(err))

	_, err = s.AttachLibraryFileToTask(context.Background(), &pb_admin.AttachLibraryFileToTaskRequest{TaskId: 1})
	require.Equal(t, codes.InvalidArgument, status.Code(err))
	_, err = s.AttachLibraryFileToTask(context.Background(), &pb_admin.AttachLibraryFileToTaskRequest{FileId: 1})
	require.Equal(t, codes.InvalidArgument, status.Code(err))

	_, err = s.DetachLibraryFileFromTask(context.Background(), &pb_admin.DetachLibraryFileFromTaskRequest{TaskId: 1})
	require.Equal(t, codes.InvalidArgument, status.Code(err))
	_, err = s.DetachLibraryFileFromTask(context.Background(), &pb_admin.DetachLibraryFileFromTaskRequest{FileId: 1})
	require.Equal(t, codes.InvalidArgument, status.Code(err))
}

// Привязка и отвязка ОТДАЮТ УСПЕХ на повторе: стор идемпотентен, и хендлер не имеет права
// придумывать поверх него ошибку — обе кнопки описывают желаемое состояние, а не событие.
func TestAttachDetachAreIdempotentAtTheHandler(t *testing.T) {
	tasks := mocks.NewMockTasks(t)
	tasks.EXPECT().AttachFileToTask(mock.Anything, 7, 42).Return(nil).Twice()
	tasks.EXPECT().DetachFileFromTask(mock.Anything, 7, 42).Return(nil).Twice()
	repo := mocks.NewMockRepository(t)
	repo.EXPECT().Tasks().Return(tasks)
	s := &Server{repo: repo}

	for range 2 {
		_, err := s.AttachLibraryFileToTask(context.Background(),
			&pb_admin.AttachLibraryFileToTaskRequest{FileId: 7, TaskId: 42})
		require.NoError(t, err)
	}
	for range 2 {
		_, err := s.DetachLibraryFileFromTask(context.Background(),
			&pb_admin.DetachLibraryFileFromTaskRequest{FileId: 7, TaskId: 42})
		require.NoError(t, err)
	}
}

// Исчезнувший конец связи — NotFound, а не Internal и не InvalidArgument: запрос был понятен, а мир
// под ним изменился (задачу удалили в соседней вкладке). 500 отправил бы человека к разработчику
// вместо кнопки «обновить».
func TestAttachLibraryFileToTaskMissingEndIsNotFound(t *testing.T) {
	tasks := mocks.NewMockTasks(t)
	tasks.EXPECT().AttachFileToTask(mock.Anything, 7, 42).Return(errors.New("fk"))
	repo := mocks.NewMockRepository(t)
	repo.EXPECT().Tasks().Return(tasks)
	repo.EXPECT().IsErrForeignKeyViolation(mock.Anything).Return(true)
	s := &Server{repo: repo}

	_, err := s.AttachLibraryFileToTask(context.Background(),
		&pb_admin.AttachLibraryFileToTaskRequest{FileId: 7, TaskId: 42})
	require.Equal(t, codes.NotFound, status.Code(err))

	// Любая другая ошибка стора остаётся Internal — NotFound нельзя раздавать вслепую, иначе
	// упавшая база выглядела бы как «задачи нет», и привязку молча потеряли бы.
	tasks2 := mocks.NewMockTasks(t)
	tasks2.EXPECT().AttachFileToTask(mock.Anything, 7, 42).Return(context.DeadlineExceeded)
	repo2 := mocks.NewMockRepository(t)
	repo2.EXPECT().Tasks().Return(tasks2)
	repo2.EXPECT().IsErrForeignKeyViolation(mock.Anything).Return(false)
	s2 := &Server{repo: repo2}

	_, err = s2.AttachLibraryFileToTask(context.Background(),
		&pb_admin.AttachLibraryFileToTaskRequest{FileId: 7, TaskId: 42})
	require.Equal(t, codes.Internal, status.Code(err))
}
