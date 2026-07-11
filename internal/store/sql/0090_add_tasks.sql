-- +migrate Up
-- Internal team kanban (task manager), for the admin panel. NOT customer orders.
-- A task belongs to exactly one board (department lane) and sits in one status
-- column at a given position; drag-and-drop moves it across columns/boards and
-- re-sequences position. Identity (assignee, created_by, comment author) is an
-- admin account username. Optional typed deep-links point at the artifact a card
-- is about (tech card / product / order / archive) and use ON DELETE SET NULL so
-- deleting that artifact never blocks on a task — the link just clears.

-- task: one kanban card. board/status/priority are enums stored as short strings
-- with a CHECK, mirroring the fitting status/verdict pattern.
CREATE TABLE task (
  id INT PRIMARY KEY AUTO_INCREMENT,
  title VARCHAR(255) NOT NULL COMMENT 'card title',
  description TEXT NULL COMMENT 'freeform / markdown body',
  board VARCHAR(16) NOT NULL COMMENT 'development|design|marketing|production|sourcing|content'
    CHECK (board REGEXP '^(development|design|marketing|production|sourcing|content)$'),
  status VARCHAR(16) NOT NULL DEFAULT 'todo' COMMENT 'backlog|todo|in_progress|review|done'
    CHECK (status REGEXP '^(backlog|todo|in_progress|review|done)$'),
  position INT NOT NULL DEFAULT 0 COMMENT 'order within its (board,status) column',
  assignee VARCHAR(255) NOT NULL DEFAULT '' COMMENT 'admin account username; "" = unassigned',
  priority VARCHAR(16) NOT NULL DEFAULT 'unknown' COMMENT 'unknown|low|medium|high|urgent'
    CHECK (priority REGEXP '^(unknown|low|medium|high|urgent)$'),
  due_date DATETIME NULL COMMENT 'optional deadline (UTC); NULL = none',
  created_by VARCHAR(255) NOT NULL DEFAULT '' COMMENT 'admin account username, from the JWT',
  tech_card_id INT NULL COMMENT 'FK tech_card(id); deep link, NULL = none',
  product_id INT NULL COMMENT 'FK product(id); deep link, NULL = none',
  order_uuid VARCHAR(36) NULL COMMENT 'customer_order.uuid; deep link, best-effort (no FK)',
  archive_id INT NULL COMMENT 'FK archive(id); deep link, NULL = none',
  created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
  updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
  INDEX idx_task_board_status_pos (board, status, position),
  INDEX idx_task_assignee (assignee),
  INDEX idx_task_tech_card (tech_card_id),
  INDEX idx_task_product (product_id),
  INDEX idx_task_archive (archive_id),
  FOREIGN KEY (tech_card_id) REFERENCES tech_card(id) ON DELETE SET NULL,
  FOREIGN KEY (product_id) REFERENCES product(id) ON DELETE SET NULL,
  FOREIGN KEY (archive_id) REFERENCES archive(id) ON DELETE SET NULL
) ENGINE = InnoDB DEFAULT CHARSET = utf8mb4 COMMENT 'Internal team kanban cards';

-- task_label: freeform lightweight tags on a card (client-side filtering).
CREATE TABLE task_label (
  id INT PRIMARY KEY AUTO_INCREMENT,
  task_id INT NOT NULL,
  label VARCHAR(64) NOT NULL,
  display_order INT NOT NULL DEFAULT 0,
  UNIQUE KEY uniq_task_label (task_id, label),
  FOREIGN KEY (task_id) REFERENCES task(id) ON DELETE CASCADE
) ENGINE = InnoDB DEFAULT CHARSET = utf8mb4 COMMENT 'Freeform tags per task';

-- task_media: reference images/files attached to a card (mirrors fitting_media).
CREATE TABLE task_media (
  id INT PRIMARY KEY AUTO_INCREMENT,
  task_id INT NOT NULL,
  media_id INT NOT NULL,
  display_order INT NOT NULL DEFAULT 0,
  FOREIGN KEY (task_id) REFERENCES task(id) ON DELETE CASCADE,
  FOREIGN KEY (media_id) REFERENCES media(id)
) ENGINE = InnoDB DEFAULT CHARSET = utf8mb4 COMMENT 'Media attached to a task';

-- task_comment: an activity/discussion comment on a card. author from the JWT.
CREATE TABLE task_comment (
  id INT PRIMARY KEY AUTO_INCREMENT,
  task_id INT NOT NULL,
  author VARCHAR(255) NOT NULL DEFAULT '' COMMENT 'admin account username, from the JWT',
  body TEXT NOT NULL,
  created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
  INDEX idx_task_comment_task (task_id, created_at),
  FOREIGN KEY (task_id) REFERENCES task(id) ON DELETE CASCADE
) ENGINE = InnoDB DEFAULT CHARSET = utf8mb4 COMMENT 'Comments on a task';

-- +migrate Down
DROP TABLE IF EXISTS task_comment;
DROP TABLE IF EXISTS task_media;
DROP TABLE IF EXISTS task_label;
DROP TABLE IF EXISTS task;
