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

	"github.com/go-sql-driver/mysql"
	"github.com/jekabolt/grbpwr-manager/internal/cache"
	"github.com/jekabolt/grbpwr-manager/internal/dependency"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/jekabolt/grbpwr-manager/internal/store/product"
	"github.com/jekabolt/grbpwr-manager/internal/store/storeutil"
)

// Pagination bounds for list endpoints.
const (
	defaultPageLimit = 50
	maxPageLimit     = 100
)

// TxFunc is a function that executes f within a transaction.
type TxFunc func(ctx context.Context, f func(context.Context, dependency.Repository) error) error

// Store implements dependency.TechCards.
type Store struct {
	storeutil.Base
	txFunc TxFunc
}

// New creates a new tech card store.
func New(base storeutil.Base, txFunc TxFunc) *Store {
	return &Store{Base: base, txFunc: txFunc}
}

// header columns shared by INSERT (AddTechCard) and UPDATE (UpdateTechCard). Cost
// targets and the flat construction-description strings are gone (description → details[];
// pricing is on costing).
// season_code/season_year are the normalized SKU-facing season (task 05). The legacy `season`
// column remains only as a canonical derived label for the existing UNIQUE key/read models.
// Q1/Q5: version/revision_date and the free-text roles designer/constructor/technologist/approved_by
// are no longer written — the card's version is its named releases (Rev.N) + the auto-journal, and
// roles are admin-account assignments. The columns stay until M3; the write path just stops touching
// them. approved_at/released_at are server-owned timestamps and remain.
// normalizeLegacyComposition maps the stored JSON-scalar form of tech_card.composition to plain
// wire text (M1) on the `SELECT *` read paths — the SQL projections do this via JSON_UNQUOTE in
// styleCompositionSelect, these scans must match (see entity.UnquoteLegacyComposition).
func normalizeLegacyComposition(cards []entity.TechCard) {
	for i := range cards {
		cards[i].Composition = entity.UnquoteLegacyComposition(cards[i].Composition)
	}
}

const techCardHeaderColumns = `style_number, style_number_source, name, brand, season, season_code, season_year, collection, category_id,
	target_gender, stage, status, approval_state, approved_at, released_at,
	base_model_id, base_sample_size_id,
	measurement_unit, concept, notes, purpose, output_material_id, aux_subtype, created_by, updated_by`

const techCardHeaderValues = `:style_number, :style_number_source, :name, :brand, :season, :season_code, :season_year, :collection, :category_id,
	:target_gender, :stage, :status, :approval_state, :approved_at, :released_at,
	:base_model_id, :base_sample_size_id,
	:measurement_unit, :concept, :notes, :purpose, :output_material_id, :aux_subtype, :created_by, :updated_by`

func techCardHeaderParams(tc *entity.TechCardInsert) (map[string]any, error) {
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
		"season":              seasonLabel,
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
		"base_model_id":       tc.BaseModelId,
		"base_sample_size_id": tc.BaseSampleSizeId,
		"measurement_unit":    string(tc.MeasurementUnit),
		"concept":             tc.Concept,
		"notes":               tc.Notes,
	}, nil
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
	s.stampApprovalTimes(tc, "", sql.NullTime{}, sql.NullTime{})
	var id int
	err := s.txFunc(ctx, func(ctx context.Context, rep dependency.Repository) error {
		params, err := techCardHeaderParams(tc)
		if err != nil {
			return err
		}
		id, err = storeutil.ExecNamedLastId(ctx, rep.DB(),
			fmt.Sprintf(`INSERT INTO tech_card (%s) VALUES (%s)`, techCardHeaderColumns, techCardHeaderValues),
			params)
		if err != nil {
			return fmt.Errorf("failed to insert tech card: %w", err)
		}
		if err := insertTechCardChildren(ctx, rep.DB(), id, tc); err != nil {
			return err
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
	err := s.txFunc(ctx, func(ctx context.Context, rep dependency.Repository) error {
		cur, err := storeutil.QueryNamedOne[struct {
			LockVersion   int          `db:"lock_version"`
			ApprovalState string       `db:"approval_state"`
			ApprovedAt    sql.NullTime `db:"approved_at"`
			ReleasedAt    sql.NullTime `db:"released_at"`
			Purpose       string       `db:"purpose"`
			Stage         string       `db:"stage"`
		}](ctx, rep.DB(),
			`SELECT lock_version, approval_state, approved_at, released_at, purpose, stage FROM tech_card WHERE id = :id`, map[string]any{"id": id})
		if err != nil {
			if err == sql.ErrNoRows {
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
		// NF-07: purpose is a one-way commitment once the card has runs or products — flipping
		// sellable↔auxiliary afterwards would strand a batch's stock destination or a product link.
		if cur.Purpose != string(tc.Purpose) {
			var refs int
			refs, err = storeutil.QueryCountNamed(ctx, rep.DB(),
				`SELECT (SELECT COUNT(*) FROM production_run WHERE tech_card_id = :id)
				      + (SELECT COUNT(*) FROM tech_card_product WHERE tech_card_id = :id)`,
				map[string]any{"id": id})
			if err != nil {
				return fmt.Errorf("failed to check tech card purpose change: %w", err)
			}
			if refs > 0 {
				return entity.ErrTechCardPurposeLocked
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
		// Server owns the lifecycle stamps (set on enter, cleared on re-open).
		s.stampApprovalTimes(tc, entity.TechCardApprovalState(cur.ApprovalState), cur.ApprovedAt, cur.ReleasedAt)

		params, err := techCardHeaderParams(tc)
		if err != nil {
			return err
		}
		params["id"] = id
		params["expected_lock_version"] = expectedLockVersion
		// R4/§14.7: UpdateTechCard writes PLM facts ONLY. The catalogue-style facts (brand, sku_season
		// [season/season_code/season_year], collection, target_gender) moved to UpdateStyle so no fact is
		// written by two paths — a season change now goes through UpdateStyle's frozen-sibling guard
		// instead of silently re-minting here. AddTechCard still seeds them at creation. category_id
		// stays a PLM fact (UpdateStyle does not write it). The unused :brand/:season/... binds remain in
		// params (sqlx.Named ignores extra keys) so techCardHeaderParams stays shared with the insert.
		rows, err := storeutil.ExecNamedRows(ctx, rep.DB(), `
			UPDATE tech_card SET
				lock_version = lock_version + 1,
				style_number = :style_number, style_number_source = :style_number_source, name = :name,
				updated_by = :updated_by,
				category_id = :category_id,
				stage = :stage, status = :status, approval_state = :approval_state,
				approved_at = :approved_at, released_at = :released_at,
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
		for _, table := range []string{
			"tech_card_size", "tech_card_product", "tech_card_media",
			"tech_card_callout", "tech_card_detail",
			"tech_card_construction", "tech_card_operation", "tech_card_label",
			"tech_card_packaging", "tech_card_costing", "tech_card_issue", "tech_card_signoff",
			"tech_card_size_pattern",
		} {
			if err := storeutil.ExecNamed(ctx, rep.DB(),
				fmt.Sprintf(`DELETE FROM %s WHERE tech_card_id = :id`, table),
				map[string]any{"id": id}); err != nil {
				return fmt.Errorf("failed to clear %s: %w", table, err)
			}
		}
		if err := insertTechCardChildren(ctx, rep.DB(), id, tc); err != nil {
			return err
		}
		// Re-mint SKUs for the style's products (a style SKU-fact change re-mints unfrozen siblings).
		if err := remintCardProducts(ctx, rep.DB(), id, prevProductLinks); err != nil {
			return err
		}
		// Q1: stamp the auto-journal — an approve/release transition is recorded as such, else `updated`.
		action, section, summary := revisionActionForUpdate(entity.TechCardApprovalState(cur.ApprovalState), tc.ApprovalState)
		return appendTechCardRevision(ctx, rep.DB(), id, tc.UpdatedBy, section, action, summary)
	})
	if err != nil {
		switch err {
		case sql.ErrNoRows, entity.ErrTechCardConflict, entity.ErrTechCardReleased:
			return err
		}
		return fmt.Errorf("can't update tech card: %w", err)
	}
	return nil
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
	// An ARCHIVED colourway (soft-deleted; product.lifecycle_status = 4) is retired work, not a live
	// downstream artifact — it must NOT pin the style's stage. Excluding it lets a style whose only
	// colourways are archived regress its stage. sample/tech_card_release have no soft-delete/archived
	// state, so their counts stay unfiltered.
	counts, err := storeutil.QueryNamedOne[struct {
		Samples   int `db:"samples"`
		Releases  int `db:"releases"`
		Colorways int `db:"colorways"`
	}](ctx, db, `SELECT
		(SELECT COUNT(*) FROM sample WHERE tech_card_id = :id)            AS samples,
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
// orphan their issued-material cost, bypassing DeleteSample's ErrSampleHasMovements guard (NF-04). It
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
	return s.txFunc(ctx, func(ctx context.Context, rep dependency.Repository) error {
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
		if err := storeutil.ExecNamed(ctx, rep.DB(),
			`DELETE FROM tech_card WHERE id = :id`, map[string]any{"id": id}); err != nil {
			var me *mysql.MySQLError
			if errors.As(err, &me) && me.Number == 1451 { // ER_ROW_IS_REFERENCED_2: an un-enumerated RESTRICT
				return entity.NewFieldViolation("tech_card_id",
					"still referenced by another record", "", "remove the referencing record first")
			}
			return fmt.Errorf("failed to delete tech card: %w", err)
		}
		return nil
	})
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
	return &cards[0], nil
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
	return cards, total, nil
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
	// production (Phase 3)
	if err := insertTechCardConstruction(ctx, db, id, tc.Construction); err != nil {
		return err
	}
	if err := insertTechCardOperations(ctx, db, id, tc.Operations, bomRes); err != nil {
		return err
	}
	if err := insertTechCardLabels(ctx, db, id, tc.Labels); err != nil {
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
	return insertTechCardPatterns(ctx, db, id, tc.Patterns)
}

func insertTechCardPatterns(ctx context.Context, db dependency.DB, id int, patterns []entity.TechCardSizePattern) error {
	if len(patterns) == 0 {
		return nil
	}
	rows := make([]map[string]any, 0, len(patterns))
	for i, p := range patterns {
		rows = append(rows, map[string]any{
			"tech_card_id":  id,
			"size_id":       p.SizeId,
			"url":           p.URL,
			"filename":      p.Filename,
			"size_bytes":    p.SizeBytes,
			"display_order": i,
		})
	}
	if err := storeutil.BulkInsert(ctx, db, "tech_card_size_pattern", rows); err != nil {
		return fmt.Errorf("failed to insert tech card patterns: %w", err)
	}
	return nil
}

// validateTechCardSizeIDs enforces S10/WS5's server-side size-write guard on the style's OWN size
// range (tech_card_size, "size_ids" on the wire): each requested size must belong to a system
// permitted for the card's CURRENT category (top/sub/type_id, owned solely by UpdateStyle -- see
// product/style.go -- so it is read fresh from the row here rather than trusted from tc, which never
// carries a category on this write path). Returns a field-tagged *entity.ValidationError naming the
// first offending size ("size_ids[i]") for the caller to surface as InvalidArgument. An id the
// dictionary cache does not recognise is skipped here -- the existing FK on tech_card_size.size_id
// already turns that into a clear foreign-key error at insert time.
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
			continue
		}
		if verr := entity.ValidateSizeAgainstCategory(fmt.Sprintf("size_ids[%d]", i), path, label, rules, sz); verr != nil {
			return verr
		}
	}
	return nil
}

// loadTechCardCategoryPath reads a tech card's CURRENT category triple. category (top/sub/type_id) is
// written exclusively by UpdateStyle (R4/§14.7), never by Add/UpdateTechCard, so this always reflects
// the latest assigned category regardless of which RPC is mid-flight in the same transaction.
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

func insertTechCardProducts(ctx context.Context, db dependency.DB, id int, productIDs []int) error {
	if len(productIDs) == 0 {
		return nil
	}
	rows := make([]map[string]any, 0, len(productIDs))
	for i, pid := range productIDs {
		rows = append(rows, map[string]any{"tech_card_id": id, "product_id": pid, "display_order": i})
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
