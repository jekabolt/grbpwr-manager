-- +migrate Up

-- ОДНА РАБОТА: НАВЕСИТЬ ЭТИКЕТКУ (HANG TAG). Файл аддитивен целиком — два INSERT'а в таблицы
-- каталога 0329 и ни одной правки существующей строки, CHECK'а или enum'а. Откат бинаря безопасен.
--
-- ЗАЧЕМ. Волна w1107, решение В15: шаг «навесить» — ручная работа, которая ссылается на этикетку
-- карточки (label_keys, 0454), как притачка `attach_label`. Клиент уже знает пункт J3 `hang_tag`
-- (HANDWORK), но пикер собирается из этого каталога, и без строки здесь пункт не виден и не
-- сохраняется (FK operation_work.token).
--
-- ПОЧЕМУ НЕ `tag_attach`. Та работа (0437) — установка фурнитуры-бирки (hardware_set, пистолет),
-- у неё другой глагол и другой блок шага; навешивание бумажной/картонной бирки на шов — рука.
--
-- ТОКЕН НАВСЕГДА: `hang_tag` и глагол `handwork` заморожены в operation_work_hang_tag_test.go.
-- `sort` = 154 — рядом с роднёй отделки (attach_label 150 … tag_attach 153).

INSERT INTO operation_work (token, verb, stage, label, machine_mode, default_machine, sort) VALUES
  ('hang_tag', 'handwork', 'finishing', 'Hang tag', 'none', NULL, 154)
ON DUPLICATE KEY UPDATE sort = sort;

INSERT INTO operation_work_syn (work_token, syn) VALUES
  ('hang_tag', 'навесить бирку'),
  ('hang_tag', 'навесная бирка'),
  ('hang_tag', 'навесить'),
  ('hang_tag', 'бирка'),
  ('hang_tag', 'hang tag'),
  ('hang_tag', 'hangtag'),
  ('hang_tag', 'hang')
ON DUPLICATE KEY UPDATE work_token = work_token;

-- +migrate Down

DELETE FROM operation_work_syn WHERE work_token = 'hang_tag';
DELETE FROM operation_work WHERE token = 'hang_tag';
