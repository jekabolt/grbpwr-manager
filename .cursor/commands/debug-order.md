# Debug an order

Trace an order across the system. Provide the order UUID or order ID.

1. Query the database via MySQL MCP on **`user-mysql-grbpwr`** (unless debugging a named beta env): `SELECT * FROM customer_order WHERE uuid = '<uuid>' OR id = <id>`
2. Check payment: `SELECT * FROM payment WHERE order_id = <order_id>`
3. Check order items: `SELECT * FROM order_item WHERE order_id = <order_id>`
4. Check shipment: `SELECT * FROM order_buyer_shipment WHERE order_id = <order_id>`
5. Check email history: `SELECT * FROM send_email_request WHERE to_email = '<buyer_email>' ORDER BY created_at DESC`
6. Check promo usage: `SELECT * FROM promo_code WHERE id = <promo_id>` (if applicable)
7. Check stock changes: `SELECT * FROM product_stock_change_history WHERE order_uuid = '<uuid>'`

Summarize: order status, payment status, shipment status, emails sent, any errors.
