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
	"github.com/shopspring/decimal"
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
		// A source owns a countable BOM line: without one it would save, show, and cost nothing.
		if l.Source != "" {
			field := fmt.Sprintf("garment_labels[%d].source", i)
			if !bom.Valid {
				return entity.NewFieldViolation(field, "no_bom_line", l.Key, "pick a material for this label")
			}
			if !entity.IsCountableSection(bomRes.sectionByID[int(bom.Int32)]) {
				return entity.NewFieldViolation(field, "measured_bom_line", l.Key, "use a BOM line counted in pieces")
			}
		}
		qty := l.QtyPerGarment
		if qty < 1 {
			qty = 1
		}
		id, err := storeutil.ExecNamedLastId(ctx, db, `
			INSERT INTO tech_card_garment_label
				(tech_card_id, label_key, placement, attachment, folding, size, qty_per_garment, bom_item_id, note, display_order,
				 callout_number, artwork_per_size, artwork_per_colorway,
				 label_source, label_kind, made_by, print_method, length_cm)
			VALUES (:tech_card_id, :label_key, :placement, :attachment, :folding, :size, :qty_per_garment, :bom_item_id, :note, :display_order,
				 :callout_number, :artwork_per_size, :artwork_per_colorway,
				 :label_source, :label_kind, :made_by, :print_method, :length_cm)`,
			map[string]any{
				"tech_card_id":         tcID,
				"label_key":            l.Key,
				"placement":            l.Placement,
				"attachment":           l.Attachment,
				"folding":              l.Folding,
				"size":                 l.Size,
				"qty_per_garment":      qty,
				"bom_item_id":          bom,
				"note":                 l.Note,
				"display_order":        i,
				"callout_number":       l.CalloutNumber,
				"artwork_per_size":     l.ArtworkPerSize,
				"artwork_per_colorway": l.ArtworkPerColorway,
				"label_source":         string(l.Source),
				"label_kind":           l.Kind,
				"made_by":              l.MadeBy,
				"print_method":         l.PrintMethod,
				"length_cm":            l.LengthCm,
			})
		if err != nil {
			return fmt.Errorf("failed to insert tech card garment label: %w", err)
		}
		if err := insertLabelMedia(ctx, db, "tech_card_garment_label_media", "label_id", id, l.MediaIds); err != nil {
			return err
		}
		if err := insertGarmentLabelFiles(ctx, db, id, l.Files); err != nil {
			return err
		}
	}
	return nil
}

// insertGarmentLabelFiles writes the artwork files of one label. A library file FK failure here means
// the file was deleted between the handler's check and this save: it surfaces as an error, it does
// not lose the row silently.
func insertGarmentLabelFiles(ctx context.Context, db dependency.DB, labelID int, files []entity.TechCardGarmentLabelFile) error {
	if len(files) == 0 {
		return nil
	}
	rows := make([]map[string]any, 0, len(files))
	for _, f := range files {
		rows = append(rows, map[string]any{
			"label_id":        labelID,
			"colorway_id":     f.ColorwayId,
			"size_id":         f.SizeId,
			"library_file_id": f.LibraryFileId,
			"source_hash":     f.SourceHash,
		})
	}
	if err := storeutil.BulkInsert(ctx, db, "tech_card_garment_label_file", rows); err != nil {
		return fmt.Errorf("failed to insert tech card garment label files: %w", err)
	}
	return nil
}

// validatePackagingPicks refuses a pick that names nothing or the wrong kind of thing. It runs in the
// save transaction, so the answer holds until the foreign keys of the same insert take over. An archived
// material still counts: archiving keeps it readable, and a card drawn with it stays valid.
func validatePackagingPicks(ctx context.Context, db dependency.DB, items []entity.TechCardPackagingItem) error {
	var materialIDs, auxIDs []int
	for i := range items {
		if items[i].MaterialId.Valid {
			materialIDs = append(materialIDs, int(items[i].MaterialId.Int32))
		}
		if items[i].AuxTechCardId.Valid {
			auxIDs = append(auxIDs, int(items[i].AuxTechCardId.Int32))
		}
	}
	targets, err := loadPackagingPickTargets(ctx, db, materialIDs, auxIDs)
	if err != nil {
		return err
	}
	for i := range items {
		it := &items[i]
		if it.MaterialId.Valid && it.AuxTechCardId.Valid {
			return entity.NewFieldViolation(fmt.Sprintf("packaging_items[%d].aux_tech_card_id", i), "at most one pick",
				fmt.Sprint(it.AuxTechCardId.Int32), "pick a material or an auxiliary card, not both")
		}
		if it.MaterialId.Valid && !targets.Materials[int(it.MaterialId.Int32)] {
			return entity.NewFieldViolation(fmt.Sprintf("packaging_items[%d].material_id", i), "not a packaging material",
				fmt.Sprintf("material %d", it.MaterialId.Int32), "select a material of class packaging")
		}
		if it.AuxTechCardId.Valid && !targets.AuxCards[int(it.AuxTechCardId.Int32)] {
			return entity.NewFieldViolation(fmt.Sprintf("packaging_items[%d].aux_tech_card_id", i), "not an auxiliary tech card",
				fmt.Sprintf("tech card %d", it.AuxTechCardId.Int32), "select an auxiliary tech card")
		}
	}
	return nil
}

type packagingIDRow struct {
	Id int `db:"id"`
}

// loadPackagingPickTargets answers, for these ids, which ones a packaging item may pick.
func loadPackagingPickTargets(ctx context.Context, db dependency.DB, materialIDs, auxIDs []int) (entity.PackagingPickTargets, error) {
	out := entity.PackagingPickTargets{Materials: map[int]bool{}, AuxCards: map[int]bool{}}
	if len(materialIDs) > 0 {
		rows, err := storeutil.QueryListNamed[packagingIDRow](ctx, db,
			`SELECT id FROM material WHERE id IN (:ids) AND material_class = :class`,
			map[string]any{"ids": materialIDs, "class": entity.MaterialClassPackaging})
		if err != nil {
			return out, fmt.Errorf("can't check picked packaging materials: %w", err)
		}
		for _, r := range rows {
			out.Materials[r.Id] = true
		}
	}
	if len(auxIDs) > 0 {
		rows, err := storeutil.QueryListNamed[packagingIDRow](ctx, db,
			`SELECT id FROM tech_card WHERE id IN (:ids) AND purpose = :purpose`,
			map[string]any{"ids": auxIDs, "purpose": entity.TechCardPurposeAuxiliary})
		if err != nil {
			return out, fmt.Errorf("can't check picked auxiliary tech cards: %w", err)
		}
		for _, r := range rows {
			out.AuxCards[r.Id] = true
		}
	}
	return out, nil
}

// PackagingPickTargets answers the save's own checks for a caller outside a save: the snapshot restore
// drops what no longer resolves, so one dead pick or a colourway that left does not refuse the restore.
func (s *Store) PackagingPickTargets(ctx context.Context, cardID int, materialIDs, auxIDs []int) (entity.PackagingPickTargets, error) {
	out, err := loadPackagingPickTargets(ctx, s.DB, materialIDs, auxIDs)
	if err != nil {
		return out, err
	}
	out.Colorways, err = loadCardColorwayIDs(ctx, s.DB, cardID)
	return out, err
}

func loadCardColorwayIDs(ctx context.Context, db dependency.DB, cardID int) (map[int]bool, error) {
	rows, err := storeutil.QueryListNamed[packagingIDRow](ctx, db,
		`SELECT id FROM product WHERE style_id = :card`, map[string]any{"card": cardID})
	if err != nil {
		return nil, fmt.Errorf("can't load colourways of tech card %d: %w", cardID, err)
	}
	out := make(map[int]bool, len(rows))
	for _, r := range rows {
		out[r.Id] = true
	}
	return out, nil
}

func insertTechCardPackagingItems(ctx context.Context, db dependency.DB, tcID int, items []entity.TechCardPackagingItem, bomRes bomResolver) error {
	if err := validatePackagingPicks(ctx, db, items); err != nil {
		return err
	}
	if err := validatePackagingColorways(ctx, db, tcID, items); err != nil {
		return err
	}
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
				(tech_card_id, item_key, item_usage, packing, size, qty_per_garment, bom_item_id, note, display_order,
				 material_id, aux_tech_card_id, per_order)
			VALUES (:tech_card_id, :item_key, :item_usage, :packing, :size, :qty_per_garment, :bom_item_id, :note, :display_order,
				 :material_id, :aux_tech_card_id, :per_order)`,
			map[string]any{
				"tech_card_id":     tcID,
				"item_key":         it.Key,
				"item_usage":       it.Usage,
				"packing":          it.Packing,
				"size":             it.Size,
				"qty_per_garment":  qty,
				"bom_item_id":      bom,
				"note":             it.Note,
				"display_order":    i,
				"material_id":      it.MaterialId,
				"aux_tech_card_id": it.AuxTechCardId,
				"per_order":        it.PerOrder,
			})
		if err != nil {
			return fmt.Errorf("failed to insert tech card packaging item: %w", err)
		}
		if err := insertLabelMedia(ctx, db, "tech_card_packaging_item_media", "item_id", id, it.MediaIds); err != nil {
			return err
		}
		for _, colorwayID := range it.ColorwayIds {
			if err := storeutil.ExecNamed(ctx, db, `
				INSERT INTO tech_card_packaging_item_colorway (item_id, colorway_id) VALUES (:item_id, :colorway_id)`,
				map[string]any{"item_id": id, "colorway_id": colorwayID}); err != nil {
				return fmt.Errorf("failed to insert tech card packaging item colourway: %w", err)
			}
		}
	}
	return nil
}

// validatePackagingColorways refuses a colourway id that is not a colourway of this card. A colourway
// is a product and a card save does not touch products, so the ids a client read are still the ids on
// the way back. The same check as the composition label's.
func validatePackagingColorways(ctx context.Context, db dependency.DB, tcID int, items []entity.TechCardPackagingItem) error {
	named := false
	for i := range items {
		named = named || len(items[i].ColorwayIds) > 0
	}
	if !named {
		return nil
	}
	ofThisCard, err := loadCardColorwayIDs(ctx, db, tcID)
	if err != nil {
		return err
	}
	for i := range items {
		for j, colorwayID := range items[i].ColorwayIds {
			if !ofThisCard[colorwayID] {
				return entity.NewFieldViolation(fmt.Sprintf("packaging_items[%d].colorway_ids[%d]", i, j), "not_in_tech_card",
					fmt.Sprintf("colourway %d", colorwayID), "pick a colourway of this style")
			}
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
	BomLineKey    sql.NullString `db:"bom_line_key"`
	Note          sql.NullString `db:"note"`

	CalloutNumber      int  `db:"callout_number"`
	ArtworkPerSize     bool `db:"artwork_per_size"`
	ArtworkPerColorway bool `db:"artwork_per_colorway"`

	Source      string          `db:"label_source"`
	Kind        string          `db:"label_kind"`
	MadeBy      string          `db:"made_by"`
	PrintMethod string          `db:"print_method"`
	LengthCm    decimal.Decimal `db:"length_cm"`
}

// garmentLabelFileRow is one artwork file with the state of its public link. LinkEpoch is 0 unless
// the link is live now, the same conditions the public route checks.
type garmentLabelFileRow struct {
	LabelID       int    `db:"label_id"`
	ColorwayId    int    `db:"colorway_id"`
	SizeId        int    `db:"size_id"`
	LibraryFileId int    `db:"library_file_id"`
	SourceHash    string `db:"source_hash"`
	LinkEpoch     int    `db:"link_epoch"`
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
	BomLineKey    sql.NullString `db:"bom_line_key"`
	Note          sql.NullString `db:"note"`
	MaterialId    sql.NullInt32  `db:"material_id"`
	AuxTechCardId sql.NullInt32  `db:"aux_tech_card_id"`
	PerOrder      sql.NullBool   `db:"per_order"`
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

// ExistingLibraryFileIds says which of the ids are still library files, with no viewer: it answers a
// server-built write, not a person looking at the library.
func (s *Store) ExistingLibraryFileIds(ctx context.Context, ids []int) (map[int]bool, error) {
	out := make(map[int]bool, len(ids))
	if len(ids) == 0 {
		return out, nil
	}
	rows, err := storeutil.QueryScalarListNamed[int](ctx, s.DB,
		`SELECT id FROM library_file WHERE id IN (:ids)`, map[string]any{"ids": ids})
	if err != nil {
		return nil, fmt.Errorf("can't check library files: %w", err)
	}
	for _, id := range rows {
		out[id] = true
	}
	return out, nil
}

// garmentLabelFilesByLabel reads the artwork files of the labels, in (colourway, size) order, with the
// generation of each file's live public link.
func (s *Store) garmentLabelFilesByLabel(ctx context.Context, labelIDs []int) (map[int][]entity.TechCardGarmentLabelFile, error) {
	out := make(map[int][]entity.TechCardGarmentLabelFile, len(labelIDs))
	if len(labelIDs) == 0 {
		return out, nil
	}
	rows, err := storeutil.QueryListNamed[garmentLabelFileRow](ctx, s.DB, `
		SELECT f.label_id, f.colorway_id, f.size_id, f.library_file_id, f.source_hash,
			CASE WHEN lf.access_level = 'link' AND pa.file_id IS NOT NULL AND pa.revoked_at IS NULL
				AND (pa.expires_at IS NULL OR pa.expires_at > NOW())
				THEN pa.epoch ELSE 0 END AS link_epoch
		FROM tech_card_garment_label_file f
		JOIN library_file lf ON lf.id = f.library_file_id
		LEFT JOIN library_file_public_access pa ON pa.file_id = f.library_file_id
		WHERE f.label_id IN (:ids)
		ORDER BY f.label_id, f.colorway_id, f.size_id`, map[string]any{"ids": labelIDs})
	if err != nil {
		return nil, fmt.Errorf("can't load tech card garment label files: %w", err)
	}
	for _, r := range rows {
		out[r.LabelID] = append(out[r.LabelID], entity.TechCardGarmentLabelFile{
			ColorwayId: r.ColorwayId, SizeId: r.SizeId, LibraryFileId: r.LibraryFileId,
			SourceHash: r.SourceHash, LinkEpoch: r.LinkEpoch,
		})
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
		SELECT l.id, l.tech_card_id, l.label_key, l.placement, l.attachment, l.folding, l.size, l.qty_per_garment,
			l.bom_item_id, b.line_key AS bom_line_key, l.note, l.callout_number, l.artwork_per_size, l.artwork_per_colorway,
			l.label_source, l.label_kind, l.made_by, l.print_method, l.length_cm
		FROM tech_card_garment_label l
		LEFT JOIN tech_card_bom_item b ON b.id = l.bom_item_id
		WHERE l.tech_card_id IN (:ids)
		ORDER BY l.tech_card_id, l.display_order, l.id`, map[string]any{"ids": ids})
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
	glFiles, err := s.garmentLabelFilesByLabel(ctx, glIDs)
	if err != nil {
		return err
	}
	glByCard := make(map[int][]entity.TechCardGarmentLabel, len(ids))
	for _, r := range glRows {
		glByCard[r.TechCardID] = append(glByCard[r.TechCardID], entity.TechCardGarmentLabel{
			Key: r.Key, Placement: r.Placement, Attachment: r.Attachment, Folding: r.Folding, Size: r.Size,
			QtyPerGarment: r.QtyPerGarment, BomItemId: r.BomItemId, BomLineKey: r.BomLineKey.String,
			Note: r.Note, MediaIds: glMedia[r.Id],
			CalloutNumber: r.CalloutNumber, ArtworkPerSize: r.ArtworkPerSize, ArtworkPerColorway: r.ArtworkPerColorway,
			Files:  glFiles[r.Id],
			Source: entity.GarmentLabelSource(r.Source), Kind: r.Kind, MadeBy: r.MadeBy, PrintMethod: r.PrintMethod,
			LengthCm: r.LengthCm,
		})
	}

	piRows, err := storeutil.QueryListNamed[packagingItemRow](ctx, s.DB, `
		SELECT i.id, i.tech_card_id, i.item_key, i.item_usage, i.packing, i.size, i.qty_per_garment,
			i.bom_item_id, b.line_key AS bom_line_key, i.note, i.material_id, i.aux_tech_card_id, i.per_order
		FROM tech_card_packaging_item i
		LEFT JOIN tech_card_bom_item b ON b.id = i.bom_item_id
		WHERE i.tech_card_id IN (:ids)
		ORDER BY i.tech_card_id, i.display_order, i.id`, map[string]any{"ids": ids})
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
	piColorways := make(map[int][]int, len(piIDs))
	if len(piIDs) > 0 {
		rows, err := storeutil.QueryListNamed[struct {
			ItemId     int `db:"item_id"`
			ColorwayId int `db:"colorway_id"`
		}](ctx, s.DB, `
			SELECT item_id, colorway_id FROM tech_card_packaging_item_colorway
			WHERE item_id IN (:ids) ORDER BY item_id, colorway_id`, map[string]any{"ids": piIDs})
		if err != nil {
			return fmt.Errorf("can't load tech card packaging item colourways: %w", err)
		}
		for _, r := range rows {
			piColorways[r.ItemId] = append(piColorways[r.ItemId], r.ColorwayId)
		}
	}
	piByCard := make(map[int][]entity.TechCardPackagingItem, len(ids))
	for _, r := range piRows {
		piByCard[r.TechCardID] = append(piByCard[r.TechCardID], entity.TechCardPackagingItem{
			Key: r.Key, Usage: r.Usage, Packing: r.Packing, Size: r.Size,
			QtyPerGarment: r.QtyPerGarment, BomItemId: r.BomItemId, BomLineKey: r.BomLineKey.String,
			Note: r.Note, MediaIds: piMedia[r.Id],
			MaterialId: r.MaterialId, AuxTechCardId: r.AuxTechCardId, PerOrder: r.PerOrder.Bool,
			ColorwayIds: piColorways[r.Id],
		})
	}

	for i := range cards {
		id := cards[i].Id
		cards[i].CareLabel = careByCard[id]
		cards[i].GarmentLabels = glByCard[id]
		cards[i].PackagingItems = piByCard[id]
	}
	return s.enrichPackagingPicks(ctx, cards)
}

// enrichPackagingPicks resolves each pick's current name and thumbnail, one batch for all the cards. A
// picked row that is gone just has no name: the foreign keys clear the id, and a read must not fail
// over the moment between. An auxiliary card's thumbnail is the one the picker shows for it
// (pickTechCardPreview), so a card never has two faces on one screen.
func (s *Store) enrichPackagingPicks(ctx context.Context, cards []entity.TechCard) error {
	materialSet, auxSet := map[int]bool{}, map[int]bool{}
	for i := range cards {
		for j := range cards[i].PackagingItems {
			it := &cards[i].PackagingItems[j]
			if it.MaterialId.Valid {
				materialSet[int(it.MaterialId.Int32)] = true
			}
			if it.AuxTechCardId.Valid {
				auxSet[int(it.AuxTechCardId.Int32)] = true
			}
		}
	}
	if len(materialSet) == 0 && len(auxSet) == 0 {
		return nil
	}

	type pick struct {
		name  string
		media *entity.MediaFull
	}
	materials := make(map[int]pick, len(materialSet))
	if len(materialSet) > 0 {
		rows, err := storeutil.QueryListNamed[struct {
			Id      int           `db:"id"`
			Name    string        `db:"name"`
			ImageId sql.NullInt32 `db:"image_id"`
		}](ctx, s.DB, `SELECT id, name, image_id FROM material WHERE id IN (:ids)`,
			map[string]any{"ids": sortedIntKeys(materialSet)})
		if err != nil {
			return fmt.Errorf("can't load picked packaging materials: %w", err)
		}
		imageSet := map[int]bool{}
		for _, r := range rows {
			if r.ImageId.Valid {
				imageSet[int(r.ImageId.Int32)] = true
			}
		}
		images := map[int]*entity.MediaFull{}
		if len(imageSet) > 0 {
			media, err := storeutil.QueryListNamed[entity.MediaFull](ctx, s.DB,
				`SELECT * FROM media WHERE id IN (:ids)`, map[string]any{"ids": sortedIntKeys(imageSet)})
			if err != nil {
				return fmt.Errorf("can't load picked material images: %w", err)
			}
			for i := range media {
				images[media[i].Id] = &media[i]
			}
		}
		for _, r := range rows {
			materials[r.Id] = pick{name: r.Name, media: images[int(r.ImageId.Int32)]}
		}
	}

	auxCards := make(map[int]pick, len(auxSet))
	if len(auxSet) > 0 {
		ids := sortedIntKeys(auxSet)
		rows, err := storeutil.QueryListNamed[struct {
			Id          int                  `db:"id"`
			Name        string               `db:"name"`
			StyleNumber sql.NullString       `db:"style_number"`
			Stage       entity.TechCardStage `db:"stage"`
		}](ctx, s.DB, `SELECT id, name, style_number, stage FROM tech_card WHERE id IN (:ids)`, map[string]any{"ids": ids})
		if err != nil {
			return fmt.Errorf("can't load picked auxiliary tech cards: %w", err)
		}
		_, full, err := s.mediaByTechCardIds(ctx, ids)
		if err != nil {
			return err
		}
		for _, r := range rows {
			name := r.Name
			if name == "" {
				name = r.StyleNumber.String
			}
			p := pick{name: name}
			if m := pickTechCardPreview(r.Stage, full[r.Id]); m != nil {
				p.media = &m.Media
			}
			auxCards[r.Id] = p
		}
	}

	for i := range cards {
		for j := range cards[i].PackagingItems {
			it := &cards[i].PackagingItems[j]
			var p pick
			switch {
			case it.MaterialId.Valid:
				p = materials[int(it.MaterialId.Int32)]
			case it.AuxTechCardId.Valid:
				p = auxCards[int(it.AuxTechCardId.Int32)]
			default:
				continue
			}
			it.PickedName, it.PickedThumbnail = p.name, p.media
		}
	}
	return nil
}
