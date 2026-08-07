-- +migrate Up

-- Дом настроек цеха (Ф2.5). Four phases of the production-cutting plan each said "this should come
-- from a workshop setting" and none of them created a place for one to live: длина стола (Ф2.5),
-- припуск по умолчанию (Ф3.2), предел высоты стопки (Ф4.8), минимальный зазор (08-cut-out). This is
-- that place. Первый жилец — длина раскройного стола.
--
-- WHY A SINGLETON ROW WITH ONE TYPED COLUMN PER SETTING, and not the key/value shape of
-- alert_setting (0089):
--
--   * alert_setting is key/value for a reason that does not hold here — every alert threshold has a
--     sensible non-NULL default (entity.DefaultAlertThresholds seeds the table), so "unset" is a
--     state that never exists and a bare DECIMAL NOT NULL column is honest. A cutting table has no
--     default: the workshop either has one of a known length or the length is not known yet, and
--     those two must stay distinguishable. 0 cm is not a table, and "no table configured" must never
--     silently become a verdict of "every marker is too long". NULL says that; a k/v table can only
--     say it by the ABSENCE of a row, which no CHECK can constrain and no reader can typo-proof.
--
--   * Every tenant is a NUMBER WITH A UNIT AND A RANGE. A typed column carries its own type, its own
--     named CHECK and its own NULL-means-unset semantics in the schema, enforced against every
--     writer — including a manual UPDATE at the mysql prompt, which is exactly when a Go-side
--     validator is not running. A k/v table can only CHECK one rule for all keys at once.
--
--   * A typo in a k/v key is a SILENT new setting: writing 'cutting_table_lenght_cm' inserts a row
--     nobody reads while the real key keeps its old value, and the operator swears they set it.
--     Writing a misspelled COLUMN is a SQL error. Same class of failure the RPC layer refuses.
--
--   * The "no migration per setting" saving is largely illusory: each tenant needs an entity field,
--     a dto mapping, a proto field and an admin control regardless, so the migration is one file out
--     of six — and the closed, already-enumerated set of four tenants is not the open vocabulary
--     that would justify k/v in the first place.
--
--   * Not every future tenant is numeric. Ф6.1 wants a report-only MODE and Ф6.9 a blocking-mode
--     switch — booleans. In a DECIMAL k/v table a boolean is a 0/1 lie; here it is a BOOLEAN column.
--
-- The table name is PLURAL on purpose: one row IS the whole configuration. `workshop_setting`
-- (singular, like alert_setting) would advertise one row per setting — precisely the shape rejected
-- above.
--
-- No table charset clause on purpose (0252/0257 precedent) -- prod/beta run the utf8mb3 server
-- default while local container tests connect utf8mb4, and an explicit clause would diverge them
-- invisibly.
CREATE TABLE IF NOT EXISTS workshop_settings (
    id INT NOT NULL PRIMARY KEY COMMENT 'singleton; the only legal value is 1',
    -- NULL = the workshop has not told us its cutting table length. NOT the same as 0, and the
    -- reader must not treat it as one: an unset length yields "no verdict", never "too long".
    cutting_table_length_cm DECIMAL(10,2) NULL COMMENT 'length of the cutting/spreading table in cm; NULL = not configured, no length verdict',
    updated_by VARCHAR(255) NOT NULL DEFAULT '' COMMENT 'admin username of the last writer',
    updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
    CONSTRAINT chk_workshop_settings_singleton CHECK (id = 1),
    -- The DB carries the INVARIANTS: unset is NULL, a stored length is strictly positive (so NULL
    -- and "0 cm" can never collapse into each other), and no row may hold an absurd magnitude. The
    -- 5000 cm ceiling is 50 m: the longest industrial spreading tables in existence run 10-30 m,
    -- with custom installations reaching about 50 m, so anything beyond it is a unit mistake or a
    -- stray zero rather than a table. The Go layer (entity.ValidateCuttingTableLengthCm) repeats the
    -- same ceiling with a readable message and adds a plausibility FLOOR that deliberately does not
    -- live here -- see the comment there.
    CONSTRAINT chk_workshop_settings_table_length CHECK (
        cutting_table_length_cm IS NULL
        OR (cutting_table_length_cm > 0 AND cutting_table_length_cm <= 5000)
    )
) ENGINE=InnoDB COMMENT 'Дом настроек цеха: singleton shop-floor configuration, one typed column per setting';

-- Seed the singleton with everything unset. INSERT IGNORE keeps the file re-runnable after a
-- half-applied retry (MySQL DDL auto-commits, so the next boot re-runs this from the top).
INSERT IGNORE INTO workshop_settings (id) VALUES (1);

-- +migrate Down
DROP TABLE IF EXISTS workshop_settings;
