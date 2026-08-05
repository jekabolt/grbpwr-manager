-- +migrate Up

-- Access-control row for one pattern object (выкройка) in the bucket -- the server-side half of
-- the tokenized read path (Ф7). The stored pattern url stays the object's IDENTITY; this table
-- holds the object's access STATE -- an epoch counter whose bump revokes every token minted for
-- the object (tokens embed the epoch), an optional expiry policy, and coarse access stats.
-- Rows are created lazily on first read/upload of an object; a missing row simply means the
-- object has default access state.
--
-- NOTE this file may be renumbered before merge -- a parallel branch reserves 0257 for
-- tech_card_marker. Renaming is safe while unapplied; only applied migrations must keep names.

CREATE TABLE IF NOT EXISTS pattern_object_access (
    id BIGINT AUTO_INCREMENT PRIMARY KEY,
    object_key VARCHAR(512) NOT NULL,
    -- The human filename the object was uploaded under, carried here because the download
    -- path (a presigned redirect) can only name the file through the object key otherwise,
    -- and that key is a timestamp plus 128 random bits. Nullable: legacy objects and any
    -- caller without the pattern row simply fall back to the key basename. NEVER populated
    -- from a request parameter -- it lands in a Content-Disposition header.
    filename VARCHAR(255) NULL DEFAULT NULL,
    epoch INT NOT NULL DEFAULT 0,
    expires_at TIMESTAMP NULL DEFAULT NULL,
    revoked_at TIMESTAMP NULL DEFAULT NULL,
    last_access_at TIMESTAMP NULL DEFAULT NULL,
    access_count BIGINT NOT NULL DEFAULT 0,
    created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    CONSTRAINT uq_pattern_object_access_key UNIQUE (object_key)
);

-- +migrate Down
DROP TABLE IF EXISTS pattern_object_access;
