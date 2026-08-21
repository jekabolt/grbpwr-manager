-- +migrate Up

-- КАТАЛОГ РАБОТ — ЧЕТЫРЕ ТАБЛИЦЫ И 53 СТРОКИ ДАННЫХ. Файл АДДИТИВЕН ЦЕЛИКОМ: ни одна
-- существующая таблица не читается и не правится, ни один словарный CHECK не трогается, ни один
-- член ни одного enum не добавляется. Откат бинаря на до-0329 безопасен — эти таблицы никто, кроме
-- нового кода, не читает.
-- 
-- ЗАЧЕМ ВООБЩЕ ТАБЛИЦА, А НЕ СПИСОК В БАНДЛЕ. Сегодня «вид операции» существует ровно одним
-- способом: пятьюдесятью тремя строками в клиентском `operation-kinds.ts`, которые НИГДЕ НЕ
-- ХРАНЯТСЯ — экран каждый раз заново выводит вид из пары (глагол, машинка). Из-за этого сервер не
-- может ни проверить «такая работа существует», ни сказать «у этой работы спрашивают длину
-- прорези», ни отдать поиск по русскому слову технолога. Каталог на сервере даёт всё три, и
-- главный выигрыш — FK на `operation_work.token` (0330): клиент физически не может предложить
-- работу, которой нет.
-- 
-- РАБОТА ЕДЕТ ПО ПРОВОДУ СТРОКОЙ-ТОКЕНОМ, А НЕ ЧЛЕНОМ ENUM. Незнакомый член proto-enum protojson
-- молча выбрасывает (инцидент Ж7); строку не теряет никакой маршалер. Поэтому словарь работ —
-- ДАННЫЕ, а не контракт, и растёт INSERT-миграцией, а не волной перегенерации клиента.
-- 
-- РАЗДЕЛЕНИЕ ПО ИЗМЕНЯЕМОСТИ, И ОНО ЖЁСТКОЕ:
--   token, verb   — ИДЕНТИЧНОСТЬ. Минтятся здесь один раз и НАВСЕГДА. `verb` войдёт в проекцию
--                   дайджеста через правило когерентности (0330), поэтому его правка задним
--                   числом раздваивает отпечаток подписанной карточки. Пара token→verb заморожена
--                   хеш-суммой в guard-тесте (internal/store/migrationlint): будущая правка обязана
--                   осознанно поменять константу и объяснить это в комментарии.
--   label, stage,
--   sort, syn     — ПРЕДСТАВЛЕНИЕ. Правится дёшево, отдельной UPDATE-миграцией, в дайджест не
--                   входит НИКОГДА (прецедент: цвет выноски не хешируется).
-- 
-- СНЯТИЕ ПУНКТА — RETIRE, НИКОГДА DELETE. `retired_at` гасит пункт в пикере, но оставляет его
-- читаемым: строка шага, уже несущая этот токен, обязана открываться и сохраняться. FK у детей
-- намеренно БЕЗ каскада (RESTRICT по умолчанию) — попытка удалить работу с синонимами или
-- машинками обязана падать громко, а не выносить их молча.
-- 
-- СЛОВАРИ stage / machine_mode / verb / machine_type НЕ ЗАКРЫТЫ CHECK'ОМ, И ЭТО РЕШЕНИЕ, А НЕ
-- ЗАБЫВЧИВОСТЬ. `ADD CONSTRAINT CHECK` в MySQL 8 копирует таблицу целиком, а потолок на ВЕСЬ
-- прогон миграций зашит в internal/store/store.go пятью минутами; заводить ради каждого словарика
-- пятую и шестую справочные таблицы план запретил прямо («4 таблицы, и только они»). Вместо этого
-- словари стережёт guard-тест над ТЕКСТОМ этого файла: verb сверяется с entity.OperationTypeTokens,
-- машинки — с entity.MachineTypeTokens, stage — с закрытым списком восьми, machine_mode — с
-- fixed|ask|none. Тест не ложно-зелёный: у него есть парный тест мутаций, который ломает разбор
-- нарочно и требует красноты.
-- 
-- ИМЕНА ПУНКТОВ, КОТОРЫЕ СЕГОДНЯ ВРУТ, СЕЮТСЯ КАК ЕСТЬ. `join_lockstitch` называет работу по
-- машинке и проваливает тест подстановки (переставь на оверлок — имя перестанет быть правдой), но
-- ПЕРЕИМЕНОВАНИЕ ЯРЛЫКА — задача 0331, а токен не меняется никогда. Сеять «правильное» имя здесь
-- значило бы развести токен и ярлык до того, как владелец их увидел.
-- 
-- ОДНА ТОНКОСТЬ ИМЕНОВАНИЯ, КОТОРУЮ ПОЗЖЕ УЖЕ НЕ ПОЧИНИТЬ: пункт G6 «Ease in» — это СУТЮЖИВАНИЕ
-- УТЮГОМ (verb = press), а 0331 заводит МАШИННУЮ посадку оката под естественным именем `ease_in`
-- (verb = machine). Два разных приёма претендуют на одно слово, поэтому ВСЁ семейство ВТО минтится
-- с префиксом `press_` (press_flat, press_steam, press_ease_in, …), и голое `ease_in` остаётся
-- свободным для 0331. Токены неизменяемы — столкновение чинится только до первого INSERT'а.
-- 
-- КАРТА «id пункта клиента → токен» (operation-kinds.ts, ветка feat/operation-kinds-ui). В файле
-- клиента 54 записи; сеются 53. Пятьдесят четвёртая — G0 «Press (action not recorded)» — помечена
-- `stateOnly` и не предлагается НИКОГДА: она называет ОТСУТСТВИЕ записанного приёма у доволновой
-- строки ВТО, а отсутствие не выбирают и в каталог работ не кладут.
-- 
--   A1  join_lockstitch           A9  amf_handstitch_imitation   C3  bartack
--   A2  topstitch                 A10 machine_other              C4  embroidery
--   A3  overlock_serge            B1  bind_tape_edge             D1  patch_pocket_automat
--   A4  coverstitch               B2  attach_elastic             D2  welt_pocket_automat
--   A5  coverlock                 B3  set_zip                    D3  template_automat
--   A6  chainstitch               B4  gather_ease                D4  collar_cuff_automat
--   A7  blindhem                  B5  attach_label               D5  sleeve_setting_automat
--   A8  zigzag                    C1  buttonhole                 D6  waistband_automat
--                                 C2  button_attach              E1  tape_seam_hot_air
--   E2  ultrasonic_weld           G1  press_flat                 H1  print_transfer
--   F0  set_hardware              G2  press_to_one_side          I1  trim_allowance
--   F1  snap_press_stud           G3  press_open                 I2  thread_trim
--   F2  rivet_burr                G4  press_steam                I3  clean
--   F3  eyelet_grommet            G5  press_final                I4  inspect_inline
--   F4  buckle_slider             G6  press_ease_in              I5  quality_control_final
--   F5  hardware_sewn             G7  press_stretch              I6  fold
--                                 G8  press_mould                I7  pack
--                                 G9  fuse                       I8  wet_process
--                                                                J1  hand_work
--                                                                J2  other
-- 
-- СИНОНИМЫ — ЧЕРНОВЫЕ СЛОВА ЦЕХА, и они здесь ровно затем, чтобы владелец печатал СВОЁ слово
-- по-русски и находил английский пункт («моско» → Hem — rolled, «закрепка» → Bartack). Владелец
-- надиктует правку (вопрос В3 плана) — она уедет отдельной UPDATE-миграцией представления, это
-- дёшево. Требование, которое стережёт тест: у КАЖДОЙ работы есть хотя бы одно кириллическое и
-- хотя бы одно латинское слово.
-- 
-- ИДЕМПОТЕНТНОСТЬ. MySQL 8 автокоммитит DDL: падение в середине файла оставляет схему
-- полуприменённой БЕЗ строки в gorp_migrations, и следующий старт перечитывает файл С ВЕРХА.
-- Поэтому CREATE TABLE IF NOT EXISTS и `ON DUPLICATE KEY UPDATE <ключ> = <ключ>` — повтор
-- является no-op. INSERT IGNORE НЕ используется намеренно: он проглотил бы и нарушение FK, то есть
-- синоним с опечаткой в токене исчез бы молча.

CREATE TABLE IF NOT EXISTS operation_work (
  token VARCHAR(32) COLLATE utf8mb4_bin NOT NULL COMMENT 'идентичность работы; минтится один раз и навсегда, в дайджест уезжает ТОЛЬКО он. _bin: сравнение побайтное, как в Go',
  verb VARCHAR(16) COLLATE utf8mb4_bin NOT NULL COMMENT 'глагол шага: токен из entity.OperationTypeTokens. Часть идентичности — правка задним числом раздваивает отпечаток',
  stage VARCHAR(16) COLLATE utf8mb4_bin NOT NULL COMMENT 'группа по СТАДИИ работы, не по железу: join_seam|edges_hems|closures|hardware|pressing|print_decorate|finishing|other',
  label VARCHAR(64) NOT NULL COMMENT 'имя в интерфейсе и на печатном листе; представление, правится UPDATE-миграцией',
  machine_mode VARCHAR(8) COLLATE utf8mb4_bin NOT NULL COMMENT 'fixed = машинка следует из работы; ask = работа живёт на нескольких, спрашиваем; none = ось «на чём» у этого глагола не машинная',
  default_machine VARCHAR(32) COLLATE utf8mb4_bin NULL COMMENT 'чем заполняется «на чём»; NOT NULL при fixed и ask, NULL при none',
  sort SMALLINT NOT NULL COMMENT 'порядок в пикере; представление',
  retired_at TIMESTAMP NULL DEFAULT NULL COMMENT 'снятие пункта: в пикере не предлагается, но старые строки шагов с этим токеном читаются и сохраняются. DELETE не бывает никогда',
  PRIMARY KEY (token),
  INDEX idx_operation_work_stage_sort (stage, sort)
) ENGINE = InnoDB DEFAULT CHARSET = utf8mb4 COMMENT 'Каталог работ (видов операций) — серверные данные, не контракт';

CREATE TABLE IF NOT EXISTS operation_work_machine (
  work_token VARCHAR(32) COLLATE utf8mb4_bin NOT NULL,
  machine_type VARCHAR(32) COLLATE utf8mb4_bin NOT NULL COMMENT 'токен entity.MachineTypeTokens; допустимая машинка этой работы',
  PRIMARY KEY (work_token, machine_type),
  CONSTRAINT fk_operation_work_machine_work FOREIGN KEY (work_token)
    REFERENCES operation_work (token)
) ENGINE = InnoDB DEFAULT CHARSET = utf8mb4 COMMENT 'Допустимые машинки работы — ответ на «на чём»';

CREATE TABLE IF NOT EXISTS operation_work_syn (
  work_token VARCHAR(32) COLLATE utf8mb4_bin NOT NULL,
  syn VARCHAR(64) NOT NULL COMMENT 'слово поиска, RU или EN. Коллация ДЕФОЛТНАЯ ai_ci (не _bin): «Закрепка» и «закрепка» обязаны быть одним синонимом, а не двумя',
  PRIMARY KEY (work_token, syn),
  INDEX idx_operation_work_syn_syn (syn),
  CONSTRAINT fk_operation_work_syn_work FOREIGN KEY (work_token)
    REFERENCES operation_work (token)
) ENGINE = InnoDB DEFAULT CHARSET = utf8mb4 COMMENT 'Синонимы поиска работ (RU/EN) — живут на сервере, а не в клиентском бандле';

CREATE TABLE IF NOT EXISTS operation_work_default (
  work_token VARCHAR(32) COLLATE utf8mb4_bin NOT NULL,
  field VARCHAR(32) COLLATE utf8mb4_bin NOT NULL COMMENT 'имя поля свойств вида; закрытый Go-реестр, машинные и ВТО-настройки запрещены (у них свой механизм наследования 0306)',
  value VARCHAR(64) NOT NULL COMMENT 'значение дефолта; валидируется теми же правилами поля, что и на шаге',
  updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
  PRIMARY KEY (work_token, field),
  CONSTRAINT fk_operation_work_default_work FOREIGN KEY (work_token)
    REFERENCES operation_work (token)
) ENGINE = InnoDB DEFAULT CHARSET = utf8mb4 COMMENT 'Глобальные дефолты свойств вида — ЕДИНСТВЕННАЯ таблица каталога с рантайм-записью (RPC), сидом не наполняется';

-- СИД РАБОТ. 53 строки, порядок = порядок пикера (`sort` шагает десятками, чтобы вставка
-- будущей работы между двумя существующими не требовала переномера всей таблицы).
-- Повтор файла — no-op: `sort = sort` это присваивание колонки самой себе, ноль изменённых строк.
INSERT INTO operation_work (token, verb, stage, label, machine_mode, default_machine, sort) VALUES
  ('join_lockstitch', 'machine', 'join_seam', 'Join — lockstitch', 'fixed', 'lockstitch', 10),
  ('topstitch', 'machine', 'join_seam', 'Topstitch', 'ask', 'lockstitch', 20),
  ('overlock_serge', 'machine', 'join_seam', 'Overlock / serge', 'fixed', 'overlock', 30),
  ('coverstitch', 'machine', 'join_seam', 'Coverstitch', 'fixed', 'coverstitch', 40),
  ('coverlock', 'machine', 'join_seam', 'Coverlock', 'fixed', 'coverlock', 50),
  ('chainstitch', 'machine', 'join_seam', 'Chainstitch', 'fixed', 'chainstitch', 60),
  ('blindhem', 'machine', 'edges_hems', 'Blindhem', 'fixed', 'blindstitch', 70),
  ('zigzag', 'machine', 'join_seam', 'Zigzag', 'fixed', 'zigzag', 80),
  ('amf_handstitch_imitation', 'machine', 'join_seam', 'AMF hand-stitch imitation', 'fixed', 'handstitch_imitation', 90),
  ('machine_other', 'machine', 'other', 'Machine — other (see note)', 'fixed', 'other', 100),
  ('bind_tape_edge', 'machine', 'edges_hems', 'Bind / tape edge', 'fixed', 'binding_taping', 110),
  ('attach_elastic', 'machine', 'edges_hems', 'Attach elastic', 'fixed', 'elastic_attach', 120),
  ('set_zip', 'machine', 'closures', 'Set zip', 'fixed', 'zipper_setting', 130),
  ('gather_ease', 'machine', 'join_seam', 'Gather / ease', 'fixed', 'gathering', 140),
  ('attach_label', 'machine', 'finishing', 'Attach label', 'fixed', 'lockstitch', 150),
  ('buttonhole', 'machine', 'closures', 'Buttonhole', 'fixed', 'buttonhole', 160),
  ('button_attach', 'machine', 'closures', 'Button attach', 'fixed', 'button_attach', 170),
  ('bartack', 'machine', 'join_seam', 'Bartack', 'fixed', 'bartack', 180),
  ('embroidery', 'machine', 'print_decorate', 'Embroidery', 'fixed', 'embroidery', 190),
  ('patch_pocket_automat', 'machine', 'join_seam', 'Patch-pocket automat', 'fixed', 'patch_pocket_auto', 200),
  ('welt_pocket_automat', 'machine', 'join_seam', 'Welt-pocket automat', 'fixed', 'welt_pocket_auto', 210),
  ('template_automat', 'machine', 'join_seam', 'Template automat', 'fixed', 'template_auto', 220),
  ('collar_cuff_automat', 'machine', 'join_seam', 'Collar / cuff automat', 'fixed', 'collar_cuff_auto', 230),
  ('sleeve_setting_automat', 'machine', 'join_seam', 'Sleeve-setting automat', 'fixed', 'sleeve_setting_auto', 240),
  ('waistband_automat', 'machine', 'join_seam', 'Waistband automat', 'fixed', 'waistband_auto', 250),
  ('tape_seam_hot_air', 'machine', 'join_seam', 'Tape seam (hot air)', 'fixed', 'seam_taping', 260),
  ('ultrasonic_weld', 'machine', 'join_seam', 'Ultrasonic weld', 'fixed', 'ultrasonic_welder', 270),
  ('set_hardware', 'hardware_set', 'hardware', 'Set hardware', 'none', NULL, 280),
  ('snap_press_stud', 'hardware_set', 'hardware', 'Snap / press stud', 'none', NULL, 290),
  ('rivet_burr', 'hardware_set', 'hardware', 'Rivet / burr', 'none', NULL, 300),
  ('eyelet_grommet', 'hardware_set', 'hardware', 'Eyelet / grommet', 'none', NULL, 310),
  ('buckle_slider', 'hardware_set', 'hardware', 'Buckle / slider (threaded)', 'none', NULL, 320),
  ('hardware_sewn', 'hardware_set', 'hardware', 'Attach hardware — sewn', 'none', NULL, 330),
  ('press_flat', 'press', 'pressing', 'Press flat', 'none', NULL, 340),
  ('press_to_one_side', 'press', 'pressing', 'Press to one side', 'none', NULL, 350),
  ('press_open', 'press_open', 'pressing', 'Press open', 'none', NULL, 360),
  ('press_steam', 'press', 'pressing', 'Steam', 'none', NULL, 370),
  ('press_final', 'press', 'pressing', 'Final press', 'none', NULL, 380),
  ('press_ease_in', 'press', 'pressing', 'Ease in', 'none', NULL, 390),
  ('press_stretch', 'press', 'pressing', 'Stretch', 'none', NULL, 400),
  ('press_mould', 'press', 'pressing', 'Mould', 'none', NULL, 410),
  ('fuse', 'fusing', 'pressing', 'Fuse', 'none', NULL, 420),
  ('print_transfer', 'print', 'print_decorate', 'Print / transfer', 'none', NULL, 430),
  ('trim_allowance', 'trim', 'finishing', 'Trim allowance', 'none', NULL, 440),
  ('thread_trim', 'thread_trim', 'finishing', 'Thread trim (tails)', 'none', NULL, 450),
  ('clean', 'clean', 'finishing', 'Clean', 'none', NULL, 460),
  ('inspect_inline', 'inspect', 'finishing', 'Inspection (in-line)', 'none', NULL, 470),
  ('quality_control_final', 'inspect', 'finishing', 'Quality control (final)', 'none', NULL, 480),
  ('fold', 'fold', 'finishing', 'Fold', 'none', NULL, 490),
  ('pack', 'pack', 'finishing', 'Pack', 'none', NULL, 500),
  ('wet_process', 'wet_process', 'finishing', 'Wet process', 'none', NULL, 510),
  ('hand_work', 'handwork', 'other', 'Hand work', 'none', NULL, 520),
  ('other', 'other', 'other', 'Other (see note)', 'none', NULL, 530)
ON DUPLICATE KEY UPDATE sort = sort;

-- ДОПУСТИМЫЕ МАШИНКИ. 26 работ с machine_mode = fixed несут ровно одну строку (ту же, что стоит в
-- default_machine), одна работа с machine_mode = ask — пять; 26 работ с machine_mode = none не
-- несут ни одной. Всего 31.
INSERT INTO operation_work_machine (work_token, machine_type) VALUES
  ('join_lockstitch', 'lockstitch'),
  ('topstitch', 'lockstitch'),
  ('topstitch', 'lockstitch_double_needle'),
  ('topstitch', 'chainstitch'),
  ('topstitch', 'coverstitch'),
  ('topstitch', 'handstitch_imitation'),
  ('overlock_serge', 'overlock'),
  ('coverstitch', 'coverstitch'),
  ('coverlock', 'coverlock'),
  ('chainstitch', 'chainstitch'),
  ('blindhem', 'blindstitch'),
  ('zigzag', 'zigzag'),
  ('amf_handstitch_imitation', 'handstitch_imitation'),
  ('machine_other', 'other'),
  ('bind_tape_edge', 'binding_taping'),
  ('attach_elastic', 'elastic_attach'),
  ('set_zip', 'zipper_setting'),
  ('gather_ease', 'gathering'),
  ('attach_label', 'lockstitch'),
  ('buttonhole', 'buttonhole'),
  ('button_attach', 'button_attach'),
  ('bartack', 'bartack'),
  ('embroidery', 'embroidery'),
  ('patch_pocket_automat', 'patch_pocket_auto'),
  ('welt_pocket_automat', 'welt_pocket_auto'),
  ('template_automat', 'template_auto'),
  ('collar_cuff_automat', 'collar_cuff_auto'),
  ('sleeve_setting_automat', 'sleeve_setting_auto'),
  ('waistband_automat', 'waistband_auto'),
  ('tape_seam_hot_air', 'seam_taping'),
  ('ultrasonic_weld', 'ultrasonic_welder')
ON DUPLICATE KEY UPDATE work_token = work_token;

-- СИНОНИМЫ ПОИСКА. Черновые слова цеха; у каждой работы есть и кириллическое, и латинское.
-- Повторы поперёк работ ЗАКОННЫ и полезны: «automat» находит все шесть автоматов сразу.
INSERT INTO operation_work_syn (work_token, syn) VALUES
  ('join_lockstitch', 'стачать'),
  ('join_lockstitch', 'стачной'),
  ('join_lockstitch', 'стачной шов'),
  ('join_lockstitch', 'прямострочка'),
  ('join_lockstitch', 'join'),
  ('join_lockstitch', 'seam'),
  ('join_lockstitch', 'lockstitch'),
  ('topstitch', 'отстрочить'),
  ('topstitch', 'отстрочка'),
  ('topstitch', 'топстич'),
  ('topstitch', 'в край'),
  ('topstitch', 'отделочная строчка'),
  ('topstitch', 'topstitch'),
  ('topstitch', 'edgestitch'),
  ('overlock_serge', 'обметать'),
  ('overlock_serge', 'обмётка'),
  ('overlock_serge', 'оверлок'),
  ('overlock_serge', '504'),
  ('overlock_serge', 'overlock'),
  ('overlock_serge', 'serge'),
  ('coverstitch', 'распошивалка'),
  ('coverstitch', 'распошивальный шов'),
  ('coverstitch', 'плоский шов'),
  ('coverstitch', 'coverstitch'),
  ('coverlock', 'коверлок'),
  ('coverlock', 'распошивальный оверлок'),
  ('coverlock', 'coverlock'),
  ('chainstitch', 'цепной шов'),
  ('chainstitch', 'цепная строчка'),
  ('chainstitch', '401'),
  ('chainstitch', 'chainstitch'),
  ('blindhem', 'потайной подгиб'),
  ('blindhem', 'потайная строчка'),
  ('blindhem', 'подшивочная'),
  ('blindhem', 'blindhem'),
  ('blindhem', 'blindstitch'),
  ('zigzag', 'зигзаг'),
  ('zigzag', 'зигзагом'),
  ('zigzag', 'зигзагообразная строчка'),
  ('zigzag', 'zigzag'),
  ('amf_handstitch_imitation', 'имитация ручной строчки'),
  ('amf_handstitch_imitation', 'амф'),
  ('amf_handstitch_imitation', 'ручная имитация'),
  ('amf_handstitch_imitation', 'amf'),
  ('amf_handstitch_imitation', 'hand-stitch imitation'),
  ('amf_handstitch_imitation', 'pick stitch'),
  ('machine_other', 'другая машина'),
  ('machine_other', 'машинная прочее'),
  ('machine_other', 'machine other'),
  ('machine_other', 'other machine'),
  ('bind_tape_edge', 'окантовать'),
  ('bind_tape_edge', 'окантовка'),
  ('bind_tape_edge', 'бейка'),
  ('bind_tape_edge', 'кант'),
  ('bind_tape_edge', 'тесьма'),
  ('bind_tape_edge', 'bind'),
  ('bind_tape_edge', 'binding'),
  ('bind_tape_edge', 'tape edge'),
  ('attach_elastic', 'притачать резинку'),
  ('attach_elastic', 'резинка'),
  ('attach_elastic', 'эластичная лента'),
  ('attach_elastic', 'elastic'),
  ('attach_elastic', 'attach elastic'),
  ('set_zip', 'втачать молнию'),
  ('set_zip', 'молния'),
  ('set_zip', 'змейка'),
  ('set_zip', 'zip'),
  ('set_zip', 'zipper'),
  ('gather_ease', 'присборить'),
  ('gather_ease', 'сборка'),
  ('gather_ease', 'оборка'),
  ('gather_ease', 'посадка'),
  ('gather_ease', 'посадить'),
  ('gather_ease', 'gather'),
  ('gather_ease', 'ease'),
  ('attach_label', 'притачать этикетку'),
  ('attach_label', 'этикетка'),
  ('attach_label', 'бирка'),
  ('attach_label', 'ярлык'),
  ('attach_label', 'label'),
  ('attach_label', 'attach label'),
  ('buttonhole', 'петля'),
  ('buttonhole', 'обметать петлю'),
  ('buttonhole', 'петельная'),
  ('buttonhole', 'прорезная петля'),
  ('buttonhole', 'buttonhole'),
  ('button_attach', 'пришить пуговицу'),
  ('button_attach', 'пуговица'),
  ('button_attach', 'пуговичная'),
  ('button_attach', 'button'),
  ('button_attach', 'button attach'),
  ('bartack', 'закрепка'),
  ('bartack', 'поставить закрепку'),
  ('bartack', 'закрепить'),
  ('bartack', 'bartack'),
  ('bartack', 'tack'),
  ('embroidery', 'вышивка'),
  ('embroidery', 'вышить'),
  ('embroidery', 'вышивальная'),
  ('embroidery', 'embroidery'),
  ('patch_pocket_automat', 'накладной карман автомат'),
  ('patch_pocket_automat', 'автомат накладного кармана'),
  ('patch_pocket_automat', 'patch pocket'),
  ('patch_pocket_automat', 'automat'),
  ('welt_pocket_automat', 'прорезной карман автомат'),
  ('welt_pocket_automat', 'листочка'),
  ('welt_pocket_automat', 'welt pocket'),
  ('welt_pocket_automat', 'automat'),
  ('template_automat', 'шаблонный автомат'),
  ('template_automat', 'шаблон'),
  ('template_automat', 'template'),
  ('template_automat', 'automat'),
  ('collar_cuff_automat', 'автомат воротника'),
  ('collar_cuff_automat', 'воротник манжета автомат'),
  ('collar_cuff_automat', 'collar cuff'),
  ('collar_cuff_automat', 'automat'),
  ('sleeve_setting_automat', 'втачивание рукава автомат'),
  ('sleeve_setting_automat', 'рукав автомат'),
  ('sleeve_setting_automat', 'sleeve setting'),
  ('sleeve_setting_automat', 'automat'),
  ('waistband_automat', 'пояс автомат'),
  ('waistband_automat', 'притачать пояс автомат'),
  ('waistband_automat', 'waistband'),
  ('waistband_automat', 'automat'),
  ('tape_seam_hot_air', 'проклеить шов'),
  ('tape_seam_hot_air', 'проклейка шва'),
  ('tape_seam_hot_air', 'горячий воздух'),
  ('tape_seam_hot_air', 'seam taping'),
  ('tape_seam_hot_air', 'hot air'),
  ('ultrasonic_weld', 'ультразвуковая сварка'),
  ('ultrasonic_weld', 'сварить шов'),
  ('ultrasonic_weld', 'ультразвук'),
  ('ultrasonic_weld', 'ultrasonic'),
  ('ultrasonic_weld', 'weld'),
  ('set_hardware', 'поставить фурнитуру'),
  ('set_hardware', 'фурнитура'),
  ('set_hardware', 'hardware'),
  ('set_hardware', 'set hardware'),
  ('snap_press_stud', 'кнопка'),
  ('snap_press_stud', 'поставить кнопку'),
  ('snap_press_stud', 'снап'),
  ('snap_press_stud', 'snap'),
  ('snap_press_stud', 'press stud'),
  ('rivet_burr', 'хольнитен'),
  ('rivet_burr', 'заклёпка'),
  ('rivet_burr', 'поставить заклёпку'),
  ('rivet_burr', 'rivet'),
  ('rivet_burr', 'burr'),
  ('eyelet_grommet', 'люверс'),
  ('eyelet_grommet', 'блочка'),
  ('eyelet_grommet', 'поставить люверс'),
  ('eyelet_grommet', 'eyelet'),
  ('eyelet_grommet', 'grommet'),
  ('buckle_slider', 'пряжка'),
  ('buckle_slider', 'регулятор'),
  ('buckle_slider', 'рамка'),
  ('buckle_slider', 'продеть'),
  ('buckle_slider', 'buckle'),
  ('buckle_slider', 'slider'),
  ('hardware_sewn', 'пришивная фурнитура'),
  ('hardware_sewn', 'пришить фурнитуру'),
  ('hardware_sewn', 'sewn hardware'),
  ('hardware_sewn', 'sew on'),
  ('press_flat', 'приутюжить'),
  ('press_flat', 'приутюживание'),
  ('press_flat', 'утюжить'),
  ('press_flat', 'press flat'),
  ('press_flat', 'press'),
  ('press_to_one_side', 'заутюжить'),
  ('press_to_one_side', 'заутюжка'),
  ('press_to_one_side', 'на одну сторону'),
  ('press_to_one_side', 'press to one side'),
  ('press_open', 'разутюжить'),
  ('press_open', 'разутюжка'),
  ('press_open', 'вразутюжку'),
  ('press_open', 'press open'),
  ('press_steam', 'отпарить'),
  ('press_steam', 'отпаривание'),
  ('press_steam', 'пар'),
  ('press_steam', 'steam'),
  ('press_final', 'окончательная вто'),
  ('press_final', 'финальная утюжка'),
  ('press_final', 'отутюжить готовое'),
  ('press_final', 'final press'),
  ('press_ease_in', 'сутюжить'),
  ('press_ease_in', 'сутюживание'),
  ('press_ease_in', 'посадить утюгом'),
  ('press_ease_in', 'ease in'),
  ('press_stretch', 'оттянуть'),
  ('press_stretch', 'оттяжка'),
  ('press_stretch', 'растянуть утюгом'),
  ('press_stretch', 'stretch'),
  ('press_mould', 'формовать'),
  ('press_mould', 'отформовать'),
  ('press_mould', 'формование'),
  ('press_mould', 'mould'),
  ('press_mould', 'mold'),
  ('fuse', 'продублировать'),
  ('fuse', 'дублирование'),
  ('fuse', 'клеевая'),
  ('fuse', 'дублерин'),
  ('fuse', 'fuse'),
  ('fuse', 'fusing'),
  ('print_transfer', 'печать'),
  ('print_transfer', 'напечатать'),
  ('print_transfer', 'термоперенос'),
  ('print_transfer', 'шелкография'),
  ('print_transfer', 'print'),
  ('print_transfer', 'transfer'),
  ('trim_allowance', 'подрезать припуск'),
  ('trim_allowance', 'подрезка'),
  ('trim_allowance', 'высечь'),
  ('trim_allowance', 'trim'),
  ('trim_allowance', 'trim allowance'),
  ('thread_trim', 'обрезать концы ниток'),
  ('thread_trim', 'чистка ниток'),
  ('thread_trim', 'концы ниток'),
  ('thread_trim', 'thread trim'),
  ('thread_trim', 'trim tails'),
  ('clean', 'чистка изделия'),
  ('clean', 'почистить'),
  ('clean', 'очистить'),
  ('clean', 'clean'),
  ('inspect_inline', 'межоперационный контроль'),
  ('inspect_inline', 'проверить'),
  ('inspect_inline', 'контроль'),
  ('inspect_inline', 'inline inspection'),
  ('inspect_inline', 'in-line'),
  ('quality_control_final', 'финальный контроль'),
  ('quality_control_final', 'отк'),
  ('quality_control_final', 'приёмка'),
  ('quality_control_final', 'quality control'),
  ('quality_control_final', 'final qc'),
  ('fold', 'сложить'),
  ('fold', 'складывание'),
  ('fold', 'fold'),
  ('pack', 'упаковать'),
  ('pack', 'упаковка'),
  ('pack', 'pack'),
  ('pack', 'packing'),
  ('wet_process', 'мокрая обработка'),
  ('wet_process', 'влажная обработка'),
  ('wet_process', 'стирка'),
  ('wet_process', 'wet process'),
  ('wet_process', 'wash'),
  ('hand_work', 'ручная работа'),
  ('hand_work', 'вручную'),
  ('hand_work', 'руками'),
  ('hand_work', 'hand work'),
  ('hand_work', 'handwork'),
  ('other', 'другое'),
  ('other', 'прочее'),
  ('other', 'other'),
  ('other', 'see note')
ON DUPLICATE KEY UPDATE work_token = work_token;

-- operation_work_default НЕ СЕЕТСЯ. Дефолты — данные пользователя, а не идентичность каталога:
-- они появляются жестом «запомнить как дефолт» через RPC. Засеять их здесь значило бы объявить
-- «так решил технолог» то, чего он не говорил.

-- +migrate Down

-- Down — ИНСТРУМЕНТ РАЗРАБОТКИ (up → down → up на одноразовой базе), не машина времени для прода.
-- Порядок обратный порядку создания: сначала дети, потом родитель, иначе FK не даст снести
-- operation_work. Если к моменту отката уже применена 0330 (колонка work с FK на этот каталог) —
-- DROP родителя честно упадёт на FK, и это правильный исход: снести словарь из-под заполненных
-- строк шагов нельзя, откатывать надо сначала 0330.
DROP TABLE IF EXISTS operation_work_default;
DROP TABLE IF EXISTS operation_work_syn;
DROP TABLE IF EXISTS operation_work_machine;
DROP TABLE IF EXISTS operation_work;
