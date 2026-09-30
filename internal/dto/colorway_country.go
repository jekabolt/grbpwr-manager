package dto

import (
	"strings"

	"google.golang.org/protobuf/types/known/fieldmaskpb"
)

// countryMaskPath is the normalised update_mask path of UpdateColorwayRequest.country_code
// (`country_code` from a gRPC caller, `countryCode` from a JSON one — both normalise to this).
const countryMaskPath = "countrycode"

// UpdateMaskIsCountryOnly reports whether a non-empty update_mask names `country_code` and nothing
// else (labels rework D-02). Such an UpdateColorway writes the colourway's country of origin ALONE —
// the tech card's composition label sets a missing country in place — so merchandising is neither
// required nor read, and media, tags, prices, translations and the development block are left as
// they are. Paths are normalised exactly like UpdateMaskIsDevelopmentOnly (trimmed, lower-cased,
// underscores dropped). An empty mask, or any other path next to it, keeps the existing behaviour.
func UpdateMaskIsCountryOnly(mask *fieldmaskpb.FieldMask) bool {
	paths := mask.GetPaths()
	if len(paths) == 0 {
		return false
	}
	for _, p := range paths {
		if strings.ToLower(strings.ReplaceAll(strings.TrimSpace(p), "_", "")) != countryMaskPath {
			return false
		}
	}
	return true
}
