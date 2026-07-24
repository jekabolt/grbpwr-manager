package storeutil

import (
	"strings"
	"testing"
)

// TestMakeQueryParameterlessColonSafe is the regression test for the GetReceivables outage
// (and the earlier tech-card afbdcf0 / JPK-evidence dfb69b4 incidents): sqlx's named-param
// scanner reads a ':' inside a SQL string literal or a '--' comment as an empty bind name and
// fails the query with `could not find name  in map`. A parameterless query must therefore
// bypass the scanner entirely and reach the driver byte-for-byte unchanged.
func TestMakeQueryParameterlessColonSafe(t *testing.T) {
	q := `
		SELECT SUBSTRING_INDEX(e.source_key, ':', 1) AS ref,
		       COALESCE(SUM(CASE WHEN l.side = 'debit' THEN l.amount ELSE 0 END), 0) AS invoiced
		-- a comment with a colon: 'bank:<id>' and 'manual:<uuid>' used to kill this query
		FROM acct_journal_line l
		GROUP BY SUBSTRING_INDEX(e.source_key, ':', 1)`

	for _, params := range []map[string]any{nil, {}} {
		got, args, err := makeQuery(q, params)
		if err != nil {
			t.Fatalf("makeQuery(parameterless) returned error: %v", err)
		}
		if got != q {
			t.Fatalf("makeQuery(parameterless) rewrote the query:\nwant %q\ngot  %q", q, got)
		}
		if len(args) != 0 {
			t.Fatalf("makeQuery(parameterless) produced args: %v", args)
		}
	}
}

// TestMakeQueryNamedParamsStillBind guards the other half of the contract: queries that do
// carry params keep going through sqlx.Named + sqlx.In unchanged.
func TestMakeQueryNamedParamsStillBind(t *testing.T) {
	q := `SELECT id FROM supplier WHERE name = :name AND id IN (:ids)`
	got, args, err := makeQuery(q, map[string]any{"name": "acme", "ids": []int{1, 2}})
	if err != nil {
		t.Fatalf("makeQuery(named) returned error: %v", err)
	}
	if strings.Contains(got, ":name") || strings.Contains(got, ":ids") {
		t.Fatalf("named params were not rewritten: %q", got)
	}
	if len(args) != 3 { // name + 2 expanded ids
		t.Fatalf("expected 3 bound args, got %d: %v", len(args), args)
	}
}
