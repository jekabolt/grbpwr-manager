-- Полоса DESIGN, кусок 3 из 7: версии листа — замороженный выпуск, который печатают.
--
-- ЧТО МОРОЗИТСЯ. Версия листа (Rev.N) — это акт человека: состав плит + номера выносок на момент
-- минта. Лист v3 печатается через год, и он обязан напечататься тем же, чем был.
--
-- ПОЧЕМУ ПЛИТЫ И ВЫНОСКИ — СТРОКИ, А НЕ JSON ВНУТРИ ВЕРСИИ. Медиатека обязана сказать «этот файл
-- держит версия листа», а не «свободен». Ссылка на media(id) внутри JSON этого не даёт:
-- `GetMediaUsage` (`internal/store/content/media_usage.go:67-188`) — чисто реляционный UNION ALL по
-- колонкам-ссылкам, без единого JSON-скана. Добавление источника туда — ОБЯЗАННОСТЬ, а не опция:
-- `TestMediaUsageRegistryCoversSchema` (`internal/store/media_usage_integration_test.go:148`)
-- диффит реестр против живых FK в media(id) и краснеет на незарегистрированной колонке. Обе новые
-- колонки этого файла (`design_sheet_version_plate.media_id`, `design_sheet_version_callout.media_id`)
-- регистрируются в куске B-6.
--
-- FK-ПОЛИТИКА ЗДЕСЬ ЖИВЁТ В ТРЁХ РАЗНЫХ РЕЖИМАХ, и каждый выбран, а не унаследован:
--   * `media_id → media RESTRICT` — намеренно: байты выпуска стереть нельзя, и медиатека покажет,
--     кто держит файл. media от карточки НЕ каскадится, поэтому RESTRICT тут не ломает удаление
--     карточки;
--   * `slot_id → design_bench_slot SET NULL` — намеренно НЕ RESTRICT: и слот, и версия каскадятся
--     от `tech_card`, а RESTRICT между двумя такими таблицами превратил бы `DELETE FROM tech_card`
--     (`techcard.go:697-733`) в 1451, который человеку нечем разрулить. Слот удаляется только через
--     `DeleteDesignDetailSlot`, который САМ отказывает, если слот процитирован любой версией
--     (`slot_in_version {versions}`) — сторож стоит в Go, где у него есть внятный отказ;
--   * `version_id → design_sheet_version CASCADE` — плита и выноска суть части версии.
--
-- ПОЧЕМУ У ПЛИТЫ ЕСТЬ `content_hash`, А У `design_picture` ЕГО НЕТ. У картинки хеш выводится
-- джойном с `media.content_hash` (0336) — вторая колонка была бы ложным расщеплением. У ПЛИТЫ он
-- же — содержание, а не выводимое: плита обязана помнить, какие байты были на бумаге В МОМЕНТ
-- МИНТА, даже если media потом переехало. Тот же довод отделяет копию `detail_name` от имени слота.
--
-- ПОЧЕМУ НА `(version_id, number)` ВЫНОСОК НЕТ UNIQUE. Номер выноски НЕ уникален по карточке:
-- эскиз и мудборд нумеруются независимо (`referencedNumbers()` возвращает [] для мудборда), и
-- схема документа дубли не запрещает (`0067:113`). UNIQUE здесь уронил бы минт на карточке,
-- которая сегодня законна.
--
-- `client_request_id` С UNIQUE НА ВЕРСИИ — потому что два конкурентных минта с одинаковым снимком
-- верстака НЕ обязаны конфликтовать: после сериализации первый создаст vN, второй увидит тот же
-- допустимый верстак и создаст фантомную vN+1 без изменения состава. То же делает потерянный HTTP-
-- ответ и повтор клиента. UNIQUE (tech_card_id, version_number) гарантирует РАЗНЫЕ номера, а не
-- единственность логического минта; единственность даёт ключ запроса.
--
-- `design_sheet_issue` — append-only журнал по образцу `tech_card_revision`: minted / printed /
-- shared. Ничего не минтит и ничего не меняет.

-- +migrate Up

CREATE TABLE IF NOT EXISTS design_sheet_version (
    id INT UNSIGNED PRIMARY KEY AUTO_INCREMENT,
    tech_card_id INT NOT NULL,
    version_number INT NOT NULL COMMENT 'Rev.N; MAX+1 по карточке в транзакции минта, UNIQUE — второй страж',
    client_request_id CHAR(36) NOT NULL COMMENT 'потерянный ответ не рождает фантомную vN+1: повтор возвращает уже созданную версию',
    mixed_consent TINYINT(1) NOT NULL DEFAULT 0 COMMENT '1 = человек явно согласился смешать плиты разного провенанса',
    minted_via VARCHAR(24) NOT NULL DEFAULT '' COMMENT 'каким жестом сминчено (кнопка листа, первая выноска и т.п.)',
    minted_by VARCHAR(255) NOT NULL DEFAULT '' COMMENT 'username из JWT',
    minted_at TIMESTAMP(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
    UNIQUE KEY uq_design_sheet_version (tech_card_id, version_number),
    UNIQUE KEY uq_design_sheet_version_request (client_request_id),
    CONSTRAINT fk_design_sheet_version_card FOREIGN KEY (tech_card_id) REFERENCES tech_card(id) ON DELETE CASCADE
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT 'Замороженный выпуск листа: акт человека, который печатают через год';

CREATE TABLE IF NOT EXISTS design_sheet_version_plate (
    id INT UNSIGNED PRIMARY KEY AUTO_INCREMENT,
    version_id INT UNSIGNED NOT NULL,
    ordinal SMALLINT UNSIGNED NOT NULL COMMENT 'порядок плит на бумаге',
    view_key VARCHAR(32) NOT NULL COMMENT 'сторона либо detail — копия адреса слота на момент минта',
    slot_id INT UNSIGNED NULL COMMENT 'адрес слота НА МОМЕНТ МИНТА; SET NULL при удалении слота — бумага остаётся читаемой',
    detail_name VARCHAR(120) NULL COMMENT 'КОПИЯ имени детали: удалённый слот не уносит имя с бумаги',
    media_id INT NOT NULL COMMENT 'байты выпуска; RESTRICT — их нельзя стереть, и медиатека это покажет',
    content_hash CHAR(64) NULL COMMENT 'копия media.content_hash НА МОМЕНТ МИНТА: здесь это содержание, а не выводимое. Пусто = медиа старше 0336',
    layer_rev INT NOT NULL DEFAULT 0 COMMENT 'ревизия слоя правки, из которой растеризована плита (0 = не из слоя)',
    source_class VARCHAR(16) NOT NULL COMMENT 'generated|uploaded|drawn|derived — провенанс плиты на бумаге; словарь растёт, CHECK намеренно нет',
    run_id INT UNSIGNED NULL COMMENT 'прогон-родитель плиты, если был. FK НЕТ намеренно: прогон архивируется и удаляется вместе с карточкой, а бумага обязана пережить это ссылкой-подсказкой',
    fit_stamp VARCHAR(50) NULL COMMENT 'посадка, штампованная на бумаге',
    mixed_input TINYINT(1) NOT NULL DEFAULT 0 COMMENT '1 = плита собрана из входов разного провенанса',
    UNIQUE KEY uq_design_sheet_plate (version_id, ordinal),
    KEY idx_design_sheet_plate_media (media_id),
    KEY idx_design_sheet_plate_slot (slot_id),
    CONSTRAINT fk_design_sheet_plate_version FOREIGN KEY (version_id) REFERENCES design_sheet_version(id) ON DELETE CASCADE,
    CONSTRAINT fk_design_sheet_plate_media FOREIGN KEY (media_id) REFERENCES media(id) ON DELETE RESTRICT,
    CONSTRAINT fk_design_sheet_plate_slot FOREIGN KEY (slot_id) REFERENCES design_bench_slot(id) ON DELETE SET NULL
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT 'Плита замороженного выпуска: строка, а не JSON, чтобы медиатека видела ссылку';

CREATE TABLE IF NOT EXISTS design_sheet_version_callout (
    id INT UNSIGNED PRIMARY KEY AUTO_INCREMENT,
    version_id INT UNSIGNED NOT NULL,
    number INT NOT NULL COMMENT 'номер выноски на бумаге; UNIQUE по (version_id, number) намеренно НЕТ — см. шапку',
    media_id INT NOT NULL COMMENT 'снимок, к которому выноска приколота; RESTRICT — байты выпуска не стираются',
    annotation JSON NULL COMMENT 'геометрия указания (TechCardAnnotation) на момент минта; NULL = выноска без геометрии',
    text TEXT NULL COMMENT 'замороженный текст выноски',
    KEY idx_design_sheet_callout_version (version_id, number),
    KEY idx_design_sheet_callout_media (media_id),
    CONSTRAINT fk_design_sheet_callout_version FOREIGN KEY (version_id) REFERENCES design_sheet_version(id) ON DELETE CASCADE,
    CONSTRAINT fk_design_sheet_callout_media FOREIGN KEY (media_id) REFERENCES media(id) ON DELETE RESTRICT
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT 'Замороженная выноска выпуска: номер, снимок, геометрия, текст';

CREATE TABLE IF NOT EXISTS design_sheet_issue (
    id INT UNSIGNED PRIMARY KEY AUTO_INCREMENT,
    version_id INT UNSIGNED NOT NULL,
    action VARCHAR(16) NOT NULL COMMENT 'minted|printed|shared; словарь растёт, CHECK намеренно нет',
    actor VARCHAR(255) NOT NULL DEFAULT '' COMMENT 'username из JWT',
    client_request_id CHAR(36) NULL COMMENT 'ключ идемпотентности записи журнала: повтор RecordDesignSheetIssue не удваивает строку. NULL законен (несколько NULL в UNIQUE не конфликтуют) — так пишется minted, рождающийся внутри транзакции минта',
    created_at TIMESTAMP(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
    UNIQUE KEY uq_design_sheet_issue_request (client_request_id),
    KEY idx_design_sheet_issue_version (version_id, id),
    CONSTRAINT fk_design_sheet_issue_version FOREIGN KEY (version_id) REFERENCES design_sheet_version(id) ON DELETE CASCADE
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT 'Журнал выпуска (append-only, как tech_card_revision): сминчено, напечатано, отдано';

-- +migrate Down

DROP TABLE IF EXISTS design_sheet_issue;
DROP TABLE IF EXISTS design_sheet_version_callout;
DROP TABLE IF EXISTS design_sheet_version_plate;
DROP TABLE IF EXISTS design_sheet_version;
