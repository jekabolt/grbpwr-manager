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
