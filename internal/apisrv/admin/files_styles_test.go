package admin

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"

	mocks "github.com/jekabolt/grbpwr-manager/internal/dependency/mocks"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	pb_admin "github.com/jekabolt/grbpwr-manager/proto/gen/admin"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// ПРОЕКТ ↔ СТИЛЬ НА УРОВНЕ RPC. Стор проверен контейнерным набором; здесь проверяется ровно то,
// чего в сторе НЕТ и быть не может, — КОДЫ ОТВЕТА. Они не украшение: клиент решает по коду,
// показать ли человеку «так нельзя» или «попробуйте ещё раз», и ошибка, приехавшая под Internal,
// превращается в «что-то пошло не так» на экране, где на самом деле сказано, что именно.

// TestLinkFileTopicStyleErrorMapping: «тема не проект» обязана быть InvalidArgument, а не
// Internal и не FailedPrecondition.
//
// InvalidArgument, потому что запрос АДРЕСОВАН не туда: обычный ярлык не становится проектом сам,
// и повтор того же запроса не поможет — в отличие от FailedPrecondition, который обещает, что
// поможет.
func TestLinkFileTopicStyleErrorMapping(t *testing.T) {
	ctx := context.Background()

	s := &Server{}
	if _, err := s.LinkFileTopicStyle(ctx, &pb_admin.LinkFileTopicStyleRequest{TechCardId: 2}); status.Code(err) != codes.InvalidArgument {
		t.Errorf("no topic: want InvalidArgument, got %v", err)
	}
	if _, err := s.LinkFileTopicStyle(ctx, &pb_admin.LinkFileTopicStyleRequest{TopicId: 1}); status.Code(err) != codes.InvalidArgument {
		t.Errorf("no style: want InvalidArgument, got %v", err)
	}

	files := mocks.NewMockFiles(t)
	files.EXPECT().LinkTopicStyle(mock.Anything, 1, 2).Return(entity.ErrStyleNeedsProjectTopic)
	repo := mocks.NewMockRepository(t)
	repo.EXPECT().Files().Return(files)
	plain := &Server{repo: repo}
	_, err := plain.LinkFileTopicStyle(ctx, &pb_admin.LinkFileTopicStyleRequest{TopicId: 1, TechCardId: 2})
	require.Equal(t, codes.InvalidArgument, status.Code(err),
		"«эта вещь сделана ярлыком» — не сбой сервера: человеку надо сказать, что тему сначала делают проектом")

	// Несуществующая тема и несуществующий стиль отвечают ОДИНАКОВО — различие кодов само
	// подтверждало бы, какая из двух сущностей существует.
	missingFiles := mocks.NewMockFiles(t)
	missingFiles.EXPECT().LinkTopicStyle(mock.Anything, 3, 4).Return(sql.ErrNoRows)
	missingRepo := mocks.NewMockRepository(t)
	missingRepo.EXPECT().Files().Return(missingFiles)
	missing := &Server{repo: missingRepo}
	_, err = missing.LinkFileTopicStyle(ctx, &pb_admin.LinkFileTopicStyleRequest{TopicId: 3, TechCardId: 4})
	require.Equal(t, codes.NotFound, status.Code(err))
}

// TestUnlinkFileTopicStyleErrorMapping: отвязка отвечает ТЕМИ ЖЕ кодами, что привязка.
//
// Единый маппинг у обеих мутаций — не экономия строк: «тема не проект» на одном RPC как
// InvalidArgument, а на другом как FailedPrecondition означало бы, что клиент обязан помнить, к
// какой кнопке какая ветка обработки, — и однажды не вспомнит.
func TestUnlinkFileTopicStyleErrorMapping(t *testing.T) {
	ctx := context.Background()

	s := &Server{}
	if _, err := s.UnlinkFileTopicStyle(ctx, &pb_admin.UnlinkFileTopicStyleRequest{TechCardId: 2}); status.Code(err) != codes.InvalidArgument {
		t.Errorf("no topic: want InvalidArgument, got %v", err)
	}
	if _, err := s.UnlinkFileTopicStyle(ctx, &pb_admin.UnlinkFileTopicStyleRequest{TopicId: 1}); status.Code(err) != codes.InvalidArgument {
		t.Errorf("no style: want InvalidArgument, got %v", err)
	}

	files := mocks.NewMockFiles(t)
	files.EXPECT().UnlinkTopicStyle(mock.Anything, 3, 4).Return(sql.ErrNoRows)
	repo := mocks.NewMockRepository(t)
	repo.EXPECT().Files().Return(files)
	s2 := &Server{repo: repo}
	_, err := s2.UnlinkFileTopicStyle(ctx, &pb_admin.UnlinkFileTopicStyleRequest{TopicId: 3, TechCardId: 4})
	require.Equal(t, codes.NotFound, status.Code(err))
}

// TestLinkFileTopicStyleReadAfterWriteIsNotNotFound: провал ДОЧИТЫВАНИЯ после успешной записи не
// имеет права выглядеть как «не найдено».
//
// Тему могли удалить в гонке между записью и перечитыванием списка. Через общий маппинг ошибок
// это дало бы NotFound на ВЫПОЛНЕННОЙ записи — клиент показал бы «не найдено» там, где связь
// заведена, и человек нажал бы ещё раз. Запись состоялась; не состоялось дочитывание.
func TestLinkFileTopicStyleReadAfterWriteIsNotNotFound(t *testing.T) {
	files := mocks.NewMockFiles(t)
	files.EXPECT().LinkTopicStyle(mock.Anything, 1, 7).Return(nil)
	files.EXPECT().ListTopicStyles(mock.Anything, 1).Return(nil, sql.ErrNoRows)
	repo := mocks.NewMockRepository(t)
	repo.EXPECT().Files().Return(files)

	s := &Server{repo: repo}
	_, err := s.LinkFileTopicStyle(context.Background(),
		&pb_admin.LinkFileTopicStyleRequest{TopicId: 1, TechCardId: 7})
	require.Equal(t, codes.Internal, status.Code(err),
		"NotFound на выполненной записи заставил бы человека нажать кнопку второй раз")
}

// TestDeleteFileTopicReportsUnlinkedStyles: удаление темы — единственный разрушительный путь
// фазы, и он не имеет права быть немым.
//
// Проект без файлов, но с привязанными вещами, удаляется штатно: счётчик на экране тем показывает
// ФАЙЛЫ и о привязках не знает, поэтому отказ был бы тупиком без способа увидеть, во что упёрся.
// Значит число обязано доехать — иначе «убрал пустую съёмку с глаз» и «у восьми вещей пропал
// ответ, каким файлом их сделали» станут двумя событиями, между которыми месяц.
//
// ЧИСЕЛ ДВА (0322), И ОНИ РАЗНЫЕ НАРОЧНО: подмена одного другим на равных количествах была бы
// невидима, а именно она — самая правдоподобная ошибка в хендлере, который перекладывает поля.
func TestDeleteFileTopicReportsUnlinkedStyles(t *testing.T) {
	files := mocks.NewMockFiles(t)
	files.EXPECT().DeleteTopic(mock.Anything, 5).
		Return(entity.FileTopicDeleteResult{UnlinkedStyles: 8, UnlinkedTasks: 3}, nil)
	repo := mocks.NewMockRepository(t)
	repo.EXPECT().Files().Return(files)

	s := &Server{repo: repo}
	resp, err := s.DeleteFileTopic(context.Background(), &pb_admin.DeleteFileTopicRequest{Id: 5})
	require.NoError(t, err)
	require.Equal(t, int32(8), resp.UnlinkedStyles)
	require.Equal(t, int32(3), resp.UnlinkedTasks,
		"задачи теряют проект, но остаются: ключ SET NULL, а не каскадный, — и число обязано быть названо отдельно от стилей")
}

// TestLinkFileTopicStyleSurvivesPreviewFailure: миниатюра — украшение, а список — ответ.
//
// Резолв превью ходит в ДРУГОЙ стор (тех-карт), потому что правило выбора картинки живёт там и
// второй его реализации быть не должно. Цена этого решения — вторая точка отказа на пути ответа,
// и она обязана быть НЕ фатальной: уронить страницу проекта из-за недоступной миниатюры значило
// бы поставить украшение выше ответа.
func TestLinkFileTopicStyleSurvivesPreviewFailure(t *testing.T) {
	ctx := context.Background()

	files := mocks.NewMockFiles(t)
	files.EXPECT().LinkTopicStyle(mock.Anything, 1, 7).Return(nil)
	files.EXPECT().ListTopicStyles(mock.Anything, 1).Return([]entity.FileTopicStyleRef{{
		TechCardId: 7, StyleNumber: "SS26-001", Name: "пальто",
		Stage: entity.TechCardStageProto, LinkedAt: time.Now(),
	}}, nil)
	cards := mocks.NewMockTechCards(t)
	cards.EXPECT().PreviewURLsByTechCardIds(mock.Anything, []int{7}).
		Return(nil, errors.New("media store is down"))
	repo := mocks.NewMockRepository(t)
	repo.EXPECT().Files().Return(files)
	repo.EXPECT().TechCards().Return(cards)

	s := &Server{repo: repo}
	resp, err := s.LinkFileTopicStyle(ctx, &pb_admin.LinkFileTopicStyleRequest{TopicId: 1, TechCardId: 7})
	require.NoError(t, err, "недоступная миниатюра не имеет права ронять ответ")
	require.Len(t, resp.Styles, 1)
	require.Equal(t, "SS26-001", resp.Styles[0].StyleNumber, "вещь опознают по артикулу, а не по картинке")
	require.Empty(t, resp.Styles[0].PreviewUrl)
	require.NotNil(t, resp.Styles[0].LinkedAt, "«привязано» печатается на карточке вещи")
}

// TestListStyleFileProjectsCarriesCountAndArchive: карточка вещи получает ЧИСЛО ФАЙЛОВ (уже
// посчитанное под предикатом видимости в сторе) и ПОМЕТКУ АРХИВА.
//
// Архив здесь не прячется — в отличие от рельса тем. Рельс это навигация по живой работе;
// карточка вещи задаёт исторический вопрос, и законченная съёмка — ровно тот ответ, ради которого
// экран заведён. Значит пометка обязана доехать: без неё архивный проект выглядит текущей работой.
func TestListStyleFileProjectsCarriesCountAndArchive(t *testing.T) {
	ctx := context.Background()

	s := &Server{}
	if _, err := s.ListStyleFileProjects(ctx, &pb_admin.ListStyleFileProjectsRequest{}); status.Code(err) != codes.InvalidArgument {
		t.Errorf("no style id: want InvalidArgument, got %v", err)
	}

	archived := entity.StyleProjectLink{LinkedAt: time.Now()}
	archived.Id = 9
	archived.Name = "съёмка осень 2026"
	archived.Kind = entity.FileTopicKindProject
	archived.ArchivedAt = sql.NullTime{Time: time.Now(), Valid: true}
	archived.FilesCount = 4

	files := mocks.NewMockFiles(t)
	files.EXPECT().ListStyleProjects(mock.Anything, 7).Return([]entity.StyleProjectLink{archived}, nil)
	repo := mocks.NewMockRepository(t)
	repo.EXPECT().Files().Return(files)

	srv := &Server{repo: repo}
	resp, err := srv.ListStyleFileProjects(ctx, &pb_admin.ListStyleFileProjectsRequest{TechCardId: 7})
	require.NoError(t, err)
	require.Len(t, resp.Projects, 1)
	require.Equal(t, int32(4), resp.Projects[0].Project.FilesCount,
		"число файлов обязано доехать: оно уже посчитано под предикатом, и потерять его здесь значит показать пустой проект вместо непустого")
	require.True(t, resp.Projects[0].Project.Archived,
		"пометка архива обязана доехать, иначе законченная съёмка читается как текущая работа")
	require.Equal(t, "project", resp.Projects[0].Project.Kind)
}

// TestListStyleFileProjectsUnknownStyleIsEmpty: несуществующий стиль — ПУСТОЙ СПИСОК, а не отказ.
//
// Отличимый отказ превратил бы RPC в ОРАКУЛ: перебирая id и различая «не найден» от «ничего не
// нашлось», обладатель одного лишь files:read пересчитал бы тех-карты, ни разу не имея права их
// читать.
func TestListStyleFileProjectsUnknownStyleIsEmpty(t *testing.T) {
	files := mocks.NewMockFiles(t)
	files.EXPECT().ListStyleProjects(mock.Anything, 999999).Return(nil, nil)
	repo := mocks.NewMockRepository(t)
	repo.EXPECT().Files().Return(files)

	s := &Server{repo: repo}
	resp, err := s.ListStyleFileProjects(context.Background(),
		&pb_admin.ListStyleFileProjectsRequest{TechCardId: 999999})
	require.NoError(t, err)
	require.Empty(t, resp.Projects)
}

// TestUpdateFileTopicMetaReportsBothLosses: понижение проекта снимает ДВА проектных свойства —
// роли на строках связи и привязки стилей, — и обязано сказать про оба.
//
// Молчание про второе стоило бы дороже: карточка вещи потеряла бы ответ на «каким файлом меня
// сделали» в тот день, когда кто-то переключил тип темы, и связать одно с другим было бы нечем.
func TestUpdateFileTopicMetaReportsBothLosses(t *testing.T) {
	files := mocks.NewMockFiles(t)
	files.EXPECT().UpdateTopicMeta(mock.Anything, mock.Anything).
		Return(entity.FileTopicMetaResult{ClearedRoles: 3, ClearedStyles: 2}, nil)
	repo := mocks.NewMockRepository(t)
	repo.EXPECT().Files().Return(files)

	s := &Server{repo: repo}
	resp, err := s.UpdateFileTopicMeta(context.Background(), &pb_admin.UpdateFileTopicMetaRequest{
		TopicId: 1, Kind: "plain",
	})
	require.NoError(t, err)
	require.Equal(t, int32(3), resp.ClearedRoles)
	require.Equal(t, int32(2), resp.ClearedStyles,
		"второе число обязано доехать: без него понижение, снявшее дюжину привязок, снаружи неотличимо от понижения, не снявшего ни одной")
}
