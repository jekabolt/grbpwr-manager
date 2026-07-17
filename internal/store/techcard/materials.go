package techcard

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/base32"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/go-sql-driver/mysql"
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
// clearTechCardPieceMaterials removes every piece_material of the card's pieces. It runs BEFORE the
// BOM upsert (Phase A of the reorder, §D5) so a BOM line the client is deleting is not falsely
// blocked by an old piece_material → bom_item RESTRICT ref: the fresh mapping is re-inserted by
// upsertTechCardPieces after the BOM ids resolve. A no-op on create (the card has no pieces yet).
func clearTechCardPieceMaterials(ctx context.Context, db dependency.DB, tcID int) error {
	if err := storeutil.ExecNamed(ctx, db, `
		DELETE FROM tech_card_piece_material
		WHERE piece_id IN (SELECT id FROM tech_card_piece WHERE tech_card_id = :id)`,
		map[string]any{"id": tcID}); err != nil {
		return fmt.Errorf("failed to clear tech card piece materials: %w", err)
	}
	return nil
}

type pieceExistingRow struct {
	Id      int    `db:"id"`
	LineKey string `db:"line_key"`
}

// calloutRef is a callout's canonical part name and the sketch it is pinned to, from the payload.
type calloutRef struct {
	part    string
	mediaID int
	pinned  bool // media_id > 0 (anchored on some sketch)
}

// calloutSync resolves a piece's callout_number against the card's payload (S6/S7/S8): the canonical
// part name (the single place a detail name is entered, §2.5) and whether the callout is anchored on a
// TECHNICAL sketch. A moodboard/unanchored callout carries no piece semantics (S7) — a piece pointing
// at one is marked detached, not name-synced.
type calloutSync struct {
	technicalMedia map[int]bool // raw media_id → is a technical sketch of this card
	byNumber       map[int]calloutRef
}

// buildCalloutSync indexes the payload's technical sketch media and its callouts. Both live in the same
// UpdateTechCard payload as the pieces, so no DB read is needed.
func buildCalloutSync(tc *entity.TechCardInsert) calloutSync {
	cs := calloutSync{technicalMedia: make(map[int]bool), byNumber: make(map[int]calloutRef, len(tc.Callouts))}
	for _, m := range tc.Media {
		if m.Category == entity.TechCardMediaCategoryTechnical {
			cs.technicalMedia[m.MediaId] = true
		}
	}
	for i := range tc.Callouts {
		c := &tc.Callouts[i]
		mediaID := 0
		if c.MediaId.Valid {
			mediaID = int(c.MediaId.Int32)
		}
		cs.byNumber[c.Number] = calloutRef{part: strings.TrimSpace(c.Part.String), mediaID: mediaID, pinned: mediaID > 0}
	}
	return cs
}

// apply syncs a piece's derived name from its technical-sketch callout and sets its detached flag
// (S7/S8): a piece linked to a technical callout takes that callout's part as its canonical name; a
// piece whose callout was removed or is a moodboard/unanchored callout keeps its own name but is
// marked detached (it has no live technical-sketch source — orphan-control keeps it, does not drop it).
func (cs calloutSync) apply(p *entity.TechCardPiece) {
	if !p.CalloutNumber.Valid {
		p.Detached = false
		return
	}
	ref, ok := cs.byNumber[int(p.CalloutNumber.Int32)]
	if ok && ref.pinned && cs.technicalMedia[ref.mediaID] {
		if ref.part != "" {
			p.Name = ref.part // S8: the detail name lives once, on callout.part; the piece derives it
		}
		p.Detached = false
		return
	}
	p.Detached = true
}

// upsertTechCardPieces reconciles a card's cut-pieces by line_key (S8), the same keyed upsert-diff the
// BOM uses (§2.3): a line_key already in the DB is UPDATEd in place (id stable — which is what lets a
// colourway recipe usage hold a real piece_id FK), a new line_key is INSERTed, and a line_key that
// vanished from the payload is DELETEd. A DELETE the FK RESTRICT blocks (piece still used by a
// colourway recipe usage) surfaces as a field-tagged error, not a raw 500 or a silent dangle — the
// deferred-from-0159 cross-aggregate guard. piece_material was cleared in Phase A, so each kept/new
// piece just re-inserts its per-colourway mapping with the BOM ids resolved by bomRes.
func upsertTechCardPieces(ctx context.Context, db dependency.DB, tcID int, pieces []entity.TechCardPiece, bomRes bomResolver, cs calloutSync) error {
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

	existingRows, err := storeutil.QueryListNamed[pieceExistingRow](ctx, db,
		`SELECT id, line_key FROM tech_card_piece WHERE tech_card_id = :id`, map[string]any{"id": tcID})
	if err != nil {
		return fmt.Errorf("failed to load existing pieces: %w", err)
	}
	existingByKey := make(map[string]int, len(existingRows))
	for _, r := range existingRows {
		existingByKey[r.LineKey] = r.Id
	}

	seen := make(map[string]bool, len(pieces))
	for i := range pieces {
		p := &pieces[i]
		// S8/S7: derive the piece name from its technical-sketch callout (canonical part name) and set
		// detached when the callout is gone or is a moodboard/unanchored callout (no piece semantics).
		cs.apply(p)
		key := strings.TrimSpace(p.LineKey)
		if key == "" {
			key = newLineKey() // legacy/keyless payload: server assigns a stable key
		}
		if seen[key] {
			return entity.NewFieldViolation(fmt.Sprintf("pieces[%d].line_key", i),
				"duplicate line_key within the payload", "", "each cut-piece needs a unique line_key")
		}
		params := map[string]any{
			"tech_card_id":       tcID,
			"name":               p.Name,
			"line_key":           key,
			"pieces_per_garment": p.PiecesPerGarment,
			"mirrored":           p.Mirrored,
			"grainline":          p.Grainline,
			"fused":              p.Fused,
			"callout_number":     p.CalloutNumber,
			"detached":           p.Detached,
			"note":               p.Note,
			"display_order":      i,
		}
		var pieceID int
		if id, ok := existingByKey[key]; ok {
			params["id"] = id
			if err := storeutil.ExecNamed(ctx, db, `
				UPDATE tech_card_piece SET
					name=:name, pieces_per_garment=:pieces_per_garment, mirrored=:mirrored, grainline=:grainline,
					fused=:fused, callout_number=:callout_number, detached=:detached, note=:note, display_order=:display_order
				WHERE id=:id`, params); err != nil {
				return fmt.Errorf("failed to update tech card piece: %w", err)
			}
			pieceID = id
		} else {
			newID, err := storeutil.ExecNamedLastId(ctx, db, `
				INSERT INTO tech_card_piece
					(tech_card_id, name, line_key, pieces_per_garment, mirrored, grainline, fused, callout_number, detached, note, display_order)
				VALUES (:tech_card_id, :name, :line_key, :pieces_per_garment, :mirrored, :grainline, :fused, :callout_number, :detached, :note, :display_order)`,
				params)
			if err != nil {
				return fmt.Errorf("failed to insert tech card piece: %w", err)
			}
			pieceID = newID
		}
		seen[key] = true

		for j := range p.Materials {
			m := &p.Materials[j]
			if !validColorway[m.ColorwayID] {
				return fmt.Errorf("tech card piece %q: colorway_id %d is not a colourway of this style", p.Name, m.ColorwayID)
			}
			// Resolve the positional refs to real bom_item ids (S2/S3). The legacy *_index columns are
			// still written for the transition (dropped in M3).
			if err := storeutil.ExecNamed(ctx, db, `
				INSERT INTO tech_card_piece_material
					(piece_id, colorway_id, bom_item_id, fusing_bom_item_id, bom_item_index, fusing_bom_item_index, note, display_order)
				VALUES (:piece_id, :colorway_id, :bom_item_id, :fusing_bom_item_id, :bom_item_index, :fusing_bom_item_index, :note, :display_order)`,
				map[string]any{
					"piece_id":              pieceID,
					"colorway_id":           m.ColorwayID,
					"bom_item_id":           resolveBomRef(bomRes, m.BomLineKey, m.BomItemIndex),
					"fusing_bom_item_id":    resolveBomRef(bomRes, m.FusingBomLineKey, m.FusingBomItemIndex),
					"bom_item_index":        m.BomItemIndex,
					"fusing_bom_item_index": m.FusingBomItemIndex,
					"note":                  m.Note,
					"display_order":         j,
				}); err != nil {
				return fmt.Errorf("failed to insert tech card piece material: %w", err)
			}
		}
	}

	// Delete pieces that vanished from the payload. FK RESTRICT (fk_usage_piece) blocks a piece still
	// referenced by a colourway recipe usage — surface that as an actionable field-tagged error
	// (the deferred-from-0159 guard, now live because pieces are keyed/stable).
	for key, id := range existingByKey {
		if seen[key] {
			continue
		}
		if err := storeutil.ExecNamed(ctx, db,
			`DELETE FROM tech_card_piece WHERE id = :id`, map[string]any{"id": id}); err != nil {
			var me *mysql.MySQLError
			if errors.As(err, &me) && me.Number == 1451 { // ER_ROW_IS_REFERENCED_2
				return entity.NewFieldViolation("pieces",
					fmt.Sprintf("cut-piece %q is still referenced", key), "a colourway recipe usage",
					"remove that consumption line before deleting the piece")
			}
			return fmt.Errorf("failed to delete tech card piece: %w", err)
		}
	}
	return nil
}

// bomResolver maps a submitted BOM line to its persisted, stable id after an upsert-diff: by its
// stable line_key (the durable reference) and by its 0-based position in submission/display order
// (the legacy positional reference that operations/pieces still carry on the wire during the
// transition). It is how referrers resolve a real bom_item_id (S2/S3).
type bomResolver struct {
	byLineKey map[string]int
	ordered   []int // bom_item.id in submission order (== display_order)
}

// idForIndex resolves a legacy 0-based bom_item_index to a real id; ok=false when out of range (a
// dangling ref — the caller leaves the FK NULL rather than pointing at the wrong line).
func (r bomResolver) idForIndex(idx int) (int, bool) {
	if idx < 0 || idx >= len(r.ordered) {
		return 0, false
	}
	return r.ordered[idx], true
}

// resolveBomID turns a legacy positional bom_item_index (NULL-able) into a real bom_item id for a
// referrer's FK column, or SQL NULL when unset or out of range (a dangling ref).
func resolveBomID(res bomResolver, idx sql.NullInt32) any {
	if !idx.Valid {
		return nil
	}
	if id, ok := res.idForIndex(int(idx.Int32)); ok {
		return id
	}
	return nil
}

// resolveBomRef turns a referrer's BOM reference into a real bom_item id for its FK column: by stable
// line_key (preferred — positionality off the wire, WS3 follow-up), else the legacy positional index,
// else SQL NULL. Like resolveBomID, an unknown key / out-of-range index resolves to NULL (the ref was
// already broken) rather than pointing at the wrong line.
func resolveBomRef(res bomResolver, lineKey string, idx sql.NullInt32) any {
	if key := strings.TrimSpace(lineKey); key != "" {
		if id, ok := res.byLineKey[key]; ok {
			return id
		}
		return nil
	}
	return resolveBomID(res, idx)
}

type bomExistingRow struct {
	Id      int    `db:"id"`
	LineKey string `db:"line_key"`
}

// upsertTechCardBom reconciles a card's BOM lines by line_key in one transaction instead of the old
// delete-all + reinsert (S2/S3 root): a line_key already in the DB is UPDATEd in place (its id is
// stable, which is what lets referrers hold a real FK), a new line_key is INSERTed, and a line_key
// that vanished from the payload is DELETEd — a DELETE the FK RESTRICT blocks (line still used by an
// operation/piece/colourway recipe) surfaces as a field-tagged error, not a raw 500. Returns a
// resolver so pieces/operations can turn their reference into a real bom_item_id.
func upsertTechCardBom(ctx context.Context, db dependency.DB, tcID int, items []entity.TechCardBomItem) (bomResolver, error) {
	res := bomResolver{byLineKey: make(map[string]int, len(items)), ordered: make([]int, 0, len(items))}

	existingRows, err := storeutil.QueryListNamed[bomExistingRow](ctx, db,
		`SELECT id, line_key FROM tech_card_bom_item WHERE tech_card_id = :id`, map[string]any{"id": tcID})
	if err != nil {
		return res, fmt.Errorf("failed to load existing bom lines: %w", err)
	}
	existingByKey := make(map[string]int, len(existingRows))
	for _, r := range existingRows {
		existingByKey[r.LineKey] = r.Id
	}

	seen := make(map[string]bool, len(items))
	for i := range items {
		b := &items[i]
		key := strings.TrimSpace(b.LineKey)
		if key == "" {
			key = newLineKey() // legacy/keyless payload: server assigns a stable key
		}
		if seen[key] {
			return res, entity.NewFieldViolation(fmt.Sprintf("bom_items[%d].line_key", i),
				"duplicate line_key within the payload", "", "each BOM line needs a unique line_key")
		}
		snapshot, err := bomMaterialSnapshot(b)
		if err != nil {
			return res, fmt.Errorf("bom line %q snapshot: %w", b.Name, err)
		}
		params := bomItemParams(tcID, b, i, key, snapshot)
		if id, ok := existingByKey[key]; ok {
			params["id"] = id
			if err := storeutil.ExecNamed(ctx, db, `
				UPDATE tech_card_bom_item SET
					material_id=:material_id, section=:section, name=:name, supplier=:supplier, supplier_ref=:supplier_ref,
					color=:color, composition=:composition, spec=:spec, unit=:unit, unit_price=:unit_price, currency=:currency,
					comment=:comment, display_order=:display_order, fabric_width=:fabric_width, fabric_weight_gsm=:fabric_weight_gsm,
					fabric_direction=:fabric_direction, wastage_percent=:wastage_percent, material_snapshot=:material_snapshot
				WHERE id=:id`, params); err != nil {
				return res, fmt.Errorf("failed to update bom line: %w", err)
			}
			res.ordered = append(res.ordered, id)
			res.byLineKey[key] = id
		} else {
			newID, err := storeutil.ExecNamedLastId(ctx, db, `
				INSERT INTO tech_card_bom_item
					(tech_card_id, material_id, section, name, supplier, supplier_ref, color, composition, spec, unit,
					 unit_price, currency, comment, display_order, fabric_width, fabric_weight_gsm, fabric_direction,
					 wastage_percent, line_key, material_snapshot)
				VALUES (:tech_card_id, :material_id, :section, :name, :supplier, :supplier_ref, :color, :composition, :spec, :unit,
					 :unit_price, :currency, :comment, :display_order, :fabric_width, :fabric_weight_gsm, :fabric_direction,
					 :wastage_percent, :line_key, :material_snapshot)`, params)
			if err != nil {
				return res, fmt.Errorf("failed to insert bom line: %w", err)
			}
			res.ordered = append(res.ordered, newID)
			res.byLineKey[key] = newID
		}
		seen[key] = true
	}

	// Delete lines that vanished from the payload. FK RESTRICT blocks a line still referenced by an
	// operation/piece/colourway recipe — surface that as an actionable field-tagged error.
	for key, id := range existingByKey {
		if seen[key] {
			continue
		}
		if err := storeutil.ExecNamed(ctx, db,
			`DELETE FROM tech_card_bom_item WHERE id = :id`, map[string]any{"id": id}); err != nil {
			var me *mysql.MySQLError
			if errors.As(err, &me) && me.Number == 1451 { // ER_ROW_IS_REFERENCED_2
				return res, entity.NewFieldViolation("bom_items",
					fmt.Sprintf("BOM line %q is still referenced", key), "an operation, piece or colourway recipe",
					"remove that reference before deleting the BOM line")
			}
			return res, fmt.Errorf("failed to delete bom line: %w", err)
		}
	}
	return res, nil
}

// bomItemParams maps a BOM line to named params for the upsert.
func bomItemParams(tcID int, b *entity.TechCardBomItem, displayOrder int, lineKey string, snapshot []byte) map[string]any {
	return map[string]any{
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
		"display_order":     displayOrder,
		"fabric_width":      b.FabricWidth,
		"fabric_weight_gsm": b.FabricWeightGsm,
		"fabric_direction":  b.FabricDirection,
		"wastage_percent":   b.WastagePercent,
		"line_key":          lineKey,
		"material_snapshot": nullBytesParam(snapshot),
	}
}

// bomMaterialSnapshot is the read-only JSON snapshot frozen on the BOM line at save (S23): the line's
// descriptive state, so the document stays self-contained. NULL for an empty line.
func bomMaterialSnapshot(b *entity.TechCardBomItem) ([]byte, error) {
	if b.Name == "" && !b.MaterialId.Valid {
		return nil, nil
	}
	snap := map[string]any{"name": b.Name}
	if b.MaterialId.Valid {
		snap["material_id"] = b.MaterialId.Int64
	}
	for k, v := range map[string]sql.NullString{
		"supplier": b.Supplier, "supplier_ref": b.SupplierRef, "composition": b.Composition,
		"spec": b.Spec, "unit": b.Unit, "color": b.Color,
	} {
		if v.Valid && v.String != "" {
			snap[k] = v.String
		}
	}
	return json.Marshal(snap)
}

// newLineKey mints a stable 26-char CHAR(26) key (ULID-shaped: base32 of 128 random bits) for a
// legacy/keyless BOM line so the upsert-diff has a durable handle even before clients send one.
func newLineKey() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	return base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(b[:])
}

// nullBytesParam yields nil (SQL NULL) for empty JSON so an unset snapshot is not stored as "".
func nullBytesParam(b []byte) any {
	if len(b) == 0 {
		return nil
	}
	return b
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
		       c.color_code, COALESCE(c.lab_dip_status, 'pending') AS lab_dip_status, IF(c.lifecycle_status <> 4, c.id, NULL) AS product_id,
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
			SELECT id, colorway_id, bom_item_id, piece_id, bom_item_index, placement, color, pantone, consumption, quantity, piece_index
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
		       fabric_width, fabric_weight_gsm, fabric_direction, wastage_percent,
		       COALESCE(line_key, '') AS line_key, material_snapshot
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
		SELECT id, tech_card_id, name, line_key, pieces_per_garment, mirrored, grainline, fused, callout_number, detached, note
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
			SELECT id, piece_id, colorway_id, bom_item_id, fusing_bom_item_id, bom_item_index, fusing_bom_item_index, note
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
