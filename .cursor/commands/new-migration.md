# Create new MySQL migration

Follow the migration workflow in @200-migration-workflow.mdc.

1. Determine next migration number: `ls -1 internal/store/sql/ | tail -1` → increment
2. Create `internal/store/sql/NNNN_short_description.sql` with `-- +migrate Up` and `-- +migrate Down`
3. Update entity structs in `internal/entity/` if schema changed
4. Update affected store queries in `internal/store/`
5. If API-exposed: update proto, run `make proto`, add DTO conversions

Use lowercase SQL, snake_case, `NULL DEFAULT NULL` for new columns. See @200-database.mdc.
