package migrationlint

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// retroactiveGrandfatheredMigrationMax is the highest migration present on
// beta when this rule was introduced. The first migration governed by it is
// 0433; older immutable files are exercised by replay instead.
const retroactiveGrandfatheredMigrationMax = 432

func TestNewMigrationsAvoidRetroactiveConstraints(t *testing.T) {
	entries, err := os.ReadDir(migrationsDir)
	if err != nil {
		t.Fatalf("read migrations dir %s: %v", migrationsDir, err)
	}

	checked := 0
	for _, entry := range entries {
		match := migrationPrefixRe.FindStringSubmatch(entry.Name())
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".sql") || match == nil {
			continue
		}
		number, err := strconv.Atoi(match[1])
		if err != nil || number <= retroactiveGrandfatheredMigrationMax {
			continue
		}
		checked++
		body, err := os.ReadFile(filepath.Join(migrationsDir, entry.Name()))
		if err != nil {
			t.Fatalf("read %s: %v", entry.Name(), err)
		}
		allowed, err := retroactiveAllowance(string(body))
		if err != nil {
			t.Errorf("%s: %v", entry.Name(), err)
			continue
		}
		if allowed {
			continue
		}
		hazards, err := detectRetroactiveHazards(string(body))
		if err != nil {
			t.Errorf("%s: %v", entry.Name(), err)
			continue
		}
		for _, hazard := range hazards {
			t.Errorf("%s: unsafe ALTER of existing table %s: %s (%s); backfill/validate first or add '-- migrationlint:allow-retroactive <reason>' after manual review",
				entry.Name(), hazard.Table, hazard.Kind, hazard.Statement)
		}
	}
	t.Logf("linted %d migration(s) numbered above retroactive-safety baseline %04d", checked, retroactiveGrandfatheredMigrationMax)
}

func TestRetroactiveHazardDetector(t *testing.T) {
	tests := []struct {
		name string
		sql  string
		want []string
	}{
		{
			name: "check with and without constraint name",
			sql: migrationSQL(`ALTER TABLE product ADD CHECK (price > 0);
ALTER TABLE product ADD CONSTRAINT product_price_positive CHECK (price > 0);`),
			want: []string{"CHECK", "CHECK"},
		},
		{
			name: "not null modify and change",
			sql:  migrationSQL("ALTER TABLE customer MODIFY email VARCHAR(255) NOT NULL, CHANGE old_code code VARCHAR(20) NOT NULL;"),
			want: []string{"NOT NULL", "NOT NULL"},
		},
		{
			name: "unique foreign key and enum",
			sql: migrationSQL(`ALTER TABLE order_item
  ADD UNIQUE KEY uq_order_sku (order_id, sku),
  ADD CONSTRAINT fk_order_item_order FOREIGN KEY (order_id) REFERENCES customer_order(id),
  MODIFY status ENUM('new', 'paid') NULL;`),
			want: []string{"UNIQUE", "FOREIGN KEY", "ENUM"},
		},
		{
			name: "new table in same file",
			sql:  migrationSQL("CREATE TABLE IF NOT EXISTS fresh (id INT);\nALTER TABLE fresh ADD CHECK (id > 0);"),
		},
		{
			name: "comments strings and nullable widening ignored",
			sql: migrationSQL(`-- ALTER TABLE product ADD CHECK (price > 0);
SET @ddl = 'ALTER TABLE product MODIFY price INT NOT NULL';
/* ALTER TABLE product ADD UNIQUE (sku); */
ALTER TABLE product MODIFY description VARCHAR(500) NULL;`),
		},
		{
			name: "comma within check stays in one clause",
			sql:  migrationSQL("ALTER TABLE product ADD CONSTRAINT ck CHECK (kind IN ('a', 'b')), ADD COLUMN note TEXT;"),
			want: []string{"CHECK"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			hazards, err := detectRetroactiveHazards(tt.sql)
			if err != nil {
				t.Fatal(err)
			}
			if len(hazards) != len(tt.want) {
				t.Fatalf("got %d hazards (%+v), want %d", len(hazards), hazards, len(tt.want))
			}
			for i, want := range tt.want {
				if !strings.Contains(hazards[i].Kind, want) {
					t.Errorf("hazard %d kind %q does not contain %q", i, hazards[i].Kind, want)
				}
			}
		})
	}
}

func TestRetroactiveAllowanceRequiresReason(t *testing.T) {
	tests := []struct {
		name      string
		sql       string
		wantAllow bool
		wantErr   bool
	}{
		{name: "reason", sql: "-- migrationlint:allow-retroactive legacy rows are backfilled above\n", wantAllow: true},
		{name: "missing reason", sql: "-- migrationlint:allow-retroactive\n", wantErr: true},
		{name: "string is not annotation", sql: "SELECT '-- migrationlint:allow-retroactive fake';\n"},
		{name: "block comment is not annotation", sql: "/* -- migrationlint:allow-retroactive fake */\n"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			allowed, err := retroactiveAllowance(tt.sql)
			if (err != nil) != tt.wantErr || allowed != tt.wantAllow {
				t.Fatalf("allowed=%v err=%v, want allowed=%v err=%v", allowed, err, tt.wantAllow, tt.wantErr)
			}
		})
	}
}

func migrationSQL(up string) string {
	return "-- +migrate Up\n" + up + "\n-- +migrate Down\nSELECT 1;\n"
}
