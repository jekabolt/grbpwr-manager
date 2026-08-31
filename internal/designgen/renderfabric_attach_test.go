package designgen

import (
	"strings"
	"testing"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/stretchr/testify/require"
)

// ЧТО ЭТИ ПРОБЫ СТОРОЖАТ, И ПОЧЕМУ ИМЕННО ЭТО.
//
// V-8 владельца — «несколько тканей на изделие» — состоит из ДВУХ половин, и вторая ломалась
// молча. Первая: промпт называет ткани и говорит, какая часть какой. Вторая: текстура КАЖДОЙ ткани
// доезжает до провайдера картинкой. Пока `referenceList` прикреплял только `colour.fabric_media_id`,
// первая половина работала, а вторая нет — и прогон при этом НЕ ВРАЛ: строка второй ткани честно
// докладывала «фотография этой ткани не отправлялась». То есть отказ был читаемым и совершенно
// невидимым: человек просто не получал того, о чём просил.
//
// Поэтому здесь два разных утверждения, и оба обязаны уметь краснеть по отдельности:
//   * ВЛОЖЕНИЯ — какие media_id реально уходят (`referenceList`);
//   * СОГЛАСИЕ ПОДПИСИ СО СПИСКОМ — номер картинки в блоке «CLOTH N» указывает на ту самую ткань.
// Проба только на первое пропустила бы перепутанные номера, только на второе — пустые вложения.

// attachedIDs — какие картинки этот прогон реально отправляет, в порядке отправки.
func attachedIDs(t *testing.T, params, inputs string) []int {
	t.Helper()
	p, in := parseParams(entity.RawJSON(params)), parseInputs(entity.RawJSON(inputs))
	return referenceMediaIDs(p, in)
}

// twoClothsAttach — два ассета полки с СОБСТВЕННЫМИ текстурами (9 и 10). Первая ткань эхом повторена в
// скалярах, ровно как велит контракт (`DesignColourRecipe.fabric_media_id`).
const twoClothsAttach = `{"views":["front","back"],"layout":"one","colour":{` +
	`"hex":"#b1121a","fabric_media_id":9,"fabrics":[` +
	`{"asset_id":1,"name":"main jersey","media_id":9,"colour_hex":"#b1121a","parts":"body, sleeves"},` +
	`{"asset_id":2,"name":"contrast rib","media_id":10,"colour_hex":"#101010","parts":"collar, cuffs"}]}}`

// TestEveryStatedClothSendsItsOwnTexture — ГЛАВНАЯ проба этой починки.
func TestEveryStatedClothSendsItsOwnTexture(t *testing.T) {
	got := attachedIDs(t, twoClothsAttach, renderSlots)
	require.Equal(t, []int{1, 2, 9, 10}, got,
		"обе ткани обязаны уехать картинками: до починки было [1 2 9] — вторая ткань называлась в промпте, но её текстура не отправлялась вовсе")
}

// TestSingleClothAttachesExactlyWhatItAlwaysDid — ЗАМОК НА СОВМЕСТИМОСТЬ.
//
// Одноткаными остаются и все замороженные прогоны, и обычная новая подача. Композированный промпт
// пишется в историю, поэтому смена ВЛОЖЕНИЙ переномеровала бы каждую ссылку «image N» после неё.
func TestSingleClothAttachesExactlyWhatItAlwaysDid(t *testing.T) {
	one := `{"views":["front"],"layout":"one","colour":{"hex":"#b1121a","fabric_media_id":9,` +
		`"fabrics":[{"asset_id":1,"name":"main jersey","media_id":9}]}}`
	legacy := `{"views":["front"],"layout":"one","colour":{"hex":"#b1121a","fabric_media_id":9}}`

	require.Equal(t, attachedIDs(t, legacy, renderSlots), attachedIDs(t, one, renderSlots),
		"одна ткань обязана прикреплять ровно то же, что прикреплял прогон без списка тканей")
	require.Equal(t, []int{1, 2, 9}, attachedIDs(t, one, renderSlots))
}

// TestSingleClothKeepsTheOldCaptionWordForWord — вторая половина того же замка: не только КАКИЕ
// картинки, но и КАКИМИ СЛОВАМИ они подписаны. Подпись входит в промпт, промпт входит в историю.
func TestSingleClothKeepsTheOldCaptionWordForWord(t *testing.T) {
	one := `{"views":["front"],"layout":"one","colour":{"hex":"#b1121a","fabric_media_id":9,` +
		`"fabrics":[{"asset_id":1,"name":"main jersey","media_id":9}]}}`
	p, in := parseParams(entity.RawJSON(one)), parseInputs(entity.RawJSON(renderSlots))
	list := referenceList(p, in)
	require.Len(t, list, 3)
	require.Equal(t,
		"fabric photograph — the material this garment is made of: read its weave, texture, sheen and drape from here",
		list[2].Caption,
		"подпись одинокой ткани — та же, что была до волны: её читает история")
}

// TestMultiClothCaptionsNameTheClothAndAgreeWithTheList — согласие двух половин.
//
// Блок ремесла говорит «CLOTH 2 … its texture is image N»; подпись вложения говорит «CLOTH 2».
// Обе нумерации выведены из одного и того же списка, и проба это ПРОВЕРЯЕТ, а не надеется:
// перепутанный индекс в цикле подписи прошёл бы любую проверку «текст содержит CLOTH 2».
func TestMultiClothCaptionsNameTheClothAndAgreeWithTheList(t *testing.T) {
	p, in := parseParams(entity.RawJSON(twoClothsAttach)), parseInputs(entity.RawJSON(renderSlots))
	list := referenceList(p, in)

	byID := map[int]string{}
	for _, rc := range list {
		byID[rc.MediaID] = rc.Caption
	}
	require.Contains(t, byID[9], "CLOTH 1 — main jersey")
	require.Contains(t, byID[10], "CLOTH 2 — contrast rib")
	require.NotContains(t, byID[9], "CLOTH 2")
	require.NotContains(t, byID[10], "CLOTH 1")

	// И НОМЕР КАРТИНКИ В ПРОМПТЕ УКАЗЫВАЕТ ТУДА ЖЕ. `imageNumberOf` считает по `list`, значит
	// ткань 2 — третья картинка (1, 2, 9, 10 → 9 это #3, 10 это #4).
	require.Equal(t, 3, imageNumberOf(list, 9))
	require.Equal(t, 4, imageNumberOf(list, 10))
}

// TestMultiClothDoesNotDescribeOnePictureTwice — тихий дефект дедупликации.
//
// Скаляр `fabric_media_id` это ЭХО первой ткани. Прикрепить его ВДОБАВОК к списку значило бы
// найти id уже стоящим и ПРИКЛЕИТЬ вторую подпись к первой ткани: одна картинка, описанная и как
// «CLOTH 1», и как «the material this garment is made of», то есть ровно то расхождение подписи и
// правила, ради устранения которого подпись однажды переписывали.
func TestMultiClothDoesNotDescribeOnePictureTwice(t *testing.T) {
	p, in := parseParams(entity.RawJSON(twoClothsAttach)), parseInputs(entity.RawJSON(renderSlots))
	for _, rc := range referenceList(p, in) {
		if rc.MediaID != 9 {
			continue
		}
		require.NotContains(t, rc.Caption, "the material this garment is made of",
			"эхо-скаляр не должен приклеивать вторую подпись к первой ткани")
		require.Equal(t, 1, strings.Count(rc.Caption, "fabric photograph"),
			"одна картинка — одна подпись")
	}
}

// TestClothWithoutATextureClaimsNoPicture — ткань без своего файла не занимает номер картинки и
// не крадёт чужой. Молчание здесь честнее догадки: соседняя фотография была бы прочитана как её.
func TestClothWithoutATextureClaimsNoPicture(t *testing.T) {
	mixed := `{"views":["front"],"layout":"one","colour":{"hex":"#b1121a","fabric_media_id":9,` +
		`"fabrics":[{"name":"main jersey","media_id":9,"parts":"body"},` +
		`{"name":"unphotographed rib","parts":"collar"}]}}`
	got := attachedIDs(t, mixed, renderSlots)
	require.Equal(t, []int{1, 2, 9}, got, "ткань без media_id не прикрепляет ничего")

	p, in := parseParams(entity.RawJSON(mixed)), parseInputs(entity.RawJSON(renderSlots))
	require.Equal(t, 0, imageNumberOf(referenceList(p, in), 0))
}
