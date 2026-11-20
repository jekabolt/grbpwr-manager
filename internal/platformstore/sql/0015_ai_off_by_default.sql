-- +migrate Up
-- X7 part 4 (owner, 20.11): ai is no longer default-on — a new team starts without it until the
-- operator switches it on in the console. Every team that exists now keeps ai: one explicit row
-- (enabled, updated_by NULL = the system's) for each team that has no ai row yet. A row an operator
-- wrote is never touched (INSERT IGNORE on the (team_id, feature) key). Additive, idempotent,
-- ANSI_QUOTES-safe. Team #1 also gets ai from its boot profile plan.

INSERT IGNORE INTO team_feature (team_id, feature, enabled, updated_by, updated_at)
SELECT id, 'ai', 1, NULL, UTC_TIMESTAMP(3) FROM team;

-- +migrate Down
-- Never removed: a rollback binary has ai default-on and ignores the rows.
