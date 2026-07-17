package entity

import (
	"database/sql"
	"encoding/json"
)

// UnquoteLegacyComposition converts the stored form of the legacy tech_card.composition column to
// its plain wire text (M1 norm: composition on the wire is plain text, always). The column is a
// native MySQL JSON column (0139), so plain text is stored as a quoted JSON string scalar — SQL
// paths built on styleCompositionSelect JSON_UNQUOTE it in the query; `SELECT *` paths (the
// tech-card reads) must apply this instead. Non-scalar historical values (structured-JSON arrays
// written by the old frontend) pass through untouched.
func UnquoteLegacyComposition(ns sql.NullString) sql.NullString {
	if !ns.Valid || len(ns.String) < 2 || ns.String[0] != '"' {
		return ns
	}
	var s string
	if err := json.Unmarshal([]byte(ns.String), &s); err != nil {
		return ns
	}
	return sql.NullString{String: s, Valid: true}
}
