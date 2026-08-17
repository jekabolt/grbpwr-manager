package admin

import (
	"database/sql"
	"testing"
	"time"

	mocks "github.com/jekabolt/grbpwr-manager/internal/dependency/mocks"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/jekabolt/grbpwr-manager/internal/fileaccess"
	pb_admin "github.com/jekabolt/grbpwr-manager/proto/gen/admin"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// ctxAs, filesWrite и storedFile объявлены в files_people_test.go — круг у доступа и у
// владельцев ОДИН И ТОТ ЖЕ, и общие фикстуры здесь не экономия, а утверждение: разъехавшиеся
// проверки означали бы, что одна из двух дверей к чужому файлу осталась открытой.

// TestGetLibraryFileAccessIsNotFoundOnAnInvisibleFile — точка 12 предиката. Файл, которого
// человек не видит, обязан отвечать NotFound, а не PermissionDenied: второй код подтвердил бы,
// что такой файл существует, а в этой библиотеке секрет — именно имена.
func TestGetLibraryFileAccessIsNotFoundOnAnInvisibleFile(t *testing.T) {
	files := mocks.NewMockFiles(t)
	// Стор под предикатом видимости отдаёт ErrNoRows и на удалённый, и на невидимый файл —
	// хендлеру эти случаи различать нечем, и он не должен.
	files.EXPECT().GetFileAccess(mock.Anything, 7).Return(nil, sql.ErrNoRows)
	repo := mocks.NewMockRepository(t)
	repo.EXPECT().Files().Return(files)
	s := &Server{repo: repo}

	_, err := s.GetLibraryFileAccess(ctxAs("mallory", false, filesWrite), &pb_admin.GetLibraryFileAccessRequest{Id: 7})
	require.Equal(t, codes.NotFound, status.Code(err))
	// Журнал невидимого файла не читается: отказ обязан случиться ДО второго чтения.
	files.AssertNotCalled(t, "ListFileAccessEvents", mock.Anything, mock.Anything, mock.Anything)
}

// TestSetLibraryFileAccessCircle: files:write необходим, но НЕ достаточен. Иначе любой, кто
// вообще работает с библиотекой, опубликовал бы чужой файл в интернет.
func TestSetLibraryFileAccessCircle(t *testing.T) {
	req := &pb_admin.SetLibraryFileAccessRequest{FileId: 7, Level: "link", LinkTtl: 24}

	t.Run("an outsider with files:write is refused and nothing is written", func(t *testing.T) {
		files := mocks.NewMockFiles(t)
		files.EXPECT().GetFileById(mock.Anything, 7).Return(storedFile(), nil)
		repo := mocks.NewMockRepository(t)
		repo.EXPECT().Files().Return(files)
		s := &Server{repo: repo}

		_, err := s.SetLibraryFileAccess(ctxAs("mallory", false, filesWrite), req)
		require.Equal(t, codes.PermissionDenied, status.Code(err))
		files.AssertNotCalled(t, "SetFileAccess", mock.Anything, mock.Anything, mock.Anything)
	})

	t.Run("rotation is guarded by the same circle", func(t *testing.T) {
		files := mocks.NewMockFiles(t)
		files.EXPECT().GetFileById(mock.Anything, 7).Return(storedFile(), nil)
		repo := mocks.NewMockRepository(t)
		repo.EXPECT().Files().Return(files)
		s := &Server{repo: repo}

		_, err := s.RotateLibraryFileLink(ctxAs("mallory", false, filesWrite),
			&pb_admin.RotateLibraryFileLinkRequest{FileId: 7})
		require.Equal(t, codes.PermissionDenied, status.Code(err))
		files.AssertNotCalled(t, "RotateFileLink", mock.Anything, mock.Anything, mock.Anything)
	})

	t.Run("a missing file answers NotFound on the write path too", func(t *testing.T) {
		files := mocks.NewMockFiles(t)
		files.EXPECT().GetFileById(mock.Anything, 7).Return(nil, sql.ErrNoRows)
		repo := mocks.NewMockRepository(t)
		repo.EXPECT().Files().Return(files)
		s := &Server{repo: repo}

		_, err := s.SetLibraryFileAccess(ctxAs("pasha", false, filesWrite), req)
		require.Equal(t, codes.NotFound, status.Code(err))
	})
}

// TestSetLibraryFileAccessAddsTheUploaderImplicitly: человек, выкинувший себя из списка на
// собственном файле, потерял бы и файл, и возможность это починить. Правило держит сервер, а не
// разметка формы.
func TestSetLibraryFileAccessAddsTheUploaderImplicitly(t *testing.T) {
	files := mocks.NewMockFiles(t)
	files.EXPECT().GetFileById(mock.Anything, 7).Return(storedFile(), nil)
	// storedFile(): загрузивший pasha = admins.id 1. Клиент прислал только kirill (9).
	files.EXPECT().SetFileAccess(mock.Anything, 7, entity.LibraryFileAccessUpdate{
		Level:    entity.LibraryFileAccessPeople,
		AdminIDs: []int{9, 1},
		Actor:    "pasha",
	}).Return(&entity.LibraryFileAccess{FileId: 7, Level: entity.LibraryFileAccessPeople}, nil)
	repo := mocks.NewMockRepository(t)
	repo.EXPECT().Files().Return(files)
	s := &Server{repo: repo}

	resp, err := s.SetLibraryFileAccess(ctxAs("pasha", false, filesWrite), &pb_admin.SetLibraryFileAccessRequest{
		FileId:   7,
		Level:    "people",
		AdminIds: []int32{9, 9}, // повтор схлопывается, а не доезжает до уникального ключа
	})
	require.NoError(t, err)
	require.Equal(t, "people", resp.GetAccess().GetLevel())
}

// TestSetLibraryFileAccessRefusesNonsense: неизвестный уровень ОТКАЗЫВАЕТ, а не толкуется —
// «непонятный = team» тихо расширил бы доступ, «= people» тихо потерял бы файл.
func TestSetLibraryFileAccessRefusesNonsense(t *testing.T) {
	s := &Server{}
	for _, tc := range []struct {
		name string
		req  *pb_admin.SetLibraryFileAccessRequest
	}{
		{"unknown level", &pb_admin.SetLibraryFileAccessRequest{FileId: 7, Level: "peoples"}},
		{"empty level", &pb_admin.SetLibraryFileAccessRequest{FileId: 7}},
		{"negative ttl", &pb_admin.SetLibraryFileAccessRequest{FileId: 7, Level: "link", LinkTtl: -1}},
		{"absurd ttl", &pb_admin.SetLibraryFileAccessRequest{FileId: 7, Level: "link", LinkTtl: 1 << 30}},
		{"no file", &pb_admin.SetLibraryFileAccessRequest{Level: "team"}},
	} {
		_, err := s.SetLibraryFileAccess(ctxAs("pasha", true, nil), tc.req)
		require.Equal(t, codes.InvalidArgument, status.Code(err), tc.name)
	}

	// `team` — не фильтр витрины, а её отрицание: пустой ответ читался бы как «ничего не открыто».
	_, err := s.ListSharedLibraryFiles(ctxAs("pasha", true, nil), &pb_admin.ListSharedLibraryFilesRequest{Level: "team"})
	require.Equal(t, codes.InvalidArgument, status.Code(err))
}

// TestAccessBlockShowsTheUrlOnlyAtLevelLink: строка публичной ссылки переживает уровень, поэтому
// url, собранный по одному лишь её наличию, был бы копируемой ссылкой, которая гарантированно
// отвечает 404.
func TestAccessBlockShowsTheUrlOnlyAtLevelLink(t *testing.T) {
	svc, err := fileaccess.New(nil, nil, "test-pepper", "https://backend.example")
	require.NoError(t, err)
	t.Cleanup(svc.Stop)

	linkRow := &entity.LibraryFilePublicAccess{
		FileId: 7, Epoch: 2,
		ExpiresAt: sql.NullTime{Time: time.Now().Add(-time.Hour), Valid: true},
	}

	t.Run("level link mints the url and badges the passed date", func(t *testing.T) {
		files := mocks.NewMockFiles(t)
		files.EXPECT().GetFileAccess(mock.Anything, 7).Return(&entity.LibraryFileAccess{
			FileId: 7, Level: entity.LibraryFileAccessLink, Link: linkRow,
		}, nil)
		files.EXPECT().ListFileAccessEvents(mock.Anything, 7, 0).Return([]entity.LibraryFileAccessEvent{
			{Id: 1, Actor: "pasha", What: "level:link доступ по ссылке", CreatedAt: time.Now()},
		}, nil)
		repo := mocks.NewMockRepository(t)
		repo.EXPECT().Files().Return(files)
		s := &Server{repo: repo, fileLinks: svc}

		resp, err := s.GetLibraryFileAccess(ctxAs("pasha", false, filesWrite), &pb_admin.GetLibraryFileAccessRequest{Id: 7})
		require.NoError(t, err)
		require.Contains(t, resp.GetAccess().GetLink().GetUrl(), "https://backend.example/api/f/f")
		// «истёк» ВЫЧИСЛЯЕТСЯ: прошедший срок не меняет уровень, он гасит маршрут и зажигает бейдж.
		require.True(t, resp.GetAccess().GetLink().GetExpired())
		require.False(t, resp.GetAccess().GetLink().GetRevoked())
		require.Len(t, resp.GetEvents(), 1)
	})

	t.Run("the same row at level team yields no url", func(t *testing.T) {
		files := mocks.NewMockFiles(t)
		files.EXPECT().GetFileAccess(mock.Anything, 7).Return(&entity.LibraryFileAccess{
			FileId: 7, Level: entity.LibraryFileAccessTeam, Link: linkRow,
		}, nil)
		files.EXPECT().ListFileAccessEvents(mock.Anything, 7, 0).Return(nil, nil)
		repo := mocks.NewMockRepository(t)
		repo.EXPECT().Files().Return(files)
		s := &Server{repo: repo, fileLinks: svc}

		resp, err := s.GetLibraryFileAccess(ctxAs("pasha", false, filesWrite), &pb_admin.GetLibraryFileAccessRequest{Id: 7})
		require.NoError(t, err)
		require.Empty(t, resp.GetAccess().GetLink().GetUrl())
		// Состояние строки при этом видно — «ссылка была, сейчас не действует», а не пустота.
		require.NotNil(t, resp.GetAccess().GetLink())
	})
}
