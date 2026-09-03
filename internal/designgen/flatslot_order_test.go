package designgen

import (
	"testing"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/stretchr/testify/require"
)

// ═══ ГДЕ В ПРОМПТЕ СТОЯТ ПЛИТЫ ВЕРСТАКА — J-10 ═════════════════════════════════════════════════
//
// Правило владельца про плиты, которые флэт-прогон берёт с верстака по просьбе: «так же они всегда
// добавляются в конец промпта». Это утверждение о ПРОМПТЕ, поэтому проверяется оно на том самом
// списке, который промпт и нумерует, — `referenceList`.
//
// ЧТО БЫЛО. Обход плит шёл ПЕРВЫМ на всяком маршруте, и флэт-прогон с `use_flat_slots` нумеровал
// плиты `image 1…k` ВПЕРЕДИ референсов, которые человек принёс. Экран при этом печатал номер
// плиты как «после последнего референса», то есть экран и промпт расходились в том, какая картинка
// называется «image 3».
//
// ⚠ ПОЧЕМУ ТАБЛИЦА ПЕРЕЧИСЛЯЕТ ВСЕ РОДЫ, А НЕ ТОЛЬКО ФЛЭТ. Переворот обязан быть УЗКИМ: на 3D
// первый url ЕСТЬ вид спереди (Meshy читает его так), поэтому «референсы вперёд» отдало бы сборке
// чужую фотографию настроения как фронт изделия. Проба, утверждающая только флэт, покраснела бы и
// на правке, которая перевернула порядок ВЕЗДЕ, — но покраснела бы одна, и разницу было бы не
// видно. Здесь каждый род называет свой ожидаемый порядок сам.

// twoRefsTwoPlates — два референса человека и две плиты верстака. Медиа различимы намеренно:
// перепутанный источник виден по номеру, а не по длине списка.
const twoRefsTwoPlates = `{"refs":[{"media_id":11,"role":"front","note":"NOTE-a"},` +
	`{"media_id":12,"role":"back","note":"NOTE-b"}],` +
	`"slots":[{"view_key":"front","media_id":21},{"view_key":"back","media_id":22}]}`

func orderOf(t *testing.T, kind, params, inputs string) []int {
	t.Helper()
	p, in := parseParams(entity.RawJSON(params)), parseInputs(entity.RawJSON(inputs))
	out := make([]int, 0, 4)
	for _, rc := range referenceList(kind, p, in) {
		out = append(out, rc.MediaID)
	}
	return out
}

func TestFlatPutsThePlatesAfterTheReferencesAndTheOtherRoutesDoNot(t *testing.T) {
	const params = `{"views":["front","back"],"layout":"one"}`

	cases := []struct {
		name string
		kind string
		want []int
	}{
		{
			// ФЛЭТ — ЕДИНСТВЕННЫЙ ПЕРЕВЁРНУТЫЙ МАРШРУТ. Референсы 11, 12 занимают image 1 и 2,
			// плиты 21, 22 — image 3 и 4, то есть «в конец промпта», как и сказано.
			name: "flat sends the operator's references first and the bench plates last",
			kind: entity.DesignRunKindFlat,
			want: []int{11, 12, 21, 22},
		},
		{
			// РЕНДЕР НЕ ТРОНУТ: его промпты заморожены в истории, и перенумерация подписей
			// переписала бы смысл каждого будущего прогона рода, который никто не просил менять.
			name: "render keeps the plates first, byte for byte as before",
			kind: entity.DesignRunKindRender,
			want: []int{21, 22, 11, 12},
		},
		{
			// 3D НЕ ТРОНУТ ПО СИЛЬНОМУ ДОВОДУ: Meshy читает ПЕРВЫЙ url как вид спереди. Референс,
			// вставший первым, стал бы фронтом изделия — и модель построилась бы по чужой одежде.
			name: "threed keeps the plates first because the first url IS the front view",
			kind: entity.DesignRunKindThreed,
			want: []int{21, 22, 11, 12},
		},
		{
			name: "vector keeps the plates first",
			kind: entity.DesignRunKindVector,
			want: []int{21, 22, 11, 12},
		},
		{
			name: "recolor keeps the plates first",
			kind: entity.DesignRunKindRecolor,
			want: []int{21, 22, 11, 12},
		},
		{
			name: "pattern keeps the plates first",
			kind: entity.DesignRunKindPattern,
			want: []int{21, 22, 11, 12},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			require.Equal(t, c.want, orderOf(t, c.kind, params, twoRefsTwoPlates))
		})
	}
}

// ПОДПИСЬ ЕДЕТ СО СВОЕЙ КАРТИНКОЙ И ПОСЛЕ ПЕРЕВОРОТА.
//
// ⚠ ЭТО НЕ ПОВТОР ПРОБЫ ВЫШЕ. Та держит ПОРЯДОК media_id; эта — что k-я подпись описывает k-ю
// картинку. Перепутать их можно независимо: обход, собирающий id в одном цикле, а подписи в
// другом, прошёл бы первую пробу целиком и соврал бы модели в каждой строке.
func TestFlatOrderKeepsEachCaptionOnItsOwnPicture(t *testing.T) {
	p, in := parseParams(entity.RawJSON(`{"views":["front"],"layout":"one"}`)),
		parseInputs(entity.RawJSON(twoRefsTwoPlates))
	list := referenceList(entity.DesignRunKindFlat, p, in)
	require.Len(t, list, 4)

	require.Equal(t, 11, list[0].MediaID)
	require.Contains(t, list[0].Caption, "NOTE-a")
	require.Equal(t, 12, list[1].MediaID)
	require.Contains(t, list[1].Caption, "NOTE-b")

	require.Equal(t, 21, list[2].MediaID)
	require.Contains(t, list[2].Caption, "current state of the garment",
		"плита обязана остаться плитой по словам, а не только по позиции")
	require.Equal(t, entity.DesignViewFront, list[2].View,
		"вид плиты едет с ней — на нём держится «фронт/бэк» каждой картинки")
	require.Equal(t, 22, list[3].MediaID)
	require.Contains(t, list[3].Caption, "current state of the garment")
}

// ПЕРЕВОРОТ НЕ ЛОМАЕТ ДЕДУПЛИКАЦИЮ, И ЭТО ОТДЕЛЬНОЕ УТВЕРЖДЕНИЕ.
//
// Правило `add` несущее: одна и та же картинка, названная двумя источниками, стоит на ПЕРВОЙ своей
// позиции и склеивает подписи. На флэте «первым источником» теперь стали референсы — значит
// картинка, которая И плита, И референс, встаёт среди референсов и обязана унести с собой ОБЕ
// подписи. Пропажа второй половины была бы молчаливой: длина списка не изменилась бы.
func TestFlatOrderStillDedupesAndKeepsBothCaptions(t *testing.T) {
	const both = `{"refs":[{"media_id":21,"role":"front","note":"NOTE-mine"}],` +
		`"slots":[{"view_key":"front","media_id":21}]}`
	p, in := parseParams(entity.RawJSON(`{"views":["front"],"layout":"one"}`)),
		parseInputs(entity.RawJSON(both))

	list := referenceList(entity.DesignRunKindFlat, p, in)
	require.Len(t, list, 1, "одна картинка — одна позиция, сколько бы источников её ни назвало")
	require.Equal(t, 21, list[0].MediaID)
	require.Contains(t, list[0].Caption, "NOTE-mine")
	require.Contains(t, list[0].Caption, "current state of the garment",
		"вторая половина подписи не должна теряться при дедупликации")
}

// ПУСТОЙ ВЕРСТАК НИЧЕГО НЕ МЕНЯЕТ. Обычный флэт-прогон — без `use_flat_slots`, то есть вовсе без
// плит в снимке — обязан нумеровать свои референсы ровно как до волны. Это положительный контроль
// того, что переворот не тронул общий случай.
func TestFlatWithoutPlatesNumbersItsReferencesExactlyAsBefore(t *testing.T) {
	const refsOnly = `{"refs":[{"media_id":11,"role":"front"},{"media_id":12,"role":"back"}]}`
	require.Equal(t, []int{11, 12},
		orderOf(t, entity.DesignRunKindFlat, `{"views":["front"],"layout":"one"}`, refsOnly))
}

// ⚠ ВЫБОРОЧНАЯ ПРАВКА ФЛЭТА НЕ ПЕРЕВОРАЧИВАЕТСЯ, И ЭТО НЕ ОСТОРОЖНОСТЬ (F2).
//
// `designSelectBench` УЖЕ отказывается сужать выборочный прогон списком `flat_slot_ids` и говорит
// почему: «правка названных плит уже сужена своими fix_targets/fix_slot_ids, два независимых
// сужения одного списка разошлись бы». Переворот порядка — сужение того же рода, и он обязан
// подчиняться той же границе; иначе одна половина волны исключает выборочный путь, а вторая молча
// его переписывает.
//
// ЧТО ЛОМАЛОСЬ. Гейт K-1 («флэт не берёт плиты, пока не попросят») обходится признаком
// `selective`, поэтому выборочный флэт-фикс ПЛИТЫ НЕСЁТ. Прогон, который говорит «поправь вид
// front» и открывается словами «Turn the garment shown in the reference image into a technical
// flat sketch», после переворота вёл СНИМКОМ НАСТРОЕНИЯ, а плиту, на которую его наводили,
// нумеровал второй — на платном вызове.
func TestSelectiveFlatFixKeepsItsSubjectFirst(t *testing.T) {
	const oneRefOnePlate = `{"refs":[{"media_id":11,"role":"front","note":"mood"}],` +
		`"slots":[{"view_key":"front","media_id":21}]}`

	cases := []struct {
		name   string
		params string
		want   []int
	}{
		{
			// ВЫБОРОЧНО ПО ВИДУ.
			name:   "fix_targets names the view — the plate under repair leads",
			params: `{"views":["front"],"layout":"one","fix_targets":["front"]}`,
			want:   []int{21, 11},
		},
		{
			// ВЫБОРОЧНО ПО СЛОТУ. Второе правописание того же сужения; читать их надо одинаково,
			// иначе прогон меняет смысл от того, каким полем его сузили.
			name:   "fix_slot_ids names the slot — same rule, second spelling",
			params: `{"views":["front"],"layout":"one","fix_slot_ids":[3]}`,
			want:   []int{21, 11},
		},
		{
			// СТАРОЕ СКАЛЯРНОЕ ПРАВОПИСАНИЕ — тоже выборочный путь.
			name:   "the legacy scalar fix_target counts as selective too",
			params: `{"views":["front"],"layout":"one","fix_target":"front"}`,
			want:   []int{21, 11},
		},
		{
			// ПОЛОЖИТЕЛЬНЫЙ КОНТРОЛЬ: обычный флэт с просьбой о плитах ПЕРЕВОРАЧИВАЕТСЯ. Без него
			// три утверждения выше зеленели бы и на правке, которая убрала переворот целиком.
			name:   "a plain flat run with use_flat_slots still flips",
			params: `{"views":["front"],"layout":"one"}`,
			want:   []int{11, 21},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			require.Equal(t, c.want, orderOf(t, entity.DesignRunKindFlat, c.params, oneRefOnePlate))
		})
	}
}

// ПЕРЕВОРОТ НЕ ТЕРЯЕТ СТОРОНУ ПЛИТЫ (F6).
//
// ⚠ ЭТО НЕ ТО ЖЕ, ЧТО ПРОБА ПРО ПОДПИСИ. Та держит СЛОВА склеенной строки; эта — поле `View`,
// которым картинка называет СТОРОНУ ИЗДЕЛИЯ. Потерять можно только его: длина списка, порядок и
// обе подписи остаются правильными, поэтому пропажа абсолютно молчалива.
//
// ЧТО ЛОМАЛОСЬ. Правило дедупликации оставляет ПЕРВУЮ позицию, а с ней и первую сторону. Пока
// плиты шли первыми, это было верно — плита приходила уже со своей стороной. После переворота
// первой стала ссылка, у которой стороны нет ВООБЩЕ, и плита, добавленная второй, свою сторону
// теряла. Комментарий у самого места при этом продолжал утверждать обратное.
func TestFlatDedupKeepsThePlatesSide(t *testing.T) {
	const both = `{"refs":[{"media_id":21,"role":"front","note":"NOTE-mine"}],` +
		`"slots":[{"view_key":"front","media_id":21}]}`
	const params = `{"views":["front"],"layout":"one"}`

	p, in := parseParams(entity.RawJSON(params)), parseInputs(entity.RawJSON(both))

	flat := referenceList(entity.DesignRunKindFlat, p, in)
	require.Len(t, flat, 1)
	require.Equal(t, entity.DesignViewFront, flat[0].View,
		"на флэте ссылка идёт первой и стороны не несёт — сторону обязана донести плита")

	// ⚠ КОНТРОЛЬ РАВЕНСТВА МАРШРУТОВ: рендер отвечал на этот вопрос правильно всегда, и починка
	// флэта обязана привести их к ОДНОМУ ответу, а не завести второй.
	render := referenceList(entity.DesignRunKindRender, p, in)
	require.Len(t, render, 1)
	require.Equal(t, flat[0].View, render[0].View,
		"одна и та же картинка обязана называть одну и ту же сторону на обоих маршрутах")

	// И СТОРОНА, УЖЕ НАЗВАННАЯ ПЛИТОЙ, НЕ ПЕРЕПИСЫВАЕТСЯ ВТОРЫМ ИСТОЧНИКОМ: ссылка стороны не
	// предлагает вовсе, поэтому заполнить пустое она может, а затереть занятое — нет.
	require.Equal(t, entity.DesignViewFront, render[0].View)
}
