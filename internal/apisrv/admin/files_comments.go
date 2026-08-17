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

// libraryCommentAuthorMsg — то, что слышит человек, дотянувшийся до чужой реплики. Оно НАЗЫВАЕТ
// правило целиком, включая исключение: «нет прав» без объяснения отправляет спрашивать в телеграм,
// то есть ровно туда, откуда этот раздел уводит.
const libraryCommentAuthorMsg = "править и удалять можно только свою реплику (супер-админ — любую)"

// mayEditLibraryFileComment — ВТОРОЙ ГЕЙТ ленты, и весь смысл этого файла в нём.
//
// ПОЧЕМУ files:write НЕДОСТАТОЧНО. Право wr(files) есть у всех, кто вообще работает с библиотекой.
// Карта прав знает СЕКЦИЮ, но не знает, чья это реплика, — а «правь что угодно в обсуждении» это
// не право на раздел, это возможность переписать чужие слова задним числом. Та же форма проверки,
// что у SetLibraryFileOwners: карта держит секцию, автора проверяет код.
//
// СРАВНЕНИЕ ИДЁТ ПО СТРОКЕ author, А НЕ ПО ССЫЛКЕ author_id. У реплики уволенного author_id уже
// NULL (SET NULL), и сравнение по ссылке на таких строках не срабатывало бы вовсе — правило
// молча поменяло бы смысл там, где его никто не проверяет.
//
// НО ОДНОГО СОВПАДЕНИЯ ИМЕНИ МАЛО, И ЭТО НЕ ПЕРЕСТРАХОВКА. UNIQUE на admins.username освобождает
// имя при удалении аккаунта: удалили pasha → author_id его реплик занулился, строка «pasha»
// осталась → завели НОВОГО pasha → он совпал бы по имени со ВСЕЙ перепиской прежнего и мог бы её
// править и удалять. Требование живой ссылки закрывает ровно это: author_id есть только у реплики,
// чей автор ВСЁ ЕЩЁ существует, а имя уникально, значит совпадение имени при живой ссылке — тот же
// самый человек. Цена: реплику уволенного не правит никто, кроме супера, — и это правильный ответ,
// потому что автора, который мог бы согласиться на правку, больше нет. Ровно тот же довод и та же
// цена, что у mayEditLibraryFileOwners (files_people.go), где это уже закреплено тестом.
//
// Fails closed на контексте без авторизации: без клеймов человек не супер, а пустое имя не
// совпадает ни с одним автором.
func mayEditLibraryFileComment(ctx context.Context, c *entity.LibraryFileComment) bool {
	if c == nil {
		return false
	}
	if az, ok := authsrv.GetAdminAuthz(ctx); ok && az.FullAccess() {
		return true
	}
	caller := authsrv.GetAdminUsername(ctx)
	if caller == "" {
		return false
	}
	return c.AuthorId.Valid && c.Author == caller
}

// ListLibraryFileComments returns the whole feed of one file, oldest first.
func (s *Server) ListLibraryFileComments(
	ctx context.Context,
	req *pb_admin.ListLibraryFileCommentsRequest,
) (*pb_admin.ListLibraryFileCommentsResponse, error) {
	if req.GetId() <= 0 {
		return nil, status.Error(codes.InvalidArgument, "file id is required")
	}
	comments, err := s.repo.Files().ListComments(ctx, int(req.GetId()))
	if err != nil {
		slog.Default().ErrorContext(ctx, "can't list library file comments", slog.String("err", err.Error()))
		return nil, status.Error(codes.Internal, "can't list comments")
	}
	return &pb_admin.ListLibraryFileCommentsResponse{
		Comments: dto.ConvertEntityLibraryFileCommentsToPb(comments),
	}, nil
}

// AddLibraryFileComment appends one remark to a file's discussion.
//
// Автор НЕ ПРИЕЗЖАЕТ В ЗАПРОСЕ ни в каком виде — его нет в контракте: имя берётся из JWT, живую
// ссылку выводит стор из этого же имени. Пришли автор с клиента — и подпись под чужими словами
// стала бы полем формы.
func (s *Server) AddLibraryFileComment(
	ctx context.Context,
	req *pb_admin.AddLibraryFileCommentRequest,
) (*pb_admin.AddLibraryFileCommentResponse, error) {
	if req.GetFileId() <= 0 {
		return nil, status.Error(codes.InvalidArgument, "file id is required")
	}
	body, err := dto.ValidateLibraryCommentBody(req.GetBody())
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "%v", err)
	}
	author := authsrv.GetAdminUsername(ctx)
	if author == "" {
		// Реплика без автора не принадлежит никому: её не смог бы поправить или удалить даже тот,
		// кто её написал, потому что «только свою» ей не с чем сопоставить. Лучше отказать сразу,
		// чем положить в ленту неудаляемую строку.
		return nil, status.Error(codes.PermissionDenied, "comment author is unknown")
	}
	stored, err := s.repo.Files().AddComment(ctx, int(req.GetFileId()), author, body)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, status.Error(codes.NotFound, "file not found")
		}
		if s.repo.IsErrForeignKeyViolation(err) {
			// Файл исчез между проверкой и вставкой — запрос понятен, а мир под ним изменился.
			return nil, status.Error(codes.NotFound, "file not found")
		}
		slog.Default().ErrorContext(ctx, "can't add library file comment", slog.String("err", err.Error()))
		return nil, status.Error(codes.Internal, "can't add comment")
	}
	return &pb_admin.AddLibraryFileCommentResponse{
		Comment: dto.ConvertEntityLibraryFileCommentToPb(stored),
	}, nil
}

// UpdateLibraryFileComment rewrites one's own remark and stamps edited_at.
func (s *Server) UpdateLibraryFileComment(
	ctx context.Context,
	req *pb_admin.UpdateLibraryFileCommentRequest,
) (*pb_admin.UpdateLibraryFileCommentResponse, error) {
	if req.GetId() <= 0 {
		return nil, status.Error(codes.InvalidArgument, "comment id is required")
	}
	body, err := dto.ValidateLibraryCommentBody(req.GetBody())
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "%v", err)
	}
	// Автор ЧИТАЕТСЯ, а не принимается на слово: в запросе его нет, и это единственный способ
	// сопоставить зовущего с тем, кто писал.
	current, err := s.repo.Files().GetCommentById(ctx, int(req.GetId()))
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, status.Error(codes.NotFound, "comment not found")
		}
		slog.Default().ErrorContext(ctx, "can't get library file comment for edit", slog.String("err", err.Error()))
		return nil, status.Error(codes.Internal, "can't update comment")
	}
	if !mayEditLibraryFileComment(ctx, current) {
		return nil, status.Error(codes.PermissionDenied, libraryCommentAuthorMsg)
	}
	stored, err := s.repo.Files().UpdateComment(ctx, int(req.GetId()), body)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			// Гонка «прочитал → сравнил → изменил» стоит ровно этого: свою же реплику удалили
			// между чтением и записью, и ответ NotFound — правда.
			return nil, status.Error(codes.NotFound, "comment not found")
		}
		slog.Default().ErrorContext(ctx, "can't update library file comment", slog.String("err", err.Error()))
		return nil, status.Error(codes.Internal, "can't update comment")
	}
	return &pb_admin.UpdateLibraryFileCommentResponse{
		Comment: dto.ConvertEntityLibraryFileCommentToPb(stored),
	}, nil
}

// DeleteLibraryFileComment removes one's own remark, за тем же вторым гейтом.
func (s *Server) DeleteLibraryFileComment(
	ctx context.Context,
	req *pb_admin.DeleteLibraryFileCommentRequest,
) (*pb_admin.DeleteLibraryFileCommentResponse, error) {
	if req.GetId() <= 0 {
		return nil, status.Error(codes.InvalidArgument, "comment id is required")
	}
	current, err := s.repo.Files().GetCommentById(ctx, int(req.GetId()))
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, status.Error(codes.NotFound, "comment not found")
		}
		slog.Default().ErrorContext(ctx, "can't get library file comment for delete", slog.String("err", err.Error()))
		return nil, status.Error(codes.Internal, "can't delete comment")
	}
	if !mayEditLibraryFileComment(ctx, current) {
		return nil, status.Error(codes.PermissionDenied, libraryCommentAuthorMsg)
	}
	if err := s.repo.Files().DeleteComment(ctx, int(req.GetId())); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, status.Error(codes.NotFound, "comment not found")
		}
		slog.Default().ErrorContext(ctx, "can't delete library file comment", slog.String("err", err.Error()))
		return nil, status.Error(codes.Internal, "can't delete comment")
	}
	return &pb_admin.DeleteLibraryFileCommentResponse{}, nil
}
