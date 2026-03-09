-- +migrate Up
-- Migration: Add bank_invoice and cash payment methods for admin custom orders
INSERT INTO payment_method (name, allowed) VALUES ('bank-invoice', true);
INSERT INTO payment_method (name, allowed) VALUES ('cash', true);

-- +migrate Down
DELETE FROM payment_method WHERE name IN ('bank-invoice', 'cash');
