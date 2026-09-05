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
	"github.com/jekabolt/grbpwr-manager/internal/fal"
	"github.com/jekabolt/grbpwr-manager/internal/openrouter"
	"github.com/jekabolt/grbpwr-manager/internal/orimages"
	"github.com/jekabolt/grbpwr-manager/internal/recraft"
	"github.com/jekabolt/grbpwr-manager/internal/store/design"
	pb_admin "github.com/jekabolt/grbpwr-manager/proto/gen/admin"
	pb_common "github.com/jekabolt/grbpwr-manager/proto/gen/common"
	"github.com/shopspring/decimal"
	"google.golang.org/genproto/googleapis/rpc/errdetails"
	pb_decimal "google.golang.org/genproto/googleapis/type/decimal"
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
	entity.DesignRunKindThreed: designThreedCeilingUSD(),
	// ВЕКТОР — ЕДИНСТВЕННЫЙ РОД, У КОТОРОГО ЦЕНА ОПУБЛИКОВАНА ПАКЕТОМ СПИСАНИЯ. Берётся ИМЕННО ОНА.
	entity.DesignRunKindVector: designVectorCeilingUSD(),
	// ЧЕРНОВИК — ЦЕНА ПУСТОЙ ДОСКИ, а не цена нажатия. Полная оценка складывается в
	// designDraftIdeaEstimate, потому что у этого рода задание измеряется не числом выходов, а
	// числом ПРОЧИТАННЫХ КАРТИНОК, и таблица «цена одного выхода» такой вопрос не выражает.
	// Строка остаётся здесь, и это несущее: designEstimateFor обязана отвечать положительным
	// числом на КАЖДЫЙ род, который дверь принимает, иначе род резервирует NULL и его трата
	// проходит мимо дневного счёта (см. TestEVERY_RUN_KIND_THE_DOOR_ACCEPTS_HAS_A_PRICE).
	entity.DesignRunKindDraftIdea: designDraftIdeaBaseUSD,
	// ПЕРЕКРАС И ПАТТЕРН — ТОТ ЖЕ ПЛАТНЫЙ ЭНДПОИНТ, ЧТО У РЕНДЕРА, ЗНАЧИТ ТА ЖЕ ЛЕСТНИЦА ЧИСЕЛ.
	// Оба вызова несут ВХОДНУЮ картинку и просят выходную того же порядка, поэтому база берётся
	// рендерная, а не флэтовая, и множится на тот же потолок дила качества. Перекрас, кроме того,
	// платит ЗА КАЖДЫЙ снимок отдельно — но это уже считает designRequestedOutputs, а не эта
	// таблица: здесь цена ОДНОГО выхода, там их число.
	entity.DesignRunKindRecolor: designRenderMediumUSD.Mul(designImageQualityCeiling),
	entity.DesignRunKindPattern: designRenderMediumUSD.Mul(designImageQualityCeiling),
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

// designThreedCeilingUSD — САМЫЙ ДОРОГОЙ ИЗ ДВУХ 3D-МАРШРУТОВ, и он выбирается ПЕРЕБОРОМ, потому
// что дверь не знает, какой из них включён.
//
// ⚠ ЧТО ЭТО ЧИСЛО ДЕЛАЕТ СЕГОДНЯ — И ЧЕГО ОНО НЕ ДЕЛАЕТ. Оно попадает в `design_run.price_estimate`
// и в `design_budget_day.reserved`, то есть в БУХГАЛТЕРИЮ и на панель рядом с `price_actual`.
// ВОРОТ ЗА НИМ НЕТ: дневной потолок снят как понятие миграцией 0358 («у нас в принципе не должно
// быть потолка»), поэтому ошибка здесь искажает учёт и число на экране, но НЕ пропускает трату за
// какой-либо предел — предела нет. Соседний абзац designPriceEstimate написан до 0358 и местами
// говорит о потолке как о живых воротах; читать его надо с этой поправкой.
//
// ⚠ ЗДЕСЬ БЫЛ ДЕФЕКТ ДЕНЕГ ТОГО ЖЕ КЛАССА, ЧТО ОПИСАН ВЫШЕ: рядом с местом списания лежала СВОЯ
// копия цены. Таблица держала литерал $0.60 — цену ушедшего hitem3d, — а маршрут fal с тех пор
// считает сборку по $1.20 (fal.EstimatedRequestUSD, цена meshy v7 с текстурой с прайс-страницы
// самого fal). Пять поворотных столов в полёте показывали $3.00 обязательств против $6.00
// настоящего списания. Теперь оценка маршрута и резерв двери — ОДНО ВЫРАЖЕНИЕ.
//
// ⚠ ЧТО ЭТА ОЦЕНКА В ПРИНЦИПЕ МОЖЕТ ОБЕЩАТЬ, СКАЗАНО ЧЕСТНО И БЕЗ ЗАПАСА. Она покрывает сборку
// РОВНО ПОКА FAL_UNIT_USD НЕ ЗАДАН: тогда `fal.CostUSDFor` отвечает плоской ценой за запрос, не
// зависящей от числа единиц, и любой ответ провайдера уложится в резерв. Как только тариф задан,
// списание считается как `тариф × единицы`, а число единиц до сборки не знает и сам провайдер —
// прогон 17 вернул сто. Значит СТАТИЧЕСКОЙ верхней границы там не существует вовсе, и эта таблица
// её не изображает: при заданном тарифе резерв — правдоподобное ожидание, а не потолок. Настоящая
// защита от того дефекта живёт не здесь, а в `fal.CostUSDFor` (без тарифа не умножать) и в потолке
// ПОВТОРОВ (designMaxPaidAttempts).
//
// ⚠ ПОЧЕМУ МАКСИМУМ, А НЕ ЧТЕНИЕ DESIGN_THREED_PROVIDER. Довод тот же, что у дила качества
// картинки двумя абзацами выше: второй читатель настройки — это второе число, и оно разойдётся с
// первым на том деплое, который задаст настройку файлом, а не средой. Максимум читателя не заводит
// вовсе, а платит за это лишь тем, что число в полёте слегка завышено — и оно снимается целиком на
// терминальном переходе, уступая место ФАКТУ.
//
// MESHY СТОИТ ЛИТЕРАЛОМ, А FAL — НЕТ, И РАЗНИЦА НЕ В ВКУСЕ. У маршрута fal есть СОБСТВЕННОЕ
// опубликованное число, которым он и списывает без заданного тарифа, — его и берём. У прямого
// Meshy такого числа нет и быть не может: сколько кредитов съест задание, до сабмита не знает и сам
// провайдер, а курс кредита — env-дил (MESHY_CREDIT_USD), которого дверь не видит. $0.60 — это
// ~30 кредитов по ~$0.02 (meshy.defaultCreditUSD), догадка двери о ПОТОЛКЕ обычного задания, и она
// не дублирует ничего: в пакете meshy такого числа нет.
func designThreedCeilingUSD() decimal.Decimal {
	return decimal.Max(fal.EstimatedRequestUSD(), designMeshyTaskCeilingUSD)
}

var designMeshyTaskCeilingUSD = decimal.RequireFromString("0.60")

// designDraftIdeaBaseUSD / designDraftIdeaPictureUSD — ДВА СЛАГАЕМЫХ ЦЕНЫ ТЕКСТОВОГО ЧЕРНОВИКА.
//
// ⚠ ПОЧЕМУ ПЛОСКОЕ ЧИСЛО ЗДЕСЬ БОЛЬШЕ НЕ ГОДИТСЯ. Пока доска давала только слова, нажатие стоило
// одинаково независимо от того, сколько на ней плиток. С тех пор как картинки уезжают на провод
// (CompleteWithImages), КАЖДАЯ ИЗ НИХ — ЭТО ВХОДНЫЕ ТОКЕНЫ, то есть число картинок и есть цена.
// Прежние «0.02» на доске из двенадцати кадров занижали расход впятеро, и занижение шло не в
// отчёт, а в РЕЗЕРВ: дневной счёт видел пятую часть того, что было потрачено.
//
// ЧИСЛА — ПОРЯДОК ВЕЛИЧИНЫ ПО ЗАМЕРУ, А НЕ ПРАЙС ПОСТАВЩИКА, и названы вслух именно так. Один
// сжатый провайдером кадр — ≈1.6k входных токенов, отсюда ≈$0.005 за картинку по входному тарифу,
// округлённые вверх до 0.006.
//
// ⚠ КРУГ 20: У БАЗЫ ПОЯВИЛАСЬ ВТОРАЯ ВЕЛИЧИНА, ПОТОМУ ЧТО У НАЖАТИЯ ПОЯВИЛИСЬ ДВЕ ФОРМЫ.
// Структурная ветка (`construction`) покупает СВЕРХ прозаической и словарь цвета во входных, и
// целый потолок ответа в выходных; прозаическая не ставит потолка вовсе.
//
// ⚠ КРУГ 21: СТРУКТУРНАЯ БАЗА БОЛЬШЕ НЕ ВЫПИСАНА ЛИТЕРАЛОМ, ОНА ВЫВЕДЕНА ИЗ ПОТОЛКА ОТВЕТА.
// Литерал 0.035 держался фразой «колорвеи стоят ≈300–500 выходных токенов ≈ $0.005» — и пережил
// свою причину: потолок ответа подняли 3000 → 8000 (designConstructionMaxTokens), то есть купили
// ещё 5000 выходных токенов, а число, которое И РЕЗЕРВИРУЕТ, И СПИСЫВАЕТ, не двинулось ни на цент.
// Прибавка в 2.67× потолка была оценена в НОЛЬ. Комментарий, просящий двигать их вместе, тут уже
// стоял; он не удержал — поэтому теперь база СЧИТАЕТСЯ ИЗ ПОТОЛКА, и разойтись им негде.
//
// ⚠ ОТВЕТ ОЦЕНИВАЕТСЯ ПО ВЫХОДНОМУ ТАРИФУ, А НЕ ПО ВХОДНОМУ, И ЭТО НЕ ПЕДАНТИЗМ. Выходной токен у
// этого семейства стоит впятеро против входного, а доктрина этого блока — ВЕРХНЯЯ ГРАНИЦА рода:
// заниженный факт врёт про потраченное навсегда. Посчитать ответ по входным $3/M было бы
// пятикратным занижением ровно того слагаемого, которое здесь и выросло.
//
// ⚠ И ИМЕННО ПОЭТОМУ ЧИСЕЛ ДВА, А НЕ ОДНО. Первая правка круга 20 подвинула ЕДИНСТВЕННУЮ базу
// 0.03 → 0.035, а `est` считается ДО выбора ветки — значит прогон со снятым флагом (старый клиент:
// ни колорвеев в ответе, ни словаря цвета в запросе) с того дня резервировал и ЗАПИСЫВАЛ В РЕГИСТР
// $0.035 за то, чего не покупал: +17% к каждому прозаическому нажатию. Направление безопасное, но
// оценка, завышенная ОДИНАКОВО ДЛЯ ВСЕХ, перестаёт отвечать на вопрос «сколько стоит вот это
// нажатие», а другого источника этого ответа у регистра нет.
//
// ⚠ ОЦЕНКА ЖЕ И СПИСЫВАЕТСЯ. Чат-эндпоинт возвращает токены, а не деньги (см. FinishAttempt ниже),
// поэтому это число — не только резерв, но и ФАКТ в регистре. Завышать его безопаснее, чем
// занижать: завышенный резерв сужает очередь на минуты, заниженный факт врёт про потраченное
// навсегда. Поэтому В ТАБЛИЦЕ РОДОВ стоит БОЛЬШАЯ из двух: designEstimateFor отвечает на вопрос
// «сколько может стоить прогон этого рода», у которого формы ещё нет.

// ─── ТАРИФ, ИЗ КОТОРОГО СЧИТАЮТСЯ БАЗЫ ───
//
// ⚠ ЭТО ТАРИФ СЕМЕЙСТВА, А НЕ СЧЁТ ПОСТАВЩИКА, и он назван вслух именно так: чат-эндпоинт
// OpenRouter возвращает ТОКЕНЫ, а не деньги (см. FinishAttempt), поэтому денег, которыми можно было
// бы свериться, у этого кода нет вовсе. Anthropic держит класс Sonnet на $3/M входных и $15/M
// выходных не первое поколение; если слуг (openrouter.defaultModel) однажды переедет в другой
// класс, ПЕРЕЕХАТЬ ОБЯЗАНЫ И ЭТИ ДВА ЧИСЛА — они здесь стоят рядом ровно для того, чтобы правка
// была одна.
const (
	designChatUSDPerMTokIn  = "3"
	designChatUSDPerMTokOut = "15"
	// designConstructionInputTokens — входные токены структурного нажатия БЕЗ КАРТИНОК: сам промпт
	// (≈2k) плюс словарь цвета (≈1k при полном потолке в 200 строк). Картинки считаются отдельным
	// слагаемым (designDraftIdeaPictureUSD), потому что их число знает только вызывающий.
	designConstructionInputTokens = 3000
	// designProseInputTokens — входные токены ПРОЗАИЧЕСКОГО нажатия без картинок: тот же ≈2k
	// промпта, что назван строкой выше, БЕЗ словаря цвета — прозаическая ветка его не читает вовсе
	// (design_run.go: `if construction { ... ListColors ... }`).
	designProseInputTokens = 2000
	// designProseAnswerTokens — ОЖИДАЕМЫЙ, А НЕ РАЗРЕШЁННЫЙ размер прозаического ответа, и разница
	// названа вслух, потому что она и есть предмет решения ниже.
	//
	// Роль просит РОВНО ТРИ раздела (draftIdeaSystemPrompt): DESCRIPTION — «at most 120 words»,
	// DESIGN ASPECTS — по строке на аспект, MISSING CALLOUTS — по строке на пропуск. Живой ответ
	// такой формы это ≈400–700 токенов; 1600 — двукратный запас поверх верхнего края.
	designProseAnswerTokens = 1600
)

// designTokensUSD — цена N токенов по тарифу «долларов за миллион». Одна функция на оба тарифа:
// два места, делящие на миллион, — это два места, где однажды окажется разное число нулей.
func designTokensUSD(tokens int, usdPerMTok string) decimal.Decimal {
	return decimal.RequireFromString(usdPerMTok).
		Mul(decimal.NewFromInt(int64(tokens))).
		Div(decimal.NewFromInt(1_000_000))
}

var (
	// ─── КРУГ 22: ПРОЗАИЧЕСКАЯ БАЗА БОЛЬШЕ НЕ ЛИТЕРАЛ, НО И НЕ ГРАНИЦА ───
	//
	// ⚠ ЧТО ЗДЕСЬ БЫЛО НЕ ТАК И ЧТО ИМЕННО ЧИНИТСЯ. Прежний довод звучал так: «выводить не из чего,
	// прозаическая ветка не ставит потолка вовсе (maxTokens = 0)», — и он верен ПРО ПОТОЛОК и
	// неверен ПРО ТАРИФ. Литерал 0.03 не был случайным числом: это ровно та же арифметика, что у
	// соседа, посчитанная однажды руками, — ≈2k входных по $3/M плюс ≈1.6k выходных по $15/M.
	// Посчитанная руками и замороженная, она гниёт по ОДНОЙ И ТОЙ ЖЕ дороге с обеими предыдущими
	// починками этого блока: тариф `designChatUSDPerMTok*` живёт наверху с просьбой «переехать
	// вместе, если слуг сменит класс», а сегодня по этой просьбе поехала бы ТОЛЬКО структурная
	// база — прозаическая осталась бы на месте молча. Просьба в комментарии уже не удержала здесь
	// однажды (см. довод круга 21 про потолок 3000 → 8000). Второй раз её сюда ставить нельзя.
	//
	// ⚠ ЧЕГО ЭТО НЕ ЧИНИТ, И ЭТО СКАЗАНО ПРЯМО. designProseAnswerTokens — ОЖИДАНИЕ, А НЕ ВЕРХНЯЯ
	// ГРАНИЦА, поэтому эта одна строка НЕ подчиняется доктрине блока («каждое число — верхняя
	// граница рода»), и подчиниться не может: у ветки без потолка верхней границы ответа НЕ
	// СУЩЕСТВУЕТ вовсе — модель вправе печатать до своего собственного предела. Изобразить её
	// потолком модели (десятки тысяч токенов ≈ $1 за нажатие) значило бы поставить в регистр число,
	// которое никогда не будет потрачено, — то же враньё, только в другую сторону.
	//
	// ⚠ ЧТО СДЕЛАЛО БЫ ЭТУ СТРОКУ ГРАНИЦЕЙ, И ПОЧЕМУ ЭТОГО НЕ СДЕЛАНО ЗДЕСЬ. Ровно одно: поставить
	// прозаической ветке свой `max_tokens`, как это сделано у структурной. Тогда база вывелась бы
	// из него той же строкой, что у соседа, и доктрина сомкнулась бы. Но потолок меняет БАЙТЫ
	// ЗАПРОСА, а байты прозаической ветки — контракт со старым клиентом (V-19: «отсутствующий флаг
	// обязан давать прежние байты»), и вместе с потолком пришлось бы включить `reasoning:none`
	// (openrouter: кто ставит потолок, тот выключает мышление) и научиться отвечать на
	// `finish_reason=length` в ветке, которая сегодня про него не знает. Это решение владельца про
	// то, чем он готов ограничить ответ, а не починка бухгалтерии — и делать его молча, внутри
	// правки про оборванный провод, было бы ровно тем, за что этот файл ругает сам себя абзацем
	// выше.
	//
	// ЧИСЛО НЕ ИЗМЕНИЛОСЬ: 2000 × $3/M + 1600 × $15/M = $0.006 + $0.024 = $0.030, тот же литерал,
	// что стоял здесь. Успешное прозаическое нажатие стоит ровно столько же, сколько вчера, —
	// TestProseBaseStillCostsWhatTheLiteralDid держит это число неподвижным.
	designDraftIdeaProseBaseUSD = designTokensUSD(designProseInputTokens, designChatUSDPerMTokIn).
					Add(designTokensUSD(designProseAnswerTokens, designChatUSDPerMTokOut))
	// ⚠ А СТРУКТУРНАЯ — ВЫВЕДЕНА, И ИМЕННО ИЗ ПОТОЛКА, КОТОРЫЙ ЭТА ВЕТКА КЛАДЁТ НА ПРОВОД. Потолок
	// не «оценивается» — он РАЗРЕШЁН, значит поставщик вправе напечатать его целиком, и верхняя
	// граница рода обязана быть посчитана по нему. Сдвиньте designConstructionMaxTokens — цена
	// сдвинется тем же коммитом, без чьей-либо памяти; тест
	// TestConstructionBasePricesTheWholeAnswerCeiling краснеет, если её снова выпишут литералом.
	designDraftIdeaConstructionBaseUSD = designTokensUSD(designConstructionInputTokens, designChatUSDPerMTokIn).
						Add(designTokensUSD(designConstructionMaxTokens, designChatUSDPerMTokOut))
	designDraftIdeaPictureUSD = decimal.RequireFromString("0.006")
)

// designDraftIdeaBaseUSD — то, что стоит в designPriceEstimate: ПОТОЛОК двух баз, а не одна из
// них. Выведен, а не выписан: третье число рядом с двумя разошлось бы с ними в тот день, когда
// правят одно.
var designDraftIdeaBaseUSD = decimal.Max(designDraftIdeaProseBaseUSD, designDraftIdeaConstructionBaseUSD)

// designDraftAnswerCeiling — ПОТОЛОК ОТВЕТА для данной формы нажатия, ноль значит «потолка нет».
//
// ⚠ ФУНКЦИЯ, А НЕ ДВА ЛИТЕРАЛА В ХЕНДЛЕРЕ, ПОТОМУ ЧТО ЭТО ЖЕ ЧИСЛО — ИСТОЧНИК ЦЕНЫ. Структурная
// база выведена из designConstructionMaxTokens (круг 21), прозаическая — не выведена НИ ИЗ ЧЕГО
// ровно потому, что здесь ноль (круг 22, см. designDraftIdeaProseBaseUSD). Пока условие «у прозы
// потолка нет» было записано литералом внутри хендлера, оно оставалось прозой: снять его мог кто
// угодно, и цена прозаического нажатия молча перестала бы отвечать на свой вопрос. Теперь его
// СПРАШИВАЮТ — TestProseBaseStillCostsWhatTheLiteralDid краснеет в тот день, когда потолок у прозы
// появится, и требует вывести базу из него так же, как у соседа.
//
// ⚠ ПОТОЛОК ТАЩИТ ЗА СОБОЙ ЕЩЁ ДВЕ ВЕЩИ, И ОБЕ НЕ ЗДЕСЬ. Первая — выключенное мышление (openrouter:
// «кто ставит потолок, тот выключает мышление»); вторая — ответ на `finish_reason=length`, которого
// прозаическая ветка сегодня не знает. Поэтому ноль здесь — не заглушка, а ТРИ несделанных решения
// разом, и все три принимает владелец, а не эта функция.
func designDraftAnswerCeiling(construction bool) int {
	if construction {
		return designConstructionMaxTokens
	}
	return 0
}

// designDraftIdeaEstimate — цена ОДНОГО нажатия «черновик» при данном числе прочитанных картинок
// и данной ФОРМЕ ответа.
//
// Считает по attachedIDs, а не по плиткам доски: платится то, что уехало на провод. База берётся
// по ветке, потому что ветки покупают разное; обе стоят рядом в одном var-блоке выше, и таблица
// родов выведена из них же.
func designDraftIdeaEstimate(pictures int, construction bool) decimal.NullDecimal {
	if _, ok := designPriceEstimate[entity.DesignRunKindDraftIdea]; !ok {
		// Род без строки в таблице резервирует NULL и проходит мимо дневного счёта — тот самый
		// инвариант, что держит TestEVERY_RUN_KIND_THE_DOOR_ACCEPTS_HAS_A_PRICE. Молчать о его
		// нарушении здесь значило бы чинить симптом.
		return decimal.NullDecimal{}
	}
	base := designDraftIdeaProseBaseUSD
	if construction {
		base = designDraftIdeaConstructionBaseUSD
	}
	if pictures < 0 {
		pictures = 0
	}
	total := base.Add(designDraftIdeaPictureUSD.Mul(decimal.NewFromInt(int64(pictures))))
	return decimal.NullDecimal{Decimal: total, Valid: true}
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
			"kind %q is not flat | render | threed | vector | recolor | pattern", kind)
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

	// ─── W-13 × L-3: 3D ТОЛЬКО ПОСЛЕ ЗАНЯТОГО РЕНДЕР-ВЕРСТАКА ТОГО ЖЕ КОЛОРВЕЯ ───
	//
	// Множество считает GetBand по ВСЕЙ карточке (занятые render-слоты, DISTINCT по колорвею), а
	// не по загруженной странице: слот вполне лежит за её потолком, и гейт, посчитанный по
	// странице, закрыл бы 3D ровно там, где оно законно.
	//
	// ГЕЙТ СПРАШИВАЕТ ВЕРСТАК, А НЕ ПОЛОСУ, ПОТОМУ ЧТО ВЕРСТАК ЖЕ ЧИТАЕТ ОТБОР ПЛИТ
	// (designSelectBench): 3D колорвея A собирается ТОЛЬКО из занятых render-слотов A. Гейт по
	// картинкам отвечал на другой вопрос — «есть ли на карточке такой файл» — и пропускал
	// главный, повседневный случай: рендер загружен, но ещё не поставлен ни на одну сторону.
	// Деньги резервировались, прогон уходил в работу без единого входа. Колорвей 0 —
	// безколорвейное 3D — требует занятого неатрибутированного верстака: ровно того, что есть у
	// каждой карточки, жившей до оси. Гейт стоит ПОСЛЕ designEffectiveParams намеренно: реран
	// наследует колорвей из params родителя, и гейт по сырому запросу спрашивал бы не про тот
	// верстак.
	if kind == entity.DesignRunKindThreed {
		cw := int(params.GetColorwayId())
		if !designHasRenderForColorway(band.RenderBenchColorways, cw) {
			return nil, designRefusal(codes.FailedPrecondition, "no_fabric_render",
				fmt.Sprintf("3D reads the render bench of the colourway it renders, and colourway %d has no "+
					"fabric render STANDING ON a render slot of this card (0 = the colourway-less bench). "+
					"Uploading a render is not enough — put it on a side first. "+
					"The order is flats → fabric render → place it on the render bench → 3D", cw),
				map[string]string{
					"tech_card_id": strconv.Itoa(cardID),
					"colorway_id":  strconv.Itoa(cw),
				})
		}
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
	// ⚠ ИСТОЧНИКИ СОБИРАЮТСЯ ЗДЕСЬ, А НЕ ПЕРЕД СНИМКОМ, потому что теперь ими пользуются И ДВЕРИ
	// (N5): предикат, которым отбор сужает верстак, обязан быть ТЕМ ЖЕ, которым дверь отвергает
	// названный адрес, — иначе один молча выбросит то, что другой принял. Значение чисто
	// описательное (род, карточка, референсы, верстак, действующие params) и до самого отбора не
	// меняется, так что перенос вверх ничего не сдвигает.
	src := designInputSources{
		Kind:   kind,
		Card:   card,
		Refs:   band.References,
		Bench:  band.Bench,
		Params: params,
	}

	if err := designRefuseForeignDetailSlots(cardID, req.GetParams(), band.Bench, src); err != nil {
		return nil, err
	}
	// ⚠ И ТО ЖЕ САМОЕ ДЛЯ fix_slot_ids, У КОТОРОГО ПРОВЕРКИ ПРИНАДЛЕЖНОСТИ НЕ БЫЛО ВОВСЕ (N5).
	// Он СУЖАЕТ отбор до названных плит, и это делает его опаснее списка деталей: денежные ворота
	// смотрят на верстак колорвея ЦЕЛИКОМ, поэтому достаточно одного занятого слота, чтобы дверь
	// открылась, — а выборочный прогон, назвавший только чужой адрес, соберёт НОЛЬ плит и уедет
	// оплаченным и пустым. Тот же приём и та же граница: спрашивают с того, КТО НАЗВАЛ, поэтому
	// сюда едет сообщение клиента, а не действующие params (унаследованный снимок рерана
	// проверять нельзя — адрес законно удаляют, и прогон стал бы неповторимым навсегда).
	if err := designRefuseForeignFixSlots(cardID, req.GetParams(), band.Bench, src); err != nil {
		return nil, err
	}
	// ТА ЖЕ ДВЕРЬ ДЛЯ СПИСКА ФЛЭТ-ПЛИТ (J-10): человек попросил послать ИМЕННО эти плиты, и просьба,
	// не совпавшая ни с одним пригодным слотом, обязана получить отказ ЗДЕСЬ, а не выпасть молча
	// при отборе — иначе прогон оплачен, а плит в нём нет.
	if err := designRefuseForeignFlatSlots(cardID, req.GetParams(), band.Bench, src); err != nil {
		return nil, err
	}
	// ТОТ ЖЕ ПРИЁМ ДЛЯ ПОЛОК: адрес, названный КЛИЕНТОМ, отвечает за себя, унаследованный — нет.
	// Довод дословно тот же, что у детали строкой выше, и он здесь ещё жёстче: полку законно
	// удаляют (DeleteDesignAsset), а параметры родителя заморожены — проверка унаследованного
	// списка сделала бы прогон неперезапускаемым НАВСЕГДА.
	if err := designRefuseForeignClothAssets(cardID, req.GetParams(), band.Assets); err != nil {
		return nil, err
	}
	// ТА ЖЕ ЛЕСТНИЦА ДЛЯ ИСТОЧНИКА ПЛИТКИ: `params.pattern.source_asset_id` — это полка ЭТОЙ
	// карточки, из которой сделан паттерн, и она замерзает в `derived_from_asset_id` сажаемого
	// ассета. Спрашивается с ГОВОРЯЩЕГО по тому же доводу, что и ткани: полку законно удаляют, и
	// проверка унаследованного id сделала бы реран невозможным навсегда.
	if err := designRefuseForeignPatternSource(cardID, req.GetParams(), band.Assets); err != nil {
		return nil, err
	}
	// ─── ПОЛКА ПЕРЕПОЛНЕНА — ОТКАЗ ДО ДЕНЕГ, А НЕ ПОСЛЕ (J-12) ───
	//
	// Прогон паттерна ПОКУПАЕТ ПЛИТКУ И ТУТ ЖЕ САЖАЕТ ЕЁ НА ПОЛКУ. Если полка уже полна, посадка в
	// транзакции закрытия не состоится — картинка останется в ленте, а ассета не будет, и человек
	// узнает об этом ПОСЛЕ списания. Здесь это стоит одного отказа и ноля денег.
	//
	// ⚠ ЭТО ПРЕДВАРИТЕЛЬНАЯ ПРОВЕРКА, А НЕ НАСТОЯЩАЯ, И ВТОРАЯ ВСЁ РАВНО НУЖНА. Между этим
	// чтением и посадкой проходят минуты работы воркера, за которые сороковую полку заводит
	// сосед; поэтому потолок считается ЕЩЁ РАЗ в транзакции посадки (store/design: keepPatternTx),
	// и там его цена — уже купленная картинка без ассета и `error_code = 'library_full'` на
	// закрытой строке. Одна из двух проверок без другой была бы либо TOCTOU, либо тратой денег на
	// заведомо невозможную посадку.
	if kind == entity.DesignRunKindPattern && len(band.Assets) >= entity.MaxDesignAssetsPerCard {
		return nil, designRefusal(codes.FailedPrecondition, entity.DesignErrorCodeLibraryFull,
			fmt.Sprintf("tech card %d already holds %d shelf rows, the ceiling is %d — a pattern run "+
				"files its tile on that shelf when it lands, so there would be nowhere to put it. "+
				"Delete a cloth or a pattern first. Nothing was reserved and nothing was charged",
				cardID, len(band.Assets), entity.MaxDesignAssetsPerCard),
			map[string]string{
				"tech_card_id": strconv.Itoa(cardID),
				"held":         strconv.Itoa(len(band.Assets)),
				"ceiling":      strconv.Itoa(entity.MaxDesignAssetsPerCard),
			})
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
	// ⚠ КАРТЫ ЦВЕТА — ЧЕТВЁРТЫЙ ИСТОЧНИК ЧУЖОГО НОМЕРА, И ОН ДОБАВЛЕН ВМЕСТЕ С ПОЛЕМ. Карта уезжает
	// поставщику как картинка прогона (designgen/snapshot.go цепляет каждую `colour_maps`), значит
	// граница у неё та же самая, что у плит, референсов и текстур. Без этой проверки достаточно
	// было положить чужой PNG в `colour.colour_maps[0].media_id` — и он уезжал бы при полностью
	// законных остальных трёх списках.
	//
	// ПОДЛОЖКА КАРТЫ (`base_media_id`) ЗДЕСЬ НЕ ПРОВЕРЯЕТСЯ, И ЭТО НЕ ПРОПУСК: она поставщику не
	// уезжает и в промпте не упоминается — это метка устаревания, которую читает клиент. Проверка
	// заморожённого номера сделала бы реран невозможным ровно тогда, когда флэт законно сменили.
	if err := s.designRefuseForeignMedia(ctx, cardID, "params.colour.colour_maps.media_id",
		designColourMapMediaIDs(params.GetColour())...); err != nil {
		return nil, err
	}
	// ФОРМА КАРТ — У ГОВОРЯЩЕГО, А НЕ У УНАСЛЕДОВАННОГО СНИМКА, и это та же лестница, что у адреса
	// полки строкой выше: словарь видов законно растёт, а параметры родителя заморожены, поэтому
	// проверка унаследованного значения сделала бы старый прогон неперезапускаемым навсегда.
	if err := designRefuseMalformedColourMaps(req.GetParams()); err != nil {
		return nil, err
	}

	// ─── РОДЫ, У КОТОРЫХ ВХОД — КОНКРЕТНАЯ КАРТИНКА, А НЕ КОНТЕКСТ ───
	//
	// Стоит ПОСЛЕ границы карточки и ДО резерва: прогон, которому нечего перекрашивать, не должен
	// заводить строку, занимать деньги дня и через тик воркера гарантированно проваливаться.
	if err := designRefuseUnworkableSources(kind, params); err != nil {
		return nil, err
	}

	inputs, fitAtLaunch, err := s.designRunInputs(ctx, src, parent)
	if err != nil {
		return nil, err
	}
	// ─── 3D БЕЗ ПЕРЕДА — ОТКАЗ ЗДЕСЬ, А НЕ ПАДЕНИЕ В ВОРКЕРЕ (J-26) ───
	//
	// СТОИТ РОВНО ЗДЕСЬ, И ЭТО ЕДИНСТВЕННОЕ ВОЗМОЖНОЕ МЕСТО. Раньше — вопросу не на чем стоять:
	// снимок ещё не собран, а у РЕРАНА он и не собирается из сегодняшнего верстака вовсе
	// (designRunInputs переписывает входы со строки родителя целиком). Позже — уже поздно:
	// s.repo.Design().StartRun тридцатью строками ниже РЕЗЕРВИРУЕТ деньги дня одной транзакцией со
	// вставкой строки, и всякий отказ после неё — это занятый резерв и `failed` в оплаченной
	// истории за просьбу, которую можно было отклонить бесплатно.
	//
	// ⚠ ЭТО ТОТ ЖЕ ПРЕДИКАТ, ЧТО У ВОРКЕРА, НА ТЕХ ЖЕ ДАННЫХ — не второе мнение о том же.
	// designgen.threedPictures читает `inputs.slots`, ищет силуэтную сторону `front` с медиа и на
	// её отсутствии возвращает ПУСТОЙ список, после чего маршрут отказывает у самой двери
	// провайдера (meshy.ErrImageCount / fal.ErrNoFrontView, оба Retryable=false). Здесь спрашивается
	// ТОТ ЖЕ снимок, на один тик раньше и до денег. Разойтись им не на чем: второго источника
	// `inputs.slots` не существует.
	//
	// ЭТО УЖЕСТОЧЕНИЕ СУЩЕСТВУЮЩИХ ВОРОТ, А НЕ ЗАМЕНА ИМ. Ворота выше (`no_fabric_render`)
	// спрашивают «занят ли рендер-верстак этого колорвея ХОТЬ ЧЕМ-НИБУДЬ» и пропускают верстак, на
	// котором стоит одна СПИНА: множество RenderBenchColorways считает занятые слоты, не различая
	// сторон. Такой прогон резервировал и гарантированно падал у провайдера. Ни один законный 3D-
	// прогон этим не задет: без переда провайдер не строит ничего ни в одном из двух маршрутов.
	if err := designRefuseThreedWithoutFront(kind, cardID, params, inputs); err != nil {
		return nil, err
	}
	// ─── ВХОД, КОТОРЫЙ НЕ КАРТИНКА, — ОТКАЗ ЗДЕСЬ, А НЕ ОПЛАЧЕННЫЙ ОТКАЗ У ПОСТАВЩИКА ───
	//
	// СТОИТ РОВНО ЗДЕСЬ ПО ДВУМ ПРИЧИНАМ СРАЗУ. Раньше нельзя: `inputs` ещё не собраны, а плиты
	// верстака и референсы живут только в них — у рерана они и вовсе переписаны со строки
	// родителя. Позже нельзя: `s.repo.Design().StartRun` двадцатью строками ниже РЕЗЕРВИРУЕТ
	// деньги дня той же транзакцией, что вставляет строку, и всякий отказ после неё — занятый
	// резерв и `failed` в оплаченной истории.
	//
	// Довод целиком — в шапке design_input_format.go: медиа опознаётся номером и адресом, content
	// type не хранит никто, и до этой двери .glb, загруженный руками, доезжал до слота картинки
	// платного вызова пятью разными путями.
	if err := s.designRefuseNonPictureInputs(ctx, params, inputs); err != nil {
		return nil, err
	}
	// ─── КАДР «ТОЛЬКО ДЛЯ ПОКАЗА» — ОТКАЗ ЗДЕСЬ ЖЕ, ПО ТЕМ ЖЕ ПЯТИ ИСТОЧНИКАМ (0361, D-24) ───
	//
	// Та же позиция и тот же довод, что у двери формата строкой выше: входы уже собраны, деньги
	// ещё нет. Довод, почему это дверь, а не фильтр в отборе плит, — в шапке design_input_format.go.
	if err := s.designRefuseDisplayOnlyInputs(ctx, designRunInputMediaRefs(params, inputs)); err != nil {
		return nil, err
	}
	// ─── КАРТА ЦВЕТА, КОТОРАЯ НА САМОМ ДЕЛЕ ПЛИТА ИЛИ РЕФЕРЕНС ───
	//
	// Та же позиция и тот же довод, что у двух сторожей выше, и тот же СПИСОК ИСТОЧНИКОВ: вопрос
	// «чем ещё является эта картинка» отвечается ровно по тем пяти местам, откуда медиа уезжает
	// поставщику. Довод целиком — в шапке designRefuseColourMapAlsoAnInput.
	if err := designRefuseColourMapAlsoAnInput(params, inputs); err != nil {
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
		// Колорвей прогона — из ДЕЙСТВУЮЩИХ params (реран наследует родительские); стор в той же
		// транзакции отказывает роду без оси и чужому колорвею — до резерва денег.
		ColorwayId: int(params.GetColorwayId()),
		// ⚠ А ВОТ «КТО ЭТО СКАЗАЛ» — ИЗ СООБЩЕНИЯ КЛИЕНТА, И ЭТО ТО ЖЕ РАЗЛИЧЕНИЕ, ЧТО У ДЕТАЛЕЙ И
		// ПОЛОК ВЫШЕ (F2). Реран, не назвавший колорвея, наследует его из замороженных params
		// родителя; колорвей законно удаляют, и строгая проверка унаследованного id отказывала бы
		// `foreign_colorway` ВЕЧНО — клиенту, которому нечего было бы написать иначе. Стор
		// деградирует такой прогон в неатрибутированный, ровно как FK погасил колонку родителя.
		//
		// ⚠ СПРАШИВАЕТСЯ «НАЗВАЛ ЛИ КОЛОРВЕЙ», А НЕ «ПРИСЛАЛ ЛИ PARAMS», И РАЗНИЦА БЫЛА ДЕФЕКТОМ.
		// Прежнее написание — `req.GetParams() != nil` — считало НАЗВАВШИМ всякого, кто прислал
		// параметры по любой причине: реран, поправивший `ask`, приезжал сюда с колорвеем,
		// УНАСЛЕДОВАННЫМ строкой выше (designEffectiveParams), и с флагом «это сказал я». Если тот
		// колорвей к тому времени удалили, мягкая половина стора (wave2.go) не срабатывала, и
		// законный реран получал `foreign_colorway` НАВСЕГДА — притом что колорвея он не называл
		// вовсе. Денег это не стоило (отказ до резерва); стоило это невозможности повторить прогон.
		//
		// НОЛЬ У РЕРАНА ЗНАЧИТ «НАСЛЕДУЙ», И ЭТО СКАЗАНО ВЫШЕ ДОСЛОВНО, поэтому «назвал ноль»
		// невыразимо и здесь ничего не теряет. У обычного прогона ноль тоже безопасен: стор входит
		// в эту развилку только при `colorwayID > 0`.
		ColorwayStated: req.GetParams().GetColorwayId() > 0,
	})
	if err != nil {
		return nil, designError(ctx, "failed to start the design run", err, nil)
	}
	return &pb_admin.StartDesignRunResponse{
		Run:    s.designRunResponse(ctx, started.Run),
		Budget: s.designBudgetResponse(ctx, started.Budget),
	}, nil
}

// designRefuseUnworkableSources — ворота двух родов, чей вход это НАЗВАННАЯ КАРТИНКА, а не набор
// контекста (K-17 и K-13).
//
// ⚠ ПОЧЕМУ ЭТО ПРОВЕРЯЕТСЯ ЗДЕСЬ, А НЕ ТОЛЬКО В ВОРКЕРЕ. Воркер тоже отказывает — и обязан, потому
// что медиа-строка могла исчезнуть между снимком и проходом, — но его отказ приходит ПОСЛЕ того,
// как строка прогона завела резерв дневного бюджета и заняла его до полуночи либо до терминального
// перехода. Клик по кнопке, у которой не выбрана ни одна фотография, обошёлся бы в один такой
// висящий резерв за клик. Здесь не зарезервировано ничего и не потрачено ничего.
//
// ⚠ ЧИСЛА РАЗНЫЕ, И РАЗНИЦА СОДЕРЖАТЕЛЬНАЯ. Перекрас берёт СКОЛЬКО УГОДНО снимков — «фото на
// модели с разных сторон» это ровно про несколько кадров, и каждый едет отдельным платным
// вызовом. Паттерн берёт РОВНО ОДИН: плитка, склеенная из двух лоскутов, не стыкуется сама с
// собой ни при каком раскладе, то есть бесполезна в том единственном смысле, ради которого её
// заказывали.
//
// ⚠ И ПЕРЕКРАС ТРЕБУЕТ НАЗВАННОЙ ЦЕЛИ. «Поменяй цвет» без цели — это просьба, на которую модель
// ответит чем угодно: вернётся тот же снимок в случайном оттенке, деньги списаны, а по истории
// такой прогон не отличить от честного. Годится ЛЮБОЕ из ЧЕТЫРЁХ написаний: код колорвея, hex,
// слова — все три доезжают до промпта одним блоком `colour`, — И ТКАНЬ С КАРТИНКОЙ, потому что
// после J-31 «переодеть в эту ткань» такая же законная цель, как «перекрасить в этот цвет», и
// уезжает она отдельной картинкой в каждый платный вызов (designgen/images.go).
//
// ⚠ А ВОТ ТКАНЬ БЕЗ КАРТИНКИ ЦЕЛЬЮ НЕ РАБОТАЕТ, И ОТКАЗ ЕЙ ОТДЕЛЬНЫЙ. Ткань, названная одними
// словами, не может быть НАЛОЖЕНА на фотографию: класть нечего, а в промпте она превратилась бы в
// описание, которое модель отработает свободной перерисовкой — то есть новым снимком за те же
// деньги. Своё имя (`cloth_without_picture`) вместо `no_target_colour` потому, что человек
// назвал цель, и сказать ему «ничего не названо» значило бы послать его чинить не то.
//
// ⚠ И ПАТТЕРН ТРЕБУЕТ ИМЕНИ. Плитка после круга 15 садится на полку карточки в той же транзакции,
// что закрывает прогон (store/design: keepPatternTx), а `design_asset.name` — NOT NULL и колонка
// на 60 знаков. Безымянная плитка либо уронила бы посадку уже оплаченного прогона, либо приехала
// бы в следующий промпт словом «pattern». Спрашивается ДО денег, потому что человек и так его
// пишет до нажатия кнопки.
func designRefuseUnworkableSources(kind string, params *pb_common.DesignRunParams) error {
	sources := len(params.GetExtraInputMediaIds())
	switch kind {
	case entity.DesignRunKindRecolor:
		if sources == 0 {
			return designRefusal(codes.InvalidArgument, "no_source_picture",
				"a recolour needs the photographs it recolours: name them in "+
					"params.extra_input_media_ids. Nothing was reserved and nothing was charged", nil)
		}
		if err := designRefuseUnworkableRecolourCloth(params); err != nil {
			return err
		}
	case entity.DesignRunKindPattern:
		if sources != 1 {
			return designRefusal(codes.InvalidArgument, "one_source_picture",
				fmt.Sprintf("a repeating tile is built from exactly one picture, and this run names %d: "+
					"put that one picture in params.extra_input_media_ids. Nothing was reserved and "+
					"nothing was charged", sources),
				map[string]string{"named": strconv.Itoa(sources)})
		}
		name := strings.TrimSpace(params.GetPattern().GetName())
		if name == "" {
			return designRefusal(codes.InvalidArgument, "pattern_name_required",
				"a pattern is filed on this card's shelf the moment it lands, and a shelf row needs a "+
					"name: state params.pattern.name. Nothing was reserved and nothing was charged", nil)
		}
		if n := len([]rune(name)); n > entity.MaxDesignAssetNameRunes {
			// НЕ СЕНТИНЕЛ, А ОБЫЧНЫЙ InvalidArgument: это не «чего-то не хватает», а «слишком
			// длинно», и правило то же самое, которому подчиняется UpsertDesignAsset.name —
			// потому что колонка одна.
			return status.Errorf(codes.InvalidArgument,
				"params.pattern.name is %d characters; the ceiling is %d — it lands in the same column "+
					"an asset name lands in", n, entity.MaxDesignAssetNameRunes)
		}
		// ⚠ И РАППОРТ ТОЖЕ ЛАНДИТ В КОЛОНКУ, А КОЛОНКА SMALLINT UNSIGNED (0354). До круга 15 это
		// число никуда, кроме промпта, не ехало, и границы ему не требовалось; теперь оно едет в
		// `design_asset.repeat_mm` при закрытии прогона, и `repeat_mm: 70000` уронил бы посадку
		// УЖЕ ОПЛАЧЕННОЙ картинки ошибкой 1264. Правило и число — те же самые, которым подчиняется
		// UpsertDesignAsset (entity.MaxDesignAssetRepeatMm), потому что колонка одна.
		if mm := int(params.GetPattern().GetRepeatMm()); mm < 0 || mm > entity.MaxDesignAssetRepeatMm {
			return status.Errorf(codes.InvalidArgument,
				"params.pattern.repeat_mm is %d; it is whole millimetres from 0 to %d — it lands in the "+
					"same column an asset repeat lands in", mm, entity.MaxDesignAssetRepeatMm)
		}
	}
	return nil
}

// designRefuseUnworkableRecolourCloth — ВСЁ, ЧТО ДВЕРЬ ЗНАЕТ ПРО ТКАНЬ ПЕРЕКРАСА, в одном месте и
// целиком ДО РЕЗЕРВА.
//
// ⚠ ЧЕТЫРЕ ОТКАЗА, И ВСЕ ЧЕТЫРЕ — ДЕНЬГИ, потому что после J-31 ткань стала ВТОРОЙ КАРТИНКОЙ
// КАЖДОГО платного вызова (designgen/images.go). До этой волны вызов перекраса нёс РОВНО ОДНУ
// ссылку, и ни одна из четырёх форм не была выразима вовсе — то есть это не «ужесточение старой
// двери», а дверь для режима, который эта же волна и завела.
//
// ⚠ СПРАШИВАЕТСЯ С ДЕЙСТВУЮЩИХ ПАРАМЕТРОВ, А НЕ С СООБЩЕНИЯ КЛИЕНТА, И ЭТО ОТЛИЧАЕТ ЭТУ ДВЕРЬ ОТ
// ГРАНИЦ ПОЛОК. Там довод был «унаследованный адрес законно устаревает, и вечный отказ клиенту,
// который ничего не присылал, — это дефект»; здесь наоборот: неработоспособная ФОРМА прогона
// остаётся неработоспособной и на реране, а замороженный прогон прода, у которого ткань совпадает
// с фотографией, обязан быть остановлен ЗДЕСЬ — иначе новый бинарь повторит его и заплатит за
// картинку дважды. Отказ бесплатный и называет поле; клиент чинит его, прислав params.
func designRefuseUnworkableRecolourCloth(params *pb_common.DesignRunParams) error {
	c := params.GetColour()
	colourStated := strings.TrimSpace(c.GetCode()) != "" || strings.TrimSpace(c.GetHex()) != "" ||
		strings.TrimSpace(c.GetWords()) != ""
	pictured := designClothsWithPicture(c)

	if !colourStated && len(pictured) == 0 {
		// ДВА РАЗНЫХ ОТКАЗА НА ОДНОМ ЭТАЖЕ, И РАЗЛИЧАЕТ ИХ ОДИН ВОПРОС: назвал ли человек ткань
		// вообще. Назвал и она без картинки — чинится выбором другой плитки; не назвал ничего —
		// чинится любым из четырёх написаний.
		if len(c.GetFabrics()) > 0 {
			return designRefusal(codes.InvalidArgument, "cloth_without_picture",
				"a cloth stated in words alone cannot be laid on a photograph: every cloth in "+
					"params.colour.fabrics has media_id 0, and no colour was stated either. Pick a "+
					"pattern that has a picture, or state a colour. Nothing was reserved and nothing "+
					"was charged", nil)
		}
		return designRefusal(codes.InvalidArgument, "no_target_colour",
			"a recolour needs the cloth to re-dress the garment IN: state params.colour.code, "+
				"params.colour.hex or params.colour.words — or a cloth with a picture in "+
				"params.colour.fabrics. Nothing was reserved and nothing was charged",
			nil)
	}

	// ─── B1: ОДНА ТКАНЬ, И ЭТО ПРО ТО, ЧТО ПРОМПТ УМЕЕТ ОБЪЯСНИТЬ ───
	//
	// Владелец (J-31) просит одну вещь: «загрузить несколько фото на модели в нашей вещи и выбрать
	// и или паттерн/цвет». Ткань — ОДНА, цвет — ОДИН, фотографий сколько угодно.
	//
	// ⚠ ПОЧЕМУ ЭТО ОТКАЗ, А НЕ ВТОРОЙ АБЗАЦ В РЕМЕСЛЕ. reclothCraft называет РОВНО ОДНУ картинку —
	// «the garment made of the cloth in image 2». Две ткани уехали бы ТРЕМЯ картинками в одном
	// платном вызове, третья не была бы упомянута ни разу, и модель законно взяла бы её как
	// материал для композиции. Написать список «CLOTH 1 … CLOTH 2» по образцу renderClothLines
	// значило бы завести фичу, которой никто не просил, и оставить без ответа второй вопрос,
	// который список немедленно задаёт: КАКИЕ ЧАСТИ изделия из какой ткани. У рендера на него
	// отвечает разметка флэтов (`parts`); у фотографии на модели такой разметки нет вовсе.
	//
	// ⚠ И ТРЕТЬЯ ПОЛОМКА ТОЙ ЖЕ ФОРМЫ — РАППОРТ. Он берётся у ПЕРВОЙ ткани с repeat_mm > 0 и
	// приписывается «its pattern», то есть паттерн + гладкая ткань читались бы как один раппорт на
	// всё изделие.
	if len(pictured) > 1 {
		return designRefusal(codes.InvalidArgument, "one_cloth_only",
			fmt.Sprintf("a recolour re-dresses the garment in ONE cloth, and this run names %d with a "+
				"picture: every one of them would ride into every paid call as another image, and the "+
				"instruction names exactly one («the garment made of the cloth in image 2»). Leave one "+
				"cloth in params.colour.fabrics with a media_id. Nothing was reserved and nothing was "+
				"charged", len(pictured)),
			map[string]string{"pictured_cloths": strconv.Itoa(len(pictured))})
	}

	// ─── B3: ФОТОГРАФИЯ И ТКАНЬ — РАЗНЫЕ КАРТИНКИ ───
	//
	// ⚠ ИНАЧЕ ЗА ОДНУ КАРТИНКУ ПЛАТЯТ ДВАЖДЫ В ОДНОМ ВЫЗОВЕ. Медиа, названное И в
	// `extra_input_media_ids`, И тканью, даёт вызов `[9.png, 9.png]`: модель просят вернуть ТУ ЖЕ
	// ФОТОГРАФИЮ лоскута, переодетую в него же. Ссылка уходит поставщику дважды и оплачивается
	// дважды как вход.
	//
	// ⚠ ОТКАЗ, А НЕ ТИХОЕ ВЫБРАСЫВАНИЕ ТКАНИ ИЗ СПИСКА, И ВЫБОР ЗДЕСЬ ВЫНУЖДЕННЫЙ. Ремесло
	// ветвится по ЗАМОРОЖЕННЫМ params (`clothsWithTexture`), а список вложений строит воркер:
	// выбросив ткань в воркере, мы получили бы промпт, который говорит «the cloth in image 2» там,
	// где картинки 2 нет вовсе. Промпт и вложения обязаны сходиться, и единственная форма, при
	// которой они сходятся, — не начинать такой прогон.
	if dup := designClothAlsoAPhotograph(params); dup > 0 {
		return designRefusal(codes.InvalidArgument, "cloth_is_also_a_photograph",
			fmt.Sprintf("media %d is named BOTH as a photograph to recolour (params.extra_input_media_ids) "+
				"and as the cloth to lay on it (params.colour.fabrics): that call would carry the same "+
				"picture twice and ask for it to be re-dressed in itself. Name a different cloth. "+
				"Nothing was reserved and nothing was charged", dup),
			map[string]string{"media_id": strconv.Itoa(dup)})
	}

	// ─── B2: ОДИН ВЫЗОВ ОБЯЗАН ПОМЕЩАТЬСЯ В ПОТОЛОК ПОСТАВЩИКА ───
	//
	// ⚠ ПОТОЛОК СЧИТАЕТСЯ ПО ВЫЗОВУ, А НЕ ПО ПРОГОНУ, и до J-31 этого различия не существовало:
	// вызов перекраса нёс одну ссылку при любом числе фотографий. Теперь он несёт `1 + ткани`, и
	// прогон, перевалив потолок, зарезервировал бы деньги, встал бы в очередь, был бы захвачен и
	// умер бы НЕПОВТОРИМО с отказом поставщика `bad_request` — без единого предложения, называющего
	// починку. Списания при этом нет (orimages отказывает ЛОКАЛЬНО, до сети, и резерв
	// освобождается терминальным переходом), но всякая другая неработоспособная форма в этой двери
	// отказывается бесплатно и словами, и эта обязана вести себя так же.
	//
	// ЧИСЛО БЕРЁТСЯ У ПОСТАВЩИКА, А НЕ ПЕРЕПИСЫВАЕТСЯ СЮДА: копия разошлась бы с потолком молча в
	// ту самую сторону, в которую дороже. B1 делает эту ветку почти недостижимой — почти, потому
	// что потолок общий, и маршрут рендера его двигает.
	return designRefuseOversizedRecolourCall(len(pictured))
}

// designRefuseOversizedRecolourCall — помещается ли ОДИН вызов перекраса в потолок поставщика.
//
// ⚠ ОТДЕЛЬНОЙ ФУНКЦИЕЙ, ПОТОМУ ЧТО ЧЕРЕЗ ДВЕРЬ ОНА НЕДОСТИЖИМА, А СТОРОЖЕМ БЫТЬ ОБЯЗАНА. Правило
// «одна ткань» (B1) выше строже и ловит тот же вход первым — и это правильный порядок: «оставь
// одну ткань» человеку исполнимее, чем «их слишком много для вызова». Но потолок ОБЩИЙ с
// маршрутом рендера и однажды поедет, а правило «одна ткань» — продуктовое и может смягчиться;
// сторож, который нельзя вызвать отдельно, был бы сторожем, которого нельзя и измерить.
//
// ЧИСЛО БЕРЁТСЯ У ПОСТАВЩИКА, А НЕ ПЕРЕПИСЫВАЕТСЯ СЮДА: копия разошлась бы с потолком молча и в
// ту сторону, в которую дороже.
func designRefuseOversizedRecolourCall(pictured int) error {
	n := 1 + pictured
	if n <= orimages.MaxInputReferences {
		return nil
	}
	return designRefusal(codes.InvalidArgument, "too_many_call_images",
		fmt.Sprintf("one recolour call carries the photograph plus every stated cloth — %d images "+
			"here — and this provider takes at most %d per call. Nothing was reserved and nothing "+
			"was charged", n, orimages.MaxInputReferences),
		map[string]string{
			"images":  strconv.Itoa(n),
			"ceiling": strconv.Itoa(orimages.MaxInputReferences),
		})
}

// designClothAlsoAPhotograph — медиа, названное И фотографией к перекрасу, И тканью. 0 = такого нет.
//
// ПЕРВОЕ СОВПАДЕНИЕ, А НЕ ВСЕ: отказ называет ОДИН номер, потому что чинится он одним жестом, и
// список из трёх повторов той же ошибки читается хуже, чем один пример.
func designClothAlsoAPhotograph(params *pb_common.DesignRunParams) int {
	photos := make(map[int]struct{}, len(params.GetExtraInputMediaIds()))
	for _, id := range params.GetExtraInputMediaIds() {
		if id > 0 {
			photos[int(id)] = struct{}{}
		}
	}
	for _, f := range params.GetColour().GetFabrics() {
		if id := int(f.GetMediaId()); id > 0 {
			if _, dup := photos[id]; dup {
				return id
			}
		}
	}
	return 0
}

// designClothsWithPicture — ткани рецепта, которые ФИЗИЧЕСКИ уедут в вызов: у них есть картинка.
func designClothsWithPicture(c *pb_common.DesignColourRecipe) []*pb_common.DesignFabricUse {
	out := make([]*pb_common.DesignFabricUse, 0, len(c.GetFabrics()))
	for _, f := range c.GetFabrics() {
		if f.GetMediaId() > 0 {
			out = append(out, f)
		}
	}
	return out
}

// designAnyClothWithPicture — есть ли в рецепте ткань, которую МОЖНО ПОКАЗАТЬ модели.
//
// КАРТИНКА, А НЕ ИМЯ И НЕ asset_id. Ткань уезжает в перекрас ВТОРОЙ КАРТИНКОЙ вызова
// (designgen/images.go: refs = [фото, ...ткани]); ткань без `media_id` не уезжает никуда, и
// считать её целью значило бы открыть дверь прогону, у которого цели нет.
//
// ОДНА ФУНКЦИЯ НА ДВЕРЬ И НА ВОРКЕРА ПО СМЫСЛУ, НО НЕ ПО КОДУ: воркер читает ЗАМОРОЖЕННЫЙ снимок
// (designgen: clothsWithTexture), дверь — сообщение клиента; типы разные, вопрос один, и он
// записан здесь теми же словами.
func designAnyClothWithPicture(c *pb_common.DesignColourRecipe) bool {
	for _, f := range c.GetFabrics() {
		if f.GetMediaId() > 0 {
			return true
		}
	}
	return false
}

// designHasRenderForColorway — открыта ли дверь 3D ДЛЯ ЭТОГО КОЛОРВЕЯ. Множество приезжает из
// GetBand (вся карточка, DISTINCT по колорвею ЗАНЯТЫХ render-слотов); 0 в нём — неатрибутированный
// легаси-верстак, открывающий только безколорвейное 3D. Членство, а не непустота: занятый верстак
// чужого колорвея открыл бы дверь прогону, чей отбор плит (designSelectBench) вернёт пустоту, — то
// есть оплаченному прогону без входов.
func designHasRenderForColorway(set []int, colorwayID int) bool {
	for _, cw := range set {
		if cw == colorwayID {
			return true
		}
	}
	return false
}

// designRefuseThreedWithoutFront — ПЕРЁД ОБЯЗАТЕЛЕН, И СПРАШИВАЕТСЯ ОН У СНИМКА.
//
// ЧТО ИМЕННО ПРОВЕРЯЕТСЯ. `inputs.slots` — это то, что прогон СОБСТВЕННО повезёт: у свежего
// прогона его собрал designSelectBench из занятых render-слотов колорвея, у рерана он переписан со
// строки родителя целиком. Оба случая отвечают на один вопрос одинаково, и ровно этот вопрос через
// тик задаст воркер (designgen.threedPictures): силуэтная сторона `front` с непустым media_id.
//
// ⚠ РОД КАДРА ЗДЕСЬ НЕ СПРАШИВАЕТСЯ, И ЭТО НЕ ПРОПУСК. Снимок не несёт рода плиты вовсе, а тот,
// кто его собирал, уже сузил верстак до `kind='render'` (designSelectBench). Спросить здесь второй
// раз было бы вторым написанием того же правила — и разошлось бы оно молча, потому что расхождение
// выглядит как лишний отказ на законном прогоне, а не как ошибка.
//
// ПОЧЕМУ ЗДЕСЬ НЕТ ОТДЕЛЬНОГО `IsDesignSilhouetteView`, ХОТЯ У ВОРКЕРА ОН ЕСТЬ. Он тут ИНЕРТЕН, и
// это замерено: игла, снимавшая его, оставалась зелёной на всех пробах — сравнение с константой
// `front` уже отвечает на силуэтный вопрос, потому что `front` силуэтной стороной ЯВЛЯЕТСЯ, а слот
// детали носит `view_key = 'detail'` по построению (createDetailSlot — единственный его писатель).
// У воркера предикат несёт настоящий вес: он строит СПИСОК видов и обязан выбросить деталь-рендер,
// иначе тот станет пятой картинкой у провайдера. Здесь список не строится, спрашивается ровно одна
// сторона. Строка, которую не может провалить ни одна мутация, — это не пояс, а декорация, и
// оставленная она означала бы покрытие, которого нет. Проба на верстак из одной ДЕТАЛИ при этом
// остаётся: она стережёт ПОВЕДЕНИЕ («деталь передом не считается»), а не эту строку.
func designRefuseThreedWithoutFront(kind string, cardID int, params *pb_common.DesignRunParams,
	inputs *pb_common.DesignInputSnapshot) error {
	if kind != entity.DesignRunKindThreed {
		return nil
	}
	for _, sl := range inputs.GetSlots() {
		if sl.GetMediaId() > 0 && sl.GetViewKey() == entity.DesignViewFront {
			return nil
		}
	}
	cw := int(params.GetColorwayId())
	return designRefusal(codes.FailedPrecondition, entity.DesignErrorCodeNoFrontRender,
		fmt.Sprintf("3D is built from the fabric render slots, and FRONT is the one it cannot do "+
			"without: the provider is handed the front as the primary view and rejects a build that "+
			"has none. Colourway %d (0 = the colourway-less bench) has renders on its bench but not "+
			"on FRONT. Put a render into the FRONT slot on FABRIC RENDER. Nothing was reserved and "+
			"nothing was charged", cw),
		map[string]string{
			"tech_card_id": strconv.Itoa(cardID),
			"colorway_id":  strconv.Itoa(cw),
			"view_key":     entity.DesignViewFront,
		})
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

	// ─── КАРТА ЦВЕТА ЗАМЕРЗАЕТ НОМЕРОМ, А НЕ КАРТИНКОЙ ───
	//
	// `media`/`deleted` на DesignColourMap — поля ЧТЕНИЯ: их заполняет полоса, отдавая план
	// (joinDesignColourPlanMedia), и клиент, собравший рецепт прогона из только что прочитанного
	// плана, вернёт их сюда не по злому умыслу, а потому что честно echo'ит полученное. Замороженные
	// `params` — не то место, и довод записан в контракте дословно: «IDS ARE STORED, MediaFull IS
	// SERVED» — объекты переезжают, поэтому URL, вмёрзший в историю, однажды перестанет быть той
	// картинкой, а строка прогона будет уверенно показывать чужую.
	//
	// СТИРАНИЕ, А НЕ ОТКАЗ: наказывать клиента за круг «прочитал план → запустил прогон» значило бы
	// сделать отказом самый обычный порядок работы. А заодно это ПРЕВРАЩАЕТ ПУСТОТУ ЭТИХ ПОЛЕЙ НА
	// ЗАМОРОЖЕННОЙ КОПИИ В СВОЙСТВО: раз записать их в `params` физически нельзя, пустой `media` на
	// рецепте прогона всегда значит «картинки здесь нет» — ровно то же, что и на плане, — и ни одно
	// из двух мест, где живёт это сообщение, не читается как «картинка есть».
	for _, m := range params.GetColour().GetColourMaps() {
		m.Media, m.Deleted = nil, false
	}

	// ─── КОЛОРВЕЙ НАСЛЕДУЕТСЯ ПОШТУЧНО, А НЕ ВМЕСТЕ СО ВСЕМ СНИМКОМ (T2) ───
	//
	// Наследование выше — ОПТОМ ИЛИ НИКАК: родительские params читаются, только если клиент не
	// прислал СВОИХ. Для всех прочих полей это верно, а для колорвея — ловушка, которую эта же
	// волна диагностирует своими словами пятьюдесятью строками выше, у DesignAsset.colorway_id:
	// голый proto3-скаляр приезжает НУЛЁМ от всякого клиента, который поля не знает, и от всякого
	// сохранения по НЕСВЯЗАННОЙ причине. Там довод применили и завели отдельный глагол; здесь —
	// нет, и «реран наследует колорвей» оставалось правдой ровно до первого рерана, тронувшего
	// `ask`.
	//
	// ЧЕМ ЭТО ПЛАТИЛОСЬ, ПОШАГОВО. Карточка с легаси-рендерами (множество [0,5]), прогон 900 —
	// турнтейбл колорвея 5, человек повторяет его и правит `ask`. Колорвей приезжает нулём →
	// ворота проверяют членство НУЛЯ, а он в множестве есть, значит ОТКРЫВАЮТСЯ → входы
	// копируются из снимка родителя целиком, то есть модель получает плиты колорвея 5 → строка
	// пишется с NULL → кадры рождаются неатрибутированными и в threed-верстак колорвея 5 их уже
	// не поставить (colorway_mismatch). Деньги потрачены, результат некуда положить, а строка
	// противоречит сама себе: её inputs описывают цвет, которого её колонка не называет.
	//
	// СЕМАНТИКА — НАСЛЕДОВАНИЕ, и это ровно то, что обещает комментарий у params.colorway_id.
	// ⚠ ЦЕНА НАЗВАНА: «повтори этот прогон, но БЕЗ колорвея» через реран невыразимо — нулём это
	// сказать больше нельзя. Оно и не нужно: прогон без колорвея читает другой верстак и
	// собирается из других плит, то есть это не повтор, а новый прогон, и заводится он обычной
	// дверью. Обратный выбор (замена) стоил бы дороже и молча — см. абзац выше.
	if in != nil && parent != nil && params.GetColorwayId() == 0 && len(parent.Params) > 0 {
		inherited := &pb_common.DesignRunParams{}
		if err := designUnmarshalJSON(parent.Params, inherited); err != nil {
			return nil, status.Errorf(codes.FailedPrecondition,
				"run %d cannot be rerun: its stored parameters do not parse", parent.Id)
		}
		params.ColorwayId = inherited.GetColorwayId()
	}

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
func designRefuseForeignDetailSlots(cardID int, spoken *pb_common.DesignRunParams, bench []entity.DesignBenchSlot, src designInputSources) error {
	// ⚠ ГРАНИЦА ЗДЕСЬ — ЭТО НЕ ТОЛЬКО КАРТОЧКА (N5). Отбор ниже сужает верстак ещё и колорвеем
	// (designBenchColorwayScope), поэтому названный адрес чужого колорвея ПРИНИМАЛСЯ дверью, а
	// потом молча выпадал из снимка: клиент получал OK на просьбу, которую никто не исполнил, и
	// платил за прогон, в котором названной им детали нет. «Принято и не исполнено» — ровно тот
	// класс, от которого эта волна отказалась на всех остальных дверях; отказывать надо ТАМ, ГДЕ
	// СКАЗАЛИ, а не выбрасывать молча там, где отбирают.
	//
	// Предикат ОБЯЗАН совпадать с отбором, поэтому он и берётся из того же designBenchColorwayScope,
	// а не пишется вторым мнением: разойдясь, они дали бы либо ложный отказ, либо ту же тихую
	// потерю обратно.
	matchColorway, wantColorway := designBenchColorwayScope(src)
	details := make(map[int]struct{}, len(bench))
	for _, slot := range bench {
		if slot.TechCardId != cardID || slot.ViewKey != entity.DesignViewDetail {
			continue
		}
		if matchColorway && entity.DesignColorwayOrNone(slot.ColorwayId) != wantColorway {
			continue
		}
		details[slot.Id] = struct{}{}
	}
	for i, id := range spoken.GetDetailSlotIds() {
		if _, ok := details[int(id)]; !ok {
			return status.Errorf(codes.InvalidArgument,
				"params.detail_slot_ids.%d %d is not a detail slot of tech card %d that this run can use "+
					"(a 3D run reads only its own colourway's bench)", i, id, cardID)
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
	shelf := designShelfIDs(cardID, assets)
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

// designShelfIDs — какие полки принадлежат ЭТОЙ карточке, множеством.
//
// ОДНА ЛЕСТНИЦА НА ВСЕ АДРЕСА ПОЛОК В ЗАПРОСЕ. Их теперь два рода — ткани рецепта и источник
// плитки, — и второе построение того же множества рядом с первым было бы вторым местом, где
// однажды забудут сравнить карточку.
func designShelfIDs(cardID int, assets []entity.DesignAsset) map[int]struct{} {
	shelf := make(map[int]struct{}, len(assets))
	for _, a := range assets {
		if a.TechCardId == cardID {
			shelf[a.Id] = struct{}{}
		}
	}
	return shelf
}

// designRefuseForeignPatternSource — «из какой полки сделана эта плитка» обязано называть полку
// ЭТОЙ карточки.
//
// ⚠ ЭТО НЕ ПОВТОР ПРОВЕРКИ ТКАНЕЙ, А ТРЕТИЙ НЕЗАВИСИМЫЙ ИСТОЧНИК ЧУЖОГО НОМЕРА. Он уезжает не в
// промпт, а в КОЛОНКУ: `design_asset.derived_from_asset_id` сажаемого паттерна. FK говорит «какая-то
// строка design_asset», а не «одна из ЭТОЙ карточки», — то есть без этой проверки паттерн одного
// стиля повис бы на ткани другого, и схема приняла бы это молча (тот же довод стоит в
// store/design/assets.go у UpsertAsset). Стор проверит то же в своей транзакции; здесь — ДО денег.
//
// СПРАШИВАЕТСЯ С ГОВОРЯЩЕГО, а не с действующих параметров: полку законно удаляют, а params
// родителя заморожены — проверка унаследованного id сделала бы прогон неперезапускаемым навсегда.
func designRefuseForeignPatternSource(cardID int, spoken *pb_common.DesignRunParams, assets []entity.DesignAsset) error {
	id := int(spoken.GetPattern().GetSourceAssetId())
	if id == 0 {
		// 0 = «источник не с полки»: файл из библиотеки, вставка из буфера. Обычный случай.
		return nil
	}
	for _, a := range assets {
		if a.Id != id || a.TechCardId != cardID {
			continue
		}
		// ⚠ И ПОЛКА ОБЯЗАНА БЫТЬ ТОЙ, КОТОРУЮ НАЗЫВАЕТ КОНТРАКТ. `source_asset_id` объявлен как
		// «design_asset(id) of THIS card, kind fabric|pattern», и запись едет в
		// `derived_from_asset_id`, чей собственный контракт говорит «set on a pattern made from a
		// fabric». Фурнитура родителем паттерна быть не может ни в одном чтении: «этот принт
		// сделан из молнии» — предложение без смысла, а строка с ним переживает прогон навсегда.
		// Схема этого не ловит — FK знает только «какая-то строка design_asset».
		if a.Kind != entity.DesignAssetKindFabric && a.Kind != entity.DesignAssetKindPattern {
			return status.Errorf(codes.InvalidArgument,
				"params.pattern.source_asset_id %d is a %s row of tech card %d, and a pattern is made "+
					"from a cloth or from another pattern", id, a.Kind, cardID)
		}
		return nil
	}
	return status.Errorf(codes.InvalidArgument,
		"params.pattern.source_asset_id %d is not a shelf row of tech card %d", id, cardID)
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

// designColourMapMediaIDs — картинки карт цвета, то есть ровно то, что цепляет снимок. Подложка
// (`base_media_id`) сюда НЕ входит: она поставщику не уезжает, это метка устаревания для клиента.
func designColourMapMediaIDs(c *pb_common.DesignColourRecipe) []int {
	out := make([]int, 0, len(c.GetColourMaps()))
	for _, m := range c.GetColourMaps() {
		out = append(out, int(m.GetMediaId()))
	}
	return out
}

// designRefuseMalformedColourMaps — ФОРМА КАРТ ЦВЕТА У ДВЕРИ ПРОГОНА.
//
// ⚠ ПОЧЕМУ ЭТО ПРОВЕРЯЕТСЯ ДВАЖДЫ — ЗДЕСЬ И В ПЛАНЕ. Это НЕ повтор: план и прогон — две
// независимые двери, и прогон законно запускают, не сохранив плана вовсе (клиент собирает рецепт
// сам, скрипт — тем более). Кривой вид или ярлык из этой двери замерзает в `params` НАВСЕГДА:
// промпт печатает `viewWord(view)` и `colourWord(hex)`, и «front-ish» доехал бы до модели словом,
// которого она не знает, на оплаченном вызове.
//
// ⚠ И ЯРЛЫК ТКАНИ ПРОВЕРЯЕТСЯ ТОЖЕ. `map_hex` — это КЛЮЧ, по которому ткань находит свои детали на
// карте; «#3A7BD5» против «#3a7bd5» означал бы ткань, потерявшую детали, без единого сообщения.
//
// ═══ ЧЕТЫРЕ ВОПРОСА, КОТОРЫХ ЗДЕСЬ НЕ ЗАДАВАЛОСЬ, И ЦЕНА КАЖДОГО (адверсарное ревью) ══════════
//
// Проверялась ОДНА ФОРМА и только она, поэтому мимо двери проходили четыре рецепта, каждый из
// которых заставлял ПЛАТНЫЙ промпт утверждать неправду. Все четыре замерены на собранном промпте:
//
//   - ЯРЛЫК БЕЗ ЕДИНОЙ КАРТЫ: строки тканей говорили «used on the parts painted steel blue
//     (#3a7bd5) on the colour map» при пустом `colour_maps` — предложения про карту и самой карты
//     в запросе не было вовсе;
//   - ЯРЛЫК, КОТОРОГО НЕТ НИ НА ОДНОЙ ПАЛИТРЕ: карта уехала, но такого цвета на ней никто не
//     красил, и модель ищет область, которой нет. Палитра — ЗАМКНУТОЕ множество ярлыков (см.
//     DesignColourSwatch), и вопрос «есть ли этот ярлык» отвечается только по ней;
//   - ДВЕ КАРТЫ НА ОДИН `media_id`: список вложений дедуплицирует по номеру медиа и СКЛЕИВАЕТ
//     подписи, поэтому одна картинка объявлялась картой двух разных видов, а предложение читалось
//     «Images 3 and 3»;
//   - ДВЕ ТКАНИ НА ОДИН `map_hex`: две строки заявляли «used on the parts painted steel blue …
//     and on no other part» — два взаимно исключающих утверждения об одной области, оба
//     абсолютные. У ПЛАНА этот дубль отвергался с первого дня (entity.DesignColourPlanSave), у
//     двери прогона эквивалента не было.
//
// Промпт с тех пор молчит про карту, которой у модели нет (designgen: colourMapsSent), но молчание
// — это не то, о чём просил человек: он просил разложить ткани по покрашенным деталям. Поэтому
// здесь ОТКАЗ СЛОВАМИ И ДО ДЕНЕГ, а не тихо усечённый промпт за полную цену.
func designRefuseMalformedColourMaps(spoken *pb_common.DesignRunParams) error {
	views := make(map[string]struct{})
	pictures := make(map[int]int)
	painted := make(map[string]struct{})
	maps := spoken.GetColour().GetColourMaps()
	if n := len(maps); n > entity.MaxDesignColourMaps {
		return status.Errorf(codes.InvalidArgument,
			"params.colour.colour_maps names %d maps; the ceiling is %d", n, entity.MaxDesignColourMaps)
	}
	for i, m := range maps {
		if !entity.IsDesignSilhouetteView(m.GetView()) {
			return status.Errorf(codes.InvalidArgument,
				"params.colour.colour_maps.%d.view %q is not a silhouette view", i, m.GetView())
		}
		if _, dup := views[m.GetView()]; dup {
			return status.Errorf(codes.InvalidArgument,
				"params.colour.colour_maps.%d names view %q a second time; one map per view",
				i, m.GetView())
		}
		views[m.GetView()] = struct{}{}
		if m.GetMediaId() <= 0 {
			return status.Errorf(codes.InvalidArgument,
				"params.colour.colour_maps.%d.media_id %d — a map is a picture", i, m.GetMediaId())
		}
		// ОДНА КАРТИНКА — ОДНА КАРТА. Дубль ВИДА отвергался строкой выше, дубль КАРТИНКИ не
		// отвергался ничем, а следствие у него хуже: вид хотя бы называется дважды честно, а
		// одна картинка, объявленная картой двух видов, приезжает к модели с одним номером на два
		// вида и склеенной подписью.
		if at, dup := pictures[int(m.GetMediaId())]; dup {
			return status.Errorf(codes.InvalidArgument,
				"params.colour.colour_maps.%d names picture %d, which colour_maps.%d already is: "+
					"one picture is one map, and a single image declared to be the map of two views "+
					"reaches the model as «Images N and N»", i, m.GetMediaId(), at)
		}
		pictures[int(m.GetMediaId())] = i
		for j, sw := range m.GetPalette() {
			if !entity.IsDesignColourMapHex(sw.GetHex()) {
				return status.Errorf(codes.InvalidArgument,
					"params.colour.colour_maps.%d.palette.%d.hex %q is not a lower-case #rrggbb label",
					i, j, sw.GetHex())
			}
			painted[sw.GetHex()] = struct{}{}
		}
	}
	claimed := make(map[string]int)
	for i, f := range spoken.GetColour().GetFabrics() {
		hex := f.GetMapHex()
		if hex == "" {
			continue
		}
		if !entity.IsDesignColourMapHex(hex) {
			return status.Errorf(codes.InvalidArgument,
				"params.colour.fabrics.%d.map_hex %q is not a lower-case #rrggbb label", i, hex)
		}
		if len(maps) == 0 {
			return status.Errorf(codes.InvalidArgument,
				"params.colour.fabrics.%d.map_hex %s addresses a colour map, and this run carries "+
					"none: the cloth would name a picture the model was never shown. Send the "+
					"painted view in params.colour.colour_maps, or state this cloth's parts in words",
				i, hex)
		}
		if _, ok := painted[hex]; !ok {
			return status.Errorf(codes.InvalidArgument,
				"params.colour.fabrics.%d.map_hex %s is on the palette of no colour map of this "+
					"run: nobody painted that colour, so the cloth would be sent to look for a "+
					"region that is not there", i, hex)
		}
		if at, dup := claimed[hex]; dup {
			return status.Errorf(codes.InvalidArgument,
				"params.colour.fabrics.%d.map_hex %s is already claimed by fabrics.%d: both lines "+
					"would say «used on the parts painted that colour — and on no other part», which "+
					"is two absolute claims over one region. The hex is the key, one cloth per label",
				i, hex, at)
		}
		claimed[hex] = i
	}
	return nil
}

// designRefuseColourMapAlsoAnInput — КАРТА ЦВЕТА НЕ МОЖЕТ БЫТЬ ПЛИТОЙ ВЕРСТАКА, РЕФЕРЕНСОМ, ЛИШНИМ
// ВХОДОМ ИЛИ ЛОСКУТОМ ТКАНИ.
//
// ⚠ ЭТО ТОТ ЖЕ КЛАСС, ЧТО designClothAlsoAPhotograph, И ФОРМА СТОРОЖА ВЗЯТА У НЕГО. Там медиа,
// названное И фотографией к перекрасу, И тканью, давало вызов «переодень эту картинку в неё же».
// Здесь — хуже: `media_id` карты и `base_media_id` карты соседи на одном сообщении, а база это и
// есть флэт, поэтому опечатка в ОДНО поле объявляет картой ПЛИТУ. Замер ревью: одна картинка,
// подписанная одновременно «current state of the garment — front view» и «colour map … those
// colours LABEL which cloth covers which part», то есть ровно тот провал, ради предотвращения
// которого блок подписей и написан.
//
// ⚠ ОТКАЗ, А НЕ ТИХОЕ РАЗЖАЛОВАНИЕ КАРТЫ, И ВЫБОР ТОТ ЖЕ, ЧТО У СОСЕДА. Список вложений роль
// картинки уже не удваивает (designgen: refCaption.IsColourMap), так что промпт не соврёт, — но
// прогон при этом молча сделает НЕ ТО, о чём просили: ткани останутся без адреса, а человек
// заплатит за картинку, которую считал картой. Одна опечатка чинится одним жестом, если её
// назвать.
//
// СТОИТ ПОСЛЕ СБОРКИ ВХОДОВ И ДО РЕЗЕРВА, по тому же доводу, что два соседних сторожа: раньше
// плит и референсов ещё не существует (у рерана они и вовсе переписаны со строки родителя), позже
// — деньги дня уже заняты.
func designRefuseColourMapAlsoAnInput(params *pb_common.DesignRunParams, inputs *pb_common.DesignInputSnapshot) error {
	maps := params.GetColour().GetColourMaps()
	if len(maps) == 0 {
		return nil
	}
	byID := make(map[int]designInputMediaRef, 16)
	for _, ref := range designRunInputMediaRefs(params, inputs) {
		byID[ref.ID] = ref
	}
	for i, m := range maps {
		ref, clash := byID[int(m.GetMediaId())]
		if !clash {
			continue
		}
		return designRefusal(codes.InvalidArgument, "colour_map_is_also_an_input",
			fmt.Sprintf("media %d is named BOTH as %s and as the colour map of the %s view "+
				"(params.colour.colour_maps.%d.media_id): one picture cannot be a drawing OF the "+
				"garment and a sheet of labels ABOUT it in the same request. A colour map is its own "+
				"upload — check that colour_maps.%d.media_id is not the flat it was painted over, "+
				"which belongs in base_media_id. Nothing was reserved and nothing was charged",
				ref.ID, ref.Where, m.GetView(), i, i),
			map[string]string{
				"media_id": strconv.Itoa(ref.ID),
				"also":     ref.Where,
				"view":     m.GetView(),
			})
	}
	return nil
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

// designIntsToInt32s — обратный переход, домен → провод. Пустой вход даёт nil, а не пустой слайс:
// в proto3 повторяемое поле из нуля членов и отсутствующее поле — одно и то же состояние, и
// заводить пустой слайс значило бы утверждать разницу, которой на проводе нет.
func designIntsToInt32s(in []int) []int32 {
	if len(in) == 0 {
		return nil
	}
	out := make([]int32, 0, len(in))
	for _, v := range in {
		out = append(out, int32(v))
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
	case entity.DesignRunKindPattern:
		// ОДНА ПЛИТКА ИЗ ОДНОЙ КАРТИНКИ. Число здесь не выводится из длины списка входов ровно
		// потому, что список обязан быть длиной один, и это проверено отдельно, у двери.
		return 1
	case entity.DesignRunKindRecolor:
		// СКОЛЬКО СНИМКОВ ДАЛИ — СТОЛЬКО КАДРОВ И ПЛАТНЫХ ВЫЗОВОВ. Владелец грузит фото «с разных
		// сторон», и каждое из них — отдельная правка отдельной картинки: `n` у провайдера
		// возвращает n ВАРИАНТОВ ОДНОГО промпта, а не n разных кадров, так что четыре снимка это
		// четыре вызова. Число обязано совпадать с тем, что построит imageCalls, иначе плитка-
		// плейсхолдер останется незаполненной и прочитается как потерянный результат.
		if n := len(params.GetExtraInputMediaIds()); n > 0 {
			return n
		}
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
	// ─── ТА ЖЕ ДВЕРЬ ДЛЯ ДОСКИ, И ТОЖЕ ДО ДЕНЕГ ───
	//
	// Адреса уже на руках, поэтому второго запроса в медиа здесь нет; политику читает та же
	// функция, что и картиночный прогон (design_input_format.go). Доска не приходит с провода, но
	// .glb, прицепленный к ней, уезжает в слот картинки того же платного вызова — вопрос «что это
	// за файл» от источника не зависит.
	if ref, ct, bad := designFirstNonPictureInput(designBoardMediaRefs(attachedIDs, boardURLs)); bad {
		return nil, designNonPictureRefusal(ref, ct)
	}
	// И ТА ЖЕ ДВЕРЬ «ТОЛЬКО ДЛЯ ПОКАЗА» (0361, D-24), тоже до денег: медиа кадра, помеченного так,
	// человек может положить на доску, и оно уехало бы в платный вызов, минуя полосу целиком.
	if err := s.designRefuseDisplayOnlyInputs(ctx, designBoardMediaRefs(attachedIDs, boardURLs)); err != nil {
		return nil, err
	}

	// ─── ПУСТАЯ ДОСКА — ОТКАЗ, А НЕ ПЛАТНЫЙ ВЫЗОВ НИ О ЧЁМ ───
	//
	// ⚠ ПРОВЕРЯЕТСЯ ДОСКА, А НЕ ДЛИНА ПРОМПТА, и это разные вопросы. Промпт несёт ещё и имя
	// изделия с фитом, поэтому у любой названной карточки он непустой ВСЕГДА — сторож по его
	// длине не сработал бы никогда и оплачивал бы «придумай одежду по слову „пальто“».
	//
	// ⚠ СТОРОЖ БОЛЬШЕ НЕ СТОИТ НА ОДНИХ СЛОВАХ, И ЭТО ТА САМАЯ ПРАВКА, КОТОРУЮ ЗДЕСЬ ЖДАЛИ.
	// Прежний комментарий на этом месте объяснял, почему доска из одних картинок отвергается:
	// снимок прогона умел хранить только СЛОВА доски, поля под список показанных картинок в
	// DesignMoodSnapshot не было, и такая доска завела бы оплаченную строку истории, которая
	// утверждает, что в модель не ушло НИЧЕГО, — а ушло двенадцать изображений. Довод был верен и
	// назвал своё условие: «расширять это надо ВМЕСТЕ С ПОЛЕМ В КОНТРАКТЕ». Поле появилось
	// (DesignMoodSnapshot.media_ids), поэтому довод исчерпан, а не обойдён: снимок теперь называет
	// каждую уехавшую картинку по id и в порядке отправки, и история про потраченные деньги
	// больше не врёт. Остаётся ровно одно условие отказа — НЕ УЕХАЛО НИЧЕГО ВООБЩЕ.
	//
	// ⚠ СЧИТАЮТСЯ attachedIDs, А НЕ boardIDs. Доска из трёх плиток, чьи строки медиа удалены,
	// посылает НОЛЬ картинок; отказ по списку желаний пропустил бы её в платный вызов ни о чём.
	//
	// ПОРЯДОК: ЭТОТ СТОРОЖ ТЕПЕРЬ ПОСЛЕ РАЗРЕШЕНИЯ КАРТИНОК, потому что он о них и спрашивает.
	// Ценой этого стали более ТОЧНЫЕ отказы у соседей: доску из сорока плиток встречает потолок,
	// а доску из одного кадра «только для показа» — его собственный отказ, оба до денег.
	mood := designMoodSnapshot(card)
	if mood == nil {
		// ДОСКА ИЗ ОДНИХ КАРТИНОК — ЗАКОННОЕ СОСТОЯНИЕ, И СНИМОК ОБЯЗАН ЕГО ВЫРАЗИТЬ. Пустой
		// снимок с непустым media_ids говорит ровно то, что было: слов не было, картинки были.
		mood = &pb_common.DesignMoodSnapshot{}
	}
	// ⚠ ПИШЕТСЯ ИМЕННО ТОТ СПИСОК, ПО КОТОРОМУ НУМЕРУЕТ ПРОМПТ, И ЭТО ОДНА ПЕРЕМЕННАЯ, А НЕ ДВЕ
	// ПОХОЖИЕ. Снимок — это запись о том, что «picture 2» значило в тот день; собранный из
	// boardIDs, он называл бы картинку, которой модель не видела, и рерун по нему послал бы не то.
	mood.MediaIds = designIntsToInt32s(attachedIDs)
	// ⚠ СПРАШИВАЕТСЯ ТО, ЧТО ФАКТИЧЕСКИ ДОЕДЕТ ДО МОДЕЛИ, А НЕ ТО, ЧТО ЛЕЖИТ НА ДОСКЕ.
	//
	// Прежний сторож звучал как `mood == nil && len(attachedIDs) == 0` и объявлял инвариант «ни
	// картинок, ни слов». Ревью круга 19 показало, что он его не держит: снимок бывает НЕПУСТЫМ от
	// одной только выноски, а designBoardPromptBody выбрасывает выноску, чья картинка не уехала
	// («слова о изображении, которого модель не видит, — инструкция ни о чём»). Доска из трёх
	// плиток с удалёнными медиа и тремя записками на них проходила дверь и покупала вызов, чей
	// промпт — две строки шапки.
	//
	// ⚠ МЕРИТСЯ ИМЕННО ТЕЛО ДОСКИ, А НЕ ДЛИНА ВСЕГО ПРОМПТА, и это разные вопросы. Промпт несёт ещё
	// имя изделия с посадкой, поэтому у любой названной карточки он непустой ВСЕГДА — сторож по его
	// длине не сработал бы никогда и оплачивал бы «придумай одежду по слову „пальто“».
	//
	// ⚠ И ЭТО ТА ЖЕ ФУНКЦИЯ, ЧТО СОБИРАЕТ ПРОМПТ, А НЕ ЕЁ ПЕРЕСКАЗ. Второе мнение о том, «что
	// считается непустой доской», разошлось бы с первым в первый же раз, когда правят одно из двух.
	if strings.TrimSpace(designBoardPromptBody(mood, attachedIDs)) == "" {
		return nil, status.Error(codes.FailedPrecondition,
			"there is nothing to read: put a picture on the moodboard or write the description")
	}

	// ─── ДВЕ ФОРМЫ ОДНОГО ВОПРОСА ───
	//
	// ⚠ ВЕТКА ВЫБИРАЕТСЯ ОДИН РАЗ И СРАЗУ ЦЕЛИКОМ: роль, промпт, json-режим и потолок токенов —
	// это ОДИН договор с моделью, а не четыре независимые настройки. Разъехавшись (json-режим без
	// роли, требующей объект; потолок без проверки finish_reason), они дают ответ, который
	// формально пришёл и содержательно наполовину.
	//
	// ⚠ ОТСУТСТВУЮЩИЙ ФЛАГ ОБЯЗАН ДАВАТЬ ПРЕЖНИЕ БАЙТЫ. Старый клиент разбирает `output_text` по
	// трём заголовкам (V-19), поэтому у него не меняется ничего: та же роль, тот же промпт, тот
	// же выключенный json и тот же отсутствующий потолок.
	construction := req.GetConstruction()
	systemPrompt := draftIdeaSystemPrompt
	if construction {
		systemPrompt = designConstructionSystemPrompt
	}
	maxTokens := designDraftAnswerCeiling(construction)

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

	// ЦЕНА СЧИТАЕТСЯ ПО ЧИСЛУ УЕХАВШИХ КАРТИНОК И ПО ФОРМЕ ОТВЕТА, а не по роду прогона: см.
	// designDraftIdeaEstimate. Флаг сюда обязателен — прогон со снятым флагом не покупает ни
	// колорвеев в ответе, ни словаря цвета в запросе, и платить за них не должен.
	est := designDraftIdeaEstimate(len(attachedIDs), construction)
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
		// ⚠ СТРУКТУРНЫЙ ОТВЕТ ПЕРЕСОБИРАЕТСЯ ИЗ СОХРАНЁННОЙ СТРОКИ, И БЕЗ ЭТОГО ФИЧА ЛОМАЛАСЬ БЫ
		// ИМЕННО НА ДВОЙНОМ КЛИКЕ — то есть на том самом жесте, ради которого идемпотентность и
		// заведена. Модель здесь не зовётся, значит всё, чего нельзя прочитать обратно из
		// `output_text`, при повторе исчезает. Спрашивается САМА СТРОКА, а не флаг запроса:
		// прогон отвечен один раз и навсегда в той форме, в какой был отвечен, а флаг — это
		// свойство нажатия. Проза разбором не признаётся и даёт nil — ровно так и отличается
		// «этот прогон был структурным» от «этот был прозой».
		//
		// ─── ПРОВАЛЕННЫЙ ПРОГОН ОТДАЁТСЯ ОТКАЗОМ, А НЕ ПУСТЫМ УСПЕХОМ ───
		//
		// ⚠ ЭТО БЫЛ ТУПИК, И НАШЛО ЕГО РЕВЬЮ КРУГА 19. Предикат перехвата (designRunResumableSQL)
		// резюмирует только `pending|running`, поэтому после designFailDraftAs строка навсегда
		// остаётся `failed` — а хендлер отвечал на неё HTTP-OK с `construction: nil` и без единой
		// ошибки. Проза отказа при этом велит «draft again», то есть человек жмёт ту же кнопку, тот
		// же client_request_id приезжает второй раз и получает молчаливую пустоту вместо новостей.
		// `invalid_output` — рядовой исход дрейфа модели, значит тупик был бы штатным.
		//
		// ОТДАЁТСЯ ТОТ ЖЕ ОТКАЗ, ЧТО В ПЕРВЫЙ РАЗ, И ИМЕННО ПО КОЛОНКЕ `error_code`, а не по прозе:
		// прогон отвечен один раз и навсегда — в том числе отвечен ПРОВАЛОМ. Повторить его нечем:
		// вторая платная попытка под одним ключом идемпотентности сломала бы единственное
		// обещание, ради которого ключ существует; новое нажатие — это НОВЫЙ client_request_id, и
		// проза отказа говорит об этом словами.
		if run.Status == entity.DesignRunFailed {
			return nil, designReplayedFailure(run)
		}
		// ─── ФЛАГ ФОРМЫ НЕ ВХОДИТ В КЛЮЧ ИДЕМПОТЕНТНОСТИ, ПОЭТОМУ СВЕРЯЕТСЯ ЗДЕСЬ ───
		//
		// ⚠ ТОТ ЖЕ client_request_id С ПРОТИВОПОЛОЖНЫМ ФЛАГОМ ОТДАВАЛ РЕЗУЛЬТАТ ДРУГОЙ ФОРМЫ МОЛЧА:
		// нажатие «структурно» по ключу прозаического прогона возвращало `construction: nil` и
		// выглядело как «модель ничего не предложила», а обратное — как «клиент просил прозу, а
		// ему прислали объект». Соседняя ось (колорвей, store/design/wave2.go:designSameStartRequest)
		// ровно этот случай считает ОТКАЗОМ, и здесь ответ обязан быть тем же.
		//
		// ⚠ ФОРМА СПРАШИВАЕТСЯ У СТРОКИ, А НЕ У ЗАПРОСА, И ФЛАГ В `inputs` НЕ ПИШЕТСЯ. Снимок
		// входов — это ДОСКА (что уехало модели), а форма ответа доской не является: записав её
		// туда, мы завели бы второе, свободное разойтись мнение о том, чем прогон уже ответил.
		// Строка отвечает на этот вопрос точно: канонический JSON читается строгим разбором
		// (designConstructionDraftFromRun), проза — нет.
		//
		// СПРАШИВАЕТСЯ ТОЛЬКО У ЗАКОНЧЕННОГО ПРОГОНА. У живой лизы `output_text` пуст ЗАКОННО —
		// вызов идёт прямо сейчас в соседнем запросе, — и отказ по «форма не совпала» обвинил бы
		// человека в том, что он всего лишь нажал дважды подряд.
		stored := designConstructionDraftFromRun(run.OutputText.String)
		if run.Status == entity.DesignRunDone && construction != (stored != nil) {
			return nil, designRefusal(codes.FailedPrecondition, designReasonShapeMismatch,
				designConstructionShapeMismatchMsg, nil)
		}
		return &pb_admin.DraftDesignIdeaResponse{
			Run:          s.designRunResponse(ctx, run),
			Budget:       s.designBudgetResponse(ctx, started.Budget),
			Construction: stored,
		}, nil
	}
	if !run.ClaimToken.Valid || run.ClaimToken.String == "" {
		// Строка без захвата не принадлежит никому, и закрыть её нечем: CompleteRun сверяет
		// токен. Это не должно случаться — токен выдаёт StartRun, — поэтому и говорится вслух.
		slog.Default().ErrorContext(ctx, "draft design idea: the run carries no claim token",
			slog.Int("run_id", run.Id))
		return nil, status.Error(codes.Internal, "the idea draft could not be claimed")
	}

	// ─── ПРОМПТ СОБИРАЕТСЯ ЗДЕСЬ, ПОСЛЕ ПОВТОРА, А НЕ ДО НЕГО ───
	//
	// Ниже этой строки прогон ТОЧНО пойдёт к модели: идемпотентный повтор уже вернулся сохранённым
	// ответом, перехват уже разрешён. Всё, что нужно только живому вызову, собирается здесь —
	// иначе двойной клик, который модель не зовёт вовсе, платил бы чтением словаря цвета за
	// вопрос, который никто не задаст.
	prompt := designDraftIdeaPrompt(card, mood, attachedIDs)
	// colours — СЛОВАРЬ ЦВЕТА, ЧИТАЕМЫЙ ОДИН РАЗ НА НАЖАТИЕ И ИДУЩИЙ В ДВА МЕСТА (B-25): в промпт
	// (что модели показали) и в проверку ответа (чем ответ поверяется). ДВА чтения между вопросом
	// и разбором развели бы эти множества: цвет, заведённый или архивированный в те секунды, пока
	// шёл платный вызов, дал бы либо код, которого модели не показывали, либо отказ коду, который
	// ей показали как законный.
	var colours []entity.Color
	if construction {
		// НЕАРХИВНЫЕ, потому что архивный код нельзя дать новому продукту: предложение с ним нельзя
		// подтвердить, и показывать его модели значило бы платить за строку, которая никуда не ведёт.
		//
		// ⚠ ОШИБКА СЛОВАРЯ НЕ РОНЯЕТ ПРОГОН, А ЗАБИРАЕТ ОДИН СПИСОК. Черновик отвечает на четыре
		// вопроса, из которых цвет — один; отказать во всём нажатии из-за соседнего, независимого
		// справочника значило бы сделать его условием работы конструкции. Промпт тогда сам говорит
		// модели «списка нет — оставь color_code пустым» (designColourTokenLine), разбор кода не
		// узнаёт и обнуляет, а человек выберет его руками на блоке предложений.
		var cerr error
		if colours, cerr = s.repo.Dictionary().ListColors(ctx, false); cerr != nil {
			slog.Default().WarnContext(ctx,
				"draft design idea: the colour dictionary is unreadable; colourways will carry no code",
				slog.Int("tech_card_id", cardID), slog.String("err", cerr.Error()))
			colours = nil
		}
		prompt = designConstructionUserPrompt(card, mood, attachedIDs, colours)
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
	text, finishReason, usage, callErr := s.aiOps.CompleteWithImages(
		ctx, systemPrompt, prompt, boardURLs, construction, maxTokens)
	if callErr == nil && strings.TrimSpace(text) == "" {
		callErr = errors.New("the model returned an empty draft")
	}
	// ─── ПОТОЛОК, СЪЕДЕННЫЙ БЕЗ ОТВЕТА, — СВОЙ ИСХОД, И ОН ОПЛАЧЕН ───
	//
	// ⚠ РЕВЬЮ КРУГА 19 НАШЛО ЗДЕСЬ ТРИ ПОЛОВИНЫ ОДНОГО ДЕФЕКТА, И ЧИНИТЬ ИХ НАДО ВМЕСТЕ. Потолок
	// стоял, мышление не выключалось (починено в multimodal.go), а исход «токены потрачены, ответа
	// ноль» падал в `provider_error` с NULL-ценой: картинки сожжены, регистр денег пишет ноль,
	// человеку говорят «погода, повтори» — и он повторяет, покупая тот же ноль.
	//
	// ⚠ КОД ПРИЧИНЫ ОТДЕЛЬНЫЙ, ПО ТОМУ ЖЕ ДОВОДУ, ЧТО У `invalid_output` (см. designFailDraftAs):
	// `provider_error` значит «ответа не было», а здесь ответ БЫЛ — пустой, оплаченный и
	// детерминированный. Слепив их, мы получили бы график «поставщик падает» там, где на самом деле
	// мал наш собственный потолок. Классификация — ПО СЕНТИНЕЛУ (openrouter.ErrBudgetExhausted),
	// никогда по прозе поставщика; ровно так же это делает разбор тех-карты (techcard_analysis.go).
	//
	// ⚠ ДЕНЬГИ СПИСЫВАЮТСЯ, И ЭТО ВЫРАВНИВАНИЕ, А НЕ УЖЕСТОЧЕНИЕ. У ОДНОГО И ТОГО ЖЕ потолка два
	// исхода: половина ответа (finish_reason=length с текстом) уходит в `invalid_output` и платится
	// оценкой, а полное отсутствие ответа платилось НУЛЁМ. Токены потрачены одинаково — их
	// напечатал сам поставщик в usage, — поэтому дешевле выглядел ровно ХУДШИЙ исход, и «сжечь
	// бюджет бесплатно» было бы способом, а не аварией. Ноль остаётся ровно там, где ответа не было
	// ВОВСЕ: транспорт, 404, неверная настройка — см. designFailDraft.
	if callErr != nil {
		if errors.Is(callErr, openrouter.ErrBudgetExhausted) {
			s.designLogConstructionDraft(ctx, cardID, run.Id, finishReason, usage,
				designConstructionStats{}, callErr)
			s.designFailDraftAs(ctx, run, attempt.AttemptNo,
				callErr, designReasonBudgetExhausted, est)
			return nil, designRefusal(codes.FailedPrecondition,
				designReasonBudgetExhausted, designConstructionBudgetRefusalMsg, nil)
		}
		s.designFailDraft(ctx, run, attempt.AttemptNo, callErr)
		return nil, s.designDraftCallError(ctx, cardID, callErr)
	}

	// ─── ПРОВЕРКА СТРУКТУРНОГО ОТВЕТА ───
	//
	// ⚠ ПОПЫТКА ЗАКРЫВАЕТСЯ ОПЛАЧЕННОЙ, ХОТЯ ПРОГОН ПРОВАЛЕН, И ЭТО НЕ ОПИСКА. Вызов состоялся:
	// картинки уехали, входные токены посчитаны, деньги поставщику причитаются. Списать ноль
	// значило бы сделать «модель ответила не по схеме» бесплатным способом жечь бюджет — а
	// бесплатным он не является ни для кого, кроме нашей бухгалтерии.
	var draft *pb_common.DesignConstructionDraft
	if construction {
		parsed, stats, perr := parseConstructionDraft(text, finishReason)
		// ⚠ СВЕРКА С НАШИМИ ДАННЫМИ — ЗДЕСЬ И ТОЛЬКО ЗДЕСЬ, ДО ЗАПИСИ КАНОНА (B-25). Разбор чист и
		// зовётся ещё раз на повторе, где ни словаря, ни свежей карточки быть не должно: сверка
		// там пересматривала бы вчерашний оплаченный ответ сегодняшним словарём. Довод целиком —
		// у designVerifyColourways.
		designVerifyColourways(parsed, designBuildColourDictionary(colours),
			designCardSlotFolds(card), &stats)
		s.designLogConstructionDraft(ctx, cardID, run.Id, finishReason, usage, stats, perr)
		if perr != nil {
			s.designFailDraftAs(ctx, run, attempt.AttemptNo,
				perr, designConstructionReasonInvalidOutput, est)
			msg := designConstructionShapeRefusalMsg
			if strings.EqualFold(strings.TrimSpace(finishReason), "length") {
				msg = designConstructionCutRefusalMsg
			}
			return nil, designRefusal(codes.FailedPrecondition,
				designConstructionReasonInvalidOutput, msg, nil)
		}
		// ⚠ В СТРОКУ УЕЗЖАЕТ ПРОВЕРЕННЫЙ КАНОНИЧЕСКИЙ JSON, А НЕ ОТВЕТ МОДЕЛИ: сохранённый прогон
		// обязан быть ровно тем, что получил клиент, иначе повтор и история показывают третью,
		// никем не виденную версию ответа.
		//
		// ⚠ ПИШЕТ СВОЙ МАРШАЛЕР (designMarshalConstructionDraft), А НЕ ТОТ, ЧТО ПИШЕТ `inputs`:
		// он заполняет ПУСТЫЕ ключи, потому что их присутствия требует читатель этой же строки.
		// Довод целиком — у designConstructionMarshal; без него черновик, содержательный одним
		// лишь `missing`, читался обратно как nil.
		canonical, merr := designMarshalConstructionDraft(parsed)
		if merr != nil {
			slog.Default().ErrorContext(ctx, "draft design idea: the construction draft did not encode",
				slog.Int("run_id", run.Id), slog.String("err", merr.Error()))
			s.designFailDraftAs(ctx, run, attempt.AttemptNo,
				merr, designConstructionReasonInvalidOutput, est)
			return nil, designRefusal(codes.FailedPrecondition,
				designConstructionReasonInvalidOutput, designConstructionShapeRefusalMsg, nil)
		}
		draft, text = parsed, string(canonical)
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
		Run:          s.designRunResponse(ctx, *done),
		Budget:       s.designBudgetResponse(ctx, budget),
		Construction: draft,
	}, nil
}

// designLogConstructionDraft — ОДНА СТРОКА ЛОГА НА ОДИН СТРУКТУРНЫЙ ЧЕРНОВИК.
//
// ДИСЦИПЛИНА ЗАИМСТВОВАНА У РАЗБОРА ТЕХ-КАРТЫ (logAnalysisRun) ЦЕЛИКОМ, ВМЕСТЕ С ДОВОДОМ: платный
// вызов, чья стоимость никогда не доезжает до лога, — это счёт, которого никто не видит. Уровень
// строки И ЕСТЬ ВЕРДИКТ: провал разбора — Error и ровно один, поэтому «как часто модель отвечает
// не по схеме» это счёт, а не join; ответ, у которого что-то поправлено или выброшено, — Warn;
// чистый — Info, и существует он ради usage.
//
// ⚠ ТОКЕНЫ ПЕЧАТАЮТСЯ ИМЕННО ЗДЕСЬ, ПОТОМУ ЧТО БОЛЬШЕ ИМ НЕГДЕ БЫТЬ. Чат-эндпоинт возвращает
// токены, а не деньги, поэтому в регистр уезжает ОЦЕНКА (см. FinishAttempt ниже), и единственное
// место, где видно фактический расход, — эта строка. Разойдясь, оценка и факт станут заметны
// только отсюда.
func (s *Server) designLogConstructionDraft(
	ctx context.Context, cardID, runID int,
	finishReason string, usage openrouter.Usage, stats designConstructionStats, err error,
) {
	attrs := []any{
		slog.Int("tech_card_id", cardID),
		slog.Int("run_id", runID),
		slog.String("model", s.aiOps.Model()),
		slog.String("base_url", s.aiOps.BaseURL()),
		slog.String("finish_reason", finishReason),
		slog.Int("prompt_tokens", usage.Prompt),
		slog.Int("completion_tokens", usage.Completion),
		slog.Int("total_tokens", usage.Total),
		slog.Int("aspects_custom", stats.AspectsCustom),
		slog.Int("aspects_dropped", stats.AspectsDropped),
		slog.Int("callouts_dropped", stats.CalloutsDropped),
		slog.Int("bom_dropped", stats.BomDropped),
		slog.Int("missing_dropped", stats.MissingDropped),
		slog.Int("enums_unset", stats.EnumsUnset),
		slog.Int("material_ids_zeroed", stats.MaterialIDs),
		slog.Int("truncated", stats.Truncated),
		slog.Int("over_limit", stats.OverLimit),
		slog.Int("deduped", stats.Deduped),
		// ─── КРУГ 19: ТРИ СЧЁТЧИКА, ЗАВЕДЁННЫЕ И НЕ НАПЕЧАТАННЫЕ ───
		//
		// Тот же шов, что у двух ниже, только волной раньше. Каждый из трёх поднимает Warn «черновик
		// коэрцирован», и до этой правки поднимал его С ПУСТЫМИ РУКАМИ: строка тревоги, у которой
		// причина посчитана, но не названа. TestConstructionDraftLogPrintsEveryCounter спрашивает
		// саму структуру, а не список имён, — поэтому следующий такой счётчик покраснеет сразу.
		slog.Int("pairs_cleared", stats.PairsCleared),
		slog.Int("non_scalars", stats.NonScalars),
		slog.Int("fields_dropped", stats.FieldsDropped),
		// ─── КРУГ 20 ───
		// callouts_unasked — модель прислала выноски, хотя правило 3 их больше не просит (B-13).
		// Ноль означает, что переписанное правило работает; растущее число — счёт за выходные
		// токены, которых клиент не рисует.
		slog.Int("callouts_unasked", stats.CalloutsUnasked),
		slog.Int("colour_codes_unset", stats.ColourCodesUnset),
		slog.Int("slot_colours_unbound", stats.SlotColoursUnbound),
		slog.Int("colourways_dropped", stats.ColourwaysDropped),
		// ⚠ ЭТИ ДВЕ ПРОПУСТИЛ ШОВ МЕЖДУ ДВУМЯ ВОЛНАМИ (B-16): счётчики завели в разборе, а список
		// печати живёт в другом файле, и он не менялся. Двенадцать строк с «est_usage»: «about 2»
		// поднимали Warn «черновик коэрцирован», у которого ВСЕ двадцать напечатанных чисел равны
		// нулю, — тревога без причины. Счётчик без строки лога это статистика, которую никто не
		// видит; строка без счётчика — «что-то пошло не так» без числа.
		slog.Int("bom_est_dropped", stats.BomEstDropped),
		slog.Int("units_unset", stats.UnitsUnset),
	}
	switch {
	case err != nil:
		attrs = append(attrs, slog.String("err", err.Error()))
		slog.Default().ErrorContext(ctx, "design construction draft was refused", attrs...)
	case stats.Coerced():
		slog.Default().WarnContext(ctx, "design construction draft was coerced", attrs...)
	default:
		slog.Default().InfoContext(ctx, "design construction draft", attrs...)
	}
}

// designFailDraft закрывает попытку и прогон после провала вызова. ЛУЧШЕЕ УСИЛИЕ И ГРОМКОЕ:
// ошибка здесь не возвращается человеку — он должен увидеть ту, из-за которой всё началось, — но
// молчание оставило бы прогон висеть с зарезервированными деньгами.
func (s *Server) designFailDraft(ctx context.Context, run entity.DesignRun, attemptNo int, cause error) {
	// ПРОВАЛ ПОСТАВЩИКА НЕ ОПЛАЧИВАЕТСЯ: ответа не было вовсе, платить не за что.
	s.designFailDraftAs(ctx, run, attemptNo, cause, "provider_error", decimal.NullDecimal{})
}

// designFailDraftAs — тот же круг закрытия, но КОД ПРИЧИНЫ И ЦЕНА ЗАДАЮТСЯ ВЫЗЫВАЮЩИМ.
//
// ⚠ ДВЕ ПРИЧИНЫ — ДВА РАЗНЫХ СОБЫТИЯ, И РАЗЛИЧАТЬ ИХ ОБЯЗАНА КОЛОНКА, А НЕ ПРОЗА. `provider_error`
// значит «ответа не было»; `invalid_output` — «ответ был, и он не той формы». Первое чинит
// дежурный, второе — промпт. Слепив их в один код, мы получили бы график «поставщик падает» там,
// где падает наша собственная схема.
//
// ⚠ И ИМЕННО ПОЭТОМУ ЦЕНА ЗДЕСЬ ПАРАМЕТР. Ответ, пришедший не по схеме, УЖЕ ОПЛАЧЕН входными
// токенами картинок; закрыть такую попытку нулём значит спрятать потраченные деньги.
func (s *Server) designFailDraftAs(
	ctx context.Context, run entity.DesignRun, attemptNo int,
	cause error, errorCode string, price decimal.NullDecimal,
) {
	if err := s.repo.Design().FinishAttempt(ctx, entity.DesignAttemptFinish{
		RunId: run.Id, AttemptNo: attemptNo,
		State:     entity.DesignAttemptFailed,
		Price:     price,
		ErrorCode: errorCode,
	}); err != nil {
		slog.Default().ErrorContext(ctx, "draft design idea: cannot close the failed attempt",
			slog.Int("run_id", run.Id), slog.String("err", err.Error()))
	}
	if _, err := s.repo.Design().FailRun(ctx, entity.DesignRunFail{
		RunId:      run.Id,
		ClaimToken: run.ClaimToken.String,
		ErrorCode:  errorCode,
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

// designReplayedFailure переводит УЖЕ ЗАКРЫТЫЙ ПРОВАЛОМ прогон в тот же отказ, который человек
// получил, когда прогон провалился впервые.
//
// ⚠ ВЫБИРАЕТ КОЛОНКА `error_code`, А НЕ ПРОЗА `last_error`. Проза — это текст ошибки поставщика или
// разбора; она годится в лог и не годится в решение. Незнакомый код — это прогон, проваленный
// кодом, которого этот файл ещё не знает, и правильный ответ на него общий, а не выдуманный.
func designReplayedFailure(run entity.DesignRun) error {
	code := strings.TrimSpace(run.ErrorCode.String)
	switch code {
	case designConstructionReasonInvalidOutput:
		return designRefusal(codes.FailedPrecondition, code, designConstructionReplayShapeMsg, nil)
	case designReasonBudgetExhausted:
		return designRefusal(codes.FailedPrecondition, code, designConstructionReplayBudgetMsg, nil)
	}
	if code == "" {
		code = "provider_error"
	}
	return designRefusal(codes.FailedPrecondition, code, designDraftReplayFailedMsg, nil)
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

// designKindReadsTheCard — ЧИТАЕТ ЛИ ЭТОТ РОД ПРОГОНА КАРТОЧКУ ВООБЩЕ, или его вход — только те
// картинки, которые человек назвал ПОИМЁННО в этом самом запросе.
//
// ⚠ ОДИН ПРЕДИКАТ НА ОБЕ ПОЛОВИНЫ СНИМКА, И ЗАВЁЛСЯ ОН ПОТОМУ, ЧТО ПОЛОВИН БЫЛО ДВЕ, А ПРАВИЛО
// ОДНО. Отбор плит (designSelectBench) знал это правило с самого начала и своим switch'ем отдавал
// перекрасу и паттерну пустоту; цикл по референсам в designAssembleInputs не знал его НИКОГДА — и
// замороженный снимок паттерна перечислял ВСЕ ссылки карточки как свои входы. Воркер их не
// отправлял (sourcePictures сужает список до названных), но снимок утверждал обратное, а панель
// прогона рисует снимок — то есть история говорила про оплаченный прогон то, чего не было.
//
// ВЛАДЕЛЕЦ УВИДЕЛ ИМЕННО ЭТО (J-6): «почему у нас в паттерн генерацию отправляются наши INPUT —
// REFERENCES … по крайне мере они есть в card's references». Последняя половина фразы — подпись
// панели, прочитанная обратно.
//
// ⚠ ЭТО НЕ КОПИЯ ФИЛЬТРА ВОРКЕРА, а его ДВЕРНАЯ половина: там решается, что уедет провайдеру,
// здесь — что будет ЗАПИСАНО как вход. Разойтись им не на чем — обе половины отвечают «только
// названные картинки», — но раньше они и не могли сойтись: у двери правило было написано в одном
// месте из двух. Теперь оно написано ровно один раз, и оба места зовут его по имени.
//
// СПИСОК ЗАКРЫТ ПО СМЫСЛУ, А НЕ ПО ОСТОРОЖНОСТИ: карточку не читает род, чей вход — конкретная
// фотография (перекрас) или конкретный лоскут (паттерн). Всякий новый род по умолчанию попадает в
// «читает» — и это безопасная сторона: лишняя строка в снимке видна человеку, недостающая нет.
func designKindReadsTheCard(kind string) bool {
	switch kind {
	case entity.DesignRunKindRecolor, entity.DesignRunKindPattern:
		return false
	}
	return true
}

// designKindReadsTheGarmentNote — уезжают ли в промпт ОПИСАНИЕ ИЗДЕЛИЯ и ПОСАДКА этой карточки.
//
// ⚠ ЭТО ОТДЕЛЬНЫЙ ВОПРОС ОТ ПРЕДЫДУЩЕГО, И РАЗНИЦА — ДЕНЬГИ. composePrompt пишет `garment:` и
// `fit:` ДО всякой развилки по роду (designgen/snapshot.go), то есть промпт паттерна нёс описание
// изделия («olive shirt, spread collar») в прогон, который делает КУСОК ТКАНИ. Экран паттерна при
// этом печатал рядом с кнопкой «Nothing else from this card travels: not the bench, not the
// references, not the garment description» — и последняя половина была неправдой.
//
// ПЕРЕКРАС ОПИСАНИЕ СОХРАНЯЕТ, и это не недосмотр: перекрашивают ФОТОГРАФИЮ ИЗДЕЛИЯ, и «olive
// shirt, spread collar» описывает ровно тот предмет, который на снимке. Владелец про перекрас не
// сказал ничего, а снять слова у рода, который их законно использует, значило бы починить одну
// жалобу и завести вторую.
//
// ПОЧЕМУ ПУСТОЙ СНИМОК, А НЕ ВЕТКА В КОМПОЗИТОРЕ. `write()` в composePrompt пропускает пустое
// значение, поэтому пустые поля снимка ГАСЯТ блоки сами — без второго читателя рода в пакете,
// который и так решает по роду четыре вещи. И снимок при этом честен: он говорит, чем прогон
// располагал, а паттерн этими словами не располагал.
func designKindReadsTheGarmentNote(kind string) bool {
	return kind != entity.DesignRunKindPattern
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
	if src.Card != nil && designKindReadsTheGarmentNote(src.Kind) {
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
	// ⚠ ССЫЛКИ КАРТОЧКИ ЧИТАЕТ НЕ ВСЯКИЙ РОД (J-6), И ПРАВИЛО ЖИВЁТ В designKindReadsTheCard —
	// одно на этот цикл и на отбор плит ниже. Цикл по `extra_input_media_ids` идёт ВСЕГДА: это и
	// есть то, что человек назвал поимённо, и у перекраса с паттерном он единственный вход.
	cardRefs := src.Refs
	if !designKindReadsTheCard(src.Kind) {
		cardRefs = nil
	}
	for _, r := range cardRefs {
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

// designRefuseForeignFixSlots — КАРТОЧНАЯ И КОЛОРВЕЙНАЯ ГРАНИЦА ДЛЯ `fix_slot_ids`.
//
// Список сужает прогон до названных плит, и до этой правки его никто не проверял против базы
// вовсе: чужой (или просто не читаемый этим прогоном) адрес принимался, отбор возвращал пустоту, и
// оплаченный прогон уходил без единого входа — притом денежные ворота 3D его пропускали, потому
// что они спрашивают верстак колорвея целиком, а не названное подмножество.
//
// Предикат ТОТ ЖЕ, что у отбора: род (`want`) и колорвей. Слот, который отбор всё равно
// выбросит, — это адрес, который прогон не исполнит, и назвать его молча принятым нельзя.
// `spoken` — сообщение КЛИЕНТА по тому же доводу, что у деталей: унаследованный снимок рерана
// заморожен, а адрес законно удаляют.
func designRefuseForeignFixSlots(cardID int, spoken *pb_common.DesignRunParams, bench []entity.DesignBenchSlot, src designInputSources) error {
	// ⚠ ОБЕ ПОЛОВИНЫ СУЖЕНИЯ, А НЕ ОДНА (T1). Первая редакция этой двери выходила первой же
	// строкой, если `fix_slot_ids` пуст, — и `fix_targets` (сужение ПО ВИДУ) проходил мимо неё
	// целиком: его проверяли только на ФОРМУ («это вообще силуэтная сторона?»), но никогда против
	// верстака. Довод, написанный ниже для адресов, применим к видам ДОСЛОВНО, и после
	// колорвейного сужения он стал ещё сильнее: до этой волны `side_l` совпадал с side_l ЛЮБОГО
	// колорвея, а теперь — только своего.
	//
	// ЧЕМ ЭТО ПЛАТИТСЯ. Прогон 3D колорвея 5 с выбором front + side_l, где side_l есть только у
	// колорвея 6: денежные ворота открыты (у 5 есть занятые слоты), дверь молчала, отбор возвращал
	// ОДНУ плиту — а у поставщика turntable нижний порог MinImages = 1, то есть вызов уходит и
	// оплачивается, турнтейбл строится из одного вида вместо двух, и человеку про потерянную
	// сторону не говорят НИЧЕГО. Это не пустой прогон, который где-то ниже отказал бы, — это
	// молча УРЕЗАННЫЙ прогон, и отличить его от заказанного нельзя даже потом.
	ids := spoken.GetFixSlotIds()
	// ТОТ ЖЕ ВЫБОР ИСТОЧНИКА, ЧТО У ОТБОРА (designSelectBench): список, когда он непуст, иначе
	// скаляр. Второе правило рядом с первым разошлось бы ровно на противоречивом входе.
	views := spoken.GetFixTargets()
	if len(views) == 0 && spoken.GetFixTarget() != "" {
		views = []string{spoken.GetFixTarget()}
	}
	if len(ids) == 0 && len(views) == 0 {
		return nil
	}
	want := entity.DesignPictureKindFlat
	if src.Kind == entity.DesignRunKindThreed {
		want = entity.DesignPictureKindRender
	}
	matchColorway, wantColorway := designBenchColorwayScope(src)
	usableIDs := make(map[int]struct{}, len(bench))
	usableViews := make(map[string]struct{}, len(bench))
	// viewOfUsableID — сторона каждого пригодного слота, для проверки 3D ниже: адрес по id тоже
	// умеет назвать три четверти, и молча урезать прогон он умеет ровно так же, как адрес по виду.
	viewOfUsableID := make(map[int]string, len(bench))
	for _, slot := range bench {
		if slot.TechCardId != cardID || entity.DesignKindOrFlat(slot.Kind) != want {
			continue
		}
		if matchColorway && entity.DesignColorwayOrNone(slot.ColorwayId) != wantColorway {
			continue
		}
		// ПЛИТА ОБЯЗАТЕЛЬНА, потому что её требует отбор: слот без картинки он выбрасывает той же
		// строкой, что и слот чужого рода. Дверь, не спросившая про плиту, приняла бы адрес,
		// который прогон всё равно не исполнит, — то есть вернула бы ту же тихую потерю.
		if slot.Picture == nil || slot.Picture.MediaId <= 0 {
			continue
		}
		usableIDs[slot.Id] = struct{}{}
		usableViews[slot.ViewKey] = struct{}{}
		viewOfUsableID[slot.Id] = slot.ViewKey
	}
	for i, id := range ids {
		if _, ok := usableIDs[int(id)]; !ok {
			return status.Errorf(codes.InvalidArgument,
				"params.fix_slot_ids.%d %d is not a %s slot this run can read on tech card %d "+
					"(a run narrowed to slots it cannot read would be paid for and empty)",
				i, id, want, cardID)
		}
	}
	for i, v := range views {
		if _, ok := usableViews[v]; !ok {
			// СООБЩЕНИЕ НАЗЫВАЕТ КОЛОРВЕЙ, а не только сторону: без него человек читает «нет
			// рендера на side_l» на карточке, где side_l прекрасно виден — просто у другого цвета.
			return status.Errorf(codes.InvalidArgument,
				"params.fix_targets.%d %q has no %s plate on colourway %d of tech card %d, so this run "+
					"would silently be built from fewer sides than were picked (0 = the colourway-less bench)",
				i, v, want, wantColorway, cardID)
		}
	}
	// ─── 3D ЧИТАЕТ ЧЕТЫРЕ ОРТОГОНАЛЬНЫЕ СТОРОНЫ, И СУЖЕНИЕ НЕ СМЕЕТ НАЗВАТЬ ПЯТУЮ (D-28) ───
	//
	// Три четверти — законная плита рендер-верстака и законный вход рендера, но ни один из двух
	// маршрутов сборки её не примет: у fal четыре именованных слота (front/back/left/right), у Meshy
	// потолок в четыре вида одного предмета, а воркер (designgen.threedPictures) отбирает плиты
	// ровно по entity.IsDesignCardinalView. Прогон 3D, суженный до `front + three_quarter_l`,
	// прошёл бы обе проверки выше (плита есть, колорвей тот) и уехал бы оплаченным с ОДНОЙ
	// стороной вместо двух — та самая молча урезанная сборка, о которой написано в шапке. Спросить
	// надо здесь, у того, кто назвал, и ДО денег.
	if src.Kind == entity.DesignRunKindThreed {
		for i, v := range views {
			if !entity.IsDesignCardinalView(v) {
				return status.Errorf(codes.InvalidArgument,
					"params.fix_targets.%d %q is not a side a 3D build can read: the turntable takes the four "+
						"cardinal sides (front, back, side_l, side_r), and a run narrowed to a three-quarter "+
						"plate would be paid for and built from fewer sides than were picked", i, v)
			}
		}
		for i, id := range ids {
			if v, ok := viewOfUsableID[int(id)]; ok && !entity.IsDesignCardinalView(v) {
				return status.Errorf(codes.InvalidArgument,
					"params.fix_slot_ids.%d %d stands on %q, which is not a side a 3D build can read: the "+
						"turntable takes the four cardinal sides (front, back, side_l, side_r)", i, id, v)
			}
		}
	}
	return nil
}

// designRefuseForeignFlatSlots держит дверь для `flat_slot_ids` (J-10) — ровно ту, которой у поля
// не было и без которой оно было «принято и не исполнено».
//
// ⚠ ЧТО ЛОМАЛОСЬ. Список сужает плиты ПЕРЕСЕЧЕНИЕМ с верстаком, поэтому адрес, не называющий ни
// одного пригодного слота (устаревший, опустевший, чужого колорвея, чужой карточки), просто ни с
// чем не совпадал: прогон СОЗДАВАЛСЯ, ОПЛАЧИВАЛСЯ и замораживал снимок, утверждающий «плит нет» —
// неотличимо от `use_flat_slots = false`, и без единого отказа, который человек мог бы прочесть.
// Он ведь именно ПОПРОСИЛ послать эти плиты. Тот же довод дословно записан у
// designRefuseForeignFixSlots и designRefuseForeignDetailSlots; это третья дверь того же класса.
//
// ПРЕДИКАТ ОБЯЗАН СОВПАДАТЬ С ОТБОРОМ (designSelectBench), поэтому проверяются ровно те же четыре
// условия — карточка, род верстака, колорвейный скоуп и НАЛИЧИЕ ПЛИТЫ. Разойдясь, дверь дала бы
// либо ложный отказ, либо ту же тихую потерю обратно.
//
// ⚠ СПРАШИВАЕТСЯ С ТОГО, КТО НАЗВАЛ: сюда едет СООБЩЕНИЕ КЛИЕНТА, а не действующие параметры.
// Унаследованный снимок рерана проверять нельзя — слот законно удаляют, и прогон стал бы
// неперезапускаемым навсегда. Та же граница и по той же причине, что у обеих соседних дверей.
//
// МОЛЧИТ ВЕЗДЕ, КРОМЕ СВОЕГО МАРШРУТА. Пустой список — это «все плиты» (контракт), выключенный
// `use_flat_slots` — «ни одной», выборочная правка сужает себя сама, а прочие роды поле
// игнорируют: во всех этих случаях спрашивать не о чем.
func designRefuseForeignFlatSlots(cardID int, spoken *pb_common.DesignRunParams, bench []entity.DesignBenchSlot, src designInputSources) error {
	ids := spoken.GetFlatSlotIds()
	if len(ids) == 0 || src.Kind != entity.DesignRunKindFlat || !spoken.GetUseFlatSlots() {
		return nil
	}
	if entity.IsDesignSelectiveFix(spoken.GetFixTarget(), spoken.GetFixTargets(),
		designInt32sToInts(spoken.GetFixSlotIds())) {
		return nil
	}
	matchColorway, wantColorway := designBenchColorwayScope(src)
	usable := make(map[int]struct{}, len(bench))
	for _, slot := range bench {
		if slot.TechCardId != cardID ||
			entity.DesignKindOrFlat(slot.Kind) != entity.DesignPictureKindFlat {
			continue
		}
		if matchColorway && entity.DesignColorwayOrNone(slot.ColorwayId) != wantColorway {
			continue
		}
		if slot.Picture == nil || slot.Picture.MediaId <= 0 {
			continue
		}
		usable[slot.Id] = struct{}{}
	}
	for i, id := range ids {
		if id <= 0 {
			return status.Errorf(codes.InvalidArgument,
				"params.flat_slot_ids.%d must be a bench slot id, got %d", i, id)
		}
		if _, ok := usable[int(id)]; !ok {
			return status.Errorf(codes.InvalidArgument,
				"params.flat_slot_ids.%d %d is not a filled flat slot of tech card %d "+
					"(a run narrowed to plates it cannot read would be paid for and carry none; "+
					"send no flat_slot_ids to use every filled slot, or use_flat_slots=false to send none)",
				i, id, cardID)
		}
	}
	return nil
}

// designBenchColorwayScope — СУЖАЕТ ЛИ КОЛОРВЕЙ ЧТЕНИЕ ВЕРСТАКА ЭТИМ ПРОГОНОМ, и каким.
//
// ОДНО ПРАВИЛО НА ОБА ПРОХОДА ПО ВЕРСТАКУ — плиты (designSelectBench) и пустые именные детали
// (designNamedEmptyDetailSlots). Написанное дважды, оно разошлось бы ровно там, где расходиться
// дороже всего: в замороженном снимке оплаченного прогона.
//
// Сужает ТОЛЬКО у 3D, и это не пропуск у рендера. Рендер строится ИЗ ФЛЭТОВ, а флэт — одна
// разметка на карточку и колорвея не имеет по существу (L-4): рендер колорвея A и рендер
// колорвея B читают ОДИН И ТОТ ЖЕ флэтовый верстак, различаясь рецептом цвета. 3D же читает
// рендер-верстак, а он живёт НА КОЛОРВЕЙ (L-2) — и без этого фильтра `want = render` брал бы
// рендеры ВСЕХ колорвеев карточки разом: оплаченный прогон собирался бы из смеси цветов, и в
// записи не оставалось бы ничего, чем это потом разобрать.
//
// «Колорвей не назван» (0) — ТОЖЕ ЗНАЧЕНИЕ, а не отсутствие фильтра: безколорвейное 3D видит
// ровно неатрибутированный верстак — тот единственный, что существовал до оси, — и потому старые
// карточки ведут себя байт в байт как раньше, а в именованный колорвей ничего чужого не
// подмешивается никогда.
func designBenchColorwayScope(src designInputSources) (match bool, want int) {
	if src.Kind != entity.DesignRunKindThreed {
		return false, 0
	}
	return true, int(src.Params.GetColorwayId())
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
	// ⚠ ТОТ ЖЕ КОЛОРВЕЙНЫЙ СКОУП, ЧТО У ОТБОРА ПЛИТ (D8). Пустая деталь — тоже ВХОД прогона: она
	// уезжает в снимок и оттуда в промпт («draw these details: collar»). Без фильтра 3D колорвея A
	// могло вписать в свой замороженный снимок пустую деталь колорвея B — вход чужого верстака в
	// оплаченном прогоне, и разобрать это потом было бы нечем. Правило одно на два прохода
	// (designBenchColorwayScope), потому что два написания одного правила расходятся молча.
	matchColorway, wantColorway := designBenchColorwayScope(src)
	named := make(map[int]entity.DesignBenchSlot, len(src.Bench))
	for _, slot := range src.Bench {
		if slot.ViewKey != entity.DesignViewDetail {
			continue
		}
		if matchColorway && entity.DesignColorwayOrNone(slot.ColorwayId) != wantColorway {
			continue
		}
		named[slot.Id] = slot
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
	// ⚠ ПЕРЕКРАС И ПАТТЕРН НЕ БЕРУТ ИЗ ВЕРСТАКА НИЧЕГО, И ЭТО ПРО ЧЕСТНОСТЬ СНИМКА, А НЕ ПРО ЭКОНОМИЮ
	// БАЙТОВ. Снимок отвечает на один вопрос — «чем этот прогон ПИТАЛСЯ», — и воркер уже сузил обоим
	// родам список до названных картинок (designgen/source_inputs.go). Оставить здесь плиты значило
	// бы записать в замороженную историю утверждение, которого не было: панель прогона показала бы
	// человеку четыре флэта как входы перекраса, он бы пошёл искать, почему модель их использовала,
	// и не нашёл бы — потому что она их не видела.
	//
	// ⚠ И ЭТО НЕ КОПИЯ ФИЛЬТРА ВОРКЕРА, а ДРУГАЯ ЕГО ПОЛОВИНА: там решается, что уедет провайдеру,
	// здесь — что будет записано как вход. Разойтись им не на чем: обе половины отвечают «только
	// названные картинки», и названные картинки в верстаке не живут вовсе.
	//
	// ⚠ ПЕРЕЧЕНЬ РОДОВ УЕХАЛ В designKindReadsTheCard, И ЭТО НЕ ПЕРЕКЛАДЫВАНИЕ. Ровно этот switch
	// был ЕДИНСТВЕННЫМ местом, где правило стояло, — а второй половине снимка (цикл по референсам в
	// designAssembleInputs) оно не досталось вовсе, и снимок паттерна перечислял ссылки карточки
	// как свои входы. Два написания одного правила расходятся молча; здесь они разошлись с самого
	// начала.
	if !designKindReadsTheCard(src.Kind) {
		return nil, nil
	}
	want := entity.DesignPictureKindFlat
	// ⚠ КОЛОРВЕЙ СУЖАЕТ ОТБОР ТОЛЬКО У 3D, и довод — у самого правила (designBenchColorwayScope),
	// в одном месте на оба прохода по верстаку.
	matchColorway, wantColorway := designBenchColorwayScope(src)
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
	// ОДНА ФУНКЦИЯ НА ОБА ЧИТАТЕЛЯ (entity.IsDesignSelectiveFix): тот же вопрос задаёт сборщик
	// ссылок промпта, и два независимых прочтения разошлись бы ровно на выборочном прогоне.
	selective := entity.IsDesignSelectiveFix(
		src.Params.GetFixTarget(), src.Params.GetFixTargets(),
		designInt32sToInts(src.Params.GetFixSlotIds()))

	/*
	 * ФЛЕТ-ПРОГОН НЕ БЕРЁТ ПЛИТЫ ФЛЕТ-СЛОТОВ, ПОКА ЕГО ОБ ЭТОМ НЕ ПОПРОСЯТ (K-1).
	 *
	 * ⚠ ЭТО БЫЛ НЕ ВЫБОР, А ПАДЕНИЕ В УМОЛЧАНИЕ. Правило выбора имеет ровно двух членов и записано
	 * выше своими словами: «рендер строится из ФЛЭТОВ, 3D — из РЕНДЕРОВ». У вида `flat` своей
	 * ветки нет вовсе, и он наследовал `want = flat` — то есть флет-прогон получал СВОИ ЖЕ старые
	 * флеты как референс. Владелец описал следствие точно: «сгенеренные флеты вообще не похожи на
	 * то что в референс картинах но 1 в 1 похожи на то что во флетах». Модель получала готовый
	 * ответ и переписывала его.
	 *
	 * Экран говорил обратное — `generation-form.tsx` печатает рядом с деньгами «a flat run reads
	 * the card's references, never the bench plates». Эта фраза была ложью ровно до этой строки.
	 *
	 * ЧТО ЗДЕСЬ НЕ ЗАТРОНУТО, и это важно. `selective` — сужение до названных плит (правка одной
	 * картинки), у него свой смысл и своя дверь, поэтому он проходит мимо гейта. Именные пустые
	 * детали заводятся ниже отдельным проходом и продолжают уезжать: «нарисуй воротник» — это
	 * просьба, а не референс. Перезапуск снимок не пересобирает вовсе, поэтому старые прогоны
	 * повторяются ровно так, как шли.
	 */
	if src.Kind == entity.DesignRunKindFlat && !selective && !src.Params.GetUseFlatSlots() {
		return nil, nil
	}

	/*
	 * …И БЕРЁТ НЕ ОБЯЗАТЕЛЬНО ВСЕ (J-10). `use_flat_slots` — выключатель на ВЕСЬ верстак, а человек
	 * смотрит на плиты по одной и про одну из них знает, что она этому прогону мешает. До этого
	 * списка у него было ровно два ответа — «все» и «ни одной», — и чтобы убрать одну плиту, ему
	 * приходилось ВЫНИМАТЬ ЕЁ ИЗ СЛОТА, то есть менять состояние карточки ради параметра одного
	 * прогона.
	 *
	 * ПУСТОЙ СПИСОК — ЭТО «ВСЕ», А НЕ «НИ ОДНОЙ», и на этом держится совместимость: каждый
	 * замороженный прогон и каждый клиент, не знающий поля, продолжают значить ровно то, что
	 * значили. «Ни одной» уже имеет своё правописание — `use_flat_slots = false`, — и второго ему
	 * не нужно.
	 *
	 * ⚠ СУЖЕНИЕ ТОЛЬКО У ФЛЭТА С ВКЛЮЧЁННЫМ ВЫКЛЮЧАТЕЛЕМ, И ЭТО НЕ ОСТОРОЖНОСТЬ, А ГРАНИЦА СМЫСЛА.
	 * У рендера плиты флэтов — это СОДЕРЖАНИЕ рода, а не опция (тот же довод, что у самого
	 * `use_flat_slots` выше); у 3D — тем более. Позволить списку сужать их значило бы дать одному
	 * полю два разных смысла на разных маршрутах.
	 *
	 * ⚠ И НЕ НА ВЫБОРОЧНОМ ПУТИ (`selective`): правка названных плит уже сужена своими
	 * `fix_targets`/`fix_slot_ids`, у которых своя дверь и свой смысл. Два независимых сужения
	 * одного списка разошлись бы ровно там, где человек сузил дважды.
	 */
	keepFlatSlots := map[int]struct{}{}
	if src.Kind == entity.DesignRunKindFlat && !selective && src.Params.GetUseFlatSlots() {
		for _, id := range src.Params.GetFlatSlotIds() {
			if id > 0 {
				keepFlatSlots[int(id)] = struct{}{}
			}
		}
	}

	out := make([]*pb_common.DesignInputSlot, 0, len(src.Bench))
	plates := make([]int32, 0, len(src.Bench))
	for _, slot := range src.Bench {
		if entity.DesignKindOrFlat(slot.Kind) != want {
			continue
		}
		if matchColorway && entity.DesignColorwayOrNone(slot.ColorwayId) != wantColorway {
			continue
		}
		if slot.Picture == nil || slot.Picture.MediaId <= 0 {
			continue
		}
		// ИМЕНОВАННОЕ СУЖЕНИЕ ФЛЭТ-ПЛИТ (J-10). Карта пуста на всяком маршруте, кроме флэта с
		// включённым `use_flat_slots` и НЕПУСТЫМ списком, — см. её построение выше.
		if len(keepFlatSlots) > 0 {
			if _, ok := keepFlatSlots[slot.Id]; !ok {
				continue
			}
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

	// ─── ЧЕТВЁРТОЕ ПОЛЕ, И ОНО ЗАКРЫВАЕТ ДЫРУ В J-6 РАЗМЕРОМ В ЦЕЛЫЙ МАРШРУТ ───
	//
	// ⚠ РЕРАН НЕ ЗОВЁТ designAssembleInputs ВОВСЕ, поэтому оба предиката круга 15 на нём не стояли.
	// Реран прогона паттерна, замороженного ДО круга 15, приносил в НОВЫЙ ПЛАТНЫЙ промпт описание
	// изделия родителя и все ссылки карточки — то есть ровно то, что J-6 (в) объявил деньгами, и
	// обойти ворота имени можно было простым «пришли params».
	//
	// ⚠ ЭТО НЕ ПЕРЕПИСЫВАНИЕ ИСТОРИИ, И РАЗЛИЧЕНИЕ РОВНО ТО ЖЕ, ЧТО У `views`/`layout` ВЫШЕ.
	// Строка РОДИТЕЛЯ не трогается ни байтом — она свидетельство и остаётся им. Здесь собирается
	// снимок РЕБЁНКА: новой строки о новом платном прогоне, у которой есть собственные параметры и
	// собственный род. Снимок, не сходящийся с собственной строкой, врёт — этим доводом маршрут
	// уже патчит три поля, и четвёртое приходит по нему же.
	//
	// ⚠ ССЫЛКИ СУЖАЮТСЯ ДО ТОГО, ЧТО ЭТОТ ПРОГОН НАЗВАЛ САМ, а не «до чего-нибудь поменьше». Тот
	// же список воркер и отправит (designgen: sourcePictures оставляет ровно
	// `extra_input_media_ids`), так что снимок и вложения совпадают по построению. Дверь при этом
	// уже отказала перекрасу без снимков и паттерну не с одной картинкой, значит пустым этот
	// список после сужения не бывает.
	if !designKindReadsTheCard(src.Kind) {
		named := make(map[int32]struct{}, len(src.Params.GetExtraInputMediaIds()))
		for _, id := range src.Params.GetExtraInputMediaIds() {
			named[id] = struct{}{}
		}
		kept := make([]*pb_common.DesignInputRef, 0, len(named))
		for _, r := range snap.GetRefs() {
			if _, ok := named[r.GetMediaId()]; ok {
				kept = append(kept, r)
			}
		}
		snap.Refs = kept
	}
	if !designKindReadsTheGarmentNote(src.Kind) {
		snap.GarmentNote = ""
		snap.Fit = ""
	}

	// ФИТ БЕРЁТСЯ ИЗ СНИМКА РОДИТЕЛЯ, А НЕ С КАРТОЧКИ. Модель получит те же слова, что получила в
	// прошлый раз, значит и `fit_at_launch` строки обязан говорить о том же: иначе плита
	// приедет со штампом сегодняшнего фита, а нарисована будет по вчерашнему, и минт сверил бы
	// её не с тем. У рода, которому фит не показывают вовсе, обе половины пусты — и это не потеря
	// штампа, а честный штамп: прогон был отправлен без единого слова о посадке.
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
//
// ⚠ nil ЗНАЧИТ «СЛОВ НЕТ», А НЕ «ДОСКИ НЕТ», И РАЗНИЦА ТЕПЕРЬ СТОИТ ДЕНЕГ. Картинки в снимок
// пишет ВЫЗЫВАЮЩИЙ (DraftDesignIdea, поле media_ids), потому что «какие картинки уехали» —
// это факт о РАЗРЕШЁННЫХ адресах, а не о карточке: половина плиток доски может не иметь живой
// строки медиа. Пустить сюда id прямо с карточки значило бы записать в историю картинки, которых
// модель не видела.
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
	b.WriteString(designBoardPromptBody(mood, attachedIDs))
	return strings.TrimSpace(b.String())
}

// designBoardPromptBody — ДОСКА СЛОВАМИ: замысел плюс записки, привязанные к картинке и месту.
//
// ⚠ ВЫДЕЛЕНА В ОТДЕЛЬНУЮ ФУНКЦИЮ РАДИ ВТОРОГО ЧИТАТЕЛЯ, И ЭТО НЕСУЩЕЕ. Структурный черновик
// (designConstructionUserPrompt) обязан задавать вопрос ПО ТОЙ ЖЕ доске и с той же привязкой; он
// брал эти строки, вырезая их из готового промпта по заголовку — то есть держался на том, что
// заголовок не поправят. Поправили бы — и замысел с записками исчезли бы из платного запроса
// МОЛЧА, оставив модель гадать по картинкам. Одна сборка на двоих такого состояния не имеет.
//
// Шапка (имя изделия, посадка) сюда НЕ входит: у двух читателей она разная — короткая у прозы,
// с категорией, полом и размерным рядом у конструкции.
func designBoardPromptBody(mood *pb_common.DesignMoodSnapshot, attachedIDs []int) string {
	var b strings.Builder
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
	return b.String()
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
