-- Fix orphaned migration records
-- This script removes migration records for files that no longer exist

-- First, check what migrations are recorded
SELECT * FROM gorp_migrations;

-- Delete the orphaned migration records for the deleted files
DELETE FROM gorp_migrations WHERE id IN ('0002_add_tailored_sizes.sql', '0003_add_bottoms_sizes.sql');

-- Verify the cleanup
SELECT * FROM gorp_migrations;

