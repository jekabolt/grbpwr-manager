package admin

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// ═════ ЗАКРЫВАЮЩИЕ ЗАПИСИ УСПЕХА: ОПЛАЧЕННЫЙ ОТВЕТ НЕ ЗАВИСИТ ОТ ТОГО, СЛУШАЕТ ЛИ ЕГО КТО-ТО ═════
//
// ЧТО БЫЛО. Круг «две записи — два срока» починил ПРОВАЛЬНУЮ половину (designFailDraftAs) и
// оставил успешную нетронутой: FinishAttempt и CompleteRun брали ЖИВОЙ контекст запроса, без
// context.WithoutCancel и без своего срока. Клиент, ушедший в окне между возвратом
// CompleteWithImages и этими двумя записями (закрытая вкладка, срок ингресса, react-query
// `retry: 1`), отменяет контекст хендлера — gRPC делает это сразу, — и обе отказывают на BeginTx.
//
// ЧЕМ ЭТО КОНЧАЛОСЬ, И ЭТО СТРОГО ХУЖЕ ПРОВАЛЬНОЙ ПОЛОВИНЫ. Там терялась СТРОКА РЕГИСТРА; здесь
// теряется ОПЛАЧЕННЫЙ ОТВЕТ. Прогон остаётся `pending` с живым захватом, резерв дня висит до
// полуночи, подметальщика на такую строку нет (см. TestThereAreThreeSweepsAndOnlyOneTakesPending),
// а как только истечёт лиза — тот же client_request_id проходит designRunResumableSQL и ПЛАТИТ
// ВТОРОЙ РАЗ за ответ, который уже был получен и выброшен.

// ОТВЕТ КЛАДЁТСЯ В СТРОКУ, ДАЖЕ ЕСЛИ СЛУШАТЬ ЕГО УЖЕ НЕКОМУ.
//
// ⚠ КАК ЗАМЕР ЛОВИТ РОВНО ЭТО ОКНО, БЕЗ СНА И БЕЗ ГОНКИ. Контекст отменяется НА ПЕРВОМ ОБРАЩЕНИИ
// ХЕНДЛЕРА К СТОРУ ПОСЛЕ ОТВЕТА ПОСТАВЩИКА (стенд знает про ответ от самого стуба). Между этими
// двумя точками хендлер не ходит в стор ничем, кроме FinishAttempt, значит точка одна и она
// детерминирована. Отменить контекст РАНЬШЕ нельзя: платный вызов идёт тем же контекстом и просто
// не состоялся бы — то есть проба мерила бы провальную половину, уже прикрытую соседней пробой.
//
// МУТАЦИЯ: снять context.WithoutCancel в успешной половине DraftDesignIdea → обе записи получают
// уже отменённый контекст, ответ не сохраняется, проба краснеет.
func TestTheAnswerIsFiledAfterTheCallerIsGone(t *testing.T) {
	rig := newDraftRig(t, http.StatusOK, "A boxy coat with a storm flap.")

	ctx, cancel := context.WithCancel(designRunCtx())
	defer cancel()
	rig.onProviderAnswered = cancel

	_, err := rig.srv.DraftDesignIdea(ctx, draftRequest())
	require.NoError(t, err)

	// ПОЛОЖИТЕЛЬНЫЙ КОНТРОЛЬ ЗАМЕРА: клиент действительно ушёл, и ушёл ПОСЛЕ платного вызова.
	require.Error(t, ctx.Err(), "проба обязана мерить именно отменённый вызов")
	require.True(t, rig.providerAnswerSeen, "отмена не сработала: окно замера не открывалось вовсе")

	require.Len(t, rig.finished, 1, "попытка не закрывалась вовсе")
	require.NoError(t, rig.finishedCtxErr[0],
		"цена попытки пошла в стор ОТМЕНЁННЫМ контекстом — BeginTx отказал бы сразу")

	require.Len(t, rig.completedCtxErr, 1, "прогон не закрывался вовсе — резерв повис бы до полуночи")
	require.NoError(t, rig.completedCtxErr[0],
		"ОПЛАЧЕННЫЙ ОТВЕТ пошёл в стор ОТМЕНЁННЫМ контекстом: он не сохранился бы, прогон остался бы "+
			"pending с живым захватом, и следующий повтор того же ключа заплатил бы за него второй раз")
	require.Equal(t, "A boxy coat with a storm flap.", rig.completedText,
		"в строку обязан лечь ответ, за который уже заплачено")
	require.Equal(t, "claim-55", rig.completedTok,
		"строка закрывается СВОИМ захватом: без него CompleteRun откажет claim_lost")
}

// И ДВА СВОИХ СРОКА, А НЕ ОДИН НА ОБЕ — ТА ЖЕ ПОЧИНКА, ЧТО У ПРОВАЛЬНОЙ ПОЛОВИНЫ.
//
// Довод целиком — у designCloseWriteBudget: медленная первая запись съедает время второй, а вторая
// и есть та, без которой резерв висит и повтор платит.
//
// МУТАЦИЯ: поставить один context.WithTimeout на обе успешные записи → краснеет.
func TestTheTwoSuccessWritesDoNotShareOneBudget(t *testing.T) {
	rig := newDraftRig(t, http.StatusOK, "an idea")
	rig.finishDelay = 200 * time.Millisecond

	_, err := rig.srv.DraftDesignIdea(designRunCtx(), draftRequest())
	require.NoError(t, err)

	// ПОЛОЖИТЕЛЬНЫЙ КОНТРОЛЬ: обе записи состоялись и обе несли СРОК. Без него проба зеленела бы в
	// мире, где закрывающих записей нет вовсе либо они идут вообще без дедлайна.
	require.Len(t, rig.finishedDeadline, 1, "первая закрывающая запись пошла в стор без срока")
	require.Len(t, rig.completedDeadline, 1, "вторая закрывающая запись пошла в стор без срока")

	gap := rig.completedDeadline[0].Sub(rig.finishedDeadline[0])
	require.GreaterOrEqual(t, gap, rig.finishDelay/2,
		"дедлайны двух записей разошлись всего на %s: срок второй отсчитан не от неё самой, "+
			"а унаследован от первой — то есть бюджет снова общий", gap)
	require.GreaterOrEqual(t, rig.completedRemaining[0], designCloseWriteBudget-50*time.Millisecond,
		"у записи ОПЛАЧЕННОГО ОТВЕТА осталось %s из %s: запись цены съела чужое время",
		rig.completedRemaining[0], designCloseWriteBudget)
}

// ═══════════ ДВА ЧИСЛА, КОТОРЫЕ КОММЕНТАРИИ ПРО ДЕНЬГИ НАЗЫВАЛИ НЕВЕРНО ═══════════
//
// Обе пробы — ЦИТАТНЫЕ: они читают ИСХОДНИК того пакета, про который утверждает комментарий, и
// потому краснеют не от переписанной прозы, а от изменившегося кода. Довод, по которому это важно
// именно здесь: неверное число в комментарии про деньги переживает свою починку — аудитор,
// пересчитавший его и нашедший ложным, снимает ВЕРНЫЙ код вместе с ложным доводом.

// СОН ПОВТОРОВ — 310 ms, А НЕ ПОЛТОРЫ СЕКУНДЫ, И 300 ms ЭТО ПОТОЛОК, КОТОРОГО НЕ ДОСТИГАЕТ НИКТО.
//
// ЧТО СТОЯЛО В КОММЕНТАРИИ: «до пяти повторов по дедлоку с паузой в 300 ms — полторы секунды
// одного только сна». txRetryBackoff даёт 10ms << attempt с потолком 300 ms, а maxTxRetries = 5,
// поэтому потолок не достигается ни разу и сумма всех пауз — 310 ms (≤ ~465 ms с 50% джиттера).
// Довод «двум SERIALIZABLE-кругам общего бюджета в 5 s не хватает» ВЕРЕН и остался, но опирается
// он теперь на ожидание блокировок, а не на сон.
//
// МУТАЦИЯ: переписать сумму в комментарии обратно в полторы секунды нельзя проверить кодом —
// поэтому проба держит ЧИСЛА ИСХОДНИКА: поднимите txRetryBaseDelay до 100 ms (сумма 3.1 s) или
// maxTxRetries до 10 — краснеет, и комментарий рядом обязан переехать вместе с ней.
func TestTheDeadlockSleepIsThreeHundredTenMillisNotFifteenHundred(t *testing.T) {
	src := readRepoSource(t, filepath.Join("..", "..", "store", "db.go"))
	retries := srcInt(t, src, `maxTxRetries\s*=\s*(\d+)`)
	base := srcDuration(t, src, `txRetryBaseDelay\s*=\s*(\d+)\s*\*\s*time\.(\w+)`)
	cap := srcDuration(t, src, `txRetryMaxDelay\s*=\s*(\d+)\s*\*\s*time\.(\w+)`)
	t.Logf("maxTxRetries=%d txRetryBaseDelay=%s txRetryMaxDelay=%s", retries, base, cap)

	var total, longest time.Duration
	for attempt := 0; attempt < retries; attempt++ {
		d := base << attempt
		if d > cap || d <= 0 {
			d = cap
		}
		if d > longest {
			longest = d
		}
		total += d
	}

	require.Equal(t, 310*time.Millisecond, total,
		"сумма пауз между повторами — %s; комментарий у designCloseWriteBudget называет 310 ms", total)
	require.LessOrEqual(t, total+total/2, 465*time.Millisecond,
		"с 50%% джиттера сон не должен превышать ~465 ms, а вышло %s", total+total/2)
	require.Less(t, longest, cap,
		"потолок паузы (%s) ДОСТИГАЕТСЯ на %d повторах — значит комментарий «300 ms это потолок, "+
			"а не пауза» перестал быть верным", cap, retries)

	// ПОЛОЖИТЕЛЬНЫЙ КОНТРОЛЬ: прежнее число было ложным, и ложным ВТРОЕ, — иначе эта проба
	// защищала бы формулировку, которую и так никто не оспаривал.
	require.Less(t, total, 1500*time.Millisecond,
		"положительный контроль: прежняя приписка про полторы секунды сна обязана быть опровергнута")
}

// МЁТЕЛ ТРИ, А НЕ ДВЕ, И ОДНА ИЗ НИХ БЕРЁТ `pending` — ВЫВОД ПРИ ЭТОМ НЕ МЕНЯЕТСЯ.
//
// ЧТО СТОЯЛО В КОММЕНТАРИИ: «обе метлы ReviveExpiredRuns фильтруют ровно `status='running'`».
// Метлы в ReviveExpiredRuns ТРИ, и sweepAbandonedCancelledRuns берёт `status IN
// ('pending','running')`. Незакрытая строка `draft_idea` проходит мимо неё по ДРУГОМУ условию —
// `cancel_requested_at IS NOT NULL`, которого у неотменённого прогона нет. Вывод «такую строку не
// подметает НИЧТО» остаётся верным; неверным было перечисление, а по перечислению его и проверяют.
//
// МУТАЦИЯ: снять `cancel_requested_at IS NOT NULL` из designRunAbandonedCancelledSQL → краснеет
// (и это ровно тот день, когда вывод комментария перестал бы быть верным).
func TestThereAreThreeSweepsAndOnlyOneTakesPending(t *testing.T) {
	src := readRepoSource(t, filepath.Join("..", "..", "store", "design", "queue.go"))
	revive := funcBody(t, src, "func (s *Store) ReviveExpiredRuns(")

	// ТРИ, И КАЖДАЯ НАЗВАНА: две вынесенные плюс возврат в очередь прямо в теле.
	require.Equal(t, 1, strings.Count(revive, "sweepAbandonedCancelledRuns(ctx, db)"))
	require.Equal(t, 1, strings.Count(revive, "closeRunsPastTheirCeiling(ctx, db)"))
	require.Equal(t, 1, strings.Count(revive, "UPDATE design_run"),
		"возврат в очередь — третья метла, и она пишет своим оператором прямо в теле")

	// ДВЕ ИЗ ТРЁХ ДЕЙСТВИТЕЛЬНО ФИЛЬТРУЮТ РОВНО `status = 'running'` — это половина прежней
	// приписки, которая была верна, и она обязана остаться верной.
	require.Contains(t, revive, "WHERE status = 'running'")
	require.Contains(t, funcBody(t, src, "func closeRunsPastTheirCeiling("), "WHERE status = 'running'")

	// А ТРЕТЬЯ БЕРЁТ И `pending` — ровно то, что прежнее перечисление отрицало.
	abandoned := constBody(t, src, "designRunAbandonedCancelledSQL")
	require.Contains(t, abandoned, "status IN ('pending', 'running')",
		"третья метла берёт и ждущие строки: перечисление «обе фильтруют ровно running» было ложным")

	// И ВЫВОД ДЕРЖИТСЯ НЕ НА СТАТУСЕ, А НА ЭТОМ УСЛОВИИ. Оно и есть причина, по которой
	// неотменённая строка draft_idea проходит мимо третьей метлы.
	require.Contains(t, abandoned, "cancel_requested_at IS NOT NULL",
		"без этого условия третья метла подметала бы и НЕОТМЕНЁННЫЙ pending — то есть вывод "+
			"комментария («не подметает ничто») стал бы ложным, и комментарий обязан переехать")
}

// ─────────────────────────── чтение исходника соседнего пакета ───────────────────────────

func readRepoSource(t *testing.T, rel string) string {
	t.Helper()
	body, err := os.ReadFile(rel)
	require.NoError(t, err, "исходник %s не прочитался — проба цитирует его, а не пересказывает", rel)
	return string(body)
}

func srcInt(t *testing.T, src, pattern string) int {
	t.Helper()
	m := regexp.MustCompile(pattern).FindStringSubmatch(src)
	require.Len(t, m, 2, "не нашлось %q: константу переименовали, и цитата обязана переехать", pattern)
	n, err := strconv.Atoi(m[1])
	require.NoError(t, err)
	return n
}

func srcDuration(t *testing.T, src, pattern string) time.Duration {
	t.Helper()
	m := regexp.MustCompile(pattern).FindStringSubmatch(src)
	require.Len(t, m, 3, "не нашлось %q: константу переименовали, и цитата обязана переехать", pattern)
	n, err := strconv.Atoi(m[1])
	require.NoError(t, err)
	units := map[string]time.Duration{
		"Nanosecond": time.Nanosecond, "Microsecond": time.Microsecond,
		"Millisecond": time.Millisecond, "Second": time.Second, "Minute": time.Minute,
	}
	unit, ok := units[m[2]]
	require.True(t, ok, "неизвестная единица времени %q", m[2])
	return time.Duration(n) * unit
}

// funcBody / constBody — кусок исходника от объявления до его закрывающей скобки первого уровня.
// Грубо, но достаточно: обе цели объявлены на нулевом отступе, значит закрывает их строка "}" или
// "`" в первой колонке.
func funcBody(t *testing.T, src, decl string) string {
	t.Helper()
	i := strings.Index(src, decl)
	require.GreaterOrEqual(t, i, 0, "объявление %q не найдено — цитата обязана переехать вместе с ним", decl)
	rest := src[i:]
	end := strings.Index(rest, "\n}\n")
	require.Greater(t, end, 0, "у %q не нашлось закрывающей скобки на нулевом отступе", decl)
	return rest[:end]
}

func constBody(t *testing.T, src, name string) string {
	t.Helper()
	i := strings.Index(src, "const "+name+" = `")
	require.GreaterOrEqual(t, i, 0, "константа %q не найдена — цитата обязана переехать вместе с ней", name)
	rest := src[i:]
	end := strings.Index(rest[len("const "+name+" = `"):], "`")
	require.Greater(t, end, 0, "у константы %q не нашлось закрывающей кавычки", name)
	return rest[:len("const "+name+" = `")+end]
}
