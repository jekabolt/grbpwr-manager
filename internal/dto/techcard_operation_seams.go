package dto

import (
	"fmt"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
)

// operationSeamKeysFromPb parses TechCardOperation.seam_keys (0434): the seams a step sews, in
// sewing order. Shape only — each key a ULID, no repeats within the step, a sane bound. Whether a
// key names a seam stored on THIS card is checked by the store inside the save transaction, where
// the seam table is visible under the card row lock.
//
// Keys are normalised the way the seam RPCs normalise them, so a lower-case echo from a client
// compares equal to the stored row.
func operationSeamKeysFromPb(i int, raw []string) ([]string, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	if len(raw) > entity.TechCardOperationMaxSeamKeys {
		return nil, entity.NewFieldViolation(stepPath(i)+".seam_keys", "too_many", "",
			fmt.Sprintf("a step sews at most %d seams", entity.TechCardOperationMaxSeamKeys))
	}
	out := make([]string, 0, len(raw))
	seen := make(map[string]int, len(raw))
	for j, k := range raw {
		k = entity.NormalizeTechCardSeamKey(k)
		f := fmt.Sprintf("%s.seam_keys[%d]", stepPath(i), j)
		if !entity.ValidTechCardSeamKey(k) {
			return nil, entity.NewFieldViolation(f, "invalid", "", "a seam_key of this card (26-char ULID)")
		}
		if prev, dup := seen[k]; dup {
			return nil, entity.NewFieldViolation(f, "duplicate",
				fmt.Sprintf("%s.seam_keys[%d]", stepPath(i), prev), "a step names each seam once")
		}
		seen[k] = j
		out = append(out, k)
	}
	return out, nil
}
