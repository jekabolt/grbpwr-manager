package admin

import (
	"net/http"
	"testing"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	pb_admin "github.com/jekabolt/grbpwr-manager/proto/gen/admin"
	pb_common "github.com/jekabolt/grbpwr-manager/proto/gen/common"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
)

// ═══ СПРЯТАННЫЙ КАДР НЕ УЕЗЖАЕТ НИ В ОДИН ПЛАТНЫЙ ВЫЗОВ — ЖИВОЙ ЗАМЕР У ДВЕРИ ═══════════════════
//
// Довод целиком — в шапке design_input_format.go (раздел про спрятанный кадр). Здесь измеряется
// то, чего не доказать чтением: что отказ приходит ДО `Design().StartRun`, то есть до строки и до
// резерва дня, и что спрашивают по ВСЕМУ множеству входов, а не по одному новому роду. Стенды
// отказа собраны БЕЗ заглушки StartRun — тот же приём, что у двух соседних дверей: сторож,
// перенесённый ниже резерва, роняет пробу на незаявленном вызове по имени.
//
// ⚠ ДЫРА БЫЛА ОБЩАЯ, И ИМЕННО ЭТО ЗДЕСЬ ДЕРЖИТСЯ. Codex нашёл её на картинках плейграунда
// (`params.freeform.items`), но про `hidden_at` не спрашивал НИ ОДИН из источников входа. Поэтому
// проб четыре, а не одна: номер с провода, картинка плейграунда, полнота вопроса и доска
// черновика. Проба, оставленная только на плейграунде, зеленела бы на двери, починившей шестой
// путь и оставившей пять.
//
// МУТАЦИИ, ЗАМЕРЕННЫЕ ЧЕРЕЗ `go test -overlay` (по числу ПРОВАЛОВ, а не по коду возврата):
//   - снять вызов `s.designRefuseHiddenInputs` из StartDesignRun — 3 провала: номер с провода,
//     плейграунд и полнота вопроса; проба доски остаётся зелёной, и это правильно;
//   - снять вызов из DraftDesignIdea — 1 провал: проба доски, и только она;
//   - оставить двери только источники `params.freeform*` («починить новый род») — 3 провала: номер
//     с провода, полнота вопроса и доска; зелёной остаётся ровно та проба, ради которой дефект и
//     завели, — то есть одной её было бы мало.
//
// Перенос двери НИЖЕ резерва ловится не утверждением, а формой стенда: три стенда отказа собраны
// БЕЗ заглушки StartRun, поэтому дверь, доехавшая до стора, роняет пробу на незаявленном вызове.

const designHiddenMediaID = 8500

// ─────────────── НОМЕР С ПРОВОДА ───────────────

// TestARunNamingAHiddenPictureIsRefusedBeforeTheReserve — путь, который минует и верстак, и
// плейграунд: медиа спрятанного кадра названо в `extra_input_media_ids`. «Спрятать» — единственный
// жест, которым человек говорит про кадр «я его отверг»; платить за отвергнутое второй раз он не
// просил.
func TestARunNamingAHiddenPictureIsRefusedBeforeTheReserve(t *testing.T) {
	rig := newHiddenInputRig(t, designBandWith(true), []int{designHiddenMediaID}, false,
		map[int]string{designHiddenMediaID: designPNGURL})

	req := designStartRequest(entity.DesignRunKindRender)
	req.Params.ExtraInputMediaIds = []int32{designHiddenMediaID}

	_, err := rig.srv.StartDesignRun(designRunCtx(), req)
	require.Error(t, err, "отвергнутый кадр в слоте картинки платного вызова")
	code, md := errorReason(t, err)
	require.Equal(t, codes.FailedPrecondition, code,
		"просьба синтаксически законна — не годится состояние кадра; это не InvalidArgument")
	require.Equal(t, "hidden_input", md["reason"],
		"клиент разбирает слово, а не английскую фразу")
	require.Equal(t, "8500", md["media_id"], "отказ обязан назвать НОМЕР, иначе его нечем чинить")
	require.Equal(t, "params.extra_input_media_ids", md["where"],
		"…и поле, иначе человек ищет по четырём экранам")
	require.Contains(t, err.Error(), "Nothing was reserved and nothing was charged")
	require.Nil(t, rig.sent, "строки прогона нет, значит и резерв дня не двигался")
	require.Contains(t, rig.askedHidden, designHiddenMediaID,
		"дверь спросила у стора именно про этот номер")
}

// ─────────────── ШЕСТОЙ ИСТОЧНИК: КАРТИНКИ ПЛЕЙГРАУНДА ───────────────

// TestAHiddenPlaygroundPictureIsRefusedBeforeTheReserve — ровно тот путь, на котором дыру нашли.
// Список `items` едет поставщику целиком, и спрятанная картинка в нём — это платный вызов по
// забракованному кадру. Отказ обязан назвать ИНДЕКС в списке: у плейграунда их бывает много, и
// «какое-то медиа» человеку нечем чинить.
func TestAHiddenPlaygroundPictureIsRefusedBeforeTheReserve(t *testing.T) {
	rig := newHiddenInputRig(t, designBandWith(true), []int{designHiddenMediaID}, false,
		map[int]string{designHiddenMediaID: designPNGURL, 8501: designPNGURL})

	req := designStartRequest(entity.DesignRunKindFreeform)
	req.Params = ffParams(entity.DesignFreeformPresetFree,
		&pb_common.DesignFreeformItem{MediaId: 8501},
		&pb_common.DesignFreeformItem{MediaId: designHiddenMediaID})

	_, err := rig.srv.StartDesignRun(designRunCtx(), req)
	require.Error(t, err)
	code, md := errorReason(t, err)
	require.Equal(t, codes.FailedPrecondition, code)
	require.Equal(t, "hidden_input", md["reason"])
	require.Equal(t, "8500", md["media_id"])
	require.Equal(t, "params.freeform.items.1.media_id", md["where"],
		"названа СТРОКА списка, а не «плейграунд»")
	require.Nil(t, rig.sent, "отказ до резерва: строки нет и бюджет дня не тронут")
}

// ─────────────── ПОЛНОТА ВОПРОСА И ПОЛОЖИТЕЛЬНЫЙ КОНТРОЛЬ ───────────────

// TestTheHiddenDoorAsksAboutEveryInputSource — ничего не спрятано, прогон стартует; а спросили при
// этом про ВСЕ пять источников: дополнительный вход, скаляр ткани, список тканей, плиту верстака и
// референс карточки. Дверь, спрашивающая только плейграунд, зеленела бы на пробе выше и краснела
// бы здесь.
func TestTheHiddenDoorAsksAboutEveryInputSource(t *testing.T) {
	const extra, clothA, clothB = 8500, 8600, 8700
	rig := newHiddenInputRig(t, designBandWith(true), nil, true,
		map[int]string{extra: designPNGURL, clothA: designPNGURL, clothB: designPNGURL})

	req := designStartRequest(entity.DesignRunKindRender)
	req.Params.ExtraInputMediaIds = []int32{extra}
	req.Params.Colour = &pb_common.DesignColourRecipe{
		FabricMediaId: clothA,
		Fabrics: []*pb_common.DesignFabricUse{
			{Name: "main cloth", Words: "washed cotton", Parts: "body", MediaId: clothA},
			{Name: "contrast rib", Words: "2x2 rib", Parts: "collar", MediaId: clothB},
		},
	}

	_, err := rig.srv.StartDesignRun(designRunCtx(), req)
	require.NoError(t, err, "ничего не спрятано — дверь обязана пропустить")
	require.NotNil(t, rig.sent)
	for _, id := range []int{extra, clothA, clothB, designPlateMediaID, designRefMediaID} {
		require.Contains(t, rig.askedHidden, id,
			"источник %d не был спрошен: тот путь остался бы открытым", id)
	}
}

// ─────────────── ТЕКСТОВЫЙ ПРОГОН ───────────────

// TestADraftWithAHiddenBoardPictureIsRefusedBeforeTheModelIsCalled — доска не проходит через
// полосу, но медиа спрятанного кадра лежит на ней тем же номером. Измеряются ОБА конца: строки
// прогона нет и ПОСТАВЩИКА НЕ ЗВАЛИ — этот вызов платный сам по себе.
func TestADraftWithAHiddenBoardPictureIsRefusedBeforeTheModelIsCalled(t *testing.T) {
	client, calls := newFakeOpenRouter(t, func(w http.ResponseWriter) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"an idea"}}]}`))
	})

	rig := newHiddenInputRig(t, designBandWith(true), []int{designBoardMediaID}, false, nil)
	rig.srv.aiOps = client

	_, err := rig.srv.DraftDesignIdea(designRunCtx(), &pb_admin.DraftDesignIdeaRequest{
		TechCardId:      designRunCardID,
		ClientRequestId: "77777777-7777-7777-7777-777777777777",
	})
	require.Error(t, err)
	code, md := errorReason(t, err)
	require.Equal(t, codes.FailedPrecondition, code)
	require.Equal(t, "hidden_input", md["reason"])
	require.Equal(t, "the moodboard of this card", md["where"])
	require.Equal(t, "900", md["media_id"])
	require.Nil(t, rig.sent, "строки прогона нет, значит и резерв дня не двигался")
	require.Empty(t, *calls, "и модель не звали: этот вызов платный сам по себе")
}
