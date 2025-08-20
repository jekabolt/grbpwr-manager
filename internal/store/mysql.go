package store

import (
	"context"
	"database/sql"
	"embed"
	"fmt"
	"time"

	"log/slog"

	_ "github.com/go-sql-driver/mysql"
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
}

// MYSQLStore implements methods to access MYSQL database
type MYSQLStore struct {
	// db is used for executing queries
	db    dependency.DB
	txDB  txDB
	ts    time.Time
	close context.CancelFunc
}

// New connects to the database, applies migrations and returns a new MYSQLStore object
func New(ctx context.Context, cfg Config) (*MYSQLStore, error) {
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

	topCategories, err := ss.Products().GetTopCategoriesProductCount(ctx, false)
	if err != nil {
		return nil, fmt.Errorf("can't get top categories product count: %w", err)
	}

	subCategories, err := ss.Products().GetSubCategoriesProductCount(ctx, false)
	if err != nil {
		return nil, fmt.Errorf("can't get sub categories product count: %w", err)
	}

	// cache initialization
	err = cache.InitConsts(ctx, di, hf, topCategories, subCategories)
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
