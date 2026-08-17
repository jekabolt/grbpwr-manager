package admin

import (
	"context"
	"database/sql"
	"errors"
	"log/slog"

	authsrv "github.com/jekabolt/grbpwr-manager/internal/apisrv/auth"
	"github.com/jekabolt/grbpwr-manager/internal/dto"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	pb_admin "github.com/jekabolt/grbpwr-manager/proto/gen/admin"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// БЛОК ДОСТУПА К ФАЙЛУ (Ф7): уровень, люди, публичная ссылка, журнал и витрина открытого.
//
// ФАЙЛ, КОТОРОГО НЕ ВИДНО, ОТВЕЧАЕТ NotFound — и на чтение, и на запись. PermissionDenied
// подтвердил бы, что файл существует, а в этой библиотеке секрет — именно ИМЕНА. Механика
// проста: невидимый файл не возвращается стором вовсе (предикат видимости T-7.3), а хендлер
// переводит sql.ErrNoRows в NotFound. Отдельной проверки видимости здесь нет и быть не должно —
// это был бы второй способ описать видимость.
//
// КРУГ ПРАВКИ — ЗАГРУЗИВШИЙ | ВЛАДЕЛЕЦ | СУПЕР, тот же самый, что у смены владельцев, и
// проверяется он ТОЙ ЖЕ функцией (mayEditLibraryFileOwners): расширить себе доступ к чужому
// файлу и назначить себя его владельцем — одно и то же действие с разных сторон, и разъехавшиеся
// проверки означали бы, что одна из дверей осталась открытой. files:write здесь необходим, но
// НЕ достаточен: он есть у всех, кто вообще работает с библиотекой, а публикация чужого файла в
// интернет — не то, что должен мочь каждый из них.

const (
	// maxLibraryFileAccessPeople bounds the named list. Список отвечает на вопрос «кому именно
	// нужен этот файл»; ответ на полсотни человек — это уже `team`, сказанный длинно. Потолок
	// ловит цикл, а не способ работы.
	maxLibraryFileAccessPeople = 50

	// libraryFileAccessMsg НАЗЫВАЕТ круг: «нет прав» без указания, чьё это право, отправляет
	// человека в телеграм — ровно тот способ работы, который раздел заменяет.
	libraryFileAccessMsg = "доступ к файлу меняет загрузивший, действующий владелец или супер-админ"
)

// linkURL mints the public url for a file — and ONLY when the file is at level `link` right now.
//
// Строка публичной ссылки переживает уровень (возврат в `team` её не удаляет и epoch не двигает),
// поэтому url, собранный по одному лишь наличию строки, был бы копируемой ссылкой, которая
// гарантированно отвечает 404. Пустой url — это и есть «ссылки сейчас нет».
func (s *Server) linkURL(level entity.LibraryFileAccessLevel, link *entity.LibraryFilePublicAccess) string {
	if level != entity.LibraryFileAccessLink || link == nil {
		return ""
	}
	// LinkURL безопасен на nil-сервисе: сборка без сервиса отдаёт блок доступа без url.
	return s.fileLinks.LinkURL(link.FileId, link.Epoch)
}

// GetLibraryFileAccess returns the access block of one file (точка 12 предиката).
func (s *Server) GetLibraryFileAccess(ctx context.Context, req *pb_admin.GetLibraryFileAccessRequest) (*pb_admin.GetLibraryFileAccessResponse, error) {
	if req.GetId() <= 0 {
		return nil, status.Error(codes.InvalidArgument, "file id is required")
	}
	fileID := int(req.GetId())
	access, err := s.repo.Files().GetFileAccess(ctx, fileID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, status.Error(codes.NotFound, "file not found")
		}
		slog.Default().ErrorContext(ctx, "can't get library file access", slog.String("err", err.Error()))
		return nil, status.Error(codes.Internal, "can't get file access")
	}
	events, err := s.repo.Files().ListFileAccessEvents(ctx, fileID, 0)
	if err != nil {
		// Журнал — часть ответа, а не украшение: «кто открыл» читают именно из него, и тихо
		// пустой журнал выглядел бы как «никто ничего не менял».
		slog.Default().ErrorContext(ctx, "can't list library file access events", slog.String("err", err.Error()))
		return nil, status.Error(codes.Internal, "can't get file access")
	}
	return &pb_admin.GetLibraryFileAccessResponse{
		Access: dto.ConvertEntityLibraryFileAccessToPb(access, s.linkURL(access.Level, access.Link)),
		Events: dto.ConvertEntityLibraryFileAccessEventsToPb(events),
	}, nil
}

// SetLibraryFileAccess sets the level and the list that goes with it atomically.
func (s *Server) SetLibraryFileAccess(ctx context.Context, req *pb_admin.SetLibraryFileAccessRequest) (*pb_admin.SetLibraryFileAccessResponse, error) {
	if req.GetFileId() <= 0 {
		return nil, status.Error(codes.InvalidArgument, "file id is required")
	}
	level, err := dto.ParseLibraryFileAccessLevel(req.GetLevel())
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, err.Error())
	}
	ttl, err := dto.ValidateLibraryFileLinkTTL(req.GetLinkTtl())
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, err.Error())
	}
	adminIDs := make([]int, 0, len(req.GetAdminIds()))
	seen := make(map[int]bool, len(req.GetAdminIds()))
	for _, id := range req.GetAdminIds() {
		if id <= 0 {
			return nil, status.Error(codes.InvalidArgument, "admin id must be positive")
		}
		if seen[int(id)] {
			// Повтор схлопывается, а не доезжает до уникального ключа сырым 1062.
			continue
		}
		seen[int(id)] = true
		adminIDs = append(adminIDs, int(id))
	}
	fileID := int(req.GetFileId())

	current, err := s.repo.Files().GetFileById(ctx, fileID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, status.Error(codes.NotFound, "file not found")
		}
		slog.Default().ErrorContext(ctx, "can't get library file for access change", slog.String("err", err.Error()))
		return nil, status.Error(codes.Internal, "can't set file access")
	}
	if !mayEditLibraryFileOwners(ctx, current) {
		return nil, status.Error(codes.PermissionDenied, libraryFileAccessMsg)
	}
	if level == entity.LibraryFileAccessPeople {
		// ЗАГРУЗИВШИЙ ДОБАВЛЯЕТСЯ НЕЯВНО. Человек, выкинувший себя из списка на собственном
		// файле, потерял бы и файл, и возможность это починить: список правит тот, кто файл
		// видит. Клиенту это не поручено — он показывает загрузившего неудаляемым, но
		// правило обязано держаться сервером, а не разметкой формы.
		if current.UploadedById.Valid && !seen[int(current.UploadedById.Int64)] {
			adminIDs = append(adminIDs, int(current.UploadedById.Int64))
		}
	}
	if len(adminIDs) > maxLibraryFileAccessPeople {
		return nil, status.Errorf(codes.InvalidArgument, "at most %d people per file", maxLibraryFileAccessPeople)
	}

	access, err := s.repo.Files().SetFileAccess(ctx, fileID, entity.LibraryFileAccessUpdate{
		Level:        level,
		AdminIDs:     adminIDs,
		LinkTTLHours: ttl,
		Actor:        authsrv.GetAdminUsername(ctx),
	})
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, status.Error(codes.NotFound, "file not found")
		}
		if s.repo.IsErrForeignKeyViolation(err) {
			// Несуществующий аккаунт — не 500: запрос понятен, а мир под ним изменился.
			return nil, status.Error(codes.InvalidArgument, "admin_id does not reference an existing account")
		}
		slog.Default().ErrorContext(ctx, "can't set library file access", slog.String("err", err.Error()))
		return nil, status.Error(codes.Internal, "can't set file access")
	}
	return &pb_admin.SetLibraryFileAccessResponse{
		Access: dto.ConvertEntityLibraryFileAccessToPb(access, s.linkURL(access.Level, access.Link)),
	}, nil
}

// RotateLibraryFileLink mints a new link and kills the old one instantly.
func (s *Server) RotateLibraryFileLink(ctx context.Context, req *pb_admin.RotateLibraryFileLinkRequest) (*pb_admin.RotateLibraryFileLinkResponse, error) {
	if req.GetFileId() <= 0 {
		return nil, status.Error(codes.InvalidArgument, "file id is required")
	}
	fileID := int(req.GetFileId())
	current, err := s.repo.Files().GetFileById(ctx, fileID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, status.Error(codes.NotFound, "file not found")
		}
		slog.Default().ErrorContext(ctx, "can't get library file for link rotation", slog.String("err", err.Error()))
		return nil, status.Error(codes.Internal, "can't rotate link")
	}
	if !mayEditLibraryFileOwners(ctx, current) {
		return nil, status.Error(codes.PermissionDenied, libraryFileAccessMsg)
	}
	row, err := s.repo.Files().RotateFileLink(ctx, fileID, authsrv.GetAdminUsername(ctx))
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, status.Error(codes.NotFound, "file not found")
		}
		slog.Default().ErrorContext(ctx, "can't rotate library file link", slog.String("err", err.Error()))
		return nil, status.Error(codes.Internal, "can't rotate link")
	}
	// Уровень берётся с ФАЙЛА: пересоздать ссылку на файле, который сейчас не по ссылке,
	// законно (поколение сдвинулось, старые токены мертвы), но url в ответе тогда пуст —
	// показывать копируемую строку, которая отвечает 404, нельзя.
	return &pb_admin.RotateLibraryFileLinkResponse{
		Link: dto.ConvertEntityLibraryFilePublicLinkToPb(row, s.linkURL(current.AccessLevel, row)),
	}, nil
}

// ListSharedLibraryFiles is the витрина: everything that is `people` or `link` right now.
func (s *Server) ListSharedLibraryFiles(ctx context.Context, req *pb_admin.ListSharedLibraryFilesRequest) (*pb_admin.ListSharedLibraryFilesResponse, error) {
	filter := entity.SharedLibraryFileFilter{
		Limit:  int(req.GetLimit()),
		Offset: int(req.GetOffset()),
	}
	if lvl := req.GetLevel(); lvl != "" {
		parsed, err := dto.ParseLibraryFileAccessLevel(lvl)
		if err != nil {
			return nil, status.Error(codes.InvalidArgument, err.Error())
		}
		if parsed == entity.LibraryFileAccessTeam {
			// `team` — не фильтр витрины, а её отрицание: витрина показывает то, что НЕ по
			// умолчанию, и пустой ответ на такой запрос читался бы как «ничего не открыто».
			return nil, status.Error(codes.InvalidArgument, "level must be people or link (team is not shared)")
		}
		filter.Level = parsed
	}
	rows, total, err := s.repo.Files().ListSharedFiles(ctx, filter)
	if err != nil {
		slog.Default().ErrorContext(ctx, "can't list shared library files", slog.String("err", err.Error()))
		return nil, status.Error(codes.Internal, "can't list shared files")
	}
	out := make([]*pb_admin.SharedLibraryFile, 0, len(rows))
	for i := range rows {
		// Та же плитка, что в сетке, — вместе с политикой inline-безопасности внутри
		// withLibraryURLs. Второй способ собрать превью здесь означал бы второй набор правил
		// о том, какому типу можно отдавать view_url.
		pbFile := s.withLibraryURLs(ctx, &rows[i].File, dto.ConvertEntityLibraryFileToPb(&rows[i].File))
		out = append(out, dto.ConvertEntitySharedLibraryFileToPb(rows[i], pbFile,
			s.linkURL(rows[i].File.AccessLevel, rows[i].Link)))
	}
	return &pb_admin.ListSharedLibraryFilesResponse{Files: out, Total: int32(total)}, nil
}
