package techcard

import (
	"context"
	"fmt"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/jekabolt/grbpwr-manager/internal/store/storeutil"
)

// ЧТЕНИЕ КАТАЛОГА РАБОТ (миграция 0329).
//
// ТРИ ЗАПРОСА, А НЕ ОДИН JOIN, И ЭТО НЕ ЛЕНЬ. Работа несёт ДВА независимых списка — допустимые
// машинки и синонимы поиска, — и один JOIN дал бы их декартово произведение: у отстрочки пять
// машинок и семь синонимов, то есть 35 строк вместо 12, из которых читателю пришлось бы обратно
// доставать два множества. Таблицы крошечные (53 + 31 + 254 строки), три коротких SELECT'а дешевле
// разбора произведения и, главное, не могут его перепутать.
//
// КЭША ПРОЦЕССА НЕТ НАМЕРЕННО. Каталог меняется ТОЛЬКО миграцией, то есть только вместе с
// перезапуском процесса, — кэш с инвалидацией стерёг бы событие, которого не бывает. Дефолты
// (`operation_work_default`) наоборот пишутся в рантайме и читаются своим запросом на каждый вызов;
// таблица размером с десяток строк.
//
// RETIRED-ПУНКТЫ ОТДАЮТСЯ ВСЕГДА. Клиенту они нужны для ЧТЕНИЯ старых строк шага: снятая работа
// обязана открываться и печататься своим именем, а не сырым токеном. Прятать их — дело пикера, и
// решает это флаг `retired_at`, а не отсутствие строки в ответе.

// GetOperationWorkCatalog returns the whole work catalog — every work with its allowed machines and
// its RU/EN search synonyms, in picker order. Retired works are included and flagged.
func (s *Store) GetOperationWorkCatalog(ctx context.Context) ([]entity.OperationWork, error) {
	works, err := storeutil.QueryListNamed[entity.OperationWork](ctx, s.DB, `
		SELECT token, verb, stage, label, machine_mode, default_machine, sort, retired_at
		FROM operation_work
		ORDER BY sort`, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to read operation work catalog: %w", err)
	}
	if len(works) == 0 {
		return nil, nil
	}

	type machineRow struct {
		WorkToken   string `db:"work_token"`
		MachineType string `db:"machine_type"`
	}
	machines, err := storeutil.QueryListNamed[machineRow](ctx, s.DB, `
		SELECT work_token, machine_type
		FROM operation_work_machine
		ORDER BY work_token, machine_type`, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to read operation work machines: %w", err)
	}

	type synRow struct {
		WorkToken string `db:"work_token"`
		Syn       string `db:"syn"`
	}
	syns, err := storeutil.QueryListNamed[synRow](ctx, s.DB, `
		SELECT work_token, syn
		FROM operation_work_syn
		ORDER BY work_token, syn`, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to read operation work synonyms: %w", err)
	}

	// Индекс по указателю на элемент среза: работы уже разложены в нужном порядке, и приписывание
	// в них на месте не заводит второй копии каталога.
	byToken := make(map[string]*entity.OperationWork, len(works))
	for i := range works {
		byToken[works[i].Token] = &works[i]
	}
	for _, m := range machines {
		if w := byToken[m.WorkToken]; w != nil {
			w.Machines = append(w.Machines, m.MachineType)
		}
	}
	for _, sy := range syns {
		if w := byToken[sy.WorkToken]; w != nil {
			w.Syn = append(w.Syn, sy.Syn)
		}
	}
	return works, nil
}

// GetOperationWorkDefaults returns every stored global work-property default. Small by construction
// — one row per (work, field) a human explicitly chose to remember.
func (s *Store) GetOperationWorkDefaults(ctx context.Context) ([]entity.OperationWorkDefault, error) {
	rows, err := storeutil.QueryListNamed[entity.OperationWorkDefault](ctx, s.DB, `
		SELECT work_token, field, value, updated_at
		FROM operation_work_default
		ORDER BY work_token, field`, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to read operation work defaults: %w", err)
	}
	return rows, nil
}

// ── ЗАПИСЬ ДЕФОЛТОВ И ПОДСКАЗКИ НОРМЫ ВРЕМЕНИ (R3) ──────────────────────────────────────────────
//
// ЧТО ЗДЕСЬ НЕ ПРОВЕРЯЕТСЯ, И ЭТО ГРАНИЦА, А НЕ ЩЕЛЬ. Ни имя поля, ни его значение хранилище не
// судит: реестр «свойства работы — можно, машинные настройки — нельзя» и диапазоны/словари живут
// в entity (techcard_operation_work_default.go) и стоят на входе RPC, потому что отказ обязан
// назвать поле человеку, а не прилететь из базы номером ошибки. Хранилище отвечает за две вещи, за
// которые кроме него не отвечает никто: строка попадает в таблицу по ключу (work, field), и
// несуществующая работа упирается во внешний ключ.

// SetOperationWorkDefault stores (or overwrites) ONE global default of a work property.
//
// UPSERT, А НЕ DELETE+INSERT: пара (work_token, field) — первичный ключ, и «запомнить ещё раз»
// обязано быть переписыванием одной строки, а не сменой её идентичности. updated_at обновляется
// схемой (ON UPDATE CURRENT_TIMESTAMP), но ТОЛЬКО когда значение реально изменилось — MySQL не
// трогает строку, если UPDATE не меняет ни одного байта; для «когда это в последний раз
// подтверждали» этого достаточно, а точнее нам и не нужно.
func (s *Store) SetOperationWorkDefault(ctx context.Context, workToken, field, value string) error {
	err := storeutil.ExecNamed(ctx, s.DB, `
		INSERT INTO operation_work_default (work_token, field, value)
		VALUES (:work_token, :field, :value)
		ON DUPLICATE KEY UPDATE value = VALUES(value)`,
		map[string]any{"work_token": workToken, "field": field, "value": value})
	if err != nil {
		return fmt.Errorf("failed to remember operation work default: %w", err)
	}
	return nil
}

// DeleteOperationWorkDefault forgets one stored default. Absent is not an error: «забудь» на том,
// чего нет, — уже исполненное желание, и отказ здесь заставил бы клиента спрашивать состояние
// перед каждым жестом.
func (s *Store) DeleteOperationWorkDefault(ctx context.Context, workToken, field string) error {
	err := storeutil.ExecNamed(ctx, s.DB, `
		DELETE FROM operation_work_default WHERE work_token = :work_token AND field = :field`,
		map[string]any{"work_token": workToken, "field": field})
	if err != nil {
		return fmt.Errorf("failed to forget operation work default: %w", err)
	}
	return nil
}

// ListOperationWorkSmvSamples reads the stored steps that BOTH name a work and carry a time norm —
// the raw material of the «last time it was N» ghost.
//
// ⚠️ «ПОСЛЕДНИЙ» ВЫБИРАЕТСЯ НЕ ЗДЕСЬ. Запрос сужает и упорядочивает, а победителя на каждую работу
// назначает entity.LatestOperationWorkSmv — чистая функция с таблицей клеток. Причина простая:
// тесты этого пакета ходят в живую базу и запускаться не могут, поэтому единственное
// содержательное правило подсказки не должно жить в SQL.
//
// ORDER BY СУЩЕСТВУЕТ РАДИ LIMIT, А НЕ РАДИ ОТВЕТА: потолок обязан отрезать САМЫЕ СТАРЫЕ строки, а
// не случайные. Пропади сортировка — ответ остался бы верным (функция сравнивает сама), потерялась
// бы только предсказуемость того, что именно отброшено.
//
// ВОЗРАСТ СТРОКИ ШАГА — ЭТО ВОЗРАСТ КАРТОЧКИ. У tech_card_operation нет своего updated_at, и это
// не упущение: строки шагов переписываются ПОЛНОЙ ЗАМЕНОЙ на каждом сохранении, то есть все они
// ровно одного возраста — возраста tech_card.updated_at. Внутри одной карточки их различает только
// auto_increment id, он и едет вторым ключом сравнения.
func (s *Store) ListOperationWorkSmvSamples(ctx context.Context) ([]entity.OperationWorkSmvSample, error) {
	rows, err := storeutil.QueryListNamed[entity.OperationWorkSmvSample](ctx, s.DB, `
		SELECT o.work AS work_token, o.smv AS smv, o.id AS operation_id,
		       c.name AS card_name, c.updated_at AS card_updated_at
		FROM tech_card_operation o
		JOIN tech_card c ON c.id = o.tech_card_id
		WHERE o.work IS NOT NULL AND o.work <> '' AND o.smv IS NOT NULL AND o.smv > 0
		ORDER BY c.updated_at DESC, o.id DESC
		LIMIT :limit`, map[string]any{"limit": operationWorkSmvSampleLimit})
	if err != nil {
		return nil, fmt.Errorf("failed to read operation work smv samples: %w", err)
	}
	return rows, nil
}

// operationWorkSmvSampleLimit — потолок выборки подсказок. Число взято с запасом на два порядка от
// сегодняшнего мира (126 шагов на всём проде, из них с работой — ноль до ручной разметки) и стоит
// затем, чтобы читаемый на КАЖДЫЙ запрос каталога срез не мог однажды стать неограниченным.
const operationWorkSmvSampleLimit = 5000
