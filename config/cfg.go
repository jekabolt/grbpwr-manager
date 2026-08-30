package config

import (
	"flag"
	"fmt"
	"net/url"
	"os"
	"strings"

	"github.com/jekabolt/grbpwr-manager/internal/acctposting"
	"github.com/jekabolt/grbpwr-manager/internal/aftership"
	bq "github.com/jekabolt/grbpwr-manager/internal/analytics/bigquery"
	"github.com/jekabolt/grbpwr-manager/internal/analytics/ga4"
	"github.com/jekabolt/grbpwr-manager/internal/analytics/ga4mp"
	"github.com/jekabolt/grbpwr-manager/internal/analytics/ga4sync"
	httpapi "github.com/jekabolt/grbpwr-manager/internal/api/http"
	"github.com/jekabolt/grbpwr-manager/internal/apisrv/auth"
	"github.com/jekabolt/grbpwr-manager/internal/bucket"
	"github.com/jekabolt/grbpwr-manager/internal/campaigndispatch"
	"github.com/jekabolt/grbpwr-manager/internal/deliverysync"
	"github.com/jekabolt/grbpwr-manager/internal/designgen"
	"github.com/jekabolt/grbpwr-manager/internal/fxsync"
	"github.com/jekabolt/grbpwr-manager/internal/mail"
	"github.com/jekabolt/grbpwr-manager/internal/marketingaggregate"
	"github.com/jekabolt/grbpwr-manager/internal/meshy"
	"github.com/jekabolt/grbpwr-manager/internal/middleware"
	"github.com/jekabolt/grbpwr-manager/internal/openrouter"
	"github.com/jekabolt/grbpwr-manager/internal/opexmaterialize"
	"github.com/jekabolt/grbpwr-manager/internal/ordercleanup"
	"github.com/jekabolt/grbpwr-manager/internal/orimages"
	"github.com/jekabolt/grbpwr-manager/internal/payment/stripe"
	"github.com/jekabolt/grbpwr-manager/internal/recraft"
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

// JPKConfig holds the Polish taxpayer identity written into the JPK_V7M header (Naglowek + Podmiot1).
// These are legal-registry values with no source in the application, so they are operator-supplied via
// env (JPK_*) or a config file. An empty NIP disables the JPK_V7M export — it cannot emit a schema-valid
// file without a taxpayer. NazwaSystemu is stamped by the generator; nothing secret lives here.
type JPKConfig struct {
	NIP       string `mapstructure:"nip"`        // 10-digit taxpayer NIP (Podmiot1/OsobaNiefizyczna/NIP)
	FullName  string `mapstructure:"full_name"`  // full legal name (Podmiot1/OsobaNiefizyczna/PelnaNazwa)
	Email     string `mapstructure:"email"`      // contact email (Podmiot1/OsobaNiefizyczna/Email)
	Phone     string `mapstructure:"phone"`      // contact phone, optional (Podmiot1/OsobaNiefizyczna/Telefon)
	TaxOffice string `mapstructure:"tax_office"` // 4-digit destination tax-office code (Naglowek/KodUrzedu)
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
	DB                 store.Config              `mapstructure:"mysql"`
	Logger             log.Config                `mapstructure:"logger"`
	HTTP               httpapi.Config            `mapstructure:"http"`
	Auth               auth.Config               `mapstructure:"auth"`
	StorefrontAuth     storefront.Config         `mapstructure:"storefront_auth"`
	Bucket             bucket.Config             `mapstructure:"bucket"`
	Mailer             mail.Config               `mapstructure:"mailer"`
	CampaignDispatch   campaigndispatch.Config   `mapstructure:"campaign_dispatch"`
	OrderCleanup       ordercleanup.Config       `mapstructure:"order_cleanup"`
	DeliverySync       deliverysync.Config       `mapstructure:"delivery_sync"`
	AfterShip          aftership.Config          `mapstructure:"aftership"`
	ShippingLabel      shippinglabel.Config      `mapstructure:"shipping_label"`
	StorefrontCleanup  storefrontcleanup.Config  `mapstructure:"storefront_cleanup"`
	TierManagement     tiermanagement.Config     `mapstructure:"tier_management"`
	MarketingAggregate marketingaggregate.Config `mapstructure:"marketing_aggregate"`
	OpexMaterialize    opexmaterialize.Config    `mapstructure:"opex_materialize"`
	Accounting         acctposting.Config        `mapstructure:"accounting"`
	StripeReconcile    stripereconcile.Config    `mapstructure:"stripe_reconcile"`
	FxSync             fxsync.Config             `mapstructure:"fx_sync"`
	Rates              RatesConfig               `mapstructure:"rates"`
	Security           SecurityConfig            `mapstructure:"security"`
	JPK                JPKConfig                 `mapstructure:"jpk"`
	StripePayment      stripe.Config             `mapstructure:"stripe_payment"`
	StripePaymentTest  stripe.Config             `mapstructure:"stripe_payment_test"`
	Revalidation       revalidation.Config       `mapstructure:"revalidation"`
	GA4                ga4.Config                `mapstructure:"ga4"`
	GA4MP              ga4mp.Config              `mapstructure:"ga4mp"`
	GA4Sync            ga4sync.Config            `mapstructure:"ga4_sync"`
	BigQuery           bq.Config                 `mapstructure:"bigquery"`
	OpenRouter         openrouter.Config         `mapstructure:"openrouter"`
	// OpenRouterImages is the SECOND OpenRouter client: the image endpoint. It is a separate
	// section rather than more fields on OpenRouter because the two are different endpoints with
	// different catalogues, different timeouts and a response ceiling that differs by an order of
	// magnitude — see internal/orimages.
	OpenRouterImages orimages.Config `mapstructure:"openrouter_images"`
	// Meshy is the 3D provider, and the ONE place this feature departs from "everything through
	// OpenRouter" (P-5). Not by preference: OpenRouter has no 3D modality at all — "3d" is not a
	// value its catalogue accepts — so there is nothing there to route to. See internal/meshy.
	Meshy meshy.Config `mapstructure:"meshy"`
	// Recraft is the VECTOR provider (owner spec P-3: «ровный вектор, а не куча полигонов»). Its
	// primary route is the OpenRouterImages client above — the vector models are ordinary rows of
	// that same image catalogue — and this section only carries the tier→slug table plus the
	// FALLBACK direct-Recraft credentials. See internal/recraft.
	Recraft recraft.Config `mapstructure:"recraft"`
	// DesignGen is the generation WORKER — the thing that actually claims a paid run and calls a
	// provider. It is inert unless DESIGN_GENERATION_ENABLED is set (precedent: ACCOUNTING_ENABLED),
	// and that is deliberate: prod stands at migration 0339 and has no DESIGN band at all, so the
	// binary must be able to carry this code without running it. See internal/designgen.
	DesignGen    designgen.Config   `mapstructure:"design_generation"`
	PatternToken PatternTokenConfig `mapstructure:"pattern_token"`
}

// PatternTokenConfig configures the tokenized pattern read path (/api/p/{token}) that
// serves private выкройка objects. The pepper is the HMAC key behind every minted token;
// the public base url is the externally reachable origin of THIS backend, prefixed onto
// minted view/download urls and printed QR codes.
type PatternTokenConfig struct {
	Pepper        string `mapstructure:"pepper"`
	PublicBaseURL string `mapstructure:"public_base_url"`
}

// LoadConfig loads the configuration from a file and/or environment variables.
// Environment variables take precedence over config file values.
// Env var names are the explicit allowlist in bindEnvVars (e.g. MYSQL_DSN,
// AUTH_JWT_SECRET), matching the flat names used in .do/app.yaml.
//
// This is the loader for anything that SERVES. Read-only command-line reports
// should use LoadConfigForReadOnlyTooling.
func LoadConfig(cfgFile string) (*Config, error) {
	return loadConfig(cfgFile, (*Config).Validate)
}

// LoadConfigForReadOnlyTooling loads the same configuration for read-only
// command-line reports, and differs from LoadConfig only in what it insists on:
// the database DSN, and nothing else.
//
// The other settings LoadConfig requires are serving secrets. The JWT signing
// key and the pattern-token pepper are required there because auth.New and
// patternaccess fail closed on an empty key: a running API with either unset
// would accept forged admin tokens or mint forgeable capability urls. A report
// opens the database, reads and prints; it never issues or verifies a token, so
// demanding those values makes nothing safer. It only forces whoever runs the
// report to paste production serving secrets into a shell, which is strictly
// worse than not asking for them.
func LoadConfigForReadOnlyTooling(cfgFile string) (*Config, error) {
	return loadConfig(cfgFile, (*Config).ValidateForReadOnlyTooling)
}

func loadConfig(cfgFile string, validate func(*Config) error) (*Config, error) {
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
		if err := validate(&config); err != nil {
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

// validateDSN checks the database DSN, which every entry point needs. By this
// point the DSN has already been constructed from MYSQL_* / db.* parts when
// possible, so an empty value means neither MYSQL_DSN nor a complete set of
// parts was given. An empty DSN would otherwise let sqlx.Open succeed lazily
// and fail much later as a generic ping error.
func (c *Config) validateDSN() error {
	if c.DB.DSN == "" {
		return fmt.Errorf("mysql.dsn is required: set MYSQL_DSN, or provide the parts " +
			"(MYSQL_HOST, MYSQL_USER, MYSQL_PASSWORD, MYSQL_DATABASE, optional MYSQL_PORT; " +
			"or the DigitalOcean db.HOSTNAME/db.USERNAME/db.PASSWORD/db.DATABASE bindings)")
	}
	return nil
}

// ValidateForReadOnlyTooling checks the one setting a read-only report cannot
// do without. The serving secrets Validate insists on are deliberately absent
// here — see LoadConfigForReadOnlyTooling for why requiring them would move
// production keys into a shell without protecting anything.
//
// It reuses Validate's DSN wording rather than restating it: the two loaders
// must fail the same way on the same missing value, and a second copy of that
// message would be the thing that drifts.
func (c *Config) ValidateForReadOnlyTooling() error {
	return c.validateDSN()
}

// Validate checks that genuinely-required configuration is present. It only
// fails on settings the app needs in every environment; optional, env-gated
// features (analytics GA4/BigQuery behind enabled flags, mail, bucket, stripe,
// revalidation) are intentionally not enforced here and are validated by their
// own constructors when actually used.
func (c *Config) Validate() error {
	if err := c.validateDSN(); err != nil {
		return err
	}

	// Auth JWT secret is required: auth.New fails closed on an empty HS256 secret
	// because it would validate any token signed with an empty key (admin token
	// forgery). Validate it here too for a clearer, earlier message.
	if c.Auth.JWTSecret == "" {
		return fmt.Errorf("auth.jwt_secret is required: set AUTH_JWT_SECRET")
	}

	// Pattern token pepper is required: patternaccess fails closed on an empty HMAC key
	// (every capability url would be forgeable). Same early-message rationale as the JWT
	// secret. The public base url rides along because minted urls are useless without it.
	if strings.TrimSpace(c.PatternToken.Pepper) == "" {
		return fmt.Errorf("pattern_token.pepper is required: set PATTERN_TOKEN_PEPPER")
	}
	if strings.TrimSpace(c.PatternToken.PublicBaseURL) == "" {
		return fmt.Errorf("pattern_token.public_base_url is required: set PATTERN_TOKEN_PUBLIC_BASE_URL " +
			"to this backend's external origin, e.g. https://backend.grbpwr.com")
	}
	// Shape matters, not just presence: this value is CONCATENATED into every minted url,
	// including the ones printed as QR codes on paper tech packs. A scheme-less or
	// path-carrying value yields links that are broken permanently and in ink.
	if u, err := url.Parse(strings.TrimRight(strings.TrimSpace(c.PatternToken.PublicBaseURL), "/")); err != nil ||
		u.Scheme != "https" || u.Host == "" || u.Path != "" || u.RawQuery != "" || u.Fragment != "" || u.User != nil {
		return fmt.Errorf("pattern_token.public_base_url must be an absolute https origin with no path, "+
			"e.g. https://backend.grbpwr.com (got %q)", c.PatternToken.PublicBaseURL)
	}

	// Accounting posting worker: when enabled, its cutover date must parse and not be in the future
	// (a misconfigured start date would either post pre-cutover history or nothing at all).
	if err := c.Accounting.Validate(); err != nil {
		return fmt.Errorf("invalid accounting configuration: %w", err)
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
	viper.BindEnv("mailer.disabled", "MAILER_DISABLED")
	viper.BindEnv("mailer.from_email", "MAILER_FROM_EMAIL")
	viper.BindEnv("mailer.from_email_name", "MAILER_FROM_EMAIL_NAME")
	viper.BindEnv("mailer.reply_to", "MAILER_REPLY_TO")
	viper.BindEnv("mailer.test_recipients", "MAILER_TEST_RECIPIENTS")
	viper.BindEnv("mailer.worker_interval", "MAILER_WORKER_INTERVAL")
	viper.BindEnv("mailer.max_send_attempts", "MAILER_MAX_SEND_ATTEMPTS")
	viper.BindEnv("mailer.retry_base_interval", "MAILER_RETRY_BASE_INTERVAL")
	viper.BindEnv("mailer.retry_max_interval", "MAILER_RETRY_MAX_INTERVAL")
	viper.BindEnv("mailer.inline_send_lease", "MAILER_INLINE_SEND_LEASE")
	viper.BindEnv("mailer.webhook_secret", "MAILER_WEBHOOK_SECRET")
	viper.BindEnv("mailer.unsubscribe_base_url", "MAILER_UNSUBSCRIBE_BASE_URL")
	viper.BindEnv("mailer.unsubscribe_pepper", "MAILER_UNSUBSCRIBE_PEPPER")

	// Pattern token (private выкройки read path)
	viper.BindEnv("pattern_token.pepper", "PATTERN_TOKEN_PEPPER")
	viper.BindEnv("pattern_token.public_base_url", "PATTERN_TOKEN_PUBLIC_BASE_URL")
	viper.BindEnv("mailer.localization_enabled", "MAILER_LOCALIZATION_ENABLED")
	viper.BindEnv("mailer.resend_requests_per_second", "MAILER_RESEND_REQUESTS_PER_SECOND")
	viper.BindEnv("mailer.resend_burst", "MAILER_RESEND_BURST")
	viper.BindEnv("mailer.transactional_reserve_tokens", "MAILER_TRANSACTIONAL_RESERVE_TOKENS")

	// Email campaign dispatcher
	viper.BindEnv("campaign_dispatch.worker_interval", "CAMPAIGN_DISPATCH_WORKER_INTERVAL")
	viper.BindEnv("campaign_dispatch.fanout_page_size", "CAMPAIGN_DISPATCH_FANOUT_PAGE_SIZE")
	viper.BindEnv("campaign_dispatch.batch_size", "CAMPAIGN_DISPATCH_BATCH_SIZE")
	viper.BindEnv("campaign_dispatch.claim_lease", "CAMPAIGN_DISPATCH_CLAIM_LEASE")
	viper.BindEnv("campaign_dispatch.max_attempts", "CAMPAIGN_DISPATCH_MAX_ATTEMPTS")
	viper.BindEnv("campaign_dispatch.retry_base", "CAMPAIGN_DISPATCH_RETRY_BASE")
	viper.BindEnv("campaign_dispatch.retry_max", "CAMPAIGN_DISPATCH_RETRY_MAX")

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

	// Marketing account aggregate (email segmentation behavioral fields)
	viper.BindEnv("marketing_aggregate.worker_interval", "MARKETING_AGGREGATE_WORKER_INTERVAL")

	// OPEX materialize (book recurring fixed-cost templates into monthly lines)
	viper.BindEnv("opex_materialize.worker_interval", "OPEX_MATERIALIZE_WORKER_INTERVAL")

	// Accounting posting worker (drain the order outbox + pull sources into the double-entry ledger)
	viper.BindEnv("accounting.enabled", "ACCOUNTING_ENABLED")
	viper.BindEnv("accounting.worker_interval", "ACCOUNTING_WORKER_INTERVAL")
	viper.BindEnv("accounting.batch_size", "ACCOUNTING_BATCH_SIZE")
	viper.BindEnv("accounting.start_date", "ACCOUNTING_START_DATE")
	viper.BindEnv("accounting.delivered_recognition_from", "ACCOUNTING_DELIVERED_RECOGNITION_FROM")
	viper.BindEnv("jpk.nip", "JPK_NIP")
	viper.BindEnv("jpk.full_name", "JPK_FULL_NAME")
	viper.BindEnv("jpk.email", "JPK_EMAIL")
	viper.BindEnv("jpk.phone", "JPK_PHONE")
	viper.BindEnv("jpk.tax_office", "JPK_TAX_OFFICE")
	viper.BindEnv("accounting.settled_wait_max", "ACCOUNTING_SETTLED_WAIT_MAX")
	viper.BindEnv("accounting.defect_normal_loss_rate", "ACCOUNTING_DEFECT_NORMAL_LOSS_RATE")

	// Stripe reconcile (orphaned pre-order PaymentIntents)
	viper.BindEnv("stripe_reconcile.worker_interval", "STRIPE_RECONCILE_WORKER_INTERVAL")
	viper.BindEnv("stripe_reconcile.pre_order_threshold", "STRIPE_RECONCILE_PRE_ORDER_THRESHOLD")

	// FX sync (external ECB reference rates → costing_fx_rate)
	viper.BindEnv("fx_sync.enabled", "FX_SYNC_ENABLED")
	viper.BindEnv("fx_sync.source_url", "FX_SYNC_SOURCE_URL")
	viper.BindEnv("fx_sync.refresh_interval", "FX_SYNC_REFRESH_INTERVAL")
	viper.BindEnv("fx_sync.http_timeout", "FX_SYNC_HTTP_TIMEOUT")

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
	// OPENROUTER_MODEL_ANALYSIS is the optional per-feature slug for the tech-card analysis pass
	// (empty => the shared slug). It needs this line to exist at all: AutomaticEnv is off above on
	// purpose, so an unbound name reads as empty — which is also exactly what a correct unset
	// override looks like, making a missing binding invisible until somebody wonders why the
	// escalation did nothing.
	viper.BindEnv("openrouter.model_analysis", "OPENROUTER_MODEL_ANALYSIS")
	viper.BindEnv("openrouter.base_url", "OPENROUTER_BASE_URL")
	viper.BindEnv("openrouter.http_timeout", "OPENROUTER_HTTP_TIMEOUT")

	// OpenRouter IMAGES (design generation, P-2). A SECOND endpoint at the same provider:
	// POST /api/v1/images, whose catalogue (GET /api/v1/images/models) does not overlap the chat
	// one — no `openai/gpt-image-*` slug appears in /api/v1/models at all, so no value of
	// OPENROUTER_MODEL can reach one. See internal/orimages.
	//
	// KEY AND ROOT FALL BACK TO THE CHAT ONES, in that order: it is one OpenRouter account, so a
	// working deployment needs NO new secret to turn pictures on. The dedicated names exist only so
	// picture spend can later be moved to its own key, or the images route to its own proxy,
	// without touching code. viper takes the FIRST variable of the list that is set.
	viper.BindEnv("openrouter_images.api_key", "OPENROUTER_IMAGES_API_KEY", "OPENROUTER_API_KEY")
	viper.BindEnv("openrouter_images.base_url", "OPENROUTER_IMAGES_BASE_URL", "OPENROUTER_BASE_URL")
	// The image slug. Empty => orimages.DefaultModel (openai/gpt-image-2 — the raster half of
	// "good raster, then a good vector"; the older gpt-image-1 was kept only for its
	// `background: transparent`, which the band's prompts do not want and the vectoriser does not
	// carry). A slug set here MUST come from the IMAGE catalogue AND accept every parameter the
	// caller sends: the per-model enums differ inside the family, so this override can turn a
	// working deployment into a 400 on every press. See internal/orimages.DefaultModel.
	viper.BindEnv("openrouter_images.model", "OPENROUTER_MODEL_IMAGE")
	// One generation is tens of seconds of provider compute; a timeout shorter than the work bills
	// for a picture nobody receives.
	viper.BindEnv("openrouter_images.http_timeout", "OPENROUTER_IMAGES_TIMEOUT")
	// The read ceiling in BYTES, and on a 0.5 GiB instance it is the OOM guard — tunable so a box
	// that grows needs no deploy and a box that is dying can be rescued by lowering one number.
	viper.BindEnv("openrouter_images.max_response_bytes", "OPENROUTER_IMAGES_MAX_RESPONSE_BYTES")

	// Meshy (3D generation, P-4). A DIFFERENT PROVIDER with a key of its own — nothing here falls
	// back to an OpenRouter variable, because there is no 3D at OpenRouter to fall back to.
	//
	// EVERY LINE BELOW IS LOAD-BEARING IN THE SAME SILENT WAY. viper.AutomaticEnv is off in this
	// package on purpose, so a variable without its own BindEnv reads as EMPTY — and empty is also
	// what a correctly-unset optional override looks like. A forgotten line here does not fail, log
	// or differ visibly; it just means the number somebody set in the DO dashboard is never the
	// number the process uses. config/cfg_meshy_env_test.go sets each one and insists it arrives.
	//
	// ⚠️ These are set IN THE DIGITALOCEAN DASHBOARD, never in .do/app.yaml: pushing the spec
	// deploys prod and overwrites live SECRET values with the empty ones in the file.

	// MESHY_API_KEY is the whole switch. Unset => the client is disabled and StartRun must refuse a
	// 3D run outright rather than queue one nobody can execute.
	viper.BindEnv("meshy.api_key", "MESHY_API_KEY")
	// The API root. Exists for tests and a possible regional host, not as a knob to turn.
	viper.BindEnv("meshy.base_url", "MESHY_BASE_URL")
	// Bounds ONE control-plane request (submit or status lookup), not the generation.
	viper.BindEnv("meshy.http_timeout", "MESHY_HTTP_TIMEOUT")
	// The waiting shape: how often to ask, and how long to keep asking. A worker sizing its lease
	// or its next_attempt_at should read these off the client rather than guess them again.
	viper.BindEnv("meshy.poll_interval", "MESHY_POLL_INTERVAL")
	viper.BindEnv("meshy.poll_timeout", "MESHY_POLL_TIMEOUT")
	// Bounds fetching the finished model, SEPARATELY from the wait above — deliberately, because a
	// download cut by the waiting deadline loses an artifact that was already paid for and whose
	// link dies in three days.
	viper.BindEnv("meshy.download_timeout", "MESHY_DOWNLOAD_TIMEOUT")
	// Price of one Meshy credit in USD, the only bridge from consumed_credits to money. Unset falls
	// back to an estimate from the published plans; set it to the real rate of the active plan.
	viper.BindEnv("meshy.credit_usd", "MESHY_CREDIT_USD")

	// Recraft (VECTOR generation, P-3). The paid call normally goes through the OpenRouter image
	// client above; this section decides WHICH MODEL it names and, for the fallback route, how to
	// reach Recraft directly.
	//
	// EVERY LINE BELOW IS LOAD-BEARING IN THE SAME SILENT WAY as the Meshy block: AutomaticEnv is
	// off, so a name without its own BindEnv reads as empty, and empty is exactly what a correctly
	// unset override looks like. config/cfg_recraft_env_test.go sets each one and insists it lands.
	//
	// ⚠️ Set these IN THE DIGITALOCEAN DASHBOARD, never in .do/app.yaml.

	// RECRAFT_ROUTE picks the transport: unset/"openrouter" (owner rule P-5, the default) or
	// "direct" — Recraft's own API, which is the only way to reach the `strength` dial.
	viper.BindEnv("recraft.route", "RECRAFT_ROUTE")
	// The two model ids, for the ACTIVE ROUTE (the routes spell the same models differently:
	// recraft/recraft-v4-vector at OpenRouter, recraftv4_vector at Recraft). Unset => the verified
	// defaults in internal/recraft. They exist because a baked-in provider slug rots silently, and
	// this repo has already lost every AI feature to exactly that once.
	viper.BindEnv("recraft.model_vector", "RECRAFT_MODEL_VECTOR")
	viper.BindEnv("recraft.model_vector_pro", "RECRAFT_MODEL_VECTOR_PRO")
	// The fallback route's own credentials. RECRAFT_API_KEY is its whole switch: unset, the direct
	// route is disabled and the service refuses up front rather than queueing a run nobody can run.
	viper.BindEnv("recraft.direct.api_key", "RECRAFT_API_KEY")
	viper.BindEnv("recraft.direct.base_url", "RECRAFT_BASE_URL")
	viper.BindEnv("recraft.direct.http_timeout", "RECRAFT_HTTP_TIMEOUT")
	// Price of one Recraft API unit in USD (published: $1.00 = 1000 units, so 80 units = $0.08 for
	// V4 Vector and 300 = $0.30 for V4 Pro Vector). The only bridge from `credits` to money.
	viper.BindEnv("recraft.direct.credit_usd", "RECRAFT_CREDIT_USD")

	// Design generation worker. Six knobs, and only the first one decides anything on its own:
	// with DESIGN_GENERATION_ENABLED unset the worker is not constructed at all, and a run started
	// by hand would sit in `pending` with nobody to claim it — which is why the handler refuses to
	// start one while the flag is off.
	//
	// ⚠ DESIGN_IMAGE_QUALITY defaults to `medium` to AGREE WITH the handler's price reservation.
	// Raising it to `high` without raising the estimate under-reserves roughly fourfold, silently,
	// in the overspending direction. The two numbers are one decision and must move together.
	viper.BindEnv("design_generation.enabled", "DESIGN_GENERATION_ENABLED")
	viper.BindEnv("design_generation.worker_interval", "DESIGN_WORKER_INTERVAL")
	viper.BindEnv("design_generation.batch_size", "DESIGN_WORKER_BATCH_SIZE")
	viper.BindEnv("design_generation.claim_lease", "DESIGN_WORKER_CLAIM_LEASE")
	viper.BindEnv("design_generation.run_timeout", "DESIGN_WORKER_RUN_TIMEOUT")
	viper.BindEnv("design_generation.image_quality", "DESIGN_IMAGE_QUALITY")
}
