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
  -p 127.0.0.1:33306:3306 mysql:8.0.40 \
  --character-set-server=utf8mb3 --collation-server=utf8mb3_general_ci

MIGREPLAY_DSN='root:replay@tcp(127.0.0.1:33306)/migration_replay_test?parseTime=true&charset=utf8mb4' \
MIGREPLAY_BASE_REF=origin/beta \
make migration-replay

docker rm -f grbpwr-migreplay-mysql
```

To verify a clean install rather than the base/new split, point the same safe
DSN at a fresh empty database and run `go run ./cmd/migreplay --full-chain`.
This is the mode used by CI's utf8mb3/utf8mb4 matrix.

The generator temporarily uses `sql_mode=''` and
`foreign_key_checks=0` on its dedicated connection. It restores both before
candidate migrations run under the server's normal mode. For each insertable
column it attempts duplicate pairs containing NULL, zero/negative/numeric
limits, declared-length character/binary values, all legacy ENUM/SET members,
temporal endpoints, and empty JSON containers. Existing unique keys are varied
so the duplicates remain available to catch newly-added unique keys; disabled
FK checks allow orphan candidates for newly-added foreign keys. Before those
hostile candidates, the generator derives admissible baseline values from
finite `IN (...)`, `REGEXP`, and `REGEXP_LIKE` CHECK vocabularies. This keeps a
checked status/kind column from invalidating every boundary aimed at another
column.

Cross-column CHECK relationships and finite unique domains can still reject a
generated candidate. The summary reports the final MySQL rejection for every
table where no synthetic row fit. Replay therefore raises coverage
substantially but cannot prove arbitrary data predicates; a targeted
backfill/validation query is still required for domain-specific constraints.
