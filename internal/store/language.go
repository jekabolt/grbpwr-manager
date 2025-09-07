package store

import (
	"context"
	"fmt"

	"github.com/jekabolt/grbpwr-manager/internal/dependency"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
)

type languageStore struct {
	*MYSQLStore
}

// Language returns an object implementing language interface
func (ms *MYSQLStore) Language() dependency.Language {
	return &languageStore{
		MYSQLStore: ms,
	}
}

// GetAllLanguages returns all available languages
func (ls *languageStore) GetAllLanguages(ctx context.Context) ([]entity.Language, error) {
	query := `SELECT id, code, name, is_default, is_active, created_at, updated_at FROM language ORDER BY is_default DESC, name ASC`

	var languages []entity.Language
	err := ls.db.SelectContext(ctx, &languages, query)
	if err != nil {
		return nil, fmt.Errorf("failed to get languages: %w", err)
	}

	return languages, nil
}

// GetActiveLanguages returns only active languages
func (ls *languageStore) GetActiveLanguages(ctx context.Context) ([]entity.Language, error) {
	query := `SELECT id, code, name, is_default, is_active, created_at, updated_at FROM language WHERE is_active = true ORDER BY is_default DESC, name ASC`

	var languages []entity.Language
	err := ls.db.SelectContext(ctx, &languages, query)
	if err != nil {
		return nil, fmt.Errorf("failed to get active languages: %w", err)
	}

	return languages, nil
}

// GetLanguageByCode returns a language by its code
func (ls *languageStore) GetLanguageByCode(ctx context.Context, code string) (*entity.Language, error) {
	query := `SELECT id, code, name, is_default, is_active, created_at, updated_at FROM language WHERE code = ?`

	var language entity.Language
	err := ls.db.GetContext(ctx, &language, query, code)
	if err != nil {
		return nil, fmt.Errorf("failed to get language by code %s: %w", code, err)
	}

	return &language, nil
}

// GetDefaultLanguage returns the default language
func (ls *languageStore) GetDefaultLanguage(ctx context.Context) (*entity.Language, error) {
	query := `SELECT id, code, name, is_default, is_active, created_at, updated_at FROM language WHERE is_default = true LIMIT 1`

	var language entity.Language
	err := ls.db.GetContext(ctx, &language, query)
	if err != nil {
		return nil, fmt.Errorf("failed to get default language: %w", err)
	}

	return &language, nil
}
