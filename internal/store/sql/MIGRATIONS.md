# Writing a team migration (X3: deploys close no team, rollback runs)

Every team database runs these files. The fleet migrates teams in the background after a deploy, so for a
while teams sit on different versions, and a rollback runs older code on newer databases. The gate serves a
team while its schema fits the running code; these rules keep that window open.

## Default: additive, needed — declare nothing
- Add tables, nullable or defaulted columns, indexes, rows. Idempotent (`IF NOT EXISTS`, guarded PREPARE).
- Ship the code that uses it in the same PR. A team that has not reached this file yet is held until it does.

## `-- compat: lazy <reason>` — code does not need it yet
- An index, a backfill, seeded reference rows: a team behind only lazy files stays open.
- Only when the code of this release works without it.

## Removing or renaming — two releases, never one PR
1. Release 1: stop using the old name in code (renaming: add the new column, write both, read the new).
2. Release 2: `NNNN_breaking_<what>.sql` with `-- compat: breaking <reason>`.
- The lint (`internal/store/migrationlint/compat.go`) refuses DROP / RENAME / CHANGE without the declaration,
  and a declared one whose old name is still in a Go string literal of `internal/` or `app/`.
- Same name used elsewhere for another reason: `-- migrationlint:name-still-used <name> <reason>`.
- Cost: after a breaking release, code older than it no longer serves teams that ran it (they answer «team is
  being updated»). Rollback past it = roll forward instead.

## Never
- Edit an applied migration. Breaking and lazy in one file.
- A breaking control-plane migration (`internal/platformstore/sql/`): those are additive only.
