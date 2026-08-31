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

// Виды кадра. ОН ЖЕ СЛОВАРЬ ВТОРОЙ ОСИ ВЕРСТАКА: design_bench_slot.kind (0349) объявлен тем же
// словарём намеренно — «род» у слота и у кадра обязан быть одним понятием, иначе рендер встанет
// на технический лист.
const (
	DesignPictureKindFlat   = "flat"
	DesignPictureKindRender = "render"
	DesignPictureKindThreed = "threed"
)

// IsDesignPictureKind сообщает, известен ли род кадра. Словарь растёт, CHECK в схеме намеренно
// нет — поэтому проверяет Go, и отказ называет значение, а не отдаёт сырой 1265.
func IsDesignPictureKind(v string) bool {
	return v == DesignPictureKindFlat || v == DesignPictureKindRender || v == DesignPictureKindThreed
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

// Виды прогона. `vector` приехал волной 2: векторизация — это ДЕНЬГИ, и у денег одна дверь, а не
// отдельный RPC мимо бюджета (31 §решения).
const (
	DesignRunKindFlat      = "flat"
	DesignRunKindRender    = "render"
	DesignRunKindThreed    = "threed"
	DesignRunKindVector    = "vector"
	DesignRunKindDraftIdea = "draft_idea"
)

// IsDesignRunKind сообщает, известен ли род прогона.
func IsDesignRunKind(v string) bool {
	switch v {
	case DesignRunKindFlat, DesignRunKindRender, DesignRunKindThreed,
		DesignRunKindVector, DesignRunKindDraftIdea:
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

	// ───────────────────────── отказы генеративной половины ─────────────────────────

	// ErrDesignBudgetExceeded — дневной потолок не пускает прогон: `spent + reserved + оценка`
	// вышло за `design_settings.daily_budget`. Проверка стоит ВНУТРИ той же транзакции, что и
	// резерв, потому что «посмотрел, потом положил» пропускает два одновременных клика.
	// FailedPrecondition на проводе: это не поломка запроса, а состояние дня.
	ErrDesignBudgetExceeded = errors.New("design: budget_exceeded")
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
	Prompt      sql.NullString `db:"prompt"`
	CreatedAt   time.Time      `db:"created_at"`
	StartedAt   sql.NullTime   `db:"started_at"`
	CompletedAt sql.NullTime   `db:"completed_at"`

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
	SourceClass    string         `db:"source_class"`
	MixedInput     bool           `db:"mixed_input"`
	LayerRev       int            `db:"layer_rev"`
	// Selected — кадр помечен выбранным (0350, W-12). НЕ обратная сторона hidden_at: спрятать —
	// убрать с глаз, выбрать — поднять над остальными; выбранных может быть несколько.
	Selected  bool           `db:"selected"`
	HiddenAt  sql.NullTime   `db:"hidden_at"`
	HiddenBy  sql.NullString `db:"hidden_by"`
	CreatedAt time.Time      `db:"created_at"`

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
	Note    sql.NullString `db:"note"`
	Ordinal int            `db:"ordinal"`
	SetBy   string         `db:"set_by"`
	SetAt   time.Time      `db:"set_at"`
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
	Ordinal            int            `db:"ordinal"`
	CreatedBy          string         `db:"created_by"`
	CreatedAt          time.Time      `db:"created_at"`
	UpdatedAt          time.Time      `db:"updated_at"`

	// Media резолвится читателем полосы тем же батчем, что и медиа кадров, — ровно как
	// DesignPicture.Media. Пропавший файл оставляет здесь nil, а не выбрасывает ассет: «файл
	// исчез» — это факт, который полка обязана уметь показать.
	Media *MediaFull `db:"-"`
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
type DesignSettings struct {
	DailyBudget    decimal.Decimal `db:"daily_budget"`
	Currency       string          `db:"currency"`
	BudgetTimezone string          `db:"budget_timezone"`
	UpdatedBy      string          `db:"updated_by"`
	UpdatedAt      time.Time       `db:"updated_at"`
}

// DesignBudget — денежная полоса дня: `today $0.41 of $2.00`.
//
// ДВА ПОЛЯ, А НЕ ОДНА СУММА, хотя гейт потолка их складывает: одно поле «потрачено», несущее
// сумму, соврало бы читателю о том, что реально оплачено.
type DesignBudget struct {
	Day      string // YYYY-MM-DD, посчитанный в BudgetTimezone В GO
	Spent    decimal.Decimal
	Reserved decimal.Decimal
	Cap      decimal.Decimal
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
	Actor       string
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
	Ordinal     int
	Actor       string
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

// DesignBand — вся полоса одним чтением. Агрегаты считаются В ТОЙ ЖЕ читающей транзакции, что
// и страница: посчитанные по загруженной странице, они соврали бы шапке ровно на то, чего нет
// на экране.
type DesignBand struct {
	Bench      []DesignBenchSlot
	Budget     DesignBudget
	References []DesignReference
	Layers     []DesignEditLayer

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

	// HasFabricRender — у карточки есть ХОТЯ БЫ ОДИН НЕСПРЯТАННЫЙ кадр рода `render` (W-13).
	// Считается в той же читающей транзакции по ВСЕЙ карточке, а не по загруженной странице:
	// рендер вполне может лежать за потолком страницы, и посчитанный по ней флаг закрыл бы 3D
	// ровно там, где оно законно. Гейт стоит на сервере в StartRun, это поле — его зеркало для
	// экрана, чтобы кнопка не обещала того, за что сервер откажет.
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
