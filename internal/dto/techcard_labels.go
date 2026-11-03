package dto

import (
	"database/sql"
	"fmt"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	pb_common "github.com/jekabolt/grbpwr-manager/proto/gen/common"
)

// Labels rework (0386): the composition label record, the garment labels and the packaging items.
//
// Validation lives here (shape, lengths, ranges, duplicates, closed vocabularies). What needs the
// database — a BOM line of THIS card, a colourway of THIS style, the fibre and media rows existing —
// is checked by the store inside the save transaction (FKs + bomResolver + product.style_id).

const (
	maxLabelNoteRunes     = 2000
	maxLabelLines         = 20  // lines per care-label line list (prose / caption / address)
	maxLabelLineRunes     = 255 // characters per printed line
	maxLabelMediaPerEntry = 20
	maxLabelEntries       = 100 // garment labels / packaging items per card
	maxLabelFilesPerEntry = 400 // artwork files per label: colourways x sizes
	maxLabelBomLineKey    = 64  // bytes, a line key is a ULID
	maxLabelSourceHash    = 64  // characters, tech_card_garment_label_file.source_hash
)

func runeLen(s string) int { return utf8.RuneCountInString(s) }

// parseLabelMediaIds validates a mockup list: positive, unique, bounded. The order is the display order.
func parseLabelMediaIds(field string, ids []int32) ([]int, error) {
	if len(ids) > maxLabelMediaPerEntry {
		return nil, entity.NewFieldViolation(field, "too_many", fmt.Sprint(len(ids)),
			fmt.Sprintf("at most %d mockups", maxLabelMediaPerEntry))
	}
	if len(ids) == 0 {
		return nil, nil
	}
	out := make([]int, 0, len(ids))
	seen := make(map[int32]bool, len(ids))
	for j, id := range ids {
		if id <= 0 {
			return nil, entity.NewFieldViolation(fmt.Sprintf("%s[%d]", field, j), "must be positive", fmt.Sprint(id), "")
		}
		if seen[id] {
			return nil, entity.NewFieldViolation(fmt.Sprintf("%s[%d]", field, j), "duplicate media id", fmt.Sprint(id),
				"attach each mockup once")
		}
		seen[id] = true
		out = append(out, int(id))
	}
	return out, nil
}

func parseLabelKey(field, key string) error {
	if strings.TrimSpace(key) == "" {
		return entity.NewFieldViolation(field, "required", "", "pick a known kind or type a name")
	}
	if runeLen(key) > maxVarchar64 {
		return entity.NewFieldViolation(field, "too_long", fmt.Sprint(runeLen(key)),
			fmt.Sprintf("at most %d characters", maxVarchar64))
	}
	return nil
}

func checkLabelText(field, v string, max int) error {
	if runeLen(v) > max {
		return entity.NewFieldViolation(field, "too_long", fmt.Sprint(runeLen(v)), fmt.Sprintf("at most %d characters", max))
	}
	return nil
}

func parseLabelQty(field string, q int32) (int, error) {
	switch {
	case q == 0:
		return 1, nil // unset = one per garment (the column default)
	case q < 0:
		return 0, entity.NewFieldViolation(field, "out_of_range", fmt.Sprint(q), "one or more per garment; send 0 for 1")
	}
	return int(q), nil
}

func parseLabelBomItemID(field string, id int32) (sql.NullInt32, error) {
	if id < 0 {
		return sql.NullInt32{}, entity.NewFieldViolation(field, "must not be negative", fmt.Sprint(id), "send 0 for none")
	}
	return nullInt32FromPb(id), nil
}

// parseLabelLines validates a line list; nil/empty = derived. A line may not carry a newline — the
// store joins the list with "\n", and a newline inside a line would read back as two lines.
func parseLabelLines(field string, lines []string) ([]string, error) {
	if len(lines) == 0 {
		return nil, nil
	}
	if len(lines) > maxLabelLines {
		return nil, entity.NewFieldViolation(field, "too_many", fmt.Sprint(len(lines)), fmt.Sprintf("at most %d lines", maxLabelLines))
	}
	out := make([]string, 0, len(lines))
	for j, l := range lines {
		f := fmt.Sprintf("%s[%d]", field, j)
		if strings.ContainsAny(l, "\r\n") {
			return nil, entity.NewFieldViolation(f, "contains a line break", "", "send one string per line")
		}
		if err := checkLabelText(f, l, maxLabelLineRunes); err != nil {
			return nil, err
		}
		out = append(out, l)
	}
	return out, nil
}

// parseTechCardCareLabel parses the composition label record. nil in = nil out («keep the stored
// record»). Colourways come back sorted by colorway_id; an entry that overrides nothing is dropped, so
// the stored set and the digest never carry an empty override.
func parseTechCardCareLabel(pb *pb_common.TechCardCareLabel) (*entity.TechCardCareLabel, error) {
	if pb == nil {
		return nil, nil
	}
	const f = "care_label"
	if pb.LogoMediaId < 0 {
		return nil, entity.NewFieldViolation(f+".logo_media_id", "must not be negative", fmt.Sprint(pb.LogoMediaId), "send 0 for the brand mark")
	}
	preset := pb.QrPreset
	if preset == "" {
		preset = entity.CareLabelQRStorefront
	}
	if !entity.ValidCareLabelQRPresets[preset] {
		return nil, entity.NewFieldViolation(f+".qr_preset", "unknown preset", preset, "storefront, custom or fixed")
	}
	if err := checkLabelText(f+".qr_template", pb.QrTemplate, maxVarchar512); err != nil {
		return nil, err
	}
	prose, err := parseLabelLines(f+".care_prose_lines", pb.CareProseLines)
	if err != nil {
		return nil, err
	}
	caption, err := parseLabelLines(f+".back_caption_lines", pb.BackCaptionLines)
	if err != nil {
		return nil, err
	}
	address, err := parseLabelLines(f+".address_lines", pb.AddressLines)
	if err != nil {
		return nil, err
	}
	out := &entity.TechCardCareLabel{
		LogoMediaId:      nullInt32FromPb(pb.LogoMediaId),
		CareProseLines:   prose,
		QRPreset:         preset,
		QRTemplate:       nullStringFromPb(pb.QrTemplate),
		BackCaptionLines: caption,
		AddressLines:     address,
	}
	seenColorway := make(map[int32]bool, len(pb.Colorways))
	for i, cw := range pb.Colorways {
		cf := fmt.Sprintf("%s.colorways[%d]", f, i)
		if cw == nil {
			continue
		}
		if cw.ColorwayId <= 0 {
			return nil, entity.NewFieldViolation(cf+".colorway_id", "required", fmt.Sprint(cw.ColorwayId), "name a colourway of this style")
		}
		if seenColorway[cw.ColorwayId] {
			return nil, entity.NewFieldViolation(cf+".colorway_id", "duplicate colourway", fmt.Sprint(cw.ColorwayId),
				"send one entry per colourway")
		}
		seenColorway[cw.ColorwayId] = true
		if err := checkLabelText(cf+".colour_name", cw.ColourName, maxVarchar64); err != nil {
			return nil, err
		}
		fibers := make([]entity.TechCardCareLabelFiber, 0, len(cw.Fibers))
		seenFiber := make(map[string]bool, len(cw.Fibers))
		for j, fb := range cw.Fibers {
			ff := fmt.Sprintf("%s.fibers[%d]", cf, j)
			if fb == nil {
				continue
			}
			part, ok := techCardBomLabelPartPbToEntity[fb.Part]
			if !ok || part == entity.BomLabelPartNotOnLabel {
				return nil, entity.NewFieldViolation(ff+".part", "must be a part of the label", fb.Part.String(),
					"shell, body lining, sleeve lining, pocket lining, hood lining, filling or trim")
			}
			code := strings.TrimSpace(fb.FiberCode)
			if code == "" || runeLen(code) > 8 {
				return nil, entity.NewFieldViolation(ff+".fiber_code", "required fibre code", fb.FiberCode, "pick a fibre from the dictionary")
			}
			if fb.Pct < 1 || fb.Pct > 100 {
				return nil, entity.NewFieldViolation(ff+".pct", "out_of_range", fmt.Sprint(fb.Pct), "1 to 100 percent")
			}
			key := string(part) + "|" + code
			if seenFiber[key] {
				return nil, entity.NewFieldViolation(ff+".fiber_code", "duplicate fibre in this part", code,
					"merge the two rows into one percentage")
			}
			seenFiber[key] = true
			fibers = append(fibers, entity.TechCardCareLabelFiber{Part: part, FiberCode: code, Pct: int(fb.Pct)})
		}
		if cw.ColourName == "" && len(fibers) == 0 {
			continue // overrides nothing: the colourway is simply derived
		}
		out.Colorways = append(out.Colorways, entity.TechCardCareLabelColorway{
			ColorwayId: int(cw.ColorwayId),
			ColourName: nullStringFromPb(cw.ColourName),
			Fibers:     digestList(fibers),
		})
	}
	sort.SliceStable(out.Colorways, func(a, b int) bool { return out.Colorways[a].ColorwayId < out.Colorways[b].ColorwayId })
	return out, nil
}

func parseTechCardGarmentLabels(pbs []*pb_common.TechCardGarmentLabel) ([]entity.TechCardGarmentLabel, error) {
	if len(pbs) > maxLabelEntries {
		return nil, entity.NewFieldViolation("garment_labels", "too_many", fmt.Sprint(len(pbs)), fmt.Sprintf("at most %d labels", maxLabelEntries))
	}
	out := make([]entity.TechCardGarmentLabel, 0, len(pbs))
	for i, l := range pbs {
		if l == nil {
			continue
		}
		f := fmt.Sprintf("garment_labels[%d]", i)
		if err := parseLabelKey(f+".key", l.Key); err != nil {
			return nil, err
		}
		for _, c := range []struct {
			name string
			v    string
			max  int
		}{
			{"placement", l.Placement, maxVarchar255}, {"attachment", l.Attachment, maxVarchar255},
			{"folding", l.Folding, maxVarchar255}, {"size", l.Size, maxVarchar64}, {"note", l.Note, maxLabelNoteRunes},
		} {
			if err := checkLabelText(f+"."+c.name, c.v, c.max); err != nil {
				return nil, err
			}
		}
		qty, err := parseLabelQty(f+".qty_per_garment", l.QtyPerGarment)
		if err != nil {
			return nil, err
		}
		bom, err := parseLabelBomItemID(f+".bom_item_id", l.BomItemId)
		if err != nil {
			return nil, err
		}
		media, err := parseLabelMediaIds(f+".media_ids", l.MediaIds)
		if err != nil {
			return nil, err
		}
		lineKey, err := parseLabelBomLineKey(f+".bom_line_key", l.BomLineKey)
		if err != nil {
			return nil, err
		}
		if l.CalloutNumber < 0 {
			return nil, entity.NewFieldViolation(f+".callout_number", "must not be negative", fmt.Sprint(l.CalloutNumber), "send 0 for no pin")
		}
		files, err := parseGarmentLabelFiles(f+".files", l.Files)
		if err != nil {
			return nil, err
		}
		out = append(out, entity.TechCardGarmentLabel{
			Key:           l.Key,
			Placement:     nullStringFromPb(l.Placement),
			Attachment:    nullStringFromPb(l.Attachment),
			Folding:       nullStringFromPb(l.Folding),
			Size:          nullStringFromPb(l.Size),
			QtyPerGarment: qty,
			BomItemId:     bom,
			Note:          nullStringFromPb(l.Note),
			MediaIds:      media,
			BomLineKey:    lineKey,

			CalloutNumber:      int(l.CalloutNumber),
			ArtworkPerSize:     l.GetArtworkPerSize(),
			ArtworkPerColorway: l.GetArtworkPerColorway(),
			Files:              files,
			ArtworkOmitted:     l.ArtworkPerSize == nil,
		})
	}
	return out, nil
}

// parseLabelBomLineKey validates the address of a BOM line. Whether a line carries it is judged
// later, against the payload's BOM: a stale key is cleared there and never breaks a save.
func parseLabelBomLineKey(field, key string) (string, error) {
	key = strings.TrimSpace(key)
	if len(key) > maxLabelBomLineKey {
		return "", entity.NewFieldViolation(field, "too_long", key, fmt.Sprintf("at most %d characters", maxLabelBomLineKey))
	}
	return key, nil
}

// parseGarmentLabelFiles validates the artwork files of one label and puts them in (colourway, size)
// order, the order the read returns. A second file for one (colourway, size) is refused in words: the
// unique index would otherwise answer with a raw constraint error.
func parseGarmentLabelFiles(field string, pbs []*pb_common.TechCardGarmentLabelFile) ([]entity.TechCardGarmentLabelFile, error) {
	if len(pbs) == 0 {
		return nil, nil
	}
	if len(pbs) > maxLabelFilesPerEntry {
		return nil, entity.NewFieldViolation(field, "too_many", fmt.Sprint(len(pbs)), fmt.Sprintf("at most %d files", maxLabelFilesPerEntry))
	}
	type slot struct{ colorway, size int32 }
	seen := make(map[slot]bool, len(pbs))
	out := make([]entity.TechCardGarmentLabelFile, 0, len(pbs))
	for i, f := range pbs {
		if f == nil {
			continue
		}
		ff := fmt.Sprintf("%s[%d]", field, i)
		if f.ColorwayId < 0 {
			return nil, entity.NewFieldViolation(ff+".colorway_id", "must not be negative", fmt.Sprint(f.ColorwayId), "send 0 for any colourway")
		}
		if f.SizeId < 0 {
			return nil, entity.NewFieldViolation(ff+".size_id", "must not be negative", fmt.Sprint(f.SizeId), "send 0 for all sizes")
		}
		if f.LibraryFileId <= 0 {
			return nil, entity.NewFieldViolation(ff+".library_file_id", "required", fmt.Sprint(f.LibraryFileId), "upload the file")
		}
		if err := checkLabelText(ff+".source_hash", f.SourceHash, maxLabelSourceHash); err != nil {
			return nil, err
		}
		s := slot{f.ColorwayId, f.SizeId}
		if seen[s] {
			return nil, entity.NewFieldViolation(ff, "duplicate file for this colourway and size", fmt.Sprint(f.LibraryFileId), "keep one file per slot")
		}
		seen[s] = true
		out = append(out, entity.TechCardGarmentLabelFile{
			ColorwayId:    int(f.ColorwayId),
			SizeId:        int(f.SizeId),
			LibraryFileId: int(f.LibraryFileId),
			SourceHash:    f.SourceHash,
		})
	}
	sort.SliceStable(out, func(a, b int) bool {
		if out[a].ColorwayId != out[b].ColorwayId {
			return out[a].ColorwayId < out[b].ColorwayId
		}
		return out[a].SizeId < out[b].SizeId
	})
	return out, nil
}

func parseTechCardPackagingItems(pbs []*pb_common.TechCardPackagingItem) ([]entity.TechCardPackagingItem, error) {
	if len(pbs) > maxLabelEntries {
		return nil, entity.NewFieldViolation("packaging_items", "too_many", fmt.Sprint(len(pbs)), fmt.Sprintf("at most %d items", maxLabelEntries))
	}
	out := make([]entity.TechCardPackagingItem, 0, len(pbs))
	for i, it := range pbs {
		if it == nil {
			continue
		}
		f := fmt.Sprintf("packaging_items[%d]", i)
		if err := parseLabelKey(f+".key", it.Key); err != nil {
			return nil, err
		}
		for _, c := range []struct {
			name string
			v    string
			max  int
		}{
			{"usage", it.Usage, maxVarchar255}, {"packing", it.Packing, maxVarchar255},
			{"size", it.Size, maxVarchar64}, {"note", it.Note, maxLabelNoteRunes},
		} {
			if err := checkLabelText(f+"."+c.name, c.v, c.max); err != nil {
				return nil, err
			}
		}
		qty, err := parseLabelQty(f+".qty_per_garment", it.QtyPerGarment)
		if err != nil {
			return nil, err
		}
		bom, err := parseLabelBomItemID(f+".bom_item_id", it.BomItemId)
		if err != nil {
			return nil, err
		}
		media, err := parseLabelMediaIds(f+".media_ids", it.MediaIds)
		if err != nil {
			return nil, err
		}
		lineKey, err := parseLabelBomLineKey(f+".bom_line_key", it.BomLineKey)
		if err != nil {
			return nil, err
		}
		out = append(out, entity.TechCardPackagingItem{
			Key:           it.Key,
			Usage:         nullStringFromPb(it.Usage),
			Packing:       nullStringFromPb(it.Packing),
			Size:          nullStringFromPb(it.Size),
			QtyPerGarment: qty,
			BomItemId:     bom,
			Note:          nullStringFromPb(it.Note),
			MediaIds:      media,
			BomLineKey:    lineKey,
		})
	}
	return out, nil
}

// --- entity → pb -------------------------------------------------------------------------------

func techCardCareLabelToPb(c *entity.TechCardCareLabel) *pb_common.TechCardCareLabel {
	if c == nil {
		return nil
	}
	out := &pb_common.TechCardCareLabel{
		LogoMediaId:      c.LogoMediaId.Int32,
		CareProseLines:   c.CareProseLines,
		QrPreset:         c.QRPreset,
		QrTemplate:       pbStringFromNull(c.QRTemplate),
		BackCaptionLines: c.BackCaptionLines,
		AddressLines:     c.AddressLines,
	}
	for _, cw := range c.Colorways {
		pcw := &pb_common.TechCardCareLabelColorway{
			ColorwayId: int32(cw.ColorwayId),
			ColourName: pbStringFromNull(cw.ColourName),
		}
		for _, fb := range cw.Fibers {
			pcw.Fibers = append(pcw.Fibers, &pb_common.TechCardCareLabelFiber{
				Part:      techCardBomLabelPartEntityToPb[fb.Part],
				FiberCode: fb.FiberCode,
				Pct:       int32(fb.Pct),
			})
		}
		out.Colorways = append(out.Colorways, pcw)
	}
	return out
}

func labelMediaIdsToPb(ids []int) []int32 {
	if len(ids) == 0 {
		return nil
	}
	out := make([]int32, 0, len(ids))
	for _, id := range ids {
		out = append(out, int32(id))
	}
	return out
}

func techCardGarmentLabelsToPb(labels []entity.TechCardGarmentLabel) []*pb_common.TechCardGarmentLabel {
	out := make([]*pb_common.TechCardGarmentLabel, 0, len(labels))
	for _, l := range labels {
		perSize := l.ArtworkPerSize
		out = append(out, &pb_common.TechCardGarmentLabel{
			Key:           l.Key,
			Placement:     pbStringFromNull(l.Placement),
			Attachment:    pbStringFromNull(l.Attachment),
			Folding:       pbStringFromNull(l.Folding),
			Size:          pbStringFromNull(l.Size),
			QtyPerGarment: int32(l.QtyPerGarment),
			BomItemId:     l.BomItemId.Int32,
			Note:          pbStringFromNull(l.Note),
			MediaIds:      labelMediaIdsToPb(l.MediaIds),
			BomLineKey:    l.BomLineKey,

			CalloutNumber:      int32(l.CalloutNumber),
			ArtworkPerSize:     &perSize,
			ArtworkPerColorway: l.ArtworkPerColorway,
			Files:              garmentLabelFilesToPb(l.Files),
		})
	}
	return out
}

func garmentLabelFilesToPb(files []entity.TechCardGarmentLabelFile) []*pb_common.TechCardGarmentLabelFile {
	if len(files) == 0 {
		return nil
	}
	out := make([]*pb_common.TechCardGarmentLabelFile, 0, len(files))
	for _, f := range files {
		out = append(out, &pb_common.TechCardGarmentLabelFile{
			ColorwayId:    int32(f.ColorwayId),
			SizeId:        int32(f.SizeId),
			LibraryFileId: int32(f.LibraryFileId),
			SourceHash:    f.SourceHash,
			PublicUrl:     f.PublicURL,
		})
	}
	return out
}

func techCardPackagingItemsToPb(items []entity.TechCardPackagingItem) []*pb_common.TechCardPackagingItem {
	out := make([]*pb_common.TechCardPackagingItem, 0, len(items))
	for _, it := range items {
		out = append(out, &pb_common.TechCardPackagingItem{
			Key:           it.Key,
			Usage:         pbStringFromNull(it.Usage),
			Packing:       pbStringFromNull(it.Packing),
			Size:          pbStringFromNull(it.Size),
			QtyPerGarment: int32(it.QtyPerGarment),
			BomItemId:     it.BomItemId.Int32,
			Note:          pbStringFromNull(it.Note),
			MediaIds:      labelMediaIdsToPb(it.MediaIds),
			BomLineKey:    it.BomLineKey,
		})
	}
	return out
}

// --- sign-off gate (D-04) ----------------------------------------------------------------------

// GarmentLabelsWithoutMockup names the garment labels with no artwork, in payload order: the labels
// that keep the LABELS sign-off from being APPROVED (owner decision D-04). Artwork is a mockup or a
// file. The composition row is not asked: the engine draws its picture. A save is never refused over
// them; only the approval is.
func GarmentLabelsWithoutMockup(tc *entity.TechCardInsert) []string {
	if tc == nil {
		return nil
	}
	var out []string
	for i := range tc.GarmentLabels {
		l := &tc.GarmentLabels[i]
		if l.IsComposition() {
			continue
		}
		if len(l.MediaIds) == 0 && len(l.Files) == 0 {
			out = append(out, fmt.Sprintf("%s (#%d)", l.Key, i+1))
		}
	}
	return out
}

// --- pin, material link, artwork on the write path --------------------------------------------------

// CarryOmittedGarmentLabelArtwork keeps the stored pin, artwork modes and files of a row a client sent
// without artwork_per_size: that client knows nothing of them, and the delete-insert save would erase
// them with no signature to say so. It runs before the restamp, so the digest hashes what stays.
func CarryOmittedGarmentLabelArtwork(stored, tc *entity.TechCardInsert) {
	if stored == nil || tc == nil {
		return
	}
	byKey := make(map[string]*entity.TechCardGarmentLabel, len(stored.GarmentLabels))
	for i := range stored.GarmentLabels {
		l := &stored.GarmentLabels[i]
		if _, ok := byKey[l.Key]; !ok {
			byKey[l.Key] = l
		}
	}
	for i := range tc.GarmentLabels {
		l := &tc.GarmentLabels[i]
		if !l.ArtworkOmitted {
			continue
		}
		if prev, ok := byKey[l.Key]; ok {
			l.CalloutNumber = prev.CalloutNumber
			l.ArtworkPerSize = prev.ArtworkPerSize
			l.ArtworkPerColorway = prev.ArtworkPerColorway
			l.Files = prev.Files
		}
	}
}

// ClearDanglingGarmentLabelPins clears a pin no callout of this payload carries. The callout is what
// the label points at and it is removed elsewhere, so a stale number must not break a save.
func ClearDanglingGarmentLabelPins(tc *entity.TechCardInsert) {
	if tc == nil {
		return
	}
	numbers := make(map[int]bool, len(tc.Callouts))
	for i := range tc.Callouts {
		if tc.Callouts[i].Number > 0 {
			numbers[tc.Callouts[i].Number] = true
		}
	}
	for i := range tc.GarmentLabels {
		if l := &tc.GarmentLabels[i]; l.CalloutNumber != 0 && !numbers[l.CalloutNumber] {
			l.CalloutNumber = 0
		}
	}
}

// NormalizeLabelBomLinks makes the BOM link of every garment label and packaging item say what the
// read will say, before the digest is taken:
//   - a key no line of this payload carries is a removed line: both the key and the id are cleared, a
//     stale link never breaks a save;
//   - a key of a line already stored gets that line's id, because the projection hashes the id and the
//     read reports it. Without this an approval made from a key-only row is born stale;
//   - a key of a line born in this save has no id yet and stays key-only.
func NormalizeLabelBomLinks(stored, tc *entity.TechCardInsert) {
	if tc == nil {
		return
	}
	keys := make(map[string]bool, len(tc.BomItems))
	for i := range tc.BomItems {
		if k := strings.TrimSpace(tc.BomItems[i].LineKey); k != "" {
			keys[k] = true
		}
	}
	idByKey := map[string]int{}
	if stored != nil {
		for i := range stored.BomItems {
			if k := strings.TrimSpace(stored.BomItems[i].LineKey); k != "" && stored.BomItems[i].Id > 0 {
				idByKey[k] = stored.BomItems[i].Id
			}
		}
	}
	normalize := func(key *string, id *sql.NullInt32) {
		k := strings.TrimSpace(*key)
		if k == "" {
			return
		}
		*key = k
		if !keys[k] {
			*key, *id = "", sql.NullInt32{}
			return
		}
		if resolved, ok := idByKey[k]; ok {
			*id = sql.NullInt32{Int32: int32(resolved), Valid: true}
			return
		}
		*id = sql.NullInt32{}
	}
	for i := range tc.GarmentLabels {
		normalize(&tc.GarmentLabels[i].BomLineKey, &tc.GarmentLabels[i].BomItemId)
	}
	for i := range tc.PackagingItems {
		normalize(&tc.PackagingItems[i].BomLineKey, &tc.PackagingItems[i].BomItemId)
	}
}

// DropForeignGarmentLabelColorwayFiles drops files of a colourway that is not a colourway of this
// style: the colourway was deleted, or a copy brought the row with it.
func DropForeignGarmentLabelColorwayFiles(colorways []entity.TechCardColorway, tc *entity.TechCardInsert) {
	if tc == nil {
		return
	}
	mine := make(map[int]bool, len(colorways))
	for i := range colorways {
		mine[colorways[i].Id] = true
	}
	for i := range tc.GarmentLabels {
		l := &tc.GarmentLabels[i]
		kept := l.Files[:0:0]
		for _, f := range l.Files {
			if f.ColorwayId == 0 || mine[f.ColorwayId] {
				kept = append(kept, f)
			}
		}
		l.Files = kept
		if len(l.Files) == 0 {
			l.Files = nil
		}
	}
}

// LabelsLinkedToBornBomLines names the rows whose only link is the key of a line this save creates.
// The read will report an id the write cannot know yet, so a LABELS approval taken now would be born
// stale; the row saves fine and the approval goes on the next save.
func LabelsLinkedToBornBomLines(tc *entity.TechCardInsert) []string {
	if tc == nil {
		return nil
	}
	var out []string
	for i := range tc.GarmentLabels {
		l := &tc.GarmentLabels[i]
		if l.BomLineKey != "" && !l.BomItemId.Valid {
			out = append(out, l.Key)
		}
	}
	return out
}
