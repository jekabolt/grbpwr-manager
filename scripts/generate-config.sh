#!/bin/sh
set -e

CONFIG_FILE="/etc/grbpwr-products-manager/config.toml"
CONFIG_DIR=$(dirname "$CONFIG_FILE")
mkdir -p "$CONFIG_DIR"

# Generate config.toml from environment variables
cat > "$CONFIG_FILE" <<EOF
[mysql]
    dsn="${MYSQL_USER}:${MYSQL_PASSWORD}@tcp(${MYSQL_HOST}:${MYSQL_PORT})/${MYSQL_DATABASE}?charset=utf8&parseTime=true&tls=custom"
    automigrate=${MYSQL_AUTOMIGRATE:-true}
    max_open_connections="${MYSQL_MAX_OPEN_CONNECTIONS:-5}"
    max_idle_connections="${MYSQL_MAX_IDLE_CONNECTIONS:-2}"
    tls_ca_path="@certs/ca-certificate.crt"

[logger]
    level=${LOG_LEVEL:--4}
    add_source=${LOG_ADD_SOURCE:-true}

[http]
    port=${HTTP_PORT:-8081}
    address="${HTTP_ADDRESS:-0.0.0.0}"
    allowed_origins=${HTTP_ALLOWED_ORIGINS:-["https://grbpwr.com","https://backend.grbpwr.com"]}

[auth]
    jwt_secret="${AUTH_JWT_SECRET}"
    master_password="${AUTH_MASTER_PASSWORD}"
    password_hasher_salt_size=${AUTH_PASSWORD_HASHER_SALT_SIZE:-16}
    password_hasher_iterations=${AUTH_PASSWORD_HASHER_ITERATIONS:-100000}
    jwt_ttl="${AUTH_JWT_TTL:-6000m}"

[bucket]
    s3_access_key="${BUCKET_S3_ACCESS_KEY}"
    s3_secret_access_key="${BUCKET_S3_SECRET_ACCESS_KEY}"
    s3_endpoint="${BUCKET_S3_ENDPOINT}"
    s3_bucket_name="${BUCKET_S3_BUCKET_NAME}"
    s3_bucket_location="${BUCKET_S3_BUCKET_LOCATION}"
    base_folder="${BUCKET_BASE_FOLDER}"
    image_store_prefix="${BUCKET_IMAGE_STORE_PREFIX}"
    subdomain_endpoint="${BUCKET_SUBDOMAIN_ENDPOINT}"

[mailer]
    sendgrid_api_key="${MAILER_SENDGRID_API_KEY}"
    from_email="${MAILER_FROM_EMAIL}"
    from_email_name="${MAILER_FROM_EMAIL_NAME}"
    reply_to="${MAILER_REPLY_TO}"
    worker_interval="${MAILER_WORKER_INTERVAL:-1m}"

[rates]
    api_key="${RATES_API_KEY}"
    rates_update_period="${RATES_UPDATE_PERIOD:-24h}"
    base_currency="${RATES_BASE_CURRENCY:-EUR}"

[usdt_tron_payment]
    addresses = ${USDT_TRON_PAYMENT_ADDRESSES:-["TRDTdbt4vsxQnXtSBX1Da9npWdwsfgYzy4"]}
    node = "${USDT_TRON_PAYMENT_NODE:-https://api.trongrid.io}"
    invoice_expiration = "${USDT_TRON_PAYMENT_INVOICE_EXPIRATION:-30m}"
    check_incoming_tx_interval = "${USDT_TRON_PAYMENT_CHECK_INTERVAL:-30s}"
    contract_address = "${USDT_TRON_PAYMENT_CONTRACT_ADDRESS}"

[usdt_tron_shasta_testnet_payment]
    addresses = ${USDT_TRON_SHASTA_TESTNET_ADDRESSES:-["TULivK5zBnouAaKyt9hxTHJQspPHNpVoKV"]}
    node = "${USDT_TRON_SHASTA_TESTNET_NODE:-https://api.shasta.trongrid.io}"
    invoice_expiration = "${USDT_TRON_SHASTA_TESTNET_INVOICE_EXPIRATION:-1m}"
    check_incoming_tx_interval = "${USDT_TRON_SHASTA_TESTNET_CHECK_INTERVAL:-10s}"
    contract_address = "${USDT_TRON_SHASTA_TESTNET_CONTRACT_ADDRESS}"

[trongrid]
    api_key = "${TRONGRID_API_KEY}"
    base_url = "${TRONGRID_BASE_URL:-https://api.trongrid.io}"
    timeout = "${TRONGRID_TIMEOUT:-30s}"

[trongrid_shasta_testnet]
    api_key = "${TRONGRID_SHASTA_TESTNET_API_KEY}"
    base_url = "${TRONGRID_SHASTA_TESTNET_BASE_URL:-https://api.shasta.trongrid.io}"
    timeout = "${TRONGRID_SHASTA_TESTNET_TIMEOUT:-30s}"

[stripe_payment]
    secret_key = "${STRIPE_PAYMENT_SECRET_KEY}"
    pub_key = "${STRIPE_PAYMENT_PUB_KEY}"
    invoice_expiration = "${STRIPE_PAYMENT_INVOICE_EXPIRATION:-24h}"

[stripe_payment_test]
    secret_key = "${STRIPE_PAYMENT_TEST_SECRET_KEY}"
    pub_key = "${STRIPE_PAYMENT_TEST_PUB_KEY}"
    invoice_expiration = "${STRIPE_PAYMENT_TEST_INVOICE_EXPIRATION:-60m}"

[revalidation]
    project_id = "${REVALIDATION_PROJECT_ID}"
    vercel_api_token = "${REVALIDATION_VERCEL_API_TOKEN}"
    revalidate_secret = "${REVALIDATION_REVALIDATE_SECRET}"
    http_timeout = "${REVALIDATION_HTTP_TIMEOUT:-10s}"
EOF

echo "Config file generated at $CONFIG_FILE"

