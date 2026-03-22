# Inspect database schema or data

Use the MySQL MCP to query the live database. **Default server:** `user-mysql-grbpwr`. Use beta MCPs only if the task names them.

Useful queries:
- `SHOW TABLES` — list all tables
- `DESCRIBE <table>` — show table schema
- `SHOW CREATE TABLE <table>` — full DDL with indexes and FKs
- `SELECT * FROM <table> WHERE ... LIMIT 10` — sample data

Servers: `user-mysql-grbpwr` (default / prod), `user-mysql-beta-09`, `user-mysql-beta-10`.

Always use the MCP `query` tool — never connect directly.
