-- +migrate Up

-- ЖИВАЯ ССЫЛКА НА АВТОРА РЕПЛИКИ ЗАДАЧИ — РАДИ ПРАВА УДАЛЕНИЯ, А НЕ РАДИ АВАТАРКИ.
--
-- Гейт «свою реплику удаляет автор, супер — любую» копируется у обсуждения файла
-- (mayEditLibraryFileComment, 0316) ВМЕСТЕ С ПРИЧИНОЙ, по которой одного совпадения имени мало:
-- UNIQUE на admins.username освобождает имя при удалении аккаунта. Удалили pasha → строка author
-- «pasha» осталась → завели НОВОГО pasha → он совпал бы по имени со ВСЕЙ перепиской прежнего и мог
-- бы её стирать. Требование живой ссылки закрывает ровно это: author_id есть только у реплики, чей
-- автор ВСЁ ЕЩЁ существует, а имя уникально — значит совпадение имени при живой ссылке — тот же
-- самый человек.
--
-- ДВА ПОЛЯ АВТОРА, КАК В 0316. `author` — строка-факт: «это написал pasha», она обязана пережить
-- удаление аккаунта, иначе лента задним числом теряет говорящих. `author_id` — живая ссылка,
-- честно обнуляется вместе с аккаунтом (ON DELETE SET NULL).
--
-- НАЗВАННОЕ ДОПУЩЕНИЕ БЭКФИЛЛА: он приписывает старые реплики ТЕКУЩЕМУ владельцу username. Если имя
-- в истории команды переиспользовалось, старые реплики достанутся новому человеку. Для этой команды
-- имена не переиспользовались. Реплики, у которых автора среди admins нет, остаются author_id =
-- NULL — их удаляет только супер, и это правильный остаток. Альтернатива «не бэкфиллить вовсе»
-- отвергнута: тогда НИ ОДИН существующий комментарий не смог бы удалить никто, кроме супера, а
-- просьба владельца ровно про существующие.
--
-- ИДЕМПОТЕНТНОСТЬ (CLAUDE.md): DDL автокоммитится, падение в середине файла оставляет схему
-- полуприменённой без строки в gorp_migrations. Колонка и FK едут ОДНИМ ALTER под гвардом по наличию
-- колонки (одиночный ALTER в MySQL 8 атомарен — состояние «колонка есть, ключа нет» недостижимо, НЕ
-- РАЗБИВАТЬ на два), бэкфилл ограничен `WHERE author_id IS NULL` и на повторе не делает ничего.
-- PREPARE/EXECUTE/DEALLOCATE — ПО ОДНОМУ ОПЕРАТОРУ НА СТРОКУ: прод ходит без multiStatements.
--
-- ЛИМИТ 5 МИНУТ: task_comment — переписка внутреннего канбана, сотни строк. ADD COLUMN NULL +
-- FK по сплошь-NULL данным валидировать нечего; ретроактивных CHECK файл не ставит.

SET @tc_author_id := (
    SELECT COUNT(*) FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE()
      AND TABLE_NAME = 'task_comment'
      AND COLUMN_NAME = 'author_id'
);
SET @ddl := IF(@tc_author_id = 0,
    'ALTER TABLE task_comment ADD COLUMN author_id INT NULL COMMENT ''живая ссылка на аккаунт автора; NULL = аккаунта больше нет (строка author остаётся). Право удаления требует ЖИВОЙ ссылки — доктрина 0316'', ADD CONSTRAINT fk_task_comment_author FOREIGN KEY (author_id) REFERENCES admins (id) ON DELETE SET NULL',
    'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

UPDATE task_comment tc
JOIN admins a ON a.username = tc.author
SET tc.author_id = a.id
WHERE tc.author_id IS NULL;

-- +migrate Down

-- Симметричный откат под тем же гвардом, ОДНИМ ALTER. Ссылки теряются; строки author целы, поэтому
-- лента после отката читается как раньше — исчезает только право автора удалять свою реплику.
SET @tc_author_id_down := (
    SELECT COUNT(*) FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE()
      AND TABLE_NAME = 'task_comment'
      AND COLUMN_NAME = 'author_id'
);
SET @ddl_down := IF(@tc_author_id_down = 1,
    'ALTER TABLE task_comment DROP FOREIGN KEY fk_task_comment_author, DROP COLUMN author_id',
    'SELECT 1');
PREPARE stmt FROM @ddl_down;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;
