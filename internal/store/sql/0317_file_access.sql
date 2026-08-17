-- +migrate Up

-- ДОСТУП К ФАЙЛУ: ТРИ УРОВНЯ, ЛЮДИ, ПУБЛИЧНАЯ ССЫЛКА, ЖУРНАЛ.
--
-- ЭТА МИГРАЦИЯ ТОЛЬКО ЗАВОДИТ МЕСТО, ГДЕ ПРАВИЛО БУДЕТ ЖИТЬ. Пока её не читает ни один запрос,
-- она не закрывает ничего: до предиката видимости (Ф7, стор) все файлы по-прежнему видны всем, у
-- кого есть files:read. Это сознательно — миграция аддитивная и уезжает на бету раньше своей
-- фазы, а закрывающий код приходит следом.
--
-- ПОЧЕМУ ENUM, А НЕ VARCHAR. Уровней ровно три, и они не «пока три»: `team` (вся команда),
-- `people` (перечисленные люди) и `link` (плюс кто угодно со ссылкой). ENUM закрывает множество
-- ПО ПОСТРОЕНИЮ — опечатка `'peoples'` не запишется никогда, ни из кода, ни из консоли. Тот же
-- эффект дал бы CHECK, но CHECK здесь пришлось бы добавлять к УЖЕ СУЩЕСТВУЮЩЕЙ таблице, а
-- ретроактивный ADD CONSTRAINT проверяет всю историю и останавливает старт прода (прецедент в
-- этом репозитории). ENUM в ADD COLUMN такой проверки не требует: колонки до сих пор не было,
-- каждая существующая строка получает DEFAULT.
--
-- `link` ВНУТРИ КОМАНДЫ ВИДЕН КАК `team`. Уровень открывает файл НАРУЖУ, а не закрывает внутрь:
-- предикат режет только `people`. Это не упрощение, а смысл: «дал ссылку подрядчику» и «спрятал
-- от коллег» — разные намерения, и второе выражается уровнем `people`.
--
-- DEFAULT 'team' — И СТАРЫЙ БИНАРЬ ЭТО ПЕРЕЖИВАЕТ. DO при провале деплоя откатывает бинарь, а
-- миграции остаются: стор собран через d.Unsafe(), поэтому лишняя колонка в SELECT * его не
-- ломает, а его INSERT'ы новую колонку не называют — файл, залитый откатившимся бинарём, честно
-- ложится в 'team'. Разрушительных операторов в файле нет ни одного, кнопка отката работает.
--
-- ЖУРНАЛ КАСКАДИТ ВМЕСТЕ С ФАЙЛОМ. Он отвечает на вопрос «кто открыл ЭТОТ файл и когда» — про
-- живой файл. Пережившие файл записи не на что было бы посмотреть и незачем хранить: аудит
-- удалений — это другая система с другим сроком жизни, и делать вид, что он тут есть, вреднее,
-- чем его не иметь.
--
-- ИДЕМПОТЕНТНОСТЬ (CLAUDE.md): MySQL 8 автокоммитит DDL, поэтому падение в середине файла
-- оставляет схему полуприменённой БЕЗ строки в gorp_migrations, и следующая загрузка перезапускает
-- файл с начала. ALTER идёт под гейтом information_schema, PREPARE / EXECUTE / DEALLOCATE — по
-- одному оператору на строку (multiStatements=true в контейнерных тестах иначе маскирует поломку,
-- которая на проде валит старт), таблицы заводятся с IF NOT EXISTS. Ретроактивных CHECK нет
-- ни одного.

-- 1. Уровень доступа на самом файле. Отдельным независимым оператором: падение ниже по файлу
--    оставляет колонку на месте, и следующий старт продолжает с таблиц, а не перезапускает DDL,
--    который уже применён. AFTER намеренно не указан — порядок колонок ни на что не влияет, а
--    привязка к соседке связала бы этот файл с тем, применилась ли 0314.
SET @lf_access_level := (SELECT COUNT(*) FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'library_file'
      AND COLUMN_NAME = 'access_level');
SET @ddl := IF(@lf_access_level = 0,
    'ALTER TABLE library_file
        ADD COLUMN access_level ENUM(''team'', ''people'', ''link'') NOT NULL DEFAULT ''team''
            COMMENT ''кому виден файл: team = всей команде, people = перечисленным (+ загрузивший, владельцы, супер), link = плюс кто угодно с публичной ссылкой; внутри команды link виден как team''',
    'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

-- 2. Люди, которым файл открыт поимённо. CASCADE в обе стороны: связь живёт ровно столько, сколько
--    живут оба её конца — удалили файл, нечего открывать; удалили аккаунт, некому.
--    `added_by` — строка, а не ссылка, по тому же доводу, что и `uploaded_by` (0312/0314): «кто
--    впустил» — журнальный факт, он не обязан умирать вместе с аккаунтом впустившего.
--    Загрузившего в этом списке может не быть — он видит файл четвёртым плечом предиката, а не
--    строкой; сервер добавляет его неявно, чтобы человек не выкинул сам себя из своего файла.
CREATE TABLE IF NOT EXISTS library_file_access_people (
  id INT PRIMARY KEY AUTO_INCREMENT,
  file_id INT NOT NULL,
  admin_id INT NOT NULL,
  added_by VARCHAR(255) NOT NULL DEFAULT '' COMMENT 'username открывшего доступ, из JWT; журнальный факт, ссылкой намеренно не является',
  created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
  UNIQUE KEY uniq_library_file_access_people (file_id, admin_id),
  INDEX idx_library_file_access_people_admin (admin_id),
  CONSTRAINT fk_lfap_file FOREIGN KEY (file_id)
    REFERENCES library_file (id) ON DELETE CASCADE,
  CONSTRAINT fk_lfap_admin FOREIGN KEY (admin_id)
    REFERENCES admins (id) ON DELETE CASCADE
) ENGINE = InnoDB DEFAULT CHARSET = utf8mb4 COMMENT 'Кому файл открыт поимённо на уровне people';

-- 3. Публичная ссылка. Зеркало схемы доступа выкроек (0288) и наряда на партию (0293) — вплоть до
--    имён колонок, потому что механика та же: токен несёт epoch, на котором был выпущен, и
--    `epoch = epoch + 1` убивает все выданные ссылки разом, не трогая ни файл, ни pepper. Это и
--    есть «пересоздать ссылку» из макета: отзыва без пересоздания не существует.
--    `access_count` BIGINT (а не INT) — как в 0293: счётчик публичного маршрута инкрементится
--    любым, кто знает ссылку, и переполнить INT дешевле, чем кажется.
--    Строка живёт ДОЛЬШЕ уровня: переключили файл обратно в team — строка остаётся с прежним
--    epoch, и старая ссылка НЕ оживает при возврате в link только потому, что маршрут проверяет
--    `access_level = 'link'` на самой строке файла, а не наличие этой строки. Возврат в link без
--    ротации — сознательное «включить ту же ссылку снова», а не случайность.
--    `updated_at` — операционное «когда строку последний раз трогали», в том числе статистикой;
--    вопрос «кто и когда менял доступ» отвечает журнал ниже, а не это поле.
CREATE TABLE IF NOT EXISTS library_file_public_access (
  file_id INT NOT NULL PRIMARY KEY,
  epoch INT NOT NULL DEFAULT 1 COMMENT 'поколение ссылки; +1 мгновенно убивает все выданные токены этого файла',
  expires_at TIMESTAMP NULL DEFAULT NULL COMMENT 'срок ссылки; NULL = бессрочно (чипы 24ч/7д/30д/бессрочно)',
  revoked_at TIMESTAMP NULL DEFAULT NULL COMMENT 'когда ссылку отозвали явно; непустое = маршрут отвечает 404 при любом epoch',
  last_access_at TIMESTAMP NULL DEFAULT NULL COMMENT 'когда по ссылке последний раз заходили; best-effort статистика',
  access_count BIGINT NOT NULL DEFAULT 0 COMMENT 'сколько раз ссылку открывали; best-effort статистика',
  created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
  updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
  CONSTRAINT fk_lfpa_file FOREIGN KEY (file_id)
    REFERENCES library_file (id) ON DELETE CASCADE
) ENGINE = InnoDB DEFAULT CHARSET = utf8mb4 COMMENT 'Публичная ссылка на файл библиотеки (/api/f/{token}): поколение, срок, статистика';

-- 4. Журнал доступа: кто / когда / что. Ровно три поля записи — больше макет не показывает, а
--    больше и нечего: журнал читается человеком в блоке доступа, а не машиной.
--    ОДНО исключение из «нечитаемо машиной» названо здесь, чтобы стор не изобретал его заново:
--    витрина показывает «кто открыл» = actor ПОСЛЕДНЕГО события установления ТЕКУЩЕГО уровня,
--    поэтому `what` обязан начинаться со стабильного машинного префикса уровня (`level:team` /
--    `level:people` / `level:link`), а человеческий хвост идёт после него. Отдельной колонки под
--    уровень нет намеренно: она была бы пустой у событий «± человек» и «срок», то есть у
--    большинства строк.
--    `actor` — строка-факт (переживает удаление аккаунта), `actor_id` — живая ссылка для аватара,
--    обнуляется вместе с аккаунтом. Тот же разнос, что у автора реплики (0316) и загрузившего (0314).
CREATE TABLE IF NOT EXISTS library_file_access_event (
  id INT PRIMARY KEY AUTO_INCREMENT,
  file_id INT NOT NULL,
  actor VARCHAR(255) NOT NULL COMMENT 'username того, кто менял доступ, из JWT; факт-строка, переживает удаление аккаунта',
  actor_id INT NULL COMMENT 'живая ссылка на аккаунт актора; NULL = аккаунта больше нет (строка actor остаётся)',
  what VARCHAR(255) NOT NULL COMMENT 'что именно изменилось; события смены уровня начинаются с машинного префикса level:<team|people|link>',
  created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
  INDEX idx_library_file_access_event_file (file_id, id),
  CONSTRAINT fk_lfae_file FOREIGN KEY (file_id)
    REFERENCES library_file (id) ON DELETE CASCADE,
  CONSTRAINT fk_lfae_actor FOREIGN KEY (actor_id)
    REFERENCES admins (id) ON DELETE SET NULL
) ENGINE = InnoDB DEFAULT CHARSET = utf8mb4 COMMENT 'Журнал изменений доступа к файлу: кто, когда, что';

-- +migrate Down
--
-- Порядок обратный Up, гейт симметричный.
DROP TABLE IF EXISTS library_file_access_event;
DROP TABLE IF EXISTS library_file_public_access;
DROP TABLE IF EXISTS library_file_access_people;

SET @lf_access_level_back := (SELECT COUNT(*) FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'library_file'
      AND COLUMN_NAME = 'access_level');
SET @ddl := IF(@lf_access_level_back = 1,
    'ALTER TABLE library_file DROP COLUMN access_level',
    'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;
