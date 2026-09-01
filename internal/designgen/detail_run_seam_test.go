package designgen

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/stretchr/testify/require"
)

// ─────────────────── ШОВ: ЧТО СЕРВЕР ЗАМОРОЗИЛ → ЧТО МОДЕЛЬ УСЛЫШИТ ───────────────────
//
// ⚠ ЭТИ ПРОБЫ НЕ СОБИРАЮТ СНИМОК РУКАМИ, И ЭТО ГЛАВНОЕ В НИХ. Пробы этой волны собирали
// `runInputs` литералом, проставляя `slot_id` — поле, которого настоящая сборка входов
// (internal/apisrv/admin, designSelectBench) не проставляла НИ ПРИ КАКОМ входе. Они были зелены,
// пока фича была мертва на обоих маршрутах. Здесь вход — файл, который пишет НАСТОЯЩИЙ хендлер и
// сверяет со своим выходом проба TestDesignFrozenSnapshotGoldenMatchesTheServer; разойтись
// половинам больше не на чем.

const serverFrozenGolden = "testdata/server_frozen_two_details.json"

type frozenRun struct {
	Params json.RawMessage `json:"params"`
	Inputs json.RawMessage `json:"inputs"`
}

// serverFrozen отдаёт ЗАМОРОЖЕННУЮ СТРОКУ прогона ровно в том виде, в каком её пишет сервер.
func serverFrozen(t *testing.T, name, kind string) entity.DesignRun {
	t.Helper()
	raw, err := os.ReadFile(serverFrozenGolden)
	require.NoError(t, err)
	var all map[string]frozenRun
	require.NoError(t, json.Unmarshal(raw, &all))
	got, ok := all[name]
	require.True(t, ok, "в эталоне сервера нет сценария %q", name)
	return entity.DesignRun{
		Id: 900, TechCardId: 41, Kind: kind,
		Params: entity.RawJSON(got.Params), Inputs: entity.RawJSON(got.Inputs),
	}
}

// MAJOR-1 + MAJOR-3: имя доезжает из ПУСТОГО слота, и лист на две детали больше не обещает одну.
func TestTwoDetailSheetNamesBothFramesAndAsksForTwo(t *testing.T) {
	run := serverFrozen(t, "one", entity.DesignRunKindFlat)
	job, err := buildJob(context.Background(), media(100, 200), run, "medium")
	require.NoError(t, err)

	// ── MAJOR-1: имя резолвится из записи БЕЗ media_id ──
	require.Contains(t, job.Prompt, "draw these details:\ncollar, patch pocket",
		"слоты пусты — картинок в них нет; имя может приехать ТОЛЬКО из записи без media_id")
	require.NotContains(t, job.Prompt, "draw these details:\ndetail",
		"ровно та строка, от которой уходили")

	// ── MAJOR-3: раскладка просит СТОЛЬКО ЖЕ кадров, сколько записал стор в composite_views ──
	require.NotContains(t, job.Prompt, "a single enlarged view of the detail",
		"абзац Эталона 2 обещает ОДНУ картинку, а views просят две — модель обязана была выбирать")
	require.Contains(t, job.Prompt, "two views on one horizontal canvas")
	require.Contains(t, job.Prompt,
		"left to right: DETAIL — collar (one enlarged construction close-up), "+
			"DETAIL — patch pocket (one enlarged construction close-up).",
		"кадры называются ИМЕНЕМ детали: ключ вида у обеих одинаков и различить их не может")

	// ── положительный контроль: промпт вообще собрался, а не оказался пустым ──
	require.Contains(t, job.Prompt, flatOutput)
	// ⚠ БЫЛО ДВА, СТАЛО ОДИН, И ЭТО ФИКС K-1, А НЕ ПОТЕРЯ. Второй картинкой была ПЛИТА ФЛЕТ-СЛОТА,
	// которую флет-прогон брал молча: модель получала свой же старый флет как референс и
	// переписывала его один в один. Уезжает то, что человек принёс, — его референс.
	require.Len(t, job.References, 1, "уезжает референс карточки; плиты верстака — только по просьбе")
}

// КОНТРОЛЬ ГРАНИЦЫ: РОВНО ОДНОЙ ДЕТАЛИ ЭТАЛОН 2 ПРИНАДЛЕЖИТ ПО-ПРЕЖНЕМУ.
//
// Без этой пробы предыдущую можно было бы «починить», выбросив абзац Эталона 2 совсем, — и владелец
// потерял бы дословную формулировку там, где она верна.
func TestSingleDetailKeepsTheOwnersVerbatimDetailLayout(t *testing.T) {
	run := serverFrozen(t, "one_single_detail", entity.DesignRunKindFlat)
	job, err := buildJob(context.Background(), media(100, 200), run, "medium")
	require.NoError(t, err)
	require.Contains(t, job.Prompt,
		"Layout: a single enlarged view of the detail, isolated and centered on the canvas, "+
			"shown from the same angle as the reference.")
	require.Contains(t, job.Prompt, "draw these details:\ncollar")
	require.NotContains(t, job.Prompt, "one horizontal canvas")
}

// MAJOR-4: `per_view` — умолчание формы, то есть ОСНОВНОЙ маршрут. Две детали давали два платных
// вызова с промптом, совпадающим ПОБАЙТОВО: дописывался ключ вида, одинаковый у обеих.
func TestPerViewMakesTwoDIFFERENTPaidCallsForTwoDetails(t *testing.T) {
	run := serverFrozen(t, "per_view", entity.DesignRunKindFlat)
	job, err := buildJob(context.Background(), media(100, 200), run, "medium")
	require.NoError(t, err)

	calls, err := imageCalls(job)
	require.NoError(t, err)
	require.Len(t, calls, 2, "два кадра — два платных вызова")
	require.NotEqual(t, calls[0].prompt, calls[1].prompt,
		"два списания за побайтово одинаковый запрос: какая из картинок воротник, не знает никто")
	require.Contains(t, calls[0].prompt, "view:\ndetail — collar")
	require.Contains(t, calls[1].prompt, "view:\ndetail — patch pocket")

	// ⚠ МЕТКА КАДРА ОСТАЁТСЯ ЧИСТЫМ КЛЮЧОМ ВИДА: её сверяет со своим словарём стор (GhostView).
	require.Equal(t, entity.DesignViewDetail, calls[0].view)
	require.Equal(t, entity.DesignViewDetail, calls[1].view)
}

// MINOR-7: 3D-прогон, унаследовавший `detail_slot_ids`, не обязан нести их в промпт сборки Meshy.
//
// Реран переписывает параметры целиком, поэтому список приезжает к роду, который ничего не рисует
// по просьбе; у Meshy при этом есть собственный ErrPromptTooLong.
func TestThreedDoesNotCarryTheDetailListIntoTheMeshyPrompt(t *testing.T) {
	run := serverFrozen(t, "one", entity.DesignRunKindThreed)
	job, err := buildJob(context.Background(), media(100, 200), run, "medium")
	require.NoError(t, err)
	require.NotContains(t, job.Prompt, "draw these details")
	require.NotContains(t, job.Prompt, "patch pocket")
	// Положительный контроль: человеческий контекст 3D-прогона на месте, промпт не пуст.
	require.Contains(t, job.Prompt, "fit:\noversized")
}

// MINOR-6: клоз третьего ранга В ОДИНОЧЕСТВЕ обязан иметь ПОЛОЖИТЕЛЬНУЮ форму.
//
// Ткань, заданная ТОЛЬКО словами, давала единственное утверждение о ткани — и оно объявляло себя
// подчинённым фотографии и заданному цвету, которых в промпте нет вовсе.
func TestFabricStatedOnlyInWordsSpeaksAffirmatively(t *testing.T) {
	run := entity.DesignRun{
		Id: 5, Kind: entity.DesignRunKindRender,
		Params: entity.RawJSON(`{"views":["front"],"layout":"one","colour":{"source":"own","words":"brushed cotton twill"}}`),
		Inputs: entity.RawJSON(`{"garment_note":"a shirt"}`),
	}
	job, err := buildJob(context.Background(), media(), run, "medium")
	require.NoError(t, err)
	require.NotContains(t, job.Prompt, "It never overrides either of them",
		"единственное утверждение о ткани не может быть подчинено пустоте")
	require.NotContains(t, job.Prompt, "neither the photograph nor the stated colour")
	require.Contains(t, job.Prompt, "is the only statement this run makes about the cloth, "+
		"and it governs the material outright")

	// И ранг сохраняется, когда соседи ЕСТЬ: одинокая форма не должна вытеснить порядок старшинства.
	ranked := entity.DesignRun{
		Id: 6, Kind: entity.DesignRunKindRender,
		Params: entity.RawJSON(`{"views":["front"],"layout":"one","colour":{"source":"dictionary","code":"OLV","hex":"#4a5a3c","words":"brushed cotton twill"}}`),
		Inputs: entity.RawJSON(`{"garment_note":"a shirt"}`),
	}
	rankedJob, err := buildJob(context.Background(), media(), ranked, "medium")
	require.NoError(t, err)
	require.Contains(t, rankedJob.Prompt, "It never overrides either of them",
		"с двумя источниками ранговая формулировка обязана вернуться")
	require.Contains(t, rankedJob.Prompt, "fixed order of authority")
}

// MINOR-10 сторожится сборкой пакета, а не пробой; здесь держится только то, что имя детали не
// протекает в метку кадра ни на одном маршруте.
func TestDetailNameNeverLeaksIntoTheGhostLabel(t *testing.T) {
	run := serverFrozen(t, "per_view", entity.DesignRunKindFlat)
	job, err := buildJob(context.Background(), media(100, 200), run, "medium")
	require.NoError(t, err)
	perViewCalls, err := imageCalls(job)
	require.NoError(t, err)
	for _, c := range perViewCalls {
		require.False(t, strings.Contains(c.view, "collar") || strings.Contains(c.view, "pocket"),
			"GhostView сверяется со словарём видов стора: имя детали делает её нечитаемой")
	}
}
