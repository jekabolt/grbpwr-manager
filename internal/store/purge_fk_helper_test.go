package store

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/jmoiron/sqlx"
)

// PURGING WHAT A TEST BOUGHT: ROWS, AND EVERYTHING THAT HOLDS THEM BY A FOREIGN KEY.
//
// Every test in this package shares one database for the whole run, so a row a test cannot delete
// is a row the next test inherits. A test that places an order cannot clean up with a handful of
// DELETEs: CreateOrder, payment and refund write order_item, buyer, payment, status history, stock
// history, packaging needs …, and each of them holds the product, the variant or the size by a
// RESTRICT key. The old best-effort deletes no-oped on the first such key without a word, and what
// outlived the test broke tests far away from it: a product frozen at a non-canonical SKU fails the
// SKU readiness gate of every later store.New, its thumbnail blocks TestMedia's DELETE FROM media,
// and its size keeps an apparel ordinal the next test inserts again.
//
// The walk reads the schema's own foreign keys, so it keeps up with every table a later migration
// hangs off orders or products. Children go first; ON DELETE SET NULL children are left to MySQL.

type purgeFKEdge struct {
	child      string
	childCols  []string
	parentCols []string
	rule       string // REFERENTIAL_CONSTRAINTS.DELETE_RULE
}

var (
	purgeFKMu    sync.Mutex
	purgeFKEdges = map[string][]purgeFKEdge{}
)

// purgeFKEdgesTo lists the foreign keys that reference parent, one edge per constraint (composite
// keys keep their column order). The schema does not change during a run, so it is read once.
func purgeFKEdgesTo(ctx context.Context, parent string) ([]purgeFKEdge, error) {
	purgeFKMu.Lock()
	defer purgeFKMu.Unlock()
	if edges, ok := purgeFKEdges[parent]; ok {
		return edges, nil
	}
	rows, err := testDB.QueryContext(ctx, `
		SELECT k.CONSTRAINT_NAME, k.TABLE_NAME, k.COLUMN_NAME, k.REFERENCED_COLUMN_NAME, r.DELETE_RULE
		FROM information_schema.KEY_COLUMN_USAGE k
		JOIN information_schema.REFERENTIAL_CONSTRAINTS r
		  ON r.CONSTRAINT_SCHEMA = k.CONSTRAINT_SCHEMA
		 AND r.CONSTRAINT_NAME = k.CONSTRAINT_NAME
		 AND r.TABLE_NAME = k.TABLE_NAME
		WHERE k.TABLE_SCHEMA = DATABASE() AND k.REFERENCED_TABLE_NAME = ?
		ORDER BY k.TABLE_NAME, k.CONSTRAINT_NAME, k.ORDINAL_POSITION`, parent)
	if err != nil {
		return nil, fmt.Errorf("purge: foreign keys to %s: %w", parent, err)
	}
	defer rows.Close()
	var edges []purgeFKEdge
	lastKey := ""
	for rows.Next() {
		var name, child, col, refCol, rule string
		if err := rows.Scan(&name, &child, &col, &refCol, &rule); err != nil {
			return nil, fmt.Errorf("purge: foreign keys to %s: %w", parent, err)
		}
		if key := child + "." + name; key != lastKey {
			edges = append(edges, purgeFKEdge{child: child, rule: rule})
			lastKey = key
		}
		e := &edges[len(edges)-1]
		e.childCols = append(e.childCols, col)
		e.parentCols = append(e.parentCols, refCol)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("purge: foreign keys to %s: %w", parent, err)
	}
	purgeFKEdges[parent] = edges
	return edges, nil
}

// purgeKeyPredicate renders `cols IN keys` for one column or a row constructor for several.
func purgeKeyPredicate(cols []string, keys [][]any) (string, []any) {
	lhs, mark := "`"+cols[0]+"`", "?"
	if len(cols) > 1 {
		lhs = "(`" + strings.Join(cols, "`,`") + "`)"
		mark = "(" + strings.TrimSuffix(strings.Repeat("?,", len(cols)), ",") + ")"
	}
	args := make([]any, 0, len(keys)*len(cols))
	marks := make([]string, len(keys))
	for i, k := range keys {
		args = append(args, k...)
		marks[i] = mark
	}
	return lhs + " IN (" + strings.Join(marks, ",") + ")", args
}

// purgeSelectKeys reads the distinct non-NULL values of cols on the rows `where` matches.
func purgeSelectKeys(ctx context.Context, table string, cols []string, where string, args []any) ([][]any, error) {
	quoted := make([]string, len(cols))
	for i, c := range cols {
		quoted[i] = "`" + c + "`"
	}
	rows, err := testDB.QueryContext(ctx,
		"SELECT DISTINCT "+strings.Join(quoted, ",")+" FROM `"+table+"` WHERE "+where, args...)
	if err != nil {
		return nil, fmt.Errorf("purge: read %s(%s): %w", table, strings.Join(cols, ","), err)
	}
	defer rows.Close()
	var keys [][]any
	for rows.Next() {
		vals := make([]any, len(cols))
		ptrs := make([]any, len(cols))
		for i := range vals {
			ptrs[i] = &vals[i]
		}
		if err := rows.Scan(ptrs...); err != nil {
			return nil, fmt.Errorf("purge: read %s: %w", table, err)
		}
		null := false
		for _, v := range vals {
			if v == nil {
				null = true
			}
		}
		if !null {
			keys = append(keys, vals)
		}
	}
	return keys, rows.Err()
}

// purgeRowsFK deletes the rows of table whose cols match one of keys, after deleting — the same
// way, recursively — every row that references them.
func purgeRowsFK(ctx context.Context, table string, cols []string, keys [][]any, depth int) error {
	if len(keys) == 0 {
		return nil
	}
	if depth > 16 {
		return fmt.Errorf("purge: foreign keys nest deeper than 16 levels below %s", table)
	}
	where, args := purgeKeyPredicate(cols, keys)
	edges, err := purgeFKEdgesTo(ctx, table)
	if err != nil {
		return err
	}
	for _, e := range edges {
		if e.rule == "SET NULL" {
			continue
		}
		parentKeys, err := purgeSelectKeys(ctx, table, e.parentCols, where, args)
		if err != nil {
			return err
		}
		if err := purgeRowsFK(ctx, e.child, e.childCols, parentKeys, depth+1); err != nil {
			return err
		}
	}
	if _, err := testDB.ExecContext(ctx, "DELETE FROM `"+table+"` WHERE "+where, args...); err != nil {
		return fmt.Errorf("purge: delete from %s: %w", table, err)
	}
	return nil
}

func purgeIDKeys[T ~int | ~int64](ids []T) [][]any {
	keys := make([][]any, 0, len(ids))
	for _, id := range ids {
		if id > 0 {
			keys = append(keys, []any{int64(id)})
		}
	}
	return keys
}

// purgeTestProducts removes products a test inserted together with every order that bought them,
// and everything either of them reaches by a foreign key. Orders go first, as whole orders: purging
// the products alone would take the order lines and leave line-less orders behind for the next
// test's order counts. Meant for t.Cleanup, so it reports instead of stopping the cleanups after it.
func purgeTestProducts[T ~int | ~int64](t *testing.T, productIDs ...T) {
	t.Helper()
	ctx := context.Background()
	productKeys := purgeIDKeys(productIDs)
	if len(productKeys) == 0 {
		return
	}
	ids := make([]any, len(productKeys))
	for i, k := range productKeys {
		ids[i] = k[0]
	}
	q, args, err := sqlx.In(`SELECT DISTINCT order_id FROM order_item WHERE product_id IN (?)`, ids)
	if err != nil {
		t.Errorf("purge: %v", err)
		return
	}
	var orderIDs []int64
	rows, err := testDB.QueryContext(ctx, q, args...)
	if err != nil {
		t.Errorf("purge: orders of products %v: %v", productIDs, err)
		return
	}
	for rows.Next() {
		var id sql.NullInt64
		if err := rows.Scan(&id); err != nil {
			t.Errorf("purge: orders of products %v: %v", productIDs, err)
		}
		if id.Valid {
			orderIDs = append(orderIDs, id.Int64)
		}
	}
	_ = rows.Close()
	if err := purgeRowsFK(ctx, "customer_order", []string{"id"}, purgeIDKeys(orderIDs), 0); err != nil {
		t.Errorf("purge: orders %v of products %v: %v", orderIDs, productIDs, err)
	}
	if err := purgeRowsFK(ctx, "product", []string{"id"}, productKeys, 0); err != nil {
		t.Errorf("purge: products %v: %v", productIDs, err)
	}
}

// purgeTestRows removes rows a test inserted into table by id, with everything that references
// them. For the rows around a purged product (its size, style, thumbnail, carrier): unlike the
// `_, _ =` deletes it replaces, a row that cannot go fails the test that left it.
func purgeTestRows[T ~int | ~int64](t *testing.T, table string, ids ...T) {
	t.Helper()
	if err := purgeRowsFK(context.Background(), table, []string{"id"}, purgeIDKeys(ids), 0); err != nil {
		t.Errorf("purge: %s %v: %v", table, ids, err)
	}
}
