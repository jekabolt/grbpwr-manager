package techcard

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log/slog"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/jekabolt/grbpwr-manager/internal/store/storeutil"
)

type techCardSizeQtyRow struct {
	TechCardID int `db:"tech_card_id"`
	entity.TechCardSizeQuantity
}

// sizeQuantitiesByTechCardIds loads per-size order quantities (only sizes that
// have one) grouped by tech card.
func (s *Store) sizeQuantitiesByTechCardIds(ctx context.Context, ids []int) (map[int][]entity.TechCardSizeQuantity, error) {
	if len(ids) == 0 {
		return map[int][]entity.TechCardSizeQuantity{}, nil
	}
	rows, err := storeutil.QueryListNamed[techCardSizeQtyRow](ctx, s.DB, `
		SELECT tech_card_id, size_id, order_qty
		FROM tech_card_size
		WHERE tech_card_id IN (:ids) AND order_qty IS NOT NULL
		ORDER BY tech_card_id, display_order`, map[string]any{"ids": ids})
	if err != nil {
		return nil, fmt.Errorf("can't load tech card size quantities: %w", err)
	}
	out := make(map[int][]entity.TechCardSizeQuantity, len(ids))
	for _, r := range rows {
		out[r.TechCardID] = append(out[r.TechCardID], r.TechCardSizeQuantity)
	}
	return out, nil
}

// enrich loads and attaches the size range, linked products, sketch media
// (writable items + resolved MediaFull), callouts and revisions for each card.
func (s *Store) enrich(ctx context.Context, cards []entity.TechCard) error {
	if len(cards) == 0 {
		return nil
	}
	ids := make([]int, 0, len(cards))
	for _, c := range cards {
		ids = append(ids, c.Id)
	}

	sizes, err := s.idListByTechCardIds(ctx, "tech_card_size", "size_id", ids)
	if err != nil {
		return err
	}
	sizeQty, err := s.sizeQuantitiesByTechCardIds(ctx, ids)
	if err != nil {
		return err
	}
	mediaItems, mediaFull, err := s.mediaByTechCardIds(ctx, ids)
	if err != nil {
		return err
	}
	callouts, err := s.calloutsByTechCardIds(ctx, ids)
	if err != nil {
		return err
	}
	revisions, err := s.revisionsByTechCardIds(ctx, ids)
	if err != nil {
		return err
	}
	patterns, err := s.patternsByTechCardIds(ctx, ids)
	if err != nil {
		return err
	}

	for i := range cards {
		id := cards[i].Id
		cards[i].SizeIds = sizes[id]
		cards[i].SizeQuantities = sizeQty[id]
		cards[i].Media = mediaItems[id]
		cards[i].ResolvedMedia = mediaFull[id]
		cards[i].Callouts = callouts[id]
		cards[i].Revisions = revisions[id]
		cards[i].Patterns = patterns[id]
	}
	if err := s.enrichMaterials(ctx, cards); err != nil {
		return err
	}
	if err := s.enrichProduction(ctx, cards); err != nil {
		return err
	}
	// ПОСЛЕ производства, а не вместе с карточными медиа: id операционных снимков известны только
	// когда операции уже прочитаны. Резолвится одним запросом на всю пачку карточек.
	return s.enrichOperationMedia(ctx, cards)
}

// enrichOperationMedia разрешает media_id операционных снимков (0308) в полные записи медиа.
//
// Дистинкт по карточке: одна и та же фотография законно висит на нескольких шагах, а клиенту
// нужен словарь «id → откуда взять картинку», а не повторы. Отсутствующее медиа (удалено из
// библиотеки) просто не попадает в словарь — строка операции при этом уже ушла каскадом, так что
// такого быть не должно, но чтение не имеет права на этом падать.
func (s *Store) enrichOperationMedia(ctx context.Context, cards []entity.TechCard) error {
	wanted := make(map[int]bool)
	for i := range cards {
		for _, op := range cards[i].Operations {
			for _, m := range op.Media {
				wanted[m.MediaId] = true
			}
		}
	}
	if len(wanted) == 0 {
		return nil
	}
	ids := make([]int, 0, len(wanted))
	for id := range wanted {
		ids = append(ids, id)
	}
	rows, err := storeutil.QueryListNamed[entity.MediaFull](ctx, s.DB,
		`SELECT * FROM media WHERE id IN (:ids)`, map[string]any{"ids": ids})
	if err != nil {
		return fmt.Errorf("can't load operation media: %w", err)
	}
	byID := make(map[int]entity.MediaFull, len(rows))
	for i := range rows {
		byID[rows[i].Id] = rows[i]
	}
	for i := range cards {
		seen := make(map[int]bool)
		var out []entity.TechCardMediaFull
		for _, op := range cards[i].Operations {
			for _, m := range op.Media {
				if seen[m.MediaId] {
					continue
				}
				full, ok := byID[m.MediaId]
				if !ok {
					continue
				}
				seen[m.MediaId] = true
				out = append(out, entity.TechCardMediaFull{Media: full})
			}
		}
		cards[i].ResolvedOperationMedia = out
	}
	return nil
}

type techCardIDRow struct {
	TechCardID int `db:"tech_card_id"`
	Value      int `db:"value"`
}

// idListByTechCardIds loads a single int column (e.g. size_id, product_id) from a
// child table, grouped by tech_card_id and ordered by display_order.
func (s *Store) idListByTechCardIds(ctx context.Context, table, column string, ids []int) (map[int][]int, error) {
	if len(ids) == 0 {
		return map[int][]int{}, nil
	}
	rows, err := storeutil.QueryListNamed[techCardIDRow](ctx, s.DB, fmt.Sprintf(`
		SELECT tech_card_id, %s AS value
		FROM %s
		WHERE tech_card_id IN (:ids)
		ORDER BY tech_card_id, display_order`, column, table), map[string]any{"ids": ids})
	if err != nil {
		return nil, fmt.Errorf("can't load %s: %w", table, err)
	}
	out := make(map[int][]int, len(ids))
	for _, r := range rows {
		out[r.TechCardID] = append(out[r.TechCardID], r.Value)
	}
	return out, nil
}

type techCardMediaRow struct {
	TechCardID int                          `db:"tech_card_id"`
	Category   entity.TechCardMediaCategory `db:"category"`
	Kind       entity.TechCardMediaKind     `db:"kind"`
	Caption    sql.NullString               `db:"caption"`
	entity.MediaFull
}

func (s *Store) mediaByTechCardIds(ctx context.Context, ids []int) (map[int][]entity.TechCardMediaItem, map[int][]entity.TechCardMediaFull, error) {
	items := make(map[int][]entity.TechCardMediaItem, len(ids))
	full := make(map[int][]entity.TechCardMediaFull, len(ids))
	if len(ids) == 0 {
		return items, full, nil
	}
	rows, err := storeutil.QueryListNamed[techCardMediaRow](ctx, s.DB, `
		SELECT tcm.tech_card_id, tcm.category, tcm.kind, tcm.caption, m.*
		FROM tech_card_media tcm
		JOIN media m ON m.id = tcm.media_id
		WHERE tcm.tech_card_id IN (:ids)
		ORDER BY tcm.tech_card_id, tcm.display_order`, map[string]any{"ids": ids})
	if err != nil {
		return nil, nil, fmt.Errorf("can't load tech card media: %w", err)
	}
	for i := range rows {
		tcID := rows[i].TechCardID
		items[tcID] = append(items[tcID], entity.TechCardMediaItem{MediaId: rows[i].Id, Category: rows[i].Category, Kind: rows[i].Kind, Caption: rows[i].Caption})
		full[tcID] = append(full[tcID], entity.TechCardMediaFull{Media: rows[i].MediaFull, Category: rows[i].Category, Kind: rows[i].Kind, Caption: rows[i].Caption})
	}
	return items, full, nil
}

// PreviewURLsByTechCardIds resolves the SAME list thumbnail ListTechCards puts on a card, for an
// explicit set of styles.
//
// ЭКСПОРТИРОВАНО РАДИ ОДНОГО ЧУЖОГО ВЫЗЫВАЮЩЕГО — списка стилей проекта в библиотеке файлов
// (0321), — и это дешевле любой альтернативы. Правило выбора картинки трёхвходовое (стадия ×
// категория медиа × вид), живёт в pickTechCardPreviewURL и уже пережило одну правку; вторая его
// реализация в пакете fileslibrary разошлась бы с первой МОЛЧА, и на экране это выглядело бы как
// «у одной и той же вещи в двух местах разные картинки» — дефект, который ищут в клиенте, а лежит
// он в SQL.
//
// Одним пакетным запросом на всю страницу, не N+1; стадия берётся отдельным чтением, потому что
// правило от неё зависит, а в tech_card_media её нет. Стиль без медиа просто отсутствует в карте —
// вызывающий рисует табличку с артикулом, и это законный вид плитки, а не ошибка.
func (s *Store) PreviewURLsByTechCardIds(ctx context.Context, ids []int) (map[int]string, error) {
	out := make(map[int]string, len(ids))
	if len(ids) == 0 {
		return out, nil
	}
	stages, err := storeutil.QueryListNamed[struct {
		Id    int                  `db:"id"`
		Stage entity.TechCardStage `db:"stage"`
	}](ctx, s.DB,
		`SELECT id, stage FROM tech_card WHERE id IN (:ids)`, map[string]any{"ids": ids})
	if err != nil {
		return nil, fmt.Errorf("can't load tech card stages for previews: %w", err)
	}
	_, full, err := s.mediaByTechCardIds(ctx, ids)
	if err != nil {
		return nil, err
	}
	for _, st := range stages {
		if url := pickTechCardPreviewURL(st.Stage, full[st.Id]); url != "" {
			out[st.Id] = url
		}
	}
	return out, nil
}

type techCardCalloutRow struct {
	TechCardID int `db:"tech_card_id"`
	entity.TechCardCallout
}

func (s *Store) calloutsByTechCardIds(ctx context.Context, ids []int) (map[int][]entity.TechCardCallout, error) {
	if len(ids) == 0 {
		return map[int][]entity.TechCardCallout{}, nil
	}
	rows, err := storeutil.QueryListNamed[techCardCalloutRow](ctx, s.DB, `
		SELECT tech_card_id, callout_number, part, description, dimensions, media_id, pos_x, pos_y,
		       kind, color, dashed, filled, points, parts, client_ref
		FROM tech_card_callout
		WHERE tech_card_id IN (:ids)
		ORDER BY tech_card_id, display_order`, map[string]any{"ids": ids})
	if err != nil {
		return nil, fmt.Errorf("can't load tech card callouts: %w", err)
	}
	out := make(map[int][]entity.TechCardCallout, len(ids))
	for _, r := range rows {
		c := r.TechCardCallout
		if len(c.PointsRaw) > 0 {
			// Битый JSON в колонке — испорченная строка, а не повод уронить чтение всей карточки:
			// указание вернётся пином, и это видно, в отличие от пятисотки (довод 0308).
			if err := json.Unmarshal(c.PointsRaw, &c.Points); err != nil {
				slog.Default().Error("tech card callout: broken points json",
					slog.Int("tech_card_id", r.TechCardID), slog.Int("callout_number", c.Number),
					slog.String("err", err.Error()))
				c.Points = nil
				c.Kind = entity.AnnotationKindPin
			}
		}
		if len(c.PartsRaw) > 0 {
			// Битый список деталей — та же логика, что у якорей: указание остаётся с одной
			// деталью из `part`, и это видно, а чтение карточки не падает.
			if err := json.Unmarshal(c.PartsRaw, &c.Parts); err != nil {
				slog.Default().Error("tech card callout: broken parts json",
					slog.Int("tech_card_id", r.TechCardID), slog.Int("callout_number", c.Number),
					slog.String("err", err.Error()))
				c.Parts = nil
			}
		}
		// ЧТЕНИЕ ПОДЧИНЯЕТСЯ ТОМУ ЖЕ ПРАВИЛУ, ЧТО И ЗАПИСЬ, а не своему. Раньше здесь `part`
		// объявлялся главнее и прокручивался в начало списка, а на записи главным был список — два
		// противоположных правила для одного инварианта, и порядок чекбоксов в интерфейсе решал,
		// как называется деталь.
		//
		// Правило одно: список главнее, `part` — его первый элемент. Строка, где список пуст (стор
		// не пишет его для одной детали), дозаполняется отсюда — иначе проекция в отпечаток видела
		// бы на чтении не то, что видела на записи.
		c.Parts = c.PartList()
		first := ""
		if len(c.Parts) > 0 {
			first = c.Parts[0]
		}
		c.Part = sql.NullString{String: first, Valid: first != ""}
		out[r.TechCardID] = append(out[r.TechCardID], c)
	}
	return out, nil
}

type techCardRevisionRow struct {
	TechCardID int `db:"tech_card_id"`
	entity.TechCardRevision
}

type techCardPatternRow struct {
	TechCardID int `db:"tech_card_id"`
	entity.TechCardSizePattern
}

// patternsByTechCardIds loads the выкройки grouped by tech card. size_id is COALESCEd because a
// graded sheet is filed under NO size (NULL since 0281) and the entity spells that 0 — the same
// value the wire uses.
func (s *Store) patternsByTechCardIds(ctx context.Context, ids []int) (map[int][]entity.TechCardSizePattern, error) {
	if len(ids) == 0 {
		return map[int][]entity.TechCardSizePattern{}, nil
	}
	rows, err := storeutil.QueryListNamed[techCardPatternRow](ctx, s.DB, `
		SELECT tech_card_id, COALESCE(size_id, 0) AS size_id, COALESCE(line_key, '') AS line_key, bom_line_key, fabric_purpose,
		       url, filename, name, size_bytes, version, uploaded_at
		FROM tech_card_size_pattern
		WHERE tech_card_id IN (:ids)
		ORDER BY tech_card_id, display_order`, map[string]any{"ids": ids})
	if err != nil {
		return nil, fmt.Errorf("can't load tech card patterns: %w", err)
	}
	out := make(map[int][]entity.TechCardSizePattern, len(ids))
	for _, r := range rows {
		out[r.TechCardID] = append(out[r.TechCardID], r.TechCardSizePattern)
	}
	return out, nil
}

func (s *Store) revisionsByTechCardIds(ctx context.Context, ids []int) (map[int][]entity.TechCardRevision, error) {
	if len(ids) == 0 {
		return map[int][]entity.TechCardRevision{}, nil
	}
	rows, err := storeutil.QueryListNamed[techCardRevisionRow](ctx, s.DB, `
		SELECT tech_card_id, author, section, action, change_note, created_at
		FROM tech_card_revision
		WHERE tech_card_id IN (:ids)
		ORDER BY tech_card_id, created_at, id`, map[string]any{"ids": ids})
	if err != nil {
		return nil, fmt.Errorf("can't load tech card revisions: %w", err)
	}
	out := make(map[int][]entity.TechCardRevision, len(ids))
	for _, r := range rows {
		out[r.TechCardID] = append(out[r.TechCardID], r.TechCardRevision)
	}
	return out, nil
}
