package aiprov

import "time"

// The TIME BUDGET of one provider call, derived from the answer ceiling of that same call. It lives
// here, and not in a transport, because every chat transport (oaichat today; anthropic and gemini
// next) must bound its request by the same arithmetic, and because the callers who hand out a lease
// for a paid call (store/design.HandlerLeaseFor, the router's ChainBudget) must ask THE SAME
// function the wire asks — two copies of this formula are two numbers that drift apart silently.
// Moved from internal/openrouter (B-11); openrouter.CompletionBudget / DefaultCompletionBudget are
// one-line delegates to these.

const (
	// DefaultBudgetBase is the BASE of a call's budget, not the whole of it: connect, upload, the
	// provider fetching whatever pictures the request points at, and time-to-first-token. The time
	// the ANSWER takes to print is ADDED on top by CompletionBudget — see minCompletionTokPerSec.
	//
	// ⚠ ЭТО БОЛЬШЕ НЕ ПОТОЛОК ВСЕГО ВЫЗОВА, И ЭТО ПОЧИНКА, А НЕ ПЕРЕИМЕНОВАНИЕ. Пока 60 s были
	// потолком целиком, потолок токенов и бюджет времени были ДВУМЯ независимыми числами, связанными
	// только просьбой в комментарии — «в этом порядке, и ни одно без другого» (openrouter:
	// analysisReasoningEffort). Просьба не удержала: designConstructionMaxTokens подняли 3000 → 8000
	// в одиночку, и с того дня ответ, который потолок РАЗРЕШИЛ, физически не успевал приехать — 8000
	// токенов за 60 s это 133 ток/с при замеренных ~60 (2500 токенов за 42 s).
	//
	// ⚠ ЧЕМ КОНЧАЛСЯ РАЗРЫВ. Клиент рвал соединение на 60-й секунде, ошибка приезжала транспортная —
	// то есть НЕ ErrBudgetExhausted, — и вызывающий (design_run.go: designFailDraft) закрывал попытку
	// ЦЕНОЙ NULL: поставщик напечатал 22k входных и 5–7k выходных токенов, регистр записал НОЛЬ, а
	// человек увидел codes.Unavailable, неотличимое от погоды, и нажал ещё раз. Это ровно тот дефект,
	// который круг 19 чинил у двери finish_reason; он вернулся через дверь транспорта.
	//
	// ПОЭТОМУ СВЯЗЬ ТЕПЕРЬ ВЫВЕДЕНА, А НЕ ЗАПИСАНА: бюджет времени СЧИТАЕТСЯ ИЗ `max_tokens` того же
	// запроса, в единственном месте, которое этот `max_tokens` на провод и кладёт (oaichat). Поднять
	// потолок, забыв про время, больше нельзя — их складывает одна функция.
	DefaultBudgetBase = 60 * time.Second

	// minCompletionTokPerSec — КОНСЕРВАТИВНЫЙ НИЖНИЙ ПРЕДЕЛ скорости печати ответа, из которого
	// считается добавка ко времени: печать `max_tokens` токенов не может занять больше, чем
	// max_tokens / minCompletionTokPerSec, иначе поставщик просто болен.
	//
	// ЧИСЛО ЗАМЕРЕНО И ПОДЕЛЕНО НАДВОЕ. Единственный живой замер в этом репозитории — 2500 токенов
	// завершения за 42 s ≈ 60 ток/с (openrouter: analysisReasoningEffort). Двукратный запас на плохой
	// день у поставщика даёт 30. Больше брать нельзя: таймаут, который срабатывает на ЗДОРОВОМ
	// вызове, покупает ноль за полную цену — именно это здесь и чинится.
	//
	// ⚠ ЗАПАС ИДЁТ В СТОРОНУ ОЖИДАНИЯ, А НЕ ОБРЫВА, И ЭТО НЕСУЩЕЕ РЕШЕНИЕ. Лишняя минута ожидания
	// стоит человеку минуты; оборванный вызов стоит денег поставщику, нуля в регистре и второго
	// нажатия. Цены этих двух ошибок несравнимы, поэтому предел занижен нарочно.
	minCompletionTokPerSec = 30
)

// CompletionBudget — СКОЛЬКО ВРЕМЕНИ ИМЕЕТ ПРАВО ЗАНЯТЬ ОДИН ВЫЗОВ, у которого попрошен потолок
// maxTokens: база (соединение, загрузка, картинки, время до первого токена) плюс время печати
// самого ответа при консервативной скорости minCompletionTokPerSec.
//
// ⚠ ЭКСПОРТИРОВАНА РАДИ ОДНОГО: ЧТОБЫ ТОТ, КТО СТАВИТ ПОТОЛОК, МОГ СПРОСИТЬ ПРО СВОЁ ВРЕМЯ. Потолок
// живёт у вызывающего (design_construction_draft.go: designConstructionMaxTokens), время — здесь, и
// раньше между ними не было ничего, кроме просьбы в комментарии. Теперь у вызывающего есть тест,
// который спрашивает эту функцию тем же числом и краснеет, если ответ физически не успевает.
//
// maxTokens <= 0 значит «потолка нет»: провайдер печатает по своему усмотрению, добавлять нечего, и
// бюджет остаётся базой. base <= 0 значит «база не задана» и читается как DefaultBudgetBase.
func CompletionBudget(base time.Duration, maxTokens int) time.Duration {
	if base <= 0 {
		base = DefaultBudgetBase
	}
	if maxTokens <= 0 {
		return base
	}
	// Целочисленно и через time.Second, а не float: секунда на minCompletionTokPerSec токенов.
	printing := time.Duration(maxTokens) * time.Second / minCompletionTokPerSec
	return base + printing
}

// DefaultCompletionBudget — CompletionBudget при НЕЗАДАННОЙ базе (OPENROUTER_HTTP_TIMEOUT и её
// будущие соседи не выставлены). Отдельная дверь потому, что вызывающий, который хочет проверить
// свой потолок, не обязан знать про конфигурацию процесса — а солгать себе, подставив базу побольше,
// ему было бы легко.
//
// ⚠ ЭТО НЕ ДВЕРЬ ДЛЯ ТОГО, КТО ВЫДАЁТ ЛИЗУ ПОД ЖИВОЙ ВЫЗОВ. Лиза обязана переживать вызов, а вызов
// считает срок от базы СВОЕГО клиента (oaichat.Client.CompletionBase / openrouter.CompletionBase);
// лиза, посчитанная отсюда, держалась бы ровно на том, что переменную базы никто не поставил. Здесь
// дверь для проб и для того, кто сверяет ПОТОЛОК с базой по умолчанию, не имея под рукой процесса.
func DefaultCompletionBudget(maxTokens int) time.Duration {
	return CompletionBudget(DefaultBudgetBase, maxTokens)
}
