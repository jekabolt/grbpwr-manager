package techcard

import (
	"context"
	"fmt"

	"github.com/jekabolt/grbpwr-manager/internal/dependency"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/jekabolt/grbpwr-manager/internal/store/storeutil"
)

// --- inserts (called within the AddTechCard / UpdateTechCard transaction) ---

// PR6 R1: the colourway write-path (insertTechCardColorways / insertTechCardColorwayUsages) was
// removed with the tech_card_colorway→product merge. Colourways are now first-class products created
// via CreateColorway, and a colourway's material recipe (usages, keyed by colorway_id = product.id)
// moves to the colourway write (ColorwayDevelopmentInsert.usages) in the T-B contract slice. The
// tech-card save no longer creates or full-replaces colourways; it only reads them for costing.

// insertTechCardPieces inserts the structural cut-pieces and, for each, its per-colourway fabric
// mapping (NF-05). It runs AFTER insertTechCardColorways in the child flow, so it re-queries the
// freshly-inserted colourways (in display order, same tx) to resolve each material's positional
// colorway_index into a real colorway_id — colourways are full-replace, so ids are recreated.
func insertTechCardPieces(ctx context.Context, db dependency.DB, tcID int, pieces []entity.TechCardPiece) error {
	if len(pieces) == 0 {
		return nil
	}
	// PR6 R1/§14.3: a piece material addresses its colourway by explicit colorway_id = product.id.
	// Validate membership — the colourway must be one of this style's products (product.style_id = card).
	cwRows, err := storeutil.QueryListNamed[techCardPieceColorwayIDRow](ctx, db, `
		SELECT id FROM product WHERE style_id = :id`,
		map[string]any{"id": tcID})
	if err != nil {
		return fmt.Errorf("failed to load colorway ids for pieces: %w", err)
	}
	validColorway := make(map[int]bool, len(cwRows))
	for _, r := range cwRows {
		validColorway[r.Id] = true
	}

	for i := range pieces {
		p := &pieces[i]
		pieceID, err := storeutil.ExecNamedLastId(ctx, db, `
			INSERT INTO tech_card_piece
				(tech_card_id, name, pieces_per_garment, mirrored, grainline, fused, callout_number, note, display_order)
			VALUES (:tech_card_id, :name, :pieces_per_garment, :mirrored, :grainline, :fused, :callout_number, :note, :display_order)`,
			map[string]any{
				"tech_card_id":       tcID,
				"name":               p.Name,
				"pieces_per_garment": p.PiecesPerGarment,
				"mirrored":           p.Mirrored,
				"grainline":          p.Grainline,
				"fused":              p.Fused,
				"callout_number":     p.CalloutNumber,
				"note":               p.Note,
				"display_order":      i,
			})
		if err != nil {
			return fmt.Errorf("failed to insert tech card piece: %w", err)
		}
		for j := range p.Materials {
			m := &p.Materials[j]
			if !validColorway[m.ColorwayID] {
				return fmt.Errorf("tech card piece %q: colorway_id %d is not a colourway of this style", p.Name, m.ColorwayID)
			}
			if err := storeutil.ExecNamed(ctx, db, `
				INSERT INTO tech_card_piece_material
					(piece_id, colorway_id, bom_item_index, fusing_bom_item_index, note, display_order)
				VALUES (:piece_id, :colorway_id, :bom_item_index, :fusing_bom_item_index, :note, :display_order)`,
				map[string]any{
					"piece_id":              pieceID,
					"colorway_id":           m.ColorwayID,
					"bom_item_index":        m.BomItemIndex,
					"fusing_bom_item_index": m.FusingBomItemIndex,
					"note":                  m.Note,
					"display_order":         j,
				}); err != nil {
				return fmt.Errorf("failed to insert tech card piece material: %w", err)
			}
		}
	}
	return nil
}

// insertTechCardBom inserts the BOM lines (material-article catalog). Per-colourway colour,
// placement and consumption now live on the colourway usages, not here.
func insertTechCardBom(ctx context.Context, db dependency.DB, tcID int, items []entity.TechCardBomItem) error {
	if len(items) == 0 {
		return nil
	}
	rows := make([]map[string]any, 0, len(items))
	for i := range items {
		b := &items[i]
		rows = append(rows, map[string]any{
			"tech_card_id":      tcID,
			"material_id":       b.MaterialId,
			"section":           string(b.Section),
			"name":              b.Name,
			"supplier":          b.Supplier,
			"supplier_ref":      b.SupplierRef,
			"color":             b.Color,
			"composition":       b.Composition,
			"spec":              b.Spec,
			"unit":              b.Unit,
			"unit_price":        b.UnitPrice,
			"currency":          b.Currency,
			"comment":           b.Comment,
			"display_order":     i,
			"fabric_width":      b.FabricWidth,
			"fabric_weight_gsm": b.FabricWeightGsm,
			"fabric_direction":  b.FabricDirection,
			"wastage_percent":   b.WastagePercent,
		})
	}
	if err := storeutil.BulkInsert(ctx, db, "tech_card_bom_item", rows); err != nil {
		return fmt.Errorf("failed to insert tech card bom item: %w", err)
	}
	return nil
}

// insertTechCardDetails inserts the construction-description aspects and, for each, its
// reference media.
func insertTechCardDetails(ctx context.Context, db dependency.DB, tcID int, details []entity.TechCardDetail) error {
	for i := range details {
		d := &details[i]
		detailID, err := storeutil.ExecNamedLastId(ctx, db, `
			INSERT INTO tech_card_detail (tech_card_id, detail_key, detail_text, display_order)
			VALUES (:tech_card_id, :detail_key, :detail_text, :display_order)`,
			map[string]any{
				"tech_card_id":  tcID,
				"detail_key":    d.Key,
				"detail_text":   d.Text,
				"display_order": i,
			})
		if err != nil {
			return fmt.Errorf("failed to insert tech card detail: %w", err)
		}
		if len(d.MediaIds) > 0 {
			rows := make([]map[string]any, 0, len(d.MediaIds))
			for j, mid := range d.MediaIds {
				rows = append(rows, map[string]any{
					"detail_id":     detailID,
					"media_id":      mid,
					"display_order": j,
				})
			}
			if err := storeutil.BulkInsert(ctx, db, "tech_card_detail_media", rows); err != nil {
				return fmt.Errorf("failed to insert tech card detail media: %w", err)
			}
		}
	}
	return nil
}

// --- enrich (load materials for read paths) ---

type techCardColorwayRow struct {
	TechCardID int `db:"tech_card_id"`
	entity.TechCardColorway
}

type techCardBomItemRow struct {
	TechCardID int `db:"tech_card_id"`
	entity.TechCardBomItem
}

type techCardColorwayUsageRow struct {
	ColorwayID int `db:"colorway_id"`
	entity.TechCardColorwayUsage
}

type techCardUsageConsumptionRow struct {
	UsageID int `db:"usage_id"`
	entity.TechCardBomSizeConsumption
}

type techCardDetailRow struct {
	TechCardID int `db:"tech_card_id"`
	entity.TechCardDetail
}

type techCardDetailMediaRow struct {
	DetailID int `db:"detail_id"`
	MediaID  int `db:"media_id"`
}

type techCardPieceRow struct {
	TechCardID int `db:"tech_card_id"`
	entity.TechCardPiece
}

type techCardPieceMaterialRow struct {
	PieceID int `db:"piece_id"`
	entity.TechCardPieceMaterial
}

// techCardPieceColorwayIDRow carries a colourway id when validating that a piece material's explicit
// colorway_id belongs to the owning style (product.style_id = card).
type techCardPieceColorwayIDRow struct {
	Id int `db:"id"`
}

// enrichMaterials loads colourways (+ their usages and per-size usage consumption), BOM
// lines (the article catalog) and construction-description details (+ media) for each card.
func (s *Store) enrichMaterials(ctx context.Context, cards []entity.TechCard) error {
	if len(cards) == 0 {
		return nil
	}
	ids := make([]int, 0, len(cards))
	for i := range cards {
		ids = append(ids, cards[i].Id)
	}

	// Colourways grouped per card (in display order). PR6 R1: tech_card_colorway was merged into
	// product, so a card's colourways are its products (product.style_id = card). PLM fields live on
	// product (dev_code/dev_name/dev_comment/dev_hex ← ex code/name/comment/hex). product_id keeps
	// its "dead SKU → NULL" contract: an archived colourway surfaces product_id = NULL.
	cwRows, err := storeutil.QueryListNamed[techCardColorwayRow](ctx, s.DB, `
		SELECT c.id, c.style_id AS tech_card_id, c.dev_code AS code, COALESCE(c.dev_name, '') AS name,
		       c.color_code, c.lab_dip_status, IF(c.lifecycle_status <> 4, c.id, NULL) AS product_id,
		       COALESCE(c.sku, '') AS sku, c.lifecycle_status,
		       c.dev_comment AS comment, c.pantone, c.pantone_system, c.dev_hex AS hex, c.swatch_media_id,
		       c.lab_dip_round, c.lab_dip_submitted_at, c.lab_dip_decided_at, c.lab_dip_decided_by, c.lab_dip_reject_reason
		FROM product c
		WHERE c.style_id IN (:ids)
		ORDER BY c.style_id, c.display_order, c.id`, map[string]any{"ids": ids})
	if err != nil {
		return fmt.Errorf("can't load tech card colorways: %w", err)
	}
	colorwaysByCard := make(map[int][]entity.TechCardColorway, len(ids))
	for _, r := range cwRows {
		colorwaysByCard[r.TechCardID] = append(colorwaysByCard[r.TechCardID], r.TechCardColorway)
	}
	// Index colourways by id to attach usages; collect ids for the usage query.
	colorwayByID := make(map[int]*entity.TechCardColorway, len(cwRows))
	colorwayIDs := make([]int, 0, len(cwRows))
	for card := range colorwaysByCard {
		cws := colorwaysByCard[card]
		for i := range cws {
			colorwayByID[cws[i].Id] = &cws[i]
			colorwayIDs = append(colorwayIDs, cws[i].Id)
		}
	}
	if len(colorwayIDs) > 0 {
		usageRows, err := storeutil.QueryListNamed[techCardColorwayUsageRow](ctx, s.DB, `
			SELECT id, colorway_id, bom_item_index, placement, color, pantone, consumption, quantity, piece_index
			FROM tech_card_colorway_usage
			WHERE colorway_id IN (:ids)
			ORDER BY colorway_id, display_order`, map[string]any{"ids": colorwayIDs})
		if err != nil {
			return fmt.Errorf("can't load tech card colorway usages: %w", err)
		}
		usageByID := make(map[int]*entity.TechCardColorwayUsage, len(usageRows))
		usageIDs := make([]int, 0, len(usageRows))
		for _, r := range usageRows {
			cw, ok := colorwayByID[r.ColorwayID]
			if !ok {
				continue
			}
			cw.Usages = append(cw.Usages, r.TechCardColorwayUsage)
		}
		// Slices are final now; index usages by id to attach per-size consumption.
		for cwID := range colorwayByID {
			us := colorwayByID[cwID].Usages
			for i := range us {
				usageByID[us[i].Id] = &us[i]
				usageIDs = append(usageIDs, us[i].Id)
			}
		}
		if len(usageIDs) > 0 {
			consRows, err := storeutil.QueryListNamed[techCardUsageConsumptionRow](ctx, s.DB, `
				SELECT usage_id, size_id, consumption
				FROM tech_card_colorway_usage_consumption
				WHERE usage_id IN (:ids)
				ORDER BY usage_id, display_order`, map[string]any{"ids": usageIDs})
			if err != nil {
				return fmt.Errorf("can't load tech card usage consumptions: %w", err)
			}
			for _, c := range consRows {
				if u, ok := usageByID[c.UsageID]; ok {
					u.SizeConsumptions = append(u.SizeConsumptions, c.TechCardBomSizeConsumption)
				}
			}
		}
	}

	// BOM lines per card (the article catalog).
	bomRows, err := storeutil.QueryListNamed[techCardBomItemRow](ctx, s.DB, `
		SELECT id, tech_card_id, material_id, section, name, supplier, supplier_ref, color, composition, spec,
		       unit, unit_price, currency, comment,
		       fabric_width, fabric_weight_gsm, fabric_direction, wastage_percent
		FROM tech_card_bom_item
		WHERE tech_card_id IN (:ids)
		ORDER BY tech_card_id, display_order, id`, map[string]any{"ids": ids})
	if err != nil {
		return fmt.Errorf("can't load tech card bom items: %w", err)
	}
	bomByCard := make(map[int][]entity.TechCardBomItem, len(ids))
	for _, r := range bomRows {
		bomByCard[r.TechCardID] = append(bomByCard[r.TechCardID], r.TechCardBomItem)
	}

	// Construction-description details per card, then media per detail.
	detailRows, err := storeutil.QueryListNamed[techCardDetailRow](ctx, s.DB, `
		SELECT id, tech_card_id, detail_key, detail_text
		FROM tech_card_detail
		WHERE tech_card_id IN (:ids)
		ORDER BY tech_card_id, display_order`, map[string]any{"ids": ids})
	if err != nil {
		return fmt.Errorf("can't load tech card details: %w", err)
	}
	detailsByCard := make(map[int][]entity.TechCardDetail, len(ids))
	for _, r := range detailRows {
		detailsByCard[r.TechCardID] = append(detailsByCard[r.TechCardID], r.TechCardDetail)
	}
	detailByID := make(map[int]*entity.TechCardDetail, len(detailRows))
	detailIDs := make([]int, 0, len(detailRows))
	for card := range detailsByCard {
		ds := detailsByCard[card]
		for i := range ds {
			detailByID[ds[i].Id] = &ds[i]
			detailIDs = append(detailIDs, ds[i].Id)
		}
	}
	if len(detailIDs) > 0 {
		dmRows, err := storeutil.QueryListNamed[techCardDetailMediaRow](ctx, s.DB, `
			SELECT detail_id, media_id
			FROM tech_card_detail_media
			WHERE detail_id IN (:ids)
			ORDER BY detail_id, display_order`, map[string]any{"ids": detailIDs})
		if err != nil {
			return fmt.Errorf("can't load tech card detail media: %w", err)
		}
		for _, m := range dmRows {
			if d, ok := detailByID[m.DetailID]; ok {
				d.MediaIds = append(d.MediaIds, m.MediaID)
			}
		}
	}

	// Cut-pieces per card (NF-05), then per-colourway fabric mapping per piece. The stored
	// colorway_id is surfaced directly (R1/§14.3 — no positional colorway_index anymore).
	pieceRows, err := storeutil.QueryListNamed[techCardPieceRow](ctx, s.DB, `
		SELECT id, tech_card_id, name, pieces_per_garment, mirrored, grainline, fused, callout_number, note
		FROM tech_card_piece
		WHERE tech_card_id IN (:ids)
		ORDER BY tech_card_id, display_order, id`, map[string]any{"ids": ids})
	if err != nil {
		return fmt.Errorf("can't load tech card pieces: %w", err)
	}
	piecesByCard := make(map[int][]entity.TechCardPiece, len(ids))
	for _, r := range pieceRows {
		piecesByCard[r.TechCardID] = append(piecesByCard[r.TechCardID], r.TechCardPiece)
	}
	pieceByID := make(map[int]*entity.TechCardPiece, len(pieceRows))
	pieceIDs := make([]int, 0, len(pieceRows))
	for card := range piecesByCard {
		ps := piecesByCard[card]
		for i := range ps {
			pieceByID[ps[i].Id] = &ps[i]
			pieceIDs = append(pieceIDs, ps[i].Id)
		}
	}
	if len(pieceIDs) > 0 {
		pmRows, err := storeutil.QueryListNamed[techCardPieceMaterialRow](ctx, s.DB, `
			SELECT id, piece_id, colorway_id, bom_item_index, fusing_bom_item_index, note
			FROM tech_card_piece_material
			WHERE piece_id IN (:ids)
			ORDER BY piece_id, display_order, id`, map[string]any{"ids": pieceIDs})
		if err != nil {
			return fmt.Errorf("can't load tech card piece materials: %w", err)
		}
		for _, r := range pmRows {
			p, ok := pieceByID[r.PieceID]
			if !ok {
				continue
			}
			p.Materials = append(p.Materials, r.TechCardPieceMaterial)
		}
	}

	for i := range cards {
		id := cards[i].Id
		cards[i].Colorways = colorwaysByCard[id]
		cards[i].BomItems = bomByCard[id]
		cards[i].Details = detailsByCard[id]
		cards[i].Pieces = piecesByCard[id]
	}
	return nil
}
