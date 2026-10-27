package admin

import (
	"fmt"
	"strings"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/jekabolt/grbpwr-manager/internal/techcardarchive"
)

// SEAMS IN THE ARCHIVE (format 1.3, FORMAT.md §5.3b).
//
// Until 1.3 the seam decisions did not travel (a P2 call left to the owner, 27.10 «научи перенос
// забирать швы»): the export cut TechCard.seams and every step's seam_keys, so an imported card
// arrived with no seams and the assembly sheet had nothing to join its pieces along.
//
// NOTHING IN A SEAM IS A FOREIGN ID. seam_key is the card's own ULID (unique per card, not per
// base), every anchor names a piece by its stable line_key (which travels verbatim in card.json),
// and a step names its seams by seam_key. So the seams travel verbatim in seams.json, the steps'
// seam_keys stay where they are in card.json, and the imported card's new row ids follow from the
// writes themselves. What does not travel is what belongs to the source instance: the source
// fingerprint (a hash over the source's sheet URLs — the import stamps its own from the imported
// sheets) and the created/updated stamps. A seam the source read as stale lands stale.

// seamsToArchive is the export half: the card's stored seams as seams.json rows, in store order.
func seamsToArchive(seams []entity.TechCardSeam) []techcardarchive.SeamPayload {
	if len(seams) == 0 {
		return nil
	}
	out := make([]techcardarchive.SeamPayload, 0, len(seams))
	for _, s := range seams {
		out = append(out, techcardarchive.SeamPayload{
			SeamKey:      s.SeamKey,
			Status:       string(s.Status),
			Kind:         string(s.Kind),
			Direction:    string(s.Direction),
			Source:       string(s.Source),
			SideA:        seamSideToArchive(s.SideA),
			SideB:        seamSideToArchive(s.SideB),
			AnchoredSize: s.AnchoredSize,
			Note:         s.Note,
			Stale:        s.Stale,
		})
	}
	return out
}

func seamSideToArchive(side []entity.TechCardSeamAnchor) []techcardarchive.SeamAnchor {
	out := make([]techcardarchive.SeamAnchor, 0, len(side))
	for _, a := range side {
		samples := make([]techcardarchive.SeamSample, 0, len(a.Samples))
		for _, p := range a.Samples {
			samples = append(samples, techcardarchive.SeamSample{U: p.U, V: p.V})
		}
		var placement *techcardarchive.SeamPlacement
		if p := a.Placement; p != nil {
			placement = &techcardarchive.SeamPlacement{XMm: p.XMm, YMm: p.YMm, RotDeg: p.RotDeg, Flip: p.Flip}
		}
		out = append(out, techcardarchive.SeamAnchor{
			PieceLineKey: a.PieceLineKey,
			Samples:      samples,
			PerimShare:   a.PerimShare,
			LenMm:        a.LenMm,
			Notches:      a.Notches,
			TurnDeg:      a.TurnDeg,
			RangeFrom:    a.RangeFrom,
			RangeTo:      a.RangeTo,
			EdgeHint:     a.EdgeHint,
			ContourSig:   a.ContourSig,
			Placement:    placement,
		})
	}
	return out
}

func seamFromArchive(p techcardarchive.SeamPayload) entity.TechCardSeamInput {
	return entity.TechCardSeamInput{
		SeamKey:      p.SeamKey,
		Status:       entity.TechCardSeamStatus(strings.TrimSpace(p.Status)),
		Kind:         entity.TechCardSeamKind(strings.TrimSpace(p.Kind)),
		Direction:    entity.TechCardSeamDirection(strings.TrimSpace(p.Direction)),
		Source:       entity.TechCardSeamSource(strings.TrimSpace(p.Source)),
		SideA:        seamSideFromArchive(p.SideA),
		SideB:        seamSideFromArchive(p.SideB),
		AnchoredSize: p.AnchoredSize,
		Note:         p.Note,
	}
}

func seamSideFromArchive(side []techcardarchive.SeamAnchor) []entity.TechCardSeamAnchor {
	out := make([]entity.TechCardSeamAnchor, 0, len(side))
	for _, a := range side {
		samples := make([]entity.TechCardSeamSample, 0, len(a.Samples))
		for _, p := range a.Samples {
			samples = append(samples, entity.TechCardSeamSample{U: p.U, V: p.V})
		}
		var placement *entity.TechCardSeamPlacement
		if p := a.Placement; p != nil {
			placement = &entity.TechCardSeamPlacement{XMm: p.XMm, YMm: p.YMm, RotDeg: p.RotDeg, Flip: p.Flip}
		}
		out = append(out, entity.TechCardSeamAnchor{
			PieceLineKey: a.PieceLineKey,
			Samples:      samples,
			PerimShare:   a.PerimShare,
			LenMm:        a.LenMm,
			Notches:      a.Notches,
			TurnDeg:      a.TurnDeg,
			RangeFrom:    a.RangeFrom,
			RangeTo:      a.RangeTo,
			EdgeHint:     a.EdgeHint,
			ContourSig:   a.ContourSig,
			Placement:    placement,
		})
	}
	return out
}

// tcimpSeamRef names a seam in the report by its key — the one identity it has on both sides.
func tcimpSeamRef(key string) string { return "seam_key=" + key }

// resolveSeams reads seams.json, validates every seam ON ITS OWN by the rules of a live
// UpsertTechCardSeams, drops the ones that do not validate or that join a piece the card does not
// have — each with a line and a counter, never failing the import — and then cuts every step's
// seam_keys down to the seams that survived. A 1.2 archive has no seams.json: every step's keys go
// (an older exporter cut them anyway; a hand-made file that kept them would be refused by the store).
//
// The store re-checks piece membership inside the transaction (insertImportedSeams) — a drop there
// moves the same counter this tally fills.
func (r *tcimpResolver) resolveSeams() error {
	var rows []techcardarchive.SeamPayload
	if _, err := r.readSidecar(techcardarchive.FileSeams, &rows); err != nil {
		return err
	}

	pieces := map[string]bool{}
	for _, p := range r.out.Insert.GetPieces() {
		if k := strings.ToUpper(strings.TrimSpace(p.GetLineKey())); k != "" {
			pieces[k] = true
		}
	}

	kept := make(map[string]bool, len(rows))
	seen := make(map[string]bool, len(rows))
	var plan []entity.TechCardSeamInput
	stale := map[string]bool{}
	var imported, skipped int
	for i, row := range rows {
		key := entity.NormalizeTechCardSeamKey(row.SeamKey)
		ref := tcimpSeamRef(key)
		if key == "" {
			ref = fmt.Sprintf("seams[%d]", i)
		}
		if seen[key] && key != "" {
			skipped++
			r.hole(techcardarchive.EntitySeam, fmt.Sprintf("%s index=%d", ref, i), techcardarchive.StatusSkipped,
				techcardarchive.ReasonArchiveRowInvalid,
				"seams.json carries this seam_key twice; the first row imported, this one was dropped")
			continue
		}
		seen[key] = true
		if len(plan) >= entity.TechCardSeamMaxRowsPerCard {
			skipped++
			r.hole(techcardarchive.EntitySeam, ref, techcardarchive.StatusSkipped, techcardarchive.ReasonArchiveRowInvalid,
				fmt.Sprintf("a card holds at most %d seams; the rows past that were dropped", entity.TechCardSeamMaxRowsPerCard))
			continue
		}
		in := entity.TechCardSeamsWrite{TechCardId: 1, Seams: []entity.TechCardSeamInput{seamFromArchive(row)}}
		if err := entity.ValidateTechCardSeamsWrite(&in); err != nil {
			skipped++
			r.hole(techcardarchive.EntitySeam, ref, techcardarchive.StatusSkipped, techcardarchive.ReasonArchiveRowInvalid,
				fmt.Sprintf("the seam does not validate (%s); it was dropped and the other seams imported", err.Error()))
			continue
		}
		seam := in.Seams[0]
		if missing := seamMissingPiece(seam, pieces); missing != "" {
			skipped++
			r.hole(techcardarchive.EntitySeam, ref, techcardarchive.StatusSkipped, techcardarchive.ReasonSeamPieceMissing,
				fmt.Sprintf("the seam joins piece %s, which is not on the card", missing))
			continue
		}
		imported++
		kept[seam.SeamKey] = true
		if row.Stale {
			stale[seam.SeamKey] = true
		}
		plan = append(plan, seam)
	}
	if len(rows) > 0 {
		r.out.Counters.AddImported(techcardarchive.EntitySeam, imported)
		r.out.Counters.AddSkipped(techcardarchive.EntitySeam, skipped)
	}
	r.out.SeamPlan = plan
	r.out.SeamStale = stale

	// The steps keep only the keys of seams that will exist, in their order, each once.
	for _, op := range r.out.Insert.GetOperations() {
		if op == nil || len(op.GetSeamKeys()) == 0 {
			continue
		}
		var keys []string
		dup := map[string]bool{}
		for _, k := range op.GetSeamKeys() {
			k = entity.NormalizeTechCardSeamKey(k)
			if kept[k] && !dup[k] {
				dup[k] = true
				keys = append(keys, k)
			}
		}
		op.SeamKeys = keys
	}
	return nil
}

// seamMissingPiece returns the first anchor piece key absent from pieces, or "".
func seamMissingPiece(seam entity.TechCardSeamInput, pieces map[string]bool) string {
	for _, side := range [][]entity.TechCardSeamAnchor{seam.SideA, seam.SideB} {
		for _, a := range side {
			if !pieces[strings.ToUpper(strings.TrimSpace(a.PieceLineKey))] {
				return a.PieceLineKey
			}
		}
	}
	return ""
}
