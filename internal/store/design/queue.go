package design

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jekabolt/grbpwr-manager/internal/dependency"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/jekabolt/grbpwr-manager/internal/store/storeutil"
	"github.com/shopspring/decimal"
)

// МАШИНА ОЧЕРЕДИ: pending → (захват, running, лиза) → done | failed | cancelled.
//
// ПРИЁМ ВЗЯТ ДОСЛОВНО У internal/store/campaign/recipient.go: сначала SELECT … FOR UPDATE SKIP
// LOCKED по предикату готовности, затем UPDATE, В КОТОРОМ ТОТ ЖЕ ПРЕДИКАТ ПОВТОРЁН ЦЕЛИКОМ,
// вместе с условием лизы.
//
// ПОЧЕМУ ПРЕДИКАТ ПОВТОРЯЕТСЯ — И ЧЕМ ЭТОТ ДОВОД БЫЛ НЕВЕРЕН. Здесь стояло: «без повтора второй
// захват выиграл бы гонку у живого токена». ЭТО ИЗМЕРЕНО И ОПРОВЕРГНУТО. Пишущая транзакция
// открыта SERIALIZABLE (internal/store/db.go), InnoDB превращает на этом уровне обычный SELECT в
// чтение ПОД БЛОКИРОВКОЙ, и строка заперта до конца транзакции захвата — окна между чтением и
// записью НЕ СУЩЕСТВУЕТ. Замер: TestDesignDBWriteTxLocksTheRunRowItRead — чужая ротация токена
// ЖДЁТ, пока прочитавшая транзакция открыта, и проходит мгновенно после её коммита. Под удалением
// этого повтора четыре поведенческие пробы очереди остаются ЗЕЛЁНЫМИ.
//
// ПОЭТОМУ ПОВТОР ОСТАЁТСЯ, НО ПО ДРУГОЙ ПРИЧИНЕ: он — страховка на ПОНИЖЕНИЕ ИЗОЛЯЦИИ. Гарантию
// сегодня несут SERIALIZABLE и SKIP LOCKED, а не он; в день, когда кто-то опустит уровень до
// READ COMMITTED ради скорости — а это правка в другом файле, которую ревьюер очереди не увидит,
// — повторённый предикат окажется единственным, что стоит между двумя воркерами и потерянным
// результатом. Ложный довод был опаснее отсутствующего: он приглашал удалить сторож («такого не
// бывает») и одновременно ПРЯТАЛ то, что корректность держится на уровне изоляции.
//
// ЧТО СТОРОЖИТ САМ ОПЕРАТОР: TestDesignDBClaimUpdateRepeatsTheClaimablePredicate достаёт текст
// запроса разбором AST и ИСПОЛНЯЕТ его на живом MySQL — своя копия SQL осталась бы зелёной.
//
// ЧЕМ ДОКАЗЫВАЕТСЯ, ЧТО ИСТЁКШИЙ ЗАХВАТ НЕ ЗАТРЁТ ЧУЖОЙ РЕЗУЛЬТАТ: claim_token стоит в WHERE у
// ОБОИХ закрывающих глаголов (CompleteRun и FailRun), а перехват задания меняет токен строки.
// Значит опоздавший воркер получает rows = 0 и отказ claim_lost — не «успех, но мимо».

// designRunClaimableSQL — предикат готовности строки к работе. ОДИН ТЕКСТ на SELECT и на UPDATE:
// две копии предиката — это два предиката, и расходятся они молча.
//
// БЕЗ АЛИАСА ТАБЛИЦЫ намеренно: UPDATE в MySQL алиас не получает, и предикат с `r.` не
// подставился бы в него без переписывания — то есть ровно та копия, которой быть не должно.
//
// `kind <> 'draft_idea'` — текстовый прогон исполняет ХЕНДЛЕР синхронно (см. designHandlerLease):
// воркер, забравший его строку, оплатил бы второй вызов той же модели.
const designRunClaimableSQL = `
	status = 'pending'
	AND kind <> 'draft_idea'
	AND cancel_requested_at IS NULL
	AND (next_attempt_at IS NULL OR next_attempt_at <= UTC_TIMESTAMP(6))
	AND (claim_token IS NULL OR claim_expires_at IS NULL OR claim_expires_at < UTC_TIMESTAMP(6))`

// designRunResumableSQL — предикат ПЕРЕХВАТА БРОШЕННОГО ХЕНДЛЕРА, и он ТОЧНОЕ ДОПОЛНЕНИЕ
// designRunClaimableSQL по роду: воркер не берёт `draft_idea` вовсе, значит подобрать его строку
// может только следующий хендлер того же client_request_id.
//
// ⚠ ЖИВОСТЬ ЛИЗЫ СТОИТ ЗДЕСЬ, В WHERE ЗАПИСИ, А НЕ В ПРОВЕРКЕ ПЕРЕД НЕЙ, и это тот же довод, что
// у ClaimRuns: проверка даёт человеческий отказ, а WHERE даёт ИСКЛЮЧЕНИЕ. Пока перехват сводился
// к чтению строки и переиспользованию ЕЁ ЖЕ токена, два одновременных повтора одного
// client_request_id проходили ОБА: оба видели истёкшую лизу, оба брали один и тот же токен, оба
// открывали попытку (MAX(attempt_no)+1 их лишь нумерует) и ОБА ПЛАТИЛИ МОДЕЛИ. Обещание
// «повтор = один платёж» — единственное, ради чего client_request_id существует, — в окне резюма
// не выполнялось.
//
// ПОЧЕМУ ТОКЕН РОТИРУЕТСЯ, А НЕ ПЕРЕИСПОЛЬЗУЕТСЯ. Так устроена вся остальная машина: перехват
// задания МЕНЯЕТ claim_token, и опоздавший получает rows = 0 и claim_lost вместо «успеха мимо»
// (см. шапку файла). Оставленный прежним токен сделал бы двух хендлеров неразличимыми для
// CompleteRun — первый закрыл бы прогон, который ведёт второй, и оба ответа выглядели бы удачными.
//
// ПОЧЕМУ ЛИЗА ПРОДЛЕВАЕТСЯ. Перехват без продления оставляет строку с истёкшей лизой, то есть
// открытой для ТРЕТЬЕГО повтора секундой позже — исключение действовало бы ровно один раз.
const designRunResumableSQL = `
	kind = 'draft_idea'
	AND status IN ('pending', 'running')
	AND cancel_requested_at IS NULL
	AND claim_token IS NOT NULL
	AND claim_expires_at IS NOT NULL
	AND claim_expires_at < UTC_TIMESTAMP(6)`

// resumeHandlerRun ПЫТАЕТСЯ перехватить строку, чей синхронный хендлер не вернулся.
//
// Возвращает (false, строка как есть, nil), когда перехватывать нечего: прогон закончен, отменён,
// либо его лиза ЖИВА — «вызов идёт прямо сейчас, в соседнем запросе», и второй звонок оплатил бы
// ту же модель второй раз. Ровно это же значение возвращается ПРОИГРАВШЕМУ гонки: строку у него
// увели между чтением и записью, и правильный ответ ему — та же строка, которую ведёт победитель.
func resumeHandlerRun(ctx context.Context, db dependency.DB, prior entity.DesignRun) (bool, entity.DesignRun, error) {
	token := uuid.NewString()
	rows, err := storeutil.ExecNamedRows(ctx, db, `
		UPDATE design_run
		SET claim_token = :tok,
		    claim_expires_at = DATE_ADD(UTC_TIMESTAMP(6), INTERVAL :lease_micros MICROSECOND),
		    started_at = COALESCE(started_at, UTC_TIMESTAMP(6))
		WHERE id = :id AND `+designRunResumableSQL,
		map[string]any{
			"id": prior.Id, "tok": token,
			"lease_micros": designHandlerLease.Microseconds(),
		})
	if err != nil {
		return false, entity.DesignRun{}, fmt.Errorf("failed to resume design run %d: %w", prior.Id, err)
	}
	if rows == 0 {
		return false, prior, nil
	}
	// Строка ПЕРЕЧИТЫВАЕТСЯ, а не правится в памяти: хендлеру уезжает claim_token, которым он
	// потом закроет прогон, и собранная из догадок копия строки — это второй источник правды о
	// том, кто ею владеет.
	run, err := runByID(ctx, db, prior.Id)
	if err != nil {
		return false, entity.DesignRun{}, err
	}
	return true, run, nil
}

const (
	// designMaxPaidAttempts — сколько раз одно задание вправе быть ОПЛАЧЕНО. Ретрай платит ВТОРОЙ
	// раз, поэтому потолок здесь — денежная величина, а не техническая.
	//
	// ⚠ ПЯТЬ ЗНАЧИТ ПЯТЬ. Здесь стояло `run.AttemptCount+1 < designMaxAttempts`, что давало
	// ЧЕТЫРЕ платных вызова при константе 5, — и три комментария в двух пакетах (этот,
	// designgen/dispatch.go и meshy.go) обещали пять. Верным признан комментарий: пятая попытка —
	// решение владельца о том, сколько денег задание вправе стоить, а четвёртая была опечаткой в
	// сравнении. Считает потолок теперь paidAttempts, и `paid < designMaxPaidAttempts` читается
	// ровно как обещание.
	//
	// ⚠ И ПОТОЛОК СТОИТ НА ЧИСЛЕ ПЛАТЕЖЕЙ, А НЕ НА attempt_count. Растровая дорога платит на
	// КАЖДОМ круге, а 3D — ОДИН раз: Submit покупает задачу, дальнейшие Collect/Await
	// БЕСПЛАТНЫ, но каждый неудавшийся опрос закрывает круг и увеличивает attempt_count. Пока
	// потолок стоял на attempt_count, оплаченная 3D-задача умирала терминально после трёх-четырёх
	// окон ожидания, не будучи оплаченной повторно ни разу: кредиты у провайдера списаны, модель,
	// возможно, ещё строится, а строка уже `failed` — и цена её так и осталась неизвестной,
	// потому что называет её только успешный сбор.
	designMaxPaidAttempts = 5

	// designMaxRounds — сколько раз задание вправе быть ВЗЯТО В РАБОТУ, платно или бесплатно.
	//
	// ЗАЧЕМ ВТОРОЙ ПОТОЛОК, РАЗ ПЕРВЫЙ ДЕНЕЖНЫЙ. Денежный сам по себе бесплатный цикл не
	// закрывает: 3D-прогон, заплативший однажды, опрашивал бы задачу, которая не придёт никогда,
	// до конца времён — и всё это время держал бы резерв дня и слот воркера. Ограничение здесь не
	// денежное, а на ЗАНЯТОСТЬ: runOnce строго последовательна, пачка по умолчанию равна единице,
	// и каждый круг держит очередь до RunTimeout.
	//
	// ПОЧЕМУ ДЕСЯТЬ. На 3D-дороге это девять окон ожидания: Await опрашивает до двенадцати минут,
	// между кругами стоит экспонента до пятнадцати — то есть больше трёх часов на задачу, которую
	// провайдер строит минуты. На растровой дороге он не срабатывает никогда: там раньше
	// упирается денежный потолок в пять.
	designMaxRounds = 10

	// designRetryBase / designRetryMax — экспонента возврата в очередь.
	designRetryBase = 30 * time.Second
	designRetryMax  = 15 * time.Minute
)

// designPaidAttemptsSQL — СКОЛЬКО РАЗ ПРОГОН БЫЛ ОПЛАЧЕН, выведенное из строк попыток.
//
// ПОЧЕМУ ВЫВОДИТСЯ, А НЕ ХРАНИТСЯ. Колонка «эта попытка была платной» потребовала бы миграции, а
// число выводится из того, что УЖЕ записано, без единой догадки — и выводится ТЕМ ЖЕ признаком, по
// которому решение принимает воркер. designgen/dispatch.go идёт в бесплатный сбор ровно тогда,
// когда acceptedRequestID нашёл попытку в состоянии `accepted` с непустым provider_request_id;
// значит всё, что стоит ПОСЛЕ такой попытки, — опросы уже купленного задания, а не новые покупки.
//
// САМА `accepted` СЧИТАЕТСЯ ПЛАТНОЙ, и это несущая половина условия: `accepted` — это и есть
// состоявшаяся отправка. Без неё второй Submit (он случается, когда чтение попыток не удалось и
// воркер счёл прогон свежим) не был бы виден потолку вовсе.
const designPaidAttemptsSQL = `
	SELECT COUNT(*) FROM design_run_attempt a
	WHERE a.run_id = :run
	  AND (a.state = 'accepted'
	       OR NOT EXISTS (
	           SELECT 1 FROM design_run_attempt b
	           WHERE b.run_id = a.run_id
	             AND b.attempt_no < a.attempt_no
	             AND b.state = 'accepted'
	             AND b.provider_request_id IS NOT NULL
	             AND b.provider_request_id <> ''))`

// paidAttempts — то же число, вызовом.
func paidAttempts(ctx context.Context, db dependency.DB, runID int) (int, error) {
	n, err := storeutil.QueryCountNamed(ctx, db, designPaidAttemptsSQL, map[string]any{"run": runID})
	if err != nil {
		return 0, fmt.Errorf("failed to count the paid attempts of design run %d: %w", runID, err)
	}
	return n, nil
}

// ClaimRuns leases up to n pending runs to a worker.
func (s *Store) ClaimRuns(ctx context.Context, n int, lease time.Duration, claimToken string) ([]entity.DesignRun, error) {
	if n <= 0 {
		return nil, fmt.Errorf("%w: a claim of %d runs asks for nothing", entity.ErrDesignInvalidArgument, n)
	}
	if lease <= 0 {
		return nil, fmt.Errorf("%w: a claim without a lease can never expire", entity.ErrDesignInvalidArgument)
	}
	if claimToken == "" {
		return nil, fmt.Errorf("%w: a claim without a token cannot be closed", entity.ErrDesignInvalidArgument)
	}

	var claimed []entity.DesignRun
	err := s.txFunc(ctx, func(ctx context.Context, rep dependency.Repository) error {
		claimed = nil
		db := rep.DB()

		ids, err := storeutil.QueryScalarListNamed[int](ctx, db, `
			SELECT id FROM design_run
			WHERE `+designRunClaimableSQL+`
			ORDER BY id
			LIMIT :n
			FOR UPDATE SKIP LOCKED`, map[string]any{"n": n})
		if err != nil {
			return fmt.Errorf("failed to pick design runs ready for work: %w", err)
		}
		if len(ids) == 0 {
			return nil
		}

		taken := make([]int, 0, len(ids))
		for _, id := range ids {
			rows, err := storeutil.ExecNamedRows(ctx, db, `
				UPDATE design_run
				SET status = 'running',
				    claim_token = :tok,
				    claim_expires_at = DATE_ADD(UTC_TIMESTAMP(6), INTERVAL :lease_micros MICROSECOND),
				    started_at = COALESCE(started_at, UTC_TIMESTAMP(6))
				WHERE id = :id AND `+designRunClaimableSQL,
				map[string]any{"id": id, "tok": claimToken, "lease_micros": lease.Microseconds()})
			if err != nil {
				return fmt.Errorf("failed to claim design run %d: %w", id, err)
			}
			// rows == 0 — строку увели между SELECT и UPDATE. Это НЕ ошибка: захват берёт
			// столько, сколько досталось, и молчаливый пропуск здесь честнее отказа всей пачке.
			if rows == 1 {
				taken = append(taken, id)
			}
		}
		if len(taken) == 0 {
			return nil
		}
		claimed, err = storeutil.QueryListNamed[entity.DesignRun](ctx, db,
			`SELECT * FROM design_run WHERE id IN (:ids) ORDER BY id`,
			map[string]any{"ids": taken})
		if err != nil {
			return fmt.Errorf("failed to read claimed design runs: %w", err)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return claimed, nil
}

// designRunAbandonedCancelledSQL — ОТМЕНЁННАЯ СТРОКА, КОТОРУЮ НЕКОМУ ДОВЕСТИ ДО КОНЦА.
//
// ⚠ ЭТО ЕДИНСТВЕННОЕ СОСТОЯНИЕ, ИЗ КОТОРОГО НЕ БЫЛО ВЫХОДА ВОВСЕ. Человек отменяет идущий прогон
// — CancelRun ставит cancel_requested_at и оставляет строку воркеру (закрыть её здесь значило бы
// выбросить оплаченный результат, который придёт секундой позже). Воркер умирает на редеплое, не
// успев закрыть строку. Дальше: подметальщик возвращает её в `pending`, а предикат захвата не
// берёт отменённые (`cancel_requested_at IS NULL`) — значит терминального перехода не будет
// НИКОГДА, а он единственный, кто снимает резерв дня. Строка висит в pending, деньги дня заняты
// прогоном, который никто не ведёт, и лечит это только смена календарной даты.
//
// ЖИВОСТЬ ЛИЗЫ В ПРЕДИКАТЕ ОБЯЗАТЕЛЬНА. Отменённый прогон с ЖИВЫМ захватом ведёт живой воркер (а
// для kind=draft_idea — живой хендлер, чью лизу выдал сам StartRun), и он честит отмену сам, до
// отправки и после ответа. Подмести такую строку значило бы отобрать её у того, кто прямо сейчас
// за неё платит.
//
// `pending` СТОИТ В СПИСКЕ НАРАВНЕ С `running` не про запас: именно в pending и лежат уже
// накопленные сироты — те, кого прежний подметальщик успел вернуть в очередь. Законного состояния
// «ждёт и отменён» не существует: CancelRun закрывает ждущий прогон терминально одним оператором.
const designRunAbandonedCancelledSQL = `
	status IN ('pending', 'running')
	AND cancel_requested_at IS NOT NULL
	AND (claim_token IS NULL OR claim_expires_at IS NULL OR claim_expires_at < UTC_TIMESTAMP(6))`

// ReviveExpiredRuns подметает всё, что осталось от умерших воркеров: возвращает в очередь строки
// с истёкшей лизой, терминально закрывает исчерпавшие потолок и отменённые-и-брошенные.
//
// БЕЗ ЭТОГО ПОДМЕТАЛЬЩИКА «истёкший захват — та же дорога» это дорога без ног: ClaimRuns берёт
// только `pending`, а строка умершего воркера осталась `running` навсегда.
//
// ТОКЕН СТИРАЕТСЯ, и у этого есть цена, названная вслух: воркер, который всё-таки жив и придёт с
// результатом ПОСЛЕ подметания, получит claim_lost, и оплаченный кадр будет потерян. Обратный
// выбор (оставить токен) стоит дороже: тогда «истёкшая лиза» перестаёт что-либо значить, и две
// копии одного задания идут к провайдеру, обе считая себя владельцами строки. Лечится это не
// выбором, а длиной лизы: она обязана превышать самый долгий вызов провайдера.
func (s *Store) ReviveExpiredRuns(ctx context.Context) (int, error) {
	var revived int
	err := s.txFunc(ctx, func(ctx context.Context, rep dependency.Repository) error {
		revived = 0
		db := rep.DB()

		// 1. ОТМЕНЁННЫЕ И БРОШЕННЫЕ — ПЕРВЫМИ, и порядок здесь несущий: пункт 2 закрыл бы их как
		// `failed`, а человек их ОТМЕНИЛ, и строка истории обязана говорить это, а не «упало».
		if err := sweepAbandonedCancelledRuns(ctx, db); err != nil {
			return err
		}

		// 2. ПОТОЛКИ ДЕЙСТВУЮТ И ЗДЕСЬ. Задание, чей воркер умирает раз за разом, иначе воскресало
		// бы вечно — и каждый круг мог бы стоить денег, потому что смерть наступает ПОСЛЕ вызова
		// провайдера так же часто, как до него.
		if err := closeRunsPastTheirCeiling(ctx, db); err != nil {
			return err
		}

		// 3. ОСТАЛЬНЫЕ ВОЗВРАЩАЮТСЯ В ОЧЕРЕДЬ. next_attempt_at ставится в «сейчас»: истёкшая
		// лиза не есть провал провайдера, и заставлять задание ждать экспоненту не за что.
		n, err := storeutil.ExecNamedRows(ctx, db, `
			UPDATE design_run
			SET status = 'pending',
			    claim_token = NULL,
			    claim_expires_at = NULL,
			    next_attempt_at = UTC_TIMESTAMP(6)
			WHERE status = 'running'
			  AND claim_expires_at IS NOT NULL
			  AND claim_expires_at < UTC_TIMESTAMP(6)`, map[string]any{})
		if err != nil {
			return fmt.Errorf("failed to revive expired design runs: %w", err)
		}
		revived = int(n)
		return nil
	})
	if err != nil {
		return 0, err
	}
	return revived, nil
}

// sweepAbandonedCancelledRuns доводит отменённую-и-брошенную строку до `cancelled`.
//
// РЕЗЕРВ СНИМАЕТСЯ ТЕМ ЖЕ releaseRunReserve, ЧТО И НА ЧЕТЫРЁХ ОСТАЛЬНЫХ ПУТЯХ (CompleteRun,
// FailRun, CancelRun ждущего, потолок здесь же), и второго механизма снятия не заводится: снятие
// в одном месте, под сторожем перехода, — единственное, что делает его однократным по построению
// (см. шапку wave2.go). Сторожем здесь служит повторённый предикат: строка, которую параллельно
// закрыл кто-то другой, даёт rows = 0, и денег этот проход не трогает.
func sweepAbandonedCancelledRuns(ctx context.Context, db dependency.DB) error {
	orphans, err := storeutil.QueryListNamed[entity.DesignRun](ctx, db, `
		SELECT * FROM design_run
		WHERE `+designRunAbandonedCancelledSQL+`
		ORDER BY id`, map[string]any{})
	if err != nil {
		return fmt.Errorf("failed to read cancelled design runs nobody is running: %w", err)
	}
	for _, run := range orphans {
		rows, err := storeutil.ExecNamedRows(ctx, db, `
			UPDATE design_run
			SET status = 'cancelled',
			    completed_at = COALESCE(completed_at, UTC_TIMESTAMP(6)),
			    error_code = COALESCE(error_code, 'cancelled_abandoned'),
			    claim_token = NULL,
			    claim_expires_at = NULL
			WHERE id = :id AND `+designRunAbandonedCancelledSQL,
			map[string]any{"id": run.Id})
		if err != nil {
			return fmt.Errorf("failed to close abandoned cancelled design run %d: %w", run.Id, err)
		}
		if rows == 1 {
			if err := releaseRunReserve(ctx, db, run); err != nil {
				return err
			}
		}
	}
	return nil
}

// closeRunsPastTheirCeiling терминально закрывает брошенные строки, исчерпавшие потолок — денежный
// либо потолок кругов.
//
// ⚠ ВЫБОРКА БОЛЬШЕ НЕ НЕСЁТ ПРЕДИКАТ ПОТОЛКА, И ЭТО ВЫНУЖДЕННО: число ПЛАТЕЖЕЙ выводится из
// строк попыток (designPaidAttemptsSQL), то есть предикат перестал быть выражением по одной
// таблице. Поэтому решение принимает Go, а сторожем повторной записи служит `attempt_count = :ac`
// — оптимистичная сверка с тем значением, ПО КОТОРОМУ решение и было принято. Роль у неё та же,
// что у повторённого предиката в ClaimRuns: гарантию сегодня несёт SERIALIZABLE, а сторож стоит
// на день, когда уровень изоляции опустят.
func closeRunsPastTheirCeiling(ctx context.Context, db dependency.DB) error {
	expired, err := storeutil.QueryListNamed[entity.DesignRun](ctx, db, `
		SELECT * FROM design_run
		WHERE status = 'running'
		  AND claim_expires_at IS NOT NULL
		  AND claim_expires_at < UTC_TIMESTAMP(6)
		ORDER BY id`, map[string]any{})
	if err != nil {
		return fmt.Errorf("failed to read design runs with an expired lease: %w", err)
	}
	for _, run := range expired {
		paid, err := paidAttempts(ctx, db, run.Id)
		if err != nil {
			return err
		}
		if paid < designMaxPaidAttempts && run.AttemptCount < designMaxRounds {
			continue
		}
		rows, err := storeutil.ExecNamedRows(ctx, db, `
			UPDATE design_run
			SET status = 'failed',
			    error_code = COALESCE(error_code, 'lease_expired'),
			    completed_at = UTC_TIMESTAMP(6),
			    claim_token = NULL,
			    claim_expires_at = NULL
			WHERE id = :id AND status = 'running' AND attempt_count = :ac`,
			map[string]any{"id": run.Id, "ac": run.AttemptCount})
		if err != nil {
			return fmt.Errorf("failed to close design run %d past its ceiling: %w", run.Id, err)
		}
		if rows == 1 {
			if err := releaseRunReserve(ctx, db, run); err != nil {
				return err
			}
		}
	}
	return nil
}

// RecordRunPrompt пишет СОБРАННЫЙ текст промпта в строку прогона — тот самый Job.Prompt, который
// воркер через мгновение отправит поставщику.
//
// ЭТО ВЫБРАННАЯ СТОРОНА РАЗВИЛКИ «хранить отправленное» против «глагол предпросмотра».
// Предпросмотр — вторая сборка текста в другое время другим кодом, и показанное могло бы молча
// разойтись с отправленным; здесь же колонка наполняется той же строкой, которая уходит в сеть,
// поэтому история несёт отправленный текст ПО ПОСТРОЕНИЮ (см. Worker.execute, record-then-spend —
// запись стоит ДО первой платной попытки).
//
// ⚠ ДВЕ ОГОВОРКИ, БЕЗ КОТОРЫХ ПРЕДЫДУЩИЙ АБЗАЦ ПЕРЕОБЕЩАЕТ. Во-первых, «отправленный текст» верен
// на одновызовном флэтовом маршруте; на `per_view` каждому платному вызову дописывается свой
// «view:», а 3D режет текст по потолку текстуры — там в колонке лежит БАЗОВАЯ инструкция.
// Во-вторых, возобновление уже оплаченного асинхронного задания эту запись НЕ делает вовсе:
// оно ничего не отправляет, а перезапись переписала бы историю текстом, которого никто не слышал.
//
// СВЕРКА ЗАХВАТА — как у всех пишущих глаголов машины. Опоздавший воркер, чья аренда истекла, не
// вправе переписать текст поверх записи воркера, который строкой владеет: их снапшот один, но
// composer мог смениться между деплоями, и строка обязана следовать за тем, кто реально шлёт.
// Повторная запись тем же владельцем безопасна, но НЕ потому, что «composePrompt — чистая функция
// замороженного снапшота»: это утверждение неверно и было здесь ошибкой. Состав промпта зависит от
// ЖИВОГО резолва медиа (`buildJob` → `GetMediaByIds`), поэтому удалённая между попытками картинка
// меняет и список вложений, и нумерацию подписей. Строка следует за тем, что ушло В ПОСЛЕДНИЙ РАЗ,
// и это осознанный выбор: история обязана описывать последнюю реальную отправку, а не первую.
func (s *Store) RecordRunPrompt(ctx context.Context, runID int, claimToken, prompt string) error {
	if runID <= 0 {
		return fmt.Errorf("%w: run id is required", entity.ErrDesignInvalidArgument)
	}
	return s.txFunc(ctx, func(ctx context.Context, rep dependency.Repository) error {
		db := rep.DB()
		run, err := runByID(ctx, db, runID)
		if err != nil {
			return err
		}
		if err := requireClaim(run, claimToken); err != nil {
			return err
		}
		if err := storeutil.ExecNamed(ctx, db, `
			UPDATE design_run
			SET prompt = :prompt
			WHERE id = :id AND claim_token = :tok`,
			map[string]any{"id": runID, "tok": claimToken, "prompt": prompt}); err != nil {
			return fmt.Errorf("failed to record the prompt of design run %d: %w", runID, err)
		}
		return nil
	})
}

// StartAttempt opens one paid provider call.
//
// ПОПЫТКА — СОБСТВЕННАЯ СТРОКА, и оплаченный провал тоже строка: полоса бюджета обязана ВИДЕТЬ,
// что ретрай заплатил второй раз. Идемпотентна по uq_design_run_attempt (run_id, attempt_no):
// повтор после потерянного ответа не заводит вторую попытку.
//
// (Эта шапка какое-то время стояла слитно с шапкой соседнего глагола, без пустой строки между
// ними, — и godoc отдавал ВЕСЬ блок соседу, а StartAttempt оставалась вовсе без описания. В этом
// коде доводы в шапках — навигация, поэтому такой слипшийся шов не косметика.)
func (s *Store) StartAttempt(ctx context.Context, req entity.DesignAttemptStart) (*entity.DesignRunAttempt, error) {
	if req.RunId <= 0 {
		return nil, fmt.Errorf("%w: run id is required", entity.ErrDesignInvalidArgument)
	}
	if req.Provider == "" {
		return nil, fmt.Errorf("%w: an attempt names the provider it goes to", entity.ErrDesignInvalidArgument)
	}
	if req.AttemptNo < 0 || req.AttemptNo > 255 {
		return nil, fmt.Errorf("%w: attempt_no %d is outside the column's range",
			entity.ErrDesignInvalidArgument, req.AttemptNo)
	}

	var out entity.DesignRunAttempt
	err := s.txFunc(ctx, func(ctx context.Context, rep dependency.Repository) error {
		db := rep.DB()
		run, err := runByID(ctx, db, req.RunId)
		if err != nil {
			return err
		}
		// ЗАХВАТ СВЕРЯЕТСЯ И ЗДЕСЬ, до всякого платного вызова: если строку уже перехватили,
		// дешевле узнать это ДО денег, чем после.
		if err := requireClaim(run, req.ClaimToken); err != nil {
			return err
		}
		no := req.AttemptNo
		if no == 0 {
			// «Следующая» вычисляется В ТРАНЗАКЦИИ, поэтому двух одинаковых номеров не бывает
			// даже при гонке: SERIALIZABLE упорядочивает чтение и вставку.
			if no, err = storeutil.QueryCountNamed(ctx, db,
				`SELECT COALESCE(MAX(attempt_no), 0) + 1 FROM design_run_attempt WHERE run_id = :run`,
				map[string]any{"run": req.RunId}); err != nil {
				return fmt.Errorf("failed to compute the next design attempt number: %w", err)
			}
		}
		if _, err := storeutil.ExecNamedLastId(ctx, db, `
			INSERT INTO design_run_attempt (run_id, attempt_no, provider, state, started_at)
			VALUES (:run, :no, :provider, 'dispatching', UTC_TIMESTAMP(6))
			ON DUPLICATE KEY UPDATE id = LAST_INSERT_ID(id)`,
			map[string]any{"run": req.RunId, "no": no, "provider": req.Provider}); err != nil {
			return fmt.Errorf("failed to open design attempt %d of run %d: %w", no, req.RunId, err)
		}
		// GREATEST, А НЕ +1: счётчик обязан сойтись с номером попытки даже если воркер
		// повторил StartAttempt после потерянного ответа. Слепой инкремент на повторе увёл бы
		// потолок ретраев вниз, и задание умерло бы, не исчерпав оплаченных попыток.
		if err := storeutil.ExecNamed(ctx, db, `
			UPDATE design_run
			SET attempt_count = GREATEST(attempt_count, :no),
			    started_at = COALESCE(started_at, UTC_TIMESTAMP(6))
			WHERE id = :run AND claim_token = :tok`,
			map[string]any{"run": req.RunId, "no": no, "tok": req.ClaimToken}); err != nil {
			return fmt.Errorf("failed to count design attempt %d of run %d: %w", no, req.RunId, err)
		}
		out, err = attemptByNo(ctx, db, req.RunId, no)
		return err
	})
	if err != nil {
		return nil, err
	}
	return &out, nil
}

// FinishAttempt closes one paid provider call and moves the day's counters.
//
// ТОКЕНА ЗАХВАТА В ЭТОМ ЗАПРОСЕ НЕТ, И ЭТО ВЕРНО. Здесь не пишется ни один РЕЗУЛЬТАТ: закрывается
// собственная строка попытки, адресованная парой (run_id, attempt_no), и растёт `spent` дня.
// Опоздавший воркер, чей вызов состоялся и был оплачен, ОБЯЗАН иметь возможность это записать —
// иначе полоса бюджета недосчитывает реальные деньги. Сторож захвата стоит там, где пишется
// результат: в CompleteRun и FailRun.
//
// ИДЕМПОТЕНТНОСТЬ — finished_at. Второй вызов на закрытую попытку денег НЕ ДВИГАЕТ: без этого
// повтор после потерянного ответа удваивал бы `spent`, то есть врал бы владельцу про его же
// траты в сторону увеличения.
//
// ⚠ И ЭТОГО БЫЛО МАЛО, ПОТОМУ ЧТО ПОВТОР ПРИХОДИТ НЕ ТОЛЬКО НА ТУ ЖЕ СТРОКУ. У асинхронной
// дороги платёж ОДИН (Submit), а строк попытки несколько: отправка закрывается как `accepted` с
// NULL-ценой, цену называет СБОР, и у сбора своя строка. Стоит первому сбору доехать, а прогону
// после этого вернуться в очередь — не сложилась загрузка в бакет, перехватили строку, — как
// следующий сбор спрашивает провайдера про ТУ ЖЕ задачу, получает ТЕ ЖЕ consumed_credits и
// закрывает СВОЮ, ещё не закрытую попытку той же суммой. finished_at про это ничего не знает.
// Дневной потолок съедался вымышленными тратами, а price_actual (СУММА цен попыток) показывал
// удвоенную цену модели, купленной один раз. См. chargeAlreadyBooked.
func (s *Store) FinishAttempt(ctx context.Context, req entity.DesignAttemptFinish) error {
	if req.RunId <= 0 || req.AttemptNo <= 0 {
		return fmt.Errorf("%w: an attempt is addressed by run id and attempt number",
			entity.ErrDesignInvalidArgument)
	}
	if !entity.IsDesignAttemptState(req.State) {
		return fmt.Errorf("%w: unknown attempt state %q", entity.ErrDesignInvalidArgument, req.State)
	}
	if req.Price.Valid && req.Price.Decimal.IsNegative() {
		return fmt.Errorf("%w: an attempt price cannot be negative", entity.ErrDesignInvalidArgument)
	}

	return s.txFunc(ctx, func(ctx context.Context, rep dependency.Repository) error {
		db := rep.DB()
		attempt, err := attemptByNo(ctx, db, req.RunId, req.AttemptNo)
		if err != nil {
			return err
		}
		if attempt.FinishedAt.Valid {
			return nil
		}
		// ЦЕНА ДУБЛЯ НЕ ПИШЕТСЯ ВОВСЕ, а не просто «не двигает день»: price_actual считается как
		// СУММА цен попыток, и записанная второй раз сумма соврала бы и в строке истории тоже.
		// Бесплатный опрос стоил ноль — NULL здесь и есть правда о нём.
		price := req.Price
		if price.Valid && price.Decimal.IsPositive() {
			booked, err := chargeAlreadyBooked(ctx, db, req.RunId, req.AttemptNo, req.ProviderRequestId)
			if err != nil {
				return err
			}
			if booked {
				price = decimal.NullDecimal{}
			}
		}
		rows, err := storeutil.ExecNamedRows(ctx, db, `
			UPDATE design_run_attempt
			SET provider_request_id = :prid,
			    state = :state,
			    price = :price,
			    error_code = :code,
			    finished_at = UTC_TIMESTAMP(6)
			WHERE run_id = :run AND attempt_no = :no AND finished_at IS NULL`,
			map[string]any{
				"run": req.RunId, "no": req.AttemptNo, "prid": nullStr(req.ProviderRequestId),
				"state": req.State, "price": price, "code": nullStr(req.ErrorCode),
			})
		if err != nil {
			return fmt.Errorf("failed to close design attempt %d of run %d: %w", req.AttemptNo, req.RunId, err)
		}
		if rows == 0 {
			// Кто-то закрыл её между чтением и записью — деньги уже посчитаны им.
			return nil
		}
		if err := syncRunPriceActual(ctx, db, req.RunId); err != nil {
			return err
		}
		if !price.Valid || !price.Decimal.IsPositive() {
			return nil
		}
		// ДЕНЬГИ ЛОЖАТСЯ НА ДЕНЬ, В КОТОРЫЙ ОНИ ПОТРАЧЕНЫ, а резерв снимается отдельно и на
		// СВОЙ день (см. releaseRunReserve): длинный прогон вполне переживает полночь.
		set, err := loadSettings(ctx, db)
		if err != nil {
			return err
		}
		run, err := runByID(ctx, db, req.RunId)
		if err != nil {
			return err
		}
		currency := run.Currency
		if currency == "" {
			currency = set.Currency
		}
		return moveBudgetDay(ctx, db,
			DesignBudgetDayKey(s.Now(), set.BudgetTimezone), decimal.Zero, price.Decimal, currency)
	})
}

// chargeAlreadyBooked — «этот счёт уже записан ДРУГОЙ попыткой того же прогона».
//
// ЧЕМ ОПОЗНАЁТСЯ ОДИН И ТОТ ЖЕ СЧЁТ: provider_request_id. У асинхронной дороги это id задачи, и
// каждый её опрос возвращает те же consumed_credits — сумма принадлежит ЗАДАЧЕ, а не обращению к
// ней. У синхронной дороги id ответа у каждого платного вызова свой; совпасть он может только
// когда провайдер сам склеил повтор по provider_idempotency_key — то есть когда второго списания
// опять-таки не было. В обе стороны правило одно: ОДИН id — ОДИН счёт.
//
// ПУСТОЙ id НЕ СКЛЕИВАЕТСЯ НИ С ЧЕМ. «Провайдер не назвал обращение» — не признак тождества, и
// считать две безымянные попытки одним платежом значило бы недосчитать реальные деньги; ошибаться
// здесь дешевле в сторону «записать», а не «потерять».
func chargeAlreadyBooked(ctx context.Context, db dependency.DB, runID, attemptNo int, providerRequestID string) (bool, error) {
	if providerRequestID == "" {
		return false, nil
	}
	n, err := storeutil.QueryCountNamed(ctx, db, `
		SELECT COUNT(*) FROM design_run_attempt
		WHERE run_id = :run
		  AND attempt_no <> :no
		  AND provider_request_id = :prid
		  AND price IS NOT NULL
		  AND price > 0`,
		map[string]any{"run": runID, "no": attemptNo, "prid": providerRequestID})
	if err != nil {
		return false, fmt.Errorf("failed to check whether the charge of design run %d is already booked: %w",
			runID, err)
	}
	return n > 0, nil
}

// CompleteRun files the outputs and closes the run.
//
// ⚠ claim_token СТОИТ В WHERE ЗАКРЫВАЮЩЕГО UPDATE, а не только в проверке перед ним. Проверка
// даёт человеческий отказ; WHERE — страховка на понижение изоляции, и ровно в этом качестве, а
// не в том, что здесь было написано раньше. Прежний довод («между чтением и записью строку может
// перехватить другой воркер») ИЗМЕРЕН И НЕВЕРЕН: транзакция SERIALIZABLE держит строку под
// блокировкой с момента чтения, перехватить её в этом окне нельзя, и requireClaim отказывает
// раньше, чем дело дойдёт до WHERE. Подробный разбор и замер — в шапке файла.
//
// ЧАСТИЧНЫЙ ОТВЕТ — ЭТО МЕНЬШЕ КАРТИНОК И ВСЁ РАВНО `done`: строка истории скажет «done · 2 of 3»
// по requested_outputs. Вставка идемпотентна по uq_design_picture_run_ordinal, поэтому повтор
// после потерянного ответа не заводит второй набор кадров.
func (s *Store) CompleteRun(ctx context.Context, req entity.DesignRunComplete) (*entity.DesignRun, error) {
	if req.RunId <= 0 {
		return nil, fmt.Errorf("%w: run id is required", entity.ErrDesignInvalidArgument)
	}
	if req.ClaimToken == "" {
		return nil, fmt.Errorf("%w: a result without a claim token cannot be attributed",
			entity.ErrDesignInvalidArgument)
	}
	seen := map[int]struct{}{}
	for i, o := range req.Outputs {
		if o.MediaId <= 0 {
			return nil, fmt.Errorf("%w: output %d has no media", entity.ErrDesignInvalidArgument, i)
		}
		if o.Ordinal < 0 {
			return nil, fmt.Errorf("%w: output %d has a negative ordinal", entity.ErrDesignInvalidArgument, i)
		}
		if _, dup := seen[o.Ordinal]; dup {
			// Одинаковые ординалы схлопнулись бы в ОДНУ строку на uq_design_picture_run_ordinal,
			// и половина оплаченной выдачи исчезла бы молча.
			return nil, fmt.Errorf("%w: two outputs share ordinal %d", entity.ErrDesignInvalidArgument, o.Ordinal)
		}
		seen[o.Ordinal] = struct{}{}
		if o.GhostView != "" && !entity.IsDesignGhostView(o.GhostView) {
			return nil, fmt.Errorf("%w: unknown ghost_view %q on output %d",
				entity.ErrDesignInvalidArgument, o.GhostView, i)
		}
		if o.Kind != "" && !entity.IsDesignPictureKind(o.Kind) {
			return nil, fmt.Errorf("%w: unknown picture kind %q on output %d",
				entity.ErrDesignInvalidArgument, o.Kind, i)
		}
	}

	var out entity.DesignRun
	err := s.txFunc(ctx, func(ctx context.Context, rep dependency.Repository) error {
		db := rep.DB()
		run, err := runByID(ctx, db, req.RunId)
		if err != nil {
			return err
		}
		// ⚠ ПОРЯДОК ДВУХ ПРОВЕРОК — РЕШЕНИЕ, А НЕ СТИЛЬ.
		//
		// Закрытая строка отвечает СОСТАВОМ, и отвечает ВСЯКОМУ, кто пришёл, — даже опоздавшему
		// с чужим токеном. Здесь ничего не пишется, значит защищать нечего; а отказать было бы
		// прямым вредом: опоздавший воркер УЖЕ загрузил свои байты в бакет и узнаёт, что их никто
		// не усыновил, ровно из этого ответа (OrphanedMedia). Отказ оставил бы файлы в бакете
		// ничьими и публично адресуемыми — то есть сторож «защитил» бы строку ценой мусора,
		// который никто больше не найдёт.
		//
		// Сторож захвата стоит НИЖЕ и охраняет РОВНО ПИСЬМО: строку, которая ещё идёт и которую
		// ведёт кто-то другой.
		switch run.Status {
		case entity.DesignRunDone:
			// Повтор: кадры уже стоят под этой строкой, второй раз их вставлять нечего.
			out = run
			return attachRunPictures(ctx, db, &out)
		case entity.DesignRunFailed, entity.DesignRunCancelled:
			return fmt.Errorf("%w: design run %d is already %s", entity.ErrDesignRunTerminal, run.Id, run.Status)
		}
		if err := requireClaim(run, req.ClaimToken); err != nil {
			return err
		}

		// ─── MIXED_INPUT СЧИТАЕТСЯ В МОМЕНТ РОЖДЕНИЯ КАРТИНКИ ───
		//
		// Не при минте и не при чтении. Смесь — свойство ВХОДОВ, а входы после прилёта
		// меняются: слот переставили, референс удалили. Посчитанный позже флаг ответил бы про
		// сегодняшний верстак, а не про то, из чего кадр действительно собран, — и согласие
		// человека на смесь стало бы декоративным.
		mixed, err := runInputsAreMixed(ctx, db, run)
		if err != nil {
			return err
		}
		params := parseRunParams(run.Params)
		defaultKind := entity.DesignPictureKindOfRun(run.Kind)

		for _, o := range req.Outputs {
			kind := o.Kind
			if kind == "" {
				kind = defaultKind
			}
			source := o.SourceClass
			if source == "" {
				source = entity.DesignSourceAI
			}
			composite, err := compositeViewsOf(o, params)
			if err != nil {
				return err
			}
			if _, err := storeutil.ExecNamedLastId(ctx, db, `
				INSERT INTO design_picture
					(tech_card_id, media_id, run_id, ordinal, kind, ghost_view, composite_views,
					 source_class, mixed_input)
				VALUES (:card, :media, :run, :ord, :kind, :ghost, :composite, :src, :mixed)
				ON DUPLICATE KEY UPDATE id = LAST_INSERT_ID(id)`,
				map[string]any{
					"card": run.TechCardId, "media": o.MediaId, "run": run.Id, "ord": o.Ordinal,
					"kind": kind, "ghost": nullStr(ghostViewOf(o, params)),
					"composite": jsonOrNil(composite), "src": source,
					// Флаг = «смешаны ВХОДЫ прогона» ИЛИ «воркер уже знает, что смешаны».
					// Одно не отменяет другого: воркер видит то, чего нет в снимке (например,
					// подмешанный им же кадр), а стор видит то, чего не видит воркер.
					"mixed": mixed || o.MixedInput,
				}); err != nil {
				return fmt.Errorf("failed to file output %d of design run %d: %w", o.Ordinal, run.Id, err)
			}
		}

		// ─── ЗАКРЫТИЕ СТРОКИ: ТОКЕН В WHERE ───
		rows, err := storeutil.ExecNamedRows(ctx, db, `
			UPDATE design_run
			SET status = 'done',
			    completed_at = UTC_TIMESTAMP(6),
			    output_text = COALESCE(:text, output_text),
			    price_actual = (SELECT COALESCE(SUM(price), 0) FROM design_run_attempt WHERE run_id = :id),
			    claim_token = NULL,
			    claim_expires_at = NULL
			WHERE id = :id AND claim_token = :tok AND status IN ('pending', 'running')`,
			map[string]any{"id": run.Id, "tok": req.ClaimToken, "text": req.OutputText})
		if err != nil {
			return fmt.Errorf("failed to close design run %d: %w", run.Id, err)
		}
		if rows == 0 {
			// Строку перехватили ровно сейчас. Вся вставка кадров уезжает вместе с откатом —
			// именно этого мы и хотим: чужую выдачу мы не дополняем.
			return fmt.Errorf("%w: design run %d changed hands while its result was being filed",
				entity.ErrDesignClaimLost, run.Id)
		}
		if err := releaseRunReserve(ctx, db, run); err != nil {
			return err
		}
		out, err = runByID(ctx, db, run.Id)
		if err != nil {
			return err
		}
		return attachRunPictures(ctx, db, &out)
	})
	if err != nil {
		return nil, err
	}
	return &out, nil
}

// attachRunPictures отдаёт вызывающему СОСТАВ, КОТОРЫЙ СТРОКА ДЕЙСТВИТЕЛЬНО УСЫНОВИЛА.
//
// ⚠ ЭТО НЕ УДОБСТВО, А ПОЛОВИНА КОМПЕНСАЦИИ СИРОТ. Байты провайдера кладутся в бакет ДО
// транзакции, поэтому воркер обязан уметь спросить «что из загруженного мною приняли»
// (OrphanedMedia(minted, adopted)) и снести остальное. Случай, ради которого это необходимо, —
// именно err == nil: повтор, разрешившийся идемпотентно, возвращает кадры ПЕРВОГО ответа, и
// свежезагруженные файлы этого вызова не усыновил никто. Без списка усыновлённых они остались бы
// в бакете и в media навсегда, публично адресуемые и ничьи.
func attachRunPictures(ctx context.Context, db dependency.DB, run *entity.DesignRun) error {
	pics, err := loadPicturesByRuns(ctx, db, []int{run.Id})
	if err != nil {
		return err
	}
	run.Pictures = pics[run.Id]
	return nil
}

// FailRun records a failure: exponential retry or a terminal `failed`.
//
// claim_token — В WHERE, по тому же доводу, что и в CompleteRun, и с той же оговоркой: сегодня
// эту гонку закрывает уровень изоляции, а токен в WHERE держит её закрытой в день, когда уровень
// опустят. Утверждение остаётся верным — воркер с истёкшим захватом не вправе ни уронить, ни
// отложить чужое задание, — но несёт его не эта строка.
func (s *Store) FailRun(ctx context.Context, req entity.DesignRunFail) (*entity.DesignRun, error) {
	if req.RunId <= 0 {
		return nil, fmt.Errorf("%w: run id is required", entity.ErrDesignInvalidArgument)
	}
	if req.ClaimToken == "" {
		return nil, fmt.Errorf("%w: a failure without a claim token cannot be attributed",
			entity.ErrDesignInvalidArgument)
	}

	var out entity.DesignRun
	err := s.txFunc(ctx, func(ctx context.Context, rep dependency.Repository) error {
		db := rep.DB()
		run, err := runByID(ctx, db, req.RunId)
		if err != nil {
			return err
		}
		if err := requireClaim(run, req.ClaimToken); err != nil {
			return err
		}
		switch run.Status {
		case entity.DesignRunDone, entity.DesignRunFailed, entity.DesignRunCancelled:
			return fmt.Errorf("%w: design run %d is already %s", entity.ErrDesignRunTerminal, run.Id, run.Status)
		}

		// ОТМЕНА, ПРИШЕДШАЯ ПОКА ЗАДАНИЕ ШЛО, ЗАКРЫВАЕТ ЕГО ТЕРМИНАЛЬНО. Без этого ретрай снова
		// поставил бы в очередь задание, которое человек уже отменил, — и предикат захвата
		// (`cancel_requested_at IS NULL`) держал бы его в pending вечно.
		cancelled := run.CancelRequestedAt.Valid

		// ДВА ПОТОЛКА, И ОНИ СЧИТАЮТ РАЗНОЕ: paid — сколько раз мы ПЛАТИЛИ, attempt_count —
		// сколько раз БРАЛИСЬ ЗА РАБОТУ. Слить их в один значит либо убить оплаченную однажды
		// 3D-задачу на четвёртом бесплатном опросе (так и было), либо разрешить пятому платному
		// вызову уйти к провайдеру. Экспонента ретрая при этом остаётся на attempt_count: ждать
		// дольше заставляет ЧИСЛО КРУГОВ, а не число платежей.
		paid, err := paidAttempts(ctx, db, run.Id)
		if err != nil {
			return err
		}
		retry := req.Retryable && !cancelled &&
			paid < designMaxPaidAttempts &&
			run.AttemptCount < designMaxRounds
		next := req.NextAttempt
		if next.IsZero() {
			next = designNextAttemptAt(s.Now(), run.AttemptCount)
		}
		lastError := req.LastError
		if len(lastError) > designMaxErrorText {
			lastError = lastError[:designMaxErrorText]
		}

		var (
			rows int64
			args = map[string]any{
				"id": run.Id, "tok": req.ClaimToken,
				"code": nullStr(req.ErrorCode), "err": nullStr(lastError),
			}
		)
		if retry {
			args["next"] = next.UTC()
			rows, err = storeutil.ExecNamedRows(ctx, db, `
				UPDATE design_run
				SET status = 'pending',
				    attempt_count = attempt_count + 1,
				    next_attempt_at = :next,
				    error_code = :code,
				    last_error = :err,
				    claim_token = NULL,
				    claim_expires_at = NULL
				WHERE id = :id AND claim_token = :tok AND status IN ('pending', 'running')`, args)
		} else {
			args["status"] = entity.DesignRunFailed
			if cancelled {
				args["status"] = entity.DesignRunCancelled
			}
			rows, err = storeutil.ExecNamedRows(ctx, db, `
				UPDATE design_run
				SET status = :status,
				    attempt_count = attempt_count + 1,
				    completed_at = UTC_TIMESTAMP(6),
				    error_code = :code,
				    last_error = :err,
				    price_actual = (SELECT COALESCE(SUM(price), 0) FROM design_run_attempt WHERE run_id = :id),
				    claim_token = NULL,
				    claim_expires_at = NULL
				WHERE id = :id AND claim_token = :tok AND status IN ('pending', 'running')`, args)
		}
		if err != nil {
			return fmt.Errorf("failed to record the failure of design run %d: %w", run.Id, err)
		}
		if rows == 0 {
			return fmt.Errorf("%w: design run %d changed hands while its failure was being recorded",
				entity.ErrDesignClaimLost, run.Id)
		}
		if !retry {
			// Терминальный переход — единственное место, где резерв снимается, и сторож
			// `status IN ('pending','running')` делает его однократным по построению.
			if err := releaseRunReserve(ctx, db, run); err != nil {
				return err
			}
		}
		out, err = runByID(ctx, db, run.Id)
		return err
	})
	if err != nil {
		return nil, err
	}
	return &out, nil
}

// ─────────────────────────── общее для машины ───────────────────────────

// requireClaim — сверка захвата. Отдельная функция, чтобы отказ звучал одинаково у всех трёх
// глаголов и чтобы «а есть ли вообще токен у строки» не оказалось где-то забытым: строка БЕЗ
// токена (свободная либо подметённая) не принадлежит никому, и писать в неё нельзя тем более.
func requireClaim(run entity.DesignRun, token string) error {
	if !run.ClaimToken.Valid || run.ClaimToken.String != token {
		return fmt.Errorf("%w: design run %d is not held by this claim", entity.ErrDesignClaimLost, run.Id)
	}
	return nil
}

// designNextAttemptAt — экспонента возврата в очередь с потолком.
func designNextAttemptAt(now time.Time, attemptCount int) time.Time {
	if attemptCount < 0 {
		attemptCount = 0
	}
	if attemptCount > 16 {
		attemptCount = 16
	}
	d := designRetryBase << uint(attemptCount)
	if d <= 0 || d > designRetryMax {
		d = designRetryMax
	}
	return now.UTC().Add(d)
}

// syncRunPriceActual пересчитывает цену прогона как СУММУ цен попыток.
//
// СУММА, А НЕ ЦЕНА ПОСЛЕДНЕЙ: ретрай платит второй раз, и строка истории обязана это показывать.
func syncRunPriceActual(ctx context.Context, db dependency.DB, runID int) error {
	if err := storeutil.ExecNamed(ctx, db, `
		UPDATE design_run
		SET price_actual = (SELECT COALESCE(SUM(price), 0) FROM design_run_attempt WHERE run_id = :id)
		WHERE id = :id`, map[string]any{"id": runID}); err != nil {
		return fmt.Errorf("failed to sum the attempts of design run %d: %w", runID, err)
	}
	return nil
}

func attemptByNo(ctx context.Context, db dependency.DB, runID, no int) (entity.DesignRunAttempt, error) {
	rows, err := storeutil.QueryListNamed[entity.DesignRunAttempt](ctx, db,
		`SELECT * FROM design_run_attempt WHERE run_id = :run AND attempt_no = :no`,
		map[string]any{"run": runID, "no": no})
	if err != nil {
		return entity.DesignRunAttempt{}, fmt.Errorf("failed to read design attempt %d of run %d: %w", no, runID, err)
	}
	if len(rows) == 0 {
		return entity.DesignRunAttempt{}, fmt.Errorf("%w: attempt %d of design run %d",
			entity.ErrDesignNotFound, no, runID)
	}
	return rows[0], nil
}

// ─────────────────────────── провенанс и композит ───────────────────────────

// designInputProvenance — провенанс ОДНОГО входа прогона.
type designInputProvenance struct {
	SourceClass string
	MixedInput  bool
}

// designMixedInput — ВЕРДИКТ О СМЕСИ, отделённый от чтения базы, чтобы его можно было проверить
// без контейнера.
//
// ДВА ПРАВИЛА, И ВТОРОЕ ВАЖНЕЕ ПЕРВОГО:
//  1. разные провенансы среди входов = смесь;
//  2. ЛЮБОЙ смешанный вход = смесь. Смесь не отмывается ещё одной операцией — ровно тот же довод,
//     по которому кроп наследует mixed_input родителя (см. SplitPicture). Иначе достаточно было
//     бы прогнать смешанный кадр через ещё одну генерацию, чтобы согласие человека перестало
//     требоваться.
func designMixedInput(inputs []designInputProvenance) bool {
	classes := make(map[string]struct{}, len(inputs))
	for _, in := range inputs {
		if in.MixedInput {
			return true
		}
		class := in.SourceClass
		if class == "" {
			class = entity.DesignSourceUploaded
		}
		classes[class] = struct{}{}
	}
	return len(classes) > 1
}

// runInputsAreMixed резолвит провенанс каждого входа прогона и выносит вердикт.
//
// ВХОД, КОТОРОГО НЕТ СРЕДИ КАРТИНОК ПОЛОСЫ, СЧИТАЕТСЯ ЗАГРУЖЕННЫМ ЧЕЛОВЕКОМ, и это не догадка:
// референс — это файл, который человек принёс сам, строки design_picture у него нет по
// построению. Не учитывать такие входы вовсе значило бы, что правка ИИ-плиты человеческим
// референсом не смесь, — а это ровно тот случай, ради которого флаг заведён.
func runInputsAreMixed(ctx context.Context, db dependency.DB, run entity.DesignRun) (bool, error) {
	ids := runInputMediaIDs(run)
	if len(ids) == 0 {
		return false, nil
	}
	type row struct {
		MediaId     int    `db:"media_id"`
		SourceClass string `db:"source_class"`
		MixedInput  bool   `db:"mixed_input"`
	}
	rows, err := storeutil.QueryListNamed[row](ctx, db, `
		SELECT media_id, source_class, mixed_input FROM design_picture
		WHERE tech_card_id = :card AND media_id IN (:ids)`,
		map[string]any{"card": run.TechCardId, "ids": ids})
	if err != nil {
		return false, fmt.Errorf("failed to read the provenance of design run %d inputs: %w", run.Id, err)
	}
	byMedia := make(map[int][]designInputProvenance, len(rows))
	for _, r := range rows {
		byMedia[r.MediaId] = append(byMedia[r.MediaId],
			designInputProvenance{SourceClass: r.SourceClass, MixedInput: r.MixedInput})
	}
	inputs := make([]designInputProvenance, 0, len(ids))
	for _, id := range ids {
		if got, ok := byMedia[id]; ok {
			inputs = append(inputs, got...)
			continue
		}
		inputs = append(inputs, designInputProvenance{SourceClass: entity.DesignSourceUploaded})
	}
	return designMixedInput(inputs), nil
}

// compositeViewsOf — ЧТО ИМЕННО СКЛЕЕНО В ОДНОМ КАДРЕ, записанное ПРИ ПРИЛЁТЕ прогона.
//
// ПОЧЕМУ ЭТО ОБЯЗАНО ПИСАТЬСЯ ЗДЕСЬ. Колонка объявлена с 0340, но писателя у неё не было ни
// одного — а читателей двое, и оба МОЛЧА ошибаются на пустой колонке: isComposite() на клиенте
// всегда возвращает false (правило «композит нельзя положить в слот» не работает, и человек
// кладёт на сторону лист из трёх видов), а резак работает вслепую. Отказа при этом нет ни у
// одного из них — есть неверный лист.
//
// ЯВНО НАЗВАННОЕ ВОРКЕРОМ ПОБЕЖДАЕТ: он видел, что реально прислал провайдер. Догадка ниже —
// для случая, когда воркер молчит: layout=one с несколькими запрошенными видами и означает
// «все виды одной картинкой» (W-4 ③).
func compositeViewsOf(o entity.DesignPictureInsert, p designRunParams) (json.RawMessage, error) {
	if len(o.CompositeViews) > 0 {
		return o.CompositeViews, nil
	}
	if p.Layout != designLayoutOne || len(p.Views) < 2 {
		return nil, nil
	}
	raw, err := json.Marshal(p.Views)
	if err != nil {
		return nil, fmt.Errorf("failed to encode the composite views of a design output: %w", err)
	}
	return raw, nil
}

// ghostViewOf — догадка о виде для одиночного кадра: запрошенные виды раздаются выдаче ПО
// ПОРЯДКУ (10 §3.4). У композита догадки нет вовсе — он не один вид, а несколько, и подставить
// ему первый значило бы дать резаку неверную подсказку.
func ghostViewOf(o entity.DesignPictureInsert, p designRunParams) string {
	if o.GhostView != "" {
		return o.GhostView
	}
	if p.Layout == designLayoutOne && len(p.Views) > 1 {
		return ""
	}
	if o.Ordinal >= 0 && o.Ordinal < len(p.Views) && entity.IsDesignGhostView(p.Views[o.Ordinal]) {
		return p.Views[o.Ordinal]
	}
	return ""
}
