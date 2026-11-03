package admin

import (
	"fmt"
	"strings"

	"github.com/jekabolt/grbpwr-manager/internal/dto"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/jekabolt/grbpwr-manager/internal/techcardarchive"
	pb_common "github.com/jekabolt/grbpwr-manager/proto/gen/common"
)

// PIECE FOLDS IN THE ARCHIVE (FORMAT.md §5.3c). card.json has carried pieceFolds since the folds were
// born and the import ignored them; the steps' fold_keys ride the same file. Nothing in a fold is a
// foreign id: fold_key is the card's own ULID and piece_line_key travels verbatim. Not written: the
// source fingerprint, the people and the stamps. A fold the source read as stale lands stale. An
// archive with no fold_keys brings its folds with no step, which reads as flat.

// tcimpFoldRef names a fold in the report by its key — the one identity it has on both sides.
func tcimpFoldRef(key string) string { return "fold_key=" + key }

// resolveFolds validates every fold of card.json ON ITS OWN by the rules of a live
// UpsertTechCardPieceFolds, drops the ones that do not validate, repeat or sit on a piece the card does
// not have — each with a line and a counter, never failing the import — and then cuts every step's
// fold_keys down to the folds that survive and to the rule of fold steps.
//
// The store re-checks piece membership and the rule inside the transaction (insertImportedFolds),
// against what actually landed.
func (r *tcimpResolver) resolveFolds() {
	rows := r.out.Card.GetPieceFolds()

	pieces := map[string]bool{}
	for _, p := range r.out.Insert.GetPieces() {
		if k := strings.ToUpper(strings.TrimSpace(p.GetLineKey())); k != "" {
			pieces[k] = true
		}
	}

	seen := make(map[string]bool, len(rows))
	seenSeq := make(map[string]bool, len(rows))
	refs := make(map[string]entity.TechCardFoldOrderRef, len(rows))
	stale := map[string]bool{}
	var plan []entity.TechCardPieceFoldInput
	var imported, skipped int
	drop := func(ref string, reason techcardarchive.Reason, detail string) {
		skipped++
		r.hole(techcardarchive.EntityFold, ref, techcardarchive.StatusSkipped, reason, detail)
	}
	for i, row := range rows {
		key := entity.NormalizeTechCardSeamKey(row.GetFoldKey())
		ref := tcimpFoldRef(key)
		if key == "" {
			ref = fmt.Sprintf("pieceFolds[%d]", i)
		}
		if seen[key] && key != "" {
			drop(fmt.Sprintf("%s index=%d", ref, i), techcardarchive.ReasonArchiveRowInvalid,
				"card.json carries this fold_key twice; the first row imported, this one was dropped")
			continue
		}
		seen[key] = true
		if len(plan) >= entity.TechCardPieceFoldMaxRowsPerCard {
			drop(ref, techcardarchive.ReasonArchiveRowInvalid,
				fmt.Sprintf("a card holds at most %d folds; the rows past that were dropped", entity.TechCardPieceFoldMaxRowsPerCard))
			continue
		}
		in, err := dto.TechCardPieceFoldsWriteFromPb(1, []*pb_common.TechCardPieceFold{row}, "archive")
		if err != nil {
			drop(ref, techcardarchive.ReasonArchiveRowInvalid,
				fmt.Sprintf("the fold does not validate (%s); it was dropped and the other folds imported", err.Error()))
			continue
		}
		fold := in.Folds[0]
		piece := strings.ToUpper(strings.TrimSpace(fold.PieceLineKey))
		if !pieces[piece] {
			drop(ref, techcardarchive.ReasonFoldPieceMissing,
				fmt.Sprintf("the fold is on piece %s, which is not on the card", fold.PieceLineKey))
			continue
		}
		seq := fmt.Sprintf("%s|%d", piece, fold.Seq)
		if seenSeq[seq] {
			drop(ref, techcardarchive.ReasonArchiveRowInvalid,
				fmt.Sprintf("another fold of piece %s already has seq %d; the first row imported, this one was dropped", fold.PieceLineKey, fold.Seq))
			continue
		}
		seenSeq[seq] = true
		imported++
		refs[fold.FoldKey] = entity.TechCardFoldOrderRef{PieceLineKey: fold.PieceLineKey, Seq: fold.Seq}
		if row.GetStale() {
			stale[fold.FoldKey] = true
		}
		plan = append(plan, fold)
	}
	if len(rows) > 0 {
		r.out.Counters.AddImported(techcardarchive.EntityFold, imported)
		r.out.Counters.AddSkipped(techcardarchive.EntityFold, skipped)
	}
	r.out.FoldPlan = plan
	r.out.FoldStale = stale

	// A key of a dropped fold goes silently, as the fold's own line already says so. A key no row of
	// pieceFolds names has no such line: it stays in for the rule, which reports it as unknown_fold and
	// cuts it with a line of its own, so no link leaves the card unreported.
	ops := r.out.Insert.GetOperations()
	steps := make([][]string, len(ops))
	wanted := false
	for i, op := range ops {
		seenHere := map[string]bool{}
		for _, k := range op.GetFoldKeys() {
			k = entity.NormalizeTechCardSeamKey(k)
			_, kept := refs[k]
			if seenHere[k] || (!kept && seen[k]) {
				continue
			}
			seenHere[k] = true
			steps[i] = append(steps[i], k)
			wanted = true
		}
	}
	var problems []entity.TechCardFoldLinkProblem
	if wanted {
		steps, problems = entity.CutTechCardFoldLinks(steps, refs)
	}
	for _, p := range problems {
		r.hole(techcardarchive.EntityFold, fmt.Sprintf("%s step=%d", tcimpFoldRef(p.Key), p.Step+1),
			techcardarchive.StatusDegraded, techcardarchive.ReasonFoldStepConflict,
			fmt.Sprintf("step %d of the archive cannot close this fold (%s); only the link was dropped", p.Step+1, p.Kind))
	}
	for i, op := range ops {
		if op != nil {
			op.FoldKeys = steps[i]
		}
	}
}
