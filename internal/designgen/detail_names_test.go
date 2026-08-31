package designgen

import (
	"testing"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/stretchr/testify/require"
)

// T-5: «Галка на каждую описанную деталь» — каждая едет своей картинкой в свой слот.
//
// Проба стережёт ровно то, чего не было: ИМЯ. `views` несёт ключ вида, а ключ вида не различает
// воротник и карман, поэтому прогон на две детали говорил модели «нарисуй две детали» и получал
// два произвольных крупных плана. Проверяются три разных утверждения, и каждое умеет быть ложным:
//   - имена доезжают в промпт и в ТОМ ЖЕ ПОРЯДКЕ, в каком их просили (порядок — тот же, которым
//     разрезчик подписывает кадры склеенного листа, поэтому пересортировка не косметика);
//   - имя берётся из ЗАМОРОЖЕННОГО снимка, а не из нынешнего состояния карточки;
//   - неизвестный слот не выдумывает соседнее имя.
func TestPromptNamesTheDetailsItWasAskedFor(t *testing.T) {
	p := runParams{Views: []string{"detail", "front", "detail"}, DetailSlotIDs: []int{7, 9}}
	in := runInputs{Slots: []inputSlot{
		{ViewKey: "detail", SlotID: 7, DetailName: "collar"},
		{ViewKey: "detail", SlotID: 9, DetailName: "patch pocket"},
		{ViewKey: "front", SlotID: 0},
	}}

	got := requestedDetailNames(p, in)
	require.Equal(t, "collar, patch pocket", got,
		"обе просимые детали обязаны быть названы поимённо и в порядке просьбы")
}

func TestPromptKeepsTheOrderTheRunAskedIn(t *testing.T) {
	// Тот же набор, обратный порядок просьбы. Если функция сортирует или ходит по слотам, а не по
	// просьбе, здесь она даст ту же строку, что и выше, — и подпись кадров разреза разъедется.
	p := runParams{Views: []string{"detail", "detail"}, DetailSlotIDs: []int{9, 7}}
	in := runInputs{Slots: []inputSlot{
		{ViewKey: "detail", SlotID: 7, DetailName: "collar"},
		{ViewKey: "detail", SlotID: 9, DetailName: "patch pocket"},
	}}
	require.Equal(t, "patch pocket, collar", requestedDetailNames(p, in))
}

func TestUnknownDetailSlotSaysDetailRatherThanGuessing(t *testing.T) {
	// Снимок мог быть записан до того, как слоты в него попали, или деталь удалили вместе со
	// слотом. Молчание честнее догадки: подставленное соседнее имя заставило бы модель нарисовать
	// НЕ ТУ деталь, и притом уверенно.
	p := runParams{Views: []string{"detail", "detail"}, DetailSlotIDs: []int{7, 404}}
	in := runInputs{Slots: []inputSlot{{ViewKey: "detail", SlotID: 7, DetailName: "collar"}}}
	require.Equal(t, "collar, detail", requestedDetailNames(p, in))
}

func TestRunWithoutDetailsSaysNothingAboutThem(t *testing.T) {
	// Пустая строка означает, что блок не пишется вовсе. Заголовок «draw these details» без
	// содержимого был бы указанием нарисовать ничто.
	p := runParams{Views: []string{"front", "back"}}
	require.Equal(t, "", requestedDetailNames(p, runInputs{}))
}

// ПРОВОДКА, А НЕ ХЕЛПЕР. Три пробы выше зовут функцию НАПРЯМУЮ и потому зеленеют даже тогда,
// когда её никто не вызывает из сборки промпта. Эта идёт через `composePrompt`.
func TestComposedPromptCarriesTheDetailNames(t *testing.T) {
	run := entity.DesignRun{
		Id:     1,
		Kind:   entity.DesignRunKindFlat,
		Params: entity.RawJSON(`{"views":["detail","detail"],"layout":"one","detail_slot_ids":[7,9]}`),
		Inputs: entity.RawJSON(`{"garment_note":"a shirt","slots":[` +
			`{"view_key":"detail","slot_id":7,"detail_name":"collar"},` +
			`{"view_key":"detail","slot_id":9,"detail_name":"patch pocket"}]}`),
	}
	p := parseParams(run.Params)
	in := parseInputs(run.Inputs)
	out := composePrompt(run, p, in, nil)
	require.Contains(t, out, "draw these details:",
		"блок обязан быть в СОБРАННОМ промпте, а не только в функции, которую никто не зовёт")
	require.Contains(t, out, "collar, patch pocket")
}
