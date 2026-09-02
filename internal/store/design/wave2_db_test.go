package design_test

import (
	"context"
	"database/sql"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	_ "github.com/go-sql-driver/mysql"
	"github.com/google/uuid"
	"github.com/jekabolt/grbpwr-manager/internal/dependency"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/jekabolt/grbpwr-manager/internal/store"
	"github.com/jekabolt/grbpwr-manager/internal/store/design"
	"github.com/jmoiron/sqlx"
	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/require"
)

// ЖИВЫЕ ПРОБЫ ГЕНЕРАТИВНОЙ ПОЛОВИНЫ — ДЕНЬГИ, ЗАХВАТ, ТОКЕН.
//
// ⚠ КАК ОНИ ОТКАЗЫВАЮТСЯ ТРОГАТЬ ПРОД. Пакет internal/store вне CI читает config/config.toml, где
// лежит ПРОДОВЫЙ DSN, и на очистке ДРОПАЕТ ТАБЛИЦЫ. В этом файле такого пути нет вовсе: DSN
// собирается ИСКЛЮЧИТЕЛЬНО из переменных CI, без CI=1 каждая проба пропускается ДО открытия
// соединения, а имя базы, не похожее на пробное, отвергается отдельно. Это свойство кода, а не
// привычка запускающего.
//
// Запуск (одноразовый контейнер, база пересоздаётся между прогонами — очистка TestMain пакета
// store обрывается на внешних ключах):
//
//	docker run -d --name grbpwr-design-b3 -e MYSQL_ROOT_PASSWORD=probe \
//	  -e MYSQL_DATABASE=grbpwr_probe -p 33107:3306 mysql:8.0
//	CI=1 MYSQL_HOST=127.0.0.1 MYSQL_PORT=33107 MYSQL_USER=root MYSQL_PASSWORD=probe \
//	  MYSQL_DATABASE=grbpwr_probe go test -count=1 -run TestDesignDB ./internal/store/design/
//
// ВНЕШНИЙ ТЕСТОВЫЙ ПАКЕТ (design_test), потому что репозиторий строит internal/store, который
// импортирует этот пакет. Собирать здесь второй, самодельный репозиторий значило бы проверять не
// тот стор, который поедет на бету, — а именно это и есть цена «своей» пробной обвязки.

var (
	probeOnce sync.Once
	probeRep  dependency.Repository
	probeRaw  *sql.DB
	probeErr  error
)

func probeRepository(t *testing.T) (dependency.Repository, *sql.DB) {
	t.Helper()
	if os.Getenv("CI") != "1" {
		t.Skip("design band DB probes run only against a disposable container (CI=1)")
	}
	host, port := os.Getenv("MYSQL_HOST"), os.Getenv("MYSQL_PORT")
	user, pass, name := os.Getenv("MYSQL_USER"), os.Getenv("MYSQL_PASSWORD"), os.Getenv("MYSQL_DATABASE")
	if host == "" || port == "" || user == "" || name == "" {
		t.Skip("MYSQL_* of the disposable container are not set")
	}
	// ПОСЛЕДНИЙ ГЕЙТ ПО ИМЕНИ. Даже под CI=1 база, чьё имя не говорит, что она пробная,
	// отвергается: цена ошибки здесь — продовая схема.
	if name == "grbpwr" || name == "grbpwr_beta" {
		t.Fatalf("refusing to run destructive probes against %q", name)
	}
	dsn := fmt.Sprintf("%s:%s@tcp(%s:%s)/%s?parseTime=true&multiStatements=true",
		user, pass, host, port, name)
	probeOnce.Do(func() {
		ctx := context.Background()
		probeRaw, probeErr = sql.Open("mysql", dsn)
		if probeErr != nil {
			return
		}
		if probeErr = probeRaw.Ping(); probeErr != nil {
			return
		}
		probeRep, probeErr = store.NewForTest(ctx, store.Config{
			DSN:                dsn,
			Automigrate:        true,
			MaxOpenConnections: 10,
			MaxIdleConnections: 5,
		})
	})
	require.NoError(t, probeErr)
	require.NotNil(t, probeRep)
	return probeRep, probeRaw
}

// ─────────────────────── фикстуры ───────────────────────

func probeCard(t *testing.T, raw *sql.DB) int {
	t.Helper()
	res, err := raw.Exec(`INSERT INTO tech_card (style_number, name, brand, target_gender, top_category_id)
		VALUES (CONCAT('DSG-', UUID_SHORT()), 'design band probe', 'b', 'unisex', 1)`)
	require.NoError(t, err)
	id, err := res.LastInsertId()
	require.NoError(t, err)
	t.Cleanup(func() { _, _ = raw.Exec(`DELETE FROM tech_card WHERE id = ?`, id) })
	return int(id)
}

func probeMedia(t *testing.T, raw *sql.DB) int {
	t.Helper()
	key := uuid.NewString()
	res, err := raw.Exec(`INSERT INTO media
		(full_size, full_size_width, full_size_height, thumbnail, thumbnail_width, thumbnail_height,
		 compressed, compressed_width, compressed_height)
		VALUES (?, 100, 100, ?, 10, 10, ?, 50, 50)`,
		"probe/"+key+"-full.png", "probe/"+key+"-thumb.png", "probe/"+key+"-small.png")
	require.NoError(t, err)
	id, err := res.LastInsertId()
	require.NoError(t, err)
	// Медиа удаляется ПОСЛЕ карточки: design_picture держит media(id) через RESTRICT, а карточка
	// уносит свои картинки каскадом. t.Cleanup исполняется LIFO, поэтому этот вызов должен быть
	// зарегистрирован ПОСЛЕ probeCard — за этим следят сами пробы.
	t.Cleanup(func() { _, _ = raw.Exec(`DELETE FROM media WHERE id = ?`, id) })
	return int(id)
}

// resetBudget приводит день к известному состоянию. design_budget_day — синглтон организации, а
// не карточки, поэтому пробы денег обязаны начинаться с этого вызова, иначе они читают остатки
// соседа.
//
// ⚠ ПОТОЛКА ЗДЕСЬ БОЛЬШЕ НЕТ (0358): у функции был аргумент `cap`, писавший
// design_settings.daily_budget, а колонка удалена вместе с самим понятием. Ставить было бы нечего
// и незачем.
func resetBudget(t *testing.T, raw *sql.DB) {
	t.Helper()
	_, err := raw.Exec(`DELETE FROM design_budget_day`)
	require.NoError(t, err)
}

func startProbeRun(t *testing.T, rep dependency.Repository, card int, est string) *entity.DesignRunStarted {
	t.Helper()
	started, err := rep.Design().StartRun(context.Background(), entity.DesignRunStart{
		TechCardId:       card,
		ClientRequestId:  uuid.NewString(),
		Kind:             entity.DesignRunKindFlat,
		RequestedOutputs: 1,
		PriceEstimate:    decimal.NullDecimal{Decimal: decimal.RequireFromString(est), Valid: true},
		Author:           "probe",
	})
	require.NoError(t, err)
	return started
}

func expireClaim(t *testing.T, raw *sql.DB, runID int) {
	t.Helper()
	_, err := raw.Exec(
		`UPDATE design_run SET claim_expires_at = DATE_SUB(UTC_TIMESTAMP(6), INTERVAL 1 HOUR) WHERE id = ?`,
		runID)
	require.NoError(t, err)
}

func runStatus(t *testing.T, raw *sql.DB, runID int) (string, sql.NullString) {
	t.Helper()
	var status string
	var token sql.NullString
	require.NoError(t, raw.QueryRow(`SELECT status, claim_token FROM design_run WHERE id = ?`, runID).
		Scan(&status, &token))
	return status, token
}

// orphanedAfter — половина компенсации сирот на стороне вызывающего: что из загруженных байт
// строка НЕ усыновила. Ровно этот расчёт делает воркер после CompleteRun.
func orphanedAfter(minted []int, run *entity.DesignRun) []int {
	adopted := make([]int, 0, len(run.Pictures))
	for _, p := range run.Pictures {
		adopted = append(adopted, p.MediaId)
	}
	return design.OrphanedMedia(minted, adopted)
}

// ─────────────────────── деньги ───────────────────────

// РЕЗЕРВ И ВСТАВКА — ОДНА ТРАНЗАКЦИЯ, А ПОВТОР НЕ ПЛАТИТ ВТОРОЙ РАЗ.
func TestDesignDBStartRunReservesOnceForOneRequestID(t *testing.T) {
	rep, raw := probeRepository(t)
	resetBudget(t, raw)
	card := probeCard(t, raw)
	ctx := context.Background()

	req := uuid.NewString()
	first, err := rep.Design().StartRun(ctx, entity.DesignRunStart{
		TechCardId: card, ClientRequestId: req, Kind: entity.DesignRunKindFlat,
		RequestedOutputs: 1, Author: "probe",
		PriceEstimate: decimal.NullDecimal{Decimal: decimal.RequireFromString("0.50"), Valid: true},
	})
	require.NoError(t, err)
	require.False(t, first.Idempotent)
	require.Equal(t, entity.DesignRunPending, first.Run.Status)
	require.True(t, first.Budget.Reserved.Equal(decimal.RequireFromString("0.5")),
		"резерв обязан быть виден сразу, вместе с кликом: %s", first.Budget.Reserved)

	second, err := rep.Design().StartRun(ctx, entity.DesignRunStart{
		TechCardId: card, ClientRequestId: req, Kind: entity.DesignRunKindFlat,
		RequestedOutputs: 1, Author: "probe",
		PriceEstimate: decimal.NullDecimal{Decimal: decimal.RequireFromString("0.50"), Valid: true},
	})
	require.NoError(t, err)
	require.True(t, second.Idempotent, "повтор после сетевого таймаута — тот же прогон, а не второй")
	require.Equal(t, first.Run.Id, second.Run.Id)
	require.True(t, second.Budget.Reserved.Equal(decimal.RequireFromString("0.5")),
		"повтор НЕ резервирует второй раз: %s", second.Budget.Reserved)
}

// ПОТОЛКА НЕТ: ПРОГОН НЕ ОТКАЗЫВАЕТСЯ ПО ДЕНЬГАМ, СКОЛЬКО БЫ ДЕНЬ УЖЕ НИ СТОИЛ (0358, L-8).
//
// ЗДЕСЬ БЫЛИ ДВЕ ПРОБЫ — «потолок отказывает и откатывает свой резерв» и «ноль значит сегодня не
// запускаем», — и обе утверждали то, что владелец потребовал убрать: «у нас в принципе не должно
// быть потолка похуй чем он съеден убери потолок». Они заменены утверждением обратного, и это не
// удаление покрытия: сумма, на которой прежняя проба отказывала, здесь ПРЕВЫШЕНА ВДВОЕ, и прогон
// обязан пройти.
//
// МУТАЦИЯ, КОТОРУЮ ЛОВИТ: вернуть в StartRun любую из двух прежних проверок.
func TestDesignDBStartRunIsNeverRefusedForMoney(t *testing.T) {
	rep, raw := probeRepository(t)
	resetBudget(t, raw)
	card := probeCard(t, raw)
	ctx := context.Background()

	// Сумма, которая при прежнем потолке в $1.00 закрывала полосу на день с запасом.
	for i := 0; i < 4; i++ {
		startProbeRun(t, rep, card, "0.60")
	}
	started, err := rep.Design().StartRun(ctx, entity.DesignRunStart{
		TechCardId: card, ClientRequestId: uuid.NewString(), Kind: entity.DesignRunKindFlat,
		RequestedOutputs: 1, Author: "probe",
		PriceEstimate: decimal.NullDecimal{Decimal: decimal.RequireFromString("99.00"), Valid: true},
	})
	require.NoError(t, err, "ни одна сумма не имеет права закрыть полосу")
	require.Equal(t, entity.DesignRunPending, started.Run.Status)
}

// ПОТОЛОК НЕГДЕ ДАЖЕ ВЫРАЗИТЬ — И ЭТО ЕДИНСТВЕННОЕ, ЧТО ОТЛИЧАЕТ «СНЯЛИ ПОНЯТИЕ» ОТ «ПОДНЯЛИ ЧИСЛО».
//
// Требование звучало «убери потолок», а не «поставь побольше», и разница между этими двумя
// прочтениями видна только в схеме: пока колонка жива, любой UPDATE одной строкой возвращает
// жалобу владельца целиком, и ни один тест поведения этого не заметит — прогон откажется ровно
// так, как раньше. Проба смотрит туда, где живёт возможность: в information_schema.
//
// Проверяется отсутствие ЛЮБОЙ денежной колонки на design_settings, а не только имени
// daily_budget: `monthly_budget` или `spend_limit` были бы тем же понятием под другим словом, и
// проба обязана поймать возврат смысла, а не возврат строки.
//
// НА ПРОВОДЕ ЭТО ЖЕ ЗАПРЕЩЕНО СИЛЬНЕЕ, ЧЕМ ПРОБОЙ: поле 4 сообщения DesignBudget объявлено
// `reserved 4; reserved "cap";`, и protoc откажется собрать файл, где его переиспользуют. Там
// проба не нужна — состояние невыразимо по построению. Здесь MySQL таких слов не знает.
//
// МУТАЦИЯ, КОТОРУЮ ЛОВИТ: заменить DROP COLUMN в 0358 на 'SELECT 1'.
func TestDesignDBACeilingHasNowhereToLive(t *testing.T) {
	_, raw := probeRepository(t)

	rows, err := raw.Query(`
		SELECT COLUMN_NAME FROM information_schema.COLUMNS
		WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'design_settings'`)
	require.NoError(t, err)
	defer rows.Close()

	var seen []string
	for rows.Next() {
		var name string
		require.NoError(t, rows.Scan(&name))
		seen = append(seen, name)
		lower := strings.ToLower(name)
		for _, word := range []string{"budget", "cap", "limit", "ceiling", "quota", "max_spend"} {
			// budget_timezone — не деньги, а часовой пояс, в котором считается ДЕНЬ.
			if name == "budget_timezone" {
				continue
			}
			require.NotContains(t, lower, word,
				"колонка %q возвращает понятие потолка на design_settings: убрано было ПОНЯТИЕ, "+
					"а не число", name)
		}
	}
	require.NoError(t, rows.Err())
	require.NotEmpty(t, seen, "таблица настроек обязана существовать: проба должна падать от "+
		"возврата потолка, а не от опечатки в имени таблицы")
}

// НО ДЕНЬГИ ПО-ПРЕЖНЕМУ СЧИТАЮТСЯ, ЗАПИСЫВАЮТСЯ И ЧИТАЮТСЯ — И ЭТО ВТОРАЯ ПОЛОВИНА ТРЕБОВАНИЯ.
//
// Владелец возражал не против учёта: поводом ко всему был прогон, стоивший $100 вместо оценки в
// $0.60, то есть он ЦЕНУ КАК РАЗ СПРАШИВАЕТ. Снята машина, решавшая, что сегодня работать нельзя,
// а не число. Проба держит ровно это: резерв виден сразу, снимается при закрытии, а фактическая
// цена садится в потраченное.
//
// МУТАЦИЯ, КОТОРУЮ ЛОВИТ: убрать вызов moveBudgetDay из StartRun — потолок бы «не вернулся», а
// учёт исчез бы молча, что ровно противоположно просьбе.
func TestDesignDBSpendIsStillMeasuredWithoutACeiling(t *testing.T) {
	rep, raw := probeRepository(t)
	resetBudget(t, raw)
	card := probeCard(t, raw)
	ctx := context.Background()

	started := startProbeRun(t, rep, card, "0.75")
	require.True(t, started.Budget.Reserved.Equal(decimal.RequireFromString("0.75")),
		"резерв обязан быть виден вместе с кликом: %s", started.Budget.Reserved)

	budget, err := rep.Design().GetBudget(ctx)
	require.NoError(t, err)
	require.True(t, budget.Reserved.Equal(decimal.RequireFromString("0.75")),
		"и читаться отдельным глаголом: %s", budget.Reserved)
	require.NotEmpty(t, budget.Day, "день по-прежнему считается в поясе организации")
	require.NotEmpty(t, budget.Currency)

	// ЗАКРЫТИЕ ПРОГОНА СНИМАЕТ РЕЗЕРВ — иначе «сколько сегодня стоило» врало бы вверх навсегда.
	claimed, err := rep.Design().ClaimRuns(ctx, 1, time.Minute, uuid.NewString())
	require.NoError(t, err)
	require.Len(t, claimed, 1)
	_, err = rep.Design().FailRun(ctx, entity.DesignRunFail{
		RunId: claimed[0].Id, ClaimToken: claimed[0].ClaimToken.String,
		ErrorCode: "probe", LastError: "probe", Retryable: false,
	})
	require.NoError(t, err)

	after, err := rep.Design().GetBudget(ctx)
	require.NoError(t, err)
	require.True(t, after.Reserved.IsZero(),
		"резерв закрытого прогона обязан уйти: %s", after.Reserved)
}

// ─────────────────────── захват ───────────────────────

// ОДНО ЗАДАНИЕ — ОДИН ВЛАДЕЛЕЦ, ДАЖЕ ПРИ ОДНОВРЕМЕННОМ ЗАХВАТЕ.
//
// МУТАЦИЯ, КОТОРАЯ ОБЯЗАНА УРОНИТЬ ЭТУ ПРОБУ: убрать повтор предиката из WHERE у UPDATE в
// ClaimRuns (оставить `WHERE id = :id`). Тогда оба воркера «захватят» строку, и второй токен
// затрёт первый — первый воркер больше никогда не сможет закрыть свой оплаченный прогон.
func TestDesignDBClaimGivesTheRunToExactlyOneWorker(t *testing.T) {
	rep, raw := probeRepository(t)
	resetBudget(t, raw)
	card := probeCard(t, raw)
	started := startProbeRun(t, rep, card, "0.10")

	var (
		wg      sync.WaitGroup
		mu      sync.Mutex
		winners []string
	)
	// СТАРТОВЫЙ БАРЬЕР. Без него две горутины не пересекаются вовсе: первая успевает закрыть свою
	// транзакцию до того, как вторая начнёт, и «ровно один победитель» становится истинным по
	// причине «одновременности не было» — то есть проба зеленела бы, ничего не измерив.
	gate := make(chan struct{})
	for i := 0; i < 2; i++ {
		token := uuid.NewString()
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-gate
			runs, err := rep.Design().ClaimRuns(context.Background(), 4, time.Minute, token)
			if err != nil {
				return
			}
			for _, r := range runs {
				if r.Id == started.Run.Id {
					mu.Lock()
					winners = append(winners, token)
					mu.Unlock()
				}
			}
		}()
	}
	close(gate)
	wg.Wait()
	require.Len(t, winners, 1, "строку обязан получить ровно один воркер")

	status, token := runStatus(t, raw, started.Run.Id)
	require.Equal(t, entity.DesignRunRunning, status)
	require.True(t, token.Valid)
	require.Equal(t, winners[0], token.String, "у строки стоит токен ПОБЕДИТЕЛЯ, а не последнего")
}

// ЖИВУЮ ЛИЗУ НЕ ПЕРЕХВАТЫВАЮТ. Это тот самый дефект первой редакции плана: предиката лизы не
// было вовсе, второй захват уводил живой токен, и первый воркер НИКОГДА не мог закрыть свой уже
// оплаченный прогон — CompleteRun сверяет токен.
//
// Проба ПОСЛЕДОВАТЕЛЬНАЯ намеренно: одновременность здесь ничего не добавляет (её закрывает
// SERIALIZABLE), а перехват живой лизы — это про ВРЕМЯ, а не про гонку.
//
// МУТАЦИЯ, КОТОРАЯ ОБЯЗАНА УРОНИТЬ ЭТУ ПРОБУ: убрать
// `AND (claim_token IS NULL OR claim_expires_at IS NULL OR claim_expires_at < UTC_TIMESTAMP(6))`
// из designRunClaimableSQL.
func TestDesignDBALiveLeaseIsNotStolen(t *testing.T) {
	rep, raw := probeRepository(t)
	resetBudget(t, raw)
	card := probeCard(t, raw)
	started := startProbeRun(t, rep, card, "0.10")
	ctx := context.Background()

	tokenA := uuid.NewString()
	first, err := rep.Design().ClaimRuns(ctx, 4, time.Minute, tokenA)
	require.NoError(t, err)
	require.Len(t, first, 1, "положительный контроль: свободную строку захват берёт")

	// СОСТОЯНИЕ, РАДИ КОТОРОГО ПРЕДИКАТ ЛИЗЫ СУЩЕСТВУЕТ, СОБИРАЕТСЯ ЯВНО: строка ЖДЁТ и при этом
	// ЗАХВАЧЕНА. Само по себе `status = 'pending'` такую строку не отсеет, а она законна и
	// сегодня — ровно её заводит StartRun для kind=draft_idea, чтобы воркер не оплатил второй
	// вызов текстовой модели, пока хендлер зовёт её сам. Без этой подготовки проба зеленела бы
	// на статусе и о предикате лизы не сказала бы ничего.
	_, err = raw.Exec(`UPDATE design_run SET status = 'pending' WHERE id = ?`, started.Run.Id)
	require.NoError(t, err)

	second, err := rep.Design().ClaimRuns(ctx, 4, time.Minute, uuid.NewString())
	require.NoError(t, err)
	for _, r := range second {
		require.NotEqual(t, started.Run.Id, r.Id, "живая лиза не перехватывается")
	}

	_, token := runStatus(t, raw, started.Run.Id)
	require.True(t, token.Valid)
	require.Equal(t, tokenA, token.String, "токен первого воркера обязан устоять")
}

// ИСТЁКШАЯ ЛИЗА ВОЗВРАЩАЕТСЯ В ОЧЕРЕДЬ. Без этого подметальщика «истёкший захват — та же дорога»
// это дорога без ног: ClaimRuns берёт только pending.
func TestDesignDBExpiredLeaseComesBackToPending(t *testing.T) {
	rep, raw := probeRepository(t)
	resetBudget(t, raw)
	card := probeCard(t, raw)
	started := startProbeRun(t, rep, card, "0.10")
	ctx := context.Background()

	claimed, err := rep.Design().ClaimRuns(ctx, 4, time.Minute, uuid.NewString())
	require.NoError(t, err)
	require.NotEmpty(t, claimed)

	// Положительный контроль: пока лиза жива, подметальщику брать нечего.
	n, err := rep.Design().ReviveExpiredRuns(ctx)
	require.NoError(t, err)
	require.Zero(t, n, "живая лиза не подметается")

	expireClaim(t, raw, started.Run.Id)
	n, err = rep.Design().ReviveExpiredRuns(ctx)
	require.NoError(t, err)
	require.GreaterOrEqual(t, n, 1)

	status, token := runStatus(t, raw, started.Run.Id)
	require.Equal(t, entity.DesignRunPending, status)
	require.False(t, token.Valid, "подметённая строка не принадлежит никому")
}

// ⚠ ГЛАВНОЕ УТВЕРЖДЕНИЕ ВСЕЙ МАШИНЫ: ВОРКЕР С ИСТЁКШИМ ЗАХВАТОМ НЕ ЗАТИРАЕТ ЧУЖОЙ РЕЗУЛЬТАТ.
//
// Сценарий: A забрал задание, умер (лиза истекла), подметальщик вернул строку, B забрал её заново
// и ПРИСЛАЛ РЕЗУЛЬТАТ. Затем оживает A и присылает СВОЙ. Правильный исход — отказ A и НЕТРОНУТАЯ
// выдача B. Именно это здесь и проверяется, целиком через путь Go.
//
// ⚠ ИСПРАВЛЕНИЕ ПРЕЖНЕГО КОММЕНТАРИЯ, КОТОРЫЙ ОБЕЩАЛ НЕ ТО. Здесь стояло: «мутация, которая
// ОБЯЗАНА уронить эту пробу — убрать `AND claim_token = :tok` из закрывающего UPDATE; requireClaim
// этого НЕ ловит». ИЗМЕРЕНО — ЛОЖЬ В ОБЕИХ ПОЛОВИНАХ: мутация выполнена, проба осталась ЗЕЛЁНОЙ,
// потому что requireClaim ловит ровно этот случай. Отказывает опоздавшему ОН, а не WHERE: строка
// читается и пишется в одной SERIALIZABLE-транзакции, войти между ними чужой сессии нечем (это
// измеряет TestDesignDBWriteTxLocksTheRunRowItRead).
//
// ЧТО ЭТА ПРОБА ДОКАЗЫВАЕТ: ГАРАНТИЮ — чужой результат не затирается, опоздавший получает
// claim_lost, а на закрытой строке — состав, из которого видно, что его байты сироты. Каким
// именно сторожем гарантия держится, проба не различает и не обязана. Сторож токена в WHERE
// покрыт отдельно: TestDesignDBCompleteRunClosingUpdateRefusesAForeignToken.
func TestDesignDBExpiredClaimCannotOverwriteAnothersResult(t *testing.T) {
	rep, raw := probeRepository(t)
	resetBudget(t, raw)
	card := probeCard(t, raw)
	mediaB := probeMedia(t, raw)
	mediaA := probeMedia(t, raw)
	started := startProbeRun(t, rep, card, "0.10")
	ctx := context.Background()

	tokenA := uuid.NewString()
	claimedA, err := rep.Design().ClaimRuns(ctx, 4, time.Minute, tokenA)
	require.NoError(t, err)
	require.NotEmpty(t, claimedA)

	expireClaim(t, raw, started.Run.Id)
	_, err = rep.Design().ReviveExpiredRuns(ctx)
	require.NoError(t, err)

	tokenB := uuid.NewString()
	claimedB, err := rep.Design().ClaimRuns(ctx, 4, time.Minute, tokenB)
	require.NoError(t, err)
	require.NotEmpty(t, claimedB)

	// ─── СЛУЧАЙ ПЕРВЫЙ: СТРОКУ ВЕДЁТ B, А РЕЗУЛЬТАТ НЕСЁТ A ───
	//
	// Здесь строка ЖИВАЯ, и записать в неё чужую выдачу было бы прямой порчей. Отказ обязателен.
	_, err = rep.Design().CompleteRun(ctx, entity.DesignRunComplete{
		RunId: started.Run.Id, ClaimToken: tokenA,
		Outputs: []entity.DesignPictureInsert{{MediaId: mediaA, Ordinal: 1}},
	})
	require.ErrorIs(t, err, entity.ErrDesignClaimLost)
	var filed int
	require.NoError(t, raw.QueryRow(`SELECT COUNT(*) FROM design_picture WHERE run_id = ?`, started.Run.Id).
		Scan(&filed))
	require.Zero(t, filed, "ни один кадр опоздавшего не должен был просочиться в чужую строку")

	doneB, err := rep.Design().CompleteRun(ctx, entity.DesignRunComplete{
		RunId: started.Run.Id, ClaimToken: tokenB,
		Outputs: []entity.DesignPictureInsert{{MediaId: mediaB, Ordinal: 0}},
	})
	require.NoError(t, err)
	// СОСТАВ ВОЗВРАЩАЕТСЯ ВЫЗЫВАЮЩЕМУ — без него воркер не может посчитать OrphanedMedia и
	// свежезагруженные, но не усыновлённые байты остались бы в бакете навсегда.
	require.Len(t, doneB.Pictures, 1)
	require.Equal(t, mediaB, doneB.Pictures[0].MediaId)
	require.Empty(t, orphanedAfter([]int{mediaB}, doneB))

	// ПОВТОР B С ДРУГИМИ БАЙТАМИ: строка уже done, кадры первого ответа остаются, а свежий файл
	// объявляется сиротой — ровно тот случай err == nil, ради которого компенсация существует.
	again, err := rep.Design().CompleteRun(ctx, entity.DesignRunComplete{
		RunId: started.Run.Id, ClaimToken: tokenB,
		Outputs: []entity.DesignPictureInsert{{MediaId: mediaA, Ordinal: 7}},
	})
	require.NoError(t, err)
	require.Len(t, again.Pictures, 1, "повтор на закрытой строке кадров не добавляет")
	require.Equal(t, []int{mediaA}, orphanedAfter([]int{mediaA}, again),
		"загруженное повтором не усыновил никто — воркер обязан это увидеть")

	// ─── СЛУЧАЙ ВТОРОЙ: A ОЖИВАЕТ, КОГДА СТРОКА УЖЕ ЗАКРЫТА ───
	//
	// Здесь писать некуда, и ответ — СОСТАВ, а не отказ: A обязан узнать, что его байты не
	// усыновил никто, иначе они останутся в бакете ничьими. ОРДИНАЛ У НЕГО ДРУГОЙ намеренно: с
	// ординалом 0 вставку остановил бы ещё и uq_design_picture_run_ordinal, и «выдача не
	// изменилась» оказалось бы истинным по ЧУЖОЙ причине — то есть проба зеленела бы и без
	// сторожа.
	late, err := rep.Design().CompleteRun(ctx, entity.DesignRunComplete{
		RunId: started.Run.Id, ClaimToken: tokenA,
		Outputs: []entity.DesignPictureInsert{{MediaId: mediaA, Ordinal: 1}},
	})
	require.NoError(t, err)
	require.Len(t, late.Pictures, 1, "опоздавший ничего не дописывает в закрытую строку")
	require.Equal(t, []int{mediaA}, orphanedAfter([]int{mediaA}, late),
		"опоздавший обязан УЗНАТЬ, что его байты — сироты, а не остаться с ними наедине")

	var media int
	var count int
	require.NoError(t, raw.QueryRow(
		`SELECT COUNT(*), COALESCE(MIN(media_id), 0) FROM design_picture WHERE run_id = ?`,
		started.Run.Id).Scan(&count, &media))
	require.Equal(t, 1, count, "выдача перехватившего обязана остаться единственной")
	require.Equal(t, mediaB, media, "в строке лежит кадр B, а не подсунутый A")
}

// ─────────────────────── прилёт результата ───────────────────────

// MIXED_INPUT СЧИТАЕТСЯ В МОМЕНТ РОЖДЕНИЯ КАДРА, а не при минте.
//
// Здесь у прогона два входа разного провенанса: кадр полосы (ai) и файл человека, которого среди
// картинок полосы нет вовсе. Контроль ниже — прогон с одним провенансом.
func TestDesignDBCompleteRunComputesMixedInputAtBirth(t *testing.T) {
	rep, raw := probeRepository(t)
	resetBudget(t, raw)
	card := probeCard(t, raw)
	aiMedia := probeMedia(t, raw)
	humanMedia := probeMedia(t, raw)
	outMixed := probeMedia(t, raw)
	outPlain := probeMedia(t, raw)
	ctx := context.Background()

	// Кадр полосы с провенансом ai — вход первого рода.
	_, err := raw.Exec(`INSERT INTO design_picture (tech_card_id, media_id, ordinal, kind, source_class)
		VALUES (?, ?, 0, 'flat', 'ai')`, card, aiMedia)
	require.NoError(t, err)

	mixedRun, err := rep.Design().StartRun(ctx, entity.DesignRunStart{
		TechCardId: card, ClientRequestId: uuid.NewString(), Kind: entity.DesignRunKindFlat,
		RequestedOutputs: 1, Author: "probe",
		PriceEstimate: decimal.NullDecimal{Decimal: decimal.RequireFromString("0.10"), Valid: true},
		Inputs: []byte(fmt.Sprintf(
			`{"slots":[{"media_id":%d}],"refs":[{"media_id":%d}]}`, aiMedia, humanMedia)),
	})
	require.NoError(t, err)
	token := uuid.NewString()
	_, err = rep.Design().ClaimRuns(ctx, 8, time.Minute, token)
	require.NoError(t, err)
	_, err = rep.Design().CompleteRun(ctx, entity.DesignRunComplete{
		RunId: mixedRun.Run.Id, ClaimToken: token,
		Outputs: []entity.DesignPictureInsert{{MediaId: outMixed, Ordinal: 0}},
	})
	require.NoError(t, err)

	var mixed bool
	require.NoError(t, raw.QueryRow(`SELECT mixed_input FROM design_picture WHERE media_id = ?`, outMixed).
		Scan(&mixed))
	require.True(t, mixed, "кадр из ИИ-плиты и человеческого референса — смесь провенансов")

	// КОНТРОЛЬ: один провенанс — не смесь. Без него проба выше зеленела бы на флаге, поднятом
	// всегда.
	plainRun, err := rep.Design().StartRun(ctx, entity.DesignRunStart{
		TechCardId: card, ClientRequestId: uuid.NewString(), Kind: entity.DesignRunKindFlat,
		RequestedOutputs: 1, Author: "probe",
		PriceEstimate: decimal.NullDecimal{Decimal: decimal.RequireFromString("0.10"), Valid: true},
		Inputs:        []byte(fmt.Sprintf(`{"refs":[{"media_id":%d}]}`, humanMedia)),
	})
	require.NoError(t, err)
	token2 := uuid.NewString()
	_, err = rep.Design().ClaimRuns(ctx, 8, time.Minute, token2)
	require.NoError(t, err)
	_, err = rep.Design().CompleteRun(ctx, entity.DesignRunComplete{
		RunId: plainRun.Run.Id, ClaimToken: token2,
		Outputs: []entity.DesignPictureInsert{{MediaId: outPlain, Ordinal: 0}},
	})
	require.NoError(t, err)
	require.NoError(t, raw.QueryRow(`SELECT mixed_input FROM design_picture WHERE media_id = ?`, outPlain).
		Scan(&mixed))
	require.False(t, mixed, "один провенанс смесью не является")
}

// COMPOSITE_VIEWS ПИШЕТСЯ ПРИ ПРИЛЁТЕ, И ПРАВИЛО «КОМПОЗИТ В СЛОТ НЕ ВСТАЁТ» СРАЗУ ОЖИВАЕТ.
//
// Это две половины одного факта: колонка без писателя делала isComposite() на клиенте ВСЕГДА
// ложным, а сторож верстака — недостижимым.
func TestDesignDBCompositeArrivesMarkedAndTheBenchRefusesIt(t *testing.T) {
	rep, raw := probeRepository(t)
	resetBudget(t, raw)
	card := probeCard(t, raw)
	sheet := probeMedia(t, raw)
	ctx := context.Background()

	started, err := rep.Design().StartRun(ctx, entity.DesignRunStart{
		TechCardId: card, ClientRequestId: uuid.NewString(), Kind: entity.DesignRunKindFlat,
		RequestedOutputs: 1, Author: "probe",
		PriceEstimate: decimal.NullDecimal{Decimal: decimal.RequireFromString("0.10"), Valid: true},
		Params:        []byte(`{"views":["front","back","side_l"],"layout":"one"}`),
	})
	require.NoError(t, err)
	token := uuid.NewString()
	_, err = rep.Design().ClaimRuns(ctx, 8, time.Minute, token)
	require.NoError(t, err)
	_, err = rep.Design().CompleteRun(ctx, entity.DesignRunComplete{
		RunId: started.Run.Id, ClaimToken: token,
		Outputs: []entity.DesignPictureInsert{{MediaId: sheet, Ordinal: 0}},
	})
	require.NoError(t, err)

	var (
		pictureID int
		composite sql.NullString
		ghost     sql.NullString
	)
	require.NoError(t, raw.QueryRow(
		`SELECT id, composite_views, ghost_view FROM design_picture WHERE media_id = ?`, sheet).
		Scan(&pictureID, &composite, &ghost))
	require.True(t, composite.Valid, "лист из трёх видов обязан приехать помеченным")
	require.JSONEq(t, `["front","back","side_l"]`, composite.String)
	require.False(t, ghost.Valid, "у композита нет ОДНОГО вида, и подставленный первый соврал бы резаку")

	_, err = rep.Design().SetBenchSlot(ctx, entity.DesignBenchSlotSet{
		TechCardId: card,
		Slot:       entity.DesignSlotRef{ViewKey: entity.DesignViewFront},
		PictureId:  pictureID,
		Actor:      "probe",
	})
	require.ErrorIs(t, err, entity.ErrDesignCompositePlate,
		"композит сначала режут — и теперь это правило достижимо")
}

// ─────────────────────── род кадра и вторая ось верстака ───────────────────────

// РОД БЕРЁТСЯ СО ВХОДА. До этой волны он был захардкожен в flat, и рендер нельзя было завести
// руками вовсе.
func TestDesignDBRegisterBatchFilesTheKindFromTheInput(t *testing.T) {
	rep, raw := probeRepository(t)
	card := probeCard(t, raw)
	renderMedia := probeMedia(t, raw)
	plainMedia := probeMedia(t, raw)

	out, err := rep.Design().RegisterBatch(context.Background(), entity.DesignBatchRegister{
		TechCardId:      card,
		ClientRequestId: uuid.NewString(),
		Actor:           "probe",
		Items: []entity.DesignUploadItem{
			{MediaId: renderMedia, Kind: entity.DesignPictureKindRender},
			{MediaId: plainMedia},
		},
	})
	require.NoError(t, err)
	require.Len(t, out.Pictures, 2)
	require.Equal(t, entity.DesignPictureKindRender, out.Pictures[0].Kind)
	require.Equal(t, entity.DesignPictureKindFlat, out.Pictures[1].Kind,
		"неназванный род читается как flat — старый вызывающий перемены не заметил")
}

// ВЕРСТАК ДЕРЖИТ ОБЕ ОСИ ОДНОГО ВИДА, и флэт-слот рендер не принимает.
func TestDesignDBBenchHoldsBothAxesOfOneView(t *testing.T) {
	rep, raw := probeRepository(t)
	card := probeCard(t, raw)
	flatMedia := probeMedia(t, raw)
	renderMedia := probeMedia(t, raw)
	ctx := context.Background()

	batch, err := rep.Design().RegisterBatch(ctx, entity.DesignBatchRegister{
		TechCardId: card, ClientRequestId: uuid.NewString(), Actor: "probe",
		Items: []entity.DesignUploadItem{
			{MediaId: flatMedia},
			{MediaId: renderMedia, Kind: entity.DesignPictureKindRender},
		},
	})
	require.NoError(t, err)
	flatPic, renderPic := batch.Pictures[0].Id, batch.Pictures[1].Id

	flatSlot, err := rep.Design().SetBenchSlot(ctx, entity.DesignBenchSlotSet{
		TechCardId: card, Slot: entity.DesignSlotRef{ViewKey: entity.DesignViewFront},
		PictureId: flatPic, Actor: "probe",
	})
	require.NoError(t, err)

	renderSlot, err := rep.Design().SetBenchSlot(ctx, entity.DesignBenchSlotSet{
		TechCardId: card,
		Slot: entity.DesignSlotRef{
			ViewKey: entity.DesignViewFront, Kind: entity.DesignPictureKindRender,
		},
		PictureId: renderPic, Actor: "probe",
	})
	require.NoError(t, err)
	require.NotEqual(t, flatSlot.Id, renderSlot.Id,
		"рендер фронта и флэт фронта — два адреса, а не один вытесняющий другой")
	require.Equal(t, entity.DesignPictureKindFlat, flatSlot.Kind)
	require.Equal(t, entity.DesignPictureKindRender, renderSlot.Kind)

	// ФЛЭТ-СЛОТ РЕНДЕР НЕ ПРИНИМАЕТ — иначе минт печатает рендер на техническом листе.
	_, err = rep.Design().SetBenchSlot(ctx, entity.DesignBenchSlotSet{
		TechCardId: card, Slot: entity.DesignSlotRef{ViewKey: entity.DesignViewBack},
		PictureId: renderPic, Actor: "probe",
	})
	require.ErrorIs(t, err, entity.ErrDesignWrongKind)

	band, err := rep.Design().GetBand(ctx, card, 4)
	require.NoError(t, err)
	require.True(t, band.HasFabricRender, "неспрятанный рендер обязан открывать 3D (W-13)")
}

// ─────────────────────── провал и деньги попыток ───────────────────────

// РЕТРАЙ ВОЗВРАЩАЕТ В ОЧЕРЕДЬ И НЕ ТРОГАЕТ РЕЗЕРВ; ТЕРМИНАЛЬНЫЙ ПРОВАЛ РЕЗЕРВ ОСВОБОЖДАЕТ.
func TestDesignDBFailRunRetriesThenReleasesTheReserve(t *testing.T) {
	rep, raw := probeRepository(t)
	resetBudget(t, raw)
	card := probeCard(t, raw)
	started := startProbeRun(t, rep, card, "0.40")
	ctx := context.Background()

	token := uuid.NewString()
	_, err := rep.Design().ClaimRuns(ctx, 8, time.Minute, token)
	require.NoError(t, err)

	run, err := rep.Design().FailRun(ctx, entity.DesignRunFail{
		RunId: started.Run.Id, ClaimToken: token, ErrorCode: "provider_429",
		LastError: "too many requests", Retryable: true,
	})
	require.NoError(t, err)
	require.Equal(t, entity.DesignRunPending, run.Status)
	require.Equal(t, 1, run.AttemptCount)
	require.False(t, run.ClaimToken.Valid)
	require.True(t, run.NextAttemptAt.Valid)

	budget, err := rep.Design().GetBudget(ctx)
	require.NoError(t, err)
	require.True(t, budget.Reserved.Equal(decimal.RequireFromString("0.4")),
		"ретрай НЕ снимает резерв: задание всё ещё в полёте, %s", budget.Reserved)

	// Терминальный провал: сначала снова забираем строку — она вернулась в очередь.
	_, err = raw.Exec(`UPDATE design_run SET next_attempt_at = UTC_TIMESTAMP(6) WHERE id = ?`, started.Run.Id)
	require.NoError(t, err)
	token2 := uuid.NewString()
	_, err = rep.Design().ClaimRuns(ctx, 8, time.Minute, token2)
	require.NoError(t, err)
	run, err = rep.Design().FailRun(ctx, entity.DesignRunFail{
		RunId: started.Run.Id, ClaimToken: token2, ErrorCode: "provider_result_unknown",
		LastError: "no answer", Retryable: false,
	})
	require.NoError(t, err)
	require.Equal(t, entity.DesignRunFailed, run.Status)

	budget, err = rep.Design().GetBudget(ctx)
	require.NoError(t, err)
	require.True(t, budget.Reserved.IsZero(),
		"терминальный переход обязан вернуть дню зарезервированное: %s", budget.Reserved)
}

// ДЕНЬГИ ПОПЫТКИ СЧИТАЮТСЯ РОВНО ОДИН РАЗ, даже если ответ потерялся и воркер повторил.
func TestDesignDBAttemptMoneyIsCountedOnce(t *testing.T) {
	rep, raw := probeRepository(t)
	resetBudget(t, raw)
	card := probeCard(t, raw)
	started := startProbeRun(t, rep, card, "0.40")
	ctx := context.Background()

	token := uuid.NewString()
	_, err := rep.Design().ClaimRuns(ctx, 8, time.Minute, token)
	require.NoError(t, err)

	attempt, err := rep.Design().StartAttempt(ctx, entity.DesignAttemptStart{
		RunId: started.Run.Id, ClaimToken: token, Provider: "openrouter",
	})
	require.NoError(t, err)
	require.Equal(t, 1, attempt.AttemptNo)
	require.Equal(t, entity.DesignAttemptDispatching, attempt.State)

	finish := entity.DesignAttemptFinish{
		RunId: started.Run.Id, AttemptNo: attempt.AttemptNo, State: entity.DesignAttemptDelivered,
		ProviderRequestId: "req-1",
		Price:             decimal.NullDecimal{Decimal: decimal.RequireFromString("0.30"), Valid: true},
	}
	require.NoError(t, rep.Design().FinishAttempt(ctx, finish))
	budget, err := rep.Design().GetBudget(ctx)
	require.NoError(t, err)
	require.True(t, budget.Spent.Equal(decimal.RequireFromString("0.3")), "spent = %s", budget.Spent)

	// ПОВТОР ПОСЛЕ ПОТЕРЯННОГО ОТВЕТА — деньги не двигаются.
	require.NoError(t, rep.Design().FinishAttempt(ctx, finish))
	budget, err = rep.Design().GetBudget(ctx)
	require.NoError(t, err)
	require.True(t, budget.Spent.Equal(decimal.RequireFromString("0.3")),
		"повтор удвоил бы траты владельца: %s", budget.Spent)

	run, err := rep.Design().GetRun(ctx, started.Run.Id)
	require.NoError(t, err)
	require.True(t, run.PriceActual.Valid)
	require.True(t, run.PriceActual.Decimal.Equal(decimal.RequireFromString("0.3")),
		"цена прогона — СУММА цен попыток: %s", run.PriceActual.Decimal)
}

// ОТМЕНА ЖДУЩЕГО ПРОГОНА ВОЗВРАЩАЕТ ДЕНЬГИ ДНЮ, отмена идущего — только просит.
func TestDesignDBCancelReleasesAPendingReserveAndOnlyAsksARunningOne(t *testing.T) {
	rep, raw := probeRepository(t)
	resetBudget(t, raw)
	card := probeCard(t, raw)
	ctx := context.Background()

	pending := startProbeRun(t, rep, card, "0.25")
	run, err := rep.Design().CancelRun(ctx, pending.Run.Id, "probe")
	require.NoError(t, err)
	require.Equal(t, entity.DesignRunCancelled, run.Status)
	budget, err := rep.Design().GetBudget(ctx)
	require.NoError(t, err)
	require.True(t, budget.Reserved.IsZero(), "ждущий прогон никто не оплачивал: %s", budget.Reserved)

	running := startProbeRun(t, rep, card, "0.25")
	_, err = rep.Design().ClaimRuns(ctx, 8, time.Minute, uuid.NewString())
	require.NoError(t, err)
	run, err = rep.Design().CancelRun(ctx, running.Run.Id, "probe")
	require.NoError(t, err)
	require.Equal(t, entity.DesignRunRunning, run.Status,
		"идущий прогон закрывает воркер: результат мог прийти секундой позже, и выбрасывать оплаченное нельзя")
	require.True(t, run.CancelRequestedAt.Valid)
	budget, err = rep.Design().GetBudget(ctx)
	require.NoError(t, err)
	require.True(t, budget.Reserved.Equal(decimal.RequireFromString("0.25")),
		"резерв идущего прогона держится до его терминального перехода: %s", budget.Reserved)
}

// ─────────────────────── W-3: СЛОВА ЧЕЛОВЕКА ХРАНЯТСЯ ───────────────────────

// ЗАПИСКА РЕФЕРЕНСА ПИШЕТСЯ ТЕМ ЖЕ АПСЕРТОМ, ЧТО И РОЛЬ, И ЧИТАЕТСЯ ОБРАТНО.
//
// ⚠ ЭТО ПРОБА КОЛОНКИ, А НЕ СТРУКТУРЫ, и без базы её сделать нельзя: хендл стора — d.Unsafe(),
// поэтому колонка, которую сущность не несёт, ЧИТАЕТСЯ В НИЧТО без единой ошибки. Ровно так все
// три поля этой волны и терялись между базой и проводом.
//
// АСИММЕТРИЯ ПРОВЕРЯЕТСЯ ОБОИМИ КОНЦАМИ: пустая записка на строке, которая сохраняет роль,
// ОЧИЩАЕТ записку; пустая роль удаляет строку целиком и уносит записку с собой.
func TestDesignDBReferenceNoteRidesWithTheRole(t *testing.T) {
	rep, raw := probeRepository(t)
	card := probeCard(t, raw)
	mediaID := probeMedia(t, raw)
	ctx := context.Background()
	_, err := raw.Exec(`INSERT INTO tech_card_media (tech_card_id, media_id, category, display_order)
		VALUES (?, ?, 'moodboard', 0)`, card, mediaID)
	require.NoError(t, err)

	ref, err := rep.Design().SetReferenceRole(ctx, entity.DesignReferenceRole{
		TechCardId: card, MediaId: mediaID, Role: entity.DesignViewFront,
		Note: "only the collar", Ordinal: 1, Actor: "probe",
	})
	require.NoError(t, err)
	require.NotNil(t, ref)
	require.Equal(t, "only the collar", ref.Note.String,
		"записка обязана вернуться той же транзакцией, которая её записала")

	// И тем же значением её видит ЧИТАТЕЛЬ ПОЛОСЫ — тот самый список, из которого собирается
	// снимок входов прогона.
	band, err := rep.Design().GetBand(ctx, card, 1)
	require.NoError(t, err)
	require.Len(t, band.References, 1)
	require.Equal(t, "only the collar", band.References[0].Note.String)

	// Пустая записка при сохранённой роли — ОЧИСТИТЬ.
	ref, err = rep.Design().SetReferenceRole(ctx, entity.DesignReferenceRole{
		TechCardId: card, MediaId: mediaID, Role: entity.DesignViewFront, Ordinal: 1, Actor: "probe",
	})
	require.NoError(t, err)
	require.NotNil(t, ref)
	require.False(t, ref.Note.Valid, "пустой текст для записки — настоящий ответ, а не молчание")
	require.Equal(t, entity.DesignViewFront, ref.Role, "роль при этом на месте")

	// Пустая роль — строки нет вовсе.
	ref, err = rep.Design().SetReferenceRole(ctx, entity.DesignReferenceRole{
		TechCardId: card, MediaId: mediaID, Note: "ignored", Actor: "probe",
	})
	require.NoError(t, err)
	require.Nil(t, ref)
}

// ОПИСАНИЕ ИЗДЕЛИЯ ПРОХОДИТ КОЛОНКУ ТУДА И ОБРАТНО, А ОТСУТСТВИЕ ПОЛЯ ЕГО СОХРАНЯЕТ.
//
// Третья нога verbatim-протокола живёт в SQL (IF(:garment_description_omitted, …)), и проверить её
// можно только выполнив этот SQL: сущность про неё ничего не знает, а sqlx свяжет параметр молча
// в любом случае.
func TestDesignDBGarmentDescriptionSurvivesASaveThatDoesNotMentionIt(t *testing.T) {
	rep, raw := probeRepository(t)
	card := probeCard(t, raw)
	ctx := context.Background()

	load := func() *entity.TechCard {
		t.Helper()
		tc, err := rep.TechCards().GetTechCardById(ctx, card)
		require.NoError(t, err)
		return tc
	}

	// ─── РОЖДЕНИЕ КАРТОЧКИ ───
	//
	// ⚠ ОТДЕЛЬНАЯ НОГА, ПОТОМУ ЧТО ЭТО ДРУГОЙ SQL. Список колонок шапки (techCardHeaderColumns)
	// читает ТОЛЬКО INSERT — его же берут клон и импорт, — а UPDATE ниже перечисляет колонки сам.
	// Проба, проверяющая один из них, зеленеет, когда второй теряет колонку: измерено мутацией,
	// которая выбросила garment_description из списка вставки и НЕ покраснела, пока этой ноги
	// здесь не было.
	born := &entity.TechCardInsert{
		Name:               "design band probe: a card born with a description",
		Stage:              entity.TechCardStageIdea,
		MeasurementUnit:    entity.TechCardUnitMm,
		ApprovalState:      entity.TechCardApprovalDraft,
		TargetGender:       sql.NullString{String: "unisex", Valid: true},
		GarmentDescription: sql.NullString{String: "born describing itself", Valid: true},
	}
	newID, err := rep.TechCards().AddTechCard(ctx, born)
	require.NoError(t, err)
	t.Cleanup(func() { _, _ = raw.Exec(`DELETE FROM tech_card WHERE id = ?`, newID) })
	fresh, err := rep.TechCards().GetTechCardById(ctx, newID)
	require.NoError(t, err)
	require.Equal(t, "born describing itself", fresh.GarmentDescription.String,
		"без колонки в INSERT описание теряется на СОЗДАНИИ карточки — и вместе с ним на клоне и импорте")

	tc := load()
	payload := tc.TechCardInsert
	payload.GarmentDescription = sql.NullString{String: "oversized boxy shirt", Valid: true}
	require.NoError(t, rep.TechCards().UpdateTechCard(ctx, card, &payload, tc.LockVersion))
	require.Equal(t, "oversized boxy shirt", load().GarmentDescription.String,
		"без колонки в INSERT/UPDATE описание уехало бы в никуда, и промпт остался бы без изделия")

	// Сейв из вкладки, которая про полосу DESIGN не знает: поля НЕТ на проводе.
	tc = load()
	silent := tc.TechCardInsert
	silent.GarmentDescription = sql.NullString{}
	silent.GarmentDescriptionOmitted = true
	require.NoError(t, rep.TechCards().UpdateTechCard(ctx, card, &silent, tc.LockVersion))
	require.Equal(t, "oversized boxy shirt", load().GarmentDescription.String,
		"отсутствующее поле обязано СОХРАНИТЬ колонку: иначе описание стирает вкладка, которая его не видела")

	// Явная пустая строка — ОЧИСТИТЬ.
	tc = load()
	clear := tc.TechCardInsert
	clear.GarmentDescription = sql.NullString{}
	clear.GarmentDescriptionOmitted = false
	require.NoError(t, rep.TechCards().UpdateTechCard(ctx, card, &clear, tc.LockVersion))
	require.False(t, load().GarmentDescription.Valid)
}

// ─────────────────────── W-12: ОТМЕТКА «ВЫБРАН» ───────────────────────

// ОТМЕТКА ХРАНИТСЯ, СНИМАЕТСЯ И НЕ ЯВЛЯЕТСЯ ОБРАТНОЙ СТОРОНОЙ hidden.
//
// Складывать их было бы жестом, который молча отменяет другой: спрятать — убрать с глаз, выбрать —
// поднять над остальными. Проба ставит эксперимент, в котором обе отметки стоят ОДНОВРЕМЕННО, —
// на сложенной паре это состояние невыразимо.
func TestDesignDBPictureSelectionIsIndependentOfHiding(t *testing.T) {
	rep, raw := probeRepository(t)
	card := probeCard(t, raw)
	mediaA, mediaB := probeMedia(t, raw), probeMedia(t, raw)
	ctx := context.Background()

	reg, err := rep.Design().RegisterBatch(ctx, entity.DesignBatchRegister{
		TechCardId: card, ClientRequestId: uuid.NewString(), Actor: "probe",
		Items: []entity.DesignUploadItem{
			{MediaId: mediaA, Kind: entity.DesignPictureKindThreed},
			{MediaId: mediaB, Kind: entity.DesignPictureKindThreed},
		},
	})
	require.NoError(t, err)
	require.Len(t, reg.Pictures, 2)
	first, second := reg.Pictures[0].Id, reg.Pictures[1].Id

	pic, err := rep.Design().SetPictureSelected(ctx, first, true, "probe")
	require.NoError(t, err)
	require.True(t, pic.Selected)

	// НИЧЕГО НЕ ИСКЛЮЧИТЕЛЬНО: владелец говорит во множественном числе.
	pic, err = rep.Design().SetPictureSelected(ctx, second, true, "probe")
	require.NoError(t, err)
	require.True(t, pic.Selected)
	again, err := rep.Design().GetPicture(ctx, first)
	require.NoError(t, err)
	require.True(t, again.Selected, "вторая отметка не смеет снимать первую")

	// Выбранный кадр можно спрятать, и обе отметки живут рядом.
	hidden, err := rep.Design().HidePicture(ctx, first, true, "probe")
	require.NoError(t, err)
	require.True(t, hidden.HiddenAt.Valid)
	require.True(t, hidden.Selected, "спрятать — не то же, что снять выбор")

	off, err := rep.Design().SetPictureSelected(ctx, first, false, "probe")
	require.NoError(t, err)
	require.False(t, off.Selected)

	_, err = rep.Design().SetPictureSelected(ctx, 0, true, "probe")
	require.ErrorIs(t, err, entity.ErrDesignNotFound,
		"неизвестный кадр — отказ, а не тихий UPDATE на ноль строк")
}

// ─────────────────────── ИМПОРТ ВЕКТОРА ───────────────────────

// ФАЙЛ ПОДШИВАЕТСЯ ОДИН РАЗ, С ПРОВЕНАНСОМ, И ПОВТОР НЕ РОЖДАЕТ ВТОРОГО СЛОЯ.
//
// ⚠ ИДЕМПОТЕНТНОСТЬ ЗДЕСЬ ПО (карточка, файл), А НЕ ПО client_request_id: у design_edit_layer
// (0343) колонки под запросный ключ нет, а 0350 её не добавляла. Повтор после потерянного ответа
// приезжает с ТЕМ ЖЕ media_id, потому что файл загружен раньше вызова, — значит покрыт ровно тот
// случай, ради которого идемпотентность и нужна.
func TestDesignDBImportVectorFilesTheFileOnceWithItsProvenance(t *testing.T) {
	rep, raw := probeRepository(t)
	card := probeCard(t, raw)
	svg, base := probeMedia(t, raw), probeMedia(t, raw)
	ctx := context.Background()

	reg, err := rep.Design().RegisterBatch(ctx, entity.DesignBatchRegister{
		TechCardId: card, ClientRequestId: uuid.NewString(), Actor: "probe",
		Items: []entity.DesignUploadItem{{MediaId: base, Kind: entity.DesignPictureKindFlat}},
	})
	require.NoError(t, err)
	source := reg.Pictures[0].Id

	req := entity.DesignVectorImport{
		TechCardId: card, ClientRequestId: uuid.NewString(),
		SourceMediaId: svg, SourcePictureId: source,
		Origin: entity.DesignLayerOriginVectorised, BaseMediaId: base,
		Strokes: []byte(`[{"d":"M0 0 L1 1"}]`), Actor: "probe",
	}
	layer, err := rep.Design().ImportVector(ctx, req)
	require.NoError(t, err)
	require.Equal(t, entity.DesignLayerOriginVectorised, layer.Origin)
	require.Equal(t, int32(svg), layer.SourceMediaId.Int32,
		"ребро «слой ↔ авторитетный файл» и есть предмет глагола")
	require.Equal(t, int32(source), layer.SourcePictureId.Int32)
	require.Equal(t, 1, layer.Rev)

	// ПОВТОР ВОЗВРАЩАЕТ ТУ ЖЕ СТРОКУ, а не вторую. Ключ запроса нарочно ДРУГОЙ: идемпотентность
	// стоит на файле, и проба обязана это показать, а не совпасть случайно.
	req.ClientRequestId = uuid.NewString()
	repeat, err := rep.Design().ImportVector(ctx, req)
	require.NoError(t, err)
	require.Equal(t, layer.Id, repeat.Id)
	var layers int
	require.NoError(t, raw.QueryRow(
		`SELECT COUNT(*) FROM design_edit_layer WHERE tech_card_id = ?`, card).Scan(&layers))
	require.Equal(t, 1, layers, "второй слой на тот же файл — это оплаченный дважды импорт в истории")

	// Слой виден полосе ВМЕСТЕ с провенансом: без него смешанность вектора невычислима.
	band, err := rep.Design().GetBand(ctx, card, 1)
	require.NoError(t, err)
	require.Len(t, band.Layers, 1)
	require.Equal(t, entity.DesignLayerOriginVectorised, band.Layers[0].Origin)
	require.Equal(t, int32(svg), band.Layers[0].SourceMediaId.Int32)
}

// ИМПОРТ ОТКАЗЫВАЕТСЯ ОТ drawn, ОТ НЕСУЩЕСТВУЮЩЕГО ФАЙЛА И ОТ ЧУЖОГО РАСТРА.
//
// Последнее схема выразить не может вовсе: design_picture(id) — законная цель, чьей бы карточки
// строка ни была, и родословная, указывающая на чужой флэт, это ложь, которую полоса потом
// нарисует.
func TestDesignDBImportVectorRefusesNonsense(t *testing.T) {
	rep, raw := probeRepository(t)
	card := probeCard(t, raw)
	other := probeCard(t, raw)
	svg, foreign := probeMedia(t, raw), probeMedia(t, raw)
	ctx := context.Background()

	reg, err := rep.Design().RegisterBatch(ctx, entity.DesignBatchRegister{
		TechCardId: other, ClientRequestId: uuid.NewString(), Actor: "probe",
		Items: []entity.DesignUploadItem{{MediaId: foreign, Kind: entity.DesignPictureKindFlat}},
	})
	require.NoError(t, err)
	foreignPicture := reg.Pictures[0].Id

	ok := entity.DesignVectorImport{
		TechCardId: card, ClientRequestId: uuid.NewString(), SourceMediaId: svg,
		Origin: entity.DesignLayerOriginImported, Actor: "probe",
	}

	drawn := ok
	drawn.Origin = entity.DesignLayerOriginDrawn
	_, err = rep.Design().ImportVector(ctx, drawn)
	require.ErrorIs(t, err, entity.ErrDesignInvalidArgument,
		"слой, нарисованный из пустоты, импортировать нечем: файла у него нет вовсе")

	missing := ok
	missing.SourceMediaId = 999999999
	_, err = rep.Design().ImportVector(ctx, missing)
	require.ErrorIs(t, err, entity.ErrDesignInvalidArgument)

	stranger := ok
	stranger.SourcePictureId = foreignPicture
	_, err = rep.Design().ImportVector(ctx, stranger)
	require.ErrorIs(t, err, entity.ErrDesignNotFound)

	// Положительный контроль: тот же запрос без чужого растра проходит. Без него три отказа выше
	// доказывали бы только то, что импорт не работает никогда.
	layer, err := rep.Design().ImportVector(ctx, ok)
	require.NoError(t, err)
	require.Equal(t, entity.DesignLayerOriginImported, layer.Origin)
}

// ═══════════════════════════════════════════════════════════════════════════════════════════════
// ТОКЕН В WHERE: ЧТО ЕГО СТЕРЕЖЁТ И ЧЕГО НИКАКАЯ ПРОБА ЗДЕСЬ НЕ ДОКАЗЫВАЕТ
// ═══════════════════════════════════════════════════════════════════════════════════════════════
//
// Комментарии над CompleteRun, FailRun и ClaimRuns обещают, что `claim_token` в WHERE закрывающего
// UPDATE (и повтор предиката захвата в UPDATE ClaimRuns) стоят там ПРОТИВ ГОНКИ: «между чтением и
// записью строку может перехватить другой воркер». Из этого обещания следует проба: вклиниться в
// окно между `runByID` и записью и увидеть отказ. ЭТА ПРОБА НЕВОЗМОЖНА, и первый тест ниже
// ИЗМЕРЯЕТ причину, а не рассуждает о ней: пишущая транзакция стора идёт SERIALIZABLE
// (internal/store/db.go:63), InnoDB на этом уровне превращает обычный SELECT в чтение ПОД
// БЛОКИРОВКОЙ, и строка заперта с первого же чтения до коммита. Чужая сессия, желающая
// ротировать токен, ЖДЁТ снаружи; войти в окно ей нечем.
//
// СЛЕДСТВИЕ, КОТОРОЕ НАДО НАЗВАТЬ ВСЛУХ: пока изоляция такая, оба сторожа НЕДОСТИЖИМЫ ЧЕРЕЗ Go —
// requireClaim успевает отказать раньше, чем дело доходит до WHERE. Ревью это и намерило: обе
// мутации («убрать AND claim_token = :tok», «убрать повтор предиката») не роняют ни одну
// поведенческую пробу, и это не слабость проб, а свойство кода. Сторожа при этом НЕ лишние: они
// страховка на день, когда Tx понизят до REPEATABLE READ — там обычное чтение снимка вернёт
// СТАРЫЙ токен, requireClaim согласится, а запись пойдёт по свежей строке. В тот день
// TestDesignDBWriteTxLocksTheRunRowItRead краснеет и говорит, что страховка стала работой.
//
// ЧТО ТОГДА ДЕЛАЮТ ТРИ ПРОБЫ НИЖЕ. Они берут ТЕКСТ ПРОДАКШЕН-ОПЕРАТОРА из исходника пакета
// (разбором AST, не грепом: конкатенация с designRunClaimableSQL собирается так же, как её
// собирает компилятор) и ИСПОЛНЯЮТ ЕГО на живой строке с чужим токеном. Это осознанный размен, и
// он назван прямо:
//
//   ✓ ДОКАЗЫВАЮТ: оператор, который сегодня стоит в queue.go, на настоящем MySQL действительно
//     отказывает — затрагивает НОЛЬ строк при чужом токене и при живой чужой лизе. Убери сторож
//     из продакшен-кода — краснеет именно эта проба, поимённо, потому что текст она берёт оттуда,
//     а не держит свою копию (копия молча разошлась бы и стерегла бы саму себя).
//   ✗ НЕ ДОКАЗЫВАЮТ: что путь Go способен привести строку в это состояние. Он не способен — см.
//     выше. Параметры пробы связывает сама, состояние строки готовит прямым UPDATE.
//
// Проба на уровне SQL со СВОЕЙ копией оператора была бы дешевле и не стоила бы ничего: мутация в
// queue.go оставила бы её зелёной.

// ─────────────────────── окно между чтением и записью ───────────────────────

// ЧУЖАЯ СЕССИЯ НЕ МОЖЕТ ВОЙТИ В ОКНО МЕЖДУ ЧТЕНИЕМ СТРОКИ И ЗАПИСЬЮ В НЕЁ.
//
// Здесь измеряется РОВНО ОДНО: обычное чтение design_run внутри пишущей транзакции стора запирает
// строку от чужой ротации токена. Пока это так, «read, check, write» в CompleteRun / FailRun /
// ClaimRuns честен без токена в WHERE, а требовать от проб покраснения на его удалении —
// требовать доказать недостижимое.
//
// ЧТО СЛОМАЕТСЯ, ЕСЛИ ГАРАНТИЯ ИСЧЕЗНЕТ (Tx понизили до REPEATABLE READ или READ COMMITTED):
// requireClaim начнёт сверять токен со снимком, а UPDATE пойдёт по свежей строке — и с этой
// секунды сторожа в WHERE станут единственным, что отделяет опоздавшего воркера от затирания
// чужого оплаченного результата. Эта проба покраснеет ПЕРВОЙ и скажет, что размен изменился.
func TestDesignDBWriteTxLocksTheRunRowItRead(t *testing.T) {
	rep, raw := probeRepository(t)
	resetBudget(t, raw)
	card := probeCard(t, raw)
	started := startProbeRun(t, rep, card, "0.10")

	read := make(chan struct{})
	release := make(chan struct{})
	closed := make(chan error, 1)

	go func() {
		closed <- rep.Tx(context.Background(), func(ctx context.Context, tx dependency.Repository) error {
			var token sql.NullString
			if err := tx.DB().GetContext(ctx, &token,
				`SELECT claim_token FROM design_run WHERE id = ?`, started.Run.Id); err != nil {
				return err
			}
			close(read)
			<-release
			return nil
		})
	}()
	<-read

	blocked, cancel := context.WithTimeout(context.Background(), 1500*time.Millisecond)
	defer cancel()
	_, err := raw.ExecContext(blocked,
		`UPDATE design_run SET claim_token = ? WHERE id = ?`, uuid.NewString(), started.Run.Id)
	require.Error(t, err,
		"пока читающая запись открыта, чужая ротация токена обязана ЖДАТЬ, а не проходить")

	close(release)
	require.NoError(t, <-closed)

	// ПОЛОЖИТЕЛЬНЫЙ КОНТРОЛЬ. Без него «ошибка» выше доказывала бы лишь то, что этот UPDATE не
	// работает никогда — например, из-за опечатки в имени колонки.
	after, cancel2 := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel2()
	_, err = raw.ExecContext(after,
		`UPDATE design_run SET claim_token = ? WHERE id = ?`, uuid.NewString(), started.Run.Id)
	require.NoError(t, err, "та же ротация ПОСЛЕ коммита проходит немедленно")
}

// ─────────────────────── сторожа, взятые из исходника и исполненные ───────────────────────

// designRunUpdatesOf возвращает операторы `UPDATE design_run`, которые именованная функция пакета
// design отдаёт исполнителю, — СОБРАННЫМИ ТАК ЖЕ, КАК ИХ СОБИРАЕТ КОМПИЛЯТОР.
//
// РАЗБОР AST, А НЕ ГРЕП, и это не педантизм. Оператор ClaimRuns существует в исходнике как
// «литерал + designRunClaimableSQL»: грепом по файлу видно только первую половину, а сторож живёт
// во второй. Разбор склеивает конкатенацию и подставляет строковые константы пакета, поэтому
// проба видит ровно тот текст, который уедет в MySQL. Ищется по ВСЕМУ пакету, а не по queue.go:
// переезд функции в соседний файл — не повод пробе ослепнуть.
func designRunUpdatesOf(t *testing.T, fn string) []string {
	t.Helper()
	entries, err := os.ReadDir(".")
	require.NoError(t, err)

	fset := token.NewFileSet()
	files := make([]*ast.File, 0, len(entries))
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(fset, filepath.Join(".", name), nil, 0)
		require.NoError(t, err, "исходник пакета design не разобрался: %s", name)
		files = append(files, f)
	}

	// Строковые константы пакета — словарь для подстановки в конкатенацию.
	consts := map[string]string{}
	for _, f := range files {
		for _, d := range f.Decls {
			gd, ok := d.(*ast.GenDecl)
			if !ok || gd.Tok != token.CONST {
				continue
			}
			for _, sp := range gd.Specs {
				vs, ok := sp.(*ast.ValueSpec)
				if !ok {
					continue
				}
				for i, nm := range vs.Names {
					if i >= len(vs.Values) {
						continue
					}
					if s, ok := designGoString(vs.Values[i], nil); ok {
						consts[nm.Name] = s
					}
				}
			}
		}
	}

	var found []string
	for _, f := range files {
		for _, d := range f.Decls {
			fd, ok := d.(*ast.FuncDecl)
			if !ok || fd.Name.Name != fn || fd.Body == nil {
				continue
			}
			ast.Inspect(fd.Body, func(n ast.Node) bool {
				call, ok := n.(*ast.CallExpr)
				if !ok {
					return true
				}
				for _, arg := range call.Args {
					s, ok := designGoString(arg, consts)
					if ok && strings.Contains(s, "UPDATE design_run") {
						found = append(found, s)
					}
				}
				return true
			})
		}
	}
	require.NotEmpty(t, found,
		"в пакете design не нашлось ни одного UPDATE design_run внутри %s — либо оператор переехал, "+
			"либо функция переименована; проба обязана быть переписана вслед, а не удалена", fn)
	return found
}

// designGoString склеивает выражение Go в строку: литерал, строковая константа пакета либо их
// конкатенация. Всё прочее — не строка, и это честный «нет».
func designGoString(e ast.Expr, consts map[string]string) (string, bool) {
	switch v := e.(type) {
	case *ast.BasicLit:
		if v.Kind != token.STRING {
			return "", false
		}
		s, err := strconv.Unquote(v.Value)
		return s, err == nil
	case *ast.Ident:
		s, ok := consts[v.Name]
		return s, ok
	case *ast.ParenExpr:
		return designGoString(v.X, consts)
	case *ast.BinaryExpr:
		if v.Op != token.ADD {
			return "", false
		}
		l, lok := designGoString(v.X, consts)
		r, rok := designGoString(v.Y, consts)
		if !lok || !rok {
			return "", false
		}
		return l + r, true
	}
	return "", false
}

var designNamedParam = regexp.MustCompile(`:([a-z_]+)`)

// execProbeStatement связывает именованные параметры продакшен-оператора и исполняет его,
// возвращая ЧИСЛО ЗАТРОНУТЫХ СТРОК. Имя, которого нет в values, — это не «подставим ноль», а
// падение с именем: оператор изменился, и проба обязана быть перечитана человеком.
func execProbeStatement(t *testing.T, raw *sql.DB, stmt string, values map[string]any) int64 {
	t.Helper()
	args := map[string]any{}
	for _, m := range designNamedParam.FindAllStringSubmatch(stmt, -1) {
		name := m[1]
		v, ok := values[name]
		require.Truef(t, ok, "оператор просит параметр %q, которого проба не знает:\n%s", name, stmt)
		args[name] = v
	}
	q, bound, err := sqlx.Named(stmt, args)
	require.NoError(t, err)
	res, err := raw.Exec(q, bound...)
	require.NoError(t, err)
	n, err := res.RowsAffected()
	require.NoError(t, err)
	return n
}

// heldProbeRun приводит строку в состояние «идёт, ведёт её token, лиза жива» — то самое, в котором
// закрывающий UPDATE обязан различать своего и чужого.
func heldProbeRun(t *testing.T, raw *sql.DB, runID int, token string) {
	t.Helper()
	_, err := raw.Exec(`
		UPDATE design_run
		SET status = 'running', claim_token = ?,
		    claim_expires_at = DATE_ADD(UTC_TIMESTAMP(6), INTERVAL 1 HOUR)
		WHERE id = ?`, token, runID)
	require.NoError(t, err)
}

// ЗАКРЫВАЮЩИЙ UPDATE CompleteRun НЕ ЗАКРЫВАЕТ ЧУЖУЮ СТРОКУ.
//
// Что сломается, если сторож исчезнет: воркер, чей захват истёк и чью работу уже переделал другой,
// закроет строку СВОИМ ответом. Обе стороны увидят успех, а карточка получит выдачу того, кто
// пришёл вторым, — молча, без единого отказа. requireClaim этого не ловит: он читает строку ДО
// записи, и его хватает ровно до тех пор, пока читать и писать нельзя порознь (см. шапку раздела).
//
// МУТАЦИЯ: убрать `AND claim_token = :tok` из этого UPDATE в queue.go.
func TestDesignDBCompleteRunClosingUpdateRefusesAForeignToken(t *testing.T) {
	rep, raw := probeRepository(t)
	resetBudget(t, raw)
	card := probeCard(t, raw)

	stmts := designRunUpdatesOf(t, "CompleteRun")
	require.Len(t, stmts, 1, "CompleteRun закрывает строку РОВНО ОДНИМ UPDATE design_run")

	run := startProbeRun(t, rep, card, "0.10").Run.Id
	mine := uuid.NewString()
	heldProbeRun(t, raw, run, mine)

	base := map[string]any{"id": run, "text": nil}

	foreign := map[string]any{"tok": uuid.NewString()}
	for k, v := range base {
		foreign[k] = v
	}
	require.Zero(t, execProbeStatement(t, raw, stmts[0], foreign),
		"чужой токен обязан закрыть НОЛЬ строк:\n%s", stmts[0])

	// ПОЛОЖИТЕЛЬНЫЙ КОНТРОЛЬ, и он здесь не украшение: без него ноль выше объяснялся бы чем
	// угодно — не тем предикатом, опечаткой в связывании, вовсе не тем оператором.
	own := map[string]any{"tok": mine}
	for k, v := range base {
		own[k] = v
	}
	require.EqualValues(t, 1, execProbeStatement(t, raw, stmts[0], own),
		"тот же оператор со СВОИМ токеном строку закрывает")

	status, token := runStatus(t, raw, run)
	require.Equal(t, entity.DesignRunDone, status)
	require.False(t, token.Valid, "закрытая строка не принадлежит никому")
}

// ОБА ЗАКРЫВАЮЩИХ UPDATE FailRun НЕ ТРОГАЮТ ЧУЖУЮ СТРОКУ.
//
// Их два, потому что отказ ветвится: ретрай возвращает строку в очередь, терминальный кладёт её
// насовсем. Проба ходит по ОБОИМ и на каждом требует отказ, иначе сторож, снятый с одной ветки,
// уехал бы незамеченным — а цена веток одинакова: опоздавший роняет задание, которое уже ведёт
// другой, и его оплаченная попытка превращается в ретрай чужого прогона.
//
// МУТАЦИЯ: убрать `AND claim_token = :tok` из любого из двух UPDATE в queue.go.
func TestDesignDBFailRunClosingUpdatesRefuseAForeignToken(t *testing.T) {
	rep, raw := probeRepository(t)
	resetBudget(t, raw)
	card := probeCard(t, raw)

	stmts := designRunUpdatesOf(t, "FailRun")
	require.Len(t, stmts, 2, "у FailRun две ветки записи: ретрай и терминальная")

	for i, stmt := range stmts {
		run := startProbeRun(t, rep, card, "0.10").Run.Id
		mine := uuid.NewString()
		heldProbeRun(t, raw, run, mine)

		base := map[string]any{
			"id":     run,
			"next":   time.Now().UTC().Add(time.Minute),
			"code":   nil,
			"err":    nil,
			"status": entity.DesignRunFailed,
		}
		foreign := map[string]any{"tok": uuid.NewString()}
		for k, v := range base {
			foreign[k] = v
		}
		require.Zerof(t, execProbeStatement(t, raw, stmt, foreign),
			"ветка %d: чужой токен обязан затронуть НОЛЬ строк:\n%s", i, stmt)

		own := map[string]any{"tok": mine}
		for k, v := range base {
			own[k] = v
		}
		require.EqualValuesf(t, 1, execProbeStatement(t, raw, stmt, own),
			"ветка %d: тот же оператор со СВОИМ токеном пишет", i)
	}
}

// UPDATE ЗАХВАТА ПОВТОРЯЕТ ПРЕДИКАТ ГОТОВНОСТИ, А НЕ ДОВЕРЯЕТ ЕГО SELECT'У.
//
// Что сломается, если повтор исчезнет: захват станет писать по id, добытому чтением, — и всякая
// причина, по которой строку брать нельзя (живая лиза соседа, отменённое задание, ещё не
// наступивший next_attempt_at), перестанет действовать в момент ЗАПИСИ. Ниже это исполнено на
// строке с ЖИВОЙ ЛИЗОЙ: сторож обязан затронуть ноль строк, а без него захват уведёт токен у
// воркера, который прямо сейчас платит провайдеру, и тот НИКОГДА не сможет закрыть свой прогон.
//
// Заметьте, чего проба НЕ утверждает: что через ClaimRuns такое состояние достижимо. Оно
// недостижимо — SELECT ... FOR UPDATE SKIP LOCKED запирает строку до записи, и предикат в UPDATE
// проверяет то же, что уже проверено. Это страховка, и здесь она измерена как страховка.
//
// МУТАЦИЯ: заменить `WHERE id = :id AND `+designRunClaimableSQL на `WHERE id = :id` в queue.go.
func TestDesignDBClaimUpdateRepeatsTheClaimablePredicate(t *testing.T) {
	rep, raw := probeRepository(t)
	resetBudget(t, raw)
	card := probeCard(t, raw)

	stmts := designRunUpdatesOf(t, "ClaimRuns")
	require.Len(t, stmts, 1, "захват берёт строку РОВНО ОДНИМ UPDATE design_run")

	// Строка, которую ведёт сосед и чья лиза ЖИВА.
	busy := startProbeRun(t, rep, card, "0.10").Run.Id
	neighbour := uuid.NewString()
	heldProbeRun(t, raw, busy, neighbour)

	require.Zero(t, execProbeStatement(t, raw, stmts[0], map[string]any{
		"id": busy, "tok": uuid.NewString(), "lease_micros": time.Minute.Microseconds(),
	}), "живую лизу захват обязан НЕ трогать:\n%s", stmts[0])

	_, token := runStatus(t, raw, busy)
	require.Equal(t, neighbour, token.String, "токен соседа устоял")

	// ПОЛОЖИТЕЛЬНЫЙ КОНТРОЛЬ: свободную строку тот же оператор берёт.
	free := startProbeRun(t, rep, card, "0.10").Run.Id
	taker := uuid.NewString()
	require.EqualValues(t, 1, execProbeStatement(t, raw, stmts[0], map[string]any{
		"id": free, "tok": taker, "lease_micros": time.Minute.Microseconds(),
	}))
	status, token := runStatus(t, raw, free)
	require.Equal(t, entity.DesignRunRunning, status)
	require.Equal(t, taker, token.String)
}
