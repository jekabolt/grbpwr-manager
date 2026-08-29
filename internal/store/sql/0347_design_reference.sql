-- Полоса DESIGN, кусок 7 из 7: роль референса — какой стороной изделия эта картинка мудборда
-- работает на входе генерации.
--
-- ПОЧЕМУ ЭТО НЕ КОЛОНКА НА `tech_card_media`. У строки медиа тех-карты НЕТ КЛЮЧА СТРОКИ вообще:
-- в схеме (`0067_add_tech_card_core.sql:98-107`) нет ни одного KEY/UNIQUE кроме PK и FK, а сама
-- таблица переписывается ЦЕЛИКОМ на каждом сейве карточки (`internal/store/techcard/techcard.go:
-- 1852-1871` — шесть колонок, `display_order` = индекс в payload). Перенести сохранённый атрибут
-- на пересланную строку просто НЕ НА ЧТО: позиционный матч — та самая опасность, из-за которой
-- всё keyed-хозяйство репо ключевое («a piece's pairing must never be inferred from a neighbour»,
-- `internal/apisrv/admin/costing_rbac.go:638-639`). Поэтому роль уезжает в полосу, где у неё есть
-- честный ключ — `(tech_card_id, media_id)`.
--
-- ПОЧЕМУ ЭТО ВТОРАЯ ОСЬ, А НЕ ЗНАЧЕНИЕ `kind`. В документе референс — это строка вида
-- `{media_id, kind, caption}`, где `kind` УЖЕ занят назначением (`moodboard|reference|swatch`).
-- Роль (`front|back|side_l|side_r|detail`) отвечает на другой вопрос — «какой стороне это
-- показывать модели», — и одновременное значение обеих осей законно. Схлопывание двух осей в один
-- словарь и есть ложное расщепление наоборот.
--
-- `role` — VARCHAR БЕЗ CHECK, по общему правилу волны: словари полосы будут расти, а поздний
-- ADD CHECK на потолстевшей таблице = COPY таблицы под пятиминутным потолком прогона.
--
-- `media_id` НЕСЁТ ГОЛЫЙ `KEY` И НЕ НЕСЁТ FK — И ЭТО НЕ ЗАБЫТО, А РЕШЕНО (2026-08-30).
--
-- Роль это ПОДСКАЗКА модели и порядок в промпте, а не владение: стёртая картинка не должна ни
-- делать себя неудаляемой, ни числиться занятой. Владеющих ссылок в волне ровно три
-- (`design_picture.media_id`, `design_sheet_version_plate.media_id`,
-- `design_sheet_version_callout.media_id`), и только они регистрируются в `mediaRefRegistry`.
--
-- Почему не FK с CASCADE (как было в первом наброске этого файла). `TestMediaUsageRegistryCoversSchema`
-- (`internal/store/media_usage_integration_test.go:148`) диффит реестр против ВСЕХ живых FK в
-- `media(id)` и НЕ различает «держит» от «упоминает». Значит FK здесь оставляет ровно два выхода, и
-- оба хуже голого KEY:
--   * зарегистрировать колонку — тогда `DeleteMediaByIdIfUnused` начнёт ОТКАЗЫВАТЬ в удалении
--     мудбордной картинки, у которой всего лишь проставлена роль. Необязательная подсказка
--     превратилась бы в замок, а медиатека говорила бы «занято» про файл, который удаляется
--     без последствий;
--   * учить реестр ярусу «упоминает» — отдельная работа в общем коде ради одной колонки.
--
-- Цена голого KEY названа честно: удаление медиа оставляет строку роли, указывающую в никуда.
-- Она невидима (чтение полосы джойнит `media` и такую строку не выдаёт) и безвредна. Прецедент —
-- в этой же волне: `design_picture.derived_from` (0340) и `design_edit_layer.base_media_id` (0343)
-- сделаны так же и по тому же доводу.

-- +migrate Up

CREATE TABLE IF NOT EXISTS design_reference (
    id INT UNSIGNED PRIMARY KEY AUTO_INCREMENT,
    tech_card_id INT NOT NULL,
    media_id INT NOT NULL,
    role VARCHAR(16) NOT NULL COMMENT 'front|back|side_l|side_r|detail — вторая ось, kind документа занят назначением',
    ordinal SMALLINT UNSIGNED NOT NULL DEFAULT 0 COMMENT 'порядок референсов в промпте',
    set_by VARCHAR(255) NOT NULL,
    set_at DATETIME(6) NOT NULL,
    UNIQUE KEY uq_design_reference (tech_card_id, media_id),
    -- Голый KEY, без FK — довод в шапке файла. Индекс всё равно нужен: в UNIQUE выше `media_id`
    -- не левая колонка, а чтение полосы джойнит `media` именно по ней.
    KEY idx_design_reference_media (media_id),
    CONSTRAINT fk_design_reference_card FOREIGN KEY (tech_card_id) REFERENCES tech_card(id) ON DELETE CASCADE
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT 'Роль референса мудборда: какой стороне изделия картинка отвечает на входе генерации';

-- +migrate Down

DROP TABLE IF EXISTS design_reference;
