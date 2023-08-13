package store

import (
	"context"
	"database/sql"
	"embed"
	"fmt"
	"log"
	"time"

	_ "github.com/go-sql-driver/mysql"
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

const maxRetries = 5
const retryInterval = 5 * time.Second

// New connects to the database, applies migrations and returns a new MYSQLStore object
func New(ctx context.Context, cfg Config) (*MYSQLStore, error) {
	d, err := sqlx.Open("mysql", cfg.DSN)
	if err != nil {
		return nil, fmt.Errorf("couldn't open database : %v", err)
	}
	if cfg.Automigrate {
		log.Default().Printf("applying migrations")
		if err := Migrate(d.Unsafe().DB); err != nil {
			return nil, err
		}
	}

	if cfg.Automigrate {
		log.Default().Printf("applying migrations")
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
	log.Default().Printf("applied %d migrations", n)
	return nil
}

func (ms *MYSQLStore) Close() {
	ms.close()
}
