-- +migrate Up
-- Enhance support_ticket table with proper status model, priority, category, and internal notes

-- Add new columns (case_number without UNIQUE first to allow backfill)
ALTER TABLE support_ticket
ADD COLUMN case_number VARCHAR(20) NULL,
ADD COLUMN priority ENUM('low', 'medium', 'high', 'urgent') NOT NULL DEFAULT 'medium',
ADD COLUMN category VARCHAR(100) NOT NULL DEFAULT '',
ADD COLUMN internal_notes TEXT NULL;

-- Backfill case numbers for existing tickets. Format: CS-YYYY-NNNNN (id ensures uniqueness)
UPDATE support_ticket SET case_number = CONCAT('CS-', YEAR(created_at), '-', LPAD(id, 5, '0'));

-- Now enforce UNIQUE NOT NULL on case_number
ALTER TABLE support_ticket MODIFY COLUMN case_number VARCHAR(20) UNIQUE NOT NULL;

-- Change status from boolean to enum
ALTER TABLE support_ticket
MODIFY COLUMN status ENUM('submitted', 'in_progress', 'waiting_customer', 'resolved', 'closed') NOT NULL DEFAULT 'submitted';

-- Add indexes for better query performance
CREATE INDEX idx_support_ticket_case_number ON support_ticket(case_number);
CREATE INDEX idx_support_ticket_status ON support_ticket(status);
CREATE INDEX idx_support_ticket_priority ON support_ticket(priority);
CREATE INDEX idx_support_ticket_category ON support_ticket(category);
CREATE INDEX idx_support_ticket_email ON support_ticket(email);
CREATE INDEX idx_support_ticket_created_at ON support_ticket(created_at);

-- +migrate Down
-- Revert support_ticket table changes

-- Drop indexes
DROP INDEX idx_support_ticket_created_at ON support_ticket;
DROP INDEX idx_support_ticket_email ON support_ticket;
DROP INDEX idx_support_ticket_category ON support_ticket;
DROP INDEX idx_support_ticket_priority ON support_ticket;
DROP INDEX idx_support_ticket_status ON support_ticket;
DROP INDEX idx_support_ticket_case_number ON support_ticket;

-- Revert status column to boolean
ALTER TABLE support_ticket
MODIFY COLUMN status BOOLEAN DEFAULT FALSE;

-- Remove new columns
ALTER TABLE support_ticket
DROP COLUMN internal_notes,
DROP COLUMN category,
DROP COLUMN priority,
DROP COLUMN case_number;
