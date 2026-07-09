create table if not exists bq_refunds (
    id                 int auto_increment primary key,
    date               date not null,
    currency           varchar(10) not null default '',
    return_reason      varchar(255) not null default '',
    refund_count       int not null default 0,
    total_refund_value decimal(14,2) not null default 0,
    created_at         timestamp default current_timestamp,
    updated_at         timestamp default current_timestamp
                       on update current_timestamp,
    unique key idx_date_currency_reason (date, currency, return_reason)
) engine=InnoDB default charset=utf8mb4 collate=utf8mb4_unicode_ci;
