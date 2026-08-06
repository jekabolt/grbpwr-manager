package entity

import (
	"database/sql"
	"time"
)

// PatternObjectAccess is the access-control row for one pattern object (выкройка) in the
// bucket — the server-side state behind the tokenized read path /api/p/{token}. The
// pattern url stored on tech-card/fitting rows stays the object's identity; this row
// carries its revocation epoch (tokens embed the epoch they were minted at — a bump
// invalidates all of them), an optional expiry policy and coarse access stats.
type PatternObjectAccess struct {
	Id        int64  `db:"id"`
	ObjectKey string `db:"object_key"`
	// Filename is the upload's human name, used for the download disposition (the object
	// key itself is a timestamp plus random hex). NULL for legacy rows.
	Filename     sql.NullString `db:"filename"`
	Epoch        int            `db:"epoch"`
	ExpiresAt    sql.NullTime   `db:"expires_at"`
	RevokedAt    sql.NullTime   `db:"revoked_at"`
	LastAccessAt sql.NullTime   `db:"last_access_at"`
	AccessCount  int64          `db:"access_count"`
	CreatedAt    time.Time      `db:"created_at"`
}

// PatternObjectRef names one managed object for an ensure pass: its bucket key plus, when
// the caller has it (the pattern row does), the filename to serve downloads under.
type PatternObjectRef struct {
	Key      string
	Filename string
}
