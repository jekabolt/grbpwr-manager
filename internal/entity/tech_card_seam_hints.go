package entity

import (
	"database/sql"
	"time"
)

// SEAM HINTS (unit-seams line F, 0440): the AI's hint per tech card input, the owner's overrides
// and the owner's code dictionary. A hint never writes a seam; the shapes of the JSON columns and
// the vocabularies live in internal/techcardseamhints.

// TechCardSeamHintRun is one stored answer of chat.techcard_seams for one (card, input digest,
// schema version). Hints is the validated hints JSON.
type TechCardSeamHintRun struct {
	Id            int64     `db:"id"`
	TechCardId    int       `db:"tech_card_id"`
	InputDigest   string    `db:"input_digest"`
	SchemaVersion int       `db:"schema_version"`
	Model         string    `db:"model"`
	Hints         string    `db:"hints"`
	CreatedBy     string    `db:"created_by"`
	CreatedAt     time.Time `db:"created_at"`
}

// TechCardSeamHintOverride is the owner's word on one hint: Kind role (HintKey = piece name) or
// join (HintKey = operation number), Value JSON (NULL allowed only with state off), State
// ok | fixed | off. Never touched by a run.
type TechCardSeamHintOverride struct {
	TechCardId int            `db:"tech_card_id"`
	Kind       string         `db:"kind"`
	HintKey    string         `db:"hint_key"`
	Value      sql.NullString `db:"value"`
	State      string         `db:"state"`
	UpdatedAt  time.Time      `db:"updated_at"`
	UpdatedBy  string         `db:"updated_by"`
}

// PieceCodeDictionaryEntry is one row of the owner's code dictionary («все карты»): a normalized
// upper token of a piece name → role (+ hand, NULL = none).
type PieceCodeDictionaryEntry struct {
	Code      string         `db:"code"`
	Role      string         `db:"role"`
	Hand      sql.NullString `db:"hand"`
	UpdatedAt time.Time      `db:"updated_at"`
	UpdatedBy string         `db:"updated_by"`
}
