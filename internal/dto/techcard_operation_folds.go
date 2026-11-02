package dto

import (
	"fmt"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
)

// operationFoldKeysFromPb parses TechCardOperation.fold_keys (0444): the piece folds that close on a
// step. Shape only — each key a ULID, no repeats within the step, a sane bound. Whether a key names a
// fold of THIS card, and the rule over the whole card, are the store's check inside the save
// transaction, where the fold table is visible under the card row lock.
//
// Keys are normalised the way the fold RPCs normalise them, so a lower-case echo from a client
// compares equal to the stored row.
func operationFoldKeysFromPb(i int, raw []string) ([]string, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	if len(raw) > entity.TechCardOperationMaxFoldKeys {
		return nil, entity.NewFieldViolation(stepPath(i)+".fold_keys", "too_many", "",
			fmt.Sprintf("a step closes at most %d folds", entity.TechCardOperationMaxFoldKeys))
	}
	out := make([]string, 0, len(raw))
	seen := make(map[string]int, len(raw))
	for j, k := range raw {
		k = entity.NormalizeTechCardSeamKey(k)
		f := fmt.Sprintf("%s.fold_keys[%d]", stepPath(i), j)
		if !entity.ValidTechCardSeamKey(k) {
			return nil, entity.NewFieldViolation(f, "invalid", "", "a fold_key of this card (26-char ULID)")
		}
		if prev, dup := seen[k]; dup {
			return nil, entity.NewFieldViolation(f, "duplicate",
				fmt.Sprintf("%s.fold_keys[%d]", stepPath(i), prev), "a step names each fold once")
		}
		seen[k] = j
		out = append(out, k)
	}
	return out, nil
}
