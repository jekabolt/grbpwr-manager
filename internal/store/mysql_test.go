package store

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"os"
	"testing"
	"time"

	_ "github.com/go-sql-driver/mysql"
	"github.com/spf13/viper"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var testDB *sql.DB
var testCfg *Config

func loadTestConfig(cfgFile string) (*Config, error) {
	// Check if running in CI
	if os.Getenv("CI") != "" {
		// Use CI environment variables
		return &Config{
			DSN: fmt.Sprintf("%s:%s@tcp(%s:%s)/%s?parseTime=true&multiStatements=true",
				os.Getenv("MYSQL_USER"),
				os.Getenv("MYSQL_PASSWORD"),
				os.Getenv("MYSQL_HOST"),
				os.Getenv("MYSQL_PORT"),
				os.Getenv("MYSQL_DATABASE"),
			),
			Automigrate:        false,
			MaxOpenConnections: 10,
			MaxIdleConnections: 5,
		}, nil
	}

	// Local environment - use config file
	viper.Reset()
	viper.SetConfigType("toml")
	viper.SetConfigFile(cfgFile)

	if err := viper.ReadInConfig(); err != nil {
		return nil, fmt.Errorf("failed to read config: %v", err)
	}

	var config Config
	if err := viper.UnmarshalKey("mysql", &config); err != nil {
		return nil, fmt.Errorf("failed to unmarshal config into struct: %v", err)
	}

	return &config, nil
}

func TestMain(m *testing.M) {
	var err error
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	// Set config path based on environment
	configPath := "../../config/config.toml"
	if os.Getenv("CI") != "" {
		slog.Info("running in CI environment")
	} else {
		slog.Info("running in local environment")
	}

	testCfg, err = loadTestConfig(configPath)
	if err != nil {
		slog.Error("failed to load test config", "error", err)
		os.Exit(1)
	}

	// Replace direct connection with retry logic
	testDB, err = connectWithRetry(testCfg.DSN, 5, time.Second)
	if err != nil {
		slog.Error("failed to connect to test database", "error", err)
		os.Exit(1)
	}

	// Configure connection pool
	testDB.SetMaxOpenConns(testCfg.MaxOpenConnections)
	testDB.SetMaxIdleConns(testCfg.MaxIdleConnections)
	testDB.SetConnMaxLifetime(time.Minute)

	// Run migrations with timeout
	_, cancel = context.WithTimeout(ctx, 30*time.Second)
	if err := Migrate(testDB); err != nil {
		slog.Error("failed to run migrations", "error", err)
		testDB.Close()
		cancel()
		os.Exit(1)
	}
	cancel()

	// Run tests
	code := m.Run()

	// Cleanup
	if err := cleanup(testDB); err != nil {
		slog.Error("failed to cleanup test database", "error", err)
	}
	testDB.Close()

	os.Exit(code)
}

func getTableNames(db *sql.DB) ([]string, error) {
	rows, err := db.Query("SHOW TABLES")
	if err != nil {
		return nil, fmt.Errorf("failed to get tables: %v", err)
	}
	defer rows.Close()

	var tables []string
	for rows.Next() {
		var table string
		if err := rows.Scan(&table); err != nil {
			return nil, fmt.Errorf("failed to scan table name: %v", err)
		}
		tables = append(tables, table)
	}
	return tables, rows.Err()
}

func cleanup(db *sql.DB) error {
	// Disable foreign key checks temporarily
	if _, err := db.Exec("SET FOREIGN_KEY_CHECKS = 0"); err != nil {
		return fmt.Errorf("failed to disable foreign key checks: %v", err)
	}
	defer db.Exec("SET FOREIGN_KEY_CHECKS = 1")

	// Drop all views first
	views, err := db.Query("SELECT TABLE_NAME FROM INFORMATION_SCHEMA.VIEWS WHERE TABLE_SCHEMA = DATABASE()")
	if err != nil {
		return fmt.Errorf("failed to get views: %v", err)
	}
	defer views.Close()

	for views.Next() {
		var view string
		if err := views.Scan(&view); err != nil {
			return fmt.Errorf("failed to scan view name: %v", err)
		}
		if _, err := db.Exec(fmt.Sprintf("DROP VIEW IF EXISTS `%s`", view)); err != nil {
			return fmt.Errorf("failed to drop view %s: %v", view, err)
		}
	}
	if err = views.Err(); err != nil {
		return fmt.Errorf("error iterating views: %v", err)
	}

	// Drop all tables
	tables, err := getTableNames(db)
	if err != nil {
		return err
	}

	for _, table := range tables {
		if _, err := db.Exec(fmt.Sprintf("DROP TABLE IF EXISTS `%s`", table)); err != nil {
			return fmt.Errorf("failed to drop table %s: %v", table, err)
		}
	}
	return nil
}

func connectWithRetry(dsn string, maxRetries int, retryDelay time.Duration) (*sql.DB, error) {
	var db *sql.DB
	var err error

	for i := 0; i < maxRetries; i++ {
		db, err = sql.Open("mysql", dsn)
		if err != nil {
			time.Sleep(retryDelay)
			continue
		}

		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		err = db.PingContext(ctx)
		cancel()

		if err == nil {
			return db, nil
		}

		db.Close()
		time.Sleep(retryDelay)
	}
	return nil, fmt.Errorf("failed to connect after %d retries: %v", maxRetries, err)
}

func TestNew(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		cfg     *Config
		wantErr bool
	}{
		{
			name:    "successful connection and migration",
			cfg:     testCfg,
			wantErr: false,
		},
		{
			name: "invalid DSN",
			cfg: &Config{
				DSN:         "invalid-dsn",
				Automigrate: false,
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()

			store, err := New(ctx, *tt.cfg)
			if tt.wantErr {
				assert.Error(t, err)
				return
			}

			require.NoError(t, err)
			require.NotNil(t, store)
			defer store.Close()

			_, err = store.GetDictionaryInfo(ctx)
			require.NoError(t, err)
		})
	}
}

func TestMigrate(t *testing.T) {
	tests := []struct {
		name    string
		cfg     *Config
		wantErr bool
	}{
		{
			name:    "successful migration",
			cfg:     testCfg,
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()
			store, err := New(ctx, *tt.cfg)
			require.NoError(t, err)
			defer store.Close()

			di, err := store.GetDictionaryInfo(ctx)
			if tt.wantErr {
				assert.Error(t, err)
				return
			}

			require.NoError(t, err)
			require.NotNil(t, di)

			hero, err := store.Hero().GetHero(ctx)
			require.NoError(t, err)
			require.NotNil(t, hero)
		})
	}
}

func TestGetDictionaryInfo(t *testing.T) {
	ctx := context.Background()
	store, err := New(ctx, *testCfg)
	require.NoError(t, err)
	require.NotNil(t, store)

	// Test getting dictionary info
	di, err := store.GetDictionaryInfo(ctx)
	require.NoError(t, err)
	require.NotNil(t, di)

	// Verify all fields are populated
	assert.NotNil(t, di.Categories, "Categories should not be nil")
	assert.NotNil(t, di.Measurements, "Measurements should not be nil")
	assert.NotNil(t, di.PaymentMethods, "PaymentMethods should not be nil")
	assert.NotNil(t, di.OrderStatuses, "OrderStatuses should not be nil")
	assert.NotNil(t, di.ShipmentCarriers, "ShipmentCarriers should not be nil")
	assert.NotNil(t, di.Sizes, "Sizes should not be nil")

	// Test with cancelled context
	ctxCancelled, cancel := context.WithCancel(ctx)
	cancel()
	_, err = store.GetDictionaryInfo(ctxCancelled)
	assert.Error(t, err, "Should return error with cancelled context")

	// Test with timeout context
	ctxTimeout, cancel := context.WithTimeout(ctx, 1*time.Nanosecond)
	defer cancel()
	time.Sleep(2 * time.Nanosecond) // Ensure timeout occurs
	_, err = store.GetDictionaryInfo(ctxTimeout)
	assert.Error(t, err, "Should return error with timed out context")
}
