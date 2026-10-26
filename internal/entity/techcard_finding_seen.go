package entity

import (
	"errors"
	"time"
)

// TechCardFindingSeenMaxPerCard caps the «seen» marks one card keeps (and the client marks one audit
// response hands back). A real card carries a few dozen findings; the cap exists so that a writer
// minting endless distinct keys cannot grow the table, and every later audit read, without bound.
const TechCardFindingSeenMaxPerCard = 500

// ErrTechCardFindingSeenLimit: a NEW mark would take the card past TechCardFindingSeenMaxPerCard.
// Re-marking a key the card already has, and unmarking, are never refused.
var ErrTechCardFindingSeenLimit = errors.New("tech card keeps too many seen marks")

// TechCardFindingSeen is one «seen» mark on a construction-audit finding (T9, table
// tech_card_finding_seen).
//
// The audit is recomputed on every call and never stored, so the mark is keyed by the finding's
// stable key and carries the fingerprint of the text the person saw. A finding reads as seen only
// while its current fingerprint equals the stored one; a mismatched row is kept (the next mark
// overwrites it) and the finding is simply unseen again.
type TechCardFindingSeen struct {
	TechCardId  int       `db:"tech_card_id"`
	FindingKey  string    `db:"finding_key"`
	Fingerprint string    `db:"fingerprint"`
	SeenBy      string    `db:"seen_by"`
	SeenAt      time.Time `db:"seen_at"`
}
