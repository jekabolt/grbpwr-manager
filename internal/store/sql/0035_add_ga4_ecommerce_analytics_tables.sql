-- +migrate Up

-- GA4 ecommerce daily metrics
create table if not exists ga4_ecommerce_metrics (
    id           int auto_increment primary key,
    date         date not null,
    purchases    int not null default 0,
    revenue      decimal(14,2) not null default 0,
    add_to_carts int not null default 0,
    checkouts    int not null default 0,
    items_viewed int not null default 0,
    created_at   timestamp default current_timestamp,
    updated_at   timestamp default current_timestamp
                 on update current_timestamp,
    unique key idx_date (date)
) engine=InnoDB default charset=utf8mb4 collate=utf8mb4_unicode_ci;

-- GA4 revenue by source
create table if not exists ga4_revenue_by_source (
    id         int auto_increment primary key,
    date       date not null,
    source     varchar(255) not null,
    medium     varchar(255) not null,
    campaign   varchar(255) not null default '',
    sessions   int not null default 0,
    revenue    decimal(14,2) not null default 0,
    purchases  int not null default 0,
    created_at timestamp default current_timestamp,
    updated_at timestamp default current_timestamp
               on update current_timestamp,
    unique key idx_date_source_medium_campaign (date, source, medium, campaign)
) engine=InnoDB default charset=utf8mb4 collate=utf8mb4_unicode_ci;

-- GA4 product conversion funnel
create table if not exists ga4_product_conversion (
    id           int auto_increment primary key,
    date         date not null,
    product_id   varchar(50) not null,
    product_name varchar(255) not null,
    items_viewed int not null default 0,
    add_to_carts int not null default 0,
    purchases    int not null default 0,
    revenue      decimal(14,2) not null default 0,
    created_at   timestamp default current_timestamp,
    updated_at   timestamp default current_timestamp
                 on update current_timestamp,
    unique key idx_date_product (date, product_id)
) engine=InnoDB default charset=utf8mb4 collate=utf8mb4_unicode_ci;

-- BigQuery Cache: DAILY Funnel (10-step expanded)
create table if not exists bq_funnel_analysis (
    id                     int auto_increment primary key,
    date                   date not null,
    session_start_users    int not null default 0,
    view_item_list_users   int not null default 0,
    select_item_users      int not null default 0,
    view_item_users        int not null default 0,
    size_selected_users    int not null default 0,
    add_to_cart_users      int not null default 0,
    begin_checkout_users   int not null default 0,
    add_shipping_info_users int not null default 0,
    add_payment_info_users int not null default 0,
    purchase_users         int not null default 0,
    created_at             timestamp default current_timestamp,
    updated_at             timestamp default current_timestamp
                          on update current_timestamp,
    unique key idx_date (date)
) engine=InnoDB default charset=utf8mb4 collate=utf8mb4_unicode_ci;

-- BigQuery Cache: DAILY OOS Impact
create table if not exists bq_oos_impact (
    id                     int auto_increment primary key,
    date                   date not null,
    product_id             varchar(50) not null,
    product_name           varchar(255) not null,
    size_id                int not null default 0,
    size_name              varchar(100) not null default '',
    product_price          decimal(14,2) not null default 0,
    currency               varchar(10) not null default 'USD',
    click_count            int not null default 0,
    estimated_lost_sales   decimal(10,4) not null default 0,
    estimated_lost_revenue decimal(14,2) not null default 0,
    created_at             timestamp default current_timestamp,
    updated_at             timestamp default current_timestamp
                           on update current_timestamp,
    unique key idx_date_product_size (date, product_id, size_id)
) engine=InnoDB default charset=utf8mb4 collate=utf8mb4_unicode_ci;

-- BigQuery Cache: DAILY Payment Failures
create table if not exists bq_payment_failures (
    id                     int auto_increment primary key,
    date                   date not null,
    error_code             varchar(255) not null default '',
    payment_type           varchar(100) not null default '',
    failure_count          int not null default 0,
    total_failed_value     decimal(14,2) not null default 0,
    avg_failed_order_value decimal(14,2) not null default 0,
    created_at             timestamp default current_timestamp,
    updated_at             timestamp default current_timestamp
                           on update current_timestamp,
    unique key idx_date_error_payment (date, error_code, payment_type)
) engine=InnoDB default charset=utf8mb4 collate=utf8mb4_unicode_ci;

-- BigQuery Cache: DAILY Web Vitals
create table if not exists bq_web_vitals (
    id               int auto_increment primary key,
    date             date not null,
    metric_name      varchar(20) not null,
    metric_rating    varchar(30) not null,
    session_count    int not null default 0,
    conversions      int not null default 0,
    avg_metric_value decimal(10,4) not null default 0,
    created_at       timestamp default current_timestamp,
    updated_at       timestamp default current_timestamp
                     on update current_timestamp,
    unique key idx_date_metric_rating (date, metric_name, metric_rating)
) engine=InnoDB default charset=utf8mb4 collate=utf8mb4_unicode_ci;

-- BigQuery Cache: DAILY User Journeys
-- path_hash uses MD5 (32 hex chars). Collisions are astronomically rare (~1 in 2^64).
create table if not exists bq_user_journeys (
    id            int auto_increment primary key,
    date          date not null,
    journey_path  text not null,
    session_count int not null default 0,
    conversions   int not null default 0,
    path_hash     varchar(32) not null,
    created_at    timestamp default current_timestamp,
    updated_at    timestamp default current_timestamp
                  on update current_timestamp,
    unique key idx_date_hash (date, path_hash)
) engine=InnoDB default charset=utf8mb4 collate=utf8mb4_unicode_ci;

-- BigQuery Cache: DAILY Session Duration
create table if not exists bq_session_duration (
    id                               int auto_increment primary key,
    date                             date not null,
    avg_time_between_events_seconds   decimal(10,4) not null default 0,
    median_time_between_events       decimal(10,4) not null default 0,
    created_at                       timestamp default current_timestamp,
    updated_at                       timestamp default current_timestamp
                                     on update current_timestamp,
    unique key idx_date (date)
) engine=InnoDB default charset=utf8mb4 collate=utf8mb4_unicode_ci;

-- BigQuery Cache: Size Intent (clicks per size)
create table if not exists bq_size_intent (
    id          int auto_increment primary key,
    date        date not null,
    product_id  varchar(50) not null,
    size_id     int not null default 0,
    size_name   varchar(100) not null default '',
    size_clicks int not null default 0,
    created_at  timestamp default current_timestamp,
    updated_at  timestamp default current_timestamp
                on update current_timestamp,
    unique key idx_date_product_size (date, product_id, size_id)
) engine=InnoDB default charset=utf8mb4 collate=utf8mb4_unicode_ci;

-- BigQuery Cache: Device funnel
create table if not exists bq_device_funnel (
    id               int auto_increment primary key,
    date             date not null,
    device_category  varchar(50) not null default 'unknown',
    sessions         int not null default 0,
    add_to_cart_users int not null default 0,
    checkout_users   int not null default 0,
    purchase_users   int not null default 0,
    created_at       timestamp default current_timestamp,
    updated_at       timestamp default current_timestamp
                     on update current_timestamp,
    unique key idx_date_device (date, device_category)
) engine=InnoDB default charset=utf8mb4 collate=utf8mb4_unicode_ci;

-- BigQuery Cache: Product engagement
create table if not exists bq_product_engagement (
    id           int auto_increment primary key,
    date         date not null,
    product_id   varchar(50) not null,
    product_name varchar(255) not null default '',
    image_views  int not null default 0,
    zoom_events  int not null default 0,
    scroll_75    int not null default 0,
    scroll_100   int not null default 0,
    created_at   timestamp default current_timestamp,
    updated_at   timestamp default current_timestamp
                 on update current_timestamp,
    unique key idx_date_product (date, product_id)
) engine=InnoDB default charset=utf8mb4 collate=utf8mb4_unicode_ci;

-- BigQuery Cache: Form errors
create table if not exists bq_form_errors (
    id          int auto_increment primary key,
    date        date not null,
    field_name  varchar(100) not null default 'unknown',
    error_count int not null default 0,
    created_at  timestamp default current_timestamp,
    updated_at  timestamp default current_timestamp
                on update current_timestamp,
    unique key idx_date_field (date, field_name)
) engine=InnoDB default charset=utf8mb4 collate=utf8mb4_unicode_ci;

-- BigQuery Cache: JS exceptions
-- path_desc_hash uses MD5(page_path || '\0' || description) to avoid prefix truncation collisions.
-- Different long URLs/descriptions that share the first 191 chars would otherwise collide on UPSERT.
create table if not exists bq_exceptions (
    id              int auto_increment primary key,
    date            date not null,
    page_path       varchar(512) not null default '/',
    exception_count int not null default 0,
    description     varchar(1024) not null default '',
    path_desc_hash  varchar(32) not null,
    created_at      timestamp default current_timestamp,
    updated_at      timestamp default current_timestamp
                    on update current_timestamp,
    unique key idx_date_hash (date, path_desc_hash)
) engine=InnoDB default charset=utf8mb4 collate=utf8mb4_unicode_ci;

-- BigQuery Cache: 404 page hits
-- path_hash uses MD5(page_path) to avoid prefix truncation collisions.
-- Different long URLs that share the first 191 chars would otherwise collide on UPSERT.
create table if not exists bq_not_found_pages (
    id         int auto_increment primary key,
    date       date not null,
    page_path  varchar(512) not null default '/',
    path_hash  varchar(32) not null,
    hit_count  int not null default 0,
    created_at timestamp default current_timestamp,
    updated_at timestamp default current_timestamp
               on update current_timestamp,
    unique key idx_date_hash (date, path_hash)
) engine=InnoDB default charset=utf8mb4 collate=utf8mb4_unicode_ci;

-- BigQuery Cache: Hero banner mini-funnel
create table if not exists bq_hero_funnel (
    id               int auto_increment primary key,
    date             date not null,
    hero_click_users int not null default 0,
    view_item_users  int not null default 0,
    purchase_users   int not null default 0,
    created_at       timestamp default current_timestamp,
    updated_at       timestamp default current_timestamp
                    on update current_timestamp,
    unique key idx_date (date)
) engine=InnoDB default charset=utf8mb4 collate=utf8mb4_unicode_ci;

-- BigQuery Cache: Size confidence
create table if not exists bq_size_confidence (
    id               int auto_increment primary key,
    date             date not null,
    product_id       varchar(50) not null,
    size_guide_views int not null default 0,
    size_selections  int not null default 0,
    created_at       timestamp default current_timestamp,
    updated_at       timestamp default current_timestamp
                    on update current_timestamp,
    unique key idx_date_product (date, product_id)
) engine=InnoDB default charset=utf8mb4 collate=utf8mb4_unicode_ci;

-- BigQuery Cache: Payment recovery
create table if not exists bq_payment_recovery (
    id              int auto_increment primary key,
    date            date not null,
    failed_users    int not null default 0,
    recovered_users int not null default 0,
    created_at      timestamp default current_timestamp,
    updated_at      timestamp default current_timestamp
                    on update current_timestamp,
    unique key idx_date (date)
) engine=InnoDB default charset=utf8mb4 collate=utf8mb4_unicode_ci;

-- BigQuery Cache: Checkout timings
create table if not exists bq_checkout_timings (
    id                      int auto_increment primary key,
    date                    date not null,
    avg_checkout_seconds    decimal(10,2) not null default 0,
    median_checkout_seconds decimal(10,2) not null default 0,
    session_count           int not null default 0,
    created_at              timestamp default current_timestamp,
    updated_at              timestamp default current_timestamp
                            on update current_timestamp,
    unique key idx_date (date)
) engine=InnoDB default charset=utf8mb4 collate=utf8mb4_unicode_ci;

-- BigQuery Cache: Scroll depth
create table if not exists bq_scroll_depth (
    id          int auto_increment primary key,
    date        date not null,
    page_type   varchar(50) not null,
    scroll_25   int not null default 0,
    scroll_50   int not null default 0,
    scroll_75   int not null default 0,
    scroll_100  int not null default 0,
    total_users int not null default 0,
    created_at  timestamp default current_timestamp,
    updated_at  timestamp default current_timestamp on update current_timestamp,
    unique key uq_bq_scroll_depth (date, page_type)
) engine=InnoDB default charset=utf8mb4 collate=utf8mb4_unicode_ci comment 'BQ cache: scroll depth engagement by page type per day';

-- BigQuery Cache: Add-to-cart rate
create table if not exists bq_add_to_cart_rate (
    id                int auto_increment primary key,
    date              date not null,
    product_id        varchar(50) not null,
    product_name      varchar(255) not null default '',
    view_count        int not null default 0,
    add_to_cart_count int not null default 0,
    cart_rate         double not null default 0,
    created_at        timestamp default current_timestamp,
    updated_at        timestamp default current_timestamp on update current_timestamp,
    unique key uq_bq_atc_rate (date, product_id)
) engine=InnoDB default charset=utf8mb4 collate=utf8mb4_unicode_ci comment 'BQ cache: per-product view-to-cart conversion per day';

-- BigQuery Cache: Browser breakdown
create table if not exists bq_browser_breakdown (
    id              int auto_increment primary key,
    date            date not null,
    browser         varchar(50) not null,
    sessions        int not null default 0,
    users           int not null default 0,
    conversions     int not null default 0,
    conversion_rate double not null default 0,
    created_at      timestamp default current_timestamp,
    updated_at      timestamp default current_timestamp on update current_timestamp,
    unique key uq_bq_browser (date, browser)
) engine=InnoDB default charset=utf8mb4 collate=utf8mb4_unicode_ci comment 'BQ cache: sessions and conversions by browser per day';

-- BigQuery Cache: Newsletter
create table if not exists bq_newsletter (
    id           int auto_increment primary key,
    date         date not null,
    signup_count int not null default 0,
    unique_users int not null default 0,
    created_at   timestamp default current_timestamp,
    updated_at   timestamp default current_timestamp on update current_timestamp,
    unique key uq_bq_newsletter (date)
) engine=InnoDB default charset=utf8mb4 collate=utf8mb4_unicode_ci comment 'BQ cache: newsletter signup events per day';

-- BigQuery Cache: Abandoned cart
create table if not exists bq_abandoned_cart (
    id                       int auto_increment primary key,
    date                     date not null,
    carts_started            int not null default 0,
    checkouts_started        int not null default 0,
    abandonment_rate         double not null default 0,
    avg_minutes_to_checkout  double not null default 0,
    avg_minutes_to_abandon   double not null default 0,
    created_at               timestamp default current_timestamp,
    updated_at               timestamp default current_timestamp on update current_timestamp,
    unique key uq_bq_abandoned_cart (date)
) engine=InnoDB default charset=utf8mb4 collate=utf8mb4_unicode_ci comment 'BQ cache: cart abandonment metrics per day';

-- BigQuery Cache: Campaign attribution
create table if not exists bq_campaign_attribution (
    id              int auto_increment primary key,
    date            date not null,
    utm_source      varchar(255) not null default '(direct)',
    utm_medium      varchar(255) not null default '(none)',
    utm_campaign    varchar(255) not null default '(not set)',
    sessions        int not null default 0,
    users           int not null default 0,
    conversions     int not null default 0,
    revenue         decimal(14,2) not null default 0.00,
    conversion_rate double not null default 0,
    created_at      timestamp default current_timestamp,
    updated_at      timestamp default current_timestamp on update current_timestamp,
    unique key uq_bq_campaign (date, utm_source, utm_medium, utm_campaign)
) engine=InnoDB default charset=utf8mb4 collate=utf8mb4_unicode_ci comment 'BQ cache: per-campaign sessions/conversions/revenue per day';

-- Standardize ga4_product_page_metrics.product_id to varchar(50) to match GA4 items[].item_id and BQ tables
alter table ga4_product_page_metrics
    modify column product_id varchar(50) not null;

-- Traffic source and device metrics are now served exclusively from BigQuery
-- (bq_campaign_attribution and bq_device_funnel). These GA4 Data API tables
-- are redundant and can be safely dropped.
drop table if exists ga4_traffic_source_metrics;
drop table if exists ga4_device_metrics;

-- Clean up stale sync_status rows so the worker doesn't try to resume them.
delete from ga4_sync_status where sync_type in ('traffic_source_metrics', 'device_metrics');

-- Change ga4_sync_status from status VARCHAR to success BOOLEAN (only success/error values)
alter table ga4_sync_status add column success boolean not null default false;
update ga4_sync_status set success = (status = 'success');
alter table ga4_sync_status drop column status;

-- +migrate Down

create table if not exists ga4_traffic_source_metrics (
    id         int auto_increment primary key,
    date       date not null,
    source     varchar(255) not null,
    medium     varchar(255) not null,
    sessions   int not null default 0,
    users      int not null default 0,
    created_at timestamp default current_timestamp,
    updated_at timestamp default current_timestamp
               on update current_timestamp,
    unique key idx_date_source_medium (date, source, medium)
) engine=InnoDB default charset=utf8mb4 collate=utf8mb4_unicode_ci;

create table if not exists ga4_device_metrics (
    id              int auto_increment primary key,
    date            date not null,
    device_category varchar(50) not null,
    sessions        int not null default 0,
    users           int not null default 0,
    created_at      timestamp default current_timestamp,
    updated_at      timestamp default current_timestamp
                    on update current_timestamp,
    unique key idx_date_device (date, device_category)
) engine=InnoDB default charset=utf8mb4 collate=utf8mb4_unicode_ci;

drop table if exists bq_campaign_attribution;
drop table if exists bq_abandoned_cart;
drop table if exists bq_newsletter;
drop table if exists bq_browser_breakdown;
drop table if exists bq_add_to_cart_rate;
drop table if exists bq_scroll_depth;
drop table if exists bq_checkout_timings;
drop table if exists bq_payment_recovery;
drop table if exists bq_size_confidence;
drop table if exists bq_hero_funnel;
drop table if exists bq_not_found_pages;
drop table if exists bq_exceptions;
drop table if exists bq_form_errors;
drop table if exists bq_product_engagement;
drop table if exists bq_device_funnel;
drop table if exists bq_size_intent;
drop table if exists bq_session_duration;
drop table if exists bq_user_journeys;
drop table if exists bq_web_vitals;
drop table if exists bq_payment_failures;
drop table if exists bq_oos_impact;
drop table if exists bq_funnel_analysis;
drop table if exists ga4_product_conversion;
drop table if exists ga4_revenue_by_source;
drop table if exists ga4_ecommerce_metrics;

alter table ga4_product_page_metrics
    modify column product_id int not null;

-- Revert ga4_sync_status: success BOOLEAN back to status VARCHAR
alter table ga4_sync_status add column status varchar(50) not null default 'error';
update ga4_sync_status set status = if(success, 'success', 'error');
alter table ga4_sync_status drop column success;
