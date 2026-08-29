package entity

import (
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"github.com/shopspring/decimal"
)

// Полоса DESIGN — студийная половина тех-карты: прогоны генерации, картинки, которые они
// произвели, верстак (какая плита принята какой стороной изделия) и замороженные версии листа,
// которые печатают.
//
// ЧТО ЭТИ ТИПЫ ЕСТЬ И ЧЕМ НЕ ЯВЛЯЮТСЯ. Это строки таблиц 0340–0347 плюс несколько запросных
// структур. Ни один из них не знает про protobuf: конверсия живёт в
// internal/apisrv/admin/design_band.go, а стор остаётся чистым от провода. JSON-колонки
// (`params`, `inputs`, `composite_views`, `strokes`, `annotation`) едут сквозь стор как
// json.RawMessage — стор их не разбирает, потому что их СОДЕРЖАНИЕ это контракт, а не схема.
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

// Виды кадра.
const (
	DesignPictureKindFlat   = "flat"
	DesignPictureKindRender = "render"
	DesignPictureKindThreed = "threed"
)

// Виды прогона.
const (
	DesignRunKindFlat      = "flat"
	DesignRunKindRender    = "render"
	DesignRunKindThreed    = "threed"
	DesignRunKindDraftIdea = "draft_idea"
)

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
)

// ---------------------------------------------------------------------------
// Строки таблиц
// ---------------------------------------------------------------------------

// DesignRun — строка design_run (0340). Она же строка истории на экране полосы.
type DesignRun struct {
	Id                     int                 `db:"id"`
	TechCardId             int                 `db:"tech_card_id"`
	Kind                   string              `db:"kind"`
	Status                 string              `db:"status"`
	ClientRequestId        string              `db:"client_request_id"`
	ProviderIdempotencyKey string              `db:"provider_idempotency_key"`
	ProfileName            string              `db:"profile_name"`
	ProfileVersion         int                 `db:"profile_version"`
	Ask                    sql.NullString      `db:"ask"`
	Params                 json.RawMessage     `db:"params"`
	Inputs                 json.RawMessage     `db:"inputs"`
	FitAtLaunch            sql.NullString      `db:"fit_at_launch"`
	Rrev                   int                 `db:"rrev"`
	RequestedOutputs       int                 `db:"requested_outputs"`
	AttemptCount           int                 `db:"attempt_count"`
	NextAttemptAt          sql.NullTime        `db:"next_attempt_at"`
	ClaimToken             sql.NullString      `db:"claim_token"`
	ClaimExpiresAt         sql.NullTime        `db:"claim_expires_at"`
	PriceEstimate          decimal.NullDecimal `db:"price_estimate"`
	PriceActual            decimal.NullDecimal `db:"price_actual"`
	Currency               string              `db:"currency"`
	Author                 string              `db:"author"`
	CancelRequestedAt      sql.NullTime        `db:"cancel_requested_at"`
	ArchivedAt             sql.NullTime        `db:"archived_at"`
	ArchivedBy             sql.NullString      `db:"archived_by"`
	ErrorCode              sql.NullString      `db:"error_code"`
	LastError              sql.NullString      `db:"last_error"`
	OutputText             sql.NullString      `db:"output_text"`
	CreatedAt              time.Time           `db:"created_at"`
	StartedAt              sql.NullTime        `db:"started_at"`
	CompletedAt            sql.NullTime        `db:"completed_at"`

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
	Id             int             `db:"id"`
	TechCardId     int             `db:"tech_card_id"`
	MediaId        int             `db:"media_id"`
	RunId          sql.NullInt32   `db:"run_id"`
	BatchId        sql.NullInt32   `db:"batch_id"`
	Ordinal        int             `db:"ordinal"`
	Kind           string          `db:"kind"`
	GhostView      sql.NullString  `db:"ghost_view"`
	CompositeViews json.RawMessage `db:"composite_views"`
	DerivedFrom    sql.NullInt32   `db:"derived_from"`
	SourceClass    string          `db:"source_class"`
	MixedInput     bool            `db:"mixed_input"`
	LayerRev       int             `db:"layer_rev"`
	HiddenAt       sql.NullTime    `db:"hidden_at"`
	HiddenBy       sql.NullString  `db:"hidden_by"`
	CreatedAt      time.Time       `db:"created_at"`

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
	Id           int            `db:"id"`
	TechCardId   int            `db:"tech_card_id"`
	ViewKey      string         `db:"view_key"`
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
	Id         int             `db:"id"`
	VersionId  int             `db:"version_id"`
	Number     int             `db:"number"`
	MediaId    int             `db:"media_id"`
	Annotation json.RawMessage `db:"annotation"`
	Text       sql.NullString  `db:"text"`

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
	Id          int             `db:"id"`
	TechCardId  int             `db:"tech_card_id"`
	BaseMediaId sql.NullInt32   `db:"base_media_id"`
	Rev         int             `db:"rev"`
	Strokes     json.RawMessage `db:"strokes"`
	UpdatedBy   string          `db:"updated_by"`
	UpdatedAt   time.Time       `db:"updated_at"`
}

// DesignReference — строка design_reference: какой стороне изделия отвечает референс на входе
// генерации. Роль живёт в полосе, а не в документе: `kind` у медиа карточки уже занят тем, ЧЕМ
// картинка является, и это настоящая вторая ось.
type DesignReference struct {
	Id         int       `db:"id"`
	TechCardId int       `db:"tech_card_id"`
	MediaId    int       `db:"media_id"`
	Role       string    `db:"role"`
	Ordinal    int       `db:"ordinal"`
	SetBy      string    `db:"set_by"`
	SetAt      time.Time `db:"set_at"`
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
	Ordinal    int
	Actor      string
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

	Runs            []DesignRun
	NextCursor      int
	NextBatchCursor int
}

// DesignSheetVersionFull — версия целиком вместе со своим журналом.
type DesignSheetVersionFull struct {
	Version DesignSheetVersion
	Issues  []DesignSheetIssue
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
}

// DesignRunStarted — строка прогона плюс бюджет ПОСЛЕ резерва, чтобы полоса двигалась вместе
// с кликом, а не на следующем опросе.
type DesignRunStarted struct {
	Run    DesignRun
	Budget DesignBudget
	// Idempotent — прогон с этим client_request_id уже существовал.
	Idempotent bool
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
