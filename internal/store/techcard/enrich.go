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
	patternHistories, err := s.patternHistoriesByTechCardIds(ctx, ids)
	if err != nil {
		return err
	}
	removedPatternHistories := attachPatternHistories(patterns, patternHistories)

	for i := range cards {
		id := cards[i].Id
		cards[i].SizeIds = sizes[id]
		cards[i].SizeQuantities = sizeQty[id]
		cards[i].Media = mediaItems[id]
		cards[i].ResolvedMedia = mediaFull[id]
		cards[i].Callouts = callouts[id]
		cards[i].Revisions = revisions[id]
		cards[i].Patterns = patterns[id]
		cards[i].RemovedPatternHistory = removedPatternHistories[id]
	}
	if err := s.enrichMaterials(ctx, cards); err != nil {
		return err
	}
	if err := s.enrichProduction(ctx, cards); err != nil {
		return err
	}
	if err := s.enrichLabelsRework(ctx, cards); err != nil {
		return err
	}
	if err := s.enrichDesignQuiz(ctx, cards); err != nil {
		return err
	}
	// ПОСЛЕ производства, а не вместе с карточными медиа: id операционных снимков известны только
	// когда операции уже прочитаны. Резолвится одним запросом на всю пачку карточек.
	if err := s.enrichOperationMedia(ctx, cards); err != nil {
		return err
	}
	// AFTER enrichLabelsRework: the label media ids are known only once the labels are read (M-02).
	return s.enrichLabelMedia(ctx, cards)
}

// enrichLabelMedia resolves the labels rework's media ids (care-label logo, garment-label and
// packaging-item mockups — entity.TechCardInsert.LabelMediaIds) into full media records, one query
// for the whole batch. Same contract as enrichOperationMedia: a missing media is simply absent.
func (s *Store) enrichLabelMedia(ctx context.Context, cards []entity.TechCard) error {
	perCard := make([][]int, len(cards))
	wanted := make(map[int]bool)
	for i := range cards {
		perCard[i] = cards[i].LabelMediaIds()
		for _, id := range perCard[i] {
			wanted[id] = true
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
		return fmt.Errorf("can't load label media: %w", err)
	}
	byID := make(map[int]entity.MediaFull, len(rows))
	for i := range rows {
		byID[rows[i].Id] = rows[i]
	}
	for i := range cards {
		var out []entity.TechCardMediaFull
		for _, id := range perCard[i] {
			if full, ok := byID[id]; ok {
				out = append(out, entity.TechCardMediaFull{Media: full})
			}
		}
		cards[i].ResolvedLabelMedia = out
	}
	return nil
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
	Role       entity.TechCardMediaRole     `db:"role"`
	entity.MediaFull
}

func (s *Store) mediaByTechCardIds(ctx context.Context, ids []int) (map[int][]entity.TechCardMediaItem, map[int][]entity.TechCardMediaFull, error) {
	items := make(map[int][]entity.TechCardMediaItem, len(ids))
	full := make(map[int][]entity.TechCardMediaFull, len(ids))
	if len(ids) == 0 {
		return items, full, nil
	}
	rows, err := storeutil.QueryListNamed[techCardMediaRow](ctx, s.DB, `
		SELECT tcm.tech_card_id, tcm.category, tcm.kind, tcm.caption, tcm.role, m.*
		FROM tech_card_media tcm
		JOIN media m ON m.id = tcm.media_id
		WHERE tcm.tech_card_id IN (:ids)
		ORDER BY tcm.tech_card_id, tcm.display_order`, map[string]any{"ids": ids})
	if err != nil {
		return nil, nil, fmt.Errorf("can't load tech card media: %w", err)
	}
	for i := range rows {
		tcID := rows[i].TechCardID
		items[tcID] = append(items[tcID], entity.TechCardMediaItem{MediaId: rows[i].Id, Category: rows[i].Category, Kind: rows[i].Kind, Caption: rows[i].Caption, Role: rows[i].Role})
		full[tcID] = append(full[tcID], entity.TechCardMediaFull{Media: rows[i].MediaFull, Category: rows[i].Category, Kind: rows[i].Kind, Caption: rows[i].Caption, Role: rows[i].Role})
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
		       kind, color, dashed, filled, caps, points, parts, client_ref, spec
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
		if c.Spec.Valid {
			// MySQL отдаёт JSON в своём порядке ключей и со своими пробелами: канонизируем тем же
			// правилом, что на записи, иначе отпечаток DESIGN не совпал бы сам с собой. Битое
			// значение читается как обычная выноска — видно, а чтение карточки не падает.
			spec, err := entity.CanonicalCalloutSpec(c.Spec.String)
			if err != nil {
				slog.Default().Error("tech card callout: broken spec json",
					slog.Int("tech_card_id", r.TechCardID), slog.Int("callout_number", c.Number),
					slog.String("err", err.Error()))
				spec = ""
			}
			c.Spec = sql.NullString{String: spec, Valid: spec != ""}
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

type techCardPatternHistoryRow struct {
	TechCardID int            `db:"tech_card_id"`
	LineKey    sql.NullString `db:"line_key"`
	ScopeKey   sql.NullString `db:"scope_key"`
	SizeID     int            `db:"size_id"`
	entity.TechCardPatternRevision
}

type techCardPatternLegacyKey struct {
	ScopeKey string
	SizeID   int
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
		       url, filename, name, size_bytes, sha256, version, uploaded_at
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

// patternHistoriesByTechCardIds loads every superseded pattern revision in one query for the whole
// card batch. A keyed history row follows the sheet's stable identity; rows predating line keys use
// the binding scope and filed size that were captured with the archived object.
func (s *Store) patternHistoriesByTechCardIds(ctx context.Context, ids []int) ([]techCardPatternHistoryRow, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	rows, err := storeutil.QueryListNamed[techCardPatternHistoryRow](ctx, s.DB, `
		SELECT id AS history_id, tech_card_id, line_key, scope_key, COALESCE(size_id, 0) AS size_id,
		       version AS revision, url, sha256, filename AS file_name,
		       COALESCE(uploaded_at, replaced_at) AS created_at, snapshot_id
		FROM tech_card_size_pattern_history
		WHERE tech_card_id IN (:ids)
		ORDER BY tech_card_id, replaced_at DESC, id DESC`, map[string]any{"ids": ids})
	if err != nil {
		return nil, fmt.Errorf("can't load tech card pattern history: %w", err)
	}
	return rows, nil
}

func attachPatternHistories(patterns map[int][]entity.TechCardSizePattern, rows []techCardPatternHistoryRow) map[int][]entity.TechCardPatternRevision {
	removed := make(map[int][]entity.TechCardPatternRevision)
	lineIndexes := make(map[int]map[string]int, len(patterns))
	legacyIndexes := make(map[int]map[techCardPatternLegacyKey]int, len(patterns))
	for techCardID, cardPatterns := range patterns {
		byLine := make(map[string]int, len(cardPatterns))
		byLegacyKey := make(map[techCardPatternLegacyKey]int, len(cardPatterns))
		for i := range cardPatterns {
			p := &cardPatterns[i]
			if p.LineKey != "" {
				byLine[p.LineKey] = i
			}
			key := techCardPatternLegacyKey{
				ScopeKey: entity.FabricScopeKey(p.FabricPurpose.String, p.BomLineKey.String),
				SizeID:   p.SizeId,
			}
			// Legacy history had no stable identity. A pair shared by several live sheets is
			// irreducibly ambiguous, so mark it unusable rather than offering the wrong file as a
			// restore revision of an arbitrary sheet.
			if _, exists := byLegacyKey[key]; !exists {
				byLegacyKey[key] = i
			} else {
				byLegacyKey[key] = -1
			}
		}
		patterns[techCardID] = cardPatterns
		lineIndexes[techCardID] = byLine
		legacyIndexes[techCardID] = byLegacyKey
	}

	for _, row := range rows {
		var (
			index int
			ok    bool
		)
		if row.LineKey.Valid && row.LineKey.String != "" {
			index, ok = lineIndexes[row.TechCardID][row.LineKey.String]
		} else {
			index, ok = legacyIndexes[row.TechCardID][techCardPatternLegacyKey{
				ScopeKey: row.ScopeKey.String,
				SizeID:   row.SizeID,
			}]
		}
		if !ok || index < 0 {
			removed[row.TechCardID] = append(removed[row.TechCardID], row.TechCardPatternRevision)
			continue
		}
		cardPatterns := patterns[row.TechCardID]
		// The file the sheet carries NOW is not a superseded revision of it. It is in the history only
		// because it once left and came back (RestoreTechCardSnapshot, or the old re-attach of a url);
		// offering «restore» of the file already on the sheet would restore a card nobody is looking at.
		if row.URL == cardPatterns[index].URL {
			continue
		}
		cardPatterns[index].History = append(cardPatterns[index].History, row.TechCardPatternRevision)
		patterns[row.TechCardID] = cardPatterns
	}
	return removed
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
