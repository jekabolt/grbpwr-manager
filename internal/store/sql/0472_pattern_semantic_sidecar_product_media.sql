-- +migrate Up

-- 0471 originally admitted only the bare-R2000 semantic envelope. Product import writes the
-- manifest-embedded R12 stream and owns a separately versioned canonical envelope; both retain the
-- same row-lock/DXF/post-save CAS. Replace only the named media check and remain safe after a
-- mid-file MySQL DDL commit.
-- migrationlint:allow-retroactive the replacement CHECK strictly widens the old one-value set, so every existing legacy row remains valid
SET @has_old_or_new_media_check := (
  SELECT COUNT(*)
  FROM information_schema.TABLE_CONSTRAINTS
  WHERE CONSTRAINT_SCHEMA = DATABASE()
    AND TABLE_NAME = 'tech_card_pattern_semantic_sidecar'
    AND CONSTRAINT_NAME = 'chk_pattern_semantic_sidecar_media_type'
    AND CONSTRAINT_TYPE = 'CHECK'
);
SET @ddl := IF(
  @has_old_or_new_media_check > 0,
  'ALTER TABLE tech_card_pattern_semantic_sidecar DROP CHECK chk_pattern_semantic_sidecar_media_type',
  'SELECT 1'
);
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

SET @has_product_media_check := (
  SELECT COUNT(*)
  FROM information_schema.TABLE_CONSTRAINTS
  WHERE CONSTRAINT_SCHEMA = DATABASE()
    AND TABLE_NAME = 'tech_card_pattern_semantic_sidecar'
    AND CONSTRAINT_NAME = 'chk_pattern_semantic_sidecar_media_type'
    AND CONSTRAINT_TYPE = 'CHECK'
);
SET @ddl := IF(
  @has_product_media_check = 0,
  'ALTER TABLE tech_card_pattern_semantic_sidecar ADD CONSTRAINT chk_pattern_semantic_sidecar_media_type CHECK (media_type IN (''application/vnd.grbpwr.pattern-dxf-semantics.v1+json'', ''application/vnd.grbpwr.pattern-dxf-product-semantics.v1+json'', ''application/vnd.grbpwr.pattern-dxf-product-composite.v3+json''))',
  'SELECT 1'
);
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

-- +migrate Down

DELETE FROM tech_card_pattern_semantic_sidecar
WHERE media_type = 'application/vnd.grbpwr.pattern-dxf-product-semantics.v1+json';

DELETE FROM tech_card_pattern_semantic_sidecar
WHERE media_type = 'application/vnd.grbpwr.pattern-dxf-product-composite.v3+json';

ALTER TABLE tech_card_pattern_semantic_sidecar
  DROP CHECK chk_pattern_semantic_sidecar_media_type,
  ADD CONSTRAINT chk_pattern_semantic_sidecar_media_type CHECK (
    media_type = 'application/vnd.grbpwr.pattern-dxf-semantics.v1+json'
  );

