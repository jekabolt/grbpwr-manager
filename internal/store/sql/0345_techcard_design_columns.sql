-- Полоса DESIGN, кусок 6 из 7: три аддитивные колонки на ЖИВЫХ таблицах документа.
--
-- ЧТО И ЗАЧЕМ:
--   * `tech_card.mood_note` — общее поле мудборда: слова, которые человек пишет ко всей доске
--     референсов, а не к отдельной картинке. Живёт по verbatim-протоколу «absent = сохранить»
--     (образец — `cutting_coefficient` в techcard.proto), поэтому вкладка со старым бандлом его
--     не сотрёт;
--   * `tech_card.callout_seq` — монотонный источник номера выноски. Сегодня номер минтит клиент
--     максимумом по своему экрану, и два экрана дают один номер; счётчик на карточке двигает
--     хендлер в том же UPDATE, который бампает `lock_version`, поэтому взаимное исключение уже
--     стоит: два сейва не сминтят один номер;
--   * `tech_card_callout.client_ref` — ключ СТРОКИ выноски, который минтит клиент (UUID) при её
--     рождении. Без него после сейва форма не понимает, какой её строке достался какой серверный
--     номер, и фокус, подсветка и «отказ ведёт в место» показывают ЧУЖУЮ выноску: сегодня у
--     выноски нет ни одного клиентского ключа, а единственная идентичность — номер, который не
--     уникален (`0067:113`, UNIQUE нет и не будет). Индекс намеренно не заводится: сопоставление
--     идёт в памяти по payload, а не запросом.
--
-- ВСЁ ТРИ — nullable либо с DEFAULT, то есть ADD COLUMN уровня INSTANT: ни одной копии таблицы,
-- пятиминутный потолок всего прогона миграций не задевается. Ретроактивных CHECK и UNIQUE в этом
-- файле НЕТ и быть не может: UNIQUE на `tech_card_callout.callout_number` уронил бы старт на
-- легаси-нулях (`0067:113`), а ретроактивный CHECK проверяет ВСЮ историю.
--
-- РАСШИРЕНИЕ СЛОВАРЯ `kind` ЗДЕСЬ НЕ ЖИВЁТ. Оно вынесено в `0346_tech_card_media_kind_dict.sql`
-- отдельным файлом, потому что это единственный COPY волны, а замер размера `tech_card_media`
-- снять не удалось. Этот файл в тихом окне не нуждается.
--
-- ИДЕМПОТЕНТНОСТЬ: MySQL не знает `ADD COLUMN IF NOT EXISTS`, поэтому каждый ALTER охраняется
-- по `information_schema.COLUMNS`. `PREPARE`/`EXECUTE`/`DEALLOCATE` — КАЖДЫЙ СВОЕЙ СТРОКОЙ: прод
-- ходит без `multiStatements`, драйвер выполняет по одному запросу, и склеенные в одну строку
-- операторы падают ТОЛЬКО на проде.
--
-- ОТКАТ БИНАРЯ ПОСЛЕ ЭТОЙ МИГРАЦИИ БЕЗОПАСЕН: старый стор эти колонки не именует ни в INSERT, ни
-- в UPDATE, а `tech_card_media` файл не трогает вовсе.

-- +migrate Up

SET @tc_mood_note := (SELECT COUNT(*) FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'tech_card'
      AND COLUMN_NAME = 'mood_note');
SET @ddl := IF(@tc_mood_note = 0,
    'ALTER TABLE tech_card
        ADD COLUMN mood_note TEXT NULL COMMENT ''общее поле мудборда; verbatim-протокол: отсутствие поля в payload = сохранить прежнее''',
    'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

SET @tc_callout_seq := (SELECT COUNT(*) FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'tech_card'
      AND COLUMN_NAME = 'callout_seq');
SET @ddl := IF(@tc_callout_seq = 0,
    'ALTER TABLE tech_card
        ADD COLUMN callout_seq INT NOT NULL DEFAULT 0 COMMENT ''монотонный источник номера выноски; двигается тем же UPDATE, что бампает lock_version''',
    'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

SET @tcc_client_ref := (SELECT COUNT(*) FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'tech_card_callout'
      AND COLUMN_NAME = 'client_ref');
SET @ddl := IF(@tcc_client_ref = 0,
    'ALTER TABLE tech_card_callout
        ADD COLUMN client_ref VARCHAR(64) NULL COMMENT ''ключ строки выноски, минтит клиент (UUID); number=0 И client_ref<>NULL = «сминти номер», number=0 и пусто = легаси-ноль, не трогать''',
    'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

-- +migrate Down

SET @tcc_client_ref_down := (SELECT COUNT(*) FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'tech_card_callout'
      AND COLUMN_NAME = 'client_ref');
SET @ddl_down := IF(@tcc_client_ref_down = 1,
    'ALTER TABLE tech_card_callout DROP COLUMN client_ref',
    'SELECT 1');
PREPARE stmt FROM @ddl_down;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

SET @tc_callout_seq_down := (SELECT COUNT(*) FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'tech_card'
      AND COLUMN_NAME = 'callout_seq');
SET @ddl_down := IF(@tc_callout_seq_down = 1,
    'ALTER TABLE tech_card DROP COLUMN callout_seq',
    'SELECT 1');
PREPARE stmt FROM @ddl_down;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

SET @tc_mood_note_down := (SELECT COUNT(*) FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'tech_card'
      AND COLUMN_NAME = 'mood_note');
SET @ddl_down := IF(@tc_mood_note_down = 1,
    'ALTER TABLE tech_card DROP COLUMN mood_note',
    'SELECT 1');
PREPARE stmt FROM @ddl_down;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;
