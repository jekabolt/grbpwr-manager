package entity

import "strings"

// GuardedPieceRenamePiece returns the exact saved piece before the document write.
func GuardedPieceRenamePiece(card *TechCard, intent *GuardedZeroOperationPieceRename) (int, error) {
	if card == nil || len(card.Operations) != 0 || len(card.Seams) != 0 || len(card.PieceFolds) != 0 {
		return 0, ErrTechCardPieceRenameProtected
	}
	index := -1
	for i, piece := range card.Pieces {
		if !strings.EqualFold(piece.LineKey, intent.PieceLineKey) {
			continue
		}
		if index != -1 || piece.LineKey != intent.PieceLineKey || piece.Name != intent.ExpectedName {
			return 0, ErrTechCardPieceRenameProtected
		}
		index = i
	}
	if index < 0 {
		return 0, ErrTechCardPieceRenameProtected
	}
	return index, nil
}
