package admin

import (
	"context"
	"database/sql"
	"testing"
	"time"

	mocks "github.com/jekabolt/grbpwr-manager/internal/dependency/mocks"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/jekabolt/grbpwr-manager/internal/rbac"
	pb_admin "github.com/jekabolt/grbpwr-manager/proto/gen/admin"
	pb_common "github.com/jekabolt/grbpwr-manager/proto/gen/common"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

var tasksWrite = map[string]entity.AccessLevel{rbac.SectionTasks: entity.AccessWrite}

// storedTaskComment — реплика, вокруг которой спорят все случаи ниже: её написал pasha, аккаунт
// которого ЖИВ (ссылка на него не занулена).
func storedTaskComment() *entity.TaskComment {
	return &entity.TaskComment{
		Id:        5,
		TaskId:    7,
		Author:    "pasha",
		AuthorId:  sql.NullInt32{Int32: 1, Valid: true},
		Body:      "я это доделаю завтра",
		CreatedAt: time.Date(2026, 8, 25, 10, 0, 0, 0, time.UTC),
	}
}

// TestTaskCommentOnlyItsAuthorMayDeleteIt — весь смысл второго гейта DeleteTaskComment.
//
// ЗЕРКАЛО TestLibraryFileCommentOnlyItsAuthorMayChangeIt (files_comments_test.go), и это сказано
// вслух: гейты скопированы построчно, значит и их доказательства обязаны спрашивать одно и то же —
// иначе они разойдутся на первой правке, причём разойдутся молча.
//
// tasks:write есть у всех, кто вообще работает с доской. Без этой проверки любой из них стирал бы
// чужие реплики, и журнал обсуждения перестал бы быть свидетельством о том, что сказали.
func TestTaskCommentOnlyItsAuthorMayDeleteIt(t *testing.T) {
	deleteReq := &pb_admin.DeleteTaskCommentRequest{Id: 5}

	t.Run("чужую реплику не удаляет даже tasks:write", func(t *testing.T) {
		tasks := mocks.NewMockTasks(t)
		tasks.EXPECT().GetTaskCommentById(mock.Anything, 5).Return(storedTaskComment(), nil)
		repo := mocks.NewMockRepository(t)
		repo.EXPECT().Tasks().Return(tasks)
		s := &Server{repo: repo}

		_, err := s.DeleteTaskComment(ctxAs("mallory", false, tasksWrite), deleteReq)
		require.Equal(t, codes.PermissionDenied, status.Code(err))
		// Ожидания на DeleteTaskComment нет вовсе: mockery роняет тест на вызове неожидаемого
		// метода, и это и есть доказательство, что отказ случился ДО удаления, а не после него.
		tasks.AssertNotCalled(t, "DeleteTaskComment", mock.Anything, mock.Anything)
	})

	t.Run("контекст без авторизации не считается ничьим автором", func(t *testing.T) {
		tasks := mocks.NewMockTasks(t)
		tasks.EXPECT().GetTaskCommentById(mock.Anything, 5).Return(storedTaskComment(), nil)
		repo := mocks.NewMockRepository(t)
		repo.EXPECT().Tasks().Return(tasks)
		s := &Server{repo: repo}

		_, err := s.DeleteTaskComment(context.Background(), deleteReq)
		require.Equal(t, codes.PermissionDenied, status.Code(err))
		tasks.AssertNotCalled(t, "DeleteTaskComment", mock.Anything, mock.Anything)
	})

	for _, tc := range []struct {
		name  string
		who   string
		super bool
	}{
		{name: "автор", who: "pasha"},
		{name: "супер-админ", who: "someone-else", super: true},
	} {
		t.Run(tc.name+" удаляет", func(t *testing.T) {
			perms := tasksWrite
			if tc.super {
				perms = nil
			}
			tasks := mocks.NewMockTasks(t)
			tasks.EXPECT().GetTaskCommentById(mock.Anything, 5).Return(storedTaskComment(), nil)
			tasks.EXPECT().DeleteTaskComment(mock.Anything, 5).Return(nil)
			repo := mocks.NewMockRepository(t)
			repo.EXPECT().Tasks().Return(tasks)
			s := &Server{repo: repo}

			_, err := s.DeleteTaskComment(ctxAs(tc.who, tc.super, perms), deleteReq)
			require.NoError(t, err)
		})
	}

	// РЕГРЕССИЯ НА ПОВТОРНОЕ ЗАНЯТИЕ ИМЕНИ — единственный случай, ради которого в 0339 вообще
	// заводилась колонка author_id.
	//
	// Строка author переживает удаление аккаунта, а UNIQUE на admins.username освобождает имя:
	// удалили pasha → author_id его реплик занулился, строка «pasha» осталась → завели НОВОГО pasha.
	// По одному совпадению строки он получил бы власть над всей перепиской прежнего.
	//
	// ЭТОТ КЕЙС НЕ ДУБЛИРУЕТ СОСЕДНИЙ: там имя НЕ совпадает, здесь совпадает — и отказ обязан
	// прийти именно из-за мёртвой ссылки. Убери `c.AuthorId.Valid &&` из гейта — покраснеет ровно он
	// и только он.
	t.Run("новый аккаунт под освободившимся именем не наследует чужие реплики", func(t *testing.T) {
		orphaned := storedTaskComment()
		orphaned.AuthorId = sql.NullInt32{} // аккаунт удалён, ссылка занулена SET NULL

		tasks := mocks.NewMockTasks(t)
		tasks.EXPECT().GetTaskCommentById(mock.Anything, 5).Return(orphaned, nil)
		repo := mocks.NewMockRepository(t)
		repo.EXPECT().Tasks().Return(tasks)
		s := &Server{repo: repo}

		_, err := s.DeleteTaskComment(ctxAs("pasha", false, tasksWrite), deleteReq)
		require.Equal(t, codes.PermissionDenied, status.Code(err))
		tasks.AssertNotCalled(t, "DeleteTaskComment", mock.Anything, mock.Anything)
	})

	// Исчезнувшая реплика — NotFound, а не «удалено». «Удалено» про то, чего нет, оставляет строку
	// на экране и человека в уверенности, что она ушла.
	t.Run("несуществующая реплика — NotFound", func(t *testing.T) {
		tasks := mocks.NewMockTasks(t)
		tasks.EXPECT().GetTaskCommentById(mock.Anything, 5).Return(nil, sql.ErrNoRows)
		repo := mocks.NewMockRepository(t)
		repo.EXPECT().Tasks().Return(tasks)
		s := &Server{repo: repo}

		_, err := s.DeleteTaskComment(ctxAs("pasha", false, tasksWrite), deleteReq)
		require.Equal(t, codes.NotFound, status.Code(err))
	})
}

// TestAddTaskCommentRefusesAnAuthorlessRemark — реплика без автора не принадлежит никому: её не смог
// бы удалить даже написавший, потому что «только свою» ей не с чем сопоставить. Отказ на входе
// дешевле неудаляемой строки в ленте (тот же отказ, что у AddLibraryFileComment).
func TestAddTaskCommentRefusesAnAuthorlessRemark(t *testing.T) {
	repo := mocks.NewMockRepository(t)
	s := &Server{repo: repo}

	_, err := s.AddTaskComment(context.Background(), &pb_admin.AddTaskCommentRequest{
		Comment: &pb_common.TaskCommentInsert{TaskId: 7, Body: "аноним"},
	})
	require.Equal(t, codes.PermissionDenied, status.Code(err))
}
