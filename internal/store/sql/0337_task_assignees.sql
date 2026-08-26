-- +migrate Up

-- МУЛЬТИАСАЙН: У ЗАДАЧИ СПИСОК ИСПОЛНИТЕЛЕЙ, А НЕ ОДИН.
--
-- ПОЧЕМУ ТАБЛИЦА, А НЕ JSON/CSV В КОЛОНКЕ. По исполнителю ФИЛЬТРУЮТ («мои задачи», ListTasks
-- assignee=…), а фильтр по элементу списка внутри колонки — это LIKE/JSON_CONTAINS без индекса.
-- Таблица даёт EXISTS по UNIQUE (task_id, username).
--
-- ПОЧЕМУ БЕЗ «ГЛАВНОГО». Ни одна функция системы (фильтр «мои», строка карточки файла, доска) не
-- различает первого исполнителя и остальных, а различие без потребителя — это поле, которое
-- заполняют наугад. Порядок отображения аватарок несёт display_order; появись потребитель у
-- «главного» — это будет элемент с display_order = 0, и схему менять не придётся.
--
-- БЕЗ FK НА admins(username), И ЭТО НЕ НЕДОСМОТР. У колонки task.assignee ключа не было никогда
-- (0090), username в этой системе — строка-факт, переживающая удаление аккаунта (та же доктрина,
-- что у author/author_id в 0316). CASCADE от аккаунта молча снимал бы людей с задач; RESTRICT
-- запретил бы удалять аккаунт. Проверка существования не делается и в коде — паритет с нынешним
-- поведением: пикер клиента наполняется из ListAccounts.
--
-- КОЛОНКА task.assignee ЭТОЙ МИГРАЦИЕЙ НЕ ДРОПАЕТСЯ, И ЭТО ОСОЗНАННОЕ РЕШЕНИЕ ВОЛНЫ. Дроп закрыл бы
-- откат на до-волновой бинарь (он именует assignee в INSERT/UPDATE задач), а платформа при провале
-- health-check откатывается на прежний образ молча. Новый код колонку НЕ ЧИТАЕТ И НЕ ПИШЕТ — она
-- осиротела ровно здесь. Снос отдельной миграцией после того, как прод отработает на новом бинаре:
-- см. internal/store/sql/README-pending-drops.md.
--
-- БЭКФИЛЛ ПЕРЕНОСИТ НЫНЕШНЕГО ЕДИНСТВЕННОГО ИСПОЛНИТЕЛЯ. Пустая строка = «не назначена» (0090:
-- '"" = unassigned»), поэтому она не превращается в исполнителя с пустым именем.
--
-- ИДЕМПОТЕНТНОСТЬ (CLAUDE.md): MySQL 8 автокоммитит DDL, поэтому падение в середине файла оставляет
-- схему полуприменённой БЕЗ строки в gorp_migrations, и следующая загрузка перезапускает файл с
-- начала. Создание таблицы идёт под IF NOT EXISTS и на повторе является no-op; у INSERT ... SELECT
-- стоит NOT EXISTS по той же паре, поэтому второй прогон не удваивает строки и не падает об UNIQUE.
--
-- ЛИМИТА 5 МИНУТ ЭТА МИГРАЦИЯ НЕ КАСАЕТСЯ: task — внутренний канбан в сотни строк; новая таблица
-- рождается пустой, а бэкфилл — один INSERT ... SELECT по этим сотням. Ретроактивных CHECK нет
-- (CHECK копирует таблицу целиком), а FK ставится на НОВОРОЖДЁННУЮ таблицу, где проверять нечего.

CREATE TABLE IF NOT EXISTS task_assignee (
  id INT PRIMARY KEY AUTO_INCREMENT,
  task_id INT NOT NULL,
  username VARCHAR(255) NOT NULL COMMENT 'admin account username; строка-факт, БЕЗ FK на admins — как task.assignee с 0090',
  display_order INT NOT NULL DEFAULT 0 COMMENT 'порядок показа аватарок; «главного» исполнителя в модели нет',
  UNIQUE KEY uniq_task_assignee (task_id, username),
  INDEX idx_task_assignee_username (username),
  CONSTRAINT fk_task_assignee_task FOREIGN KEY (task_id) REFERENCES task (id) ON DELETE CASCADE
) ENGINE = InnoDB DEFAULT CHARSET = utf8mb4 COMMENT 'Исполнители задачи (мультиасайн)';

INSERT INTO task_assignee (task_id, username, display_order)
SELECT t.id, t.assignee, 0 FROM task t
WHERE t.assignee <> ''
  AND NOT EXISTS (SELECT 1 FROM task_assignee ta WHERE ta.task_id = t.id);

-- +migrate Down

-- Откат сносит таблицу целиком. Колонка task.assignee при этом ЖИВА (Up её не трогал), поэтому
-- откат возвращает ровно ту картину, что была до волны, — единственного исполнителя на карточку.
-- Исполнители, добавленные вторыми и далее, теряются: их негде было хранить до этой миграции.
DROP TABLE IF EXISTS task_assignee;
