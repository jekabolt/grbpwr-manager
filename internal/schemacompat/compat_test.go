package schemacompat

import (
	"sort"
	"testing"
)

func TestCompareFollowsSQLMigrateOrder(t *testing.T) {
	ids := []string{"10_b.sql", "2_a.sql", "0485_x.sql", "0485_a.sql", "readme.sql", "0001_init.sql"}
	sort.Slice(ids, func(i, j int) bool { return Compare(ids[i], ids[j]) < 0 })
	want := []string{"0001_init.sql", "2_a.sql", "10_b.sql", "0485_a.sql", "0485_x.sql", "readme.sql"}
	for i := range want {
		if ids[i] != want[i] {
			t.Fatalf("order %v, want %v", ids, want)
		}
	}
	if Compare("0486_a.sql", "0486_a.sql") != 0 {
		t.Fatal("equal ids must compare 0")
	}
}

func TestParseHeader(t *testing.T) {
	cases := []struct {
		body    string
		want    Header
		wantErr bool
	}{
		{"-- +migrate Up\nALTER TABLE x ADD COLUMN y INT;", Header{}, false},
		{"-- compat: lazy backfill of old rows\n", Header{Lazy: true, Reason: "backfill of old rows"}, false},
		{"  -- compat: breaking drops product.old_price\n", Header{Breaking: true, Reason: "drops product.old_price"}, false},
		{"-- compat: lazy\n", Header{}, true},
		{"-- compat: maybe because\n", Header{}, true},
		{"-- compat: lazy a\n-- compat: lazy b\n", Header{}, true},
	}
	for _, c := range cases {
		got, err := ParseHeader(c.body)
		if (err != nil) != c.wantErr || (err == nil && got != c.want) {
			t.Fatalf("ParseHeader(%q) = %+v, %v", c.body, got, err)
		}
	}
}

func TestBuildRefusesMismatchedDeclarations(t *testing.T) {
	if _, err := Build([]Migration{{ID: "0486_breaking_drop.sql"}}); err == nil {
		t.Fatal("name says breaking, header does not")
	}
	if _, err := Build([]Migration{{ID: "0486_drop.sql", Header: Header{Breaking: true, Reason: "r"}}}); err == nil {
		t.Fatal("header says breaking, name does not")
	}
	if _, err := Build([]Migration{{ID: "0486_breaking_x.sql", Header: Header{Breaking: true, Lazy: true, Reason: "r"}}}); err == nil {
		t.Fatal("breaking and lazy together")
	}
}

// fleet history: 0484 additive, 0485 lazy index, 0486 additive, 0487 breaking drop, 0488 additive.
func history(t *testing.T, upTo int) Set {
	all := []Migration{
		{ID: "0484_add.sql"},
		{ID: "0485_index.sql", Header: Header{Lazy: true, Reason: "index"}},
		{ID: "0486_add.sql"},
		{ID: "0487_breaking_drop_old.sql", Header: Header{Breaking: true, Reason: "drop old"}},
		{ID: "0488_add.sql"},
	}
	var ms []Migration
	for _, m := range all {
		if Number(m.ID) <= int64(upTo) {
			ms = append(ms, m)
		}
	}
	s, err := Build(ms)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// Code N on schema N-1: open unless N-1 lacks a migration the code needs.
func TestCodeNOnSchemaNMinus1(t *testing.T) {
	n := history(t, 485) // N's newest migration is lazy
	if n.Need != "0484_add.sql" || n.Latest != "0485_index.sql" {
		t.Fatalf("set %+v", n)
	}
	if !n.Servable("0484_add.sql", "") {
		t.Fatal("behind only a lazy migration must stay open")
	}
	n = history(t, 486) // N needs 0486
	if n.Servable("0485_index.sql", "") {
		t.Fatal("behind a needed migration must be closed")
	}
	if !n.Servable("0486_add.sql", "") {
		t.Fatal("at latest must be open")
	}
}

// Code N on schema N+1 (a rollback): open over additive releases, closed past a breaking one.
func TestCodeNOnSchemaNPlus1(t *testing.T) {
	n := history(t, 485)
	if !n.Servable("0486_add.sql", "") {
		t.Fatal("ahead by an additive migration must stay open")
	}
	if !n.Ahead("0486_add.sql") || n.Ahead("0485_index.sql") {
		t.Fatal("Ahead wrong")
	}
	full := history(t, 488)
	minCode := full.MinCodeAt("0487_breaking_drop_old.sql")
	if minCode != "0487_breaking_drop_old.sql" {
		t.Fatalf("min code %q", minCode)
	}
	n = history(t, 486)
	if n.Servable("0487_breaking_drop_old.sql", minCode) {
		t.Fatal("code older than a breaking migration must not serve that database")
	}
	n = history(t, 487)
	if !n.Servable("0488_add.sql", minCode) {
		t.Fatal("code that embeds the breaking migration serves the database ahead of it")
	}
}

func TestEmptySetServesNothing(t *testing.T) {
	if (Set{}).Servable("0484_add.sql", "") {
		t.Fatal("an unreadable set must fail closed")
	}
	if history(t, 488).Servable("", "") {
		t.Fatal("a team with no version must be closed")
	}
}

func TestMinCodeAtAndRaise(t *testing.T) {
	s := history(t, 488)
	if s.MinCodeAt("0486_add.sql") != "" || s.MinCodeAt("0488_add.sql") != "0487_breaking_drop_old.sql" || s.MinCodeAt("") != "" {
		t.Fatal("MinCodeAt wrong")
	}
	if Raise("", "0487_x.sql") != "0487_x.sql" || Raise("0490_y.sql", "0487_x.sql") != "0490_y.sql" || Raise("0487_x.sql", "") != "0487_x.sql" {
		t.Fatal("Raise wrong")
	}
}

func TestAheadOnly(t *testing.T) {
	known := []string{"0001_a.sql", "0009_b.sql"}
	if _, ok := AheadOnly(known, known); ok {
		t.Fatal("nothing unknown is not ahead")
	}
	if u, ok := AheadOnly(known, append(known, "0010_c.sql")); !ok || len(u) != 1 {
		t.Fatal("a newer additive migration must be tolerated")
	}
	if _, ok := AheadOnly(known, append(known, "0010_breaking_c.sql")); ok {
		t.Fatal("a newer breaking migration must refuse")
	}
	if _, ok := AheadOnly(known, append(known, "0005_gap.sql")); ok {
		t.Fatal("an unknown migration in the middle must refuse")
	}
}
