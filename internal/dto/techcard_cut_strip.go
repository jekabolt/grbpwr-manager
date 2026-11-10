package dto

import (
	"database/sql"
	"fmt"
	"strings"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	pb_common "github.com/jekabolt/grbpwr-manager/proto/gen/common"
)

// parseTechCardCutStrips reads the strips of a whole-card save. It runs after the BOM, the pieces and
// the operations are parsed and before the assembly canonicalizer, which classifies inputs by these keys.
//
// A strip list with no aware flag is refused instead of ignored: «flag absent» means the client does not
// know about strips, and a client that sends them while not saying so would otherwise lose them silently.
func parseTechCardCutStrips(pb *pb_common.TechCardInsert, insert *entity.TechCardInsert) ([]entity.TechCardCutStrip, error) {
	raw := pb.GetCutStrips()
	if !pb.GetCutStripsAware() {
		if len(raw) > 0 {
			return nil, entity.NewFieldViolation("cut_strips", "aware_required", "",
				"set cut_strips_aware when sending strips")
		}
		return nil, nil
	}
	if len(raw) > entity.CutStripMaxPerCard {
		return nil, entity.NewFieldViolation("cut_strips", "too_many", fmt.Sprint(len(raw)),
			fmt.Sprintf("a card has at most %d strips", entity.CutStripMaxPerCard))
	}
	bomKeys := make(map[string]string, len(insert.BomItems))
	for i := range insert.BomItems {
		bomKeys[entity.CutStripLineKey(insert.BomItems[i].LineKey)] = insert.BomItems[i].LineKey
	}
	taken := make(map[string]bool, len(insert.Pieces))
	for i := range insert.Pieces {
		taken[entity.CutStripLineKey(insert.Pieces[i].LineKey)] = true
	}
	inRange := make(map[int]bool, len(insert.SizeIds))
	for _, id := range insert.SizeIds {
		inRange[id] = true
	}
	seen := make(map[string]int, len(raw))
	out := make([]entity.TechCardCutStrip, 0, len(raw))
	for i, p := range raw {
		path := fmt.Sprintf("cut_strips[%d]", i)
		if p == nil {
			return nil, entity.NewFieldViolation(path, "required", "", "remove the empty row")
		}
		lineKey := strings.TrimSpace(p.GetLineKey())
		if lineKey == "" {
			minted, err := mintTechCardLineKey()
			if err != nil {
				return nil, fmt.Errorf("%s.line_key: %w", path, err)
			}
			lineKey = minted
		}
		if err := validatePatternLineKey(lineKey, path+".line_key"); err != nil {
			return nil, entity.NewFieldViolation(path+".line_key", "invalid", "", "a 26-character key")
		}
		cmp := entity.CutStripLineKey(lineKey)
		if prev, dup := seen[cmp]; dup {
			return nil, entity.NewFieldViolation(path+".line_key", "duplicate",
				fmt.Sprintf("cut_strips[%d].line_key", prev), "each strip has its own key")
		}
		seen[cmp] = i
		if taken[cmp] {
			return nil, entity.NewFieldViolation(path+".line_key", "cut_strip_key_taken", "",
				"the key belongs to a piece of this card")
		}
		strip, err := cutStripFromPb(p, lineKey, i, path)
		if err != nil {
			return nil, err
		}
		bomKey, ok := bomKeys[entity.CutStripLineKey(strip.BomLineKey)]
		if !ok {
			return nil, entity.NewFieldViolation(path+".bom_line_key", "cut_strip_bom_line_missing", "",
				"a BOM line of this card")
		}
		strip.BomLineKey = bomKey
		entity.KeepCutStripSizes(&strip, inRange)
		out = append(out, strip)
	}
	if len(out) == 0 {
		return nil, nil
	}
	return out, nil
}

// cutStripFromPb reads and validates one strip on its own, with nothing of the card around it.
func cutStripFromPb(p *pb_common.TechCardCutStrip, lineKey string, order int, path string) (entity.TechCardCutStrip, error) {
	strip := entity.TechCardCutStrip{
		LineKey:      lineKey,
		BomLineKey:   p.GetBomLineKey(),
		LengthMode:   p.GetLengthMode(),
		SeamKey:      nullStringFromPb(p.GetSeamKey()),
		Qty:          int(p.GetQty()),
		DisplayOrder: order,
	}
	var err error
	if strip.FitPct, err = fusingDecimalFromPb(p.GetFitPct(), path+".fit_pct"); err != nil {
		return strip, err
	}
	if strip.OverlapMm, err = fusingDecimalFromPb(p.GetOverlapMm(), path+".overlap_mm"); err != nil {
		return strip, err
	}
	if strip.WidthMm, err = fusingDecimalFromPb(p.GetWidthMm(), path+".width_mm"); err != nil {
		return strip, err
	}
	if p.OneLengthMm != nil {
		strip.OneLengthMm = sql.NullInt32{Int32: p.GetOneLengthMm(), Valid: true}
	}
	for j, s := range p.GetSizes() {
		rowPath := fmt.Sprintf("%s.sizes[%d]", path, j)
		if s == nil {
			return strip, entity.NewFieldViolation(rowPath, "required", "", "remove the empty row")
		}
		seam, err := nullDecimalFromPb(s.GetSeamMm())
		if err != nil {
			return strip, entity.NewFieldViolation(rowPath+".seam_mm", "not a number", s.GetSeamMm().GetValue(), "a length in millimetres")
		}
		row := entity.TechCardCutStripSize{SizeId: int(s.GetSizeId()), SeamMm: seam}
		if s.ManualCutMm != nil {
			row.ManualCutMm = sql.NullInt32{Int32: s.GetManualCutMm(), Valid: true}
		}
		strip.Sizes = append(strip.Sizes, row)
	}
	return strip, entity.NormalizeCutStrip(&strip, path)
}

// CutStripRowError validates one strip of an archive by the rules of a live save, so the import can
// drop a bad row with a report line where the whole-card convert would refuse the whole card.
func CutStripRowError(p *pb_common.TechCardCutStrip) error {
	key := strings.TrimSpace(p.GetLineKey())
	if key == "" {
		return entity.NewFieldViolation("line_key", "required", "", "a strip has its own key")
	}
	if err := validatePatternLineKey(key, "line_key"); err != nil {
		return err
	}
	_, err := cutStripFromPb(p, key, 0, "cut_strip")
	return err
}

// CutStripLineProblemOfPb is the rule of the mark for a BOM line of a payload. unit is the one the line
// will read with, which is the linked article's when it has one.
func CutStripLineProblemOfPb(line *pb_common.TechCardBomItem, unit string) string {
	return entity.CutStripLineProblem(unit, techCardBomSectionPbToEntity[line.GetSection()],
		string(techCardBomPurposePbToEntity[line.GetPurpose()]))
}

// TechCardCutStripsToPb emits the strips with the numbers the one length function gives. A size of the
// range with no stored row still gets a row when its length is known (one_length), and a size whose
// length is unknown emits no cut_mm: unknown is absent, never 0.
func TechCardCutStripsToPb(strips []entity.TechCardCutStrip, sizeIDs []int) []*pb_common.TechCardCutStrip {
	if len(strips) == 0 {
		return nil
	}
	out := make([]*pb_common.TechCardCutStrip, 0, len(strips))
	for i := range strips {
		s := &strips[i]
		pb := &pb_common.TechCardCutStrip{
			LineKey:    s.LineKey,
			BomLineKey: s.BomLineKey,
			LengthMode: s.LengthMode,
			SeamKey:    pbStringFromNull(s.SeamKey),
			FitPct:     pbDecimalFromDecimal(s.FitPct),
			OverlapMm:  pbDecimalFromDecimal(s.OverlapMm),
			WidthMm:    pbDecimalFromDecimal(s.WidthMm),
			Qty:        int32(s.Qty),
		}
		if s.OneLengthMm.Valid {
			v := s.OneLengthMm.Int32
			pb.OneLengthMm = &v
		}
		for _, id := range cutStripWireSizes(s, sizeIDs) {
			row := &pb_common.TechCardCutStripSize{SizeId: int32(id)}
			for j := range s.Sizes {
				if s.Sizes[j].SizeId != id {
					continue
				}
				row.SeamMm = pbDecimalFromNull(s.Sizes[j].SeamMm)
				if s.Sizes[j].ManualCutMm.Valid {
					v := s.Sizes[j].ManualCutMm.Int32
					row.ManualCutMm = &v
				}
			}
			if mm, ok := entity.CutStripLengthMm(s, id); ok {
				v := int32(mm)
				row.CutMm = &v
				row.SewnMm = pbDecimalFromDecimal(entity.CutStripSewnMm(s, mm))
			}
			pb.Sizes = append(pb.Sizes, row)
		}
		out = append(out, pb)
	}
	return out
}

// cutStripWireSizes is the range in its own order, then any stored row outside it. A stored row is
// never hidden: the client echoes what it reads, and a dropped row would read as a cleared manual value.
func cutStripWireSizes(s *entity.TechCardCutStrip, sizeIDs []int) []int {
	seen := make(map[int]bool, len(sizeIDs)+len(s.Sizes))
	out := make([]int, 0, len(sizeIDs)+len(s.Sizes))
	for _, id := range sizeIDs {
		if !seen[id] {
			seen[id] = true
			out = append(out, id)
		}
	}
	for i := range s.Sizes {
		if id := s.Sizes[i].SizeId; !seen[id] {
			seen[id] = true
			out = append(out, id)
		}
	}
	return out
}

// TechCardCutStripNormsToPb emits the shared derived-line projection, including topstitch thread.
func TechCardCutStripNormsToPb(bomItems []entity.TechCardBomItem, strips []entity.TechCardCutStrip,
	topstitch []entity.TechCardTopstitchLine, sizeIDs []int, linked map[int]entity.MaterialWithPrice,
) []*pb_common.TechCardCutStripNorm {
	norms := entity.DerivedLineNormsForRecipe(bomItems, strips, topstitch, sizeIDs, linked, nil)
	if len(norms) == 0 {
		return nil
	}
	out := make([]*pb_common.TechCardCutStripNorm, 0, len(norms))
	for i := range norms {
		pb := &pb_common.TechCardCutStripNorm{
			BomLineKey: norms[i].BomLineKey,
			Norm:       pbDecimalFromNull(norms[i].Flat),
			Complete:   norms[i].Complete,
		}
		for _, row := range norms[i].BySize {
			pb.SizeNorms = append(pb.SizeNorms, &pb_common.TechCardBomSizeConsumption{
				SizeId:      int32(row.SizeId),
				Consumption: pbDecimalFromDecimal(row.Consumption),
			})
		}
		out = append(out, pb)
	}
	return out
}
