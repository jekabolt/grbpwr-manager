-- Полоса DESIGN, шаг PATTERN (STEP 3): ТКАНЬ ПАРЫ (КОЛОРВЕЙ, СЛОТ).
--
-- МОДЕЛЬ ВЛАДЕЛЬЦА. Экран паттерна сгруппирован по колорвеям карточки, внутри колорвея — строка на
-- каждый слот ткани (строки BOM рулонных секций: fabric / lining / interlining / insulation —
-- «white → outer, inner»). Для каждой пары человек делает свотч и ОТМЕЧАЕТ его как ткань этой пары,
-- и фабрик-рендер берёт её сам. Ответ на вопрос «что носит white снаружи» — это одна строка здесь.
--
-- ПОЧЕМУ СТРОКА, А НЕ КОЛОНКА НА design_asset (D1). Выбор принадлежит ПАРЕ, а не плитке: одна и та
-- же ткань законно бывает верхом двух колорвеев или верхом и подкладкой одного. Колонка «слот» на
-- ассете называла бы ОДИН слот на плитку, потребовала бы снять uq_design_asset_colorway, развести
-- по слотам обе кражи (SetAssetColorway и посадку плитки) — и всё равно не дала бы одной плитке
-- служить двум слотам. С парой в ключе повторный выбор — один upsert
-- (INSERT … ON DUPLICATE KEY UPDATE asset_id), а не охота за прежним носителем.
--
-- ЧТО НЕ ТРОГАЕТСЯ. design_asset.colorway_id (0357), uq_design_asset_colorway, обе кражи и глагол
-- SetDesignAssetColorway остаются как есть: это legacy «ткань всего колорвея», и связка её не
-- переписывает и не читает. Новые читатели к той колонке не откатываются (ревью, S3).
--
-- СЛОТ АДРЕСУЕТСЯ bom_item_id, А НЕ bom_line_key. Ключ строки у легаси-строк бывает NULL
-- (uniq_bom_line_key его допускает), а id строки BOM стабилен с 0159 (сверка по line_key сохраняет
-- id при правке) — и именно на него уже разрешается рецепт колорвея. Снимок строкой разошёлся бы с
-- живой строкой при первом переименовании.
--
-- ЧЕТЫРЕ КОНЦА, И КАЖДЫЙ — ON DELETE CASCADE. Пара без любой из половин ни о чём не говорит:
--   * tech_card — карточка уносит всю свою полосу;
--   * product (колорвей) — удалили цвет, его ткани больше некому носить. ⚠ ПЯТАЯ ССЫЛКА ПОЛОСЫ НА
--     product(id): она зарегистрирована в designColorwayHolders (store/product/relink.go), считается
--     вердиктом удаления (readColorwayDeletionFacts) и называется оператору строкой каскада — урок
--     0356/0357 («колонку и вердикт пишут одним движением»). Сетка 1451 её не увидит: CASCADE она
--     не ловит. TestDesignDBRelinkGuardCoversEveryColorwayHolder сверяет список со схемой;
--   * tech_card_bom_item (слот) — строку BOM удалили, слота больше нет, и это правда о паре;
--   * design_asset — плитку удалили с полки, пара остаётся без ткани (DeleteAsset считает такие
--     пары до удаления, пока их ещё есть кому считать).
--
-- ПИСАТЕЛЕЙ ДВА: глагол SetDesignAssetBinding (выбор человека; asset_id 0 снимает) и посадка
-- плитки прогона паттерна, сделанного ДЛЯ пары (params.pattern.bom_item_id вместе с
-- run.colorway_id), в той же транзакции, что закрывает прогон. Принадлежность всех трёх id одной
-- карточке схема выразить не может (tech_card_id у строки BOM и у колорвея — разные колонки разных
-- таблиц), поэтому её проверяет Go в пишущей транзакции.
--
-- ОДИН ОПЕРАТОР CREATE TABLE IF NOT EXISTS — идемпотентен сам по себе: повтор файла после сбоя
-- (DDL коммитится сам, строки gorp_migrations нет) ничего не делает. Таблица новая и пустая, так
-- что четыре FK валидировать нечем. Никаких CHECK — общее правило полосы.

-- +migrate Up

CREATE TABLE IF NOT EXISTS design_asset_binding (
    id INT AUTO_INCREMENT PRIMARY KEY,
    tech_card_id INT NOT NULL COMMENT 'карточка пары; все три id ниже — её',
    colorway_id INT NOT NULL COMMENT 'FK product(id): колорвей пары',
    bom_item_id INT NOT NULL COMMENT 'FK tech_card_bom_item(id): слот — рулонная строка BOM',
    asset_id INT UNSIGNED NOT NULL COMMENT 'FK design_asset(id), kind fabric|pattern: ткань пары',
    set_by VARCHAR(255) NOT NULL DEFAULT '' COMMENT 'кто выбрал (автор прогона при посадке)',
    set_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    UNIQUE KEY uq_design_asset_binding (colorway_id, bom_item_id),
    KEY idx_design_asset_binding_card (tech_card_id),
    KEY idx_design_asset_binding_bom_item (bom_item_id),
    KEY idx_design_asset_binding_asset (asset_id),
    CONSTRAINT fk_design_asset_binding_card FOREIGN KEY (tech_card_id) REFERENCES tech_card(id) ON DELETE CASCADE,
    CONSTRAINT fk_design_asset_binding_colorway FOREIGN KEY (colorway_id) REFERENCES product(id) ON DELETE CASCADE,
    CONSTRAINT fk_design_asset_binding_bom_item FOREIGN KEY (bom_item_id) REFERENCES tech_card_bom_item(id) ON DELETE CASCADE,
    CONSTRAINT fk_design_asset_binding_asset FOREIGN KEY (asset_id) REFERENCES design_asset(id) ON DELETE CASCADE
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT 'Ткань пары (колорвей, слот BOM): одна строка на пару';

-- +migrate Down

-- Выборы теряются вместе со своим смыслом: без таблицы «ткань слота» не выражается. САМИ АССЕТЫ
-- НЕ УДАЛЯЮТСЯ — плитки остаются на полке, а legacy design_asset.colorway_id связка не трогала.
DROP TABLE IF EXISTS design_asset_binding;
