-- +migrate Up

-- tech_card_size_measurement.measurement_name_id was declared ON DELETE CASCADE in 0141. Deleting a
-- row from the measurement_name dictionary therefore silently wiped that measurement out of EVERY
-- style's size chart -- the same class of destruction 0149 closed for size_id, and the last surviving
-- CASCADE on this table. 0149 flipped only fk_tcsm_card's sibling fk_tcsm_size (to
-- fk_tcsm_size_restrict) and left this one alone; 0210 then documented its own fk_tcgr_name as
-- "mirrors fk_tcsm_size_restrict / fk_tcsm_name from 0141+0149", which was true of the size FK but
-- NOT of this one. This migration makes that claim true.
--
-- A measurement name still referenced by any style chart can no longer be physically deleted -- it
-- must be archived instead, exactly like a referenced size. The graded-rule table (0210) already
-- holds RESTRICT on the same dictionary, so the two paths now agree.
--
-- Idempotent. The old constraint name is discovered from information_schema rather than hard-coded
-- (MySQL has no DROP FOREIGN KEY IF EXISTS, and this FK is auto-named on schemas built before the
-- explicit name landed), and the block only acts while the FK is still CASCADE, so a re-run after a
-- partial apply is a no-op. DROP and ADD ride in one atomic ALTER, so there is no window without a
-- foreign key. The new FK takes a distinct name (fk_tcsm_name_restrict, mirroring
-- fk_tcsm_size_restrict) because MySQL forbids dropping and re-adding the same FK name in one ALTER.
-- 0141/0149/0210 are not edited.

SET @old_fk := (SELECT CONSTRAINT_NAME FROM information_schema.KEY_COLUMN_USAGE
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'tech_card_size_measurement'
      AND COLUMN_NAME = 'measurement_name_id' AND REFERENCED_TABLE_NAME = 'measurement_name' LIMIT 1);
SET @is_cascade := (SELECT COUNT(*) FROM information_schema.REFERENTIAL_CONSTRAINTS
    WHERE CONSTRAINT_SCHEMA = DATABASE() AND TABLE_NAME = 'tech_card_size_measurement'
      AND CONSTRAINT_NAME = @old_fk AND DELETE_RULE = 'CASCADE');
SET @sql := IF(@old_fk IS NOT NULL AND @is_cascade = 1,
    CONCAT('ALTER TABLE tech_card_size_measurement DROP FOREIGN KEY ', @old_fk,
           ', ADD CONSTRAINT fk_tcsm_name_restrict FOREIGN KEY (measurement_name_id) REFERENCES measurement_name(id) ON DELETE RESTRICT'),
    'SELECT 1');
PREPARE s FROM @sql;
EXECUTE s;
DEALLOCATE PREPARE s;

-- +migrate Down
SELECT 1;
