package admin

// ПРОБЫ ВОЛНЫ 26.09 (O-33, D-32): ДЕТАЛИ ДЛЯ ОТДЕЛЬНОГО РИСУНКА — НЕ АСПЕКТЫ.
//
// Клиент делал DETAIL-слот из КАЖДОГО аспекта конструкции — и у пуловера появлялся «DETAIL ·
// FASTENING». Но «какие детали заслуживают собственного рисунка» — другой вопрос, чем «какая тут
// конструкция», и обычный ответ на него — «никакие». Здесь прибито:
//
//  1. ВОПРОС ЗАДАЁТСЯ ОТДЕЛЬНОЙ ИНСТРУКЦИЕЙ ТОГО ЖЕ ВЫЗОВА (правило 12, ключ `flat_details`), с
//     явным «если ничего — пустой список» и запретом стандартных элементов.
//  2. РАЗБОР: имя обязательно, записка нет; «none» — не имя; потолок 6, имя ≤ 40 рун, записка ≤
//     200; дедуп по складке имени; пустой список — не коэрция.
//  3. КРУГ: канон пишет ключ и читает его обратно — повтор отдаёт детали без модели.
//  4. ХЕНДЛЕР: детали в ответе рядом с аспектами, а не вместо них; канон несёт их.

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	pb_common "github.com/jekabolt/grbpwr-manager/proto/gen/common"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
)

// РОЛЬ ПРОСИТ СПИСОК ДЕТАЛЕЙ ДЛЯ ОТДЕЛЬНОГО РИСУНКА — ОТДЕЛЬНЫМ ПРАВИЛОМ, С ЯВНЫМ «ПУСТО — ЗАКОННО».
func TestConstructionSystemPromptAsksWhichDetailsNeedTheirOwnDrawing(t *testing.T) {
	require.Contains(t, designConstructionSystemPrompt,
		"\"flat_details\": [{\"name\": string, \"note\": string}]", "ключ в форме ответа")
	require.Contains(t, designConstructionSystemPrompt, "\n12. \"flat_details\"", "правило 12, без перенумерации")
	require.Contains(t, designConstructionSystemPrompt, "need a drawing of their OWN")
	require.Contains(t, designConstructionSystemPrompt, "cannot be understood from the front and back flats")
	require.Contains(t, designConstructionSystemPrompt,
		"an unusual pocket construction, a special collar, cuff, placket or vent, a hidden fastening detail, a hardware detail")
	require.Contains(t, designConstructionSystemPrompt, "If nothing needs a separate drawing, return an empty list")
	require.Contains(t, designConstructionSystemPrompt, "Do not list standard elements")
	require.Contains(t, designConstructionSystemPrompt, "plain hems, plain seams, topstitching, labels")
	require.Contains(t, designConstructionSystemPrompt, "6 flat details", "потолок назван в правиле 7")
	// ОДИН ВЫЗОВ, ДВА ВОПРОСА — И ОНИ НЕ ИСКЛЮЧАЮТ ДРУГ ДРУГА (ревью 26.09, MAJOR 3): необычный
	// карман — аспект по правилу 3 И деталь для рисунка по правилу 12. Прежнее «do not repeat
	// aspects» позволяло буквальной модели опустить рисунок.
	require.NotContains(t, designConstructionSystemPrompt, "do not repeat \"aspects\"")
	require.Contains(t, designConstructionSystemPrompt,
		"Do not mechanically turn every aspect into a flat detail")
	require.Contains(t, designConstructionSystemPrompt,
		"a feature may appear in both when its construction fact belongs in \"aspects\" and it also needs its own drawing")
}

// ХОРОШИЙ ОТВЕТ: имя и записка доезжают; записка не обязательна.
func TestParseConstructionDraftReadsFlatDetails(t *testing.T) {
	draft, stats, err := parseConstructionDraft(`{"silhouette":"tee",
	  "flat_details":[
	    {"name":"welt pocket with flap","note":"flap caught in the welt seam; show the bag"},
	    {"name":"two-piece cuff with placket"}]}`, "stop")
	require.NoError(t, err)
	require.Len(t, draft.GetFlatDetails(), 2)
	require.Equal(t, "welt pocket with flap", draft.GetFlatDetails()[0].GetName())
	require.Equal(t, "flap caught in the welt seam; show the bag", draft.GetFlatDetails()[0].GetNote())
	require.Equal(t, "two-piece cuff with placket", draft.GetFlatDetails()[1].GetName())
	require.Equal(t, "", draft.GetFlatDetails()[1].GetNote(), "записка не обязательна")
	require.Equal(t, 0, stats.FlatDetailsDropped)
	require.False(t, stats.Coerced())
}

// «НИЧЕГО РИСОВАТЬ ОТДЕЛЬНО НЕ НУЖНО» — ЗАКОННЫЙ ОТВЕТ, И ОН НЕ КОЭРЦИЯ.
func TestParseConstructionDraftAcceptsAnEmptyFlatDetailList(t *testing.T) {
	for name, body := range map[string]string{
		"пустой список": `{"silhouette":"tee","flat_details":[]}`,
		"null":          `{"silhouette":"tee","flat_details":null}`,
		"ключа нет":     `{"silhouette":"tee"}`,
	} {
		t.Run(name, func(t *testing.T) {
			draft, stats, err := parseConstructionDraft(body, "stop")
			require.NoError(t, err)
			require.Empty(t, draft.GetFlatDetails())
			require.False(t, stats.Coerced(), "«ничего» — не потеря и не поправка")
		})
	}

	// ОТВЕТ, СОДЕРЖАТЕЛЬНЫЙ ОДНИМ СПИСКОМ ДЕТАЛЕЙ, — ОТВЕТ ПО СХЕМЕ: ключу есть куда лечь.
	only, _, err := parseConstructionDraft(`{"flat_details":[{"name":"storm flap"}]}`, "stop")
	require.NoError(t, err)
	require.Len(t, only.GetFlatDetails(), 1)

	// НЕ-СПИСОК НА МЕСТЕ СПИСКА — выброшен ПООДИНОЧКЕ, остальной ответ цел.
	bad, stats, err := parseConstructionDraft(`{"silhouette":"tee","flat_details":"none"}`, "stop")
	require.NoError(t, err)
	require.Equal(t, "tee", bad.GetSilhouette())
	require.Empty(t, bad.GetFlatDetails())
	require.Equal(t, 1, stats.FieldsDropped)

	// ОБЪЕКТ НА МЕСТЕ ИМЕНИ — не имя: строка выброшена и посчитана дважды, как и везде.
	obj, stats, err := parseConstructionDraft(`{"flat_details":[{"name":{"a":1},"note":"x"}]}`, "stop")
	require.NoError(t, err)
	require.Empty(t, obj.GetFlatDetails())
	require.Equal(t, 1, stats.NonScalars)
	require.Equal(t, 1, stats.FlatDetailsDropped)
}

// ПОТОЛОК, ДЕДУП, ОБРЕЗКА, «NONE» — ВСЁ В ОДНОМ ОТВЕТЕ.
func TestParseConstructionDraftCapsDedupesAndDropsFlatDetails(t *testing.T) {
	longName := strings.Repeat("н", designConstructionMaxFlatDetailNameRunes+5)
	longNote := strings.Repeat("з", designConstructionMaxFlatDetailNoteRunes+5)
	items := []map[string]string{
		{"name": "none", "note": ""},                  // отсутствие — не имя
		{"name": "No separate drawing needed"},        // отсутствие
		{"name": "", "note": "a note without a name"}, // без имени
		{"name": "—"},                                 // одна пунктуация — пусто
		{"name": "Welt Pocket", "note": "first"},
		{"name": "welt pocket", "note": "second"}, // повтор другим написанием
		{"name": "welt-pocket", "note": "third"},  // и с дефисом
		{"name": longName, "note": longNote},
	}
	for i := 0; i < designConstructionMaxFlatDetails+2; i++ {
		items = append(items, map[string]string{"name": "detail " + string(rune('a'+i)), "note": "why"})
	}
	body, err := json.Marshal(map[string]any{"silhouette": "coat", "flat_details": items})
	require.NoError(t, err)

	draft, stats, perr := parseConstructionDraft(string(body), "stop")
	require.NoError(t, perr)

	got := draft.GetFlatDetails()
	require.Len(t, got, designConstructionMaxFlatDetails, "потолок держит")
	require.Equal(t, 4, stats.FlatDetailsDropped, "два отсутствия, без имени, одна пунктуация")
	require.Equal(t, 2, stats.Deduped, "три написания одного кармана — одна строка")
	// Кандидатов после выброса: карман + длинное имя + 8 «detail x» = 10; потолок 6 → 4 сверх.
	require.Equal(t, 4, stats.OverLimit)
	require.True(t, stats.Coerced())

	require.Equal(t, "Welt Pocket", got[0].GetName(), "первое написание побеждает")
	require.Equal(t, "first", got[0].GetNote())
	require.Len(t, []rune(got[1].GetName()), designConstructionMaxFlatDetailNameRunes)
	require.Len(t, []rune(got[1].GetNote()), designConstructionMaxFlatDetailNoteRunes)
	require.True(t, strings.HasSuffix(got[1].GetName(), "…"), "обрезка маркируется, а не молчит")
	require.Equal(t, 2, stats.Truncated)
	for _, d := range got {
		require.False(t, designIsAbsentFlatDetailName(d.GetName()), "%q: заглушка доехала до ответа", d.GetName())
		require.NotEmpty(t, d.GetName())
	}
}

// КРУГ «МАРШАЛЕР → СТРОКА → ТОТ ЖЕ РАЗБОР» ДЕРЖИТ ДЕТАЛИ — ПОВТОР ОТДАСТ ИХ БЕЗ МОДЕЛИ.
func TestConstructionDraftRoundTripsFlatDetails(t *testing.T) {
	in := &pb_common.DesignConstructionDraft{
		Silhouette: "coat",
		FlatDetails: []*pb_common.DesignFlatDetail{
			{Name: "storm flap", Note: "single layer; show how it is caught in the yoke seam"},
			{Name: "two-piece cuff"},
		},
	}
	stored, err := designMarshalConstructionDraft(in)
	require.NoError(t, err)
	require.Contains(t, string(stored), `"flat_details"`)
	back := designConstructionDraftFromRun(string(stored))
	require.NotNil(t, back)
	require.True(t, proto.Equal(in, back), "хранилось: %s\nпрочитано: %v", stored, back)

	// ПУСТОЙ СПИСОК ТОЖЕ СХОДИТСЯ: канон пишет ключ пустым, читатель читает пусто.
	empty := &pb_common.DesignConstructionDraft{Silhouette: "tee"}
	stored, err = designMarshalConstructionDraft(empty)
	require.NoError(t, err)
	require.Contains(t, string(stored), `"flat_details"`)
	back = designConstructionDraftFromRun(string(stored))
	require.NotNil(t, back)
	require.Empty(t, back.GetFlatDetails())
	require.True(t, proto.Equal(empty, back))
}

// ХЕНДЛЕР: ДЕТАЛИ В ОТВЕТЕ РЯДОМ С АСПЕКТАМИ, «NONE» ВЫБРОШЕН, КАНОН НЕСЁТ ДЕТАЛИ.
func TestDraftDesignIdeaAnswersWithFlatDetails(t *testing.T) {
	answer := `{"silhouette":"Field jacket","fabric":"Cotton canvas",
	  "aspects":[{"key":"pockets","text":"Four bellows pockets with flaps"}],
	  "flat_details":[
	    {"name":"bellows pocket with flap","note":"show the pleat depth and how the flap is set"},
	    {"name":"none"}],
	  "bom":[{"section":"fabric","purpose":"main","name":"main fabric"}]}`
	rig := newDraftRig(t, http.StatusOK, answer)
	resp, err := rig.srv.DraftDesignIdea(designRunCtx(), draftConstructionRequest())
	require.NoError(t, err)

	// ОДИН ВЫЗОВ: инструкция про детали уехала той же ролью, тем же запросом — по БАЙТАМ.
	require.Contains(t, rig.stub.body, "flat_details")
	require.Contains(t, rig.stub.body, "return an empty list")

	got := resp.GetConstruction().GetFlatDetails()
	require.Len(t, got, 1, "«none» выброшен, настоящая деталь доехала")
	require.Equal(t, "bellows pocket with flap", got[0].GetName())
	require.Equal(t, "show the pleat depth and how the flap is set", got[0].GetNote())
	// АСПЕКТ ОСТАЛСЯ АСПЕКТОМ: детали не собраны из аспектов и не подменяют их.
	require.Len(t, resp.GetConstruction().GetAspects(), 1)
	require.Equal(t, "pockets", resp.GetConstruction().GetAspects()[0].GetKey())

	// КАНОН НЕСЁТ ДЕТАЛИ — повтор отдаст их без модели; выброшенное в нём не живёт.
	stored := designConstructionDraftFromRun(rig.completedText)
	require.NotNil(t, stored)
	require.Len(t, stored.GetFlatDetails(), 1)
	require.Equal(t, "bellows pocket with flap", stored.GetFlatDetails()[0].GetName())
	require.NotContains(t, rig.completedText, `"none"`)
	require.Empty(t, rig.failed)
}
