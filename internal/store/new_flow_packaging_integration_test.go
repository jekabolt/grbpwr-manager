package store

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

// seedOrder inserts a minimal customer_order (uuid + total_price + any valid status) and returns its
// id, so the order_packaging_consumed FK is satisfied without building a whole order.
func seedOrder(ctx context.Context, t *testing.T) int {
	t.Helper()
	res, err := testDB.ExecContext(ctx, `
		INSERT INTO customer_order (uuid, total_price, order_status_id)
		SELECT UUID(), 100.00, id FROM order_status LIMIT 1`)
	require.NoError(t, err)
	id, err := res.LastInsertId()
	require.NoError(t, err)
	return int(id)
}
