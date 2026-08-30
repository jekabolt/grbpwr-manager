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

// ─────────────────────── состав листа: только флэт-ось ───────────────────────

func slotOf(id int, kind, view string, rev, picture int) entity.DesignBenchSlot {
	return benchSlotKind(id, kind, view, rev, picture)
}

// ФЛЭТ ОСТАЁТСЯ — ПОЛОЖИТЕЛЬНЫЙ КОНТРОЛЬ. Без него проба ниже зеленела бы на фильтре, который
// отбрасывает ВСЁ, и «рендер не попадает на лист» было бы истинно по причине «на лист не
// попадает ничто».
func TestMintCompositionKeepsTheFlatAxis(t *testing.T) {
	got := orderMintSlots([]entity.DesignBenchSlot{
		slotOf(1, entity.DesignPictureKindFlat, entity.DesignViewFront, 1, 900),
		slotOf(2, entity.DesignPictureKindFlat, entity.DesignViewBack, 1, 901),
	})
	require.Len(t, got, 2)
	require.Equal(t, entity.DesignViewFront, got[0].ViewKey)
	require.Equal(t, entity.DesignViewBack, got[1].ViewKey)
}

// ПУСТОЙ РОД ЧИТАЕТСЯ КАК flat. Все строки, написанные до 0349, имеют именно его по DEFAULT, и
// бумага обязана продолжать печататься с них.
func TestMintCompositionTreatsAnUnnamedKindAsFlat(t *testing.T) {
	got := orderMintSlots([]entity.DesignBenchSlot{slotOf(1, "", entity.DesignViewFront, 1, 900)})
	require.Len(t, got, 1)
}

// РЕНДЕР И ТУРНТЕЙБЛ НА ТЕХНИЧЕСКИЙ ЛИСТ НЕ ПОПАДАЮТ. Это и есть тот дефект, ради которого
// фильтр существует: после 0349 `render/front` и `flat/front` — две законные строки одной
// карточки, и состав без фильтра положил бы на бумагу ту, которая нашлась раньше.
func TestMintCompositionDropsRenderAndThreedSlots(t *testing.T) {
	got := orderMintSlots([]entity.DesignBenchSlot{
		slotOf(1, entity.DesignPictureKindRender, entity.DesignViewFront, 1, 900),
		slotOf(2, entity.DesignPictureKindThreed, entity.DesignViewBack, 1, 901),
	})
	require.Empty(t, got, "рендер и турнтейбл не чертёж и на техническом листе им нечего делать")
}

// СМЕШАННЫЙ ВЕРСТАК: флэт остаётся, рендер той же стороны отброшен. Одна проба на оба
// утверждения сразу — ровно тот случай, который отличает фильтр от «пусто».
func TestMintCompositionSeparatesTheTwoAxesOfOneView(t *testing.T) {
	got := orderMintSlots([]entity.DesignBenchSlot{
		slotOf(1, entity.DesignPictureKindFlat, entity.DesignViewFront, 1, 900),
		slotOf(2, entity.DesignPictureKindRender, entity.DesignViewFront, 1, 901),
	})
	require.Len(t, got, 1)
	require.Equal(t, 1, got[0].Id, "на бумагу обязан уехать ФЛЭТ фронта, а не рендер фронта")
}

// CAS МИНТА ТОЖЕ РАЗЛИЧАЕТ ОСИ. Ожидание про `render/front` не имеет права сойтись с ревизией
// `flat/front`: иначе минт сверял бы не тот слот и «сошлось» ничего не значило бы.
func TestExpectedPlatesTellTheTwoAxesApart(t *testing.T) {
	slots := []entity.DesignBenchSlot{
		slotOf(1, entity.DesignPictureKindFlat, entity.DesignViewFront, 3, 900),
	}
	// Положительный контроль: своя ось сходится.
	require.NoError(t, casExpectedPlates(slots, []entity.DesignExpectedPlate{
		{Slot: entity.DesignSlotRef{ViewKey: entity.DesignViewFront}, SlotRev: 3},
	}))
	// Чужая ось — «слота нет», а не «ревизия совпала».
	err := casExpectedPlates(slots, []entity.DesignExpectedPlate{{
		Slot:    entity.DesignSlotRef{ViewKey: entity.DesignViewFront, Kind: entity.DesignPictureKindRender},
		SlotRev: 3,
	}})
	require.ErrorIs(t, err, entity.ErrDesignBenchMoved)
	var refusal *entity.DesignMintRefusal
	require.ErrorAs(t, err, &refusal)
	require.Equal(t, "true", refusal.Metadata["slot_gone"])
}

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

// benchSlotKind — фикстура слота с родом. Отдельно от benchSlot (mint_test.go), чтобы не менять
// подпись, которой пользуются пробы предыдущей волны.
func benchSlotKind(id int, kind, view string, rev, picture int) entity.DesignBenchSlot {
	s := benchSlot(id, view, rev, picture)
	s.Kind = kind
	return s
}
