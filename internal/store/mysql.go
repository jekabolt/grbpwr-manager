package store

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"database/sql"
	"embed"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"log/slog"

	"github.com/go-sql-driver/mysql"
	"github.com/jekabolt/grbpwr-manager/internal/cache"
	"github.com/jekabolt/grbpwr-manager/internal/dependency"
	"github.com/jmoiron/sqlx"
	migrate "github.com/rubenv/sql-migrate"

	_ "github.com/golang-migrate/migrate/v4/database/mysql"
)

// Config defines configurations to connect database
type Config struct {
	DSN                string `mapstructure:"dsn"`
	Automigrate        bool   `mapstructure:"automigrate"`
	MaxOpenConnections int    `mapstructure:"max_open_connections"`
	MaxIdleConnections int    `mapstructure:"max_idle_connections"`
	TLSCAPath          string `mapstructure:"tls_ca_path"`
}

// MYSQLStore implements methods to access MYSQL database
type MYSQLStore struct {
	// db is used for executing queries
	db    dependency.DB
	txDB  txDB
	ts    time.Time
	close context.CancelFunc
}

// resolveCertPath resolves @certs paths to the config/certs directory
func resolveCertPath(path string) string {
	if strings.HasPrefix(path, "@certs/") {
		// Try common config locations
		configPaths := []string{
			"./config/certs",
			"config/certs",
			"$HOME/config/grbpwr-products-manager/certs",
			"/etc/grbpwr-products-manager/certs",
		}

		certFile := strings.TrimPrefix(path, "@certs/")
		for _, basePath := range configPaths {
			if strings.HasPrefix(basePath, "$") {
				basePath = os.ExpandEnv(basePath)
			}
			fullPath := filepath.Join(basePath, certFile)
			if _, err := os.Stat(fullPath); err == nil {
				return fullPath
			}
		}
		// Default to ./config/certs if none found
		return filepath.Join("./config/certs", certFile)
	}
	return path
}

// registerTLSConfig registers a custom TLS configuration with the MySQL driver
// Priority order:
// 1. db.CA_CERT environment variable (DigitalOcean App Platform - contains cert content directly)
// 2. TLSCAPath from config (file path, supports @certs/ prefix for local development)
// 3. No TLS config (if neither is provided)
func registerTLSConfig(cfg Config) error {
	var caCert []byte
	var err error

	// Check for DigitalOcean's db.CA_CERT environment variable first
	// This contains the certificate content directly (not a file path)
	if dbCACert := os.Getenv("db.CA_CERT"); dbCACert != "" {
		caCert = []byte(dbCACert)
		slog.Default().Info("using CA certificate from db.CA_CERT environment variable")
	} else if cfg.TLSCAPath != "" {
		// Fall back to file-based certificate for local development
		// Supports @certs/ prefix which resolves to config/certs directory
		certPath := resolveCertPath(cfg.TLSCAPath)
		caCert, err = os.ReadFile(certPath)
		if err != nil {
			return fmt.Errorf("failed to read CA certificate from %s: %w", certPath, err)
		}
		slog.Default().Info("using CA certificate from file", "path", certPath)
	} else {
		// No TLS config needed
		return nil
	}

	caCertPool := x509.NewCertPool()
	if !caCertPool.AppendCertsFromPEM(caCert) {
		return fmt.Errorf("failed to parse CA certificate")
	}

	tlsConfig := &tls.Config{
		RootCAs: caCertPool,
	}

	// Register the TLS configuration with name "custom"
	mysql.RegisterTLSConfig("custom", tlsConfig)
	return nil
}

// New connects to the database, applies migrations and returns a new MYSQLStore object.
func New(ctx context.Context, cfg Config) (*MYSQLStore, error) {
	// Register custom TLS config if provided
	if err := registerTLSConfig(cfg); err != nil {
		return nil, fmt.Errorf("failed to register TLS config: %w", err)
	}

	d, err := sqlx.Open("mysql", cfg.DSN)
	if err != nil {
		return nil, fmt.Errorf("couldn't open database : %v", err)
	}

	// Configure connection pool
	if cfg.MaxOpenConnections > 0 {
		d.SetMaxOpenConns(cfg.MaxOpenConnections)
	}
	if cfg.MaxIdleConnections > 0 {
		d.SetMaxIdleConns(cfg.MaxIdleConnections)
	}
	// Aggressive connection lifecycle management to prevent stale connections
	d.SetConnMaxLifetime(2 * time.Minute)  // Recycle connections every 2 minutes
	d.SetConnMaxIdleTime(30 * time.Second) // Close idle connections after 30 seconds

	// Test connection with timeout
	pingCtx, pingCancel := context.WithTimeout(ctx, 10*time.Second)
	defer pingCancel()
	if err := d.PingContext(pingCtx); err != nil {
		d.Close()
		return nil, fmt.Errorf("failed to ping database: %w", err)
	}

	if cfg.Automigrate {
		slog.Default().InfoContext(ctx, "applying migrations")
		// Run migrations with timeout
		migrateCtx, migrateCancel := context.WithTimeout(ctx, 5*time.Minute)
		defer migrateCancel()
		if err := MigrateWithContext(migrateCtx, d.Unsafe().DB); err != nil {
			d.Close()
			return nil, fmt.Errorf("migration failed: %w", err)
		}
	}

	ctx, c := context.WithCancel(ctx)
	ss := &MYSQLStore{
		db:    d,
		close: c,
	}

	di, err := ss.GetDictionaryInfo(ctx)
	if err != nil {
		return nil, fmt.Errorf("can't get dictionary info: %w", err)
	}

	hf, err := ss.Hero().GetHero(ctx)
	if err != nil {
		return nil, fmt.Errorf("can't get hero: %w", err)
	}

	// cache initialization
	err = cache.InitConsts(ctx, di, hf)
	if err != nil {
		return nil, fmt.Errorf("can't init consts: %w", err)
	}

	go func() {
		<-ctx.Done()
		d.Close()
	}()

	return ss, nil
}

//go:embed sql
var fs embed.FS

func Migrate(db *sql.DB) error {
	return MigrateWithContext(context.Background(), db)
}

func MigrateWithContext(ctx context.Context, db *sql.DB) error {
	m := &migrate.EmbedFileSystemMigrationSource{
		FileSystem: fs,
		Root:       "sql",
	}

	// Run migration in goroutine with context timeout
	type result struct {
		n   int
		err error
	}
	done := make(chan result, 1)
	go func() {
		n, err := migrate.Exec(db, "mysql", m, migrate.Up)
		done <- result{n: n, err: err}
	}()

	select {
	case <-ctx.Done():
		return fmt.Errorf("migration timeout: %w", ctx.Err())
	case res := <-done:
		if res.err != nil {
			return fmt.Errorf("db migrations have failed: %w", res.err)
		}
		slog.Default().InfoContext(ctx, "applied migrations",
			slog.Int("count", res.n),
		)
		return nil
	}
}

// NewForTest creates a new store instance for testing without initializing cache
func NewForTest(ctx context.Context, cfg Config) (*MYSQLStore, error) {
	// Register custom TLS config if provided
	if err := registerTLSConfig(cfg); err != nil {
		return nil, fmt.Errorf("failed to register TLS config: %w", err)
	}

	d, err := sqlx.Open("mysql", cfg.DSN)
	if err != nil {
		return nil, fmt.Errorf("couldn't open database : %v", err)
	}

	// Configure connection pool
	if cfg.MaxOpenConnections > 0 {
		d.SetMaxOpenConns(cfg.MaxOpenConnections)
	}
	if cfg.MaxIdleConnections > 0 {
		d.SetMaxIdleConns(cfg.MaxIdleConnections)
	}
	// Aggressive connection lifecycle management to prevent stale connections
	d.SetConnMaxLifetime(2 * time.Minute)  // Recycle connections every 2 minutes
	d.SetConnMaxIdleTime(30 * time.Second) // Close idle connections after 30 seconds

	// Test connection with timeout
	pingCtx, pingCancel := context.WithTimeout(ctx, 10*time.Second)
	defer pingCancel()
	if err := d.PingContext(pingCtx); err != nil {
		d.Close()
		return nil, fmt.Errorf("failed to ping database: %w", err)
	}

	if cfg.Automigrate {
		slog.Default().InfoContext(ctx, "applying migrations")
		// Run migrations with timeout
		migrateCtx, migrateCancel := context.WithTimeout(ctx, 5*time.Minute)
		defer migrateCancel()
		if err := MigrateWithContext(migrateCtx, d.Unsafe().DB); err != nil {
			d.Close()
			return nil, fmt.Errorf("migration failed: %w", err)
		}
	}

	ctx, c := context.WithCancel(ctx)
	ss := &MYSQLStore{
		db:    d,
		close: c,
	}

	go func() {
		<-ctx.Done()
		d.Close()
	}()

	return ss, nil
}

func (ms *MYSQLStore) Close() {
	ms.close()
}

// Ping checks database connectivity by executing a simple query
func (ms *MYSQLStore) Ping(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	// Use a simple query to check database connectivity
	var result int
	err := ms.db.QueryRowxContext(ctx, "SELECT 1").Scan(&result)
	if err != nil {
		return fmt.Errorf("database ping failed: %w", err)
	}
	return nil
}
