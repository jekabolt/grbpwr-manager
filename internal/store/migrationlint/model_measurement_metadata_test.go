package migrationlint

import (
	"strings"
	"testing"
)

func TestModelMeasurementMetadataMigrationIsAdditiveAndRetrySafe(t *testing.T) {
	body := readMigrationFile(t, "0470_model_measurement_metadata.sql")
	for _, required := range []string{
		"metadata_schema_version SMALLINT UNSIGNED NULL",
		"measurement_source VARCHAR(24)",
		"measurement_protocol_id VARCHAR(128)",
		"CONSTRAINT chk_model_measurement_metadata_v1",
		"metadata_schema_version IS NOT NULL AND measurement_source IS NOT NULL AND measurement_protocol_id IS NOT NULL",
		"information_schema.COLUMNS",
		"information_schema.TABLE_CONSTRAINTS",
		"migrationlint:allow-retroactive all three columns are added nullable",
	} {
		if !strings.Contains(body, required) {
			t.Errorf("0470 measurement metadata migration is missing %q", required)
		}
	}
	for _, forbidden := range []string{
		"MODIFY measurement_name",
		"CHANGE measurement_name",
		"UPDATE model_measurement",
		"DELETE FROM model_measurement",
	} {
		if strings.Contains(body, forbidden) {
			t.Errorf("0470 must not rewrite legacy measurements: found %q", forbidden)
		}
	}
}
