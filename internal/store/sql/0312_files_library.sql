-- +migrate Up

-- БИБЛИОТЕКА ФАЙЛОВ (MVP). Приватные объекты в S3 + метаданные к ним. Ровно три действия:
-- смотреть, загружать, прикреплять к задаче.
--
-- ПОЧЕМУ ТЕМА — ЯРЛЫК, А НЕ ПАПКА. Макет бирки для FW27 честно принадлежит и производству, и
-- коллекции, и теме «бирки». Дерево заставило бы выбрать одну полку, а коллега пошёл бы искать на
-- другой — это и есть момент, когда файл теряется. Связь многие-ко-многим снимает выбор; в
-- интерфейсе она всё равно выглядит как папки (рейл со счётчиками), но файл лежит в нескольких
-- сразу. Тот же приём, что у `task_label` (0090): свободные метки поверх карточки.
--
-- ПОЧЕМУ ТЕМА НЕОБЯЗАТЕЛЬНА. Обязательное поле команда заполняет первым попавшимся значением — в их
-- собственном Obsidian-vault `type: freeform` стоит в 112 записях из 267, а `season` заполнен 9 раз
-- из 91. Пустая тема честнее неправильной, поэтому файл без тем легален и попадает в «Разобрать».
--
-- ПОЧЕМУ `library_file`, А НЕ `file`. FILE — слово с особым статусом в MySQL, и, что важнее, `file`
-- не греппится в кодовой базе этого размера.
--
-- ИДЕМПОТЕНТНОСТЬ (CLAUDE.md): MySQL 8 автокоммитит DDL, поэтому падение в середине файла оставляет
-- схему полуприменённой БЕЗ строки в gorp_migrations, и следующая загрузка перезапускает файл с
-- начала. Здесь только CREATE TABLE IF NOT EXISTS и INSERT IGNORE — повтор является no-op. Никакого
-- ретроактивного ADD CONSTRAINT (он проверяет всю историю и может остановить старт прода): все
-- таблицы новые. PREPARE/EXECUTE не нужны, поэтому и multiStatements-ловушки здесь нет.

-- library_file: один загруженный файл. Байты лежат приватно в S3 под files-library/ — приватность
-- достигается ОТСУТСТВИЕМ заголовка x-amz-acl: public-read, который ставят image.go/pattern.go/
-- label.go. Наружу объект отдаётся только presigned-ссылкой с коротким окном.
CREATE TABLE IF NOT EXISTS library_file (
  id INT PRIMARY KEY AUTO_INCREMENT,
  object_key VARCHAR(512) COLLATE utf8mb4_bin NOT NULL COMMENT 'private S3 key under files-library/; sole pointer to the bytes, never reused',
  preview_object_key VARCHAR(512) COLLATE utf8mb4_bin NULL COMMENT 'private S3 key of the browser-rendered preview image; NULL = no preview',
  file_name VARCHAR(255) NOT NULL COMMENT 'original filename at upload; display + download disposition (sanitized at presign time)',
  content_type VARCHAR(128) NOT NULL DEFAULT '' COMMENT 'MIME declared at upload; inline-safe types get a view url, the rest download-only',
  size_bytes BIGINT NOT NULL DEFAULT 0 COMMENT 'stored byte size, counted server-side while streaming the upload',
  sha256 CHAR(64) COLLATE utf8mb4_bin NOT NULL DEFAULT '' COMMENT 'hex sha256 computed server-side while streaming; duplicate hint now, dedup and LLM-description cache key later',
  uploaded_by VARCHAR(255) NOT NULL DEFAULT '' COMMENT 'admin account username, from the JWT',
  created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
  updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
  UNIQUE KEY uniq_library_file_object_key (object_key),
  INDEX idx_library_file_sha256 (sha256),
  INDEX idx_library_file_created (created_at)
) ENGINE = InnoDB DEFAULT CHARSET = utf8mb4 COMMENT 'Files library: metadata of private S3 objects';

-- file_topic: словарь тем-ярлыков. Тема заводится на лету набором нового имени — иначе человек
-- воткнёт файл в ближайшую неподходящую.
--
-- КОЛЛАЦИЯ ИМЕНИ — ДЕФОЛТНАЯ ai_ci, И ЭТО НАМЕРЕННО (в отличие от object_key/sha256, где стоит _bin).
-- Уникальность имени регистро- и диакритико-независима, поэтому «Brand» и «brand» схлопываются в одну
-- тему, а не разъезжаются в две.
--
-- ЗАЧЕМ ОПИСАНИЕ У ЯРЛЫКА. Эталонный сценарий владельца — «разрабатываем металлическую фурнитуру:
-- файлы + описание + метаданные». Тема обязана уметь быть минимальной страницей проекта, иначе в
-- первый же день окажется, что описание положить некуда. Одна колонка вместо отдельной сущности
-- «проект»: сущность потребовала бы церемонии (создать, назвать, определить границы), а эта команда
-- не тянет даже выбор значения из списка.
CREATE TABLE IF NOT EXISTS file_topic (
  id INT PRIMARY KEY AUTO_INCREMENT,
  name VARCHAR(64) NOT NULL COMMENT 'topic display name; unique case-insensitively',
  description TEXT NULL COMMENT 'what this topic is about; what lets a label stand in for a project page',
  created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
  UNIQUE KEY uniq_file_topic_name (name)
) ENGINE = InnoDB DEFAULT CHARSET = utf8mb4 COMMENT 'Topic labels for the files library';

-- library_file_topic: связь файл ↔ тема.
-- FK на тему БЕЗ каскада (RESTRICT по умолчанию): удаление непустой темы обязано падать, иначе оно
-- молча отвязало бы файлы и они бы уехали в «Разобрать». Хендлер проверяет это заранее и отвечает
-- понятным текстом, а констрейнт — последний рубеж. FK на файл каскадный: удалили файл — связи ушли
-- вместе с ним, держать их не за что.
CREATE TABLE IF NOT EXISTS library_file_topic (
  id INT PRIMARY KEY AUTO_INCREMENT,
  file_id INT NOT NULL,
  topic_id INT NOT NULL,
  created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
  UNIQUE KEY uniq_library_file_topic (file_id, topic_id),
  INDEX idx_library_file_topic_topic (topic_id),
  CONSTRAINT fk_library_file_topic_file FOREIGN KEY (file_id)
    REFERENCES library_file (id) ON DELETE CASCADE,
  CONSTRAINT fk_library_file_topic_topic FOREIGN KEY (topic_id)
    REFERENCES file_topic (id)
) ENGINE = InnoDB DEFAULT CHARSET = utf8mb4 COMMENT 'File-to-topic label links';

-- task_file: файлы библиотеки, прикреплённые к карточке канбана. Зеркалит task_media (0090),
-- включая раскладку FK: задача каскадит (удалили карточку — вложения ушли), файл RESTRICT (удалить
-- файл, который держит задача, нельзя; стор возвращает id держателей, чтобы сообщение назвало их —
-- иначе получится «не удаляется и непонятно почему»).
CREATE TABLE IF NOT EXISTS task_file (
  id INT PRIMARY KEY AUTO_INCREMENT,
  task_id INT NOT NULL,
  file_id INT NOT NULL,
  display_order INT NOT NULL DEFAULT 0,
  UNIQUE KEY uniq_task_file (task_id, file_id),
  INDEX idx_task_file_file (file_id),
  CONSTRAINT fk_task_file_task FOREIGN KEY (task_id)
    REFERENCES task (id) ON DELETE CASCADE,
  CONSTRAINT fk_task_file_file FOREIGN KEY (file_id)
    REFERENCES library_file (id)
) ENGINE = InnoDB DEFAULT CHARSET = utf8mb4 COMMENT 'Library files attached to a task';

-- Стартовые темы = папки Obsidian-vault владельца. Они не выдуманы: сложились сами за 75 коммитов и
-- отражают то, как в компании реально думают о своей работе. Раздел, открывшийся с пустым рейлом,
-- заставляет придумывать структуру на месте — а придумывать её никто не будет.
--
-- 'legal' И 'finance' СОЗНАТЕЛЬНО НЕ ЗАВЕДЕНЫ. У библиотеки одна секция прав, то есть всё в ней
-- видно каждому, у кого есть files:read. Готовые темы с такими именами были бы приглашением сложить
-- туда договоры и финансы — ровно то, чего эта библиотека удержать не может. Та же граница, которую
-- позже проведёт allowlist папок при синке vault. Завести их вручную по-прежнему можно: это будет
-- осознанное решение человека, а не подсказка системы.
INSERT IGNORE INTO file_topic (name) VALUES
  ('brand'), ('marketing'), ('collections'), ('materials'), ('packaging'),
  ('bizdev'), ('content'), ('copywriting'), ('community'), ('products'),
  ('warehouse'), ('ui-ux'), ('texts'), ('meetings'), ('atelier'),
  ('ecommerce-tech');

-- +migrate Down
DROP TABLE IF EXISTS task_file;
DROP TABLE IF EXISTS library_file_topic;
DROP TABLE IF EXISTS file_topic;
DROP TABLE IF EXISTS library_file;
