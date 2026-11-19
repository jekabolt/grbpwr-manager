package techcard

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	"github.com/jekabolt/grbpwr-manager/internal/dto"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"google.golang.org/protobuf/encoding/protojson"
)

const savedAssemblyAuthoredFoldSourceDomainV1 = "tech-card-saved-assembly-authored-fold-source/1"

func buildCheckedSavedAssemblyAuthoredFoldSourceV1(
	card *entity.TechCard, complete *entity.SavedAssemblyCompleteSourceV1,
	sizeTokens map[int]string, in entity.SavedAssemblySnapshotRequest,
) (*entity.SavedAssemblyAuthoredFoldSourceV1, error) {
	sizeID := in.SelectedSizeID
	token := sizeTokens[sizeID]
	if !slices.Contains(card.SizeIds, sizeID) || token == "" || strings.TrimSpace(token) != token {
		return nil, savedAssemblyAuthoredFoldViolation("selected_size_id", "missing_size", "", "choose a current card size with a declared dictionary token")
	}
	for id, otherToken := range sizeTokens {
		if id != sizeID && otherToken == token {
			return nil, savedAssemblyAuthoredFoldViolation("selected_size_id", "ambiguous_size_token", token, "choose a rank with a unique current dictionary token")
		}
	}

	owned, err := completeSourceOwnedFolds(card, complete)
	if err != nil {
		return nil, err
	}
	source := &entity.SavedAssemblyAuthoredFoldSourceV1{
		CompleteSourceSHA256: complete.SourceSHA256,
		SelectedSizeID:       sizeID,
		SelectedSizeToken:    token,
		Folds:                owned,
	}
	wire, err := (protojson.MarshalOptions{EmitUnpopulated: true}).Marshal(dto.SavedAssemblyAuthoredFoldSourceV1ToPb(source))
	if err != nil {
		return nil, fmt.Errorf("marshal saved authored folds: %w", err)
	}
	var value map[string]any
	if err := json.Unmarshal(wire, &value); err != nil {
		return nil, fmt.Errorf("decode saved authored fold wire JSON: %w", err)
	}
	delete(value, "sourceSha256")
	delete(value, "canonicalJson")
	canonical, err := savedAssemblyCanonicalJSON(value)
	if err != nil {
		return nil, err
	}
	sum := sha256.Sum256(append([]byte(savedAssemblyAuthoredFoldSourceDomainV1+"\x00"), canonical...))
	source.SourceSHA256 = hex.EncodeToString(sum[:])
	source.CanonicalJSON = canonical
	if in.ExpectedAuthoredFoldSourceSHA256 != "" && in.ExpectedAuthoredFoldSourceSHA256 != source.SourceSHA256 {
		return nil, fmt.Errorf("%w: %w", entity.ErrTechCardSavedAssemblyMoved, entity.NewFieldViolation(
			"expected_authored_fold_source_sha256", "authored_folds_moved", "", "reload the current authored folds"))
	}
	return source, nil
}

func completeSourceOwnedFolds(card *entity.TechCard, complete *entity.SavedAssemblyCompleteSourceV1) ([]entity.SavedAssemblyAuthoredFoldV1, error) {
	if len(card.PieceFolds) > entity.TechCardPieceFoldMaxRowsPerCard {
		return nil, savedAssemblyAuthoredFoldViolation("piece_folds", "too_many_folds", "", "reduce the number of stored fold rows")
	}
	byKey := make(map[string]entity.TechCardPieceFold, len(card.PieceFolds))
	refs := make(map[string]entity.TechCardFoldOrderRef, len(card.PieceFolds))
	for _, fold := range card.PieceFolds {
		key := entity.NormalizeTechCardSeamKey(fold.FoldKey)
		if _, duplicate := byKey[key]; duplicate {
			return nil, savedAssemblyAuthoredFoldViolation("piece_folds", "duplicate_fold_row", key, "repair the repeated stored fold key")
		}
		byKey[key] = fold
		refs[key] = entity.TechCardFoldOrderRef{PieceLineKey: fold.PieceLineKey, Seq: fold.Seq}
	}
	steps := make([][]string, len(card.Operations))
	for i, op := range card.Operations {
		if len(op.FoldKeys) > entity.TechCardOperationMaxFoldKeys {
			return nil, savedAssemblyAuthoredFoldViolation(fmt.Sprintf("operations[%d].fold_keys", i),
				"too_many_folds", "", "reduce the number of fold links on the operation")
		}
		steps[i] = op.FoldKeys
	}
	if problems := entity.CheckTechCardFoldLinks(steps, refs); len(problems) > 0 {
		p := problems[0]
		return nil, fmt.Errorf("%w: %w", entity.ErrTechCardSavedAssemblyUnsupported, foldLinkViolation(p, nil))
	}

	aliased := make(map[string]bool, len(complete.Aliases))
	for _, alias := range complete.Aliases {
		aliased[alias.PieceLineKey] = true
	}
	owned := make([]entity.SavedAssemblyAuthoredFoldV1, 0, len(card.PieceFolds))
	shapes := entity.TechCardPieceFoldsWrite{TechCardId: card.Id}
	for i, op := range card.Operations {
		for j, key := range op.FoldKeys {
			fold := byKey[entity.NormalizeTechCardSeamKey(key)]
			field := fmt.Sprintf("operations[%d].fold_keys[%d]", i, j)
			if !entity.ValidTechCardSeamKey(key) || key != fold.FoldKey || fold.TechCardId != card.Id || !aliased[fold.PieceLineKey] {
				return nil, savedAssemblyAuthoredFoldViolation(field, "invalid_fold_ownership", key, "select a canonical fold on a piece with a saved DXF scope")
			}
			if fold.Stale {
				return nil, savedAssemblyAuthoredFoldViolation(field, "stale_fold", key, "reconfirm the fold against the current DXF source")
			}
			if fold.SizeId.Valid || !entity.ValidPatternSemanticSidecarSHA256(fold.SourceFingerprint) {
				return nil, savedAssemblyAuthoredFoldViolation(field, "unsupported_fold_source", key, "select a current fold with a verified source fingerprint")
			}
			owned = append(owned, entity.SavedAssemblyAuthoredFoldV1{
				OperationNumber: op.OperationNumber.Int32, OperationOrder: i, Fold: fold,
			})
			shapes.Folds = append(shapes.Folds, fold.TechCardPieceFoldInput)
		}
	}
	// The validator operates on copies of the write DTOs. Hash the original persisted values.
	if len(shapes.Folds) > 0 {
		if err := entity.ValidateTechCardPieceFoldsWrite(&shapes); err != nil {
			return nil, fmt.Errorf("%w: %w", entity.ErrTechCardSavedAssemblyUnsupported, err)
		}
	}
	return owned, nil
}

func savedAssemblyAuthoredFoldViolation(field, reason, conflict, guidance string) error {
	return fmt.Errorf("%w: %w", entity.ErrTechCardSavedAssemblyUnsupported,
		entity.NewFieldViolation(field, reason, conflict, guidance))
}
