package design_test

import (
	"context"
	"database/sql"
	"fmt"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jekabolt/grbpwr-manager/internal/dependency"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/require"
)

// ЖИВЫЕ ПРОБЫ ДЕНЕЖНОГО СЧЁТА ОЧЕРЕДИ: потолок платежей против потолка кругов, один счёт
// провайдера против нескольких строк попытки, и отменённый прогон, которого некому вести.
//
// Обвязка (probeRepository / probeCard / resetBudget / startProbeRun / expireClaim / runStatus) —
// общая с wave2_db_test.go: второй, «свой» набор фикстур означал бы, что две половины проб полосы
// готовят базу по-разному, а расходятся такие наборы молча.

// ─────────────────────── общее для проб этого файла ───────────────────────

// reclaimProbeRun возвращает прогон в работу и отдаёт СВЕЖИЙ токен захвата — ровно то, что делает
// воркер в начале круга. Без сброса next_attempt_at круг не начался бы: FailRun ставит экспоненту.
func reclaimProbeRun(t *testing.T, rep dependency.Repository, raw *sql.DB, runID int) string {
	t.Helper()
	_, err := raw.Exec(`UPDATE design_run SET next_attempt_at = UTC_TIMESTAMP(6) WHERE id = ?`, runID)
	require.NoError(t, err)
	token := uuid.NewString()
	claimed, err := rep.Design().ClaimRuns(context.Background(), 32, time.Minute, token)
	require.NoError(t, err)
	for _, r := range claimed {
		if r.Id == runID {
			return token
		}
	}
	t.Fatalf("прогон %d не вернулся в очередь и не был захвачен", runID)
	return ""
}

// attemptStates — состояния всех попыток прогона, по номеру. Читается сырым запросом: предмет
// проверки — то, что ЛЕЖИТ В БАЗЕ, а не то, что вернул тот же метод, который туда и писал.
func attemptStates(t *testing.T, raw *sql.DB, runID int) map[int]string {
	t.Helper()
	rows, err := raw.Query(`SELECT attempt_no, state FROM design_run_attempt WHERE run_id = ? ORDER BY attempt_no`, runID)
	require.NoError(t, err)
	defer rows.Close()
	out := map[int]string{}
	for rows.Next() {
		var no int
		var state string
		require.NoError(t, rows.Scan(&no, &state))
		out[no] = state
	}
	require.NoError(t, rows.Err())
	return out
}

// ─────────────────────── 1. бесплатный опрос не жжёт денежный потолок ───────────────────────

// ОПЛАЧЕННАЯ ОДИН РАЗ 3D-ЗАДАЧА НЕ УМИРАЕТ НА ЧЕТВЁРТОМ БЕСПЛАТНОМ ОКНЕ ОЖИДАНИЯ.
//
// ЧТО БЫЛО. Потолок стоял на attempt_count (`run.AttemptCount+1 < designMaxAttempts`), а на
// асинхронной дороге attempt_count растёт и от БЕСПЛАТНЫХ опросов: отправка — своя строка попытки,
// каждый сбор — своя. Прогон, купленный однажды и ни разу не купленный повторно, закрывался
// терминально на третьем круге — при том что модель у провайдера, возможно, ещё строилась.
//
// ЧТО ПРОВЕРЯЕТСЯ ЗДЕСЬ: круги идут, пока их не исчерпает ПОТОЛОК КРУГОВ, а денежный потолок при
// одном платеже не срабатывает вовсе.
func TestDesignDBFreePollsDoNotBurnThePaidCap(t *testing.T) {
	rep, raw := probeRepository(t)
	resetBudget(t, raw)
	card := probeCard(t, raw)
	started := startProbeRun(t, rep, card, "0.40")
	ctx := context.Background()
	const task = "task-42"

	// ─── КРУГ ПЕРВЫЙ: ЕДИНСТВЕННАЯ ПЛАТНАЯ ОТПРАВКА, И СРАЗУ ЖЕ БЕСПЛАТНЫЙ СБОР ───
	token := reclaimProbeRun(t, rep, raw, started.Run.Id)
	submit, err := rep.Design().StartAttempt(ctx, entity.DesignAttemptStart{
		RunId: started.Run.Id, ClaimToken: token, Provider: "meshy",
	})
	require.NoError(t, err)
	// Отправка закрывается как `accepted` с id задачи и БЕЗ ЦЕНЫ: consumed_credits называет только
	// законченная задача. Ровно эта строка и есть признак «дальше опросы, а не покупки».
	require.NoError(t, rep.Design().FinishAttempt(ctx, entity.DesignAttemptFinish{
		RunId: started.Run.Id, AttemptNo: submit.AttemptNo,
		State: entity.DesignAttemptAccepted, ProviderRequestId: task,
	}))

	rounds := 0
	status := entity.DesignRunPending
	for status == entity.DesignRunPending {
		rounds++
		if rounds > 1 {
			token = reclaimProbeRun(t, rep, raw, started.Run.Id)
		}
		collect, err := rep.Design().StartAttempt(ctx, entity.DesignAttemptStart{
			RunId: started.Run.Id, ClaimToken: token, Provider: "meshy",
		})
		require.NoError(t, err)
		// Опрос не дождался задачи: цена неизвестна, деньги не двигаются, id задачи тот же.
		require.NoError(t, rep.Design().FinishAttempt(ctx, entity.DesignAttemptFinish{
			RunId: started.Run.Id, AttemptNo: collect.AttemptNo,
			State: entity.DesignAttemptUnknown, ProviderRequestId: task,
			ErrorCode: "provider_timeout",
		}))
		run, err := rep.Design().FailRun(ctx, entity.DesignRunFail{
			RunId: started.Run.Id, ClaimToken: token,
			ErrorCode: "provider_timeout", LastError: "the task is still building", Retryable: true,
		})
		require.NoError(t, err)
		status = run.Status
		require.Less(t, rounds, 40, "круги обязаны кончиться: потолок кругов не сработал вовсе")
	}

	// ГЛАВНОЕ ЧИСЛО. При потолке кругов 10 круг первый доводит attempt_count до 2 (две строки
	// попытки), каждый следующий — до k+1, и терминальным становится тот, на котором счётчик
	// достиг потолка. Под прежним кодом (потолок 5 по attempt_count) кругов было ТРИ.
	require.Equal(t, 9, rounds,
		"оплаченная однажды задача обязана пережить бесплатные опросы до потолка КРУГОВ")
	require.Equal(t, entity.DesignRunFailed, status)

	states := attemptStates(t, raw, started.Run.Id)
	accepted := 0
	for _, s := range states {
		if s == entity.DesignAttemptAccepted {
			accepted++
		}
	}
	require.Equal(t, 1, accepted, "платёж был ровно один: %v", states)
	require.Len(t, states, rounds+1, "одна отправка плюс по опросу на круг: %v", states)

	budget, err := rep.Design().GetBudget(ctx)
	require.NoError(t, err)
	require.True(t, budget.Spent.IsZero(),
		"цену задачи не назвал никто, и выдумывать её нечем: spent = %s", budget.Spent)
	require.True(t, budget.Reserved.IsZero(),
		"терминальный переход обязан вернуть дню зарезервированное: %s", budget.Reserved)
}

// ─────────────────────── 2. один счёт провайдера двигает день один раз ───────────────────────

// ОДИН ПЛАТЁЖ MESHY УЧИТЫВАЕТСЯ ОДИН РАЗ, СКОЛЬКО БЫ СБОРОВ ЕГО НИ НАЗВАЛИ.
//
// ЧТО БЫЛО. Платёж один (при создании задачи), а строк попытки несколько, и FinishAttempt двигал
// дневной spent на КАЖДОЙ, у которой проставлена цена. Повторный сбор — а он случается всякий раз,
// когда прогон вернулся в очередь после удачного сбора: не сложилась загрузка в бакет, перехватили
// строку — спрашивал провайдера про ту же задачу, получал те же consumed_credits и прибавлял ту же
// сумму второй раз. Дневной потолок съедался вымышленными тратами.
func TestDesignDBOneProviderChargeMovesTheDayOnce(t *testing.T) {
	rep, raw := probeRepository(t)
	resetBudget(t, raw)
	card := probeCard(t, raw)
	started := startProbeRun(t, rep, card, "0.40")
	ctx := context.Background()

	token := reclaimProbeRun(t, rep, raw, started.Run.Id)
	price := decimal.NullDecimal{Decimal: decimal.RequireFromString("0.60"), Valid: true}

	open := func(state, prid string, p decimal.NullDecimal) int {
		t.Helper()
		att, err := rep.Design().StartAttempt(ctx, entity.DesignAttemptStart{
			RunId: started.Run.Id, ClaimToken: token, Provider: "meshy",
		})
		require.NoError(t, err)
		require.NoError(t, rep.Design().FinishAttempt(ctx, entity.DesignAttemptFinish{
			RunId: started.Run.Id, AttemptNo: att.AttemptNo,
			State: state, ProviderRequestId: prid, Price: p,
		}))
		return att.AttemptNo
	}
	spent := func() decimal.Decimal {
		t.Helper()
		budget, err := rep.Design().GetBudget(ctx)
		require.NoError(t, err)
		return budget.Spent
	}

	// Отправка: цену никто ещё не знает, NULL — слово схемы про это.
	open(entity.DesignAttemptAccepted, "task-42", decimal.NullDecimal{})
	require.True(t, spent().IsZero(), "отправка цены не называет: %s", spent())

	// Первый сбор: цена приехала, и день обязан её увидеть.
	open(entity.DesignAttemptDelivered, "task-42", price)
	require.True(t, spent().Equal(decimal.RequireFromString("0.6")),
		"счёт провайдера обязан попасть в журнал трат: %s", spent())

	// ⚠ ВТОРОЙ СБОР ТОЙ ЖЕ ЗАДАЧИ — СВОЯ, ЕЩЁ НЕ ЗАКРЫТАЯ СТРОКА ПОПЫТКИ И ТА ЖЕ СУММА.
	second := open(entity.DesignAttemptDelivered, "task-42", price)
	require.True(t, spent().Equal(decimal.RequireFromString("0.6")),
		"повторный сбор одной задачи удвоил траты владельца: %s", spent())

	var secondPrice decimal.NullDecimal
	require.NoError(t, raw.QueryRow(
		`SELECT price FROM design_run_attempt WHERE run_id = ? AND attempt_no = ?`,
		started.Run.Id, second).Scan(&secondPrice))
	require.False(t, secondPrice.Valid,
		"бесплатный опрос стоил ноль, и цена дубля не должна попасть даже в price_actual")

	run, err := rep.Design().GetRun(ctx, started.Run.Id)
	require.NoError(t, err)
	require.True(t, run.PriceActual.Decimal.Equal(decimal.RequireFromString("0.6")),
		"price_actual — СУММА цен попыток, и она обязана остаться ценой одной модели: %s",
		run.PriceActual.Decimal)

	// ⚠ ПОЛОЖИТЕЛЬНЫЙ КОНТРОЛЬ, БЕЗ КОТОРОГО ПРОБА ЗЕЛЕНЕЛА БЫ И У СТОРОЖА, НЕ ПИШУЩЕГО ДЕНЬГИ
	// ВОВСЕ: ДРУГАЯ задача провайдера — другой счёт, и он двигает день как обычно.
	open(entity.DesignAttemptDelivered, "task-99", decimal.NullDecimal{
		Decimal: decimal.RequireFromString("0.10"), Valid: true,
	})
	require.True(t, spent().Equal(decimal.RequireFromString("0.7")),
		"второй, настоящий счёт обязан попасть в журнал: %s", spent())
}

// ─────────────────────── 3. отменённый и брошенный доходит до терминала ───────────────────────

// ОТМЕНЁННЫЙ ПРОГОН, ЧЕЙ ВОРКЕР УМЕР, ПРИХОДИТ В ТЕРМИНАЛЬНОЕ СОСТОЯНИЕ И ОТПУСКАЕТ РЕЗЕРВ.
//
// ЧТО БЫЛО. Человек отменяет идущий прогон — строка остаётся воркеру с cancel_requested_at. Воркер
// умирает на редеплое. Подметальщик возвращает строку в `pending`, а предикат захвата отменённые не
// берёт (`cancel_requested_at IS NULL`): терминального перехода не будет НИКОГДА, а он единственный
// снимает резерв дня. Резерв висел до полуночи, занимая деньги дня прогоном, которого нет.
func TestDesignDBCancelledAndAbandonedRunReachesTerminalAndReleasesItsReserve(t *testing.T) {
	rep, raw := probeRepository(t)
	resetBudget(t, raw)
	card := probeCard(t, raw)
	started := startProbeRun(t, rep, card, "0.40")
	ctx := context.Background()

	token := reclaimProbeRun(t, rep, raw, started.Run.Id)
	require.NotEmpty(t, token)

	run, err := rep.Design().CancelRun(ctx, started.Run.Id, "probe")
	require.NoError(t, err)
	require.Equal(t, entity.DesignRunRunning, run.Status,
		"идущий прогон отменой не закрывается — его ведёт воркер, и результат может прийти секундой позже")
	require.True(t, run.CancelRequestedAt.Valid)

	// ⚠ ПОЛОЖИТЕЛЬНЫЙ КОНТРОЛЬ ЖИВОСТИ ЛИЗЫ: пока захват жив, строку ведёт живой воркер, и
	// подметальщику брать её нельзя — иначе он отберёт задание у того, кто за него платит.
	_, err = rep.Design().ReviveExpiredRuns(ctx)
	require.NoError(t, err)
	status, _ := runStatus(t, raw, started.Run.Id)
	require.Equal(t, entity.DesignRunRunning, status, "живая лиза не подметается")
	budget, err := rep.Design().GetBudget(ctx)
	require.NoError(t, err)
	require.True(t, budget.Reserved.Equal(decimal.RequireFromString("0.4")),
		"резерв идущего прогона на месте: %s", budget.Reserved)

	// ─── ВОРКЕР УМЕР ───
	expireClaim(t, raw, started.Run.Id)
	_, err = rep.Design().ReviveExpiredRuns(ctx)
	require.NoError(t, err)

	status, claim := runStatus(t, raw, started.Run.Id)
	require.Equal(t, entity.DesignRunCancelled, status,
		"брошенная отменённая строка обязана прийти в терминальное состояние, а не залечь в pending")
	require.False(t, claim.Valid, "закрытая строка не принадлежит никому")

	budget, err = rep.Design().GetBudget(ctx)
	require.NoError(t, err)
	require.True(t, budget.Reserved.IsZero(),
		"терминальный переход обязан вернуть дню зарезервированное: %s", budget.Reserved)
}

// ─────────────────────── 4. потолок платежей — пять, а не четыре ───────────────────────

// ПЯТЬ ОПЛАЧЕННЫХ ПОПЫТОК, КАК И ОБЕЩАЛИ ТРИ КОММЕНТАРИЯ.
//
// ЧТО БЫЛО. `run.AttemptCount+1 < designMaxAttempts` при константе 5 давало ЧЕТЫРЕ платных вызова.
// Владелец, читая «потолок пять», получал четыре — расхождение не денежно опасное, но делающее
// названную политику неправдой; а всё, что рядом с деньгами, обязано значить ровно то, что сказано.
func TestDesignDBTheAttemptCapIsFivePaidCallsNotFour(t *testing.T) {
	rep, raw := probeRepository(t)
	resetBudget(t, raw)
	card := probeCard(t, raw)
	started := startProbeRun(t, rep, card, "0.10")
	ctx := context.Background()

	paid := 0
	status := entity.DesignRunPending
	for status == entity.DesignRunPending {
		token := reclaimProbeRun(t, rep, raw, started.Run.Id)
		att, err := rep.Design().StartAttempt(ctx, entity.DesignAttemptStart{
			RunId: started.Run.Id, ClaimToken: token, Provider: "openrouter",
		})
		require.NoError(t, err)
		paid++
		// У каждого платного вызова СВОЙ id ответа — это разные счета, и склеивать их нечем.
		require.NoError(t, rep.Design().FinishAttempt(ctx, entity.DesignAttemptFinish{
			RunId: started.Run.Id, AttemptNo: att.AttemptNo,
			State: entity.DesignAttemptFailed, ProviderRequestId: fmt.Sprintf("req-%d", att.AttemptNo),
			Price:     decimal.NullDecimal{Decimal: decimal.RequireFromString("0.01"), Valid: true},
			ErrorCode: "provider_unavailable",
		}))
		run, err := rep.Design().FailRun(ctx, entity.DesignRunFail{
			RunId: started.Run.Id, ClaimToken: token,
			ErrorCode: "provider_unavailable", LastError: "weather", Retryable: true,
		})
		require.NoError(t, err)
		status = run.Status
		require.Less(t, paid, 20, "потолок платежей не сработал вовсе")
	}

	require.Equal(t, 5, paid, "потолок обещает пять ОПЛАЧЕННЫХ попыток")
	require.Equal(t, entity.DesignRunFailed, status)

	budget, err := rep.Design().GetBudget(ctx)
	require.NoError(t, err)
	require.True(t, budget.Spent.Equal(decimal.RequireFromString("0.05")),
		"пять разных счетов по 0.01 — пять записей в журнале трат: %s", budget.Spent)
}
