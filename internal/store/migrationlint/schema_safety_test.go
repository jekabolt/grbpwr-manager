package migrationlint

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const schemaSafetyGrandfatheredMigrationMax = 432

func TestNewMigrationsKeepIndexesWithinInnoDBLimitAndDropChecksFirst(t *testing.T) {
	entries, err := os.ReadDir(migrationsDir)
	if err != nil {
		t.Fatalf("read migrations dir %s: %v", migrationsDir, err)
	}
	var migrations []namedMigration
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".sql") {
			continue
		}
		body, err := os.ReadFile(filepath.Join(migrationsDir, entry.Name()))
		if err != nil {
			t.Fatalf("read %s: %v", entry.Name(), err)
		}
		migrations = append(migrations, namedMigration{Name: entry.Name(), Content: string(body)})
	}
	hazards, err := detectSchemaHazards(migrations, schemaSafetyGrandfatheredMigrationMax)
	if err != nil {
		t.Fatal(err)
	}
	for _, hazard := range hazards {
		t.Errorf("unsafe schema change on %s: %s (%s)", hazard.Table, hazard.Kind, hazard.Statement)
	}
}

func TestCompositeIndexByteLimitIncludesDateAndCurrency(t *testing.T) {
	base := migrationSQL(`CREATE TABLE channel_spend (
  date DATE NOT NULL,
  utm_source VARCHAR(255) NOT NULL,
  utm_medium VARCHAR(255) NOT NULL,
  utm_campaign VARCHAR(255) NOT NULL,
  currency VARCHAR(10) NOT NULL
) DEFAULT CHARSET=utf8mb4;`)
	unsafe := migrationSQL(`CREATE UNIQUE INDEX uq_channel_spend
  ON channel_spend (date, utm_source, utm_medium, utm_campaign, currency);`)
	hazards, err := detectSchemaHazards([]namedMigration{
		{Name: "0432_base.sql", Content: base},
		{Name: "0433_candidate.sql", Content: unsafe},
	}, schemaSafetyGrandfatheredMigrationMax)
	if err != nil {
		t.Fatal(err)
	}
	if len(hazards) != 1 || !strings.Contains(hazards[0].Kind, "3103 bytes") {
		t.Fatalf("hazards = %+v, want one 3103-byte index violation", hazards)
	}

	safeBase := strings.ReplaceAll(base, "VARCHAR(255)", "VARCHAR(191)")
	hazards, err = detectSchemaHazards([]namedMigration{
		{Name: "0432_base.sql", Content: safeBase},
		{Name: "0433_candidate.sql", Content: unsafe},
	}, schemaSafetyGrandfatheredMigrationMax)
	if err != nil {
		t.Fatal(err)
	}
	if len(hazards) != 0 {
		t.Fatalf("0085-sized composite key should fit: %+v", hazards)
	}
}

func TestDropColumnRequiresEarlierDropCheckInSameFile(t *testing.T) {
	base := migrationSQL(`CREATE TABLE product (
  price INT,
  CONSTRAINT chk_product_price CHECK (price >= 0)
);`)
	tests := []struct {
		name       string
		candidate  string
		wantHazard bool
	}{
		{name: "check remains", candidate: "ALTER TABLE product DROP COLUMN price;", wantHazard: true},
		{name: "check dropped first", candidate: "ALTER TABLE product DROP CHECK chk_product_price;\nALTER TABLE product DROP COLUMN price;"},
		{name: "same alter ordered", candidate: "ALTER TABLE product DROP CHECK chk_product_price, DROP COLUMN price;"},
		{name: "waived with reason", candidate: "-- migrationlint:allow-drop-column-check CHECK is removed by a version-gated statement\nALTER TABLE product DROP COLUMN price;"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			hazards, err := detectSchemaHazards([]namedMigration{
				{Name: "0432_base.sql", Content: base},
				{Name: "0433_candidate.sql", Content: migrationSQL(tt.candidate)},
			}, schemaSafetyGrandfatheredMigrationMax)
			if err != nil {
				t.Fatal(err)
			}
			if (len(hazards) > 0) != tt.wantHazard {
				t.Fatalf("hazards = %+v, wantHazard=%v", hazards, tt.wantHazard)
			}
		})
	}
}

func TestDynamicDDLInPrepareInputRunsSchemaRules(t *testing.T) {
	base := migrationSQL("CREATE TABLE subscriber (email VARCHAR(800)) DEFAULT CHARSET=utf8mb4;")
	candidate := migrationSQL(`SET @s = 'ALTER TABLE subscriber ADD UNIQUE INDEX uq_subscriber_email (email)';
PREPARE stmt FROM @s;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;`)
	hazards, err := detectSchemaHazards([]namedMigration{
		{Name: "0432_base.sql", Content: base},
		{Name: "0433_candidate.sql", Content: candidate},
	}, schemaSafetyGrandfatheredMigrationMax)
	if err != nil {
		t.Fatal(err)
	}
	if len(hazards) != 1 || !strings.Contains(hazards[0].Kind, "3200 bytes") {
		t.Fatalf("dynamic DDL hazards = %+v, want one 3200-byte violation", hazards)
	}
}

func TestDynamicDropColumnInPrepareInputRunsCheckRule(t *testing.T) {
	base := migrationSQL(`CREATE TABLE product (
  price INT,
  CONSTRAINT chk_product_price CHECK (price >= 0)
);`)
	candidate := migrationSQL(`SET @ddl = 'ALTER TABLE product DROP COLUMN price';
PREPARE stmt FROM @ddl;`)
	hazards, err := detectSchemaHazards([]namedMigration{
		{Name: "0432_base.sql", Content: base},
		{Name: "0433_candidate.sql", Content: candidate},
	}, schemaSafetyGrandfatheredMigrationMax)
	if err != nil {
		t.Fatal(err)
	}
	if len(hazards) != 1 || !strings.Contains(hazards[0].Kind, "CHECK chk_product_price") {
		t.Fatalf("dynamic DROP hazards = %+v", hazards)
	}
}

func TestDropColumnAllowanceRequiresReason(t *testing.T) {
	_, err := detectSchemaHazards([]namedMigration{{
		Name:    "0433_candidate.sql",
		Content: migrationSQL("-- migrationlint:allow-drop-column-check\nALTER TABLE t DROP COLUMN c;"),
	}}, schemaSafetyGrandfatheredMigrationMax)
	if err == nil || !strings.Contains(err.Error(), "requires a non-empty reason") {
		t.Fatalf("error = %v", err)
	}
}

func TestIndexFixedWidthAccounting(t *testing.T) {
	column := schemaColumn{DataType: "decimal", Precision: 14, Scale: 2}
	if got, ok := column.indexBytes("utf8mb4", 0); !ok || got != 7 {
		t.Fatalf("DECIMAL(14,2) bytes = %d, ok=%v; want 7", got, ok)
	}
	date := schemaColumn{DataType: "date"}
	if got, _ := date.indexBytes("utf8mb4", 0); got != 3 {
		t.Fatalf("DATE bytes = %d, want 3", got)
	}
}
