package admin

import (
	"context"
	"database/sql"
	"errors"
	"log/slog"

	"github.com/jekabolt/grbpwr-manager/internal/dto"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	pb_admin "github.com/jekabolt/grbpwr-manager/proto/gen/admin"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// ГРУППИРОВКА: ТИП ТЕМЫ И СЛОВАРЬ РОЛЕЙ.
//
// Роль файла живёт на СТРОКЕ СВЯЗИ «файл ↔ проект», а не меткой на файле, и весь этот файл —
// следствия. Пользовательская модель при этом та, что заказывали: ряд проектов, ряд ролей,
// группировка пересечением; отличается только то, где лежит байт.

// UpdateFileTopicMeta sets a topic's kind, dates and archive flag.
//
// ПОЛНАЯ ЗАМЕНА БЕЗОПАСНА ИМЕННО ПОТОМУ, ЧТО СООБЩЕНИЕ НОВОЕ: старого клиента у этого RPC нет,
// значит форма всегда приезжает целиком. Дописать те же поля в уже отгруженный RenameFileTopic
// было бы на один RPC дешевле и опаснее по существу — клиент, не знающий про `kind`, прислал бы
// его пустым и молча понизил бы проект до обычной темы при первом же переименовании. Прецедент
// такой потери в проекте уже был (черновик тех-карты стирал отсутствующие поля).
func (s *Server) UpdateFileTopicMeta(ctx context.Context, req *pb_admin.UpdateFileTopicMetaRequest) (*pb_admin.UpdateFileTopicMetaResponse, error) {
	if req.TopicId <= 0 {
		return nil, status.Error(codes.InvalidArgument, "topic id is required")
	}
	kind, err := entity.ParseFileTopicKind(req.Kind)
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "%v", err)
	}
	startsAt, err := dto.ParseLibraryDate(req.StartsAt)
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "starts_at: %v", err)
	}
	endsAt, err := dto.ParseLibraryDate(req.EndsAt)
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "ends_at: %v", err)
	}
	// Порядок дат проверяется здесь, а не констрейнтом: ретроактивный CHECK проверил бы ВСЮ
	// историю и остановил бы старт прода, а таких дат в истории пока нет вовсе.
	if startsAt.Valid && endsAt.Valid && endsAt.Time.Before(startsAt.Time) {
		return nil, status.Error(codes.InvalidArgument, "ends_at cannot be earlier than starts_at")
	}
	cleared, err := s.repo.Files().UpdateTopicMeta(ctx, entity.FileTopicMetaUpdate{
		TopicId:  int(req.TopicId),
		Kind:     kind,
		StartsAt: startsAt,
		EndsAt:   endsAt,
		Archived: req.Archived,
	})
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, status.Error(codes.NotFound, "topic not found")
		}
		slog.Default().ErrorContext(ctx, "can't update file topic meta", slog.String("err", err.Error()))
		return nil, status.Error(codes.Internal, "can't update topic")
	}
	return &pb_admin.UpdateFileTopicMetaResponse{ClearedRoles: int32(cleared)}, nil
}

// ListFileRoles returns the closed role vocabulary with cross-project counts.
func (s *Server) ListFileRoles(ctx context.Context, req *pb_admin.ListFileRolesRequest) (*pb_admin.ListFileRolesResponse, error) {
	roles, err := s.repo.Files().ListRoles(ctx, req.GetIncludeArchived())
	if err != nil {
		slog.Default().ErrorContext(ctx, "can't list file roles", slog.String("err", err.Error()))
		return nil, status.Error(codes.Internal, "can't list roles")
	}
	return &pb_admin.ListFileRolesResponse{Roles: dto.ConvertEntityFileRolesToPb(roles)}, nil
}

// UpsertFileRole creates or edits one role — THE only path that creates one.
func (s *Server) UpsertFileRole(ctx context.Context, req *pb_admin.UpsertFileRoleRequest) (*pb_admin.UpsertFileRoleResponse, error) {
	name, err := dto.ValidateLibraryRoleName(req.Name)
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "%v", err)
	}
	id, err := s.repo.Files().UpsertRole(ctx, entity.FileRoleUpsert{
		Id:        int(req.Id),
		Name:      name,
		SortOrder: int(req.SortOrder),
		Archived:  req.Archived,
	})
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, status.Error(codes.NotFound, "role not found")
		}
		// Совпадение имени — отказ, а не молчаливое схлопывание в существующую роль: словарь
		// правят руками на одном экране, и «создал новую, а получил чужую» читается там как
		// потеря.
		if s.repo.IsErrUniqueViolation(err) {
			return nil, status.Error(codes.InvalidArgument, "a role with this name already exists")
		}
		slog.Default().ErrorContext(ctx, "can't upsert file role", slog.String("err", err.Error()))
		return nil, status.Error(codes.Internal, "can't save role")
	}
	return &pb_admin.UpsertFileRoleResponse{Id: int32(id)}, nil
}

// MergeFileRoles folds one role into another and deletes the source.
func (s *Server) MergeFileRoles(ctx context.Context, req *pb_admin.MergeFileRolesRequest) (*pb_admin.MergeFileRolesResponse, error) {
	if req.SourceId <= 0 || req.TargetId <= 0 {
		return nil, status.Error(codes.InvalidArgument, "source and target role ids are required")
	}
	if req.SourceId == req.TargetId {
		// Не no-op: слияние необратимо, и ответ «готово» на бессмысленный запрос убедил бы
		// человека, что он сделал то, чего не делал.
		return nil, status.Error(codes.InvalidArgument, "a role cannot be merged into itself")
	}
	moved, err := s.repo.Files().MergeRoles(ctx, int(req.SourceId), int(req.TargetId))
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, status.Error(codes.NotFound, "role not found")
		}
		slog.Default().ErrorContext(ctx, "can't merge file roles", slog.String("err", err.Error()))
		return nil, status.Error(codes.Internal, "can't merge roles")
	}
	return &pb_admin.MergeFileRolesResponse{MovedLinks: int32(moved)}, nil
}

// SetLibraryFileRoles puts a batch of files into one project in one role.
//
// Семантика пачки — та же, что у AssignLibraryFileTopics: ОДИН невидимый id отказывает ВСЕЙ
// пачке (NotFound), потому что частичное применение по-разному отвечало бы на видимый и
// невидимый id и тем подтверждало бы существование файла.
func (s *Server) SetLibraryFileRoles(ctx context.Context, req *pb_admin.SetLibraryFileRolesRequest) (*pb_admin.SetLibraryFileRolesResponse, error) {
	if len(req.FileIds) == 0 {
		return nil, status.Error(codes.InvalidArgument, "at least one file id is required")
	}
	if req.ProjectTopicId <= 0 {
		return nil, status.Error(codes.InvalidArgument, "project topic id is required")
	}
	if req.RoleId < 0 {
		return nil, status.Error(codes.InvalidArgument, "role id must not be negative")
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
	updated, err := s.repo.Files().SetFileRoles(ctx, fileIDs, int(req.ProjectTopicId), int(req.RoleId))
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			// Невидимый файл, несуществующий проект и несуществующая роль отвечают ОДИНАКОВО, и
			// это не небрежность: различие кодов ответа само подтверждало бы существование.
			return nil, status.Error(codes.NotFound, "file, project or role not found")
		}
		if errors.Is(err, entity.ErrRoleNeedsProjectTopic) {
			return nil, status.Errorf(codes.InvalidArgument, "%v", err)
		}
		if errors.Is(err, entity.ErrFileRoleArchived) {
			// FailedPrecondition, а не InvalidArgument: запрос правильный, состояние мира — нет.
			return nil, status.Errorf(codes.FailedPrecondition, "%v", err)
		}
		if errors.Is(err, entity.ErrLibraryBatchTooLarge) {
			return nil, status.Error(codes.InvalidArgument, err.Error())
		}
		slog.Default().ErrorContext(ctx, "can't set library file roles", slog.String("err", err.Error()))
		return nil, status.Error(codes.Internal, "can't set roles")
	}
	return &pb_admin.SetLibraryFileRolesResponse{Updated: int32(updated)}, nil
}
