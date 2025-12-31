-- +migrate Up
-- Migration: Increase size name column length
-- Description: Increases the size.name VARCHAR length from 10 to 20 to accommodate longer size names
-- Affected tables: size
-- Date: 2025-12-31

ALTER TABLE size MODIFY COLUMN name VARCHAR(20) NOT NULL UNIQUE;

-- +migrate Down
-- Revert size name column length back to 10
ALTER TABLE size MODIFY COLUMN name VARCHAR(10) NOT NULL UNIQUE;

