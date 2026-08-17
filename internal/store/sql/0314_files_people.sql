-- +migrate Up

-- ЛЮДИ У ФАЙЛА: КТО ЗАГРУЗИЛ И КТО ВЕДЁТ.
--
-- ДВЕ КОЛОНКИ АВТОРСТВА, А НЕ ОДНА, И ЭТО НЕ ДУБЛИРОВАНИЕ. `uploaded_by` (строка из JWT, 0312)
-- остаётся нетронутой: это ИСТОРИЧЕСКИЙ ФАКТ, который обязан пережить удаление аккаунта. Новая
-- `uploaded_by_id` — ЖИВАЯ ССЫЛКА на человека: ею резолвятся инициалы, специальности и круг «кому
-- можно менять владельцев». У них разный срок жизни, поэтому ON DELETE SET NULL здесь честен:
-- аккаунт удалили — ссылки больше нет, а надпись «загрузил pasha» остаётся навсегда. Схлопнуть их
-- в одну колонку значило бы выбрать между «файл теряет автора при увольнении» и «в admins нельзя
-- удалить строку».
--
-- ВЛАДЕЛЕЦ — ОТНОШЕНИЕ, ПОЭТОМУ CASCADE. Владение живёт ровно столько, сколько живёт аккаунт:
-- удалили человека — он больше никого не ведёт, и строка связи не значит ничего. Файл без
-- владельцев ЛЕГАЛЕН по тому же доводу, по которому легален файл без тем (0312): пустое поле
-- честнее, чем назначенный наугад ответственный.
--
-- ИДЕМПОТЕНТНОСТЬ (CLAUDE.md): MySQL 8 автокоммитит DDL, поэтому падение в середине файла
-- оставляет схему полуприменённой БЕЗ строки в gorp_migrations, и следующая загрузка перезапускает
-- файл с начала. ALTER идёт под гейтом information_schema, PREPARE / EXECUTE / DEALLOCATE — по
-- одному оператору на строку (multiStatements=true в контейнерных тестах иначе маскирует поломку,
-- которая на проде валит старт). Backfill отбирает `IS NULL`, поэтому повтор — no-op. Ретроактивных
-- CHECK нет ни одного: они проверяют ВСЮ историю и останавливают старт прода.

-- 1. Ссылка на аккаунт загрузившего. Гейт — существование колонки; ADD COLUMN и ADD CONSTRAINT
--    ОДНИМ оператором, чтобы половинчатого состояния «колонка есть, ключа нет» не возникало вовсе.
--    INT (signed) — admins.id объявлен в 0001 как `INT PRIMARY KEY AUTO_INCREMENT`, а MySQL 8
--    отвергает внешний ключ между колонками разной знаковости ошибкой 3780.
SET @lf_uploader := (SELECT COUNT(*) FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'library_file'
      AND COLUMN_NAME = 'uploaded_by_id');
SET @ddl := IF(@lf_uploader = 0,
    'ALTER TABLE library_file
        ADD COLUMN uploaded_by_id INT NULL COMMENT ''живая ссылка на аккаунт загрузившего; NULL = аккаунта больше нет (строка uploaded_by остаётся)'',
        ADD CONSTRAINT fk_library_file_uploader FOREIGN KEY (uploaded_by_id)
            REFERENCES admins (id) ON DELETE SET NULL',
    'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

-- 2. Backfill истории по имени. Имя аккаунта уникально (admins.username UNIQUE с 0001), поэтому
--    JOIN не размножает строк. Файлы, чей загрузивший уже уволен, остаются с NULL — это правильный
--    ответ, а не потеря: ссылки на несуществующий аккаунт и не должно быть.
--
--    CONVERT С ОБЕИХ СТОРОН — ЭТО НЕ ПЕРЕСТРАХОВКА, А ЕДИНСТВЕННОЕ, ЧТО ДЕРЖИТ СТАРТ ПРОДА.
--    `admins` (0001) объявлена БЕЗ charset-клаузы и наследует дефолт схемы на момент создания;
--    `library_file` (0312) объявлена явным `DEFAULT CHARSET = utf8mb4`. Встреча двух IMPLICIT-
--    коллаций ОДНОГО charset'а в одном сравнении — это не приведение, а жёсткая ошибка 1267
--    (довод и воспроизведение — в шапке 0220, где ровно это положило рассыльщик кампаний).
--    Замерено на MySQL 8.0 против симулированных схем прямо сейчас:
--      * utf8mb4_general_ci (admins) vs utf8mb4_0900_ai_ci (library_file) → 1267, файл падает;
--      * utf8mb3 (admins) vs utf8mb4 (library_file) → работает, MySQL расширяет узкий операнд.
--    Какой из двух случаев на проде, из репозитория НЕ ЧИТАЕТСЯ. Ставка на второй стоила бы
--    остановки старта (MYSQL_AUTOMIGRATE), поэтому обе стороны приводятся к одному charset'у
--    безусловно — тогда коллация у них общая при любом дефолте схемы. Проверено обоими шаблонами,
--    включая кириллические имена. Индекс по username при этом не используется, и это неважно:
--    в admins единицы строк.
--
--    updated_at ПРИСВАИВАЕТСЯ САМ СЕБЕ намеренно: колонка объявлена ON UPDATE CURRENT_TIMESTAMP,
--    и без этой строки backfill переписал бы «когда файл трогали» всей истории — миграция молча
--    изменила бы факт, о котором не заявляла. Явное присваивание отменяет авто-обновление.
UPDATE library_file lf
JOIN admins a ON CONVERT(a.username USING utf8mb4) = CONVERT(lf.uploaded_by USING utf8mb4)
SET lf.uploaded_by_id = a.id, lf.updated_at = lf.updated_at
WHERE lf.uploaded_by_id IS NULL;

-- 3. Владельцы файла. `added_by` — строка, а не ссылка, по тому же доводу, что и uploaded_by:
--    «кто назначил» — журнальный факт, он не обязан умирать вместе с аккаунтом назначившего.
CREATE TABLE IF NOT EXISTS library_file_owner (
  id INT PRIMARY KEY AUTO_INCREMENT,
  file_id INT NOT NULL,
  admin_id INT NOT NULL,
  added_by VARCHAR(255) NOT NULL DEFAULT '' COMMENT 'username назначившего, из JWT; журнальный факт, ссылкой намеренно не является',
  created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
  UNIQUE KEY uniq_library_file_owner (file_id, admin_id),
  INDEX idx_library_file_owner_admin (admin_id),
  CONSTRAINT fk_library_file_owner_file FOREIGN KEY (file_id)
    REFERENCES library_file (id) ON DELETE CASCADE,
  CONSTRAINT fk_library_file_owner_admin FOREIGN KEY (admin_id)
    REFERENCES admins (id) ON DELETE CASCADE
) ENGINE = InnoDB DEFAULT CHARSET = utf8mb4 COMMENT 'Владельцы файла библиотеки: кто ведёт файл (несколько на файл, ноль легален)';

-- +migrate Down
--
-- Порядок обратный Up, гейт симметричный. Строка uploaded_by не трогается ни в одну сторону —
-- Down её и не наполнял.
DROP TABLE IF EXISTS library_file_owner;

SET @lf_uploader_back := (SELECT COUNT(*) FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'library_file'
      AND COLUMN_NAME = 'uploaded_by_id');
SET @ddl := IF(@lf_uploader_back = 1,
    'ALTER TABLE library_file
        DROP FOREIGN KEY fk_library_file_uploader,
        DROP COLUMN uploaded_by_id',
    'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;
