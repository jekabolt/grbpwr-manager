package store

import (
	"context"
	"fmt"
	"testing"

	"github.com/spf13/viper"
	"github.com/stretchr/testify/assert"
)

func loadConfig(cfgFile string) (*Config, error) {
	viper.SetConfigType("toml")
	viper.SetConfigFile(cfgFile)
	if cfgFile != "" {
		viper.SetConfigFile(cfgFile)
	} else {
		viper.SetConfigName("config")
		viper.AddConfigPath("../../config")
		viper.AddConfigPath("/usr/local/config")
	}

	if err := viper.ReadInConfig(); err != nil {
		return nil, fmt.Errorf("failed to read config: %v", err)
	}

	var config Config

	err := viper.UnmarshalKey("mysql", &config)
	if err != nil {
		return nil, fmt.Errorf("failed to unmarshal config into struct: %v", err)
	}

	// fmt.Printf("conf---- %+v", config)
	return &config, nil
}

func newTestDB(t *testing.T) *MYSQLStore {

	cfg, err := loadConfig("")
	assert.NoError(t, err)

	db, err := New(context.Background(), *cfg)
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

	_, err = db.db.ExecContext(context.Background(), "DELETE FROM send_email_request")
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
