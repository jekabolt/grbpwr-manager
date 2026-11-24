package store

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// CountBusyCardOrders (X18-L3): a team shop keeps its Stripe while a card order awaits its payment or
// a refund, or was paid inside the refund window. Other rows in the shared test schema may exist, so
// every case asserts the change its own order makes.
func TestCountBusyCardOrders(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	cfg := *testCfg
	cfg.Automigrate = true
	s, err := NewForTest(ctx, cfg)
	require.NoError(t, err)
	defer s.Close()

	id := func(table, name string) int64 {
		var v int64
		require.NoError(t, testDB.QueryRowContext(ctx, "SELECT id FROM "+table+" WHERE name = ?", name).Scan(&v))
		return v
	}
	now := time.Now().UTC().Truncate(time.Second)
	since := now.Add(-30 * 24 * time.Hour)
	var orders []int64
	t.Cleanup(func() {
		for _, o := range orders {
			_, _ = testDB.ExecContext(context.Background(), "DELETE FROM payment WHERE order_id = ?", o)
			_, _ = testDB.ExecContext(context.Background(), "DELETE FROM customer_order WHERE id = ?", o)
		}
	})
	add := func(status, method string, placed time.Time) {
		res, err := testDB.ExecContext(ctx, `INSERT INTO customer_order (uuid, order_status_id, currency, total_price, placed)
			VALUES (?, ?, 'EUR', 100, ?)`, fmt.Sprintf("BUSY-%d-%d", now.UnixNano(), len(orders)), id("order_status", status), placed)
		require.NoError(t, err)
		o, err := res.LastInsertId()
		require.NoError(t, err)
		orders = append(orders, o)
		_, err = testDB.ExecContext(ctx, `INSERT INTO payment (order_id, payment_method_id, transaction_amount,
			transaction_amount_payment_currency, is_transaction_done) VALUES (?, ?, 100, 100, 0)`, o, id("payment_method", method))
		require.NoError(t, err)
	}
	count := func() int {
		n, err := s.Order().CountBusyCardOrders(ctx, since)
		require.NoError(t, err)
		return n
	}

	old := now.Add(-90 * 24 * time.Hour)
	for _, c := range []struct {
		status, method string
		placed         time.Time
		busy           bool
	}{
		{"awaiting_payment", "card", old, true},
		{"awaiting_payment", "card-test", now, true},
		{"refund_in_progress", "card", old, true},
		{"pending_return", "card-test", old, true},
		{"confirmed", "card", now, true},
		{"delivered", "card", now.Add(-24 * time.Hour), true},
		{"partially_refunded", "card", now, true},
		{"confirmed", "card", old, false},
		{"shipped", "card-test", old, false},
		{"refunded", "card", now, false},
		{"cancelled", "card", now, false},
		{"placed", "card", now, false},
		{"awaiting_payment", "bank-invoice", now, false},
		{"confirmed", "cash", now, false},
	} {
		before := count()
		add(c.status, c.method, c.placed)
		want := before
		if c.busy {
			want++
		}
		require.Equal(t, want, count(), "%s %s placed %s", c.status, c.method, c.placed)
	}
}
