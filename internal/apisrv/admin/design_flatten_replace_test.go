package admin

import (
	"context"
	"database/sql"
	"fmt"
	"testing"
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/dependency/mocks"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	pb_admin "github.com/jekabolt/grbpwr-manager/proto/gen/admin"
	pb_common "github.com/jekabolt/grbpwr-manager/proto/gen/common"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
	pb_decimal "google.golang.org/genproto/googleapis/type/decimal"
	"google.golang.org/grpc/codes"
	"google.golang.org/protobuf/encoding/protojson"
)

// «ПЕРЕЗАПИСАТЬ» ПРАВКОЙ (0368, O-53) — ТО, ЧТО ЖИВЁТ В ХЕНДЛЕРЕ.
//
// ЧТО ЭТИ ПРОБЫ МОГУТ ДОКАЗАТЬ. Стор замокан, значит ни сторожа (replace_mismatch, already_replaced,
// cut_sheet), ни переезд слота, ни штамп replaced_by ими не проверяются: решение сторожей проверено
// без базы в entity (design_replace_test.go), транзакция — живыми пробами
// internal/store/design/replace_db_test.go (одноразовый контейнер, CI=1). Здесь — то, что ломается
// молча между проводом и стором: поле запроса доезжает до стора, три отказа выходят на провод своими
// словами, а replaced_by выходит из конвертера.

type designFlattenRig struct {
	srv    *Server
	design *mocks.MockDesign
	// sent — то, что дверь отдала стору. nil значит, что до стора дело не дошло.
	sent *entity.DesignEditLayerFlatten
}

func newDesignFlattenRig(t *testing.T, pic *entity.DesignPicture, err error) *designFlattenRig {
	t.Helper()
	rig := &designFlattenRig{design: mocks.NewMockDesign(t)}
	repo := mocks.NewMockRepository(t)
	repo.EXPECT().Design().Return(rig.design).Maybe()
	rig.design.EXPECT().FlattenEditLayer(mock.Anything, mock.AnythingOfType("entity.DesignEditLayerFlatten")).
		Run(func(_ context.Context, req entity.DesignEditLayerFlatten) {
			cp := req
			rig.sent = &cp
		}).Return(pic, err).Once()
	rig.srv = &Server{repo: repo}
	return rig
}

// designFlattenEdit — правка, как её возвращает стор: сиблинг оригинала 7 под тем же прогоном.
func designFlattenEdit() *entity.DesignPicture {
	return &entity.DesignPicture{
		Id: 12, TechCardId: designRunCardID, MediaId: 901, Kind: entity.DesignPictureKindFlat,
		RunId:       sql.NullInt32{Int32: 3, Valid: true},
		DerivedFrom: sql.NullInt32{Int32: 7, Valid: true},
		Derivation:  entity.DesignDerivationFlatten,
		SourceClass: entity.DesignSourceAIEdits,
		LayerRev:    4,
		CreatedAt:   time.Date(2026, 9, 26, 12, 0, 0, 0, time.UTC),
	}
}

// ЗАПРОС ДОЕЗЖАЕТ ДО СТОРА ЦЕЛИКОМ — И С МЕСТОМ, КОТОРОЕ ПРАВКА ЗАНИМАЕТ.
//
// МУТАЦИЯ: выбросить ReplacePictureId из сборки entity.DesignEditLayerFlatten в хендлере. Всё
// компилируется и отвечает OK; «overwrite» молча становится «save as new» — слот остаётся на
// оригинале, оригинал не штампуется, и человек видит две картинки там, где выбрал одну.
//
// ВТОРАЯ ПОЛОВИНА — «рядом» остаётся рядом: запрос без поля доезжает нулём, то есть ровно тем, чем
// флэттен был до поля. Без неё проба зеленела бы и на хендлере, подставляющем свою догадку.
//
// КЛЮЧ ЖЕСТА (0369) ЕДЕТ ТУДА ЖЕ, срезанным по краям. МУТАЦИЯ: выбросить ClientRequestId из сборки —
// всё компилируется, повтор после потерянного ответа снова получает already_replaced на собственный
// успех (или второго сиблинга).
func TestFlattenDesignEditLayerCarriesTheReplaceTargetToTheStore(t *testing.T) {
	t.Run("overwrite", func(t *testing.T) {
		rig := newDesignFlattenRig(t, designFlattenEdit(), nil)
		resp, err := rig.srv.FlattenDesignEditLayer(designRunCtx(), &pb_admin.FlattenDesignEditLayerRequest{
			TechCardId: designRunCardID, LayerId: 5, ExpectedRev: 4, MediaId: 901, ReplacePictureId: 7,
			ClientRequestId: "  k-1\t",
		})
		require.NoError(t, err)
		require.NotNil(t, rig.sent)
		require.Equal(t, entity.DesignEditLayerFlatten{
			TechCardId: designRunCardID, LayerId: 5, ExpectedRev: 4, MediaId: 901,
			ReplacePictureId: 7, ClientRequestId: "k-1", Actor: "designer",
		}, *rig.sent)
		require.Equal(t, int32(12), resp.GetPicture().GetId())
		require.Equal(t, int32(7), resp.GetPicture().GetDerivedFrom(), "правка — сиблинг оригинала")
	})
	t.Run("save as new", func(t *testing.T) {
		rig := newDesignFlattenRig(t, designFlattenEdit(), nil)
		_, err := rig.srv.FlattenDesignEditLayer(designRunCtx(), &pb_admin.FlattenDesignEditLayerRequest{
			TechCardId: designRunCardID, LayerId: 5, ExpectedRev: 4, MediaId: 901,
		})
		require.NoError(t, err)
		require.NotNil(t, rig.sent)
		require.Zero(t, rig.sent.ReplacePictureId, "без поля правка подаётся рядом, как до поля")
		require.Empty(t, rig.sent.ClientRequestId, "без ключа — без защиты от повтора, как до поля")
	})
}

// already_replaced НЕСЁТ ГОЛОВУ ЦЕПОЧКИ — head_picture_id в метаданных ErrorInfo (O-53 review).
//
// Голова приходит из стора ТИПИЗИРОВАННОЙ ошибкой, завёрнутой в прозу, и перекладывается в метаданные
// в одном месте — designError, — а не у каждой двери. Без неё «уже заменён» оставлял клиенту гадать,
// чья правка встала на место.
//
// МУТАЦИИ: не звать designErrorFacts из designError (ключа нет); класть в ключ названный кадр вместо
// головы. Вторая половина — контроль: голый сентинел без головы ключа не получает, то есть значение
// берётся из ошибки, а не выдумывается.
func TestFlattenAlreadyReplacedNamesTheHead(t *testing.T) {
	rig := newDesignFlattenRig(t, nil,
		fmt.Errorf("failed to flatten: %w", &entity.DesignReplacedError{PictureId: 7, HeadPictureId: 19}))
	_, err := rig.srv.FlattenDesignEditLayer(designRunCtx(), &pb_admin.FlattenDesignEditLayerRequest{
		TechCardId: designRunCardID, LayerId: 5, ExpectedRev: 4, MediaId: 901, ReplacePictureId: 7,
	})
	code, md := errorReason(t, err)
	require.Equal(t, codes.FailedPrecondition, code)
	require.Equal(t, "already_replaced", md["reason"])
	require.Equal(t, "19", md["head_picture_id"], "голова цепочки, а не названный кадр")

	rig = newDesignFlattenRig(t, nil, fmt.Errorf("%w: picture 7 (probe)", entity.ErrDesignAlreadyReplaced))
	_, err = rig.srv.FlattenDesignEditLayer(designRunCtx(), &pb_admin.FlattenDesignEditLayerRequest{
		TechCardId: designRunCardID, LayerId: 5, ExpectedRev: 4, MediaId: 901, ReplacePictureId: 7,
	})
	_, md = errorReason(t, err)
	require.Equal(t, "already_replaced", md["reason"])
	_, ok := md["head_picture_id"]
	require.False(t, ok, "голова берётся из ошибки, а не выдумывается")
}

// КЛЮЧ ЖЕСТА НА ПРОВОДЕ — `clientRequestId`, и гейтвей, отвергающий неизвестные поля, его принимает.
//
// МУТАЦИЯ: переименовать поле в proto — клиент шлёт `clientRequestId`, гейтвей отвечает 400 на
// каждый флэттен.
func TestFlattenRequestTakesTheKeyOffTheWire(t *testing.T) {
	var req pb_admin.FlattenDesignEditLayerRequest
	require.NoError(t, protojson.UnmarshalOptions{DiscardUnknown: false}.Unmarshal(
		[]byte(`{"techCardId":41,"layerId":5,"expectedRev":4,"mediaId":901,"replacePictureId":7,"clientRequestId":"k-1"}`),
		&req))
	require.Equal(t, "k-1", req.GetClientRequestId())
}

// designSplitWholeFrame — один кадр во весь лист: форма, которую designSplitRects пропускает.
func designSplitWholeFrame() *pb_admin.DesignSplitFrame {
	return &pb_admin.DesignSplitFrame{
		X: &pb_decimal.Decimal{Value: "0"}, Y: &pb_decimal.Decimal{Value: "0"},
		W: &pb_decimal.Decimal{Value: "1"}, H: &pb_decimal.Decimal{Value: "1"},
		ViewKey: entity.DesignViewFront,
	}
}

// ЗАМЕНЁННЫЙ ЛИСТ НЕ РЕЖЕТСЯ — И ОТКАЗ ЗВУЧИТ ДО БАЙТОВОЙ РАБОТЫ (O-53 review).
//
// Правило авторитетно в транзакции стора; хендлер повторяет его предпроверкой, чтобы не читать
// оригинал, не резать и не заливать куски, которые тут же пришлось бы убирать. Отказ несёт голову
// цепочки — та же, что даёт стор: обход один (entity.DesignAlreadyReplaced).
//
// ЧЕМ ДОКАЗАНО «ДО БАЙТОВ»: хранилище — мок без единого ожидания, и у листа есть файл с управляемым
// адресом, так что без предпроверки хендлер пошёл бы читать оригинал и провалил пробу на первом же
// вызове хранилища; SplitPicture у мока стора не ожидается тоже.
//
// МУТАЦИИ: снять предпроверку (неожиданный вызов хранилища); отдать в голову первую замену (12
// вместо 19).
func TestSplitDesignPictureRefusesAReplacedSheetBeforeTheBytes(t *testing.T) {
	design := mocks.NewMockDesign(t)
	repo := mocks.NewMockRepository(t)
	repo.EXPECT().Design().Return(design).Maybe()
	link := func(id, next int32) *entity.DesignPicture {
		p := &entity.DesignPicture{
			Id: int(id), TechCardId: designRunCardID, MediaId: 900 + int(id), Kind: entity.DesignPictureKindFlat,
			Media: &entity.MediaFull{Id: 900 + int(id), MediaItem: entity.MediaItem{
				FullSizeMediaURL: fmt.Sprintf("https://files.grbpwr.test/design/sheet-%d-og.png", id),
			}},
		}
		if next > 0 {
			p.ReplacedBy = sql.NullInt32{Int32: next, Valid: true}
		}
		return p
	}
	design.EXPECT().GetPicture(mock.Anything, 7).Return(link(7, 12), nil).Once()
	design.EXPECT().GetPicture(mock.Anything, 12).Return(link(12, 19), nil).Once()
	design.EXPECT().GetPicture(mock.Anything, 19).Return(link(19, 0), nil).Once()
	srv := &Server{repo: repo, bucket: mocks.NewMockFileStore(t)}

	_, err := srv.SplitDesignPicture(designRunCtx(), &pb_admin.SplitDesignPictureRequest{
		PictureId: 7, ClientRequestId: "split-1",
		Frames: []*pb_admin.DesignSplitFrame{designSplitWholeFrame()},
	})
	code, md := errorReason(t, err)
	require.Equal(t, codes.FailedPrecondition, code)
	require.Equal(t, "already_replaced", md["reason"])
	require.Equal(t, "19", md["head_picture_id"])
}

// СПРЯТАННЫЙ КАДР НЕ РЕЖЕТСЯ — И ОТКАЗ ЗВУЧИТ ДО БАЙТОВОЙ РАБОТЫ (O-53 review, раунд 3).
//
// Та же обвязка, что у заменённого листа выше: хранилище — мок без единого ожидания, у кадра есть
// файл с управляемым адресом, SplitPicture у мока стора не ожидается. Без предпроверки хендлер пошёл
// бы читать оригинал и провалил пробу на первом же вызове хранилища.
//
// Вторая половина — порядок: заменённый И спрятанный кадр получает already_replaced с головой, как в
// транзакции стора, — устаревшей вкладке нужен ответ «режь голову», а не «кадр спрятан».
//
// МУТАЦИИ: снять предпроверку (неожиданный вызов хранилища); отдать hidden_plate или другой код
// (краснеет reason или код); поставить проверку видимости раньше заменённости (вторая половина
// получает hidden_picture без головы).
func TestSplitDesignPictureRefusesAHiddenPictureBeforeTheBytes(t *testing.T) {
	sheet := func(id int, hidden bool, next int32) *entity.DesignPicture {
		p := &entity.DesignPicture{
			Id: id, TechCardId: designRunCardID, MediaId: 900 + id, Kind: entity.DesignPictureKindFlat,
			Media: &entity.MediaFull{Id: 900 + id, MediaItem: entity.MediaItem{
				FullSizeMediaURL: fmt.Sprintf("https://files.grbpwr.test/design/sheet-%d-og.png", id),
			}},
		}
		if hidden {
			p.HiddenAt = sql.NullTime{Time: time.Date(2026, 9, 26, 12, 0, 0, 0, time.UTC), Valid: true}
		}
		if next > 0 {
			p.ReplacedBy = sql.NullInt32{Int32: next, Valid: true}
		}
		return p
	}
	split := func(t *testing.T, pictures ...*entity.DesignPicture) error {
		design := mocks.NewMockDesign(t)
		repo := mocks.NewMockRepository(t)
		repo.EXPECT().Design().Return(design).Maybe()
		for _, p := range pictures {
			design.EXPECT().GetPicture(mock.Anything, p.Id).Return(p, nil).Once()
		}
		srv := &Server{repo: repo, bucket: mocks.NewMockFileStore(t)}
		_, err := srv.SplitDesignPicture(designRunCtx(), &pb_admin.SplitDesignPictureRequest{
			PictureId: int32(pictures[0].Id), ClientRequestId: "split-1",
			Frames: []*pb_admin.DesignSplitFrame{designSplitWholeFrame()},
		})
		return err
	}

	t.Run("спрятанный кадр", func(t *testing.T) {
		code, md := errorReason(t, split(t, sheet(7, true, 0)))
		require.Equal(t, codes.FailedPrecondition, code)
		require.Equal(t, "hidden_picture", md["reason"])
		require.NotContains(t, md, "head_picture_id", "спрятанный, но не заменённый кадр головы не имеет")
	})
	t.Run("заменённый и спрятанный — сперва голова", func(t *testing.T) {
		code, md := errorReason(t, split(t, sheet(7, true, 12), sheet(12, false, 0)))
		require.Equal(t, codes.FailedPrecondition, code)
		require.Equal(t, "already_replaced", md["reason"])
		require.Equal(t, "12", md["head_picture_id"])
	})
}

// hidden_picture СТОИТ В ТАБЛИЦЕ РОВНО ОДНОЙ СТРОКОЙ — И ЭТО НЕ СТРОКА hidden_plate.
//
// МУТАЦИЯ: сопоставить сентинел разреза токену постановки в слот (или добавить ему вторую строку) —
// клиент показал бы «плиту нельзя поставить» на жест разреза.
func TestSplitHiddenRefusalIsMappedOnce(t *testing.T) {
	n := 0
	for _, r := range designRefusals {
		if r.err == entity.ErrDesignHiddenPicture {
			n++
			require.Equal(t, codes.FailedPrecondition, r.code)
			require.Equal(t, "hidden_picture", r.reason)
		}
	}
	require.Equal(t, 1, n)
}

// ТРИ ОТКАЗА ПЕРЕЗАПИСИ ДОЕЗЖАЮТ ДО КЛИЕНТА САМИМИ СОБОЙ.
//
// ЧТО ЛОВИТСЯ: sentinel, которого нет в таблице designRefusals, уходит на провод как codes.Internal
// «failed to …» ПЛЮС строка ERROR в лог. Для already_replaced это хуже поломки вида: клиент ветвится
// по этому слову («this picture was already replaced — re-read»), а слепой повтор перезаписи получал
// бы «сервер сломался» и повторял бы снова.
//
// Ошибка приходит из стора ЗАВЁРНУТОЙ в прозу — так, как её отдаёт стор, — потому что таблица
// бесполезна, если путь до неё теряет sentinel.
//
// МУТАЦИИ: убрать любую из трёх строк из designRefusals (подслучай становится Internal без reason);
// поменять код строки (подслучай краснеет на коде).
func TestFlattenReplaceRefusalsReachTheClientAsThemselves(t *testing.T) {
	for _, tc := range []struct {
		name   string
		err    error
		code   codes.Code
		reason string
	}{
		{"не тот кадр", entity.ErrDesignReplaceMismatch, codes.InvalidArgument, "replace_mismatch"},
		{"уже заменён", entity.ErrDesignAlreadyReplaced, codes.FailedPrecondition, "already_replaced"},
		{"лист разрезан", entity.ErrDesignCutSheet, codes.FailedPrecondition, "cut_sheet"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rig := newDesignFlattenRig(t, nil, fmt.Errorf("%w: picture 7 (probe)", tc.err))
			_, err := rig.srv.FlattenDesignEditLayer(designRunCtx(), &pb_admin.FlattenDesignEditLayerRequest{
				TechCardId: designRunCardID, LayerId: 5, ExpectedRev: 4, MediaId: 901, ReplacePictureId: 7,
			})
			require.Error(t, err)
			code, md := errorReason(t, err)
			require.Equal(t, tc.code, code)
			require.NotNil(t, md, "отказ перезаписи обязан нести машинную причину, а не одну прозу")
			require.Equal(t, tc.reason, md["reason"])
		})
	}
}

// КАЖДЫЙ ОТКАЗ ПЕРЕЗАПИСИ СТОИТ В ТАБЛИЦЕ РОВНО ОДНОЙ СТРОКОЙ.
//
// Проба через хендлер выше доказывает, что строка ЕСТЬ; эта — что она ОДНА. Вторая строка с тем же
// sentinel-ом и другим словом молча проиграла бы первой (таблица читается сверху), и спор двух
// словарей был бы невидим, пока кто-нибудь не переставил бы их местами.
func TestFlattenReplaceRefusalsAreMappedOnce(t *testing.T) {
	for _, sentinel := range []error{
		entity.ErrDesignReplaceMismatch, entity.ErrDesignAlreadyReplaced, entity.ErrDesignCutSheet,
	} {
		n := 0
		for _, r := range designRefusals {
			if r.err == sentinel {
				n++
			}
		}
		require.Equal(t, 1, n, "%v", sentinel)
	}
}

// replaced_by ВЫХОДИТ ИЗ КОНВЕРТЕРА — И ВЫХОДИТ ЯВНЫМ НУЛЁМ У НЕЗАМЕНЁННОГО.
//
// Конвертер один на все чтения (полоса, прогон, лента прогонов, верстак, ответы записей), поэтому
// поле, забытое здесь, пропало бы отовсюду сразу, а клиентский воркбенч показывал бы оригинал на
// месте правки после каждой перезагрузки.
//
// ⚠ ВТОРАЯ ПОЛОВИНА — ПРО ПРОВОД, А НЕ ПРО GO. Клиент отличает «не заменён» (ключ с нулём) от
// «сервер старше поля» (ключа нет) и во втором случае закрывает «overwrite». Имя ключа на проводе —
// `replacedBy`, и при эмиссии незаполненных полей (так настроен admin-гейтвей) он приходит и с нулём.
// Имя поля запроса — `replacePictureId`: его и пишет клиент.
//
// МУТАЦИИ: не присвоить ReplacedBy в designPictureToPb (заменённый кадр приходит нулём); переименовать
// поле в proto (ключи на проводе меняются).
func TestDesignPictureCarriesReplacedBy(t *testing.T) {
	original := entity.DesignPicture{
		Id: 7, TechCardId: designRunCardID, MediaId: 900, Kind: entity.DesignPictureKindFlat,
		ReplacedBy: sql.NullInt32{Int32: 12, Valid: true},
	}
	require.Equal(t, int32(12), designPictureToPb(original).GetReplacedBy())

	head := *designFlattenEdit()
	require.Zero(t, designPictureToPb(head).GetReplacedBy(), "NULL колонки — ноль на проводе")

	wire := protojson.MarshalOptions{EmitUnpopulated: true}
	b, err := wire.Marshal(designPictureToPb(head))
	require.NoError(t, err)
	require.Contains(t, string(b), `"replacedBy":0`,
		"незаменённый кадр обязан нести ключ с нулём — отсутствие ключа клиент читает как старый сервер")
	b, err = wire.Marshal(designPictureToPb(original))
	require.NoError(t, err)
	require.Contains(t, string(b), `"replacedBy":12`)

	var req pb_admin.FlattenDesignEditLayerRequest
	require.NoError(t, protojson.UnmarshalOptions{DiscardUnknown: false}.Unmarshal(
		[]byte(`{"techCardId":41,"layerId":5,"expectedRev":4,"mediaId":901,"replacePictureId":7}`), &req))
	require.Equal(t, int32(7), req.GetReplacePictureId())

	// Контроль на ложную зелень: сообщение без поля — это pb_common.DesignPicture той же сборки, и
	// ключ в нём обязан существовать именно потому, что поле объявлено, а не потому, что его
	// присвоили.
	b, err = wire.Marshal(&pb_common.DesignPicture{Id: 1})
	require.NoError(t, err)
	require.Contains(t, string(b), `"replacedBy":0`)
}
