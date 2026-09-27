-- AI providers wave (27.09), commit A "shadow" — THE CONFIGURATION: which provider is on, whose key it
-- answers with, which provider serves which purpose, and the version number by which a running server
-- learns that any of it changed.
--
-- WHY TABLES AND NOT ENV. Every provider client copies its key out of the env config in New() and has no
-- way to swap it (10-CURRENT-SYSTEM B.1): rotating a key, or turning a provider off, is a redeploy today.
-- These four tables are what the registry reads instead. A write bumps ai_settings.config_version in the
-- SAME transaction, the registry polls that one number, and the next request answers with the new key.
-- Env stays the fallback: a provider with no key here keeps answering with its env key, so an empty
-- table is exactly today's behaviour.
--
-- KEYS ARE CIPHERTEXT. api_key_enc / admin_key_enc hold 12-byte nonce + AES-256-GCM(key) under
-- AI_KEYS_MASTER_KEY, which lives in the environment and never in this database; the associated data
-- is "provider_key:kind", so a blob copied onto another row or into the other slot does not open.
-- A dump of this table is not a dump of our keys. *_last4 exists only to show the owner which key is
-- set; *_updated_at / *_updated_by answer "set by whom, when". VARBINARY(2048) leaves room for the
-- longest key any of the nine issues plus the 28 bytes of nonce and tag.
-- admin_key_* is the RECONCILIATION key (D-06): the cost APIs of OpenAI, Anthropic and fal refuse the
-- ordinary key and want an admin one. It never serves a generation.
--
-- VOCABULARIES ARE VARCHAR, CLOSED IN GO (entity.IsAIProviderKey, entity.IsAIPurpose), no ENUM and no
-- CHECK — the design band rule (0340): a new provider or purpose is one Go line and one seed row, never
-- an ALTER on a live table.
--
-- ai_route: provider_key '' means "the default provider of this capability" (ai_settings.default_*),
-- model '' means "the client's own env/default slug" (OPENROUTER_MODEL, FAL_MODEL_3D, ...). That '' is
-- why ai_route has NO foreign key to ai_provider. The seed is today's behaviour, one row per purpose at
-- position 1 with model '' everywhere, so commit A changes nothing until the owner saves a route.
--
-- ai_model holds ONLY custom slugs typed into a route. The curated catalogue and its prices are code
-- (aiprov/pricing, with its Version), not rows — a price we read on a web page must not look like data
-- the owner entered. Cascades from ai_provider.
--
-- ai_settings is a singleton on the 0272 / 0344 pattern: one row IS the configuration, hence the
-- plural and the NAMED CHECK (id = 1). A CHECK on a NEW one-row table is safe (it has no history to
-- re-validate), unlike one added to an existing table.
--
-- IDEMPOTENT: MySQL autocommits DDL, so a half-applied file is re-run from the top on the next boot.
-- Every table is IF NOT EXISTS and every seed is a no-op upsert (ON DUPLICATE KEY UPDATE pk = pk), which
-- also means a re-run never overwrites what the owner chose later (an enabled flag, a route).
-- Seeded enabled = 1: openrouter, fal, meshy, recraft (the four that serve traffic today). The five new
-- ones start off.

-- +migrate Up

CREATE TABLE IF NOT EXISTS ai_provider (
    provider_key VARCHAR(32) NOT NULL PRIMARY KEY COMMENT 'entity.AIProviderKeys; closed in Go, no ENUM',
    label VARCHAR(64) NOT NULL COMMENT 'display name in the panel',
    enabled TINYINT(1) NOT NULL DEFAULT 0 COMMENT '0 wins over any key, DB or env: a disabled provider answers nothing',
    api_key_enc VARBINARY(2048) NULL COMMENT 'nonce + AES-256-GCM under AI_KEYS_MASTER_KEY, aad provider_key + kind; NULL = use the env key',
    api_key_last4 CHAR(4) NULL COMMENT 'display only',
    api_key_updated_at DATETIME(6) NULL,
    api_key_updated_by VARCHAR(255) NULL COMMENT 'JWT username of the last writer of this key',
    admin_key_enc VARBINARY(2048) NULL COMMENT 'reconciliation key for the cost APIs (D-06); never serves a generation',
    admin_key_last4 CHAR(4) NULL,
    admin_key_updated_at DATETIME(6) NULL,
    admin_key_updated_by VARCHAR(255) NULL,
    updated_by VARCHAR(255) NOT NULL DEFAULT '' COMMENT 'JWT username of the last writer of the row',
    updated_at DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6) ON UPDATE CURRENT_TIMESTAMP(6)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT 'AI provider registry: on/off and encrypted keys, one row per provider';

INSERT INTO ai_provider (provider_key, label, enabled) VALUES
    ('openai', 'OpenAI', 0),
    ('anthropic', 'Anthropic', 0),
    ('google', 'Google Gemini', 0),
    ('openrouter', 'OpenRouter', 1),
    ('apibost', 'apibost', 0),
    ('fal', 'fal', 1),
    ('meshy', 'Meshy', 1),
    ('runblob', 'runblob', 0),
    ('recraft', 'Recraft', 1)
ON DUPLICATE KEY UPDATE provider_key = provider_key;

CREATE TABLE IF NOT EXISTS ai_model (
    provider_key VARCHAR(32) NOT NULL,
    model VARCHAR(128) NOT NULL COMMENT 'the slug exactly as the provider spells it',
    label VARCHAR(128) NOT NULL DEFAULT '',
    kind VARCHAR(16) NOT NULL COMMENT 'capability, entity.AICapabilities; closed in Go',
    disabled TINYINT(1) NOT NULL DEFAULT 0,
    updated_by VARCHAR(255) NOT NULL DEFAULT '',
    updated_at DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6) ON UPDATE CURRENT_TIMESTAMP(6),
    PRIMARY KEY (provider_key, model),
    CONSTRAINT fk_ai_model_provider FOREIGN KEY (provider_key) REFERENCES ai_provider (provider_key) ON DELETE CASCADE
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT 'Custom model slugs typed into a route; the curated list is code';

CREATE TABLE IF NOT EXISTS ai_route (
    purpose VARCHAR(48) NOT NULL COMMENT 'entity.AIPurposes; closed in Go',
    position TINYINT UNSIGNED NOT NULL COMMENT '1 = primary, 2.. = fallbacks in order',
    provider_key VARCHAR(32) NOT NULL DEFAULT '' COMMENT 'empty = the default provider of the capability (ai_settings.default_*); hence no FK',
    model VARCHAR(128) NOT NULL DEFAULT '' COMMENT 'empty = the client env/default slug',
    updated_by VARCHAR(255) NOT NULL DEFAULT '',
    updated_at DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6) ON UPDATE CURRENT_TIMESTAMP(6),
    PRIMARY KEY (purpose, position)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT 'Which provider answers which purpose, primary first';

INSERT INTO ai_route (purpose, position, provider_key, model) VALUES
    ('chat.techcard_operations_draft', 1, 'openrouter', ''),
    ('chat.techcard_enhance', 1, 'openrouter', ''),
    ('chat.techcard_analysis', 1, 'openrouter', ''),
    ('chat.note_markdown', 1, 'openrouter', ''),
    ('chat.email_translate', 1, 'openrouter', ''),
    ('chat.design_draft_idea', 1, 'openrouter', ''),
    ('chat.playground_ideas', 1, 'openrouter', ''),
    ('image.generate', 1, 'openrouter', ''),
    ('image.cutout', 1, 'fal', ''),
    ('image.extend', 1, 'fal', ''),
    ('image.inpaint', 1, 'fal', ''),
    ('threed', 1, 'fal', ''),
    ('vector', 1, 'recraft', '')
ON DUPLICATE KEY UPDATE purpose = purpose;

CREATE TABLE IF NOT EXISTS ai_settings (
    id TINYINT UNSIGNED NOT NULL PRIMARY KEY COMMENT 'singleton; the only legal value is 1',
    config_version BIGINT UNSIGNED NOT NULL DEFAULT 1 COMMENT 'bumped in the same transaction as every config write; the registry polls it',
    default_chat_provider_key VARCHAR(32) NOT NULL DEFAULT 'openrouter' COMMENT 'answers a chat route whose provider_key is empty',
    default_image_provider_key VARCHAR(32) NOT NULL DEFAULT 'openrouter' COMMENT 'answers an image route whose provider_key is empty',
    updated_by VARCHAR(255) NOT NULL DEFAULT '',
    updated_at DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6) ON UPDATE CURRENT_TIMESTAMP(6),
    CONSTRAINT chk_ai_settings_singleton CHECK (id = 1)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT 'AI providers settings: one row IS the configuration version';

INSERT INTO ai_settings (id) VALUES (1)
ON DUPLICATE KEY UPDATE id = id;

-- +migrate Down

DROP TABLE IF EXISTS ai_settings;
DROP TABLE IF EXISTS ai_route;
DROP TABLE IF EXISTS ai_model;
DROP TABLE IF EXISTS ai_provider;
