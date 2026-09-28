// Package dictionary implements CRUD for the controlled merch dictionaries (colour, collection, tag)
// and set-active for the closed country dictionary (R9). Every mutation bumps the namespace's
// dictionary_revision row in the SAME transaction, under a FOR UPDATE lock that also enforces the
// caller's optimistic expected_version — so a stale writer is rejected and every instance can detect a
// changed dictionary via the revision counter (see internal/cache versioned invalidation).
package dictionary

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/jekabolt/grbpwr-manager/internal/dependency"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/jekabolt/grbpwr-manager/internal/store/storeutil"
)

// TxFunc executes f within a serializable transaction with deadlock retry.
type TxFunc func(ctx context.Context, f func(context.Context, dependency.Repository) error) error

// Store implements dependency.Dictionary.
type Store struct {
	storeutil.Base
	txFunc TxFunc
}

// New creates a new dictionary store.
func New(base storeutil.Base, txFunc TxFunc) *Store {
	return &Store{Base: base, txFunc: txFunc}
}

type revRow struct {
	Revision int64 `db:"revision"`
}

func nullStr(s string) sql.NullString {
	s = strings.TrimSpace(s)
	return sql.NullString{String: s, Valid: s != ""}
}

// mutateWithRevision locks the namespace revision row, enforces the optimistic expected_version
// (0 opts out), runs the mutation, bumps the revision, and returns the new revision — all in one tx.
func (s *Store) mutateWithRevision(
	ctx context.Context,
	ns entity.DictionaryNamespace,
	expectedVersion int64,
	mutate func(ctx context.Context, rep dependency.Repository) error,
) (int64, error) {
	var newRev int64
	err := s.txFunc(ctx, func(ctx context.Context, rep dependency.Repository) error {
		cur, err := storeutil.QueryNamedOne[revRow](ctx, rep.DB(),
			`SELECT revision FROM dictionary_revision WHERE namespace = :ns FOR UPDATE`,
			map[string]any{"ns": string(ns)})
		if err != nil {
			return fmt.Errorf("lock dictionary_revision %q: %w", ns, err)
		}
		if err := entity.CheckExpectedRevision(expectedVersion, cur.Revision); err != nil {
			return err
		}
		if err := mutate(ctx, rep); err != nil {
			return err
		}
		if err := storeutil.ExecNamed(ctx, rep.DB(),
			`UPDATE dictionary_revision SET revision = revision + 1 WHERE namespace = :ns`,
			map[string]any{"ns": string(ns)}); err != nil {
			return fmt.Errorf("bump dictionary_revision %q: %w", ns, err)
		}
		newRev = cur.Revision + 1
		return nil
	})
	if err != nil {
		return 0, err
	}
	return newRev, nil
}

// GetDictionaryRevisions returns the current revision for every dictionary namespace.
func (s *Store) GetDictionaryRevisions(ctx context.Context) (map[entity.DictionaryNamespace]int64, error) {
	rows, err := storeutil.QueryListNamed[entity.DictionaryRevision](ctx, s.DB,
		`SELECT namespace, revision FROM dictionary_revision`, map[string]any{})
	if err != nil {
		return nil, fmt.Errorf("list dictionary revisions: %w", err)
	}
	out := make(map[entity.DictionaryNamespace]int64, len(rows))
	for _, r := range rows {
		out[entity.DictionaryNamespace(r.Namespace)] = r.Revision
	}
	return out, nil
}

// ---- Colour ----------------------------------------------------------------

// ListColors returns colour dictionary entries; archived entries are included only when requested.
func (s *Store) ListColors(ctx context.Context, includeArchived bool) ([]entity.Color, error) {
	q := `SELECT id, code, name, hex, archived_at FROM color`
	if !includeArchived {
		q += ` WHERE archived_at IS NULL`
	}
	q += ` ORDER BY code`
	colors, err := storeutil.QueryListNamed[entity.Color](ctx, s.DB, q, map[string]any{})
	if err != nil {
		return nil, fmt.Errorf("list colours: %w", err)
	}
	return colors, nil
}

// CreateColor inserts a new colour. Code is normalised and validated ([A-Z0-9]{3}); uniqueness is
// enforced by the table constraint. Returns the created entry and the new colour revision.
func (s *Store) CreateColor(ctx context.Context, code, name, hex string, expectedVersion int64) (entity.Color, int64, error) {
	code = entity.NormalizeColorCode(code)
	if err := entity.ValidateColorCode(code); err != nil {
		return entity.Color{}, 0, err
	}
	name = strings.TrimSpace(name)
	if name == "" {
		return entity.Color{}, 0, fmt.Errorf("colour name is required")
	}
	var created entity.Color
	rev, err := s.mutateWithRevision(ctx, entity.DictNamespaceColor, expectedVersion, func(ctx context.Context, rep dependency.Repository) error {
		id, err := storeutil.ExecNamedLastId(ctx, rep.DB(),
			`INSERT INTO color (code, name, hex) VALUES (:code, :name, :hex)`,
			map[string]any{"code": code, "name": name, "hex": nullStr(hex)})
		if err != nil {
			return fmt.Errorf("insert colour: %w", err)
		}
		created = entity.Color{ID: id, Code: code, Name: name, Hex: nullStr(hex)}
		return nil
	})
	return created, rev, err
}

// UpdateColor updates the display name and base hex of an existing colour. The code is immutable (R9):
// it is the lookup key and is never rewritten, so an in-use colour can never be renamed at the code
// level, only re-labelled. Returns the new colour revision.
func (s *Store) UpdateColor(ctx context.Context, code, name, hex string, expectedVersion int64) (int64, error) {
	code = entity.NormalizeColorCode(code)
	if err := entity.ValidateColorCode(code); err != nil {
		return 0, err
	}
	name = strings.TrimSpace(name)
	if name == "" {
		return 0, fmt.Errorf("colour name is required")
	}
	return s.mutateWithRevision(ctx, entity.DictNamespaceColor, expectedVersion, func(ctx context.Context, rep dependency.Repository) error {
		n, err := storeutil.ExecNamedRows(ctx, rep.DB(),
			`UPDATE color SET name = :name, hex = :hex WHERE code = :code`,
			map[string]any{"code": code, "name": name, "hex": nullStr(hex)})
		if err != nil {
			return fmt.Errorf("update colour: %w", err)
		}
		if n == 0 {
			return fmt.Errorf("colour %q not found", code)
		}
		return nil
	})
}

// ArchiveColor soft-deletes a colour (R9 archive-not-delete). The row and its FK references stay valid;
// the colour is simply hidden from selection for new products. Returns the new colour revision.
func (s *Store) ArchiveColor(ctx context.Context, code string, expectedVersion int64) (int64, error) {
	code = entity.NormalizeColorCode(code)
	return s.mutateWithRevision(ctx, entity.DictNamespaceColor, expectedVersion, func(ctx context.Context, rep dependency.Repository) error {
		n, err := storeutil.ExecNamedRows(ctx, rep.DB(),
			`UPDATE color SET archived_at = NOW() WHERE code = :code AND archived_at IS NULL`,
			map[string]any{"code": code})
		if err != nil {
			return fmt.Errorf("archive colour: %w", err)
		}
		if n == 0 {
			return fmt.Errorf("colour %q not found or already archived", code)
		}
		return nil
	})
}

// ---- Collection ------------------------------------------------------------

// ListCollections returns collection dictionary entries; archived entries only when requested.
func (s *Store) ListCollections(ctx context.Context, includeArchived bool) ([]entity.CollectionDict, error) {
	q := `SELECT id, code, name, translations, archived_at FROM collection`
	if !includeArchived {
		q += ` WHERE archived_at IS NULL`
	}
	q += ` ORDER BY name`
	rows, err := storeutil.QueryListNamed[entity.CollectionDict](ctx, s.DB, q, map[string]any{})
	if err != nil {
		return nil, fmt.Errorf("list collections: %w", err)
	}
	return rows, nil
}

// CreateCollection inserts a new collection. The code is derived from the name via NormalizeDictSlug
// (byte-compatible with the 0151 backfill), so re-adding a legacy value maps onto the same code.
func (s *Store) CreateCollection(ctx context.Context, name string, expectedVersion int64) (entity.CollectionDict, int64, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return entity.CollectionDict{}, 0, fmt.Errorf("collection name is required")
	}
	code := entity.NormalizeDictSlug(name)
	if code == "" {
		return entity.CollectionDict{}, 0, fmt.Errorf("collection name %q has no alphanumeric content", name)
	}
	var created entity.CollectionDict
	rev, err := s.mutateWithRevision(ctx, entity.DictNamespaceCollection, expectedVersion, func(ctx context.Context, rep dependency.Repository) error {
		id, err := storeutil.ExecNamedLastId(ctx, rep.DB(),
			`INSERT INTO collection (code, name) VALUES (:code, :name)`,
			map[string]any{"code": code, "name": name})
		if err != nil {
			return fmt.Errorf("insert collection: %w", err)
		}
		created = entity.CollectionDict{ID: id, Code: code, Name: name}
		return nil
	})
	return created, rev, err
}

// UpdateCollection re-labels a collection (display name only; the code is immutable, R9).
func (s *Store) UpdateCollection(ctx context.Context, id int, name string, expectedVersion int64) (int64, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return 0, fmt.Errorf("collection name is required")
	}
	return s.mutateWithRevision(ctx, entity.DictNamespaceCollection, expectedVersion, func(ctx context.Context, rep dependency.Repository) error {
		n, err := storeutil.ExecNamedRows(ctx, rep.DB(),
			`UPDATE collection SET name = :name WHERE id = :id`,
			map[string]any{"id": id, "name": name})
		if err != nil {
			return fmt.Errorf("update collection: %w", err)
		}
		if n == 0 {
			return fmt.Errorf("collection %d not found", id)
		}
		return nil
	})
}

// ArchiveCollection soft-deletes a collection (R9 archive-not-delete).
func (s *Store) ArchiveCollection(ctx context.Context, id int, expectedVersion int64) (int64, error) {
	return s.mutateWithRevision(ctx, entity.DictNamespaceCollection, expectedVersion, func(ctx context.Context, rep dependency.Repository) error {
		n, err := storeutil.ExecNamedRows(ctx, rep.DB(),
			`UPDATE collection SET archived_at = NOW() WHERE id = :id AND archived_at IS NULL`,
			map[string]any{"id": id})
		if err != nil {
			return fmt.Errorf("archive collection: %w", err)
		}
		if n == 0 {
			return fmt.Errorf("collection %d not found or already archived", id)
		}
		return nil
	})
}

// ---- Tag -------------------------------------------------------------------

// ListTags returns tag dictionary entries; archived entries only when requested.
func (s *Store) ListTags(ctx context.Context, includeArchived bool) ([]entity.TagDict, error) {
	q := `SELECT id, code, name, translations, archived_at FROM tag`
	if !includeArchived {
		q += ` WHERE archived_at IS NULL`
	}
	q += ` ORDER BY name`
	rows, err := storeutil.QueryListNamed[entity.TagDict](ctx, s.DB, q, map[string]any{})
	if err != nil {
		return nil, fmt.Errorf("list tags: %w", err)
	}
	return rows, nil
}

// CreateTag inserts a new tag. The code is derived from the name via NormalizeDictSlug.
func (s *Store) CreateTag(ctx context.Context, name string, expectedVersion int64) (entity.TagDict, int64, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return entity.TagDict{}, 0, fmt.Errorf("tag name is required")
	}
	code := entity.NormalizeDictSlug(name)
	if code == "" {
		return entity.TagDict{}, 0, fmt.Errorf("tag name %q has no alphanumeric content", name)
	}
	var created entity.TagDict
	rev, err := s.mutateWithRevision(ctx, entity.DictNamespaceTag, expectedVersion, func(ctx context.Context, rep dependency.Repository) error {
		id, err := storeutil.ExecNamedLastId(ctx, rep.DB(),
			`INSERT INTO tag (code, name) VALUES (:code, :name)`,
			map[string]any{"code": code, "name": name})
		if err != nil {
			return fmt.Errorf("insert tag: %w", err)
		}
		created = entity.TagDict{ID: id, Code: code, Name: name}
		return nil
	})
	return created, rev, err
}

// UpdateTag re-labels a tag (display name only; the code is immutable, R9).
func (s *Store) UpdateTag(ctx context.Context, id int, name string, expectedVersion int64) (int64, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return 0, fmt.Errorf("tag name is required")
	}
	return s.mutateWithRevision(ctx, entity.DictNamespaceTag, expectedVersion, func(ctx context.Context, rep dependency.Repository) error {
		n, err := storeutil.ExecNamedRows(ctx, rep.DB(),
			`UPDATE tag SET name = :name WHERE id = :id`,
			map[string]any{"id": id, "name": name})
		if err != nil {
			return fmt.Errorf("update tag: %w", err)
		}
		if n == 0 {
			return fmt.Errorf("tag %d not found", id)
		}
		return nil
	})
}

// ArchiveTag soft-deletes a tag (R9 archive-not-delete).
func (s *Store) ArchiveTag(ctx context.Context, id int, expectedVersion int64) (int64, error) {
	return s.mutateWithRevision(ctx, entity.DictNamespaceTag, expectedVersion, func(ctx context.Context, rep dependency.Repository) error {
		n, err := storeutil.ExecNamedRows(ctx, rep.DB(),
			`UPDATE tag SET archived_at = NOW() WHERE id = :id AND archived_at IS NULL`,
			map[string]any{"id": id})
		if err != nil {
			return fmt.Errorf("archive tag: %w", err)
		}
		if n == 0 {
			return fmt.Errorf("tag %d not found or already archived", id)
		}
		return nil
	})
}

// ---- Fiber (controlled fibre vocabulary, S17/P0.4) -------------------------

// ListFibers returns fibre dictionary entries; archived entries only when requested.
func (s *Store) ListFibers(ctx context.Context, includeArchived bool) ([]entity.Fiber, error) {
	q := `SELECT code, name, archived_at, animal_non_textile FROM fiber`
	if !includeArchived {
		q += ` WHERE archived_at IS NULL`
	}
	q += ` ORDER BY code`
	rows, err := storeutil.QueryListNamed[entity.Fiber](ctx, s.DB, q, map[string]any{})
	if err != nil {
		return nil, fmt.Errorf("list fibres: %w", err)
	}
	return rows, nil
}

// CreateFiber inserts a new fibre. The code is normalised (upper-case, trimmed) and validated
// ([A-Z0-9]{1,8}); uniqueness is enforced by the table primary key. labelNames are the optional
// care-label names (already normalised by entity.NormalizeFiberLabelTranslations; nil = none) and
// ride the same transaction. Returns the created entry and the new fibre revision.
func (s *Store) CreateFiber(ctx context.Context, code, name string, labelNames map[string]string, expectedVersion int64) (entity.Fiber, int64, error) {
	code = strings.ToUpper(strings.TrimSpace(code))
	if err := entity.ValidateFiberCode(code); err != nil {
		return entity.Fiber{}, 0, err
	}
	name = strings.TrimSpace(name)
	if name == "" {
		return entity.Fiber{}, 0, fmt.Errorf("fibre name is required")
	}
	var created entity.Fiber
	rev, err := s.mutateWithRevision(ctx, entity.DictNamespaceFiber, expectedVersion, func(ctx context.Context, rep dependency.Repository) error {
		if err := storeutil.ExecNamed(ctx, rep.DB(),
			`INSERT INTO fiber (code, name) VALUES (:code, :name)`,
			map[string]any{"code": code, "name": name}); err != nil {
			return fmt.Errorf("insert fibre: %w", err)
		}
		if err := insertFiberLabelNames(ctx, rep, code, labelNames); err != nil {
			return err
		}
		created = entity.Fiber{Code: code, Name: name, LabelTranslations: labelNames}
		return nil
	})
	return created, rev, err
}

// UpsertFiberLabelTranslations replaces a fibre's care-label names as a whole set: every stored
// language is deleted and labelNames (already normalised — see entity.NormalizeFiberLabelTranslations,
// which drops empty names) is inserted, so a language left out or sent empty disappears. A non-nil
// animalNonTextile sets the flag; nil leaves it. All of it, and the revision bump, is one
// transaction. Returns the fibre as stored after the write.
func (s *Store) UpsertFiberLabelTranslations(ctx context.Context, code string, labelNames map[string]string, animalNonTextile *bool, expectedVersion int64) (entity.Fiber, int64, error) {
	code = strings.ToUpper(strings.TrimSpace(code))
	if err := entity.ValidateFiberCode(code); err != nil {
		return entity.Fiber{}, 0, err
	}
	var out entity.Fiber
	rev, err := s.mutateWithRevision(ctx, entity.DictNamespaceFiber, expectedVersion, func(ctx context.Context, rep dependency.Repository) error {
		// FOR UPDATE: the fibre row is the anchor of the set; locking it keeps a concurrent archive or
		// flag write from interleaving with the delete-then-insert below.
		f, err := storeutil.QueryNamedOne[entity.Fiber](ctx, rep.DB(),
			`SELECT code, name, archived_at, animal_non_textile FROM fiber WHERE code = :code FOR UPDATE`,
			map[string]any{"code": code})
		if err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return fmt.Errorf("fibre %q not found", code)
			}
			return fmt.Errorf("load fibre %q: %w", code, err)
		}
		if animalNonTextile != nil {
			if err := storeutil.ExecNamed(ctx, rep.DB(),
				`UPDATE fiber SET animal_non_textile = :flag WHERE code = :code`,
				map[string]any{"code": code, "flag": *animalNonTextile}); err != nil {
				return fmt.Errorf("set fibre animal_non_textile: %w", err)
			}
			f.AnimalNonTextile = *animalNonTextile
		}
		if err := storeutil.ExecNamed(ctx, rep.DB(),
			`DELETE FROM fiber_label_translation WHERE fiber_code = :code`,
			map[string]any{"code": code}); err != nil {
			return fmt.Errorf("clear fibre label translations: %w", err)
		}
		if err := insertFiberLabelNames(ctx, rep, code, labelNames); err != nil {
			return err
		}
		f.LabelTranslations = labelNames
		out = f
		return nil
	})
	return out, rev, err
}

// insertFiberLabelNames writes one fiber_label_translation row per language, in the label's language
// order so the statement sequence is deterministic. A language outside entity.LabelLangs is refused
// here too (the CHECK would refuse it anyway, but with 3819 and no field address).
func insertFiberLabelNames(ctx context.Context, rep dependency.Repository, code string, labelNames map[string]string) error {
	for lang := range labelNames {
		if !entity.IsLabelLang(lang) {
			return entity.NewFieldViolation("translations", "unknown_label_lang", lang,
				"use one of "+strings.Join(entity.LabelLangs, " "))
		}
	}
	for _, lang := range entity.LabelLangs {
		name, ok := labelNames[lang]
		if !ok || strings.TrimSpace(name) == "" {
			continue
		}
		if err := storeutil.ExecNamed(ctx, rep.DB(),
			`INSERT INTO fiber_label_translation (fiber_code, label_lang, name) VALUES (:code, :lang, :name)`,
			map[string]any{"code": code, "lang": lang, "name": name}); err != nil {
			return fmt.Errorf("insert fibre label translation %s/%s: %w", code, lang, err)
		}
	}
	return nil
}

// ArchiveFiber soft-deletes a fibre (R9 archive-not-delete). The row and its composition FK references
// stay valid; the fibre is simply hidden from selection for new compositions. Returns the new revision.
func (s *Store) ArchiveFiber(ctx context.Context, code string, expectedVersion int64) (int64, error) {
	code = strings.ToUpper(strings.TrimSpace(code))
	return s.mutateWithRevision(ctx, entity.DictNamespaceFiber, expectedVersion, func(ctx context.Context, rep dependency.Repository) error {
		n, err := storeutil.ExecNamedRows(ctx, rep.DB(),
			`UPDATE fiber SET archived_at = NOW() WHERE code = :code AND archived_at IS NULL`,
			map[string]any{"code": code})
		if err != nil {
			return fmt.Errorf("archive fibre: %w", err)
		}
		if n == 0 {
			return fmt.Errorf("fibre %q not found or already archived", code)
		}
		return nil
	})
}

// ---- Country (closed dictionary: set-active only) --------------------------

// ListCountries returns country dictionary entries; when activeOnly is set, inactive codes are omitted.
func (s *Store) ListCountries(ctx context.Context, activeOnly bool) ([]entity.Country, error) {
	q := `SELECT code, display_name, translations, active FROM country`
	if activeOnly {
		q += ` WHERE active = 1`
	}
	q += ` ORDER BY code`
	rows, err := storeutil.QueryListNamed[entity.Country](ctx, s.DB, q, map[string]any{})
	if err != nil {
		return nil, fmt.Errorf("list countries: %w", err)
	}
	return rows, nil
}

// SetCountryActive toggles a country's availability. The country dictionary is CLOSED (R9): the ISO set
// is seeded and immutable, so this is the only country mutation — no create/rename/delete.
func (s *Store) SetCountryActive(ctx context.Context, code string, active bool, expectedVersion int64) (int64, error) {
	code = strings.ToUpper(strings.TrimSpace(code))
	if len(code) != 2 {
		return 0, fmt.Errorf("invalid country code %q: must be ISO 3166-1 alpha-2", code)
	}
	return s.mutateWithRevision(ctx, entity.DictNamespaceCountry, expectedVersion, func(ctx context.Context, rep dependency.Repository) error {
		n, err := storeutil.ExecNamedRows(ctx, rep.DB(),
			`UPDATE country SET active = :active WHERE code = :code`,
			map[string]any{"code": code, "active": active})
		if err != nil {
			return fmt.Errorf("set country active: %w", err)
		}
		if n == 0 {
			return fmt.Errorf("country %q not found", code)
		}
		return nil
	})
}
