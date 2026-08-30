package entity

import (
	"database/sql"
	"database/sql/driver"
	"encoding/json"
	"errors"
	"fmt"
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
// произвели, верстак (какая плита принята какой стороной изделия) и замороженные версии листа,
// которые печатают.
//
// ЧТО ЭТИ ТИПЫ ЕСТЬ И ЧЕМ НЕ ЯВЛЯЮТСЯ. Это строки таблиц 0340–0347 плюс несколько запросных
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

// Действия журнала выпуска.
const (
	DesignIssueMinted  = "minted"
	DesignIssuePrinted = "printed"
	DesignIssueShared  = "shared"
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
	// ErrDesignSlotInVersion — слот процитирован замороженной версией листа. FK RESTRICT тут
	// стоять не может: и слот, и версия каскадятся от tech_card, и удаление карточки уперлось бы
	// в 1451 в ЕДИНСТВЕННОЙ операции удаления, которой нечего было бы предложить человеку.
	ErrDesignSlotInVersion = errors.New("design: slot_in_version")
	// ErrDesignNotADetailSlot — DeleteDesignDetailSlot позвали на одну из четырёх сторон.
	// В плане этот отказ не назван; он добавлен, потому что иначе единственный законный ответ
	// на «удали front» — молчаливое удаление стороны, которую слот-адрес обязан переживать.
	ErrDesignNotADetailSlot = errors.New("design: not_a_detail_slot")
	// ErrDesignInSlot / ErrDesignInVersion / ErrDesignLiveRunInput / ErrDesignLiveCropParent —
	// четыре сторожа HidePicture. Читаются в ТОЙ ЖЕ транзакции, что и UPDATE, иначе TOCTOU.
	ErrDesignInSlot         = errors.New("design: in_slot")
	ErrDesignInVersion      = errors.New("design: in_version")
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

	// ───────────────────────── отказы атомарного минта ─────────────────────────
	//
	// ДВА ЗАМКА, А НЕ ОДИН, и это не перестраховка. expected_lock_version стережёт ДОКУМЕНТ
	// (выноски, эскизы, спецификацию), expected_plates — ВЕРСТАК (какая плита какой стороной
	// принята). Это две разные вещи, живущие в разных таблицах и двигаемые разными жестами:
	// минт, проверивший только документ, заморозил бы состав, который кто-то переставил секундой
	// раньше, и человек узнал бы об этом с бумаги.

	// ErrDesignBenchMoved — CAS по expected_plates не сошёлся: слот уехал под минтом. Aborted, и
	// details называют ИМЕННО ТОТ слот — «верстак изменился» без имени слота не действие, а
	// новость.
	ErrDesignBenchMoved = errors.New("design: bench_moved")
	// ErrDesignMixedNeedsConsent — состав смешивает провенансы (две и более разных генерации
	// среди четырёх сторон), и человек этого не подтвердил. FailedPrecondition: согласие даётся
	// галкой в диалоге, а не догадкой сервера.
	ErrDesignMixedNeedsConsent = errors.New("design: mixed_needs_consent")
	// ErrDesignUploadedFitUnconfirmed — среди плит есть загруженные руками, а они посадки не
	// заявляют вовсе (её заявляет ПРОГОН). Минт спрашивает, а не подставляет карточкину.
	ErrDesignUploadedFitUnconfirmed = errors.New("design: uploaded_fit_unconfirmed")
	// ErrDesignFitMismatch — плита нарисована под ОДНУ посадку, карточка теперь говорит другую.
	// Согласием это не снимается: посадка — свойство изделия, и одно из двух утверждений неверно.
	// details несут view, fit и card_fit.
	ErrDesignFitMismatch = errors.New("design: fit_mismatch")
	// ErrDesignSheetMinUnmet — обязательные стороны листа не заполнены. Проверяется В МИНТЕ, а не
	// на прогоне: пустой обязательный слот запирает и v2+, не только v1.
	ErrDesignSheetMinUnmet = errors.New("design: sheet_min_unmet")
	// ErrDesignUnrepinnedCallouts — П-Е, СОСТАВ ЗАМОРОЗКИ. Морозятся выноски, стоящие на медиа
	// ПЛИТ. Выноска, стоявшая на медиа плиты ПРОШЛОЙ версии, чья плита в этом составе заменена,
	// осталась висеть на картинке, которой на бумаге больше нет: её надо перепинить либо снять.
	// details несут НОМЕРА — «часть выносок потеряна» без номеров человеку нечем закрыть.
	//
	// ГРАНИЦА УЖЕ, ЧЕМ «ВСЕ ВЫНОСКИ ВНЕ ПЛИТ», и это решение. Мудбордная выноска и выноска на
	// легаси-эскизе НИКОГДА не были плитой листа, значит их никто не заменял; запирать ими минт
	// значило бы сделать минт недостижимым на каждой карточке с мудбордом (К-14).
	ErrDesignUnrepinnedCallouts = errors.New("design: unrepinned_callouts")
	// ErrDesignPlatesNotInDocument — П-А, ПОЯС. Документ, который минт замораживает, обязан
	// содержать плиты верстака как technical-медиа: механизм «деталь кроя ↔ выноска» читает
	// ровно tc.Media с category='technical' (store/techcard/materials.go), и плита вне этого
	// множества делает КАЖДУЮ деталь на листовой выноске detached, а тех-пак печатает пустой
	// эскиз. Вкладывает их хендлер; эта проверка стоит в транзакции, потому что только она видит
	// верстак и документ одновременно.
	ErrDesignPlatesNotInDocument = errors.New("design: plates_not_in_document")
)

// DesignMintRefusal — отказ минта ВМЕСТЕ с машинными подробностями для details.
//
// ЗАЧЕМ ТИП, А НЕ ТЕКСТ. Контракт обещает не просто «unrepinned_callouts», а
// «unrepinned_callouts {numbers}» и «bench_moved, naming which slot moved»: без номеров и без
// имени слота человеку нечем закрыть отказ, и клиент вместо действия показывает новость. Вынимать
// их обратно разбором строки значило бы завести второе, хрупкое написание того, что стор и так
// знает точно.
//
// Sentinel остаётся первым классом: Unwrap отдаёт его, поэтому таблица отказов хендлера,
// errors.Is и все существующие пробы работают, ничего не зная про этот тип.
type DesignMintRefusal struct {
	Err      error
	Metadata map[string]string
}

func (e *DesignMintRefusal) Error() string { return e.Err.Error() }
func (e *DesignMintRefusal) Unwrap() error { return e.Err }

// DesignSheetMinViews — стороны, без которых лист не выпускается. Проверяются В МИНТЕ (прототип,
// mintAnalysis: «the run above is free, the minimum is checked here»), а не на генерации: пустой
// обязательный слот обязан запирать и v2+, иначе «минимум» это пожелание к первой версии.
var DesignSheetMinViews = []string{DesignViewFront, DesignViewBack}

// DesignMintedVia — каким актом рождена версия. Словарь закрыт: `minted` в журнале обязан быть
// достижим ТОЛЬКО минтом, а «каким жестом» — это то, что аудит потом читает.
const (
	DesignMintedViaCallout = "callout"
	DesignMintedViaPrint   = "print"
	DesignMintedViaRelease = "release"
	DesignMintedViaShare   = "share"
)

// IsDesignMintedVia сообщает, законен ли акт минта.
func IsDesignMintedVia(v string) bool {
	switch v {
	case DesignMintedViaCallout, DesignMintedViaPrint, DesignMintedViaRelease, DesignMintedViaShare:
		return true
	}
	return false
}

// DesignPlateMediaKind — под каким видом плита верстака ложится в tech_card_media карточки (П-А).
//
// ГЕЙТ СЛОВАРЯ ОДИН НА ВЕСЬ РЕПОЗИТОРИЙ — TechCardMediaKindDictExtended. Пока 0346 не применена,
// chk_tech_card_media_kind не знает side_l/side_r, и плита боковой стороны обязана лечь как
// DETAIL: она всё равно остаётся ТЕХНИЧЕСКОЙ, а больше механизму «деталь ↔ выноска» ничего и не
// нужно. Второй флаг здесь был бы ложным расщеплением — разошлись бы ровно в тот день, когда
// первый флипнут, а второй забыли.
func DesignPlateMediaKind(viewKey string) TechCardMediaKind {
	switch viewKey {
	case DesignViewFront:
		return TechCardMediaFront
	case DesignViewBack:
		return TechCardMediaBack
	case DesignViewSideL:
		if TechCardMediaKindDictExtended {
			return TechCardMediaSideL
		}
		return TechCardMediaDetail
	case DesignViewSideR:
		if TechCardMediaKindDictExtended {
			return TechCardMediaSideR
		}
		return TechCardMediaDetail
	default:
		return TechCardMediaDetail
	}
}

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
	CreatedAt         time.Time           `db:"created_at"`
	StartedAt         sql.NullTime        `db:"started_at"`
	CompletedAt       sql.NullTime        `db:"completed_at"`

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

// DesignSheetVersion — строка design_sheet_version: замороженный выпуск листа.
type DesignSheetVersion struct {
	Id              int       `db:"id"`
	TechCardId      int       `db:"tech_card_id"`
	VersionNumber   int       `db:"version_number"`
	ClientRequestId string    `db:"client_request_id"`
	MixedConsent    bool      `db:"mixed_consent"`
	MintedVia       string    `db:"minted_via"`
	MintedBy        string    `db:"minted_by"`
	MintedAt        time.Time `db:"minted_at"`

	Plates   []DesignSheetPlate   `db:"-"`
	Callouts []DesignSheetCallout `db:"-"`
}

// DesignSheetPlate — строка design_sheet_version_plate. Строка, а не JSON, чтобы медиатека
// видела ссылку на media(id): версия печатается через год, и её байты стереть нельзя.
type DesignSheetPlate struct {
	Id          int            `db:"id"`
	VersionId   int            `db:"version_id"`
	Ordinal     int            `db:"ordinal"`
	ViewKey     string         `db:"view_key"`
	SlotId      sql.NullInt32  `db:"slot_id"`
	DetailName  sql.NullString `db:"detail_name"`
	MediaId     int            `db:"media_id"`
	ContentHash sql.NullString `db:"content_hash"`
	LayerRev    int            `db:"layer_rev"`
	SourceClass string         `db:"source_class"`
	RunId       sql.NullInt32  `db:"run_id"`
	FitStamp    sql.NullString `db:"fit_stamp"`
	MixedInput  bool           `db:"mixed_input"`

	Media *MediaFull `db:"-"`
}

// DesignSheetCallout — строка design_sheet_version_callout. Геометрия хранится как protojson
// common.TechCardAnnotation: стор её не разбирает, потому что примитив указания у системы один
// и его форма — контракт, а не схема полосы.
type DesignSheetCallout struct {
	Id         int            `db:"id"`
	VersionId  int            `db:"version_id"`
	Number     int            `db:"number"`
	MediaId    int            `db:"media_id"`
	Annotation RawJSON        `db:"annotation"`
	Text       sql.NullString `db:"text"`

	Media *MediaFull `db:"-"`
}

// DesignSheetIssue — строка design_sheet_issue: append-only журнал выпуска.
type DesignSheetIssue struct {
	Id              int            `db:"id"`
	VersionId       int            `db:"version_id"`
	VersionNumber   int            `db:"version_number"`
	Action          string         `db:"action"`
	Actor           string         `db:"actor"`
	ClientRequestId sql.NullString `db:"client_request_id"`
	CreatedAt       time.Time      `db:"created_at"`
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
	UpdatedBy       string        `db:"updated_by"`
	UpdatedAt       time.Time     `db:"updated_at"`
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
	Note    string
	Ordinal int
	Actor   string
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
	Bench          []DesignBenchSlot
	VersionNumbers []int
	LatestVersion  *DesignSheetVersion
	Journal        []DesignSheetIssue
	Budget         DesignBudget
	References     []DesignReference
	Layers         []DesignEditLayer

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

	Runs            []DesignRun
	NextCursor      int
	NextBatchCursor int
}

// DesignSheetVersionFull — версия целиком вместе со своим журналом.
type DesignSheetVersionFull struct {
	Version DesignSheetVersion
	Issues  []DesignSheetIssue
	// OrphanedPatternURLs — ПОБОЧНЫЙ РЕЗУЛЬТАТ ДОКУМЕНТНОЙ ЗАПИСИ ВНУТРИ МИНТА: объекты выкроек,
	// которые полная замена сделала глобально неупомянутыми. Хендлер удаляет их ПОСЛЕ коммита —
	// ровно то же, что делает сейв (deleteOrphanedPatternObjects). Не отдать их значило бы, что
	// минт течёт в S3 там, где сейв не течёт, и разошлись бы два пути одной записи.
	//
	// У ЧТЕНИЯ ПУСТО, и это не «не заполнено»: GetSheetVersion ничего не пишет, значит ничему
	// осиротеть не могло.
	OrphanedPatternURLs []string
	// Idempotent — версия с этим client_request_id уже существовала, и вернулась ОНА, а не
	// вторая. Хендлер отдаёт OK: потерянный ответ не рождает фантомную vN+1.
	Idempotent bool
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

// DesignExpectedPlate — одна строка оптимистичной блокировки минта ПО ВЕРСТАКУ.
type DesignExpectedPlate struct {
	Slot    DesignSlotRef
	SlotRev int
}

// DesignSheetMint — атомарный минт: запись документа и рождение версии в ОДНОЙ транзакции.
// TechCard едет отдельным аргументом (*TechCardInsert), потому что документ пишется ТЕМ ЖЕ
// кодом, что и UpdateTechCard.
type DesignSheetMint struct {
	TechCardId          int
	ClientRequestId     string
	TechCard            *TechCardInsert
	ExpectedLockVersion int
	ExpectedPlates      []DesignExpectedPlate
	MixedConsent        bool
	UploadedFitConfirm  bool
	MintedVia           string
	Actor               string
}

// DesignSheetIssueRecord — строка журнала printed/shared. Ничего не минтит.
type DesignSheetIssueRecord struct {
	TechCardId      int
	VersionNumber   int
	Action          string
	ClientRequestId string
	Actor           string
}
