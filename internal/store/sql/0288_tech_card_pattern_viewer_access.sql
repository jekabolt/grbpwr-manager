-- +migrate Up

-- Access-control row for a tech card's PATTERN VIEWER -- the card-level twin of
-- pattern_object_access (0258). A printed tech-pack QR now carries one card-scope token
-- ('c', /api/pv/{token}) per fabric scope instead of a raw storage url per sheet, and this
-- row is what lets that paper be revoked without re-printing: tokens embed the epoch they
-- were minted at, so bumping epoch kills every QR printed so far while the card and its
-- objects stay untouched (per-FILE revocation stays on pattern_object_access).
--
-- WHAT A BUMP DOES NOT REACH, so nobody plans a leak response around a promise this table
-- cannot keep: it stops the MANIFEST being fetched, but the per-file /api/p urls handed out
-- inside manifests already served carry the OBJECT epoch and keep working. Killing those is
-- a bump on pattern_object_access (0258), per file. Card revocation is «no new page», not
-- «no more downloads».
--
-- expires_at is a column the handler checks, NOT a policy: printed tech packs live on the
-- factory floor for months and an auto-expired QR mid-production-run is worse than a
-- long-lived link, so rows are created with NULL and nothing ever defaults a TTL in
-- (owner decision 2026-08-08). Revocation is manual: epoch = epoch + 1.
--
-- Rows are created lazily on the first GetTechCard that mints a viewer token; a card
-- without a row simply has no viewer tokens in the wild yet.

CREATE TABLE IF NOT EXISTS tech_card_pattern_viewer_access (
    tech_card_id INT NOT NULL PRIMARY KEY,
    epoch INT NOT NULL DEFAULT 1,
    expires_at TIMESTAMP NULL DEFAULT NULL,
    revoked_at TIMESTAMP NULL DEFAULT NULL,
    last_access_at TIMESTAMP NULL DEFAULT NULL,
    access_count BIGINT NOT NULL DEFAULT 0,
    created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    CONSTRAINT fk_tcpva_card FOREIGN KEY (tech_card_id) REFERENCES tech_card (id) ON DELETE CASCADE
);

-- +migrate Down
DROP TABLE IF EXISTS tech_card_pattern_viewer_access;
