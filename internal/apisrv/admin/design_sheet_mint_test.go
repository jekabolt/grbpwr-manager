package admin

import (
	"context"
	"database/sql"
	"os"
	"strings"
	"testing"

	authsrv "github.com/jekabolt/grbpwr-manager/internal/apisrv/auth"
	"github.com/jekabolt/grbpwr-manager/internal/dependency/mocks"
	"github.com/jekabolt/grbpwr-manager/internal/dto"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	pb_admin "github.com/jekabolt/grbpwr-manager/proto/gen/admin"
	pb_common "github.com/jekabolt/grbpwr-manager/proto/gen/common"
	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
	"google.golang.org/genproto/googleapis/rpc/errdetails"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// ПРОБЫ АТОМАРНОГО МИНТА, ХЕНДЛЕРНАЯ ПОЛОВИНА.
//
// ЧТО ЭТИ ПРОБЫ МОГУТ ДОКАЗАТЬ, А ЧТО НЕТ, названо вслух, потому что зелень без этой границы
// читается шире, чем она есть. Стор здесь замокан, значит SQL-половина минта (UNIQUE на
// client_request_id, MAX+1 на номер версии, откат транзакции) ими НЕ проверяется — она проверяется
// пробами внутри internal/store/design и, в конечном счёте, живой базой. Проверяется здесь то, что
// живёт в хендлере и ломается молча: КОНВЕЙЕР (П-Д), ВКЛАДКА ПЛИТ (П-А) и перевод отказов.

const mintCardID = 7

// mintRig — сервер с замоканным репозиторием и перехватом того, что уехало в стор.
type mintRig struct {
	srv    *Server
	repo   *mocks.MockRepository
	cards  *mocks.MockTechCards
	design *mocks.MockDesign
	// sent — запрос минта, каким его увидел стор. Копируется поверхностно: интересен именно
	// указатель на документ, потому что весь конвейер писал в него.
	sent *entity.DesignSheetMint
}

// mintCtx — полный доступ плюс имя автора: хендлеры отказывают ЗАКРЫТО на контексте без
// авторизации, поэтому проба обязана сказать это явно.
func mintCtx() context.Context {
	return authsrv.PutAdminUsername(fullAccessCtx(), "designer")
}

// newMintRig собирает стенд. storeErr — что вернёт стор: ErrTechCardConflict позволяет добраться
// до стора и остановиться ДО после-коммитной половины, не заводя мок на каждый её шаг (тот же
// приём, что у всех существующих проб UpdateTechCard).
func newMintRig(t *testing.T, stored *entity.TechCard, bench []entity.DesignBenchSlot,
	result *entity.DesignSheetVersionFull, storeErr error) *mintRig {
	t.Helper()
	rig := &mintRig{
		repo:   mocks.NewMockRepository(t),
		cards:  mocks.NewMockTechCards(t),
		design: mocks.NewMockDesign(t),
	}
	// Перевод отказов документной записи спрашивает у репозитория, 1062 это или 1452, — значит
	// эти двое участвуют в КАЖДОМ отказе минта, а не только в пробе про них.
	rig.repo.EXPECT().IsErrUniqueViolation(mock.Anything).Return(false).Maybe()
	rig.repo.EXPECT().IsErrForeignKeyViolation(mock.Anything).Return(false).Maybe()
	rig.repo.EXPECT().TechCards().Return(rig.cards).Maybe()
	rig.repo.EXPECT().Design().Return(rig.design).Maybe()
	rig.cards.EXPECT().GetTechCardByIdConsistent(mock.Anything, mintCardID).Return(stored, nil).Maybe()
	rig.design.EXPECT().GetBand(mock.Anything, mintCardID, mock.Anything).
		Return(&entity.DesignBand{Bench: bench}, nil).Maybe()
	rig.design.EXPECT().MintSheetVersion(mock.Anything, mock.AnythingOfType("entity.DesignSheetMint")).
		Run(func(_ context.Context, req entity.DesignSheetMint) {
			cp := req
			rig.sent = &cp
		}).Return(result, storeErr).Maybe()
	rig.srv = &Server{repo: rig.repo}
	return rig
}

// mintPlateSlot — заполненный слот верстака: сторона, картинка и её медиа.
func mintPlateSlot(id int, view string, pictureID, mediaID int) entity.DesignBenchSlot {
	return entity.DesignBenchSlot{
		Id:         id,
		TechCardId: mintCardID,
		ViewKey:    view,
		PictureId:  sql.NullInt32{Int32: int32(pictureID), Valid: true},
		SlotRev:    1,
		Picture:    &entity.DesignPicture{Id: pictureID, TechCardId: mintCardID, MediaId: mediaID},
	}
}

// mintStoredCard — хранимая карточка в состоянии «черновик, выносок ещё не было».
func mintStoredCard() *entity.TechCard {
	return &entity.TechCard{TechCardInsert: entity.TechCardInsert{CalloutSeq: 0}}
}

func mintRequest(doc *pb_common.TechCardInsert) *pb_admin.MintDesignSheetVersionRequest {
	return &pb_admin.MintDesignSheetVersionRequest{
		TechCardId:          mintCardID,
		ClientRequestId:     "11111111-1111-1111-1111-111111111111",
		TechCard:            doc,
		ExpectedLockVersion: 3,
		MintedVia:           entity.DesignMintedViaCallout,
	}
}

// mintDoc — минимальный законный документ карточки.
func mintDoc() *pb_common.TechCardInsert {
	return &pb_common.TechCardInsert{
		StyleNumber:     "TC-MINT",
		Name:            "mint subject",
		Stage:           pb_common.TechCardStage_TECH_CARD_STAGE_IDEA,
		MeasurementUnit: pb_common.TechCardMeasurementUnit_TECH_CARD_MEASUREMENT_UNIT_MM,
	}
}

// errorReason достаёт машинную причину отказа. Именно на неё ветвится клиент — проза меняется,
// причина нет.
func errorReason(t *testing.T, err error) (codes.Code, map[string]string) {
	t.Helper()
	st, ok := status.FromError(err)
	require.True(t, ok, "отказ обязан быть grpc-статусом, иначе клиенту нечего разбирать")
	for _, d := range st.Details() {
		if info, ok := d.(*errdetails.ErrorInfo); ok {
			md := map[string]string{"reason": info.GetReason()}
			for k, v := range info.GetMetadata() {
				md[k] = v
			}
			return st.Code(), md
		}
	}
	return st.Code(), nil
}

// ─────────────────────── 1. ИДЕМПОТЕНТНОСТЬ ───────────────────────

// ПОВТОР ВОЗВРАЩАЕТ ТУ ЖЕ ВЕРСИЮ И НЕ ДЕЛАЕТ ПОСЛЕ-КОММИТНУЮ РАБОТУ ВТОРОЙ РАЗ.
//
// Потерянный ответ и нажатая дважды кнопка неразличимы, и оба обязаны дать ОДНУ версию. Хендлерная
// половина этого обещания — две вещи: отдать номер, который назвал стор (а не сочинить следующий),
// и НЕ звать finalizeTechCardWrite, потому что ничего не записано. Второе проверяется строгим
// моком: любой незаявленный вызов репозитория роняет пробу, так что «после-коммитной работы не
// было» здесь — измеренный факт, а не отсутствие проверки.
func TestMintIdempotentRepeatReturnsTheSameVersionAndSkipsPostCommit(t *testing.T) {
	existing := &entity.DesignSheetVersionFull{
		Version:    entity.DesignSheetVersion{Id: 42, TechCardId: mintCardID, VersionNumber: 1},
		Idempotent: true,
		// Непустой список — ловушка: позвав finalize, хендлер пошёл бы удалять объекты выкроек,
		// которых эта запись не осиротила, потому что записи не было.
		OrphanedPatternURLs: []string{"patterns/one.dxf"},
	}
	rig := newMintRig(t, mintStoredCard(), nil, existing, nil)

	resp, err := rig.srv.MintDesignSheetVersion(mintCtx(), mintRequest(mintDoc()))
	require.NoError(t, err)
	require.NotNil(t, resp.GetVersion())
	require.EqualValues(t, 1, resp.GetVersion().GetVersionNumber(),
		"повтор обязан вернуть УЖЕ созданную версию, а не фантомную vN+1")
}

// КЛЮЧ ЗАПРОСА ДОЕЗЖАЕТ ДО СТОРА ДОСЛОВНО. Без него идемпотентности не существует вовсе, а
// потерять его можно молча: пустая строка выглядит как обычный запрос.
func TestMintCarriesTheClientRequestIdToTheStore(t *testing.T) {
	rig := newMintRig(t, mintStoredCard(), nil, nil, entity.ErrTechCardConflict)
	_, err := rig.srv.MintDesignSheetVersion(mintCtx(), mintRequest(mintDoc()))
	require.Error(t, err)
	require.NotNil(t, rig.sent, "запрос не доехал до стора — проба ниже сторожила бы пустоту")
	require.Equal(t, "11111111-1111-1111-1111-111111111111", rig.sent.ClientRequestId)
}

// ПУСТОЙ КЛЮЧ ЗАПРОСА ОТВЕРГАЕТСЯ ДО ЛЮБОЙ РАБОТЫ.
func TestMintRefusesAnEmptyClientRequestId(t *testing.T) {
	rig := newMintRig(t, mintStoredCard(), nil, nil, nil)
	req := mintRequest(mintDoc())
	req.ClientRequestId = "   "
	_, err := rig.srv.MintDesignSheetVersion(mintCtx(), req)
	require.Equal(t, codes.InvalidArgument, status.Code(err))
}

// ─────────────────────── 2. ПРОТУХШАЯ ВЕРСИЯ БЛОКИРОВКИ ───────────────────────

// КОНФЛИКТ ДОКУМЕНТА ПЕРЕВОДИТСЯ В Aborted С МАШИННОЙ ПРИЧИНОЙ lock_version_mismatch.
//
// Код без причины клиенту недостаточен: Aborted он получает и на bench_moved, а откатывает их
// по-разному — один просит перечитать карточку, другой перечитать верстак.
func TestMintStaleLockVersionIsAbortedWithItsOwnReason(t *testing.T) {
	rig := newMintRig(t, mintStoredCard(), nil, nil, entity.ErrTechCardConflict)

	_, err := rig.srv.MintDesignSheetVersion(mintCtx(), mintRequest(mintDoc()))
	code, md := errorReason(t, err)
	require.Equal(t, codes.Aborted, code)
	require.Equal(t, "lock_version_mismatch", md["reason"])
}

// ОЖИДАЕМАЯ ВЕРСИЯ БЛОКИРОВКИ ДОЕЗЖАЕТ ДО СТОРА. Без этого CAS сравнивал бы ноль с чем угодно.
func TestMintCarriesTheExpectedLockVersion(t *testing.T) {
	rig := newMintRig(t, mintStoredCard(), nil, nil, entity.ErrTechCardConflict)
	_, err := rig.srv.MintDesignSheetVersion(mintCtx(), mintRequest(mintDoc()))
	require.Error(t, err)
	require.NotNil(t, rig.sent)
	require.Equal(t, 3, rig.sent.ExpectedLockVersion)
}

// ─────────────────────── 3. BENCH_MOVED ───────────────────────

// УЕХАВШИЙ ВЕРСТАК ПЕРЕВОДИТСЯ В Aborted:bench_moved И НАЗЫВАЕТ СЛОТ.
//
// «Верстак изменился» без имени слота — новость, а не действие: человек не знает, что перечитать
// и что он потеряет, согласившись.
func TestMintBenchMovedNamesTheSlot(t *testing.T) {
	refusal := &entity.DesignMintRefusal{
		Err:      entity.ErrDesignBenchMoved,
		Metadata: map[string]string{"slot": "front", "slot_rev": "4", "expected_slot_rev": "3"},
	}
	rig := newMintRig(t, mintStoredCard(), nil, nil, refusal)

	_, err := rig.srv.MintDesignSheetVersion(mintCtx(), mintRequest(mintDoc()))
	code, md := errorReason(t, err)
	require.Equal(t, codes.Aborted, code)
	require.Equal(t, "bench_moved", md["reason"])
	require.Equal(t, "front", md["slot"], "отказ обязан назвать слот, иначе он не действие")
	require.Equal(t, "4", md["slot_rev"])
}

// ОЖИДАЕМЫЕ ПЛИТЫ ДОЕЗЖАЮТ ДО СТОРА В ОБОИХ НАПИСАНИЯХ АДРЕСА — по виду и по id слота.
func TestMintCarriesExpectedPlatesBothWaysOfAddressing(t *testing.T) {
	rig := newMintRig(t, mintStoredCard(), nil, nil, entity.ErrTechCardConflict)
	req := mintRequest(mintDoc())
	req.ExpectedPlates = []*pb_admin.DesignExpectedPlate{
		{Slot: &pb_admin.DesignBenchSlotRef{Slot: &pb_admin.DesignBenchSlotRef_ViewKey{ViewKey: "front"}}, SlotRev: 3},
		{Slot: &pb_admin.DesignBenchSlotRef{Slot: &pb_admin.DesignBenchSlotRef_SlotId{SlotId: 91}}, SlotRev: 2},
	}
	_, err := rig.srv.MintDesignSheetVersion(mintCtx(), req)
	require.Error(t, err)
	require.NotNil(t, rig.sent)
	require.Len(t, rig.sent.ExpectedPlates, 2)
	require.Equal(t, "front", rig.sent.ExpectedPlates[0].Slot.ViewKey)
	require.Equal(t, 3, rig.sent.ExpectedPlates[0].SlotRev)
	require.Equal(t, 91, rig.sent.ExpectedPlates[1].Slot.SlotId)
	require.Equal(t, 2, rig.sent.ExpectedPlates[1].SlotRev)
}

// ─────────────────────── 4. ШОВ: НОМЕР ВЫНОСКИ И ПОДПИСЬ ───────────────────────

// МИНТ НА ДОКУМЕНТЕ С НОВОЙ ВЫНОСКОЙ: ЗАМОРАЖИВАЕМАЯ ВЫНОСКА НЕСЁТ НОМЕР ≥ 1, А ПОДПИСЬ DESIGN
// СЧИТАНА ПО СОХРАНЁННЫМ НОМЕРАМ.
//
// Это П-Д целиком, и это самый дорогой дефект волны. Первая выноска черновика — та самая, что
// рождает v1: если хендлер минта не унаследовал минт номеров, она замерзает в версии под нулём, а
// подпись DESIGN, поставленная этим же запросом, считается по документу с нулём, тогда как в
// колонку уезжает единица. Такая подпись РОЖДАЕТСЯ ПРОТУХШЕЙ и не лечится переутверждением:
// повторный штамп берёт то же расхождение.
//
// Проверяются ОБА факта, потому что они ломаются порознь: номер — что минт вообще был, дайджест —
// что он был ДО штампа.
func TestMintAssignsCalloutNumberBeforeStampingTheDesignDigest(t *testing.T) {
	doc := mintDoc()
	doc.TechnicalMedia = []*pb_common.TechCardMediaItem{
		{MediaId: 55, Kind: pb_common.TechCardMediaKind_TECH_CARD_MEDIA_KIND_FRONT},
	}
	doc.Callouts = []*pb_common.TechCardCallout{{
		// Ноль плюс ключ строки — ровно та комбинация, которая означает «сминти номер».
		Number:      0,
		ClientRef:   "cr-first-callout",
		MediaId:     55,
		Part:        "collar",
		Description: "first callout of the draft",
	}}
	doc.Signoffs = []*pb_common.TechCardSignoff{{
		Section: pb_common.TechCardSignoffSection_TECH_CARD_SIGNOFF_SECTION_DESIGN,
		State:   pb_common.TechCardSignoffState_TECH_CARD_SIGNOFF_STATE_APPROVED,
	}}
	rig := newMintRig(t, mintStoredCard(), nil, nil, entity.ErrTechCardConflict)

	_, err := rig.srv.MintDesignSheetVersion(mintCtx(), mintRequest(doc))
	require.Error(t, err, "стор отвечает конфликтом — значит запрос дошёл до него целиком")
	require.NotNil(t, rig.sent, "минт не доехал до стора: дальше проба сторожила бы пустоту")
	sent := rig.sent.TechCard
	require.NotNil(t, sent)
	require.Len(t, sent.Callouts, 1)
	require.GreaterOrEqual(t, sent.Callouts[0].Number, 1,
		"выноска замерзает в версии под номером 0: хендлер минта не унаследовал минт номеров")
	require.Equal(t, sent.Callouts[0].Number, sent.CalloutSeq,
		"счётчик карточки обязан уехать в стор вместе с присвоенным номером")

	// ПОДПИСЬ СЧИТАНА ПО ТОМУ, ЧТО УЕХАЛО В КОЛОНКУ. Пересчёт по сохранённому документу обязан
	// совпасть байт в байт; расхождение здесь и есть «подпись родилась протухшей».
	var design *entity.TechCardSignoff
	for i := range sent.Signoffs {
		if sent.Signoffs[i].Section == entity.SignoffDesign {
			design = &sent.Signoffs[i]
		}
	}
	require.NotNil(t, design, "секция DESIGN обязана быть среди подписей")
	require.True(t, design.SignedDigest.Valid, "свежее утверждение обязано нести отпечаток")
	recomputed := dto.TechCardSectionDigestsAsRead(sent, nil)
	require.Equal(t, recomputed[entity.SignoffDesign], design.SignedDigest.String,
		"отпечаток DESIGN не сходится с пересчётом по сохранённым номерам — подпись родилась протухшей")
}

// ─────────────────────── 5. П-А: ПЛИТЫ В ТЕХНИЧЕСКИХ МЕДИА ───────────────────────

// ПОСЛЕ МИНТА ДЕТАЛЬ КРОЯ НА ЛИСТОВОЙ ВЫНОСКЕ НЕ ОТОРВАНА.
//
// Механизм «деталь ↔ выноска» (entity.TechCardCalloutIndex) считает источником имени только
// выноску, запиненную на ТЕХНИЧЕСКОЕ медиа документа. Плиты верстака в tech_card_media новой
// карточки не лежат вовсе — значит без вкладки каждая деталь кроя стала бы detached, тех-пак
// напечатал бы пустой эскиз, а подпись DESIGN не видела бы чертежа.
//
// Проба СПРАШИВАЕТ ТОТ САМЫЙ МЕХАНИЗМ, а не наличие строки в списке: сравнение списков зеленело бы
// и на медиа, положенном в мудборд, то есть сторожило бы форму вместо смысла.
func TestMintKeepsThePieceOnASheetCalloutAttached(t *testing.T) {
	doc := mintDoc()
	doc.Callouts = []*pb_common.TechCardCallout{{
		Number:  4,
		MediaId: 55, // медиа плиты верстака, которого документ САМ не перечисляет
		Part:    "collar",
	}}
	bench := []entity.DesignBenchSlot{mintPlateSlot(1, entity.DesignViewFront, 900, 55)}
	rig := newMintRig(t, mintStoredCard(), bench, nil, entity.ErrTechCardConflict)

	_, err := rig.srv.MintDesignSheetVersion(mintCtx(), mintRequest(doc))
	require.Error(t, err)
	require.NotNil(t, rig.sent, "минт не доехал до стора")
	sent := rig.sent.TechCard
	require.NotNil(t, sent)

	piece := &entity.TechCardPiece{
		Name:          "collar",
		CalloutNumber: sql.NullInt32{Int32: 4, Valid: true},
		Detached:      false,
	}
	entity.NewTechCardCalloutIndex(sent.Media, sent.Callouts).ApplyToPiece(piece)
	require.False(t, piece.Detached,
		"деталь на листовой выноске объявлена оторванной: плиты верстака не попали в technical_media, "+
			"и тех-пак напечатает пустой эскиз")
}

// ПЛИТА ЛОЖИТСЯ ТЕХНИЧЕСКИМ МЕДИА С ВИДОМ, КОТОРЫЙ КОЛОНКА СЕГОДНЯ ЗНАЕТ.
//
// Боковой вид — это side_l, но словарь колонки tech_card_media.kind узнаёт его только после 0346.
// Пока флаг не флипнут, бок обязан лечь как DETAIL: он остаётся ТЕХНИЧЕСКИМ, а больше механизму
// связи ничего и не нужно. Без этого сохранение упало бы сырым 3819 с именем constraint.
func TestMintFilesSidePlatesUnderAKindTheColumnAccepts(t *testing.T) {
	require.False(t, entity.TechCardMediaKindDictExtended,
		"гейт снимают ВМЕСТЕ с выкаткой 0346 на ПРОД; если он снят, а миграция не уехала, отказ приедет из MySQL")
	bench := []entity.DesignBenchSlot{
		mintPlateSlot(1, entity.DesignViewFront, 900, 55),
		mintPlateSlot(2, entity.DesignViewSideL, 901, 56),
	}
	rig := newMintRig(t, mintStoredCard(), bench, nil, entity.ErrTechCardConflict)

	_, err := rig.srv.MintDesignSheetVersion(mintCtx(), mintRequest(mintDoc()))
	require.Error(t, err)
	require.NotNil(t, rig.sent)
	kinds := map[int]entity.TechCardMediaKind{}
	for _, m := range rig.sent.TechCard.Media {
		require.Equal(t, entity.TechCardMediaCategoryTechnical, m.Category,
			"плита обязана лечь в ТЕХНИЧЕСКИЙ список, иначе связь детали с выноской мертва")
		kinds[m.MediaId] = m.Kind
	}
	require.Equal(t, entity.TechCardMediaFront, kinds[55])
	require.Equal(t, entity.TechCardMediaDetail, kinds[56],
		"боковая плита обязана лечь как detail, пока 0346 не на проде — side_l колонка ещё не знает")
}

// ПЛИТА, КОТОРУЮ ДОКУМЕНТ УЖЕ ПЕРЕЧИСЛИЛ, НЕ УДВАИВАЕТСЯ.
//
// UNIQUE на (tech_card_id, media_id) в tech_card_media нет, поэтому дубль лёг бы второй строкой и
// эскиз удвоился бы на бумаге — молча.
func TestMintDoesNotDuplicateAPlateTheDocumentAlreadyLists(t *testing.T) {
	doc := mintDoc()
	doc.TechnicalMedia = []*pb_common.TechCardMediaItem{
		{MediaId: 55, Kind: pb_common.TechCardMediaKind_TECH_CARD_MEDIA_KIND_FRONT},
	}
	bench := []entity.DesignBenchSlot{mintPlateSlot(1, entity.DesignViewFront, 900, 55)}
	rig := newMintRig(t, mintStoredCard(), bench, nil, entity.ErrTechCardConflict)

	_, err := rig.srv.MintDesignSheetVersion(mintCtx(), mintRequest(doc))
	require.Error(t, err)
	require.NotNil(t, rig.sent)
	seen := 0
	for _, m := range rig.sent.TechCard.Media {
		if m.MediaId == 55 {
			seen++
		}
	}
	require.Equal(t, 1, seen, "плита перечислена дважды — эскиз удвоится на бумаге")
}

// ЗАПРОС НЕ МУТИРУЕТСЯ. Сервер дописывает плиты в КЛОН: перехватчики, логи и повтор gRPC видят
// исходное сообщение, и «откуда взялись две картинки» не превращается в поиск по чужой памяти.
func TestMintDoesNotMutateTheIncomingRequest(t *testing.T) {
	doc := mintDoc()
	bench := []entity.DesignBenchSlot{mintPlateSlot(1, entity.DesignViewFront, 900, 55)}
	rig := newMintRig(t, mintStoredCard(), bench, nil, entity.ErrTechCardConflict)

	req := mintRequest(doc)
	_, err := rig.srv.MintDesignSheetVersion(mintCtx(), req)
	require.Error(t, err)
	require.Empty(t, req.GetTechCard().GetTechnicalMedia(),
		"сервер дописал плиты прямо во входящее сообщение")
	require.NotEmpty(t, rig.sent.TechCard.Media, "…но в стор они уехать обязаны")
}

// ─────────────────────── 6. МУДБОРДНАЯ ВЫНОСКА НЕ ЕСТ НОМЕР ЛИСТА ───────────────────────

// ДВЕ ВЫНОСКИ БЕЗ НОМЕРА: ОДНА НА ПЛИТЕ ЛИСТА, ОДНА НА МУДБОРДНОЙ ПЛИТКЕ.
//
// Номер выноски — адрес НА ЛИСТЕ: на него ссылаются деталь кроя, операция, дефект и печатный
// тех-пак. Как только клиент начнёт слать client_ref и на мудбордных выносках (F-4 это требует,
// иначе они станут новыми легаси-нулями), заметка съела бы очередной номер листа — нумерация
// поехала бы дырами, а номер перестал бы означать «выноска N на эскизе».
//
// Проба идёт через ОБЫЧНЫЙ СЕЙВ: гейт живёт в общем конвейере, и минт наследует его оттуда же.
func TestSaveMintsTheSheetCalloutButNotTheMoodboardNote(t *testing.T) {
	repo := mocks.NewMockRepository(t)
	cards := mocks.NewMockTechCards(t)
	repo.EXPECT().TechCards().Return(cards).Maybe()
	cards.EXPECT().GetTechCardByIdConsistent(mock.Anything, mintCardID).Return(mintStoredCard(), nil)
	var written *entity.TechCardInsert
	cards.EXPECT().UpdateTechCardAndListOrphanedPatternURLs(mock.Anything, mintCardID,
		mock.AnythingOfType("*entity.TechCardInsert"), 3).
		Run(func(_ context.Context, _ int, tc *entity.TechCardInsert, _ int) { written = tc }).
		Return(nil, entity.ErrTechCardConflict)

	doc := mintDoc()
	doc.TechnicalMedia = []*pb_common.TechCardMediaItem{
		{MediaId: 55, Kind: pb_common.TechCardMediaKind_TECH_CARD_MEDIA_KIND_FRONT},
	}
	doc.MoodboardMedia = []*pb_common.TechCardMediaItem{
		{MediaId: 77, Kind: pb_common.TechCardMediaKind_TECH_CARD_MEDIA_KIND_MOODBOARD},
	}
	doc.Callouts = []*pb_common.TechCardCallout{
		{Number: 0, ClientRef: "cr-sheet", MediaId: 55, Part: "collar"},
		{Number: 0, ClientRef: "cr-mood", MediaId: 77, Description: "this reference, but darker"},
	}

	_, err := (&Server{repo: repo}).UpdateTechCard(mintCtx(), &pb_admin.UpdateTechCardRequest{
		Id: mintCardID, ExpectedLockVersion: 3, TechCard: doc,
	})
	require.Equal(t, codes.Aborted, status.Code(err), "запрос обязан дойти до стора целиком")
	require.NotNil(t, written)
	require.Len(t, written.Callouts, 2)

	byRef := map[string]entity.TechCardCallout{}
	for _, c := range written.Callouts {
		byRef[c.ClientRef.String] = c
	}
	require.GreaterOrEqual(t, byRef["cr-sheet"].Number, 1,
		"листовая выноска обязана получить номер")
	require.Equal(t, 0, byRef["cr-mood"].Number,
		"мудбордная заметка съела номер листа: нумерация листа поедет дырами, а номер перестанет "+
			"означать «выноска N на эскизе»")
	require.Equal(t, "cr-mood", byRef["cr-mood"].ClientRef.String,
		"её личность — ключ строки, и он обязан уцелеть: ноль без ключа это легаси-ноль, а это не он")
}

// ГРАНИЦА ПЕРЕД ОТПЕЧАТКОМ СОГЛАСОВАНА С ПРЕДИКАТОМ МИНТА.
//
// Если CalloutsAwaitingNumber считает мудбордную заметку «ждущей номера», а минт её не трогает, то
// граница срабатывает ВЕЧНО: сохранение любой карточки с мудбордом отказывает словами про
// отпечаток секций. Проба ловит именно расхождение двух половин одного правила.
func TestAwaitingNumberAgreesWithTheMintPredicate(t *testing.T) {
	tc := &entity.TechCardInsert{
		Media: []entity.TechCardMediaItem{
			{MediaId: 55, Category: entity.TechCardMediaCategoryTechnical, Kind: entity.TechCardMediaFront},
			{MediaId: 77, Category: entity.TechCardMediaCategoryMoodboard, Kind: entity.TechCardMediaMoodboard},
		},
		Callouts: []entity.TechCardCallout{
			{Number: 0, MediaId: sql.NullInt32{Int32: 77, Valid: true},
				ClientRef: sql.NullString{String: "cr-mood", Valid: true}},
		},
	}
	require.False(t, dto.CalloutsAwaitingNumber(tc),
		"мудбордная заметка объявлена ждущей номера, а минт её не трогает — граница будет отказывать вечно")

	tc.Callouts = append(tc.Callouts, entity.TechCardCallout{
		Number: 0, MediaId: sql.NullInt32{Int32: 55, Valid: true},
		ClientRef: sql.NullString{String: "cr-sheet", Valid: true},
	})
	require.True(t, dto.CalloutsAwaitingNumber(tc),
		"листовая выноска без номера обязана держать границу — иначе она сторожит мёртвый код")
}

// ─────────────────────── 7. ПОРЯДОК, КОТОРОГО НЕ ВИДНО В ИСПОЛНЕНИИ ───────────────────────

// ИДЕМПОТЕНТНОСТЬ ПРОВЕРЯЕТСЯ РАНЬШЕ ДОКУМЕНТНОЙ ЗАПИСИ.
//
// Порядок несущий и с замоканным стором неисполним: документная запись бампает lock_version, и
// повтор, дошедший до неё, получил бы Aborted:lock_version_mismatch — то есть человек увидел бы
// конфликт вместо своего же успеха. Проверяется чтением исходника, ровно как порядок щита
// количеств перед записью; оба якоря обязаны найтись, иначе пустой результат прочитался бы как
// «порядок соблюдён».
func TestMintChecksIdempotencyBeforeWritingTheDocument(t *testing.T) {
	src := readSourceFile(t, "../../store/design/mint.go")
	lookup := strings.Index(src, "versionByRequestID(ctx, db, req.ClientRequestId)")
	write := strings.Index(src, "s.cards.UpdateTechCardTx(ctx, rep, req.TechCardId")
	if lookup < 0 || write < 0 {
		t.Fatalf("якоря не найдены (поиск %d, запись %d) — проверка ничего не измеряет", lookup, write)
	}
	require.Less(t, lookup, write,
		"проверка client_request_id стоит ПОСЛЕ документной записи: повтор получит конфликт версии "+
			"блокировки вместо своей же версии")
}

// readSourceFile читает исходник для текстовых проверок порядка. Отдельная функция, потому что
// нечитаемый файл обязан РОНЯТЬ пробу: «не нашлось» на пустой строке читается как «порядок
// соблюдён», и проверка тихо перестаёт что-либо измерять.
func readSourceFile(t *testing.T, path string) string {
	t.Helper()
	body, err := os.ReadFile(path)
	require.NoError(t, err, "не читается %s — проба обязана упасть, а не «не найти якорей»", path)
	return string(body)
}

// ОТКАЗЫ ДОКУМЕНТНОЙ ЗАПИСИ ПЕРЕВОДЯТСЯ У МИНТА ТАК ЖЕ, КАК У СЕЙВА.
//
// Они приезжают из ОДНОГО кода, значит и объясняться обязаны одинаково. Разойдясь, минт отвечал бы
// «внутренняя ошибка» на payload, который сейв разбирает по полям, — и человек чинил бы карточку
// вслепую.
func TestMintTranslatesDocumentWriteRefusalsLikeTheSaveDoes(t *testing.T) {
	for name, tc := range map[string]struct {
		err  error
		want codes.Code
	}{
		"released":     {entity.ErrTechCardReleased, codes.FailedPrecondition},
		"purpose lock": {entity.ErrTechCardPurposeLocked, codes.FailedPrecondition},
		"card gone":    {sql.ErrNoRows, codes.NotFound},
	} {
		t.Run(name, func(t *testing.T) {
			rig := newMintRig(t, mintStoredCard(), nil, nil, tc.err)
			_, err := rig.srv.MintDesignSheetVersion(mintCtx(), mintRequest(mintDoc()))
			require.Equal(t, tc.want, status.Code(err),
				"минт объясняет отказ документной записи не так, как сейв")
		})
	}
}

// ЧТО МИНТ ЗАМОРАЖИВАЕТ — ТО ЧИТАТЕЛЬ ВЕРСИИ И ОТДАЁТ.
//
// ⚠ ЭТОТ ШОВ ЛОМАЕТСЯ БЕЗ ЕДИНОЙ ОШИБКИ. Колонка annotation объявлена как protojson
// common.TechCardAnnotation, а читатель разбирает её с DiscardUnknown: объект, сложенный руками из
// ХРАНИМЫХ строк («dim», «red»), разбирается в ПУСТОЕ сообщение и возвращает err == nil. То есть
// лист печатается без единой мерки, в логах чисто, и узнают об этом из цеха.
//
// Поэтому проба утверждает СОДЕРЖИМОЕ, а не отсутствие ошибки: на err она зеленела бы в обоих
// исходах, то есть сторожила бы мёртвый код.
func TestFrozenCalloutGeometrySurvivesTheReaderThatShipsIt(t *testing.T) {
	raw, err := dto.TechCardCalloutAnnotationJSON(entity.TechCardCallout{
		Number:  4,
		MediaId: sql.NullInt32{Int32: 55, Valid: true},
		Kind:    entity.AnnotationKindDim,
		Color:   entity.AnnotationColorRed,
		PosX:    decimal.NewNullDecimal(decimal.RequireFromString("0.25")),
		PosY:    decimal.NewNullDecimal(decimal.RequireFromString("0.50")),
		Points: []entity.TechCardAnnotationPoint{
			{X: decimal.RequireFromString("0.10"), Y: decimal.RequireFromString("0.20")},
			{X: decimal.RequireFromString("0.30"), Y: decimal.RequireFromString("0.40")},
		},
		Dashed: true,
	})
	require.NoError(t, err)

	a := &pb_common.TechCardAnnotation{}
	require.NoError(t, designUnmarshalJSON(raw, a), "замороженная геометрия обязана разбираться читателем")
	require.Equal(t, pb_common.TechCardAnnotationKind_TECH_CARD_ANNOTATION_KIND_DIM, a.GetKind(),
		"вид потерян: читатель отдаст пустую выноску, и мерка исчезнет с бумаги молча")
	require.Equal(t, pb_common.TechCardAnnotationColor_TECH_CARD_ANNOTATION_COLOR_RED, a.GetColor())
	require.Len(t, a.GetPoints(), 2, "якоря потеряны: размерная линия рисуется НЕ ТАМ")
	require.True(t, a.GetDashed(), "пунктир входит в подпись секции — терять его нельзя")
	require.NotNil(t, a.GetLabelX(), "положение плашки с номером потеряно")
}
