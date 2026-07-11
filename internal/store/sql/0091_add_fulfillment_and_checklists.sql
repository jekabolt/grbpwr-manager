-- +migrate Up
-- Two additions on top of the kanban task manager (0090):
--   1. Task soft-archive + task checklists (subtasks).
--   2. The orders-fulfillment board — a kanban PROJECTION of orders. Orders stay
--      the single source of truth for status; the only board-owned data is a
--      lightweight annotation (assignee, notes, packing checklist) keyed by the
--      order uuid. No order status is duplicated here, so the board cannot drift.

-- Task soft-archive: archived_at set = hidden from the board and the default list
-- (restorable), NULL = active. Orthogonal to placement (board/status/position).
ALTER TABLE task
  ADD COLUMN archived_at DATETIME NULL DEFAULT NULL COMMENT 'set = archived (hidden, restorable); NULL = active',
  ADD INDEX idx_task_archived_at (archived_at);

-- task_checklist_item: an ordered subtask with a done flag. Item-level state lives
-- here (not on task) so a content edit never wipes it; managed by dedicated RPCs.
CREATE TABLE task_checklist_item (
  id INT PRIMARY KEY AUTO_INCREMENT,
  task_id INT NOT NULL,
  content VARCHAR(512) NOT NULL,
  is_done BOOLEAN NOT NULL DEFAULT FALSE,
  position INT NOT NULL DEFAULT 0 COMMENT 'order within the checklist',
  created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
  INDEX idx_task_checklist_task (task_id, position),
  FOREIGN KEY (task_id) REFERENCES task(id) ON DELETE CASCADE
) ENGINE = InnoDB DEFAULT CHARSET = utf8mb4 COMMENT 'Checklist items (subtasks) per task';

-- order_fulfillment: the board-owned annotation on an order. 1:1 with an order
-- (order_uuid UNIQUE), lazily created on first edit. Best-effort link to
-- customer_order.uuid, no FK (mirrors task.order_uuid). Carries NO order status.
CREATE TABLE order_fulfillment (
  id INT PRIMARY KEY AUTO_INCREMENT,
  order_uuid VARCHAR(36) NOT NULL COMMENT 'customer_order.uuid; 1:1, best-effort (no FK)',
  assignee VARCHAR(255) NOT NULL DEFAULT '' COMMENT 'admin account username; "" = unassigned',
  notes TEXT NULL COMMENT 'internal packing notes',
  created_by VARCHAR(255) NOT NULL DEFAULT '' COMMENT 'admin username who first annotated, from the JWT',
  created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
  updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
  UNIQUE KEY uniq_order_fulfillment_order (order_uuid),
  INDEX idx_order_fulfillment_assignee (assignee)
) ENGINE = InnoDB DEFAULT CHARSET = utf8mb4 COMMENT 'Board-owned fulfillment annotation per order';

-- order_fulfillment_checklist_item: an ordered packing-checklist row on an order
-- (e.g. picked/packed/label printed).
CREATE TABLE order_fulfillment_checklist_item (
  id INT PRIMARY KEY AUTO_INCREMENT,
  order_fulfillment_id INT NOT NULL,
  content VARCHAR(512) NOT NULL,
  is_done BOOLEAN NOT NULL DEFAULT FALSE,
  position INT NOT NULL DEFAULT 0 COMMENT 'order within the checklist',
  created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
  INDEX idx_ofci_fulfillment (order_fulfillment_id, position),
  FOREIGN KEY (order_fulfillment_id) REFERENCES order_fulfillment(id) ON DELETE CASCADE
) ENGINE = InnoDB DEFAULT CHARSET = utf8mb4 COMMENT 'Packing-checklist items per order fulfillment';

-- +migrate Down
DROP TABLE IF EXISTS order_fulfillment_checklist_item;
DROP TABLE IF EXISTS order_fulfillment;
DROP TABLE IF EXISTS task_checklist_item;
ALTER TABLE task DROP COLUMN archived_at;
