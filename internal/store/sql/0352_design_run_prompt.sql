-- Полоса DESIGN: собранный промпт прогона — В СТРОКУ ИСТОРИИ.
--
-- ЧТО И ЗАЧЕМ. `design_run.prompt` — текст, который воркер РЕАЛЬНО отправил поставщику: ask,
-- описание изделия, посадка, цвет, нумерованные подписи референсов («image 1: ...») и, на
-- флэтовом маршруте, эталонные абзацы владельца. До этой колонки собранный текст жил одно
-- мгновение в памяти воркера (designgen.buildJob) и не был виден никому: человек смотрел в опись
-- снапшота, не находил там слов эталонов и делал вывод, что их нет вовсе. Пишет колонку ТОЛЬКО
-- воркер (RecordRunPrompt), под токеном захвата, ДО первой платной попытки — история несёт
-- отправленное, а не реконструкцию. NULL значит «воркер прогон ещё не поднимал».
--
-- MEDIUMTEXT, потому что вход прогона (inputs) сам ограничен 64 KB и собранный текст с подписями
-- и эталонами может перерасти потолок TEXT; MEDIUMTEXT в InnoDB живёт вне строки и не стоит
-- ничего лишнего.
--
-- NULLABLE ADD COLUMN уровня INSTANT: ни одной копии таблицы, пятиминутный потолок прогона
-- миграций не задевается. Ретроактивных CHECK и UNIQUE здесь нет.
--
-- ОТКАТ БИНАРЯ ПОСЛЕ ЭТОЙ МИГРАЦИИ БЕЗОПАСЕН: хендл стора — Unsafe() (store.New), лишнюю колонку
-- старый `SELECT *` просто не сканирует, а в INSERT/UPDATE старый код её не именует.
--
-- ИДЕМПОТЕНТНОСТЬ: MySQL не знает `ADD COLUMN IF NOT EXISTS`, поэтому ALTER охраняется по
-- `information_schema.COLUMNS`. `PREPARE`/`EXECUTE`/`DEALLOCATE` — каждый своей строкой: прод
-- ходит без `multiStatements`.

-- +migrate Up

SET @dr_prompt := (SELECT COUNT(*) FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'design_run'
      AND COLUMN_NAME = 'prompt');
SET @ddl := IF(@dr_prompt = 0,
    'ALTER TABLE design_run
        ADD COLUMN prompt MEDIUMTEXT NULL COMMENT ''собранный текст, ушедший модели; пишет воркер (RecordRunPrompt) до первой платной попытки; NULL = прогон ещё не поднимали''',
    'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

-- +migrate Down

SET @dr_prompt_down := (SELECT COUNT(*) FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'design_run'
      AND COLUMN_NAME = 'prompt');
SET @ddl_down := IF(@dr_prompt_down = 1,
    'ALTER TABLE design_run DROP COLUMN prompt',
    'SELECT 1');
PREPARE stmt FROM @ddl_down;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;
