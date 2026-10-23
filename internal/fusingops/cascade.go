package fusingops

import (
	"database/sql"
	"strings"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
)

const generatedFusingPatternPrefix = "Fusing · generated · "

func bomSections(tc *entity.TechCardInsert) (map[string]entity.TechCardBomSection, []string) {
	sections := make(map[string]entity.TechCardBomSection)
	var interlinings []string
	if tc == nil {
		return sections, interlinings
	}
	for i := range tc.BomItems {
		key := strings.TrimSpace(tc.BomItems[i].LineKey)
		if key == "" {
			continue
		}
		sections[key] = tc.BomItems[i].Section
		if tc.BomItems[i].Section == entity.BomSectionInterlining {
			interlinings = append(interlinings, key)
		}
	}
	return sections, interlinings
}

func clearPieceFusing(piece *entity.TechCardPiece) {
	piece.Fused = false
	piece.FusingOmitted = false
	piece.FusingBomOmitted = false
	piece.FusingEdgesOmitted = false
	piece.FusingZoneOmitted = false
	piece.FusingInsetMmOmitted = false
	piece.FusingInsetNetOmitted = false
	piece.FusingGrainOmitted = false
	piece.FusingDartsOmitted = false
	piece.NormalizeFusing()
}

func filterBomRefs(op *entity.TechCardOperation, removed map[string]bool) {
	op.BomIds = nil
	op.BomLineKeys = filterStrings(op.BomLineKeys, func(key string) bool {
		return !removed[strings.TrimSpace(key)]
	})
	quantities := op.BomQuantities[:0]
	for _, qty := range op.BomQuantities {
		if !removed[strings.TrimSpace(qty.LineKey)] {
			quantities = append(quantities, qty)
		}
	}
	op.BomQuantities = quantities
}

func clearIssueRefs(issues []entity.TechCardIssue, deleted map[int32]bool) {
	for i := range issues {
		if number := issues[i].OperationNumber; number.Valid && deleted[number.Int32] {
			issues[i].OperationNumber = sql.NullInt32{}
		}
	}
}

func patternMatches(a, b entity.TechCardSizePattern) bool {
	if strings.TrimSpace(a.LineKey) != "" && strings.TrimSpace(b.LineKey) != "" {
		return strings.TrimSpace(a.LineKey) == strings.TrimSpace(b.LineKey)
	}
	return a.SizeId == b.SizeId && a.URL == b.URL
}

// Deleting the line removes it from the wire-visible BOM chips before the private legacy identity
// can be matched by its old shape. Match that one deliberate edit against stored grouped automatic
// rows so a reviewed (non-draft) legacy step is deleted too, rather than reclassified as manual.
func restoreRemovedLineAutoIdentities(stored, incoming *entity.TechCardInsert, removed map[string]bool) {
	for i := range stored.Operations {
		old := &stored.Operations[i]
		if old.OperationType != entity.OpTypeFusing || !old.AutoFusingLineKey.Valid ||
			!removed[strings.TrimSpace(old.AutoFusingLineKey.String)] {
			continue
		}
		already := false
		for j := range incoming.Operations {
			if incoming.Operations[j].AutoFusingLineKey == old.AutoFusingLineKey {
				already = true
				break
			}
		}
		if already {
			continue
		}
		withoutLine := *old
		filterBomRefs(&withoutLine, removed)
		var candidates []int
		for j := range incoming.Operations {
			candidate := &incoming.Operations[j]
			if candidate.OperationType == entity.OpTypeFusing && !candidate.AutoFusingLineKey.Valid &&
				!candidate.AutoFusingPieceKey.Valid && sameFusingOpContent(&withoutLine, candidate) {
				candidates = append(candidates, j)
			}
		}
		pick := -1
		if len(candidates) == 1 {
			pick = candidates[0]
		} else if old.OperationNumber.Valid {
			for _, j := range candidates {
				if incoming.Operations[j].OperationNumber == old.OperationNumber {
					if pick >= 0 {
						pick = -1
						break
					}
					pick = j
				}
			}
		}
		if pick >= 0 {
			incoming.Operations[pick].AutoFusingLineKey = old.AutoFusingLineKey
		}
	}
}

// CascadeRemovedInterlining makes deletion of an INTERLINING BOM line one document mutation. It
// runs before validation and again after stale-client field carries: piece fusing, automatic steps,
// direct sheets/aliases and every material link leave together, while manual FUSING instructions
// survive with only the deleted material link removed.
func CascadeRemovedInterlining(stored, incoming *entity.TechCardInsert) bool {
	if stored == nil || incoming == nil {
		return false
	}
	storedSections, storedInterlinings := bomSections(stored)
	incomingSections, _ := bomSections(incoming)
	removed := make(map[string]bool)
	for key, section := range storedSections {
		if section == entity.BomSectionInterlining && incomingSections[key] != entity.BomSectionInterlining {
			removed[key] = true
		}
	}
	if len(removed) == 0 {
		return false
	}

	// The wire does not expose automatic identities. Restore them before deciding which FUSING rows
	// are server-owned; otherwise an edited/reviewed automatic step is indistinguishable from manual.
	matchAutoFusingOps(stored, incoming)
	restoreRemovedLineAutoIdentities(stored, incoming, removed)

	storedPieces := make(map[string]*entity.TechCardPiece, len(stored.Pieces))
	for i := range stored.Pieces {
		storedPieces[strings.TrimSpace(stored.Pieces[i].LineKey)] = &stored.Pieces[i]
	}
	affectedPieces := make(map[string]bool)
	soleStored := ""
	if len(storedInterlinings) == 1 {
		soleStored = storedInterlinings[0]
	}
	changed := false
	for i := range incoming.Pieces {
		piece := &incoming.Pieces[i]
		pieceKey := strings.TrimSpace(piece.LineKey)
		selected := strings.TrimSpace(piece.FusingBomLineKey)
		// A deliberate rebind to another live INTERLINING line wins over the cascade.
		if selected == "" || incomingSections[selected] != entity.BomSectionInterlining {
			storedPiece := storedPieces[pieceKey]
			storedSelected := ""
			storedImplicit := false
			if storedPiece != nil {
				storedSelected = strings.TrimSpace(storedPiece.FusingBomLineKey)
				storedImplicit = storedPiece.Fused && storedSelected == "" && removed[soleStored]
			}
			if removed[selected] || removed[storedSelected] || storedImplicit ||
				(piece.Fused && selected == "" && removed[soleStored]) {
				affectedPieces[pieceKey] = true
				clearPieceFusing(piece)
				changed = true
			}
		}
		for mi := range piece.Materials {
			material := &piece.Materials[mi]
			if removed[strings.TrimSpace(material.FusingBomLineKey)] {
				material.FusingBomLineKey = ""
				material.FusingBomItemId = sql.NullInt64{}
				material.FusingBomItemIndex = sql.NullInt32{}
				changed = true
			}
		}
	}

	deletedNumbers := make(map[int32]bool)
	operations := make([]entity.TechCardOperation, 0, len(incoming.Operations))
	for i := range incoming.Operations {
		op := incoming.Operations[i]
		filterBomRefs(&op, removed)
		deleteAuto := op.OperationType == entity.OpTypeFusing &&
			((op.AutoFusingLineKey.Valid && removed[strings.TrimSpace(op.AutoFusingLineKey.String)]) ||
				(op.AutoFusingPieceKey.Valid && affectedPieces[strings.TrimSpace(op.AutoFusingPieceKey.String)]))
		if deleteAuto {
			if op.OperationNumber.Valid {
				deletedNumbers[op.OperationNumber.Int32] = true
			}
			changed = true
			continue
		}
		operations = append(operations, op)
	}
	incoming.Operations = operations
	clearIssueRefs(incoming.Issues, deletedNumbers)

	patterns := make([]entity.TechCardSizePattern, 0, len(incoming.Patterns))
	for _, pattern := range incoming.Patterns {
		boundToRemoved := pattern.BomLineKey.Valid && removed[strings.TrimSpace(pattern.BomLineKey.String)]
		if !pattern.BomLineKey.Valid {
			for _, old := range stored.Patterns {
				if patternMatches(pattern, old) && old.BomLineKey.Valid && removed[strings.TrimSpace(old.BomLineKey.String)] {
					boundToRemoved = true
					break
				}
			}
		}
		if boundToRemoved {
			changed = true
			continue
		}
		patterns = append(patterns, pattern)
	}
	incoming.Patterns = patterns

	if !incoming.PieceDxfAliasesSet {
		incoming.PieceDxfAliases = append([]entity.TechCardPieceDxfAlias(nil), stored.PieceDxfAliases...)
		incoming.PieceDxfAliasesSet = true
	}
	aliases := incoming.PieceDxfAliases[:0]
	for _, alias := range incoming.PieceDxfAliases {
		if removed[strings.TrimSpace(alias.BomLineKey)] {
			changed = true
			continue
		}
		aliases = append(aliases, alias)
	}
	incoming.PieceDxfAliases = aliases
	return changed
}

// DropDanglingFusingRefs is a read-model repair for cards already corrupted in production. It does
// not write the database; it omits only fusing-owned references whose BOM line no longer exists, so
// the card can load and its next ordinary save converges the stored document.
func DropDanglingFusingRefs(tc *entity.TechCardInsert) bool {
	if tc == nil {
		return false
	}
	sections, interlinings := bomSections(tc)
	targets := make(map[string]bool)
	for i := range tc.Pieces {
		piece := &tc.Pieces[i]
		if key := strings.TrimSpace(piece.FusingBomLineKey); key != "" && sections[key] != entity.BomSectionInterlining {
			targets[key] = true
		}
		for mi := range piece.Materials {
			key := strings.TrimSpace(piece.Materials[mi].FusingBomLineKey)
			if key != "" && sections[key] != entity.BomSectionInterlining {
				targets[key] = true
			}
		}
	}
	for i := range tc.Operations {
		op := &tc.Operations[i]
		if op.OperationType != entity.OpTypeFusing {
			continue
		}
		if op.AutoFusingLineKey.Valid {
			key := strings.TrimSpace(op.AutoFusingLineKey.String)
			if key != "" && sections[key] != entity.BomSectionInterlining {
				targets[key] = true
			}
		}
		for _, key := range op.BomLineKeys {
			key = strings.TrimSpace(key)
			if key != "" {
				if _, live := sections[key]; !live {
					targets[key] = true
				}
			}
		}
	}
	for _, pattern := range tc.Patterns {
		key := strings.TrimSpace(pattern.BomLineKey.String)
		if pattern.BomLineKey.Valid && key != "" {
			if _, live := sections[key]; !live && pattern.Name.Valid && strings.HasPrefix(pattern.Name.String, generatedFusingPatternPrefix) {
				targets[key] = true
			}
		}
	}

	changed := false
	affectedPieces := make(map[string]bool)
	for i := range tc.Pieces {
		piece := &tc.Pieces[i]
		key := strings.TrimSpace(piece.FusingBomLineKey)
		if targets[key] || (piece.Fused && key == "" && len(interlinings) == 0) {
			affectedPieces[strings.TrimSpace(piece.LineKey)] = true
			clearPieceFusing(piece)
			changed = true
		}
		for mi := range piece.Materials {
			material := &piece.Materials[mi]
			if targets[strings.TrimSpace(material.FusingBomLineKey)] {
				material.FusingBomLineKey = ""
				material.FusingBomItemId = sql.NullInt64{}
				material.FusingBomItemIndex = sql.NullInt32{}
				changed = true
			}
		}
	}

	deletedNumbers := make(map[int32]bool)
	operations := make([]entity.TechCardOperation, 0, len(tc.Operations))
	for i := range tc.Operations {
		op := tc.Operations[i]
		filterBomRefs(&op, targets)
		deleteAuto := op.OperationType == entity.OpTypeFusing &&
			((op.AutoFusingLineKey.Valid && targets[strings.TrimSpace(op.AutoFusingLineKey.String)]) ||
				(op.AutoFusingPieceKey.Valid && affectedPieces[strings.TrimSpace(op.AutoFusingPieceKey.String)]))
		if deleteAuto {
			if op.OperationNumber.Valid {
				deletedNumbers[op.OperationNumber.Int32] = true
			}
			changed = true
			continue
		}
		operations = append(operations, op)
	}
	tc.Operations = operations
	clearIssueRefs(tc.Issues, deletedNumbers)

	patterns := tc.Patterns[:0]
	for _, pattern := range tc.Patterns {
		if pattern.BomLineKey.Valid && targets[strings.TrimSpace(pattern.BomLineKey.String)] {
			changed = true
			continue
		}
		patterns = append(patterns, pattern)
	}
	tc.Patterns = patterns
	aliases := tc.PieceDxfAliases[:0]
	for _, alias := range tc.PieceDxfAliases {
		if targets[strings.TrimSpace(alias.BomLineKey)] {
			changed = true
			continue
		}
		aliases = append(aliases, alias)
	}
	tc.PieceDxfAliases = aliases
	return changed
}
