package admin

import (
	"context"
	"database/sql"
	"strings"
	"testing"
	"time"

	mocks "github.com/jekabolt/grbpwr-manager/internal/dependency/mocks"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	pb_admin "github.com/jekabolt/grbpwr-manager/proto/gen/admin"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// storedComment — реплика, вокруг которой спорят все случаи ниже: её написал pasha, аккаунт
// которого жив (ссылка на него не занулена).
func storedComment() *entity.LibraryFileComment {
	return &entity.LibraryFileComment{
		Id:        5,
		FileId:    7,
		Author:    "pasha",
		AuthorId:  sql.NullInt64{Int64: 1, Valid: true},
		Body:      "это финальная версия?",
		CreatedAt: time.Date(2026, 8, 17, 10, 0, 0, 0, time.UTC),
	}
}

// TestLibraryFileCommentOnlyItsAuthorMayChangeIt — весь смысл хендлеров правки и удаления.
//
// files:write есть у всех, кто работает с библиотекой. Без этой проверки любой из них переписал бы
// чужую реплику задним числом, и обсуждение перестало бы быть свидетельством о том, что сказали.
func TestLibraryFileCommentOnlyItsAuthorMayChangeIt(t *testing.T) {
	updateReq := &pb_admin.UpdateLibraryFileCommentRequest{Id: 5, Body: "поправил"}
	deleteReq := &pb_admin.DeleteLibraryFileCommentRequest{Id: 5}

	t.Run("чужую реплику не правит и не удаляет даже files:write", func(t *testing.T) {
		files := mocks.NewMockFiles(t)
		files.EXPECT().GetCommentById(mock.Anything, 5).Return(storedComment(), nil)
		repo := mocks.NewMockRepository(t)
		repo.EXPECT().Files().Return(files)
		s := &Server{repo: repo}

		_, err := s.UpdateLibraryFileComment(ctxAs("mallory", false, filesWrite), updateReq)
		require.Equal(t, codes.PermissionDenied, status.Code(err))
		_, err = s.DeleteLibraryFileComment(ctxAs("mallory", false, filesWrite), deleteReq)
		require.Equal(t, codes.PermissionDenied, status.Code(err))
		// Ожиданий на запись нет: mockery роняет тест на вызове неожидаемого метода, и это и есть
		// доказательство, что отказ случился ДО записи, а не после неё.
		files.AssertNotCalled(t, "UpdateComment", mock.Anything, mock.Anything, mock.Anything)
		files.AssertNotCalled(t, "DeleteComment", mock.Anything, mock.Anything)
	})

	t.Run("контекст без авторизации не считается ничьим автором", func(t *testing.T) {
		files := mocks.NewMockFiles(t)
		files.EXPECT().GetCommentById(mock.Anything, 5).Return(storedComment(), nil)
		repo := mocks.NewMockRepository(t)
		repo.EXPECT().Files().Return(files)
		s := &Server{repo: repo}

		_, err := s.UpdateLibraryFileComment(context.Background(), updateReq)
		require.Equal(t, codes.PermissionDenied, status.Code(err))
	})

	for _, tc := range []struct {
		name  string
		who   string
		super bool
	}{
		{name: "автор", who: "pasha"},
		{name: "супер-админ", who: "someone-else", super: true},
	} {
		t.Run(tc.name+" правит и удаляет", func(t *testing.T) {
			perms := filesWrite
			if tc.super {
				perms = nil
			}
			edited := storedComment()
			edited.Body = "поправил"
			edited.EditedAt = sql.NullTime{Time: time.Date(2026, 8, 17, 11, 0, 0, 0, time.UTC), Valid: true}

			files := mocks.NewMockFiles(t)
			files.EXPECT().GetCommentById(mock.Anything, 5).Return(storedComment(), nil)
			files.EXPECT().UpdateComment(mock.Anything, 5, "поправил").Return(edited, nil)
			files.EXPECT().DeleteComment(mock.Anything, 5).Return(nil)
			repo := mocks.NewMockRepository(t)
			repo.EXPECT().Files().Return(files)
			s := &Server{repo: repo}

			resp, err := s.UpdateLibraryFileComment(ctxAs(tc.who, tc.super, perms), updateReq)
			require.NoError(t, err)
			// Метка «изменено» приезжает с сервера, а не рисуется клиентом по факту отправки.
			require.NotNil(t, resp.Comment.EditedAt)
			require.Equal(t, "поправил", resp.Comment.Body)

			_, err = s.DeleteLibraryFileComment(ctxAs(tc.who, tc.super, perms), deleteReq)
			require.NoError(t, err)
		})
	}

	// РЕГРЕССИЯ НА ПОВТОРНОЕ ЗАНЯТИЕ ИМЕНИ. Строка author переживает удаление аккаунта, а UNIQUE
	// на admins.username освобождает имя — значит нового человека под тем же именем нельзя пускать
	// в чужую переписку по одному совпадению строки. Та же проверка, что у владельцев файла.
	t.Run("новый аккаунт под освободившимся именем не наследует чужие реплики", func(t *testing.T) {
		orphaned := storedComment()
		orphaned.AuthorId = sql.NullInt64{} // аккаунт удалён, ссылка занулена SET NULL

		files := mocks.NewMockFiles(t)
		files.EXPECT().GetCommentById(mock.Anything, 5).Return(orphaned, nil)
		repo := mocks.NewMockRepository(t)
		repo.EXPECT().Files().Return(files)
		s := &Server{repo: repo}

		_, err := s.UpdateLibraryFileComment(ctxAs("pasha", false, filesWrite), updateReq)
		require.Equal(t, codes.PermissionDenied, status.Code(err))
		_, err = s.DeleteLibraryFileComment(ctxAs("pasha", false, filesWrite), deleteReq)
		require.Equal(t, codes.PermissionDenied, status.Code(err))

		// Супер по-прежнему может убрать такую реплику: иначе она стала бы вечной.
		files2 := mocks.NewMockFiles(t)
		files2.EXPECT().GetCommentById(mock.Anything, 5).Return(orphaned, nil)
		files2.EXPECT().DeleteComment(mock.Anything, 5).Return(nil)
		repo2 := mocks.NewMockRepository(t)
		repo2.EXPECT().Files().Return(files2)
		s2 := &Server{repo: repo2}
		_, err = s2.DeleteLibraryFileComment(ctxAs("boss", true, nil), deleteReq)
		require.NoError(t, err)
	})

	t.Run("пропавшая реплика — NotFound, а не отказ в правах", func(t *testing.T) {
		files := mocks.NewMockFiles(t)
		files.EXPECT().GetCommentById(mock.Anything, 5).Return(nil, sql.ErrNoRows)
		repo := mocks.NewMockRepository(t)
		repo.EXPECT().Files().Return(files)
		s := &Server{repo: repo}

		_, err := s.UpdateLibraryFileComment(ctxAs("pasha", false, filesWrite), updateReq)
		require.Equal(t, codes.NotFound, status.Code(err))
	})

	t.Run("гонка: свою реплику удалили между чтением и записью", func(t *testing.T) {
		files := mocks.NewMockFiles(t)
		files.EXPECT().GetCommentById(mock.Anything, 5).Return(storedComment(), nil)
		files.EXPECT().UpdateComment(mock.Anything, 5, "поправил").Return(nil, sql.ErrNoRows)
		repo := mocks.NewMockRepository(t)
		repo.EXPECT().Files().Return(files)
		s := &Server{repo: repo}

		_, err := s.UpdateLibraryFileComment(ctxAs("pasha", false, filesWrite), updateReq)
		require.Equal(t, codes.NotFound, status.Code(err))
	})
}

// TestAddLibraryFileCommentStampsTheAuthorItself: подпись под словами не может быть полем формы.
func TestAddLibraryFileCommentStampsTheAuthorItself(t *testing.T) {
	t.Run("автор берётся из JWT, а текст ложится как набран", func(t *testing.T) {
		stored := storedComment()
		stored.Body = "@kirill глянь, это финал?"
		files := mocks.NewMockFiles(t)
		// Имя автора приходит третьим аргументом ИЗ КОНТЕКСТА; тело — с обрезанными краями и с
		// упоминанием как плоским текстом: сервер его не разбирает и не размечает.
		files.EXPECT().AddComment(mock.Anything, 7, "kirill", "@kirill глянь, это финал?").Return(stored, nil)
		repo := mocks.NewMockRepository(t)
		repo.EXPECT().Files().Return(files)
		s := &Server{repo: repo}

		resp, err := s.AddLibraryFileComment(ctxAs("kirill", false, filesWrite),
			&pb_admin.AddLibraryFileCommentRequest{FileId: 7, Body: "  @kirill глянь, это финал?  "})
		require.NoError(t, err)
		require.Equal(t, "pasha", resp.Comment.Author)
		require.Equal(t, int32(1), resp.Comment.AuthorId)
		// Реплику никто не правил: время правки обязано ехать пустым, иначе лента напечатает
		// «изменено» на всём, что вообще существует.
		require.Nil(t, resp.Comment.EditedAt)
		require.NotNil(t, resp.Comment.CreatedAt)
	})

	t.Run("пустое тело и пустой файл не доезжают до стора", func(t *testing.T) {
		s := &Server{}
		_, err := s.AddLibraryFileComment(ctxAs("kirill", false, filesWrite),
			&pb_admin.AddLibraryFileCommentRequest{FileId: 7, Body: "   \n  "})
		require.Equal(t, codes.InvalidArgument, status.Code(err))

		_, err = s.AddLibraryFileComment(ctxAs("kirill", false, filesWrite),
			&pb_admin.AddLibraryFileCommentRequest{FileId: 0, Body: "текст"})
		require.Equal(t, codes.InvalidArgument, status.Code(err))

		// Предел меряется в рунах, а не в байтах: кириллице нельзя разрешать вдвое меньше текста,
		// чем латинице, и в TEXT (65535 байт) обязано влезать всё, что сюда пропущено.
		_, err = s.AddLibraryFileComment(ctxAs("kirill", false, filesWrite),
			&pb_admin.AddLibraryFileCommentRequest{FileId: 7, Body: strings.Repeat("я", 10001)})
		require.Equal(t, codes.InvalidArgument, status.Code(err))
	})

	t.Run("реплика без автора не пишется вовсе", func(t *testing.T) {
		s := &Server{}
		_, err := s.AddLibraryFileComment(context.Background(),
			&pb_admin.AddLibraryFileCommentRequest{FileId: 7, Body: "текст"})
		require.Equal(t, codes.PermissionDenied, status.Code(err))
	})

	t.Run("файла нет — NotFound", func(t *testing.T) {
		files := mocks.NewMockFiles(t)
		files.EXPECT().AddComment(mock.Anything, 7, "kirill", "текст").Return(nil, sql.ErrNoRows)
		repo := mocks.NewMockRepository(t)
		repo.EXPECT().Files().Return(files)
		s := &Server{repo: repo}

		_, err := s.AddLibraryFileComment(ctxAs("kirill", false, filesWrite),
			&pb_admin.AddLibraryFileCommentRequest{FileId: 7, Body: "текст"})
		require.Equal(t, codes.NotFound, status.Code(err))
	})
}

// TestListLibraryFileCommentsFeedShape пришпиливает то, чем лента отличается от списка строк:
// имя автора переживает его аккаунт, а «изменено» отличимо от «не правилось».
func TestListLibraryFileCommentsFeedShape(t *testing.T) {
	feed := []entity.LibraryFileComment{
		{
			Id: 1, FileId: 7, Author: "pasha", AuthorId: sql.NullInt64{Int64: 1, Valid: true},
			Body: "первая", CreatedAt: time.Date(2026, 8, 17, 10, 0, 0, 0, time.UTC),
		},
		{
			// Аккаунт удалён: ссылки нет, ИМЯ ОСТАЛОСЬ. Иначе переписка задним числом теряет
			// говорящих и перестаёт быть перепиской.
			Id: 2, FileId: 7, Author: "leaver", Body: "вторая, поправленная",
			CreatedAt: time.Date(2026, 8, 17, 10, 5, 0, 0, time.UTC),
			EditedAt:  sql.NullTime{Time: time.Date(2026, 8, 17, 10, 6, 0, 0, time.UTC), Valid: true},
		},
	}
	files := mocks.NewMockFiles(t)
	files.EXPECT().ListComments(mock.Anything, 7).Return(feed, nil)
	repo := mocks.NewMockRepository(t)
	repo.EXPECT().Files().Return(files)
	s := &Server{repo: repo}

	resp, err := s.ListLibraryFileComments(ctxAs("kirill", false, filesWrite),
		&pb_admin.ListLibraryFileCommentsRequest{Id: 7})
	require.NoError(t, err)
	require.Len(t, resp.Comments, 2)
	require.Equal(t, int32(1), resp.Comments[0].AuthorId)
	require.Nil(t, resp.Comments[0].EditedAt)
	require.Equal(t, "leaver", resp.Comments[1].Author)
	require.Zero(t, resp.Comments[1].AuthorId, "0 значит «аккаунта нет», а не «автор неизвестен»")
	require.NotNil(t, resp.Comments[1].EditedAt)

	s2 := &Server{}
	_, err = s2.ListLibraryFileComments(ctxAs("kirill", false, filesWrite),
		&pb_admin.ListLibraryFileCommentsRequest{Id: 0})
	require.Equal(t, codes.InvalidArgument, status.Code(err))
}
