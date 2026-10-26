package entity

import "time"

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
