-- +migrate Up

-- Composite V6 preserves the complete verified V5 carrier and adds only a canonical,
-- presentation-only mannequin arrangement. 0474 is already applied and immutable, so replace its
-- stable named CHECK with a strict superset. Existing CHECK replacement is one atomic ALTER, so
-- MySQL's implicit DDL commit can never leave the table without a constraint. The absent-CHECK
-- branch exists only to recover an interrupted older attempt and is replay-safe.
-- migrationlint:allow-retroactive the replacement CHECK only adds Composite V6, preserving every media type admitted by 0474
SET @has_composite_v6_media_check := (
  SELECT COUNT(*)
  FROM information_schema.TABLE_CONSTRAINTS tc
  JOIN information_schema.CHECK_CONSTRAINTS cc
    ON cc.CONSTRAINT_SCHEMA = tc.CONSTRAINT_SCHEMA
   AND cc.CONSTRAINT_NAME = tc.CONSTRAINT_NAME
  WHERE tc.CONSTRAINT_SCHEMA = DATABASE()
    AND tc.TABLE_NAME = 'tech_card_pattern_semantic_sidecar'
    AND tc.CONSTRAINT_NAME = 'chk_pattern_semantic_sidecar_media_type'
    AND tc.CONSTRAINT_TYPE = 'CHECK'
    AND cc.CHECK_CLAUSE LIKE '%application/vnd.grbpwr.pattern-dxf-product-composite.v6+json%'
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
  @has_media_check > 0 AND @has_composite_v6_media_check = 0,
  'ALTER TABLE tech_card_pattern_semantic_sidecar DROP CHECK chk_pattern_semantic_sidecar_media_type, ADD CONSTRAINT chk_pattern_semantic_sidecar_media_type CHECK (media_type IN (''application/vnd.grbpwr.pattern-dxf-semantics.v1+json'', ''application/vnd.grbpwr.pattern-dxf-product-semantics.v1+json'', ''application/vnd.grbpwr.pattern-dxf-product-composite.v3+json'', ''application/vnd.grbpwr.pattern-dxf-product-composite.v4+json'', ''application/vnd.grbpwr.pattern-dxf-product-composite.v5+json'', ''application/vnd.grbpwr.pattern-dxf-product-composite.v6+json''))',
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
  'ALTER TABLE tech_card_pattern_semantic_sidecar ADD CONSTRAINT chk_pattern_semantic_sidecar_media_type CHECK (media_type IN (''application/vnd.grbpwr.pattern-dxf-semantics.v1+json'', ''application/vnd.grbpwr.pattern-dxf-product-semantics.v1+json'', ''application/vnd.grbpwr.pattern-dxf-product-composite.v3+json'', ''application/vnd.grbpwr.pattern-dxf-product-composite.v4+json'', ''application/vnd.grbpwr.pattern-dxf-product-composite.v5+json'', ''application/vnd.grbpwr.pattern-dxf-product-composite.v6+json''))',
  'SELECT 1'
);
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

-- +migrate Down

-- V6 mannequin arrangement bytes are user-authored presentation state. Never destroy them during
-- rollback. First repair any pre-existing half-apply by restoring the permissive V6 CHECK. Once an
-- operator has explicitly exported/removed V6 rows, narrow to the exact 0474 allowlist with one
-- atomic ALTER. A concurrent V6 insert either lands before that ALTER and makes validation fail
-- while the old CHECK remains, or lands afterwards and is rejected by the new CHECK.
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
  'ALTER TABLE tech_card_pattern_semantic_sidecar ADD CONSTRAINT chk_pattern_semantic_sidecar_media_type CHECK (media_type IN (''application/vnd.grbpwr.pattern-dxf-semantics.v1+json'', ''application/vnd.grbpwr.pattern-dxf-product-semantics.v1+json'', ''application/vnd.grbpwr.pattern-dxf-product-composite.v3+json'', ''application/vnd.grbpwr.pattern-dxf-product-composite.v4+json'', ''application/vnd.grbpwr.pattern-dxf-product-composite.v5+json'', ''application/vnd.grbpwr.pattern-dxf-product-composite.v6+json''))',
  'SELECT 1'
);
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

SET @composite_v6_rows := (
  SELECT COUNT(*)
  FROM tech_card_pattern_semantic_sidecar
  WHERE media_type = 'application/vnd.grbpwr.pattern-dxf-product-composite.v6+json'
);
SET @ddl := IF(
  @composite_v6_rows > 0,
  'SIGNAL SQLSTATE ''45000'' SET MESSAGE_TEXT = ''0475 rollback refused: export and explicitly remove Composite V6 sidecars first''',
  'SELECT 1'
);
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

SET @has_composite_v6_media_check := (
  SELECT COUNT(*)
  FROM information_schema.TABLE_CONSTRAINTS tc
  JOIN information_schema.CHECK_CONSTRAINTS cc
    ON cc.CONSTRAINT_SCHEMA = tc.CONSTRAINT_SCHEMA
   AND cc.CONSTRAINT_NAME = tc.CONSTRAINT_NAME
  WHERE tc.CONSTRAINT_SCHEMA = DATABASE()
    AND tc.TABLE_NAME = 'tech_card_pattern_semantic_sidecar'
    AND tc.CONSTRAINT_NAME = 'chk_pattern_semantic_sidecar_media_type'
    AND tc.CONSTRAINT_TYPE = 'CHECK'
    AND cc.CHECK_CLAUSE LIKE '%application/vnd.grbpwr.pattern-dxf-product-composite.v6+json%'
);
SET @ddl := IF(
  @has_composite_v6_media_check > 0,
  'ALTER TABLE tech_card_pattern_semantic_sidecar DROP CHECK chk_pattern_semantic_sidecar_media_type, ADD CONSTRAINT chk_pattern_semantic_sidecar_media_type CHECK (media_type IN (''application/vnd.grbpwr.pattern-dxf-semantics.v1+json'', ''application/vnd.grbpwr.pattern-dxf-product-semantics.v1+json'', ''application/vnd.grbpwr.pattern-dxf-product-composite.v3+json'', ''application/vnd.grbpwr.pattern-dxf-product-composite.v4+json'', ''application/vnd.grbpwr.pattern-dxf-product-composite.v5+json''))',
  'SELECT 1'
);
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;
