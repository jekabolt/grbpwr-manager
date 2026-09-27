-- T45 (27.09) — the SKU colour segment leaves the colour dictionary. FIRST PUSH of two (D-69).
-- Owner's decisions (06-COLOURWAYS-RESEARCH.md §7), items 1, 2 and 4.
-- ---
-- Until now product.color_code (FK into the 17-colour dictionary) was at once the colour, the SKU
-- segment and the per-style uniqueness key uniq_product_style_color (style_id, color_code), 0151.
-- From here on:
--   * product.sku_color_token CHAR(3) is the SKU segment — minted by the server when the colourway
--     is created, never changed afterwards, unique per style (uniq_product_style_sku_color_token);
--   * product.color_code stays mandatory as the dictionary FAMILY tag (filters, assembly).
-- ---
-- THIS FILE IS ADDITIVE ONLY. uniq_product_style_color STAYS: dropping it — which is what lets two
-- colourways of one style share a family — is 0377, a separate migration shipped in a separate,
-- later push. Between the two pushes a second colourway of one family in a style is refused by that
-- index (MySQL 1062), and the application answers it with its ordinary «a colourway with this colour
-- already exists» (FailedPrecondition), never an Internal.
--
-- Rollback in that window: the schema stays readable and writable by the previous binary (the
-- column is NULLABLE, nothing it uses is gone). The DATA is safe under it only while EVERY token
-- equals its family: an older binary re-derives an unfrozen SKU from color_code (…-BLK), so the
-- first row whose token and family differ closes the window, however it got there. This binary
-- writes such rows already in this window, for example:
--   * a colourway created with a palette — its token is minted from its name («black and white» →
--     BKW);
--   * a family edit on an existing colourway — color_code moves, the token stays frozen;
--   * an archive restore — the token comes back verbatim from its source, palette or not.
-- So before any binary rollback past T45 this must return 0 (0376 Down refuses on the same count):
--     SELECT COUNT(*) FROM product WHERE sku_color_token IS NOT NULL AND sku_color_token <> color_code;
-- Anything above 0: roll forward, not back. After 0377 a binary rollback past T45 is never safe
-- (see its header).
-- ---
-- STEPS, each re-runnable (MySQL auto-commits DDL, a failed apply re-runs this file from the top):
--   1. add the column, NULLABLE. An older binary (a rollback) inserts products without naming it,
--      and a NOT NULL column without a default would refuse every colourway it creates. Readers take
--      COALESCE(sku_color_token, color_code), so such a row still reads the same token it minted.
--   2. backfill the token from color_code. The SKUs of existing colourways therefore do not move.
--      updated_at is assigned to itself so ON UPDATE CURRENT_TIMESTAMP does not stamp every product
--      as edited today (the storefront uses it for freshness).
--   3. add the unique on (style_id, sku_color_token). Step 2 copies a column that
--      uniq_product_style_color keeps unique per style, so this index cannot meet a duplicate.
-- NULL tokens (rollback rows) do not collide in a MySQL unique index; the application pins them.
-- No CHECK constraint. PREPARE / EXECUTE / DEALLOCATE one per line (no multiStatements on prod).

-- +migrate Up

SET @t45_has_token := (SELECT COUNT(*) FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'product' AND COLUMN_NAME = 'sku_color_token');
SET @ddl := IF(@t45_has_token = 0,
    'ALTER TABLE product ADD COLUMN sku_color_token CHAR(3) NULL COMMENT ''SKU colour segment, minted on create, immutable (T45)''',
    'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

UPDATE product SET sku_color_token = color_code, updated_at = updated_at WHERE sku_color_token IS NULL;

SET @t45_has_token_uniq := (SELECT COUNT(*) FROM information_schema.STATISTICS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'product' AND INDEX_NAME = 'uniq_product_style_sku_color_token');
SET @ddl := IF(@t45_has_token_uniq = 0,
    'ALTER TABLE product ADD CONSTRAINT uniq_product_style_sku_color_token UNIQUE (style_id, sku_color_token)',
    'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

-- +migrate Down
-- THE REFUSALS COME FIRST, BEFORE ANY DDL (precedent 0273 / 0287 Down). A Down that skipped what it
-- could not restore and dropped the rest would leave a schema satisfying neither model — the token
-- gone and the old unique not back. SIGNAL cannot be prepared («This command is not supported in the
-- prepared statement protocol yet», MySQL 8.0.46), so a refusal is a SELECT of a column whose NAME
-- is the message: ERROR 1054, deterministic, nothing dropped, and sql-migrate keeps its gorp row.
-- Identifiers stop at 64 characters, so the text is short and ASCII (a 10-digit count still fits).
--
-- Two states the pre-T45 schema cannot hold, each a refusal:
--   1. a style holding two colourways of one family — uniq_product_style_color cannot come back over
--      them. 0377 Down refuses the same thing and runs first; this one guards a Down of 0376 alone.
--   2. a colourway whose SKU token is not its family — a token minted from a name (BKW), or a
--      legacy colourway whose family moved after T45 (family GRY, token BLK). Dropping the column
--      loses that identity for good: a re-applied Up backfills the FAMILY over it, and a SKU frozen
--      with the token would then carry a colour segment no column names. The count is read only when
--      the column exists, so a re-run after a partial Down refuses for the right reason or not at all.
-- What to do when one fires: for (1), re-family or delete all but one colourway per style and family;
-- for (2), decide per colourway — delete the drafts, or accept the loss explicitly
-- (UPDATE product SET sku_color_token = color_code …) — then run the Down again.

SET @t45_family_dupes := (SELECT COUNT(*) FROM (
    SELECT 1 FROM product GROUP BY style_id, color_code HAVING COUNT(*) > 1) d);
SET @ddl := IF(@t45_family_dupes = 0, 'SELECT 1',
    CONCAT('SELECT `0376 Down blocked: ', @t45_family_dupes, ' families are shared within a style`'));
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

SET @t45_has_token_down := (SELECT COUNT(*) FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'product' AND COLUMN_NAME = 'sku_color_token');
SET @ddl := IF(@t45_has_token_down = 0, 'SELECT 0 INTO @t45_minted',
    'SELECT COUNT(*) INTO @t45_minted FROM product WHERE sku_color_token IS NOT NULL AND sku_color_token <> color_code');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

SET @ddl := IF(@t45_minted = 0, 'SELECT 1',
    CONCAT('SELECT `0376 Down blocked: ', @t45_minted, ' SKU tokens differ from the family`'));
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

-- Then the pre-T45 shape, in the order that never leaves the style_id foreign key without an index
-- (idx_product_style_id, 0138, serves it throughout): the family unique if it is absent (0377 Down
-- normally put it back already; refusal 1 proved it fits), the token unique, the column.

SET @t45_has_colour_uniq_down := (SELECT COUNT(*) FROM information_schema.STATISTICS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'product' AND INDEX_NAME = 'uniq_product_style_color');
SET @ddl := IF(@t45_has_colour_uniq_down = 0,
    'ALTER TABLE product ADD CONSTRAINT uniq_product_style_color UNIQUE (style_id, color_code)',
    'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

SET @t45_has_token_uniq_down := (SELECT COUNT(*) FROM information_schema.STATISTICS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'product' AND INDEX_NAME = 'uniq_product_style_sku_color_token');
SET @ddl := IF(@t45_has_token_uniq_down > 0,
    'ALTER TABLE product DROP INDEX uniq_product_style_sku_color_token',
    'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

SET @ddl := IF(@t45_has_token_down > 0,
    'ALTER TABLE product DROP COLUMN sku_color_token',
    'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;
