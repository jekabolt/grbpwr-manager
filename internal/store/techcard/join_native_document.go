package techcard

import (
	"context"

	"github.com/jekabolt/grbpwr-manager/internal/dependency"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/jekabolt/grbpwr-manager/internal/patternjoin"
)

// Called by the Join signature owner only while its ordinary document transaction
// holds the card's FOR UPDATE lock. Actual alias groups come from stored rows.
func checkNativeJoinDocumentUnderLock(ctx context.Context, rep dependency.Repository, doc patternjoin.Document, selection patternjoin.Selection) error {
	card, err := rep.TechCards().GetTechCardById(ctx, doc.Row.TechCardID)
	if err != nil {
		return err
	}
	want, err := nativeJoinRetirement(card, doc, selection)
	if err != nil {
		return err
	}
	_, err = readJoinRetirementUnderLock(ctx, rep, want)
	return err
}
func nativeJoinRetirement(card *entity.TechCard, doc patternjoin.Document, selection patternjoin.Selection) (joinRetirement, error) {
	want := joinRetirement{row: doc.Row, assemblyRevision: doc.AssemblyRevision, completeSourceSHA256: doc.CompleteSourceSHA256, resultLineKey: doc.ResultLineKey, resultBlock: selection.Result}
	if card == nil || card.Id != doc.Row.TechCardID || card.Id != selection.CardID || card.LockVersion != doc.Row.CardLockVersion {
		return joinRetirement{}, entity.ErrTechCardSavedAssemblyMoved
	}
	lines := entity.RollGoodsLinesOfBom(card.BomItems)
	matched := 0
	for _, l := range lines {
		if l.LineKey == selection.BOMLineKey && l.Purpose == selection.FabricPurpose && entity.EffectiveFabricScopeKey(l) == doc.Row.ScopeKey {
			matched++
		}
	}
	if matched != 1 {
		return joinRetirement{}, blockedJoin("native manifest material differs from current BOM")
	}
	for i, key := range []string{doc.FixedLineKey, doc.MovingLineKey} {
		id := []string{selection.Fixed, selection.Moving}[i]
		want.inputs[i].lineKey = key
		for _, a := range card.PieceDxfAliases {
			if a.PieceLineKey != key {
				continue
			}
			if entity.FabricScopeIdentity(a.FabricPurpose, a.BomLineKey, lines) != doc.Row.ScopeKey || (a.BlockName != id && a.BlockName != id+"_"+selection.SizeToken) {
				return joinRetirement{}, blockedJoin("original native identity differs from stored alias")
			}
			want.inputs[i].blockNames = append(want.inputs[i].blockNames, a.BlockName)
		}
		if len(want.inputs[i].blockNames) == 0 {
			return joinRetirement{}, blockedJoin("missing original native alias")
		}
	}
	return want, nil
}
