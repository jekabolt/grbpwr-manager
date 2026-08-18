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
	res, err := s.repo.Files().UpdateTopicMeta(ctx, entity.FileTopicMetaUpdate{
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
	// ТРИ ЧИСЛА, ПОТОМУ ЧТО ПРОЕКТНЫХ СВОЙСТВ У ТЕМЫ ТРИ: роли на строках связи (0320), привязанные
	// стили (0321) и задачи, которые в этом проекте делаются (0322). Понижение снимает все три, и
	// молчание о любом стоило бы дороже: карточка вещи потеряла бы ответ на «каким файлом меня
	// сделали», а доска — контекст работы, в тот день, когда кто-то переключил тип темы, и связать
	// одно с другим было бы нечем.
	//
	// ЧИСЛА ЕДУТ РАЗДЕЛЬНО, А ФРАЗУ СОБИРАЕТ КЛИЕНТ. Три числа в одном тосте — это уже
	// перечисление, и сказать его надо так, как человек читает; но сложить их на сервере значило бы
	// отнять у экрана возможность это сделать, потому что сумма не разбирается обратно.
	return &pb_admin.UpdateFileTopicMetaResponse{
		ClearedRoles:  int32(res.ClearedRoles),
		ClearedStyles: int32(res.ClearedStyles),
		ClearedTasks:  int32(res.ClearedTasks),
	}, nil
}

// ListFileRoles returns ONE project's role vocabulary — or, with project_topic_id = 0, every role
// tagged with its owner.
//
// НОЛЬ НЕ ОТКАЗЫВАЕТ, И ЭТО РЕШЕНИЕ ПРО ОКНО СОВМЕСТИМОСТИ, А НЕ ПОБЛАЖКА. Бэкенд уезжает на бету
// раньше клиента, и клиент, ничего не знающий про владельца, обязан продолжать листать роли и
// фильтровать по ним; ему же ноль нужен, чтобы разрешить старую ссылку `?frole=N` в проект.
// Опасности в этом нет: словарь для ВЫБОРА определяется ответом с проектом, а простановку чужой
// роли сервер отвергает независимо от того, откуда клиент её взял.
func (s *Server) ListFileRoles(ctx context.Context, req *pb_admin.ListFileRolesRequest) (*pb_admin.ListFileRolesResponse, error) {
	if req.ProjectTopicId < 0 {
		return nil, status.Error(codes.InvalidArgument, "project topic id must not be negative")
	}
	roles, err := s.repo.Files().ListRoles(ctx, req.GetIncludeArchived(), int(req.GetProjectTopicId()))
	if err != nil {
		slog.Default().ErrorContext(ctx, "can't list file roles", slog.String("err", err.Error()))
		return nil, status.Error(codes.Internal, "can't list roles")
	}
	return &pb_admin.ListFileRolesResponse{Roles: dto.ConvertEntityFileRolesToPb(roles)}, nil
}

// UpsertFileRole creates or edits one role IN A PROJECT — THE only path that creates one.
func (s *Server) UpsertFileRole(ctx context.Context, req *pb_admin.UpsertFileRoleRequest) (*pb_admin.UpsertFileRoleResponse, error) {
	name, err := dto.ValidateLibraryRoleName(req.Name)
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "%v", err)
	}
	if req.ProjectTopicId < 0 {
		return nil, status.Error(codes.InvalidArgument, "project topic id must not be negative")
	}
	id, err := s.repo.Files().UpsertRole(ctx, entity.FileRoleUpsert{
		Id:             int(req.Id),
		ProjectTopicId: int(req.ProjectTopicId),
		Name:           name,
		SortOrder:      int(req.SortOrder),
		Archived:       req.Archived,
	})
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			// ДВА РАЗНЫХ ОТСУТСТВИЯ, И РАЗЛИЧАЕТ ИХ ЗАПРОС, А НЕ ОШИБКА. При создании нет
			// только одного кандидата на пропажу — темы-проекта; при правке — самой роли.
			// Одна фраза на оба случая отправила бы человека искать не то, что потерялось.
			if req.Id <= 0 {
				return nil, status.Error(codes.NotFound, "project topic not found")
			}
			return nil, status.Error(codes.NotFound, "role not found")
		}
		// РОЛЬ ЗАВОДИТСЯ ТОЛЬКО ВНУТРИ ПРОЕКТА. Это же и есть названное окно несовместимости:
		// старый клиент, знающий общий словарь, шлёт создание без проекта и получает читаемый
		// отказ вместо роли-сироты, которую потом не нашёл бы ни один экран.
		if errors.Is(err, entity.ErrFileRoleNeedsProject) {
			return nil, status.Errorf(codes.InvalidArgument, "%v", err)
		}
		if errors.Is(err, entity.ErrFileRoleProjectImmutable) {
			return nil, status.Errorf(codes.InvalidArgument, "%v", err)
		}
		// Совпадение имени — отказ, а не молчаливое схлопывание в существующую роль: словарь
		// правят руками на одном экране, и «создал новую, а получил чужую» читается там как
		// потеря. Уникальность теперь ПАРНАЯ, поэтому и фраза говорит «в этом проекте»: без
		// уточнения она врала бы — то же имя в соседнем проекте совершенно законно.
		if s.repo.IsErrUniqueViolation(err) {
			return nil, status.Error(codes.InvalidArgument, "a role with this name already exists in this project")
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
		// Роли разных проектов слить нельзя: их строки связи живут в разных проектах, и одной
		// сущностью они не были никогда. Диалог обязан предлагать цели только своего проекта —
		// этот отказ страхует от списка, собранного не по тому словарю.
		if errors.Is(err, entity.ErrFileRoleProjectMismatch) {
			return nil, status.Errorf(codes.InvalidArgument, "%v", err)
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
		// РОЛЬ ЧУЖОГО ПРОЕКТА — ОТДЕЛЬНАЯ ФРАЗА, А НЕ ТРОЙНОЙ NotFound. Тройка «файл, проект или
		// роль не найдены» одинакова намеренно: различие кодов подтверждало бы существование
		// файла. Здесь подтверждать нечего — роль в ответе уже была, клиент сам её показал, — а
		// вот сказать, ЧТО именно не так, обязательно: иначе диалог, собравший словарь не того
		// проекта, выглядит сломанным без единой подсказки, что чинить.
		if errors.Is(err, entity.ErrFileRoleForeignProject) {
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
