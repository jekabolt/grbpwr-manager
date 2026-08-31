-- Снос подсистемы ВЕРСИЙ ЛИСТА полосы DESIGN: минт (Rev.N), его плиты, его выноски и журнал
-- выпуска. Отменяет 0342 целиком, вместе с уже выпущенными подписанными листами.
--
-- ЧЬЁ ЭТО РЕШЕНИЕ И ЧЕМ ЗА НЕГО ЗАПЛАЧЕНО. Владелец: «PRINT — MINTS V1 вообще этот функционал не
-- нужен как и VERSIONS», и на вопрос, до какой глубины резать, с НАЗВАННОЙ ценой — «Снести целиком,
-- включая бэкенд». Цена названа и принята, поэтому она перечисляется здесь поимённо, а не
-- прячется за словом «cleanup»:
--   * все уже сминченные версии (Rev.1..Rev.N каждой карточки) исчезают вместе со своим составом;
--   * исчезают их content_hash — тот самый снимок «какие БАЙТЫ были на бумаге в момент минта»,
--     который 0342 завела ровно затем, чтобы пережить последующую перезапись media;
--   * исчезает append-only журнал выпуска: кто, когда и каким жестом напечатал или отдал лист;
--   * ссылки медиатеки вида «этот файл держит версия листа» пропадают из GetMediaUsage вместе с
--     регистрацией их колонок в mediaRefRegistry.
-- ЭТО НЕОБРАТИМО И ЗАДУМАНО ТАКИМ. Восстановить подписанный лист после этой миграции нельзя ничем.
--
-- ЭТО ЖЕ И ОСВОБОЖДАЕТ БАЙТЫ. `design_sheet_version_plate.media_id` и
-- `design_sheet_version_callout.media_id` держат media(id) через ON DELETE RESTRICT (0342) —
-- именно этим версия и «пиннила» файл, не давая его удалить. Дроп ЭТИХ ТАБЛИЦ и есть то
-- единственное, что снимает пин: пока таблицы существуют, снятие подсистемы из кода оставило бы
-- файлы неудаляемыми навсегда, без единого живого читателя, способного объяснить человеку, КТО их
-- держит.
--
-- ПОРЯДОК ДРОПА — ДЕТИ ПЕРЕД РОДИТЕЛЕМ, и он не косметический. На `design_sheet_version`
-- смотрят ТРИ входящих FK (все ON DELETE CASCADE, 0342): `design_sheet_version_plate.version_id`,
-- `design_sheet_version_callout.version_id`, `design_sheet_issue.version_id`. CASCADE описывает
-- удаление СТРОК и ничего не обещает про DROP TABLE: родитель, снесённый первым, упирается в 1217
-- (foreign key constraint fails). Снаружи этой четвёрки на них не ссылается ни одна таблица —
-- проверено по всей `internal/store/sql`, — поэтому после неё чистить нечего.
--
-- ИДЕМПОТЕНТНО ЧЕРЕЗ `IF EXISTS` НА КАЖДОМ ДРОПЕ, по общему правилу репозитория: DDL в MySQL
-- автокоммитится, поэтому падение в СЕРЕДИНЕ файла оставляет схему полуснесённой и БЕЗ строки в
-- `gorp_migrations`, а следующий старт прогоняет файл заново С НАЧАЛА. Без `IF EXISTS` второй
-- проход умер бы на уже снесённой первой таблице и заклинил бы старт.
--
-- БЕЗОПАСНО И ПРИ ПРИМЕНЕНИИ ВНЕ ЧИСЛОВОГО ПОРЯДКА. `sql-migrate` собирает отставшие миграции и
-- кладёт их ПОСЛЕ старших номеров (`ToCatchup`), поэтому файл не имеет права читать объекты,
-- заводимые старшими номерами. Этот и не читает: он трогает ровно четыре таблицы, заведённые
-- МЛАДШИМ номером 0342, и ни одного объекта 0343–0352.
--
-- НИ ОДНОГО `ADD CONSTRAINT`/`CHECK` здесь нет и быть не может: они копируют таблицу целиком, а
-- потолок проверки миграций в пять минут захардкожен и останавливает старт прода.

-- +migrate Up

DROP TABLE IF EXISTS design_sheet_issue;
DROP TABLE IF EXISTS design_sheet_version_callout;
DROP TABLE IF EXISTS design_sheet_version_plate;
DROP TABLE IF EXISTS design_sheet_version;

-- +migrate Down
-- No-op, and deliberately honest about why: the schema could be re-created from 0342 verbatim, but
-- the DATA cannot. Every minted revision, every frozen plate with its content_hash, every frozen
-- callout and every journal line is destroyed by the Up branch above. Re-creating four empty tables
-- would restore the SHAPE of the evidence and none of the evidence — a rollback that reads as
-- «the sheets are back» while every signed sheet is gone. There is no code left that reads them
-- either, so an empty schema would serve nothing but the appearance of reversibility.
