// Package techcard implements garment tech pack (техкарта) management: the header,
// size range, linked products, sketch media, callouts and revision log.
package techcard

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/go-sql-driver/mysql"
	"github.com/jekabolt/grbpwr-manager/internal/cache"
	"github.com/jekabolt/grbpwr-manager/internal/dependency"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/jekabolt/grbpwr-manager/internal/store/product"
	"github.com/jekabolt/grbpwr-manager/internal/store/storeutil"
	"github.com/shopspring/decimal"
)

// Pagination bounds for list endpoints.
const (
	defaultPageLimit = 50
	maxPageLimit     = 100
)

// TxFunc is a function that executes f within a transaction.
type TxFunc func(ctx context.Context, f func(context.Context, dependency.Repository) error) error

// RepFunc returns the current repository so dictionary-dependent writes can refresh the shared
// in-memory dictionary before opening their transaction.
type RepFunc func() dependency.Repository

// Store implements dependency.TechCards.
type Store struct {
	storeutil.Base
	txFunc     TxFunc
	readTxFunc TxFunc
	repFunc    RepFunc
}

// New creates a new tech card store.
func New(base storeutil.Base, txFunc, readTxFunc TxFunc, repFunc RepFunc) *Store {
	return &Store{Base: base, txFunc: txFunc, readTxFunc: readTxFunc, repFunc: repFunc}
}

func (s *Store) ensureDictionaryFresh(ctx context.Context, action string) error {
	if _, err := cache.EnsureDictionaryFresh(ctx, s.repFunc().Dictionary(), s.repFunc().Cache()); err != nil {
		return fmt.Errorf("can't refresh dictionary before tech card %s: %w", action, err)
	}
	return nil
}

// header columns shared by INSERT (AddTechCard) and UPDATE (UpdateTechCard). Cost
// targets and the flat construction-description strings are gone (description → details[];
// pricing is on costing).
// season_code/season_year are the normalized SKU-facing season (task 05). The legacy `season`
// column remains only as a canonical derived label for the existing UNIQUE key/read models.
// Q1/Q5: version/revision_date and the free-text roles designer/constructor/technologist/approved_by
// are no longer written — the card's version is its named releases (Rev.N) + the auto-journal, and
// roles are admin-account assignments. Migration 0223 removes the retired columns;
// approved_at/released_at are server-owned timestamps and remain.
// normalizeLegacyComposition maps the stored JSON-scalar form of tech_card.composition to plain
// wire text (M1) on the `SELECT *` read paths — the SQL projections do this via JSON_UNQUOTE in
// styleCompositionSelect, these scans must match (see entity.UnquoteLegacyComposition).
func normalizeLegacyComposition(cards []entity.TechCard) {
	for i := range cards {
		cards[i].Composition = entity.UnquoteLegacyComposition(cards[i].Composition)
	}
}

const techCardHeaderColumns = `style_number, style_number_source, name, brand, season, season_code, season_year, collection, category_id,
	target_gender, stage, status, approval_state, approved_at, released_at, target_drop_date,
	required_seam_allowance_mm, base_model_id, base_sample_size_id,
	measurement_unit, concept, notes, purpose, output_material_id, aux_subtype, created_by, updated_by`

const techCardHeaderValues = `:style_number, :style_number_source, :name, :brand, :season, :season_code, :season_year, :collection, :category_id,
	:target_gender, :stage, :status, :approval_state, :approved_at, :released_at, :target_drop_date,
	:required_seam_allowance_mm, :base_model_id, :base_sample_size_id,
	:measurement_unit, :concept, :notes, :purpose, :output_material_id, :aux_subtype, :created_by, :updated_by`

func techCardHeaderParams(tc *entity.TechCardInsert) map[string]any {
	// Default an unset purpose to sellable so a direct entity insert (not via dto) satisfies the
	// chk_tech_card_purpose CHECK — the dto already defaults it, this covers store-level callers.
	purpose := tc.Purpose
	if purpose == "" {
		purpose = entity.TechCardPurposeSellable
	}
	// Default an unset provenance to `generated` so a direct entity insert satisfies the
	// chk_tech_card_style_number_source CHECK (the dto defaults it too; this covers store callers).
	styleNumberSource := tc.StyleNumberSource
	if styleNumberSource == "" {
		styleNumberSource = entity.StyleNumberSourceGenerated
	}
	return map[string]any{
		"style_number":        tc.StyleNumber,
		"style_number_source": string(styleNumberSource),
		"created_by":          tc.CreatedBy,
		"updated_by":          tc.UpdatedBy,
		"purpose":             string(purpose),
		"output_material_id":  tc.OutputMaterialId,
		"aux_subtype":         tc.AuxSubtype,
		"name":                tc.Name,
		"brand":               tc.Brand,
		"season":              tc.SeasonLabel,
		"season_code":         tc.SeasonCode,
		"season_year":         tc.SeasonYear,
		"collection":          tc.Collection,
		"category_id":         tc.CategoryId,
		"target_gender":       tc.TargetGender,
		"stage":               string(tc.Stage),
		"status":              tc.Status,
		"approval_state":      string(tc.ApprovalState),
		"approved_at":         tc.ApprovedAt,
		"released_at":         tc.ReleasedAt,
		"target_drop_date":    tc.TargetDropDate,
		"base_model_id":       tc.BaseModelId,
		"base_sample_size_id": tc.BaseSampleSizeId,
		"measurement_unit":    string(tc.MeasurementUnit),
		"concept":             tc.Concept,
		"notes":               tc.Notes,

		// ТРЕБУЕМЫЙ ПРИПУСК (Ф3.2): the card's override of the workshop default. NULL = «take the
		// workshop's», 0 = «this model's выкройки carry the cut line». Written like any header scalar
		// and, deliberately, into no section digest projection.
		"required_seam_allowance_mm": tc.RequiredSeamAllowanceMm,
	}
}

// techCardInsertHeaderParams validates and derives the season fields that AddTechCard owns. Update
// deliberately skips this step because its SET list does not persist any season column; UpdateStyle
// is the sole write owner after creation.
func techCardInsertHeaderParams(tc *entity.TechCardInsert) (map[string]any, error) {
	if tc.SeasonCode.Valid != tc.SeasonYear.Valid {
		return nil, fmt.Errorf("sku_season code and year must be set or omitted together")
	}
	var seasonLabel sql.NullString
	if tc.SeasonCode.Valid {
		code := entity.SeasonEnum(tc.SeasonCode.String)
		if !entity.IsValidSeason(code) {
			return nil, fmt.Errorf("sku_season code %q is invalid", tc.SeasonCode.String)
		}
		if tc.SeasonYear.Int32 < 2000 || tc.SeasonYear.Int32 > 2099 {
			return nil, fmt.Errorf("sku_season year must be between 2000 and 2099")
		}
		seasonLabel = sql.NullString{
			String: fmt.Sprintf("%s%02d", code, tc.SeasonYear.Int32%100),
			Valid:  true,
		}
	}
	// Never trust a caller-provided display label: keep it a projection of the typed pair.
	tc.SeasonLabel = seasonLabel
	return techCardHeaderParams(tc), nil
}

// stampApprovalTimes makes the server authoritative for approved_at/released_at,
// ignoring any client-sent value: the stamp is set on the transition INTO
// approved/released, preserved across edits and re-release, and CLEARED when the
// card leaves those states (e.g. re-opened to draft) so a stale stamp can never lie.
func (s *Store) stampApprovalTimes(tc *entity.TechCardInsert, prevState entity.TechCardApprovalState, prevApprovedAt, prevReleasedAt sql.NullTime) {
	now := sql.NullTime{Time: s.Now(), Valid: true}
	prevApprovedish := prevState == entity.TechCardApprovalApproved || prevState == entity.TechCardApprovalReleased
	switch tc.ApprovalState {
	case entity.TechCardApprovalApproved, entity.TechCardApprovalReleased:
		if prevApprovedish && prevApprovedAt.Valid {
			tc.ApprovedAt = prevApprovedAt // keep the original approval time across edits
		} else {
			tc.ApprovedAt = now
		}
		if tc.ApprovalState == entity.TechCardApprovalReleased {
			if prevState == entity.TechCardApprovalReleased && prevReleasedAt.Valid {
				tc.ReleasedAt = prevReleasedAt
			} else {
				tc.ReleasedAt = now
			}
		} else {
			tc.ReleasedAt = sql.NullTime{} // approved but not (yet) released
		}
	default: // draft, in_review, obsolete — clear both so re-open can't carry a stale stamp
		tc.ApprovedAt = sql.NullTime{}
		tc.ReleasedAt = sql.NullTime{}
	}
}

// AddTechCard inserts a tech card and its child sections, returning the new id.
func (s *Store) AddTechCard(ctx context.Context, tc *entity.TechCardInsert) (int, error) {
	if err := s.ensureDictionaryFresh(ctx, "create"); err != nil {
		return 0, err
	}
	s.stampApprovalTimes(tc, "", sql.NullTime{}, sql.NullTime{})
	var id int
	err := s.txFunc(ctx, func(ctx context.Context, rep dependency.Repository) error {
		params, err := techCardInsertHeaderParams(tc)
		if err != nil {
			return err
		}
		id, err = storeutil.ExecNamedLastId(ctx, rep.DB(),
			fmt.Sprintf(`INSERT INTO tech_card (%s) VALUES (%s)`, techCardHeaderColumns, techCardHeaderValues),
			params)
		if err != nil {
			return fmt.Errorf("failed to insert tech card: %w", err)
		}
		// Derive the top/sub/type triple from category_id BEFORE the children go in:
		// insertTechCardChildren validates the size range against the card's category, which it reads
		// back off this row (validateTechCardSizeIDs -> loadTechCardCategoryPath).
		if err := syncStyleCategoryTriple(ctx, rep.DB(), id, tc.CategoryId); err != nil {
			return err
		}
		if err := insertTechCardChildren(ctx, rep.DB(), id, tc); err != nil {
			return err
		}
		// A card created directly in RELEASED state freezes at commit exactly like the update path,
		// and the apisrv snapshots it post-commit the same way — same backfill, same reasons (see
		// updateTechCardAndListOrphanedPatternURLs).
		if tc.ApprovalState == entity.TechCardApprovalReleased {
			if err := backfillBomPricesOnRelease(ctx, rep.DB(), id); err != nil {
				return err
			}
		}
		// A new card's colourways may already link products (they become "styled" and take the
		// style's season/model + colourway colour) — re-mint their SKUs while unlocked.
		if err := remintCardProducts(ctx, rep.DB(), id, nil); err != nil {
			return err
		}
		// Q1: open the auto-journal with the creation event.
		return appendTechCardRevision(ctx, rep.DB(), id, tc.CreatedBy, "header", "created", "tech card created")
	})
	if err != nil {
		return 0, fmt.Errorf("can't add tech card: %w", err)
	}
	return id, nil
}

// captureCardProductLinks returns the product ids belonging to this style. PR6 R1: after the
// tech_card_colorway→product merge every colourway is a product (product.style_id = card), so the
// style's products ARE its colourways.
func captureCardProductLinks(ctx context.Context, db dependency.DB, tcID int) ([]int, error) {
	rows, err := storeutil.QueryListNamed[struct {
		ProductID int `db:"product_id"`
	}](ctx, db, `SELECT id AS product_id FROM product WHERE style_id = :id`,
		map[string]any{"id": tcID})
	if err != nil {
		return nil, fmt.Errorf("capture card product links: %w", err)
	}
	ids := make([]int, 0, len(rows))
	for _, r := range rows {
		ids = append(ids, r.ProductID)
	}
	return ids, nil
}

// remintCardProducts re-mints the SKUs of every product affected by a colourway save: those linked
// after the save UNION any passed in `previous` (products that were linked before and may now be
// unlinked, so they revert to a standalone SKU). MintProductSKUs is a no-op for a frozen product.
func remintCardProducts(ctx context.Context, db dependency.DB, tcID int, previous []int) error {
	current, err := captureCardProductLinks(ctx, db, tcID)
	if err != nil {
		return err
	}
	seen := make(map[int]struct{}, len(current)+len(previous))
	for _, id := range append(current, previous...) {
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		if err := product.MintProductSKUs(ctx, db, id); err != nil {
			return fmt.Errorf("re-mint product %d after colourway change: %w", id, err)
		}
	}
	return nil
}

// UpdateTechCard updates a tech card and replaces its child sections. It is
// optimistically locked on expectedLockVersion (entity.ErrTechCardConflict on a
// mismatch), refuses to mutate a RELEASED card unless it is re-opened to DRAFT
// (entity.ErrTechCardReleased), and returns sql.ErrNoRows when no card exists.
func (s *Store) UpdateTechCard(ctx context.Context, id int, tc *entity.TechCardInsert, expectedLockVersion int) error {
	_, err := s.updateTechCardAndListOrphanedPatternURLs(ctx, id, tc, expectedLockVersion)
	return err
}

// UpdateTechCardAndListOrphanedPatternURLs updates a card and returns pattern-object URLs that the
// committed full-replace made globally unreferenced. The caller may remove those objects post-commit.
func (s *Store) UpdateTechCardAndListOrphanedPatternURLs(ctx context.Context, id int, tc *entity.TechCardInsert, expectedLockVersion int) ([]string, error) {
	return s.updateTechCardAndListOrphanedPatternURLs(ctx, id, tc, expectedLockVersion)
}

func (s *Store) updateTechCardAndListOrphanedPatternURLs(ctx context.Context, id int, tc *entity.TechCardInsert, expectedLockVersion int) ([]string, error) {
	if err := s.ensureDictionaryFresh(ctx, "update"); err != nil {
		return nil, err
	}
	var orphanedPatternURLs []string
	err := s.txFunc(ctx, func(ctx context.Context, rep dependency.Repository) error {
		// txFunc may retry the callback after a deadlock; never carry candidates from an aborted attempt.
		orphanedPatternURLs = nil
		cur, err := storeutil.QueryNamedOne[struct {
			LockVersion     int          `db:"lock_version"`
			ApprovalState   string       `db:"approval_state"`
			ApprovedAt      sql.NullTime `db:"approved_at"`
			ReleasedAt      sql.NullTime `db:"released_at"`
			Purpose         string       `db:"purpose"`
			Stage           string       `db:"stage"`
			MeasurementUnit string       `db:"measurement_unit"`
		}](ctx, rep.DB(),
			`SELECT lock_version, approval_state, approved_at, released_at, purpose, stage, measurement_unit
			 FROM tech_card WHERE id = :id`, map[string]any{"id": id})
		if err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return sql.ErrNoRows
			}
			return fmt.Errorf("failed to load tech card for update: %w", err)
		}
		// Freeze check BEFORE the version check (plan §4): a released card is frozen for
		// the factory — only a re-open to draft is allowed; any other edit while still
		// released is rejected. Checking this first means a stale-version edit of a
		// released card gets the actionable "re-open to draft" (FailedPrecondition) rather
		// than a misleading "modified concurrently" (Aborted).
		if cur.ApprovalState == string(entity.TechCardApprovalReleased) &&
			tc.ApprovalState != entity.TechCardApprovalDraft {
			return entity.ErrTechCardReleased
		}
		if cur.LockVersion != expectedLockVersion {
			return entity.ErrTechCardConflict
		}
		// NF-07: purpose is a commitment to what the card PRODUCES, so it may only move while nothing
		// downstream has committed to the old answer. The line is the first sale: until a colourway of
		// this style has actually been bought, mis-filing a dust bag as a garment is a correctable data
		// entry mistake, and the operator has to be able to correct it. Past that line the flip would
		// rewrite what a customer already bought, strand a batch's stock destination, or turn a
		// packing-spec component into a garment.
		//
		// A merely EXISTING colourway is not such a commitment — it is archivable, and an archived one
		// is retired work exactly as it is for the stage-regression guard below. That is why this reads
		// product.style_id (the single source, PR6 R1) rather than the tech_card_product mirror: the
		// mirror keeps its row for an archived colourway, so counting it left the operator with a lock
		// no admin action could clear.
		if cur.Purpose != string(tc.Purpose) {
			refs, err := storeutil.QueryNamedOne[struct {
				Runs           int `db:"runs"`
				LiveColorways  int `db:"live_colorways"`
				SoldColorways  int `db:"sold_colorways"`
				Assemblies     int `db:"assemblies"`
				OutputVariants int `db:"output_variants"`
			}](ctx, rep.DB(),
				// A CANCELLED run produced nothing, so it pins nothing. "Sold" is any order that got
				// past payment — a refunded sale still happened, and the placed/awaiting_payment/
				// cancelled carts the ordercleanup worker sweeps are not sales at all.
				`SELECT (SELECT COUNT(*) FROM production_run
				           WHERE tech_card_id = :id AND status <> 'cancelled')       AS runs,
				        (SELECT COUNT(*) FROM product
				           WHERE style_id = :id AND lifecycle_status <> :archived)   AS live_colorways,
				        (SELECT COUNT(DISTINCT oi.product_id)
				           FROM order_item oi
				           JOIN product p ON p.id = oi.product_id
				           JOIN customer_order co ON co.id = oi.order_id
				           JOIN order_status os ON os.id = co.order_status_id
				          WHERE p.style_id = :id
				            AND os.name NOT IN ('placed', 'awaiting_payment', 'cancelled')
				        )                                                            AS sold_colorways,
				        (SELECT COUNT(*) FROM style_assembly
				           WHERE component_tech_card_id = :id)                       AS assemblies,
				        (SELECT COUNT(*) FROM tech_card_output_variant
				           WHERE tech_card_id = :id)                                 AS output_variants`,
				map[string]any{"id": id, "archived": uint8(entity.ColorwayStatusArchived)})
			if err != nil {
				return fmt.Errorf("failed to check tech card purpose change: %w", err)
			}
			// Name the references that actually pin the purpose. The rule has independent arms and a
			// card usually trips exactly one of them, so reporting all of them reads as a false
			// positive ("but it has no runs") and hides the one thing to clear.
			if reason := purposeLockReason(refs.Runs, refs.LiveColorways, refs.SoldColorways,
				refs.Assemblies, refs.OutputVariants); reason != "" {
				return fmt.Errorf("%w: %s", entity.ErrTechCardPurposeLocked, reason)
			}
		}
		// A card's stage may advance but must not REGRESS once downstream artifacts exist: a sample, a
		// release snapshot, or a colourway (product.style_id) is work already committed at the card's
		// current maturity, so moving the stage back to an earlier ordinal (e.g. proto → idea) would
		// desync those artifacts from the card's declared stage. Forward and same-stage saves are always
		// allowed; a backward move is allowed only while nothing downstream exists. This runs inside the
		// same tx as the write, so a concurrent sample/colourway insert cannot slip past the count.
		if err := guardTechCardStageRegression(ctx, rep.DB(), id, entity.TechCardStage(cur.Stage), tc.Stage); err != nil {
			return err
		}
		// The measurement unit is a FACT ABOUT the values already on file, not an instruction to
		// convert them: tech_card_size_measurement.measurement_value is a bare DECIMAL(10,2) carrying
		// no unit of its own, and the storefront serves it without one either. Flipping cm↔mm on a
		// charted card therefore re-reads every point of measure as 10× or 1/10 of what was measured,
		// silently, all the way to the buyer.
		//
		// Two rules keep that impossible. An ABSENT unit preserves the stored one (a save that never
		// mentioned the unit must not re-unit the chart via the create-time default). An EXPLICIT flip
		// is refused while any measurement exists — the author has to clear the chart and re-enter it
		// in the new unit, which is the only conversion this code can perform reliably.
		if !tc.MeasurementUnitSet {
			tc.MeasurementUnit = entity.TechCardMeasurementUnit(cur.MeasurementUnit)
		} else if string(tc.MeasurementUnit) != cur.MeasurementUnit {
			charted, err := storeutil.QueryCountNamed(ctx, rep.DB(),
				`SELECT COUNT(*) FROM tech_card_size_measurement WHERE tech_card_id = :id`, map[string]any{"id": id})
			if err != nil {
				return fmt.Errorf("count tech card %d measurements: %w", id, err)
			}
			if charted > 0 {
				return entity.NewFieldViolation("measurement_unit", "chart_already_measured",
					fmt.Sprintf("%d values recorded in %s", charted, cur.MeasurementUnit),
					fmt.Sprintf("clear the size chart, then re-enter the measurements in %s — the stored numbers carry no unit, so switching it would re-read every one of them", tc.MeasurementUnit))
			}
		}

		// Server owns the lifecycle stamps (set on enter, cleared on re-open).
		s.stampApprovalTimes(tc, entity.TechCardApprovalState(cur.ApprovalState), cur.ApprovedAt, cur.ReleasedAt)

		params := techCardHeaderParams(tc)
		params["id"] = id
		params["expected_lock_version"] = expectedLockVersion
		// R4/§14.7: UpdateTechCard writes PLM facts ONLY. The catalogue-style facts (brand, sku_season
		// [season/season_code/season_year], collection, target_gender) moved to UpdateStyle so no fact is
		// written by two paths — a season change now goes through UpdateStyle's frozen-sibling guard
		// instead of silently re-minting here. AddTechCard still seeds them at creation. category_id
		// stays a PLM fact. The unused :brand/:season/... binds remain in params (sqlx.Named ignores
		// extra keys) so the base header parameter map stays shared with the insert.
		//
		// category_id is COALESCEd, not assigned: THE TECH-CARD WRITE NEVER UN-SETS A CATEGORY. This
		// update is a full replace of the header, so a card whose category was never chosen through
		// this UI — every style predating the category derivation — would otherwise have its category
		// silently wiped by an unrelated save (a note edit, a stage change). That is the exact "the
		// category disappears when I save" symptom this change fixes. Changing a category means picking
		// a different one; there is deliberately no way to clear it back to none.
		//
		// This is load-bearing on the bind being SQL NULL, not 0: entity.TechCardInsert.CategoryId is a
		// sql.NullInt32, and the wire's `0 = unset` is translated at the dto boundary by
		// nullInt32FromPb (internal/dto/model.go), which maps 0 to Valid:false. If that translation
		// ever changed to bind Valid:true/Int32:0, this COALESCE would see 0 rather than NULL, treat it
		// as a real value and write category_id = 0 — tripping fk_tech_card_category. A dto change
		// there must keep 0 mapping to NULL, or this needs NULLIF(:category_id, 0) instead.
		rows, err := storeutil.ExecNamedRows(ctx, rep.DB(), `
			UPDATE tech_card SET
				lock_version = lock_version + 1,
				style_number = :style_number, style_number_source = :style_number_source, name = :name,
				updated_by = :updated_by,
				category_id = COALESCE(:category_id, category_id),
				stage = :stage, status = :status, approval_state = :approval_state,
				approved_at = :approved_at, released_at = :released_at,
				target_drop_date = :target_drop_date,
				required_seam_allowance_mm = :required_seam_allowance_mm,
				base_model_id = :base_model_id, base_sample_size_id = :base_sample_size_id,
				measurement_unit = :measurement_unit, concept = :concept, notes = :notes,
					purpose = :purpose, output_material_id = :output_material_id, aux_subtype = :aux_subtype
			WHERE id = :id AND lock_version = :expected_lock_version`, params)
		if err != nil {
			return fmt.Errorf("failed to update tech card: %w", err)
		}
		// The row provably exists (loaded above), so 0 rows means lock_version moved
		// under us — make the WHERE guard load-bearing, not just the in-Go check.
		if rows == 0 {
			return entity.ErrTechCardConflict
		}

		// Re-derive the top/sub/type triple from the category_id just written, BEFORE the children are
		// rebuilt: insertTechCardChildren validates the new size range against the card's category,
		// which it reads back off this row (validateTechCardSizeIDs -> loadTechCardCategoryPath). Sync
		// any later and a save that changes the category AND the sizes together would validate the new
		// sizes against the OLD category. A no-op when category_id is unset — see the clobber rule on
		// syncStyleCategoryTriple.
		if err := syncStyleCategoryTriple(ctx, rep.DB(), id, tc.CategoryId); err != nil {
			return err
		}
		priorPatterns, err := techCardPatternRows(ctx, rep.DB(), id)
		if err != nil {
			return err
		}
		patternCleanupCandidates := patternURLsRemovedByPayload(priorPatterns, tc.Patterns)

		// Capture the style's products before the full-replace so a change to the style's SKU facts
		// re-mints every (unfrozen) sibling. PR6 R1: colourways are products (product.style_id), so
		// they are NOT part of the tech-card full-replace and keep their stable ids and sample links.
		prevProductLinks, err := captureCardProductLinks(ctx, rep.DB(), id)
		if err != nil {
			return err
		}

		// Full-replace: clear all child rows by tech_card_id. Grandchildren cascade from their
		// parents (detail media via tech_card_detail). Colourways are no longer a child of the card
		// (R1 merge) — they live in product and are managed via CreateColorway.
		// NB: tech_card_bom_item is NOT full-replaced here — it is reconciled by line_key in
		// upsertTechCardBom (S2/S3) so its ids stay stable for the referrer FKs. tech_card_piece is
		// likewise NOT full-replaced (WS4 / S8) — it is keyed-upserted so its ids stay stable for the
		// usage.piece_id FK; its piece_material grandchildren are cleared in Phase A (see
		// insertTechCardChildren) rather than here. The operation referrer IS cleared here BEFORE the
		// BOM upsert, so the only bom_item_id / piece_id RESTRICT that can fire is from a persistent
		// colourway usage — the intended cross-aggregate guard.
		// tech_card_revision is intentionally ABSENT as well: it is the append-only auto-journal
		// (Q1), not a client-replaced child, so a save must never wipe the history.
		//
		// The three 1:1 message sections are presence-aware: nil means the protobuf field was absent,
		// so preserve its stored row; non-nil means replace it. A present-but-empty message parses to
		// a non-nil all-zero entity and deliberately takes the replace path, clearing every value by
		// inserting an all-NULL row (valid for all three schemas). Lists remain full-replace.
		preserveAbsentSection := map[string]bool{
			"tech_card_construction": tc.Construction == nil,
			"tech_card_packaging":    tc.Packaging == nil,
			"tech_card_costing":      tc.Costing == nil,
		}
		for _, table := range []string{
			"tech_card_size", "tech_card_product", "tech_card_media",
			"tech_card_callout", "tech_card_detail",
			"tech_card_construction", "tech_card_operation", "tech_card_label",
			"tech_card_packaging", "tech_card_costing", "tech_card_issue", "tech_card_signoff",
		} {
			if preserveAbsentSection[table] {
				continue
			}
			if err := storeutil.ExecNamed(ctx, rep.DB(),
				fmt.Sprintf(`DELETE FROM %s WHERE tech_card_id = :id`, table),
				map[string]any{"id": id}); err != nil {
				return fmt.Errorf("failed to clear %s: %w", table, err)
			}
		}
		if err := insertTechCardChildren(ctx, rep.DB(), id, tc); err != nil {
			return err
		}
		// A card that goes RELEASED this save is frozen the moment this transaction commits, and the
		// post-commit release snapshot (snapshotReleaseIfReleased) marshals what the COLUMNS then
		// hold — so a linked BOM line whose catalog price appeared only after the material was linked
		// (unit_price still NULL) must take the current catalog price NOW, inside this transaction.
		// The freeze check above guarantees this save IS the release transition (a released card only
		// re-opens to draft), so the fill runs exactly once per release episode. Only NULL prices are
		// filled — an agreed price is never overwritten — and an unpriceable line stays NULL rather
		// than blocking the release. NB: the fill legitimately stales an approved MATERIALS sign-off,
		// including one approved in this same save — that is documented, deliberate, and non-blocking;
		// see the KNOWN CONSEQUENCE note on backfillBomPricesOnRelease before "fixing" it.
		if tc.ApprovalState == entity.TechCardApprovalReleased {
			if err := backfillBomPricesOnRelease(ctx, rep.DB(), id); err != nil {
				return err
			}
		}
		// UpdateTechCard is the BOM's write owner, so refresh the auto-derived style composition from
		// the just-upserted fabric lines before committing. Manual composition remains untouched.
		if err := product.ReconcileStyleCompositionTx(ctx, rep.DB(), id); err != nil {
			return err
		}
		// Re-mint SKUs for the style's products (a style SKU-fact change re-mints unfrozen siblings).
		if err := remintCardProducts(ctx, rep.DB(), id, prevProductLinks); err != nil {
			return err
		}
		// Q1: stamp the auto-journal — an approve/release transition is recorded as such, else `updated`.
		action, section, summary := revisionActionForUpdate(entity.TechCardApprovalState(cur.ApprovalState), tc.ApprovalState)
		if err := appendTechCardRevision(ctx, rep.DB(), id, tc.UpdatedBy, section, action, summary); err != nil {
			return err
		}
		orphanedPatternURLs, err = storeutil.UnreferencedPatternObjectURLs(ctx, rep.DB(), patternCleanupCandidates)
		return err
	})
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) || errors.Is(err, entity.ErrTechCardConflict) ||
			errors.Is(err, entity.ErrTechCardReleased) || errors.Is(err, entity.ErrTechCardPurposeLocked) {
			return nil, err
		}
		var ve *entity.ValidationError
		if errors.As(err, &ve) {
			return nil, ve
		}
		return nil, fmt.Errorf("can't update tech card: %w", err)
	}
	return orphanedPatternURLs, nil
}

// purposeLockReason renders the references that pin a card's purpose as an operator-readable list,
// or "" when nothing does. Each arm names both what is referencing the card and what to clear, so
// the message is a next step rather than a restatement of the rule.
func purposeLockReason(runs, liveColorways, soldColorways, assemblies, outputVariants int) string {
	var parts []string
	// Sold first: it is the only arm with no way out, so it must not read as one more chore in a
	// list of things to clear.
	if soldColorways > 0 {
		parts = append(parts, plural(soldColorways, "colourway", "colourways")+" already sold — the purpose is fixed once a customer has bought the style")
	}
	if runs > 0 {
		parts = append(parts, plural(runs, "production run", "production runs")+" already produced against it")
	}
	if liveColorways > 0 {
		parts = append(parts, plural(liveColorways, "live colourway", "live colourways")+" linked to it (archive them first — an archived colourway no longer pins the purpose)")
	}
	if assemblies > 0 {
		parts = append(parts, "used as a component in "+plural(assemblies, "style assembly", "style assemblies")+" (remove it there first)")
	}
	// Counted whether or not the variant is ACTIVE, matching the assemblies arm above (a deactivated
	// bill line pins too): a deactivated colour still owns a warehouse bucket with stock and history
	// that only an auxiliary card can produce into. The escape is delete, not deactivate.
	if outputVariants > 0 {
		parts = append(parts, plural(outputVariants, "colour variant", "colour variants")+
			" registered (delete them first — a colour variant pins the auxiliary purpose)")
	}
	return strings.Join(parts, "; ")
}

// plural renders "1 colourway" / "2 colourways" — spelled out per word because the -s rule gets
// "style assemblies" wrong.
func plural(n int, one, many string) string {
	if n == 1 {
		return fmt.Sprintf("%d %s", n, one)
	}
	return fmt.Sprintf("%d %s", n, many)
}

// guardTechCardStageRegression blocks a backward stage move (to an earlier lifecycle ordinal) when
// the card already has downstream artifacts: ≥1 sample, ≥1 release snapshot, or ≥1 colourway
// (product.style_id → this card). Forward moves, same-stage saves, and any move on a card with
// nothing downstream are allowed. It returns a field-tagged *entity.ValidationError on `stage`
// naming the first blocking artifact kind (the apisrv layer maps a ValidationError to a 400
// InvalidArgument); an unknown from/to stage is not comparable and is deferred to the schema CHECK,
// so the guard is a no-op there.
func guardTechCardStageRegression(ctx context.Context, db dependency.DB, id int, from, to entity.TechCardStage) error {
	fromOrd, fromOK := entity.TechCardStageOrdinal(from)
	toOrd, toOK := entity.TechCardStageOrdinal(to)
	if !fromOK || !toOK || toOrd >= fromOrd {
		return nil // forward, same-stage, or non-comparable: nothing to guard
	}
	// An ARCHIVED colourway (soft-deleted; product.lifecycle_status = 4) and a SCRAPPED sample are
	// retired work, not live downstream artifacts — neither pins the style's stage. Release snapshots
	// are immutable history and have no soft-delete state, so only their count stays unfiltered.
	counts, err := storeutil.QueryNamedOne[struct {
		Samples   int `db:"samples"`
		Releases  int `db:"releases"`
		Colorways int `db:"colorways"`
	}](ctx, db, `SELECT
		(SELECT COUNT(*) FROM sample WHERE tech_card_id = :id
			AND status <> 'scrapped')                                       AS samples,
		(SELECT COUNT(*) FROM tech_card_release WHERE tech_card_id = :id) AS releases,
		(SELECT COUNT(*) FROM product WHERE style_id = :id
			AND lifecycle_status <> :archived)                           AS colorways`,
		map[string]any{"id": id, "archived": uint8(entity.ColorwayStatusArchived)})
	if err != nil {
		return fmt.Errorf("count downstream artifacts for stage-regression guard: %w", err)
	}
	switch {
	case counts.Samples > 0:
		return stageRegressionViolation(to, counts.Samples, "sample")
	case counts.Releases > 0:
		return stageRegressionViolation(to, counts.Releases, "release")
	case counts.Colorways > 0:
		return stageRegressionViolation(to, counts.Colorways, "colourway")
	}
	return nil
}

// stageRegressionViolation builds the field-tagged rejection naming why a card cannot return to an
// earlier stage (n downstream artifacts of the given kind already exist).
func stageRegressionViolation(to entity.TechCardStage, n int, artifact string) error {
	return entity.NewFieldViolation("stage",
		fmt.Sprintf("cannot return to %s: %d %s(s) already exist", to, n, artifact),
		"", "advance the stage forward instead, or remove the downstream artifacts first")
}

// DeleteTechCard deletes a tech card by id (child sections cascade). It refuses when any of the
// card's samples has material stock movements: the sample rows cascade (ON DELETE CASCADE) and would
// orphan their issued-material cost. This guard stays STRICTER than deleting one sample (which asks
// only that the material came back — entity.SampleDeletionVerdict) on purpose: that path has a dialog
// naming the material and what to do with it, and this one snaps every sample of the card at once,
// silently and without a verdict, so «есть движения» is the only honest line it can draw (NF-04). It
// also refuses when the card is used as an auxiliary component in another style's assembly bill
// (style_assembly.component_tech_card_id -> tech_card ON DELETE RESTRICT, 0174) — a raw DB 1451 there
// would otherwise surface as an unreadable Internal (P4-flyover M2/S24-regression); both checks and the
// delete run in one transaction so a concurrent issue/assembly write cannot slip between them.
//
// sample_substitution.bom_item_id -> tech_card_bom_item is deliberately NOT guarded here: as of 0178 it
// is ON DELETE SET NULL (P4-flyover M3), so a substitution recorded against one of this card's own BOM
// lines degrades gracefully (bom_item_id -> NULL, original_material_id snapshot untouched) instead of
// blocking the delete — no COUNT-guard is needed because that FK can no longer 1451.
//
// Any OTHER RESTRICT this does not explicitly enumerate (e.g. the pre-existing product.style_id ->
// tech_card RESTRICT, 0138 — a style with live colourway products) still raises 1451; the caller
// (apisrv/admin) maps that residual case to a field-tagged FailedPrecondition rather than Internal.
func (s *Store) DeleteTechCard(ctx context.Context, id int) error {
	_, err := s.deleteTechCardAndListOrphanedPatternURLs(ctx, id)
	return err
}

// DeleteTechCardAndListOrphanedPatternURLs deletes a card and returns pattern-object URLs that no
// remaining card or fitting references. Candidate URLs are captured before the cascading delete.
func (s *Store) DeleteTechCardAndListOrphanedPatternURLs(ctx context.Context, id int) ([]string, error) {
	return s.deleteTechCardAndListOrphanedPatternURLs(ctx, id)
}

func (s *Store) deleteTechCardAndListOrphanedPatternURLs(ctx context.Context, id int) ([]string, error) {
	var orphanedPatternURLs []string
	err := s.txFunc(ctx, func(ctx context.Context, rep dependency.Repository) error {
		orphanedPatternURLs = nil
		n, err := storeutil.QueryCountNamed(ctx, rep.DB(), `
			SELECT COUNT(*) FROM material_stock_movement m
			JOIN sample s ON s.id = m.sample_id WHERE s.tech_card_id = :id`, map[string]any{"id": id})
		if err != nil {
			return fmt.Errorf("check tech card sample movements: %w", err)
		}
		if n > 0 {
			return entity.ErrSampleHasMovements
		}
		asmCount, err := storeutil.QueryCountNamed(ctx, rep.DB(), `
			SELECT COUNT(*) FROM style_assembly WHERE component_tech_card_id = :id`, map[string]any{"id": id})
		if err != nil {
			return fmt.Errorf("check tech card assembly usage: %w", err)
		}
		if asmCount > 0 {
			return entity.NewFieldViolation("tech_card_id",
				fmt.Sprintf("used as an assembly component in %d style(s)", asmCount),
				"style_assembly", "remove it from those assembly bills first")
		}
		priorPatternURLs, err := techCardPatternURLs(ctx, rep.DB(), id)
		if err != nil {
			return err
		}
		rows, err := storeutil.ExecNamedRows(ctx, rep.DB(),
			`DELETE FROM tech_card WHERE id = :id`, map[string]any{"id": id})
		if err != nil {
			var me *mysql.MySQLError
			if errors.As(err, &me) && me.Number == 1451 { // ER_ROW_IS_REFERENCED_2: an un-enumerated RESTRICT
				return entity.NewFieldViolation("tech_card_id",
					"still referenced by another record", "", "remove the referencing record first")
			}
			return fmt.Errorf("failed to delete tech card: %w", err)
		}
		if rows == 0 {
			return sql.ErrNoRows
		}
		orphanedPatternURLs, err = storeutil.UnreferencedPatternObjectURLs(ctx, rep.DB(), priorPatternURLs)
		return err
	})
	if err != nil {
		return nil, err
	}
	return orphanedPatternURLs, nil
}

// GetTechCardById returns a tech card with its child sections and resolved media.
func (s *Store) GetTechCardById(ctx context.Context, id int) (*entity.TechCard, error) {
	tc, err := storeutil.QueryNamedOne[entity.TechCard](ctx, s.DB,
		`SELECT * FROM tech_card WHERE id = :id`, map[string]any{"id": id})
	if err != nil {
		return nil, fmt.Errorf("failed to get tech card: %w", err)
	}
	cards := []entity.TechCard{tc}
	normalizeLegacyComposition(cards)
	if err := s.enrich(ctx, cards); err != nil {
		return nil, err
	}
	// Q5: responsible-account roles are their own child collection (managed via dedicated RPCs), so
	// load them for the single-card read here rather than through the full-replace enrich.
	roles, err := s.ListTechCardRoleAssignments(ctx, id)
	if err != nil {
		return nil, err
	}
	cards[0].RoleAssignments = roles
	// M1 fix: load the structured composition (S17) into its own typed field, alongside — never
	// instead of — the legacy free-text column already read by the `SELECT *` above.
	if err := loadStructuredComposition(ctx, s.DB, &cards[0]); err != nil {
		return nil, err
	}
	// Slots: attach the identity + latest price of every catalog article the card references —
	// BOM slot defaults AND colourway pins — so the costing can price a pinned article and the
	// production plan can label/convert its rollup rows without N extra reads per caller.
	ids := make([]int, 0, len(cards[0].BomItems))
	seenIDs := map[int]bool{}
	addID := func(v sql.NullInt64) {
		if v.Valid && v.Int64 > 0 && !seenIDs[int(v.Int64)] {
			seenIDs[int(v.Int64)] = true
			ids = append(ids, int(v.Int64))
		}
	}
	for i := range cards[0].BomItems {
		addID(cards[0].BomItems[i].MaterialId)
	}
	for i := range cards[0].Colorways {
		for j := range cards[0].Colorways[i].Usages {
			addID(cards[0].Colorways[i].Usages[j].MaterialId)
		}
	}
	linked, err := s.getMaterialsByIDs(ctx, ids)
	if err != nil {
		return nil, err
	}
	cards[0].LinkedMaterials = linked
	// Colour variants (0252) of an auxiliary card's warehouse output. Loaded on the same connection
	// as everything above so the consistent read sees one snapshot, and NOT degraded to a warning
	// like the list enrichers: on the single-card read this is the card's editable content, and an
	// editor that silently rendered zero variants would offer a "+ add colour" that duplicates them.
	//
	// Only an auxiliary card can have them, and the guards make that an invariant rather than a
	// convention — so a sellable style neither pays for a guaranteed-empty query on every read nor
	// takes a dependency on the new table.
	if cards[0].Purpose == entity.TechCardPurposeAuxiliary {
		variants, err := listOutputVariants(ctx, s.DB, id)
		if err != nil {
			return nil, err
		}
		cards[0].OutputVariants = variants
	}
	// Saved раскладки (0257), summaries only — the blob rides GetTechCardMarker. Loaded for every
	// purpose (an auxiliary кофр is cut from fabric exactly like a garment), same
	// no-degradation reasoning as the variants above: this is editable card content.
	// The card's cut-pieces travel in because the Ф3.6 comparison («did the set of pieces change since
	// this раскладка was taken») needs them and s.enrich has already loaded them — so the whole check
	// costs zero extra queries here.
	markers, err := listMarkerSummaries(ctx, s.DB, id, cards[0].Pieces)
	if err != nil {
		return nil, err
	}
	cards[0].Markers = markers
	// Измеренные площади деталей (Ф0, 0297) — вход, из которого выводится норма расхода, когда её
	// никто не вписал. Читаются здесь, а не по требованию: их видят и костинг, и смета, и плановая
	// цена партии, и все они ходят через эту же карточку. Пустая карта — законное «никто ещё не
	// мерил», и это ДРУГОЕ утверждение, чем «этой ткани не нужно полотна».
	areas, err := s.GetTechCardPieceAreas(ctx, id)
	if err != nil {
		return nil, err
	}
	cards[0].PieceAreaScopes = areas
	// Токен входов себестоимости, которых нет в записи карточки (Ф-П): площади и назначения деталей
	// на ткань. Считается ТОЙ ЖЕ функцией, что на записи, — не «так же», а буквально той же: чтение
	// рецепта не выбирает line_key (поля провода), и вторая реализация дала бы другой токен об одном
	// и том же множестве, то есть подпись, устаревшую с рождения. Цена — один запрос на чтение
	// карточки, у которой есть площади или пер-детальные строки.
	derived, err := s.GetTechCardDerivedCostInputsDigest(ctx, id)
	if err != nil {
		return nil, err
	}
	cards[0].DerivedCostInputsDigest = derived
	return &cards[0], nil
}

// GetTechCardByIdConsistent returns the same enriched card as GetTechCardById, with every
// query participating in one REPEATABLE READ snapshot.
func (s *Store) GetTechCardByIdConsistent(ctx context.Context, id int) (*entity.TechCard, error) {
	var card *entity.TechCard
	err := s.readTxFunc(ctx, func(ctx context.Context, rep dependency.Repository) error {
		var err error
		card, err = rep.TechCards().GetTechCardById(ctx, id)
		return err
	})
	if err != nil {
		return nil, fmt.Errorf("failed to get consistent tech card: %w", err)
	}
	return card, nil
}

// ListTechCards returns a paged, header-only list of tech cards (no child
// sections), with the total number of matching cards (ignoring pagination).
func (s *Store) ListTechCards(ctx context.Context, limit, offset int, orderFactor entity.OrderFactor, filter entity.TechCardListFilter) ([]entity.TechCard, int, error) {
	limit, offset = clampPagination(limit, offset)

	params := map[string]any{}
	where := ""
	if filter.Stage != "" {
		where += " AND stage = :stage"
		params["stage"] = filter.Stage
	}
	if filter.Gender != "" {
		where += " AND target_gender = :gender"
		params["gender"] = filter.Gender
	}
	if filter.Brand != "" {
		where += " AND brand LIKE :brand"
		params["brand"] = "%" + escapeLike(filter.Brand) + "%"
	}
	if filter.SeasonCode != "" {
		where += " AND season_code = :seasonCode AND season_year = :seasonYear"
		params["seasonCode"] = string(filter.SeasonCode)
		params["seasonYear"] = filter.SeasonYear
	}
	if filter.Name != "" {
		where += " AND (name LIKE :nameSearch OR style_number LIKE :nameSearch)"
		params["nameSearch"] = "%" + escapeLike(filter.Name) + "%"
	}
	if filter.ProductId > 0 {
		where += " AND id IN (SELECT tech_card_id FROM tech_card_product WHERE product_id = :productId)"
		params["productId"] = filter.ProductId
	}
	if filter.Purpose != "" {
		where += " AND purpose = :purpose"
		params["purpose"] = filter.Purpose
	}
	if len(filter.CategoryIds) > 0 {
		// Matched at every level, so one id works whichever node the operator picked: the leaf tag
		// (category_id) and the derived taxonomy triple are all compared against the same set. The
		// triple is maintained by syncStyleCategoryTriple on every write, so this needs no join.
		where += ` AND (category_id IN (:categoryIds) OR top_category_id IN (:categoryIds)
			OR sub_category_id IN (:categoryIds) OR type_id IN (:categoryIds))`
		params["categoryIds"] = filter.CategoryIds
	}

	total, err := storeutil.QueryCountNamed(ctx, s.DB,
		fmt.Sprintf(`SELECT COUNT(*) FROM tech_card WHERE 1=1%s`, where), params)
	if err != nil {
		return nil, 0, fmt.Errorf("can't count tech cards: %w", err)
	}

	params["limit"] = limit
	params["offset"] = offset
	cards, err := storeutil.QueryListNamed[entity.TechCard](ctx, s.DB, fmt.Sprintf(`
		SELECT * FROM tech_card
		WHERE 1=1%s
		ORDER BY id %s
		LIMIT :limit OFFSET :offset`, where, orderFactor.String()), params)
	if err != nil {
		return nil, 0, fmt.Errorf("can't list tech cards: %w", err)
	}
	normalizeLegacyComposition(cards)

	// Resolve a preview thumbnail per card for grid/gallery views (B-9). One batched media query for
	// the whole page (not N+1); a failure to load media degrades to an empty preview, not a list error.
	ids := make([]int, len(cards))
	for i := range cards {
		ids[i] = cards[i].Id
	}
	if _, full, mErr := s.mediaByTechCardIds(ctx, ids); mErr != nil {
		slog.Default().WarnContext(ctx, "can't resolve tech card list previews; previews omitted",
			slog.String("err", mErr.Error()))
	} else {
		for i := range cards {
			cards[i].PreviewURL = pickTechCardPreviewURL(cards[i].Stage, full[cards[i].Id])
		}
	}
	// Colourway counts and auxiliary output stock, batched for the page like the previews above and
	// degrading the same way: a list must still render when a secondary fact cannot be resolved.
	if err := s.enrichListFacts(ctx, cards); err != nil {
		slog.Default().WarnContext(ctx, "can't resolve tech card list facts; counts omitted",
			slog.String("err", err.Error()))
	}
	return cards, total, nil
}

// enrichListFacts fills the per-row facts a list/board row shows but the tech_card row does not
// carry: how many live colourways the style has, and (for an auxiliary card) the name and on-hand
// balance of the material its runs receipt into.
//
// Two batched queries for the whole page. Both were previously N+1 in the client — the aux picker
// ran one GetTechCard per card plus a warehouse read, capped at 40 cards to stop it fanning out.
func (s *Store) enrichListFacts(ctx context.Context, cards []entity.TechCard) error {
	if len(cards) == 0 {
		return nil
	}
	ids := make([]int, 0, len(cards))
	for i := range cards {
		ids = append(ids, cards[i].Id)
	}
	counts, err := storeutil.QueryListNamed[struct {
		StyleID int `db:"style_id"`
		N       int `db:"n"`
	}](ctx, s.DB, `
		SELECT style_id, COUNT(*) AS n FROM product
		WHERE style_id IN (:ids) AND lifecycle_status <> :archived
		GROUP BY style_id`,
		map[string]any{"ids": ids, "archived": uint8(entity.ColorwayStatusArchived)})
	if err != nil {
		return fmt.Errorf("count colourways for tech card list: %w", err)
	}
	countByStyle := make(map[int]int, len(counts))
	for _, c := range counts {
		countByStyle[c.StyleID] = c.N
	}

	// Only auxiliary cards have an output material; asking for the others' would join for nothing.
	auxIDs := make([]int, 0, len(cards))
	for i := range cards {
		if cards[i].OutputMaterialId.Valid && cards[i].OutputMaterialId.Int64 > 0 {
			auxIDs = append(auxIDs, cards[i].Id)
		}
	}
	type auxRow struct {
		TechCardID int                 `db:"tech_card_id"`
		Name       string              `db:"name"`
		OnHand     decimal.NullDecimal `db:"on_hand"`
	}
	outputByCard := make(map[int]auxRow, len(auxIDs))
	if len(auxIDs) > 0 {
		// LEFT JOIN on material_stock: a material with no movements yet has no stock row at all, and
		// that must read as "no balance recorded", not as an absent material.
		rows, err := storeutil.QueryListNamed[auxRow](ctx, s.DB, `
			SELECT t.id AS tech_card_id, m.name AS name, ms.on_hand AS on_hand
			FROM tech_card t
			JOIN material m ON m.id = t.output_material_id
			LEFT JOIN material_stock ms ON ms.material_id = m.id
			WHERE t.id IN (:ids)`, map[string]any{"ids": auxIDs})
		if err != nil {
			return fmt.Errorf("load auxiliary output materials for tech card list: %w", err)
		}
		for _, r := range rows {
			outputByCard[r.TechCardID] = r
		}
	}

	// Colour variants (0252) summarised for the row: how many colours the card produces and what the
	// warehouse holds across them. ACTIVE only — a retired colour is not a colour this card makes, and
	// the badge is read as "what can I plan", not as an archive count (the purpose lock, which counts
	// every row, is a different question asked for a different reason).
	type variantRow struct {
		TechCardID int                 `db:"tech_card_id"`
		N          int                 `db:"n"`
		OnHand     decimal.NullDecimal `db:"on_hand"`
	}
	// Only auxiliary cards can have variants at all, so a page of garment styles asks nothing —
	// same reasoning as the output-material query above, keyed on purpose rather than on the
	// legacy single output (a varianted card need never have had one).
	auxCards := make([]int, 0, len(cards))
	for i := range cards {
		if cards[i].Purpose == entity.TechCardPurposeAuxiliary {
			auxCards = append(auxCards, cards[i].Id)
		}
	}
	variantsByCard := make(map[int]variantRow, len(auxCards))
	if len(auxCards) > 0 {
		// SUM(ms.on_hand), NOT SUM(COALESCE(ms.on_hand, 0)): SQL's SUM skips NULLs and returns NULL for
		// a group where every bucket is unstocked, which is exactly the distinction the row must keep.
		// A card whose colours have no stock ROW has no balance recorded and renders "—"; coalescing
		// would assert a measured zero — "we counted, there are none" — which is a different and
		// possibly wrong statement. A group mixing stocked and unstocked buckets sums the stocked ones.
		variantRows, err := storeutil.QueryListNamed[variantRow](ctx, s.DB, `
			SELECT v.tech_card_id, COUNT(*) AS n, SUM(ms.on_hand) AS on_hand
			FROM tech_card_output_variant v
			LEFT JOIN material_stock ms ON ms.material_id = v.material_id
			WHERE v.tech_card_id IN (:ids) AND v.active = TRUE
			GROUP BY v.tech_card_id`, map[string]any{"ids": auxCards})
		if err != nil {
			return fmt.Errorf("count colour variants for tech card list: %w", err)
		}
		for _, r := range variantRows {
			variantsByCard[r.TechCardID] = r
		}
	}

	// Saved раскладки (0257): the row badge is a bare count — a "latest consumption" here would
	// lie without naming the size and BOM slot it was measured for, so it stays off the list.
	//
	// КАРТОЧНЫЕ ONLY — run_id IS NULL (Ф4, 0282), the same filter listMarkerSummaries applies, and
	// the two have to agree: this badge is read as «сколько раскладок у карточки», and the list the
	// operator opens next shows exactly the card's markers. Counting раскройные однодневки here would
	// make the badge drift upward with every прогон and never come back down, and clicking it would
	// show fewer rows than the number promised — the badge accusing the list of hiding something.
	type markerCountRow struct {
		TechCardID int `db:"tech_card_id"`
		N          int `db:"n"`
	}
	allIDs := make([]int, 0, len(cards))
	for i := range cards {
		allIDs = append(allIDs, cards[i].Id)
	}
	markersByCard := make(map[int]int, len(cards))
	if len(allIDs) > 0 {
		markerRows, err := storeutil.QueryListNamed[markerCountRow](ctx, s.DB, `
			SELECT tech_card_id, COUNT(*) AS n FROM tech_card_marker
			WHERE tech_card_id IN (:ids) AND run_id IS NULL
			GROUP BY tech_card_id`, map[string]any{"ids": allIDs})
		if err != nil {
			return fmt.Errorf("count markers for tech card list: %w", err)
		}
		for _, r := range markerRows {
			markersByCard[r.TechCardID] = r.N
		}
	}

	for i := range cards {
		cards[i].ColorwayCount = countByStyle[cards[i].Id]
		if out, ok := outputByCard[cards[i].Id]; ok {
			cards[i].OutputMaterialName = out.Name
			cards[i].OutputMaterialOnHand = out.OnHand
		}
		if v, ok := variantsByCard[cards[i].Id]; ok {
			cards[i].OutputVariantCount = v.N
			cards[i].OutputVariantsOnHand = v.OnHand
		}
		cards[i].MarkerCount = markersByCard[cards[i].Id]
	}
	return nil
}

// pickTechCardPreviewURL chooses the thumbnail URL for a list/gallery card (B-9). `media` is ordered
// by display_order. For an IDEA card the mood/reference image best represents it (a technical sketch
// may not exist yet); otherwise the flat PREVIEW sketch is preferred. Falls back down a chain so any
// media beats none, and returns "" when the card has no media.
func pickTechCardPreviewURL(stage entity.TechCardStage, media []entity.TechCardMediaFull) string {
	if len(media) == 0 {
		return ""
	}
	var firstMoodboard, firstTechnical, previewKind string
	for i := range media {
		url := media[i].Media.ThumbnailMediaURL
		if url == "" {
			url = media[i].Media.CompressedMediaURL
		}
		if url == "" {
			continue
		}
		switch media[i].Category {
		case entity.TechCardMediaCategoryMoodboard:
			if firstMoodboard == "" {
				firstMoodboard = url
			}
		case entity.TechCardMediaCategoryTechnical:
			if firstTechnical == "" {
				firstTechnical = url
			}
			if previewKind == "" && media[i].Kind == entity.TechCardMediaPreview {
				previewKind = url
			}
		}
	}
	if stage == entity.TechCardStageIdea {
		if firstMoodboard != "" {
			return firstMoodboard
		}
		if previewKind != "" {
			return previewKind
		}
		return firstTechnical
	}
	if previewKind != "" {
		return previewKind
	}
	if firstTechnical != "" {
		return firstTechnical
	}
	return firstMoodboard
}

// defaultPipelineCardsPerStage is how many light cards each pipeline column returns when the caller
// doesn't specify (gap-01).
const defaultPipelineCardsPerStage = 8

// stylePipelineOrder is the lifecycle order of the development-board columns.
var stylePipelineOrder = []entity.TechCardStage{
	entity.TechCardStageIdea, entity.TechCardStageProto, entity.TechCardStageFit,
	entity.TechCardStageSMS, entity.TechCardStagePP, entity.TechCardStageProd,
}

// GetStylePipeline returns the development board (gap-01): one column per lifecycle stage in order,
// each with its full card count and up to cardsPerStage most-recently-updated light cards (with a
// resolved preview thumbnail). DB scale is small, so this is one grouped count query plus one small
// query per stage — no window functions needed — and a single batched media resolve for previews.
func (s *Store) GetStylePipeline(ctx context.Context, cardsPerStage int) ([]entity.StylePipelineColumn, error) {
	if cardsPerStage <= 0 {
		cardsPerStage = defaultPipelineCardsPerStage
	}
	if cardsPerStage > maxPageLimit {
		cardsPerStage = maxPageLimit
	}

	countRows, err := storeutil.QueryListNamed[struct {
		Stage string `db:"stage"`
		C     int    `db:"c"`
	}](ctx, s.DB, `SELECT stage, COUNT(*) AS c FROM tech_card GROUP BY stage`, map[string]any{})
	if err != nil {
		return nil, fmt.Errorf("can't count tech cards by stage: %w", err)
	}
	counts := make(map[string]int, len(countRows))
	for _, r := range countRows {
		counts[r.Stage] = r.C
	}

	cols := make([]entity.StylePipelineColumn, 0, len(stylePipelineOrder))
	var previewIDs []int
	for _, st := range stylePipelineOrder {
		cards, err := storeutil.QueryListNamed[entity.TechCard](ctx, s.DB, `
			SELECT * FROM tech_card WHERE stage = :stage
			ORDER BY updated_at DESC, id DESC LIMIT :n`,
			map[string]any{"stage": string(st), "n": cardsPerStage})
		if err != nil {
			return nil, fmt.Errorf("can't list %s tech cards: %w", st, err)
		}
		normalizeLegacyComposition(cards)
		cols = append(cols, entity.StylePipelineColumn{Stage: st, Count: counts[string(st)], Cards: cards})
		for i := range cards {
			previewIDs = append(previewIDs, cards[i].Id)
		}
	}

	// Resolve preview thumbnails for every card on the board in one batched query (degrade to no
	// preview on failure, don't fail the board).
	if _, full, mErr := s.mediaByTechCardIds(ctx, previewIDs); mErr != nil {
		slog.Default().WarnContext(ctx, "can't resolve pipeline previews; previews omitted",
			slog.String("err", mErr.Error()))
	} else {
		for ci := range cols {
			for i := range cols[ci].Cards {
				cols[ci].Cards[i].PreviewURL = pickTechCardPreviewURL(cols[ci].Cards[i].Stage, full[cols[ci].Cards[i].Id])
			}
		}
		// The board renders the same list-item message as ListTechCards, so its cards need the same
		// per-row facts — otherwise a colourway count that is real in the list reads as 0 on the board.
		for ci := range cols {
			if err := s.enrichListFacts(ctx, cols[ci].Cards); err != nil {
				slog.Default().WarnContext(ctx, "can't resolve pipeline list facts; counts omitted",
					slog.String("stage", string(cols[ci].Stage)), slog.String("err", err.Error()))
				continue
			}
		}
	}
	return cols, nil
}

// insertTechCardChildren inserts the size range, product links, sketch media,
// callouts and revisions for a tech card (used by both Add and Update).
func insertTechCardChildren(ctx context.Context, db dependency.DB, id int, tc *entity.TechCardInsert) error {
	if err := validateTechCardSizeIDs(ctx, db, id, tc.SizeIds); err != nil {
		return err
	}
	if err := insertTechCardSizes(ctx, db, id, tc.SizeIds, tc.SizeQuantities); err != nil {
		return err
	}
	if err := pruneSizeScopedDataOutsideRange(ctx, db, id, tc.SizeIds); err != nil {
		return err
	}
	// PR6 R1/R4: the product↔style link is derived from product.style_id (single source), never
	// client-supplied. Keep tech_card_product (the denormalised link every cost/margin/inventory
	// consumer still reads) in sync with the canonical set on every save. On create it is empty
	// (colourways get their style_id via CreateColorway); on update it re-asserts the current set.
	productLinks, err := captureCardProductLinks(ctx, db, id)
	if err != nil {
		return err
	}
	if err := insertTechCardProducts(ctx, db, id, productLinks); err != nil {
		return err
	}
	if err := insertTechCardMedia(ctx, db, id, tc.Media); err != nil {
		return err
	}
	if err := insertTechCardCallouts(ctx, db, id, tc.Callouts); err != nil {
		return err
	}
	// Q1: tech_card_revision is a server-stamped auto-journal now, not a client full-replace — it is
	// appended by AddTechCard/UpdateTechCard (appendTechCardRevision), never written from tc.Revisions.
	if err := insertTechCardDetails(ctx, db, id, tc.Details); err != nil {
		return err
	}
	// Cut-pieces (WS4 / S8): pieces are keyed-upserted (not full-replaced) so their ids stay stable —
	// which is what lets a colourway recipe usage hold a real piece_id FK RESTRICT (the deferred half
	// of 0159). Phase A (§D5): release each piece's OLD piece_material → bom_item refs BEFORE the BOM
	// upsert, so a BOM line the client is deleting is not falsely blocked by a stale RESTRICT; the
	// fresh mapping is re-inserted by upsertTechCardPieces once the BOM ids resolve. No-op on create.
	if err := clearTechCardPieceMaterials(ctx, db, id); err != nil {
		return err
	}
	// Materials (WS3 / S2-S3): the BOM article catalog is reconciled by line_key (keyed upsert-diff),
	// not full-replaced, so each line's id is stable — which is what lets pieces/operations/colourway
	// recipes hold a real bom_item_id FK. The resolver turns a line's key/position into that id.
	bomRes, err := upsertTechCardBom(ctx, db, id, tc.BomItems)
	if err != nil {
		return err
	}
	// Cut-pieces (WS4 / S8): keyed-upsert by line_key (piece ids stable); re-insert each piece's
	// per-colourway fabric mapping with the resolved bom_item_id. calloutSync (built from the same
	// payload) derives each piece's name from its technical-sketch callout and marks moodboard/orphan
	// links detached (S6/S7/S8).
	if err := upsertTechCardPieces(ctx, db, id, tc.Pieces, bomRes, buildCalloutSync(tc)); err != nil {
		return err
	}
	// DXF block aliases (§2.2) resolve piece_line_key against THIS save's pieces, so they run after
	// the piece upsert — the resolveUsagePiece ordering precedent.
	if err := upsertTechCardPieceDxfAliases(ctx, db, id, tc.PieceDxfAliasesSet, tc.PieceDxfAliases, tc.UpdatedBy); err != nil {
		return err
	}
	// production (Phase 3)
	if err := insertTechCardConstruction(ctx, db, id, tc.Construction); err != nil {
		return err
	}
	if err := insertTechCardOperations(ctx, db, id, tc.Operations, bomRes); err != nil {
		return err
	}
	if err := insertTechCardLabels(ctx, db, id, tc.Labels, bomRes); err != nil {
		return err
	}
	if err := insertTechCardPackaging(ctx, db, id, tc.Packaging); err != nil {
		return err
	}
	if err := insertTechCardCosting(ctx, db, id, tc.Costing); err != nil {
		return err
	}
	if err := insertTechCardIssues(ctx, db, id, tc.Issues); err != nil {
		return err
	}
	if err := insertTechCardSignoffs(ctx, db, id, tc.Signoffs); err != nil {
		return err
	}
	return insertTechCardPatterns(ctx, db, id, tc.Patterns, tc.SizeIds)
}

// patternHistoryRow is what a pattern row remembers across a full-replace: which revision it is and
// when its PDF first arrived.
//
// A ROW is identified by (size_id, url), not by url alone. The url does identify the uploaded object
// (bucket.GetMediaName embeds a timestamp plus a random suffix, so two uploads never collide), but the
// same object is legitimately attached under several sizes — the factory sends one combined sheet and
// the operator hangs it on XS and S — while `version` is numbered per (tech_card_id, size_id). Keying
// the carry-forward on url alone let one size's sheet inherit the OTHER size's Rev number, and the
// per-size MAX+1 numbering then skipped or duplicated revisions on later saves.
type patternHistoryRow struct {
	Id            int            `db:"id"`
	LineKey       string         `db:"line_key"`
	BomLineKey    sql.NullString `db:"bom_line_key"`
	FabricPurpose sql.NullString `db:"fabric_purpose"`
	URL           string         `db:"url"`
	SizeId        int            `db:"size_id"`
	Version       int            `db:"version"`
	UploadedAt    sql.NullTime   `db:"uploaded_at"`
	Name          sql.NullString `db:"name"`
}

func techCardPatternURLs(ctx context.Context, db dependency.DB, techCardID int) ([]string, error) {
	rows, err := techCardPatternRows(ctx, db, techCardID)
	if err != nil {
		return nil, err
	}
	urls := make([]string, 0, len(rows))
	for _, row := range rows {
		urls = append(urls, row.URL)
	}
	return urls, nil
}

func techCardPatternRows(ctx context.Context, db dependency.DB, techCardID int) ([]patternHistoryRow, error) {
	rows, err := storeutil.QueryListNamed[patternHistoryRow](ctx, db,
		// COALESCE on size_id: NULL is the graded sheet (0281) and reads as 0 in the entity, the
		// same value the wire uses for it.
		`SELECT id, COALESCE(line_key, '') AS line_key, bom_line_key, fabric_purpose,
		        url, COALESCE(size_id, 0) AS size_id, version, uploaded_at, name
		 FROM tech_card_size_pattern WHERE tech_card_id = :id`,
		map[string]any{"id": techCardID})
	if err != nil {
		return nil, fmt.Errorf("load tech card patterns: %w", err)
	}
	return rows, nil
}

// patternURLsRemovedByPayload limits cleanup to object identities the user actually removed. The
// size-range filter in insertTechCardPatterns is a server-side projection: a pattern the request
// still carries must not have its object deleted merely because its size is not currently live.
// Comparing canonical keys also treats origin/CDN forms of the same object as the same intent.
func patternURLsRemovedByPayload(prior []patternHistoryRow, payload []entity.TechCardSizePattern) []string {
	payloadObjects := make(map[string]struct{}, len(payload))
	for _, pattern := range payload {
		payloadObjects[patternObjectIdentity(pattern.URL)] = struct{}{}
	}
	candidates := make([]string, 0, len(prior))
	seen := make(map[string]struct{}, len(prior))
	for _, pattern := range prior {
		identity := patternObjectIdentity(pattern.URL)
		if _, carried := payloadObjects[identity]; carried {
			continue
		}
		if _, duplicate := seen[identity]; duplicate {
			continue
		}
		seen[identity] = struct{}{}
		candidates = append(candidates, pattern.URL)
	}
	return candidates
}

func patternObjectIdentity(raw string) string {
	if key, ok := storeutil.PatternObjectKey(raw); ok {
		return "key:" + key
	}
	return "url:" + raw
}

// insertTechCardPatterns reconciles a card's pattern rows as a keyed upsert-diff (Ф9.2; it was a
// full-replace before line keys existed), CARRYING FORWARD each sheet's revision, upload time, name
// and fabric binding.
//
// Unlike the other children this owns its own reads and deletes (it is excluded from the blind
// delete loop in UpdateTechCard) because it has to read before it writes. Rows outside the current
// size range are dropped during narrowing; duplicate (size, url) payload rows keep their first
// occurrence, while duplicate line_keys are REJECTED upstream in parseTechCardPatterns (a duplicate
// key may carry a distinct row — keeping the first would silently delete the row the second
// claimed). Runs AFTER upsertTechCardBom in insertTechCardChildren, so a binding to a slot created
// by the same save resolves.
func insertTechCardPatterns(ctx context.Context, db dependency.DB, id int, patterns []entity.TechCardSizePattern, sizeIDs []int) error {
	prior, err := techCardPatternRows(ctx, db, id)
	if err != nil {
		return fmt.Errorf("failed to read tech card patterns: %w", err)
	}
	// Carry-forward stays keyed EXACTLY as before (version per (size, url), uploaded_at per url,
	// name presence-gated per (size, url)) — the line_key decides only which ROW a payload entry is
	// (UPDATE vs INSERT vs DELETE), so a legacy keyless save behaves byte-identically to the old
	// delete-all path, and a keyed save survives a url change (sheet replacement) without losing the
	// row's identity or its fabric binding.
	known := make(map[string]patternHistoryRow, len(prior))
	byLineKey := make(map[string]patternHistoryRow, len(prior))
	firstUploadByURL := make(map[string]sql.NullTime, len(prior))
	maxVersionBySize := make(map[int]int, len(prior))
	for _, r := range prior {
		known[patternHistoryKey(r.SizeId, r.URL)] = r
		if r.LineKey != "" {
			byLineKey[r.LineKey] = r
		}
		if prev, seen := firstUploadByURL[r.URL]; !seen ||
			(r.UploadedAt.Valid && (!prev.Valid || r.UploadedAt.Time.Before(prev.Time))) {
			firstUploadByURL[r.URL] = r.UploadedAt
		}
		if r.Version > maxVersionBySize[r.SizeId] {
			maxVersionBySize[r.SizeId] = r.Version
		}
	}
	now := time.Now().UTC()
	liveSizes := make(map[int]struct{}, len(sizeIDs))
	for _, sizeID := range sizeIDs {
		liveSizes[sizeID] = struct{}{}
	}
	// A payload row is projected onto the CURRENT size range: a sheet left on a size the save is
	// dropping is not written (its object survives — patternURLsRemovedByPayload compares urls).
	// Size 0 is not a size but the absence of one (0281): the sheet is graded, its sizes live in the
	// file, and the range has nothing to say about it — including when the range is still empty,
	// which is the whole point of the sizeless row.
	filedUnderLiveSize := func(sizeID int) bool {
		if sizeID == 0 {
			return true
		}
		_, ok := liveSizes[sizeID]
		return ok
	}
	// The card's cloth lines are loaded lazily, only when some payload row binds a NEW cloth —
	// bindings that merely round-trip the stored value are tolerated even when the target is gone
	// («слот удалён» is a UI state, not a reason to block the save). Both halves come along: since
	// 0267 a sheet may name a назначение instead of a line, and resolving that needs the purposes.
	var rollGoods []entity.RollGoodsLine
	loadRollGoods := func() ([]entity.RollGoodsLine, error) {
		if rollGoods != nil {
			return rollGoods, nil
		}
		lines, err := loadRollGoodsLines(ctx, db, id)
		if err != nil {
			return nil, err
		}
		rollGoods = lines
		return rollGoods, nil
	}
	seenPayload := make(map[string]struct{}, len(patterns))
	seenKeys := make(map[string]struct{}, len(patterns))
	consumed := make(map[int]struct{}, len(prior))
	// Pass 1: reserve every EXPLICIT payload line_key before any keyless adoption runs, so payload
	// order cannot change the outcome — with a keyless row first, single-pass matching would let it
	// adopt a stored row that a later keyed row names, silently dropping that keyed row. A keyless
	// row may only adopt a stored row no keyed payload row claims.
	reservedKeys := make(map[string]struct{}, len(patterns))
	// keyedPairs records the (size, url) of every KEYED payload row: a KEYLESS row carrying the
	// same pair is that row's echo (a stale duplicate of one sheet) — letting it adopt or insert
	// would either steal the keyed row's stored identity or mint a phantom duplicate, and which
	// one happened would depend on payload order.
	keyedPairs := make(map[string]struct{}, len(patterns))
	// Every (size, url) the payload claims, keyed or not. Read only by the sizeless adoption below,
	// and only to REFUSE: a stored row whose own pair some payload row still names belongs to that
	// row, and re-filing a different row onto it would depend on payload order.
	payloadPairs := make(map[string]struct{}, len(patterns))
	for _, p := range patterns {
		if !filedUnderLiveSize(p.SizeId) {
			continue
		}
		payloadPairs[patternHistoryKey(p.SizeId, p.URL)] = struct{}{}
		if p.LineKey != "" {
			reservedKeys[p.LineKey] = struct{}{}
			keyedPairs[patternHistoryKey(p.SizeId, p.URL)] = struct{}{}
		}
	}
	order := 0
	for i, p := range patterns {
		if !filedUnderLiveSize(p.SizeId) {
			continue
		}
		key := patternHistoryKey(p.SizeId, p.URL)
		if p.LineKey == "" {
			if _, keyed := keyedPairs[key]; keyed {
				// A keyed payload row owns this sheet; this keyless duplicate is its echo.
				continue
			}
			if _, duplicate := seenPayload[key]; duplicate {
				// Keyless-vs-keyless (size, url) dupe — a genuine duplicate, keep the first
				// (lossless). Keyed rows never enter this dedupe: the dto rejects keyed pairs
				// sharing (size, url), and for direct store callers keeping both is the
				// non-destructive choice.
				continue
			}
			seenPayload[key] = struct{}{}
		}
		// Row identity, three steps (PIECES-WASTAGE-DESIGN §2.1): (1) an explicit line_key names its
		// stored row; (2) a keyless payload row adopts the line_key of an unconsumed stored row with
		// the same (size, url) — the legacy path, so stale clients cannot sever bindings; (3) no
		// match — a genuinely new row, with the client's fresh ULID or a server-minted key.
		var matched *patternHistoryRow
		lineKey := p.LineKey
		if lineKey != "" {
			if r, ok := byLineKey[lineKey]; ok {
				if _, used := consumed[r.Id]; !used {
					matched = &r
				}
			}
		} else if r, ok := known[key]; ok && r.LineKey != "" {
			_, used := consumed[r.Id]
			_, taken := reservedKeys[r.LineKey]
			if !used && !taken {
				matched = &r
				lineKey = r.LineKey
			}
		} else if p.SizeId == 0 {
			// (2b) A KEYLESS SIZELESS row adopts the one stored row carrying this url, whatever size
			// it was filed under. Re-filing a sheet to «no size» (0281) is the same sheet, and without
			// this the (size, url) lookup above misses, the stored row falls into the delete-the-rest
			// loop below, and the sheet comes back as a brand-new row — losing its line_key, its name
			// (the fallback is read off the row this misses) and its fabric binding. Only the identity
			// is lost, not the file: patternURLsRemovedByPayload compares urls, and the url is still
			// here. Today only a direct store caller can produce this — the admin client round-trips
			// line_keys — but the dto used to reject size 0 outright, so this is the guard that
			// rejection was quietly serving.
			//
			// Order-independent by construction, which the whole two-pass design exists to protect:
			// a stored row is adopted only when NOTHING else can claim it — no live payload row names
			// its own (size, url), no keyed row reserves its key, it is not consumed — and only when
			// it is the ONLY candidate for the url. The legitimate «one combined sheet hung on XS and
			// S» leaves two candidates, so nothing is adopted and the old behaviour stands.
			var only *patternHistoryRow
			for i := range prior {
				r := prior[i]
				if r.URL != p.URL || r.LineKey == "" {
					continue
				}
				if _, used := consumed[r.Id]; used {
					continue
				}
				if _, taken := reservedKeys[r.LineKey]; taken {
					continue
				}
				if _, claimed := payloadPairs[patternHistoryKey(r.SizeId, r.URL)]; claimed {
					continue
				}
				if only != nil {
					only = nil
					break
				}
				only = &r
			}
			if only != nil {
				matched = only
				lineKey = only.LineKey
			}
		}
		if lineKey == "" {
			lineKey = newLineKey()
		}
		// Unreachable from the API — parseTechCardPatterns rejects duplicate payload keys before the
		// tx — kept as defense-in-depth for direct store callers. NOTE this is NOT like a (size, url)
		// dupe: a duplicate KEY may carry a distinct row, so silently keeping the first would delete
		// the stored row the duplicate claimed; the dto reject is the real guard.
		if _, dup := seenKeys[lineKey]; dup {
			continue
		}
		seenKeys[lineKey] = struct{}{}
		version, uploadedAt := p.Version, p.UploadedAt
		nameFallback := known[key].Name
		if matched != nil {
			// line_key IS the identity: the matched row's own name is the fallback — including when
			// the new url happens to coincide with a DIFFERENT stored row's (size, url) history.
			nameFallback = matched.Name
		}
		name := storeutil.ResolvePatternName(p.Name, nameFallback)
		if seen, ok := known[key]; ok {
			// This exact sheet is already on this size: it keeps its identity, the client never owns these.
			if version <= 0 {
				version = seen.Version
			}
			uploadedAt = seen.UploadedAt
		} else if first, ok := firstUploadByURL[p.URL]; ok {
			// The same PDF, but under a different size (a combined sheet spread across the range, or a
			// sheet moved between sizes). Its arrival time carries over; its revision number does NOT —
			// numbering is per size, so within THIS size the sheet is new and takes MAX+1 below.
			uploadedAt = first
		} else {
			uploadedAt = sql.NullTime{Time: now, Valid: true}
		}
		if _, exactPair := known[key]; matched != nil && !exactPair &&
			version == matched.Version && matched.URL != p.URL {
			// A keyed row whose (size, url) left its stored history BECAUSE THE FILE CHANGED — the
			// sheet was replaced — carrying the SAME version the replaced row had is an ECHO: the
			// schema round-trips version, so a client naturally resends the old number. A replacement
			// is a new revision by definition: force MAX+1. A genuine manual pin differs from the
			// replaced row's number and passes untouched.
			//
			// A MOVE is excluded (matched.URL == p.URL): the same file changing which size it is filed
			// under — including onto no size at all (0281) — is not a new revision of anything, and
			// renumbering it would rewind the operator's visible «Rev.3» to «Rev.1» AND stale the 0280
			// size index for every scope holding that sheet, because the fingerprint hashes version.
			// The gate would then answer UNKNOWN until somebody re-ran «⌕ размеры в файлах», for a
			// re-file that changed no geometry whatsoever.
			version = 0
		}
		if version <= 0 {
			maxVersionBySize[p.SizeId]++
			version = maxVersionBySize[p.SizeId]
		} else if version > maxVersionBySize[p.SizeId] {
			// A client-pinned number still moves the high-water mark, or the next auto-assigned
			// revision for that size would collide with it.
			maxVersionBySize[p.SizeId] = version
		}
		// The fabric binding is presence-gated like the name: absent carries the stored value
		// forward. Both halves are gated independently, which is what makes the 0267 transition
		// additive — a client that speaks only назначение leaves the legacy line exactly as it was,
		// and a client that predates 0267 leaves the назначение exactly as it was.
		var storedBinding, storedPurpose sql.NullString
		if matched != nil {
			storedBinding = matched.BomLineKey
			storedPurpose = matched.FabricPurpose
		}
		bomLineKey := storeutil.ResolveNullableOnPresence(p.BomLineKey, storedBinding)
		fabricPurpose := storeutil.ResolveNullableOnPresence(p.FabricPurpose, storedPurpose)
		// A present, non-empty value that CHANGES the binding must name something live; an unchanged
		// round-trip passes even when the target has gone away since.
		if fabricPurpose.Valid && fabricPurpose.String != "" &&
			(!storedPurpose.Valid || storedPurpose.String != fabricPurpose.String) {
			lines, err := loadRollGoods()
			if err != nil {
				return err
			}
			if !entity.ResolveFabricScope(fabricPurpose.String, "", lines).Live() {
				return entity.NewFieldViolation(fmt.Sprintf("patterns[%d].fabric_purpose", i),
					fmt.Sprintf("ни одна строка ткани этой карты не имеет назначения %q", fabricPurpose.String), "",
					"задай это назначение нужной строке на вкладке BOM (поле «назначение»), потом привяжи к нему лекало — или оставь лист непривязанным")
			}
		}
		if bomLineKey.Valid && bomLineKey.String != "" &&
			(!storedBinding.Valid || storedBinding.String != bomLineKey.String) {
			lines, err := loadRollGoods()
			if err != nil {
				return err
			}
			if !entity.ResolveFabricScope("", bomLineKey.String, lines).Live() {
				return entity.NewFieldViolation(fmt.Sprintf("patterns[%d].bom_line_key", i),
					fmt.Sprintf("no roll-goods BOM line %q in this tech card", bomLineKey.String), "",
					"на вкладке ВЫКРОЙКИ выбери для этого DXF ткань — строку BOM секции ткань, подкладка, бортовка или утеплитель, — или оставь лист непривязанным")
			}
		}
		params := map[string]any{
			"tech_card_id":   id,
			"line_key":       lineKey,
			"bom_line_key":   bomLineKey,
			"fabric_purpose": fabricPurpose,
			// 0 goes down as NULL, not as size 0: the FK would reject a literal 0, and NULL is the
			// column's own word for «размер живёт в файле».
			"size_id":       sql.NullInt64{Int64: int64(p.SizeId), Valid: p.SizeId > 0},
			"url":           p.URL,
			"filename":      p.Filename,
			"name":          name,
			"size_bytes":    p.SizeBytes,
			"version":       version,
			"uploaded_at":   uploadedAt,
			"display_order": order,
		}
		order++
		if matched != nil {
			consumed[matched.Id] = struct{}{}
			params["id"] = matched.Id
			if err := storeutil.ExecNamed(ctx, db, `
				UPDATE tech_card_size_pattern SET
					line_key = :line_key, bom_line_key = :bom_line_key, fabric_purpose = :fabric_purpose,
					size_id = :size_id, url = :url,
					filename = :filename, name = :name, size_bytes = :size_bytes, version = :version,
					uploaded_at = :uploaded_at, display_order = :display_order
				WHERE id = :id`, params); err != nil {
				return fmt.Errorf("failed to update tech card pattern: %w", err)
			}
			continue
		}
		if err := storeutil.ExecNamed(ctx, db, `
			INSERT INTO tech_card_size_pattern
				(tech_card_id, line_key, bom_line_key, fabric_purpose, size_id, url, filename, name, size_bytes, version, uploaded_at, display_order)
			VALUES (:tech_card_id, :line_key, :bom_line_key, :fabric_purpose, :size_id, :url, :filename, :name, :size_bytes, :version, :uploaded_at, :display_order)`,
			params); err != nil {
			return fmt.Errorf("failed to insert tech card pattern: %w", err)
		}
	}
	// Stored rows no payload entry claimed are gone — их urls попадают в GC-кандидаты ровно как при
	// старом delete-all (patternURLsRemovedByPayload сравнивает urls, не строки).
	for _, r := range prior {
		if _, used := consumed[r.Id]; used {
			continue
		}
		if err := storeutil.ExecNamed(ctx, db,
			`DELETE FROM tech_card_size_pattern WHERE id = :id`, map[string]any{"id": r.Id}); err != nil {
			return fmt.Errorf("failed to delete tech card pattern: %w", err)
		}
	}
	return nil
}

// patternHistoryKey identifies one pattern row across a full-replace: the sheet (url) AND the size it
// hangs on, because `version` is numbered within a size (see patternHistoryRow).
func patternHistoryKey(sizeID int, url string) string {
	return fmt.Sprintf("%d|%s", sizeID, url)
}

// validateTechCardSizeIDs enforces S10/WS5's server-side size-write guard on the style's OWN size
// range (tech_card_size, "size_ids" on the wire): each requested size must belong to a system
// permitted for the card's CURRENT category (top/sub/type_id, owned solely by UpdateStyle -- see
// product/style.go -- so it is read fresh from the row here rather than trusted from tc, which never
// carries a category on this write path). Returns a field-tagged *entity.ValidationError naming the
// first offending size ("size_ids[i]") for the caller to surface as InvalidArgument. Add/Update
// refresh the revisioned dictionary before entering their transaction, so an unknown id here is a
// genuine invalid input rather than another instance's recently-created size.
func validateTechCardSizeIDs(ctx context.Context, db dependency.DB, id int, sizeIDs []int) error {
	if len(sizeIDs) == 0 {
		return nil
	}
	path, err := loadTechCardCategoryPath(ctx, db, id)
	if err != nil {
		return fmt.Errorf("load tech card %d category: %w", id, err)
	}
	rules := cache.GetCategorySizeSystems()
	label := cache.CategoryLabel(path)
	for i, sid := range sizeIDs {
		sz, ok := cache.GetSizeById(sid)
		if !ok {
			return entity.NewFieldViolation(fmt.Sprintf("size_ids[%d]", i), "size_not_found", "",
				fmt.Sprintf("choose an existing size from the refreshed dictionary; size id %d does not exist", sid))
		}
		if verr := entity.ValidateSizeAgainstCategory(fmt.Sprintf("size_ids[%d]", i), path, label, rules, sz); verr != nil {
			return verr
		}
	}
	return nil
}

// loadTechCardCategoryPath reads a tech card's CURRENT category triple. Two paths write it and both
// leave the row self-consistent, so this always reflects the latest assigned category regardless of
// which RPC is mid-flight in the same transaction:
//   - Add/UpdateTechCard DERIVE the triple from the card's single category_id (syncStyleCategoryTriple),
//     which runs earlier in this same transaction — so a category change is visible here immediately.
//   - UpdateStyle (R4/§14.7) writes the triple directly on the product/legacy route and derives
//     category_id back from it, so the two representations cannot diverge.
func loadTechCardCategoryPath(ctx context.Context, db dependency.DB, id int) (entity.StyleCategoryPath, error) {
	return storeutil.QueryNamedOne[entity.StyleCategoryPath](ctx, db,
		`SELECT top_category_id, sub_category_id, type_id FROM tech_card WHERE id = :id`,
		map[string]any{"id": id})
}

func insertTechCardSizes(ctx context.Context, db dependency.DB, id int, sizeIDs []int, quantities []entity.TechCardSizeQuantity) error {
	if len(sizeIDs) == 0 {
		return nil
	}
	qtyBySize := make(map[int]int, len(quantities))
	for _, q := range quantities {
		qtyBySize[q.SizeId] = q.OrderQty
	}
	rows := make([]map[string]any, 0, len(sizeIDs))
	for i, sid := range sizeIDs {
		var orderQty any
		if q, ok := qtyBySize[sid]; ok {
			orderQty = q
		}
		rows = append(rows, map[string]any{"tech_card_id": id, "size_id": sid, "order_qty": orderQty, "display_order": i})
	}
	if err := storeutil.BulkInsert(ctx, db, "tech_card_size", rows); err != nil {
		return fmt.Errorf("failed to insert tech card sizes: %w", err)
	}
	return nil
}

// pruneSizeScopedDataOutsideRange drops the size-scoped style data a save leaves stranded when it
// NARROWS the size range. It runs immediately after the new range is written, in the same transaction.
//
// Nothing does this by itself: tech_card_size_measurement hangs off size(id) with RESTRICT (0149), not
// off tech_card_size, so dropping L from the range used to leave the L column on file — and the
// storefront then served it to buyers as current measurements for a colourway that still has a live L
// variant, which is the actual harm. The grade rule's base size is the same shape of leak: it is a
// plain FK to size(id), so a narrowed range could keep grading from a size the style no longer makes.
//
// An EMPTY range is deliberately left alone. It means "no grid declared", the early-stage state every
// other size guard treats as permissive (storeutil.TechCardSizeRange, the sample writer) — reading it
// as "delete the chart" would let one save with no size_ids wipe authored measurements.
func pruneSizeScopedDataOutsideRange(ctx context.Context, db dependency.DB, id int, sizeIDs []int) error {
	if len(sizeIDs) == 0 {
		return nil
	}
	params := map[string]any{"id": id, "sizes": sizeIDs}
	if err := storeutil.ExecNamed(ctx, db, `
		DELETE FROM tech_card_size_measurement
		WHERE tech_card_id = :id AND size_id NOT IN (:sizes)`, params); err != nil {
		return fmt.Errorf("prune style %d measurements outside its size range: %w", id, err)
	}
	if err := storeutil.ExecNamed(ctx, db, `
		UPDATE tech_card SET grade_base_size_id = NULL
		WHERE id = :id AND grade_base_size_id IS NOT NULL AND grade_base_size_id NOT IN (:sizes)`,
		params); err != nil {
		return fmt.Errorf("clear style %d grade base outside its size range: %w", id, err)
	}
	return nil
}

func insertTechCardProducts(ctx context.Context, db dependency.DB, id int, productIDs []int) error {
	if len(productIDs) == 0 {
		return nil
	}
	rows := make([]map[string]any, 0, len(productIDs))
	for _, pid := range productIDs {
		rows = append(rows, map[string]any{"tech_card_id": id, "product_id": pid})
	}
	if err := storeutil.BulkInsert(ctx, db, "tech_card_product", rows); err != nil {
		return fmt.Errorf("failed to insert tech card products: %w", err)
	}
	return nil
}

func insertTechCardMedia(ctx context.Context, db dependency.DB, id int, media []entity.TechCardMediaItem) error {
	if len(media) == 0 {
		return nil
	}
	rows := make([]map[string]any, 0, len(media))
	for i, m := range media {
		rows = append(rows, map[string]any{
			"tech_card_id":  id,
			"media_id":      m.MediaId,
			"category":      string(m.Category),
			"kind":          string(m.Kind),
			"caption":       m.Caption,
			"display_order": i,
		})
	}
	if err := storeutil.BulkInsert(ctx, db, "tech_card_media", rows); err != nil {
		return fmt.Errorf("failed to insert tech card media: %w", err)
	}
	return nil
}

func insertTechCardCallouts(ctx context.Context, db dependency.DB, id int, callouts []entity.TechCardCallout) error {
	if len(callouts) == 0 {
		return nil
	}
	rows := make([]map[string]any, 0, len(callouts))
	for i, c := range callouts {
		rows = append(rows, map[string]any{
			"tech_card_id":   id,
			"callout_number": c.Number,
			"part":           c.Part,
			"description":    c.Description,
			"dimensions":     c.Dimensions,
			"media_id":       c.MediaId,
			"pos_x":          c.PosX,
			"pos_y":          c.PosY,
			"display_order":  i,
		})
	}
	if err := storeutil.BulkInsert(ctx, db, "tech_card_callout", rows); err != nil {
		return fmt.Errorf("failed to insert tech card callouts: %w", err)
	}
	return nil
}

// appendTechCardRevision writes one server-stamped entry to the auto-journal (Q1): who (author,
// GetAdminUsername), what (section + action + human summary) and when (created_at DEFAULT now). It is
// append-only — never a full-replace — so the history of a card's significant transitions accrues.
func appendTechCardRevision(ctx context.Context, db dependency.DB, id int, author, section, action, summary string) error {
	if err := storeutil.ExecNamed(ctx, db, `
		INSERT INTO tech_card_revision (tech_card_id, author, section, action, change_note)
		VALUES (:tech_card_id, :author, :section, :action, :summary)`,
		map[string]any{
			"tech_card_id": id,
			"author":       sql.NullString{String: author, Valid: author != ""},
			"section":      sql.NullString{String: section, Valid: section != ""},
			"action":       action,
			"summary":      sql.NullString{String: summary, Valid: summary != ""},
		}); err != nil {
		return fmt.Errorf("failed to append tech card revision: %w", err)
	}
	return nil
}

// revisionActionForUpdate classifies an update into the journal action (Q1): a transition INTO
// approved/released is recorded as such; any other save is a generic `updated`.
func revisionActionForUpdate(prev, next entity.TechCardApprovalState) (action, section, summary string) {
	switch {
	case next == entity.TechCardApprovalReleased && prev != entity.TechCardApprovalReleased:
		return "released", "signoff", "released to manufacture"
	case next == entity.TechCardApprovalApproved && prev != entity.TechCardApprovalApproved:
		return "approved", "signoff", "approved"
	default:
		return "updated", "header", "tech card updated"
	}
}

// clampPagination normalizes a client-supplied limit/offset.
func clampPagination(limit, offset int) (int, int) {
	if limit <= 0 {
		limit = defaultPageLimit
	}
	if limit > maxPageLimit {
		limit = maxPageLimit
	}
	if offset < 0 {
		offset = 0
	}
	return limit, offset
}

// escapeLike escapes LIKE wildcards in a user-supplied search term.
func escapeLike(s string) string {
	r := strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`)
	return r.Replace(s)
}
