-- +migrate Up

-- «Seen» marks on construction-audit findings (T9). The audit is recomputed on every call and never
-- stored, so a mark is keyed by the finding's stable key (code@refs, see
-- internal/techcardanalysis/findingkey.go) and remembers the fingerprint of the text that was seen.
-- A finding reads as seen only while its current fingerprint equals the stored one; a stale row is
-- kept and overwritten by the next mark. Unmark deletes the row.
--
-- finding_key is utf8mb4_bin: unit keys inside it are byte-exact («Base» and «base» are two units).
-- PK width: 4 + 512*4 = 2052 bytes, below InnoDB's 3072-byte key ceiling.
CREATE TABLE IF NOT EXISTS tech_card_finding_seen (
  tech_card_id INT NOT NULL,
  finding_key VARCHAR(512) CHARACTER SET utf8mb4 COLLATE utf8mb4_bin NOT NULL,
  fingerprint CHAR(16) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  seen_by VARCHAR(255) NOT NULL DEFAULT '',
  seen_at DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
  PRIMARY KEY (tech_card_id, finding_key),
  CONSTRAINT fk_tech_card_finding_seen_card FOREIGN KEY (tech_card_id) REFERENCES tech_card(id) ON DELETE CASCADE
) ENGINE = InnoDB DEFAULT CHARSET = utf8mb4 COMMENT 'Seen marks on construction-audit findings';

-- +migrate Down

DROP TABLE IF EXISTS tech_card_finding_seen;
