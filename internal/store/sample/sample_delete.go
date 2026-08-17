package sample

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"

	"github.com/go-sql-driver/mysql"
	"github.com/jekabolt/grbpwr-manager/internal/dependency"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/jekabolt/grbpwr-manager/internal/store/storeutil"
)

// УДАЛЕНИЕ СЕМПЛА. Довод, границу и три категории вердикта см. в internal/entity/sample_deletion.go
// — здесь только чтение фактов и сама транзакция.
//
// ДВА ВХОДА, ОДНО ПРАВИЛО. EvaluateSampleDeletion отвечает на вопрос, ничего не меняя (его читает
// диалог подтверждения), DeleteSample задаёт тот же вопрос ПОВТОРНО ВНУТРИ транзакции и удаляет.
// Оба зовут readSampleDeletionFacts + entity.ClassifySampleDeletion, поэтому расходиться они могут
// фактами, но не правилом. Ради расхождения фактов пере-проверка и существует: между сухим
// прогоном и подтверждением проходят секунды, и за них на семпл успевают списать ткань или
// записать примерку — тогда транзакция ОБЯЗАНА решить иначе, чем показал диалог. Предикат,
// доказанный вне транзакции, — это гонка, а эта гонка удаляет.
//
// ПОЧЕМУ ПОВТОРНОГО ЧТЕНИЯ ДОСТАТОЧНО — и это не самоочевидно. Само по себе «прочитать ещё раз»
// гонку не закрывает: под обычным снимком (REPEATABLE READ) транзакция не увидела бы списание,
// закоммиченное соседом ПОСЛЕ её первого чтения, и снесла бы семпл, оставив ткань числящейся ни на
// чём. Спасает уровень изоляции: пишущие транзакции стора идут в SERIALIZABLE (MYSQLStore.Tx), где
// InnoDB делает обычный SELECT блокирующим чтением с next-key локами по просканированному
// диапазону — то есть чтение фактов ЗАПИРАЕТ material_stock_movement и fitting по этому sample_id
// до конца транзакции, и соседнее списание либо уже видно здесь, либо ждёт и упирается во внешний
// ключ после удаления. Утверждение проверяется на настоящем MySQL, а не берётся на веру:
// TestSampleDeletionFactsLockOutConcurrentIssue (internal/store).

// EvaluateSampleDeletion — сухой прогон: тот же вердикт, ноль записей. sql.ErrNoRows, если семпла нет.
// Второе возвращаемое значение — статус «списан»: он не меняет вердикт, но меняет ВЫХОД из него
// (списанному семплу склад запрещает возврат материала), и API-слой кладёт его в совет.
func (s *Store) EvaluateSampleDeletion(ctx context.Context, id int) (*entity.SampleDeletionVerdict, bool, error) {
	facts, err := readSampleDeletionFacts(ctx, s.DB, id)
	if err != nil {
		return nil, false, err
	}
	v := entity.ClassifySampleDeletion(*facts)
	return &v, facts.Scrapped, nil
}

// DeleteSample удаляет семпл, если вердикт это разрешает.
//
// Возвращает вердикт ВСЕГДА, когда сумел его посчитать, — и на успехе, и вместе с
// entity.ErrSampleNotDeletable на отказе. На успехе он описывает то, что ТОЛЬКО ЧТО произошло:
// каскад посчитан ДО DELETE, потому что после него считать уже нечего.
//
// Оптимистической версии семпла (lock_version) здесь нет намеренно, по тому же доводу, что и у
// удаления колорвея: версию двигает правка ПОЛЕЙ семпла, а решают удаляемость факты, которые её не
// трогают вовсе (движение склада, примерка). Проверка версии не закрыла бы ни одной настоящей
// гонки, зато отказывала бы после безобидной правки заметки. Гонку закрывает пере-проверка фактов
// внутри транзакции.
func (s *Store) DeleteSample(ctx context.Context, id int) (*entity.SampleDeletionVerdict, bool, error) {
	var verdict *entity.SampleDeletionVerdict
	var scrapped bool
	err := s.txFunc(ctx, func(ctx context.Context, rep dependency.Repository) error {
		db := rep.DB()
		facts, err := readSampleDeletionFacts(ctx, db, id)
		if err != nil {
			return err
		}
		scrapped = facts.Scrapped
		v := entity.ClassifySampleDeletion(*facts)
		verdict = &v
		if !v.Deletable {
			return entity.ErrSampleNotDeletable
		}
		rows, err := storeutil.ExecNamedRows(ctx, db, `DELETE FROM sample WHERE id = :id`, map[string]any{"id": id})
		if err != nil {
			var me *mysql.MySQLError
			if errors.As(err, &me) && me.Number == 1451 { // ER_ROW_IS_REFERENCED_2
				// СЕТКА БЕЗОПАСНОСТИ ПРОТИВ УШЕДШЕЙ ВПЕРЁД СХЕМЫ, а не рабочий путь отказа: все
				// известные ссылки на семпл — SET NULL или CASCADE, поэтому сюда попадает только
				// FK с RESTRICT, заведённый после этой функции. Каждый такой случай — дефект
				// перечисления, и чинится он добавлением факта, а не текстом здесь. Отказ всё
				// равно читаемый, а не Internal: сырая ошибка MySQL в лице оператора — ровно тот
				// провал, ради устранения которого фича написана. Но запись ЧЕСТНО НЕ НАЗЫВАЕТ
				// факт: count = 0 значит «сколько именно, отсюда не видно» — MySQL сообщает имя
				// ограничения, а не мощность.
				slog.Default().ErrorContext(ctx, "sample delete hit an FK the deletion facts do not enumerate; add it to readSampleDeletionFacts",
					slog.Int("sample_id", id), slog.String("err", err.Error()))
				v.Deletable = false
				v.Blockers = append(v.Blockers, entity.SampleDeletionEntry{
					Reason: entity.SampleBlockerReferenced,
					Count:  0,
					Text:   "a record references it that this refusal can't name (the schema has changed)",
				})
				return entity.ErrSampleNotDeletable
			}
			return fmt.Errorf("can't delete sample %d: %w", id, err)
		}
		if rows == 0 {
			return sql.ErrNoRows
		}
		return nil
	})
	if err != nil {
		return verdict, scrapped, err
	}
	return verdict, scrapped, nil
}

// readSampleDeletionFacts снимает всё, что решает судьбу семпла, — и то, что решает вердикт, и то,
// что диалог обязан назвать. Читается и вне транзакции (сухой прогон), и внутри неё (удаление).
func readSampleDeletionFacts(ctx context.Context, db dependency.DB, id int) (*entity.SampleDeletionFacts, error) {
	head, err := storeutil.QueryNamedOne[struct {
		Number int    `db:"number"`
		Status string `db:"status"`
	}](ctx, db, `SELECT number, status FROM sample WHERE id = :id`, map[string]any{"id": id})
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, sql.ErrNoRows
		}
		return nil, fmt.Errorf("read sample %d for deletion: %w", id, err)
	}

	f := &entity.SampleDeletionFacts{
		SampleID: id,
		Label:    fmt.Sprintf("#%d", head.Number),
		Scrapped: head.Status == entity.SampleStatusScrapped,
	}

	// ЧИСТЫЙ расход по каждому материалу: выдано минус возвращено. Та же арифметика, по которой
	// склад ограничивает возврат (outstandingIssued в internal/store/inventory) — иначе вердикт
	// требовал бы вернуть больше, чем склад готов принять, и отказ стал бы невыполнимым.
	//
	// Материалы с нулевым остатком отсюда НЕ отфильтрованы: ноль — это не «ничего не было», а
	// «всё вернулось», и классификация должна видеть разницу. Строк на семпл единицы.
	materials, err := storeutil.QueryListNamed[entity.SampleOutstandingMaterial](ctx, db, `
		SELECT
			mv.material_id AS material_id,
			COALESCE(mt.name, '') AS name,
			COALESCE(mt.unit, '') AS unit,
			COALESCE(SUM(CASE mv.movement_type
				WHEN :issue  THEN mv.quantity
				WHEN :return THEN -mv.quantity ELSE 0 END), 0) AS qty,
			COALESCE(SUM(CASE
				WHEN mv.unit_cost_base IS NULL THEN 0
				WHEN mv.movement_type = :issue  THEN mv.quantity * mv.unit_cost_base
				WHEN mv.movement_type = :return THEN -mv.quantity * mv.unit_cost_base ELSE 0 END), 0) AS costed_value
		FROM material_stock_movement mv
		LEFT JOIN material mt ON mt.id = mv.material_id
		WHERE mv.sample_id = :id
		GROUP BY mv.material_id, mt.name, mt.unit
		ORDER BY mt.name, mv.material_id`, map[string]any{
		"id":     id,
		"issue":  string(entity.MaterialMovementIssueSample),
		"return": string(entity.MaterialMovementReturnSample),
	})
	if err != nil {
		return nil, fmt.Errorf("read sample %d material balance: %w", id, err)
	}
	f.Materials = materials

	counts := []struct {
		dst   *int
		query string
	}{
		// Блокер: примерки. Схема их пережила бы (SET NULL), но примерка без семпла — вердикт,
		// снятый ни с чего.
		{&f.Fittings, `SELECT COUNT(*) FROM fitting WHERE sample_id = :id`},
		// Каскад — собственность семпла.
		{&f.Cascade.Media, `SELECT COUNT(*) FROM sample_media WHERE sample_id = :id`},
		{&f.Cascade.Substitutions, `SELECT COUNT(*) FROM sample_substitution WHERE sample_id = :id`},
		// Сироты — переживут удаление и потеряют семпл. Движения считаем ВСЕ: к моменту, когда
		// вердикт разрешит удаление, они уже сошлись в ноль, но из ленты склада никуда не денутся.
		{&f.Orphans.MaterialMovements, `SELECT COUNT(*) FROM material_stock_movement WHERE sample_id = :id`},
		{&f.Orphans.DevExpenses, `SELECT COUNT(*) FROM tech_card_dev_expense WHERE sample_id = :id`},
		{&f.Orphans.Tasks, `SELECT COUNT(*) FROM task WHERE sample_id = :id`},
		{&f.Orphans.NextRounds, `SELECT COUNT(*) FROM sample WHERE previous_sample_id = :id`},
	}
	for _, c := range counts {
		n, err := storeutil.QueryCountNamed(ctx, db, c.query, map[string]any{"id": id})
		if err != nil {
			return nil, fmt.Errorf("read sample %d deletion facts: %w", id, err)
		}
		*c.dst = n
	}
	return f, nil
}
