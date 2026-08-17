package product

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

// ФИЗИЧЕСКОЕ УДАЛЕНИЕ КОЛОРВЕЯ. Довод, границу и три категории вердикта см. в
// internal/entity/colorway_deletion.go — здесь только чтение фактов и сама транзакция.
//
// ДВА ВХОДА, ОДНО ПРАВИЛО. EvaluateColorwayDeletion отвечает на вопрос, ничего не меняя (его читает
// диалог подтверждения), DeleteColorway задаёт тот же вопрос ПОВТОРНО ВНУТРИ транзакции и удаляет.
// Оба зовут readColorwayDeletionFacts + entity.ClassifyColorwayDeletion, поэтому расходиться они
// могут только фактами, но не правилом. Именно расхождения фактов ради пере-проверка и существует:
// между сухим прогоном и подтверждением оператора проходят секунды, и за них колорвей успевает
// быть проданным или попасть в состав партии — тогда транзакция ОБЯЗАНА решить иначе, чем показал
// диалог. Предикат, доказанный вне транзакции, — это гонка, а эта гонка удаляет.

// EvaluateColorwayDeletion — сухой прогон: тот же вердикт, ноль записей. sql.ErrNoRows, если
// колорвея нет.
func (s *Store) EvaluateColorwayDeletion(ctx context.Context, colorwayID int) (*entity.ColorwayDeletionVerdict, error) {
	facts, err := readColorwayDeletionFacts(ctx, s.DB, colorwayID)
	if err != nil {
		return nil, err
	}
	v := entity.ClassifyColorwayDeletion(*facts)
	return &v, nil
}

// DeleteColorway физически удаляет колорвей, если вердикт это разрешает.
//
// Возвращает вердикт ВСЕГДА, когда сумел его посчитать, — и на успехе, и вместе с
// entity.ErrColorwayNotDeletable на отказе. На успехе он описывает то, что ТОЛЬКО ЧТО произошло:
// каскад посчитан ДО DELETE, потому что после него считать уже нечего.
//
// ОПТИМИСТИЧЕСКАЯ ВЕРСИЯ СЮДА НЕ ПРИХОДИТ — намеренно, по прецеденту ArchiveColorwayByID (см.
// комментарий у RPC в admin.proto и обработчик). Довод не «так исторически»: версия колорвея — это
// tech_card.lock_version, и НИ ОДИН из фактов, которые здесь решают (продажа, строка партии,
// настил, остаток), её не двигает. Проверка версии не закрыла бы ни одной настоящей гонки и при
// этом отказывала бы на правке рецепта СОСЕДНЕГО колорвея той же карточки. Гонку закрывает
// пере-проверка фактов в транзакции, а не число.
func (s *Store) DeleteColorway(ctx context.Context, colorwayID int) (*entity.ColorwayDeletionVerdict, error) {
	var verdict *entity.ColorwayDeletionVerdict
	err := s.txFunc(ctx, func(ctx context.Context, rep dependency.Repository) error {
		db := rep.DB()
		facts, err := readColorwayDeletionFacts(ctx, db, colorwayID)
		if err != nil {
			return err
		}
		v := entity.ClassifyColorwayDeletion(*facts)
		verdict = &v
		if !v.Deletable {
			return entity.ErrColorwayNotDeletable
		}
		// Владеющий стиль читаем ДО удаления: после него у строки не спросишь, чей список
		// колорвеев только что изменился.
		style, err := storeutil.QueryNamedOne[struct {
			StyleID sql.NullInt32 `db:"style_id"`
		}](ctx, db, `SELECT style_id FROM product WHERE id = :id`, map[string]any{"id": colorwayID})
		if err != nil {
			return fmt.Errorf("load owning style of colourway %d before delete: %w", colorwayID, err)
		}
		rows, err := storeutil.ExecNamedRows(ctx, db,
			`DELETE FROM product WHERE id = :id`, map[string]any{"id": colorwayID})
		if err != nil {
			var me *mysql.MySQLError
			if errors.As(err, &me) && me.Number == 1451 { // ER_ROW_IS_REFERENCED_2
				// СЕТКА БЕЗОПАСНОСТИ ПРОТИВ УШЕДШЕЙ ВПЕРЁД СХЕМЫ, а не рабочий путь отказа. Сюда
				// попадает только RESTRICT, которого нет в перечислении фактов выше, — то есть FK,
				// заведённый после этой функции. Каждый ТАКОЙ случай — дефект перечисления, и
				// чинится он добавлением факта, а не текстом здесь.
				//
				// Отказ всё равно читаемый, а не Internal (прецедент DeleteTechCard): сырая ошибка
				// MySQL в лице оператора — ровно тот провал, ради устранения которого фича
				// написана. Но эта запись ЧЕСТНО НЕ НАЗЫВАЕТ факт — она называет своё незнание, и
				// count = 0 значит «сколько именно, отсюда не видно». Поставить сюда 1 значило бы
				// выдумать число: MySQL сообщает имя ограничения, а не мощность.
				//
				// Вердикт переписывается на неудаляемый: вернуть deletable = true рядом с отказом
				// значило бы соврать вызывающему в ту же секунду.
				slog.Default().ErrorContext(ctx, "colourway delete hit an FK the deletion facts do not enumerate; add it to readColorwayDeletionFacts",
					slog.Int("colorway_id", colorwayID), slog.String("err", err.Error()))
				v.Deletable = false
				v.Blockers = append(v.Blockers, entity.ColorwayDeletionEntry{
					Reason: entity.ColorwayBlockerReferenced,
					Count:  0,
					Text:   "a record references it that this refusal can't name (the schema has changed)",
				})
				return entity.ErrColorwayNotDeletable
			}
			return fmt.Errorf("delete colourway %d: %w", colorwayID, err)
		}
		if rows == 0 {
			return sql.ErrNoRows
		}
		// БАМП ВЕРСИИ КАРТОЧКИ. Удаление меняет СОДЕРЖИМОЕ карточки, а не соседнее измерение:
		// из её списка колорвеев уходит строка, а вместе с колорвеем каскадом уходят его строки
		// рецепта (tech_card_colorway_usage) и привязки тканей к деталям кроя
		// (tech_card_piece_material) — то есть костинг карточки после этого читается иначе. Это
		// ровно тот критерий, по которому bumpTechCardLockForNorm бампает на смене нормы и НЕ
		// бампает на обычной раскладке. Клиент, державший карточку открытой, не затрёт своим
		// устаревшим списком колорвеев то, что здесь произошло: следующее оптимистичное сохранение
		// УЦЕЛЕВШЕГО колорвея изменяемой карточки упрётся в ErrTechCardConflict (Aborted → 409).
		// Дальше этого утверждение не идёт: сохранение на утверждённой карточке отказывает раньше
		// и по другой причине (RequireMutableTechCard → FailedPrecondition), а сохранение САМОГО
		// удалённого колорвея — это NotFound, а не конфликт версий.
		//
		// Бамп безусловный: ни RequireMutableTechCard, ни предиката по ожидаемой версии — оба
		// были бы НОВЫМ отказом на пути, который их не имел (утверждённая карточка вполне может
		// нести брошенный черновой колорвей, и запрет его вычистить — не то, что решал владелец).
		if style.StyleID.Valid {
			if err := storeutil.ExecNamed(ctx, db, `
				UPDATE tech_card SET lock_version = lock_version + 1, updated_at = NOW()
				WHERE id = :id`, map[string]any{"id": int(style.StyleID.Int32)}); err != nil {
				return fmt.Errorf("bump tech card %d lock after colourway %d delete: %w",
					int(style.StyleID.Int32), colorwayID, err)
			}
		}
		return nil
	})
	if err != nil {
		return verdict, err
	}
	return verdict, nil
}

// colorwayDeletionFactsRow — плоский снимок под один запрос. Отдельный тип от
// entity.ColorwayDeletionFacts: там доменная форма, здесь строки sqlx.
type colorwayDeletionFactsRow struct {
	Label string `db:"label"`

	Orders           int `db:"orders"`
	OrderLines       int `db:"order_lines"`
	StockUnits       int `db:"stock_units"`
	InventoryTargets int `db:"inventory_targets"`
	Fittings         int `db:"fittings"`

	Variants         int `db:"variants"`
	VariantPrices    int `db:"variant_prices"`
	Prices           int `db:"prices"`
	Media            int `db:"media"`
	Tags             int `db:"tags"`
	Translations     int `db:"translations"`
	RecipeUsages     int `db:"recipe_usages"`
	SizeConsumptions int `db:"size_consumptions"`
	PieceMaterials   int `db:"piece_materials"`
	PackagingRecipes int `db:"packaging_recipes"`
	LabDipRounds     int `db:"lab_dip_rounds"`
	CostEvents       int `db:"cost_events"`
	Waitlist         int `db:"waitlist"`
	StockHistory     int `db:"stock_history"`
	StyleLinks       int `db:"style_links"`

	Markers           int `db:"markers"`
	MaterialMovements int `db:"material_movements"`
	Samples           int `db:"samples"`
	Tasks             int `db:"tasks"`
}

// readColorwayDeletionFacts собирает всё, что решает судьбу колорвея, ОДНИМ запросом плюс два
// списочных (партии и настилы — их надо назвать поимённо, счётчика мало).
//
// ПРОДАЖА ЧИТАЕТСЯ ПО ОБЕИМ ССЫЛКАМ. Строка заказа адресует колорвей дважды: order_item.product_id
// (с 0001) и order_item.variant_id → product_size (канонический ключ с 0153, пара product_id+size_id
// оставлена как денормализованное удобство чтения). Они обязаны совпадать, но предикат «никогда не
// продавался» — это то место, где «обязаны» не годится: считаем объединение.
//
// CAST(... AS SIGNED) на остатке не украшение. SUM() по INT-колонке возвращает DECIMAL, драйвер
// отдаёт его строкой, и дробная форма («12.0000») не разобралась бы в int уже в рантайме — то есть
// остаток перестал бы держать удаление ровно там, где он единственный, кто его держит.
//
// Пояснения живут в Go-комментариях, а не внутри SQL: двоеточие в '--' комментарии ломает
// именованную привязку sqlx («could not find name in map»).
func readColorwayDeletionFacts(ctx context.Context, db dependency.DB, colorwayID int) (*entity.ColorwayDeletionFacts, error) {
	row, err := storeutil.QueryNamedOne[colorwayDeletionFactsRow](ctx, db, `
		SELECT
			COALESCE(NULLIF(p.sku, ''), CONCAT(COALESCE(p.color, ''), ' (', COALESCE(p.color_code, ''), ')')) AS label,
			(SELECT COUNT(DISTINCT oi.order_id) FROM order_item oi
				WHERE oi.product_id = p.id
				   OR oi.variant_id IN (SELECT ps.id FROM product_size ps WHERE ps.product_id = p.id)) AS orders,
			(SELECT COUNT(*) FROM order_item oi
				WHERE oi.product_id = p.id
				   OR oi.variant_id IN (SELECT ps.id FROM product_size ps WHERE ps.product_id = p.id)) AS order_lines,
			(SELECT CAST(COALESCE(SUM(ps.quantity), 0) AS SIGNED) FROM product_size ps WHERE ps.product_id = p.id) AS stock_units,
			(SELECT COUNT(*) FROM inventory_target it WHERE it.product_id = p.id) AS inventory_targets,
			(SELECT COUNT(*) FROM fitting f WHERE f.product_id = p.id) AS fittings,

			(SELECT COUNT(*) FROM product_size ps WHERE ps.product_id = p.id) AS variants,
			(SELECT COUNT(*) FROM product_size_price pp
				JOIN product_size ps ON ps.id = pp.product_size_id WHERE ps.product_id = p.id) AS variant_prices,
			(SELECT COUNT(*) FROM product_price pr WHERE pr.product_id = p.id) AS prices,
			(SELECT COUNT(*) FROM product_media pm WHERE pm.product_id = p.id) AS media,
			(SELECT COUNT(*) FROM product_tag pt WHERE pt.product_id = p.id) AS tags,
			(SELECT COUNT(*) FROM product_translation pt WHERE pt.product_id = p.id) AS translations,
			(SELECT COUNT(*) FROM tech_card_colorway_usage u WHERE u.colorway_id = p.id) AS recipe_usages,
			(SELECT COUNT(*) FROM tech_card_colorway_usage_consumption sc
				JOIN tech_card_colorway_usage u ON u.id = sc.usage_id WHERE u.colorway_id = p.id) AS size_consumptions,
			(SELECT COUNT(*) FROM tech_card_piece_material pm WHERE pm.colorway_id = p.id) AS piece_materials,
			(SELECT COUNT(*) FROM packaging_recipe pr WHERE pr.product_id = p.id) AS packaging_recipes,
			(SELECT COUNT(*) FROM product_lab_dip_round r WHERE r.product_id = p.id) AS lab_dip_rounds,
			(SELECT COUNT(*) FROM product_cost_event e WHERE e.product_id = p.id) AS cost_events,
			(SELECT COUNT(*) FROM product_waitlist w WHERE w.product_id = p.id) AS waitlist,
			(SELECT COUNT(*) FROM product_stock_change_history h WHERE h.product_id = p.id) AS stock_history,
			(SELECT COUNT(*) FROM tech_card_product tp WHERE tp.product_id = p.id) AS style_links,

			(SELECT COUNT(*) FROM tech_card_marker m WHERE m.colorway_id = p.id) AS markers,
			(SELECT COUNT(*) FROM material_stock_movement msm WHERE msm.product_id = p.id) AS material_movements,
			(SELECT COUNT(*) FROM sample sm WHERE sm.colorway_id = p.id) AS samples,
			(SELECT COUNT(*) FROM task t WHERE t.product_id = p.id) AS tasks
		FROM product p
		WHERE p.id = :id`, map[string]any{"id": colorwayID})
	if err != nil {
		// sql.ErrNoRows здесь означает «колорвея нет», и она обязана дойти до вызывающего в этом
		// виде: API-слой отображает её в NotFound.
		if errors.Is(err, sql.ErrNoRows) {
			return nil, err
		}
		return nil, fmt.Errorf("read deletion facts of colourway %d: %w", colorwayID, err)
	}

	// ЛЮБАЯ партия, включая ЧЕРНОВУЮ. Статус в фильтр не входит вовсе: граница владельца — «не
	// ссылается ни одна партия», а черновик держит намеренно (оператор убирает колорвей из состава
	// сам — снести чужие плановые строки за него мы права не имеем).
	runs, err := storeutil.QueryListNamed[entity.ColorwayRunRef](ctx, db, `
		SELECT DISTINCT r.id AS id, r.status AS status
		FROM production_run_line l
		JOIN production_run r ON r.id = l.run_id
		WHERE l.product_id = :id
		ORDER BY r.id`, map[string]any{"id": colorwayID})
	if err != nil {
		return nil, fmt.Errorf("read production runs of colourway %d: %w", colorwayID, err)
	}

	lays, err := storeutil.QueryListNamed[entity.ColorwayLayRef](ctx, db, `
		SELECT ly.id AS id, ly.run_id AS run_id, ly.name AS name
		FROM production_run_lay ly
		WHERE ly.colorway_id = :id
		ORDER BY ly.id`, map[string]any{"id": colorwayID})
	if err != nil {
		return nil, fmt.Errorf("read lays of colourway %d: %w", colorwayID, err)
	}

	return &entity.ColorwayDeletionFacts{
		ColorwayID:       colorwayID,
		Label:            row.Label,
		Orders:           row.Orders,
		OrderLines:       row.OrderLines,
		StockUnits:       row.StockUnits,
		Runs:             runs,
		Lays:             lays,
		InventoryTargets: row.InventoryTargets,
		Fittings:         row.Fittings,
		Cascade: entity.ColorwayCascadeCounts{
			Variants:         row.Variants,
			VariantPrices:    row.VariantPrices,
			Prices:           row.Prices,
			Media:            row.Media,
			Tags:             row.Tags,
			Translations:     row.Translations,
			RecipeUsages:     row.RecipeUsages,
			SizeConsumptions: row.SizeConsumptions,
			PieceMaterials:   row.PieceMaterials,
			PackagingRecipes: row.PackagingRecipes,
			LabDipRounds:     row.LabDipRounds,
			CostEvents:       row.CostEvents,
			Waitlist:         row.Waitlist,
			StockHistory:     row.StockHistory,
			StyleLinks:       row.StyleLinks,
		},
		Orphans: entity.ColorwayOrphanCounts{
			Markers:           row.Markers,
			MaterialMovements: row.MaterialMovements,
			Samples:           row.Samples,
			Tasks:             row.Tasks,
		},
	}, nil
}
