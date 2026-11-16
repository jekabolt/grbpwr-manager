-- +migrate Up

-- Composite V4 adds exact-N Assembly authority; V5 preserves those V4 bytes and adds user-authored
-- topology annotations. 0472 is already applied and immutable, so widen only its stable named
-- CHECK. MySQL DDL auto-commits: both halves are guarded so a boot interrupted after DROP CHECK can
-- safely run this file again.
-- migrationlint:allow-retroactive the replacement CHECK strictly widens the existing allowlist, so every previously valid sidecar remains valid
SET @has_composite_v4_v5_media_check := (
  SELECT COUNT(*)
  FROM information_schema.TABLE_CONSTRAINTS tc
  JOIN information_schema.CHECK_CONSTRAINTS cc
    ON cc.CONSTRAINT_SCHEMA = tc.CONSTRAINT_SCHEMA
   AND cc.CONSTRAINT_NAME = tc.CONSTRAINT_NAME
  WHERE tc.CONSTRAINT_SCHEMA = DATABASE()
    AND tc.TABLE_NAME = 'tech_card_pattern_semantic_sidecar'
    AND tc.CONSTRAINT_NAME = 'chk_pattern_semantic_sidecar_media_type'
    AND tc.CONSTRAINT_TYPE = 'CHECK'
    AND cc.CHECK_CLAUSE LIKE '%application/vnd.grbpwr.pattern-dxf-product-composite.v4+json%'
    AND cc.CHECK_CLAUSE LIKE '%application/vnd.grbpwr.pattern-dxf-product-composite.v5+json%'
);
SET @has_media_check := (
  SELECT COUNT(*)
  FROM information_schema.TABLE_CONSTRAINTS
  WHERE CONSTRAINT_SCHEMA = DATABASE()
    AND TABLE_NAME = 'tech_card_pattern_semantic_sidecar'
    AND CONSTRAINT_NAME = 'chk_pattern_semantic_sidecar_media_type'
    AND CONSTRAINT_TYPE = 'CHECK'
);
SET @ddl := IF(
  @has_media_check > 0 AND @has_composite_v4_v5_media_check = 0,
  'ALTER TABLE tech_card_pattern_semantic_sidecar DROP CHECK chk_pattern_semantic_sidecar_media_type',
  'SELECT 1'
);
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

SET @has_media_check := (
  SELECT COUNT(*)
  FROM information_schema.TABLE_CONSTRAINTS
  WHERE CONSTRAINT_SCHEMA = DATABASE()
    AND TABLE_NAME = 'tech_card_pattern_semantic_sidecar'
    AND CONSTRAINT_NAME = 'chk_pattern_semantic_sidecar_media_type'
    AND CONSTRAINT_TYPE = 'CHECK'
);
SET @ddl := IF(
  @has_media_check = 0,
  'ALTER TABLE tech_card_pattern_semantic_sidecar ADD CONSTRAINT chk_pattern_semantic_sidecar_media_type CHECK (media_type IN (''application/vnd.grbpwr.pattern-dxf-semantics.v1+json'', ''application/vnd.grbpwr.pattern-dxf-product-semantics.v1+json'', ''application/vnd.grbpwr.pattern-dxf-product-composite.v3+json'', ''application/vnd.grbpwr.pattern-dxf-product-composite.v4+json'', ''application/vnd.grbpwr.pattern-dxf-product-composite.v5+json''))',
  'SELECT 1'
);
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

-- +migrate Down

-- V5 can contain non-rebuildable user measurements and callout text. V4 is also the exact-N owner
-- that V5 preserves. Never destroy either implicitly on rollback: fail before DDL and require an
-- explicit export/removal decision by an operator. Once no V4/V5 rows remain, the guarded DDL is
-- safe to retry after a partial rollback.
SET @composite_v4_v5_rows := (
  SELECT COUNT(*)
  FROM tech_card_pattern_semantic_sidecar
  WHERE media_type IN (
    'application/vnd.grbpwr.pattern-dxf-product-composite.v4+json',
    'application/vnd.grbpwr.pattern-dxf-product-composite.v5+json'
  )
);
SET @ddl := IF(
  @composite_v4_v5_rows > 0,
  'SIGNAL SQLSTATE ''45000'' SET MESSAGE_TEXT = ''0474 rollback refused: export and explicitly remove Composite V4/V5 sidecars first''',
  'SELECT 1'
);
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

SET @has_media_check := (
  SELECT COUNT(*)
  FROM information_schema.TABLE_CONSTRAINTS
  WHERE CONSTRAINT_SCHEMA = DATABASE()
    AND TABLE_NAME = 'tech_card_pattern_semantic_sidecar'
    AND CONSTRAINT_NAME = 'chk_pattern_semantic_sidecar_media_type'
    AND CONSTRAINT_TYPE = 'CHECK'
);
SET @ddl := IF(
  @has_media_check > 0,
  'ALTER TABLE tech_card_pattern_semantic_sidecar DROP CHECK chk_pattern_semantic_sidecar_media_type',
  'SELECT 1'
);
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

SET @has_media_check := (
  SELECT COUNT(*)
  FROM information_schema.TABLE_CONSTRAINTS
  WHERE CONSTRAINT_SCHEMA = DATABASE()
    AND TABLE_NAME = 'tech_card_pattern_semantic_sidecar'
    AND CONSTRAINT_NAME = 'chk_pattern_semantic_sidecar_media_type'
    AND CONSTRAINT_TYPE = 'CHECK'
);
SET @ddl := IF(
  @has_media_check = 0,
  'ALTER TABLE tech_card_pattern_semantic_sidecar ADD CONSTRAINT chk_pattern_semantic_sidecar_media_type CHECK (media_type IN (''application/vnd.grbpwr.pattern-dxf-semantics.v1+json'', ''application/vnd.grbpwr.pattern-dxf-product-semantics.v1+json'', ''application/vnd.grbpwr.pattern-dxf-product-composite.v3+json''))',
  'SELECT 1'
);
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;
