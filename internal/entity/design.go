package entity

import (
	"database/sql"
	"database/sql/driver"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/shopspring/decimal"
)

// RawJSON — СЫРАЯ JSON-КОЛОНКА, ПЕРЕЖИВАЮЩАЯ NULL.
//
// ЗАЧЕМ ОТДЕЛЬНЫЙ ТИП, А НЕ json.RawMessage. json.RawMessage сканером НЕ является, а
// database/sql умеет положить NULL только в тип, который либо реализует sql.Scanner, либо
// является *[]byte / *any / *sql.RawBytes. Именованный слайс под это не подходит, поэтому
// `json.RawMessage` в db-структуре роняет чтение на КАЖДОЙ строке, где колонка пуста:
// «struct scan: unsupported Scan, storing driver.Value type <nil> into type *json.RawMessage».
//
// А ПУСТА ОНА В НОРМЕ, И ЭТО НЕ КРАЙ. `composite_views` не пишет НИКТО (NULL = одиночная плита,
// то есть каждая загруженная картинка); `params`/`inputs` пусты у строки, заведённой не
// провайдером; `strokes` — у только что созданного слоя; `annotation` — у выноски без геометрии.
// Отказ приходит не пустым полем, а обрывом чтения в середине — то есть выглядит как поломка
// стора, а не как «нечего показать».
//
// СОДЕРЖАНИЕ ЭТОТ ТИП НЕ ТРОГАЕТ, ровно как не трогал json.RawMessage: JSON внутри — контракт, а
// не схема. Пустое значение уезжает в базу как NULL, а не как строка «null»: два способа сказать
// «ничего» в одной колонке — это то, ради чего колонка объявлена NULLable.
type RawJSON []byte

// Scan принимает NULL, []byte и string — три формы, в которых MySQL-драйвер отдаёт JSON-колонку.
func (r *RawJSON) Scan(src any) error {
	switch v := src.(type) {
	case nil:
		*r = nil
	case []byte:
		// КОПИЯ, А НЕ ССЫЛКА: буфер драйвера переиспользуется между строками, и сохранённый
		// слайс показал бы содержимое СЛЕДУЮЩЕЙ строки.
		*r = append(RawJSON(nil), v...)
	case string:
		*r = RawJSON(v)
	default:
		return fmt.Errorf("cannot scan %T into RawJSON", src)
	}
	return nil
}

// Value отдаёт NULL на пустом значении — см. довод в шапке типа.
func (r RawJSON) Value() (driver.Value, error) {
	if len(r) == 0 {
		return nil, nil
	}
	return []byte(r), nil
}

// MarshalJSON / UnmarshalJSON — ЧТОБЫ ТИП БЫЛ ТЕМ, ЧЕМ СЕБЯ НАЗЫВАЕТ.
//
// `json.RawMessage` печатает СЫРЫЕ байты; голый именованный `[]byte` печатает **base64**. Шапка
// выше подаёт RawJSON как замену json.RawMessage, и в отношении сериализации без этих двух методов
// он ею НЕ является. Сегодня недостижимо — ни один `json.Marshal` этих структур не касается, — но
// первый, кто сложит прогон в снапшот, лог или экспорт, получит base64 молча, и молчание тут самое
// дорогое: JSON останется валидным, а содержимое станет нечитаемым для всех, кто его ждёт.
func (r RawJSON) MarshalJSON() ([]byte, error) {
	if len(r) == 0 {
		return []byte("null"), nil
	}
	return r, nil
}

func (r *RawJSON) UnmarshalJSON(data []byte) error {
	if r == nil {
		return errors.New("entity.RawJSON: UnmarshalJSON on nil pointer")
	}
	*r = append((*r)[0:0], data...)
	return nil
}

// Полоса DESIGN — студийная половина тех-карты: прогоны генерации, картинки, которые они
// произвели, и верстак (какая плита принята какой стороной изделия).
//
// ЧТО ЭТИ ТИПЫ ЕСТЬ И ЧЕМ НЕ ЯВЛЯЮТСЯ. Это строки таблиц 0340–0352 плюс несколько запросных
// структур. Ни один из них не знает про protobuf: конверсия живёт в
// internal/apisrv/admin/design_band.go, а стор остаётся чистым от провода. JSON-колонки
// (`params`, `inputs`, `composite_views`, `strokes`, `annotation`) едут сквозь стор как
// RawJSON — стор их не разбирает, потому что их СОДЕРЖАНИЕ это контракт, а не схема. Тип свой, а
// не json.RawMessage, по одной причине: все пять колонок NULLable, а json.RawMessage не Scanner
// и на NULL роняет чтение. См. шапку RawJSON.
//
// ⚠ ОДНО СОГЛАШЕНИЕ, КОТОРОЕ ОБЯЗАНО ПЕРЕЖИТЬ ВОЛНУ 2. `design_run.params` и
// `design_run.inputs` хранят protojson с UseProtoNames: true, то есть snake_case ключами
// (`$.colour`, `$.slots[*].media_id`, `$.extra_input_media_ids`). SQL-пути в этом пакете —
// сторож HidePicture и сборка чипов истории цвета — написаны по этим именам. protojson по
// умолчанию пишет lowerCamelCase, и переключение писателя на дефолт сделает оба запроса молча
// пустыми: сторож перестанет отказывать, чипы перестанут появляться. Тесты этого не поймают,
// потому что пустой результат — законное состояние карточки без прогонов.
const (
	// DesignRunJSONFieldColour — путь до рецепта цвета внутри design_run.params.
	DesignRunJSONFieldColour = "$.colour"
	// DesignInputsJSONSlotMedia / DesignInputsJSONRefMedia — пути до медиа снимка входов.
	DesignInputsJSONSlotMedia = "$.slots[*].media_id"
	DesignInputsJSONRefMedia  = "$.refs[*].media_id"
	// DesignParamsJSONExtraMedia — дополнительные входы рендера.
	DesignParamsJSONExtraMedia = "$.extra_input_media_ids"
)

// Словари полосы. CHECK в схеме намеренно нет (словарь растёт), поэтому проверяет их Go — и
// отказ называет значение, а не отдаёт сырой 3819 с именем колонки.
const (
	DesignViewFront  = "front"
	DesignViewBack   = "back"
	DesignViewSideL  = "side_l"
	DesignViewSideR  = "side_r"
	DesignViewDetail = "detail"
)

// DesignSilhouetteViews — четыре стороны, которые рождаются лениво первым касанием и
// адресуются по view_key. Деталь в этот список НЕ входит: она адресуется своим id.
var DesignSilhouetteViews = []string{DesignViewFront, DesignViewBack, DesignViewSideL, DesignViewSideR}

// IsDesignSilhouetteView сообщает, адресуема ли сторона по view_key.
func IsDesignSilhouetteView(v string) bool {
	for _, s := range DesignSilhouetteViews {
		if s == v {
			return true
		}
	}
	return false
}

// IsDesignGhostView сообщает, законна ли догадка о виде у загруженного файла либо у кадра
// разреза. Четыре стороны плюс `detail`; пустая строка (догадки нет) законна отдельно.
func IsDesignGhostView(v string) bool {
	return v == DesignViewDetail || IsDesignSilhouetteView(v)
}

// IsDesignSelectiveFix — СУЖЕН ЛИ ПРОГОН ДО НАЗВАННЫХ ПЛИТ, то есть правка ли это конкретных
// кадров, а не обычный прогон рода.
//
// ⚠ ЖИВЁТ ЗДЕСЬ, ПОТОМУ ЧТО ЧИТАТЕЛЕЙ ДВА, А ОТВЕТ ОБЯЗАН БЫТЬ ОДИН. Признак спрашивают отбор
// плит верстака (apisrv/admin: какие плиты уедут) и сборщик ссылок промпта (designgen: в каком
// порядке они там встанут). Два независимых прочтения «сужен ли прогон» разошлись бы ровно на
// выборочном прогоне — то есть там, где человек сузил намеренно и где ошибка дороже всего, — и
// разошлись бы молча. Одна волна это уже допустила: отбор выборочный путь исключал, а порядок
// переворачивал.
//
// ТРИ ПРАВОПИСАНИЯ ОДНОГО СУЖЕНИЯ, и все три равноправны: список видов, скалярный вид (легаси) и
// список слотов. Прогон не имеет права менять смысл от того, каким из них его сузили.
func IsDesignSelectiveFix(fixTarget string, fixTargets []string, fixSlotIDs []int) bool {
	return fixTarget != "" || len(fixTargets) > 0 || len(fixSlotIDs) > 0
}

// Провенанс кадра. СЛОВАРЬ ВЗЯТ С ПРОВОДА (common.DesignPicture.source_class:
// ai | uploaded | ai_edits | imported_svg | drawn), а не из комментария 0340, который называет
// generated|uploaded|drawn|derived. Расхождение реально, и разрешено в пользу контракта: именно
// эти строки клиент рисует человеку, а комментарий миграции — проза без CHECK.
const (
	DesignSourceAI          = "ai"
	DesignSourceUploaded    = "uploaded"
	DesignSourceAIEdits     = "ai_edits"
	DesignSourceImportedSVG = "imported_svg"
	DesignSourceDrawn       = "drawn"
)

// ЧЕМ КАДР ПРОИЗВЕДЁН ОТ РОДИТЕЛЯ (0359, J-1/J-23): разрезом или сплющенной правкой.
//
// ⚠ ЭТО НЕ ВТОРОЕ ИМЯ ПРОВЕНАНСА. `source_class` отвечает «ОТКУДА пиксели» (машина, рука, чужой
// файл), derivation — «КАКИМ ЖЕСТОМ эта строка отделилась от родительской». Кроп машинного листа
// и флэттен машинного листа несут ОДИН source_class и РАЗНЫЕ глаголы, и лента складывается в
// колоду ровно по глаголу.
//
// ⚠ И ЭТО НЕ `layer_rev`. Кроп КОПИРУЕТ ревизию родителя (pictures.go), поэтому кроп
// отредактированного листа неотличим по ней от флэттена; обещание контракта «0 = not flattened»
// сломано этим наследованием и было сломано ещё до того, как эта колонка появилась.
const (
	// DesignDerivationNone — «глагол не назван». Читается ТОЛЬКО в паре с DerivedFrom: пустой
	// родитель = корень, непустой = легаси-строка, которую бэкфилл 0359 не смог классифицировать
	// (родителя уже нет). Схлопывать эти два смысла нельзя — см. шапку 0359.
	DesignDerivationNone = ""
	// DesignDerivationCrop — кадр вырезан из родителя (SplitPicture).
	DesignDerivationCrop = "crop"
	// DesignDerivationFlatten — кадр это сплющенная правка родителя (FlattenEditLayer с базой).
	DesignDerivationFlatten = "flatten"
)

// Виды кадра. ОН ЖЕ СЛОВАРЬ ВТОРОЙ ОСИ ВЕРСТАКА: design_bench_slot.kind (0349) объявлен тем же
// словарём намеренно — «род» у слота и у кадра обязан быть одним понятием, иначе рендер встанет
// на технический лист.
const (
	DesignPictureKindFlat   = "flat"
	DesignPictureKindRender = "render"
	DesignPictureKindThreed = "threed"
	// DesignPictureKindPattern — ПОВТОРЯЕМАЯ ПЛИТКА, выход прогона рода `pattern` (K-13).
	//
	// ⚠ ОНА НЕ ФЛЭТ И НЕ РЕНДЕР, И ОБА ЭТИХ ИМЕНИ БЫЛИ БЫ ЛОЖЬЮ С ПОСЛЕДСТВИЯМИ, а не неточностью.
	// Названная флэтом, плитка попадает в список, из которого человек ставит кадр в СЛОТ ВЕРСТАКА,
	// и «перёд изделия» оказывается куском ткани. Названная рендером — открывает ворота W-13
	// («3D только после fabric render»), то есть карточка, на которой сгенерили только обои,
	// начинает считаться готовой к сборке 3D. Своё имя не стоит ничего и закрывает оба случая.
	DesignPictureKindPattern = "pattern"
)

// IsDesignPictureKind сообщает, известен ли род кадра. Словарь растёт, CHECK в схеме намеренно
// нет — поэтому проверяет Go, и отказ называет значение, а не отдаёт сырой 1265.
func IsDesignPictureKind(v string) bool {
	switch v {
	case DesignPictureKindFlat, DesignPictureKindRender, DesignPictureKindThreed,
		DesignPictureKindPattern:
		return true
	}
	return false
}

// IsDesignBenchKind сообщает, законен ли род как ВТОРАЯ ОСЬ ВЕРСТАКА (design_bench_slot.kind).
//
// ⚠ ЭТО НЕ РАСЩЕПЛЕНИЕ СЛОВАРЯ НАДВОЕ, А ЕГО СУЖЕНИЕ В ОДНУ СТОРОНУ, и разница проверяется одним
// вопросом: «может ли член попасть в чужую половину молча?». Верстак держит СОСТОЯНИЕ ИЗДЕЛИЯ по
// сторонам силуэта, и всякий его род обязан быть родом кадра — обратное неверно с появлением
// `pattern`: плитка обоев это кадр карточки, но не состояние изделия с какой-либо стороны.
//
// До этой волны функция была бы посимвольной копией IsDesignPictureKind, поэтому её и не было. Она
// заводится ровно в тот момент, когда словарь кадров вырос на члена, которому в верстаке места
// нет, — и список здесь ТОТ ЖЕ, что верстак принимал вчера, то есть ни одна существующая строка не
// меняет смысла.
func IsDesignBenchKind(v string) bool {
	switch v {
	case DesignPictureKindFlat, DesignPictureKindRender, DesignPictureKindThreed:
		return true
	}
	return false
}

// DesignKindOrFlat — ПУСТОЕ ЧИТАЕТСЯ КАК flat, ровно как DEFAULT 'flat' в 0349. Один способ
// сказать «род не назван» на всех трёх ярусах (провод, Go, схема): иначе строка, написанная
// старым клиентом, и строка, написанная новым, попали бы в РАЗНЫЕ слоты одного адреса.
func DesignKindOrFlat(v string) string {
	if v == "" {
		return DesignPictureKindFlat
	}
	return v
}

// ───────────────────────── ось колорвея (0356, L-2/L-3) ─────────────────────────
//
// Модель владельца дословно: «флеты одна разметка … у фабрик рендера должно быть так 1 колорвей
// там должно быть мультивью которое мы генерим + из его нарезаем сплитом стороны размеченные и
// на каждый колорвей так и потом мы в 3д рендере уже выбираем колорвей который будем рендерить».
// Флэт колорвея не имеет ПО СУЩЕСТВУ; рендер и 3D-кадр — атрибутируются колорвею; NULL у
// рендера читается как «до оси / не атрибутирован», и это законное состояние навсегда.

// DesignBenchExclusiveKey — СТРОКА ЭКСКЛЮЗИВНОСТИ адреса стороны на верстаке. exclusive_key и
// задуман (0341) как строка, называющая «что ровно одно на карточке»: у флэтовой стороны это сам
// вид, у детали — минтованный uuid, у КОЛОРВЕЙНОЙ стороны — пара вид+колорвей. Ключ
// uq_design_bench_view (tech_card_id, kind, exclusive_key) при этом не перестраивается вовсе:
// NULLable colorway_id в UNIQUE не ограничивал бы ничего (MySQL считает NULL != NULL), а
// закодированный сюда колорвей дробит домен ровно так, как требует владелец — front колорвея A и
// front колорвея B заняты ОДНОВРЕМЕННО.
//
// ⚠ ПРИ colorwayID == 0 ВОЗВРАЩАЕТСЯ ГОЛЫЙ ВИД — байт в байт легаси-адрес. Это не удобство, а
// обратная совместимость строк: каждый слот, рождённый до оси, остаётся достижим тем же ключом,
// каким рождался. Разделитель `@cw:` не встречается ни в одном виде и ни в одном детальном
// ключе (`detail:<uuid>`), поэтому коллизий доменов нет по построению.
func DesignBenchExclusiveKey(viewKey string, colorwayID int) string {
	if colorwayID <= 0 {
		return viewKey
	}
	return fmt.Sprintf("%s@cw:%d", viewKey, colorwayID)
}

// DesignPictureKindTakesColorway — у каких РОДОВ КАДРА ось колорвея есть. Рендер и 3D-кадр —
// изображения изделия В ЦВЕТЕ, их колорвей и есть предмет L-2. Флэт — одна разметка на карточку
// (L-4): ему колорвей ОТКАЗЫВАЮТ, а не молча обнуляют.
//
// ⚠ ПАТТЕРН ПЕРЕЕХАЛ СЮДА В КРУГЕ 15, И ЭТО ТА ЖЕ ОДНА ПРИЧИНА, ЧТО У ПРОГОНА. Владелец решил, что
// плитка называется ИМЕНЕМ И КОЛОРВЕЕМ до денег: «тут мы делаем только сам паттерн выбираем ему
// название и колорвей и все». Значит прогон паттерна законно несёт колорвей (DesignRunKindTakesColorway),
// а CompleteRun копирует колорвей СТРОКИ в каждый её кадр — и сторож D6 отверг бы собственный кадр
// такого прогона («a kind that has no colourway axis»), то есть закрыл бы род целиком. Правило одно
// на прогон и на кадр, потому что оно ОДНО: «этот паттерн — для колорвея N».
//
// ⚠ ЭТО НЕ ДЕЛАЕТ ПЛИТКУ СОСТОЯНИЕМ ИЗДЕЛИЯ. Верстак её по-прежнему не берёт (IsDesignBenchKind), и
// довод там не изменился: плитка обоев — кадр карточки, но не сторона силуэта.
func DesignPictureKindTakesColorway(kind string) bool {
	switch DesignKindOrFlat(kind) {
	case DesignPictureKindRender, DesignPictureKindThreed, DesignPictureKindPattern:
		return true
	}
	return false
}

// DesignRunKindTakesColorway — у каких РОДОВ ПРОГОНА ось колорвея есть: render и recolor рождают
// кадры рода render (мультивью ЭТОГО колорвея), threed ВЫБИРАЕТ колорвей, чей верстак рендерит
// (L-3), pattern НАЗЫВАЕТ колорвей, для которого делается плитка, и отдаёт ему сделанный ассет в
// той же транзакции, что закрывает прогон (круг 15, J-12). Флэт, вектор и текст цвета не имеют —
// названный там колорвей был бы записью, которую ни один читатель не смог бы честно истолковать.
//
// ⚠ У ПАТТЕРНА КОЛОРВЕЙ — НЕ ПОДПИСЬ ВЫХОДА, А АДРЕС ПОЛКИ, и читателя у него ровно один:
// keepPatternTx, который сажает готовую плитку на колорвей карточки. Поэтому род остаётся в
// «мягкой» половине DesignRunKindReadsColorwayBench: верстака он не читает вовсе, и потеря
// атрибуции после удаления колорвея честна — ассет встаёт на полку ничей, ровно как его гасит FK.
func DesignRunKindTakesColorway(kind string) bool {
	switch kind {
	case DesignRunKindRender, DesignRunKindThreed, DesignRunKindRecolor, DesignRunKindPattern:
		return true
	}
	return false
}

// DesignRunKindReadsColorwayBench — ВЫБИРАЕТ ЛИ РОД ПРОГОНА ВЕРСТАК ПО КОЛОРВЕЮ, в отличие от
// родов, для которых колорвей — только атрибуция результата.
//
// Разделение нужно ровно в одном месте и решает там ровно один вопрос: можно ли МОЛЧА обнулить
// колорвей, которого больше нет. У render/recolor можно — они строятся из общего флэтового
// верстака, а колорвей лишь подписывает выход. У threed нельзя: он ЧИТАЕТ верстак названного
// цвета, снимок входов замерзает против него, и обнулённая колонка сделала бы строку прогона
// несогласной с собственными inputs.
// ⚠ ПЕРЕЧИСЛЯЕТСЯ БЕЗОПАСНОЕ, А НЕ ОПАСНОЕ, И ЭТО ПРО БУДУЩЕЕ (T9). `kind == threed` верно
// сегодня и отказывает в безопасную сторону только сегодня: новый род, читающий верстак по
// колорвею, не попал бы в список и МОЛЧА получил бы деградацию — то есть строку, чьи входы
// описывают один верстак, а колонка называет другой. Список «кому деградация честна» закрыт по
// смыслу: она честна ровно тем, у кого колорвей — подпись выхода, а не выбор входа. Новый род по
// умолчанию попадает в строгую половину и отказывает; цена ошибки в эту сторону — отказ, который
// человек прочитает.
func DesignRunKindReadsColorwayBench(kind string) bool {
	switch kind {
	case DesignRunKindRender, DesignRunKindRecolor:
		// Колорвей ПОДПИСЫВАЕТ выход: оба строятся из общего флэтового верстака, и потеря
		// атрибуции у них честна — реран уезжает неатрибутированным, params помнят просимое.
		return false
	case DesignRunKindPattern:
		// ⚠ ПАТТЕРН ДОБАВЛЕН СЮДА НАМЕРЕННО, И ЭТО РОВНО ТОТ ЖЕСТ, О КОТОРОМ ГОВОРИТ АБЗАЦ ВЫШЕ
		// («новый род по умолчанию попадает в строгую половину»). Он ВЕРСТАКА НЕ ЧИТАЕТ ВОВСЕ:
		// designSelectBench отдаёт ему пустоту, снимок входов ни против какого колорвея не
		// заморожен, а единственный читатель колорвея — посадка готовой плитки на полку
		// (keepPatternTx), которая берёт ЖИВУЮ колонку. Колорвей, удалённый между стартом и
		// посадкой, и так гасится FK в NULL — ассет встаёт на полку ничей. Значит деградация на
		// старте даёт ТОТ ЖЕ исход, а строгость дала бы вечный отказ рерану, который клиент не
		// умеет написать иначе: ни params, ни колорвея он не присылал.
		return false
	}
	return true
}

// DesignColorwayOrNone — NULL колонки читается как 0 («колорвея нет»), одним правилом на всех
// ярусах, ровно как DesignKindOrFlat для рода.
func DesignColorwayOrNone(v sql.NullInt32) int {
	if !v.Valid {
		return 0
	}
	return int(v.Int32)
}

// ───────────── «НАЗВАЛ НИЧЕГО» ПРОТИВ «НАЗВАЛ БЕЗКОЛОРВЕЙНЫЙ ВЕРСТАК» (D2/D3/D4) ─────────────
//
// У КОЛОНКИ 0 значит ровно одно — «колорвея нет». У ЗАПРОСА тот же ноль значил ДВА разных
// намерения, и они расходились молча:
//   - «я про колорвей ничего не сказал» — читай всё / подставь разумное;
//   - «я назвал БЕЗКОЛОРВЕЙНЫЙ верстак» — тот самый, законный и вечный верстак
//     неатрибутированных легаси-рендеров, который 3D выбирает наравне с именованными.
//
// Пока эти два смысла делили ноль, безколорвейный верстак нельзя было ни ПРОЧИТАТЬ отдельно
// (GetDesignBand отдавал всю полосу), ни АДРЕСОВАТЬ отдельно (загрузка с постановкой молча
// переписывала цель колорвеем самого файла и клала кадр НЕ ТУДА, куда её послали).
//
// ПОЧЕМУ СЕНТИНЕЛ, А НЕ `optional` (явное присутствие proto3). Presence решил бы задачу для
// честного клиента и СЛОМАЛ бы её для обычного: генераторы TS сплошь и рядом заполняют скаляры
// нулями, и `bench_colorway_id: 0` пришёл бы «названным» — то есть каждый существующий вызов
// молча сузился бы до безколорвейного верстака. Сентинел устроен наоборот: ноль остаётся
// legacy-нулём для всякого, кто его не имел в виду, и НАЗВАТЬ безколорвейный верстак может
// только тот, кто написал -1 сознательно. Отрицательное значение до этой волны отвергалось
// InvalidArgument'ом на обеих дверях, поэтому -1 не отнимает ни одного смысла, который уже был.
const DesignColorwayUnattributed = -1

// DesignColorwayRef — колорвей, НАЗВАННЫЙ В ЗАПРОСЕ. Отдельный тип, а не голый int, потому что
// пара «значение + названо ли» разъезжается ровно тогда, когда её хранят двумя членами (пять
// признаков ложного расщепления): здесь смысл один, и читатель обязан пройти через Stated/Id.
//
//	0  — не названо (совместимость: ровно то, что шлёт всякий сегодняшний клиент)
//	-1 — названо, и назван БЕЗКОЛОРВЕЙНЫЙ верстак
//	>0 — назван этот колорвей
type DesignColorwayRef int

// Valid — форма значения, а не его допустимость в контексте: всё, что меньше сентинела,
// бессмысленно и отвергается InvalidArgument'ом на самой двери.
func (r DesignColorwayRef) Valid() bool { return r >= DesignColorwayUnattributed }

// Stated — назвал ли вызывающий колорвей ВООБЩЕ. Единственный вопрос, ответ на который менялся
// молча, пока смыслов было два на один ноль.
func (r DesignColorwayRef) Stated() bool { return r != 0 }

// Id — колорвей в терминах КОЛОНКИ: сентинел схлопывается в 0, потому что «безколорвейный
// верстак» и есть colorway_id IS NULL. Ни одна строка никогда не хранит -1.
func (r DesignColorwayRef) Id() int {
	if r < 0 {
		return 0
	}
	return int(r)
}

// Виды прогона. `vector` приехал волной 2: векторизация — это ДЕНЬГИ, и у денег одна дверь, а не
// отдельный RPC мимо бюджета (31 §решения).
const (
	DesignRunKindFlat      = "flat"
	DesignRunKindRender    = "render"
	DesignRunKindThreed    = "threed"
	DesignRunKindVector    = "vector"
	DesignRunKindDraftIdea = "draft_idea"
	// DesignRunKindRecolor — ПЕРЕКРАС ВЕЩИ НА ГОТОВОЙ ФОТОГРАФИИ (K-17). Владелец: «раздел ON MODEL
	// должен быть таким что мы можем загрузить фото реальное на модели с разных сторон и нам можно
	// будет поменять цвет вещи», и решение о механизме — его же: цвет меняется ГЕНЕРАЦИЕЙ, то есть
	// модель перерисовывает вещь в новый цвет, сохраняя ткань, складки и тени.
	//
	// ЭТО ОТДЕЛЬНЫЙ РОД, А НЕ РЕНДЕР С ФЛАЖКОМ, и различие несущее. Рендер СОЧИНЯЕТ фотографию по
	// флэтам и по описанию ткани; перекрас НИЧЕГО НЕ СОЧИНЯЕТ — он обязан вернуть ту же самую
	// фотографию, тот же кадр, ту же позу и то же освещение, изменив ровно одно. Это две
	// противоположные инструкции модели, и один род не может нести обе: прогон закончился бы тем
	// абзацем ремесла, который случайно написан последним.
	DesignRunKindRecolor = "recolor"
	// DesignRunKindPattern — ПОВТОРЯЕМАЯ ПЛИТКА ИЗ КАРТИНКИ (K-13). Владелец: «заапдоудить картинку
	// и через gpt image 2 сделать из неё повторяемый паттерн».
	//
	// КЛЮЧЕВОЕ СЛОВО — ПОВТОРЯЕМЫЙ: результат обязан стыковаться сам с собой по краям, иначе он
	// бесполезен. Поэтому род отдельный: у него единственный вход (одна картинка), единственный
	// выход (одна плитка) и инструкция, которой нет ни у одного другого рода.
	DesignRunKindPattern = "pattern"
)

// IsDesignRunKind сообщает, известен ли род прогона.
func IsDesignRunKind(v string) bool {
	switch v {
	case DesignRunKindFlat, DesignRunKindRender, DesignRunKindThreed,
		DesignRunKindVector, DesignRunKindDraftIdea,
		DesignRunKindRecolor, DesignRunKindPattern:
		return true
	}
	return false
}

// DesignPictureKindOfRun — какого рода кадры рождает прогон этого рода. Вектор рождает ПЛОСКИЙ
// кадр: SVG остаётся флэтом изделия, а не третьим родом верстака.
func DesignPictureKindOfRun(runKind string) string {
	switch runKind {
	case DesignRunKindRender:
		return DesignPictureKindRender
	case DesignRunKindThreed:
		return DesignPictureKindThreed
	// ПЕРЕКРАС РОЖДАЕТ РЕНДЕР, И ЭТО ПРАВДА, А НЕ УДОБСТВО: на выходе фотография изделия в сцене —
	// ровно то, что означает `render`. Следствие названо вслух, потому что оно неочевидно: такой
	// кадр УДОВЛЕТВОРЯЕТ ворота W-13 («3D только после fabric render»). Это верно по существу —
	// перекрашенный настоящий снимок изделия основание для сборки не худшее, а лучшее, чем
	// сочинённый рендер.
	case DesignRunKindRecolor:
		return DesignPictureKindRender
	case DesignRunKindPattern:
		return DesignPictureKindPattern
	default:
		return DesignPictureKindFlat
	}
}

// Состояния попытки — СЛОВАРЬ ИЗ СХЕМЫ 0340 ДОСЛОВНО. `unknown` значит «деньги, возможно,
// списаны, результата нет»; это единственное состояние, которое человек обязан прочитать в
// истории, поэтому оно названо, а не выведено из пустоты.
const (
	DesignAttemptDispatching = "dispatching"
	DesignAttemptAccepted    = "accepted"
	DesignAttemptDelivered   = "delivered"
	DesignAttemptFailed      = "failed"
	DesignAttemptUnknown     = "unknown"
)

// IsDesignAttemptState сообщает, известно ли состояние попытки.
func IsDesignAttemptState(v string) bool {
	switch v {
	case DesignAttemptDispatching, DesignAttemptAccepted, DesignAttemptDelivered,
		DesignAttemptFailed, DesignAttemptUnknown:
		return true
	}
	return false
}

// Статусы прогона.
const (
	DesignRunPending   = "pending"
	DesignRunRunning   = "running"
	DesignRunDone      = "done"
	DesignRunFailed    = "failed"
	DesignRunCancelled = "cancelled"
)

// Полки ассетов карточки (0354, V-11): ткани, паттерны, фурнитура.
//
// ОДИН СЛОВАРЬ, А НЕ ТРИ ТАБЛИЦЫ — довод целиком лежит в шапке 0354_design_asset.sql и здесь не
// пересказывается. Читателю Go нужна ровно одна его половина: `kind` говорит, ЧЕМ ассет ЯВЛЯЕТСЯ,
// и никогда — КАК он получен. Происхождение паттерна это DerivedFromAssetId, отдельное ребро, и
// потому паттерн, нарисованный моделью, и паттерн, разложенный из загруженного лоскута, — ОДИН
// род с разной родословной.
const (
	DesignAssetKindFabric   = "fabric"
	DesignAssetKindPattern  = "pattern"
	DesignAssetKindHardware = "hardware"
)

// DesignErrorCodeLibraryFull — «плитка сделана, а места на полке карточки нет».
//
// ⚠ ОДНА СТРОКА НА ДВУХ ПИСАТЕЛЕЙ ИЗ РАЗНЫХ ПАКЕТОВ, И ИМЕННО ПОЭТОМУ ОНА КОНСТАНТА. Дверь
// (apisrv/admin) отказывает этим сентинелом ДО денег, а стор (store/design, keepPatternTx) пишет
// его же в `design_run.error_code`, когда полка переполнилась, пока прогон шёл. Клиент читает ОДНО
// слово в обоих местах; два литерала в двух пакетах разошлись бы на первой же опечатке, и
// разошлись бы молча — экран просто перестал бы узнавать вторую половину случая.
const DesignErrorCodeLibraryFull = "library_full"

// DesignAssetKinds — три полки в том порядке, в каком их называет владелец: ткани, паттерны,
// фурнитура. Порядок значим ровно настолько, насколько значим порядок полок на стене.
var DesignAssetKinds = []string{DesignAssetKindFabric, DesignAssetKindPattern, DesignAssetKindHardware}

// IsDesignAssetKind сообщает, известна ли полка.
//
// CHECK в схеме намеренно нет — словарь растёт, а поздний `ADD CONSTRAINT ... CHECK` на
// потолстевшей таблице это КОПИРОВАНИЕ таблицы целиком, у которого захардкожен пятиминутный потолок
// прогона миграций, то есть остановленный старт прода. Поэтому проверяет Go, и отказ называет
// значение, а не отдаёт сырой 3819 с именем колонки.
func IsDesignAssetKind(v string) bool {
	for _, k := range DesignAssetKinds {
		if k == v {
			return true
		}
	}
	return false
}

// MaxDesignAssetsPerCard — потолок полок одной карточки. Изделие из сорока тканей это не изделие,
// а именно поэтому полки едут в полосе ЦЕЛИКОМ, без страницы (см. DesignBand.Assets): потолок и
// есть то, что делает «всё сразу» законным ответом.
const MaxDesignAssetsPerCard = 40

// Границы двух чисел паттерна и двух текстовых полей ассета.
//
// РАППОРТ И ПОВОРОТ — ЧИСЛА, НА КОТОРЫЕ МОДЕЛЬ МОЖЕТ ДЕЙСТВОВАТЬ, в отличие от «крупный» и
// «мелкий». Два метра это уже не раппорт, а полотно; поворот считается по часовой и замыкается на
// 360, поэтому 360 запрещён — он и есть 0, записанный вторым способом.
const (
	MaxDesignAssetRepeatMm    = 2000
	MaxDesignAssetRotationDeg = 359
	// Имя и записка меряются В РУНАХ, а не в байтах: колонки объявлены VARCHAR(60)/VARCHAR(500), а
	// VARCHAR в MySQL считает символы. Байтовый предел отказал бы кириллице вдвое раньше срока.
	MaxDesignAssetNameRunes = 60
	MaxDesignAssetNoteRunes = 500
)

// ---------------------------------------------------------------------------
// Отказы полосы
// ---------------------------------------------------------------------------

// Таксономия отказов — ДОСЛОВНО из 10 §3. Каждая ошибка здесь имеет ровно один код на проводе,
// и хендлер переводит её в grpc-статус по одной таблице. Ошибка, которой нет в этом списке,
// клиентом не откатывается — именно поэтому остаточный 1062 ленивого рождения слота мапится
// сюда, а не уходит наружу как Internal.
var (
	// ErrDesignSlotRevMismatch — CAS по slot_rev не сошёлся: человек смотрел на старый экран.
	// Хендлер отдаёт Aborted и кладёт ТЕКУЩЕЕ состояние слота в details.
	ErrDesignSlotRevMismatch = errors.New("design: slot_rev_mismatch")
	// ErrDesignForeignCardPlate — плита принадлежит другой карточке. Схема этого выразить не
	// может (композитный FK потребовал бы CASCADE, а слот детали обязан пережить исчезновение
	// плиты), поэтому проверяет Go в той же транзакции.
	ErrDesignForeignCardPlate = errors.New("design: foreign_card_plate")
	// ErrDesignCompositePlate — композит в слот не встаёт, его сначала режут.
	ErrDesignCompositePlate = errors.New("design: composite_plate")
	// ErrDesignHiddenPlate — скрытую плиту нельзя принять стороной.
	ErrDesignHiddenPlate = errors.New("design: hidden_plate")
	// ErrDesignWrongKind — кадр турнтейбла (kind=threed) не может стоять плитой листа.
	ErrDesignWrongKind = errors.New("design: wrong_kind")
	// ErrDesignPictureAlreadyInSlot — плита уже стоит в ДРУГОМ слоте этой карточки.
	// Это не косметика: без предварительной проверки INSERT … ON DUPLICATE KEY UPDATE
	// столкнулся бы на uq_design_bench_picture и обновил бы ЧУЖУЮ строку слота.
	ErrDesignPictureAlreadyInSlot = errors.New("design: picture_already_in_slot")
	// ErrDesignDetailNameRequired — новый слот детали без имени.
	ErrDesignDetailNameRequired = errors.New("design: detail_name_required")
	// ErrDesignSlotFilled — удаляемый слот детали не пуст.
	ErrDesignSlotFilled = errors.New("design: slot_filled")
	// ErrDesignNotADetailSlot — DeleteDesignDetailSlot позвали на одну из четырёх сторон.
	// В плане этот отказ не назван; он добавлен, потому что иначе единственный законный ответ
	// на «удали front» — молчаливое удаление стороны, которую слот-адрес обязан переживать.
	ErrDesignNotADetailSlot = errors.New("design: not_a_detail_slot")
	// ErrDesignInSlot / ErrDesignLiveRunInput / ErrDesignLiveCropParent — три сторожа
	// HidePicture. Читаются в ТОЙ ЖЕ транзакции, что и UPDATE, иначе TOCTOU.
	ErrDesignInSlot         = errors.New("design: in_slot")
	ErrDesignLiveRunInput   = errors.New("design: live_run_input")
	ErrDesignLiveCropParent = errors.New("design: live_crop_parent")
	// ErrDesignNotComposite — режут не композит.
	//
	// СЕЙЧАС ЕГО НИКТО НЕ ПОДНИМАЕТ, и это записано здесь, чтобы читатель не решил, будто разрез
	// может отказать по этой причине. Проверку сняли и в хендлере, и в сторе: единственным
	// писателем `composite_views` был прилёт генеративного прогона, а генерация из волны отрезана,
	// поэтому колонка пуста на каждой картинке, которая вообще может существовать, и проверка
	// отказывала ВСЕМ разрезам. Что режется на куски, объявляет человек — `view_key` на рамке.
	// Сентинел и его отображение в код оставлены: они вернутся вместе с писателем колонки.
	ErrDesignNotComposite = errors.New("design: not_composite")
	// ErrDesignLayerRevMismatch — CAS по rev слоя правки.
	ErrDesignLayerRevMismatch = errors.New("design: layer_rev_mismatch")
	// ErrDesignEmptyLayer — флэттен пустого слоя.
	ErrDesignEmptyLayer = errors.New("design: empty_layer")
	// ErrDesignStrokesTooLarge — штрихи сверх потолка (10 §6).
	ErrDesignStrokesTooLarge = errors.New("design: strokes_too_large")
	// ErrDesignNotFound — строки полосы нет либо она принадлежит другой карточке.
	ErrDesignNotFound = errors.New("design: not found")
	// ErrDesignInvalidArgument — аргумент не годится: пустой список, неизвестное значение
	// словаря, отсутствующий client_request_id. ОТДЕЛЬНО ОТ ErrDesignNotFound, и это не
	// косметика: «неизвестный ghost_view» на проводе обязан быть InvalidArgument (10 §3), а
	// NotFound сказал бы клиенту, что карточка исчезла, — он пошёл бы перезагружать полосу
	// вместо того, чтобы починить запрос.
	ErrDesignInvalidArgument = errors.New("design: invalid argument")
	// ErrDesignForeignMedia — медиа не принадлежит этой карточке (референс, которого карточка
	// не держит; чужой файл во флэттене).
	ErrDesignForeignMedia = errors.New("design: foreign_media")
	// ErrDesignNotImplemented — тело метода приезжает следующей волной. Интерфейс заморожен
	// целиком СЕЙЧАС именно для того, чтобы следующий исполнитель добавлял ФАЙЛЫ в пакет, а не
	// правил шов в dependency.go и store.go повторно.
	ErrDesignNotImplemented = errors.New("design: not implemented in this wave")

	// ───────────────────────── отказы полок ассетов (0354) ─────────────────────────

	// ErrDesignAssetKindUnknown — полки с таким именем нет. ОТДЕЛЬНО ОТ ErrDesignInvalidArgument,
	// хотя код на проводе у них один: у клиента здесь есть ЧТО ПОКАЗАТЬ человеку («выберите полку»)
	// вместо общего «запрос не годится», и различить это он может только по машинному токену.
	ErrDesignAssetKindUnknown = errors.New("design: asset_kind_unknown")
	// ErrDesignAssetNameRequired — ассет без имени. Имя обязательно не из аккуратности: промпт
	// цитирует ткань именем («contrast rib on the collar»), и безымянный ассет доезжает до модели
	// словом «ткань» — ровно тот провал, который слоты деталей уже проходили однажды.
	ErrDesignAssetNameRequired = errors.New("design: asset_name_required")
	// ErrDesignAssetTooMany — полки уперлись в MaxDesignAssetsPerCard. FailedPrecondition, а не
	// InvalidArgument: запрос правильный, кончилось место, и чинится это удалением, а не правкой.
	ErrDesignAssetTooMany = errors.New("design: asset_too_many")
	// ErrDesignAssetNotAPattern — не-паттерн заявил родителя либо раппорт. Оба поля значат
	// «во что и какого размера разложена ткань», и у ткани либо фурнитуры они не значат НИЧЕГО:
	// принять их значило бы завести форму, которую экран не умеет ни нарисовать, ни отредактировать,
	// а промпт прочитал бы как раппорт гладкого полотна.
	ErrDesignAssetNotAPattern = errors.New("design: asset_not_a_pattern")

	// ───────────────────────── отказы оси колорвея (0356, L-2/L-3) ─────────────────────────

	// ErrDesignColorwayForbidden — колорвей назван там, где оси колорвея НЕТ ПО СУЩЕСТВУ: у
	// флэта (и паттерна) как кадра, у флэтового верстака как адреса, у прогона рода
	// flat|vector|pattern|draft_idea. Это НЕ «поле пока не заполняют»: чертёж изделия один на все
	// цвета (L-4), и состояние «флэт с колорвеем» не должно быть выразимо ни через одну дверь
	// записи. Отказ, а не молчаливый сброс: сброшенное значение — это принятая, но не
	// исполненная просьба, и разошлись бы они молча.
	ErrDesignColorwayForbidden = errors.New("design: colorway_forbidden")
	// ErrDesignForeignColorway — названный колорвей не принадлежит этой карточке (колорвей после
	// 0151 — строка product с primary_tech_card_id карточки). Тот же класс границы, что
	// foreign_card_plate и foreign_media, и проверяется он в ТОЙ ЖЕ транзакции, что запись.
	ErrDesignForeignColorway = errors.New("design: foreign_colorway")
	// ErrDesignColorwayMismatch — колорвей плиты не совпал с колорвеем слота. Рендер колорвея A
	// в верстаке колорвея B — та же ложь листа, что рендер во флэт-слоте (wrong_kind), только по
	// второй оси; и неатрибутированный рендер в именованном верстаке — тоже она: атрибуцию
	// постановкой не выдумывают.
	ErrDesignColorwayMismatch = errors.New("design: colorway_mismatch")
	// ErrDesignAmbiguousFlattenBase — подложку слоя нельзя привязать к ОДНОЙ картинке: слой не
	// назвал source_picture_id, а его base_media_id зарегистрирован на карточке НЕСКОЛЬКО раз, и
	// эти регистрации не согласны о колорвее. Один файл законно бывает кадром двух колорвеев
	// (тот же мультивью, перезалитый на другой цвет), поэтому «первая строка по id» — не
	// умолчание, а бросок монеты: флэттен уезжал в ЧУЖОЙ верстак с вероятностью, зависящей от
	// порядка вставки. Отказ, потому что выдумывать атрибуцию нельзя ни фильтром, ни сортировкой.
	ErrDesignAmbiguousFlattenBase = errors.New("design: ambiguous_flatten_base")

	// ───────────────────────── отказы генеративной половины ─────────────────────────

	// ⚠ ЗДЕСЬ БЫЛ ErrDesignBudgetExceeded, И ОН СНЯТ ВМЕСТЕ С САМИМ ПОНЯТИЕМ ПОТОЛКА (0358, L-8).
	// Слова владельца: «у нас в принципе не должно быть потолка похуй чем он съеден убери
	// потолок». Отказ по деньгам не существует ни в одной форме — не «выключен», а СНЯТ: пока
	// sentinel жив, кто-нибудь once again подключит его к проверке. Деньги при этом
	// по-прежнему считаются и записываются (DesignBudget ниже) — убрана МАШИНА, РЕШАВШАЯ, ЧТО
	// сегодня работать нельзя, а не учёт.
	// ErrDesignClaimLost — воркер пришёл с claim_token, которого у строки уже нет: его лизу
	// подмёл ReviveExpiredRuns либо строку перехватил другой воркер.
	//
	// ЭТО ОТКАЗ, А НЕ УСПЕХ, И ИМЕННО ОН СТЕРЕЖЁТ ЧУЖОЙ РЕЗУЛЬТАТ. Без токена в WHEREе
	// CompleteRun/FailRun воркер с истёкшим захватом затёр бы картинки того, кто перехватил
	// задание, — и оба ответа выглядели бы успешными.
	ErrDesignClaimLost = errors.New("design: claim_lost")
	// ErrDesignRunTerminal — прогон уже закрыт (done|failed|cancelled), и повторное закрытие
	// его не двигает. Отдельно от ErrDesignClaimLost: «ты опоздал» и «строка уже кончилась» —
	// разные новости, и воркер поступает с ними по-разному.
	ErrDesignRunTerminal = errors.New("design: run_terminal")
)

// ---------------------------------------------------------------------------
// Строки таблиц
// ---------------------------------------------------------------------------

// DesignRun — строка design_run (0340). Она же строка истории на экране полосы.
type DesignRun struct {
	Id                     int            `db:"id"`
	TechCardId             int            `db:"tech_card_id"`
	Kind                   string         `db:"kind"`
	Status                 string         `db:"status"`
	ClientRequestId        string         `db:"client_request_id"`
	ProviderIdempotencyKey string         `db:"provider_idempotency_key"`
	ProfileName            string         `db:"profile_name"`
	ProfileVersion         int            `db:"profile_version"`
	Ask                    sql.NullString `db:"ask"`
	Params                 RawJSON        `db:"params"`
	Inputs                 RawJSON        `db:"inputs"`
	// RerunOf — прогон, который повторяем (0348). FK НЕТ намеренно: читатель джойнит и на
	// пустоту говорит «прогон удалён», а не роняет строку истории.
	RerunOf           sql.NullInt32       `db:"rerun_of"`
	FitAtLaunch       sql.NullString      `db:"fit_at_launch"`
	Rrev              int                 `db:"rrev"`
	RequestedOutputs  int                 `db:"requested_outputs"`
	AttemptCount      int                 `db:"attempt_count"`
	NextAttemptAt     sql.NullTime        `db:"next_attempt_at"`
	ClaimToken        sql.NullString      `db:"claim_token"`
	ClaimExpiresAt    sql.NullTime        `db:"claim_expires_at"`
	PriceEstimate     decimal.NullDecimal `db:"price_estimate"`
	PriceActual       decimal.NullDecimal `db:"price_actual"`
	Currency          string              `db:"currency"`
	Author            string              `db:"author"`
	CancelRequestedAt sql.NullTime        `db:"cancel_requested_at"`
	ArchivedAt        sql.NullTime        `db:"archived_at"`
	ArchivedBy        sql.NullString      `db:"archived_by"`
	ErrorCode         sql.NullString      `db:"error_code"`
	LastError         sql.NullString      `db:"last_error"`
	OutputText        sql.NullString      `db:"output_text"`
	// Prompt — СОБРАННЫЙ текст, ушедший модели (0352): пишется воркером при диспатче
	// (RecordRunPrompt), ДО первой платной попытки, той же строкой, что уходит поставщику.
	// NULL = воркер прогон ещё не поднимал. Это хранение отправленного, не предпросмотр.
	Prompt sql.NullString `db:"prompt"`
	// ColorwayId — ДЛЯ КАКОГО КОЛОРВЕЯ прогон (0356): render/recolor генерят его мультивью,
	// threed рендерит его верстак. Пишется сервером из params при старте, дальше неизменен;
	// NULL = род без оси либо прогон до оси. Замороженные params помнят просимый id и после
	// удаления колорвея (FK SET NULL гасит только колонку).
	ColorwayId  sql.NullInt32 `db:"colorway_id"`
	CreatedAt   time.Time     `db:"created_at"`
	StartedAt   sql.NullTime  `db:"started_at"`
	CompletedAt sql.NullTime  `db:"completed_at"`

	// Собирается читателем, не колонки.
	Attempts []DesignRunAttempt `db:"-"`
	Pictures []DesignPicture    `db:"-"`
}

// DesignRunAttempt — строка design_run_attempt: одна попытка платного вызова. Оплаченный
// провал — тоже строка, иначе полоса бюджета недосчитывает ретраи.
type DesignRunAttempt struct {
	Id                int                 `db:"id"`
	RunId             int                 `db:"run_id"`
	AttemptNo         int                 `db:"attempt_no"`
	Provider          string              `db:"provider"`
	ProviderRequestId sql.NullString      `db:"provider_request_id"`
	State             string              `db:"state"`
	Price             decimal.NullDecimal `db:"price"`
	ErrorCode         sql.NullString      `db:"error_code"`
	StartedAt         time.Time           `db:"started_at"`
	FinishedAt        sql.NullTime        `db:"finished_at"`
}

// DesignBatch — строка design_batch: один жест загрузки.
//
// ПАЧКА ЕДЕТ СО СВОИМИ КАРТИНКАМИ, и это не украшение. У ручной загрузки `run_id` равен NULL, а
// строки прогона у неё нет по построению — значит её картинка не висит ни под одной строкой
// истории. Без пачек в чтении полосы полка загрузок пуста после первой же перезагрузки вкладки:
// картинки существуют только в ответе RegisterDesignUpload той же сессии.
type DesignBatch struct {
	Id              int       `db:"id"`
	TechCardId      int       `db:"tech_card_id"`
	ClientRequestId string    `db:"client_request_id"`
	Author          string    `db:"author"`
	FilesCount      int       `db:"files_count"`
	SizeBytes       int64     `db:"size_bytes"`
	CreatedAt       time.Time `db:"created_at"`

	Pictures []DesignPicture `db:"-"`
}

// DesignPicture — строка design_picture: плита, кадр прогона, кроп композита или флэттен слоя.
type DesignPicture struct {
	Id             int            `db:"id"`
	TechCardId     int            `db:"tech_card_id"`
	MediaId        int            `db:"media_id"`
	RunId          sql.NullInt32  `db:"run_id"`
	BatchId        sql.NullInt32  `db:"batch_id"`
	Ordinal        int            `db:"ordinal"`
	Kind           string         `db:"kind"`
	GhostView      sql.NullString `db:"ghost_view"`
	CompositeViews RawJSON        `db:"composite_views"`
	DerivedFrom    sql.NullInt32  `db:"derived_from"`
	// Derivation — КАКИМ ГЛАГОЛОМ кадр отделился от DerivedFrom (0359): crop | flatten | пусто.
	// Пустое значение читается ТОЛЬКО в паре с DerivedFrom — см. DesignDerivationNone.
	Derivation  string `db:"derivation"`
	SourceClass string `db:"source_class"`
	MixedInput  bool   `db:"mixed_input"`
	LayerRev    int    `db:"layer_rev"`
	// Selected — кадр помечен выбранным (0350, W-12). НЕ обратная сторона hidden_at: спрятать —
	// убрать с глаз, выбрать — поднять над остальными; выбранных может быть несколько.
	Selected bool `db:"selected"`
	// ColorwayId — ЧЕЙ это кадр (0356): FK product(id). NULL у флэта = колорвея нет по существу
	// (двери записи флэту значения не дают); NULL у рендера/3D = кадр до оси либо колорвей
	// удалён — «не атрибутирован», и читатель обязан различать эти два NULL парой с Kind.
	ColorwayId sql.NullInt32  `db:"colorway_id"`
	HiddenAt   sql.NullTime   `db:"hidden_at"`
	HiddenBy   sql.NullString `db:"hidden_by"`
	CreatedAt  time.Time      `db:"created_at"`

	// Media резолвится джойном на media(id) читателем полосы.
	Media *MediaFull `db:"-"`
}

// DesignBenchSlot — строка design_bench_slot: адрес, по которому лежит ПРИНЯТАЯ плита.
//
// СЛОТ ЕДЕТ С РАЗРЕШЁННОЙ КАРТИНКОЙ, а не с голым picture_id. Плита слота вполне может быть из
// старой загрузки, лежащей ВНЕ первой страницы полосы: тогда у слота нет ни миниатюры, ни
// source_class — то есть слот нечем нарисовать, а предупреждение о смеси провенансов нечем
// посчитать, потому что считается оно ровно по source_class плит.
//
// ТО ЖЕ САМОЕ ОБЯЗАТЕЛЬНО В ОТКАЗЕ slot_rev_mismatch: в details едет текущее состояние слота, и
// весь смысл этого отказа — показать человеку, ЧТО там стоит сейчас. Голый id этого не показывает.
type DesignBenchSlot struct {
	Id         int    `db:"id"`
	TechCardId int    `db:"tech_card_id"`
	ViewKey    string `db:"view_key"`
	// Kind — ВТОРАЯ ОСЬ ВЕРСТАКА (0349): flat | render | threed. Адрес слота это ПАРА
	// (род, вид), а не один вид: пока ось была одна, рендер фронта вытеснял флэт фронта, и
	// технический лист печатал рендер. Пустое читается как flat — см. DesignKindOrFlat.
	Kind         string         `db:"kind"`
	ExclusiveKey string         `db:"exclusive_key"`
	DetailName   sql.NullString `db:"detail_name"`
	PictureId    sql.NullInt32  `db:"picture_id"`
	SlotRev      int            `db:"slot_rev"`
	SetBy        string         `db:"set_by"`
	SetAt        sql.NullTime   `db:"set_at"`
	// ColorwayId — ЧЕЙ это верстак (0356): FK product(id). Рендер-верстак живёт НА КОЛОРВЕЙ —
	// front колорвея A и front колорвея B заняты одновременно, потому что колорвей входит в
	// exclusive_key (DesignBenchExclusiveKey). NULL = флэтовый верстак (оси нет по существу)
	// либо неатрибутированный легаси-рендер-верстак. Колонка — читаемая половина того же факта,
	// что закодирован в ExclusiveKey; оба пишутся одним INSERT из одного значения и после
	// рождения строки не меняются.
	ColorwayId sql.NullInt32 `db:"colorway_id"`

	Picture *DesignPicture `db:"-"`
}

// DesignEditLayer — строка design_edit_layer: векторная калька поверх картинки либо поверх
// пустоты. Strokes пусты везде, кроме GetEditLayer, — 512 KB на слой, слоёв на карточке
// несколько, и полоса не обязана возить их все ради списка миниатюр.
type DesignEditLayer struct {
	Id          int           `db:"id"`
	TechCardId  int           `db:"tech_card_id"`
	BaseMediaId sql.NullInt32 `db:"base_media_id"`
	Rev         int           `db:"rev"`
	Strokes     RawJSON       `db:"strokes"`
	// Origin — ОТКУДА У СЛОЯ ВЕКТОР (0350): drawn | imported | vectorised. Пустое читается как
	// drawn — это правда про каждый слой до 0350, других способов родиться у него не было.
	//
	// ⚠ СЛОВАРЬ БЕРЁТСЯ С ПРОВОДА, А НЕ ИЗ ПРОЗЫ МИГРАЦИИ, ровно как у DesignSourceAIEdits:
	// комментарий 0350 называет третье значение `imported_svg`, контракт
	// (common.DesignEditLayer.origin и ImportDesignVectorRequest.origin) — `imported`. Клиент
	// ветвится по проводу, значит побеждает провод; CHECK в схеме намеренно нет, и разойтись эти
	// два написания могли бы только молча.
	Origin string `db:"origin"`
	// SourceMediaId — АВТОРИТЕТНЫЙ ВЕКТОРНЫЙ ФАЙЛ, проекцией которого является Strokes. Медиа
	// держит файл, штрихи — его редактируемую проекцию, и обратно в байты они не разворачиваются:
	// «скачать SVG» отдаёт ЭТО медиа, а не пересериализацию штрихов.
	SourceMediaId sql.NullInt32 `db:"source_media_id"`
	// SourcePictureId — растр, из которого получен вектор, когда он из растра получен. Пусто =
	// файл пришёл извне полосы либо слой нарисован из пустоты. FK ставит SET NULL при исчезновении
	// картинки: слой работоспособен и без родословной, а висящее число соврало бы.
	SourcePictureId sql.NullInt32 `db:"source_picture_id"`
	// RasterMediaId — ПИКСЕЛЬНЫЙ КАНАЛ СЛОЯ (0355): ОДНО медиа RGBA, ПОЛНОЕ состояние пикселей.
	// NULL = ничего не закрашивали, слой чисто векторный.
	//
	// ⚠ ПОЛНОЕ СОСТОЯНИЕ, А НЕ ДЕЛЬТА, и это не выбор формата. Ластик по решению владельца
	// прогрызает И ПОДЛОЖКУ, поэтому результат не выражается как «база плюс что-то сверху»
	// вообще: дырка в фотографии — это АЛЬФА ЭТОЙ картинки. Отдельной маски базы поэтому нет.
	//
	// Живёт на ОДНОЙ строке со Strokes и ходит под ОДНИМ Rev: два канала одного слоя, одна
	// ревизия, один CAS. Вторая ревизия означала бы две независимые гонки на одном экране.
	RasterMediaId sql.NullInt32 `db:"raster_media_id"`
	// ClientRequestId — КЛЮЧ ИДЕМПОТЕНТНОСТИ ИМПОРТА (0351), тот самый, который объявляет контракт.
	// NULL у всякого слоя, заведённого не импортом: SaveEditLayer — это compare-and-set по rev, у
	// него однократной подачи нет и запроса тоже. Хранится, а не только сверяется, потому что стор
	// обязан отличить «тот же запрос про тот же файл» от «тот же запрос про ДРУГОЙ файл».
	ClientRequestId sql.NullString `db:"client_request_id"`
	UpdatedBy       string         `db:"updated_by"`
	UpdatedAt       time.Time      `db:"updated_at"`
}

// Словарь origin слоя правки — ПРОВОДНОЙ, см. DesignEditLayer.Origin.
const (
	DesignLayerOriginDrawn      = "drawn"
	DesignLayerOriginImported   = "imported"
	DesignLayerOriginVectorised = "vectorised"
)

// IsDesignImportableLayerOrigin — что ВОЛЬНО ПОДШИТЬ ImportDesignVector. `drawn` сюда не входит и
// это не придирка: слой, нарисованный из пустоты, рождается SaveDesignEditLayer и импортировать
// ему нечего — файла у него нет вовсе.
func IsDesignImportableLayerOrigin(v string) bool {
	return v == DesignLayerOriginImported || v == DesignLayerOriginVectorised
}

// DesignLayerOriginOrDrawn — ПУСТОЕ ЧИТАЕТСЯ КАК drawn, ровно как DEFAULT 'drawn' в 0350.
func DesignLayerOriginOrDrawn(v string) string {
	if v == "" {
		return DesignLayerOriginDrawn
	}
	return v
}

// DesignReference — строка design_reference: какой стороне изделия отвечает референс на входе
// генерации. Роль живёт в полосе, а не в документе: `kind` у медиа карточки уже занят тем, ЧЕМ
// картинка является, и это настоящая вторая ось.
type DesignReference struct {
	Id         int    `db:"id"`
	TechCardId int    `db:"tech_card_id"`
	MediaId    int    `db:"media_id"`
	Role       string `db:"role"`
	// Note — ЧТО ИМЕННО ЭТА КАРТИНКА ДОБАВЛЯЕТ (0348, W-3): «только воротник», «ткань, а не крой».
	// Едет к модели рядом с картинкой и замерзает в снимке входов как DesignInputRef.note.
	//
	// Живёт РЯДОМ С РОЛЬЮ, а не вместо неё: роль отвечает «какой стороне изделия отвечает этот
	// референс» и питает ПОРЯДОК в промпте, записка — свободный текст человека. Восемь референсов
	// без записок называют восемь сторон и ни одного намерения, а намерение и есть та половина,
	// которая меняет ответ.
	Note sql.NullString `db:"note"`
	// DetailSlotId — КАКОЙ ИМЕННО ДЕТАЛИ этот референс (0360, J-9): FK design_bench_slot(id).
	//
	// ССЫЛКА, А НЕ КОПИЯ ИМЕНИ: имя детали переименовываемо, и копия разошлась бы с оригиналом
	// молча. Осмысленно только при Role == DesignViewDetail; у прочих ролей стор держит NULL.
	// NULL при роли `detail` значит «слот удалён» (FK ON DELETE SET NULL) либо «строка старше
	// колонки» — оба состояния клиент печатает словами, а не именем, которого у него нет.
	DetailSlotId sql.NullInt32 `db:"detail_slot_id"`
	Ordinal      int           `db:"ordinal"`
	SetBy        string        `db:"set_by"`
	SetAt        time.Time     `db:"set_at"`
}

// DesignAsset — строка design_asset (0354): одна вещь, ИЗ КОТОРОЙ СДЕЛАНО изделие и которая не
// является изображением самого изделия — ткань, паттерн из этой ткани, фурнитура.
//
// АССЕТ ПЕРЕЖИВАЕТ ПРОГОН, и в этом весь смысл полки. Прогон — это подача, он умирает в историю;
// ткань — факт о модели, и она стоит на полке, пока её не уберут руками.
//
// NULLABLE ТАМ, ГДЕ «НЕ СКАЗАНО» — НАСТОЯЩИЙ ОТВЕТ. media_id пуст у ткани, которую назвали словами
// и цветом раньше, чем сфотографировали; derived_from_asset_id пуст у всего, кроме паттерна, да и
// у паттерна он гаснет вместе с исчезнувшей тканью (FK SET NULL) — паттерн с картинкой и раппортом
// остаётся законченным указанием фабрике и без своего лоскута.
type DesignAsset struct {
	Id         int    `db:"id"`
	TechCardId int    `db:"tech_card_id"`
	Kind       string `db:"kind"`
	Name       string `db:"name"`
	// MediaId — текстура, плитка паттерна либо снимок фурнитуры. FK RESTRICT: файл, на котором
	// держится ткань изделия, удалять нельзя, и ровно поэтому колонка ЗАРЕГИСТРИРОВАНА в
	// mediaRefRegistry (в отличие от design_reference.media_id — там медиа подсказка, здесь оно
	// и есть сам ассет).
	MediaId            sql.NullInt32  `db:"media_id"`
	ColourCode         sql.NullString `db:"colour_code"`
	ColourHex          sql.NullString `db:"colour_hex"`
	Note               sql.NullString `db:"note"`
	DerivedFromAssetId sql.NullInt32  `db:"derived_from_asset_id"`
	RepeatMm           int            `db:"repeat_mm"`
	RotationDeg        int            `db:"rotation_deg"`
	// ColorwayId — ЧЬЯ ЭТО ТКАНЬ (0357, G-15): FK product(id), NULL = ничья. «Паттерн — это
	// бесшовная плитка, а бесшовная плитка это ткань», и колорвей носит цвет ИЛИ паттерн; цветной
	// случай здесь не хранится вовсе — строка product УЖЕ несёт свой цвет, и второе поле было бы
	// конкурирующим ответом на вопрос, у которого ответ есть.
	//
	// ⚠ ПИШЕТСЯ РОВНО ОДНИМ ГЛАГОЛОМ — SetAssetColorway. UpsertAsset колонку НЕ НАЗЫВАЕТ в своём
	// SET-списке, и это не забывчивость: Upsert — полная замена, а proto3-скаляр без presence
	// заставил бы всякую правку имени или цвета молча снимать назначение (ловушка material_id из
	// рецепта колорвея, оплаченная дважды). Назначение переживает любые правки ассета.
	//
	// Единственность («одна ткань на колорвей») держит Go в транзакции глагола, не схема: UNIQUE
	// по NULLable колонке не ограничил бы ничьи ассеты и превратил бы обычный перенос назначения
	// в 1062 — см. шапку 0357.
	ColorwayId sql.NullInt32 `db:"colorway_id"`
	Ordinal    int           `db:"ordinal"`
	CreatedBy  string        `db:"created_by"`
	CreatedAt  time.Time     `db:"created_at"`
	UpdatedAt  time.Time     `db:"updated_at"`

	// Media резолвится читателем полосы тем же батчем, что и медиа кадров, — ровно как
	// DesignPicture.Media. Пропавший файл оставляет здесь nil, а не выбрасывает ассет: «файл
	// исчез» — это факт, который полка обязана уметь показать.
	Media *MediaFull `db:"-"`
}

// DesignAssetColorwaySet — «ткань колорвея N — вот этот ассет», единственный писатель
// design_asset.colorway_id (0357).
//
// ColorwayId == 0 СНИМАЕТ назначение, и это настоящий ответ человека («пусть носит свой
// собственный цвет»), а не отсутствие ответа: у глагола ровно одна работа, и молчания в нём быть
// не может — кто его позвал, тот про колорвей и говорит. Поэтому здесь НЕ нужен сентинел
// DesignColorwayRef, которым различаются «не назвал» и «назвал безколорвейный» на дверях верстака.
//
// SINGLE-SELECT: назначение ассета X колорвею N в ТОЙ ЖЕ ТРАНЗАКЦИИ снимает N со всех прочих
// ассетов карточки. Клик по соседнему чипу и ЕСТЬ намерение «теперь ткань N — вот эта», и
// отказывать ему 1062 значило бы требовать от человека сначала снять, потом назначить — два
// круга там, где намерение одно.
type DesignAssetColorwaySet struct {
	TechCardId int
	AssetId    int
	ColorwayId int
	Actor      string
}

// DesignAssetPlacement — строка design_asset_placement (0354): ОДНА МЕТКА НА ОДНОМ ФЛЭТЕ,
// говорящая, что вот этот ассет — вот здесь.
//
// ОДИН ОТВЕТ НА ТРИ ТРЕБОВАНИЯ ВЛАДЕЛЬЦА (V-6 фурнитура, V-7 паттерн, V-8 ткань по частям): все
// три звучат как «эта вещь, на этом чертеже, здесь», значит все три — эта строка. Три механизма
// были бы тремя геометриями, расходящимися на одной картинке.
//
// ⚠ ТУТ НЕТ tech_card_id, И ЭТО РЕШЕНИЕ СХЕМЫ, А НЕ ПРОБЕЛ. Карточка выводится через design_asset;
// второй дом для одного факта разошёлся бы с первым при первом же переносе. Чтение полосы джойнит
// ассет, запись проверяет в Go, что картинка и ассет принадлежат ОДНОЙ карточке.
type DesignAssetPlacement struct {
	Id        int `db:"id"`
	AssetId   int `db:"asset_id"`
	PictureId int `db:"picture_id"`
	// Annotation — та же common.TechCardAnnotation, что рисует вся система. Стор её не разбирает:
	// СОДЕРЖАНИЕ этой колонки — контракт, а не схема. Тип RawJSON, а не json.RawMessage, по общей
	// причине пакета — см. шапку RawJSON.
	Annotation RawJSON        `db:"annotation"`
	Note       sql.NullString `db:"note"`
	SetBy      string         `db:"set_by"`
	SetAt      time.Time      `db:"set_at"`
}

// DesignSettings — строка design_settings (singleton id=1).
//
// ⚠ ПОЛЯ DailyBudget ЗДЕСЬ БОЛЬШЕ НЕТ (0358): колонка удалена вместе с понятием потолка. Осталось
// то, что отвечает на «в чём считать» и «чей сегодня», а не на «можно ли работать».
type DesignSettings struct {
	Currency       string    `db:"currency"`
	BudgetTimezone string    `db:"budget_timezone"`
	UpdatedBy      string    `db:"updated_by"`
	UpdatedAt      time.Time `db:"updated_at"`
}

// DesignBudget — ДЕНЬГИ ДНЯ КАК ЗАПИСЬ, А НЕ КАК ВОРОТА: `today $0.41`.
//
// ⚠ ПОЛЕ Cap СНЯТО (0358, L-8) ВМЕСТЕ С САМИМ ПОТОЛКОМ. Эта структура больше ничего не
// разрешает и не запрещает — она отвечает на «сколько сегодня стоило», и ровно этого владелец
// и хотел: он возражал против машины, решающей, что работать нельзя, а цену как раз спрашивает
// (поводом была фактическая сотня долларов против оценки в шестьдесят центов).
//
// ДВА ПОЛЯ, А НЕ ОДНА СУММА, и довод пережил снятие потолка, хотя раньше его формулировали через
// гейт: `spent` — то, что РЕАЛЬНО оплачено, `reserved` — оценки ещё не закрытых заданий. Одно
// поле «потрачено», несущее их сумму, соврало бы читателю о заплаченном.
type DesignBudget struct {
	Day      string // YYYY-MM-DD, посчитанный в BudgetTimezone В GO
	Spent    decimal.Decimal
	Reserved decimal.Decimal
	Currency string
	Timezone string
}

// ---------------------------------------------------------------------------
// Запросные структуры
// ---------------------------------------------------------------------------

// DesignSlotRef адресует ОДИН слот верстака одним из двух способов, ровно как
// admin.DesignBenchSlotRef: по виду для четырёх сторон, по минтованному id для детали.
// Ровно одно из полей заполнено.
type DesignSlotRef struct {
	ViewKey string
	SlotId  int
	// Kind — род верстака, на котором живёт этот адрес (flat | render | threed). Пустое = flat,
	// поэтому всякий существующий писатель, который род не именует, продолжает попадать ровно
	// туда, куда попадал до 0349.
	Kind string
	// ColorwayId — ЧЕЙ верстак адресуется (0356). С родом flat положительное значение
	// ОТКАЗЫВАЕТСЯ (ErrDesignColorwayForbidden).
	//
	// ⚠ ПРИ АДРЕСАЦИИ ПО SlotId ОНО НЕ ИГНОРИРУЕТСЯ, И ЭТО ОТЛИЧИЕ ОТ Kind — СОЗНАТЕЛЬНОЕ (D2).
	// Формулировка контракта у Kind («минтованный id уже назвал свой верстак, и несогласный род
	// рассудить некому») говорит, ОТКУДА БРАТЬ ЗНАЧЕНИЕ, а не «противоречие можно выбросить».
	// Выброшенное противоречие — это принятая и не исполненная просьба: клиент, пославший id
	// флэтового слота с колорвеем 5, получал OK на действие, которого не произошло. Значение
	// по-прежнему берётся У СТРОКИ, но НАЗВАННОЕ и несогласное — отказывается
	// (colorway_forbidden у флэта, colorway_mismatch у чужого колорвея). Kind остался
	// игнорируемым: его поведение уехало на прод с 0349, и менять его этой волной значило бы
	// чинить не тот дефект — но довод у него ТОТ ЖЕ, и когда-нибудь его стоит выровнять.
	ColorwayId DesignColorwayRef
}

// DesignBenchSlotSet — постановка, вытеснение либо unmark плиты.
//
// UNMARK КОДИРУЕТСЯ PictureId == 0 (решение принято контрактом: «0 = UNMARK — опустошить слот,
// не удаляя его»). Это НЕ то же самое, что удаление слота детали, и оба акта обязаны остаться
// разными.
type DesignBenchSlotSet struct {
	TechCardId      int
	Slot            DesignSlotRef
	PictureId       int
	ExpectedSlotRev int
	NewDetailName   string
	Actor           string
}

// DesignUploadItem — один уже загруженный файл, вносимый в полосу.
type DesignUploadItem struct {
	MediaId   int
	GhostView string
	SizeBytes int64
	// Kind — РОД ЗАГРУЖАЕМОГО КАДРА (flat | render | threed). До волны 2 писатель хардкодил
	// `flat`, и это означало ровно одно: рендер и 3D невозможно было завести руками вовсе
	// (W-8), хотя ручная загрузка — единственный путь, который работает всегда. Пустое = flat.
	Kind string
	// ColorwayId — ЧЕЙ это кадр (0356): product(id), 0 = не атрибутирован. Утверждение
	// загружающего, как и Kind: из пикселей колорвей не восстановить. С родом flat|pattern
	// значение отказывается (ErrDesignColorwayForbidden) — у флэта колорвея нет по существу.
	ColorwayId int
}

// DesignBatchRegister — один жест загрузки: пачка плюс её картинки, опционально с постановкой
// первой картинки в слот ТЕМ ЖЕ CAS.
type DesignBatchRegister struct {
	TechCardId      int
	ClientRequestId string
	Items           []DesignUploadItem
	Target          *DesignSlotRef
	ExpectedSlotRev int
	Actor           string
}

// DesignBatchResult — что вернула регистрация пачки.
type DesignBatchResult struct {
	Batch    DesignBatch
	Pictures []DesignPicture
	Slot     *DesignBenchSlot
	// Idempotent — пачка с этим client_request_id уже существовала, и вернулась она, а не
	// вторая. Хендлер отдаёт OK: повтор после сетевого таймаута не заводит второй набор.
	Idempotent bool
}

// DesignSplitFrame — один кадр разреза композита. Байтовая работа сделана ДО транзакции:
// MediaId это уже загруженный кроп.
type DesignSplitFrame struct {
	MediaId int
	ViewKey string
}

// DesignSplitRequest — разрез композита на сиблингов под той же строкой прогона.
type DesignSplitRequest struct {
	PictureId       int
	ClientRequestId string
	Frames          []DesignSplitFrame
	Actor           string
	// ForInput — просил ли ВЫЗЫВАЮЩИЙ показать кропы модели. Ложь = кадры получают вид, но НЕ
	// получают роль в промпте. См. `SplitDesignPictureRequest.for_input`: умолчание ложно, потому
	// что разрез на верстаке — это раскладка видов по слотам, а не пополнение промпта.
	ForInput bool
}

// DesignEditLayerSave — CAS-сохранение слоя. LayerId == 0 рождает слой; BaseMediaId == 0
// означает чистую векторную базу.
type DesignEditLayerSave struct {
	TechCardId  int
	LayerId     int
	BaseMediaId int
	ExpectedRev int
	Strokes     json.RawMessage
	// RasterMediaId / ClearRaster — ПИКСЕЛЬНЫЙ КАНАЛ, У КОТОРОГО ТРИ СОСТОЯНИЯ, А МОЛЧАНИЕ ЗНАЧИТ
	// «ОСТАВИТЬ», А НЕ «СТЕРЕТЬ».
	//
	//	RasterMediaId > 0                  — вот новое состояние пикселей;
	//	RasterMediaId == 0, ClearRaster== false — ПРО РАСТР НИЧЕГО НЕ СКАЗАНО, хранимое выживает;
	//	RasterMediaId == 0, ClearRaster== true  — снять пиксельный канал, вернуться к чистому вектору.
	//
	// АСИММЕТРИЯ С DesignReferenceRole.Note НАМЕРЕННА И ЭТО ТО ЖЕ ПРАВИЛО, а не исключение из него.
	// Там пустой ТЕКСТ — настоящий ответ человека, поэтому пустая записка очищает записку. Здесь
	// ссылка на медиа — это СУЩЕСТВОВАНИЕ, и пустая ссылка означает уничтожение; уничтожение
	// обязано быть сказано вслух. Читай отсутствие как «очистить» — и автосейв, тронувший одни
	// штрихи, либо вкладка со вчерашним бандлом стёрли бы человеку всю его роспись молча, то есть
	// ровно тот исход, ради которого на этой строке вообще заведён CAS.
	//
	// Оба поля разом — противоречие, а не задача о приоритете: отказ InvalidArgument.
	RasterMediaId int
	ClearRaster   bool
	Actor         string
}

// DesignVectorImport — подшивка УЖЕ ЗАГРУЖЕННОГО векторного файла в полосу как слоя правки:
// медиа держит авторитетный SVG, слой — его редактируемую проекцию, а SourceMediaId и есть ребро
// между ними.
//
// ⚠ ЭТО НИЧЕГО НЕ ТРАТИТ, И В ЭТОМ ГРАНИЦА С ГЕНЕРАЦИЕЙ. Векторизация машиной — платный вызов
// поставщика и идёт через StartRun с kind = vector; этот запрос подшивает файл, который уже
// существует. Две двери для денег означали бы две проверки бюджета.
//
// ИДЕМПОТЕНТНОСТЬ ЗДЕСЬ — ПО (TechCardId, SourceMediaId), А НЕ ПО ClientRequestId, и это
// вынужденно: у design_edit_layer (0343) колонки под запросный ключ нет вовсе, а 0350 её не
// добавляла. Повтор после потерянного ответа приезжает с ТЕМ ЖЕ файлом — медиа загружено раньше и
// его id у клиента на руках, — поэтому пара «карточка + файл» покрывает ровно тот случай, ради
// которого идемпотентность и нужна. Пере-проверка живёт ВНУТРИ SERIALIZABLE-транзакции, где
// обычный SELECT уже блокирует, поэтому гонка двух повторов закрыта, а не сглажена.
type DesignVectorImport struct {
	TechCardId      int
	ClientRequestId string
	SourceMediaId   int
	SourcePictureId int
	Origin          string
	BaseMediaId     int
	Strokes         json.RawMessage
	Actor           string
}

// DesignEditLayerFlatten — регистрация уже растеризованного клиентом изображения как картинки
// полосы. ExpectedRev ОБЯЗАТЕЛЕН: без него коллега сохраняет r4, а флэттен материализует его
// под намерением того, кто видел r3.
type DesignEditLayerFlatten struct {
	TechCardId  int
	LayerId     int
	ExpectedRev int
	MediaId     int
	Actor       string
}

// DesignReferenceRole — роль референса. Пустая Role СТИРАЕТ роль: «сторона не названа» это
// настоящий ответ, и он не должен требовать второго глагола.
type DesignReferenceRole struct {
	TechCardId int
	MediaId    int
	Role       string
	// Note — записка человека про ЭТУ картинку, пишется ТЕМ ЖЕ апсертом, что и роль: она лежит на
	// той же строке, и второй глагол для неё был бы вторым писателем, способным сработать
	// наполовину.
	//
	// АСИММЕТРИЯ С РОЛЬЮ НАМЕРЕННА. Пустая записка на строке, которая сохраняет роль, ОЧИЩАЕТ
	// записку: записка — это текст, и пустой текст для неё настоящий ответ. Пустая РОЛЬ удаляет
	// строку и уносит записку с собой, потому что строка И ЕСТЬ существование роли.
	Note string
	// NoteOmitted — «про записку НИЧЕГО НЕ СКАЗАНО», третье состояние рядом с «вот текст» и
	// «сотри». Без него у поля было два состояния там, где глаголу нужно три, и недостающее не
	// академическое: вкладка со старым JS не шлёт поле вовсе, proto3 декодирует отсутствие в "",
	// апсерт читает "" как «очистить», и слова человека исчезают без единого жеста с чьей-либо
	// стороны. Тот же приём и по той же причине несёт TechCard.GarmentDescriptionOmitted — и
	// именно поэтому ТО поле этой беды не знало.
	NoteOmitted bool
	// DetailSlotId — АДРЕС ДЕТАЛИ, про которую этот референс (0360, J-9). Правило записи имеет
	// ТРИ члена, и третий — не украшение, а тот же приём, что спас записку выше:
	//
	//	Role != detail            — колонка ОЧИЩАЕТСЯ. Референс, переставший быть деталью, не может
	//	                            продолжать указывать на деталь;
	//	Role == detail, Id  > 0   — колонка ПИШЕТСЯ;
	//	Role == detail, Id == 0   — колонка ОСТАЁТСЯ КАК БЫЛА. Ноль на проводе это «про слот ничего
	//	                            не сказано», и никогда «сотри»: proto3 не отличает незаполненный
	//	                            int32 от нуля, поэтому вкладка со старым JS, переписывающая
	//	                            записку или ординал, стёрла бы связь с деталью без единого
	//	                            жеста человека. Ровно эта беда уже случалась с Note (0348).
	DetailSlotId int
	Ordinal      int
	Actor        string
}

// DesignAssetUpsert — ОДНА строка полки, заведённая либо переписанная целиком.
//
// ЭТО ЗАМЕНА, А НЕ ПАТЧ, и флага присутствия у полей нет намеренно: экран держит всю плитку в
// форме и шлёт её целиком. Вызывающего, который знает одно свойство ассета и не знает остальных,
// не существует, поэтому флаг на поле был бы третьим состоянием без единого писателя — и первым
// же местом, где кто-нибудь потеряет ordinal или родословную.
//
// AssetId == 0 — ЗАВЕДЕНИЕ. Один глагол на оба жеста стоит здесь по той же причине, по какой
// SetBenchSlot один: у экрана жест один — «плитку заполнили и сохранили».
type DesignAssetUpsert struct {
	TechCardId int
	AssetId    int
	Kind       string
	Name       string
	MediaId    int
	ColourCode string
	ColourHex  string
	Note       string
	// DerivedFromAssetId — ткань, из которой сделан ЭТОТ паттерн (V-7). Осмысленно только на
	// паттерне; у ткани и фурнитуры отказывается (ErrDesignAssetNotAPattern).
	DerivedFromAssetId int
	RepeatMm           int
	RotationDeg        int
	Ordinal            int
	Actor              string
}

// Validate — ВСЕ ЧИСТЫЕ ПРАВИЛА АПСЕРТА, отделённые от тех, которым нужна база.
//
// ЗАЧЕМ ОТДЕЛЬНО. Правил здесь семь, и каждое из них — слово контракта, а не деталь SQL; оставь их
// внутри транзакции, и единственный способ проверить их — база, то есть на практике никак. Всё,
// чему нужна строка (родитель существует и той же карточки, медиа не чужое, полка не переполнена),
// осталось в сторе и проверяется В ТОЙ ЖЕ транзакции, что и запись, — вынести это сюда значило бы
// завести TOCTOU с красивым именем.
//
// ПОРЯДОК ПРОВЕРОК ЗНАЧИМ ровно в одном месте: границы чисел читаются РАНЬШЕ правила «не-паттерн
// не носит раппорт». Раппорт в пять метров — бессмыслица независимо от полки, и назвать её
// «это не паттерн» значило бы отправить человека менять полку вместо числа.
func (r DesignAssetUpsert) Validate() error {
	if !IsDesignAssetKind(r.Kind) {
		return fmt.Errorf("%w: unknown design asset kind %q", ErrDesignAssetKindUnknown, r.Kind)
	}
	name := strings.TrimSpace(r.Name)
	if name == "" {
		return fmt.Errorf("%w: a shelf row is cited by its name", ErrDesignAssetNameRequired)
	}
	if len([]rune(name)) > MaxDesignAssetNameRunes {
		return fmt.Errorf("%w: an asset name is at most %d characters",
			ErrDesignInvalidArgument, MaxDesignAssetNameRunes)
	}
	if len([]rune(strings.TrimSpace(r.Note))) > MaxDesignAssetNoteRunes {
		return fmt.Errorf("%w: an asset note is at most %d characters",
			ErrDesignInvalidArgument, MaxDesignAssetNoteRunes)
	}
	if r.RepeatMm < 0 || r.RepeatMm > MaxDesignAssetRepeatMm {
		return fmt.Errorf("%w: repeat_mm is whole millimetres from 0 to %d",
			ErrDesignInvalidArgument, MaxDesignAssetRepeatMm)
	}
	if r.RotationDeg < 0 || r.RotationDeg > MaxDesignAssetRotationDeg {
		return fmt.Errorf("%w: rotation_deg is degrees clockwise from 0 to %d",
			ErrDesignInvalidArgument, MaxDesignAssetRotationDeg)
	}
	if r.DerivedFromAssetId < 0 {
		return fmt.Errorf("%w: derived_from_asset_id %d", ErrDesignInvalidArgument, r.DerivedFromAssetId)
	}
	if r.Kind != DesignAssetKindPattern && (r.DerivedFromAssetId != 0 || r.RepeatMm != 0) {
		return fmt.Errorf("%w: %q carries a parent or a repeat, and both belong to a pattern",
			ErrDesignAssetNotAPattern, r.Kind)
	}
	// САМ СЕБЕ РОДИТЕЛЬ — не «странный ввод», а ребро, которого нельзя нарисовать: у полки
	// появилась бы строка, чья родословная указывает на неё же, и всякий обход происхождения
	// закрутился бы на ней навсегда. Схема этого не ловит: FK на СВОЮ таблицу самоссылку разрешает.
	if r.AssetId != 0 && r.DerivedFromAssetId == r.AssetId {
		return fmt.Errorf("%w: asset %d cannot be built from itself", ErrDesignInvalidArgument, r.AssetId)
	}
	return nil
}

// DesignAssetPlacementSet — постановка либо перенос ОДНОЙ метки на флэте.
//
// PlacementId == 0 — новая метка; иначе двигается существующая. TechCardId тут есть, хотя у самой
// строки размещения его нет: он и есть та карточка, ПРИНАДЛЕЖНОСТЬ КОТОРОЙ проверяется у обоих
// концов — и у ассета, и у картинки. Без него запись приняла бы метку чужого ассета на своём флэте
// и наоборот, а схема этого выразить не может.
type DesignAssetPlacementSet struct {
	TechCardId  int
	PlacementId int
	AssetId     int
	PictureId   int
	// Annotation — геометрия метки, уже проверенная общим сводом указаний и сериализованная
	// protojson'ом провода. Стор её не разбирает и не переписывает.
	Annotation json.RawMessage
	Note       string
	Actor      string
}

// RefusePicture — МОЖЕТ ЛИ ЭТА КАРТИНКА НЕСТИ МЕТКУ ПОЛКИ. Один вопрос, две половины: та ли это
// карточка и тот ли это РОД кадра.
//
// ⚠ РОД ПРОВЕРЯЛСЯ НИГДЕ, ХОТЯ О НЁМ ГОВОРЯТ ВСЕ ТРИ ЗАПИСИ ФАКТА: схема («флэт, на котором стоит
// метка», 0354), контракт («puts ONE mark on ONE flat») и клиент, который предлагает выбрать
// только флэты. Метка, поставленная на рендер или на кадр поворотного стола, принималась и
// возвращалась полосой как метка на флэте — система утверждала о картинке род, которого у той нет.
//
// И ЭТО НЕ ФОРМАЛЬНОСТЬ. Координаты метки — ДОЛИ ЭТОГО КАДРА (0354, там же). Флэт и рендер одного
// вида кадрированы по-разному, поэтому одна доля показывает у них на разные места изделия: метка,
// уехавшая на рендер, указывает не туда, куда указал человек, и заметить это по данным нечем.
//
// ⚠ ПУСТОЙ РОД ЧИТАЕТСЯ КАК flat, через DesignKindOrFlat, а не сравнением с "". Один способ
// прочесть пустую колонку на весь репозиторий — это ровно то, ради чего та функция и заведена
// (DEFAULT 'flat' в 0349); второй здесь разошёлся бы с первым при первой же правке словаря.
//
// ⚠ ДВЕ ПОЛОВИНЫ ОТВЕЧАЮТ РАЗНЫМИ ТОКЕНАМИ, потому что человеку они предлагают разные починки:
// foreign_card_plate — «ты не на той карточке», wrong_kind — «эту метку надо ставить на флэт».
// Общий invalid_argument отдал бы клиенту один экран на два разных действия.
func (r DesignAssetPlacementSet) RefusePicture(pic DesignPicture) error {
	if pic.TechCardId != r.TechCardId {
		return fmt.Errorf("%w: picture %d belongs to tech card %d",
			ErrDesignForeignCardPlate, pic.Id, pic.TechCardId)
	}
	if kind := DesignKindOrFlat(pic.Kind); kind != DesignPictureKindFlat {
		return fmt.Errorf("%w: picture %d is a %s, and a shelf mark is drawn on a flat",
			ErrDesignWrongKind, pic.Id, kind)
	}
	return nil
}

// DesignRunPage — страница истории. Курсор, а не смещение: строки рождаются В ГОЛОВЕ этого
// списка, и страница по offset дублировала бы и пропускала ровно тогда, когда кто-то генерит.
type DesignRunPage struct {
	TechCardId int
	Limit      int
	// Cursor — курсор ПРОГОНОВ, 0 = с начала (самая свежая строка).
	Cursor int
	// BatchCursor — курсор ПАЧЕК. Страница везёт два списка, и у каждого свой keyset: пачка и
	// прогон рождаются независимо, и один курсор на оба означал бы, что более активный список
	// таскает менее активный за собой. Оба едут в ОДНОМ непрозрачном page_token, потому что на
	// проводе токен один.
	BatchCursor     int
	IncludeArchived bool
}

// DesignRunPageResult — страница вместе с картинками своих строк и полками загрузок.
type DesignRunPageResult struct {
	Runs    []DesignRun
	Batches []DesignBatch
	// NextCursor / NextBatchCursor == 0 означает «этот список кончился здесь». Токен пуст только
	// когда кончились ОБА.
	NextCursor      int
	NextBatchCursor int
}

// DesignCardOutput — ОДИН ГЕНЕРАТИВНЫЙ ВЫХОД КАРТОЧКИ вместе со штампом прогона, из которого он
// вышел.
//
// ШТАМП НУЖЕН РОВНО ПОТОМУ, ЧТО СПИСОК НЕ ПОСТРАНИЧНЫЙ. Прогон такого кадра вполне может лежать
// вне выданной страницы истории — тогда из самой картинки не восстановить двух вещей, и обе
// несущие:
//
//   - В КАКОЙ РАЗДЕЛ ОНА ИДЁТ. Перекрас рождает кадры рода `render` (DesignPictureKindOfRun), и это
//     правда по существу: на выходе фотография изделия. Читатель, глядящий на Kind картинки, кладёт
//     результат ON MODEL в RENDERS. Род ПРОГОНА — единственное место, где различие выживает.
//   - КАКОЙ КОЛОРВЕЙ И КАКАЯ РЕВИЗИЯ. Оба живут на прогоне и из картинки не выводятся.
//
// ⚠ ШТАМП — ЭТО «ИЗ КАКОГО ПРОГОНА ЭТА ВЕТКА ВЫРОСЛА», А НЕ «ЭТО ВЫДАЛ ПРОГОН». Различие
// НАСТОЯЩЕЕ, и раньше здесь стояло обещание, которого происхождение не даёт. Кроп (pictures.go) и
// флэттен (layer.go) НАСЛЕДУЮТ run_id родителя, поэтому нарисованный руками флэттен поверх рендера
// приезжает как RunId=X, RunKind="render", RunRrev=7 — неотличимо от прямого выхода прогона X,
// если читать ТОЛЬКО штамп. Что их разводит: Picture.SourceClass (у выхода прогона `generated`, у
// правки поверх — редакторский класс, см. DesignSourceAIEdits) и Picture.DerivedFrom (у прямого
// выхода он пуст). Оба едут на проводе. Читатель, которому важно «сгенерировано ли это», обязан
// смотреть туда, а не на непустой RunKind.
//
// ЧТО ЖЕ ШТАМП УДОСТОВЕРЯЕТ ТОЧНО: род/ревизию/колорвей ТОГО ПРОГОНА, из которого вышел предок
// этой картинки, — а раз наследование сохраняет и kind, и колорвей, это ровно те факты, которые
// нужны, чтобы положить кадр в правильный раздел.
//
// Кадр из пачки загрузки прогона не имеет вовсе: RunId 0 и RunKind "" — придумывать штамп нечему.
// ⚠ И «прогона нет» НЕ ЗНАЧИТ «есть пачка»: флэттен без подложки (layer.go, parent == nil) кладёт
// NULL и в run_id, и в batch_id. Такой кадр законен, попадает в этот список по своему роду и
// отвечает нулём на оба вопроса о происхождении.
//
// BatchId здесь НЕ ДУБЛИРУЕТСЯ: он уже колонка самой картинки, и вторая копия была бы вторым
// источником правды о том же факте.
type DesignCardOutput struct {
	Picture DesignPicture
	// RunId / RunKind / RunRrev / RunColorwayId — штамп прогона. Нули и пустая строка означают
	// «прогона нет» (кадр из пачки ЛИБО безродный флэттен), а не «прогон неизвестен».
	//
	// ⚠ RunColorwayId — это колорвей ПРОГОНА, и он НЕ ключ раздела. Ключ раздела — Picture.ColorwayId
	// (по нему режется окно потолка и считается OutputsTotalByColorway). На всём, что рождено
	// прогоном, они равны по записи: queue.go кладёт в кадр колорвей строки, кроп и флэттен его
	// наследуют. Расходятся они на кадре БЕЗ прогона — у загруженной плиты колорвей назван, а
	// прогона нет, — и сужать по RunColorwayId значило бы выкинуть её из собственного раздела.
	RunId         int
	RunKind       string
	RunRrev       int
	RunColorwayId int
}

// DesignBand — вся полоса одним чтением. Агрегаты считаются В ТОЙ ЖЕ читающей транзакции, что
// и страница: посчитанные по загруженной странице, они соврали бы шапке ровно на то, чего нет
// на экране.
type DesignBand struct {
	Bench      []DesignBenchSlot
	Budget     DesignBudget
	References []DesignReference
	Layers     []DesignEditLayer

	// RenderBenchColorways — колорвеи, чей РЕНДЕР-ВЕРСТАК на этой карточке ЗАНЯТ: есть хотя бы
	// один render-слот с плитой (неспрятанной, с медиа). 0 в списке = неатрибутированный
	// легаси-верстак. Ворота 3D открываются НА КОЛОРВЕЙ (L-3) и спрашивают ровно то, что читает
	// отбор входов (designSelectBench) — СЛОТЫ, а не картинки: загруженный, но не поставленный
	// рендер двери не открывает, иначе прогон платил бы за пустой набор плит. Считается по всей
	// карточке, никогда по загруженной странице. Отсортировано по возрастанию.
	//
	// ⚠ НЕ СВОДИТСЯ К HasFabricRender И НЕ ЯВЛЯЕТСЯ ЕГО РАЗБИВКОЙ: тот считает картинки полосы
	// (0349, W-13) и остаётся подсказкой экрана, этот — заявление о том, что 3D можно запустить.
	// Карточка с рендером в пачке и пустым верстаком даёт HasFabricRender = true и ПУСТОЙ список.
	RenderBenchColorways []int

	// Batches — полки ручной загрузки со своими картинками, свежие первыми. Пока генеративная
	// машина отрезана от волны, это ГЛАВНАЯ ветка чтения, а не второстепенная: прогонов на бете
	// не будет ни одного, а пачки будут все.
	Batches []DesignBatch
	// TotalBatches — сколько пачек у карточки ВСЕГО, посчитано в той же читающей транзакции.
	// Нужно, чтобы «после потолка» было измеримо, а не догадкой.
	TotalBatches int

	TotalRuns    int
	ArchivedRuns int
	MaxRrev      int
	// ColourRecipes — сырые JSON рецептов цвета (design_run.params->'$.colour') последних
	// render-прогонов, свежие первыми, уже дедуплицированные. Стор их не разбирает: форма
	// рецепта — контракт.
	ColourRecipes []json.RawMessage
	// HiddenByRun — run_id → сколько картинок ЭТОГО прогона скрыто, по всей карточке.
	// Прогоны без скрытых картинок в карте отсутствуют.
	//
	// ПАЧЕК ЗДЕСЬ НЕТ, И ЭТО РЕШЕНИЕ, А НЕ ПРОБЕЛ. Агрегат по прогонам существует потому, что
	// история ПАГИНИРОВАНА: свёрнутой строке вне страницы всё равно нужен бейдж «· 2 hidden», а
	// её картинок клиенту не дали. У пачки такой проблемы нет: каждая пачка, доехавшая в полосе,
	// приезжает СО ВСЕМ своим составом (один жест человека — единицы файлов), и скрытость её
	// картинки читается по самой картинке. Пачка за потолком бейджа не получает вовсе — её нет и
	// на экране.
	HiddenByRun map[int]int
	// HiddenByBatch — batch_id → сколько картинок ЭТОЙ пачки скрыто, по всей карточке. Существует
	// по той же причине, что и HiddenByRun: полки тоже пагинированы, и полка вне страницы иначе
	// осталась бы без бейджа «· 2 hidden», которого клиенту нечем посчитать.
	HiddenByBatch map[int]int

	// HasFabricRender — у карточки есть ХОТЯ БЫ ОДИН НЕСПРЯТАННЫЙ КАДР рода `render` (W-13).
	// Считается в той же читающей транзакции по ВСЕЙ карточке, а не по загруженной странице.
	//
	// ⚠ ЭТО БОЛЬШЕ НЕ ЗЕРКАЛО ГЕЙТА 3D, И ФОРМУЛИРОВКУ ПРИШЛОСЬ ПЕРЕПИСАТЬ (F4). Пока ось была
	// одна, «на карточке есть рендер» и «3D запустится» отвечали одинаково, и поле честно звалось
	// зеркалом. С тех пор гейт спрашивает ЗАНЯТЫЕ РЕНДЕР-СЛОТЫ (RenderBenchColorways ниже) —
	// потому что именно из них прогон собирает входы, — и два ответа законно расходятся:
	// загруженный, но не поставленный рендер даёт здесь true, а множество оставляет пустым.
	// Клиент, рисующий дверь 3D по ЭТОМУ флагу, ведёт человека в отказ `no_fabric_render` — то
	// есть ровно в тот исход, ради устранения которого поле заводили.
	//
	// ЧТО ОНО ЗНАЧИТ ТЕПЕРЬ: «у карточки вообще есть фабрик-рендеры» — вопрос про полосу, не про
	// дверь. Полезно для пустых состояний («рендеров ещё нет» против «рендеры есть, разложи их»),
	// и не более того. Поле оставлено, а не снято, потому что оно уехало на прод с 0349 и на этот
	// вопрос по-прежнему отвечает правду; снятие сломало бы клиентов ради переименования.
	HasFabricRender bool

	// Assets / AssetPlacements — ПОЛКИ КАРТОЧКИ И ВСЯ РАЗМЕТКА, КОТОРУЮ ОНИ ОСТАВИЛИ НА ФЛЭТАХ
	// (0354). Читаются В ТОЙ ЖЕ транзакции, что и верстак с референсами: студия рисует стену полок,
	// верстак и референсы одним кадром, и второе чтение позволило бы им разойтись во мнении о том,
	// какой момент карточки на экране.
	//
	// ЭТИ ДВА СПИСКА НЕ ПАГИНИРОВАНЫ, В ОТЛИЧИЕ ОТ ПРОГОНОВ, и это не недосмотр. Полок у карточки
	// горстка по построению — изделие из сорока тканей это не изделие, — а сервер их ещё и
	// ОГРАНИЧИВАЕТ (MaxDesignAssetsPerCard, отказ asset_too_many на записи). Потолок и есть то, что
	// делает «всё сразу» честным: число на стене полок — всегда вся правда, а не «столько влезло».
	//
	// Разметка едет РЯДОМ с ассетами, а не вложенной в них, потому что экран читает её с другой
	// стороны — «что размечено на ЭТОМ чертеже», — и вложенность заставила бы клиента обойти все
	// полки ради одной картинки.
	Assets          []DesignAsset
	AssetPlacements []DesignAssetPlacement

	// Outputs / OutputsTotal / OutputsTotalByColorway — ГЕНЕРАТИВНЫЕ ВЫХОДЫ КАРТОЧКИ, а не выходы
	// загруженной страницы. Роды render|threed|pattern|recolor, свежие первыми, вместе со штампом
	// прогона.
	//
	// ЗАЧЕМ ОТДЕЛЬНЫЙ СПИСОК, КОГДА ЕСТЬ Runs. Runs — ОДНА страница ленты (12 строк), и раздел
	// «рендеры этой карточки» читал именно её. Всякий прогон любого рода — перетрассировка флэта,
	// векторная перерисовка, попытка 3D — выталкивает из окна один старый прогон, и рендеры
	// покидали раздел по одному, унося с собой кропы, нарезанные из их листов. Заголовок обещал
	// КАРТОЧКУ, ответ имел область видимости СТРАНИЦЫ.
	//
	// ⚠ ЧЕМ ЭТОТ ОТВЕТ ОГРАНИЧЕН. Здесь стоял довод «эти роды привязаны к деньгам: за каждый такой
	// кадр заплачено, потолок практически недостижим». ЭТО НЕВЕРНО. Кроп (pictures.go, «никаких
	// денег на разрез не потрачено») и флэттен (layer.go) наследуют run_id И kind, то есть попадают
	// в этот же список, стоят ноль и не дедуплицируются; hidden_at предикатом намеренно не
	// фильтруется, поэтому «спрятать и перерезать» добавляет строки навсегда. Замер: один платный
	// рендер-прогон плюс три бесплатных цикла = 16 выходов, ещё дюжина = 200.
	//
	// Поэтому потолок ПОКОЛОРВЕЙНЫЙ (MaxCardOutputsPerColorway), а не общий: он ограничивает размер
	// ОТВЕТА, а тратится по той оси, по которой раздел сужается на экране. Общий потолок с «свежие
	// первыми» выкашивал бы самые старые кадры карточки — то есть мог опустошить раздел целого
	// колорвея, воспроизведя дефект H-9 на горизонте в 200 строк.
	//
	// OutputsTotalByColorway — ключ Picture.ColorwayId (0 = неатрибутированный), значение — сколько
	// выходов у этого колорвея ВСЕГО. Именно он подписывает усечение тому читателю, который сузил;
	// OutputsTotal (сумма тех же чисел) отвечает только на карточный вопрос. Ключ КАРТИНКИ, а не
	// прогона, — довод у designCardOutputsColorway и у DesignCardOutput.RunColorwayId.
	//
	// СПРЯТАННЫЕ КАДРЫ ВХОДЯТ СО СВОИМ ФЛАГОМ — тот же контракт, что у Runs и Batches: сервер не
	// врёт о том, что существует, фильтрует клиент.
	Outputs                []DesignCardOutput
	OutputsTotal           int
	OutputsTotalByColorway map[int]int

	Runs            []DesignRun
	NextCursor      int
	NextBatchCursor int
}

// ---------------------------------------------------------------------------
// Запросные структуры волны 2 — заморожены СЕЙЧАС, тела приезжают позже
// ---------------------------------------------------------------------------

// DesignRunStart — открытие платного задания.
type DesignRunStart struct {
	TechCardId       int
	ClientRequestId  string
	Kind             string
	Ask              string
	Params           json.RawMessage
	Inputs           json.RawMessage
	ProfileName      string
	ProfileVersion   int
	FitAtLaunch      string
	RequestedOutputs int
	PriceEstimate    decimal.NullDecimal
	Currency         string
	Author           string
	// RerunOf — прогон, который повторяем (W-7). Снимок входов собирает СЕРВЕР, а не клиент:
	// клиентский снимок позволил бы истории утверждать входы, которых не было.
	RerunOf int
	// ColorwayId — колорвей прогона (0356), из ДЕЙСТВУЮЩИХ params (реран наследует их у
	// родителя). 0 = не назван. Стор отказывает значению на роде без оси колорвея
	// (ErrDesignColorwayForbidden) и чужому колорвею (ErrDesignForeignColorway) — в той же
	// транзакции, что резерв денег, то есть ДО того, как строка что-либо заняла.
	ColorwayId int
	// ColorwayStated — НАЗВАЛ ЛИ КОЛОРВЕЙ САМ ВЫЗЫВАЮЩИЙ, или он унаследован из замороженных
	// params родителя. Ровно то же различение, что у detail-слотов и полок в хендлере («адрес,
	// названный КЛИЕНТОМ, отвечает за себя, унаследованный — нет»), и заведено оно по той же
	// причине: колорвей законно удаляют, а замороженные params правке не подлежат. Строгая
	// проверка унаследованного значения сделала бы такой прогон неповторимым НАВСЕГДА, без
	// единого написания запроса, которое прошло бы. Унаследованный и исчезнувший колорвей
	// деградирует в неатрибутированный — ровно так же, как FK SET NULL погасил колонку у
	// родителя; унаследованный ЧУЖОЙ по-прежнему отказывается (это состояние обратимо).
	//
	// ⚠ «НАЗВАЛ КОЛОРВЕЙ» — НЕ ТО ЖЕ, ЧТО «ПРИСЛАЛ ПАРАМЕТРЫ», и одно время писавший это поле
	// хендлер их путал. Реран, приславший params по СВОЕЙ причине (поправил `ask`, добавил вид),
	// колорвея не называет: ноль в params рерана значит «наследуй», и наследование делается ДО
	// этой строки. Считая такого вызывающего «назвавшим», мы отдавали строгую половину стора
	// прогону, которому нечего было бы прислать иначе, — и законный реран прогона, чей колорвей с
	// тех пор удалили, отказывался `foreign_colorway` НАВСЕГДА. Пишется поэтому из
	// `req.GetParams().GetColorwayId() > 0`.
	ColorwayStated bool
}

// DesignRunStarted — строка прогона плюс бюджет ПОСЛЕ резерва, чтобы полоса двигалась вместе
// с кликом, а не на следующем опросе.
type DesignRunStarted struct {
	Run    DesignRun
	Budget DesignBudget
	// Idempotent — прогон с этим client_request_id уже существовал.
	Idempotent bool
	// Resumed — ЭТОТ вызывающий ПЕРЕХВАТИЛ брошенную строку и вправе доисполнить её.
	//
	// ⚠ ЗАЧЕМ ОТДЕЛЬНОЕ ПОЛЕ, А НЕ ВЫЧИСЛЕНИЕ ПО ЛИЗЕ У ВЫЗЫВАЮЩЕГО. «Лиза истекла» — это ВОПРОС;
	// ответ на него имеет право дать только тот, кто перехват и совершил, потому что перехват
	// исключающий: из двух одновременных повторов истёкшую лизу видят ОБА, а забирает строку
	// ровно один. Вызывающий, считающий признак сам, оплачивает модель во второй раз — и это не
	// гипотеза, а тот самый дефект, ради которого поле заведено.
	//
	// Resumed истинно ТОЛЬКО вместе с Idempotent: первый запуск не перехватывает ничего, он
	// рождает строку и получает её токен от INSERT'а.
	Resumed bool
}

// DesignAttemptStart — начало одной платной попытки.
type DesignAttemptStart struct {
	RunId      int
	ClaimToken string
	AttemptNo  int
	Provider   string
}

// DesignAttemptFinish — закрытие попытки вместе с её деньгами. Ретрай платит второй раз, и
// полоса бюджета это ВИДИТ.
type DesignAttemptFinish struct {
	RunId             int
	AttemptNo         int
	ProviderRequestId string
	State             string
	Price             decimal.NullDecimal
	ErrorCode         string
	EstShare          decimal.NullDecimal
}

// DesignPictureInsert — один выход прогона.
type DesignPictureInsert struct {
	MediaId        int
	Ordinal        int
	Kind           string
	GhostView      string
	CompositeViews json.RawMessage
	SourceClass    string
	MixedInput     bool
}

// DesignRunComplete — закрытие прогона. Частичный ответ = меньше картинок, статус всё равно
// done. OutputText несёт результат текстового прогона (kind=draft_idea).
type DesignRunComplete struct {
	RunId      int
	ClaimToken string
	Outputs    []DesignPictureInsert
	OutputText sql.NullString
}

// DesignRunFail — провал попытки: экспонента ретрая ЛИБО терминальный failed.
type DesignRunFail struct {
	RunId       int
	ClaimToken  string
	ErrorCode   string
	LastError   string
	Retryable   bool
	NextAttempt time.Time
}
