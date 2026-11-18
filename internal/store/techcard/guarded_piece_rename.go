package techcard

import (
	"context"
	"fmt"
	"reflect"

	"github.com/jekabolt/grbpwr-manager/internal/dependency"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/jekabolt/grbpwr-manager/internal/store/storeutil"
)

func verifyGuardedPieceRenameUnderLock(ctx context.Context, db dependency.DB, cardID int, card *entity.TechCard, intent *entity.GuardedZeroOperationPieceRename) error {
	if _, err := entity.GuardedPieceRenamePiece(card, intent); err != nil {
		return err
	}
	// Every semantic sidecar writer takes the same card lock, including V1 writers that do not
	// advance it. Test every pattern on the card before the ordinary document rewrite can delete one.
	protection, err := storeutil.QueryNamedOne[struct {
		Attached bool `db:"attached"`
	}](ctx, db, `SELECT (
		EXISTS(SELECT 1 FROM tech_card_pattern_semantic_sidecar AS sidecar
			JOIN tech_card_size_pattern AS pattern ON pattern.id = sidecar.pattern_row_id
			WHERE pattern.tech_card_id = :id)
		OR EXISTS(SELECT 1 FROM tech_card_piece_area WHERE tech_card_id = :id)
		OR EXISTS(SELECT 1 FROM tech_card_marker WHERE tech_card_id = :id)
	) AS attached`, map[string]any{"id": cardID})
	if err != nil {
		return fmt.Errorf("check protected piece-rename attachments: %w", err)
	}
	if protection.Attached {
		return entity.ErrTechCardPieceRenameProtected
	}

	lines := entity.RollGoodsLinesOfBom(card.BomItems)
	if !matchesGuardedPieceRenamePattern(card, lines, intent) {
		return entity.ErrTechCardPieceRenameProtected
	}
	return nil
}

func matchesGuardedPieceRenamePattern(card *entity.TechCard, lines []entity.RollGoodsLine, intent *entity.GuardedZeroOperationPieceRename) bool {
	var scopeExists bool
	for _, line := range lines {
		if entity.EffectiveFabricScopeKey(line) == intent.ScopeKey {
			scopeExists = true
			break
		}
	}
	if !scopeExists {
		return false
	}
	matches := 0
	for _, row := range card.Patterns {
		if row.LineKey != intent.PatternLineKey {
			continue
		}
		if !row.SHA256.Valid || row.SHA256.String != intent.DxfSHA256 ||
			row.SavedRowRevision != intent.SavedRowRevision ||
			entity.FabricScopeIdentity(row.FabricPurpose.String, row.BomLineKey.String, lines) != intent.ScopeKey {
			return false
		}
		matches++
	}
	if matches != 1 {
		return false
	}
	for _, alias := range card.PieceDxfAliases {
		if alias.PieceLineKey == intent.PieceLineKey &&
			entity.FabricScopeIdentity(alias.FabricPurpose, alias.BomLineKey, lines) == intent.ScopeKey {
			return true
		}
	}
	return false
}

// Verify the existing full-card writer's result before its transaction commits. A legacy value
// normalized by the ordinary writer may require the card to use the ordinary edit workflow.
func matchesGuardedPieceRenameResult(before, after *entity.TechCard, intent *entity.GuardedZeroOperationPieceRename) bool {
	if after == nil || after.LockVersion != before.LockVersion+1 ||
		len(after.Operations) != 0 || len(after.Seams) != 0 || len(after.PieceFolds) != 0 ||
		len(before.Patterns) != len(after.Patterns) || len(before.Pieces) != len(after.Pieces) ||
		len(before.PieceDxfAliases) != len(after.PieceDxfAliases) {
		return false
	}
	live := *intent
	live.ExpectedName = intent.NextName
	live.SavedRowRevision++
	if _, err := entity.GuardedPieceRenamePiece(after, &live); err != nil ||
		!matchesGuardedPieceRenamePattern(after, entity.RollGoodsLinesOfBom(after.BomItems), &live) {
		return false
	}

	pieces := make(map[string]entity.TechCardPiece, len(before.Pieces))
	for _, piece := range before.Pieces {
		if piece.LineKey == intent.PieceLineKey {
			piece.Name = intent.NextName
		}
		pieces[piece.LineKey] = piece
	}
	for _, piece := range after.Pieces {
		if !reflect.DeepEqual(pieces[piece.LineKey], piece) {
			return false
		}
		delete(pieces, piece.LineKey)
	}
	aliases := make(map[entity.TechCardPieceDxfAlias]int, len(before.PieceDxfAliases))
	for _, alias := range before.PieceDxfAliases {
		aliases[alias]++
	}
	for _, alias := range after.PieceDxfAliases {
		if aliases[alias] == 0 {
			return false
		}
		aliases[alias]--
	}
	patterns := make(map[string]entity.TechCardSizePattern, len(before.Patterns))
	for _, row := range before.Patterns {
		patterns[row.LineKey] = row
	}
	for _, row := range after.Patterns {
		old, exists := patterns[row.LineKey]
		if !exists {
			return false
		}
		// Every sheet round-trips through the ordinary DTO. The row revision is the
		// only permitted difference, including on sheets outside the renamed scope.
		old.SavedRowRevision++
		if !reflect.DeepEqual(old, row) {
			return false
		}
		delete(patterns, row.LineKey)
	}
	return true
}
