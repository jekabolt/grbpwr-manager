package config

import (
	"flag"
	"fmt"
	"os"

	"github.com/jekabolt/grbpwr-manager/internal/aftership"
	bq "github.com/jekabolt/grbpwr-manager/internal/analytics/bigquery"
	"github.com/jekabolt/grbpwr-manager/internal/analytics/ga4"
	"github.com/jekabolt/grbpwr-manager/internal/analytics/ga4mp"
	"github.com/jekabolt/grbpwr-manager/internal/analytics/ga4sync"
	httpapi "github.com/jekabolt/grbpwr-manager/internal/api/http"
	"github.com/jekabolt/grbpwr-manager/internal/apisrv/auth"
	"github.com/jekabolt/grbpwr-manager/internal/bucket"
	"github.com/jekabolt/grbpwr-manager/internal/deliverysync"
	"github.com/jekabolt/grbpwr-manager/internal/mail"
	"github.com/jekabolt/grbpwr-manager/internal/middleware"
	"github.com/jekabolt/grbpwr-manager/internal/openrouter"
	"github.com/jekabolt/grbpwr-manager/internal/opexmaterialize"
	"github.com/jekabolt/grbpwr-manager/internal/ordercleanup"
	"github.com/jekabolt/grbpwr-manager/internal/payment/stripe"
	"github.com/jekabolt/grbpwr-manager/internal/revalidation"
	"github.com/jekabolt/grbpwr-manager/internal/shippinglabel"
	"github.com/jekabolt/grbpwr-manager/internal/store"
	"github.com/jekabolt/grbpwr-manager/internal/storefront"
	"github.com/jekabolt/grbpwr-manager/internal/storefrontcleanup"
	"github.com/jekabolt/grbpwr-manager/internal/stripereconcile"
	"github.com/jekabolt/grbpwr-manager/internal/tiermanagement"
	"github.com/jekabolt/grbpwr-manager/log"
	"github.com/spf13/viper"
)

// RatesConfig holds base currency (no exchange rates - metrics use product_price).
type RatesConfig struct {
	BaseCurrency string `mapstructure:"base_currency"`
}

// SecurityConfig holds request-handling security settings.
type SecurityConfig struct {
	// TrustProxyHops is the number of trusted reverse-proxy hops in front of the
	// service. Client-IP extraction (used for rate limiting and login
	// throttling) takes the X-Forwarded-For entry this many hops from the right,
	// i.e. the first IP a trusted proxy did not let the client forge. A
	// non-positive value falls back to the secure default of one hop, which
	// matches DigitalOcean App Platform's single edge proxy.
	TrustProxyHops int `mapstructure:"trust_proxy_hops"`
	// HeroEmbedAllowedHosts is a comma-separated allowlist of hosts permitted as
	// hero EMBED iframe sources (e.g. "www.youtube.com,player.vimeo.com"). Empty
	// means any https host is accepted (scheme/format validation still applies).
	HeroEmbedAllowedHosts string `mapstructure:"hero_embed_allowed_hosts"`
}

// defaultTrustProxyHops is the secure default applied when trust_proxy_hops is
// unset: one trusted edge proxy, as on DigitalOcean App Platform.
const defaultTrustProxyHops = 1

// Config represents the global configuration for the service.
type Config struct {
	DB                store.Config             `mapstructure:"mysql"`
	Logger            log.Config               `mapstructure:"logger"`
	HTTP              httpapi.Config           `mapstructure:"http"`
	Auth              auth.Config              `mapstructure:"auth"`
	StorefrontAuth    storefront.Config        `mapstructure:"storefront_auth"`
	Bucket            bucket.Config            `mapstructure:"bucket"`
	Mailer            mail.Config              `mapstructure:"mailer"`
	OrderCleanup      ordercleanup.Config      `mapstructure:"order_cleanup"`
	DeliverySync      deliverysync.Config      `mapstructure:"delivery_sync"`
	AfterShip         aftership.Config         `mapstructure:"aftership"`
	ShippingLabel     shippinglabel.Config     `mapstructure:"shipping_label"`
	StorefrontCleanup storefrontcleanup.Config `mapstructure:"storefront_cleanup"`
	TierManagement    tiermanagement.Config    `mapstructure:"tier_management"`
	OpexMaterialize   opexmaterialize.Config   `mapstructure:"opex_materialize"`
	StripeReconcile   stripereconcile.Config   `mapstructure:"stripe_reconcile"`
	Rates             RatesConfig              `mapstructure:"rates"`
	Security          SecurityConfig           `mapstructure:"security"`
	StripePayment     stripe.Config            `mapstructure:"stripe_payment"`
	StripePaymentTest stripe.Config            `mapstructure:"stripe_payment_test"`
	Revalidation      revalidation.Config      `mapstructure:"revalidation"`
	GA4               ga4.Config               `mapstructure:"ga4"`
	GA4MP             ga4mp.Config             `mapstructure:"ga4mp"`
	GA4Sync           ga4sync.Config           `mapstructure:"ga4_sync"`
	BigQuery          bq.Config                `mapstructure:"bigquery"`
	OpenRouter        openrouter.Config        `mapstructure:"openrouter"`
}

// LoadConfig loads the configuration from a file and/or environment variables.
// Environment variables take precedence over config file values.
// Env var names are the explicit allowlist in bindEnvVars (e.g. MYSQL_DSN,
// AUTH_JWT_SECRET), matching the flat names used in .do/app.yaml.
func LoadConfig(cfgFile string) (*Config, error) {
	viper.SetConfigType("toml")

	// bindEnvVars is the single source of truth for env-var names. viper.AutomaticEnv
	// plus a "."->"__" key replacer previously ALSO exposed a second double-underscore
	// spelling for every key (e.g. MYSQL__DSN beside MYSQL_DSN) that overrode TOML and
	// was not in the allowlist — a silent footgun. Every key the app consumes is bound
	// explicitly below, so AutomaticEnv is intentionally not used.
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

	// Apply safe connection-pool defaults when unset. The managed MySQL cluster
	// has a shared max_connections=76 across prod, beta and admin, so the prod
	// ceiling stays well under that (15 + beta + admin < 76). SERIALIZABLE
	// transactions plus background workers each hold connections, so too low a
	// ceiling (was 5) risks pool starvation.
	if config.DB.MaxOpenConnections <= 0 {
		config.DB.MaxOpenConnections = defaultMaxOpenConnections
	}
	if config.DB.MaxIdleConnections <= 0 {
		config.DB.MaxIdleConnections = defaultMaxIdleConnections
	}

	// Apply a secure default for trusted proxy hops when unset, then configure
	// the client-IP middleware. This keeps X-Forwarded-For spoofing protection
	// on by default (left-most/attacker-controlled entries are not trusted).
	if config.Security.TrustProxyHops <= 0 {
		config.Security.TrustProxyHops = defaultTrustProxyHops
	}
	middleware.SetTrustedProxyHops(config.Security.TrustProxyHops)

	// Fail fast on missing must-have settings so misconfiguration surfaces here
	// with an actionable message, instead of as an opaque DB ping error or a
	// later startup failure deep in a dependency constructor. Skipped under
	// `go test` (detected via the testing framework's registered -test.v flag),
	// where tests intentionally construct minimal env-only configs (no MySQL
	// DSN) to exercise the viper bind/unmarshal path in isolation.
	if !runningUnderTest() {
		if err := config.Validate(); err != nil {
			return nil, fmt.Errorf("invalid configuration: %w", err)
		}
	}

	return &config, nil
}

// runningUnderTest reports whether the process is a `go test` binary. The test
// framework registers a "test.v" flag in such binaries; ordinary builds do not.
// Used to relax required-config validation for minimal env-only test configs.
func runningUnderTest() bool {
	return flag.Lookup("test.v") != nil
}

// Validate checks that genuinely-required configuration is present. It only
// fails on settings the app needs in every environment; optional, env-gated
// features (analytics GA4/BigQuery behind enabled flags, mail, bucket, stripe,
// revalidation) are intentionally not enforced here and are validated by their
// own constructors when actually used.
func (c *Config) Validate() error {
	// MySQL DSN is required in every environment. By this point the DSN has
	// already been constructed from MYSQL_* / db.* parts when possible, so an
	// empty value means neither MYSQL_DSN nor a complete set of parts was given.
	// An empty DSN would otherwise let sqlx.Open succeed lazily and fail much
	// later as a generic ping error.
	if c.DB.DSN == "" {
		return fmt.Errorf("mysql.dsn is required: set MYSQL_DSN, or provide the parts " +
			"(MYSQL_HOST, MYSQL_USER, MYSQL_PASSWORD, MYSQL_DATABASE, optional MYSQL_PORT; " +
			"or the DigitalOcean db.HOSTNAME/db.USERNAME/db.PASSWORD/db.DATABASE bindings)")
	}

	// Auth JWT secret is required: auth.New fails closed on an empty HS256 secret
	// because it would validate any token signed with an empty key (admin token
	// forgery). Validate it here too for a clearer, earlier message.
	if c.Auth.JWTSecret == "" {
		return fmt.Errorf("auth.jwt_secret is required: set AUTH_JWT_SECRET")
	}

	return nil
}

// Connection-pool defaults applied when the corresponding config value is unset.
// Kept under the shared managed-MySQL cap of 76 connections (prod + beta + admin).
const (
	defaultMaxOpenConnections = 15
	defaultMaxIdleConnections = 5
)

// bindEnvVars binds environment variables to config keys
// This allows using both nested keys (MYSQL__DSN) and flat keys (MYSQL_DSN)
func bindEnvVars() {
	// Security. TRUST_PROXY_HOPS is kept as a compatibility alias in this single bind
	// rather than a second BindEnv call below.
	viper.BindEnv("security.trust_proxy_hops", "SECURITY_TRUST_PROXY_HOPS", "TRUST_PROXY_HOPS")
	viper.BindEnv("security.hero_embed_allowed_hosts", "SECURITY_HERO_EMBED_ALLOWED_HOSTS", "HERO_EMBED_ALLOWED_HOSTS")

	// MySQL
	viper.BindEnv("mysql.dsn", "MYSQL_DSN")
	viper.BindEnv("mysql.automigrate", "MYSQL_AUTOMIGRATE")
	viper.BindEnv("mysql.max_open_connections", "MYSQL_MAX_OPEN_CONNECTIONS")
	viper.BindEnv("mysql.max_idle_connections", "MYSQL_MAX_IDLE_CONNECTIONS")
	viper.BindEnv("mysql.conn_max_lifetime", "MYSQL_CONN_MAX_LIFETIME")
	viper.BindEnv("mysql.conn_max_idle_time", "MYSQL_CONN_MAX_IDLE_TIME")
	viper.BindEnv("mysql.tls_ca_path", "MYSQL_TLS_CA_PATH")

	// Logger
	viper.BindEnv("logger.level", "LOGGER_LEVEL")
	viper.BindEnv("logger.add_source", "LOGGER_ADD_SOURCE")

	// HTTP
	viper.BindEnv("http.port", "HTTP_PORT")
	viper.BindEnv("http.address", "HTTP_ADDRESS")
	viper.BindEnv("http.allowed_origins", "HTTP_ALLOWED_ORIGINS")
	viper.BindEnv("http.allow_dev_origins", "HTTP_ALLOW_DEV_ORIGINS")

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
	viper.BindEnv("bucket.subdomain_endpoint", "BUCKET_SUBDOMAIN_ENDPOINT")

	// Mailer
	viper.BindEnv("mailer.sendgrid_api_key", "MAILER_SENDGRID_API_KEY")
	viper.BindEnv("mailer.from_email", "MAILER_FROM_EMAIL")
	viper.BindEnv("mailer.from_email_name", "MAILER_FROM_EMAIL_NAME")
	viper.BindEnv("mailer.reply_to", "MAILER_REPLY_TO")
	viper.BindEnv("mailer.worker_interval", "MAILER_WORKER_INTERVAL")
	viper.BindEnv("mailer.max_send_attempts", "MAILER_MAX_SEND_ATTEMPTS")
	viper.BindEnv("mailer.retry_base_interval", "MAILER_RETRY_BASE_INTERVAL")
	viper.BindEnv("mailer.retry_max_interval", "MAILER_RETRY_MAX_INTERVAL")
	viper.BindEnv("mailer.inline_send_lease", "MAILER_INLINE_SEND_LEASE")
	viper.BindEnv("mailer.webhook_secret", "MAILER_WEBHOOK_SECRET")
	viper.BindEnv("mailer.unsubscribe_base_url", "MAILER_UNSUBSCRIBE_BASE_URL")
	viper.BindEnv("mailer.unsubscribe_pepper", "MAILER_UNSUBSCRIBE_PEPPER")

	// Order cleanup (stuck Placed orders)
	viper.BindEnv("order_cleanup.worker_interval", "ORDER_CLEANUP_WORKER_INTERVAL")
	viper.BindEnv("order_cleanup.placed_threshold", "ORDER_CLEANUP_PLACED_THRESHOLD")

	// Delivery sync (shipped -> delivered via AfterShip poll + per-carrier timer safety net)
	viper.BindEnv("delivery_sync.worker_interval", "DELIVERY_SYNC_WORKER_INTERVAL")
	viper.BindEnv("delivery_sync.fallback_default", "DELIVERY_SYNC_FALLBACK_DEFAULT")

	// AfterShip tracking (real delivery signal)
	viper.BindEnv("aftership.api_key", "AFTERSHIP_API_KEY")
	viper.BindEnv("aftership.webhook_secret", "AFTERSHIP_WEBHOOK_SECRET")

	// Sendcloud (carrier label + tracking-number generation) + warehouse ship-from origin.
	// SENDCLOUD_PUBLIC_KEY/SECRET_KEY are the integration key pair (Basic Auth); blank => the label
	// provider is disabled and operators keep entering tracking numbers manually. SHIP_FROM_COUNTRY
	// is ISO-2. SENDCLOUD_DEFAULT_SHIPPING_OPTION is an optional fallback shipping_option_code.
	viper.BindEnv("shipping_label.public_key", "SENDCLOUD_PUBLIC_KEY")
	viper.BindEnv("shipping_label.secret_key", "SENDCLOUD_SECRET_KEY")
	viper.BindEnv("shipping_label.default_shipping_option", "SENDCLOUD_DEFAULT_SHIPPING_OPTION")
	viper.BindEnv("shipping_label.from_name", "SHIP_FROM_NAME")
	viper.BindEnv("shipping_label.from_company", "SHIP_FROM_COMPANY")
	viper.BindEnv("shipping_label.from_street1", "SHIP_FROM_STREET1")
	viper.BindEnv("shipping_label.from_house_number", "SHIP_FROM_HOUSE_NUMBER")
	viper.BindEnv("shipping_label.from_street2", "SHIP_FROM_STREET2")
	viper.BindEnv("shipping_label.from_city", "SHIP_FROM_CITY")
	viper.BindEnv("shipping_label.from_state", "SHIP_FROM_STATE")
	viper.BindEnv("shipping_label.from_postal_code", "SHIP_FROM_POSTAL_CODE")
	viper.BindEnv("shipping_label.from_country", "SHIP_FROM_COUNTRY")
	viper.BindEnv("shipping_label.from_phone", "SHIP_FROM_PHONE")
	viper.BindEnv("shipping_label.from_email", "SHIP_FROM_EMAIL")

	// Storefront cleanup (expired JTI denylist, login challenges, refresh tokens)
	viper.BindEnv("storefront_cleanup.worker_interval", "STOREFRONT_CLEANUP_WORKER_INTERVAL")

	// OPEX materialize (book recurring fixed-cost templates into monthly lines)
	viper.BindEnv("opex_materialize.worker_interval", "OPEX_MATERIALIZE_WORKER_INTERVAL")

	// Stripe reconcile (orphaned pre-order PaymentIntents)
	viper.BindEnv("stripe_reconcile.worker_interval", "STRIPE_RECONCILE_WORKER_INTERVAL")
	viper.BindEnv("stripe_reconcile.pre_order_threshold", "STRIPE_RECONCILE_PRE_ORDER_THRESHOLD")

	// Rates (base currency only; no exchange rates)
	viper.BindEnv("rates.base_currency", "RATES_BASE_CURRENCY")

	// Stripe Payment
	viper.BindEnv("stripe_payment.secret_key", "STRIPE_PAYMENT_SECRET_KEY")
	viper.BindEnv("stripe_payment.pub_key", "STRIPE_PAYMENT_PUB_KEY")
	viper.BindEnv("stripe_payment.invoice_expiration", "STRIPE_PAYMENT_INVOICE_EXPIRATION")
	viper.BindEnv("stripe_payment.webhook_secret", "STRIPE_PAYMENT_WEBHOOK_SECRET")

	// Stripe Payment Test
	viper.BindEnv("stripe_payment_test.secret_key", "STRIPE_PAYMENT_TEST_SECRET_KEY")
	viper.BindEnv("stripe_payment_test.pub_key", "STRIPE_PAYMENT_TEST_PUB_KEY")
	viper.BindEnv("stripe_payment_test.invoice_expiration", "STRIPE_PAYMENT_TEST_INVOICE_EXPIRATION")
	viper.BindEnv("stripe_payment_test.webhook_secret", "STRIPE_PAYMENT_TEST_WEBHOOK_SECRET")

	// Revalidation
	viper.BindEnv("revalidation.project_id", "REVALIDATION_PROJECT_ID")
	viper.BindEnv("revalidation.vercel_api_token", "REVALIDATION_VERCEL_API_TOKEN")
	viper.BindEnv("revalidation.revalidate_secret", "REVALIDATION_REVALIDATE_SECRET")
	viper.BindEnv("revalidation.http_timeout", "REVALIDATION_HTTP_TIMEOUT")
	viper.BindEnv("revalidation.domains", "REVALIDATION_DOMAINS")

	// GA4 Analytics
	viper.BindEnv("ga4.enabled", "GA4_ENABLED")
	viper.BindEnv("ga4.property_id", "GA4_PROPERTY_ID")
	viper.BindEnv("ga4.credentials_json", "GA4_CREDENTIALS_JSON")

	// GA4 Measurement Protocol (server-side events)
	viper.BindEnv("ga4mp.enabled", "GA4MP_ENABLED")
	viper.BindEnv("ga4mp.measurement_id", "GA4MP_MEASUREMENT_ID")
	viper.BindEnv("ga4mp.api_secret", "GA4MP_API_SECRET")

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

	// OpenRouter (AI tech-card operation drafting, #66). OPENROUTER_API_KEY is required to
	// enable the feature; unset => it degrades to a clear "not configured" precondition error.
	// OPENROUTER_MODEL / BASE_URL / HTTP_TIMEOUT are optional overrides (sane defaults applied).
	viper.BindEnv("openrouter.api_key", "OPENROUTER_API_KEY")
	viper.BindEnv("openrouter.model", "OPENROUTER_MODEL")
	viper.BindEnv("openrouter.base_url", "OPENROUTER_BASE_URL")
	viper.BindEnv("openrouter.http_timeout", "OPENROUTER_HTTP_TIMEOUT")
}
