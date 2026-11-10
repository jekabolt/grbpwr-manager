package dto

import (
	"fmt"
	"strings"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	pb_common "github.com/jekabolt/grbpwr-manager/proto/gen/common"
)

// parseTechCardTopstitchLines fills the operation-owned lines after the rest of the card and the
// compatibility carry exist. That is when piece, BOM, size and shared line-key membership are known.
func parseTechCardTopstitchLines(pb *pb_common.TechCardInsert, insert *entity.TechCardInsert) error {
	if pb == nil || insert == nil {
		return nil
	}
	if !pb.GetTopstitchLinesAware() {
		for i, op := range pb.GetOperations() {
			if len(op.GetTopstitchLines()) > 0 {
				return entity.NewFieldViolation(fmt.Sprintf("operations[%d].topstitch_lines", i),
					"aware_required", "", "set topstitch_lines_aware when sending lines")
			}
		}
		return entity.ValidateTechCardTopstitchLines(insert)
	}
	total := 0
	for _, op := range pb.GetOperations() {
		total += len(op.GetTopstitchLines())
	}
	if total > entity.TechCardTopstitchMaxPerCard {
		return entity.NewFieldViolation("operations", "too_many_topstitch_lines", fmt.Sprint(total),
			fmt.Sprintf("at most %d per card", entity.TechCardTopstitchMaxPerCard))
	}
	for i := range insert.Operations {
		insert.Operations[i].TopstitchLines = nil
		if i >= len(pb.GetOperations()) {
			continue
		}
		for j, raw := range pb.GetOperations()[i].GetTopstitchLines() {
			path := fmt.Sprintf("operations[%d].topstitch_lines[%d]", i, j)
			if raw == nil {
				return entity.NewFieldViolation(path, "required", "", "remove the empty row")
			}
			if len(raw.GetParts()) > entity.TechCardTopstitchMaxParts {
				return entity.NewFieldViolation(path+".parts", "too_many", "",
					fmt.Sprintf("at most %d anchors", entity.TechCardTopstitchMaxParts))
			}
			key := strings.TrimSpace(raw.GetLineKey())
			if key == "" {
				minted, err := mintTechCardLineKey()
				if err != nil {
					return fmt.Errorf("%s.line_key: %w", path, err)
				}
				key = minted
			}
			line := entity.TechCardTopstitchLine{
				LineKey:      key,
				BomLineKey:   raw.GetBomLineKey(),
				Parts:        seamSideFromPb(&pb_common.TechCardSeamSide{Parts: raw.GetParts()}),
				AnchoredSize: raw.GetAnchoredSize(),
				Rows:         int(raw.GetRows()),
				Measured:     raw.GetMeasured(),
				DisplayOrder: j,
			}
			for k, size := range raw.GetSizes() {
				field := fmt.Sprintf("%s.sizes[%d]", path, k)
				if size == nil {
					return entity.NewFieldViolation(field, "required", "", "remove the empty row")
				}
				mm, err := nullDecimalFromPb(size.GetLineMm())
				if err != nil {
					return entity.NewFieldViolation(field+".line_mm", "not_a_number", size.GetLineMm().GetValue(), "a length in mm")
				}
				if !mm.Valid {
					continue
				}
				line.Sizes = append(line.Sizes, entity.TechCardTopstitchLineSize{
					SizeId: int(size.GetSizeId()), LineMm: mm,
				})
			}
			insert.Operations[i].TopstitchLines = append(insert.Operations[i].TopstitchLines, line)
		}
	}
	return entity.ValidateTechCardTopstitchLines(insert)
}

func topstitchLinesToPb(lines []entity.TechCardTopstitchLine) []*pb_common.TechCardTopstitchLine {
	if len(lines) == 0 {
		return nil
	}
	out := make([]*pb_common.TechCardTopstitchLine, 0, len(lines))
	for i := range lines {
		line := &lines[i]
		pb := &pb_common.TechCardTopstitchLine{
			LineKey:      line.LineKey,
			BomLineKey:   line.BomLineKey,
			Parts:        seamSideToPb(line.Parts).GetParts(),
			AnchoredSize: line.AnchoredSize,
			Rows:         int32(line.Rows),
			Stale:        line.Stale,
		}
		for _, size := range line.Sizes {
			pb.Sizes = append(pb.Sizes, &pb_common.TechCardTopstitchLineSize{
				SizeId: int32(size.SizeId), LineMm: pbDecimalFromNull(size.LineMm),
			})
		}
		out = append(out, pb)
	}
	return out
}
