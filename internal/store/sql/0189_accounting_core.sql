-- +migrate Up

-- Accounting core (double-entry ledger), phase 1. The ledger is a DERIVED, append-only
-- projection of existing operational facts (orders, material movements, production runs,
-- opex) plus manual entries. Base currency EUR. See docs/plan-accounting/.

CREATE TABLE IF NOT EXISTS acct_account (
    id          INT AUTO_INCREMENT PRIMARY KEY,
    code        VARCHAR(8)  NOT NULL,                -- '1030', '4020', ... (Excel-совместимые коды)
    name        VARCHAR(128) NOT NULL,
    -- section управляет местом в отчётах и знаком нормального сальдо:
    -- asset/cogs/opex — дебетовое, liability/equity/revenue — кредитовое.
    section     VARCHAR(16) NOT NULL,
    statement   VARCHAR(2)  NOT NULL,                -- 'BS' | 'PL'
    -- system-счета участвуют в автопостинге: их нельзя архивировать/переименовывать кодом.
    is_system   BOOLEAN NOT NULL DEFAULT FALSE,
    archived    BOOLEAN NOT NULL DEFAULT FALSE,
    created_at  TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at  TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
    UNIQUE KEY uniq_acct_account_code (code),
    CONSTRAINT chk_acct_account_section
        CHECK (section IN ('asset','liability','equity','revenue','cogs','opex')),
    CONSTRAINT chk_acct_account_statement CHECK (statement IN ('BS','PL'))
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

CREATE TABLE IF NOT EXISTS acct_period (
    period      DATE NOT NULL PRIMARY KEY,           -- всегда 1-е число месяца
    status      VARCHAR(8) NOT NULL DEFAULT 'open',
    closed_at   TIMESTAMP NULL,
    closed_by   VARCHAR(255) NULL,                   -- admin username
    CONSTRAINT chk_acct_period_status CHECK (status IN ('open','closed'))
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

CREATE TABLE IF NOT EXISTS acct_journal_entry (
    id           INT AUTO_INCREMENT PRIMARY KEY,
    -- дата хозяйственной операции (не дата постинга); период = первое число её месяца
    occurred_at  DATE NOT NULL,
    description  VARCHAR(512) NOT NULL,
    -- источник + ключ = идемпотентность автопостинга и трассировка к операции
    source_type  VARCHAR(32) NOT NULL,
    source_key   VARCHAR(64) NOT NULL,               -- uuid | uuid:seq | movement id | run id | 'YYYY-MM[:vN]' | manual:<uuid> | rev:<id>
    -- сторно: ссылка на исходную проводку; исходная получает reversed_by (денормализовано для UI)
    reversal_of  INT NULL,
    reversed_by  INT NULL,
    created_by   VARCHAR(255) NOT NULL DEFAULT 'system',
    -- caveat-флаг: в проводке есть допущение (uncosted позиции пропущены и т.п.)
    has_caveat   BOOLEAN NOT NULL DEFAULT FALSE,
    caveat       VARCHAR(512) NULL,
    created_at   TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    UNIQUE KEY uniq_acct_entry_source (source_type, source_key),
    KEY idx_acct_entry_occurred (occurred_at),
    CONSTRAINT chk_acct_entry_source_type CHECK (source_type IN (
        'order_sale','order_refund',
        'material_receipt','material_issue','material_return',
        'material_writeoff','material_adjustment',
        'production_receive','opex_month','manual','reversal')),
    CONSTRAINT fk_acct_entry_reversal_of FOREIGN KEY (reversal_of)
        REFERENCES acct_journal_entry(id) ON DELETE RESTRICT,
    CONSTRAINT fk_acct_entry_reversed_by FOREIGN KEY (reversed_by)
        REFERENCES acct_journal_entry(id) ON DELETE RESTRICT
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

CREATE TABLE IF NOT EXISTS acct_journal_line (
    id          INT AUTO_INCREMENT PRIMARY KEY,
    entry_id    INT NOT NULL,
    account_id  INT NOT NULL,
    side        VARCHAR(6) NOT NULL,                 -- 'debit' | 'credit'
    amount      DECIMAL(12,2) NOT NULL,              -- base currency (EUR), строго > 0
    -- след исходной валюты для ручных проводок (внесли GBP-аренду → тут 250.00 GBP), NULL для авто
    amount_src   DECIMAL(12,2) NULL,
    currency_src VARCHAR(4) NULL,                    -- VARCHAR(4): 'USDT' (прецедент 0185)
    note        VARCHAR(255) NULL,
    CONSTRAINT chk_acct_line_side CHECK (side IN ('debit','credit')),
    CONSTRAINT chk_acct_line_amount CHECK (amount > 0),
    CONSTRAINT fk_acct_line_entry FOREIGN KEY (entry_id)
        REFERENCES acct_journal_entry(id) ON DELETE CASCADE,
    CONSTRAINT fk_acct_line_account FOREIGN KEY (account_id)
        REFERENCES acct_account(id) ON DELETE RESTRICT,
    KEY idx_acct_line_account (account_id, entry_id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

-- Push-outbox для событий заказов (см. 03-event-capture.md). Вставляется в ту же Tx,
-- что и бизнес-операция; разбирается воркером acctposting.
CREATE TABLE IF NOT EXISTS acct_event (
    id           BIGINT AUTO_INCREMENT PRIMARY KEY,
    event_type   VARCHAR(32) NOT NULL,               -- 'order_paid' | 'order_refund'
    source_key   VARCHAR(64) NOT NULL,               -- order uuid | order uuid + ':' + refund seq
    payload      JSON NOT NULL,
    occurred_at  DATETIME NOT NULL,
    created_at   TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    processed_at DATETIME NULL,
    attempts     INT NOT NULL DEFAULT 0,
    -- backoff в явном виде: воркер выставляет при неудаче; NULL = можно брать сразу
    next_retry_at DATETIME NULL,
    last_error   VARCHAR(1024) NULL,
    UNIQUE KEY uniq_acct_event (event_type, source_key),
    KEY idx_acct_event_pending (processed_at, next_retry_at, id),
    CONSTRAINT chk_acct_event_type CHECK (event_type IN ('order_paid','order_refund'))
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

-- Чекпоинты pull-источников (движения склада по id, opex по updated_at и т.п.)
CREATE TABLE IF NOT EXISTS acct_checkpoint (
    source     VARCHAR(32) NOT NULL PRIMARY KEY,     -- 'material_movement' | 'opex_line'
    last_id    BIGINT NULL,
    last_ts    DATETIME NULL,
    updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

-- +migrate Down

DROP TABLE IF EXISTS acct_checkpoint;
DROP TABLE IF EXISTS acct_event;
DROP TABLE IF EXISTS acct_journal_line;
DROP TABLE IF EXISTS acct_journal_entry;
DROP TABLE IF EXISTS acct_period;
DROP TABLE IF EXISTS acct_account;
