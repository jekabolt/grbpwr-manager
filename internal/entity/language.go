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

// Announce represents global announcement settings
type Announce struct {
	Id        int       `db:"id" json:"id"`
	Link      string    `db:"link" json:"link"` // Single link URL for all announcement translations
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

// AnnounceWithTranslations combines announce settings with translations
type AnnounceWithTranslations struct {
	Link         string
	Translations []AnnounceTranslation
}
