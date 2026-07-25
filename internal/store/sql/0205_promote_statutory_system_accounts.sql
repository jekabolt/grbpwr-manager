-- +migrate Up
-- Promote the statutory / autoposting target accounts to is_system=TRUE (review pass 2, M-3).
--
-- Why a new migration instead of editing 0204/0195: those migrations are already applied on beta,
-- and sql-migrate tracks by filename — re-seeding is_system inside an applied file is a silent
-- no-op on any DB that already ran it, so the protection would never activate on beta. This
-- forward migration applies cleanly on the existing beta DB and on a fresh prod migrate alike.
--
-- is_system=TRUE stops the account being archived from the admin UI. Archiving one of these would
-- make resolveAccounts reject the next automated posting and poison the review queue:
--   3005/2045/6335/2060 — seeded by 0204 (statutory layer)
--   1225/6370            — depreciation autoposting targets seeded by 0195_frs105
--   2015                 — director's loan (wizard / cash-flow report target)
-- Idempotent: only flips rows still FALSE.
UPDATE acct_account
SET is_system = TRUE
WHERE code IN ('3005', '2045', '6335', '2060', '1225', '6370', '2015')
  AND is_system = FALSE;

-- +migrate Down
-- No-op: re-enabling archiving on statutory/autoposting targets would reintroduce the
-- queue-poisoning hazard this migration closes.
