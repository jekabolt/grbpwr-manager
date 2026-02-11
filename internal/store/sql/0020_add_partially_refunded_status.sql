-- +migrate Up
INSERT INTO order_status (name) VALUES ('partially_refunded');

-- +migrate Down
DELETE FROM order_status WHERE name = 'partially_refunded';
