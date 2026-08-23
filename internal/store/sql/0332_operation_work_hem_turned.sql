-- +migrate Up

-- ОДНА РАБОТА: ПОДГИБ ВДВОЕ НА ПРЯМОСТРОЧКЕ. Файл аддитивен целиком — три INSERT'а в таблицы
-- каталога 0329 и ни одной правки существующей строки, ни одного тронутого CHECK'а, ни одного
-- нового члена enum. Откат бинаря безопасен: эти таблицы читает только код волны операций.
--
-- ЗАЧЕМ. Замер прода 2026-08-23 показал дыру, которую до экрана ратификации не было видно: на
-- прямострочке в каталоге живут РОВНО ЧЕТЫРЕ работы — `moscow_hem`, `attach_label`,
-- `join_lockstitch`, `topstitch`. Самого обычного подгиба в мире среди них нет. Четыре строки
-- прода несут класс шва `ef_hem_turned` на прямострочке, и экран ратификации честно молчал на
-- них: предложить было нечего, а изобретать имя работы он не имеет права.
--
-- ПОЧЕМУ НЕ ГОДИЛОСЬ НИ ОДНО СУЩЕСТВУЮЩЕЕ ИМЯ. `moscow_hem` — узкий рулонный шов (он же
-- «рубильник»), приём другой ширины и другой оснастки; назвать им обычный подгиб значило бы
-- отправить в цех бумагу, которая врёт про оснастку. `blindhem` живёт на ПОТАЙНОЙ машине, а эти
-- четыре строки стоят на прямострочке. Владелец подтвердил 2026-08-23: заводим отдельную работу.
--
-- ТОКЕН НАВСЕГДА. `token` и `verb` — идентичность (0329): `verb` входит в проекцию дайджеста через
-- правило когерентности 0330, и правка задним числом раздвоила бы отпечаток подписанной карточки.
-- Ярлык (`label`) — представление, его можно поправить дешёвой UPDATE-миграцией; токен — нельзя.
--
-- `sort` = 76 — МЕЖСТРОЧНЫЙ, как у 0331: подгиб встаёт рядом с роднёй (потайная 70, московский
-- 75), потому что пикер листают глазами. Ровно ради этого 0329 шагала десятками.

INSERT INTO operation_work (token, verb, stage, label, machine_mode, default_machine, sort) VALUES
  ('hem_turned', 'machine', 'edges_hems', 'Hem — turned twice', 'fixed', 'lockstitch', 76)
ON DUPLICATE KEY UPDATE sort = sort;

-- Режим `fixed` — ровно одна машинка, та же, что в `default_machine`.
INSERT INTO operation_work_machine (work_token, machine_type) VALUES
  ('hem_turned', 'lockstitch')
ON DUPLICATE KEY UPDATE work_token = work_token;

-- СИНОНИМЫ: и кириллица, и латиница — требование guard-теста, а не пожелание. Технолог печатает
-- «подгиб», а не «hem». Повтор слова поперёк работ законен (ключ — пара): «подгиб» найдёт и
-- московский шов, и этот, и выбор между ними — как раз то, что человек обязан сделать сам.
INSERT INTO operation_work_syn (work_token, syn) VALUES
  ('hem_turned', 'подгиб'),
  ('hem_turned', 'подгибка'),
  ('hem_turned', 'подогнуть'),
  ('hem_turned', 'подгиб вдвое'),
  ('hem_turned', 'закрытый срез'),
  ('hem_turned', 'hem'),
  ('hem_turned', 'turned hem'),
  ('hem_turned', 'double fold hem'),
  ('hem_turned', 'double turn')
ON DUPLICATE KEY UPDATE work_token = work_token;

-- +migrate Down

DELETE FROM operation_work_syn WHERE work_token = 'hem_turned';
DELETE FROM operation_work_machine WHERE work_token = 'hem_turned';
DELETE FROM operation_work WHERE token = 'hem_turned';
