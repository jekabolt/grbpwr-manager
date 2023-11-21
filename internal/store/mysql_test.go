package store

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
)

func newTestDB(t *testing.T) *MYSQLStore {
	db, err := New(context.Background(), Config{
		// TODO: use test database
		DSN:         "user:pass@(localhost:3306)/grbpwr?charset=utf8&parseTime=true",
		Automigrate: true,
	})
	assert.NoError(t, err)

	_, err = db.db.ExecContext(context.Background(), "SET FOREIGN_KEY_CHECKS = 0")
	assert.NoError(t, err)

	_, err = db.db.ExecContext(context.Background(), "DELETE FROM product_tag")
	assert.NoError(t, err)

	_, err = db.db.ExecContext(context.Background(), "DELETE FROM product_media")
	assert.NoError(t, err)

	_, err = db.db.ExecContext(context.Background(), "DELETE FROM product")
	assert.NoError(t, err)

	_, err = db.db.ExecContext(context.Background(), "DELETE FROM promo_code")
	assert.NoError(t, err)

	_, err = db.db.ExecContext(context.Background(), "DELETE FROM size_measurement")
	assert.NoError(t, err)

	_, err = db.db.ExecContext(context.Background(), "DELETE FROM product_size")
	assert.NoError(t, err)

	_, err = db.db.ExecContext(context.Background(), "DELETE FROM shipment")
	assert.NoError(t, err)

	_, err = db.db.ExecContext(context.Background(), "DELETE FROM order_item")
	assert.NoError(t, err)

	_, err = db.db.ExecContext(context.Background(), "DELETE FROM customer_order")
	assert.NoError(t, err)

	_, err = db.db.ExecContext(context.Background(), "DELETE FROM payment")
	assert.NoError(t, err)

	_, err = db.db.ExecContext(context.Background(), "DELETE FROM buyer")
	assert.NoError(t, err)

	_, err = db.db.ExecContext(context.Background(), "DELETE FROM address")
	assert.NoError(t, err)

	_, err = db.db.ExecContext(context.Background(), "DELETE FROM subscriber")
	assert.NoError(t, err)

	_, err = db.db.ExecContext(context.Background(), "DELETE FROM hero")
	assert.NoError(t, err)

	_, err = db.db.ExecContext(context.Background(), "DELETE FROM hero")
	assert.NoError(t, err)

	_, err = db.db.ExecContext(context.Background(), "DELETE FROM hero_product")
	assert.NoError(t, err)

	_, err = db.db.ExecContext(context.Background(), "DELETE FROM admins")
	assert.NoError(t, err)

	_, err = db.db.ExecContext(context.Background(), "DELETE FROM archive")
	assert.NoError(t, err)

	_, err = db.db.ExecContext(context.Background(), "DELETE FROM archive_item")
	assert.NoError(t, err)

	_, err = db.db.ExecContext(context.Background(), "DELETE FROM media")
	assert.NoError(t, err)

	_, err = db.db.ExecContext(context.Background(), "SET FOREIGN_KEY_CHECKS = 1")
	assert.NoError(t, err)

	return db
}
