package admin

import (
	"context"
	"database/sql"
	"testing"

	"github.com/jekabolt/grbpwr-manager/internal/dependency/mocks"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	pb_admin "github.com/jekabolt/grbpwr-manager/proto/gen/admin"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
)

// ПРОБЫ ВТОРОЙ ОСИ ВЕРСТАКА НА ПРОВОДЕ.
//
// ОБЩИЙ КЛАСС ДЕФЕКТА, КОТОРЫЙ ОНИ СТЕРЕГУТ: поле контракта существует, стор его ждёт, а хендлер
// выбрасывает его на пол. Ни один round trip такое не показывает — ответ приходит с кодом OK,
// строка появляется, и только род у неё не тот, о котором просили. Поэтому пробы смотрят НЕ на
// ответ, а на то, ЧТО ХЕНДЛЕР ОТДАЛ СТОРУ.

// designUploadRig — сервер, у которого замокан ровно один глагол: RegisterBatch. Взятое им
// значение и есть предмет пробы.
type designUploadRig struct {
	srv    *Server
	design *mocks.MockDesign
	sent   *entity.DesignBatchRegister
}

func newDesignUploadRig(t *testing.T) *designUploadRig {
	t.Helper()
	rig := &designUploadRig{design: mocks.NewMockDesign(t)}
	repo := mocks.NewMockRepository(t)
	repo.EXPECT().Design().Return(rig.design).Maybe()
	rig.design.EXPECT().RegisterBatch(mock.Anything, mock.AnythingOfType("entity.DesignBatchRegister")).
		Run(func(_ context.Context, req entity.DesignBatchRegister) {
			cp := req
			rig.sent = &cp
		}).
		Return(&entity.DesignBatchResult{
			Batch: entity.DesignBatch{Id: 7, TechCardId: designRunCardID},
		}, nil).Maybe()
	rig.srv = &Server{repo: repo}
	return rig
}

// ─────────────────────── E: род загружаемого файла ───────────────────────

// РОД ЗАГРУЗКИ ДОЕЗЖАЕТ ДО СТОРА.
//
// МУТАЦИЯ, КОТОРУЮ ОНА ЛОВИТ: убрать `Kind: it.GetKind()` из сборки entity.DesignUploadItem —
// ровно то состояние, в котором файл и был. Род тогда читается как flat (DesignKindOrFlat), и
// цветной рендер, принесённый руками, ложится в полосу флэтом: гейт `pic.Kind != kind` пускает его
// во флэт-слот, минт печатает его на ТЕХНИЧЕСКОМ ЛИСТЕ, а счётчик W-13 его не считает — дверь 3D
// нарисована закрытой при живом рендере на карточке.
func TestRegisterDesignUploadCarriesTheKindToTheStore(t *testing.T) {
	rig := newDesignUploadRig(t)
	_, err := rig.srv.RegisterDesignUpload(designRunCtx(), &pb_admin.RegisterDesignUploadRequest{
		TechCardId:      designRunCardID,
		ClientRequestId: "33333333-3333-3333-3333-333333333333",
		Items: []*pb_admin.DesignUploadItem{
			{MediaId: 501, Kind: entity.DesignPictureKindRender, GhostView: entity.DesignViewFront},
			{MediaId: 502, Kind: entity.DesignPictureKindThreed},
			{MediaId: 503},
		},
	})
	require.NoError(t, err)
	require.NotNil(t, rig.sent)
	require.Len(t, rig.sent.Items, 3)
	require.Equal(t, entity.DesignPictureKindRender, rig.sent.Items[0].Kind,
		"род объявлен на проводе УТВЕРЖДЕНИЕМ загружающего, и восстановить его из пикселей нечем")
	require.Equal(t, entity.DesignPictureKindThreed, rig.sent.Items[1].Kind)
	require.Empty(t, rig.sent.Items[2].Kind,
		"неназванный род остаётся пустым: пустое читается как flat ОДНИМ правилом на всех ярусах")
}

// НЕИЗВЕСТНЫЙ РОД — ОТКАЗ У ДВЕРИ, а не 1265 из-под колонки и не молчаливый flat.
func TestRegisterDesignUploadRefusesAnUnknownKind(t *testing.T) {
	rig := newDesignUploadRig(t)
	_, err := rig.srv.RegisterDesignUpload(designRunCtx(), &pb_admin.RegisterDesignUploadRequest{
		TechCardId:      designRunCardID,
		ClientRequestId: "33333333-3333-3333-3333-333333333333",
		Items:           []*pb_admin.DesignUploadItem{{MediaId: 501, Kind: "sketch"}},
	})
	require.Error(t, err)
	code, _ := errorReason(t, err)
	require.Equal(t, codes.InvalidArgument, code)
	require.Nil(t, rig.sent, "отказ у двери не доходит до стора")
}

// TestRegisterDesignUploadRefusesTheGeneratedONLY_KINDS.
//
// ⚠ ЭТО ПРОВЕНАНС, А НЕ ОПРЯТНОСТЬ СЛОВАРЯ. `freeform` и `cutout` объявлены родами ВЫХОДА: их
// шапки прямо говорят, что таким кадром нельзя стать загрузкой руками, и на этом стоит предикат
// выходов полосы. Ручная загрузка объявляет род САМА — значит непрозрачный JPEG, принесённый с
// `kind: "cutout"`, вставал в ленту кадром выреза, с провенансом «это вырезано» и без единого
// прохода проверки альфы, которая заведена ровно затем, чтобы такую картинку не принять даже от
// поставщика. `freeform` тем же движением приписывал файлу прогон, которого не было.
//
// ОТКАЗ СТОИТ У ОБЕИХ ДВЕРЕЙ — здесь и в сторе (RegisterBatch), — и они спрашивают ОДИН предикат:
// дверь называет элемент пачки, стор остаётся последним замком.
func TestRegisterDesignUploadRefusesTheGeneratedONLY_KINDS(t *testing.T) {
	for _, kind := range []string{entity.DesignPictureKindFreeform, entity.DesignPictureKindCutout} {
		t.Run(kind, func(t *testing.T) {
			rig := newDesignUploadRig(t)
			_, err := rig.srv.RegisterDesignUpload(designRunCtx(), &pb_admin.RegisterDesignUploadRequest{
				TechCardId:      designRunCardID,
				ClientRequestId: "55555555-5555-5555-5555-555555555555",
				Items:           []*pb_admin.DesignUploadItem{{MediaId: 501, Kind: kind}},
			})
			require.Error(t, err)
			code, _ := errorReason(t, err)
			require.Equal(t, codes.InvalidArgument, code)
			require.Nil(t, rig.sent, "отказ у двери не доходит до стора")
			require.Contains(t, err.Error(), "only as the OUTPUT of a run",
				"человеку надо сказать не «неизвестный род», а почему именно этот род не его")

			// ⚠ И ОН ПО-ПРЕЖНЕМУ РОД КАДРА — просто не род ЗАГРУЗКИ. Выход прогона пишет его сам
			// (store/design/queue.go), и запретить его там значило бы сломать оба маршрута.
			require.True(t, entity.IsDesignPictureKind(kind))
			require.False(t, entity.IsDesignUploadKind(kind))
		})
	}

	// ЧЕТЫРЕ ЗАКОННЫХ РОДА ЗАГРУЗКИ ПРОХОДЯТ КАК ПРЕЖДЕ — без этой половины проба была бы зелена и
	// у предиката, отказывающего всему.
	for _, kind := range []string{
		entity.DesignPictureKindFlat, entity.DesignPictureKindRender,
		entity.DesignPictureKindThreed, entity.DesignPictureKindPattern,
	} {
		require.Truef(t, entity.IsDesignUploadKind(kind), "kind %q is uploadable by hand", kind)
	}
	require.False(t, entity.IsDesignUploadKind(""),
		"пустое значит «род не назван» и читается как flat ОДНИМ правилом (DesignKindOrFlat); "+
			"пускать его сюда значило бы завести второе написание того же")
}

// ─────────────────────── F: род адреса верстака ───────────────────────

// РОД АДРЕСА ДОЕЗЖАЕТ ДО СТОРА, И БЕЗ НЕГО ДВУХОСНОГО ВЕРСТАКА НЕТ ВОВСЕ.
//
// МУТАЦИЯ: убрать `Kind: ref.GetKind()` из ветки view_key. Тогда всякий адрес приезжает в стор с
// пустым родом, то есть flat; SetDesignBenchSlot с родом render молча спрашивает про флэт-слот,
// гейт отказывает любому рендеру в любом слоте, и цепочка «флэты → рендер в слот → 3D»
// непроходима — designInputSlots для 3D ищет слоты рода render, а завести их не может никто.
func TestDesignSlotRefCarriesTheBenchKind(t *testing.T) {
	ref, err := designSlotRefFromPb(&pb_admin.DesignBenchSlotRef{
		Slot: &pb_admin.DesignBenchSlotRef_ViewKey{ViewKey: entity.DesignViewFront},
		Kind: entity.DesignPictureKindRender,
	})
	require.NoError(t, err)
	require.Equal(t, entity.DesignViewFront, ref.ViewKey)
	require.Equal(t, entity.DesignPictureKindRender, ref.Kind,
		"рендер фронта и флэт фронта — ДВА РАЗНЫХ СЛОТА одного вида; без рода адрес называет один")
}

// РОД ПРИ АДРЕСАЦИИ ПО slot_id НЕ ПЕРЕНОСИТСЯ — это слово контракта, а не упущение: минтованный id
// уже назвал свой верстак, и противоречащий ему род некому рассудить. Противоречие, которого нет в
// структуре, послать нельзя.
func TestDesignSlotRefIgnoresTheKindWhenAddressedById(t *testing.T) {
	ref, err := designSlotRefFromPb(&pb_admin.DesignBenchSlotRef{
		Slot: &pb_admin.DesignBenchSlotRef_SlotId{SlotId: 12},
		Kind: entity.DesignPictureKindRender,
	})
	require.NoError(t, err)
	require.Equal(t, 12, ref.SlotId)
	require.Empty(t, ref.Kind,
		"по id род игнорируется контрактом; перенесённый сюда, он стал бы вторым мнением о верстаке строки")
}

func TestDesignSlotRefRefusesAnUnknownKind(t *testing.T) {
	_, err := designSlotRefFromPb(&pb_admin.DesignBenchSlotRef{
		Slot: &pb_admin.DesignBenchSlotRef_ViewKey{ViewKey: entity.DesignViewFront},
		Kind: "moodboard",
	})
	require.Error(t, err)
	code, _ := errorReason(t, err)
	require.Equal(t, codes.InvalidArgument, code)
}

// ТОТ ЖЕ РОД ЧЕРЕЗ ЖИВОЙ ХЕНДЛЕР: проба выше проверяет перевод, эта — что перевод действительно
// стоит на пути SetDesignBenchSlot, а не только в функции, которую никто не зовёт.
func TestSetDesignBenchSlotCarriesTheKindToTheStore(t *testing.T) {
	design := mocks.NewMockDesign(t)
	repo := mocks.NewMockRepository(t)
	repo.EXPECT().Design().Return(design).Maybe()
	var sent *entity.DesignBenchSlotSet
	design.EXPECT().SetBenchSlot(mock.Anything, mock.AnythingOfType("entity.DesignBenchSlotSet")).
		Run(func(_ context.Context, req entity.DesignBenchSlotSet) {
			cp := req
			sent = &cp
		}).
		Return(&entity.DesignBenchSlot{
			Id: 5, TechCardId: designRunCardID, ViewKey: entity.DesignViewFront,
			Kind: entity.DesignPictureKindRender, PictureId: sql.NullInt32{Int32: 77, Valid: true},
		}, nil).Once()
	srv := &Server{repo: repo}

	_, err := srv.SetDesignBenchSlot(designRunCtx(), &pb_admin.SetDesignBenchSlotRequest{
		TechCardId: designRunCardID,
		Slot: &pb_admin.DesignBenchSlotRef{
			Slot: &pb_admin.DesignBenchSlotRef_ViewKey{ViewKey: entity.DesignViewFront},
			Kind: entity.DesignPictureKindRender,
		},
		PictureId: 77,
	})
	require.NoError(t, err)
	require.NotNil(t, sent)
	require.Equal(t, entity.DesignPictureKindRender, sent.Slot.Kind)
}

// РОД ЦЕЛИ ЗАГРУЗКИ — ТОТ ЖЕ ПУТЬ, ТРЕТИЙ CALL-SITE. Он проверяется отдельно, потому что
// designSlotRefFromPb зовут три разных хендлера, и «работает у одного» ничего не говорит о двух
// других.
func TestRegisterDesignUploadCarriesTheTargetKind(t *testing.T) {
	rig := newDesignUploadRig(t)
	_, err := rig.srv.RegisterDesignUpload(designRunCtx(), &pb_admin.RegisterDesignUploadRequest{
		TechCardId:      designRunCardID,
		ClientRequestId: "33333333-3333-3333-3333-333333333333",
		Items:           []*pb_admin.DesignUploadItem{{MediaId: 501, Kind: entity.DesignPictureKindRender}},
		Target: &pb_admin.DesignBenchSlotRef{
			Slot: &pb_admin.DesignBenchSlotRef_ViewKey{ViewKey: entity.DesignViewFront},
			Kind: entity.DesignPictureKindRender,
		},
	})
	require.NoError(t, err)
	require.NotNil(t, rig.sent)
	require.NotNil(t, rig.sent.Target)
	require.Equal(t, entity.DesignPictureKindRender, rig.sent.Target.Kind)
}

// T15: ИМЯ ДЕТАЛИ, МИНТУЕМОЙ ЗАГРУЗКОЙ, ДОЕЗЖАЕТ ДО СТОРА.
//
// МУТАЦИЯ, КОТОРУЮ ОНА ЛОВИТ: убрать `NewDetailName` из сборки entity.DesignBatchRegister — ровно то
// состояние, в котором хендлер и был. Тогда `target` с view_key = detail отказывал
// detail_name_required на каждой загрузке-с-минтом, хотя имя лежало в запросе. Пустое имя остаётся
// пустым: клиент, который грузит и минтует двумя записями, его не шлёт.
func TestRegisterDesignUploadCarriesTheNewDetailNameToTheStore(t *testing.T) {
	rig := newDesignUploadRig(t)
	_, err := rig.srv.RegisterDesignUpload(designRunCtx(), &pb_admin.RegisterDesignUploadRequest{
		TechCardId:      designRunCardID,
		ClientRequestId: "44444444-4444-4444-4444-444444444444",
		Items:           []*pb_admin.DesignUploadItem{{MediaId: 501}},
		Target: &pb_admin.DesignBenchSlotRef{
			Slot: &pb_admin.DesignBenchSlotRef_ViewKey{ViewKey: entity.DesignViewDetail},
		},
		NewDetailName: "  pocket flap  ",
	})
	require.NoError(t, err)
	require.NotNil(t, rig.sent)
	require.NotNil(t, rig.sent.Target)
	require.Equal(t, entity.DesignViewDetail, rig.sent.Target.ViewKey)
	require.Equal(t, "pocket flap", rig.sent.NewDetailName, "trimmed exactly as in SetDesignBenchSlot")

	rig = newDesignUploadRig(t)
	_, err = rig.srv.RegisterDesignUpload(designRunCtx(), &pb_admin.RegisterDesignUploadRequest{
		TechCardId:      designRunCardID,
		ClientRequestId: "55555555-5555-5555-5555-555555555555",
		Items:           []*pb_admin.DesignUploadItem{{MediaId: 502}},
	})
	require.NoError(t, err)
	require.Empty(t, rig.sent.NewDetailName)
}
