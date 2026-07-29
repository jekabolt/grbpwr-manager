-- +migrate Up

-- Gives the lab-dip approval loop a HISTORY (PLM UI gap 1).
--
-- A lab dip is a round trip: the dyehouse sends a swatch, the studio approves or rejects it, and a
-- rejection starts another round. The colourway (product) carries only the CURRENT round as flat
-- scalars -- lab_dip_status / lab_dip_round / lab_dip_submitted_at / lab_dip_decided_at /
-- lab_dip_decided_by / lab_dip_reject_reason (added by 0151 when tech_card_colorway was folded into
-- product). Every earlier round was overwritten, so "R1 rejected, R2 rejected, R3 approved" -- the one
-- thing anyone actually wants to see on a colour that is running late -- was unrecoverable. The admin
-- rendered a one-row "timeline" and said so in a caption.
--
-- SHAPE: the scalars on `product` stay the current round and stay the source of truth for every
-- existing reader. This table is the append-side journal keyed (product_id, round_number), written
-- from the same lab-dip write path: writing round 3 leaves rounds 1 and 2 untouched. Two sources
-- cannot disagree because only one of them is ever queried for "current" -- the journal is the
-- history OF that column, not a parallel copy of it.
--
-- The status CHECK mirrors entity.IsValidTechCardLabDipStatus. Note the scalars on `product` have no
-- such CHECK (0151 dropped it deliberately, enum enforced in Go); we constrain the new table because
-- there is no legacy data to trip over.
--
-- swatch_media_id ON DELETE SET NULL mirrors fk_product_swatch_media: losing the image must not
-- delete the record that a round happened.
--
-- Idempotent: CREATE TABLE IF NOT EXISTS plus an INSERT ... SELECT that skips rows already present.

CREATE TABLE IF NOT EXISTS product_lab_dip_round (
    id              INT PRIMARY KEY AUTO_INCREMENT,
    product_id      INT NOT NULL COMMENT 'FK product(id): the colourway this round belongs to',
    round_number    INT NOT NULL COMMENT '1-based; the highest round is the one the product scalars describe',
    status          VARCHAR(16) NOT NULL COMMENT 'pending|submitted|approved|rejected',
    submitted_at    DATE NULL,
    decided_at      DATE NULL,
    decided_by      VARCHAR(255) NULL,
    reject_reason   TEXT NULL,
    comment         TEXT NULL,
    swatch_media_id INT NULL COMMENT 'FK media(id): the swatch this round was judged on',
    created_at      TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at      TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
    CONSTRAINT fk_pldr_product FOREIGN KEY (product_id) REFERENCES product(id) ON DELETE CASCADE,
    CONSTRAINT fk_pldr_media FOREIGN KEY (swatch_media_id) REFERENCES media(id) ON DELETE SET NULL,
    CONSTRAINT uniq_pldr_product_round UNIQUE (product_id, round_number),
    CONSTRAINT chk_pldr_status CHECK (status REGEXP '^(pending|submitted|approved|rejected)$')
) ENGINE = InnoDB COMMENT 'Lab-dip round journal per colourway; the product.lab_dip_* scalars are its latest row';

-- Seed the journal from the current scalars so an existing colourway does not read as "no history"
-- the moment this ships. Only the round the product is actually on can be recovered -- the rounds it
-- overwrote are gone, and inventing them would be worse than an honest single entry.
--
-- COALESCE(lab_dip_round, 1): a colourway with a status but no round number is on its first round.
-- Rows with no lab-dip state at all are skipped; they have no history to seed.
INSERT INTO product_lab_dip_round
    (product_id, round_number, status, submitted_at, decided_at, decided_by, reject_reason, swatch_media_id)
SELECT p.id,
       GREATEST(COALESCE(p.lab_dip_round, 1), 1),
       COALESCE(p.lab_dip_status, 'pending'),
       p.lab_dip_submitted_at,
       p.lab_dip_decided_at,
       p.lab_dip_decided_by,
       p.lab_dip_reject_reason,
       p.swatch_media_id
FROM product p
WHERE (p.lab_dip_status IS NOT NULL OR p.lab_dip_round IS NOT NULL)
  AND COALESCE(p.lab_dip_status, 'pending') REGEXP '^(pending|submitted|approved|rejected)$'
  AND NOT EXISTS (
      SELECT 1 FROM product_lab_dip_round r
      WHERE r.product_id = p.id
        AND r.round_number = GREATEST(COALESCE(p.lab_dip_round, 1), 1)
  );

-- +migrate Down
DROP TABLE IF EXISTS product_lab_dip_round;
