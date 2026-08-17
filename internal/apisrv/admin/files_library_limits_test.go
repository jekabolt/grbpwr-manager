package admin

import (
	"errors"
	"testing"

	mocks "github.com/jekabolt/grbpwr-manager/internal/dependency/mocks"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/jekabolt/grbpwr-manager/internal/rbac"
	pb_admin "github.com/jekabolt/grbpwr-manager/proto/gen/admin"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// Предел — довод вызывающего, а не сбой сервера, и разница видна ровно на экране.
//
// Обе проверки живут в сторе, а хендлеры ловили из него только `sql.ErrNoRows` и нарушение
// внешнего ключа — всё остальное падало в `Internal` с фразой места. То есть человек, упёршийся
// в задокументированный предел, читал «не удалось проставить темы» и не узнавал ни причины, ни
// что делать дальше; клиентская таблица перевода помочь не могла, потому что сообщение до неё
// не доезжало вовсе.
//
// Каждый случай идёт с отрицательным контролем в том же тесте: обычная ошибка стора обязана
// остаться `Internal` с прежней фразой. Без контроля тест доказывал бы только то, что мы
// научились отвечать `InvalidArgument` на что угодно.
// Право на чтение раздела — своё, а не заимствованное у filesWrite: список файлов читают и те,
// кто в него не пишет, и отказ по пределу обязан выглядеть у них так же.
var filesReadOnly = map[string]entity.AccessLevel{rbac.SectionFiles: entity.AccessRead}

func TestLibraryBoundsReachTheCallerAsArgumentErrors(t *testing.T) {
	t.Run("список файлов: предел пересечения тем", func(t *testing.T) {
		files := mocks.NewMockFiles(t)
		files.EXPECT().ListFiles(mock.Anything, mock.Anything).Return(nil, 0,
			entity.NewErrLibraryBatchTooLarge("at most 20 topics can be combined in one filter, got 21"))
		repo := mocks.NewMockRepository(t)
		repo.EXPECT().Files().Return(files)
		s := &Server{repo: repo}

		_, err := s.ListLibraryFiles(ctxAs("pasha", false, filesReadOnly), &pb_admin.ListLibraryFilesRequest{})

		require.Error(t, err)
		require.Equal(t, codes.InvalidArgument, status.Code(err))
		// Само число обязано доехать: «слишком много» без предела нечего исправлять.
		require.Contains(t, status.Convert(err).Message(), "at most 20 topics")
	})

	t.Run("КОНТРОЛЬ: обычная ошибка списка остаётся внутренней", func(t *testing.T) {
		files := mocks.NewMockFiles(t)
		files.EXPECT().ListFiles(mock.Anything, mock.Anything).Return(nil, 0, errors.New("dial tcp: connection refused"))
		repo := mocks.NewMockRepository(t)
		repo.EXPECT().Files().Return(files)
		s := &Server{repo: repo}

		_, err := s.ListLibraryFiles(ctxAs("pasha", false, filesReadOnly), &pb_admin.ListLibraryFilesRequest{})

		require.Error(t, err)
		require.Equal(t, codes.Internal, status.Code(err))
		// Внутренности наружу не уходят — на месте прежняя фраза места.
		require.Equal(t, "can't list files", status.Convert(err).Message())
	})

	t.Run("проставление тем: предел числа файлов за раз", func(t *testing.T) {
		files := mocks.NewMockFiles(t)
		files.EXPECT().AssignTopics(mock.Anything, mock.Anything, mock.Anything, mock.Anything).Return(0,
			entity.NewErrLibraryBatchTooLarge("at most 200 files can be labelled in one call, got 201"))
		repo := mocks.NewMockRepository(t)
		repo.EXPECT().Files().Return(files)
		s := &Server{repo: repo}

		_, err := s.AssignLibraryFileTopics(ctxAs("pasha", false, filesWrite), &pb_admin.AssignLibraryFileTopicsRequest{
			FileIds:  []int32{1, 2},
			TopicIds: []int32{3},
		})

		require.Error(t, err)
		require.Equal(t, codes.InvalidArgument, status.Code(err))
		require.Contains(t, status.Convert(err).Message(), "at most 200 files")
	})

	t.Run("КОНТРОЛЬ: обычная ошибка проставления остаётся внутренней", func(t *testing.T) {
		files := mocks.NewMockFiles(t)
		files.EXPECT().AssignTopics(mock.Anything, mock.Anything, mock.Anything, mock.Anything).
			Return(0, errors.New("deadlock found when trying to get lock"))
		repo := mocks.NewMockRepository(t)
		repo.EXPECT().Files().Return(files)
		repo.EXPECT().IsErrForeignKeyViolation(mock.Anything).Return(false)
		s := &Server{repo: repo}

		_, err := s.AssignLibraryFileTopics(ctxAs("pasha", false, filesWrite), &pb_admin.AssignLibraryFileTopicsRequest{
			FileIds:  []int32{1, 2},
			TopicIds: []int32{3},
		})

		require.Error(t, err)
		require.Equal(t, codes.Internal, status.Code(err))
		require.Equal(t, "can't assign topics", status.Convert(err).Message())
	})
}
