// Package ai implements the AI providers store: the configuration of 0373 (ai_provider, ai_model,
// ai_route, ai_settings) and the ledger of 0374 (ai_usage_event, ai_provider_cost_daily).
//
// Two halves with opposite rules. The CONFIG half is small, rare and written by one super admin at a
// time: every write runs in one transaction that first moves ai_settings.config_version, so all
// config writers serialise on that one row and the registry learns about the write by polling one
// number; its one read (GetConfig) runs in one read-only snapshot, so it never returns a mix of two
// versions. The LEDGER half is written on every provider call: single autocommit statements, never a
// SERIALIZABLE transaction (whose range locks would make a report block the calls it is reporting on),
// and no validation beyond what the columns cannot hold at all: a refused row is money unrecorded.
//
// Every statement goes through storeutil.MakeQuery, the named-parameter builder the storeutil helpers
// use, and reaches the handle through ExecContext / GetContext / SelectContext only — the three
// methods ai_shape_test.go answers with a recording fake, so every query here is exercised without a
// database.
package ai

import (
	"cmp"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jekabolt/grbpwr-manager/internal/dependency"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/jekabolt/grbpwr-manager/internal/store/storeutil"
	"github.com/shopspring/decimal"
)

// TxFunc is the transaction runner injected by the parent store.
type TxFunc func(ctx context.Context, f func(context.Context, dependency.Repository) error) error

// Store implements dependency.AI.
type Store struct {
	storeutil.Base
	txFunc     TxFunc
	readTxFunc TxFunc
}

var _ dependency.AI = (*Store)(nil)

// New creates a new AI providers store. readTxFunc is separate and load-bearing, as in store/design:
// GetConfig reads its five tables inside it (store.readTx — REPEATABLE READ, read-only), one
// snapshot, where the SERIALIZABLE txFunc would take a shared lock on every config row it reads.
func New(base storeutil.Base, txFunc, readTxFunc TxFunc) *Store {
	return &Store{Base: base, txFunc: txFunc, readTxFunc: readTxFunc}
}

// NewInTx builds the AI store of a TRANSACTIONAL repository — store.go's initSubStoresForTx, with
// enclosing the transaction's own repository. Its writes keep txFunc, as every sub-store there does;
// GetConfig reads through enclosing and begins nothing (Codex second review, P2). txFunc there is
// the ROOT store's Tx, so a read run in it opened a SECOND transaction on another connection: blind
// to the enclosing transaction's uncommitted writes, and waiting on its locks until the context
// expired. Before the one-snapshot read, GetConfig read s.DB — which on that path IS the enclosing
// transaction; this keeps it there.
func NewInTx(base storeutil.Base, txFunc TxFunc, enclosing dependency.Repository) *Store {
	return New(base, txFunc, sameTx(enclosing))
}

// sameTx is a runner that begins nothing: f runs on rep, inside whatever transaction rep already is,
// and its error is returned to the caller that owns that transaction.
func sameTx(rep dependency.Repository) TxFunc {
	return func(ctx context.Context, f func(context.Context, dependency.Repository) error) error {
		return f(ctx, rep)
	}
}

const (
	// defaultBudgetTimezone is design_settings.budget_timezone's column default (0344) and the
	// fallback store/design uses when that singleton is missing: the ledger's days and the design
	// band's days must be the same days.
	defaultBudgetTimezone = "Europe/Warsaw"
	// defaultProviderKey is the column default of both ai_settings.default_*_provider_key (0373).
	defaultProviderKey = entity.AIProviderOpenRouter
	// maxRouteCandidates bounds one purpose's route. The panel offers a primary and a fallback; the
	// bound only keeps a buggy client from writing positions the TINYINT column cannot hold.
	maxRouteCandidates = 8
	// dayLayout is the wire and column form of a calendar day.
	dayLayout = "2006-01-02"
)

// Column widths of 0373 / 0374, in characters. The ledger CLIPS to them (a money row is never lost to
// a 1406 because a provider sent a long request id); the config half REFUSES past them (a person typed
// it and can fix it).
const (
	widthProviderKey  = 32
	widthModel        = 128
	widthLabel        = 128
	widthPurpose      = 48
	widthActor        = 255
	widthRequestID    = 128
	widthErrorCode    = 64
	widthUnit         = 16
	widthPriceVersion = 16
	widthLast4        = 4
	widthKeyEnc       = 2048 // bytes, VARBINARY
	maxTinyUnsigned   = 255
)

// ───────────────────────── config: reads ─────────────────────────

const selectAISettings = `
	SELECT config_version, default_chat_provider_key, default_image_provider_key, updated_by, updated_at
	FROM ai_settings WHERE id = 1`

// The key slots are nullable; the entity carries "" for "never set", so the read folds NULL there.
// The ciphertext columns stay as they are: nil []byte IS "no key".
const selectAIProviders = `
	SELECT provider_key, label, enabled,
	       api_key_enc, COALESCE(api_key_last4, '') AS api_key_last4, api_key_updated_at,
	       COALESCE(api_key_updated_by, '') AS api_key_updated_by,
	       admin_key_enc, COALESCE(admin_key_last4, '') AS admin_key_last4, admin_key_updated_at,
	       COALESCE(admin_key_updated_by, '') AS admin_key_updated_by,
	       updated_by, updated_at
	FROM ai_provider`

const selectAIModels = `
	SELECT provider_key, model, label, kind, disabled
	FROM ai_model
	ORDER BY provider_key, model`

const selectAIRoutes = `
	SELECT purpose, position, provider_key, model
	FROM ai_route
	ORDER BY purpose, position`

const selectBudgetTimezone = `SELECT budget_timezone FROM design_settings WHERE id = 1`

// routeRow is one ai_route row before it is grouped into its purpose.
type routeRow struct {
	Purpose string `db:"purpose"`
	entity.AIRouteCandidate
}

// GetConfig returns the whole configuration the registry snapshots — the data of ONE config_version.
//
// ONE SNAPSHOT (Codex A1 #2). As five autocommit reads, a write committed between two of them handed
// the registry settings of one version and routes of the next — a configuration no version ever
// described — and the registry published it until its next poll. Now the reads run inside readTxFunc
// (store.readTx: REPEATABLE READ, read-only), where InnoDB answers every read from the one snapshot
// the first read takes; every config writer moves config_version and its rows in ONE transaction, so
// the snapshot holds all of a write or none of it, and the version returned describes exactly the rows
// returned.
//
// INSIDE A TRANSACTION (NewInTx) the reads run in the ENCLOSING transaction, not in a new one: they
// see what that transaction has written so far, and begin no second transaction that would wait on
// its locks.
//
// THE VERSION IS STILL READ FIRST. Inside a snapshot the order is free; it is not where the enclosing
// transaction is SERIALIZABLE (db.Tx's): there every read is a locking read, and taking ai_settings
// first takes the row each writer takes first, so no write can land between the reads.
func (s *Store) GetConfig(ctx context.Context) (*entity.AIConfig, error) {
	var cfg *entity.AIConfig
	err := s.readTxFunc(ctx, func(ctx context.Context, rep dependency.Repository) error {
		var err error
		cfg, err = readConfig(ctx, rep.DB())
		return err
	})
	if err != nil {
		return nil, err
	}
	return cfg, nil
}

// readConfig is GetConfig's five reads on one handle — the snapshot's.
func readConfig(ctx context.Context, db dependency.DB) (*entity.AIConfig, error) {
	settings, err := loadSettings(ctx, db)
	if err != nil {
		return nil, err
	}

	var providers []entity.AIProvider
	if err := selectNamed(ctx, db, &providers, selectAIProviders, nil); err != nil {
		return nil, fmt.Errorf("failed to read ai providers: %w", err)
	}
	byKey := vocabCompare(entity.AIProviderKeys())
	slices.SortStableFunc(providers, func(a, b entity.AIProvider) int { return byKey(a.Key, b.Key) })

	var models []entity.AIModel
	if err := selectNamed(ctx, db, &models, selectAIModels, nil); err != nil {
		return nil, fmt.Errorf("failed to read ai models: %w", err)
	}

	var rows []routeRow
	if err := selectNamed(ctx, db, &rows, selectAIRoutes, nil); err != nil {
		return nil, fmt.Errorf("failed to read ai routes: %w", err)
	}

	tz, err := loadBudgetTimezone(ctx, db)
	if err != nil {
		return nil, err
	}

	return &entity.AIConfig{
		Providers:      providers,
		Models:         models,
		Routes:         groupRoutes(rows),
		Settings:       settings,
		BudgetTimezone: tz,
	}, nil
}

// ConfigVersion is the one number the registry polls.
func (s *Store) ConfigVersion(ctx context.Context) (uint64, error) {
	st, err := loadSettings(ctx, s.DB)
	if err != nil {
		return 0, err
	}
	return st.ConfigVersion, nil
}

// defaultSettings is what 0373's column defaults would give a freshly inserted singleton.
func defaultSettings() entity.AISettings {
	return entity.AISettings{
		ConfigVersion:           1,
		DefaultChatProviderKey:  defaultProviderKey,
		DefaultImageProviderKey: defaultProviderKey,
	}
}

func loadSettings(ctx context.Context, db dependency.DB) (entity.AISettings, error) {
	var st entity.AISettings
	if err := getNamed(ctx, db, &st, selectAISettings, nil); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			// 0373 seeds the row, so only a manual DELETE reaches this. It reads as the schema
			// defaults — version 1 — and bumpVersion re-creates the row with those same defaults on
			// the next write, so a page that loaded version 1 saves on the first try instead of
			// meeting a conflict nothing could ever resolve.
			return defaultSettings(), nil
		}
		return st, fmt.Errorf("failed to read ai settings: %w", err)
	}
	return st, nil
}

func loadBudgetTimezone(ctx context.Context, db dependency.DB) (string, error) {
	var tz string
	if err := getNamed(ctx, db, &tz, selectBudgetTimezone, nil); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			// The same fallback store/design reads for a missing design_settings row.
			return defaultBudgetTimezone, nil
		}
		return "", fmt.Errorf("failed to read design budget timezone: %w", err)
	}
	return tz, nil
}

// groupRoutes folds ai_route rows into one route per purpose: candidates by position, purposes in the
// vocabulary's order, an unknown purpose (a row written by a newer build) after them, alphabetically.
func groupRoutes(rows []routeRow) []entity.AIRoute {
	index := map[string]int{}
	var routes []entity.AIRoute
	for _, r := range rows {
		i, ok := index[r.Purpose]
		if !ok {
			i = len(routes)
			index[r.Purpose] = i
			routes = append(routes, entity.AIRoute{Purpose: r.Purpose})
		}
		routes[i].Candidates = append(routes[i].Candidates, r.AIRouteCandidate)
	}
	byPurpose := vocabCompare(entity.AIPurposes())
	slices.SortStableFunc(routes, func(a, b entity.AIRoute) int { return byPurpose(a.Purpose, b.Purpose) })
	for i := range routes {
		slices.SortStableFunc(routes[i].Candidates, func(a, b entity.AIRouteCandidate) int {
			return cmp.Compare(a.Position, b.Position)
		})
	}
	return routes
}

// vocabCompare orders known values by their position in vocab and unknown ones after, alphabetically.
func vocabCompare(vocab []string) func(a, b string) int {
	rank := make(map[string]int, len(vocab))
	for i, v := range vocab {
		rank[v] = i
	}
	return func(a, b string) int {
		ra, okA := rank[a]
		rb, okB := rank[b]
		switch {
		case okA && okB:
			return cmp.Compare(ra, rb)
		case okA:
			return -1
		case okB:
			return 1
		}
		return cmp.Compare(a, b)
	}
}

// ───────────────────────── config: the version ─────────────────────────

const bumpConfigVersionChecked = `
	UPDATE ai_settings SET config_version = config_version + 1, updated_by = :by
	WHERE id = 1 AND config_version = :expected_version`

const bumpConfigVersion = `
	UPDATE ai_settings SET config_version = config_version + 1, updated_by = :by
	WHERE id = 1`

const countAISettings = `SELECT COUNT(*) FROM ai_settings WHERE id = 1`

const ensureAISettings = `INSERT IGNORE INTO ai_settings (id) VALUES (1)`

// bumpVersion moves config_version on by one inside the caller's transaction, as its FIRST statement:
// every config writer takes the singleton's row lock before anything else, so two writers serialise
// instead of deadlocking on each other's rows. expected != nil makes it a compare-and-swap.
//
// 0 rows means the page is stale — unless the singleton itself is gone (only a manual DELETE does
// that), in which case it is re-created with 0373's defaults (version 1, what loadSettings reported
// for the missing row) and the bump is tried once more. The existence probe runs only on that 0-row
// path, so the ordinary write takes no shared lock it would then have to upgrade.
func bumpVersion(ctx context.Context, db dependency.DB, expected *uint64, by string) error {
	query, params := bumpConfigVersion, map[string]any{"by": by}
	if expected != nil {
		query = bumpConfigVersionChecked
		params["expected_version"] = *expected
	}
	n, err := execRows(ctx, db, query, params)
	if err != nil {
		return fmt.Errorf("failed to bump ai config version: %w", err)
	}
	if n == 1 {
		return nil
	}
	var present int
	if err := getNamed(ctx, db, &present, countAISettings, nil); err != nil {
		return fmt.Errorf("failed to probe ai settings: %w", err)
	}
	if present == 0 {
		if _, err := execNamed(ctx, db, ensureAISettings, nil); err != nil {
			return fmt.Errorf("failed to re-create ai settings: %w", err)
		}
		if n, err = execRows(ctx, db, query, params); err != nil {
			return fmt.Errorf("failed to bump ai config version: %w", err)
		}
		if n == 1 {
			return nil
		}
	}
	if expected != nil {
		return entity.ErrAIVersionConflict
	}
	return fmt.Errorf("ai config version did not move (%d rows)", n)
}

// ───────────────────────── config: writes ─────────────────────────

const countAIProvider = `SELECT COUNT(*) FROM ai_provider WHERE provider_key = :provider_key`

const updateAIProviderEnabled = `
	UPDATE ai_provider SET enabled = :enabled, updated_by = :by
	WHERE provider_key = :provider_key`

// requireProvider refuses a write to a provider row that is not there. RowsAffected cannot say it:
// MySQL counts CHANGED rows, and re-saving the value a row already holds reports 0.
func requireProvider(ctx context.Context, db dependency.DB, key string) error {
	var n int
	if err := getNamed(ctx, db, &n, countAIProvider, map[string]any{"provider_key": key}); err != nil {
		return fmt.Errorf("failed to probe ai provider %q: %w", key, err)
	}
	if n == 0 {
		return fmt.Errorf("ai provider %q: %w", key, sql.ErrNoRows)
	}
	return nil
}

func validateProviderKey(field, key string) error {
	if !entity.IsAIProviderKey(key) {
		return entity.NewFieldViolation(field, "unknown_ai_provider", "",
			"name one of: "+strings.Join(entity.AIProviderKeys(), ", "))
	}
	return nil
}

// UpdateProvider applies a partial provider patch and bumps the version, compare-and-swap.
func (s *Store) UpdateProvider(ctx context.Context, key string, patch entity.AIProviderPatch, expectedVersion uint64, by string) error {
	if err := validateProviderKey("provider_key", key); err != nil {
		return err
	}
	if patch.Enabled == nil {
		return entity.NewFieldViolation("patch", "no_settings_named", "", "name at least one setting to write")
	}
	return s.txFunc(ctx, func(ctx context.Context, rep dependency.Repository) error {
		if err := bumpVersion(ctx, rep.DB(), &expectedVersion, by); err != nil {
			return err
		}
		if err := requireProvider(ctx, rep.DB(), key); err != nil {
			return err
		}
		if _, err := execNamed(ctx, rep.DB(), updateAIProviderEnabled, map[string]any{
			"provider_key": key,
			"enabled":      *patch.Enabled,
			"by":           by,
		}); err != nil {
			return fmt.Errorf("failed to update ai provider %q: %w", key, err)
		}
		return nil
	})
}

const setAIProviderAPIKey = `
	UPDATE ai_provider SET
		api_key_enc = :enc, api_key_last4 = :last4,
		api_key_updated_at = :at, api_key_updated_by = :by,
		updated_by = :by
	WHERE provider_key = :provider_key`

const setAIProviderAdminKey = `
	UPDATE ai_provider SET
		admin_key_enc = :enc, admin_key_last4 = :last4,
		admin_key_updated_at = :at, admin_key_updated_by = :by,
		updated_by = :by
	WHERE provider_key = :provider_key`

// keyWriteSQL picks the statement of one key slot. Two static statements, not a column name spliced
// from the kind: the kind arrives from a request.
func keyWriteSQL(kind entity.AIKeyKind) (string, bool) {
	switch kind {
	case entity.AIKeyAPI:
		return setAIProviderAPIKey, true
	case entity.AIKeyAdmin:
		return setAIProviderAdminKey, true
	}
	return "", false
}

// keyWriteParams binds one key write. An empty enc CLEARS the slot (ciphertext and last4 go NULL) but
// still records who cleared it and when.
func keyWriteParams(key string, enc []byte, last4, by string, at time.Time) map[string]any {
	p := map[string]any{
		"provider_key": key,
		"enc":          nil,
		"last4":        nil,
		"at":           at.UTC(),
		"by":           by,
	}
	if len(enc) > 0 {
		p["enc"] = enc
		if last4 != "" {
			p["last4"] = last4
		}
	}
	return p
}

// SetProviderKey stores one key slot's ciphertext (enc == nil or empty clears it) and bumps the version
// WITHOUT a check: a key write must never be blocked by a stale page.
func (s *Store) SetProviderKey(ctx context.Context, key string, kind entity.AIKeyKind, enc []byte, last4 string, by string) error {
	if err := validateProviderKey("provider_key", key); err != nil {
		return err
	}
	query, ok := keyWriteSQL(kind)
	if !ok {
		return entity.NewFieldViolation("kind", "unknown_ai_key_kind", "", "name api or admin")
	}
	if len(enc) > widthKeyEnc {
		return entity.NewFieldViolation("key", "key_too_long", "",
			fmt.Sprintf("the sealed key must fit %d bytes", widthKeyEnc))
	}
	if len(enc) > 0 && utf8.RuneCountInString(last4) > widthLast4 {
		return entity.NewFieldViolation("last4", "last4_too_long", "", "at most 4 characters")
	}
	params := keyWriteParams(key, enc, last4, by, s.Now())
	return s.txFunc(ctx, func(ctx context.Context, rep dependency.Repository) error {
		if err := bumpVersion(ctx, rep.DB(), nil, by); err != nil {
			return err
		}
		if err := requireProvider(ctx, rep.DB(), key); err != nil {
			return err
		}
		if _, err := execNamed(ctx, rep.DB(), query, params); err != nil {
			return fmt.Errorf("failed to write %s key of ai provider %q: %w", kind, key, err)
		}
		return nil
	})
}

// Presence, not value: an omitted default keeps its stored column (the 0265 IF() idiom, as in
// store/workshop).
const updateAIDefaults = `
	UPDATE ai_settings SET
		default_chat_provider_key = IF(:chat_omitted, default_chat_provider_key, :chat_provider_key),
		default_image_provider_key = IF(:image_omitted, default_image_provider_key, :image_provider_key),
		updated_by = :by
	WHERE id = 1`

func validateDefault(field string, key *string, capability string) error {
	if key == nil {
		return nil
	}
	if err := validateProviderKey(field, *key); err != nil {
		return err
	}
	if !entity.AIProviderServes(*key, capability) {
		return entity.NewFieldViolation(field, "provider_cannot_serve", *key,
			"choose a provider that serves "+capability)
	}
	return nil
}

func defaultsParams(patch entity.AIDefaultsPatch, by string) map[string]any {
	p := map[string]any{
		"chat_omitted":       patch.ChatProviderKey == nil,
		"chat_provider_key":  nil,
		"image_omitted":      patch.ImageProviderKey == nil,
		"image_provider_key": nil,
		"by":                 by,
	}
	if patch.ChatProviderKey != nil {
		p["chat_provider_key"] = *patch.ChatProviderKey
	}
	if patch.ImageProviderKey != nil {
		p["image_provider_key"] = *patch.ImageProviderKey
	}
	return p
}

// SetDefaults writes the default chat / image provider (the one an empty route provider means),
// compare-and-swap.
func (s *Store) SetDefaults(ctx context.Context, patch entity.AIDefaultsPatch, expectedVersion uint64, by string) error {
	if patch.ChatProviderKey == nil && patch.ImageProviderKey == nil {
		return entity.NewFieldViolation("patch", "no_settings_named", "", "name at least one default to write")
	}
	if err := validateDefault("chat_provider_key", patch.ChatProviderKey, entity.AICapabilityChat); err != nil {
		return err
	}
	if err := validateDefault("image_provider_key", patch.ImageProviderKey, entity.AICapabilityImage); err != nil {
		return err
	}
	params := defaultsParams(patch, by)
	return s.txFunc(ctx, func(ctx context.Context, rep dependency.Repository) error {
		if err := bumpVersion(ctx, rep.DB(), &expectedVersion, by); err != nil {
			return err
		}
		if _, err := execNamed(ctx, rep.DB(), updateAIDefaults, params); err != nil {
			return fmt.Errorf("failed to update ai defaults: %w", err)
		}
		return nil
	})
}

const deleteAIRoute = `DELETE FROM ai_route WHERE purpose = :purpose`

const insertAIRouteCandidate = `
	INSERT INTO ai_route (purpose, position, provider_key, model, updated_by)
	VALUES (:purpose, :position, :provider_key, :model, :by)`

// normaliseRoute validates a route and returns its candidates in order with positions 1..n.
//
// Order = Position ascending, the given order among equal positions (all-zero Positions therefore mean
// "as listed"). The stored positions are always renumbered 1..n, so a gap or a duplicate the caller
// sent can never reach the primary key.
func normaliseRoute(purpose string, candidates []entity.AIRouteCandidate) ([]entity.AIRouteCandidate, error) {
	capability := entity.AIPurposeCapability(purpose)
	if capability == "" {
		return nil, entity.NewFieldViolation("purpose", "unknown_ai_purpose", "",
			"name one of: "+strings.Join(entity.AIPurposes(), ", "))
	}
	if len(candidates) == 0 {
		return nil, entity.NewFieldViolation("candidates", "route_empty", "",
			"name at least the primary; an empty provider means the default one")
	}
	if len(candidates) > maxRouteCandidates {
		return nil, entity.NewFieldViolation("candidates", "route_too_long", "",
			fmt.Sprintf("at most %d candidates", maxRouteCandidates))
	}
	out := make([]entity.AIRouteCandidate, len(candidates))
	for i, c := range candidates {
		field := fmt.Sprintf("candidates[%d]", i)
		c.ProviderKey = strings.TrimSpace(c.ProviderKey)
		c.Model = strings.TrimSpace(c.Model)
		if c.ProviderKey != "" {
			if err := validateProviderKey(field+".provider_key", c.ProviderKey); err != nil {
				return nil, err
			}
			if !entity.AIProviderServes(c.ProviderKey, capability) {
				return nil, entity.NewFieldViolation(field+".provider_key", "provider_cannot_serve", c.ProviderKey,
					"choose a provider that serves "+capability)
			}
		}
		if utf8.RuneCountInString(c.Model) > widthModel {
			return nil, entity.NewFieldViolation(field+".model", "model_too_long", "",
				fmt.Sprintf("at most %d characters", widthModel))
		}
		out[i] = c
	}
	slices.SortStableFunc(out, func(a, b entity.AIRouteCandidate) int { return cmp.Compare(a.Position, b.Position) })
	for i := range out {
		out[i].Position = i + 1
	}
	return out, nil
}

// SetRoute replaces the purpose's whole route in one transaction, compare-and-swap.
func (s *Store) SetRoute(ctx context.Context, purpose string, candidates []entity.AIRouteCandidate, expectedVersion uint64, by string) error {
	route, err := normaliseRoute(purpose, candidates)
	if err != nil {
		return err
	}
	return s.txFunc(ctx, func(ctx context.Context, rep dependency.Repository) error {
		if err := bumpVersion(ctx, rep.DB(), &expectedVersion, by); err != nil {
			return err
		}
		if _, err := execNamed(ctx, rep.DB(), deleteAIRoute, map[string]any{"purpose": purpose}); err != nil {
			return fmt.Errorf("failed to clear ai route %q: %w", purpose, err)
		}
		for _, c := range route {
			if _, err := execNamed(ctx, rep.DB(), insertAIRouteCandidate, map[string]any{
				"purpose":      purpose,
				"position":     c.Position,
				"provider_key": c.ProviderKey,
				"model":        c.Model,
				"by":           by,
			}); err != nil {
				return fmt.Errorf("failed to write ai route %q position %d: %w", purpose, c.Position, err)
			}
		}
		return nil
	})
}

const upsertAIModel = `
	INSERT INTO ai_model (provider_key, model, label, kind, disabled, updated_by)
	VALUES (:provider_key, :model, :label, :kind, :disabled, :by)
	ON DUPLICATE KEY UPDATE
		label = VALUES(label),
		kind = VALUES(kind),
		disabled = VALUES(disabled),
		updated_by = VALUES(updated_by)`

// UpsertModel records a custom slug typed into a route and bumps the version WITHOUT a check (the
// model list is part of the snapshot, so it must move the version; it has no page of its own to be
// stale against).
func (s *Store) UpsertModel(ctx context.Context, m entity.AIModel, by string) error {
	if err := validateProviderKey("provider_key", m.ProviderKey); err != nil {
		return err
	}
	m.Model = strings.TrimSpace(m.Model)
	if m.Model == "" {
		return entity.NewFieldViolation("model", "model_required", "", "name the slug")
	}
	if utf8.RuneCountInString(m.Model) > widthModel {
		return entity.NewFieldViolation("model", "model_too_long", "", fmt.Sprintf("at most %d characters", widthModel))
	}
	if utf8.RuneCountInString(m.Label) > widthLabel {
		return entity.NewFieldViolation("label", "label_too_long", "", fmt.Sprintf("at most %d characters", widthLabel))
	}
	if !entity.IsAICapability(m.Kind) {
		return entity.NewFieldViolation("kind", "unknown_ai_capability", "",
			"name one of: "+strings.Join(entity.AICapabilities(), ", "))
	}
	if !entity.AIProviderServes(m.ProviderKey, m.Kind) {
		return entity.NewFieldViolation("kind", "provider_cannot_serve", m.ProviderKey,
			"choose a kind the provider serves")
	}
	params := map[string]any{
		"provider_key": m.ProviderKey,
		"model":        m.Model,
		"label":        m.Label,
		"kind":         m.Kind,
		"disabled":     m.Disabled,
		"by":           by,
	}
	return s.txFunc(ctx, func(ctx context.Context, rep dependency.Repository) error {
		if err := bumpVersion(ctx, rep.DB(), nil, by); err != nil {
			return err
		}
		if _, err := execNamed(ctx, rep.DB(), upsertAIModel, params); err != nil {
			return fmt.Errorf("failed to upsert ai model %s/%s: %w", m.ProviderKey, m.Model, err)
		}
		return nil
	})
}

// ───────────────────────── ledger ─────────────────────────

const insertAICall = `
	INSERT INTO ai_usage_event
		(occurred_at, day_local, provider_key, model, purpose, actor, actor_admin_id,
		 run_id, attempt_no, call_no, fallback_from, status, cost_source)
	VALUES
		(:occurred_at, :day_local, :provider_key, :model, :purpose, :actor, :actor_admin_id,
		 :run_id, :attempt_no, :call_no, :fallback_from, 'dispatching', 'none')`

// aiCallEndSet is the ONE finalisation of a ledger row, shared by FinishCall and PriceAcceptedCall.
// Every nullable column is COALESCEd with what the row already holds: on a dispatching row that is
// NULL anyway, and on an accepted row it keeps what the submit wrote (request id, http status,
// engaged) when the collect does not repeat it.
const aiCallEndSet = `
		status            = :status,
		error_code        = COALESCE(:error_code, error_code),
		http_status       = COALESCE(:http_status, http_status),
		engaged           = COALESCE(:engaged, engaged),
		request_id        = COALESCE(:request_id, request_id),
		model             = COALESCE(:model_actual, model),
		prompt_tokens     = COALESCE(:prompt_tokens, prompt_tokens),
		completion_tokens = COALESCE(:completion_tokens, completion_tokens),
		cached_tokens     = COALESCE(:cached_tokens, cached_tokens),
		reasoning_tokens  = COALESCE(:reasoning_tokens, reasoning_tokens),
		units             = COALESCE(:units, units),
		unit              = COALESCE(:unit, unit),
		cost_usd          = COALESCE(:cost_usd, cost_usd),
		cost_source       = COALESCE(:cost_source, cost_source),
		price_version     = COALESCE(:price_version, price_version),
		latency_ms        = COALESCE(:latency_ms, latency_ms),
		finished_at       = :finished_at`

const finishAICall = `
	UPDATE ai_usage_event SET` + aiCallEndSet + `
	WHERE id = :id AND status = 'dispatching'`

const priceAcceptedAICall = `
	UPDATE ai_usage_event SET` + aiCallEndSet + `
	WHERE run_id = :run_id AND attempt_no = :attempt_no AND call_no = :call_no AND status = 'accepted'`

const sweepAIDispatching = `
	UPDATE ai_usage_event
	SET status = 'unknown', error_code = 'sweeper', finished_at = :finished_at
	WHERE status = 'dispatching' AND occurred_at < :older_than`

// beginCallParams binds a new ledger row. It refuses only what the columns cannot hold at all — a
// missing time or day, a key-less row, a counter out of range — and clips every string to its column,
// because a refused row is a lost row and the caller proceeds with the paid call regardless.
func beginCallParams(st entity.AICallStart) (map[string]any, error) {
	if st.OccurredAt.IsZero() {
		return nil, errors.New("ai ledger: occurred_at is required")
	}
	if _, err := time.Parse(dayLayout, st.DayLocal); err != nil {
		return nil, fmt.Errorf("ai ledger: day_local %q is not YYYY-MM-DD", st.DayLocal)
	}
	if strings.TrimSpace(st.ProviderKey) == "" || strings.TrimSpace(st.Purpose) == "" {
		return nil, errors.New("ai ledger: provider_key and purpose are required")
	}
	callNo := st.CallNo
	if callNo == 0 {
		callNo = 1
	}
	if callNo < 1 || callNo > maxTinyUnsigned {
		return nil, fmt.Errorf("ai ledger: call_no %d out of range 1..%d", st.CallNo, maxTinyUnsigned)
	}
	if st.AttemptNo != nil && (*st.AttemptNo < 1 || *st.AttemptNo > maxTinyUnsigned) {
		return nil, fmt.Errorf("ai ledger: attempt_no %d out of range 1..%d", *st.AttemptNo, maxTinyUnsigned)
	}
	if st.RunID != nil && *st.RunID < 1 {
		return nil, fmt.Errorf("ai ledger: run_id %d must be positive", *st.RunID)
	}
	if st.ActorAdminID != nil && *st.ActorAdminID < 1 {
		return nil, fmt.Errorf("ai ledger: actor_admin_id %d must be positive", *st.ActorAdminID)
	}
	return map[string]any{
		"occurred_at":    st.OccurredAt.UTC(),
		"day_local":      st.DayLocal,
		"provider_key":   clip(st.ProviderKey, widthProviderKey),
		"model":          clip(st.Model, widthModel),
		"purpose":        clip(st.Purpose, widthPurpose),
		"actor":          clip(st.Actor, widthActor),
		"actor_admin_id": intParam(st.ActorAdminID),
		"run_id":         intParam(st.RunID),
		"attempt_no":     intParam(st.AttemptNo),
		"call_no":        callNo,
		"fallback_from":  nullString(clip(st.FallbackFrom, widthProviderKey)),
	}, nil
}

// BeginCall inserts a ledger row with status 'dispatching' BEFORE the physical call and returns its id.
func (s *Store) BeginCall(ctx context.Context, start entity.AICallStart) (int64, error) {
	params, err := beginCallParams(start)
	if err != nil {
		return 0, err
	}
	res, err := execNamed(ctx, s.DB, insertAICall, params)
	if err != nil {
		return 0, fmt.Errorf("failed to begin ai call: %w", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return 0, fmt.Errorf("failed to read ai call id: %w", err)
	}
	return id, nil
}

// endCallParams binds a finalisation. Empty strings and nil pointers bind NULL, which the COALESCEs
// of aiCallEndSet read as "keep".
func endCallParams(end entity.AICallEnd, now time.Time) (map[string]any, error) {
	if !entity.IsAICallStatus(end.Status) || end.Status == entity.AICallDispatching {
		return nil, fmt.Errorf("ai ledger: %q is not a status a call can end in", end.Status)
	}
	if end.CostSource != "" && !entity.IsAICostSource(end.CostSource) {
		return nil, fmt.Errorf("ai ledger: unknown cost source %q", end.CostSource)
	}
	var units any
	if end.Units != nil {
		units = *end.Units
	}
	var cost any
	if end.CostUSD.Valid {
		cost = end.CostUSD.Decimal
	}
	var engaged any
	if end.Engaged != nil {
		engaged = *end.Engaged
	}
	return map[string]any{
		"status":            end.Status,
		"error_code":        nullString(clip(end.ErrorCode, widthErrorCode)),
		"http_status":       intParam(end.HTTPStatus),
		"engaged":           engaged,
		"request_id":        nullString(clip(end.RequestID, widthRequestID)),
		"model_actual":      nullString(clip(end.ModelActual, widthModel)),
		"prompt_tokens":     intParam(end.PromptTokens),
		"completion_tokens": intParam(end.CompletionTokens),
		"cached_tokens":     intParam(end.CachedTokens),
		"reasoning_tokens":  intParam(end.ReasoningTokens),
		"units":             units,
		"unit":              nullString(clip(end.Unit, widthUnit)),
		"cost_usd":          cost,
		"cost_source":       nullString(end.CostSource),
		"price_version":     nullString(clip(end.PriceVersion, widthPriceVersion)),
		"latency_ms":        intParam(end.LatencyMs),
		"finished_at":       now.UTC(),
	}, nil
}

// FinishCall finalises a 'dispatching' row. A row that already left dispatching (finished twice, or
// swept) is left alone and nil is returned: finishing is idempotent.
func (s *Store) FinishCall(ctx context.Context, id int64, end entity.AICallEnd) error {
	if id < 1 {
		return fmt.Errorf("ai ledger: call id %d must be positive", id)
	}
	params, err := endCallParams(end, s.Now())
	if err != nil {
		return err
	}
	params["id"] = id
	if _, err := execNamed(ctx, s.DB, finishAICall, params); err != nil {
		return fmt.Errorf("failed to finish ai call %d: %w", id, err)
	}
	return nil
}

// PriceAcceptedCall finalises the 'accepted' row of (run, attempt, call) — the async submit's row —
// when the collect delivers. A row that is not 'accepted' any more (a de-duplicated collect) is left
// alone and nil is returned.
func (s *Store) PriceAcceptedCall(ctx context.Context, runID, attemptNo, callNo int, end entity.AICallEnd) error {
	if callNo == 0 {
		callNo = 1
	}
	if runID < 1 || attemptNo < 1 || callNo < 1 {
		return fmt.Errorf("ai ledger: run %d attempt %d call %d is not a design call", runID, attemptNo, callNo)
	}
	if end.Status == entity.AICallAccepted {
		return errors.New("ai ledger: pricing an accepted call must move it past accepted")
	}
	params, err := endCallParams(end, s.Now())
	if err != nil {
		return err
	}
	params["run_id"] = runID
	params["attempt_no"] = attemptNo
	params["call_no"] = callNo
	if _, err := execNamed(ctx, s.DB, priceAcceptedAICall, params); err != nil {
		return fmt.Errorf("failed to price ai call run %d attempt %d call %d: %w", runID, attemptNo, callNo, err)
	}
	return nil
}

// SweepDispatching turns 'dispatching' rows that occurred before olderThan into 'unknown' with
// error_code 'sweeper': the process that opened them died before it could finish them, and money may
// have moved.
func (s *Store) SweepDispatching(ctx context.Context, olderThan time.Time) (int64, error) {
	n, err := execRows(ctx, s.DB, sweepAIDispatching, map[string]any{
		"older_than":  olderThan.UTC(),
		"finished_at": s.Now().UTC(),
	})
	if err != nil {
		return 0, fmt.Errorf("failed to sweep ai calls: %w", err)
	}
	return n, nil
}

// ───────────────────────── report ─────────────────────────

// spendByProvider — our ledger per provider beside the provider's own daily numbers.
//
// THEIR NUMBER IS SUMMED PER PROVIDER BEFORE THE JOIN. Joining the raw daily rows onto the ledger rows
// would multiply every ledger row by the number of days the provider reported, and multiply their sum
// by our call count.
//
// SUM(cost_usd) IS NEVER COALESCEd: a provider none of whose rows carries a price reports NULL, which
// the page shows as unknown, not $0. Unpriced counts the rows that owe a number and have none.
const spendByProvider = `
	SELECT u.provider_key, u.our_usd, c.their_usd, u.calls, u.failed, u.unpriced
	FROM (
		SELECT provider_key,
		       SUM(cost_usd) AS our_usd,
		       COUNT(*) AS calls,
		       SUM(CASE WHEN status IN ('free','failed','charged_failed','unknown') THEN 1 ELSE 0 END) AS failed,
		       SUM(CASE WHEN cost_usd IS NULL AND status IN ('ok','charged_failed','unknown') THEN 1 ELSE 0 END) AS unpriced
		FROM ai_usage_event
		WHERE day_local BETWEEN :from_day AND :to_day
		GROUP BY provider_key
	) u
	LEFT JOIN (
		SELECT provider_key, SUM(amount_usd) AS their_usd
		FROM ai_provider_cost_daily
		WHERE day BETWEEN :from_day AND :to_day
		GROUP BY provider_key
	) c ON c.provider_key = u.provider_key
	ORDER BY u.provider_key`

// spendByActor — who spent it, on what, where. actor_admin_id is MAX()ed, not grouped: the same
// username may carry a NULL id on some rows (the lookup failed) and the id on others, and splitting
// one person into two lines for that would be noise.
const spendByActor = `
	SELECT actor, MAX(actor_admin_id) AS actor_admin_id, purpose, provider_key, model,
	       SUM(cost_usd) AS usd, COUNT(*) AS calls
	FROM ai_usage_event
	WHERE day_local BETWEEN :from_day AND :to_day
	GROUP BY actor, purpose, provider_key, model
	ORDER BY actor, purpose, provider_key, model`

// SpendReport sums the ledger over the inclusive day_local range.
//
// Plain reads, deliberately outside any transaction: the store's transactions are SERIALIZABLE, whose
// range locks on ai_usage_event would hold up every BeginCall landing in the reported period.
func (s *Store) SpendReport(ctx context.Context, fromDay, toDay string) (*entity.AISpendReport, error) {
	from, err := time.Parse(dayLayout, fromDay)
	if err != nil {
		return nil, entity.NewFieldViolation("from", "bad_day", "", "a calendar day, YYYY-MM-DD")
	}
	to, err := time.Parse(dayLayout, toDay)
	if err != nil {
		return nil, entity.NewFieldViolation("to", "bad_day", "", "a calendar day, YYYY-MM-DD")
	}
	if to.Before(from) {
		return nil, entity.NewFieldViolation("to", "range_reversed", "", "the last day must not precede the first")
	}
	params := map[string]any{"from_day": fromDay, "to_day": toDay}

	var byProvider []entity.AISpendByProvider
	if err := selectNamed(ctx, s.DB, &byProvider, spendByProvider, params); err != nil {
		return nil, fmt.Errorf("failed to report ai spend by provider: %w", err)
	}
	var byActor []entity.AISpendByActor
	if err := selectNamed(ctx, s.DB, &byActor, spendByActor, params); err != nil {
		return nil, fmt.Errorf("failed to report ai spend by actor: %w", err)
	}

	rep := spendTotals(byProvider)
	rep.FromDay, rep.ToDay = fromDay, toDay
	rep.ByProvider, rep.ByActor = byProvider, byActor
	return &rep, nil
}

// spendTotals folds the per-provider lines into the report's totals. The total USD stays NULL when no
// provider line carries one — the same rule SQL's SUM keeps inside each line.
func spendTotals(lines []entity.AISpendByProvider) entity.AISpendReport {
	var rep entity.AISpendReport
	for _, l := range lines {
		rep.Calls += l.Calls
		rep.Failed += l.Failed
		rep.Unpriced += l.Unpriced
		if !l.OurUSD.Valid {
			continue
		}
		if rep.TotalUSD.Valid {
			rep.TotalUSD.Decimal = rep.TotalUSD.Decimal.Add(l.OurUSD.Decimal)
		} else {
			rep.TotalUSD = decimal.NewNullDecimal(l.OurUSD.Decimal)
		}
	}
	return rep
}

const upsertAICostDaily = `
	INSERT INTO ai_provider_cost_daily (provider_key, day, amount_usd, currency, fetched_at)
	VALUES (:provider_key, :day, :amount_usd, :currency, :fetched_at)
	ON DUPLICATE KEY UPDATE
		amount_usd = VALUES(amount_usd),
		currency = VALUES(currency),
		fetched_at = VALUES(fetched_at)`

func costDailyParams(r entity.AICostDaily, now time.Time) (map[string]any, error) {
	if !entity.IsAIProviderKey(r.ProviderKey) {
		return nil, fmt.Errorf("ai cost daily: unknown provider %q", r.ProviderKey)
	}
	if _, err := time.Parse(dayLayout, r.Day); err != nil {
		return nil, fmt.Errorf("ai cost daily: day %q is not YYYY-MM-DD", r.Day)
	}
	currency := strings.ToUpper(strings.TrimSpace(r.Currency))
	if currency == "" {
		currency = "USD"
	}
	if len(currency) != 3 {
		return nil, fmt.Errorf("ai cost daily: currency %q is not a 3-letter code", r.Currency)
	}
	fetched := r.FetchedAt
	if fetched.IsZero() {
		fetched = now
	}
	return map[string]any{
		"provider_key": r.ProviderKey,
		"day":          r.Day,
		"amount_usd":   r.AmountUSD,
		"currency":     currency,
		"fetched_at":   fetched.UTC(),
	}, nil
}

// UpsertCostDaily writes the providers' own daily numbers; a day fetched again replaces its row.
func (s *Store) UpsertCostDaily(ctx context.Context, rows []entity.AICostDaily) error {
	if len(rows) == 0 {
		return nil
	}
	now := s.Now()
	params := make([]map[string]any, 0, len(rows))
	for _, r := range rows {
		p, err := costDailyParams(r, now)
		if err != nil {
			return err
		}
		params = append(params, p)
	}
	return s.txFunc(ctx, func(ctx context.Context, rep dependency.Repository) error {
		for _, p := range params {
			if _, err := execNamed(ctx, rep.DB(), upsertAICostDaily, p); err != nil {
				return fmt.Errorf("failed to upsert ai cost of %v on %v: %w", p["provider_key"], p["day"], err)
			}
		}
		return nil
	})
}

// ───────────────────────── helpers ─────────────────────────

func execNamed(ctx context.Context, db dependency.DB, query string, params map[string]any) (sql.Result, error) {
	q, args, err := storeutil.MakeQuery(query, params)
	if err != nil {
		return nil, err
	}
	return db.ExecContext(ctx, q, args...)
}

func execRows(ctx context.Context, db dependency.DB, query string, params map[string]any) (int64, error) {
	res, err := execNamed(ctx, db, query, params)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

func getNamed(ctx context.Context, db dependency.DB, dest any, query string, params map[string]any) error {
	q, args, err := storeutil.MakeQuery(query, params)
	if err != nil {
		return err
	}
	return db.GetContext(ctx, dest, q, args...)
}

func selectNamed(ctx context.Context, db dependency.DB, dest any, query string, params map[string]any) error {
	q, args, err := storeutil.MakeQuery(query, params)
	if err != nil {
		return err
	}
	return db.SelectContext(ctx, dest, q, args...)
}

// clip cuts s to at most n characters (VARCHAR counts characters, not bytes).
func clip(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	return string([]rune(s)[:n])
}

// nullString binds "" as NULL.
func nullString(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// intParam binds a nil pointer as NULL.
func intParam(v *int) any {
	if v == nil {
		return nil
	}
	return *v
}
