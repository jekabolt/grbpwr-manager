-- +migrate Up

-- production-costing Phase 4 (plan file 05, amendment 4): idempotency = REPLAY, not reject. One row
-- per executed command, written IN THE SAME transaction as the command's effects, so "the row
-- exists" and "the effects happened" are one fact. A retry with the same (command_type, key) and
-- the same request_hash gets the stored response replayed as the original success; the same key
-- with a DIFFERENT hash is a client bug and is rejected with AlreadyExists — never silently
-- executed twice, never silently swallowed.
--
-- status is 'succeeded' for every visible row today (tx atomicity: a failed command rolls its row
-- back); the column exists so multi-step commands in later phases can park 'in_progress' markers
-- without a schema change.
CREATE TABLE IF NOT EXISTS command_idempotency (
    id INT AUTO_INCREMENT PRIMARY KEY,
    command_type VARCHAR(64) NOT NULL,
    idempotency_key VARCHAR(64) NOT NULL,
    -- SHA-256 hex of the canonical request payload; 64 chars exactly.
    request_hash CHAR(64) NOT NULL,
    status VARCHAR(16) NOT NULL DEFAULT 'succeeded',
    -- the created aggregate(s), e.g. 'receipt:123' — for operators tracing a replayed command.
    result_ids VARCHAR(255) NULL,
    -- canonical JSON of the command's response, replayed verbatim on a matching retry.
    response MEDIUMTEXT NULL,
    created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    CONSTRAINT uq_cmdidem UNIQUE (command_type, idempotency_key),
    CONSTRAINT chk_cmdidem_status CHECK (status REGEXP '^(in_progress|succeeded|failed)$')
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

-- +migrate Down

DROP TABLE IF EXISTS command_idempotency;
