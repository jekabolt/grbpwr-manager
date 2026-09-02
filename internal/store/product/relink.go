package product

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/jekabolt/grbpwr-manager/internal/dependency"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/jekabolt/grbpwr-manager/internal/store/storeutil"
)

// detachRelinkedColorwayReferences keeps the recipe slots but removes their identities against the
// source style. Piece-material mappings belong to source-card pieces and cannot follow the colourway.
//
// ШТАМП НОРМЫ (norm_marker_id, 0291) — ТАКАЯ ЖЕ ПРИВЯЗКА К ИСХОДНОМУ СТИЛЮ, ЧТО И ТРИ СОСЕДНИЕ, и
// снимается по той же причине: раскладки принадлежат КАРТОЧКЕ (tech_card_marker.tech_card_id), а
// колорвей уезжает на другую. Оставить id значило бы, что строка рецепта продолжает называть
// источником своей нормы раскладку, которой на этой карточке нет и не будет.
//
// И это не только про аккуратность аудита: UpdateColorwayRecipe проверяет ЯВНО присланный штамп на
// принадлежность карточке (marker_not_on_card), а сегодняшний клиент перечитывает хранимый штамп и
// шлёт его обратно ЯВНО на каждом полном перезаписывании рецепта. То есть неснятый штамп сделал бы
// рецепт перепривязанного колорвея НЕСОХРАНЯЕМЫМ — до тех пор, пока человек не догадается
// демотировать норму в ручную. Отказ был бы правдой о данных, но эти данные создал бы сам перенос.
//
// Само ЧИСЛО и его источник (consumption_source = 'marker') остаются: длина снята с ткани и
// по-прежнему содержит межлекальные выпады, поэтому гросс-ап процентом раскроя к ней применять
// по-прежнему нельзя. Устаревает не измерение, а ссылка на него — её и убираем. Дата применения
// уходит вместе с id: отметка «применено тогда-то» без раскладки не отвечает ни на один вопрос
// (тот же довод, что в usageProvenance.normalized()).
func detachRelinkedColorwayReferences(ctx context.Context, db dependency.DB, colorwayID int) error {
	if err := storeutil.ExecNamed(ctx, db, `
		UPDATE tech_card_colorway_usage
		SET bom_item_id = NULL, piece_id = NULL, bom_item_index = NULL, piece_index = NULL,
		    norm_marker_id = NULL, norm_applied_at = NULL
		WHERE colorway_id = :id`, map[string]any{"id": colorwayID}); err != nil {
		return fmt.Errorf("detach recipe references for colourway %d: %w", colorwayID, err)
	}
	if err := storeutil.ExecNamed(ctx, db, `
		DELETE FROM tech_card_piece_material WHERE colorway_id = :id`, map[string]any{"id": colorwayID}); err != nil {
		return fmt.Errorf("delete source piece-material mappings for colourway %d: %w", colorwayID, err)
	}
	return nil
}

// designColorwayHolders — КАЖДАЯ ТАБЛИЦА ПОЛОСЫ, ССЫЛАЮЩАЯСЯ НА КОЛОРВЕЙ, ОДНИМ СПИСКОМ.
//
// ⚠ СПИСОК, А НЕ ЛИТЕРАЛ ВНУТРИ ЦИКЛА, ПОТОМУ ЧТО ОН УЖЕ ОТСТАВАЛ ОДИН РАЗ. 0356 завела три
// ссылки, 0357 — четвёртую (design_asset.colorway_id), и сторож перепривязки о ней не узнал:
// ассет, назначенный тканью колорвея N, уезжал вместе с N на чужой стиль и оставался на исходной
// карточке, называя колорвей, который ей больше не принадлежит. Хуже: кража в SetAssetColorway
// скоупится КАРТОЧКОЙ, поэтому назначение ассета целевой карточки тому же N не сняло бы старый —
// и один колорвей носили бы две ткани на двух карточках, ровно то состояние, которое 0357
// объявляет невыразимым.
//
// Пятую ссылку этот список тоже не заметит сам — его стережёт проба
// TestDesignDBRelinkGuardCoversEveryColorwayHolder, которая читает FK из information_schema и
// сверяет с ним ЭТОТ список. Список — единственное место, где перечисление живёт; проба — то, что
// заставляет его быть полным.
var designColorwayHolders = []struct {
	table, column, what string
	// countedInDeletionFacts — НАЗЫВАЕТ ЛИ ЭТУ ТАБЛИЦУ ВЕРДИКТ УДАЛЕНИЯ (readColorwayDeletionFacts).
	//
	// ⚠ ФЛАГ, А НЕ МОЛЧАНИЕ, ПОТОМУ ЧТО ПЕРЕЧИСЛЕНИЙ ОДНОГО ФАКТА БЫЛО ДВА (T3). Этот список
	// стерёг ТОЛЬКО перепривязку; вердикт удаления пересчитывал те же таблицы РУКАМИ в SQL, и
	// схемная проба на него не смотрела. То есть будущая 0358 покраснила бы пробу, кто-то дописал
	// бы таблицу СЮДА, проба позеленела бы — и диалог удаления продолжил бы молчать про новые
	// строки, а сетка 1451 их не поймает (SET NULL/CASCADE она не видит, это записано в шапке
	// 0357). Флаг делает вторую половину ВЫРАЗИМОЙ в том же месте, и проба сверяет обе.
	countedInDeletionFacts bool
}{
	{"design_run", "colorway_id", "generation run", true},
	{"design_picture", "colorway_id", "picture", true},
	{"design_bench_slot", "colorway_id", "bench slot", true},
	{"design_asset", "colorway_id", "shelf asset", true},
}

// DesignColorwayHolderColumns — пары (таблица, колонка) для схемной пробы.
//
// ⚠ ПАРА, А НЕ ИМЯ ТАБЛИЦЫ (T8): вторая колонка, ссылающаяся на product(id) на УЖЕ известной
// таблице полосы, при сверке по именам таблиц прошла бы незамеченной — множество имён совпало бы,
// а держатель остался бы вне сторожа.
func DesignColorwayHolderColumns() [][2]string {
	out := make([][2]string, 0, len(designColorwayHolders))
	for _, h := range designColorwayHolders {
		out = append(out, [2]string{h.table, h.column})
	}
	return out
}

// DesignColorwayDeletionCountedColumns — те держатели, которые вердикт удаления обязан посчитать
// и назвать оператору. Сегодня это ВСЕ; функция существует, чтобы «все» было УТВЕРЖДЕНИЕМ,
// проверяемым пробой, а не совпадением.
func DesignColorwayDeletionCountedColumns() [][2]string {
	out := make([][2]string, 0, len(designColorwayHolders))
	for _, h := range designColorwayHolders {
		if h.countedInDeletionFacts {
			out = append(out, [2]string{h.table, h.column})
		}
	}
	return out
}

// refuseRelinkWithDesignRows is the DESIGN-band half of the relink boundary (0356/0357): a
// colourway cannot leave its style while that style's design band still holds rows naming it.
//
// ─── ПОЧЕМУ ОТКАЗ, А НЕ ПЕРЕНОС ───
//
// Ось колорвея завела ЧЕТЫРЕ ссылки на product(id): design_run.colorway_id (для какого колорвея
// прогон), design_picture.colorway_id (чей кадр), design_bench_slot.colorway_id (чей верстак) —
// все три из 0356 — и design_asset.colorway_id (чья ткань, 0357). Все четыре висят на строках, у
// которых есть ВТОРОЙ владелец — tech_card_id ИСХОДНОЙ карточки. Перепривязка меняет product.style_id и не трогает ни одну из них, поэтому без
// сторожа карточка A остаётся с рядами, называющими колорвей, который теперь принадлежит B, а
// незакрытый прогон продолжает штамповать на A новые кадры чужого колорвея.
//
// Перенос (вариант «увезти строки вместе с колорвеем») отвергнут, и не по цене:
//   - ИСТОРИЯ — СВИДЕТЕЛЬСТВО, А НЕ ЗАЯВЛЕНИЕ. Рендер колорвея X сделан НА КАРТОЧКЕ A, из её
//     флэтов, её референсов и её описания изделия; замороженные params и inputs прогона называют
//     слоты, медиа и полки карточки A поимённо. Переписать у такой строки tech_card_id значит
//     заявить, что работа шла на B. Снимок при этом не переезжает и переехать не может: адреса в
//     нём — id верстака A. Ровно тот класс «правдоподобной, но ложной атрибуции», от которого
//     отказалась и сама 0356, не став бэкфиллить колорвей у старых рендеров.
//   - ДЕНЬГИ. design_run несёт price_estimate/price_actual и участвует в дневном бюджете; перенос
//     переписал бы, какая карточка что потратила.
//   - АДРЕСА СТОЛКНУЛИСЬ БЫ. Единственность слота — (tech_card_id, kind, exclusive_key), а ключ
//     несёт колорвей: `front@cw:5` на карточке B может быть уже занят. Перенос потребовал бы
//     политики слияния верстаков — то есть решения, которое некому принять, кроме человека.
//   - ГОНКА. Прогон в статусе pending/running закрывается воркером ВНЕ этой транзакции и минтует
//     кадры по своему run.tech_card_id. Перенос под живым прогоном не атомарен ни при каком
//     порядке операторов.
//
// Отказ же цену имеет и она известна: человек, начавший рисовать колорвей и решивший увезти его
// на другой стиль, должен сначала сам разобрать полосу (спрятать/удалить кадры, снять слоты). Это
// ручная работа, но она ЗРЯЧАЯ — а молчаливая порча полосы не чинится вовсе. Тот же довод, что у
// соседнего сторожа: перепривязывать разрешено только ЧЕРНОВИК, потому что у всего остального
// есть история, привязанная к стилю. Полоса DESIGN — тоже история.
//
// ⚠ ПРОВЕРКА В ТОЙ ЖЕ ТРАНЗАКЦИИ, ЧТО И САМ UPDATE (SERIALIZABLE), иначе это TOCTOU: прогон
// стартует между чтением и записью, и его строка родится уже сиротой.
func refuseRelinkWithDesignRows(ctx context.Context, db dependency.DB, colorwayID int) error {
	// Отдельный счёт на держателя, а не один UNION: имя таблицы уезжает в сообщение, и человеку
	// надо сказать, ЧТО именно держит колорвей — иначе «полоса держит» не подсказывает ни одного
	// следующего шага.
	for _, t := range designColorwayHolders {
		n, err := storeutil.QueryCountNamed(ctx, db,
			// #nosec G201 -- t.table and t.column are constants from the literal above, never caller input.
			"SELECT COUNT(*) FROM "+t.table+" WHERE "+t.column+" = :id",
			map[string]any{"id": colorwayID})
		if err != nil {
			return fmt.Errorf("check design %s rows of colourway %d: %w", t.what, colorwayID, err)
		}
		if n > 0 {
			return fmt.Errorf("%w: %d design %s row(s) of the source style name colourway %d; "+
				"clear the design band of that colourway first",
				entity.ErrColorwayHasDesignRows, n, t.what, colorwayID)
		}
	}
	return nil
}

// RelinkDraftColorway moves a DRAFT colourway onto a different style (R4 official workaround for the
// frozen-sibling problem: CloneStyleForSeason a style under a new season, then relink the draft rather
// than re-minting frozen siblings). Only a DRAFT may be relinked — an ACTIVE/HIDDEN/ARCHIVED colourway
// has (or had) a public identity with order/label history bound to its style, so it is frozen to it
// (entity.ErrColorwayNotDraft). Both sides are optimistically guarded on their shared
// tech_card.lock_version (entity.ErrTechCardConflict on a stale value or a concurrent relink), the
// target style must exist (sql.ErrNoRows otherwise), and the colourway's SKU is re-minted from the
// target style's facts (season/model) so its identity reflects its new style.
//
// A colourway that the SOURCE style's design band still names is refused outright
// (entity.ErrColorwayHasDesignRows) — see refuseRelinkWithDesignRows for why moving those rows is
// the wrong boundary.
func (s *Store) RelinkDraftColorway(ctx context.Context, colorwayID, targetStyleID, expectedColorwayVersion, expectedTargetStyleVersion int) error {
	return s.txFunc(ctx, func(ctx context.Context, rep dependency.Repository) error {
		cw, err := storeutil.QueryNamedOne[struct {
			StyleID int   `db:"style_id"`
			Status  uint8 `db:"lifecycle_status"`
		}](ctx, rep.DB(), `SELECT style_id, lifecycle_status FROM product WHERE id = :id`, map[string]any{"id": colorwayID})
		if err != nil {
			return err // sql.ErrNoRows -> NOT_FOUND upstream
		}
		if entity.ColorwayStatus(cw.Status) != entity.ColorwayStatusDraft {
			return fmt.Errorf("colourway %d: %w", colorwayID, entity.ErrColorwayNotDraft)
		}
		if cw.StyleID == targetStyleID {
			return fmt.Errorf("colourway %d already belongs to style %d", colorwayID, targetStyleID)
		}
		// The colourway's version is the shared lock of its CURRENT style.
		curLV, err := styleLockVersion(ctx, rep.DB(), cw.StyleID)
		if err != nil {
			return err
		}
		if curLV != expectedColorwayVersion {
			return entity.ErrTechCardConflict
		}
		tgtLV, err := styleLockVersion(ctx, rep.DB(), targetStyleID)
		if err != nil {
			if err == sql.ErrNoRows {
				return fmt.Errorf("target style %d not found: %w", targetStyleID, sql.ErrNoRows)
			}
			return err
		}
		if tgtLV != expectedTargetStyleVersion {
			return entity.ErrTechCardConflict
		}
		// ПОЛОСА DESIGN — ДО ЛЮБОЙ ЗАПИСИ. Сторож стоит выше detach'а намеренно: отказ обязан
		// оставить обе стороны нетронутыми, а detach уже снял бы штампы норм.
		if err := refuseRelinkWithDesignRows(ctx, rep.DB(), colorwayID); err != nil {
			return err
		}
		if err := detachRelinkedColorwayReferences(ctx, rep.DB(), colorwayID); err != nil {
			return err
		}
		// Relink under a source-membership + still-draft guard, so a concurrent relink/publish is rejected.
		rows, err := storeutil.ExecNamedRows(ctx, rep.DB(),
			`UPDATE product SET style_id = :target WHERE id = :id AND lifecycle_status = :draft AND style_id = :source`,
			map[string]any{"target": targetStyleID, "id": colorwayID, "draft": uint8(entity.ColorwayStatusDraft), "source": cw.StyleID})
		if err != nil {
			return fmt.Errorf("relink colourway %d to style %d: %w", colorwayID, targetStyleID, err)
		}
		if rows != 1 {
			return entity.ErrTechCardConflict
		}
		if err := moveColorwayOwnershipMirrors(ctx, rep.DB(), cw.StyleID, targetStyleID, colorwayID); err != nil {
			return err
		}
		// Re-mint the colourway's SKU from the target style's facts (a no-op if it is SKU-frozen — but a
		// draft never is). The base/variant SKUs now reflect the target season/model.
		if err := MintProductSKUs(ctx, rep.DB(), colorwayID); err != nil {
			return fmt.Errorf("re-mint colourway %d after relink: %w", colorwayID, err)
		}
		if err := bumpRelinkStyleVersions(ctx, rep.DB(), cw.StyleID, targetStyleID); err != nil {
			return err
		}
		return nil
	})
}

// styleLockVersion loads a style's shared optimistic-lock token (tech_card.lock_version); sql.ErrNoRows
// when the style is absent.
func styleLockVersion(ctx context.Context, db dependency.DB, styleID int) (int, error) {
	row, err := storeutil.QueryNamedOne[struct {
		LockVersion int `db:"lock_version"`
	}](ctx, db, `SELECT lock_version FROM tech_card WHERE id = :id`, map[string]any{"id": styleID})
	if err != nil {
		return 0, err
	}
	return row.LockVersion, nil
}

func bumpRelinkStyleVersions(ctx context.Context, db dependency.DB, sourceStyleID, targetStyleID int) error {
	rows, err := storeutil.ExecNamedRows(ctx, db, `
		UPDATE tech_card SET lock_version = lock_version + 1 WHERE id IN (:source, :target)`, map[string]any{
		"source": sourceStyleID,
		"target": targetStyleID,
	})
	if err != nil {
		return fmt.Errorf("bump source and target style versions after relink: %w", err)
	}
	if rows != 2 {
		return fmt.Errorf("bump source and target style versions after relink: %w", entity.ErrTechCardConflict)
	}
	return nil
}
