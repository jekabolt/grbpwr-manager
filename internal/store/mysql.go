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
// If TLSCAPath is provided, registers a TLS config named "custom"
func registerTLSConfig(cfg Config) error {
	if cfg.TLSCAPath == "" {
		return nil // No TLS config needed
	}

	certPath := resolveCertPath(cfg.TLSCAPath)
	caCert, err := os.ReadFile(certPath)
	if err != nil {
		return fmt.Errorf("failed to read CA certificate from %s: %w", certPath, err)
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

// New connects to the database, applies migrations and returns a new MYSQLStore object
func New(ctx context.Context, cfg Config) (*MYSQLStore, error) {
	// Register custom TLS config if provided
	if err := registerTLSConfig(cfg); err != nil {
		return nil, fmt.Errorf("failed to register TLS config: %w", err)
	}

	d, err := sqlx.Open("mysql", cfg.DSN)
	if err != nil {
		return nil, fmt.Errorf("couldn't open database : %v", err)
	}

	if cfg.Automigrate {
		slog.Default().InfoContext(ctx, "applying migrations")
		if err := Migrate(d.Unsafe().DB); err != nil {
			return nil, err
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
	m := &migrate.EmbedFileSystemMigrationSource{
		FileSystem: fs,
		Root:       "sql",
	}
	n, err := migrate.Exec(db, "mysql", m, migrate.Up)
	if err != nil {
		return fmt.Errorf("db migrations have failed: %v", err)
	}

	slog.Default().Info("applied migrations",
		slog.Int("count", n),
	)
	return nil
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

	if cfg.Automigrate {
		slog.Default().InfoContext(ctx, "applying migrations")
		if err := Migrate(d.Unsafe().DB); err != nil {
			return nil, err
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
