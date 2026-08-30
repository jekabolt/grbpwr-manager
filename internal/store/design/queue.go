package design

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

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
// ПОЧЕМУ ПРЕДИКАТ ПОВТОРЯЕТСЯ. SKIP LOCKED пропускает строки, ЗАБЛОКИРОВАННЫЕ ПРЯМО СЕЙЧАС, — он
// ничего не говорит о строке, которую другой воркер захватил и ОТПУСТИЛ (транзакция захвата
// коротка, а лиза живёт минутами). Без повтора предиката второй захват выиграл бы гонку у живого
// токена: первый воркер уходит звать провайдера, второй перезаписывает claim_token, и первый
// уже НИКОГДА не сможет закрыть свой прогон — CompleteRun сверяет токен. Деньги списаны, строка
// висит. Ровно этот дефект был в первой редакции плана, и он назван там вслух.
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

const (
	// designMaxAttempts — сколько раз одно задание вправе быть оплачено. Ретрай платит ВТОРОЙ
	// раз, поэтому потолок здесь — денежная величина, а не техническая.
	designMaxAttempts = 5
	// designRetryBase / designRetryMax — экспонента возврата в очередь.
	designRetryBase = 30 * time.Second
	designRetryMax  = 15 * time.Minute
)

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

// ReviveExpiredRuns returns runs whose lease expired to `pending`.
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

		// 1. ПОТОЛОК ПОПЫТОК ДЕЙСТВУЕТ И ЗДЕСЬ. Задание, чей воркер умирает раз за разом,
		// иначе воскресало бы вечно — и каждый круг мог бы стоить денег, потому что смерть
		// наступает ПОСЛЕ вызова провайдера так же часто, как до него. Строки, исчерпавшие
		// потолок, закрываются терминально и называют причину.
		expired, err := storeutil.QueryListNamed[entity.DesignRun](ctx, db, `
			SELECT * FROM design_run
			WHERE status = 'running'
			  AND claim_expires_at IS NOT NULL
			  AND claim_expires_at < UTC_TIMESTAMP(6)
			  AND attempt_count >= :cap
			ORDER BY id`, map[string]any{"cap": designMaxAttempts})
		if err != nil {
			return fmt.Errorf("failed to read design runs past the attempt cap: %w", err)
		}
		for _, run := range expired {
			rows, err := storeutil.ExecNamedRows(ctx, db, `
				UPDATE design_run
				SET status = 'failed',
				    error_code = COALESCE(error_code, 'lease_expired'),
				    completed_at = UTC_TIMESTAMP(6),
				    claim_token = NULL,
				    claim_expires_at = NULL
				WHERE id = :id AND status = 'running' AND attempt_count >= :cap`,
				map[string]any{"id": run.Id, "cap": designMaxAttempts})
			if err != nil {
				return fmt.Errorf("failed to close design run %d past the attempt cap: %w", run.Id, err)
			}
			if rows == 1 {
				if err := releaseRunReserve(ctx, db, run); err != nil {
					return err
				}
			}
		}

		// 2. ОСТАЛЬНЫЕ ВОЗВРАЩАЮТСЯ В ОЧЕРЕДЬ. next_attempt_at ставится в «сейчас»: истёкшая
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

// StartAttempt opens one paid provider call.
//
// ПОПЫТКА — СОБСТВЕННАЯ СТРОКА, и оплаченный провал тоже строка: полоса бюджета обязана ВИДЕТЬ,
// что ретрай заплатил второй раз. Идемпотентна по uq_design_run_attempt (run_id, attempt_no):
// повтор после потерянного ответа не заводит вторую попытку.
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
				"state": req.State, "price": req.Price, "code": nullStr(req.ErrorCode),
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
		if !req.Price.Valid || !req.Price.Decimal.IsPositive() {
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
			DesignBudgetDayKey(s.Now(), set.BudgetTimezone), decimal.Zero, req.Price.Decimal, currency)
	})
}

// CompleteRun files the outputs and closes the run.
//
// ⚠ claim_token СТОИТ В WHERE ЗАКРЫВАЮЩЕГО UPDATE, а не только в проверке перед ним. Проверка
// даёт человеческий отказ; WHERE даёт ГАРАНТИЮ: между чтением и записью строку может перехватить
// другой воркер, и без токена в WHERE опоздавший затёр бы результат перехватившего — обе
// стороны при этом считали бы, что всё прошло успешно.
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
// claim_token — В WHERE, по тому же доводу, что и в CompleteRun: воркер с истёкшим захватом не
// вправе ни уронить, ни отложить задание, которое уже ведёт другой.
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
		retry := req.Retryable && !cancelled && run.AttemptCount+1 < designMaxAttempts
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
