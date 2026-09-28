package fal

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/shopspring/decimal"
)

// ─────────────────────────── ВЫРЕЗ ФОНА: ВТОРОЙ МАРШРУТ ЭТОГО ТРАНСПОРТА ───────────────────────────
//
// ЗАЧЕМ ОН ВООБЩЕ ЕСТЬ, ЕСЛИ «УБРАТЬ ФОН» УМЕЕТ ЛЮБАЯ КАРТИНОЧНАЯ МОДЕЛЬ. Не умеет. Обычная
// генеративная модель на просьбу «убери фон» рисует БЕЛЫЙ ФОН или, что хуже, НАРИСОВАННУЮ шахматку —
// прозрачности в ответе нет вовсе, а картинка выглядит убедительно (RESEARCH §Поправки п.3). Альфа
// берётся только у матирующей модели, и здесь это BiRefNet.
//
// ⚠ И ИМЕННО BiRefNet, А НЕ `rembg`-ПО-УМОЛЧАНИЮ. Самый очевидный путь — библиотека `rembg` — тянет
// весами `bria-rmbg`, а это CC BY-NC: коммерческое использование только по отдельному договору с
// BRIA. Лицензия — не деталь развёртывания, она свойство ВЫДАЧИ, и цена ошибки здесь не в деньгах за
// вызов. BiRefNet под MIT (см. DefaultModelCutout).
//
// ПОЧЕМУ ЭТО ОДИН КЛИЕНТ С 3D, А НЕ ВТОРОЙ ПАКЕТ. Тот же провайдер, тот же ключ, та же очередь, те
// же коды отказов и та же арифметика денег. Второй пакет означал бы вторую копию классификации
// ошибок — а она здесь единственное, что отличает «модель снята» от «сервис занят», и разъезд этих
// двух копий уже однажды рубил обе AI-функции разом. Отличается ровно ТЕЛО ЗАПРОСА, ОТВЕТ и СЛАГ, и
// они живут в этом файле.
//
// ПОЧЕМУ ЗДЕСЬ ПАРА Submit/Collect, КАК У 3D, ХОТЯ ВЫРЕЗ СЧИТАЕТСЯ СЕКУНДЫ. Раздельные глаголы
// существуют не ради ДЛИТЕЛЬНОСТИ, а ради ТОЧКИ ОПЛАТЫ: сабмит — это платёж, сбор бесплатен, и
// воркер, умерший между ними, обязан возобновить, а не купить второй раз. Секунды сборки уменьшают
// ВЕРОЯТНОСТЬ такого разрыва и ничего не меняют в его ЦЕНЕ — а сам fal прямо предупреждает, что
// второй submit это второй charge. Прежняя синхронная форма честно называла этот долг вслух; долг
// закрыт разделением, которое стоит одного поля в строке попытки.
//
// Всё, что происходит ПОСЛЕ сабмита, несёт на себе `chargedWith`: деньги должны доехать до книги
// даже там, где картинка не доехала.

const (
	// DefaultModelCutout is the matting model the band cuts backgrounds with.
	//
	// ⚠ ЭТО ЛИЦЕНЗИОННЫЙ ВЫБОР, А НЕ ВКУСОВОЙ. BiRefNet (ZhengPeng7) — MIT, единственный вариант
	// топового качества, свободный для коммерции целиком. Соседние по качеству веса — BRIA RMBG-2.0
	// (та же архитектура плюс проприетарные данные) — живут под CC BY-NC, и попасть на них проще
	// всего НЕ выбрав ничего: `rembg` грузит их по умолчанию. Слаг здесь именно для того, чтобы
	// «ничего не выбрали» вело к MIT-модели, а не к чужой лицензии.
	//
	// ⚠ И ОН В КОДЕ, А НЕ В ПЕРЕМЕННОЙ ОКРУЖЕНИЯ, ПО ТОЙ ЖЕ ПРИЧИНЕ, ЧТО DefaultModel3D: правка
	// спеки DigitalOcean САМА ПО СЕБЕ выкатывает текущий master, поэтому смена модели через
	// окружение везёт с собой посторонний релиз. FAL_MODEL_CUTOUT существует как аварийный руль на
	// случай снятого слага, а не как штатная ручка.
	DefaultModelCutout = "fal-ai/birefnet/v2"

	// cutoutOutputFormat is the only format worth asking for: PNG is the format of this route's
	// entire purpose, because it is the one the provider will put an alpha channel into. Asking for
	// the provider's default would mean accepting whatever it feels like returning — and a JPEG
	// answer to a background removal is a picture with the background painted white.
	cutoutOutputFormat = "png"

	// maxCutoutBytes caps the returned picture. It REFUSES at the limit rather than truncating, for
	// the reason readCapped gives: a PNG cut at the boundary decodes to nothing and reads as a
	// provider defect for as long as it takes somebody to compare byte counts.
	maxCutoutBytes = 25 << 20

	// cutoutFirstPoll is how soon after the submit the first status lookup happens.
	//
	// ⚠ ОНО СВОЁ, А НЕ FAL_POLL_INTERVAL, И ЭТО НЕ МИКРО-ОПТИМИЗАЦИЯ. Интервал 3D-маршрута — пять
	// секунд, и он правилен для сборки, которая идёт минуты. Вырез отвечает за секунду-две, поэтому
	// тот же интервал превратил бы двухсекундную операцию в пятисекундную: человек ждёт у экрана,
	// а воркер держит лизу втрое дольше нужного. Дальше интервал удваивается до настроенного
	// FAL_POLL_INTERVAL — быстрый ответ ловится сразу, медленный не долбит очередь.
	cutoutFirstPoll = 400 * time.Millisecond

	// defaultCutoutUSD is what ONE background removal is assumed to cost when FAL_UNIT_USD_CUTOUT is
	// unset. Same argument as defaultRequestUSD, and the same direction of error: «this run was
	// free» is a worse lie than «this run cost about two cents».
	//
	// ⚠ И ЭТО ЦЕНА ЗАПРОСА, А НЕ ЕДИНИЦЫ — та же ловушка, что уже стоила сто долларов на 3D. Не зная
	// ТАРИФА, честно назвать можно только порядок цены операции: что провайдер меряет своими
	// единицами — секунды, мегапиксели, вызовы — знает только его прайс. Число взято с верхнего края
	// известных: ≈$0.018 за инференс матирующей модели на fal и $0.02 у Photoroom (RESEARCH-web).
	// Верхний край выбран намеренно: две ошибки не симметричны, и занижение реальных трат — ровно
	// тот провал, ради предотвращения которого эта бухгалтерия существует.
	defaultCutoutUSD = 0.02
)

// ErrNoCutout is the most expensive answer this route can get: the request COMPLETED — so the
// picture was made and the units are spent — and the answer carries no url to fetch it with.
//
// ⚠ ОНО ОБОРАЧИВАЕТ ErrNoModel НАМЕРЕННО, И ЭТО ПРО ДЕНЬГИ, А НЕ ПРО ВЕЖЛИВОСТЬ. Классификатор
// прогонов знает про этот маршрут ровно ничего, а разбирает ошибки по СЕНТИНЕЛАМ: ErrNoModel уже
// значит «оплачено, показать нечего» — не повторять, закрыть попытку `unknown`. Свежий сентинел без
// пары в классификаторе провалился бы в его retryable-умолчание и КУПИЛ БЫ ТОТ ЖЕ ПУСТОЙ ОТВЕТ ПЯТЬ
// РАЗ. Слово «picture» в тексте — для человека; отношение «это тот же класс отказа» — для машины.
var ErrNoCutout = fmt.Errorf("%w: the finished request carries no picture", ErrNoModel)

// CutoutResult describes ONE delivered cut-out. Like Result it carries NO url: fal's artifact links
// expire, so the only honest thing to hand back is what was written to the sink and what it cost.
type CutoutResult struct {
	// RequestID is the provider's id for the job — durable, free to look up again, and the one
	// thing worth storing on the attempt row.
	RequestID string
	// Model is the slug that actually answered.
	Model string
	// ContentType is what the PROVIDER CALLED the file it handed back.
	//
	// ⚠ ЭТО ЕГО СЛОВО, А НЕ ФАКТ, И ОНО НИЧЕГО НЕ РЕШАЕТ. Смысл маршрута — альфа, а альфа есть или
	// нет в БАЙТАХ; ярлык `image/png` над картинкой без прозрачности — ровно тот случай, ради
	// которого выше по течению стоит проверка результата. Поле нужно, чтобы жалоба могла назвать,
	// чем провайдер считал присланное, и ни для чего больше.
	ContentType string
	// Bytes and SHA256 describe what was actually written to dst.
	Bytes  int64
	SHA256 string
	// BillableUnits is what fal's own x-fal-billable-units header reported for this request.
	BillableUnits float64
	// UnitsAssumed says the header was ABSENT and BillableUnits is this package's assumption of one
	// unit per request rather than the provider's number — see billableUnits.
	UnitsAssumed bool
}

// ModelCutout returns the effective background-removal slug. Nil-safe.
//
// ⚠ УМОЛЧАНИЕ ПОДСТАВЛЯЕТСЯ ЗДЕСЬ, А НЕ В New, И ЭТО ОСОЗНАННО. У 3D-слага умолчание живёт в New,
// потому что там оно нормализует конфиг один раз. Здесь оно живёт в читателе, чтобы НУЛЕВОЙ
// fal.Config — и nil-клиент — отвечали на вопрос «какой моделью ты режешь фон» одинаково честно, а
// не пустой строкой, которую вызывающий примет за «маршрута нет».
func (c *Client) ModelCutout() string {
	if c == nil {
		return DefaultModelCutout
	}
	if m := strings.Trim(strings.TrimSpace(c.cfg.ModelCutout), "/"); m != "" {
		return m
	}
	return DefaultModelCutout
}

// EstimatedCutoutUSD is what ONE background removal off this route is expected to cost before the
// provider has said anything. It is the number CostCutoutUSD falls back on when no tariff is
// configured, and it is EXPORTED FOR THE SAME REASON EstimatedRequestUSD is: the door reserves
// against it, and an estimate the door keeps its own copy of is two numbers that will disagree.
func EstimatedCutoutUSD() decimal.Decimal { return decimal.NewFromFloat(defaultCutoutUSD) }

// CostCutoutUSD converts billable units into money at FAL_UNIT_USD_CUTOUT.
//
// ⚠ У ВЫРЕЗА СВОЙ ТАРИФ, А НЕ FAL_UNIT_USD, И ЭТО НЕ ЛИШНЯЯ РУЧКА. Одна переменная на два маршрута
// означала бы, что цена единицы матирующей модели и цена единицы сборки 3D — одно число; они
// отличаются на два порядка, и общий тариф превратил бы двухцентовую операцию в доллар с лишним или
// доллар — в два цента, в зависимости от того, под какой маршрут его настроили.
func (c *Client) CostCutoutUSD(units float64) decimal.Decimal {
	if c == nil || units <= 0 {
		return decimal.Zero
	}
	// ТАРИФ НЕ ЗАДАН — УМНОЖАТЬ НЕ НА ЧТО, ровно как в CostUSDFor. Число единиц провайдер называет
	// честно, но что он ими меряет, знает только его прайс; умножение на выдуманный тариф даёт не
	// оценку, а уверенное враньё, тем более убедительное, чем больше единиц вернул провайдер.
	if c.cfg.UnitUSDCutout <= 0 {
		return EstimatedCutoutUSD()
	}
	return decimal.NewFromFloat(c.cfg.UnitUSDCutout).Mul(decimal.NewFromFloat(units))
}

// SubmitCutout puts ONE picture into the matting queue and returns the request id. IT NEVER
// RETRIES, because a second submit is a second charge.
//
// ⚠ ЭТО ГЛАГОЛ, НА КОТОРОМ УХОДЯТ ДЕНЬГИ, И ОН ОТДЕЛЁН ОТ СБОРА ИМЕННО ПОЭТОМУ. Раньше здесь стоял
// один синхронный глагол, а довод был «вырез считается секунды, глагол один»; секунды — правда, но
// они ничего не говорят про то, что происходит с ОПЛАЧЕННЫМ заданием, если процесс умер между
// сабмитом и ответом. Умер — id никуда не записан, и следующий проход шлёт ВТОРОЙ ПЛАТНЫЙ САБМИТ:
// провайдер прямо предупреждает, что второй submit — второй charge. Разделение стоит одного поля в
// строке попытки и закрывает двойное списание насовсем: id закрывает попытку `accepted`, а сбор по
// нему бесплатен.
func (c *Client) SubmitCutout(ctx context.Context, imageURL string) (string, error) {
	return c.SubmitCutoutAt(ctx, c.ModelCutout(), imageURL)
}

// SubmitCutoutAt is SubmitCutout into a NAMED matting queue — the image.cutout route row's model (B-24),
// "" = ModelCutout(). The body is the same: every slug this route has run on reads image_url.
func (c *Client) SubmitCutoutAt(ctx context.Context, model, imageURL string) (string, error) {
	if model = strings.Trim(strings.TrimSpace(model), "/"); model == "" {
		model = c.ModelCutout()
	}
	if !c.Enabled() {
		return "", ErrNotConfigured
	}
	imageURL = strings.TrimSpace(imageURL)
	if err := validateImageRef(imageURL); err != nil {
		// Refused HERE, before the request leaves and therefore before anything is billed. A
		// reference the provider cannot fetch is not weather and does not improve on a retry.
		return "", fmt.Errorf("cut-out source: %w", err)
	}

	var sub submitResponse
	if err := c.callJSON(ctx, http.MethodPost, "/"+model, cutoutSubmitBody{
		ImageURL:     imageURL,
		OutputFormat: cutoutOutputFormat,
		// ⚠ ЭТО НЕ КОСМЕТИКА. Без доводки переднего плана альфа по краю несёт ЦВЕТ ФОНА, и вырез,
		// положенный на другой фон, обводится каймой прежнего — ровно тем, что его просили убрать.
		// Стоит ли это денег, решает провайдер; выключенный по умолчанию флаг дал бы результат,
		// который выглядит правильно ровно до момента, когда его на что-нибудь положат.
		RefineForeground: true,
	}, &sub, nil); err != nil {
		return "", err
	}
	id := strings.TrimSpace(sub.RequestID)
	if id == "" {
		// ⚠ ОПЛАЧЕНО И ПОТЕРЯНО. Сабмит принят, значит единицы списаны, а вернуть по нему нечего:
		// без id ни забрать результат, ни возобновить. Отдельное слово нужно, чтобы этот исход не
		// читался как обычный отказ транспорта.
		return "", submitLost(sub.httpStatus)
	}
	return id, nil
}

// CollectCutout waits for a submitted request and writes the resulting file into dst. IT IS FREE:
// a lookup of a request that was already paid for.
//
// THE BYTES ARE TAKEN IMMEDIATELY AND THE LINK IS NEVER RETURNED, for the reason the 3D route gives:
// fal's artifact urls expire, so a stored link is a picture that quietly stops existing.
//
// ⚠ ЧТО ЭТА ФУНКЦИЯ НЕ ПРОВЕРЯЕТ: ЕСТЬ ЛИ В ОТВЕТЕ АЛЬФА. Транспорт отвечает за «доставлено и
// оплачено»; «доставленное — действительно вырез» — вопрос о СОДЕРЖИМОМ, и он решается там, где уже
// есть декодер картинок и куда её всё равно кладут. Здесь эта проверка означала бы, что транспорт
// разбирает растр ради чужого решения — и что «вырез без альфы» нельзя ни сохранить, ни увидеть.
//
// ⚠ СЛАГ БЕРЁТСЯ ИЗ СЕГОДНЯШНЕЙ КОНФИГУРАЦИИ, И У ЭТОГО ЕСТЬ ОКНО. Задание, отправленное до
// переезда модели, собирается по НОВОМУ слагу и не находится. У 3D ровно для этого есть
// locateRequest — поиск оплаченного задания в чужих неймспейсах; там он оправдан тем, что сборка
// идёт МИНУТЫ и переживает деплой. Вырез отвечает за секунду-две, поэтому окно здесь — это
// «процесс умер сразу после сабмита И в ту же минуту сменили слаг»; копия самого деликатного кода
// пакета ради него стоила бы дороже, чем он.
func (c *Client) CollectCutout(ctx context.Context, requestID string, dst io.Writer) (*CutoutResult, error) {
	if !c.Enabled() {
		return nil, ErrNotConfigured
	}
	if dst == nil {
		// ⚠ РЕФУЗ ДО ЕДИНОГО ЗАПРОСА, И ЭТО ПРО ДЕНЬГИ, А НЕ ПРО ЧИСТОПЛОТНОСТЬ. Картинка уже
		// оплачена сабмитом; собрать её и выбросить — это потратить её второй раз, теперь впустую.
		return nil, errors.New("fal: CollectCutout has nowhere to put the picture")
	}
	requestID = strings.TrimSpace(requestID)
	if requestID == "" {
		return nil, fmt.Errorf("%w: collect was given no request id", ErrBadRequest)
	}
	return c.awaitFile(ctx, c.ModelCutout(), requestID, pickCutout, dst, maxCutoutBytes)
}

// CollectCutoutAt is CollectCutout polled at the slug the request was SUBMITTED to, not today's
// FAL_MODEL_CUTOUT (G-03, Codex 2: the queue namespace is the slug's, and a slug moved between a paid
// submit and its resume would poll another model's queue for it).
//
// An empty model is a LEGACY bare id (stored before the locator): today's slug first, then — on a 404
// past the grace — the route's known namespaces (CutoutLegacyModels), G-03 r2, Codex 4.
func (c *Client) CollectCutoutAt(ctx context.Context, model, requestID string, dst io.Writer) (*CutoutResult, error) {
	model = strings.Trim(strings.TrimSpace(model), "/")
	if model == "" {
		res, err := c.CollectCutout(ctx, requestID, dst)
		legacy := c.CutoutLegacyModels()
		if !errors.Is(err, ErrRequestNotFound) || len(legacy) < 2 {
			return res, err
		}
		switch alt, out, cause := c.searchNamespaces(ctx, legacy[0], legacy[1:], strings.TrimSpace(requestID)); out {
		case locateFound:
			model = alt
		case locateUnknown:
			return nil, notFoundYetUnsure(requestID, legacy[0], cause)
		default:
			return res, err
		}
	}
	if !c.Enabled() {
		return nil, ErrNotConfigured
	}
	if dst == nil {
		return nil, errors.New("fal: CollectCutout has nowhere to put the picture")
	}
	requestID = strings.TrimSpace(requestID)
	if requestID == "" {
		return nil, fmt.Errorf("%w: collect was given no request id", ErrBadRequest)
	}
	return c.awaitFile(ctx, model, requestID, pickCutout, dst, maxCutoutBytes)
}

// CutoutLegacyModels — every slug a BARE cut-out request id may have been queued under: today's
// FAL_MODEL_CUTOUT first, then the code default. See RouteLegacyModels.
func (c *Client) CutoutLegacyModels() []string {
	return []string{c.ModelCutout(), DefaultModelCutout}
}

// pickCutout is PickImage under the cut-out's own sentinel: a completed matting request with no url
// is ErrNoCutout (which wraps ErrNoModel through ErrNoFile's parent — see ErrNoCutout), so the
// history keeps the word it has always carried.
//
// THE WAIT ITSELF IS awaitFile (generic.go) SINCE PLAYGROUND phase 3: the cut-out was the first
// fal JSON route and carried its own copy of the queue code; the outpaint and fill routes would have
// been the second and third copies. Same requests, same order, same money envelope — the cut-out
// probes below are unchanged and still green.
func pickCutout(body json.RawMessage) (string, string, error) {
	u, ct, err := PickImage(body)
	if errors.Is(err, ErrNoFile) {
		return "", "", ErrNoCutout
	}
	return u, ct, err
}

// --- wire types ---

// cutoutSubmitBody is the matting payload. Field names are the provider's.
type cutoutSubmitBody struct {
	ImageURL         string `json:"image_url"`
	OutputFormat     string `json:"output_format"`
	RefineForeground bool   `json:"refine_foreground"`
}
