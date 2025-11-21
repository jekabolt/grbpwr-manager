package config

import (
	"fmt"
	"os"
	"strings"

	httpapi "github.com/jekabolt/grbpwr-manager/internal/api/http"
	"github.com/jekabolt/grbpwr-manager/internal/apisrv/auth"
	"github.com/jekabolt/grbpwr-manager/internal/bucket"
	"github.com/jekabolt/grbpwr-manager/internal/mail"
	"github.com/jekabolt/grbpwr-manager/internal/payment/stripe"
	"github.com/jekabolt/grbpwr-manager/internal/payment/tron"
	"github.com/jekabolt/grbpwr-manager/internal/payment/trongrid"
	"github.com/jekabolt/grbpwr-manager/internal/rates"
	"github.com/jekabolt/grbpwr-manager/internal/revalidation"
	"github.com/jekabolt/grbpwr-manager/internal/store"
	"github.com/jekabolt/grbpwr-manager/log"
	"github.com/spf13/viper"
)

// Config represents the global configuration for the service.
type Config struct {
	DB                           store.Config        `mapstructure:"mysql"`
	Logger                       log.Config          `mapstructure:"logger"`
	HTTP                         httpapi.Config      `mapstructure:"http"`
	Auth                         auth.Config         `mapstructure:"auth"`
	Bucket                       bucket.Config       `mapstructure:"bucket"`
	Mailer                       mail.Config         `mapstructure:"mailer"`
	Rates                        rates.Config        `mapstructure:"rates"`
	Trongrid                     trongrid.Config     `mapstructure:"trongrid"`
	TrongridShasta               trongrid.Config     `mapstructure:"trongrid_shasta_testnet"`
	USDTTronPayment              tron.Config         `mapstructure:"usdt_tron_payment"`
	USDTTronShastaTestnetPayment tron.Config         `mapstructure:"usdt_tron_shasta_testnet_payment"`
	StripePayment                stripe.Config       `mapstructure:"stripe_payment"`
	StripePaymentTest            stripe.Config       `mapstructure:"stripe_payment_test"`
	Revalidation                 revalidation.Config `mapstructure:"revalidation"`
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
				config.DB.DSN = fmt.Sprintf("%s:%s@tcp(%s:%s)/%s?charset=utf8&parseTime=true&tls=custom",
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
	viper.BindEnv("logger.level", "LOG_LEVEL")
	viper.BindEnv("logger.add_source", "LOG_ADD_SOURCE")

	// HTTP
	viper.BindEnv("http.port", "HTTP_PORT")
	viper.BindEnv("http.address", "HTTP_ADDRESS")
	viper.BindEnv("http.allowed_origins", "HTTP_ALLOWED_ORIGINS")

	// Auth
	viper.BindEnv("auth.jwt_secret", "AUTH_JWT_SECRET")
	viper.BindEnv("auth.master_password", "AUTH_MASTER_PASSWORD")
	viper.BindEnv("auth.password_hasher_salt_size", "AUTH_PASSWORD_HASHER_SALT_SIZE")
	viper.BindEnv("auth.password_hasher_iterations", "AUTH_PASSWORD_HASHER_ITERATIONS")
	viper.BindEnv("auth.jwt_ttl", "AUTH_JWT_TTL")

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

	// Rates
	viper.BindEnv("rates.api_key", "RATES_API_KEY")
	viper.BindEnv("rates.rates_update_period", "RATES_UPDATE_PERIOD")
	viper.BindEnv("rates.base_currency", "RATES_BASE_CURRENCY")

	// USDT Tron Payment
	viper.BindEnv("usdt_tron_payment.addresses", "USDT_TRON_PAYMENT_ADDRESSES")
	viper.BindEnv("usdt_tron_payment.node", "USDT_TRON_PAYMENT_NODE")
	viper.BindEnv("usdt_tron_payment.invoice_expiration", "USDT_TRON_PAYMENT_INVOICE_EXPIRATION")
	viper.BindEnv("usdt_tron_payment.check_incoming_tx_interval", "USDT_TRON_PAYMENT_CHECK_INTERVAL")
	viper.BindEnv("usdt_tron_payment.contract_address", "USDT_TRON_PAYMENT_CONTRACT_ADDRESS")

	// USDT Tron Shasta Testnet Payment
	viper.BindEnv("usdt_tron_shasta_testnet_payment.addresses", "USDT_TRON_SHASTA_TESTNET_ADDRESSES")
	viper.BindEnv("usdt_tron_shasta_testnet_payment.node", "USDT_TRON_SHASTA_TESTNET_NODE")
	viper.BindEnv("usdt_tron_shasta_testnet_payment.invoice_expiration", "USDT_TRON_SHASTA_TESTNET_INVOICE_EXPIRATION")
	viper.BindEnv("usdt_tron_shasta_testnet_payment.check_incoming_tx_interval", "USDT_TRON_SHASTA_TESTNET_CHECK_INTERVAL")
	viper.BindEnv("usdt_tron_shasta_testnet_payment.contract_address", "USDT_TRON_SHASTA_TESTNET_CONTRACT_ADDRESS")

	// TronGrid
	viper.BindEnv("trongrid.api_key", "TRONGRID_API_KEY")
	viper.BindEnv("trongrid.base_url", "TRONGRID_BASE_URL")
	viper.BindEnv("trongrid.timeout", "TRONGRID_TIMEOUT")

	// TronGrid Shasta Testnet
	viper.BindEnv("trongrid_shasta_testnet.api_key", "TRONGRID_SHASTA_TESTNET_API_KEY")
	viper.BindEnv("trongrid_shasta_testnet.base_url", "TRONGRID_SHASTA_TESTNET_BASE_URL")
	viper.BindEnv("trongrid_shasta_testnet.timeout", "TRONGRID_SHASTA_TESTNET_TIMEOUT")

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
}
