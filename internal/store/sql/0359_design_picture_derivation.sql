-- Полоса DESIGN, круг 15 (J-1/J-23): ЧЕМ КАДР ПРОИЗВЕДЁН ОТ РОДИТЕЛЯ — РАЗРЕЗОМ ИЛИ ПРАВКОЙ.
--
-- ЧТО ПРОВОД НЕ МОГ ОТВЕТИТЬ ДО ЭТОЙ КОЛОНКИ. `derived_from` пишут ДВА РАЗНЫХ ГЛАГОЛА:
-- SplitPicture (разрез композита) и FlattenEditLayer (сплющенная правка). Различить их на проводе
-- было НЕЧЕМ:
--
--   * кроп НАСЛЕДУЕТ у родителя и `source_class`, и `mixed_input`, и `layer_rev`
--     (internal/store/design/pictures.go, INSERT кропа) — то есть кроп ОТРЕДАКТИРОВАННОГО листа
--     приходит с `layer_rev > 0` ровно так же, как приходит флэттен;
--   * флэттен пишет `layer_rev = layer.Rev` (internal/store/design/layer.go), и две правки одного
--     кадра («правь → сохрани → правь снова») дают два флэттена, чьи ревизии могут совпасть.
--
-- Отсюда следует то, что и было дефектом: обещание контракта «`layer_rev` … 0 = not flattened»
-- (common/design.proto, DesignPicture.layer_rev) УЖЕ ЛОЖНО — его ломает наследование выше. Клиент,
-- складывающий ленту в колоду «после сплита», по `layer_rev` угадать не мог, а основной поток
-- владельца — «отредактировать мультивью, ПОТОМ его разрезать» — бьёт ровно по этой догадке.
--
-- СЛОВАРЬ: '' | crop | flatten. VARCHAR БЕЗ CHECK, по общему правилу волны и по прямому запрету
-- круга: ретроактивный ADD CONSTRAINT CHECK копирует таблицу ЦЕЛИКОМ и упирается в пятиминутный
-- потолок прогона миграций в store.New. Словарь держат Go-писатели, а не схема.
--
-- ПУСТАЯ СТРОКА ЧИТАЕТСЯ ТОЛЬКО В ПАРЕ С `derived_from`, и это ДВА РАЗНЫХ «ничего»:
--   * derived_from IS NULL и derivation = '' — КОРЕНЬ: кадр ни от чего не произведён (выход
--     прогона, загруженный файл, флэттен слоя, нарисованного с чистого листа);
--   * derived_from IS NOT NULL и derivation = '' — строка, которую бэкфилл НЕ СМОГ
--     классифицировать (родителя уже нет: `derived_from` объявлен голым KEY без FK, 0340).
-- Читатель, схлопнувший эти два в одно, назовёт осиротевший кроп корнем — что и есть та самая
-- ошибка, ради которой колонка заводится.
--
-- ⚠ БЭКФИЛЛ НИЖЕ — ЭТО ЭВРИСТИКА, А НЕ ИСТИНА, и разница названа здесь, потому что шапка
-- переживает код. Для строк, записанных ДО этой миграции, глагол не сохранён нигде; единственный
-- различающий след — совпадение `layer_rev` с родительским (кроп КОПИРУЕТ, флэттен ПИШЕТ СВОЮ).
-- Эвристика ошибается ровно там, где ревизии совпали случайно: правка кадра, чей слой стоял на той
-- же ревизии, что и родитель, будет названа кропом. С этой миграции и далее ИСТИНА — это то, что
-- пишут сами глаголы (pictures.go: 'crop', layer.go: 'flatten'), и она не выводится ниоткуда.
--
-- ЦЕНА. ADD COLUMN … NOT NULL DEFAULT '' в КОНЕЦ таблицы — INSTANT (MySQL 8.0.12+): словарь
-- меняется, строки не переписываются. Дальнейший UPDATE трогает ТОЛЬКО производные строки
-- (`derived_from IS NOT NULL`) и идёт по существующему idx_design_picture_derived_from (0340);
-- на карточке производных кадров единицы, на базе — сотни, а не миллионы.

-- +migrate Up

SET @dp_der := (SELECT COUNT(*) FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'design_picture'
      AND COLUMN_NAME = 'derivation');
SET @ddl := IF(@dp_der = 0,
    'ALTER TABLE design_picture
        ADD COLUMN derivation VARCHAR(16) NOT NULL DEFAULT ''''
            COMMENT ''crop|flatten — КАКИМ ГЛАГОЛОМ кадр произведён от derived_from. Пустое читается ПАРОЙ с derived_from: NULL родитель = корень, непустой родитель = легаси, неклассифицированное бэкфиллом. layer_rev дискриминатором НЕ является — кроп его наследует''',
    'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

-- БЭКФИЛЛ ОДНИМ МНОГОТАБЛИЧНЫМ UPDATE, А НЕ ПОДЗАПРОСОМ. MySQL запрещает читать в подзапросе ту
-- же таблицу, которую этот UPDATE меняет (1093), поэтому самосоединение здесь не стиль, а
-- единственная форма, в которой запрос вообще исполняется.
--
-- JOIN, А НЕ LEFT JOIN: строка, чей родитель исчез, остаётся с '' — см. шапку про два «ничего».
-- Назвать её кропом значило бы вписать в летопись догадку там, где не осталось даже следа для
-- эвристики.
--
-- `derivation = ''` в условии делает бэкфилл ПОВТОРЯЕМЫМ и, что важнее, не даёт ему ПЕРЕПИСАТЬ
-- правду, записанную новыми глаголами, если миграция почему-то прогонится второй раз.
SET @dp_der_fill := (SELECT COUNT(*) FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'design_picture'
      AND COLUMN_NAME = 'derivation');
SET @dml := IF(@dp_der_fill = 1,
    'UPDATE design_picture c JOIN design_picture p ON p.id = c.derived_from
        SET c.derivation = IF(c.layer_rev <> p.layer_rev, ''flatten'', ''crop'')
        WHERE c.derived_from IS NOT NULL AND c.derivation = ''''',
    'SELECT 1');
PREPARE stmt FROM @dml;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

-- +migrate Down
--
-- ОТКАТ ТЕРЯЕТ ГЛАГОЛ, И ЭТО ОСОЗНАННО: восстановить его после DROP COLUMN нечем — эвристика выше
-- на новых строках соврала бы точно так же, как на старых. Прежний бинарь читает ленту по
-- `layer_rev`, то есть возвращается к тому самому дефекту, который колонка закрывает.

SET @dp_der_down := (SELECT COUNT(*) FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'design_picture'
      AND COLUMN_NAME = 'derivation');
SET @ddl := IF(@dp_der_down = 1,
    'ALTER TABLE design_picture DROP COLUMN derivation',
    'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;
