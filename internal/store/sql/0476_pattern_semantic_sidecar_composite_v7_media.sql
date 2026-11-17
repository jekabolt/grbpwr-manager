-- +migrate Up

-- Composite V7 preserves the verified V6 carrier and registers the canonical saved Full Assembly
-- physical envelope on the existing sidecar row. 0475 is immutable, so atomically widen its stable
-- media CHECK. The absent-CHECK branch recovers a prior interrupted attempt and is replay-safe.
-- migrationlint:allow-retroactive the replacement CHECK only adds Composite V7, preserving every media type admitted by 0475
SET @has_composite_v7_media_check := (
  SELECT COUNT(*)
  FROM information_schema.TABLE_CONSTRAINTS tc
  JOIN information_schema.CHECK_CONSTRAINTS cc
    ON cc.CONSTRAINT_SCHEMA = tc.CONSTRAINT_SCHEMA
   AND cc.CONSTRAINT_NAME = tc.CONSTRAINT_NAME
  WHERE tc.CONSTRAINT_SCHEMA = DATABASE()
    AND tc.TABLE_NAME = 'tech_card_pattern_semantic_sidecar'
    AND tc.CONSTRAINT_NAME = 'chk_pattern_semantic_sidecar_media_type'
    AND tc.CONSTRAINT_TYPE = 'CHECK'
    AND cc.CHECK_CLAUSE LIKE '%application/vnd.grbpwr.pattern-dxf-product-composite.v7+json%'
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
  @has_media_check > 0 AND @has_composite_v7_media_check = 0,
  'ALTER TABLE tech_card_pattern_semantic_sidecar DROP CHECK chk_pattern_semantic_sidecar_media_type, ADD CONSTRAINT chk_pattern_semantic_sidecar_media_type CHECK (media_type IN (''application/vnd.grbpwr.pattern-dxf-semantics.v1+json'', ''application/vnd.grbpwr.pattern-dxf-product-semantics.v1+json'', ''application/vnd.grbpwr.pattern-dxf-product-composite.v3+json'', ''application/vnd.grbpwr.pattern-dxf-product-composite.v4+json'', ''application/vnd.grbpwr.pattern-dxf-product-composite.v5+json'', ''application/vnd.grbpwr.pattern-dxf-product-composite.v6+json'', ''application/vnd.grbpwr.pattern-dxf-product-composite.v7+json''))',
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
  'ALTER TABLE tech_card_pattern_semantic_sidecar ADD CONSTRAINT chk_pattern_semantic_sidecar_media_type CHECK (media_type IN (''application/vnd.grbpwr.pattern-dxf-semantics.v1+json'', ''application/vnd.grbpwr.pattern-dxf-product-semantics.v1+json'', ''application/vnd.grbpwr.pattern-dxf-product-composite.v3+json'', ''application/vnd.grbpwr.pattern-dxf-product-composite.v4+json'', ''application/vnd.grbpwr.pattern-dxf-product-composite.v5+json'', ''application/vnd.grbpwr.pattern-dxf-product-composite.v6+json'', ''application/vnd.grbpwr.pattern-dxf-product-composite.v7+json''))',
  'SELECT 1'
);
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

-- +migrate Down

-- V7 full-graph/physical bytes must never be destroyed implicitly. Restore a permissive V7 CHECK
-- after a half-apply, refuse rollback while V7 rows exist, then atomically restore 0475's allowlist.
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
  'ALTER TABLE tech_card_pattern_semantic_sidecar ADD CONSTRAINT chk_pattern_semantic_sidecar_media_type CHECK (media_type IN (''application/vnd.grbpwr.pattern-dxf-semantics.v1+json'', ''application/vnd.grbpwr.pattern-dxf-product-semantics.v1+json'', ''application/vnd.grbpwr.pattern-dxf-product-composite.v3+json'', ''application/vnd.grbpwr.pattern-dxf-product-composite.v4+json'', ''application/vnd.grbpwr.pattern-dxf-product-composite.v5+json'', ''application/vnd.grbpwr.pattern-dxf-product-composite.v6+json'', ''application/vnd.grbpwr.pattern-dxf-product-composite.v7+json''))',
  'SELECT 1'
);
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

SET @composite_v7_rows := (
  SELECT COUNT(*)
  FROM tech_card_pattern_semantic_sidecar
  WHERE media_type = 'application/vnd.grbpwr.pattern-dxf-product-composite.v7+json'
);
SET @ddl := IF(
  @composite_v7_rows > 0,
  'SIGNAL SQLSTATE ''45000'' SET MESSAGE_TEXT = ''0476 rollback refused: export and explicitly remove Composite V7 sidecars first''',
  'SELECT 1'
);
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

SET @has_composite_v7_media_check := (
  SELECT COUNT(*)
  FROM information_schema.TABLE_CONSTRAINTS tc
  JOIN information_schema.CHECK_CONSTRAINTS cc
    ON cc.CONSTRAINT_SCHEMA = tc.CONSTRAINT_SCHEMA
   AND cc.CONSTRAINT_NAME = tc.CONSTRAINT_NAME
  WHERE tc.CONSTRAINT_SCHEMA = DATABASE()
    AND tc.TABLE_NAME = 'tech_card_pattern_semantic_sidecar'
    AND tc.CONSTRAINT_NAME = 'chk_pattern_semantic_sidecar_media_type'
    AND tc.CONSTRAINT_TYPE = 'CHECK'
    AND cc.CHECK_CLAUSE LIKE '%application/vnd.grbpwr.pattern-dxf-product-composite.v7+json%'
);
SET @ddl := IF(
  @has_composite_v7_media_check > 0,
  'ALTER TABLE tech_card_pattern_semantic_sidecar DROP CHECK chk_pattern_semantic_sidecar_media_type, ADD CONSTRAINT chk_pattern_semantic_sidecar_media_type CHECK (media_type IN (''application/vnd.grbpwr.pattern-dxf-semantics.v1+json'', ''application/vnd.grbpwr.pattern-dxf-product-semantics.v1+json'', ''application/vnd.grbpwr.pattern-dxf-product-composite.v3+json'', ''application/vnd.grbpwr.pattern-dxf-product-composite.v4+json'', ''application/vnd.grbpwr.pattern-dxf-product-composite.v5+json'', ''application/vnd.grbpwr.pattern-dxf-product-composite.v6+json''))',
  'SELECT 1'
);
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;
