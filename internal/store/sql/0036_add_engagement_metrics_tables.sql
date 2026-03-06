-- +migrate Up

-- Remove old scroll depth table
drop table if exists bq_scroll_depth;

-- Time on Page
create table if not exists bq_time_on_page (
    id                      int auto_increment primary key,
    date                    date not null,
    page_path               varchar(512) not null,
    avg_visible_time_seconds decimal(10,2) default 0.0,
    avg_total_time_seconds  decimal(10,2) default 0.0,
    avg_engagement_score    decimal(5,3) default 0.0,
    page_views              bigint default 0,
    created_at              timestamp default current_timestamp,
    updated_at              timestamp default current_timestamp on update current_timestamp,
    unique key unique_date_path (date, page_path),
    index idx_date (date)
) engine=InnoDB default charset=utf8mb4 collate=utf8mb4_unicode_ci;

-- Product Zoom
create table if not exists bq_product_zoom (
    id          int auto_increment primary key,
    date        date not null,
    product_id  varchar(50) not null,
    product_name varchar(255),
    zoom_method varchar(20) not null,
    zoom_count  bigint default 0,
    created_at  timestamp default current_timestamp,
    updated_at  timestamp default current_timestamp on update current_timestamp,
    unique key unique_date_product_method (date, product_id, zoom_method),
    index idx_date (date),
    index idx_product_id (product_id)
) engine=InnoDB default charset=utf8mb4 collate=utf8mb4_unicode_ci;

-- Image Swipes
create table if not exists bq_image_swipes (
    id             int auto_increment primary key,
    date           date not null,
    product_id     varchar(50) not null,
    product_name   varchar(255),
    swipe_direction varchar(20) not null,
    swipe_count    bigint default 0,
    created_at     timestamp default current_timestamp,
    updated_at     timestamp default current_timestamp on update current_timestamp,
    unique key unique_date_product_direction (date, product_id, swipe_direction),
    index idx_date (date),
    index idx_product_id (product_id)
) engine=InnoDB default charset=utf8mb4 collate=utf8mb4_unicode_ci;

-- Size Guide Clicks
create table if not exists bq_size_guide_clicks (
    id            int auto_increment primary key,
    date          date not null,
    product_id    varchar(50) not null,
    product_name  varchar(255),
    page_location varchar(20) not null,
    click_count   bigint default 0,
    created_at    timestamp default current_timestamp,
    updated_at    timestamp default current_timestamp on update current_timestamp,
    unique key unique_date_product_location (date, product_id, page_location),
    index idx_date (date),
    index idx_product_id (product_id)
) engine=InnoDB default charset=utf8mb4 collate=utf8mb4_unicode_ci;

-- Details Expansion
create table if not exists bq_details_expansion (
    id           int auto_increment primary key,
    date         date not null,
    product_id   varchar(50) not null,
    product_name varchar(255),
    section_name varchar(50) not null,
    expand_count bigint default 0,
    created_at   timestamp default current_timestamp,
    updated_at   timestamp default current_timestamp on update current_timestamp,
    unique key unique_date_product_section (date, product_id, section_name),
    index idx_date (date),
    index idx_product_id (product_id)
) engine=InnoDB default charset=utf8mb4 collate=utf8mb4_unicode_ci;

-- Notify Me Intent
create table if not exists bq_notify_me_intent (
    id             int auto_increment primary key,
    date           date not null,
    product_id     varchar(50) not null,
    product_name   varchar(255),
    action         varchar(30) not null,
    count          bigint default 0,
    conversion_rate decimal(5,2) default 0.0,
    created_at     timestamp default current_timestamp,
    updated_at     timestamp default current_timestamp on update current_timestamp,
    unique key unique_date_product_action (date, product_id, action),
    index idx_date (date),
    index idx_product_id (product_id)
) engine=InnoDB default charset=utf8mb4 collate=utf8mb4_unicode_ci;

-- +migrate Down

drop table if exists bq_notify_me_intent;
drop table if exists bq_details_expansion;
drop table if exists bq_size_guide_clicks;
drop table if exists bq_image_swipes;
drop table if exists bq_product_zoom;
drop table if exists bq_time_on_page;

-- Recreate old scroll depth table
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
