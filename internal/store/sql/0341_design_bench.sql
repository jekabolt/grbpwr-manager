-- Полоса DESIGN, кусок 2 из 7: верстак — какая картинка стоит какой стороной изделия.
--
-- ЭКРАННЫЙ ФАКТ. Студия показывает четыре стороны (front, back, side_l, side_r) и произвольное
-- число слотов деталей. Слот — это адрес, по которому лежит ПРИНЯТАЯ плита; из слотов собирается
-- версия листа. Стороны рождаются ЛЕНИВО, первым касанием: пустая карточка не заводит четыре
-- пустые строки заранее.
--
-- ЛЕНИВОЕ РОЖДЕНИЕ ДЕЛАЕТСЯ ОДНИМ UPSERT-ОМ, А НЕ «SELECT → нет строки → INSERT». Двое,
-- одновременно кладущие front, оба увидят «строки нет», оба вставят, и второй получит 1062 —
-- ошибку, которой нет в таксономии отказов и которую клиент не откатит (он ждёт
-- `Aborted: slot_rev_mismatch`). Прецедент дословный: вторая примерка на семпл падала 1062 ровно
-- так же. Форма записи — `INSERT … ON DUPLICATE KEY UPDATE` с CAS по `slot_rev` внутри IF(),
-- затем перечитать строку в той же транзакции и, если `slot_rev` не вырос, вернуть
-- `slot_rev_mismatch` с текущим состоянием слота. Остаточный 1062 всё равно мапится в тот же
-- отказ — это пояс, а не механизм.
--
-- ПОЧЕМУ ДВА КЛЮЧА, А НЕ ОДИН.
--   * `uq_design_bench_view (tech_card_id, exclusive_key)` — «ровно один слот на адрес». Для
--     четырёх сторон `exclusive_key` = `view_key`; для детали он несёт собственный ключ слота,
--     потому что деталей на карточке много и `view_key='detail'` их не различает. Имя «деталь 1/2»
--     ключом быть не может: переименование детали не должно двигать слот.
--   * `uq_design_bench_picture (tech_card_id, picture_id)` — «одна плита максимум в одном слоте».
--     Без него две транзакции, ставящие ОДНУ картинку в РАЗНЫЕ слоты, трогают разные строки и обе
--     законны, а состав версии получает одну физическую плиту под двумя видами. Несколько пустых
--     слотов ключу не мешают: в MySQL несколько NULL в UNIQUE законны.
--
-- ЧЕГО СХЕМА ВЫРАЗИТЬ НЕ МОЖЕТ и что поэтому проверяет Go в той же транзакции:
-- `picture.tech_card_id = slot.tech_card_id`. Композитный FK
-- `(tech_card_id, picture_id) → design_picture(tech_card_id, id)` это выразил бы, но его ON DELETE
-- пришлось бы делать CASCADE (обе колонки NOT NULL), а слот детали ОБЯЗАН пережить исчезновение
-- своей плиты. Отказ на чужую карточку называется `foreign_card_plate`.
--
-- FK: `picture_id → design_picture ON DELETE SET NULL` — слот переживает исчезновение плиты, и обе
-- таблицы каскадятся от `tech_card`, поэтому RESTRICT здесь дал бы 1451 в единственной операции
-- удаления карточки (см. шапку 0340). Отдельный `KEY idx_design_bench_slot_picture` нужен потому,
-- что в `uq_design_bench_picture` колонка `picture_id` НЕ левая, а FK требует индекс со своей
-- колонкой слева.

-- +migrate Up

CREATE TABLE IF NOT EXISTS design_bench_slot (
    id INT UNSIGNED PRIMARY KEY AUTO_INCREMENT COMMENT 'адрес слота; «деталь 1/2» как ключ запрещён — переименование не должно двигать слот',
    tech_card_id INT NOT NULL,
    view_key VARCHAR(32) NOT NULL COMMENT 'front|back|side_l|side_r|detail — что за сторона; словарь растёт, CHECK намеренно нет',
    exclusive_key VARCHAR(64) NOT NULL COMMENT 'что ровно одно на карточке: для стороны = view_key, для детали = собственный ключ слота',
    detail_name VARCHAR(120) NULL COMMENT 'имя детали; NULL для четырёх сторон. Копируется в плиту версии — удалённый слот не уносит имя с бумаги',
    picture_id INT UNSIGNED NULL COMMENT 'NULL = слот пуст (в том числе после исчезновения плиты)',
    slot_rev INT NOT NULL DEFAULT 0 COMMENT 'CAS-ревизия слота: SERIALIZABLE закрывает гонку записи, но не «А. смотрел на старый экран»',
    set_by VARCHAR(255) NOT NULL DEFAULT '' COMMENT 'username из JWT, поставивший плиту',
    set_at DATETIME(6) NULL,
    UNIQUE KEY uq_design_bench_view (tech_card_id, exclusive_key),
    UNIQUE KEY uq_design_bench_picture (tech_card_id, picture_id),
    KEY idx_design_bench_slot_picture (picture_id),
    CONSTRAINT fk_design_bench_slot_card FOREIGN KEY (tech_card_id) REFERENCES tech_card(id) ON DELETE CASCADE,
    CONSTRAINT fk_design_bench_slot_picture FOREIGN KEY (picture_id) REFERENCES design_picture(id) ON DELETE SET NULL
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT 'Верстак дизайн-бэнда: какая плита принята какой стороной изделия';

-- +migrate Down

DROP TABLE IF EXISTS design_bench_slot;
