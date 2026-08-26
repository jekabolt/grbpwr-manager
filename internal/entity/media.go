package entity

import (
	"database/sql"
	"time"
)

type MediaFull struct {
	Id        int       `db:"id" json:"id"`
	CreatedAt time.Time `db:"created_at" json:"created_at"`
	MediaItem
}

// MediaUsageRef is a single place still holding on to a media item: which entity, which
// slot inside it, and a name a human recognises. It answers the only question the media
// library could not previously answer — "may I delete this file, and if not, why not".
// One entity can appear more than once when it uses the same file in several slots.
type MediaUsageRef struct {
	MediaId  int    `db:"media_id" json:"media_id"`
	Kind     string `db:"kind" json:"kind"`
	EntityId int    `db:"entity_id" json:"entity_id"`
	Label    string `db:"label" json:"label"`
	Slot     string `db:"slot" json:"slot"`
}

type MediaItem struct {
	FullSizeMediaURL   string         `db:"full_size" json:"full_size"`
	FullSizeWidth      int            `db:"full_size_width" json:"full_size_width"`
	FullSizeHeight     int            `db:"full_size_height" json:"full_size_height"`
	ThumbnailMediaURL  string         `db:"thumbnail" json:"thumbnail"`
	ThumbnailWidth     int            `db:"thumbnail_width" json:"thumbnail_width"`
	ThumbnailHeight    int            `db:"thumbnail_height" json:"thumbnail_height"`
	CompressedMediaURL string         `db:"compressed" json:"compressed"`
	CompressedWidth    int            `db:"compressed_width" json:"compressed_width"`
	CompressedHeight   int            `db:"compressed_height" json:"compressed_height"`
	BlurHash           sql.NullString `db:"blur_hash" json:"blur_hash"`
	// ContentHash is the hex SHA-256 of the FULL-SIZE object exactly as it lies in the
	// bucket — not of the bytes the client posted. The image path re-encodes to WebP, so
	// hashing the payload would fingerprint something that is nowhere in storage; the
	// archive export downloads the full-size object and the archive import compares its
	// sha against this column, and those two are only ever the same number when both are
	// taken over the STORED bytes.
	//
	// Invalid = "not computed": every row written before 0336 has no hash and must compare
	// equal to nothing. It is deliberately not unique — the same file legitimately appears
	// in media more than once, and de-duplication is import policy, not a storage invariant.
	ContentHash sql.NullString `db:"content_hash" json:"content_hash"`
}
