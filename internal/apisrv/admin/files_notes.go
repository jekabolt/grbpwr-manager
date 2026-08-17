package admin

import (
	"context"
	"database/sql"
	"errors"
	"log/slog"
	"strings"
	"time"

	authsrv "github.com/jekabolt/grbpwr-manager/internal/apisrv/auth"
	"github.com/jekabolt/grbpwr-manager/internal/bucket"
	"github.com/jekabolt/grbpwr-manager/internal/dto"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	pb_admin "github.com/jekabolt/grbpwr-manager/proto/gen/admin"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// MARKDOWN-ЗАМЕТКИ (Ф8).
//
// Заметка — ОБЫЧНЫЙ файл библиотеки с content_type text/markdown: темы, владельцы, доступ,
// обсуждение и задачи достаются ей теми же RPC, что и всем остальным файлам. Своего здесь ровно
// три вещи, и все три про текст: создать файл вместе с первым текстом, отдать текст и записать
// новый ПОД СРАВНЕНИЕМ ОТПЕЧАТКОВ.
//
// ПОРЯДОК ВО ВСЕХ ТРЁХ ХЕНДЛЕРАХ ОДИН И ТОТ ЖЕ: сначала байты в бакет, потом строка в базу, и
// только ПОСЛЕ успеха — уборка старого объекта. Он обратный удалению файла и выбран по той же
// причине: упавшая заливка не трогает строку вообще, а осиротевший объект стоит несравнимо дешевле
// потерянного текста.

// noteNotAFileMsg is the single answer for «этот файл не заметка». FailedPrecondition, а не
// InvalidArgument: запрос корректен, просто открывать редактором нечего.
const noteNotAFileMsg = "this file is not a markdown note"

// CreateLibraryNote creates the file and its first text in one call.
//
// Имя спрашивается сразу (заметка без названия — заметка, которую потом не найдут), а `.md`
// дописывает сервер: человек пишет текст, а не выбирает формат хранения.
func (s *Server) CreateLibraryNote(ctx context.Context, req *pb_admin.CreateLibraryNoteRequest) (*pb_admin.CreateLibraryNoteResponse, error) {
	fileName, err := dto.LibraryNoteFileName(req.GetFileName())
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "%v", err)
	}
	topicIDs, newTopics, err := dto.ConvertPbTopicSelectionToEntity(req.GetTopicIds(), req.GetNewTopics())
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "%v", err)
	}
	content := req.GetContent()
	// Потолок проверяется ДО заливки: 512 KiB, отправленные в бакет ради последующего отказа, это
	// оплаченный трафик и объект-сирота на ровном месте.
	if err := dto.ValidateLibraryNoteContent(content); err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "%v", err)
	}
	username := authsrv.GetAdminUsername(ctx)

	// ПРИВАТНОСТЬ = ОТСУТСТВИЕ public-read ACL. UploadLibraryObject не ставит его никому, и заметка
	// приватна ровно тем же способом, что и всё остальное в библиотеке.
	objectKey, sha256hex, size, err := s.bucket.UploadLibraryObject(
		ctx, strings.NewReader(content), dto.LibraryNoteContentType, dto.LibraryNoteExtension)
	if err != nil {
		slog.Default().ErrorContext(ctx, "can't store library note object",
			slog.String("username", username), slog.String("err", err.Error()))
		return nil, status.Error(codes.Internal, "could not store the note")
	}

	insert := &entity.LibraryNoteInsert{
		LibraryFileInsert: entity.LibraryFileInsert{
			ObjectKey:   objectKey,
			FileName:    fileName,
			ContentType: dto.LibraryNoteContentType,
			SizeBytes:   size,
			Sha256:      sha256hex,
			UploadedBy:  username,
		},
		ContentExcerpt: dto.LibraryNoteExcerpt(content),
	}
	// «Кто правил последним» ставится только если текст при создании БЫЛ: создать заметку с текстом
	// — это правка, а создать пустую — нет, и шапка такой заметки честно покажет «загрузил».
	if strings.TrimSpace(content) != "" {
		insert.ContentUpdatedBy = username
		// Значение времени берёт база (стор пишет NOW()); отсюда едет только факт «правка была».
		insert.ContentUpdatedAt = sql.NullTime{Time: time.Now().UTC(), Valid: true}
	}

	id, err := s.repo.Files().CreateNote(ctx, insert, topicIDs, newTopics)
	if err != nil {
		// Байты уже в бакете, а строки, которая на них указывает, нет — значит, это мусор.
		cleanupObjects(ctx, s.bucket, objectKey)
		if s.repo.IsErrForeignKeyViolation(err) {
			return nil, status.Error(codes.InvalidArgument, "topic_id does not reference an existing topic")
		}
		slog.Default().ErrorContext(ctx, "can't create library note",
			slog.String("username", username), slog.String("err", err.Error()))
		return nil, status.Error(codes.Internal, "could not create the note")
	}

	stored, err := s.repo.Files().GetFileById(ctx, id)
	if err != nil {
		slog.Default().ErrorContext(ctx, "can't read back library note", slog.String("err", err.Error()))
		return nil, status.Error(codes.Internal, "the note was created but could not be read back")
	}
	slog.Default().InfoContext(ctx, "library note created",
		slog.String("username", username), slog.Int("id", id),
		slog.String("file_name", fileName), slog.Int64("size_bytes", size))

	return &pb_admin.CreateLibraryNoteResponse{
		File: s.withLibraryURLs(ctx, stored, dto.ConvertEntityLibraryFileToPb(stored)),
	}, nil
}

// GetLibraryNoteContent returns the TEXT over the RPC rather than a presigned url: text/markdown is
// not inline-safe (so it would only ever get an attachment link), and fetching a presigned url from
// JS runs into the bucket's CORS — грабли, за которые фича выкроек уже заплатила.
func (s *Server) GetLibraryNoteContent(ctx context.Context, req *pb_admin.GetLibraryNoteContentRequest) (*pb_admin.GetLibraryNoteContentResponse, error) {
	if req.GetId() <= 0 {
		return nil, status.Error(codes.InvalidArgument, "file id is required")
	}
	note, err := s.noteRow(ctx, int(req.GetId()))
	if err != nil {
		return nil, err
	}
	content, err := s.noteContent(ctx, note.ObjectKey)
	if err != nil {
		return nil, err
	}
	return dto.ConvertEntityLibraryNoteToPb(note, content), nil
}

// SaveLibraryNoteContent writes a new version under a compare-and-set on sha256.
//
// ЧУЖУЮ ПРАВКУ НЕЛЬЗЯ ЗАТЕРЕТЬ МОЛЧА, и это главное свойство всего экрана: строка перечитывается
// внутри транзакции, и без совпавшего отпечатка (или явного force) ни один UPDATE не выходит.
// Разошедшийся отпечаток приезжает ДАННЫМИ ответа вместе с чужим текстом — клиент рисует баннер и
// три исхода, не потеряв ни своего буфера, ни второго запроса.
func (s *Server) SaveLibraryNoteContent(ctx context.Context, req *pb_admin.SaveLibraryNoteContentRequest) (*pb_admin.SaveLibraryNoteContentResponse, error) {
	if req.GetFileId() <= 0 {
		return nil, status.Error(codes.InvalidArgument, "file id is required")
	}
	content := req.GetContent()
	if err := dto.ValidateLibraryNoteContent(content); err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "%v", err)
	}
	username := authsrv.GetAdminUsername(ctx)

	// Строка читается ДО заливки: файл, которого нет (или который зовущему не виден), не должен
	// стоить бакету объекта, а сравнение отпечатков всё равно произойдёт заново внутри транзакции —
	// это чтение ничего не решает и решать не может.
	note, err := s.noteRow(ctx, int(req.GetFileId()))
	if err != nil {
		return nil, err
	}

	objectKey, sha256hex, size, err := s.bucket.UploadLibraryObject(
		ctx, strings.NewReader(content), dto.LibraryNoteContentType, dto.LibraryNoteExtension)
	if err != nil {
		slog.Default().ErrorContext(ctx, "can't store library note object",
			slog.String("username", username), slog.Int("id", note.FileId), slog.String("err", err.Error()))
		return nil, status.Error(codes.Internal, "could not store the note")
	}

	res, err := s.repo.Files().SaveNoteContent(ctx, entity.LibraryNoteSave{
		FileId:         note.FileId,
		BaseSha256:     req.GetBaseSha256(),
		Force:          req.GetForce(),
		ObjectKey:      objectKey,
		Sha256:         sha256hex,
		SizeBytes:      size,
		ContentExcerpt: dto.LibraryNoteExcerpt(content),
		EditedBy:       username,
	})
	if err != nil {
		cleanupObjects(ctx, s.bucket, objectKey)
		if errors.Is(err, sql.ErrNoRows) {
			return nil, status.Error(codes.NotFound, "file not found")
		}
		slog.Default().ErrorContext(ctx, "can't save library note content",
			slog.String("username", username), slog.Int("id", note.FileId), slog.String("err", err.Error()))
		return nil, status.Error(codes.Internal, "could not save the note")
	}

	if res.Conflict {
		// Записано НИЧЕГО — значит, только что залитый объект не принадлежит никому.
		cleanupObjects(ctx, s.bucket, objectKey)
		// Чужой текст обязан приехать целиком. Пустой current_content на конфликте был бы ложью
		// худшего сорта: клиент показал бы различия так, будто коллега стёр заметку, и «записать
		// поверх» выглядело бы безобидным.
		current, err := s.noteContent(ctx, res.CurrentObjectKey)
		if err != nil {
			slog.Default().ErrorContext(ctx, "can't read the conflicting library note version",
				slog.String("username", username), slog.Int("id", note.FileId))
			return nil, status.Error(codes.Internal,
				"somebody else saved this note in the meantime and their version could not be read — nothing was overwritten, try saving again")
		}
		slog.Default().InfoContext(ctx, "library note save conflicted",
			slog.String("username", username), slog.Int("id", note.FileId),
			slog.String("last_edited_by", res.LastEditedBy))
		return dto.ConvertEntityLibraryNoteSaveResultToPb(res, current), nil
	}

	// Порядок тот же, что у DeleteLibraryFile: строка раньше байтов. Снеси старый объект первым — и
	// падение записи оставило бы заметку, указывающую на исчезнувший текст.
	if res.PreviousObjectKey != "" {
		cleanupObjects(ctx, s.bucket, res.PreviousObjectKey)
	}
	slog.Default().InfoContext(ctx, "library note saved",
		slog.String("username", username), slog.Int("id", note.FileId),
		slog.Int64("size_bytes", size), slog.Bool("forced", req.GetForce()))

	return dto.ConvertEntityLibraryNoteSaveResultToPb(res, ""), nil
}

// noteRow reads the note's row and refuses anything that is not a note. Общий для чтения и записи,
// потому что «файла нет / файл не виден / файл не заметка» обязаны отвечать одинаково с обеих
// сторон: разные ответы на одном и том же файле — это и есть способ выяснить, что он существует.
func (s *Server) noteRow(ctx context.Context, id int) (*entity.LibraryNote, error) {
	note, err := s.repo.Files().GetNote(ctx, id)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, status.Error(codes.NotFound, "file not found")
		}
		slog.Default().ErrorContext(ctx, "can't get library note",
			slog.Int("id", id), slog.String("err", err.Error()))
		return nil, status.Error(codes.Internal, "can't get the note")
	}
	if !dto.IsLibraryNoteFile(note.ContentType, note.FileName) {
		return nil, status.Error(codes.FailedPrecondition, noteNotAFileMsg)
	}
	return note, nil
}

// noteContent reads the text of one stored object.
func (s *Server) noteContent(ctx context.Context, objectKey string) (string, error) {
	raw, err := s.bucket.GetLibraryObject(ctx, objectKey)
	if err != nil {
		if errors.Is(err, bucket.ErrLibraryObjectTooLarge) {
			// Не Internal: файл цел, он просто больше, чем редактор берётся открыть. Так отвечает
			// .md, залитый файлом и переросший потолок заметки.
			return "", status.Error(codes.FailedPrecondition, "this file is too large to open as a note")
		}
		slog.Default().ErrorContext(ctx, "can't read library note object", slog.String("err", err.Error()))
		return "", status.Error(codes.Internal, "could not read the note")
	}
	content := string(raw)
	// Валидность UTF-8 проверяется здесь, а не на шлюзе: строка протокола обязана быть валидным
	// UTF-8, и невалидная уронила бы маршалинг ответа сообщением, по которому нельзя понять вообще
	// ничего. Это ровно тот случай, когда файл с расширением .md на деле не текст.
	if err := dto.ValidateLibraryNoteContent(content); err != nil {
		return "", status.Errorf(codes.FailedPrecondition, "%v", err)
	}
	return content, nil
}
