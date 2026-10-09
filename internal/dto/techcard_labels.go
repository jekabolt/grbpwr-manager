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
		})
	}
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
		out = append(out, entity.TechCardPackagingItem{
			Key:           it.Key,
			Usage:         nullStringFromPb(it.Usage),
			Packing:       nullStringFromPb(it.Packing),
			Size:          nullStringFromPb(it.Size),
			QtyPerGarment: qty,
			BomItemId:     bom,
			Note:          nullStringFromPb(it.Note),
			MediaIds:      media,
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
		})
	}
	return out
}

// --- sign-off gate (D-04) ----------------------------------------------------------------------

// GarmentLabelsWithoutMockup names the garment labels that carry no mockup, in payload order — the
// labels that keep the LABELS sign-off from being APPROVED (owner decision D-04). A save is never
// refused over them; only the approval is.
func GarmentLabelsWithoutMockup(tc *entity.TechCardInsert) []string {
	if tc == nil {
		return nil
	}
	var out []string
	for i, l := range tc.GarmentLabels {
		if len(l.MediaIds) == 0 {
			out = append(out, fmt.Sprintf("%s (#%d)", l.Key, i+1))
		}
	}
	return out
}
