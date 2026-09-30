package techcard

import (
	"context"
	"database/sql"
	"fmt"
	"sort"
	"strings"

	"github.com/jekabolt/grbpwr-manager/internal/dependency"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/jekabolt/grbpwr-manager/internal/store/storeutil"
)

// Labels rework (0386): the composition (care) label record, the garment labels and the packaging
// items. Write = full replace inside the card save (the delete list in UpdateTechCardTx clears them,
// these functions insert), exactly like tech_card_detail + tech_card_detail_media. Read = enrich.
//
// What the DTO cannot check is checked here, inside the save transaction: a bom_item_id must be a
// line of THIS card (bomResolver, the legacy labels precedent), a colourway must be a product of THIS
// style. Media and fibre existence is the FK's job and surfaces as InvalidArgument on write.

// joinLabelLines stores a line list as one TEXT value; nil/empty = NULL = derived.
func joinLabelLines(lines []string) sql.NullString {
	if len(lines) == 0 {
		return sql.NullString{}
	}
	return sql.NullString{String: strings.Join(lines, "\n"), Valid: true}
}

func splitLabelLines(v sql.NullString) []string {
	if !v.Valid {
		return nil
	}
	return strings.Split(v.String, "\n")
}

// insertTechCardCareLabel writes the composition label record and its per-colourway rows. nil = the
// payload did not speak: the caller preserved the stored record, nothing to insert.
func insertTechCardCareLabel(ctx context.Context, db dependency.DB, tcID int, c *entity.TechCardCareLabel) error {
	if c == nil {
		return nil
	}
	if len(c.Colorways) > 0 {
		own, err := storeutil.QueryListNamed[struct {
			Id int `db:"id"`
		}](ctx, db, `SELECT id FROM product WHERE style_id = :card`, map[string]any{"card": tcID})
		if err != nil {
			return fmt.Errorf("load colourways of tech card %d: %w", tcID, err)
		}
		mine := make(map[int]bool, len(own))
		for _, p := range own {
			mine[p.Id] = true
		}
		for i, cw := range c.Colorways {
			if !mine[cw.ColorwayId] {
				return entity.NewFieldViolation(fmt.Sprintf("care_label.colorways[%d].colorway_id", i), "not_in_tech_card",
					fmt.Sprintf("colourway %d", cw.ColorwayId), "pick a colourway of this style")
			}
		}
	}
	preset := c.QRPreset
	if preset == "" {
		preset = entity.CareLabelQRStorefront
	}
	if err := storeutil.ExecNamed(ctx, db, `
		INSERT INTO tech_card_care_label
			(tech_card_id, logo_media_id, care_prose_lines, qr_preset, qr_template, back_caption_lines, address_lines)
		VALUES (:tech_card_id, :logo_media_id, :care_prose_lines, :qr_preset, :qr_template, :back_caption_lines, :address_lines)`,
		map[string]any{
			"tech_card_id":       tcID,
			"logo_media_id":      c.LogoMediaId,
			"care_prose_lines":   joinLabelLines(c.CareProseLines),
			"qr_preset":          preset,
			"qr_template":        c.QRTemplate,
			"back_caption_lines": joinLabelLines(c.BackCaptionLines),
			"address_lines":      joinLabelLines(c.AddressLines),
		}); err != nil {
		return fmt.Errorf("failed to insert tech card care label: %w", err)
	}
	for i, cw := range c.Colorways {
		cwID, err := storeutil.ExecNamedLastId(ctx, db, `
			INSERT INTO tech_card_care_label_colorway (tech_card_id, colorway_id, colour_name, display_order)
			VALUES (:tech_card_id, :colorway_id, :colour_name, :display_order)`,
			map[string]any{
				"tech_card_id":  tcID,
				"colorway_id":   cw.ColorwayId,
				"colour_name":   cw.ColourName,
				"display_order": i,
			})
		if err != nil {
			return fmt.Errorf("failed to insert tech card care label colourway: %w", err)
		}
		if len(cw.Fibers) == 0 {
			continue
		}
		rows := make([]map[string]any, 0, len(cw.Fibers))
		for j, f := range cw.Fibers {
			rows = append(rows, map[string]any{
				"care_label_colorway_id": cwID,
				"label_part":             string(f.Part),
				"fiber_code":             f.FiberCode,
				"pct":                    f.Pct,
				"display_order":          j,
			})
		}
		if err := storeutil.BulkInsert(ctx, db, "tech_card_care_label_fiber", rows); err != nil {
			return fmt.Errorf("failed to insert tech card care label fibres: %w", err)
		}
	}
	return nil
}

// resolveLabelBomItem turns a label's BOM reference into the FK value: a line key (server-built
// clone payload) resolves against the ids this save just minted; an id must be a line of this card.
func resolveLabelBomItem(field string, id sql.NullInt32, lineKey string, bomRes bomResolver) (sql.NullInt32, error) {
	if key := strings.TrimSpace(lineKey); key != "" {
		if resolved, ok := bomRes.byLineKey[key]; ok {
			return sql.NullInt32{Int32: int32(resolved), Valid: true}, nil
		}
		return sql.NullInt32{}, entity.NewFieldViolation(field, "not_in_tech_card",
			fmt.Sprintf("BOM line %s", lineKey), "select a BOM line from this tech card")
	}
	if !id.Valid || id.Int32 == 0 {
		return sql.NullInt32{}, nil
	}
	if !bomRes.containsID(int(id.Int32)) {
		return sql.NullInt32{}, entity.NewFieldViolation(field, "not_in_tech_card",
			fmt.Sprintf("BOM line %d", id.Int32), "select a BOM line from this tech card")
	}
	return id, nil
}

func insertLabelMedia(ctx context.Context, db dependency.DB, table, parentCol string, parentID int, mediaIDs []int) error {
	if len(mediaIDs) == 0 {
		return nil
	}
	rows := make([]map[string]any, 0, len(mediaIDs))
	for j, mid := range mediaIDs {
		rows = append(rows, map[string]any{parentCol: parentID, "media_id": mid, "display_order": j})
	}
	if err := storeutil.BulkInsert(ctx, db, table, rows); err != nil {
		return fmt.Errorf("failed to insert %s: %w", table, err)
	}
	return nil
}

func insertTechCardGarmentLabels(ctx context.Context, db dependency.DB, tcID int, labels []entity.TechCardGarmentLabel, bomRes bomResolver) error {
	for i := range labels {
		l := &labels[i]
		bom, err := resolveLabelBomItem(fmt.Sprintf("garment_labels[%d].bom_item_id", i), l.BomItemId, l.BomLineKey, bomRes)
		if err != nil {
			return err
		}
		qty := l.QtyPerGarment
		if qty < 1 {
			qty = 1
		}
		id, err := storeutil.ExecNamedLastId(ctx, db, `
			INSERT INTO tech_card_garment_label
				(tech_card_id, label_key, placement, attachment, folding, size, qty_per_garment, bom_item_id, note, display_order)
			VALUES (:tech_card_id, :label_key, :placement, :attachment, :folding, :size, :qty_per_garment, :bom_item_id, :note, :display_order)`,
			map[string]any{
				"tech_card_id":    tcID,
				"label_key":       l.Key,
				"placement":       l.Placement,
				"attachment":      l.Attachment,
				"folding":         l.Folding,
				"size":            l.Size,
				"qty_per_garment": qty,
				"bom_item_id":     bom,
				"note":            l.Note,
				"display_order":   i,
			})
		if err != nil {
			return fmt.Errorf("failed to insert tech card garment label: %w", err)
		}
		if err := insertLabelMedia(ctx, db, "tech_card_garment_label_media", "label_id", id, l.MediaIds); err != nil {
			return err
		}
	}
	return nil
}

func insertTechCardPackagingItems(ctx context.Context, db dependency.DB, tcID int, items []entity.TechCardPackagingItem, bomRes bomResolver) error {
	for i := range items {
		it := &items[i]
		bom, err := resolveLabelBomItem(fmt.Sprintf("packaging_items[%d].bom_item_id", i), it.BomItemId, it.BomLineKey, bomRes)
		if err != nil {
			return err
		}
		qty := it.QtyPerGarment
		if qty < 1 {
			qty = 1
		}
		id, err := storeutil.ExecNamedLastId(ctx, db, `
			INSERT INTO tech_card_packaging_item
				(tech_card_id, item_key, item_usage, packing, size, qty_per_garment, bom_item_id, note, display_order)
			VALUES (:tech_card_id, :item_key, :item_usage, :packing, :size, :qty_per_garment, :bom_item_id, :note, :display_order)`,
			map[string]any{
				"tech_card_id":    tcID,
				"item_key":        it.Key,
				"item_usage":      it.Usage,
				"packing":         it.Packing,
				"size":            it.Size,
				"qty_per_garment": qty,
				"bom_item_id":     bom,
				"note":            it.Note,
				"display_order":   i,
			})
		if err != nil {
			return fmt.Errorf("failed to insert tech card packaging item: %w", err)
		}
		if err := insertLabelMedia(ctx, db, "tech_card_packaging_item_media", "item_id", id, it.MediaIds); err != nil {
			return err
		}
	}
	return nil
}

// --- read ------------------------------------------------------------------------------------------

type careLabelRow struct {
	TechCardID       int            `db:"tech_card_id"`
	LogoMediaId      sql.NullInt32  `db:"logo_media_id"`
	CareProseLines   sql.NullString `db:"care_prose_lines"`
	QRPreset         string         `db:"qr_preset"`
	QRTemplate       sql.NullString `db:"qr_template"`
	BackCaptionLines sql.NullString `db:"back_caption_lines"`
	AddressLines     sql.NullString `db:"address_lines"`
}

type careLabelColorwayRow struct {
	Id         int            `db:"id"`
	TechCardID int            `db:"tech_card_id"`
	ColorwayId int            `db:"colorway_id"`
	ColourName sql.NullString `db:"colour_name"`
}

type careLabelFiberRow struct {
	ColorwayRowID int    `db:"care_label_colorway_id"`
	LabelPart     string `db:"label_part"`
	FiberCode     string `db:"fiber_code"`
	Pct           int    `db:"pct"`
}

type garmentLabelRow struct {
	Id            int            `db:"id"`
	TechCardID    int            `db:"tech_card_id"`
	Key           string         `db:"label_key"`
	Placement     sql.NullString `db:"placement"`
	Attachment    sql.NullString `db:"attachment"`
	Folding       sql.NullString `db:"folding"`
	Size          sql.NullString `db:"size"`
	QtyPerGarment int            `db:"qty_per_garment"`
	BomItemId     sql.NullInt32  `db:"bom_item_id"`
	Note          sql.NullString `db:"note"`
}

type packagingItemRow struct {
	Id            int            `db:"id"`
	TechCardID    int            `db:"tech_card_id"`
	Key           string         `db:"item_key"`
	Usage         sql.NullString `db:"item_usage"`
	Packing       sql.NullString `db:"packing"`
	Size          sql.NullString `db:"size"`
	QtyPerGarment int            `db:"qty_per_garment"`
	BomItemId     sql.NullInt32  `db:"bom_item_id"`
	Note          sql.NullString `db:"note"`
}

type labelMediaRow struct {
	ParentID int `db:"parent_id"`
	MediaID  int `db:"media_id"`
}

// labelMediaByParent reads a mockup join table into parent id → media ids in display order.
func (s *Store) labelMediaByParent(ctx context.Context, table, parentCol string, parentIDs []int) (map[int][]int, error) {
	out := make(map[int][]int, len(parentIDs))
	if len(parentIDs) == 0 {
		return out, nil
	}
	rows, err := storeutil.QueryListNamed[labelMediaRow](ctx, s.DB, fmt.Sprintf(`
		SELECT %[2]s AS parent_id, media_id FROM %[1]s
		WHERE %[2]s IN (:ids)
		ORDER BY %[2]s, display_order, id`, table, parentCol), map[string]any{"ids": parentIDs})
	if err != nil {
		return nil, fmt.Errorf("can't load %s: %w", table, err)
	}
	for _, r := range rows {
		out[r.ParentID] = append(out[r.ParentID], r.MediaID)
	}
	return out, nil
}

// enrichLabelsRework loads the composition label record, the garment labels and the packaging items
// (+ mockups) for each card.
func (s *Store) enrichLabelsRework(ctx context.Context, cards []entity.TechCard) error {
	if len(cards) == 0 {
		return nil
	}
	ids := make([]int, 0, len(cards))
	for i := range cards {
		ids = append(ids, cards[i].Id)
	}

	careRows, err := storeutil.QueryListNamed[careLabelRow](ctx, s.DB, `
		SELECT tech_card_id, logo_media_id, care_prose_lines, qr_preset, qr_template, back_caption_lines, address_lines
		FROM tech_card_care_label WHERE tech_card_id IN (:ids)`, map[string]any{"ids": ids})
	if err != nil {
		return fmt.Errorf("can't load tech card care labels: %w", err)
	}
	careByCard := make(map[int]*entity.TechCardCareLabel, len(careRows))
	for _, r := range careRows {
		careByCard[r.TechCardID] = &entity.TechCardCareLabel{
			LogoMediaId:      r.LogoMediaId,
			CareProseLines:   splitLabelLines(r.CareProseLines),
			QRPreset:         r.QRPreset,
			QRTemplate:       r.QRTemplate,
			BackCaptionLines: splitLabelLines(r.BackCaptionLines),
			AddressLines:     splitLabelLines(r.AddressLines),
		}
	}
	if len(careRows) > 0 {
		cwRows, err := storeutil.QueryListNamed[careLabelColorwayRow](ctx, s.DB, `
			SELECT id, tech_card_id, colorway_id, colour_name
			FROM tech_card_care_label_colorway WHERE tech_card_id IN (:ids)
			ORDER BY tech_card_id, colorway_id`, map[string]any{"ids": ids})
		if err != nil {
			return fmt.Errorf("can't load tech card care label colourways: %w", err)
		}
		cwIDs := make([]int, 0, len(cwRows))
		for _, r := range cwRows {
			cwIDs = append(cwIDs, r.Id)
		}
		fibersByRow := make(map[int][]entity.TechCardCareLabelFiber, len(cwRows))
		if len(cwIDs) > 0 {
			fRows, err := storeutil.QueryListNamed[careLabelFiberRow](ctx, s.DB, `
				SELECT care_label_colorway_id, label_part, fiber_code, pct
				FROM tech_card_care_label_fiber WHERE care_label_colorway_id IN (:ids)
				ORDER BY care_label_colorway_id, display_order, id`, map[string]any{"ids": cwIDs})
			if err != nil {
				return fmt.Errorf("can't load tech card care label fibres: %w", err)
			}
			for _, f := range fRows {
				fibersByRow[f.ColorwayRowID] = append(fibersByRow[f.ColorwayRowID], entity.TechCardCareLabelFiber{
					Part: entity.TechCardBomLabelPart(f.LabelPart), FiberCode: f.FiberCode, Pct: f.Pct,
				})
			}
		}
		for _, r := range cwRows {
			c, ok := careByCard[r.TechCardID]
			if !ok {
				continue
			}
			c.Colorways = append(c.Colorways, entity.TechCardCareLabelColorway{
				ColorwayId: r.ColorwayId, ColourName: r.ColourName, Fibers: fibersByRow[r.Id],
			})
		}
		for _, c := range careByCard {
			sort.SliceStable(c.Colorways, func(i, j int) bool { return c.Colorways[i].ColorwayId < c.Colorways[j].ColorwayId })
		}
	}

	glRows, err := storeutil.QueryListNamed[garmentLabelRow](ctx, s.DB, `
		SELECT id, tech_card_id, label_key, placement, attachment, folding, size, qty_per_garment, bom_item_id, note
		FROM tech_card_garment_label WHERE tech_card_id IN (:ids)
		ORDER BY tech_card_id, display_order, id`, map[string]any{"ids": ids})
	if err != nil {
		return fmt.Errorf("can't load tech card garment labels: %w", err)
	}
	glIDs := make([]int, 0, len(glRows))
	for _, r := range glRows {
		glIDs = append(glIDs, r.Id)
	}
	glMedia, err := s.labelMediaByParent(ctx, "tech_card_garment_label_media", "label_id", glIDs)
	if err != nil {
		return err
	}
	glByCard := make(map[int][]entity.TechCardGarmentLabel, len(ids))
	for _, r := range glRows {
		glByCard[r.TechCardID] = append(glByCard[r.TechCardID], entity.TechCardGarmentLabel{
			Key: r.Key, Placement: r.Placement, Attachment: r.Attachment, Folding: r.Folding, Size: r.Size,
			QtyPerGarment: r.QtyPerGarment, BomItemId: r.BomItemId, Note: r.Note, MediaIds: glMedia[r.Id],
		})
	}

	piRows, err := storeutil.QueryListNamed[packagingItemRow](ctx, s.DB, `
		SELECT id, tech_card_id, item_key, item_usage, packing, size, qty_per_garment, bom_item_id, note
		FROM tech_card_packaging_item WHERE tech_card_id IN (:ids)
		ORDER BY tech_card_id, display_order, id`, map[string]any{"ids": ids})
	if err != nil {
		return fmt.Errorf("can't load tech card packaging items: %w", err)
	}
	piIDs := make([]int, 0, len(piRows))
	for _, r := range piRows {
		piIDs = append(piIDs, r.Id)
	}
	piMedia, err := s.labelMediaByParent(ctx, "tech_card_packaging_item_media", "item_id", piIDs)
	if err != nil {
		return err
	}
	piByCard := make(map[int][]entity.TechCardPackagingItem, len(ids))
	for _, r := range piRows {
		piByCard[r.TechCardID] = append(piByCard[r.TechCardID], entity.TechCardPackagingItem{
			Key: r.Key, Usage: r.Usage, Packing: r.Packing, Size: r.Size,
			QtyPerGarment: r.QtyPerGarment, BomItemId: r.BomItemId, Note: r.Note, MediaIds: piMedia[r.Id],
		})
	}

	for i := range cards {
		id := cards[i].Id
		cards[i].CareLabel = careByCard[id]
		cards[i].GarmentLabels = glByCard[id]
		cards[i].PackagingItems = piByCard[id]
	}
	return nil
}
