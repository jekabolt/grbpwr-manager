package entity

import (
	"database/sql"
	"encoding/json"
	"strings"
)

// UnquoteLegacyComposition converts the stored form of the legacy tech_card.composition column to
// its plain wire text (M1 norm: composition on the wire is plain text, always). The column is a
// native MySQL JSON column (0139): plain text is stored as a quoted JSON string scalar ("100% cotton"),
// and the old frontend also stored it as a JSON array of strings (["100% cotton"], ["70% cotton",
// "30% polyester"]). SQL paths built on styleCompositionSelect JSON_UNQUOTE the scalar form; `SELECT *`
// paths (the tech-card reads) apply this instead. Both scalar and string-array forms fold to plain
// text (array elements joined by ", "); anything else (a bare non-JSON string, an object) passes
// through untouched. Migration 0179 flattens the stored arrays to scalars so JSON_UNQUOTE alone suffices
// on the SQL paths; this stays as defence in depth and for the Go read paths.
func UnquoteLegacyComposition(ns sql.NullString) sql.NullString {
	if !ns.Valid || len(ns.String) < 2 {
		return ns
	}
	switch ns.String[0] {
	case '"':
		var s string
		if err := json.Unmarshal([]byte(ns.String), &s); err != nil {
			return ns
		}
		return sql.NullString{String: s, Valid: true}
	case '[':
		var arr []string
		if err := json.Unmarshal([]byte(ns.String), &arr); err != nil {
			return ns // not an array of plain strings — leave as-is
		}
		return sql.NullString{String: strings.Join(arr, ", "), Valid: true}
	default:
		return ns
	}
}
