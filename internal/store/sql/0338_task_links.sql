-- +migrate Up

-- САБТАСКИ, БЛОКЕРЫ И «СВЯЗАНО» — ДВА МЕХАНИЗМА, А НЕ ОДИН, И ВОТ ДОВОД.
--
-- РОДИТЕЛЬ — КОЛОНКА. Отношение «сабтаска» имеет кардинальность 1: у карточки один родитель. В
-- колонке две строки «у X родитель Y» и «у X родитель Z» НЕВОЗМОЖНЫ физически, а в таблице связей
-- потребовали бы условного UNIQUE, которого у MySQL нет. У родителя вдобавок своя семантика
-- удаления (дети остаются жить) и свой запрет циклов.
--
-- БЛОКЕР И «СВЯЗАНО» — ТАБЛИЦА. Это отношения многие-ко-многим между равноправными карточками: одна
-- задача блокирует пять, пять блокируют одну. Затащить сюда и родителя (kind='subtask_of') значило
-- бы потерять кардинальность 1 и завести ВТОРОЙ способ записать иерархию.
--
-- ЧЕК-ЛИСТ (task_checklist_item, 0090, «lightweight subtask» по собственному комментарию) НЕ
-- ТРОГАЕТСЯ И НИЧЕГО НЕ МИГРИРУЕТ. Владелец просит сабтаски ПРИ живом чек-листе — значит, ему нужны
-- настоящие карточки со своим исполнителем, статусом и местом на доске, а не строки с галочкой.
--
-- ОДНА СТРОКА НА ФАКТ БЛОКИРОВКИ: from_task_id = БЛОКЕР, to_task_id = блокируемая. Читается в обе
-- стороны. Две строки на один факт («A знает, что блокирует B» + «B знает, что заблокирована»)
-- позволили бы паре полусуществовать — это та же болезнь, что у хвоста дайджеста позициями вместо
-- пар.
--
-- relates СИММЕТРИЧНА И НОРМАЛИЗУЕТСЯ ПРИ ЗАПИСИ (from < to). Инвариант закреплён CHECK'ом, поэтому
-- дубль (A,B)+(B,A) НЕВЫРАЗИМ схемой, а не только кодом.
--
-- CHECK'И ЗДЕСЬ ЗАКОННЫ, ХОТЯ «ADD CHECK = COPY»: таблица НОВОРОЖДЁННАЯ, копировать нечего, истории
-- нет. Ретроактивного CHECK ни на одной живой таблице этот файл не ставит.
--
-- ON DELETE SET NULL У РОДИТЕЛЯ, А НЕ CASCADE. Удалили родителя — дети становятся верхнеуровневыми и
-- живут. CASCADE значил бы, что удаление ОДНОЙ карточки молча уносит дерево чужой работы: ровно
-- история order_item ON DELETE CASCADE из 0001, повторять её не надо. Архив ортогонален: архив
-- родителя детей не трогает.
--
-- task_link КАСКАДИТ С ОБЕИХ СТОРОН: связь без конца бессодержательна, своего текста у неё нет.
--
-- БЛОКЕР — СОВЕТ, А НЕ ЗАМОК: перевод в DONE схема не запрещает и запрещать не будет. Доска —
-- drag-and-drop, жёсткий замок превращал бы MoveTask в отказ посреди жеста, а блокер, который
-- заархивировали не доделав, замуровал бы карточку навсегда. Сервер отдаёт статус второго конца,
-- бейдж рисует клиент.
--
-- ИДЕМПОТЕНТНОСТЬ (CLAUDE.md): DDL автокоммитится, падение в середине файла оставляет схему
-- полуприменённой без строки в gorp_migrations. Колонка+индекс+FK едут ОДНИМ ALTER под гвардом по
-- наличию колонки (одиночный ALTER в MySQL 8 атомарен, поэтому состояние «колонка есть, ключа нет»
-- недостижимо — НЕ РАЗБИВАТЬ его на два «ради читаемости»); таблица создаётся под IF NOT EXISTS.
-- PREPARE/EXECUTE/DEALLOCATE — ПО ОДНОМУ ОПЕРАТОРУ НА СТРОКУ: прод ходит без multiStatements, и
-- склеенная строка молча не выполнится.
--
-- ЛИМИТ 5 МИНУТ НЕ ЗАДЕВАЕТСЯ: ALTER добавляет СПЛОШЬ NULL-колонку (валидировать нечего) на таблице
-- в сотни строк, task_link рождается пустой.

SET @task_parent_col := (
    SELECT COUNT(*) FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE()
      AND TABLE_NAME = 'task'
      AND COLUMN_NAME = 'parent_task_id'
);
SET @ddl := IF(@task_parent_col = 0,
    'ALTER TABLE task ADD COLUMN parent_task_id INT NULL COMMENT ''родитель-сабтаска; NULL = верхний уровень; циклы запрещает стор, а не схема'', ADD INDEX idx_task_parent (parent_task_id), ADD CONSTRAINT fk_task_parent FOREIGN KEY (parent_task_id) REFERENCES task (id) ON DELETE SET NULL',
    'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

CREATE TABLE IF NOT EXISTS task_link (
  id INT PRIMARY KEY AUTO_INCREMENT,
  from_task_id INT NOT NULL COMMENT 'для kind=blocks: БЛОКЕР; для relates: меньший id (нормализация)',
  to_task_id INT NOT NULL COMMENT 'для kind=blocks: блокируемая; для relates: больший id',
  kind VARCHAR(16) NOT NULL COMMENT 'blocks|relates; BLOCKED_BY — это blocks, прочитанный с другого конца, отдельного вида в хранилище НЕТ',
  created_by VARCHAR(255) NOT NULL DEFAULT '' COMMENT 'admin account username, из JWT',
  created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
  UNIQUE KEY uniq_task_link (from_task_id, to_task_id, kind),
  INDEX idx_task_link_to (to_task_id),
  CONSTRAINT fk_task_link_from FOREIGN KEY (from_task_id) REFERENCES task (id) ON DELETE CASCADE,
  CONSTRAINT fk_task_link_to FOREIGN KEY (to_task_id) REFERENCES task (id) ON DELETE CASCADE,
  CONSTRAINT chk_task_link_kind CHECK (kind IN ('blocks', 'relates')),
  CONSTRAINT chk_task_link_not_self CHECK (from_task_id <> to_task_id),
  CONSTRAINT chk_task_link_relates_ordered CHECK (kind <> 'relates' OR from_task_id < to_task_id)
) ENGINE = InnoDB DEFAULT CHARSET = utf8mb4 COMMENT 'Типизированные связи задач (блокеры и «связано»)';

-- +migrate Down

-- Порядок обратный Up: сначала таблица связей, затем колонка родителя. Данные теряются целиком —
-- других мест, где иерархия и связи хранились бы, нет.
DROP TABLE IF EXISTS task_link;

SET @task_parent_col_down := (
    SELECT COUNT(*) FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE()
      AND TABLE_NAME = 'task'
      AND COLUMN_NAME = 'parent_task_id'
);
SET @ddl_down := IF(@task_parent_col_down = 1,
    'ALTER TABLE task DROP FOREIGN KEY fk_task_parent, DROP INDEX idx_task_parent, DROP COLUMN parent_task_id',
    'SELECT 1');
PREPARE stmt FROM @ddl_down;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;
