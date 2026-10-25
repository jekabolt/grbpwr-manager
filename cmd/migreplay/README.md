# Migration replay

`migreplay` recreates the schema from the merge-base with a Git ref, inserts
synthetic legacy/boundary rows, and applies migrations that exist only in the
working tree. It uses `github.com/rubenv/sql-migrate` with the MySQL dialect,
the same path as application startup.

The command is intentionally strict:

- the DSN must use TCP, a local/container host accepted by `testdbguard`, and a
  database name ending in `_test`;
- the database must be empty; the command never drops or resets it;
- `multiStatements=true` is rejected because beta and production execute
  sql-migrate statements one at a time;
- a migration present at the base commit may not be changed, removed, or
  renamed.

Example with a disposable MySQL 8 container:

```bash
docker run -d --rm --name grbpwr-migreplay-mysql \
  -e MYSQL_ROOT_PASSWORD=replay \
  -e MYSQL_DATABASE=migration_replay_test \
  -p 127.0.0.1:33306:3306 mysql:8.0

MIGREPLAY_DSN='root:replay@tcp(127.0.0.1:33306)/migration_replay_test?parseTime=true&charset=utf8mb4' \
MIGREPLAY_BASE_REF=origin/beta \
make migration-replay

docker rm -f grbpwr-migreplay-mysql
```

The generator temporarily uses `sql_mode=''` and
`foreign_key_checks=0` on its dedicated connection. It restores both before
candidate migrations run under the server's normal mode. For each insertable
column it attempts duplicate pairs containing NULL, zero/negative/numeric
limits, declared-length character/binary values, all legacy ENUM/SET members,
temporal endpoints, and empty JSON containers. Existing unique keys are varied
so the duplicates remain available to catch newly-added unique keys; disabled
FK checks allow orphan candidates for newly-added foreign keys.

Existing CHECK constraints, triggers, finite unique domains, and relationships
between several columns can reject a generated candidate. The summary reports
those rejections and tables for which no synthetic row fit. Replay therefore
raises coverage substantially but cannot prove arbitrary data predicates; a
targeted backfill/validation query is still required for domain-specific
constraints.
