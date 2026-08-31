package admin

import (
	"context"
	"database/sql"
	"encoding/json"
	"testing"
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/dependency/mocks"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	pb_admin "github.com/jekabolt/grbpwr-manager/proto/gen/admin"
	pb_common "github.com/jekabolt/grbpwr-manager/proto/gen/common"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
	pb_decimal "google.golang.org/genproto/googleapis/type/decimal"
	"google.golang.org/grpc/codes"
)

// ПРОБЫ ПОЛОК АССЕТОВ НА ПРОВОДЕ (0354).
//
// ЧТО ОНИ СТЕРЕГУТ. Всё, что ломается МОЛЧА: конвертер, потерявший поле (ответ приходит с OK, у
// плитки просто нет раппорта); чтение полосы, из которого выпали две строки (полоса отвечает 200,
// стена полок пуста); отказ, которого нет в таблице designRefusals (ожидаемое состояние уезжает
// клиенту как Internal); и сериализация геометрии в camelCase (колонка читается по-прежнему, а
// всякий SQL-путь по ней замолкает навсегда).
//
// ЧТО ОНИ НЕ ДОКАЗЫВАЮТ: стор здесь замокан, поэтому потолок полки, принадлежность родителя и
// SERIALIZABLE-транзакция — собственность internal/store/design и живой базы.

// designAssetRig — сервер, у которого замокан ровно Design(). sentUpsert / sentPlacement — то, что
// хендлер отдал стору: у половины проб предмет именно это, а не ответ.
type designAssetRig struct {
	srv           *Server
	design        *mocks.MockDesign
	sentUpsert    *entity.DesignAssetUpsert
	sentPlacement *entity.DesignAssetPlacementSet
}

func newDesignAssetRig(t *testing.T) *designAssetRig {
	t.Helper()
	rig := &designAssetRig{design: mocks.NewMockDesign(t)}
	repo := mocks.NewMockRepository(t)
	repo.EXPECT().Design().Return(rig.design).Maybe()
	rig.srv = &Server{repo: repo}
	return rig
}

func (r *designAssetRig) expectUpsert(asset *entity.DesignAsset, err error) {
	r.design.EXPECT().UpsertAsset(mock.Anything, mock.AnythingOfType("entity.DesignAssetUpsert")).
		Run(func(_ context.Context, req entity.DesignAssetUpsert) {
			cp := req
			r.sentUpsert = &cp
		}).Return(asset, err).Maybe()
}

func (r *designAssetRig) expectPlacement(pl *entity.DesignAssetPlacement, err error) {
	r.design.EXPECT().SetAssetPlacement(mock.Anything, mock.AnythingOfType("entity.DesignAssetPlacementSet")).
		Run(func(_ context.Context, req entity.DesignAssetPlacementSet) {
			cp := req
			r.sentPlacement = &cp
		}).Return(pl, err).Maybe()
}

// designSampleAsset — строка полки со ВСЕМИ заполненными полями, включая те, что законно пусты.
// Заполнены они именно затем, чтобы потерянное поле было видно.
func designSampleAsset() entity.DesignAsset {
	born := time.Date(2026, 8, 30, 11, 0, 0, 0, time.UTC)
	return entity.DesignAsset{
		Id:                 12,
		TechCardId:         designRunCardID,
		Kind:               entity.DesignAssetKindPattern,
		Name:               "diagonal stripe",
		MediaId:            sql.NullInt32{Int32: 77, Valid: true},
		ColourCode:         sql.NullString{String: "PANTONE 19-4052", Valid: true},
		ColourHex:          sql.NullString{String: "#0F4C81", Valid: true},
		Note:               sql.NullString{String: "brushed, slight sheen", Valid: true},
		DerivedFromAssetId: sql.NullInt32{Int32: 5, Valid: true},
		RepeatMm:           120,
		RotationDeg:        45,
		Ordinal:            3,
		CreatedBy:          "designer",
		CreatedAt:          born,
		UpdatedAt:          born.Add(time.Hour),
		Media: &entity.MediaFull{
			Id:        77,
			MediaItem: entity.MediaItem{FullSizeMediaURL: "https://example.test/stripe.webp"},
		},
	}
}

// КОНВЕРТЕР АССЕТА НЕ ТЕРЯЕТ НИ ОДНОГО ПОЛЯ.
//
// МУТАЦИЯ, КОТОРУЮ ОНА ЛОВИТ: выбросить любую строку из designAssetToPb — например `RepeatMm` или
// `Media`. Компилируется, ответ приходит с OK, и плитка на экране просто теряет раппорт либо
// свою текстуру. Round trip этого не показывает: у ответа ЕСТЬ ассет, у ассета есть id и имя.
func TestDesignAssetConverterCarriesEveryField(t *testing.T) {
	a := designSampleAsset()
	pb := designAssetToPb(a)
	require.NotNil(t, pb)

	assert.Equal(t, int32(12), pb.GetId())
	assert.Equal(t, int32(designRunCardID), pb.GetTechCardId())
	assert.Equal(t, entity.DesignAssetKindPattern, pb.GetKind())
	assert.Equal(t, "diagonal stripe", pb.GetName())
	assert.Equal(t, int32(77), pb.GetMediaId())
	assert.Equal(t, "PANTONE 19-4052", pb.GetColourCode())
	assert.Equal(t, "#0F4C81", pb.GetColourHex())
	assert.Equal(t, "brushed, slight sheen", pb.GetNote())
	assert.Equal(t, int32(5), pb.GetDerivedFromAssetId())
	assert.Equal(t, int32(120), pb.GetRepeatMm(), "раппорт — то самое число, на которое модель действует")
	assert.Equal(t, int32(45), pb.GetRotationDeg())
	assert.Equal(t, int32(3), pb.GetOrdinal())
	assert.Equal(t, "designer", pb.GetCreatedBy())
	require.NotNil(t, pb.GetCreatedAt())
	require.NotNil(t, pb.GetUpdatedAt())
	assert.Equal(t, a.CreatedAt.Unix(), pb.GetCreatedAt().GetSeconds())
	assert.Equal(t, a.UpdatedAt.Unix(), pb.GetUpdatedAt().GetSeconds())
	require.NotNil(t, pb.GetMedia(), "плитка без файла — рамка без картинки")
	assert.Equal(t, int32(77), pb.GetMedia().GetId())

	// Незаполненные колонки уезжают проводным «не сказано», а не выдуманным третьим состоянием.
	bare := designAssetToPb(entity.DesignAsset{Id: 1, Kind: entity.DesignAssetKindFabric, Name: "calico"})
	assert.Zero(t, bare.GetMediaId())
	assert.Nil(t, bare.GetMedia())
	assert.Empty(t, bare.GetColourHex())
	assert.Zero(t, bare.GetDerivedFromAssetId())
}

// designSampleAnnotation — фигура с ЗАПОЛНЕННЫМИ полями всех родов: вид, две точки, плашка, цвет,
// пунктир. Пустая фигура прошла бы любой конвертер.
func designSampleAnnotation() *pb_common.TechCardAnnotation {
	return &pb_common.TechCardAnnotation{
		Kind: pb_common.TechCardAnnotationKind_TECH_CARD_ANNOTATION_KIND_DIM,
		Points: []*pb_common.TechCardAnnotationPoint{
			{X: &pb_decimal.Decimal{Value: "0.25"}, Y: &pb_decimal.Decimal{Value: "0.4"}},
			{X: &pb_decimal.Decimal{Value: "0.75"}, Y: &pb_decimal.Decimal{Value: "0.4"}},
		},
		LabelX: &pb_decimal.Decimal{Value: "0.5"},
		LabelY: &pb_decimal.Decimal{Value: "0.33"},
		Color:  pb_common.TechCardAnnotationColor_TECH_CARD_ANNOTATION_COLOR_RED,
		Dashed: true,
	}
}

// КОНВЕРТЕР МЕТКИ ВОЗВРАЩАЕТ ТУ ЖЕ ФИГУРУ, КОТОРУЮ ЗАПИСАЛИ.
//
// МУТАЦИЯ: убрать разбор `p.Annotation` в designAssetPlacementToPb — метка приезжает без геометрии,
// клиент рисует пустоту на флэте, а строка при этом утверждает, что ассет размечен.
func TestDesignAssetPlacementConverterRoundTripsTheGeometry(t *testing.T) {
	raw, err := designMarshalJSON(designSampleAnnotation())
	require.NoError(t, err)

	set := time.Date(2026, 8, 30, 12, 0, 0, 0, time.UTC)
	pb := designAssetPlacementToPb(entity.DesignAssetPlacement{
		Id:         8,
		AssetId:    12,
		PictureId:  91,
		Annotation: entity.RawJSON(raw),
		Note:       sql.NullString{String: "cut on the bias here", Valid: true},
		SetBy:      "designer",
		SetAt:      set,
	})
	require.NotNil(t, pb)
	assert.Equal(t, int32(8), pb.GetId())
	assert.Equal(t, int32(12), pb.GetAssetId())
	assert.Equal(t, int32(91), pb.GetPictureId())
	assert.Equal(t, "cut on the bias here", pb.GetNote())
	assert.Equal(t, "designer", pb.GetSetBy())
	require.NotNil(t, pb.GetSetAt())
	assert.Equal(t, set.Unix(), pb.GetSetAt().GetSeconds())

	ann := pb.GetAnnotation()
	require.NotNil(t, ann, "метка без фигуры не указывает никуда")
	assert.Equal(t, pb_common.TechCardAnnotationKind_TECH_CARD_ANNOTATION_KIND_DIM, ann.GetKind(),
		"вид и цвет на проводе — ЭНУМЫ: собранный руками объект разбирается в ПУСТОЕ сообщение без ошибки")
	require.Len(t, ann.GetPoints(), 2)
	assert.Equal(t, "0.25", ann.GetPoints()[0].GetX().GetValue())
	assert.Equal(t, "0.4", ann.GetPoints()[1].GetY().GetValue())
	assert.Equal(t, "0.5", ann.GetLabelX().GetValue())
	assert.Equal(t, pb_common.TechCardAnnotationColor_TECH_CARD_ANNOTATION_COLOR_RED, ann.GetColor())
	assert.True(t, ann.GetDashed())
}

// ГЕОМЕТРИЯ ЛОЖИТСЯ В КОЛОНКУ В SNAKE_CASE.
//
// МУТАЦИЯ: заменить designMarshalJSON на голый protojson.Marshal. Компилируется, круг чтения
// по-прежнему сходится (protojson принимает ОБА написания на чтении) — и ровно поэтому дефект
// молчалив: колонка станет `labelX`, а любой SQL-путь по ней вернёт пустоту навсегда, как это уже
// случилось бы с `$.colour` в design_run.params.
func TestSetDesignAssetPlacementWritesProtoNames(t *testing.T) {
	rig := newDesignAssetRig(t)
	rig.expectPlacement(&entity.DesignAssetPlacement{Id: 8, AssetId: 12, PictureId: 91}, nil)

	_, err := rig.srv.SetDesignAssetPlacement(designRunCtx(), &pb_admin.SetDesignAssetPlacementRequest{
		TechCardId: designRunCardID,
		AssetId:    12,
		PictureId:  91,
		Annotation: designSampleAnnotation(),
	})
	require.NoError(t, err)
	require.NotNil(t, rig.sentPlacement)

	var obj map[string]any
	require.NoError(t, json.Unmarshal(rig.sentPlacement.Annotation, &obj))
	assert.Contains(t, obj, "label_x", "колонки полосы пишутся с UseProtoNames: true")
	assert.NotContains(t, obj, "labelX")
}

// ФИГУРУ, КОТОРУЮ ОТВЕРГЛА БЫ КАРТОЧКА, ОТВЕРГАЕТ И МЕТКА АССЕТА — И СТОР ЕЁ НЕ ВИДИТ.
//
// ЭТО ПРОБА ПЕРЕИСПОЛЬЗОВАНИЯ, а не проба правил: правила принадлежат ОДНОМУ своду указаний
// (dto.TechCardAnnotationFromPb). Она краснеет ровно тогда, когда кто-нибудь заведёт здесь второй,
// более мягкий валидатор — а именно так однажды и обошли защиту от показателя степени: копия,
// у которой её не было, сделала сторожа своей же уязвимостью.
//
// МУТАЦИЯ: убрать вызов dto.TechCardAnnotationFromPb из designAssetAnnotationJSON — все четыре
// случая станут OK вместо InvalidArgument, и стор получит фигуру, которую нечем нарисовать.
func TestSetDesignAssetPlacementRefusesShapesTheCardRefuses(t *testing.T) {
	dim := func(mut func(*pb_common.TechCardAnnotation)) *pb_common.TechCardAnnotation {
		a := designSampleAnnotation()
		mut(a)
		return a
	}
	for _, tc := range []struct {
		name string
		ann  *pb_common.TechCardAnnotation
	}{
		{"фигуры нет вовсе", nil},
		{"вид не назван", dim(func(a *pb_common.TechCardAnnotation) {
			a.Kind = pb_common.TechCardAnnotationKind_TECH_CARD_ANNOTATION_KIND_UNKNOWN
		})},
		{"у мерки одна точка вместо двух", dim(func(a *pb_common.TechCardAnnotation) {
			a.Points = a.Points[:1]
		})},
		{"координата вне кадра", dim(func(a *pb_common.TechCardAnnotation) {
			a.Points[0].X = &pb_decimal.Decimal{Value: "1.4"}
		})},
		{"координата показателем степени — тот самый рескейл, ради которого предел и заводился",
			dim(func(a *pb_common.TechCardAnnotation) {
				a.Points[0].Y = &pb_decimal.Decimal{Value: "1E-10000000"}
			})},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rig := newDesignAssetRig(t)
			// Стор замокан с Maybe(): проба обязана показать, что до него не дошло, а не что он
			// отказал сам.
			rig.expectPlacement(&entity.DesignAssetPlacement{Id: 1}, nil)

			_, err := rig.srv.SetDesignAssetPlacement(designRunCtx(), &pb_admin.SetDesignAssetPlacementRequest{
				TechCardId: designRunCardID,
				AssetId:    12,
				PictureId:  91,
				Annotation: tc.ann,
			})
			require.Error(t, err)
			code, _ := errorReason(t, err)
			assert.Equal(t, codes.InvalidArgument, code)
			assert.Nil(t, rig.sentPlacement, "фигура, которую нечем нарисовать, до стора не доезжает")
		})
	}
}

// ЧЕТЫРЕ ОТКАЗА ПОЛКИ ДОЕЗЖАЮТ ДО КЛИЕНТА САМИМИ СОБОЙ.
//
// ЧТО ЛОВИТСЯ: sentinel, которого нет в таблице designRefusals, уходит на провод как
// codes.Internal «failed to …» ПЛЮС строка ERROR в лог — то есть ожидаемое состояние («полка
// полна», «выберите полку») человек читает как поломку сервера, а машинного токена, который
// комментарий у самого sentinel-а обещает, не появляется вовсе.
//
// МУТАЦИЯ: убрать любую из четырёх строк {entity.ErrDesignAsset…} из designRefusals — ровно один
// подслучай краснеет и становится Internal без reason.
func TestDesignAssetRefusalsReachTheClientAsThemselves(t *testing.T) {
	for _, tc := range []struct {
		name   string
		err    error
		code   codes.Code
		reason string
	}{
		{"неизвестная полка", entity.ErrDesignAssetKindUnknown, codes.InvalidArgument, "asset_kind_unknown"},
		{"имя обязательно", entity.ErrDesignAssetNameRequired, codes.InvalidArgument, "asset_name_required"},
		{"полки полны", entity.ErrDesignAssetTooMany, codes.FailedPrecondition, "asset_too_many"},
		{"раппорт не на той полке", entity.ErrDesignAssetNotAPattern, codes.FailedPrecondition, "asset_not_a_pattern"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rig := newDesignAssetRig(t)
			rig.expectUpsert(nil, tc.err)

			_, err := rig.srv.UpsertDesignAsset(designRunCtx(), &pb_admin.UpsertDesignAssetRequest{
				TechCardId: designRunCardID,
				Kind:       entity.DesignAssetKindFabric,
				Name:       "main jersey",
			})
			require.Error(t, err)
			code, md := errorReason(t, err)
			assert.Equal(t, tc.code, code)
			require.NotNil(t, md, "отказ полки обязан нести машинную причину, а не одну прозу")
			assert.Equal(t, tc.reason, md["reason"])
		})
	}
}

// ЗАПРОС АПСЕРТА ДОЕЗЖАЕТ ДО СТОРА ЦЕЛИКОМ.
//
// МУТАЦИЯ: выбросить `RepeatMm` (или `DerivedFromAssetId`, или `Ordinal`) из сборки
// entity.DesignAssetUpsert в UpsertDesignAsset. Компилируется; ответ OK; паттерн просто теряет
// раппорт по дороге, и человек видит сохранённую плитку с нулём там, где он написал 120.
func TestUpsertDesignAssetCarriesTheWholeTileToTheStore(t *testing.T) {
	rig := newDesignAssetRig(t)
	saved := designSampleAsset()
	rig.expectUpsert(&saved, nil)

	resp, err := rig.srv.UpsertDesignAsset(designRunCtx(), &pb_admin.UpsertDesignAssetRequest{
		TechCardId:         designRunCardID,
		AssetId:            12,
		Kind:               entity.DesignAssetKindPattern,
		Name:               "diagonal stripe",
		MediaId:            77,
		ColourCode:         "PANTONE 19-4052",
		ColourHex:          "#0F4C81",
		Note:               "brushed, slight sheen",
		DerivedFromAssetId: 5,
		RepeatMm:           120,
		RotationDeg:        45,
		Ordinal:            3,
	})
	require.NoError(t, err)
	require.NotNil(t, resp.GetAsset())

	sent := rig.sentUpsert
	require.NotNil(t, sent)
	assert.Equal(t, designRunCardID, sent.TechCardId)
	assert.Equal(t, 12, sent.AssetId)
	assert.Equal(t, entity.DesignAssetKindPattern, sent.Kind)
	assert.Equal(t, "diagonal stripe", sent.Name)
	assert.Equal(t, 77, sent.MediaId)
	assert.Equal(t, "PANTONE 19-4052", sent.ColourCode)
	assert.Equal(t, "#0F4C81", sent.ColourHex)
	assert.Equal(t, "brushed, slight sheen", sent.Note)
	assert.Equal(t, 5, sent.DerivedFromAssetId)
	assert.Equal(t, 120, sent.RepeatMm)
	assert.Equal(t, 45, sent.RotationDeg)
	assert.Equal(t, 3, sent.Ordinal)
	assert.Equal(t, "designer", sent.Actor, "без имени два автора на одной строке неразличимы")
}

// ПОЛОСА ВЕЗЁТ ПОЛКИ И ИХ РАЗМЕТКУ.
//
// МУТАЦИЯ, РАДИ КОТОРОЙ ПРОБА И НАПИСАНА: убрать из GetDesignBand строки
// `Assets: designAssetsToPb(...)` и `AssetPlacements: designAssetPlacementsToPb(...)`.
// Компилируется, полоса отвечает 200 со всем остальным на месте — и стена полок пуста, а метки на
// флэтах исчезли. Ни один статус этого не показывает.
func TestGetDesignBandCarriesTheShelvesAndTheirMarks(t *testing.T) {
	raw, err := designMarshalJSON(designSampleAnnotation())
	require.NoError(t, err)

	rig := newDesignAssetRig(t)
	rig.design.EXPECT().GetBand(mock.Anything, designRunCardID, mock.AnythingOfType("int")).
		Return(&entity.DesignBand{
			Assets: []entity.DesignAsset{designSampleAsset()},
			AssetPlacements: []entity.DesignAssetPlacement{{
				Id: 8, AssetId: 12, PictureId: 91,
				Annotation: entity.RawJSON(raw), SetBy: "designer",
				SetAt: time.Date(2026, 8, 30, 12, 0, 0, 0, time.UTC),
			}},
		}, nil).Once()

	resp, err := rig.srv.GetDesignBand(designRunCtx(), &pb_admin.GetDesignBandRequest{
		TechCardId: designRunCardID,
	})
	require.NoError(t, err)

	require.Len(t, resp.GetAssets(), 1, "стена полок едет ЭТИМ чтением, а не вторым")
	assert.Equal(t, int32(12), resp.GetAssets()[0].GetId())
	assert.Equal(t, int32(120), resp.GetAssets()[0].GetRepeatMm())
	require.NotNil(t, resp.GetAssets()[0].GetMedia())

	require.Len(t, resp.GetAssetPlacements(), 1, "метки едут РЯДОМ с полками, а не вложенными в них")
	assert.Equal(t, int32(91), resp.GetAssetPlacements()[0].GetPictureId())
	require.NotNil(t, resp.GetAssetPlacements()[0].GetAnnotation())
	assert.Equal(t, pb_common.TechCardAnnotationKind_TECH_CARD_ANNOTATION_KIND_DIM,
		resp.GetAssetPlacements()[0].GetAnnotation().GetKind())
}

// ─────────────────────── СКОУП КАРТОЧКИ У ГЛАГОЛА УДАЛЕНИЯ ───────────────────────

// УДАЛЕНИЕ АССЕТА НАЗЫВАЕТ СТОРУ КАРТОЧКУ, А НЕ НОЛЬ.
//
// ЧТО БЫЛО. Хендлер звал `DeleteAsset(ctx, 0, id)`, а ноль в сторе ОТКЛЮЧАЕТ проверку
// принадлежности (requireAssetOfCard). Карточки в запросе не было вовсе, поэтому клиент,
// державший идентификатор ассета ДРУГОЙ карточки — устаревший список, вторая вкладка, карточка,
// переключённая под открытой панелью, — удалял чужую строку и КАСКАДОМ все её метки на чужих
// флэтах. Молча: ответ OK, число удалённых меток правдоподобное, и ни на одном из двух экранов
// нет ничего, по чему это заметить.
//
// ⚠ ПРОБА СМОТРИТ НА ТО, ЧТО ОТДАНО СТОРУ, А НЕ НА ОТВЕТ. Ответ здесь одинаков в обоих мирах —
// весь дефект в аргументе, которого не видно снаружи.
func TestDeleteDesignAssetNamesTheCardItDeletesFrom(t *testing.T) {
	rig := newDesignAssetRig(t)
	var gotCard, gotAsset int
	rig.design.EXPECT().DeleteAsset(mock.Anything, mock.AnythingOfType("int"), mock.AnythingOfType("int")).
		Run(func(_ context.Context, card, asset int) { gotCard, gotAsset = card, asset }).
		Return(3, nil).Once()

	resp, err := rig.srv.DeleteDesignAsset(designRunCtx(), &pb_admin.DeleteDesignAssetRequest{
		TechCardId: designRunCardID,
		AssetId:    12,
	})
	require.NoError(t, err)
	assert.Equal(t, int32(3), resp.GetRemovedPlacements())
	assert.Equal(t, 12, gotAsset)
	assert.Equal(t, designRunCardID, gotCard,
		"ноль вместо карточки снимает единственную проверку, отделяющую эту полку от чужой")
}

// ТО ЖЕ ДЛЯ МЕТКИ НА ФЛЭТЕ.
//
// У размещения своего tech_card_id нет по решению 0354, и скоуп до него дотягивается ТОЛЬКО через
// JOIN на ассет — то есть ровно через карточку, которую хендлер и обязан назвать. Ноль отменял и
// этот JOIN.
func TestDeleteDesignAssetPlacementNamesTheCardItUnmarks(t *testing.T) {
	rig := newDesignAssetRig(t)
	var gotCard, gotPlacement int
	rig.design.EXPECT().DeleteAssetPlacement(mock.Anything, mock.AnythingOfType("int"), mock.AnythingOfType("int")).
		Run(func(_ context.Context, card, placement int) { gotCard, gotPlacement = card, placement }).
		Return(nil).Once()

	_, err := rig.srv.DeleteDesignAssetPlacement(designRunCtx(), &pb_admin.DeleteDesignAssetPlacementRequest{
		TechCardId:  designRunCardID,
		PlacementId: 8,
	})
	require.NoError(t, err)
	assert.Equal(t, 8, gotPlacement)
	assert.Equal(t, designRunCardID, gotCard,
		"без карточки метка адресуется по одному номеру, и JOIN, который и есть скоуп, не строится")
}
