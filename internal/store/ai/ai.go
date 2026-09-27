// Package ai implements the AI providers store: the configuration of 0373 (ai_provider, ai_model,
// ai_route, ai_settings) and the ledger of 0374 (ai_usage_event, ai_provider_cost_daily).
//
// Two halves with opposite rules. The CONFIG half is small, rare and written by one super admin at a
// time: every write runs in one transaction that first moves ai_settings.config_version, so all
// config writers serialise on that one row and the registry learns about the write by polling one
// number; its one read (GetConfig) runs in one read-only snapshot, so it never returns a mix of two
// versions. The LEDGER half is written on every provider call: single autocommit statements, never
// the SERIALIZABLE write runner (whose range locks would make a report block the calls it is reporting
// on), and no validation beyond what the columns cannot hold at all: a refused row is money
// unrecorded. Its one report (SpendReport) reads in the same read-only snapshot GetConfig uses, which
// takes no locks at all.
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
// GetConfig reads its five tables and SpendReport its four reads inside it (store.readTx — REPEATABLE
// READ, read-only), one snapshot each, where the SERIALIZABLE txFunc would take a shared lock on every
// row it reads — on the ledger, range locks that would hold up every BeginCall of the period.
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

// insertAIModelIfAbsent records a slug typed into a route. An existing row — its label, kind and
// disabled flag somebody chose — is left exactly as it is: the no-op ON DUPLICATE KEY UPDATE changes
// nothing (and, unlike INSERT IGNORE, downgrades no other error to a warning).
const insertAIModelIfAbsent = `
	INSERT INTO ai_model (provider_key, model, label, kind, disabled, updated_by)
	VALUES (:provider_key, :model, '', :kind, 0, :by)
	ON DUPLICATE KEY UPDATE provider_key = provider_key`

// SetRoute replaces the purpose's whole route in one transaction, compare-and-swap, and records in
// ai_model every slug the route names — IN THE SAME TRANSACTION, under the same one version bump
// (Codex B #6). Recorded afterwards, in a transaction of its own, a slug whose provider is "" was
// resolved against a default read in yet another snapshot: a SetAiDefaults committed in between left
// the route following the new default and the slug listed under the old one, and the unchecked bump of
// that second write landed after the other admin's checked one. Here the provider "" means is read
// from the ai_settings row this transaction has already locked (bumpVersion goes first), so it is the
// default the route will follow.
//
// Every named slug is recorded, catalogue or not: the store does not know the pricing catalogue (it
// must not import it). The panel's view decides what is custom — a row whose slug the catalogue names
// is the catalogue's entry, listed once (admin aiModels).
func (s *Store) SetRoute(ctx context.Context, purpose string, candidates []entity.AIRouteCandidate, expectedVersion uint64, by string) error {
	route, err := normaliseRoute(purpose, candidates)
	if err != nil {
		return err
	}
	capability := entity.AIPurposeCapability(purpose)
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
		return recordRouteModels(ctx, rep.DB(), capability, route, by)
	})
}

// recordRouteModels is SetRoute's ai_model half, on the route transaction's handle: one
// insertAIModelIfAbsent per distinct (provider, model) the route names. A candidate with no model
// names none; a "" provider is the capability's default as the settings row of THIS transaction holds
// it (read only when some candidate needs it); a capability with no default provider, or a provider
// that cannot serve the capability, records nothing — the registry skips such a candidate too.
func recordRouteModels(ctx context.Context, db dependency.DB, capability string, route []entity.AIRouteCandidate, by string) error {
	var settings *entity.AISettings
	seen := map[string]bool{}
	for _, c := range route {
		if c.Model == "" {
			continue
		}
		provider := c.ProviderKey
		if provider == "" {
			if capability != entity.AICapabilityChat && capability != entity.AICapabilityImage {
				continue
			}
			if settings == nil {
				st, err := loadSettings(ctx, db)
				if err != nil {
					return err
				}
				settings = &st
			}
			provider = settings.DefaultProviderFor(capability)
		}
		if !entity.IsAIProviderKey(provider) || !entity.AIProviderServes(provider, capability) {
			continue
		}
		if seen[provider+"\x00"+c.Model] {
			continue
		}
		seen[provider+"\x00"+c.Model] = true
		if _, err := execNamed(ctx, db, insertAIModelIfAbsent, map[string]any{
			"provider_key": provider,
			"model":        c.Model,
			"kind":         capability,
			"by":           by,
		}); err != nil {
			return fmt.Errorf("failed to record ai model %s/%s: %w", provider, c.Model, err)
		}
	}
	return nil
}

const upsertAIModel = `
	INSERT INTO ai_model (provider_key, model, label, kind, disabled, updated_by)
	VALUES (:provider_key, :model, :label, :kind, :disabled, :by)
	ON DUPLICATE KEY UPDATE
		label = VALUES(label),
		kind = VALUES(kind),
		disabled = VALUES(disabled),
		updated_by = VALUES(updated_by)`

// UpsertModel writes one ai_model row (label, kind and disabled included) and bumps the version
// WITHOUT a check (the model list is part of the snapshot, so it must move the version; it has no page
// of its own to be stale against). A slug typed into a route is NOT recorded through here: SetRoute
// records it inside the route's own checked transaction.
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

// ───────────────────────── provider faults (the panel's badge) ─────────────────────────

// The badge words — AiProviderInfo.fault_code. A badge says CONFIGURATION, never weather: the key is
// refused, the account is empty, the model is not served. Each repeats identically however often it
// is retried, which is why a person has to see it even when a fallback served the request (D-09).
const (
	faultKeyRejected  = "key_rejected"
	faultOutOfCredits = "out_of_credits"
	faultModelUnknown = "model_unknown"
)

// faultBadges maps the error_code words the ledger holds onto the badge. Two writers fill
// error_code: designgen's classify (designgen/classify.go Code*: provider_unauthorized = 401/403,
// provider_out_of_credit = 402, provider_model_retired = a 404 on the model) and aiprov.CallError.Code
// (key_rejected, out_of_credits, model_unknown). Every other word — a timeout, a 5xx, a rate limit, a
// request we built wrong, the sweeper's — has no badge.
var faultBadges = map[string]string{
	"provider_unauthorized":  faultKeyRejected,
	faultKeyRejected:         faultKeyRejected,
	"provider_out_of_credit": faultOutOfCredits,
	faultOutOfCredits:        faultOutOfCredits,
	"provider_model_retired": faultModelUnknown,
	faultModelUnknown:        faultModelUnknown,
}

// recentFaults counts the failed calls of the window per (provider, error_code). Only `failed` and
// `free` rows: a configuration refusal is never billed, so it never ends `charged_failed`, and an
// `unknown` row is a call whose outcome nobody knows. idx_ai_usage_status (status, occurred_at) serves
// the WHERE. The words are folded into badges in Go (faultBadges), so the vocabulary lives once.
//
// A KEY WRITE ENDS THE KEY'S FAULTS (Codex B #9). A key-class fault — the key refused, its account
// empty: the words of faultBadges that map to key_rejected / out_of_credits — that occurred before the
// provider's last api-key write (ai_provider.api_key_updated_at: a new key saved, or the stored one
// cleared so the env key answers) is about a key no longer in force, and is not counted; the save's
// own probe already says what the new key does. The LEFT JOIN keeps the plain window for a provider
// with no row or no key write, and for every other fault: a model fault is not the key's and keeps
// the plain window. The admin key serves no call and does not move this bound.
const recentFaults = `
	SELECT e.provider_key, e.error_code, COUNT(*) AS n, MAX(e.occurred_at) AS last_at
	FROM ai_usage_event AS e
	LEFT JOIN ai_provider AS p ON p.provider_key = e.provider_key
	WHERE e.status IN ('failed', 'free') AND e.occurred_at >= :since AND e.error_code IS NOT NULL
	  AND NOT (e.error_code IN ('key_rejected', 'out_of_credits', 'provider_unauthorized', 'provider_out_of_credit')
	           AND p.api_key_updated_at IS NOT NULL AND e.occurred_at < p.api_key_updated_at)
	GROUP BY e.provider_key, e.error_code`

// faultRow is one (provider, error_code) count of the window.
type faultRow struct {
	ProviderKey string    `db:"provider_key"`
	ErrorCode   string    `db:"error_code"`
	N           int       `db:"n"`
	LastAt      time.Time `db:"last_at"`
}

// RecentFaults returns, per provider, the badge of the most frequent configuration fault among its
// failed calls since `since` (key_rejected | out_of_credits | model_unknown); key faults count only
// from the provider's last api-key write on. A provider with no such fault is absent from the map.
func (s *Store) RecentFaults(ctx context.Context, since time.Time) (map[string]string, error) {
	var rows []faultRow
	if err := selectNamed(ctx, s.DB, &rows, recentFaults, map[string]any{"since": since.UTC()}); err != nil {
		return nil, fmt.Errorf("failed to read recent ai faults: %w", err)
	}
	return pickFaults(rows), nil
}

// pickFaults folds the counts into one badge per provider: the badge with the most calls (two words
// of one badge add up); a tie goes to the badge seen last, then to the alphabetically first, so the
// answer never depends on the order the rows arrived in.
func pickFaults(rows []faultRow) map[string]string {
	type tally struct {
		n    int
		last time.Time
	}
	per := map[string]map[string]*tally{}
	for _, r := range rows {
		badge, ok := faultBadges[r.ErrorCode]
		if !ok || r.N <= 0 {
			continue
		}
		if per[r.ProviderKey] == nil {
			per[r.ProviderKey] = map[string]*tally{}
		}
		t := per[r.ProviderKey][badge]
		if t == nil {
			t = &tally{}
			per[r.ProviderKey][badge] = t
		}
		t.n += r.N
		if r.LastAt.After(t.last) {
			t.last = r.LastAt
		}
	}
	out := make(map[string]string, len(per))
	for provider, badges := range per {
		var best string
		var bt *tally
		for badge, t := range badges {
			switch {
			case bt == nil, t.n > bt.n,
				t.n == bt.n && t.last.After(bt.last),
				t.n == bt.n && t.last.Equal(bt.last) && badge < best:
				best, bt = badge, t
			}
		}
		out[provider] = best
	}
	return out
}

// ───────────────────────── ledger ─────────────────────────

// insertAICall opens a ledger row. actor_admin_id is RESOLVED HERE, at write time (Codex B #2, D-10):
// the caller's id when it has one, else the admins row that carries the actor's username NOW. A row
// is thereby tied to the account that existed when the call was made — a deleted account's rows keep
// its id, and a new account recreated under the same username gets a new id and none of the old
// history. designgen's recorder names only a username, so this is where its rows get their id. No
// such admin (system, unknown, a deleted account) leaves the id NULL: the report shows such rows as
// their own line.
const insertAICall = `
	INSERT INTO ai_usage_event
		(occurred_at, day_local, provider_key, model, purpose, actor, actor_admin_id,
		 run_id, attempt_no, call_no, fallback_from, status, cost_source)
	VALUES
		(:occurred_at, :day_local, :provider_key, :model, :purpose, :actor,
		 COALESCE(:actor_admin_id, (SELECT id FROM admins WHERE username = :actor LIMIT 1)),
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

// spendByProvider — our ledger per provider.
//
// SUM(cost_usd) IS NEVER COALESCEd: a provider none of whose rows carries a price reports NULL, which
// the page shows as unknown, not $0. A `free` row carries a real 0 (aiprov.Ledger writes it), so a
// provider whose only calls were free sums to a real 0 and says so.
//
// UNPRICED = EVERY COUNTED ROW WITHOUT A KNOWN COST THAT IS NOT `free` (Codex B #3) — the wire's
// "calls with no known cost", over the same rows `calls` counts. That includes `accepted` and
// `dispatching` (an async job submitted and not yet collected, a call in flight: an unknown liability,
// not a zero) and `failed` (the request was written and no charge was reported — which is not the
// same as known to be free). A status list here would drop every status it forgot, silently.
const spendByProvider = `
	SELECT provider_key,
	       SUM(cost_usd) AS our_usd,
	       COUNT(*) AS calls,
	       SUM(CASE WHEN status IN ('free','failed','charged_failed','unknown') THEN 1 ELSE 0 END) AS failed,
	       SUM(CASE WHEN cost_usd IS NULL AND status <> 'free' THEN 1 ELSE 0 END) AS unpriced
	FROM ai_usage_event
	WHERE day_local BETWEEN :from_day AND :to_day
	GROUP BY provider_key`

// spendTheirByProvider — the providers' own daily numbers (the reconcile worker's rows), summed per
// provider. It is its own statement, not a JOIN onto the ledger side: SpendReport unions the two, so a
// provider that billed us in the period while our ledger recorded no call still gets its line.
const spendTheirByProvider = `
	SELECT provider_key, SUM(amount_usd) AS their_usd
	FROM ai_provider_cost_daily
	WHERE day BETWEEN :from_day AND :to_day
	GROUP BY provider_key`

// spendByActor — who spent it, on what, where. THE ACCOUNT IS THE ID (D-10, Codex B #2): rows group
// by actor_admin_id AND actor, so an account deleted and recreated under the same username is two
// lines, never one line charging the new account with the old one's history (a MAX(actor_admin_id)
// over the username did exactly that). A row whose id is NULL — no admin carried that username when
// it was written: system, unknown, an account already gone — is a line of its own (wire id 0).
const spendByActor = `
	SELECT actor, actor_admin_id, purpose, provider_key, model,
	       SUM(cost_usd) AS usd, COUNT(*) AS calls
	FROM ai_usage_event
	WHERE day_local BETWEEN :from_day AND :to_day
	GROUP BY actor_admin_id, actor, purpose, provider_key, model
	ORDER BY actor, actor_admin_id, purpose, provider_key, model`

// ourSpendRow is one provider's line of our ledger (spendByProvider).
type ourSpendRow struct {
	ProviderKey string              `db:"provider_key"`
	OurUSD      decimal.NullDecimal `db:"our_usd"`
	Calls       int                 `db:"calls"`
	Failed      int                 `db:"failed"`
	Unpriced    int                 `db:"unpriced"`
}

// theirSpendRow is one provider's own number over the period (spendTheirByProvider).
type theirSpendRow struct {
	ProviderKey string              `db:"provider_key"`
	TheirUSD    decimal.NullDecimal `db:"their_usd"`
}

// SpendReport sums the ledger over the inclusive day_local range, beside the providers' own numbers,
// and names the timezone those days were counted in.
//
// ONE SNAPSHOT (Codex B #5). The four reads run inside readTxFunc (store.readTx: REPEATABLE READ,
// read-only — GetConfig's runner), where InnoDB answers every read from the one snapshot the first
// read takes. As four autocommit reads, a call finishing between the provider totals and the actor
// rows made `calls` and `total_usd` disagree with the sum of by_actor in one response, and a
// reconciliation write could land on one side only. The snapshot's reads are consistent NON-LOCKING
// reads, so the report still holds up no BeginCall; the SERIALIZABLE txFunc, whose shared range locks
// on ai_usage_event would, is never used here. Inside a transaction (NewInTx) the reads run in the
// enclosing one.
func (s *Store) SpendReport(ctx context.Context, fromDay, toDay string) (*entity.AISpendReport, error) {
	from, err := time.Parse(dayLayout, fromDay)
	if err != nil {
		return nil, entity.NewFieldViolation("from_day", "bad_day", "", "a calendar day, YYYY-MM-DD")
	}
	to, err := time.Parse(dayLayout, toDay)
	if err != nil {
		return nil, entity.NewFieldViolation("to_day", "bad_day", "", "a calendar day, YYYY-MM-DD")
	}
	if to.Before(from) {
		return nil, entity.NewFieldViolation("to_day", "range_reversed", "", "the last day must not precede the first")
	}
	params := map[string]any{"from_day": fromDay, "to_day": toDay}

	var (
		ours    []ourSpendRow
		theirs  []theirSpendRow
		byActor []entity.AISpendByActor
		tz      string
	)
	err = s.readTxFunc(ctx, func(ctx context.Context, rep dependency.Repository) error {
		db := rep.DB()
		if err := selectNamed(ctx, db, &ours, spendByProvider, params); err != nil {
			return fmt.Errorf("failed to report ai spend by provider: %w", err)
		}
		if err := selectNamed(ctx, db, &theirs, spendTheirByProvider, params); err != nil {
			return fmt.Errorf("failed to report ai spend by the providers' own numbers: %w", err)
		}
		if err := selectNamed(ctx, db, &byActor, spendByActor, params); err != nil {
			return fmt.Errorf("failed to report ai spend by actor: %w", err)
		}
		var err error
		tz, err = loadBudgetTimezone(ctx, db)
		return err
	})
	if err != nil {
		return nil, err
	}
	if tz = strings.TrimSpace(tz); tz == "" {
		// The zone aiprov.Ledger stamps day_local in when the setting is blank.
		tz = entity.DefaultBudgetTimezone
	}

	byProvider := unionSpendLines(ours, theirs)
	rep := spendTotals(byProvider)
	rep.FromDay, rep.ToDay, rep.Timezone = fromDay, toDay, tz
	rep.ByProvider, rep.ByActor = byProvider, byActor
	return &rep, nil
}

// unionSpendLines is THE UNION of the two sides: a provider has a line when our ledger OR its own cost
// API has anything in the period. Ours alone would drop a provider that billed us on days we recorded
// no call — exactly the gap «their number» exists to show. A line with only their number keeps our
// USD invalid (unknown) and zero calls. Lines come in entity.AIProviderKeys() order, a key a newer
// build wrote after them, alphabetically.
func unionSpendLines(ours []ourSpendRow, theirs []theirSpendRow) []entity.AISpendByProvider {
	index := make(map[string]int, len(ours)+len(theirs))
	lines := make([]entity.AISpendByProvider, 0, len(ours)+len(theirs))
	at := func(key string) int {
		i, ok := index[key]
		if !ok {
			i = len(lines)
			index[key] = i
			lines = append(lines, entity.AISpendByProvider{ProviderKey: key})
		}
		return i
	}
	for _, o := range ours {
		i := at(o.ProviderKey)
		lines[i].OurUSD, lines[i].Calls, lines[i].Failed, lines[i].Unpriced = o.OurUSD, o.Calls, o.Failed, o.Unpriced
	}
	for _, t := range theirs {
		i := at(t.ProviderKey) // before indexing: at may append, and lines[at(k)] reads lines in an unspecified order
		lines[i].TheirUSD = t.TheirUSD
	}
	byProvider := vocabCompare(entity.AIProviderKeys())
	slices.SortStableFunc(lines, func(a, b entity.AISpendByProvider) int { return byProvider(a.ProviderKey, b.ProviderKey) })
	return lines
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
