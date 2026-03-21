package config

import (
	"fmt"
	"os"
	"strings"

	bq "github.com/jekabolt/grbpwr-manager/internal/analytics/bigquery"
	"github.com/jekabolt/grbpwr-manager/internal/analytics/ga4"
	"github.com/jekabolt/grbpwr-manager/internal/analytics/ga4sync"
	httpapi "github.com/jekabolt/grbpwr-manager/internal/api/http"
	"github.com/jekabolt/grbpwr-manager/internal/apisrv/auth"
	"github.com/jekabolt/grbpwr-manager/internal/bucket"
	"github.com/jekabolt/grbpwr-manager/internal/mail"
	"github.com/jekabolt/grbpwr-manager/internal/ordercleanup"
	"github.com/jekabolt/grbpwr-manager/internal/payment/stripe"
	"github.com/jekabolt/grbpwr-manager/internal/storefrontcleanup"
	"github.com/jekabolt/grbpwr-manager/internal/revalidation"
	"github.com/jekabolt/grbpwr-manager/internal/store"
	"github.com/jekabolt/grbpwr-manager/internal/storefront"
	"github.com/jekabolt/grbpwr-manager/internal/stripereconcile"
	"github.com/jekabolt/grbpwr-manager/log"
	"github.com/spf13/viper"
)

// RatesConfig holds base currency (no exchange rates - metrics use product_price).
type RatesConfig struct {
	BaseCurrency string `mapstructure:"base_currency"`
}

// Config represents the global configuration for the service.
type Config struct {
	DB                store.Config           `mapstructure:"mysql"`
	Logger            log.Config             `mapstructure:"logger"`
	HTTP              httpapi.Config         `mapstructure:"http"`
	Auth              auth.Config            `mapstructure:"auth"`
	StorefrontAuth    storefront.Config      `mapstructure:"storefront_auth"`
	Bucket            bucket.Config          `mapstructure:"bucket"`
	Mailer            mail.Config            `mapstructure:"mailer"`
	OrderCleanup        ordercleanup.Config        `mapstructure:"order_cleanup"`
	StorefrontCleanup   storefrontcleanup.Config   `mapstructure:"storefront_cleanup"`
	StripeReconcile     stripereconcile.Config     `mapstructure:"stripe_reconcile"`
	Rates             RatesConfig            `mapstructure:"rates"`
	StripePayment     stripe.Config          `mapstructure:"stripe_payment"`
	StripePaymentTest stripe.Config          `mapstructure:"stripe_payment_test"`
	Revalidation      revalidation.Config    `mapstructure:"revalidation"`
	GA4               ga4.Config             `mapstructure:"ga4"`
	GA4Sync           ga4sync.Config         `mapstructure:"ga4_sync"`
	BigQuery          bq.Config              `mapstructure:"bigquery"`
}

// LoadConfig loads the configuration from a file and/or environment variables.
// Environment variables take precedence over config file values.
// Env vars use underscores and uppercase, e.g., MYSQL_DSN, AUTH_JWT_SECRET
// Nested config keys use double underscore, e.g., MYSQL__DSN for mysql.dsn
func LoadConfig(cfgFile string) (*Config, error) {
	viper.SetConfigType("toml")

	// Enable environment variable support
	// Viper will automatically read env vars and override config file values
	viper.AutomaticEnv()
	// Replace dots and dashes with underscores in env var names
	// e.g., mysql.dsn -> MYSQL__DSN, auth.jwt_secret -> AUTH__JWT_SECRET
	viper.SetEnvKeyReplacer(strings.NewReplacer(".", "__", "-", "__"))

	// Bind common environment variables to config keys
	// This allows using simpler env var names that match app.yaml
	bindEnvVars()

	// Try to read config file (optional - can work with env vars only)
	if cfgFile != "" {
		viper.SetConfigFile(cfgFile)
		if err := viper.ReadInConfig(); err != nil {
			// If config file doesn't exist, continue with env vars only
			if !os.IsNotExist(err) {
				return nil, fmt.Errorf("failed to read config file: %v", err)
			}
		}
	} else {
		viper.SetConfigName("config")
		viper.AddConfigPath("./config")
		viper.AddConfigPath("$HOME/config/grbpwr-products-manager")
		viper.AddConfigPath("/etc/grbpwr-products-manager")
		// Try to read config, but don't fail if it doesn't exist
		_ = viper.ReadInConfig()
	}

	var config Config
	if err := viper.Unmarshal(&config); err != nil {
		return nil, fmt.Errorf("failed to unmarshal config into struct: %v", err)
	}

	// Handle MySQL DSN construction from individual env vars if DSN is not set
	// Supports both MYSQL_* env vars and DigitalOcean's db.* env vars
	if config.DB.DSN == "" {
		var mysqlHost, mysqlPort, mysqlUser, mysqlPassword, mysqlDatabase string

		// Check for DigitalOcean's db.* env vars first
		if dbHost := os.Getenv("db.HOSTNAME"); dbHost != "" {
			mysqlHost = dbHost
			mysqlPort = os.Getenv("db.PORT")
			mysqlUser = os.Getenv("db.USERNAME")
			mysqlPassword = os.Getenv("db.PASSWORD")
			mysqlDatabase = os.Getenv("db.DATABASE")
		} else {
			// Fall back to MYSQL_* env vars
			mysqlHost = os.Getenv("MYSQL_HOST")
			mysqlPort = os.Getenv("MYSQL_PORT")
			mysqlUser = os.Getenv("MYSQL_USER")
			mysqlPassword = os.Getenv("MYSQL_PASSWORD")
			mysqlDatabase = os.Getenv("MYSQL_DATABASE")
		}

		if mysqlHost != "" {
			if mysqlPort == "" {
				mysqlPort = "3306"
			}
			if mysqlUser != "" && mysqlPassword != "" && mysqlDatabase != "" {
				// Construct DSN for DO managed database (with TLS)
				// Add connection validation and timeout parameters to prevent stale connections
				config.DB.DSN = fmt.Sprintf("%s:%s@tcp(%s:%s)/%s?charset=utf8&parseTime=true&tls=custom&timeout=10s&readTimeout=30s&writeTimeout=30s",
					mysqlUser, mysqlPassword, mysqlHost, mysqlPort, mysqlDatabase)
			}
		}
	}

	return &config, nil
}

// bindEnvVars binds environment variables to config keys
// This allows using both nested keys (MYSQL__DSN) and flat keys (MYSQL_DSN)
func bindEnvVars() {
	// MySQL
	viper.BindEnv("mysql.dsn", "MYSQL_DSN")
	viper.BindEnv("mysql.automigrate", "MYSQL_AUTOMIGRATE")
	viper.BindEnv("mysql.max_open_connections", "MYSQL_MAX_OPEN_CONNECTIONS")
	viper.BindEnv("mysql.max_idle_connections", "MYSQL_MAX_IDLE_CONNECTIONS")
	viper.BindEnv("mysql.tls_ca_path", "MYSQL_TLS_CA_PATH")

	// Logger
	viper.BindEnv("logger.level", "LOGGER_LEVEL")
	viper.BindEnv("logger.add_source", "LOGGER_ADD_SOURCE")

	// HTTP
	viper.BindEnv("http.port", "HTTP_PORT")
	viper.BindEnv("http.address", "HTTP_ADDRESS")
	viper.BindEnv("http.allowed_origins", "HTTP_ALLOWED_ORIGINS")

	// Auth
	viper.BindEnv("auth.jwt_secret", "AUTH_JWT_SECRET")
	viper.BindEnv("auth.jwt_issuer", "AUTH_JWT_ISSUER")
	viper.BindEnv("auth.jwt_audience", "AUTH_JWT_AUDIENCE")
	viper.BindEnv("auth.master_password", "AUTH_MASTER_PASSWORD")
	viper.BindEnv("auth.password_hasher_salt_size", "AUTH_PASSWORD_HASHER_SALT_SIZE")
	viper.BindEnv("auth.password_hasher_iterations", "AUTH_PASSWORD_HASHER_ITERATIONS")
	viper.BindEnv("auth.jwt_ttl", "AUTH_JWT_TTL")

	// Storefront account (customer JWT)
	viper.BindEnv("storefront_auth.access_jwt_secret", "STOREFRONT_AUTH_ACCESS_JWT_SECRET")
	viper.BindEnv("storefront_auth.access_jwt_issuer", "STOREFRONT_AUTH_ACCESS_JWT_ISSUER")
	viper.BindEnv("storefront_auth.access_jwt_audience", "STOREFRONT_AUTH_ACCESS_JWT_AUDIENCE")
	viper.BindEnv("storefront_auth.access_jti_revocation_enabled", "STOREFRONT_AUTH_ACCESS_JTI_REVOCATION_ENABLED")
	viper.BindEnv("storefront_auth.access_jwt_ttl", "STOREFRONT_AUTH_ACCESS_JWT_TTL")
	viper.BindEnv("storefront_auth.refresh_ttl", "STOREFRONT_AUTH_REFRESH_TTL")
	viper.BindEnv("storefront_auth.login_challenge_ttl", "STOREFRONT_AUTH_LOGIN_CHALLENGE_TTL")
	viper.BindEnv("storefront_auth.login_pepper", "STOREFRONT_AUTH_LOGIN_PEPPER")
	viper.BindEnv("storefront_auth.refresh_pepper", "STOREFRONT_AUTH_REFRESH_PEPPER")
	viper.BindEnv("storefront_auth.magic_link_base_url", "STOREFRONT_AUTH_MAGIC_LINK_BASE_URL")

	// Bucket
	viper.BindEnv("bucket.s3_access_key", "BUCKET_S3_ACCESS_KEY")
	viper.BindEnv("bucket.s3_secret_access_key", "BUCKET_S3_SECRET_ACCESS_KEY")
	viper.BindEnv("bucket.s3_endpoint", "BUCKET_S3_ENDPOINT")
	viper.BindEnv("bucket.s3_bucket_name", "BUCKET_S3_BUCKET_NAME")
	viper.BindEnv("bucket.s3_bucket_location", "BUCKET_S3_BUCKET_LOCATION")
	viper.BindEnv("bucket.base_folder", "BUCKET_BASE_FOLDER")
	viper.BindEnv("bucket.image_store_prefix", "BUCKET_IMAGE_STORE_PREFIX")
	viper.BindEnv("bucket.subdomain_endpoint", "BUCKET_SUBDOMAIN_ENDPOINT")

	// Mailer
	viper.BindEnv("mailer.sendgrid_api_key", "MAILER_SENDGRID_API_KEY")
	viper.BindEnv("mailer.from_email", "MAILER_FROM_EMAIL")
	viper.BindEnv("mailer.from_email_name", "MAILER_FROM_EMAIL_NAME")
	viper.BindEnv("mailer.reply_to", "MAILER_REPLY_TO")
	viper.BindEnv("mailer.worker_interval", "MAILER_WORKER_INTERVAL")

	// Order cleanup (stuck Placed orders)
	viper.BindEnv("order_cleanup.worker_interval", "ORDER_CLEANUP_WORKER_INTERVAL")
	viper.BindEnv("order_cleanup.placed_threshold", "ORDER_CLEANUP_PLACED_THRESHOLD")

	// Storefront cleanup (expired JTI denylist, login challenges, refresh tokens)
	viper.BindEnv("storefront_cleanup.worker_interval", "STOREFRONT_CLEANUP_WORKER_INTERVAL")

	// Stripe reconcile (orphaned pre-order PaymentIntents)
	viper.BindEnv("stripe_reconcile.worker_interval", "STRIPE_RECONCILE_WORKER_INTERVAL")
	viper.BindEnv("stripe_reconcile.pre_order_threshold", "STRIPE_RECONCILE_PRE_ORDER_THRESHOLD")

	// Rates (base currency only; no exchange rates)
	viper.BindEnv("rates.base_currency", "RATES_BASE_CURRENCY")

	// Stripe Payment
	viper.BindEnv("stripe_payment.secret_key", "STRIPE_PAYMENT_SECRET_KEY")
	viper.BindEnv("stripe_payment.pub_key", "STRIPE_PAYMENT_PUB_KEY")
	viper.BindEnv("stripe_payment.invoice_expiration", "STRIPE_PAYMENT_INVOICE_EXPIRATION")

	// Stripe Payment Test
	viper.BindEnv("stripe_payment_test.secret_key", "STRIPE_PAYMENT_TEST_SECRET_KEY")
	viper.BindEnv("stripe_payment_test.pub_key", "STRIPE_PAYMENT_TEST_PUB_KEY")
	viper.BindEnv("stripe_payment_test.invoice_expiration", "STRIPE_PAYMENT_TEST_INVOICE_EXPIRATION")

	// Revalidation
	viper.BindEnv("revalidation.project_id", "REVALIDATION_PROJECT_ID")
	viper.BindEnv("revalidation.vercel_api_token", "REVALIDATION_VERCEL_API_TOKEN")
	viper.BindEnv("revalidation.revalidate_secret", "REVALIDATION_REVALIDATE_SECRET")
	viper.BindEnv("revalidation.http_timeout", "REVALIDATION_HTTP_TIMEOUT")

	// GA4 Analytics
	viper.BindEnv("ga4.enabled", "GA4_ENABLED")
	viper.BindEnv("ga4.property_id", "GA4_PROPERTY_ID")
	viper.BindEnv("ga4.credentials_json", "GA4_CREDENTIALS_JSON")

	// GA4 Sync Worker
	viper.BindEnv("ga4_sync.worker_interval", "GA4_SYNC_WORKER_INTERVAL")
	viper.BindEnv("ga4_sync.bq_interval", "GA4_SYNC_BQ_INTERVAL")
	viper.BindEnv("ga4_sync.lookback_days", "GA4_SYNC_LOOKBACK_DAYS")
	viper.BindEnv("ga4_sync.retention_days", "GA4_SYNC_RETENTION_DAYS")
	viper.BindEnv("ga4_sync.max_backoff_retries", "GA4_SYNC_MAX_BACKOFF_RETRIES")
	viper.BindEnv("ga4_sync.initial_backoff", "GA4_SYNC_INITIAL_BACKOFF")
	viper.BindEnv("ga4_sync.max_backoff", "GA4_SYNC_MAX_BACKOFF")
	viper.BindEnv("ga4_sync.ga4_stale_threshold", "GA4_SYNC_GA4_STALE_THRESHOLD")
	viper.BindEnv("ga4_sync.bq_stale_threshold", "GA4_SYNC_BQ_STALE_THRESHOLD")

	// BigQuery
	viper.BindEnv("bigquery.project_id", "BIGQUERY_PROJECT_ID")
	viper.BindEnv("bigquery.dataset_id", "BIGQUERY_DATASET_ID")
	viper.BindEnv("bigquery.credentials_json", "BIGQUERY_CREDENTIALS_JSON")
	viper.BindEnv("bigquery.query_timeout", "BIGQUERY_QUERY_TIMEOUT")
	viper.BindEnv("bigquery.use_literal_dates", "BIGQUERY_USE_LITERAL_DATES")
	viper.BindEnv("bigquery.circuit_breaker.max_failures", "BIGQUERY_CIRCUIT_BREAKER_MAX_FAILURES")
	viper.BindEnv("bigquery.circuit_breaker.open_timeout", "BIGQUERY_CIRCUIT_BREAKER_OPEN_TIMEOUT")
	viper.BindEnv("bigquery.circuit_breaker.half_open_max_retries", "BIGQUERY_CIRCUIT_BREAKER_HALF_OPEN_MAX_RETRIES")

	// GA4 Circuit Breaker
	viper.BindEnv("ga4.circuit_breaker.max_failures", "GA4_CIRCUIT_BREAKER_MAX_FAILURES")
	viper.BindEnv("ga4.circuit_breaker.open_timeout", "GA4_CIRCUIT_BREAKER_OPEN_TIMEOUT")
	viper.BindEnv("ga4.circuit_breaker.half_open_max_retries", "GA4_CIRCUIT_BREAKER_HALF_OPEN_MAX_RETRIES")
}
