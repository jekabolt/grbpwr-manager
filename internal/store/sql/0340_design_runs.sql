-- Полоса DESIGN, кусок 1 из 7: прогоны генерации, попытки платного вызова, пачки загрузки и
-- картинки полосы.
--
-- ЧТО ЭТО ЗА ПОДСИСТЕМА. Флэт (технический эскиз) сегодня рождается четырьмя путями сразу:
-- Illustrator, CLO, фотография вместо чертежа, снимок бумажного эскиза. Генерация встаёт ПЯТЫМ
-- источником того же артефакта, а не заменой чего-то. Отсюда весь ярус: строка задания
-- (`design_run`), её платные попытки (`design_run_attempt`), пачка ручной загрузки
-- (`design_batch`) и сам артефакт (`design_picture`), у которого источник — колонка, а не догадка.
--
-- ПОЧЕМУ ПОПЫТКА — ОТДЕЛЬНАЯ СТРОКА, А НЕ ПОЛЕ ПРОГОНА. Строка истории обязана уметь сказать
-- «попытка 1 оплачена, ответ не доехал; попытка 2 привезла картинки». Без отдельной строки
-- `price_actual` показывает цену ПОСЛЕДНЕЙ попытки, полоса дневного бюджета недосчитывает ретраи,
-- и фича «сколько мы тратим на ИИ» отвечает неправдой. Прецедент в этом же репо:
-- `internal/apisrv/admin/techcard_analysis.go` пишет usage и у провалившегося прогона.
--
-- FK-ПОЛИТИКА (главное в файле). `DeleteTechCard` — ОДИН голый `DELETE FROM tech_card`
-- (`internal/store/techcard/techcard.go:697-733`), всё остальное делают каскады, а любой не
-- перечисленный явно RESTRICT поднимет 1451 и покажет человеку «still referenced by another
-- record — remove the referencing record first». Удалить ему будет нечего: ссылающиеся строки —
-- собственная полоса карточки, а RPC удаления `DesignPicture` в контракте нет вовсе. Отсюда
-- правило волны: НИ ОДНОГО RESTRICT между двумя таблицами, каждая из которых каскадится от
-- `tech_card`. Два осознанных исключения:
--   * `design_picture.media_id → media` — RESTRICT: `media` от карточки НЕ каскадится, это сторож
--     байтов, и он ОБЯЗАН быть виден в `GetMediaUsage` (`TestMediaUsageRegistryCoversSchema`
--     диффит реестр против живых FK в `media(id)` и краснеет на незарегистрированной колонке);
--   * `design_picture.derived_from → design_picture` — FK НЕТ ВОВСЕ, только `KEY`. Самоссылка на
--     таблице, которая сама принимает входящий каскад, — единственная комбинация здесь, которую
--     нельзя доказать на бумаге; целостность держит Go, и выигрыш от FK меньше риска 1451 в
--     ЕДИНСТВЕННОЙ операции удаления карточки. Проверяется прогоном на бете (карточка → прогон →
--     композит → кроп → флэттен → слот → минт версии → DELETE карточки должен пройти) с
--     отрицательным контролем: временно вернуть RESTRICT и показать 1451.
--
-- СЛОВАРНЫЕ КОЛОНКИ — VARCHAR БЕЗ CHECK. `kind`, `status`, `source_class`, `state` будут расти
-- (`source_class` уже расширялся на `drawn` ещё до первой строки), а поздний ADD CHECK на
-- потолстевшей таблице — это COPY таблицы под пятиминутным потолком всего прогона миграций.
-- Словарь проверяет Go, где отказ называет карточку и значение, а не сырой 3819 с именем колонки.
--
-- ТИПЫ, ПРИНЯТЫЕ ЯВНО, А НЕ МОЛЧА. `ordinal` и `files_count` — SMALLINT UNSIGNED, а не TINYINT:
-- 255 кадров 3D-турнтейбла и 255 файлов в пачке — это решение, которое TINYINT принял бы за нас
-- молча. Ключи FK на `tech_card(id)` и `media(id)` — знаковый `INT`, потому что обе колонки-цели
-- знаковые (`0067:13`, `0001:513`), а расхождение знаковости даёт 3780 на создании FK.
--
-- ИДЕМПОТЕНТНОСТЬ: каждая таблица заводится через `IF NOT EXISTS` — это требование красного теста
-- `internal/store/migrationlint/idempotency_test.go:41`. MySQL коммитит DDL пооператорно, поэтому
-- файл, упавший на второй таблице, не запишется в журнал миграций, и следующий старт зайдёт с
-- начала и упрётся в уже созданную первую.
--
-- ИНЕРТНА для живого прода: ни один существующий читатель этих таблиц не знает, ни одна живая
-- таблица не изменена. Откат бинаря после применения безопасен.

-- +migrate Up

-- design_run: строка задания генерации. Она же — строка истории на экране полосы.
CREATE TABLE IF NOT EXISTS design_run (
    id INT UNSIGNED PRIMARY KEY AUTO_INCREMENT,
    tech_card_id INT NOT NULL,
    kind VARCHAR(16) NOT NULL COMMENT 'flat|render|threed|draft_idea — какое состояние студии породило строку; словарь растёт, CHECK намеренно нет',
    status VARCHAR(16) NOT NULL DEFAULT 'pending' COMMENT 'pending|running|done|failed|cancelled',
    client_request_id CHAR(36) NOT NULL COMMENT 'идемпотентность ЧЕЛОВЕКА: двойной клик GENERATE = один прогон и один платёж',
    provider_idempotency_key CHAR(36) NOT NULL COMMENT 'ключ, уходящий ПРОВАЙДЕРУ; один на прогон, СТАБИЛЕН между попытками — иначе ретрай платит второй раз',
    profile_name VARCHAR(64) NOT NULL DEFAULT '' COMMENT 'профиль промпта; подпись «flat-3view @ v4» в истории',
    profile_version INT NOT NULL DEFAULT 0 COMMENT 'пин версии профиля на момент запуска',
    ask TEXT NULL COMMENT 'фраза дельты, набранная человеком — подпись строки в истории',
    params JSON NULL COMMENT 'DesignRunParams: виды, layout, рецепт цвета, 3D, fix_target; читается путями (params->>"$.colour")',
    inputs JSON NULL COMMENT 'DesignInputSnapshot: снимок входов НА МОМЕНТ ЗАПУСКА; морозит media_id, а НЕ URL — объекты переезжают. Снимок = провенанс, а не владение: в реестр использования медиа он намеренно НЕ попадает',
    fit_at_launch VARCHAR(32) NULL COMMENT 'посадка, которую видела модель; бейдж «fit slim ≠ card oversized»',
    rrev INT NOT NULL DEFAULT 0 COMMENT 'подпись r4 в истории цвета: MAX+1 по карточке среди render-прогонов',
    requested_outputs SMALLINT UNSIGNED NOT NULL DEFAULT 0 COMMENT 'сколько кадров просили — «done · 2 of 3» при частичном ответе',
    attempt_count SMALLINT UNSIGNED NOT NULL DEFAULT 0 COMMENT 'счётчик попыток для экспоненты ретрая',
    next_attempt_at DATETIME(6) NULL DEFAULT CURRENT_TIMESTAMP(6) COMMENT 'когда строку можно брать в работу (UTC); предикат claim-машины',
    claim_token CHAR(36) NULL COMMENT 'лиза воркера; см. idx_design_run_claim',
    claim_expires_at DATETIME(6) NULL COMMENT 'срок лизы (UTC); истёкшую возвращает в pending отдельный подметальщик',
    price_estimate DECIMAL(8,4) NULL COMMENT 'оценка ДО запуска; она резервируется в design_budget_day',
    price_actual DECIMAL(8,4) NULL COMMENT 'СУММА цен попыток, а не цена последней (см. design_run_attempt)',
    currency CHAR(3) NOT NULL DEFAULT 'USD' COMMENT 'валюта денег строки; «$» в полосе бюджета не захардкожен',
    author VARCHAR(255) NOT NULL DEFAULT '' COMMENT 'username из JWT: без штампа автора гонка авторов невидима',
    cancel_requested_at DATETIME(6) NULL COMMENT 'пилюля cancelling… на идущей строке; воркер честит поле до отправки и после ответа',
    archived_at DATETIME(6) NULL COMMENT 'презентационный флаг сворачивания строки; картинки не прячет',
    archived_by VARCHAR(255) NULL,
    error_code VARCHAR(64) NULL COMMENT 'машинный код отказа: provider_result_unknown и т.п.',
    last_error TEXT NULL COMMENT 'усечённый текст ошибки провайдера; сырой ответ не хранится',
    output_text MEDIUMTEXT NULL COMMENT 'результат текстового прогона (kind=draft_idea)',
    created_at TIMESTAMP(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
    started_at DATETIME(6) NULL,
    completed_at DATETIME(6) NULL,
    UNIQUE KEY uq_design_run_client_request (client_request_id),
    KEY idx_design_run_card (tech_card_id, id),
    KEY idx_design_run_ready (status, next_attempt_at, id),
    KEY idx_design_run_claim (status, claim_expires_at),
    CONSTRAINT fk_design_run_card FOREIGN KEY (tech_card_id) REFERENCES tech_card(id) ON DELETE CASCADE
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT 'Прогон генерации дизайн-бэнда: одно задание = одна строка истории';

-- design_run_attempt: одна попытка платного вызова провайдера.
CREATE TABLE IF NOT EXISTS design_run_attempt (
    id INT UNSIGNED PRIMARY KEY AUTO_INCREMENT,
    run_id INT UNSIGNED NOT NULL,
    attempt_no TINYINT UNSIGNED NOT NULL COMMENT 'номер попытки внутри прогона, с 1; потолок ретраев — единицы, TINYINT честен',
    provider VARCHAR(32) NOT NULL COMMENT 'кому ушёл вызов',
    provider_request_id VARCHAR(128) NULL COMMENT 'сверка с биллингом провайдера; если он есть — следующий шаг Lookup, а не повторный платный вызов',
    state VARCHAR(24) NOT NULL COMMENT 'dispatching|accepted|delivered|failed|unknown — unknown = деньги, возможно, списаны, результата нет',
    price DECIMAL(8,4) NULL COMMENT 'цена ИМЕННО этой попытки; сумма по прогону едет в design_run.price_actual',
    error_code VARCHAR(64) NULL,
    started_at DATETIME(6) NOT NULL,
    finished_at DATETIME(6) NULL,
    UNIQUE KEY uq_design_run_attempt (run_id, attempt_no),
    CONSTRAINT fk_design_run_attempt_run FOREIGN KEY (run_id) REFERENCES design_run(id) ON DELETE CASCADE
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT 'Попытка платного вызова: оплаченный провал — тоже строка';

-- design_batch: пачка ручной загрузки. Один жест человека = одна пачка.
CREATE TABLE IF NOT EXISTS design_batch (
    id INT UNSIGNED PRIMARY KEY AUTO_INCREMENT,
    tech_card_id INT NOT NULL,
    client_request_id CHAR(36) NOT NULL COMMENT 'повтор после сетевого таймаута иначе заводит ВТОРУЮ пачку и второй набор картинок',
    author VARCHAR(255) NOT NULL DEFAULT '' COMMENT 'username из JWT: штамп полки «uploaded · Т. · 14:41»',
    files_count SMALLINT UNSIGNED NOT NULL DEFAULT 0 COMMENT 'SMALLINT, а не TINYINT: 255 файлов в пачке — решение, а не побочный эффект типа',
    size_bytes BIGINT UNSIGNED NOT NULL DEFAULT 0 COMMENT 'суммарный вес пачки для штампа «12.4 MB»',
    created_at TIMESTAMP(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
    UNIQUE KEY uq_design_batch_client_request (client_request_id),
    KEY idx_design_batch_card (tech_card_id, id),
    CONSTRAINT fk_design_batch_card FOREIGN KEY (tech_card_id) REFERENCES tech_card(id) ON DELETE CASCADE
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT 'Пачка загрузки: носитель когерентности «эти файлы принесли одним жестом»';

-- design_picture: сам артефакт полосы — плита, кадр, кроп, флэттен.
CREATE TABLE IF NOT EXISTS design_picture (
    id INT UNSIGNED PRIMARY KEY AUTO_INCREMENT,
    tech_card_id INT NOT NULL,
    media_id INT NOT NULL COMMENT 'байты живут в media; хеш содержимого НЕ дублируется сюда — он на media.content_hash (0336) и отдаётся джойном. Вторая колонка была бы ложным расщеплением',
    run_id INT UNSIGNED NULL COMMENT 'NULL = картинка не из прогона (ручная загрузка, кроп, флэттен)',
    batch_id INT UNSIGNED NULL COMMENT 'NULL = картинка не из пачки загрузки',
    ordinal SMALLINT UNSIGNED NOT NULL DEFAULT 0 COMMENT 'позиция в выдаче прогона либо в пачке; SMALLINT — 255 кадров турнтейбла это не потолок',
    kind VARCHAR(16) NOT NULL DEFAULT 'flat' COMMENT 'flat|render|threed — что это за кадр; словарь растёт, CHECK намеренно нет',
    ghost_view VARCHAR(32) NULL COMMENT 'вид, под который кадр встал заглушкой в выдаче (front|back|side_l|side_r|detail)',
    composite_views JSON NULL COMMENT 'виды, склеенные в ОДНОМ кадре; NULL = одиночная плита. Композит в слот не встаёт — его сначала режут',
    derived_from INT UNSIGNED NULL COMMENT 'родитель: кроп композита или флэттен слоя. FK НЕТ НАМЕРЕННО (см. шапку): самоссылка под входящим каскадом — единственный риск 1451 в DeleteTechCard, которого нельзя доказать на бумаге',
    source_class VARCHAR(16) NOT NULL DEFAULT 'uploaded' COMMENT 'generated|uploaded|drawn|derived — провенанс кадра; словарь уже расширялся на drawn',
    mixed_input TINYINT(1) NOT NULL DEFAULT 0 COMMENT '1 = кадр собран из входов разного провенанса; минт версии требует явного согласия человека',
    layer_rev INT NOT NULL DEFAULT 0 COMMENT 'ревизия слоя правки, из которой растеризован кадр (0 = не из слоя)',
    hidden_at DATETIME(6) NULL COMMENT 'единственный персистентный глагол невидимости; стирание байтов — другой ярус',
    hidden_by VARCHAR(255) NULL,
    created_at TIMESTAMP(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
    UNIQUE KEY uq_design_picture_run_ordinal (run_id, ordinal),
    UNIQUE KEY uq_design_picture_batch_ordinal (batch_id, ordinal),
    KEY idx_design_picture_card (tech_card_id, id),
    KEY idx_design_picture_media (media_id),
    KEY idx_design_picture_derived_from (derived_from),
    CONSTRAINT fk_design_picture_card FOREIGN KEY (tech_card_id) REFERENCES tech_card(id) ON DELETE CASCADE,
    CONSTRAINT fk_design_picture_run FOREIGN KEY (run_id) REFERENCES design_run(id) ON DELETE CASCADE,
    CONSTRAINT fk_design_picture_batch FOREIGN KEY (batch_id) REFERENCES design_batch(id) ON DELETE CASCADE,
    CONSTRAINT fk_design_picture_media FOREIGN KEY (media_id) REFERENCES media(id) ON DELETE RESTRICT
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT 'Картинка дизайн-бэнда: плита, кадр прогона, кроп композита или флэттен слоя';

-- +migrate Down

DROP TABLE IF EXISTS design_picture;
DROP TABLE IF EXISTS design_batch;
DROP TABLE IF EXISTS design_run_attempt;
DROP TABLE IF EXISTS design_run;
