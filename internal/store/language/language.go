// Package language implements language management operations.
package language

import (
	"context"
	"fmt"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/jekabolt/grbpwr-manager/internal/store/storeutil"
)

// Store implements dependency.Language.
type Store struct {
	storeutil.Base
}

// New creates a new language store.
func New(base storeutil.Base) *Store {
	return &Store{Base: base}
}

// GetAllLanguages returns all available languages.
func (s *Store) GetAllLanguages(ctx context.Context) ([]entity.Language, error) {
	query := `SELECT id, code, name, is_default, is_active, created_at, updated_at FROM language ORDER BY is_default DESC, name ASC`

	var languages []entity.Language
	err := s.DB.SelectContext(ctx, &languages, query)
	if err != nil {
		return nil, fmt.Errorf("failed to get languages: %w", err)
	}

	return languages, nil
}

// GetActiveLanguages returns only active languages.
func (s *Store) GetActiveLanguages(ctx context.Context) ([]entity.Language, error) {
	query := `SELECT id, code, name, is_default, is_active, created_at, updated_at FROM language WHERE is_active = true ORDER BY is_default DESC, name ASC`

	var languages []entity.Language
	err := s.DB.SelectContext(ctx, &languages, query)
	if err != nil {
		return nil, fmt.Errorf("failed to get active languages: %w", err)
	}

	return languages, nil
}

// GetLanguageByCode returns a language by its code.
func (s *Store) GetLanguageByCode(ctx context.Context, code string) (*entity.Language, error) {
	query := `SELECT id, code, name, is_default, is_active, created_at, updated_at FROM language WHERE code = ?`

	var language entity.Language
	err := s.DB.GetContext(ctx, &language, query, code)
	if err != nil {
		return nil, fmt.Errorf("failed to get language by code %s: %w", code, err)
	}

	return &language, nil
}

// GetDefaultLanguage returns the default language.
func (s *Store) GetDefaultLanguage(ctx context.Context) (*entity.Language, error) {
	query := `SELECT id, code, name, is_default, is_active, created_at, updated_at FROM language WHERE is_default = true LIMIT 1`

	var language entity.Language
	err := s.DB.GetContext(ctx, &language, query)
	if err != nil {
		return nil, fmt.Errorf("failed to get default language: %w", err)
	}

	return &language, nil
}
