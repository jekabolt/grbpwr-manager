package migreplay

import (
	"database/sql"
	"errors"
	"reflect"
	"testing"

	"github.com/go-sql-driver/mysql"
)

func TestEnumValues(t *testing.T) {
	got := enumValues(`enum('new','can''t','back\\slash')`)
	want := []string{"new", "can't", `back\slash`}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("enumValues() = %#v, want %#v", got, want)
	}
}

func TestBoundariesCoverRetroactiveFailureClasses(t *testing.T) {
	number := column{Name: "quantity", DataType: "int", ColumnType: "int", Nullable: true}
	gotNumber := boundaries(number)
	for _, want := range []any{nil, int64(0), int64(-1), int64(2147483647)} {
		if !containsBoundary(gotNumber, want) {
			t.Errorf("numeric boundaries %#v do not contain %#v", gotNumber, want)
		}
	}

	text := column{Name: "code", DataType: "varchar", ColumnType: "varchar(4)", CharMax: nullInt64(4)}
	gotText := boundaries(text)
	if !containsBoundary(gotText, "") || !containsBoundary(gotText, "xxxx") {
		t.Errorf("text boundaries %#v do not include empty and declared maximum", gotText)
	}
}

func TestBuildRowKeepsFutureDuplicateWhileVaryingExistingUnique(t *testing.T) {
	columns := []column{
		{Name: "id", DataType: "int", ColumnType: "int"},
		{Name: "legacy_code", DataType: "varchar", ColumnType: "varchar(30)", CharMax: nullInt64(30)},
		{Name: "quantity", DataType: "int", ColumnType: "int"},
	}
	indexes := []uniqueIndex{{Name: "PRIMARY", Columns: []string{"id"}}}
	template := map[string]any{"id": int64(1), "legacy_code": []byte("same"), "quantity": int64(5)}
	first := buildRow(columns, indexes, template, nil, nil, nil, "quantity", int64(0), 1)
	second := buildRow(columns, indexes, template, nil, nil, nil, "quantity", int64(0), 2)
	if first["id"] == second["id"] {
		t.Fatal("existing unique key was not varied")
	}
	if string(first["legacy_code"].([]byte)) != "same" || string(second["legacy_code"].([]byte)) != "same" {
		t.Fatal("non-unique future key candidate should remain duplicated")
	}
	if first["quantity"] != int64(0) || second["quantity"] != int64(0) {
		t.Fatal("target boundary should remain duplicated")
	}
}

func TestBuildRowOverridesAUniqueDefault(t *testing.T) {
	columns := []column{
		{Name: "code", DataType: "varchar", ColumnType: "varchar(30)", CharMax: nullInt64(30), Default: sql.NullString{String: "same", Valid: true}},
		{Name: "quantity", DataType: "int", ColumnType: "int"},
	}
	indexes := []uniqueIndex{{Name: "uq_code", Columns: []string{"code"}}}
	first := buildRow(columns, indexes, nil, nil, nil, nil, "quantity", int64(0), 1)
	second := buildRow(columns, indexes, nil, nil, nil, nil, "quantity", int64(0), 2)
	if first["code"] == nil || first["code"] == second["code"] {
		t.Fatalf("unique default was not replaced with distinct values: %#v %#v", first, second)
	}
}

func TestCheckHintsBuildValidBaselinesAndProtectFiniteUniqueColumns(t *testing.T) {
	columns := []column{
		{Name: "name", DataType: "varchar", ColumnType: "varchar(20)", CharMax: nullInt64(20)},
		{Name: "sku_system", DataType: "varchar", ColumnType: "varchar(16)", CharMax: nullInt64(16)},
		{Name: "sku_ord", DataType: "smallint", ColumnType: "smallint unsigned"},
		{Name: "archive_code", DataType: "varchar", ColumnType: "varchar(12)", CharMax: nullInt64(12)},
	}
	hints := inferCheckHints(columns, []string{
		"(binary `sku_system` in (_utf8mb4'apparel',_utf8mb4'shoe'))",
		"regexp_like(`archive_code`,_utf8mb4'^AR[0-9A-Z]{1,10}$','c')",
	})
	if !reflect.DeepEqual(hints["sku_system"], []any{"apparel", "shoe"}) {
		t.Fatalf("sku_system hints = %#v", hints["sku_system"])
	}
	if !reflect.DeepEqual(hints["archive_code"], []any{"AR1"}) {
		t.Fatalf("archive_code hints = %#v", hints["archive_code"])
	}

	indexes := []uniqueIndex{{Name: "uniq_size", Columns: []string{"sku_system", "sku_ord"}}}
	row := buildRow(columns, indexes, nil, hints, nil, nil, "name", "boundary", 7)
	if row["sku_system"] != "apparel" {
		t.Fatalf("checked vocabulary was overwritten: %#v", row)
	}
	if row["sku_ord"] == 1 {
		t.Fatalf("unconstrained composite-key member was not varied: %#v", row)
	}
	if row["archive_code"] != "AR1" {
		t.Fatalf("REGEXP_LIKE hint not used: %#v", row)
	}
}

func TestCheckHintsUseMySQLNormalizedClauses(t *testing.T) {
	columns := []column{
		{Name: "sku_ord", DataType: "smallint", ColumnType: "smallint unsigned"},
		{Name: "sku_system", DataType: "varchar", ColumnType: "varchar(16)", Default: sql.NullString{String: "", Valid: true}},
		{Name: "code", DataType: "varchar", ColumnType: "varchar(12)", Default: sql.NullString{String: "", Valid: true}},
		{Name: "label_type", DataType: "varchar", ColumnType: "varchar(16)", Default: sql.NullString{String: "", Valid: true}},
		{Name: "event_type", DataType: "varchar", ColumnType: "varchar(32)", Default: sql.NullString{String: "", Valid: true}},
	}
	checks := []string{
		"((`sku_ord` between 1 and 99) and (`sku_system` is not null) and (cast(`sku_system` as char charset binary) in (_utf8mb4'apparel',_utf8mb4'shoe',_utf8mb4'composite_ta',_utf8mb4'composite_bo')))",
		"regexp_like(`code`,_utf8mb4'^AR[0-9A-Z]{1,10}$',_utf8mb4'c')",
		"regexp_like(`label_type`,_utf8mb4'^(main|size|care|origin|flag|hangtag|barcode|special)$')",
		"(`event_type` in (_utf8mb4'order_paid',_utf8mb4'order_refund',_utf8mb4'order_shipped',_utf8mb4'order_delivered',_utf8mb4'order_dispute'))",
	}
	hints := inferCheckHints(columns, checks)
	want := map[string][]any{
		"sku_ord":    {int64(1)},
		"sku_system": {"apparel", "shoe", "composite_ta", "composite_bo"},
		"code":       {"AR1"},
		"label_type": {"main", "size", "care", "origin", "flag", "hangtag", "barcode", "special"},
		"event_type": {"order_paid", "order_refund", "order_shipped", "order_delivered", "order_dispute"},
	}
	if !reflect.DeepEqual(hints, want) {
		t.Fatalf("inferCheckHints() = %#v, want %#v", hints, want)
	}
	row := buildRow(columns, nil, nil, hints, nil, checks, "", nil, 1)
	for name, values := range want {
		if row[name] != values[0] {
			t.Errorf("baseline %s = %#v, want CHECK-derived %#v", name, row[name], values[0])
		}
	}
}

func TestBuildRowUsesForeignKeysAndCheckRelationships(t *testing.T) {
	t.Run("not null foreign key overrides expression default", func(t *testing.T) {
		columns := []column{
			{Name: "board", DataType: "varchar", ColumnType: "varchar(16)"},
			{Name: "board_id", DataType: "int", ColumnType: "int", Default: sql.NullString{String: "(case `board`)", Valid: true}},
		}
		row := buildRow(columns, nil, nil, nil, map[string]any{"board_id": int64(1)}, nil, "", nil, 1)
		if row["board_id"] != int64(1) {
			t.Fatalf("board_id = %#v, want existing task_board id 1", row["board_id"])
		}
	})

	t.Run("nullable foreign key xor gets exactly one owner", func(t *testing.T) {
		columns := []column{
			{Name: "order_id", DataType: "int", ColumnType: "int", Nullable: true},
			{Name: "run_id", DataType: "int", ColumnType: "int", Nullable: true},
		}
		checks := []string{"((`order_id` is null) <> (`run_id` is null))"}
		row := buildRow(columns, nil, nil, nil, map[string]any{"order_id": int64(11), "run_id": int64(12)}, checks, "", nil, 1)
		if row["order_id"] != int64(11) || row["run_id"] != nil {
			t.Fatalf("owner xor row = %#v", row)
		}
	})

	t.Run("relationship endpoints remain distinct", func(t *testing.T) {
		columns := []column{
			{Name: "from_task_id", DataType: "int", ColumnType: "int"},
			{Name: "to_task_id", DataType: "int", ColumnType: "int"},
			{Name: "kind", DataType: "varchar", ColumnType: "varchar(16)"},
		}
		checks := []string{
			"(`kind` in (_utf8mb4'blocks',_utf8mb4'relates'))",
			"(`from_task_id` <> `to_task_id`)",
		}
		hints := inferCheckHints(columns, checks)
		row := buildRow(columns, nil, nil, hints, map[string]any{"from_task_id": int64(7), "to_task_id": int64(8)}, checks, "", nil, 1)
		if row["kind"] != "blocks" || row["from_task_id"] != int64(7) || row["to_task_id"] != int64(8) {
			t.Fatalf("task link row = %#v", row)
		}
	})
}

func TestExistingMigrationRowCountsAsCovered(t *testing.T) {
	covered, alreadyCovered := tableSeedCoverage(0, map[string]any{"id": int64(1)})
	if !covered || !alreadyCovered {
		t.Fatalf("tableSeedCoverage() = %v, %v; want true, true", covered, alreadyCovered)
	}
	covered, alreadyCovered = tableSeedCoverage(0, nil)
	if covered || alreadyCovered {
		t.Fatalf("empty tableSeedCoverage() = %v, %v; want false, false", covered, alreadyCovered)
	}
}

func TestQuoteIdentifierUsesANSICompatibleBackticks(t *testing.T) {
	if got, want := quoteIdentifier("select`value"), "`select``value`"; got != want {
		t.Fatalf("quoteIdentifier() = %q, want %q", got, want)
	}
}

func TestRegexpCheckHintsCoverRequiredCatalogColumns(t *testing.T) {
	columns := []column{{Name: "movement_type", DataType: "varchar", ColumnType: "varchar(24)"}}
	hints := inferCheckHints(columns, []string{"(`movement_type` regexp _utf8mb4'^(receipt|issue_sample|writeoff)$')"})
	want := []any{"receipt", "issue_sample", "writeoff"}
	if !reflect.DeepEqual(hints["movement_type"], want) {
		t.Fatalf("movement_type hints = %#v, want %#v", hints["movement_type"], want)
	}
}

func TestCheckHintsCoverSingletonAndBuyerFormats(t *testing.T) {
	columns := []column{
		{Name: "singleton", DataType: "tinyint", ColumnType: "tinyint"},
		{Name: "email", DataType: "varchar", ColumnType: "varchar(100)", CharMax: nullInt64(100)},
		{Name: "phone", DataType: "varchar", ColumnType: "varchar(20)", CharMax: nullInt64(20)},
	}
	hints := inferCheckHints(columns, []string{"(`singleton` = 1)"})
	if !reflect.DeepEqual(hints["singleton"], []any{int64(1)}) {
		t.Fatalf("singleton hints = %#v", hints["singleton"])
	}
	row := buildRow(columns, nil, nil, hints, nil, nil, "", nil, 42)
	if row["email"] != "migfuzz+42@example.invalid" {
		t.Fatalf("email seed = %#v", row["email"])
	}
	if row["phone"] != "1000000042" {
		t.Fatalf("phone seed = %#v", row["phone"])
	}
}

func TestBoundaryRejectionDoesNotHideInfrastructureErrors(t *testing.T) {
	if !isBoundaryRejection(&mysql.MySQLError{Number: 3819, Message: "check failed"}) {
		t.Fatal("CHECK failure should reject only the candidate row")
	}
	if isBoundaryRejection(&mysql.MySQLError{Number: 1146, Message: "table missing"}) {
		t.Fatal("schema/tooling errors must stop the replay")
	}
	if isBoundaryRejection(errors.New("connection lost")) {
		t.Fatal("non-MySQL errors must stop the replay")
	}
}

func containsBoundary(values []boundaryValue, want any) bool {
	for _, value := range values {
		if reflect.DeepEqual(value.Value, want) {
			return true
		}
	}
	return false
}

func nullInt64(value int64) sql.NullInt64 {
	return sql.NullInt64{Int64: value, Valid: true}
}
