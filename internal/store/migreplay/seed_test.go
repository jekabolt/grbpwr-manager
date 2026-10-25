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
	first := buildRow(columns, indexes, template, "quantity", int64(0), 1)
	second := buildRow(columns, indexes, template, "quantity", int64(0), 2)
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
	first := buildRow(columns, indexes, nil, "quantity", int64(0), 1)
	second := buildRow(columns, indexes, nil, "quantity", int64(0), 2)
	if first["code"] == nil || first["code"] == second["code"] {
		t.Fatalf("unique default was not replaced with distinct values: %#v %#v", first, second)
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
