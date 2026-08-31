-- ПОЛКИ АССЕТОВ ТЕХ-КАРТЫ: ткани, паттерны, фурнитура — и их разметка на флэтах.
--
-- Владелец (круг 5, V-11), дословно: «как-то надо хранить отдельно набор асетов тканей паттернов
-- сгенеренных и фурнитуры внутни одной тех карты». Форму выбрал он же прямым ответом: «Своя
-- секция ASSETS в студии» — три полки, из которых берут и фабрик-рендер, и разметка на флэтах.
--
-- ОДНА ТАБЛИЦА С `kind`, А НЕ ТРИ, И ЭТО ГЛАВНОЕ РЕШЕНИЕ ФАЙЛА.
--
-- Проверка на ЛОЖНОЕ РАСЩЕПЛЕНИЕ СЛОВАРЯ (репозиторий ловил его дважды): два члена — это один
-- словарь, когда единственная разница между ними в том, какой сосед не заполнен. Ткань, паттерн и
-- фурнитура делят ВСЕ операции без исключения: их заводят одной дверью, называют одним именем,
-- держат одним медиа, кладут на флэт одной разметкой, удаляют одним глаголом и цитируют одной
-- строкой размещения. У паттерна заполнены `repeat_mm` и `derived_from_asset_id`, у прочих нет —
-- это ровно «заданный и незаданный сосед», то есть признак ОДНОГО словаря, а не двух.
--
-- Три таблицы стоили бы полиморфного размещения: три nullable FK в одной строке либо тег рядом с
-- идентификатором. Обе формы — это способ записать «ровно одно из трёх заполнено» так, чтобы схема
-- этого не проверяла.
--
-- Обратная проверка — ВЕДРО ПРОТИВ ЛИЧНОСТИ (один ключ под двумя смыслами): не прячется ли в
-- `kind` вторая ось? Не прячется. `kind` говорит, ЧЕМ АССЕТ ЯВЛЯЕТСЯ, и никогда — как он получен:
-- происхождение паттерна это `derived_from_asset_id`, отдельное ребро. Поэтому паттерн, нарисованный
-- моделью, и паттерн, разложенный из загруженного лоскута, — ОДИН род с разной родословной, а у
-- одной ткани законно бывает несколько паттернов с разным раппортом. «Ткань с полем раппорта» этого
-- не выражала бы вовсе, и полка ПАТТЕРНЫ, которую владелец назвал отдельно, осталась бы пустой.
--
-- `kind` — VARCHAR БЕЗ CHECK, по общему правилу полосы: словари растут, а поздний
-- `ADD CONSTRAINT ... CHECK` на потолстевшей таблице это COPY всей таблицы, у которой захардкожен
-- пятиминутный потолок прогона миграций — то есть остановленный старт прода. Проверяет словарь Go
-- (entity.IsDesignAssetKind), и отказ называет значение, а не отдаёт сырой 3819.
--
-- `media_id` НЕСЁТ FK RESTRICT И РЕГИСТРИРУЕТСЯ В `mediaRefRegistry`, и это ПРОТИВОПОЛОЖНО решению
-- 0347 по `design_reference.media_id` — сознательно. Там медиа было ПОДСКАЗКОЙ (роль референса в
-- промпте), здесь оно и есть сам ассет: текстура ткани, плитка паттерна, снимок фурнитуры. Удалить
-- файл, на котором держится ткань изделия, значит оставить полку с пустой рамкой и промпт без
-- материала. RESTRICT без регистрации был бы худшим из двух исходов (GetMediaUsage назвал бы файл
-- свободным, человек нажал бы delete и получил ошибку внешнего ключа с именем таблицы, о которой не
-- слышал), поэтому строка реестра приезжает этой же волной, и `TestMediaUsageRegistryCoversSchema`
-- держит связь.
--
-- `derived_from_asset_id` — SET NULL, А НЕ RESTRICT И НЕ CASCADE. Паттерн переживает удаление своей
-- ткани: у него есть своя картинка и свой раппорт, то есть законченное указание фабрике. CASCADE
-- унёс бы работу, RESTRICT сделал бы ткань неудаляемой из-за производной, которая в ней уже не
-- нуждается.
--
-- РАЗМЕЩЕНИЕ ВИСИТ НА КАРТИНКЕ, А НЕ НА СЛОТЕ ВЕРСТАКА. Координаты — доли КАДРА; перевесив их на
-- ту плиту, которая завтра встанет во фронт, мы указали бы на пиксели, где никто ничего не рисовал.
-- ON DELETE CASCADE с обеих сторон: без картинки геометрия бессмысленна, без ассета — безымянна.
--
-- В `design_asset_placement` НАМЕРЕННО НЕТ `tech_card_id`. Он выводится через `design_asset`, и
-- второй дом для одного факта разошёлся бы с первым при первом же переносе. Чтение полосы джойнит
-- ассет; запись проверяет в Go, что картинка и ассет принадлежат ОДНОЙ карточке.

-- +migrate Up

CREATE TABLE IF NOT EXISTS design_asset (
    id INT UNSIGNED PRIMARY KEY AUTO_INCREMENT,
    tech_card_id INT NOT NULL,
    kind VARCHAR(16) NOT NULL COMMENT 'fabric|pattern|hardware — ЧЕМ ассет является; происхождение это derived_from_asset_id',
    name VARCHAR(60) NOT NULL COMMENT 'обязательно: промпт и лист цитируют ассет именем',
    media_id INT NULL COMMENT 'текстура/плитка/снимок; NULL = ассет назван словами и цветом',
    colour_code VARCHAR(32) NULL,
    colour_hex VARCHAR(9) NULL COMMENT '#RRGGBB, экранное приближение',
    note VARCHAR(500) NULL,
    derived_from_asset_id INT UNSIGNED NULL COMMENT 'ткань, из которой сделан паттерн (V-7)',
    repeat_mm SMALLINT UNSIGNED NOT NULL DEFAULT 0 COMMENT 'раппорт паттерна в целых мм на готовом изделии; 0 = гладкая ткань',
    rotation_deg SMALLINT UNSIGNED NOT NULL DEFAULT 0 COMMENT 'поворот паттерна, градусы по часовой, 0..359',
    ordinal SMALLINT UNSIGNED NOT NULL DEFAULT 0 COMMENT 'порядок на своей полке',
    created_by VARCHAR(255) NOT NULL,
    created_at DATETIME(6) NOT NULL,
    updated_at DATETIME(6) NOT NULL,
    KEY idx_design_asset_card (tech_card_id, kind, ordinal, id),
    KEY idx_design_asset_media (media_id),
    KEY idx_design_asset_parent (derived_from_asset_id),
    CONSTRAINT fk_design_asset_card FOREIGN KEY (tech_card_id) REFERENCES tech_card(id) ON DELETE CASCADE,
    CONSTRAINT fk_design_asset_media FOREIGN KEY (media_id) REFERENCES media(id) ON DELETE RESTRICT,
    CONSTRAINT fk_design_asset_parent FOREIGN KEY (derived_from_asset_id) REFERENCES design_asset(id) ON DELETE SET NULL
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT 'Полки ассетов тех-карты: ткани, паттерны, фурнитура (V-11)';

CREATE TABLE IF NOT EXISTS design_asset_placement (
    id INT UNSIGNED PRIMARY KEY AUTO_INCREMENT,
    asset_id INT UNSIGNED NOT NULL,
    picture_id INT UNSIGNED NOT NULL COMMENT 'флэт, на котором стоит метка; координаты — доли ЭТОГО кадра',
    annotation JSON NOT NULL COMMENT 'та же TechCardAnnotation, что рисует вся система: вид, якоря, плашка, цвет',
    note VARCHAR(500) NULL,
    set_by VARCHAR(255) NOT NULL,
    set_at DATETIME(6) NOT NULL,
    KEY idx_design_placement_asset (asset_id),
    KEY idx_design_placement_picture (picture_id),
    CONSTRAINT fk_design_placement_asset FOREIGN KEY (asset_id) REFERENCES design_asset(id) ON DELETE CASCADE,
    CONSTRAINT fk_design_placement_picture FOREIGN KEY (picture_id) REFERENCES design_picture(id) ON DELETE CASCADE
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT 'Метка ассета на флэте: этот ассет — вот здесь (V-6/V-7/V-8)';

-- +migrate Down

-- ДЕТИ РАНЬШЕ РОДИТЕЛЕЙ: размещение держит внешний ключ на ассет, поэтому уходит первым.
DROP TABLE IF EXISTS design_asset_placement;
DROP TABLE IF EXISTS design_asset;
