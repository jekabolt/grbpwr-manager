package admin

import (
	"fmt"
	"slices"
	"strings"

	"github.com/jekabolt/grbpwr-manager/internal/dto"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/jekabolt/grbpwr-manager/internal/techcardarchive"
	pb_common "github.com/jekabolt/grbpwr-manager/proto/gen/common"
)

// CUT STRIPS IN THE ARCHIVE (FORMAT.md §5.3d). They ride card.json and land through the insert half. Nothing
// in a strip is a foreign id but the size rows' size_id, which resolveSizes has already remapped.

// tcimpCutStripRef names a strip in the report by its key, the one identity it has on both sides.
func tcimpCutStripRef(key string) string { return "cut_strip_key=" + key }

// resolveCutStrips holds every strip of card.json ON ITS OWN to the rules of a live save and drops the
// ones that break them, each with a line and a counter, never failing the import. The steps' input keys
// for a dropped strip go with it, because the convert would otherwise refuse them as dangling units.
//
// The effective BOM unit is what the rule reads here, through the same resolver used by the store;
// otherwise the importer could retain a strip that the live save later refuses and fail the import.
func (r *tcimpResolver) resolveCutStrips() {
	ins := r.out.Insert
	rows := ins.GetCutStrips()
	if len(rows) == 0 {
		return
	}
	lines := make(map[string]*pb_common.TechCardBomItem, len(ins.GetBomItems()))
	for _, b := range ins.GetBomItems() {
		lines[entity.CutStripLineKey(b.GetLineKey())] = b
	}
	pieces := make(map[string]bool, len(ins.GetPieces()))
	for _, p := range ins.GetPieces() {
		pieces[entity.CutStripLineKey(p.GetLineKey())] = true
	}

	seen := make(map[string]bool, len(rows))
	dropped := map[string]bool{}
	kept := make([]*pb_common.TechCardCutStrip, 0, len(rows))
	var skipped int
	for i, row := range rows {
		key := entity.CutStripLineKey(row.GetLineKey())
		ref := tcimpCutStripRef(key)
		if key == "" {
			ref = fmt.Sprintf("cutStrips[%d]", i)
		}
		drop := func(reason techcardarchive.Reason, detail string) {
			skipped++
			dropped[key] = true
			r.hole(techcardarchive.EntityCutStrip, ref, techcardarchive.StatusSkipped, reason, detail)
		}
		if seen[key] && key != "" {
			// The first row stays, so its key is not cut from the steps.
			skipped++
			r.hole(techcardarchive.EntityCutStrip, fmt.Sprintf("%s index=%d", ref, i), techcardarchive.StatusSkipped,
				techcardarchive.ReasonArchiveRowInvalid,
				"card.json carries this strip key twice; the first row imported, this one was dropped")
			continue
		}
		seen[key] = true
		if len(kept) >= entity.CutStripMaxPerCard {
			drop(techcardarchive.ReasonArchiveRowInvalid,
				fmt.Sprintf("a card holds at most %d strips; the rows past that were dropped", entity.CutStripMaxPerCard))
			continue
		}
		if pieces[key] {
			drop(techcardarchive.ReasonArchiveRowInvalid, "the strip's key belongs to a piece of this card; it was dropped")
			continue
		}
		// A row whose size this base lacks was cleared by resolveSizes, whose own line says so.
		row.Sizes = slices.DeleteFunc(row.GetSizes(), func(s *pb_common.TechCardCutStripSize) bool {
			return s.GetSizeId() <= 0
		})
		if err := dto.CutStripRowError(row); err != nil {
			drop(techcardarchive.ReasonArchiveRowInvalid,
				fmt.Sprintf("the strip does not validate (%s); it was dropped and the other strips imported", err.Error()))
			continue
		}
		line, ok := lines[entity.CutStripLineKey(row.GetBomLineKey())]
		if !ok {
			drop(techcardarchive.ReasonCutStripLineUnusable,
				fmt.Sprintf("the strip is cut from line %s, which is not on the card", row.GetBomLineKey()))
			continue
		}
		unit := line.GetUnit()
		if id := line.GetMaterialId(); id > 0 && r.articleUnits[id] != "" {
			section := entity.BomSectionOther
			if line.GetSection() == pb_common.TechCardBomSection_TECH_CARD_BOM_SECTION_THREAD {
				section = entity.BomSectionThread
			}
			unit = entity.ResolveBomLineUnit(section, unit, r.articleUnits[id])
		}
		if problem := dto.CutStripLineProblemOfPb(line, unit); problem != "" {
			drop(techcardarchive.ReasonCutStripLineUnusable,
				fmt.Sprintf("line %q cannot carry a strip here (%s)", line.GetName(), problem))
			continue
		}
		kept = append(kept, row)
	}
	ins.CutStrips = kept
	r.out.Counters.AddImported(techcardarchive.EntityCutStrip, len(kept))
	r.out.Counters.AddSkipped(techcardarchive.EntityCutStrip, skipped)

	if len(dropped) == 0 {
		return
	}
	for _, op := range ins.GetOperations() {
		named := func(k string) bool { return dropped[strings.ToUpper(strings.TrimSpace(k))] }
		op.InputKeys = slices.DeleteFunc(op.GetInputKeys(), named)
		op.PieceLineKeys = slices.DeleteFunc(op.GetPieceLineKeys(), named)
	}
}
