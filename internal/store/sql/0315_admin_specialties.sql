-- +migrate Up

-- СПЕЦИАЛЬНОСТИ АККАУНТА: «чем я занимаюсь».
--
-- ЭТО САМООПИСАНИЕ, А НЕ ПРАВО. Специальность не даёт ни грамма доступа — права живут в
-- admin_permission и только там. Она нужна затем, чтобы в пикере владельцев файла и в упоминаниях
-- рядом с именем стояло «kirill · конструктор»: в команде, где половина людей знакома по никам,
-- это разница между «выбрать наугад» и «выбрать нужного».
--
-- СЛОВАРЬ + СВЯЗЬ, ТА ЖЕ ГРАММАТИКА, ЧТО У ТЕМ ФАЙЛА (0312). Имя уникально РЕГИСТРО- И
-- ДИАКРИТИКО-НЕЗАВИСИМО (коллация по умолчанию ai_ci, как у file_topic.name): «Дизайнер» и
-- «дизайнер» — одна специальность, а не две строки в списке. Заводится на лету набором нового
-- имени — справочник, который заполняет не тот, кто им пользуется, остаётся пустым.
--
-- FK НА СЛОВАРЬ БЕЗ КАСКАДА (RESTRICT по умолчанию), НА АККАУНТ — С КАСКАДОМ. Ровно та же
-- раскладка и по тем же причинам, что у library_file_topic: удаление используемой специальности
-- обязано падать (иначе оно молча стёрло бы подписи у всех, кто её нёс), а удаление аккаунта уносит
-- его собственные связи — они без него ничего не значат.
--
-- ИДЕМПОТЕНТНОСТЬ (CLAUDE.md): только CREATE TABLE IF NOT EXISTS и INSERT IGNORE, повтор файла —
-- no-op. ALTER-ов нет, поэтому нет и PREPARE/EXECUTE. Ретроактивных CHECK нет: они проверяют всю
-- историю и роняют старт прода.

CREATE TABLE IF NOT EXISTS admin_specialty (
  id INT PRIMARY KEY AUTO_INCREMENT,
  name VARCHAR(64) NOT NULL COMMENT 'название специальности; уникально регистро-независимо (коллация ai_ci)',
  created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
  UNIQUE KEY uniq_admin_specialty_name (name)
) ENGINE = InnoDB DEFAULT CHARSET = utf8mb4 COMMENT 'Словарь специальностей аккаунтов: самоописание, не право';

-- Затравка — роли, которые в этой команде реально есть. Пустой словарь заставил бы каждого
-- придумывать формулировку с нуля, а придумывать её никто не будет: поле останется пустым, и
-- пикер владельцев потеряет ровно то, ради чего заводился. Список открытый — «+ добавить свою»
-- дописывает в этот же словарь.
INSERT IGNORE INTO admin_specialty (name) VALUES
  ('дизайнер'), ('графический дизайнер'), ('конструктор'), ('технолог'),
  ('фотограф'), ('видеограф'), ('стилист'), ('продюсер съёмки'),
  ('копирайтер'), ('smm'), ('маркетинг'), ('биздев'),
  ('разработчик'), ('производство');

CREATE TABLE IF NOT EXISTS admin_specialty_link (
  id INT PRIMARY KEY AUTO_INCREMENT,
  admin_id INT NOT NULL,
  specialty_id INT NOT NULL,
  created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
  UNIQUE KEY uniq_admin_specialty_link (admin_id, specialty_id),
  INDEX idx_admin_specialty_link_specialty (specialty_id),
  CONSTRAINT fk_admin_specialty_link_admin FOREIGN KEY (admin_id)
    REFERENCES admins (id) ON DELETE CASCADE,
  CONSTRAINT fk_admin_specialty_link_specialty FOREIGN KEY (specialty_id)
    REFERENCES admin_specialty (id)
) ENGINE = InnoDB DEFAULT CHARSET = utf8mb4 COMMENT 'Связь аккаунт ↔ специальность';

-- +migrate Down
DROP TABLE IF EXISTS admin_specialty_link;
DROP TABLE IF EXISTS admin_specialty;
