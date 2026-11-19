-- +migrate Up

-- Composite V9 registers the complete-source native cross-row placement on the existing slot.
-- Atomically widen the stable media CHECK. Recover an interrupted prior change if the CHECK is absent.
-- migrationlint:allow-retroactive the replacement CHECK only adds Composite V9, preserving every media type admitted by 0479
SET @has_composite_v9_media_check := (
  SELECT COUNT(*)
  FROM information_schema.TABLE_CONSTRAINTS tc
  JOIN information_schema.CHECK_CONSTRAINTS cc
    ON cc.CONSTRAINT_SCHEMA = tc.CONSTRAINT_SCHEMA
   AND cc.CONSTRAINT_NAME = tc.CONSTRAINT_NAME
  WHERE tc.CONSTRAINT_SCHEMA = DATABASE()
    AND tc.TABLE_NAME = 'tech_card_pattern_semantic_sidecar'
    AND tc.CONSTRAINT_NAME = 'chk_pattern_semantic_sidecar_media_type'
    AND tc.CONSTRAINT_TYPE = 'CHECK'
    AND cc.CHECK_CLAUSE LIKE '%application/vnd.grbpwr.pattern-dxf-product-composite.v9+json%'
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
  @has_media_check > 0 AND @has_composite_v9_media_check = 0,
  'ALTER TABLE tech_card_pattern_semantic_sidecar DROP CHECK chk_pattern_semantic_sidecar_media_type, ADD CONSTRAINT chk_pattern_semantic_sidecar_media_type CHECK (media_type IN (''application/vnd.grbpwr.pattern-dxf-semantics.v1+json'', ''application/vnd.grbpwr.pattern-dxf-product-semantics.v1+json'', ''application/vnd.grbpwr.pattern-dxf-product-composite.v3+json'', ''application/vnd.grbpwr.pattern-dxf-product-composite.v4+json'', ''application/vnd.grbpwr.pattern-dxf-product-composite.v5+json'', ''application/vnd.grbpwr.pattern-dxf-product-composite.v6+json'', ''application/vnd.grbpwr.pattern-dxf-product-composite.v7+json'', ''application/vnd.grbpwr.pattern-dxf-product-composite.v8+json'', ''application/vnd.grbpwr.pattern-dxf-product-composite.v9+json''))',
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
  'ALTER TABLE tech_card_pattern_semantic_sidecar ADD CONSTRAINT chk_pattern_semantic_sidecar_media_type CHECK (media_type IN (''application/vnd.grbpwr.pattern-dxf-semantics.v1+json'', ''application/vnd.grbpwr.pattern-dxf-product-semantics.v1+json'', ''application/vnd.grbpwr.pattern-dxf-product-composite.v3+json'', ''application/vnd.grbpwr.pattern-dxf-product-composite.v4+json'', ''application/vnd.grbpwr.pattern-dxf-product-composite.v5+json'', ''application/vnd.grbpwr.pattern-dxf-product-composite.v6+json'', ''application/vnd.grbpwr.pattern-dxf-product-composite.v7+json'', ''application/vnd.grbpwr.pattern-dxf-product-composite.v8+json'', ''application/vnd.grbpwr.pattern-dxf-product-composite.v9+json''))',
  'SELECT 1'
);
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

-- +migrate Down

-- Refuse rollback while V9 placements exist. Recover a missing CHECK before checking whether
-- the previous 0479 allowlist can be restored atomically.
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
  'ALTER TABLE tech_card_pattern_semantic_sidecar ADD CONSTRAINT chk_pattern_semantic_sidecar_media_type CHECK (media_type IN (''application/vnd.grbpwr.pattern-dxf-semantics.v1+json'', ''application/vnd.grbpwr.pattern-dxf-product-semantics.v1+json'', ''application/vnd.grbpwr.pattern-dxf-product-composite.v3+json'', ''application/vnd.grbpwr.pattern-dxf-product-composite.v4+json'', ''application/vnd.grbpwr.pattern-dxf-product-composite.v5+json'', ''application/vnd.grbpwr.pattern-dxf-product-composite.v6+json'', ''application/vnd.grbpwr.pattern-dxf-product-composite.v7+json'', ''application/vnd.grbpwr.pattern-dxf-product-composite.v8+json'', ''application/vnd.grbpwr.pattern-dxf-product-composite.v9+json''))',
  'SELECT 1'
);
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

-- The atomic ALTER validates existing rows before replacing the CHECK. A V9 row causes the
-- narrowing to fail, retaining the wider constraint and every existing review.
SET @has_composite_v9_media_check := (
  SELECT COUNT(*)
  FROM information_schema.TABLE_CONSTRAINTS tc
  JOIN information_schema.CHECK_CONSTRAINTS cc
    ON cc.CONSTRAINT_SCHEMA = tc.CONSTRAINT_SCHEMA
   AND cc.CONSTRAINT_NAME = tc.CONSTRAINT_NAME
  WHERE tc.CONSTRAINT_SCHEMA = DATABASE()
    AND tc.TABLE_NAME = 'tech_card_pattern_semantic_sidecar'
    AND tc.CONSTRAINT_NAME = 'chk_pattern_semantic_sidecar_media_type'
    AND tc.CONSTRAINT_TYPE = 'CHECK'
    AND cc.CHECK_CLAUSE LIKE '%application/vnd.grbpwr.pattern-dxf-product-composite.v9+json%'
);
SET @ddl := IF(
  @has_composite_v9_media_check > 0,
  'ALTER TABLE tech_card_pattern_semantic_sidecar DROP CHECK chk_pattern_semantic_sidecar_media_type, ADD CONSTRAINT chk_pattern_semantic_sidecar_media_type CHECK (media_type IN (''application/vnd.grbpwr.pattern-dxf-semantics.v1+json'', ''application/vnd.grbpwr.pattern-dxf-product-semantics.v1+json'', ''application/vnd.grbpwr.pattern-dxf-product-composite.v3+json'', ''application/vnd.grbpwr.pattern-dxf-product-composite.v4+json'', ''application/vnd.grbpwr.pattern-dxf-product-composite.v5+json'', ''application/vnd.grbpwr.pattern-dxf-product-composite.v6+json'', ''application/vnd.grbpwr.pattern-dxf-product-composite.v7+json'', ''application/vnd.grbpwr.pattern-dxf-product-composite.v8+json''))',
  'SELECT 1'
);
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;
