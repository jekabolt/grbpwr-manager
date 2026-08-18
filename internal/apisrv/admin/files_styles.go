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

// ПРОЕКТ ↔ СТИЛЬ: «КАКИМ ФАЙЛОМ СДЕЛАНА ЭТА ВЕЩЬ».
//
// Ф0 разложила файлы ВНУТРИ проекта. Здесь закрывается обратный вопрос: человек стоит на карточке
// вещи и спрашивает, каким .zprj она сшита. Связь многие-ко-многим, потому что множественны обе
// стороны: съёмка покрывает капсулу, а бекап CLO — одну вещь, попадающую и в съёмку, и в лукбук.
//
// ОДНА ОБЩАЯ ФОРМА ОТВЕТА У ОБЕИХ МУТАЦИЙ — список стилей ПРОЕКТА после правки. Тот же приём, что
// у SetLibraryFileOwners: экран перерисовывается из того, что реально сохранилось, а не из того,
// что он надеялся отправить.
//
// ЭТО СПИСОК ПРОЕКТА, А НЕ СПИСОК ВЕЩИ, и вызывающему с КАРТОЧКИ ВЕЩИ он не заменяет
// ListStyleFileProjects — оттуда после привязки нужен свой перечитать. Возвращать оба списка
// было бы можно (оба id в запросе есть), но ответ мутации стал бы двумя разными ответами, и
// половина его всегда была бы лишней. Отдаётся тот, чья ФОРМА изменилась необратимо: у проекта
// список вещей и есть содержимое экрана, а у вещи проекты — боковой блок.

// projectStylesResponse resolves the project's styles and paints their previews.
//
// ПРЕВЬЮ РЕЗОЛВИТСЯ ЗДЕСЬ, А НЕ В СТОРЕ БИБЛИОТЕКИ, И ЭТО ЕДИНСТВЕННЫЙ СПОСОБ НЕ ЗАВЕСТИ ВТОРУЮ
// ПРАВДУ. Правило выбора картинки трёхвходовое (стадия × категория медиа × вид) и живёт в сторе
// тех-карт; вторая его реализация в fileslibrary разошлась бы с первой молча, и на экране это
// выглядело бы как «у одной и той же вещи в двух местах разные картинки» — дефект, который ищут в
// клиенте, а лежит он в SQL. Хендлер поэтому СОСТАВЛЯЕТ ответ из двух источников, каждый из
// которых остаётся единственным в своём вопросе.
//
// Провал резолва превью НЕ роняет ответ: список опознаётся по артикулу и имени, а картинка —
// украшение. Уронить страницу проекта из-за недоступной миниатюры значило бы поставить украшение
// выше ответа.
func (s *Server) projectStylesResponse(ctx context.Context, topicID int) ([]*pb_admin.FileTopicStyle, error) {
	styles, err := s.repo.Files().ListTopicStyles(ctx, topicID)
	if err != nil {
		return nil, err
	}
	ids := make([]int, 0, len(styles))
	for _, st := range styles {
		ids = append(ids, st.TechCardId)
	}
	previews, err := s.repo.TechCards().PreviewURLsByTechCardIds(ctx, ids)
	if err != nil {
		slog.Default().WarnContext(ctx, "can't resolve project style previews; previews omitted",
			slog.String("err", err.Error()))
		previews = nil
	}
	return dto.ConvertEntityFileTopicStylesToPb(styles, previews), nil
}

// linkStyleError maps the store's refusals onto codes ONE way for both mutations, so «тема не
// проект» never reads as InvalidArgument on one rpc and FailedPrecondition on the other.
func linkStyleError(ctx context.Context, err error, what string) error {
	if errors.Is(err, sql.ErrNoRows) {
		// Несуществующая тема и несуществующий стиль отвечают ОДИНАКОВО. Различие кодов здесь
		// само подтверждало бы, какая из двух сущностей существует.
		return status.Error(codes.NotFound, "project or style not found")
	}
	if errors.Is(err, entity.ErrStyleNeedsProjectTopic) {
		// InvalidArgument, а не FailedPrecondition: запрос АДРЕСОВАН не туда — обычный ярлык не
		// становится проектом сам, и повтор того же запроса не поможет.
		return status.Errorf(codes.InvalidArgument, "%v", err)
	}
	slog.Default().ErrorContext(ctx, what, slog.String("err", err.Error()))
	return status.Error(codes.Internal, "can't change project styles")
}

// afterWriteReadError is the code for a failure to RE-READ the list after the write already
// succeeded, and it is deliberately NOT linkStyleError.
//
// Через linkStyleError sql.ErrNoRows (тему успели удалить в гонке) превратился бы в NotFound на
// ВЫПОЛНЕННОЙ записи — то есть клиент показал бы «не найдено» там, где связь заведена, и человек
// нажал бы ещё раз. Запись состоялась; не состоялось дочитывание, и это Internal, что бы там ни
// вернул читающий запрос.
func afterWriteReadError(ctx context.Context, err error, what string) error {
	slog.Default().ErrorContext(ctx, what, slog.String("err", err.Error()))
	return status.Error(codes.Internal, "styles changed, but the project's style list could not be read")
}

// LinkFileTopicStyle attaches a style to a project. Idempotent: a repeat is a
// no-op, because the button lives on two screens at once and the second person to
// press it got exactly what they wanted — the link exists.
func (s *Server) LinkFileTopicStyle(ctx context.Context, req *pb_admin.LinkFileTopicStyleRequest) (*pb_admin.LinkFileTopicStyleResponse, error) {
	if req.TopicId <= 0 {
		return nil, status.Error(codes.InvalidArgument, "project topic id is required")
	}
	if req.TechCardId <= 0 {
		return nil, status.Error(codes.InvalidArgument, "style id is required")
	}
	if err := s.repo.Files().LinkTopicStyle(ctx, int(req.TopicId), int(req.TechCardId)); err != nil {
		return nil, linkStyleError(ctx, err, "can't link a style to a project")
	}
	styles, err := s.projectStylesResponse(ctx, int(req.TopicId))
	if err != nil {
		return nil, afterWriteReadError(ctx, err, "can't list project styles after linking")
	}
	return &pb_admin.LinkFileTopicStyleResponse{Styles: styles}, nil
}

// UnlinkFileTopicStyle detaches a style. Idempotent for the same reason as the
// link: the row is a bare fact, and removing what is not there has already
// achieved what was asked.
func (s *Server) UnlinkFileTopicStyle(ctx context.Context, req *pb_admin.UnlinkFileTopicStyleRequest) (*pb_admin.UnlinkFileTopicStyleResponse, error) {
	if req.TopicId <= 0 {
		return nil, status.Error(codes.InvalidArgument, "project topic id is required")
	}
	if req.TechCardId <= 0 {
		return nil, status.Error(codes.InvalidArgument, "style id is required")
	}
	if err := s.repo.Files().UnlinkTopicStyle(ctx, int(req.TopicId), int(req.TechCardId)); err != nil {
		return nil, linkStyleError(ctx, err, "can't unlink a style from a project")
	}
	styles, err := s.projectStylesResponse(ctx, int(req.TopicId))
	if err != nil {
		return nil, afterWriteReadError(ctx, err, "can't list project styles after unlinking")
	}
	return &pb_admin.UnlinkFileTopicStyleResponse{Styles: styles}, nil
}

// ListFileTopicStyles returns the garments a project is about.
func (s *Server) ListFileTopicStyles(ctx context.Context, req *pb_admin.ListFileTopicStylesRequest) (*pb_admin.ListFileTopicStylesResponse, error) {
	if req.TopicId <= 0 {
		return nil, status.Error(codes.InvalidArgument, "topic id is required")
	}
	styles, err := s.projectStylesResponse(ctx, int(req.TopicId))
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, status.Error(codes.NotFound, "topic not found")
		}
		slog.Default().ErrorContext(ctx, "can't list project styles", slog.String("err", err.Error()))
		return nil, status.Error(codes.Internal, "can't list project styles")
	}
	return &pb_admin.ListFileTopicStylesResponse{Styles: styles}, nil
}

// ListStyleFileProjects is THE call this phase exists for: «какие проекты меня
// касаются», asked from the garment's own card.
//
// Число файлов в каждом проекте посчитано ПОД ПРЕДИКАТОМ ВИДИМОСТИ ещё в сторе — иначе эта
// карточка стала бы боковым каналом, через который видно, что в проекте есть скрытые файлы.
//
// НЕСУЩЕСТВУЮЩИЙ СТИЛЬ — ПУСТОЙ СПИСОК, А НЕ ОТКАЗ, и здесь он даже не проверяется. Отличимый
// отказ превратил бы RPC в ОРАКУЛ: перебирая id и различая «не найден» от «ничего не нашлось»,
// обладатель одного лишь files:read пересчитал бы тех-карты, ни разу не имея права их читать. Тот
// же довод, по которому фильтр по человеку не проверяет существование аккаунта.
func (s *Server) ListStyleFileProjects(ctx context.Context, req *pb_admin.ListStyleFileProjectsRequest) (*pb_admin.ListStyleFileProjectsResponse, error) {
	if req.TechCardId <= 0 {
		return nil, status.Error(codes.InvalidArgument, "style id is required")
	}
	links, err := s.repo.Files().ListStyleProjects(ctx, int(req.TechCardId))
	if err != nil {
		slog.Default().ErrorContext(ctx, "can't list style projects", slog.String("err", err.Error()))
		return nil, status.Error(codes.Internal, "can't list style projects")
	}
	return &pb_admin.ListStyleFileProjectsResponse{Projects: dto.ConvertEntityStyleProjectsToPb(links)}, nil
}
