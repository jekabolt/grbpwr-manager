package store

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// TestVatNamedQueriesBind is a regression test for the "could not find name  in map" bug: a stray ':'
// inside a SQL '--' comment in GetUkVatReturn / VatSalesEvidence was read by sqlx's named-param scanner
// (which does not skip SQL comments) as an empty-named bind parameter, failing the whole query — the UK
// VAT 9-box tab showed "Failed to load". The bug is data-independent (it fails at bind time before any
// row is read), so calling each method on a freshly-migrated DB with zero UK orders is enough to catch
// a regression: before the fix these error; after it they return an (empty) result.
//
// SAFE ONLY against a local container DSN — see the guard and mysql_test.go / project memory.
func TestVatNamedQueriesBind(t *testing.T) {
	if os.Getenv("CI") == "" &&
		!strings.Contains(testCfg.DSN, "127.0.0.1") &&
		!strings.Contains(testCfg.DSN, "localhost") {
		t.Skip("skipping outside CI unless the DSN targets a local container (avoids the configured prod DB)")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	s, err := NewForTest(ctx, *testCfg)
	require.NoError(t, err)

	q := time.Date(2026, time.July, 1, 0, 0, 0, 0, time.UTC) // Q3 2026, the quarter from the bug report

	_, err = s.Accounting().GetUkVatReturn(ctx, q)
	require.NoError(t, err, "GetUkVatReturn named-query must bind (no stray ':' in SQL comments)")

	_, err = s.Accounting().VatSalesEvidence(ctx, q)
	require.NoError(t, err, "VatSalesEvidence named-query must bind (no stray ':' in SQL comments)")
}
