package admin

import (
	"context"
	"database/sql"
	"io"
	"strings"
	"testing"
	"time"

	mocks "github.com/jekabolt/grbpwr-manager/internal/dependency/mocks"
	"github.com/jekabolt/grbpwr-manager/internal/dto"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	pb_admin "github.com/jekabolt/grbpwr-manager/proto/gen/admin"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// storedNote is the note every case below argues about: written by pasha, one version in the bucket.
func storedNote() *entity.LibraryNote {
	return &entity.LibraryNote{
		FileId:           7,
		FileName:         "план съёмки.md",
		ContentType:      dto.LibraryNoteContentType,
		ObjectKey:        "files-library/2026/august/note-v1.md",
		Sha256:           "aaaa",
		SizeBytes:        12,
		ContentUpdatedBy: "pasha",
		ContentUpdatedAt: sql.NullTime{Time: time.Date(2026, 8, 17, 10, 0, 0, 0, time.UTC), Valid: true},
		UploadedBy:       "pasha",
	}
}

// TestSaveLibraryNoteContentConflictIsDataNotOverwrite is the whole point of the phase.
//
// Правка содержимого без перезаливки впервые создаёт столкновение двух правок, и молча затереть
// чужой текст — худшее, что здесь можно сделать. Тест доказывает три вещи разом: расхождение
// отпечатков приезжает ДАННЫМИ (а не ошибкой), чужая версия приезжает ЦЕЛИКОМ, и объект, который
// зовущий успел залить, убирается — то есть в бакете не остаётся байтов, на которые никто не
// указывает.
func TestSaveLibraryNoteContentConflictIsDataNotOverwrite(t *testing.T) {
	const (
		theirKey  = "files-library/2026/august/note-v2.md"
		theirText = "# план\nверсия кирилла"
		mineKey   = "files-library/2026/august/note-mine.md"
	)

	files := mocks.NewMockFiles(t)
	files.EXPECT().GetNote(mock.Anything, 7).Return(storedNote(), nil)
	// Стор не записал НИЧЕГО и вернул то, что лежит.
	files.EXPECT().SaveNoteContent(mock.Anything, mock.Anything).
		Return(&entity.LibraryNoteSaveResult{
			Conflict:         true,
			CurrentSha256:    "bbbb",
			CurrentObjectKey: theirKey,
			LastEditedBy:     "kirill",
			LastEditedAt:     sql.NullTime{Time: time.Date(2026, 8, 17, 11, 0, 0, 0, time.UTC), Valid: true},
		}, nil)

	fs := mocks.NewMockFileStore(t)
	fs.EXPECT().UploadLibraryObject(mock.Anything, mock.Anything, dto.LibraryNoteContentType, dto.LibraryNoteExtension).
		Return(mineKey, "cccc", 20, nil)
	fs.EXPECT().GetLibraryObject(mock.Anything, theirKey).Return([]byte(theirText), nil)
	// Ровно один ключ уходит в уборку — МОЙ. Ожидание именно на нём: mockery валит тест на любой
	// незаявленный вызов, поэтому уборка чужого (то есть живого) объекта здесь недостижима.
	fs.EXPECT().RemoveObjectsByKeys(mock.Anything, mineKey).Return(nil)

	repo := mocks.NewMockRepository(t)
	repo.EXPECT().Files().Return(files)
	s := &Server{repo: repo, bucket: fs}

	resp, err := s.SaveLibraryNoteContent(ctxAs("pasha", false, filesWrite), &pb_admin.SaveLibraryNoteContentRequest{
		FileId:     7,
		Content:    "# план\nверсия паши",
		BaseSha256: "aaaa", // база, с которой начиналась ЭТА правка
	})

	// Конфликт — это успешный ответ с данными: у клиента остаётся и свой буфер, и чужой текст, и
	// база для следующего сохранения, без второго запроса.
	require.NoError(t, err)
	require.True(t, resp.Conflict)
	require.Equal(t, "bbbb", resp.CurrentSha256)
	require.Equal(t, theirText, resp.CurrentContent)
	require.Equal(t, "kirill", resp.LastEditedBy)
	require.NotNil(t, resp.LastEditedAt)
}

// TestSaveLibraryNoteContentSuccessRetiresTheOldObject covers the other branch: запись прошла,
// значит старый объект больше никому не нужен, а эхо собственного текста в ответе не едет.
func TestSaveLibraryNoteContentSuccessRetiresTheOldObject(t *testing.T) {
	const (
		previousKey = "files-library/2026/august/note-v1.md"
		newKey      = "files-library/2026/august/note-v2.md"
	)

	files := mocks.NewMockFiles(t)
	files.EXPECT().GetNote(mock.Anything, 7).Return(storedNote(), nil)

	var saved entity.LibraryNoteSave
	files.EXPECT().SaveNoteContent(mock.Anything, mock.Anything).
		Run(func(_ context.Context, in entity.LibraryNoteSave) { saved = in }).
		Return(&entity.LibraryNoteSaveResult{
			CurrentSha256:     "dddd",
			PreviousObjectKey: previousKey,
			LastEditedBy:      "pasha",
			LastEditedAt:      sql.NullTime{Time: time.Now().UTC(), Valid: true},
		}, nil)

	fs := mocks.NewMockFileStore(t)
	var uploaded string
	fs.EXPECT().UploadLibraryObject(mock.Anything, mock.Anything, dto.LibraryNoteContentType, dto.LibraryNoteExtension).
		Run(func(_ context.Context, r io.Reader, _ string, _ string) {
			b, _ := io.ReadAll(r)
			uploaded = string(b)
		}).
		Return(newKey, "dddd", 30, nil)
	fs.EXPECT().RemoveObjectsByKeys(mock.Anything, previousKey).Return(nil)

	repo := mocks.NewMockRepository(t)
	repo.EXPECT().Files().Return(files)
	s := &Server{repo: repo, bucket: fs}

	resp, err := s.SaveLibraryNoteContent(ctxAs("pasha", false, filesWrite), &pb_admin.SaveLibraryNoteContentRequest{
		FileId:     7,
		Content:    "# план\n\nснимаем в четверг",
		BaseSha256: "aaaa",
	})
	require.NoError(t, err)
	require.False(t, resp.Conflict)
	require.Equal(t, "dddd", resp.CurrentSha256)
	require.Empty(t, resp.CurrentContent, "на успехе эхо собственного текста не едет")

	require.Equal(t, "# план\n\nснимаем в четверг", uploaded, "в бакет уезжает ровно присланный текст")
	// Заливка идёт ПОД НОВЫМ ключом, а в строку едут отпечаток, размер, автор правки и выдержка.
	require.Equal(t, newKey, saved.ObjectKey)
	require.Equal(t, "dddd", saved.Sha256)
	require.Equal(t, int64(30), saved.SizeBytes)
	require.Equal(t, "pasha", saved.EditedBy)
	require.Equal(t, "aaaa", saved.BaseSha256)
	require.False(t, saved.Force)
	require.Equal(t, "план снимаем в четверг", saved.ContentExcerpt)
}

// TestSaveLibraryNoteContentUnreadableConflictRefuses locks the one degradation that must NOT
// degrade quietly: чужая версия не прочиталась. Пустой current_content на конфликте клиент показал
// бы как «коллега стёр заметку», и «записать поверх» выглядело бы безобидным.
func TestSaveLibraryNoteContentUnreadableConflictRefuses(t *testing.T) {
	const mineKey = "files-library/2026/august/note-mine.md"

	files := mocks.NewMockFiles(t)
	files.EXPECT().GetNote(mock.Anything, 7).Return(storedNote(), nil)
	files.EXPECT().SaveNoteContent(mock.Anything, mock.Anything).
		Return(&entity.LibraryNoteSaveResult{
			Conflict:         true,
			CurrentSha256:    "bbbb",
			CurrentObjectKey: "files-library/2026/august/note-v2.md",
			LastEditedBy:     "kirill",
		}, nil)

	fs := mocks.NewMockFileStore(t)
	fs.EXPECT().UploadLibraryObject(mock.Anything, mock.Anything, mock.Anything, mock.Anything).
		Return(mineKey, "cccc", 20, nil)
	fs.EXPECT().RemoveObjectsByKeys(mock.Anything, mineKey).Return(nil)
	fs.EXPECT().GetLibraryObject(mock.Anything, "files-library/2026/august/note-v2.md").
		Return(nil, io.ErrUnexpectedEOF)

	repo := mocks.NewMockRepository(t)
	repo.EXPECT().Files().Return(files)
	s := &Server{repo: repo, bucket: fs}

	_, err := s.SaveLibraryNoteContent(ctxAs("pasha", false, filesWrite), &pb_admin.SaveLibraryNoteContentRequest{
		FileId: 7, Content: "мой текст", BaseSha256: "aaaa",
	})
	require.Equal(t, codes.Internal, status.Code(err))
	require.Contains(t, status.Convert(err).Message(), "nothing was overwritten")
}

// TestLibraryNoteContentLimit proves the cap is enforced BEFORE the bytes are shipped: 512 KiB sent
// to the bucket only to be refused afterwards is paid traffic and an orphan for nothing.
func TestLibraryNoteContentLimit(t *testing.T) {
	tooLong := strings.Repeat("я", entity.MaxLibraryNoteBytes) // кириллица: 2 байта на символ

	t.Run("create refuses without touching the bucket", func(t *testing.T) {
		fs := mocks.NewMockFileStore(t)
		repo := mocks.NewMockRepository(t)
		s := &Server{repo: repo, bucket: fs}

		_, err := s.CreateLibraryNote(ctxAs("pasha", false, filesWrite), &pb_admin.CreateLibraryNoteRequest{
			FileName: "большая", Content: tooLong,
		})
		require.Equal(t, codes.InvalidArgument, status.Code(err))
		require.Contains(t, status.Convert(err).Message(), "not a book")
		fs.AssertNotCalled(t, "UploadLibraryObject", mock.Anything, mock.Anything, mock.Anything, mock.Anything)
	})

	t.Run("save refuses without touching the bucket or the row", func(t *testing.T) {
		fs := mocks.NewMockFileStore(t)
		repo := mocks.NewMockRepository(t)
		s := &Server{repo: repo, bucket: fs}

		_, err := s.SaveLibraryNoteContent(ctxAs("pasha", false, filesWrite), &pb_admin.SaveLibraryNoteContentRequest{
			FileId: 7, Content: tooLong,
		})
		require.Equal(t, codes.InvalidArgument, status.Code(err))
		fs.AssertNotCalled(t, "UploadLibraryObject", mock.Anything, mock.Anything, mock.Anything, mock.Anything)
	})
}

// TestCreateLibraryNoteAppendsExtensionAndStamps covers what the create path derives on its own: the
// `.md` the person did not type, the excerpt that stands in for the missing preview picture, and the
// «правил» stamp that only a note created WITH text earns.
func TestCreateLibraryNoteAppendsExtensionAndStamps(t *testing.T) {
	newFile := func() *entity.LibraryFile {
		f := &entity.LibraryFile{Id: 42}
		f.FileName = "план съёмки.md"
		f.ContentType = dto.LibraryNoteContentType
		f.UploadedBy = "pasha"
		return f
	}

	t.Run("a note with text stamps its editor", func(t *testing.T) {
		files := mocks.NewMockFiles(t)
		var insert entity.LibraryNoteInsert
		files.EXPECT().CreateNote(mock.Anything, mock.Anything, []int{3}, []string{"съёмка"}).
			Run(func(_ context.Context, n *entity.LibraryNoteInsert, _ []int, _ []string) { insert = *n }).
			Return(42, nil)
		files.EXPECT().GetFileById(mock.Anything, 42).Return(newFile(), nil)

		fs := mocks.NewMockFileStore(t)
		fs.EXPECT().UploadLibraryObject(mock.Anything, mock.Anything, dto.LibraryNoteContentType, dto.LibraryNoteExtension).
			Return("files-library/2026/august/note-v1.md", "aaaa", 40, nil)
		// PresignLibraryObject лежит на пути ответа (файл едет с ссылками, как любой другой).
		fs.EXPECT().PresignLibraryObject(mock.Anything, mock.Anything, mock.Anything, mock.Anything).
			Return("", time.Time{}, nil).Maybe()

		repo := mocks.NewMockRepository(t)
		repo.EXPECT().Files().Return(files)
		s := &Server{repo: repo, bucket: fs}

		resp, err := s.CreateLibraryNote(ctxAs("pasha", false, filesWrite), &pb_admin.CreateLibraryNoteRequest{
			FileName:  "план съёмки", // без расширения — его дописывает сервер
			TopicIds:  []int32{3},
			NewTopics: []string{"съёмка"},
			Content:   "# план\n\nснимаем в четверг",
		})
		require.NoError(t, err)
		require.Equal(t, int32(42), resp.File.Id)

		require.Equal(t, "план съёмки.md", insert.FileName)
		require.Equal(t, dto.LibraryNoteContentType, insert.ContentType)
		require.Equal(t, "pasha", insert.UploadedBy)
		require.Equal(t, "план снимаем в четверг", insert.ContentExcerpt)
		require.Equal(t, "pasha", insert.ContentUpdatedBy, "создать заметку С ТЕКСТОМ — это правка")
		require.True(t, insert.ContentUpdatedAt.Valid)
	})

	t.Run("an empty note leaves the editor stamp alone", func(t *testing.T) {
		files := mocks.NewMockFiles(t)
		var insert entity.LibraryNoteInsert
		files.EXPECT().CreateNote(mock.Anything, mock.Anything, mock.Anything, mock.Anything).
			Run(func(_ context.Context, n *entity.LibraryNoteInsert, _ []int, _ []string) { insert = *n }).
			Return(42, nil)
		files.EXPECT().GetFileById(mock.Anything, 42).Return(newFile(), nil)

		fs := mocks.NewMockFileStore(t)
		fs.EXPECT().UploadLibraryObject(mock.Anything, mock.Anything, mock.Anything, mock.Anything).
			Return("files-library/2026/august/note-empty.md", "e3b0", 0, nil)
		fs.EXPECT().PresignLibraryObject(mock.Anything, mock.Anything, mock.Anything, mock.Anything).
			Return("", time.Time{}, nil).Maybe()

		repo := mocks.NewMockRepository(t)
		repo.EXPECT().Files().Return(files)
		s := &Server{repo: repo, bucket: fs}

		_, err := s.CreateLibraryNote(ctxAs("pasha", false, filesWrite), &pb_admin.CreateLibraryNoteRequest{
			FileName: "на завтра.md", // расширение набрано вручную — второго не появляется
		})
		require.NoError(t, err)
		require.Equal(t, "на завтра.md", insert.FileName)
		require.Empty(t, insert.ContentUpdatedBy, "пустую заметку никто не правил")
		require.False(t, insert.ContentUpdatedAt.Valid)
		require.Empty(t, insert.ContentExcerpt)
	})
}

// TestGetLibraryNoteContentBoundaries covers the two refusals of the read path: файла нет и файл не
// заметка. Оба обязаны случиться ДО чтения байтов — карточка чужого файла не должна уметь стать
// способом вытащить его содержимое.
func TestGetLibraryNoteContentBoundaries(t *testing.T) {
	t.Run("a missing file is not found", func(t *testing.T) {
		files := mocks.NewMockFiles(t)
		files.EXPECT().GetNote(mock.Anything, 7).Return(nil, sql.ErrNoRows)
		repo := mocks.NewMockRepository(t)
		repo.EXPECT().Files().Return(files)
		fs := mocks.NewMockFileStore(t)
		s := &Server{repo: repo, bucket: fs}

		_, err := s.GetLibraryNoteContent(ctxAs("pasha", false, filesWrite), &pb_admin.GetLibraryNoteContentRequest{Id: 7})
		require.Equal(t, codes.NotFound, status.Code(err))
		fs.AssertNotCalled(t, "GetLibraryObject", mock.Anything, mock.Anything)
	})

	t.Run("a file that is not a note is refused", func(t *testing.T) {
		note := storedNote()
		note.FileName = "макет.pdf"
		note.ContentType = "application/pdf"

		files := mocks.NewMockFiles(t)
		files.EXPECT().GetNote(mock.Anything, 7).Return(note, nil)
		repo := mocks.NewMockRepository(t)
		repo.EXPECT().Files().Return(files)
		fs := mocks.NewMockFileStore(t)
		s := &Server{repo: repo, bucket: fs}

		_, err := s.GetLibraryNoteContent(ctxAs("pasha", false, filesWrite), &pb_admin.GetLibraryNoteContentRequest{Id: 7})
		require.Equal(t, codes.FailedPrecondition, status.Code(err))
		fs.AssertNotCalled(t, "GetLibraryObject", mock.Anything, mock.Anything)
	})

	t.Run("a note answers with its text and the sha256 of the ROW", func(t *testing.T) {
		files := mocks.NewMockFiles(t)
		files.EXPECT().GetNote(mock.Anything, 7).Return(storedNote(), nil)
		repo := mocks.NewMockRepository(t)
		repo.EXPECT().Files().Return(files)
		fs := mocks.NewMockFileStore(t)
		fs.EXPECT().GetLibraryObject(mock.Anything, "files-library/2026/august/note-v1.md").
			Return([]byte("# план\n\nснимаем в четверг"), nil)
		s := &Server{repo: repo, bucket: fs}

		resp, err := s.GetLibraryNoteContent(ctxAs("pasha", false, filesWrite), &pb_admin.GetLibraryNoteContentRequest{Id: 7})
		require.NoError(t, err)
		require.Equal(t, "# план\n\nснимаем в четверг", resp.Content)
		// Отпечаток — из строки: именно с ним сравнивает сохранение, и счёт по прочитанным байтам
		// разошёлся бы с ним ровно тогда, когда объект и строка перестали соответствовать.
		require.Equal(t, "aaaa", resp.Sha256)
		require.Equal(t, "pasha", resp.LastEditedBy)
		require.NotNil(t, resp.LastEditedAt)
	})
}

// TestLibraryNoteExcerptShapesTheTile pins the derived preview: у `.md` нет первой страницы, поэтому
// плитка показывает начало ТЕКСТА, а не строку с решётками.
func TestLibraryNoteExcerptShapesTheTile(t *testing.T) {
	require.Equal(t, "план съёмки снимаем в четверг",
		dto.LibraryNoteExcerpt("# план съёмки\n\n\nснимаем в четверг\n"))
	require.Equal(t, "пункт один пункт два",
		dto.LibraryNoteExcerpt("- пункт один\n- пункт два"))
	require.Equal(t, "заголовок хвост",
		dto.LibraryNoteExcerpt("## заголовок\n\n```\nrm -rf /\n```\n\n---\n\nхвост"))
	require.Empty(t, dto.LibraryNoteExcerpt("\n\n   \n"))

	long := dto.LibraryNoteExcerpt(strings.Repeat("абв ", 500))
	require.LessOrEqual(t, len([]rune(long)), 401, "выдержка обязана влезать в колонку")
	require.True(t, strings.HasSuffix(long, "…"), "обрезанная выдержка обязана быть видно обрезанной")
}
