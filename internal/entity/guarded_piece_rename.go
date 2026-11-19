package entity

import (
	"errors"
	"strings"
)

var ErrTechCardAttachedPieceRenameUnsupported = errors.New("attached piece rename requires one current V10 and a unique saved piece without a technical callout")

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

func GuardedAttachedPieceRenamePiece(card *TechCard, intent *GuardedZeroOperationPieceRename) (TechCardPiece, error) {
	if card == nil || len(card.Operations) == 0 || intent == nil || intent.Attached == nil || intent.ScopeKey != "main" {
		return TechCardPiece{}, ErrTechCardAttachedPieceRenameUnsupported
	}
	var piece TechCardPiece
	for _, candidate := range card.Pieces {
		if strings.EqualFold(candidate.LineKey, intent.PieceLineKey) {
			if piece.Id != 0 || candidate.Id <= 0 || candidate.LineKey != intent.PieceLineKey ||
				candidate.Name != intent.ExpectedName || candidate.CalloutNumber.Valid {
				return TechCardPiece{}, ErrTechCardAttachedPieceRenameUnsupported
			}
			piece = candidate
			continue
		}
		name := strings.TrimSpace(candidate.Name)
		if strings.EqualFold(name, strings.TrimSpace(intent.ExpectedName)) || strings.EqualFold(name, intent.NextName) {
			return TechCardPiece{}, ErrTechCardAttachedPieceRenameUnsupported
		}
	}
	if piece.Id == 0 {
		return TechCardPiece{}, ErrTechCardAttachedPieceRenameUnsupported
	}
	return piece, nil
}
