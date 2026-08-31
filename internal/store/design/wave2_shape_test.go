package design

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/stretchr/testify/require"
)

// ПРОБЫ ГЕНЕРАТИВНОЙ ПОЛОВИНЫ, КОТОРЫМ БАЗА НЕ НУЖНА.
//
// Здесь проверяются решения, целиком живущие в Go: какие слоты попадают на бумагу, когда кадр
// считается смесью, что записывается в composite_views и как раздаются догадки о видах. Живая
// половина (деньги, захват, токен) — в wave2_db_test.go, она требует одноразового контейнера.

// ─────────────────────── смесь провенансов ───────────────────────

// ОДИН ПРОВЕНАНС — НЕ СМЕСЬ. Положительный контроль ко всем пробам ниже.
func TestMixedInputIsFalseForOneProvenance(t *testing.T) {
	require.False(t, designMixedInput([]designInputProvenance{
		{SourceClass: entity.DesignSourceAI},
		{SourceClass: entity.DesignSourceAI},
	}))
}

// ДВА ПРОВЕНАНСА — СМЕСЬ. Ровно то, на что человек даёт согласие при минте.
func TestMixedInputIsTrueForTwoProvenances(t *testing.T) {
	require.True(t, designMixedInput([]designInputProvenance{
		{SourceClass: entity.DesignSourceAI},
		{SourceClass: entity.DesignSourceUploaded},
	}))
}

// СМЕСЬ НЕ ОТМЫВАЕТСЯ ЕЩЁ ОДНОЙ ГЕНЕРАЦИЕЙ. Иначе согласие обходится одним лишним прогоном.
func TestMixedInputInheritsFromAMixedInput(t *testing.T) {
	require.True(t, designMixedInput([]designInputProvenance{
		{SourceClass: entity.DesignSourceAI, MixedInput: true},
	}))
}

// ВХОД БЕЗ ПРОВЕНАНСА — ЭТО ФАЙЛ ЧЕЛОВЕКА. Референс не является картинкой полосы по построению,
// и не учитывать его вовсе значило бы, что правка ИИ-плиты человеческим референсом не смесь.
func TestMixedInputTreatsAnUnknownInputAsUploaded(t *testing.T) {
	require.True(t, designMixedInput([]designInputProvenance{
		{SourceClass: entity.DesignSourceAI},
		{SourceClass: ""},
	}))
}

// ─────────────────────── снимок входов ───────────────────────

// SNAKE_CASE — НЕСУЩЕЕ. protojson здесь с UseProtoNames: true, и разбор по lowerCamelCase был бы
// МОЛЧА пустым: ни одной ошибки, просто «входов нет» — то есть mixed_input никогда не поднялся
// бы. Обе половины проверяются вместе, потому что зелень первой без второй ничего не значит.
func TestRunInputMediaIDsReadSnakeCaseAndNothingElse(t *testing.T) {
	run := entity.DesignRun{
		Inputs: entity.RawJSON(`{"refs":[{"media_id":11}],"slots":[{"media_id":12},{"media_id":11}]}`),
		Params: entity.RawJSON(`{"extra_input_media_ids":[13]}`),
	}
	require.Equal(t, []int{11, 12, 13}, runInputMediaIDs(run),
		"порядок сохранён, дубликат снят")

	camel := entity.DesignRun{
		Inputs: entity.RawJSON(`{"refs":[{"mediaId":11}],"slots":[{"mediaId":12}]}`),
		Params: entity.RawJSON(`{"extraInputMediaIds":[13]}`),
	}
	require.Empty(t, runInputMediaIDs(camel),
		"дефолтный protojson написал бы lowerCamelCase — и провенанс стал бы пустым БЕЗ ЕДИНОЙ ОШИБКИ")
}

// ИСПОРЧЕННЫЙ СНИМОК НЕ РОНЯЕТ ПРИЛЁТ. Терять тут можно догадку о виде, но не оплаченный кадр.
func TestRunInputMediaIDsSurviveBrokenJSON(t *testing.T) {
	require.Empty(t, runInputMediaIDs(entity.DesignRun{Inputs: entity.RawJSON(`{oops`)}))
}

// ─────────────────────── композит и догадка о виде ───────────────────────

// LAYOUT=ONE С НЕСКОЛЬКИМИ ВИДАМИ = КОМПОЗИТ. Это и есть писатель, которого у колонки не было:
// без него isComposite() на клиенте ВСЕГДА ложно, правило «композит нельзя положить в слот» не
// работает, а резак режет вслепую.
func TestCompositeViewsAreWrittenForASingleSheet(t *testing.T) {
	raw, err := compositeViewsOf(entity.DesignPictureInsert{},
		designRunParams{Layout: designLayoutOne, Views: []string{"front", "back", "side_l"}})
	require.NoError(t, err)
	var views []string
	require.NoError(t, json.Unmarshal(raw, &views))
	require.Equal(t, []string{"front", "back", "side_l"}, views)
}

// ОТДЕЛЬНЫЕ КАРТИНКИ КОМПОЗИТОМ НЕ ЯВЛЯЮТСЯ, и один запрошенный вид — тоже.
func TestCompositeViewsStayEmptyForSeparatePictures(t *testing.T) {
	raw, err := compositeViewsOf(entity.DesignPictureInsert{},
		designRunParams{Layout: designLayoutPerView, Views: []string{"front", "back"}})
	require.NoError(t, err)
	require.Nil(t, raw)

	raw, err = compositeViewsOf(entity.DesignPictureInsert{},
		designRunParams{Layout: designLayoutOne, Views: []string{"front"}})
	require.NoError(t, err)
	require.Nil(t, raw)
}

// НАЗВАННОЕ ВОРКЕРОМ ПОБЕЖДАЕТ ДОГАДКУ: он видел, что реально прислал провайдер.
func TestCompositeViewsFromTheWorkerWin(t *testing.T) {
	raw, err := compositeViewsOf(
		entity.DesignPictureInsert{CompositeViews: json.RawMessage(`["front","detail"]`)},
		designRunParams{Layout: designLayoutOne, Views: []string{"front", "back"}})
	require.NoError(t, err)
	require.JSONEq(t, `["front","detail"]`, string(raw))
}

// ВИДЫ РАЗДАЮТСЯ ВЫДАЧЕ ПО ПОРЯДКУ — и композит догадки не получает вовсе: он не один вид, и
// подставленный ему первый дал бы резаку неверную подсказку.
func TestGhostViewFollowsTheRequestedOrder(t *testing.T) {
	p := designRunParams{Layout: designLayoutPerView, Views: []string{"front", "back"}}
	require.Equal(t, "front", ghostViewOf(entity.DesignPictureInsert{Ordinal: 0}, p))
	require.Equal(t, "back", ghostViewOf(entity.DesignPictureInsert{Ordinal: 1}, p))
	require.Equal(t, "", ghostViewOf(entity.DesignPictureInsert{Ordinal: 9}, p))
	require.Equal(t, "detail", ghostViewOf(entity.DesignPictureInsert{GhostView: "detail"}, p))

	composite := designRunParams{Layout: designLayoutOne, Views: []string{"front", "back"}}
	require.Equal(t, "", ghostViewOf(entity.DesignPictureInsert{Ordinal: 0}, composite))
}

// ─────────────────────── экспонента ретрая ───────────────────────

// РАСТЁТ И УПИРАЕТСЯ В ПОТОЛОК. Без потолка восьмая попытка ушла бы на двое суток вперёд, то
// есть задание умерло бы, не сказав об этом.
func TestNextAttemptGrowsAndIsCapped(t *testing.T) {
	now := time.Date(2026, 8, 30, 12, 0, 0, 0, time.UTC)
	require.Equal(t, now.Add(designRetryBase), designNextAttemptAt(now, 0))
	require.Equal(t, now.Add(2*designRetryBase), designNextAttemptAt(now, 1))
	require.Equal(t, now.Add(designRetryMax), designNextAttemptAt(now, 30))
}
