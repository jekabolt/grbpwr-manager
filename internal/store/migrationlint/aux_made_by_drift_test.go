package migrationlint

import (
	"testing"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
)

// TestAuxMadeByDBCheckNoDrift: entity.ValidAuxMadeBy <-> DB CHECK chk_tech_card_aux_made_by (0456).
func TestAuxMadeByDBCheckNoDrift(t *testing.T) {
	content := readMigrationFile(t, "0456_aux_made_by_and_bom_consumption.sql")
	dbValues := extractDBEnumValues(t, content, "aux_made_by REGEXP", 100)
	assertSameSet(t, "AuxMadeBy", dbValues, mapKeysAsStrings(entity.ValidAuxMadeBy))
}
