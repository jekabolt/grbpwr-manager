package admin

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"slices"
	"strconv"
	"strings"

	"github.com/jekabolt/grbpwr-manager/internal/dto"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/jekabolt/grbpwr-manager/internal/openrouter"
	"github.com/jekabolt/grbpwr-manager/internal/recraft"
	"github.com/jekabolt/grbpwr-manager/internal/store/design"
	pb_admin "github.com/jekabolt/grbpwr-manager/proto/gen/admin"
	pb_common "github.com/jekabolt/grbpwr-manager/proto/gen/common"
	"github.com/shopspring/decimal"
	pb_decimal "google.golang.org/genproto/googleapis/type/decimal"
	"google.golang.org/genproto/googleapis/rpc/errdetails"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
)

// ГЕНЕРАТИВНАЯ ПОЛОВИНА ПОЛОСЫ — ХЕНДЛЕРЫ.
//
// ЧТО ЭТОТ ФАЙЛ ДЕЛАЕТ И ЧЕГО НЕ ДЕЛАЕТ. Он разбирает запрос, стоит на воротах, СОБИРАЕТ СНИМОК
// ВХОДОВ и переводит отказы стора в коды gRPC. Он не резервирует деньги (это одна SERIALIZABLE
// транзакция внутри Design().StartRun — вторая обёртка вокруг неё подвесила бы резерв при падении
// между ними), не ходит к поставщику картинок (это воркер) и не пишет ни одной строки сам.
//
// ЧЕТЫРЕ ВОРОТ, И КАЖДЫЕ СТОЯТ ИМЕННО ЗДЕСЬ, А НЕ НА ЭКРАНЕ:
//
//  1. ФЛАГ DESIGN_GENERATION_ENABLED. Выключен — StartDesignRun отказывает и НЕ заводит строку.
//     Без воркера строку некому забрать: она навсегда останется pending, деньги останутся
//     зарезервированными до полуночи, а человек будет смотреть на вечное «идёт генерация».
//  2. W-13 — 3D ТОЛЬКО ПОСЛЕ РЕНДЕРА. Клиентское приглушение это подсказка, а не защита: вкладку
//     открывают ссылкой, а платит владелец.
//  3. W-15 — МУДБОРД НЕ УХОДИТ В ГЕНЕРАЦИЮ. Владелец процитировал строку прототипа дословно:
//     «the mood, not the prompt: nothing here is sent to generation». Экранного обещания
//     недостаточно; гарантия — designAssembleInputs, см. её шапку.
//  4. РЕРАН СОБИРАЕТ СЕРВЕР. Входы берутся из строки прогона-родителя, а не из того, что прислал
//     клиент: клиентский снимок позволил бы истории утверждать входы, которых не было.

// ─────────────────────────── отказы этого яруса ───────────────────────────

// designRefusal — отказ, РОЖДЁННЫЙ ХЕНДЛЕРОМ, а не переведённый со стора.
//
// ЗАЧЕМ ОТДЕЛЬНАЯ ФУНКЦИЯ РЯДОМ С designError. designError — таблица переводов sentinel-ошибок
// стора; у ворот, стоящих ДО стора (флаг, W-13, чужой родитель рерана), никакой sentinel-ошибки
// нет и заводить её было бы враньём: стор о них не знает и знать не должен. Но домен и форма
// детали ОБЯЗАНЫ совпадать, иначе клиент разбирает две разные оболочки одной и той же новости.
//
// Деталь — бонус, а не полезная нагрузка: если её не удалось приложить, ОТКАЗ ВСЁ РАВНО УХОДИТ.
func designRefusal(code codes.Code, reason, msg string, metadata map[string]string) error {
	st := status.New(code, msg)
	md := map[string]string{"reason": reason}
	for k, v := range metadata {
		md[k] = v
	}
	withDetails, err := st.WithDetails(&errdetails.ErrorInfo{
		Reason: reason, Domain: designErrorDomain, Metadata: md,
	})
	if err != nil {
		return st.Err()
	}
	return withDetails.Err()
}

// ─────────────────────────── флаг ───────────────────────────

// designGenerationDisabledMsg — ОТКАЗ ВЫКЛЮЧЕННОГО ФЛАГА, ПРОЗОЙ.
//
// Он называет и переменную, и последствие, потому что и то и другое нужно разным читателям:
// человеку — почему кнопка не сработала, дежурному — что именно включить. Отказ приходит ДО
// стора, поэтому строки прогона не появляется вовсе и день не теряет ни цента резерва.
const designGenerationDisabledMsg = "design generation is switched off in this deployment " +
	"(DESIGN_GENERATION_ENABLED is unset): there is no worker to pick a run up, so a run opened " +
	"now would reserve money and sit in `pending` forever — it is refused instead"

// designReasonGenerationDisabled — машинная причина того же отказа, чтобы клиент отличал
// «выключено у нас» от «модель отвергла запрос», не разбирая английскую прозу.
const designReasonGenerationDisabled = "generation_disabled"

// SetDesignGenerationEnabled переключает генеративную половину полосы.
//
// ⚠ ПО УМОЛЧАНИЮ ВЫКЛЮЧЕНО, И ЭТО НАМЕРЕННО FAIL-CLOSED. Сервер, собранный без этого вызова
// (сегодня — любой, потому что app/app.go принадлежит задаче воркера B8), отказывает на
// StartDesignRun и DraftDesignIdea внятными словами вместо того, чтобы завести оплаченный прогон,
// который никто не исполнит.
//
// ЗАЧЕМ СЕТТЕР, А НЕ АРГУМЕНТ admin.New. Флаг живёт в конфигурации воркера, а config/cfg.go и
// app/app.go принадлежат ЕГО задаче; расширение сигнатуры New сломало бы сборку у всех, кто её
// зовёт, до того как та задача сядет.
//
// ⚠ ЗНАЧЕНИЕ ОБЯЗАНО БЫТЬ ТЕМ ЖЕ САМЫМ, ЧТО ГЕЙТИТ РЕГИСТРАЦИЮ ВОРКЕРА, и «тем же самым» здесь
// значит из ОДНОГО выражения, а не из двух чтений одной переменной. В app/app.go это одна строка
// рядом с регистрацией:
//
//	dg := designgen.ConfigFromEnv()       // ← уже есть у воркера
//	adminS.SetDesignGenerationEnabled(dg.Enabled)
//
// Разойдясь, эти два значения дают ровно два состояния, каждое из которых хуже выключенного:
// хендлер открыт без воркера — оплаченные прогоны висят в pending навсегда; воркер поднят при
// закрытом хендлере — работающий воркер над пустой очередью.
//
// Звать ДО начала обслуживания: поле читается запросами и после старта не меняется.
func (s *Server) SetDesignGenerationEnabled(v bool) { s.designGenerationEnabled = v }

// designGenerationGate — единственная проверка флага. Одна на два платных глагола: две копии
// разошлись бы молча, и один из глаголов однажды остался бы включённым на проде.
func (s *Server) designGenerationGate() error {
	if s.designGenerationEnabled {
		return nil
	}
	return designRefusal(codes.FailedPrecondition, designReasonGenerationDisabled,
		designGenerationDisabledMsg, nil)
}

// ─────────────────── ВТОРЫЕ ВОРОТА: РОД, КОТОРЫЙ ВСЁ РАВНО НЕ ДОЕДЕТ ───────────────────

// designKindRefusal — то единственное, что дверь хочет знать об ошибке ворот: МАШИННУЮ ПРИЧИНУ.
//
// Объявлено интерфейсом, а не импортом типа воркера, намеренно: этот ярус не должен зависеть от
// пакета генерации ради одного слова. Ошибка, которая метод не реализует, — тоже ошибка: причина
// тогда общая, а отказ всё равно происходит.
type designKindRefusal interface{ RefusalReason() string }

// designReasonKindUnavailable — общая машинная причина, когда ворота отказали, но своей причины не
// назвали. Совпадает со словарём `design_run.error_code` (designgen.CodeKindNotAvailable): клиент
// разбирает одни и те же слова, откуда бы отказ ни пришёл.
const designReasonKindUnavailable = "kind_not_available"

// SetDesignKindGate вешает на дверь ПРОВЕРКУ ВОЗМОЖНОСТЕЙ прогона — ровно тот предполётный
// вопрос, который воркер задаёт первым: «этот прогон вообще может доехать?»
//
// ⚠ ЗАЧЕМ ЭТО ОТДЕЛЬНЫЕ ВОРОТА, А НЕ ЕЩЁ ОДНА СТРОЧКА В ФЛАГЕ. StartDesignRun РЕЗЕРВИРУЕТ ДЕНЬГИ:
// строка прогона заводится с price_estimate, и резерв держится до полуночи или до терминального
// перехода. Род, чей выход некуда положить (или чей маршрут не подключён), был бы принят, оплачен
// резервом и через тик воркера гарантированно провален — по разу за каждый клик. Отказ обязан
// стоять ДО резерва.
//
// ⚠ ЗДЕСЬ НЕТ И НЕ ДОЛЖНО БЫТЬ СПИСКА РОДОВ. Функция приходит из воркера и считает ответ из
// возможностей маршрута и приёмника (Produces() × Accepts()). Список имён родов, зашитый в этом
// файле, разошёлся бы с реальностью молча — и в ту, и в другую сторону: он продолжал бы отказывать
// после того, как хранилище научилось типу, и продолжал бы пропускать после того, как маршрут стал
// возвращать новый. Считаемый ответ гаснет САМ.
//
// Звать ДО начала обслуживания, рядом с SetDesignGenerationEnabled: поле читается запросами.
func (s *Server) SetDesignKindGate(gate func(kind string) error) { s.designKindGate = gate }

// designKindGateCheck — ворота рода. Молчат, когда ворот не повесили (см. поле designKindGate:
// сервер без них — это сервер с выключенным флагом денег, который отказал строчкой выше).
func (s *Server) designKindGateCheck(kind string) error {
	if s.designKindGate == nil {
		return nil
	}
	err := s.designKindGate(kind)
	if err == nil {
		return nil
	}
	reason := designReasonKindUnavailable
	var named designKindRefusal
	if errors.As(err, &named) {
		reason = named.RefusalReason()
	}
	// FailedPrecondition, а не InvalidArgument: запрос правильный, не готова СИСТЕМА — ключа нет,
	// маршрут не подключён, хранилище не умеет такой файл. Тот же код, что у флага и у W-13,
	// потому что это та же новость: «сейчас — нельзя», а не «вы прислали ерунду».
	return designRefusal(codes.FailedPrecondition, reason,
		fmt.Sprintf("a %s run cannot be started: %s. Nothing was reserved and nothing was charged",
			kind, err.Error()),
		map[string]string{"kind": kind})
}

// ─────────────────────────── цена до клика ───────────────────────────

// designPriceEstimate — ОЦЕНКА, А НЕ КОТИРОВКА, и она названа оценкой вслух (34-PLAN §5.4;
// серверной котировки на проводе нет вовсе).
//
// Она делает ровно одно: наполняет `design_run.price_estimate`, то есть РЕЗЕРВ дня. Резерв — это
// потолок, а не счёт: фактическую цену пишет попытка (`FinishAttempt`), а резерв снимается целиком
// на терминальном переходе. Поэтому оценка обязана быть скорее завышенной, чем заниженной:
// заниженная пропускает за дневной потолок больше прогонов, чем владелец согласился оплатить.
//
// ⚠ КАЖДОЕ ЧИСЛО ЗДЕСЬ — ВЕРХНЯЯ ГРАНИЦА РОДА, А НЕ ЕГО ОЖИДАНИЕ, И ЭТО НЕСУЩЕЕ РЕШЕНИЕ.
//
// ЧТО БЫЛО. Вектор резервировал $0.04, а собственная константа провайдерского пакета
// (recraft.Tier.EstimatedUSD) говорит $0.08 за стандартный тир и $0.30 за pro — то есть дневной
// потолок пропускал ВДВОЕ больше трат, чем с владельцем согласовано, и делал это молча. Картинка
// же стоила плоскую константу БЕЗ члена качества вовсе, хотя дил DESIGN_IMAGE_QUALITY — «the
// single largest multiplier on what a press costs» (designgen.Config.ImageQuality), и на `high`
// кадр стоит вчетверо против той константы. Обе поломки — один класс: рядом с местом списания
// лежала СВОЯ копия цены, а две копии расходятся в тот день, когда правят одну.
//
// ЧТО СТАЛО. Там, где у списания есть СОБСТВЕННЫЙ источник числа, оценка ВЫВОДИТСЯ из него
// (вектор — из recraft.Tiers()), а не повторяет его. Там, где источника нет — картинку тарифицирует
// сам провайдер, и локальной таблицы цен у неё не существует, — оценка берёт САМОЕ ДОРОГОЕ
// положение дила, и связь с дилом становится не нужна: покрыто любое.
//
// ⚠ ПОЧЕМУ ПОТОЛОК, А НЕ ЧТЕНИЕ ДИЛА. Чтобы резерв следовал за DESIGN_IMAGE_QUALITY, дверь должна
// прочитать ту же настройку, что читает воркер, — а «ту же» значит из ОДНОГО выражения, а не из
// двух чтений одной переменной (см. SetDesignGenerationEnabled: настройка едет из TOML И из среды,
// и второе чтение os.Getenv молча разошлось бы с первым на любом деплое, который задал её файлом).
// Второй читатель дила — это второе число, ровно то, что здесь и чинится. Потолок читателя не
// заводит вовсе. Платит за это только ОДНОВРЕМЕННОСТЬ: резерв висит от старта прогона до его
// терминального перехода и снимается целиком, а в дневной потолок после этого входит ФАКТ
// (`spent`), — то есть завышенная оценка сужает очередь в полёте на минуты и не отнимает у дня ни
// цента.
//
// ⚠ ЦИФРЫ ЖДУТ ВЛАДЕЛЬЦА (34-PLAN §6). До тех пор это правдоподобная догадка в долларах, и она
// стоит здесь одной таблицей ровно затем, чтобы её было где заменить одной правкой.
var designPriceEstimate = map[string]decimal.Decimal{
	entity.DesignRunKindFlat:   designImageMediumUSD.Mul(designImageQualityCeiling),
	entity.DesignRunKindRender: designRenderMediumUSD.Mul(designImageQualityCeiling),
	// 3D: ~30 кредитов Meshy по ~$0.02 (meshy.defaultCreditUSD и его же комментарий). Вывести это
	// число из пакета нечем и не нужно: сколько кредитов съест задание, до сабмита не знает и сам
	// провайдер, а курс кредита — env-дил (MESHY_CREDIT_USD), которого дверь не видит. Списание
	// считает meshy.CostUSD по ФАКТИЧЕСКИ съеденным кредитам; здесь стоит потолок обычного задания.
	entity.DesignRunKindThreed: decimal.RequireFromString("0.60"),
	// ВЕКТОР — ЕДИНСТВЕННЫЙ РОД, У КОТОРОГО ЦЕНА ОПУБЛИКОВАНА ПАКЕТОМ СПИСАНИЯ. Берётся ИМЕННО ОНА.
	entity.DesignRunKindVector:    designVectorCeilingUSD(),
	entity.DesignRunKindDraftIdea: decimal.RequireFromString("0.02"),
}

// Базовые цены картиночных родов НА `medium` — том положении дила, которое стоит в
// designgen.DefaultConfig(). Флэт и рендер различаются здесь исторической догадкой (§6), а не
// прайсом провайдера: он тарифицирует выходные токены, а не жанр картинки.
var (
	designImageMediumUSD  = decimal.RequireFromString("0.04")
	designRenderMediumUSD = decimal.RequireFromString("0.08")
)

// designImageQualityFactor — ДИЛ ЦЕНЫ КАРТИНКИ (DESIGN_IMAGE_QUALITY, он же
// designgen.Config.ImageQuality), выраженный множителем к `medium`.
//
// Множитель — это КОЛИЧЕСТВО ВЫХОДНЫХ ТОКЕНОВ, а не ставка: каталог тарифицирует один токен, а
// качество решает, сколько их в картинке. На gpt-image-1 замерено 272 / 1056 / 4160 токенов на
// 1024² для low / medium / high, отсюда ¼ и ×4 (см. orimages: «`high` roughly four times
// `medium`»; per-quality токенов для gpt-image-2 провайдер не публикует, а деньги на нём поехали
// ВНИЗ — −25% за выходной токен, — так что старые множители над новой моделью покрывают с запасом).
//
// ВСЕ ЧЕТЫРЕ ПОЛОЖЕНИЯ НАЗВАНЫ, ХОТЯ СЕГОДНЯ ЧИТАЕТСЯ ТОЛЬКО МАКСИМУМ. Таблица — это то место, где
// дил станет читаться, если дверь однажды его увидит; тот, кто это сделает, возьмёт множитель
// отсюда, а не напишет рядом второй.
var designImageQualityFactor = map[string]decimal.Decimal{
	"low":    decimal.RequireFromString("0.25"),
	"medium": decimal.RequireFromString("1"),
	"high":   decimal.RequireFromString("4"),
	// `auto` ОТДАЁТ ВЫБОР ПРОВАЙДЕРУ, поэтому стоит столько же, сколько самое дорогое положение:
	// оценка не вправе спорить с решением, которого она не принимала.
	"auto": decimal.RequireFromString("4"),
}

// designImageQualityCeiling — самое дорогое положение дила. Считается ПО ТАБЛИЦЕ, а не вписано
// числом: положение, добавленное в таблицу завтра, поднимает потолок само.
var designImageQualityCeiling = designMaxFactor(designImageQualityFactor)

func designMaxFactor(m map[string]decimal.Decimal) decimal.Decimal {
	out := decimal.Zero
	for _, v := range m {
		if v.GreaterThan(out) {
			out = v
		}
	}
	if out.IsZero() {
		// Пустая таблица — это не «бесплатно»: множитель 1 оставляет базовую цену как есть.
		return decimal.NewFromInt(1)
	}
	return out
}

// designVectorCeilingUSD — САМЫЙ ДОРОГОЙ ИЗ ОПУБЛИКОВАННЫХ ТАРИФОВ ВЕКТОРА, взятый из
// recraft.Tier.EstimatedUSD() — того самого числа, которое провайдерский пакет называет «the number
// to RESERVE before the call».
//
// ⚠ ПОЧЕМУ ПО МАКСИМУМУ, А НЕ ПО ТОМУ ТИРУ, КОТОРЫЙ СЕГОДНЯ ЗОВЁТ ВОРКЕР. Тир на проводе не
// выбирается вовсе: designgen/vector.go зашивает recraft.TierVector, то есть $0.08. Но слаг ЭТОГО
// тира переопределяется средой (RECRAFT_MODEL_VECTOR), и деплой, направивший стандартный тир на
// pro-модель, получил бы списание $0.30 против резерва $0.08 — резерв ниже факта, ровно то, что
// здесь чинится. Плюс тот день, когда выбор тира появится на проводе: оценка, привязанная к
// зашитому тиру, промолчала бы. Перебор по recraft.Tiers() гасит оба случая сам и поднимется сам,
// если у провайдера появится третий тир.
func designVectorCeilingUSD() decimal.Decimal {
	out := decimal.Zero
	for _, t := range recraft.Tiers() {
		if v := decimal.NewFromFloat(t.EstimatedUSD()); v.GreaterThan(out) {
			out = v
		}
	}
	return out
}

// designEstimateFor — оценка задания целиком: цена одного выхода на число запрошенных выходов.
func designEstimateFor(kind string, outputs int) decimal.NullDecimal {
	per, ok := designPriceEstimate[kind]
	if !ok {
		return decimal.NullDecimal{}
	}
	if outputs < 1 {
		outputs = 1
	}
	return decimal.NullDecimal{Decimal: per.Mul(decimal.NewFromInt(int64(outputs))), Valid: true}
}

// ─────────────────────────── потолки снимка ───────────────────────────

const (
	// designMaxParamsBytes / designMaxInputsBytes — потолки, объявленные контрактом дословно
	// («Total encoded size is capped at 8 KB», «capped at 64 KB»). Проверяются ПОСЛЕ сборки, по
	// закодированным байтам: считать поля вместо байтов значит проверять другое число.
	designMaxParamsBytes = 8 << 10
	designMaxInputsBytes = 64 << 10
	// designMaxInputRefs / designMaxInputSlots — «refs ≤ 24; slots ≤ 8. A snapshot must fit in a
	// row and in an eye».
	designMaxInputRefs  = 24
	designMaxInputSlots = 8
	// designMaxAskRunes — потолок дельта-фразы. Строка `ask` едет в промпт и в историю; без
	// потолка одно нажатие вставляет туда целую книгу.
	designMaxAskRunes = 4000
	// designMaxGarmentNoteRunes — потолок ОПИСАНИЯ ИЗДЕЛИЯ (tech_card.garment_description, W-3).
	//
	// ⚠ ЭТО ТОТ ЖЕ ПОТОЛОК, ЧТО У `ask`, И ПО ТОЙ ЖЕ ПРИЧИНЕ: оба — слова человека, оба уезжают в
	// промпт и оба ЗАМЕРЗАЮТ в снимке прогона (designAssembleInputs пишет описание в
	// `inputs.garment_note` копией, а не джойном). Разница ровно одна: `ask` пишется этой дверью, а
	// описание — сейвом карточки, поэтому здесь стоит ОТКАЗ ЗАПУСКА, а не отказ записи.
	//
	// ⚠ ПОЧЕМУ ОТКАЗ, А НЕ ОБРЕЗКА. Молчаливая обрезка уже ловилась в этой же волне как отдельный
	// дефект: снимок обязан говорить, ЧТО УШЛО В МОДЕЛЬ, а обрезанная копия утверждала бы слова,
	// которых человек не писал, — и утверждала бы навсегда. Колонка объявлена TEXT, то есть до 64 KB
	// одного описания, а весь снимок ограничен теми же 64 KB: без этого потолка одно описание
	// съедало бы снимок целиком и отказ приходил бы про БАЙТЫ СНИМКА — про число, которого человек
	// не видит и починить не может.
	designMaxGarmentNoteRunes = 4000
	// designMaxRefNoteRunes — потолок записки НА ОДНОЙ КАРТИНКЕ (design_reference.note, W-3:
	// «только воротник», «ткань, а не крой»).
	//
	// МЕНЬШЕ, ЧЕМ У ОПИСАНИЯ, И ЭТО СЧИТАЕТСЯ, А НЕ ВКУС: записок в снимке до designMaxInputRefs
	// штук, и они делят те же 64 KB с выносками и плитами. 24 × 1000 рун кириллицы — это ~48 KB,
	// то есть обычный снимок остаётся далеко от стены, а патологический по-прежнему ловится
	// потолком байтов.
	designMaxRefNoteRunes = 1000
)

// Раскладки кадра — словарь DesignRunParams.layout, повторённый здесь потому, что в сторе он
// приватный, а проверять запрос обязан тот, кто его принимает.
const (
	designLayoutOne     = "one"
	designLayoutPerView = "per_view"
)

// designProfileName / designProfileVersion — подпись профиля промпта, замерзающая в строке
// прогона. Профиль пока один; версия растёт, когда меняется СМЫСЛ собираемого промпта, и именно
// по ней история отличает «то же самое» от «другое, просто похоже выглядит».
const (
	designProfileName    = "design-band"
	designProfileVersion = 1
)

// ─────────────────────────── StartDesignRun ───────────────────────────

// StartDesignRun opens a paid picture job.
//
// ПОРЯДОК ПРОВЕРОК — РЕШЕНИЕ, А НЕ СТИЛЬ: сначала то, что не стоит ни одного чтения (флаг, форма
// запроса), потом чтения (карточка, полоса), и только в самом конце стор, который тратит деньги.
// Перевёрнутый порядок платил бы за запрос, который всё равно был бы отвергнут.
func (s *Server) StartDesignRun(ctx context.Context, req *pb_admin.StartDesignRunRequest) (*pb_admin.StartDesignRunResponse, error) {
	cardID := int(req.GetTechCardId())
	if cardID <= 0 {
		return nil, status.Error(codes.InvalidArgument, "tech_card_id is required")
	}
	clientRequestID := strings.TrimSpace(req.GetClientRequestId())
	if clientRequestID == "" {
		return nil, status.Error(codes.InvalidArgument,
			"client_request_id is required — without it a double click pays twice")
	}
	if err := s.designGenerationGate(); err != nil {
		return nil, err
	}

	kind := strings.TrimSpace(req.GetKind())
	if !entity.IsDesignRunKind(kind) {
		return nil, status.Errorf(codes.InvalidArgument,
			"kind %q is not flat | render | threed | vector", kind)
	}
	// draft_idea ОТКАЗЫВАЕТСЯ ЗДЕСЬ, дословно по контракту. Текстовый прогон исполняется в
	// хендлере синхронно и возвращает свой ответ; заведённый отсюда, он вернул бы строку
	// `pending`, которую никто никогда не опрашивает, — предикат захвата воркера исключает
	// draft_idea намеренно, иначе воркер оплатил бы второй вызов той же модели.
	if kind == entity.DesignRunKindDraftIdea {
		return nil, status.Error(codes.InvalidArgument,
			"a text run has its own verb: call DraftDesignIdea, which executes inline and returns its answer")
	}
	// ⚠ ДО ЧТЕНИЙ И ДО РЕЗЕРВА. Род, чей маршрут не подключён, не имеет ключа или чей выход
	// некуда положить, не должен заводить строку: она зарезервировала бы деньги дня и через тик
	// воркера гарантированно провалилась. Ответ считает воркер из своих же возможностей.
	if err := s.designKindGateCheck(kind); err != nil {
		return nil, err
	}
	ask := strings.TrimSpace(req.GetAsk())
	if len([]rune(ask)) > designMaxAskRunes {
		return nil, status.Errorf(codes.InvalidArgument,
			"ask is %d characters; the ceiling is %d", len([]rune(ask)), designMaxAskRunes)
	}

	// ─── карточка и полоса ───
	card, err := s.repo.TechCards().GetTechCardById(ctx, cardID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, status.Error(codes.NotFound, "tech card not found")
		}
		slog.Default().ErrorContext(ctx, "design run: cannot load the tech card",
			slog.Int("tech_card_id", cardID), slog.String("err", err.Error()))
		return nil, status.Error(codes.Internal, "cannot load the tech card")
	}
	// ОПИСАНИЕ ИЗДЕЛИЯ МЕРЯЕТСЯ ЗДЕСЬ, ХОТЯ ПИШЕТСЯ НЕ ЗДЕСЬ: оно замерзает в снимке ЭТОГО прогона
	// и едет в промпт, а сейв карточки о промпте ничего не знает. См. designMaxGarmentNoteRunes.
	if n := len([]rune(card.GarmentDescription.String)); n > designMaxGarmentNoteRunes {
		return nil, status.Errorf(codes.InvalidArgument,
			"the card's garment description is %d characters; the ceiling is %d — it is copied into "+
				"this run's frozen snapshot and into the prompt, so shorten it on the card first",
			n, designMaxGarmentNoteRunes)
	}
	// runLimit = 1: истории здесь не нужно ни строки, нужны верстак, референсы и флаг рендера.
	band, err := s.repo.Design().GetBand(ctx, cardID, 1)
	if err != nil {
		return nil, designError(ctx, "failed to read the design band before starting a run", err, nil)
	}

	// ─── W-13: 3D ТОЛЬКО ПОСЛЕ FABRIC RENDER ───
	//
	// Флаг считает GetBand по ВСЕЙ карточке (`design_picture.kind = 'render' AND hidden_at IS
	// NULL`), а не по загруженной странице: рендер вполне лежит за потолком страницы, и гейт,
	// посчитанный по ней, закрыл бы 3D ровно там, где оно законно.
	if kind == entity.DesignRunKindThreed && !band.HasFabricRender {
		return nil, designRefusal(codes.FailedPrecondition, "no_fabric_render",
			"3D needs a fabric render first: this card has no render that is not hidden. "+
				"The order is flats → fabric render → 3D",
			map[string]string{"tech_card_id": strconv.Itoa(cardID)})
	}

	// ─── реран: параметры и входы приезжают ИЗ БАЗЫ ───
	var parent *entity.DesignRun
	if req.GetRerunOfRunId() > 0 {
		parent, err = s.designRerunParent(ctx, cardID, kind, int(req.GetRerunOfRunId()))
		if err != nil {
			return nil, err
		}
	}

	params, err := designEffectiveParams(req.GetParams(), parent)
	if err != nil {
		return nil, err
	}
	// ИДЕНТИЧНОСТЬ ДЕТАЛИ ДЕРЖИТСЯ НА ОБЕИХ ГРАНИЦАХ. designEffectiveParams проверяет форму
	// списка, но намеренно не знает базы; уже загруженная полоса отвечает на второй вопрос — каждый
	// названный адрес действительно является detail-слотом ЭТОЙ карточки. Положительный чужой id
	// иначе замёрз бы в истории как правдоподобная, но ложная подпись результата.
	//
	// ⚠ ЕЙ ОТДАЁТСЯ req.GetParams(), А НЕ params, И ЭТО ТО ЖЕ РАЗЛИЧЕНИЕ, ЧТО У ПРАВИЛА ДЛИН
	// («THE LENGTH RULE BINDS THE CALLER, NOT HISTORY», designEffectiveParams). params у рерана без
	// параметров — УНАСЛЕДОВАННЫЕ: клиент не присылал ни поля, ни списка, и отказ назвал бы ему
	// `params.detail_slot_ids.0` в запросе, где нет ни `params`. Хуже того, он был бы ВЕЧНЫМ:
	// пустой detail-слот законно удаляется (DeleteDetailSlot), сегодняшний верстак его больше не
	// содержит, а снимок задним числом не чинят — прогон стал бы неперезапускаемым навсегда.
	//
	// ДЕГРАДАЦИЯ ЧЕСТНАЯ, А НЕ МОЛЧАЛИВАЯ. Унаследованный список НЕ чистится: выбросить из него
	// «уже не существующий» адрес значило бы переписать замороженную просьбу и сдвинуть позиционное
	// соответствие с `views`. Он едет как есть, а ИМЯ к нему приходит из снимка РОДИТЕЛЯ, который
	// реран переписывает целиком (designRunInputs) — то есть из той единственной записи, где имя
	// удалённой детали ещё живо. Не нашлось и там — промпт скажет «detail», ровно столько, сколько
	// известно.
	//
	// Передавать сюда сообщение КЛИЕНТА, а не флаг, — тоже решение: функция физически не видит
	// унаследованного списка и не может начать его проверять по недосмотру.
	if err := designRefuseForeignDetailSlots(cardID, req.GetParams(), band.Bench); err != nil {
		return nil, err
	}
	// ТОТ ЖЕ ПРИЁМ ДЛЯ ПОЛОК: адрес, названный КЛИЕНТОМ, отвечает за себя, унаследованный — нет.
	// Довод дословно тот же, что у детали строкой выше, и он здесь ещё жёстче: полку законно
	// удаляют (DeleteDesignAsset), а параметры родителя заморожены — проверка унаследованного
	// списка сделала бы прогон неперезапускаемым НАВСЕГДА.
	if err := designRefuseForeignClothAssets(cardID, req.GetParams(), band.Assets); err != nil {
		return nil, err
	}
	// ГРАНИЦА КАРТОЧКИ — ДО ДЕНЕГ. Все три списка приезжают с провода и все три уезжают
	// ПОСТАВЩИКУ: designgen/snapshot.go собирает ссылки прогона из плит, референсов,
	// `extra_input_media_ids`, `colour.fabric_media_id` И текстуры КАЖДОЙ ткани `colour.fabrics`.
	// Проверяются все, потому что дефект у них один.
	if err := s.designRefuseForeignMedia(ctx, cardID, "params.extra_input_media_ids",
		designInt32sToInts(params.GetExtraInputMediaIds())...); err != nil {
		return nil, err
	}
	if err := s.designRefuseForeignMedia(ctx, cardID, "params.colour.fabric_media_id",
		int(params.GetColour().GetFabricMediaId())); err != nil {
		return nil, err
	}
	// ⚠ СПИСОК ТКАНЕЙ — ТРЕТИЙ НЕЗАВИСИМЫЙ ИСТОЧНИК ЧУЖОГО НОМЕРА, И ЭТО НЕ ПОВТОР СКАЛЯРА.
	// Скаляр — эхо ПЕРВОЙ ткани (контракт DesignColourRecipe), поэтому проверка скаляра ничего не
	// говорит о второй: достаточно было положить чужую картинку в `fabrics[1]`, и она уезжала
	// поставщику при полностью законном `fabric_media_id`. Волна, научившая воркер отправлять
	// текстуру КАЖДОЙ ткани (snapshot.go), обязана была расширить и эту границу.
	//
	// ЗДЕСЬ ДЕЙСТВУЮЩИЕ ПАРАМЕТРЫ, А НЕ СООБЩЕНИЕ КЛИЕНТА, и это ТА ЖЕ ГРАНИЦА, ЧТО У ДВУХ
	// ПРОВЕРОК ВЫШЕ: медиа уезжает поставщику и с унаследованных параметров тоже, а строка
	// media(id) под собой не исчезает (FK держат её RESTRICT'ом), так что вечного отказа, из-за
	// которого адрес полки проверяется только у говорящего, здесь просто не бывает.
	if err := s.designRefuseForeignMedia(ctx, cardID, "params.colour.fabrics.media_id",
		designClothMediaIDs(params.GetColour())...); err != nil {
		return nil, err
	}

	src := designInputSources{
		Kind:   kind,
		Card:   card,
		Refs:   band.References,
		Bench:  band.Bench,
		Params: params,
	}
	inputs, fitAtLaunch, err := s.designRunInputs(ctx, src, parent)
	if err != nil {
		return nil, err
	}
	// ⚠ ПЛИТЫ ШТАМПУЮТСЯ ДО КОДИРОВКИ ПАРАМЕТРОВ — порядок здесь несущий, а не стилистический:
	// иначе в колонку уедет то, что прислал клиент.
	designStampSourcePictures(kind, params, designRunPlates(src, parent))

	paramsJSON, err := designMarshalJSON(params)
	if err != nil {
		slog.Default().ErrorContext(ctx, "design run: params did not encode",
			slog.String("err", err.Error()))
		return nil, status.Error(codes.Internal, "the run parameters could not be stored")
	}
	if len(paramsJSON) > designMaxParamsBytes {
		return nil, status.Errorf(codes.InvalidArgument,
			"params encode to %d bytes; the ceiling is %d", len(paramsJSON), designMaxParamsBytes)
	}

	inputsJSON, err := designMarshalJSON(inputs)
	if err != nil {
		slog.Default().ErrorContext(ctx, "design run: the input snapshot did not encode",
			slog.String("err", err.Error()))
		return nil, status.Error(codes.Internal, "the input snapshot could not be stored")
	}
	if len(inputsJSON) > designMaxInputsBytes {
		return nil, status.Errorf(codes.InvalidArgument,
			"the input snapshot encodes to %d bytes; the ceiling is %d",
			len(inputsJSON), designMaxInputsBytes)
	}

	outputs := designRequestedOutputs(kind, params)
	started, err := s.repo.Design().StartRun(ctx, entity.DesignRunStart{
		TechCardId:      cardID,
		ClientRequestId: clientRequestID,
		Kind:            kind,
		Ask:             ask,
		// json.RawMessage, А НЕ entity.RawJSON, И ЭТО НЕ ОПЕЧАТКА. RawJSON существует ради
		// NULL-безопасного ЧТЕНИЯ колонки; здесь тип принадлежит запросной структуре стора, у
		// которой NULL взяться неоткуда. Правило «только RawJSON» — про колонки.
		Params:           json.RawMessage(paramsJSON),
		Inputs:           json.RawMessage(inputsJSON),
		ProfileName:      designProfileName,
		ProfileVersion:   designProfileVersion,
		FitAtLaunch:      fitAtLaunch,
		RequestedOutputs: outputs,
		PriceEstimate:    designEstimateFor(kind, outputs),
		Author:           designActor(ctx),
		RerunOf:          designParentID(parent),
	})
	if err != nil {
		return nil, designError(ctx, "failed to start the design run", err, nil)
	}
	return &pb_admin.StartDesignRunResponse{
		Run:    s.designRunResponse(ctx, started.Run),
		Budget: s.designBudgetResponse(ctx, started.Budget),
	}, nil
}

// designParentID — id родителя рерана либо 0. Отдельной функцией, чтобы «nil значит обычный
// прогон» было написано ровно один раз.
func designParentID(parent *entity.DesignRun) int {
	if parent == nil {
		return 0
	}
	return parent.Id
}

// designRerunParent читает прогон, который повторяют, и отвечает за то, чтобы им нельзя было
// указать на чужую карточку, на текстовый прогон или на прогон ДРУГОГО РОДА.
//
// СТОР ПРОВЕРЯЕТ ТО ЖЕ САМОЕ ВНУТРИ СВОЕЙ ТРАНЗАКЦИИ, и это не дубликат: там проверка — пояс
// против гонки (родителя могли удалить между чтением и вставкой), здесь — источник ВХОДОВ,
// которые без этого чтения неоткуда взять.
//
// ⚠ РОД ОБЯЗАН СОВПАСТЬ, И ЭТО НЕ АККУРАТНОСТЬ, А ГРАНИЦА ВХОДА. Реран копирует снимок родителя
// ДОСЛОВНО («из сегодняшнего состояния карточки берётся ноль полей»), а `DesignInputSlot` рода
// плиты не несёт: в нём есть вид и media_id и нет ответа на вопрос, флэт это или плита рендера.
// Значит всякий читатель снимка вынужден считать род ИЗ РОДА ПРОГОНА — и threedPictures именно
// это и делает, законно узнавая в скопированной строке «плиту этого прогона». Реран 3D по
// рендер-родителю поэтому отправлял поворотному столу ФЛЭТЫ, из которых делали рендер: технические
// чертежи вместо четырёх видов готовой вещи, прогон закрывался `done`, деньги списывались. Это тот
// же V-14 с другой стороны — там вход считали два писателя, здесь его подменяет род.
//
// ПОЧЕМУ ПРАВИЛО ЗДЕСЬ, А НЕ В ФИЛЬТРЕ 3D. Починить это в threedPictures было бы нечем: у него на
// руках снимок, в котором рода нет. Написать род в снимок значило бы завести ВТОРОЙ дом для факта,
// который уже сказан колонкой `kind` строки прогона, — и старые замороженные снимки его всё равно
// не несут. Совпадение рода — это утверждение о ДВУХ СТРОКАХ, и проверяется оно там, где обе видны.
func (s *Server) designRerunParent(ctx context.Context, cardID int, kind string, parentID int) (*entity.DesignRun, error) {
	parent, err := s.repo.Design().GetRun(ctx, parentID)
	if err != nil {
		return nil, designError(ctx, "failed to read the run being rerun", err, nil)
	}
	if parent.TechCardId != cardID {
		// NotFound, а не PermissionDenied: «этого прогона у этой карточки нет» — правда, и она
		// не рассказывает постороннему, что прогон с таким номером существует у кого-то ещё.
		return nil, designRefusal(codes.NotFound, "not_found",
			fmt.Sprintf("run %d does not belong to tech card %d", parentID, cardID), nil)
	}
	if parent.Kind == entity.DesignRunKindDraftIdea {
		return nil, status.Errorf(codes.InvalidArgument,
			"run %d is a text run: it has no picture inputs to repeat", parentID)
	}
	if parent.Kind != kind {
		return nil, status.Errorf(codes.InvalidArgument,
			"run %d is a %s run and this is a %s run: a repeat sends the model the SAME inputs, "+
				"and the pictures a %s run froze are not the pictures a %s run may send",
			parentID, parent.Kind, kind, parent.Kind, kind)
	}
	return parent, nil
}

// ─────────────────── ЧУЖОЕ МЕДИА: ГРАНИЦА КАРТОЧКИ ───────────────────

// designRefuseForeignMedia — ГРАНИЦА КАРТОЧКИ У ДВЕРИ: медиа, ПРИНАДЛЕЖАЩЕЕ ДРУГОЙ ТЕХ-КАРТЕ, не
// открывает здесь оплаченного прогона.
//
// ЧТО БЫЛО. Идентификаторы медиа приезжали с провода и проверялись ровно на «> 0». Любой номер из
// системы — картинка чужой карточки в том числе — уезжал в платную генерацию
// (`extra_input_media_ids`, `colour.fabric_media_id`), замерзал в снимке и оставался в истории
// утверждением, которого никто не делал.
//
// ⚠ ПРАВИЛО ЖИВЁТ В СТОРЕ, А ЗДЕСЬ ТОЛЬКО СПРАШИВАЮТ. Сначала эта функция отвечала на вопрос сама,
// через реестр ссылок media, — и это было ВТОРОЕ мнение о том же вопросе, на который внутри своей
// транзакции отвечает ImportVector: два множества «держателей» (реестр знает ещё выноски карточки,
// плиты версий и примерки) разошлись бы в первый же день, когда правят одно. Спрашивается ОДИН
// глагол — Design().AssertMediaNotForeign, — и потому у двери и у стора ответ один по построению.
//
// ⚠ ЗАЧЕМ ТОГДА ВООБЩЕ СПРАШИВАТЬ ЗДЕСЬ, РАЗ СТОР ЗНАЕТ. Потому что StartRun РЕЗЕРВИРУЕТ ДЕНЬГИ:
// отказ, пришедший после резерва, стоил бы дню оплаченной строки. Тот же довод, по которому здесь
// же стоят ворота рода и W-13.
func (s *Server) designRefuseForeignMedia(ctx context.Context, cardID int, field string, ids ...int) error {
	want := make([]int, 0, len(ids))
	seen := make(map[int]struct{}, len(ids))
	for _, id := range ids {
		if id <= 0 {
			continue
		}
		if _, dup := seen[id]; dup {
			continue
		}
		seen[id] = struct{}{}
		want = append(want, id)
	}
	if len(want) == 0 {
		return nil
	}
	err := s.repo.Design().AssertMediaNotForeign(ctx, cardID, want)
	if err == nil {
		return nil
	}
	// ⚠ ПРОВЕРКА НА nil ОБЯЗАТЕЛЬНА ЗДЕСЬ, А НЕ ВНУТРИ designError: та таблица переводов не знает
	// «всё хорошо» — не найдя ошибку в списке, она отвечает Internal. Переданный ей nil закрыл бы
	// КАЖДЫЙ законный прогон.
	//
	// Поле называется в детали отказа: у прогона два независимых источника чужого номера, и
	// человеку надо знать, какой из них чинить.
	return designError(ctx, "failed to check who the input pictures belong to", err,
		map[string]string{"field": field})
}

// designEffectiveParams — ЧТО ПРОСЯТ У МОДЕЛИ.
//
// РАЗДЕЛЕНИЕ, КОТОРОЕ ВАЖНО НЕ ПЕРЕПУТАТЬ. Параметры — это ЗАПРОС («какие виды, какая
// раскладка»), и он принадлежит человеку, который жмёт кнопку. Входы — это ПРОВЕНАНС («что
// именно ушло в модель»), и он принадлежит серверу. Поэтому клиентские параметры рерана
// применяются поверх (контракт: «`ask` and `params` still apply ON TOP»), а входы всегда
// переписываются со строки родителя, и клиент не может прислать их вовсе.
//
// Когда клиент параметров не прислал, наследуются родительские: реран «то же самое ещё раз» —
// самый частый, и заставлять клиента пересобирать снимок значило бы вернуть ему ровно ту
// возможность соврать, ради устранения которой реран отдан серверу.
func designEffectiveParams(in *pb_common.DesignRunParams, parent *entity.DesignRun) (*pb_common.DesignRunParams, error) {
	params := in
	if params == nil && parent != nil && len(parent.Params) > 0 {
		inherited := &pb_common.DesignRunParams{}
		if err := designUnmarshalJSON(parent.Params, inherited); err != nil {
			return nil, status.Errorf(codes.FailedPrecondition,
				"run %d cannot be rerun: its stored parameters do not parse", parent.Id)
		}
		params = inherited
	}
	if params == nil {
		params = &pb_common.DesignRunParams{}
	}
	// КЛОН, А НЕ ЗАПРОС: ниже поля правятся (пустая раскладка становится явной), а мутировать
	// входящее сообщение значит писать в чужую память — перехватчики и логи видят тот же объект.
	params, _ = proto.Clone(params).(*pb_common.DesignRunParams)

	detailViews := 0
	for i, v := range params.GetViews() {
		if !entity.IsDesignGhostView(v) {
			return nil, status.Errorf(codes.InvalidArgument,
				"params.views.%d %q is not a view of the garment", i, v)
		}
		if v == entity.DesignViewDetail {
			detailViews++
		}
	}
	detailSlots := params.GetDetailSlotIds()
	seenDetailSlots := make(map[int32]int, len(detailSlots))
	for i, id := range detailSlots {
		if id <= 0 {
			return nil, status.Errorf(codes.InvalidArgument,
				"params.detail_slot_ids.%d must be a detail slot id", i)
		}
		if first, duplicate := seenDetailSlots[id]; duplicate {
			return nil, status.Errorf(codes.InvalidArgument,
				"params.detail_slot_ids.%d duplicates params.detail_slot_ids.%d (slot %d)", i, first, id)
		}
		seenDetailSlots[id] = i
	}
	// ─── THE LENGTH RULE BINDS THE CALLER, NOT HISTORY ───
	//
	// `in == nil` means these params were INHERITED from the parent run of a rerun, and a run
	// frozen before this field existed carries `views:["detail"]` with no ids at all. Holding
	// inherited params to the rule would mean that every detail run recorded before today can
	// never be rerun again — a validation added now would retroactively condemn rows already on
	// disk, which no amount of correctness at the door is worth. The prompt degrades honestly
	// instead: an unnamed detail is asked for as «detail», which is exactly as much as that run
	// ever knew.
	//
	// A CALLER THAT SPEAKS, THOUGH, IS HELD TO BOTH STATEMENTS AT ONCE: `views` says how many
	// detail pictures were asked for and `detail_slot_ids` says which slots they are for, and a
	// pair that disagrees means one of them is false.
	if in != nil && len(detailSlots) != detailViews {
		return nil, status.Errorf(codes.InvalidArgument,
			"params.detail_slot_ids has %d elements but params.views has %d detail elements; "+
				"each detail view must name exactly one slot", len(detailSlots), detailViews)
	}
	switch params.GetLayout() {
	case "":
		// ПУСТОЕ ЗАПИСЫВАЕТСЯ ЯВНО. Раскладка замерзает в истории и участвует в дивайдере
		// «earlier — inputs have changed»; строка, у которой её нет, сравнивалась бы с строкой,
		// у которой она `one`, и разошлась бы без причины.
		params.Layout = designLayoutOne
	case designLayoutOne, designLayoutPerView:
	default:
		return nil, status.Errorf(codes.InvalidArgument,
			"params.layout %q is not one | per_view", params.GetLayout())
	}
	for i, v := range params.GetFixTargets() {
		if !entity.IsDesignSilhouetteView(v) {
			return nil, status.Errorf(codes.InvalidArgument,
				"params.fix_targets.%d %q is not a silhouette side; a detail is named in fix_slot_ids", i, v)
		}
	}
	if t := params.GetFixTarget(); t != "" {
		if !entity.IsDesignSilhouetteView(t) {
			return nil, status.Errorf(codes.InvalidArgument,
				"params.fix_target %q is not a silhouette side", t)
		}
		// ─── ДВА НАПИСАНИЯ ОДНОГО ПОЛЯ ПРИВОДЯТСЯ К ОДНОМУ ЗДЕСЬ, У ДВЕРИ ───
		//
		// Контракт называет ОДНО правило: читатель берёт `fix_targets`, когда список непуст, и
		// падает на скаляр `fix_target` только когда он пуст. Скаляр — старое написание, которое
		// обязано остаться читаемым навсегда.
		//
		// ЧТО БЫЛО. Правил жило ДВА: промпт (designgen/snapshot.go) честно падал на скаляр, а
		// отбор плит верстака строил ОБЪЕДИНЕНИЕ списка и скаляра. Запрос с fix_target="front" и
		// fix_targets=["back"] отдавал модели плиты front И back, а текстом просил «правь back» —
		// то есть оплаченный кадр собирался не из того, о чём его просили, и ни один из двух
		// читателей при этом не ошибался «по-своему»: они просто отвечали на разные вопросы.
		//
		// ПРОТИВОРЕЧИЕ ОТВЕРГАЕТСЯ, А НЕ РАЗРЕШАЕТСЯ МОЛЧА. Выбросить `front` по правилу «список
		// сильнее» значило бы снова тихо отрезать часть просьбы — тот же класс, что молчаливая
		// обрезка входов. Совпадающие написания (скаляр назван И входит в список) законны:
		// клиент, который шлёт оба для совместимости, ничему не противоречит — и при таком входе
		// ОБА мыслимых правила, объединение и падение, дают ОДИН И ТОТ ЖЕ ответ. Разойтись им
		// больше не на чем.
		if n := len(params.GetFixTargets()); n > 0 && !slices.Contains(params.GetFixTargets(), t) {
			return nil, designRefusal(codes.InvalidArgument, "contradictory_fix_target",
				fmt.Sprintf("params.fix_target %q is not among params.fix_targets %v: the two spellings "+
					"of one field contradict each other, and the server will not guess which one you meant",
					t, params.GetFixTargets()),
				map[string]string{"fix_target": t, "fix_targets": strings.Join(params.GetFixTargets(), ",")})
		}
	}
	for i, id := range params.GetExtraInputMediaIds() {
		if id <= 0 {
			return nil, status.Errorf(codes.InvalidArgument,
				"params.extra_input_media_ids.%d must be a media id", i)
		}
	}
	// ЧИСЛО ДОП-ВХОДОВ МЕРЯЕТСЯ ЗДЕСЬ, А НЕ ТОЛЬКО ПОСЛЕ СБОРКИ СНИМКА, И ЭТО НЕ ДУБЛИКАТ.
	// Потолок designMaxInputRefs в designAssembleInputs стоит на СОБРАННЫХ refs — а у рерана
	// снимок переписывается со строки родителя и сборка не зовётся вовсе, тогда как воркер читает
	// `extra_input_media_ids` ИЗ ПАРАМЕТРОВ (designgen/snapshot.go: referenceMediaIDs), то есть
	// список без потолка доехал бы до поставщика в обход обоих. Тот же потолок и по той же причине:
	// снимок обязан помещаться в строку и в глаз.
	if n := len(params.GetExtraInputMediaIds()); n > designMaxInputRefs {
		return nil, status.Errorf(codes.InvalidArgument,
			"params.extra_input_media_ids names %d pictures; the ceiling is %d", n, designMaxInputRefs)
	}
	return params, nil
}

// designRefuseForeignDetailSlots держит КАРТОЧНУЮ половину адреса детали. Проверка формы живёт в
// designEffectiveParams; эта стоит после GetBand и не делает второго чтения: Bench уже содержит
// полный набор адресов карточки.
//
// ⚠ `spoken` — СООБЩЕНИЕ КЛИЕНТА, а не действующие параметры прогона. Разницу видно только на
// реране без параметров, и она там решающая: см. довод у места вызова. nil-сообщение отдаёт пустой
// список, цикл не исполняется ни разу — «тот, кто молчит, не может противоречить».
func designRefuseForeignDetailSlots(cardID int, spoken *pb_common.DesignRunParams, bench []entity.DesignBenchSlot) error {
	details := make(map[int]struct{}, len(bench))
	for _, slot := range bench {
		if slot.TechCardId == cardID && slot.ViewKey == entity.DesignViewDetail {
			details[slot.Id] = struct{}{}
		}
	}
	for i, id := range spoken.GetDetailSlotIds() {
		if _, ok := details[int(id)]; !ok {
			return status.Errorf(codes.InvalidArgument,
				"params.detail_slot_ids.%d %d is not a detail slot of tech card %d", i, id, cardID)
		}
	}
	return nil
}

// designRefuseForeignClothAssets держит КАРТОЧНУЮ половину адреса полки, ровно как
// designRefuseForeignDetailSlots держит её для адреса детали. Второго чтения не делает: полки
// приезжают в полосе ЦЕЛИКОМ, без страницы (entity.DesignBand.Assets, потолок
// MaxDesignAssetsPerCard), поэтому band.Assets — это весь набор адресов карточки.
//
// ⚠ ЗАЧЕМ ВООБЩЕ ПРОВЕРЯТЬ ПОЛЕ, КОТОРОЕ НИКТО НЕ РАЗРЕШАЕТ. Именно поэтому и проверять. Контракт
// говорит прямо: факты ткани ЗАМОРОЖЕНЫ копиями, а `asset_id` едет рядом как ПРОВЕНАНС — «какая
// это была строка полки» — и читатель его не резолвит. Значит ложное значение никогда не всплывёт
// ошибкой: строка истории просто навсегда утверждает, что ткань пришла с полки, которой у этой
// карточки нет. Параметры прогона заморожены, задним числом это не чинится.
//
// `spoken` — СООБЩЕНИЕ КЛИЕНТА, и разница видна только на реране без параметров: см. довод у места
// вызова. nil-сообщение отдаёт пустой список, цикл не исполняется ни разу.
//
// НОЛЬ ПРОПУСКАЕТСЯ МОЛЧА: контракт объявляет его законным («0 when the cloth was stated without a
// shelf row»), и ткань, названную одними словами, эта функция отвергать не вправе.
func designRefuseForeignClothAssets(cardID int, spoken *pb_common.DesignRunParams, assets []entity.DesignAsset) error {
	shelf := make(map[int]struct{}, len(assets))
	for _, a := range assets {
		if a.TechCardId == cardID {
			shelf[a.Id] = struct{}{}
		}
	}
	for i, f := range spoken.GetColour().GetFabrics() {
		id := int(f.GetAssetId())
		if id == 0 {
			continue
		}
		if _, ok := shelf[id]; !ok {
			return status.Errorf(codes.InvalidArgument,
				"params.colour.fabrics.%d.asset_id %d is not a shelf row of tech card %d", i, id, cardID)
		}
	}
	return nil
}

// designClothMediaIDs — текстуры ВСЕХ тканей рецепта. Отдельной функцией, чтобы у места вызова
// стоял список, а не цикл: границе карточки отдаётся один набор номеров, и дедупликацию с нулями
// разбирает она сама.
func designClothMediaIDs(c *pb_common.DesignColourRecipe) []int {
	out := make([]int, 0, len(c.GetFabrics()))
	for _, f := range c.GetFabrics() {
		out = append(out, int(f.GetMediaId()))
	}
	return out
}

// designInt32sToInts — один переход между шириной провода и шириной домена. Отдельной функцией,
// чтобы цикл-конвертер не размножался по местам вызова.
func designInt32sToInts(in []int32) []int {
	out := make([]int, 0, len(in))
	for _, v := range in {
		out = append(out, int(v))
	}
	return out
}

// ─────────────────── ПЛИТЫ, ИЗ КОТОРЫХ СОБРАН ПОВОРОТНЫЙ СТОЛ ───────────────────

// designStampSourcePictures ЗАПОЛНЯЕТ DesignThreedParams.source_picture_ids — поле, которое до сих
// пор не писал НИКТО.
//
// ЧТО БЫЛО. Контракт объявляет его дословно: «The four render plates of ONE rrev that this
// turntable was built from. Without them the run panel cannot show what the rotation was assembled
// out of». Писателей у поля было ноль, и это опаснее обычной мёртвой строки: параметры прогона —
// ЗАМОРОЖЕННАЯ ИСТОРИЯ, и пустое поле в ней не молчит, а УТВЕРЖДАЕТ, что прогон не видел ни одной
// плиты. Видел: 3D по построению собирается из плит рендера (designInputSlots), и никакого второго
// источника у него нет. Задним числом это не чинится — снимок заморожен.
//
// ⚠ ПОЛЕ ПРИНАДЛЕЖИТ СЕРВЕРУ ЦЕЛИКОМ, ровно как входы, поэтому клиентское значение ЗАТИРАЕТСЯ
// ВСЕГДА, а не дополняется. Иначе к пустому полю, которое врёт молчанием, добавилось бы непустое,
// которое врёт содержанием: клиент назвал бы плиты, которых прогон не брал, и история подтвердила
// бы это навсегда.
//
// ⚠ И ЗАТИРАЕТСЯ ОНО У ЛЮБОГО РОДА, А НЕ ТОЛЬКО У 3D. Поле «meaningful only for kind=threed», но
// присланное на флэте оно всё равно замёрзло бы в строке и читалось бы как провенанс. Род, который
// плит не берёт, обязан говорить об этом пустотой — и это единственный случай, когда пустота здесь
// правдива.
func designStampSourcePictures(kind string, params *pb_common.DesignRunParams, plates []int32) {
	if params == nil {
		return
	}
	if kind != entity.DesignRunKindThreed {
		if params.GetThreed() != nil {
			params.Threed.SourcePictureIds = nil
		}
		return
	}
	if params.GetThreed() == nil {
		// Прогон 3D без блока параметров — законный вход (все поля блока необязательны), но
		// провенанс у него всё равно есть, и положить его больше некуда.
		params.Threed = &pb_common.DesignThreedParams{}
	}
	params.Threed.SourcePictureIds = plates
}

// designParentPlates — плиты РОДИТЕЛЯ рерана.
//
// Реран посылает модели ТО ЖЕ САМОЕ: входы переписываются со строки родителя целиком, значит и
// сказать про плиты он обязан то же, что сказал родитель. Сегодняшний верстак здесь не читается
// вовсе — он мог с тех пор смениться, и посчитанный по нему список назвал бы плиты, которых этот
// прогон не увидит.
func designParentPlates(parent *entity.DesignRun) []int32 {
	if parent == nil || len(parent.Params) == 0 {
		return nil
	}
	stored := &pb_common.DesignRunParams{}
	if err := designUnmarshalJSON(parent.Params, stored); err != nil {
		// Нечитаемые параметры родителя уже отказали выше (designEffectiveParams) на том пути, где
		// они нужны; здесь молчание честнее выдумки.
		return nil
	}
	return stored.GetThreed().GetSourcePictureIds()
}

// designRequestedOutputs — сколько кадров задание вправе ждать. Число едет в строку и рисует
// плитки-плейсхолдеры, поэтому «сколько-нибудь» здесь не годится: пустая плитка, которую никто
// не заполнит, читается как потерянный результат.
func designRequestedOutputs(kind string, params *pb_common.DesignRunParams) int {
	switch kind {
	case entity.DesignRunKindThreed, entity.DesignRunKindVector:
		// Одна модель и один SVG. Кадры поворотного стола, если они появятся, приедут одним
		// артефактом, а не отдельными кадрами полосы.
		return 1
	}
	if params.GetLayout() == designLayoutOne {
		// КОМПОЗИТ — ОДНА КАРТИНКА, сколько бы видов на ней ни было. Разрез на N кадров это
		// отдельный, бесплатный акт (SplitDesignPicture), и считать его выходами прогона значило
		// бы обещать плитки, которых генерация не приносит.
		return 1
	}
	if n := len(params.GetViews()); n > 0 {
		return n
	}
	return 1
}

// ─────────────────────────── CancelDesignRun ───────────────────────────

// CancelDesignRun stops a run.
//
// ДВА РАЗНЫХ АКТА ПОД ОДНИМ ИМЕНЕМ, и решает между ними стор: ждущий прогон закрывается сразу и
// возвращает резерв дню, идущий получает только `cancel_requested_at`, потому что он УЖЕ у
// поставщика и выбросить оплаченный результат нельзя. Хендлер здесь ничего не выбирает — он бы
// выбирал по устаревшему чтению.
func (s *Server) CancelDesignRun(ctx context.Context, req *pb_admin.CancelDesignRunRequest) (*pb_admin.CancelDesignRunResponse, error) {
	if req.GetRunId() <= 0 {
		return nil, status.Error(codes.InvalidArgument, "run_id is required")
	}
	run, err := s.repo.Design().CancelRun(ctx, int(req.GetRunId()), designActor(ctx))
	if err != nil {
		return nil, designError(ctx, "failed to cancel the design run", err, nil)
	}
	return &pb_admin.CancelDesignRunResponse{Run: s.designRunResponse(ctx, *run)}, nil
}

// ─────────────────────────── GetDesignRun ───────────────────────────

// GetDesignRun reads ONE run whole — the row a rerun is assembled from and the row the run panel
// draws. Its shape is the history row's shape exactly, so the answer drops into what the client
// already drew instead of forcing it to merge two shapes of one row.
func (s *Server) GetDesignRun(ctx context.Context, req *pb_admin.GetDesignRunRequest) (*pb_admin.GetDesignRunResponse, error) {
	if req.GetRunId() <= 0 {
		return nil, status.Error(codes.InvalidArgument, "run_id is required")
	}
	run, err := s.repo.Design().GetRun(ctx, int(req.GetRunId()))
	if err != nil {
		return nil, designError(ctx, "failed to read the design run", err, nil)
	}
	return &pb_admin.GetDesignRunResponse{Run: s.designRunResponse(ctx, *run)}, nil
}

// ─────────────────────────── DraftDesignIdea ───────────────────────────

// draftIdeaSystemPrompt — роль модели. Она пишет ПРОЗУ ДЛЯ ЧЕЛОВЕКА, а не JSON: ответ ложится в
// `design_run.output_text` и читается глазами, поэтому и просить надо связный текст.
//
// ⚠ РОЛЬ ГОВОРИТ ПРО КАРТИНКИ, ПОТОМУ ЧТО КАРТИНКИ ТЕПЕРЬ ПРИЕЗЖАЮТ. Пока этот путь слал только
// текст, «from the notes» было правдой. Теперь доска уезжает изображениями (см. DraftDesignIdea), и
// роль, продолжающая говорить «по заметкам», прямо велела бы модели не смотреть на то, за что уже
// заплачено: картинки в биллинге — входные токены, и потраченные впустую они всё равно потрачены.
// ⚠ THE THREE SECTION TITLES ARE A CONTRACT WITH THE CLIENT, NOT A STYLE CHOICE (V-19). The owner
// asks the draft for three different answers with three different fates: the description is
// offered line by line into the printed concept, the aspects are advice for the construction
// block, the missing callouts are advice to go pin something — and the client
// (head/mood-draft.tsx, parseDraftSections) tells them apart BY THESE TITLES. Renaming a title
// here silently demotes its section to "offer everything into the concept".
const draftIdeaSystemPrompt = "You are a fashion designer's assistant. " +
	"You are shown the pictures of a garment's moodboard, the designer's concept & construction " +
	"description, and the notes pinned on the pictures — every note names its picture by number " +
	"and the spot on it, so you know exactly which part of which image it marks. " +
	"Look at the pictures and answer in exactly three titled sections, plain English prose:\n" +
	"DESCRIPTION — one paragraph, at most 120 words: the garment the board is reaching for — " +
	"silhouette, proportions, construction, the two or three details that carry the idea — " +
	"written so it can stand as the concept & construction description itself.\n" +
	"DESIGN ASPECTS — the construction aspects the pictures and the notes imply, one line each " +
	"(closure, collar, pockets, seams, hem and the like).\n" +
	"MISSING CALLOUTS — what deserves a pinned note and has none: name the picture by its number " +
	"and the spot on it.\n" +
	"Never invent a fabric, a colour or a measurement that the pictures do not show and the notes " +
	"do not mention — say what is missing instead."

// draftIdeaNotConfiguredMsg / draftIdeaModelUnavailableMsg — те же две несводимые настройки, что
// у остальных функций на s.aiOps, и те же слова: одна причина обязана звучать одинаково везде,
// иначе дежурный чинит две разные поломки вместо одной.
const (
	draftIdeaNotConfiguredMsg    = "drafting the idea is not configured (set OPENROUTER_API_KEY)"
	draftIdeaModelUnavailableMsg = "drafting the idea is misconfigured: " + modelUnavailableAdviceMsg
)

// ─────────────────────── ДВА БЕСПЛАТНЫХ ГЛАГОЛА ───────────────────────
//
// ⚠ НИ ОДИН ИЗ НИХ НЕ ПРОХОДИТ designGenerationGate, И ЭТО ПРОВЕРЯЕМОЕ РЕШЕНИЕ, А НЕ ЗАБЫВЧИВОСТЬ.
// Флаг DESIGN_GENERATION_ENABLED стережёт ДЕНЬГИ: он существует затем, чтобы при отсутствующем
// воркере не заводилась оплаченная строка, которую некому забрать. Отметить кадр выбранным и
// подшить уже загруженный SVG не тратят ни цента и не заводят прогона вовсе — навешенный на них
// флаг выключал бы обычную работу с полосой в деплое, где генерация просто не включена.
//
// Оба стоят в этом файле по указанию задачи; по существу они соседи HideDesignPicture и
// SaveDesignEditLayer из design_band.go — там же живут остальные бесплатные глаголы.

// SetDesignPictureSelected marks a picture as CHOSEN, and un-marks it (owner requirement W-12:
// «мы так же можем маркать 3д рендеры как выбранные»).
//
// A VERB OF ITS OWN, DELIBERATELY NOT A FLAG ON HideDesignPicture: hidden says «do not show me
// this», selected says «this is the one». The two are independent — a chosen picture may later be
// hidden — and folding them would make one gesture silently undo the other.
//
// Nothing is exclusive: the owner speaks in the plural, so many pictures may be chosen at once.
func (s *Server) SetDesignPictureSelected(ctx context.Context, req *pb_admin.SetDesignPictureSelectedRequest) (*pb_admin.SetDesignPictureSelectedResponse, error) {
	pic, err := s.repo.Design().SetPictureSelected(ctx,
		int(req.GetPictureId()), req.GetSelected(), designActor(ctx))
	if err != nil {
		return nil, designError(ctx, "failed to set the design picture selection", err, nil)
	}
	return &pb_admin.SetDesignPictureSelectedResponse{Picture: designPictureToPb(*pic)}, nil
}

// ImportDesignVector files an ALREADY-UPLOADED vector file into the band as an edit layer.
//
// IT SPENDS NOTHING, AND THAT IS THE LINE BETWEEN IT AND GENERATION. Vectorising BY MACHINE is a
// paid provider call and goes through StartDesignRun with kind = vector; this verb files a file
// that already exists, which is why the money gate above does not stand here.
//
// THE CLIENT PARSES, THE SERVER RECORDS THE PROVENANCE — the same division of labour
// FlattenDesignEditLayer draws, and for the same reason: there is no SVG parser and no vector
// renderer anywhere in this repository.
func (s *Server) ImportDesignVector(ctx context.Context, req *pb_admin.ImportDesignVectorRequest) (*pb_admin.ImportDesignVectorResponse, error) {
	if req.GetTechCardId() <= 0 {
		return nil, status.Error(codes.InvalidArgument, "tech_card_id is required")
	}
	clientRequestID := strings.TrimSpace(req.GetClientRequestId())
	if clientRequestID == "" {
		return nil, status.Error(codes.InvalidArgument,
			"client_request_id is required — without it a retry files the same SVG twice")
	}
	// КЛЮЧ ЧИТАЕТСЯ СТОРОМ, И ЭТО ПОТРЕБОВАЛО КОЛОНКИ (0351). До неё поле требовалось здесь,
	// доезжало в entity.DesignVectorImport и НЕ ЧИТАЛОСЬ ничем: дедупликация шла по паре
	// (карточка, файл), из-за чего ДРУГОЙ файл под тем же запросом заводил второй слой. Теперь
	// ключ — он, а пара осталась вторым поясом того же обещания; см. шапку store.ImportVector.
	strokes := req.GetStrokes()
	if len(strokes) > design.MaxStrokesBytes {
		return nil, designError(ctx, "strokes too large",
			fmt.Errorf("%w: %d bytes, the ceiling is %d",
				entity.ErrDesignStrokesTooLarge, len(strokes), design.MaxStrokesBytes), nil)
	}
	// The column VALIDATES JSON, so an opaque payload would come back as a raw 3140 naming a
	// column. Checked here, the person is told what is wrong with what they sent — the same
	// treatment SaveDesignEditLayer gives the same bytes.
	if len(strokes) > 0 && !json.Valid(strokes) {
		return nil, status.Error(codes.InvalidArgument, "strokes must be JSON")
	}
	// ⚠ ГРАНИЦУ КАРТОЧКИ ДЕРЖИТ СТОР, И ЗДЕСЬ ЕЁ НАМЕРЕННО НЕТ. Раньше на принадлежность
	// проверялась только картинка (`source_picture_id`); файл и подложка не проверялись ничем,
	// кроме существования строки медиа, — картинка чужой карточки становилась векторным слоем этой.
	// Закрыто в той же SERIALIZABLE-транзакции, где идёт вставка (refuseForeignMedia): у двери нет
	// бесплатного источника этого факта — она не читает ни карточку, ни полосу, — а вторая копия
	// правила здесь была бы вторым мнением о том же вопросе. Этот глагол ничего не резервирует,
	// поэтому и опережать стор ему незачем: у StartDesignRun довод обратный, там ответ нужен ДО
	// денег.
	layer, err := s.repo.Design().ImportVector(ctx, entity.DesignVectorImport{
		TechCardId:      int(req.GetTechCardId()),
		ClientRequestId: clientRequestID,
		SourceMediaId:   int(req.GetSourceMediaId()),
		SourcePictureId: int(req.GetSourcePictureId()),
		Origin:          strings.TrimSpace(req.GetOrigin()),
		BaseMediaId:     int(req.GetBaseMediaId()),
		Strokes:         json.RawMessage(strokes),
		Actor:           designActor(ctx),
	})
	if err != nil {
		return nil, designError(ctx, "failed to import the design vector", err, nil)
	}
	// STROKES ARE ECHOED, exactly as the contract's response comment says — the caller has just
	// sent them, and the layer it must now compare-and-set against is the whole answer.
	return &pb_admin.ImportDesignVectorResponse{Layer: designLayerToPb(*layer, true)}, nil
}

// DraftDesignIdea runs a MULTIMODAL model over the moodboard — its PICTURES and its WORDS — and
// returns the answer inline.
//
// ПОЧЕМУ СИНХРОННО, А НЕ ВОРКЕРОМ, КАК ВСЁ ОСТАЛЬНОЕ. Стор намеренно исключает `draft_idea` из
// предиката захвата (`kind <> 'draft_idea'` в designRunClaimableSQL): воркер, забравший эту
// строку, оплатил бы ВТОРОЙ вызов той же модели. Ответ здесь приходит за секунды и нужен человеку
// на экране немедленно, а не строкой истории, которую он потом опрашивает.
//
// ─────────────────── ⚠ ГРАНИЦА W-15 ПРОХОДИТ ЗДЕСЬ, И ОНА НЕ ТАМ, ГДЕ КАЖЕТСЯ ───────────────────
//
// W-15 ЗАПРЕЩАЕТ ДОСКУ В ГЕНЕРАЦИИ, А НЕ ВО ВСЯКОМ ПРОМПТЕ. Решение владельца дословно: «только в
// генерации». То есть картинки доски не смеют уехать во ФЛЭТЫ, РЕНДЕРЫ и 3D — и обязаны уехать
// сюда: прототип обещает про эту кнопку «reads the pictures, the shared note and the card»
// (proto.html:3223) и показывает счётчик «read N pictures», а строку самого прототипа, которую
// процитировал владелец, надо читать целиком: «read by “draft the idea”, NEVER SENT TO GENERATION»
// (proto.html:3268) — два адресата в одном предложении, один разрешён, другой запрещён.
//
// ⚠ ДВА СОСЕДНИХ ГЛАГОЛА ТЕПЕРЬ ВЕДУТ СЕБЯ ПО-РАЗНОМУ, И РАЗЛИЧИЕ ДЕРЖИТСЯ ТОЛЬКО НА КОДЕ.
// StartDesignRun собирает входы через designAssembleInputs, куда доска не приходит ни при каком
// входе; этот глагол читает доску напрямую. Соблазн «унифицировать два пути» сломает требование
// владельца молча, поэтому различие прибито пробой TestW15BoardReachesTheDraftButNeverGeneration —
// она краснеет с ОБЕИХ сторон: и если доска перестанет доезжать сюда, и если начнёт доезжать туда.
func (s *Server) DraftDesignIdea(ctx context.Context, req *pb_admin.DraftDesignIdeaRequest) (*pb_admin.DraftDesignIdeaResponse, error) {
	cardID := int(req.GetTechCardId())
	if cardID <= 0 {
		return nil, status.Error(codes.InvalidArgument, "tech_card_id is required")
	}
	clientRequestID := strings.TrimSpace(req.GetClientRequestId())
	if clientRequestID == "" {
		return nil, status.Error(codes.InvalidArgument,
			"client_request_id is required — without it a double click pays twice")
	}
	// ФЛАГ ЗАКРЫВАЕТ И ЭТУ ДВЕРЬ, ПО ДРУГОЙ ПРИЧИНЕ. Воркер ей не нужен, но это тот же платный
	// вызов в том же денежном регистре, и полоса выкатывается инертной ЦЕЛИКОМ: половина
	// включённой генерации — это состояние, которого никто не проверял.
	if err := s.designGenerationGate(); err != nil {
		return nil, err
	}
	if !s.aiOps.Enabled() {
		return nil, aiRefusal(aiReasonNotConfigured, draftIdeaNotConfiguredMsg, nil)
	}

	card, err := s.repo.TechCards().GetTechCardById(ctx, cardID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, status.Error(codes.NotFound, "tech card not found")
		}
		slog.Default().ErrorContext(ctx, "draft design idea: cannot load the tech card",
			slog.Int("tech_card_id", cardID), slog.String("err", err.Error()))
		return nil, status.Error(codes.Internal, "cannot load the tech card")
	}
	// ПУСТАЯ ДОСКА — ОТКАЗ, А НЕ ПЛАТНЫЙ ВЫЗОВ НИ О ЧЁМ.
	//
	// ⚠ ПРОВЕРЯЕТСЯ mood, А НЕ ДЛИНА ПРОМПТА, и это разные вопросы. Промпт несёт ещё и имя
	// изделия с фитом, поэтому у любой названной карточки он непустой ВСЕГДА — сторож по его
	// длине не сработал бы никогда и оплачивал бы «придумай одежду по слову „пальто“».
	// ⚠ СТОРОЖ ОСТАЛСЯ НА СЛОВАХ, ХОТЯ КАРТИНКИ ТЕПЕРЬ ЧИТАЮТСЯ, И ЭТО РЕШЕНИЕ, А НЕ НЕДОСМОТР.
	// Доска из одних картинок, без записки и выносок, теперь технически осмысленна — модель их
	// увидит. Но снимок прогона (`inputs`) умеет хранить только СЛОВА доски: поля под список
	// показанных картинок в DesignMoodSnapshot нет. Пустив такую доску, мы завели бы оплаченную
	// строку истории, которая утверждает, что в модель не ушло НИЧЕГО, — а ушло 12 изображений.
	// История, врущая про потраченные деньги, хуже отказа, поэтому дверь пока закрыта.
	// РАСШИРЯТЬ ЭТО НАДО ВМЕСТЕ С ПОЛЕМ В КОНТРАКТЕ, а не отдельно (прото сейчас чужое).
	mood := designMoodSnapshot(card)
	if mood == nil {
		return nil, status.Error(codes.FailedPrecondition,
			"the moodboard says nothing yet: write the description or pin a callout, then draft the idea")
	}

	// ─── КАРТИНКИ ДОСКИ: ПОТОЛОК ДО ДЕНЕГ, ПОТОМ АДРЕСА ───
	//
	// ПОТОЛОК СТОИТ ЗДЕСЬ, А НЕ У ТРАНСПОРТА, И ЭТО НЕ ДУБЛИРОВАНИЕ. Число одно и то же —
	// openrouter.MaxImageParts, — но проверить его надо ДО StartRun: транспорт откажет уже после
	// того, как строка заведена и деньги зарезервированы, и тогда прогон придётся закрывать
	// вместо того, чтобы не открывать. Ровно та же логика, что у потолка снимка ниже.
	//
	// ⚠ СЕРВЕРНЫЙ ПОТОЛОК ДОСКИ ПОЯВИЛСЯ ИМЕННО СЕЙЧАС, ПОТОМУ ЧТО ЧИСЛО КАРТИНОК СТАЛО ДЕНЬГАМИ.
	// Клиент держит MOOD_MAX = 12, но клиентский потолок обходится вторым клиентом и повтором
	// запроса; пока доска давала только слова, цена от числа плиток не зависела и обход ничего не
	// стоил. Теперь каждая картинка — входные токены, поэтому это ОТКАЗ, А НЕ ОБРЕЗКА: молча
	// послать 16 из 40 значит показать модели произвольную часть доски и выдать ответ по ней за
	// ответ по доске.
	boardIDs := designBoardMediaIDs(card)
	if len(boardIDs) > openrouter.MaxImageParts {
		return nil, status.Errorf(codes.InvalidArgument,
			"the moodboard carries %d pictures; one draft may read %d — remove some, or draft from a smaller board",
			len(boardIDs), openrouter.MaxImageParts)
	}
	// АДРЕСА, А НЕ БАЙТЫ: провайдер скачивает наши публичные url сам. Загрузка их сюда прошла бы
	// через процесс с 0.5 GiB RAM и base64-раздуванием — тот же довод, что в designgen.buildJob.
	//
	// РАЗРЕШАЕТСЯ ДО StartRun по той же причине, что и потолок: сорванное чтение медиа не должно
	// оставлять открытый прогон с зарезервированными деньгами. Цена — один лишний запрос на
	// идемпотентном повторе, а повтор это двойной клик, а не горячий путь.
	//
	// И ДО СБОРКИ ПРОМПТА, потому что промпт нумерует выноски по ФАКТИЧЕСКИ приложенным картинкам
	// (V-19): слова и провод обязаны читаться с одного списка, см. designDraftIdeaPrompt.
	boardURLs, attachedIDs, err := s.designBoardPictureURLs(ctx, boardIDs)
	if err != nil {
		slog.Default().ErrorContext(ctx, "draft design idea: cannot resolve the moodboard pictures",
			slog.Int("tech_card_id", cardID), slog.String("err", err.Error()))
		return nil, status.Error(codes.Internal, "cannot read the moodboard pictures")
	}
	prompt := designDraftIdeaPrompt(card, mood, attachedIDs)

	// СНИМОК ВХОДОВ ТЕКСТОВОГО ПРОГОНА — ЭТО ДОСКА, И ТОЛЬКО ОНА. Ни refs, ни slots: ни одного
	// референса и ни одной плиты верстака этот прогон не читает, и пустые списки — утверждение.
	inputs := &pb_common.DesignInputSnapshot{Mood: mood, Fit: card.Fit.String}
	inputsJSON, err := designMarshalJSON(inputs)
	if err != nil {
		slog.Default().ErrorContext(ctx, "draft design idea: the input snapshot did not encode",
			slog.String("err", err.Error()))
		return nil, status.Error(codes.Internal, "the input snapshot could not be stored")
	}
	// ⚠ ТОТ ЖЕ ПОТОЛОК, ЧТО У КАРТИНОЧНОГО ПРОГОНА, И ЕГО ЗДЕСЬ НЕ БЫЛО ВОВСЕ. Снимок этого прогона
	// — доска целиком: записка плюс ТЕКСТ каждой выноски, ни на одно из которых своего потолка нет.
	// Без проверки строка уезжала в стор, где `inputs` объявлена JSON-колонкой, и отказ приходил бы
	// от MySQL про размер пакета — либо не приходил вовсе, а доска молча уезжала в платный вызов
	// мегабайтом. Потолок объявлен контрактом дословно («capped at 64 KB») и проверяется по
	// ЗАКОДИРОВАННЫМ байтам: считать поля вместо байтов значит проверять другое число.
	if len(inputsJSON) > designMaxInputsBytes {
		return nil, status.Errorf(codes.InvalidArgument,
			"the moodboard encodes to %d bytes; the ceiling is %d — shorten the board's note or its callouts",
			len(inputsJSON), designMaxInputsBytes)
	}

	est := designEstimateFor(entity.DesignRunKindDraftIdea, 1)
	started, err := s.repo.Design().StartRun(ctx, entity.DesignRunStart{
		TechCardId:       cardID,
		ClientRequestId:  clientRequestID,
		Kind:             entity.DesignRunKindDraftIdea,
		Inputs:           json.RawMessage(inputsJSON),
		ProfileName:      designProfileName,
		ProfileVersion:   designProfileVersion,
		FitAtLaunch:      card.Fit.String,
		RequestedOutputs: 0, // текстовый прогон не рождает ни одного кадра
		PriceEstimate:    est,
		Author:           designActor(ctx),
	})
	if err != nil {
		return nil, designError(ctx, "failed to open the design idea draft", err, nil)
	}
	run := started.Run

	// ─── ПОВТОР ───
	//
	// ЗАКОНЧЕННЫЙ ПРОГОН ОТДАЁТСЯ КАК ЕСТЬ И МОДЕЛЬ НЕ ЗОВЁТСЯ ВТОРОЙ РАЗ: ровно за этим и стоит
	// client_request_id. Незаконченный — тот, чей хендлер умер посреди вызова; его перехват
	// разрешён ТОЛЬКО по истёкшей лизе, потому что живая лиза означает, что вызов идёт прямо
	// сейчас, и второй звонок оплатил бы его второй раз.
	//
	// ⚠ ПРИЗНАК ПЕРЕХВАТА ПРИХОДИТ ИЗ СТОРА, А НЕ ВЫЧИСЛЯЕТСЯ ЗДЕСЬ. Считать «лиза истекла» на
	// этой стороне значит отвечать на вопрос, у которого нет одного ответа: из двух одновременных
	// повторов истёкшую лизу видят ОБА, и оба пошли бы платить. Перехват исключающий, и совершает
	// его та же транзакция, что решает про идемпотентность (см. designRunResumableSQL): она же
	// ротирует токен и продлевает лизу, а проигравшему возвращает Resumed = false — то есть ровно
	// тот ответ, что и живой лизе.
	if started.Idempotent && !started.Resumed {
		return &pb_admin.DraftDesignIdeaResponse{
			Run:    s.designRunResponse(ctx, run),
			Budget: s.designBudgetResponse(ctx, started.Budget),
		}, nil
	}
	if !run.ClaimToken.Valid || run.ClaimToken.String == "" {
		// Строка без захвата не принадлежит никому, и закрыть её нечем: CompleteRun сверяет
		// токен. Это не должно случаться — токен выдаёт StartRun, — поэтому и говорится вслух.
		slog.Default().ErrorContext(ctx, "draft design idea: the run carries no claim token",
			slog.Int("run_id", run.Id))
		return nil, status.Error(codes.Internal, "the idea draft could not be claimed")
	}

	attempt, err := s.repo.Design().StartAttempt(ctx, entity.DesignAttemptStart{
		RunId:      run.Id,
		ClaimToken: run.ClaimToken.String,
		Provider:   "openrouter",
	})
	if err != nil {
		return nil, designError(ctx, "failed to open the idea draft attempt", err, nil)
	}

	// ⚠ CompleteWithImages, А НЕ Complete: ЭТО И ЕСТЬ ВСЯ ПОЧИНКА. Пустой boardURLs законен и
	// вырождается в обычный текстовый запрос — доска без картинок это нормальное состояние, и
	// выбирать между двумя методами по этому признаку значило бы держать один промпт в двух местах.
	//
	// СЛУГ МОДЕЛИ ТОТ ЖЕ, ОБЩИЙ. Отдельная переменная окружения здесь не нужна и была бы вредна:
	// P-1 спеки владельца называет «OpenRouter, Sonnet», а defaultModel и есть
	// anthropic/claude-sonnet-5 — модель мультимодальная. Второй слуг был бы вторым именем,
	// которое однажды протухнет у поставщика молча.
	text, _, _, callErr := s.aiOps.CompleteWithImages(ctx, draftIdeaSystemPrompt, prompt, boardURLs, false, 0)
	if callErr == nil && strings.TrimSpace(text) == "" {
		callErr = errors.New("the model returned an empty draft")
	}
	if callErr != nil {
		s.designFailDraft(ctx, run, attempt.AttemptNo, callErr)
		return nil, s.designDraftCallError(ctx, cardID, callErr)
	}

	// ЦЕНА ПОПЫТКИ — ОЦЕНКА, И ЭТО НАЗВАНО ВСЛУХ. Чат-эндпоинт OpenRouter возвращает токены, но
	// не деньги, а `spent`, который никогда не растёт, — это дневной потолок, который никогда не
	// исчерпывается: черновики можно было бы жать бесконечно, оставаясь «в бюджете».
	if err := s.repo.Design().FinishAttempt(ctx, entity.DesignAttemptFinish{
		RunId: run.Id, AttemptNo: attempt.AttemptNo,
		State: entity.DesignAttemptDelivered, Price: est,
	}); err != nil {
		return nil, designError(ctx, "failed to close the idea draft attempt", err, nil)
	}
	done, err := s.repo.Design().CompleteRun(ctx, entity.DesignRunComplete{
		RunId:      run.Id,
		ClaimToken: run.ClaimToken.String,
		OutputText: sql.NullString{String: text, Valid: true},
	})
	if err != nil {
		return nil, designError(ctx, "failed to file the idea draft", err, nil)
	}
	budget, err := s.repo.Design().GetBudget(ctx)
	if err != nil {
		return nil, designError(ctx, "failed to read the design budget", err, nil)
	}
	return &pb_admin.DraftDesignIdeaResponse{
		Run:    s.designRunResponse(ctx, *done),
		Budget: s.designBudgetResponse(ctx, budget),
	}, nil
}

// designFailDraft закрывает попытку и прогон после провала вызова. ЛУЧШЕЕ УСИЛИЕ И ГРОМКОЕ:
// ошибка здесь не возвращается человеку — он должен увидеть ту, из-за которой всё началось, — но
// молчание оставило бы прогон висеть с зарезервированными деньгами.
func (s *Server) designFailDraft(ctx context.Context, run entity.DesignRun, attemptNo int, cause error) {
	if err := s.repo.Design().FinishAttempt(ctx, entity.DesignAttemptFinish{
		RunId: run.Id, AttemptNo: attemptNo,
		State:     entity.DesignAttemptFailed,
		ErrorCode: "provider_error",
	}); err != nil {
		slog.Default().ErrorContext(ctx, "draft design idea: cannot close the failed attempt",
			slog.Int("run_id", run.Id), slog.String("err", err.Error()))
	}
	if _, err := s.repo.Design().FailRun(ctx, entity.DesignRunFail{
		RunId:      run.Id,
		ClaimToken: run.ClaimToken.String,
		ErrorCode:  "provider_error",
		LastError:  cause.Error(),
		// НЕ RETRYABLE: ретраить некому. Воркер эту строку не заберёт по построению, а повтор —
		// это новый клик человека с новым client_request_id.
		Retryable: false,
	}); err != nil {
		slog.Default().ErrorContext(ctx, "draft design idea: cannot close the failed run",
			slog.Int("run_id", run.Id), slog.String("err", err.Error()))
	}
}

// designDraftCallError переводит провал вызова модели в отказ, который человек может починить.
func (s *Server) designDraftCallError(ctx context.Context, cardID int, err error) error {
	if errors.Is(err, openrouter.ErrNotConfigured) {
		return aiRefusal(aiReasonNotConfigured, draftIdeaNotConfiguredMsg, nil)
	}
	slog.Default().ErrorContext(ctx, "draft design idea: the model call failed",
		slog.Int("tech_card_id", cardID), slog.String("model", s.aiOps.Model()),
		slog.String("base_url", s.aiOps.BaseURL()), slog.String("err", err.Error()))
	if errors.Is(err, openrouter.ErrModelUnavailable) {
		return aiModelRefusal(draftIdeaModelUnavailableMsg, s.aiOps.Model())
	}
	return status.Errorf(codes.Unavailable, "drafting the idea failed: %v", err)
}

// ─────────────────────────── W-15: СБОРКА ВХОДОВ ───────────────────────────

// designInputSources — ВСЁ, ИЗ ЧЕГО СОБИРАЕТСЯ СНИМОК ВХОДОВ, и ничего сверх.
type designInputSources struct {
	Kind   string
	Card   *entity.TechCard
	Refs   []entity.DesignReference
	Bench  []entity.DesignBenchSlot
	Params *pb_common.DesignRunParams
}

// designAssembleInputs — ГАРАНТИЯ W-15, И ОНА ЖИВЁТ ЗДЕСЬ, А НЕ НА ЭКРАНЕ.
//
// ВЛАДЕЛЕЦ ПРОЦИТИРОВАЛ СТРОКУ ПРОТОТИПА ДОСЛОВНО: «the mood, not the prompt: nothing here is
// sent to generation». Экран это обещает; обещание экрана обходится любым вторым клиентом,
// повтором запроса и просто вкладкой, открытой по ссылке. Гарантия — вот эта функция.
//
// ПРАВИЛО ОДНОЙ ФРАЗОЙ: КАРТИНКИ ВХОДЯТ ТОЛЬКО ИЗ ДВУХ ИМЕНОВАННЫХ ИСТОЧНИКОВ — строк
// `design_reference` (то, что человек ЯВНО перенёс в INPUT — REFERENCES) и
// `params.extra_input_media_ids` (то, что он ЯВНО назвал вторым способом). Плюс плиты верстака,
// которые не картинки доски, а собственные чертежи карточки. `Card.Media` в сборке картинок не
// читается ВООБЩЕ — ни для фильтра, ни для проверки.
//
// ⚠ ФИЛЬТР «ВЫБРОСИТЬ ТО, ЧТО ЛЕЖИТ НА ДОСКЕ» БЫЛ БЫ ОШИБКОЙ, И ЭТО НЕ ТОНКОСТЬ. Владелец
// требует ровно обратного (U-5 §5, A2): щелчок по плитке доски ЗАВОДИТ запись референса с тем же
// media_id — это и есть «явный перенос». Медиа, лежащее и на доске, и в референсах, уходит в
// модель ЗАКОННО, потому что человек его туда положил. Фильтровать по членству в доске значило бы
// молча ломать главный жест полосы; правило про ИСТОЧНИК, а не про картинку.
//
// `mood` У КАРТИНОЧНЫХ ПРОГОНОВ ОСТАЁТСЯ ПУСТЫМ. Не потому что доска бесполезна, а потому что
// снимок — это то, что ушло в модель: заполненный `mood` дал бы воркеру, который его прочитает,
// media_id доски прямо в руки, и W-15 держалась бы на его дисциплине. Единственный прогон, у
// которого `mood` заполнен, — текстовый: он читает СЛОВА доски и не получает ни одной картинки
// (см. DraftDesignIdea).
func designAssembleInputs(src designInputSources) (*pb_common.DesignInputSnapshot, error) {
	out := &pb_common.DesignInputSnapshot{
		Views:  src.Params.GetViews(),
		Layout: src.Params.GetLayout(),
	}
	if src.Card != nil {
		out.Fit = src.Card.Fit.String
		// ОПИСАНИЕ ИЗДЕЛИЯ (W-3) — «пишем общий коммент», который уходит в КАЖДЫЙ прогон.
		// Замораживается КОПИЕЙ, а не джойном: правка описания завтра не имеет права переписать
		// то, что сказали модели вчера, — иначе история перестаёт быть свидетельством.
		//
		// ⚠ ЭТО НЕ MoodNote. Та про ДОСКУ и её читает ровно один текстовый прогон (W-15); эта про
		// ИЗДЕЛИЕ и её читает каждая генерация. Подставленная сюда записка доски отправила бы в
		// модель ровно те слова, которые W-15 запрещает.
		out.GarmentNote = src.Card.GarmentDescription.String
	}

	// ─── refs: design_reference, затем явно названные extra_input_media_ids ───
	//
	// ВЫНОСКИ ПРИКАЛЫВАЮТСЯ К РЕФЕРЕНСУ, А НЕ ПРИХОДЯТ С ДОСКИ, и разница здесь не словесная.
	// Ключом служит media_id УЖЕ ОТОБРАННОГО референса: доска в этот отбор не входит ни при каком
	// входе (см. шапку), поэтому картинка попадает сюда только пройдя через явный перенос
	// человеком — а раз картинка законна, законна и разметка, которую он на ней нарисовал.
	// Card.Media при этом по-прежнему не читается ВООБЩЕ: карта строится по Card.Callouts.
	callouts := designCalloutsByMedia(src.Card)
	seen := make(map[int]struct{}, len(src.Refs))
	for _, r := range src.Refs {
		if r.MediaId <= 0 {
			continue
		}
		if _, dup := seen[r.MediaId]; dup {
			continue
		}
		seen[r.MediaId] = struct{}{}
		out.Refs = append(out.Refs, &pb_common.DesignInputRef{
			MediaId: int32(r.MediaId),
			Role:    r.Role,
			// ЗАПИСКА ЧЕЛОВЕКА ПРО ЭТУ КАРТИНКУ (W-3): «только воротник», «ткань, а не крой».
			// Восемь референсов без записок называют восемь сторон и ни одного намерения.
			Note:     r.Note.String,
			Callouts: callouts[r.MediaId],
		})
	}
	for _, id := range src.Params.GetExtraInputMediaIds() {
		if id <= 0 {
			continue
		}
		if _, dup := seen[int(id)]; dup {
			continue
		}
		seen[int(id)] = struct{}{}
		// РОЛЬ ПУСТА, И ЭТО ПРАВДА: дополнительный вход назван человеком прямо в запросе и о
		// стороне изделия ничего не говорит. Придуманная здесь роль соврала бы модели.
		//
		// ЗАПИСКИ ТОЖЕ НЕТ — её негде взять: записка живёт на строке design_reference, а этого
		// входа в референсах нет. РАЗМЕТКА ЖЕ ЕСТЬ, если она есть: выноска нарисована человеком
		// на этой самой картинке, и молчать о ней значило бы послать в модель меньше, чем он
		// нарисовал, ровно по тому же доводу, по которому геометрия перестала теряться у доски.
		out.Refs = append(out.Refs, &pb_common.DesignInputRef{
			MediaId:  id,
			Callouts: callouts[int(id)],
		})
	}
	if len(out.Refs) > designMaxInputRefs {
		return nil, status.Errorf(codes.InvalidArgument,
			"a run may carry %d reference images; this one has %d", designMaxInputRefs, len(out.Refs))
	}

	// ─── slots: плиты верстака ───
	//
	// ⚠ ПОТОЛОК СЧИТАЕТ ПЛИТЫ, А НЕ ЗАПИСИ, И РАЗЛИЧИЕ ПОЯВИЛОСЬ ВМЕСТЕ С ПУСТЫМИ ДЕТАЛЯМИ.
	// designInputSlots отдаёт теперь два разных рода записей: плиты (несут media_id, уезжают
	// поставщику ссылкой и подписью) и записи-имена для просимых пустых деталей (не несут ничего,
	// кроме slot_id и названия). Считать их одним числом значило бы отказывать в ЗАКОННОМ прогоне:
	// карточка с четырьмя сторонами и четырьмя деталями даёт восемь плит, и первая же галка на
	// пустую деталь перевалила бы за потолок — то есть новая фича закрыла бы старый сценарий.
	// Довод потолка («снимок обязан помещаться в строку и в глаз») от разделения не страдает: обе
	// половины ограничены, а запись-имя весит десятки байт против плиты с хешем и адресом.
	out.Slots = designInputSlots(src)
	plates, asked := 0, 0
	for _, s := range out.Slots {
		if s.GetMediaId() > 0 {
			plates++
			continue
		}
		asked++
	}
	if plates > designMaxInputSlots {
		return nil, status.Errorf(codes.InvalidArgument,
			"a run may carry %d bench plates; this one has %d", designMaxInputSlots, plates)
	}
	if asked > designMaxInputSlots {
		return nil, status.Errorf(codes.InvalidArgument,
			"a run may ask for %d empty detail slots; this one asks for %d", designMaxInputSlots, asked)
	}
	return out, nil
}

// designInputSlots — КАКИЕ ПЛИТЫ ВЕРСТАКА ЕДУТ В ЭТОТ ПРОГОН.
//
// Ось верстака ДВЕ (вид × род, 0349), и род выбирается по роду прогона: рендер строится из
// ФЛЭТОВ, 3D — из РЕНДЕРОВ. Одноосное чтение брало бы рендер фронта вместо флэта фронта ровно
// там, где оба есть, — то есть на любой карточке, дошедшей до 3D.
//
// ВЫБОРКА `fix` СУЖАЕТ СПИСОК. `fix_targets` + `fix_slot_ids` — ОДНА выборка, а не два режима:
// «выбрать всё в FLAT SLOTS» (W-10) называет три стороны и манжету одним прогоном.
func designInputSlots(src designInputSources) []*pb_common.DesignInputSlot {
	slots, _ := designSelectBench(src)
	return append(slots, designNamedEmptyDetailSlots(src, slots)...)
}

// designNamedEmptyDetailSlots — ЗАПИСЬ БЕЗ КАРТИНКИ ДЛЯ ДЕТАЛИ, КОТОРУЮ ПРОСЯТ НАРИСОВАТЬ.
//
// ЧТО БЫЛО СЛОМАНО, И ПОЧЕМУ ЭТО НЕЛЬЗЯ БЫЛО ЗАМЕТИТЬ. `params.detail_slot_ids` резолвится в ИМЯ
// («collar», «patch pocket») ровно одним читателем — designgen/snapshot.go, requestedDetailNames, —
// и читает он `inputs.slots[*].slot_id`. А отбор плит (designSelectBench) выбрасывает слот, у
// которого нет картинки. Пустой слот детали — это ГЛАВНЫЙ случай фичи, а не краевой: галку ставят
// именно затем, чтобы воротник НАРИСОВАЛИ, то есть в слоте картинки ещё нет. Слот в снимок не
// попадал, имя не резолвилось, и промпт говорил «draw these details: detail» — ровно та строка,
// от которой уходили.
//
// И ОБРАТНОЕ БЫЛО ВЕРНО ПО ПОСТРОЕНИЮ: слот, попавший в отбор, несёт картинку, а значит уже получил
// подпись `slotCaption` вида «current state of the garment — detail view (collar)». То есть в
// единственном случае, когда имя резолвилось, оно было сказано и без этой волны.
//
// ПОЧЕМУ СНИМОК, А НЕ ВТОРОЕ ПОЛЕ РЯДОМ С id. Заморозить имена в `params` рядом с адресами —
// рабочая альтернатива, и она отвергнута сознательно: `DesignInputSlot` УЖЕ несёт пару
// (`slot_id`, `detail_name`), и единственный читатель уже ходит именно в неё. Второе написание
// того же факта дало бы двух писателей и двух читателей одной пары, а расходятся такие пары
// молча — здесь это уже случалось (`fix_target` против `fix_targets`, две трактовки одного поля).
// Плюс параметры принадлежат ЧЕЛОВЕКУ («что просят»), а имя детали — факт сервера; провенанс
// живёт в снимке (см. designEffectiveParams).
//
// ПОДПИСИ КАРТИНОК НЕ СДВИГАЮТСЯ. referenceList складывает картинки через `add`, который выходит
// на `id <= 0` первой же строкой, — запись без media_id не даёт ни url, ни строки «- image k»,
// поэтому нумерация подписей остаётся ровно такой, какой была. Читающий join (joinDesignRunInputMedia)
// на нулевом id тоже молчит: `fill(0)` отдаёт (nil, false), то есть «картинки нет» и «не удалена».
//
// ЧТО СЮДА НЕ ПОПАДАЕТ. Адрес, которого на верстаке нет вовсе: имени для него взять негде, и
// выдуманного тут не будет. Для свежего прогона такого адреса и не бывает — дверь отвергает чужой
// (designRefuseForeignDetailSlots); у рерана имя приезжает из снимка родителя целиком.
func designNamedEmptyDetailSlots(src designInputSources, already []*pb_common.DesignInputSlot) []*pb_common.DesignInputSlot {
	ids := src.Params.GetDetailSlotIds()
	if len(ids) == 0 {
		return nil
	}
	covered := make(map[int32]struct{}, len(already))
	for _, s := range already {
		if s.GetSlotId() > 0 {
			covered[s.GetSlotId()] = struct{}{}
		}
	}
	named := make(map[int]entity.DesignBenchSlot, len(src.Bench))
	for _, slot := range src.Bench {
		if slot.ViewKey == entity.DesignViewDetail {
			named[slot.Id] = slot
		}
	}
	out := make([]*pb_common.DesignInputSlot, 0, len(ids))
	for _, id := range ids {
		if _, dup := covered[id]; dup {
			continue
		}
		slot, ok := named[int(id)]
		if !ok {
			continue
		}
		covered[id] = struct{}{}
		// MediaId, ContentHash и LayerRev ОСТАЮТСЯ ПУСТЫМИ, И ЭТО ПРАВДА, А НЕ ПРОПУСК: в слоте
		// нет картинки, сравнивать «вход протух» не с чем, а нулевой media_id читается ровно как
		// «эта деталь была ПРОСИМА, а не ПОКАЗАНА».
		out = append(out, &pb_common.DesignInputSlot{
			ViewKey:    entity.DesignViewDetail,
			SlotId:     id,
			DetailName: slot.DetailName.String,
		})
	}
	return out
}

// designSelectBench — САМ ОТБОР, и он ЕДИНСТВЕННЫЙ. Отдаёт две проекции одного прохода: слоты для
// снимка и id КАРТИНОК тех же слотов для `params.threed.source_picture_ids`.
//
// ⚠ ДВЕ ПРОЕКЦИИ ИЗ ОДНОГО ЦИКЛА, А НЕ ДВА ОБХОДА ВЕРСТАКА. Второй обход был бы вторым мнением о
// том, какие плиты взял прогон, и разошлись бы они ровно на выборке `fix` — то есть там, где
// человек сузил прогон и где ошибка дороже всего. Звать эту функцию дважды за один запрос при этом
// БЕЗОПАСНО: это один и тот же ответ на один и тот же вопрос, ценой прохода по восьми слотам.
func designSelectBench(src designInputSources) ([]*pb_common.DesignInputSlot, []int32) {
	want := entity.DesignPictureKindFlat
	if src.Kind == entity.DesignRunKindThreed {
		want = entity.DesignPictureKindRender
	}
	// ОДНО ПРАВИЛО, ДОСЛОВНО КОНТРАКТНОЕ: список, когда он непуст, иначе скаляр. Здесь было
	// ОБЪЕДИНЕНИЕ, и это второе правило рядом с первым — промпт (designgen/snapshot.go) читает
	// ровно падение. Расхождение видно только на противоречивом входе, который дверь теперь
	// отвергает (designEffectiveParams), но полагаться на это нельзя: снимки старых прогонов
	// заморожены и противоречие в них уже есть, а читать их обязаны одинаково оба.
	targets := map[string]struct{}{}
	if list := src.Params.GetFixTargets(); len(list) > 0 {
		for _, v := range list {
			targets[v] = struct{}{}
		}
	} else if t := src.Params.GetFixTarget(); t != "" {
		targets[t] = struct{}{}
	}
	slotIDs := map[int]struct{}{}
	for _, id := range src.Params.GetFixSlotIds() {
		slotIDs[int(id)] = struct{}{}
	}
	selective := len(targets) > 0 || len(slotIDs) > 0

	out := make([]*pb_common.DesignInputSlot, 0, len(src.Bench))
	plates := make([]int32, 0, len(src.Bench))
	for _, slot := range src.Bench {
		if entity.DesignKindOrFlat(slot.Kind) != want {
			continue
		}
		if slot.Picture == nil || slot.Picture.MediaId <= 0 {
			continue
		}
		if selective {
			_, byView := targets[slot.ViewKey]
			_, bySlot := slotIDs[slot.Id]
			if !byView && !bySlot {
				continue
			}
		}
		in := &pb_common.DesignInputSlot{
			ViewKey:    slot.ViewKey,
			DetailName: slot.DetailName.String,
			MediaId:    int32(slot.Picture.MediaId),
			LayerRev:   int32(slot.Picture.LayerRev),
		}
		if !entity.IsDesignSilhouetteView(slot.ViewKey) {
			// slot_id ТОЛЬКО У ДЕТАЛЕЙ: вид сам называет каждую из четырёх сторон, а две
			// детали по виду не различить — и без ключа сравнение «вход протух» стало бы
			// невычислимым НАВСЕГДА, потому что снимок замерзает и не чинится задним числом.
			in.SlotId = int32(slot.Id)
		}
		if slot.Picture.Media != nil && slot.Picture.Media.ContentHash.Valid {
			in.ContentHash = slot.Picture.Media.ContentHash.String
		}
		out = append(out, in)
		if slot.Picture.Id > 0 {
			// id КАРТИНКИ, а не медиа: `source_picture_ids` объявлено ссылкой на design_picture(id),
			// и панель прогона по нему поднимает саму плиту — её род, её rrev и её принадлежность
			// прогону. Медиа этого не знает: один и тот же файл может лежать под несколькими
			// картинками полосы.
			plates = append(plates, int32(slot.Picture.Id))
		}
	}
	return out, plates
}

// designRunInputs — снимок ЭТОГО прогона плюс фит, под которым он уходит.
//
// РЕРАН ПЕРЕПИСЫВАЕТ ВХОДЫ СО СТРОКИ РОДИТЕЛЯ ЦЕЛИКОМ, и это ровно то, ради чего реран отдан
// серверу: повторить прогон значит послать модели ТО ЖЕ САМОЕ, а «то же самое» знает только
// история. Из сегодняшнего состояния карточки берётся ноль полей.
//
// ДВА ПОЛЯ ВСЁ-ТАКИ ОБНОВЛЯЮТСЯ, И ОБА — НЕ ПРОВЕНАНС. `views`/`layout` — это КОПИЯ параметров
// внутри снимка (контракт: «the fingerprint that draws the current / earlier divider»), и снимок,
// чей отпечаток не сходится с собственными параметрами строки, врал бы дивайдеру истории.
func (s *Server) designRunInputs(ctx context.Context, src designInputSources, parent *entity.DesignRun) (*pb_common.DesignInputSnapshot, string, error) {
	if parent == nil {
		snap, err := designAssembleInputs(src)
		if err != nil {
			return nil, "", err
		}
		fit := ""
		if src.Card != nil {
			fit = src.Card.Fit.String
		}
		return snap, fit, nil
	}
	snap := &pb_common.DesignInputSnapshot{}
	if len(parent.Inputs) > 0 {
		if err := designUnmarshalJSON(parent.Inputs, snap); err != nil {
			slog.Default().WarnContext(ctx, "design rerun: the parent's input snapshot did not parse",
				slog.Int("run_id", parent.Id), slog.String("err", err.Error()))
			return nil, "", status.Errorf(codes.FailedPrecondition,
				"run %d cannot be rerun: its stored inputs do not parse", parent.Id)
		}
	}
	snap.Views = src.Params.GetViews()
	snap.Layout = src.Params.GetLayout()
	// ─── ТРЕТЬЕ ПОЛЕ, КОТОРОЕ ДОПОЛНЯЕТСЯ, И ОНО ИЗ ТОЙ ЖЕ КАТЕГОРИИ, ЧТО ДВА ВЫШЕ ───
	//
	// Реран вправе изменить просьбу: контракт говорит «`ask` and `params` still apply ON TOP», то
	// есть клиент может поставить галку на ДРУГУЮ деталь. Снимок родителя про неё не знает ничего —
	// и без этой строки повторение с новой галкой снова говорило бы «draw these details: detail»,
	// то есть MAJOR-1 был бы починен ровно на половине входов.
	//
	// ⚠ ЭТО НЕ ПРОТИВОРЕЧИТ ПРАВИЛУ «ВХОДЫ ПЕРЕПИСЫВАЮТСЯ СО СТРОКИ РОДИТЕЛЯ». Правило про
	// ПРОВЕНАНС — про то, какие КАРТИНКИ ушли в модель; ни одной картинки здесь не добавляется
	// (записи идут без media_id). Добавляется КОПИЯ ПАРАМЕТРОВ этого прогона, ровно как `views` и
	// `layout` строкой выше, и по тому же доводу: снимок, чей отпечаток не сходится с собственными
	// параметрами строки, врёт.
	//
	// ⚠ И ИМЕНА РОДИТЕЛЯ ПРИ ЭТОМ НЕПРИКОСНОВЕННЫ: covered-фильтр внутри пропускает только адреса,
	// которых в снимке ЕЩЁ НЕТ. Деталь, переименованная после родителя, останется в этом реране
	// под СТАРЫМ именем — тем, с которым прогон был отправлен, — а сегодняшнее имя получит только
	// та, которую только что попросили впервые.
	snap.Slots = append(snap.GetSlots(), designNamedEmptyDetailSlots(src, snap.GetSlots())...)
	// ФИТ БЕРЁТСЯ ИЗ СНИМКА РОДИТЕЛЯ, А НЕ С КАРТОЧКИ. Модель получит те же слова, что получила в
	// прошлый раз, значит и `fit_at_launch` строки обязан говорить о том же: иначе плита
	// приедет со штампом сегодняшнего фита, а нарисована будет по вчерашнему, и минт сверил бы
	// её не с тем.
	return snap, snap.GetFit(), nil
}

// designRunPlates — ПЛИТЫ ЭТОГО ПРОГОНА, id картинок.
//
// Свежий прогон называет то, что отобрал он сам; реран — то, что назвал родитель, потому что входы
// у него родительские и сегодняшний верстак к делу не относится.
//
// ⚠ ДЛЯ СВЕЖЕГО ПРОГОНА ЭТО ВТОРОЙ ВЫЗОВ ТОЙ ЖЕ designSelectBench, что уже сделала сборка входов —
// и это НАМЕРЕННО дешевле альтернатив. Протащить плиты через designAssembleInputs значило бы
// сменить её подпись, а её зовут пробы соседних задач в этом же дереве; посчитать плиты вторым,
// собственным обходом верстака значило бы завести второе правило отбора. Один и тот же вызов
// одной и той же функции разойтись не может ни при каком входе, а стоит он прохода по восьми
// слотам.
func designRunPlates(src designInputSources, parent *entity.DesignRun) []int32 {
	if parent != nil {
		return designParentPlates(parent)
	}
	_, plates := designSelectBench(src)
	return plates
}

// ─────────────────────────── доска: только слова ───────────────────────────

// designMoodSnapshot — доска в том виде, в каком её ЧИТАЕТ ТЕКСТОВЫЙ ПРОГОН: записка (после V-16 —
// `concept`, с легаси-`mood_note` вторым абзацем) плюс выноски, приколотые на картинки доски.
//
// ⚠ ЗДЕСЬ ЕДИНСТВЕННОЕ МЕСТО ВО ВСЁМ ФАЙЛЕ, ГДЕ ЧИТАЕТСЯ Card.Media, и зовётся оно ровно из одного
// глагола — DraftDesignIdea. Card.Callouts читает ещё designCalloutsByMedia, и она доски не знает:
// она отдаёт КАРТУ по media_id, а какие ключи из неё спросить, решает уже отобранный список
// референсов. Всё остальное собирается designAssembleInputs, куда доска не приходит ни при каком
// входе.
func designMoodSnapshot(card *entity.TechCard) *pb_common.DesignMoodSnapshot {
	if card == nil {
		return nil
	}
	board := make(map[int]struct{}, len(card.Media))
	for _, m := range card.Media {
		if m.Category == entity.TechCardMediaCategoryMoodboard && m.MediaId > 0 {
			board[m.MediaId] = struct{}{}
		}
	}
	// V-16: THE BOARD'S NOTE IS THE CONCEPT. The owner: «CONCEPT & CONSTRUCTION DESCRIPTION это и
	// есть SHARED NOTE в MOODBOARD» — the client now edits exactly one text, `concept`, in the
	// moodboard block. The legacy `mood_note` column is no longer editable anywhere, but cards
	// written before the merge still carry it, and a note the model reads must be a note the
	// history shows — so the snapshot composes BOTH, concept first, and stores the composition.
	// The columns themselves are NOT merged: `concept` sits inside the DESIGN digest projection
	// and `mood_note` deliberately does not (see entity.TechCard), so a column merge would flip
	// every signed DESIGN section at once.
	note := strings.TrimSpace(card.Concept.String)
	if legacy := strings.TrimSpace(card.MoodNote.String); legacy != "" && legacy != note {
		if note == "" {
			note = legacy
		} else {
			note = note + "\n" + legacy
		}
	}
	out := &pb_common.DesignMoodSnapshot{Note: note}
	for _, c := range card.Callouts {
		if !c.MediaId.Valid {
			continue
		}
		if _, ok := board[int(c.MediaId.Int32)]; !ok {
			// Выноска на техническом эскизе — не доска. Пустить её сюда значило бы, что
			// «мудборд» на экране и «мудборд» в снимке — два разных множества.
			continue
		}
		mc := designFrozenCallout(c)
		if mc == nil {
			continue
		}
		out.Callouts = append(out.Callouts, mc)
	}
	if out.GetNote() == "" && len(out.GetCallouts()) == 0 {
		return nil
	}
	return out
}

// designBoardMediaIDs — КАРТИНКИ ДОСКИ, В ПОРЯДКЕ КАРТОЧКИ, без повторов.
//
// ⚠ ЭТО ВТОРОЙ ЧИТАТЕЛЬ Card.Media, И ОБА ЖИВУТ РЯДОМ НАМЕРЕННО. Первый — designMoodSnapshot, он
// строит МНОЖЕСТВО доски, чтобы отсеять выноски чужих картинок; этот отдаёт СПИСОК, потому что
// порядок уезжает на провод и должен быть воспроизводим: один и тот же мудборд обязан давать один
// и тот же запрос, иначе повтор с тем же client_request_id перестал бы быть тем же запросом.
// Условие членства («категория moodboard») обязано совпадать у обоих — разойдясь, они дали бы
// картинку, чья выноска прочитана как доска, но сама картинка не послана, или наоборот.
//
// ФИЛЬТР ПО КАТЕГОРИИ, А НЕ ПО ОТСУТСТВИЮ ССЫЛОК: технический эскиз карточки лежит в том же
// Card.Media и доской не является — послать его значило бы читать чертёж как вдохновение.
func designBoardMediaIDs(card *entity.TechCard) []int {
	if card == nil {
		return nil
	}
	seen := make(map[int]struct{}, len(card.Media))
	out := make([]int, 0, len(card.Media))
	for _, m := range card.Media {
		if m.Category != entity.TechCardMediaCategoryMoodboard || m.MediaId <= 0 {
			continue
		}
		if _, dup := seen[m.MediaId]; dup {
			continue
		}
		seen[m.MediaId] = struct{}{}
		out = append(out, m.MediaId)
	}
	return out
}

// designBoardPictureURLs — АДРЕСА КАРТИНОК ДОСКИ, в порядке пришедших id, ПЛЮС САМИ id выживших.
//
// ⚠ ПРОПАВШАЯ СТРОКА МЕДИА ПРОПУСКАЕТСЯ, А НЕ РОНЯЕТ ЧЕРНОВИК. Тот же довод, что в
// designgen.buildJob: id, которого нет у нас, провайдер тоже не скачает, а отказ всей кнопки
// из-за одной удалённой плитки выбросил бы доску, чьи остальные картинки целы. Пустой url
// пропускается по той же причине: CompleteWithImages отказал бы на нём поимённо уже после
// StartAttempt, то есть внутри оплаченного круга.
//
// ⚠ ВТОРОЕ ВОЗВРАЩАЕМОЕ — НЕ УДОБСТВО, А ПОЛОВИНА ПРИВЯЗКИ (V-19). Промпт нумерует выноски
// «picture N» по порядку ПРИЛОЖЕННЫХ картинок, и обе половины — url на проводе и номер в словах —
// обязаны читаться С ОДНОГО списка, собранного одним циклом. Нумеровать по boardIDs до резолюции
// значило бы, что одна нечитаемая строка медиа сдвигает каждый номер после себя на чужую картинку —
// ровно тот дефект, ради которого designgen.composePrompt считает подписи по `attached`, а не по
// снимку.
func (s *Server) designBoardPictureURLs(ctx context.Context, ids []int) ([]string, []int, error) {
	if len(ids) == 0 {
		return nil, nil, nil
	}
	byID, err := s.repo.Media().GetMediaByIds(ctx, ids)
	if err != nil {
		return nil, nil, err
	}
	urls := make([]string, 0, len(ids))
	attached := make([]int, 0, len(ids))
	for _, id := range ids {
		m, ok := byID[id]
		if !ok {
			continue
		}
		if u := strings.TrimSpace(m.FullSizeMediaURL); u != "" {
			urls = append(urls, u)
			attached = append(attached, id)
		}
	}
	return urls, attached, nil
}

// designFrozenCallout — ОДНА ВЫНОСКА В ЗАМОРОЖЕННОМ ВИДЕ: слова плюс геометрия. nil, если слов
// нет — снимок хранит то, ЧТО ПРОЧИТАЛИ, и безымянный маркер прочитать нечем.
//
// ⚠ ОДНА ФУНКЦИЯ НА ДВА ЧИТАТЕЛЯ (доска и референсы), И ЭТО НЕСУЩЕЕ. Контракт говорит прямо, что
// DesignMoodCallout — ЕДИНСТВЕННАЯ форма замороженной выноски и что она переиспользуется дословно
// для референсов; две копии сборки разошлись бы в первый же раз, когда одна из них отрастила поле.
//
// ГЕОМЕТРИЯ СОБИРАЕТСЯ ТЕМ ЖЕ КОДОМ, ЧТО И У ЛИСТА, И НИКАКИМ ДРУГИМ. Самодельный JSON здесь уже
// стоил репозиторию молчаливой потери всей разметки: вид и цвет на проводе — ЭНУМЫ, и объект с
// хранимыми строками разбирается без ошибки в ПУСТОЕ сообщение. Единственный способ не разойтись —
// пройти теми же картами.
func designFrozenCallout(c entity.TechCardCallout) *pb_common.DesignMoodCallout {
	if !c.MediaId.Valid {
		return nil
	}
	text := entity.TechCardCalloutPrintedLine(c)
	if strings.TrimSpace(text) == "" {
		return nil
	}
	mc := &pb_common.DesignMoodCallout{MediaId: c.MediaId.Int32, Text: text}
	if raw, err := dto.TechCardCalloutAnnotationJSON(c); err == nil {
		ann := &pb_common.TechCardAnnotation{}
		if err := designUnmarshalJSON(raw, ann); err == nil {
			mc.Annotation = ann
		}
	}
	return mc
}

// designCalloutsByMedia — РАЗМЕТКА КАРТОЧКИ, РАЗЛОЖЕННАЯ ПО КАРТИНКАМ, для сборки входов.
//
// ⚠ ЭТО КАРТА, А НЕ ОТБОР, и разница здесь и есть граница W-15. Функция не знает ни доски, ни
// референсов и ничего не решает о том, какая картинка законна: она отвечает на вопрос «что
// нарисовано на медиа N», а спрашивают её ТОЛЬКО про те медиа, которые уже прошли отбор
// designAssembleInputs. Фильтр «доска» здесь был бы вторым, расходящимся мнением о том же
// правиле — а правило про ИСТОЧНИК картинки, и живёт оно ровно в одном месте.
func designCalloutsByMedia(card *entity.TechCard) map[int][]*pb_common.DesignMoodCallout {
	if card == nil || len(card.Callouts) == 0 {
		return nil
	}
	out := make(map[int][]*pb_common.DesignMoodCallout, len(card.Callouts))
	for _, c := range card.Callouts {
		mc := designFrozenCallout(c)
		if mc == nil {
			continue
		}
		id := int(mc.GetMediaId())
		out[id] = append(out[id], mc)
	}
	return out
}

// designDraftIdeaPrompt — СЛОВЕСНАЯ ЧАСТЬ ЗАПРОСА. Картинки едут ОТДЕЛЬНЫМИ content-parts мимо
// этой функции (см. DraftDesignIdea); здесь — их НОМЕРА и всё, что к ним приколото.
//
// ПРИВЯЗКА — ВЕСЬ СМЫСЛ ЭТОЙ ФУНКЦИИ (V-19, владелец дословно: «важно что модель принимала
// картинку и знала какой пин из колаутов как размечен а не что он просто есть (к какой картинке и
// какой части картинки)»). До этого выноски уезжали плоским списком текстов: модель знала, ЧТО
// написано, и не знала, ГДЕ. Теперь каждая строка называет свою картинку номером и место на ней
// в долях кадра — тем же приёмом, каким designgen.composePrompt нумерует референсы («image k:»),
// потому что два способа привязывать слова к картинкам в одном репозитории уже расходились.
//
// ⚠ `attachedIDs` — КАРТИНКИ, ФАКТИЧЕСКИ УХОДЯЩИЕ НА ПРОВОД, в порядке отправки: выжившие после
// резолюции медиа, а не список желаний доски. Номер считается ПО НИМ, потому что в глазах модели
// «picture 2» — это второй content-part; нумерация по снимку сдвигалась бы на каждой пропавшей
// строке медиа. Выноска картинки, которая не уехала, выбрасывается ВМЕСТЕ с картинкой — слова о
// изображении, которого модель не видит, это инструкция ни о чём (довод composePrompt, дословно).
//
// ⚠ media_id ПО-ПРЕЖНЕМУ НЕ ПИШЕТСЯ: это наш внутренний ключ, модели он не сообщает ничего.
// Номер здесь — порядковый номер content-части, и только он.
func designDraftIdeaPrompt(card *entity.TechCard, mood *pb_common.DesignMoodSnapshot, attachedIDs []int) string {
	var b strings.Builder
	if card != nil {
		if v := strings.TrimSpace(card.Name); v != "" {
			b.WriteString("Garment: " + v + "\n")
		}
		if v := strings.TrimSpace(card.Fit.String); v != "" {
			b.WriteString("Fit: " + v + "\n")
		}
	}
	if note := strings.TrimSpace(mood.GetNote()); note != "" {
		b.WriteString("\nConcept & construction description — the designer's own words, build on them:\n" + note + "\n")
	}
	if len(attachedIDs) > 0 {
		b.WriteString("\nThe moodboard pictures are attached in order: «picture 1» is the first attached image, «picture 2» the second, and so on.\n")
	}
	pictureAt := make(map[int32]int, len(attachedIDs))
	for i, id := range attachedIDs {
		pictureAt[int32(id)] = i + 1
	}
	var lines []string
	for _, c := range mood.GetCallouts() {
		n, ok := pictureAt[c.GetMediaId()]
		if !ok {
			continue // картинка не уехала — её слова едут вместе с ней, то есть никуда
		}
		lines = append(lines,
			"- picture "+strconv.Itoa(n)+designCalloutSpot(c.GetAnnotation())+": "+designOneLine(c.GetText()))
	}
	if len(lines) > 0 {
		b.WriteString("\nNotes pinned on the pictures — each names its picture and the spot it marks:\n")
		b.WriteString(strings.Join(lines, "\n") + "\n")
	}
	return strings.TrimSpace(b.String())
}

// designOneLine сплющивает человеческий текст в одну строку — тот же приём и тот же довод, что
// designgen.oneLine: строки выносок нумерованы («- picture 2 …»), и перевод строки внутри текста
// вписал бы в промпт СВОЮ строку, которую модель прочла бы как подпись соседней картинки.
func designOneLine(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

// designCalloutSpot — МЕСТО УКАЗАНИЯ НА КАДРЕ, словами для модели: вид отметки и точка в
// процентах кадра. Пустая строка — законный ответ (легаси-выноска без геометрии): строка без
// места хуже строки без выноски не становится.
//
// ТОЧКА БЕРЁТСЯ У ЯКОРЕЙ ФИГУРЫ, А НЕ У ПЛАШКИ, когда якоря есть: у линии/дуги плашка с текстом
// намеренно отводится ОТ отмеченного места (чтобы не лечь на саму линию — см. mood-callouts.add),
// то есть отвечает на вопрос «где подпись», а не «где отметка». У пина якорей нет — его
// label_x/label_y И ЕСТЬ точка.
func designCalloutSpot(ann *pb_common.TechCardAnnotation) string {
	if ann == nil {
		return ""
	}
	dec := func(d *pb_decimal.Decimal) (float64, bool) {
		if d == nil {
			return 0, false
		}
		v, err := strconv.ParseFloat(d.GetValue(), 64)
		if err != nil {
			return 0, false
		}
		return v, true
	}
	var x, y float64
	var got bool
	if pts := ann.GetPoints(); len(pts) > 0 {
		var sx, sy float64
		n := 0
		for _, p := range pts {
			px, okx := dec(p.GetX())
			py, oky := dec(p.GetY())
			if !okx || !oky {
				continue
			}
			sx, sy, n = sx+px, sy+py, n+1
		}
		if n > 0 {
			x, y, got = sx/float64(n), sy/float64(n), true
		}
	}
	if !got {
		px, okx := dec(ann.GetLabelX())
		py, oky := dec(ann.GetLabelY())
		if !okx || !oky {
			return ""
		}
		x, y, got = px, py, true
	}
	word := "mark"
	switch ann.GetKind() {
	case pb_common.TechCardAnnotationKind_TECH_CARD_ANNOTATION_KIND_PIN:
		word = "pin"
	case pb_common.TechCardAnnotationKind_TECH_CARD_ANNOTATION_KIND_LABEL:
		word = "note"
	case pb_common.TechCardAnnotationKind_TECH_CARD_ANNOTATION_KIND_DIM:
		word = "measurement"
	case pb_common.TechCardAnnotationKind_TECH_CARD_ANNOTATION_KIND_BRACKET:
		word = "bracket"
	case pb_common.TechCardAnnotationKind_TECH_CARD_ANNOTATION_KIND_MULTI,
		pb_common.TechCardAnnotationKind_TECH_CARD_ANNOTATION_KIND_INK:
		word = "line"
	case pb_common.TechCardAnnotationKind_TECH_CARD_ANNOTATION_KIND_ARC:
		word = "arc"
	case pb_common.TechCardAnnotationKind_TECH_CARD_ANNOTATION_KIND_POLYGON:
		word = "area"
	}
	return " — " + word + " at " + strconv.Itoa(int(math.Round(x*100))) + "% from the left, " +
		strconv.Itoa(int(math.Round(y*100))) + "% from the top"
}

// ─────────────────────────── общее ───────────────────────────

// designJSONMarshal — ПИСАТЕЛЬ КОЛОНОК `params` И `inputs`.
//
// ⚠ UseProtoNames: true — НЕСУЩЕЕ, И МЕНЯТЬ ЕГО НЕЛЬЗЯ. Стор ходит в эти колонки SQL-путями
// (`$.slots[*].media_id`, `$.extra_input_media_ids`, `$.colour`), написанными по snake_case.
// Дефолтный protojson написал бы lowerCamelCase, и оба запроса стали бы МОЛЧА пустыми: сторож
// HidePicture перестал бы отказывать, чипы цвета исчезли бы, mixed_input никогда не поднялся бы.
// Ни одной ошибки при этом не появится — пустой результат законен для карточки без прогонов,
// поэтому тест сам по себе этого не ловит.
//
// EmitUnpopulated НЕ ВКЛЮЧЁН: снимок ограничен 64 KB, и заполнение всех пустых полей тратило бы
// потолок на нули.
var designJSONMarshal = protojson.MarshalOptions{UseProtoNames: true}

func designMarshalJSON(m proto.Message) ([]byte, error) { return designJSONMarshal.Marshal(m) }

// designRunResponse — одна дверь из строки в ответ: конверсия плюс редакция денег. Две копии
// этой пары разошлись бы, и один из глаголов однажды отдал бы цены аккаунту без costing:read.
func (s *Server) designRunResponse(ctx context.Context, run entity.DesignRun) *pb_common.DesignRun {
	pb := designRunToPb(ctx, run)
	s.joinDesignRunInputMedia(ctx, []*pb_common.DesignRun{pb})
	s.stripDesignCosting(ctx, []*pb_common.DesignRun{pb}, nil)
	return pb
}

// designBudgetResponse — то же для полосы бюджета.
func (s *Server) designBudgetResponse(ctx context.Context, b entity.DesignBudget) *pb_common.DesignBudget {
	pb := designBudgetToPb(b)
	s.stripDesignCosting(ctx, nil, pb)
	return pb
}
