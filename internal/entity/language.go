package entity

import (
	"time"
)

// Language represents supported languages for translations
type Language struct {
	Id        int       `db:"id" json:"id"`
	Code      string    `db:"code" json:"code"`             // ISO 639-1 language code (e.g., en, es, fr)
	Name      string    `db:"name" json:"name"`             // Human readable language name
	IsDefault bool      `db:"is_default" json:"is_default"` // Indicates the default/fallback language
	IsActive  bool      `db:"is_active" json:"is_active"`   // Whether this language is currently supported
	CreatedAt time.Time `db:"created_at" json:"created_at"`
	UpdatedAt time.Time `db:"updated_at" json:"updated_at"`
}

// AnnounceTranslation represents site announcement translations
type AnnounceTranslation struct {
	Id         int       `db:"id" json:"id"`
	LanguageId int       `db:"language_id" json:"language_id"`
	Text       string    `db:"text" json:"text"`
	CreatedAt  time.Time `db:"created_at" json:"created_at"`
	UpdatedAt  time.Time `db:"updated_at" json:"updated_at"`
}
