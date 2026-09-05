package admin

import (
	"context"
	"database/sql"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/jekabolt/grbpwr-manager/internal/openrouter"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
)

// ═══════════ ПОСЛЕДНЯЯ ДЫРА ДЕНЕЖНОГО РЕГИСТРА: ОБОРВАННЫЙ ПЛАТНЫЙ ВЫЗОВ ═══════════
//
// ЧТО БЫЛО. Всякий провал вызова модели закрывался как `provider_error` с NULL-ценой — по
// ЯВНОМУ решению («ответа не было, платить не за что»). Решение верно ровно для половины исходов.
// Вторая половина — обрыв ПОСЛЕ отправки: истёкший срок, отменённый контекст, разорванное краем
// соединение, закрытая вкладка. К этой секунде поставщик уже получил доску из двенадцати кадров
// (≈22k входных токенов) и напечатал сколько-то ответа. Регистр писал НОЛЬ, человек видел
// codes.Unavailable — новость, неотличимую от погоды, — и жал ещё раз с новым client_request_id.
// Ни одна строка регистра никогда не показывала, что деньги ушли.
//
// ЧТО ДЕРЖАТ ЭТИ ПРОБЫ. Ровно ДВА утверждения, и вторая половина не менее несущая первой:
// оборванный ПОСЛЕ отправки вызов ОПЛАЧЕН, а не доехавший до поставщика — БЕСПЛАТЕН. Починка,
// у которой есть только первая половина, — это «списывать всегда», то есть выдуманные деньги на
// каждом отказе соединения.

// hangingModel — поставщик, который получает запрос ЦЕЛИКОМ и не отвечает.
//
// ⚠ ТЕЛО ЧИТАЕТСЯ ЯВНО. Во-первых, дочитанное тело и есть «поставщик получил запрос» — то самое,
// что проба утверждает; канал `reached` делает это утверждение проверяемым, а не подразумеваемым.
// Во-вторых, net/http отменяет r.Context() по разрыву соединения только после того, как тело
// прочитано, и обработчик, ждущий отмены с непрочитанным телом, вешает Close навсегда.
func hangingModel(t *testing.T) (url string, reached <-chan struct{}) {
	t.Helper()
	release := make(chan struct{})
	got := make(chan struct{})
	var once sync.Once
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		once.Do(func() { close(got) })
		select {
		case <-release:
		case <-r.Context().Done():
		}
	}))
	t.Cleanup(func() {
		close(release)
		srv.Close()
	})
	return srv.URL, got
}

// deadModelAddr — адрес, на котором ГАРАНТИРОВАННО никто не слушает. Слушатель поднят только
// затем, чтобы у ядра спросить свободный порт, и сразу закрыт: литеральный 127.0.0.1:1 однажды
// окажется занят на чужой машине, и «соединение отказано» молча станет другим исходом.
func deadModelAddr(t *testing.T) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	addr := l.Addr().String()
	require.NoError(t, l.Close())
	return "http://" + addr
}

// pointDraftAt переводит стенд на другого «поставщика» и на короткий срок. Срок здесь — предмет
// пробы, а не удобство: обрыв по сроку и есть тот исход, который прежде стоил ноль.
func pointDraftAt(t *testing.T, rig *draftRig, url string, budget time.Duration) {
	t.Helper()
	rig.srv.aiOps = openrouter.New(openrouter.Config{
		APIKey: "test-key", BaseURL: url, Model: "anthropic/claude-sonnet-5", HTTPTimeout: budget,
	})
}

// ─────────────────────── ПОЛОВИНА ПЕРВАЯ: ОБРЫВ ПОСЛЕ ОТПРАВКИ ОПЛАЧЕН ───────────────────────

// ⚠ ЭТО ТА САМАЯ ДЫРА. Прогон открыт, деньги зарезервированы, запрос уехал, ответ не вернулся —
// и попытка обязана закрыться СВОИМ кодом и С ЦЕНОЙ.
//
// МУТАЦИИ (все — по ЧИСЛУ ИСПОЛНЕННЫХ ИСХОДОВ, не по коду возврата):
//   - вернуть designFailDraft к безусловному `provider_error` + decimal.NullDecimal{} → краснеет
//     и цена, и код;
//   - убрать `engaged(...)` у ветки Do в postChatCompletion → цена снова NULL (4/11 красных).
//
// ⚠ ЗДЕСЬ СТОЯЛО «ЭТА МУТАЦИЯ ОСТАЛАСЬ ЗЕЛЁНОЙ, httptest такого исхода не даёт», И УТВЕРЖДЕНИЕ
// БЫЛО СЛИШКОМ СИЛЬНЫМ. Оборванная на полуслове запись производится сырым net.Listener'ом, который
// принимает соединение и тут же закрывает его с SetLinger(0): при теле крупнее буферов сокета
// клиент упирается в RST ПОСРЕДИ записи, и WroteRequest приезжает с info.Err != nil. Условие
// `info.Err == nil` теперь стоит на пробе, а не на доводе, —
// openrouter.TestARequestCutMidWriteIsNotCharged, и мутация «поднимать флаг безусловно» краснеет
// ровно на ней. Там же прибит и второй, более широкий класс: флаг описывает ПОСЛЕДНЮЮ попытку
// транспорта, а не объединение всех (TestTheEngagedFlagDescribesTheLastAttemptOnly).
func TestADraftCutAfterTheModelWasEngagedIsChargedInTheRegister(t *testing.T) {
	rig := newDraftRig(t, http.StatusOK, "unused: this provider never answers")
	url, reached := hangingModel(t)
	pointDraftAt(t, rig, url, 250*time.Millisecond)

	_, err := rig.srv.DraftDesignIdea(designRunCtx(), draftRequest())
	require.Error(t, err)

	select {
	case <-reached:
	default:
		t.Fatal("поставщик не получил запроса — проба измеряет не то, что утверждает")
	}

	require.Len(t, rig.finished, 1, "ровно одна закрытая попытка: второй двери списания нет")
	require.Equal(t, entity.DesignAttemptFailed, rig.finished[0].State)
	require.Equal(t, designReasonProviderCut, rig.finished[0].ErrorCode,
		"`provider_error` значит «до поставщика не доехали»; здесь доехали и заплатили")
	require.True(t, rig.finished[0].Price.Valid,
		"регистр обязан показать деньги, которые ушли: NULL здесь и был всей дырой")
	require.True(t, rig.finished[0].Price.Decimal.IsPositive())

	require.Len(t, rig.failed, 1)
	require.Equal(t, designReasonProviderCut, rig.failed[0].ErrorCode,
		"колонка прогона обязана называть то же событие, что колонка попытки")

	// ЧЕЛОВЕКУ ГОВОРЯТ ПРО ДЕНЬГИ ДО ТОГО, КАК ОН НАЖМЁТ СНОВА. Код остаётся Unavailable — совет
	// «повторить» верен, — но молчащая погода и превращала одно нажатие в три.
	code, _ := errorReason(t, err)
	require.Equal(t, codes.Unavailable, code)
	require.Contains(t, err.Error(), "charged",
		"иначе человек уходит с экрана, считая оборванный вызов бесплатным")
}

// ─────────────────── ПОЛОВИНА ВТОРАЯ, РАЗЛИЧАЮЩАЯ: НЕ УЕХАВШЕЕ БЕСПЛАТНО ───────────────────

// ⚠ БЕЗ ЭТОЙ ПРОБЫ ПОЧИНКА ВЫШЕ ВЫПОЛНИМА ОДНОЙ СТРОКОЙ «СПИСЫВАТЬ ВСЕГДА» — и это была бы та же
// ложь регистра, только в другую сторону: выдуманные деньги на каждом отказе соединения, на
// каждом протухшем слуге модели, на каждом неверном ключе.
//
// РЕЗЕРВ ПРИ ЭТОМ БЫЛ СДЕЛАН, И ЭТО НЕ ПРОТИВОРЕЧИЕ: резерв — потолок дня, он снимается целиком на
// терминальном переходе; факт — то, что действительно потрачено. Здесь факта нет.
//
// МУТАЦИЯ: вернуть `engaged(failed)` из ветки Do безусловно (снять проверку wrote.Load()) →
// краснеет ровно эта проба, соседняя остаётся зелёной.
func TestADraftThatNeverReachedTheModelIsStillFree(t *testing.T) {
	rig := newDraftRig(t, http.StatusOK, "unused: nothing is listening")
	pointDraftAt(t, rig, deadModelAddr(t), 5*time.Second)

	_, err := rig.srv.DraftDesignIdea(designRunCtx(), draftRequest())
	require.Error(t, err)

	require.Len(t, rig.finished, 1)
	require.Equal(t, entity.DesignAttemptFailed, rig.finished[0].State)
	require.Equal(t, "provider_error", rig.finished[0].ErrorCode,
		"соединение отказано — поставщик не получил ни байта")
	require.False(t, rig.finished[0].Price.Valid,
		"ноль здесь ПРАВДА, и она обязана остаться: иначе регистр начнёт выдумывать деньги")

	require.True(t, rig.started.PriceEstimate.Valid,
		"резерв всё равно был сделан — он потолок дня, а не счёт, и снимается на терминале")
}

// ПРОТУХШИЙ СЛУГ МОДЕЛИ ТОЖЕ БЕСПЛАТЕН.
//
// Это не педантизм, а прожитая авария: снятый у поставщика слуг отвечает 404 за 0.2 с на КАЖДОЕ
// нажатие. Пометив ворота поставщика тратой, мы бы выписали регистру счёт ровно в тот день, когда
// фича мертва целиком.
//
// МУТАЦИЯ: обернуть ветку non-2xx в postChatCompletion в engaged(...) → краснеет.
func TestAModelThatDoesNotExistIsNotCharged(t *testing.T) {
	rig := newDraftRig(t, http.StatusNotFound, "")
	_, err := rig.srv.DraftDesignIdea(designRunCtx(), draftRequest())
	require.Error(t, err)

	require.Len(t, rig.finished, 1)
	require.Equal(t, "provider_error", rig.finished[0].ErrorCode)
	require.False(t, rig.finished[0].Price.Valid, "404 — это ворота поставщика, а не его счётчик")
}

// ─────────────────── ФАКТ И РЕЗЕРВ НЕ МОГУТ РАЗОЙТИСЬ НИ НА ОДНОЙ ВЕТКЕ ───────────────────

// СПИСЫВАЕТСЯ РОВНО ТО, ЧТО ЗАРЕЗЕРВИРОВАНО, И ЭТО ПРОВЕРЯЕТСЯ НА ОБЕИХ ФОРМАХ НАЖАТИЯ.
//
// ⚠ ПОЧЕМУ ВЕСЬ `est`, А НЕ ОДНА ВХОДНАЯ ЕГО ПОЛОВИНА. Обрыв случается в любой момент — и на
// первом токене ответа, и на последнем; usage приезжает ровно в том ответе, которого не было,
// значит сузить нечем, а доктрина блока (designPriceEstimate) требует ВЕРХНЕЙ границы. Взяв то же
// число, что и резерв, мы вдобавок лишаем их возможности разойтись: второй цены рядом с местом
// списания просто нет.
//
// ⚠ ОБЕ ВЕТКИ, А НЕ ОДНА: базы у них разные (designDraftIdeaProseBaseUSD против
// designDraftIdeaConstructionBaseUSD), и починка, взявшая «какую-нибудь» из двух, покраснела бы
// ровно на одной из этих половин.
//
// МУТАЦИЯ: передать в designFailDraft константу designDraftIdeaBaseUSD вместо `est` → краснеет
// прозаическая половина (у неё база меньше общей) и картиночное слагаемое обеих.
//
// ⚠ ОБРЫВ ЗДЕСЬ — ЗАКРЫТАЯ ВКЛАДКА, А НЕ СРОК, И ЭТО ВЫНУЖДЕННО И ПОЛЕЗНО ОДНОВРЕМЕННО. Срок
// вызова ВЫВОДИТСЯ из потолка ответа (openrouter.CompletionBudget), поэтому у структурной ветки
// он ≈266 s даже при крошечной базе — коротким таймаутом её не оборвать, не переписав тот вывод.
// Отмена же приходит с той двери, ради которой всё и чинится: gRPC отменяет контекст хендлера,
// как только клиент ушёл.
func TestTheCutChargeIsExactlyWhatTheRunReserved(t *testing.T) {
	for _, tc := range []struct {
		name         string
		construction bool
	}{
		{"проза", false},
		{"конструкция", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rig := newDraftRig(t, http.StatusOK, "unused")
			url, reached := hangingModel(t)
			pointDraftAt(t, rig, url, 10*time.Second)

			ctx, cancel := context.WithCancel(designRunCtx())
			go func() {
				<-reached
				cancel()
			}()
			defer cancel()

			req := draftRequest()
			req.Construction = tc.construction
			_, err := rig.srv.DraftDesignIdea(ctx, req)
			require.Error(t, err)

			require.Len(t, rig.finished, 1)
			require.True(t, rig.started.PriceEstimate.Valid)
			require.True(t, rig.finished[0].Price.Valid)
			require.Equal(t,
				rig.started.PriceEstimate.Decimal.String(),
				rig.finished[0].Price.Decimal.String(),
				"факт и резерв обязаны быть ОДНИМ числом, иначе полоса бюджета врёт")
			require.Equal(t,
				designDraftIdeaEstimate(1, tc.construction).Decimal.String(),
				rig.finished[0].Price.Decimal.String(),
				"и это число — цена ЭТОЙ формы нажатия с ЭТИМ числом картинок")

			// ⚠ И ЗАПИСЬ ОБЯЗАНА БЫЛА СОСТОЯТЬСЯ ЖИВЫМ КОНТЕКСТОМ. Контекст хендлера здесь
			// ОТМЕНЁН — это и есть ушедший клиент; пойди закрывающая транзакция им, стор отказал бы
			// на BeginTx, ошибка уехала бы в лог, и «человек закрыл вкладку» снова стоил бы
			// регистру ноль. МУТАЦИЯ: снять context.WithoutCancel в designFailDraftAs → краснеет.
			require.Len(t, rig.finishedCtxErr, 1)
			require.NoError(t, rig.finishedCtxErr[0],
				"закрывающая запись пошла в стор ОТМЕНЁННЫМ контекстом — она бы не состоялась")
			require.Error(t, ctx.Err(), "проба обязана мерить именно отменённый вызов")
		})
	}
}

// ─────────────────────── НИ ОДНОГО ВТОРОГО СПИСАНИЯ ───────────────────────

// СЪЕДЕННЫЙ ПОТОЛОК ПЛАТИТ ЧЕРЕЗ СВОЮ ДВЕРЬ И ТОЛЬКО ЧЕРЕЗ НЕЁ.
//
// ⚠ ЭТО СТОРОЖ ПОРЯДКА ВЕТОК, И ОН ЗАВЁЛСЯ ИМЕННО ЭТОЙ ПРАВКОЙ. `finish_reason=length` без ответа
// — исход, который поставщик ОТДАЛ (2xx), то есть теперь он тоже «вовлечён». Списывают его обе
// ветки одним и тем же `est`, но списать обязана РОВНО ОДНА: `return` в ветке ErrBudgetExhausted и
// есть вся защита от второй двери. Проба меряет ОБА следствия: попытка закрыта один раз, и код
// причины остался СВОИМ — иначе на графике «нам рвёт провод» оказался бы наш собственный потолок.
//
// МУТАЦИЯ: поставить ветку ProviderEngaged ПЕРЕД веткой ErrBudgetExhausted → код становится
// `provider_cut`, проба краснеет (число закрытий при этом остаётся 1 — потому и проверяются оба).
func TestABurnedCeilingStillPaysThroughItsOwnDoorOnly(t *testing.T) {
	rig := newDraftRig(t, http.StatusOK, "")
	rig.stub.raw = `{"choices":[{"message":{"content":""},"finish_reason":"length"}],` +
		`"usage":{"prompt_tokens":1200,"completion_tokens":8000,"total_tokens":9200}}`

	_, err := rig.srv.DraftDesignIdea(designRunCtx(), draftConstructionRequest())
	require.Error(t, err)

	require.Len(t, rig.finished, 1, "одна попытка — одно списание, сколько бы дверей ни вело к нему")
	require.Equal(t, designReasonBudgetExhausted, rig.finished[0].ErrorCode,
		"наш потолок не должен выглядеть как оборванный провод")
	require.Equal(t, designDraftIdeaEstimate(1, true).Decimal.String(),
		rig.finished[0].Price.Decimal.String(), "и платится он ровно один раз, той же оценкой")
	require.Len(t, rig.failed, 1)
	require.Equal(t, designReasonBudgetExhausted, rig.failed[0].ErrorCode)
}

// ПОВТОР ПОД ТЕМ ЖЕ КЛЮЧОМ НЕ ПЛАТИТ ВТОРОЙ РАЗ.
//
// Прогон, закрытый `provider_cut`, остаётся `failed` навсегда (предикат перехвата резюмирует
// только pending|running). Второе нажатие с ТЕМ ЖЕ client_request_id обязано вернуть тот же отказ,
// НЕ ЗАВОДЯ платной попытки, — и обязано сказать про деньги, иначе человек уйдёт с экрана,
// считая оборванный вызов бесплатным.
//
// СТРОГИЙ МОК И ЕСТЬ ПОЛОВИНА УТВЕРЖДЕНИЯ: StartAttempt и FinishAttempt здесь НЕ ОЖИДАЮТСЯ
// ВОВСЕ, поэтому любой платный шаг покраснеет сам, без единого require.
//
// МУТАЦИЯ: снять ветку `run.Status == entity.DesignRunFailed` из идемпотентного повтора → падает
// на неожиданном вызове CompleteRun/StartAttempt.
func TestAnIdempotentRepeatOfACutDraftDoesNotPayAgain(t *testing.T) {
	rig := newDraftIdeaRig(t, openrouter.New(openrouter.Config{
		APIKey: "test-key", BaseURL: deadModelAddr(t),
	}))
	prior := entity.DesignRun{
		Id: 902, TechCardId: designRunCardID, Kind: entity.DesignRunKindDraftIdea,
		Status:    entity.DesignRunFailed,
		ErrorCode: sql.NullString{String: designReasonProviderCut, Valid: true},
	}
	rig.design.EXPECT().StartRun(mock.Anything, mock.AnythingOfType("entity.DesignRunStart")).
		Return(&entity.DesignRunStarted{Run: prior, Idempotent: true}, nil).Once()

	_, err := rig.srv.DraftDesignIdea(designRunCtx(), draftRequest())
	require.Error(t, err)
	code, md := errorReason(t, err)
	require.Equal(t, codes.FailedPrecondition, code)
	require.Equal(t, designReasonProviderCut, md["reason"])
	require.Contains(t, err.Error(), "charged",
		"повтор не платит, но и не умалчивает, что первый раз был оплачен")
}

// ═══════ ДВЕ ЗАКРЫВАЮЩИЕ ЗАПИСИ — ДВА БЮДЖЕТА, А НЕ ОДИН НА ОБЕ ═══════
//
// ЧТО БЫЛО. designFailDraftAs ставил ОДИН context.WithTimeout(designCloseWriteBudget) и отдавал его
// И FinishAttempt, И FailRun. Съев бюджет, первая оставляла второй ИСТЁКШИЙ контекст, а BeginTx на
// истёкшем отказывает СРАЗУ.
//
// ⚠ ВРЕМЯ ЕСТ НЕ СОН ПОВТОРОВ, И ПРЕЖНЕЕ ЧИСЛО ЗДЕСЬ БЫЛО ЛОЖНЫМ ВТРОЕ. Стояло «до пяти повторов
// по дедлоку с паузой в 300 ms, то есть полторы секунды одного только сна». 300 ms — это ПОТОЛОК
// паузы (txRetryMaxDelay), а не пауза: txRetryBackoff даёт 10ms << attempt, и при maxTxRetries = 5
// сны складываются в 10+20+40+80+160 = 310 ms, максимум ~465 ms с 50% джиттера; потолка не
// достигает ни один из них. Съедает бюджет САМА ПОПЫТКА: обе записи — SERIALIZABLE-транзакции к
// управляемому MySQL через TLS, у первой семь операторов, включая moveBudgetDay, и каждый вправе
// ЖДАТЬ чужой next-key lock столько, сколько его держит сосед. Этот отрезок не ограничен ни нашей
// паузой, ни нашими пятью секундами — и потому два круга подряд в общий бюджет не помещаются.
//
// ЧЕМ ЭТО КОНЧАЛОСЬ — И ЭТО ПРОВЕРЕНО ПО ИСХОДНИКУ ПОДМЕТАЛЬЩИКОВ, А НЕ ПРЕДПОЛОЖЕНО. Списание
// записано, прогон НЕ закрыт, releaseRunReserve не позвана. Строку рода `draft_idea` не подбирает
// НИКТО: StartRun вставляет `pending`, `running` ставит только ClaimRuns, а она этот род не берёт
// (`kind <> 'draft_idea'`). Мётел в ReviveExpiredRuns ТРИ, а не две (прежняя приписка считала
// неверно): возврат в очередь и closeRunsPastTheirCeiling фильтруют ровно `status='running'`, а
// sweepAbandonedCancelledRuns берёт `status IN ('pending','running')` — мимо неё строка проходит
// по другому условию, `cancel_requested_at IS NOT NULL`, которого у неотменённого прогона нет.
// Вывод не изменился: резерв дня висит до полуночи, а как только истечёт лиза, строка становится
// добычей следующего повтора того же client_request_id (designRunResumableSQL) — то есть второго
// платного вызова.
//
// МУТАЦИЯ: вернуть общий контекст (один WithTimeout на обе записи) → краснеет.
func TestTheTwoClosingWritesDoNotShareOneBudget(t *testing.T) {
	rig := newDraftRig(t, http.StatusInternalServerError, "")
	// Первая запись занимает ощутимое время — ровно то, что делал дедлочный повтор.
	rig.finishDelay = 200 * time.Millisecond

	_, err := rig.srv.DraftDesignIdea(designRunCtx(), draftRequest())
	require.Error(t, err)

	// ПОЛОЖИТЕЛЬНЫЙ КОНТРОЛЬ: обе записи вообще состоялись и обе несли срок. Без него проба
	// зеленела бы в мире, где закрывающих записей нет вовсе.
	require.Len(t, rig.finishedDeadline, 1, "первая закрывающая запись не пошла в стор со сроком")
	require.Len(t, rig.failedDeadline, 1, "прогон не закрывался вовсе — резерв повис бы до полуночи")

	require.NoError(t, rig.failedCtxErr[0],
		"FailRun получила уже истёкший контекст: BeginTx откажет сразу, и прогон не закроется НИКОГДА")

	gap := rig.failedDeadline[0].Sub(rig.finishedDeadline[0])
	require.GreaterOrEqual(t, gap, rig.finishDelay/2,
		"дедлайны двух записей разошлись всего на %s: срок второй отсчитан не от неё самой, "+
			"а унаследован от первой — то есть бюджет снова общий", gap)

	require.GreaterOrEqual(t, rig.failedRemaining[0], designCloseWriteBudget-50*time.Millisecond,
		"у закрытия прогона осталось %s из %s: первая запись съела чужое время",
		rig.failedRemaining[0], designCloseWriteBudget)
}
