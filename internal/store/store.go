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
	"github.com/jekabolt/grbpwr-manager/internal/health"
	"github.com/jekabolt/grbpwr-manager/internal/store/account"
	"github.com/jekabolt/grbpwr-manager/internal/store/admin"
	"github.com/jekabolt/grbpwr-manager/internal/store/bqcache"
	"github.com/jekabolt/grbpwr-manager/internal/store/communication"
	"github.com/jekabolt/grbpwr-manager/internal/store/content"
	"github.com/jekabolt/grbpwr-manager/internal/store/fitting"
	"github.com/jekabolt/grbpwr-manager/internal/store/fulfillment"
	"github.com/jekabolt/grbpwr-manager/internal/store/ga4data"
	"github.com/jekabolt/grbpwr-manager/internal/store/language"
	"github.com/jekabolt/grbpwr-manager/internal/store/membership"
	"github.com/jekabolt/grbpwr-manager/internal/store/metrics"
	"github.com/jekabolt/grbpwr-manager/internal/store/model"
	"github.com/jekabolt/grbpwr-manager/internal/store/order"
	"github.com/jekabolt/grbpwr-manager/internal/store/product"
	"github.com/jekabolt/grbpwr-manager/internal/store/productionrun"
	"github.com/jekabolt/grbpwr-manager/internal/store/promo"
	"github.com/jekabolt/grbpwr-manager/internal/store/settings"
	"github.com/jekabolt/grbpwr-manager/internal/store/storeutil"
	"github.com/jekabolt/grbpwr-manager/internal/store/support"
	"github.com/jekabolt/grbpwr-manager/internal/store/task"
	"github.com/jekabolt/grbpwr-manager/internal/store/techcard"
	"github.com/jmoiron/sqlx"
	migrate "github.com/rubenv/sql-migrate"

	_ "github.com/golang-migrate/migrate/v4/database/mysql"
)

// Default connection-pool lifetimes used when the corresponding config field
// is unset (zero). A 5m lifetime avoids frequent TLS re-handshakes to managed
// MySQL under steady load while still cycling connections regularly.
const (
	defaultConnMaxLifetime = 5 * time.Minute
	defaultConnMaxIdleTime = 1 * time.Minute
)

// Config defines configurations to connect database
type Config struct {
	DSN                string        `mapstructure:"dsn"`
	Automigrate        bool          `mapstructure:"automigrate"`
	MaxOpenConnections int           `mapstructure:"max_open_connections"`
	MaxIdleConnections int           `mapstructure:"max_idle_connections"`
	ConnMaxLifetime    time.Duration `mapstructure:"conn_max_lifetime"`
	ConnMaxIdleTime    time.Duration `mapstructure:"conn_max_idle_time"`
	TLSCAPath          string        `mapstructure:"tls_ca_path"`
}

// connMaxLifetime returns the configured connection max lifetime or the default
// when unset.
func (c Config) connMaxLifetime() time.Duration {
	if c.ConnMaxLifetime > 0 {
		return c.ConnMaxLifetime
	}
	return defaultConnMaxLifetime
}

// connMaxIdleTime returns the configured connection max idle time or the default
// when unset.
func (c Config) connMaxIdleTime() time.Duration {
	if c.ConnMaxIdleTime > 0 {
		return c.ConnMaxIdleTime
	}
	return defaultConnMaxIdleTime
}

// MYSQLStore implements methods to access MYSQL database
type MYSQLStore struct {
	db    dependency.DB
	txDB  txDB
	ts    time.Time
	close context.CancelFunc

	// Sub-stores (composed for transaction propagation)
	productStore       *product.Store
	orderStore         *order.Store
	bqcache            *bqcache.Store
	ga4                *ga4data.Store
	syncStatus         *ga4data.SyncStatusStore
	metrics            *metrics.Store
	content            *content.Store
	settingsStore      *settings.Store
	comm               *communication.Store
	supportStore       *support.Store
	adminStore         *admin.Store
	promoStore         *promo.Store
	langStore          *language.Store
	accountStore       *account.Store
	membershipStore    *membership.Store
	modelStore         *model.Store
	fittingStore       *fitting.Store
	taskStore          *task.Store
	fulfillmentStore   *fulfillment.Store
	techCardStore      *techcard.Store
	productionRunStore *productionrun.Store
}

// resolveCertPath resolves @certs paths to the config/certs directory
func resolveCertPath(path string) string {
	if strings.HasPrefix(path, "@certs/") {
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
		return filepath.Join("./config/certs", certFile)
	}
	return path
}

// registerTLSConfig registers a custom TLS configuration with the MySQL driver
func registerTLSConfig(cfg Config) error {
	var caCert []byte
	var err error

	if dbCACert := os.Getenv("db.CA_CERT"); dbCACert != "" {
		caCert = []byte(dbCACert)
		slog.Default().Info("using CA certificate from db.CA_CERT environment variable")
	} else if cfg.TLSCAPath != "" {
		certPath := resolveCertPath(cfg.TLSCAPath)
		caCert, err = os.ReadFile(certPath)
		if err != nil {
			return fmt.Errorf("failed to read CA certificate from %s: %w", certPath, err)
		}
		slog.Default().Info("using CA certificate from file", "path", certPath)
	} else {
		return nil
	}

	caCertPool := x509.NewCertPool()
	if !caCertPool.AppendCertsFromPEM(caCert) {
		return fmt.Errorf("failed to parse CA certificate")
	}

	tlsConfig := &tls.Config{
		RootCAs: caCertPool,
	}

	mysql.RegisterTLSConfig("custom", tlsConfig)
	return nil
}

// New connects to the database, applies migrations and returns a new MYSQLStore object.
func New(ctx context.Context, cfg Config) (*MYSQLStore, error) {
	if err := registerTLSConfig(cfg); err != nil {
		return nil, fmt.Errorf("failed to register TLS config: %w", err)
	}

	d, err := sqlx.Open("mysql", cfg.DSN)
	if err != nil {
		return nil, fmt.Errorf("couldn't open database : %v", err)
	}

	if cfg.MaxOpenConnections > 0 {
		d.SetMaxOpenConns(cfg.MaxOpenConnections)
	}
	if cfg.MaxIdleConnections > 0 {
		d.SetMaxIdleConns(cfg.MaxIdleConnections)
	}
	d.SetConnMaxLifetime(cfg.connMaxLifetime())
	d.SetConnMaxIdleTime(cfg.connMaxIdleTime())

	pingCtx, pingCancel := context.WithTimeout(ctx, 10*time.Second)
	defer pingCancel()
	if err := d.PingContext(pingCtx); err != nil {
		d.Close()
		return nil, fmt.Errorf("failed to ping database: %w", err)
	}

	if cfg.Automigrate {
		slog.Default().InfoContext(ctx, "applying migrations")
		migrateCtx, migrateCancel := context.WithTimeout(ctx, 5*time.Minute)
		defer migrateCancel()
		if err := MigrateWithContext(migrateCtx, d.Unsafe().DB); err != nil {
			d.Close()
			return nil, fmt.Errorf("migration failed: %w", err)
		}
	}

	ctx, c := context.WithCancel(ctx)
	ss := &MYSQLStore{
		// Unsafe: ignore DB columns that have no matching struct field. Beta applies
		// every migration via automigrate ahead of prod, so a column added by a
		// migration whose struct field was not (yet) added would otherwise make every
		// SELECT * on that table fail (struct scan: missing destination name ...).
		// This keeps reads resilient to that schema/struct drift; transactions inherit
		// the flag from the DB in sqlx.
		db:    d.Unsafe(),
		close: c,
	}
	initSubStores(ss)

	di, err := ss.settingsStore.GetDictionaryInfo(ctx)
	if err != nil {
		return nil, fmt.Errorf("can't get dictionary info: %w", err)
	}

	hf, err := ss.Hero().GetHero(ctx)
	if err != nil {
		return nil, fmt.Errorf("can't get hero: %w", err)
	}

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
	if err := registerTLSConfig(cfg); err != nil {
		return nil, fmt.Errorf("failed to register TLS config: %w", err)
	}

	d, err := sqlx.Open("mysql", cfg.DSN)
	if err != nil {
		return nil, fmt.Errorf("couldn't open database : %v", err)
	}

	if cfg.MaxOpenConnections > 0 {
		d.SetMaxOpenConns(cfg.MaxOpenConnections)
	}
	if cfg.MaxIdleConnections > 0 {
		d.SetMaxIdleConns(cfg.MaxIdleConnections)
	}
	d.SetConnMaxLifetime(cfg.connMaxLifetime())
	d.SetConnMaxIdleTime(cfg.connMaxIdleTime())

	pingCtx, pingCancel := context.WithTimeout(ctx, 10*time.Second)
	defer pingCancel()
	if err := d.PingContext(pingCtx); err != nil {
		d.Close()
		return nil, fmt.Errorf("failed to ping database: %w", err)
	}

	if cfg.Automigrate {
		slog.Default().InfoContext(ctx, "applying migrations")
		migrateCtx, migrateCancel := context.WithTimeout(ctx, 5*time.Minute)
		defer migrateCancel()
		if err := MigrateWithContext(migrateCtx, d.Unsafe().DB); err != nil {
			d.Close()
			return nil, fmt.Errorf("migration failed: %w", err)
		}
	}

	ctx, c := context.WithCancel(ctx)
	ss := &MYSQLStore{
		// Unsafe: ignore DB columns that have no matching struct field. Beta applies
		// every migration via automigrate ahead of prod, so a column added by a
		// migration whose struct field was not (yet) added would otherwise make every
		// SELECT * on that table fail (struct scan: missing destination name ...).
		// This keeps reads resilient to that schema/struct drift; transactions inherit
		// the flag from the DB in sqlx.
		db:    d.Unsafe(),
		close: c,
	}
	initSubStores(ss)

	go func() {
		<-ctx.Done()
		d.Close()
	}()

	return ss, nil
}

// initSubStores initializes composed sub-stores for the given MYSQLStore.
func initSubStores(ms *MYSQLStore) {
	base := storeutil.Base{DB: ms.db, Now: ms.Now}
	ms.langStore = language.New(base)
	ms.promoStore = promo.New(base)
	ms.comm = communication.New(base)
	ms.supportStore = support.New(base)
	ms.adminStore = admin.New(base, ms.Tx)
	ms.settingsStore = settings.New(base, ms.Tx, func() dependency.Repository { return ms })
	ms.productStore = product.New(base, ms.Tx, func() dependency.Repository { return ms })
	ms.bqcache = bqcache.New(base)
	ms.ga4 = ga4data.New(base, ms.Tx)
	ms.syncStatus = ga4data.NewSyncStatus(base, ms.Tx)
	ms.metrics = metrics.New(base, ms)
	ms.content = content.New(base, ms.Tx, func() dependency.Repository { return ms })
	ms.orderStore = order.New(base, ms.Tx, func() dependency.Repository { return ms })
	ms.accountStore = account.New(base, ms.Tx)
	ms.membershipStore = membership.New(base, ms.Tx)
	ms.modelStore = model.New(base, ms.Tx)
	ms.fittingStore = fitting.New(base, ms.Tx)
	ms.taskStore = task.New(base, ms.Tx)
	ms.fulfillmentStore = fulfillment.New(base, ms.Tx)
	ms.techCardStore = techcard.New(base, ms.Tx)
	ms.productionRunStore = productionrun.New(base, ms.Tx)
}

// initSubStoresForTx initializes sub-stores for a transactional MYSQLStore.
func initSubStoresForTx(txStore *MYSQLStore, outerTx func(context.Context, func(context.Context, dependency.Repository) error) error) {
	base := storeutil.Base{DB: txStore.db, Now: txStore.Now}
	txStore.langStore = language.New(base)
	txStore.promoStore = promo.New(base)
	txStore.comm = communication.New(base)
	txStore.supportStore = support.New(base)
	txStore.adminStore = admin.New(base, outerTx)
	txStore.settingsStore = settings.New(base, outerTx, func() dependency.Repository { return txStore })
	txStore.productStore = product.New(base, outerTx, func() dependency.Repository { return txStore })
	txStore.bqcache = bqcache.New(base)
	txStore.ga4 = ga4data.New(base, outerTx)
	txStore.syncStatus = ga4data.NewSyncStatus(base, outerTx)
	txStore.metrics = metrics.New(base, txStore)
	txStore.content = content.New(base, outerTx, func() dependency.Repository { return txStore })
	txStore.orderStore = order.New(base, outerTx, func() dependency.Repository { return txStore })
	txStore.accountStore = account.New(base, outerTx)
	txStore.membershipStore = membership.New(base, outerTx)
	txStore.modelStore = model.New(base, outerTx)
	txStore.fittingStore = fitting.New(base, outerTx)
	txStore.taskStore = task.New(base, outerTx)
	txStore.fulfillmentStore = fulfillment.New(base, outerTx)
	txStore.techCardStore = techcard.New(base, outerTx)
	txStore.productionRunStore = productionrun.New(base, outerTx)
}

func (ms *MYSQLStore) Close() {
	ms.close()
}

// Stats returns connection-pool statistics for the underlying *sql.DB, mapped
// into health.DBStats so the http/health packages don't import database/sql or
// the store. ms.db is a *sqlx.DB (see store.New: d.Unsafe()), which embeds the
// *sql.DB whose Stats() this reports. Returns a zero value if the handle does
// not expose Stats (e.g. inside a transaction), so the status endpoint degrades
// gracefully instead of panicking.
func (ms *MYSQLStore) Stats() health.DBStats {
	type statser interface{ Stats() sql.DBStats }
	s, ok := ms.db.(statser)
	if !ok {
		return health.DBStats{}
	}
	st := s.Stats()
	return health.DBStats{
		OpenConnections:    st.OpenConnections,
		InUse:              st.InUse,
		Idle:               st.Idle,
		WaitCount:          st.WaitCount,
		WaitDuration:       st.WaitDuration,
		MaxOpenConnections: st.MaxOpenConnections,
	}
}

// Ping checks database connectivity by executing a simple query
func (ms *MYSQLStore) Ping(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	var result int
	err := ms.db.QueryRowxContext(ctx, "SELECT 1").Scan(&result)
	if err != nil {
		return fmt.Errorf("database ping failed: %w", err)
	}
	return nil
}

// ========== Repository Accessor Methods ==========

func (ms *MYSQLStore) Products() dependency.Products             { return ms.productStore }
func (ms *MYSQLStore) Order() dependency.Order                   { return ms.orderStore }
func (ms *MYSQLStore) BQCache() dependency.BQCacheStore          { return ms.bqcache }
func (ms *MYSQLStore) GA4Data() dependency.GA4DataStore          { return ms.ga4 }
func (ms *MYSQLStore) SyncStatus() dependency.SyncStatusStore    { return ms.syncStatus }
func (ms *MYSQLStore) Metrics() dependency.Metrics               { return ms.metrics }
func (ms *MYSQLStore) Retention() dependency.Retention           { return ms.metrics }
func (ms *MYSQLStore) Inventory() dependency.Inventory           { return ms.metrics }
func (ms *MYSQLStore) Analytics() dependency.Analytics           { return ms.metrics }
func (ms *MYSQLStore) Hero() dependency.Hero                     { return ms.content }
func (ms *MYSQLStore) Archive() dependency.Archive               { return ms.content }
func (ms *MYSQLStore) Media() dependency.Media                   { return ms.content }
func (ms *MYSQLStore) Settings() dependency.Settings             { return ms.settingsStore }
func (ms *MYSQLStore) Cache() dependency.Cache                   { return ms.settingsStore }
func (ms *MYSQLStore) Mail() dependency.Mail                     { return ms.comm }
func (ms *MYSQLStore) Subscribers() dependency.Subscribers       { return ms.comm }
func (ms *MYSQLStore) Support() dependency.Support               { return ms.supportStore }
func (ms *MYSQLStore) Admin() dependency.Admin                   { return ms.adminStore }
func (ms *MYSQLStore) Promo() dependency.Promo                   { return ms.promoStore }
func (ms *MYSQLStore) Language() dependency.Language             { return ms.langStore }
func (ms *MYSQLStore) Membership() dependency.Membership         { return ms.membershipStore }
func (ms *MYSQLStore) Models() dependency.Models                 { return ms.modelStore }
func (ms *MYSQLStore) Fittings() dependency.Fittings             { return ms.fittingStore }
func (ms *MYSQLStore) Tasks() dependency.Tasks                   { return ms.taskStore }
func (ms *MYSQLStore) Fulfillment() dependency.Fulfillment       { return ms.fulfillmentStore }
func (ms *MYSQLStore) TechCards() dependency.TechCards           { return ms.techCardStore }
func (ms *MYSQLStore) ProductionRuns() dependency.ProductionRuns { return ms.productionRunStore }
func (ms *MYSQLStore) StorefrontAccount() dependency.StorefrontAccount {
	return ms.accountStore
}

// ErrOrderItemsUpdated is re-exported from the order sub-package for backward compatibility.
var ErrOrderItemsUpdated = order.ErrOrderItemsUpdated

// ErrPaymentIntentAlreadyAssociated is re-exported from the order sub-package.
var ErrPaymentIntentAlreadyAssociated = order.ErrPaymentIntentAlreadyAssociated

// ValidStatusTransitions is re-exported from the order sub-package for backward compatibility.
var ValidStatusTransitions = order.ValidStatusTransitions

// ErrProductInOrders is re-exported from the product sub-package for backward compatibility.
var ErrProductInOrders = product.ErrProductInOrders
