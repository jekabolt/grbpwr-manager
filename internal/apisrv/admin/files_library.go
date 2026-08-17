package admin

import (
	"context"
	"database/sql"
	"errors"
	"log/slog"

	"github.com/jekabolt/grbpwr-manager/internal/dto"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	pb_admin "github.com/jekabolt/grbpwr-manager/proto/gen/admin"
	pb_common "github.com/jekabolt/grbpwr-manager/proto/gen/common"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// withLibraryURLs mints the short-lived presigned urls for one file. It is the
// single place where the inline-safety policy is applied: a type that is not
// inline-safe gets NO view url at all, only a download one, so that svg and html
// can never be rendered from the bucket origin.
//
// Failures to sign are logged and swallowed: a file whose url could not be minted
// still lists correctly (with an inert tile) instead of failing the whole page.
func (s *Server) withLibraryURLs(ctx context.Context, f *entity.LibraryFile, pb *pb_admin.LibraryFile) *pb_admin.LibraryFile {
	if pb == nil || s.bucket == nil {
		return pb
	}
	if dto.IsInlineSafeContentType(f.ContentType) {
		url, expiresAt, err := s.bucket.PresignLibraryObject(ctx, f.ObjectKey, false, f.FileName)
		if err != nil {
			slog.Default().ErrorContext(ctx, "can't presign library object",
				slog.Int("id", f.Id), slog.String("err", err.Error()))
		} else {
			pb.Url = url
			pb.UrlsExpireAt = timestamppb.New(expiresAt)
		}
	}
	dl, expiresAt, err := s.bucket.PresignLibraryObject(ctx, f.ObjectKey, true, f.FileName)
	if err != nil {
		slog.Default().ErrorContext(ctx, "can't presign library object for download",
			slog.Int("id", f.Id), slog.String("err", err.Error()))
	} else {
		pb.DownloadUrl = dl
		if pb.UrlsExpireAt == nil {
			pb.UrlsExpireAt = timestamppb.New(expiresAt)
		}
	}
	if f.PreviewObjectKey.Valid && f.PreviewObjectKey.String != "" {
		prev, _, err := s.bucket.PresignLibraryObject(ctx, f.PreviewObjectKey.String, false, f.FileName)
		if err != nil {
			slog.Default().ErrorContext(ctx, "can't presign library preview",
				slog.Int("id", f.Id), slog.String("err", err.Error()))
		} else {
			pb.PreviewUrl = prev
		}
	}
	return pb
}

// libraryFilesToPb converts and signs a page of files.
func (s *Server) libraryFilesToPb(ctx context.Context, files []entity.LibraryFile) []*pb_admin.LibraryFile {
	out := make([]*pb_admin.LibraryFile, 0, len(files))
	for i := range files {
		out = append(out, s.withLibraryURLs(ctx, &files[i], dto.ConvertEntityLibraryFileToPb(&files[i])))
	}
	return out
}

// GetLibraryFile returns one file with its topics and freshly minted urls.
func (s *Server) GetLibraryFile(ctx context.Context, req *pb_admin.GetLibraryFileRequest) (*pb_admin.GetLibraryFileResponse, error) {
	if req.Id <= 0 {
		return nil, status.Error(codes.InvalidArgument, "file id is required")
	}
	f, err := s.repo.Files().GetFileById(ctx, int(req.Id))
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, status.Error(codes.NotFound, "file not found")
		}
		slog.Default().ErrorContext(ctx, "can't get library file", slog.String("err", err.Error()))
		return nil, status.Error(codes.Internal, "can't get file")
	}
	return &pb_admin.GetLibraryFileResponse{
		File: s.withLibraryURLs(ctx, f, dto.ConvertEntityLibraryFileToPb(f)),
	}, nil
}

// ListLibraryFiles is the grid.
func (s *Server) ListLibraryFiles(ctx context.Context, req *pb_admin.ListLibraryFilesRequest) (*pb_admin.ListLibraryFilesResponse, error) {
	topicIDs, err := dto.ConvertPbTopicFilterToEntity(req.TopicIds)
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "%v", err)
	}
	// Фильтр по человеку разбирается ОДНОЙ функцией вместе с ролью: id и роль — половины
	// одного контрола, и разбери их порознь, «роль без человека» и «человек без роли»
	// разъехались бы между вызывающими. Несуществующий id — пустая выдача, а не отказ (довод
	// про оракул существования аккаунтов — в самом конвертере).
	personID, personRole := dto.ConvertPbLibraryPersonFilterToEntity(req.PersonId, req.PersonRole)
	files, total, err := s.repo.Files().ListFiles(ctx, entity.LibraryFileListFilter{
		TopicId: int(req.TopicId),
		// Пересечение: файл обязан нести ВСЕ выбранные темы. Приоритет фильтров
		// (untopiced > topic_ids > topic_id) разрешается в сторе, чтобы правило
		// жило в одном месте и не разъехалось между вызывающими.
		TopicIds:   topicIDs,
		Untopiced:  req.Untopiced,
		Search:     req.Search,
		PersonId:   personID,
		PersonRole: personRole,
		Limit:      int(req.Limit),
		Offset:     int(req.Offset),
		SortBy:     dto.ConvertPbLibraryFileSortToEntity(req.SortBy),
		// A library is read newest-first, so an unspecified order must mean
		// descending. The shared converter maps UNKNOWN to ascending, which is right
		// for the lists it was written for and wrong here — so the default is chosen
		// at this call site rather than by changing what UNKNOWN means everywhere.
		OrderFactor: listOrderOrNewestFirst(req.OrderFactor),
	})
	if err != nil {
		// Предел — довод вызывающего, а не сбой сервера: под Internal он доезжал бы
		// как «не удалось», и упёршийся в него человек не узнал бы ни причины, ни
		// того, что делать дальше.
		if errors.Is(err, entity.ErrLibraryBatchTooLarge) {
			return nil, status.Error(codes.InvalidArgument, err.Error())
		}
		slog.Default().ErrorContext(ctx, "can't list library files", slog.String("err", err.Error()))
		return nil, status.Error(codes.Internal, "can't list files")
	}
	return &pb_admin.ListLibraryFilesResponse{
		Files: s.libraryFilesToPb(ctx, files),
		Total: int32(total),
	}, nil
}

// UpdateLibraryFile renames a file and replaces its topic set.
func (s *Server) UpdateLibraryFile(ctx context.Context, req *pb_admin.UpdateLibraryFileRequest) (*pb_admin.UpdateLibraryFileResponse, error) {
	if req.Id <= 0 {
		return nil, status.Error(codes.InvalidArgument, "file id is required")
	}
	name, err := dto.ValidateLibraryFileName(req.FileName)
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "%v", err)
	}
	topicIDs, newTopics, err := dto.ConvertPbTopicSelectionToEntity(req.TopicIds, req.NewTopics)
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "%v", err)
	}
	if err := s.repo.Files().UpdateFile(ctx, int(req.Id), name, topicIDs, newTopics); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, status.Error(codes.NotFound, "file not found")
		}
		if s.repo.IsErrForeignKeyViolation(err) {
			return nil, status.Error(codes.InvalidArgument, "topic_id does not reference an existing topic")
		}
		slog.Default().ErrorContext(ctx, "can't update library file", slog.String("err", err.Error()))
		return nil, status.Error(codes.Internal, "can't update file")
	}
	return &pb_admin.UpdateLibraryFileResponse{}, nil
}

// DeleteLibraryFile removes the row and then, best-effort, the bytes.
//
// The order matters: the row goes first inside a transaction that also refuses
// while a task holds the file. Deleting the object first would risk a dangling
// row pointing at bytes that are gone — a file that lists but cannot open, which
// is worse than an orphaned object nobody sees.
func (s *Server) DeleteLibraryFile(ctx context.Context, req *pb_admin.DeleteLibraryFileRequest) (*pb_admin.DeleteLibraryFileResponse, error) {
	if req.Id <= 0 {
		return nil, status.Error(codes.InvalidArgument, "file id is required")
	}
	keys, err := s.repo.Files().DeleteFile(ctx, int(req.Id))
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, status.Error(codes.NotFound, "file not found")
		}
		if errors.Is(err, entity.ErrLibraryFileInUse) {
			// FailedPrecondition, not InvalidArgument: the request was fine, the
			// world is not ready for it. The message names the holding cards.
			return nil, status.Errorf(codes.FailedPrecondition, "%v", err)
		}
		slog.Default().ErrorContext(ctx, "can't delete library file", slog.String("err", err.Error()))
		return nil, status.Error(codes.Internal, "can't delete file")
	}
	if len(keys) > 0 {
		// The row is already gone; anything left in the bucket is now unreachable.
		// Detached context: the delete has already succeeded from the caller's point
		// of view, and cleanup must not depend on them still waiting.
		cleanupObjects(ctx, s.bucket, keys...)
	}
	return &pb_admin.DeleteLibraryFileResponse{}, nil
}

// ListFileTopics is the rail: topics by usage plus the two badges.
func (s *Server) ListFileTopics(ctx context.Context, _ *pb_admin.ListFileTopicsRequest) (*pb_admin.ListFileTopicsResponse, error) {
	topics, untopiced, total, err := s.repo.Files().ListTopics(ctx)
	if err != nil {
		slog.Default().ErrorContext(ctx, "can't list file topics", slog.String("err", err.Error()))
		return nil, status.Error(codes.Internal, "can't list topics")
	}
	return &pb_admin.ListFileTopicsResponse{
		Topics:         dto.ConvertEntityFileTopicsWithCountToPb(topics),
		UntopicedCount: int32(untopiced),
		TotalFiles:     int32(total),
	}, nil
}

// CreateFileTopic creates a topic, or returns the existing one with that name.
func (s *Server) CreateFileTopic(ctx context.Context, req *pb_admin.CreateFileTopicRequest) (*pb_admin.CreateFileTopicResponse, error) {
	name, err := dto.ValidateLibraryTopicName(req.Name)
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "%v", err)
	}
	id, err := s.repo.Files().CreateTopic(ctx, name, req.Description)
	if err != nil {
		slog.Default().ErrorContext(ctx, "can't create file topic", slog.String("err", err.Error()))
		return nil, status.Error(codes.Internal, "can't create topic")
	}
	return &pb_admin.CreateFileTopicResponse{Id: int32(id)}, nil
}

// RenameFileTopic updates a topic's name and description together.
func (s *Server) RenameFileTopic(ctx context.Context, req *pb_admin.RenameFileTopicRequest) (*pb_admin.RenameFileTopicResponse, error) {
	if req.Id <= 0 {
		return nil, status.Error(codes.InvalidArgument, "topic id is required")
	}
	name, err := dto.ValidateLibraryTopicName(req.Name)
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "%v", err)
	}
	if err := s.repo.Files().RenameTopic(ctx, int(req.Id), name, req.Description); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, status.Error(codes.NotFound, "topic not found")
		}
		if s.repo.IsErrUniqueViolation(err) {
			return nil, status.Error(codes.InvalidArgument, "a topic with this name already exists")
		}
		slog.Default().ErrorContext(ctx, "can't rename file topic", slog.String("err", err.Error()))
		return nil, status.Error(codes.Internal, "can't rename topic")
	}
	return &pb_admin.RenameFileTopicResponse{}, nil
}

// DeleteFileTopic removes a topic, refused while files still carry it.
func (s *Server) DeleteFileTopic(ctx context.Context, req *pb_admin.DeleteFileTopicRequest) (*pb_admin.DeleteFileTopicResponse, error) {
	if req.Id <= 0 {
		return nil, status.Error(codes.InvalidArgument, "topic id is required")
	}
	if err := s.repo.Files().DeleteTopic(ctx, int(req.Id)); err != nil {
		if errors.Is(err, entity.ErrFileTopicInUse) {
			return nil, status.Errorf(codes.FailedPrecondition, "%v", err)
		}
		// A file attached between the pre-check and the DELETE lands here as a raw
		// FK violation. Same situation, same answer — not a 500.
		if s.repo.IsErrForeignKeyViolation(err) {
			return nil, status.Error(codes.FailedPrecondition, "topic still has files")
		}
		slog.Default().ErrorContext(ctx, "can't delete file topic", slog.String("err", err.Error()))
		return nil, status.Error(codes.Internal, "can't delete topic")
	}
	return &pb_admin.DeleteFileTopicResponse{}, nil
}

// MergeFileTopics folds one topic into another and deletes the source.
func (s *Server) MergeFileTopics(ctx context.Context, req *pb_admin.MergeFileTopicsRequest) (*pb_admin.MergeFileTopicsResponse, error) {
	if req.SourceId <= 0 || req.TargetId <= 0 {
		return nil, status.Error(codes.InvalidArgument, "source and target topic ids are required")
	}
	if req.SourceId == req.TargetId {
		// Не no-op: слияние необратимо, и ответ «готово» на бессмысленный запрос
		// убедил бы человека, что он сделал то, чего не делал.
		return nil, status.Error(codes.InvalidArgument, "a topic cannot be merged into itself")
	}
	moved, err := s.repo.Files().MergeTopics(ctx, int(req.SourceId), int(req.TargetId))
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, status.Error(codes.NotFound, "topic not found")
		}
		slog.Default().ErrorContext(ctx, "can't merge file topics", slog.String("err", err.Error()))
		return nil, status.Error(codes.Internal, "can't merge topics")
	}
	return &pb_admin.MergeFileTopicsResponse{MovedFiles: int32(moved)}, nil
}

// AssignLibraryFileTopics ADDS topics to a set of files; it never replaces a
// file's set (see the rpc comment — a bulk write has not seen what it would erase).
func (s *Server) AssignLibraryFileTopics(ctx context.Context, req *pb_admin.AssignLibraryFileTopicsRequest) (*pb_admin.AssignLibraryFileTopicsResponse, error) {
	if len(req.FileIds) == 0 {
		return nil, status.Error(codes.InvalidArgument, "at least one file id is required")
	}
	fileIDs := make([]int, 0, len(req.FileIds))
	seen := make(map[int]bool, len(req.FileIds))
	for _, id := range req.FileIds {
		if id <= 0 {
			return nil, status.Error(codes.InvalidArgument, "file id must be positive")
		}
		if seen[int(id)] {
			continue
		}
		seen[int(id)] = true
		fileIDs = append(fileIDs, int(id))
	}
	topicIDs, newTopics, err := dto.ConvertPbTopicSelectionToEntity(req.TopicIds, req.NewTopics)
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "%v", err)
	}
	if len(topicIDs) == 0 && len(newTopics) == 0 {
		return nil, status.Error(codes.InvalidArgument, "at least one topic is required")
	}
	assigned, err := s.repo.Files().AssignTopics(ctx, fileIDs, topicIDs, newTopics)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, status.Error(codes.NotFound, "topic not found")
		}
		// Предел проверяется ПЕРЕД вопросом к драйверу: это наша собственная граница,
		// а не нарушение внешнего ключа, и спрашивать про неё MySQL бессмысленно.
		if errors.Is(err, entity.ErrLibraryBatchTooLarge) {
			return nil, status.Error(codes.InvalidArgument, err.Error())
		}
		if s.repo.IsErrForeignKeyViolation(err) {
			return nil, status.Error(codes.InvalidArgument, "topic_id does not reference an existing topic")
		}
		slog.Default().ErrorContext(ctx, "can't assign library file topics", slog.String("err", err.Error()))
		return nil, status.Error(codes.Internal, "can't assign topics")
	}
	return &pb_admin.AssignLibraryFileTopicsResponse{Assigned: int32(assigned)}, nil
}

// listOrderOrNewestFirst resolves the list order, defaulting to newest-first.
func listOrderOrNewestFirst(f pb_common.OrderFactor) entity.OrderFactor {
	if f == pb_common.OrderFactor_ORDER_FACTOR_UNKNOWN {
		return entity.Descending
	}
	return dto.ConvertPBCommonOrderFactorToEntity(f)
}
