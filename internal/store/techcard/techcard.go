// Package techcard implements garment tech pack (техкарта) management: the header,
// size range, linked products, sketch media, callouts and revision log.
package techcard

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"strings"

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
// season_code/season_year are the normalized SKU-facing season (task 05), derived from the free-text
// `season` in techCardHeaderParams. The legacy free-text `season` is kept (UNIQUE key + filters).
const techCardHeaderColumns = `style_number, name, brand, season, season_code, season_year, collection, category_id,
	target_gender, stage, status, approval_state, approved_by, approved_at, released_at, version, revision_date,
	base_model_id, base_sample_size_id, designer, constructor, technologist,
	measurement_unit, concept, notes, purpose, output_material_id`

const techCardHeaderValues = `:style_number, :name, :brand, :season, :season_code, :season_year, :collection, :category_id,
	:target_gender, :stage, :status, :approval_state, :approved_by, :approved_at, :released_at, :version, :revision_date,
	:base_model_id, :base_sample_size_id, :designer, :constructor, :technologist,
	:measurement_unit, :concept, :notes, :purpose, :output_material_id`

func techCardHeaderParams(tc *entity.TechCardInsert) map[string]any {
	// Default an unset purpose to sellable so a direct entity insert (not via dto) satisfies the
	// chk_tech_card_purpose CHECK — the dto already defaults it, this covers store-level callers.
	purpose := tc.Purpose
	if purpose == "" {
		purpose = entity.TechCardPurposeSellable
	}
	// Derive the normalized SKU-facing season (task 05) from the free-text season. Unparseable
	// values ("-", "", junk) leave season_code/season_year NULL — the generator then falls back.
	var seasonCode sql.NullString
	var seasonYear sql.NullInt32
	if tc.Season.Valid {
		if code, year, ok := entity.ParseSeasonText(tc.Season.String); ok {
			seasonCode = sql.NullString{String: string(code), Valid: true}
			seasonYear = sql.NullInt32{Int32: int32(year), Valid: true}
		}
	}
	return map[string]any{
		"style_number":        tc.StyleNumber,
		"purpose":             string(purpose),
		"output_material_id":  tc.OutputMaterialId,
		"name":                tc.Name,
		"brand":               tc.Brand,
		"season":              tc.Season,
		"season_code":         seasonCode,
		"season_year":         seasonYear,
		"collection":          tc.Collection,
		"category_id":         tc.CategoryId,
		"target_gender":       tc.TargetGender,
		"stage":               string(tc.Stage),
		"status":              tc.Status,
		"approval_state":      string(tc.ApprovalState),
		"approved_by":         tc.ApprovedBy,
		"approved_at":         tc.ApprovedAt,
		"released_at":         tc.ReleasedAt,
		"version":             tc.Version,
		"revision_date":       tc.RevisionDate,
		"base_model_id":       tc.BaseModelId,
		"base_sample_size_id": tc.BaseSampleSizeId,
		"designer":            tc.Designer,
		"constructor":         tc.Constructor,
		"technologist":        tc.Technologist,
		"measurement_unit":    string(tc.MeasurementUnit),
		"concept":             tc.Concept,
		"notes":               tc.Notes,
	}
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
		var err error
		id, err = storeutil.ExecNamedLastId(ctx, rep.DB(),
			fmt.Sprintf(`INSERT INTO tech_card (%s) VALUES (%s)`, techCardHeaderColumns, techCardHeaderValues),
			techCardHeaderParams(tc))
		if err != nil {
			return fmt.Errorf("failed to insert tech card: %w", err)
		}
		if err := insertTechCardChildren(ctx, rep.DB(), id, tc); err != nil {
			return err
		}
		// A new card's colourways may already link products (they become "styled" and take the
		// style's season/model + colourway colour) — re-mint their SKUs while unlocked.
		return remintCardProducts(ctx, rep.DB(), id, nil)
	})
	if err != nil {
		return 0, fmt.Errorf("can't add tech card: %w", err)
	}
	return id, nil
}

// captureCardProductLinks returns the product ids currently linked via this card's colourways.
func captureCardProductLinks(ctx context.Context, db dependency.DB, tcID int) ([]int, error) {
	rows, err := storeutil.QueryListNamed[struct {
		ProductID int `db:"product_id"`
	}](ctx, db, `SELECT product_id FROM tech_card_colorway WHERE tech_card_id = :id AND product_id IS NOT NULL`,
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
		}](ctx, rep.DB(),
			`SELECT lock_version, approval_state, approved_at, released_at, purpose FROM tech_card WHERE id = :id`, map[string]any{"id": id})
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
		// Server owns the lifecycle stamps (set on enter, cleared on re-open).
		s.stampApprovalTimes(tc, entity.TechCardApprovalState(cur.ApprovalState), cur.ApprovedAt, cur.ReleasedAt)

		params := techCardHeaderParams(tc)
		params["id"] = id
		params["expected_lock_version"] = expectedLockVersion
		rows, err := storeutil.ExecNamedRows(ctx, rep.DB(), `
			UPDATE tech_card SET
				lock_version = lock_version + 1,
				style_number = :style_number, name = :name, brand = :brand, season = :season,
				collection = :collection, category_id = :category_id, target_gender = :target_gender,
				stage = :stage, status = :status, approval_state = :approval_state,
				approved_by = :approved_by, approved_at = :approved_at, released_at = :released_at,
				version = :version, revision_date = :revision_date,
				base_model_id = :base_model_id, base_sample_size_id = :base_sample_size_id,
				designer = :designer, constructor = :constructor, technologist = :technologist,
				measurement_unit = :measurement_unit, concept = :concept, notes = :notes,
					purpose = :purpose, output_material_id = :output_material_id
			WHERE id = :id AND lock_version = :expected_lock_version`, params)
		if err != nil {
			return fmt.Errorf("failed to update tech card: %w", err)
		}
		// The row provably exists (loaded above), so 0 rows means lock_version moved
		// under us — make the WHERE guard load-bearing, not just the in-Go check.
		if rows == 0 {
			return entity.ErrTechCardConflict
		}

		// Samples reference colourways by FK with ON DELETE SET NULL, and the full-replace below deletes
		// and re-inserts colourways with fresh ids — which would silently null every sample's colour-
		// model link. Capture the links by colourway identity now so they can be restored afterwards.
		sampleColorwayLinks, err := captureSampleColorwayLinks(ctx, rep.DB(), id)
		if err != nil {
			return err
		}

		// Capture product links before the full-replace so products that get UNLINKED by this save
		// are still re-minted (reverting to a standalone SKU) alongside the newly-linked ones.
		prevProductLinks, err := captureCardProductLinks(ctx, rep.DB(), id)
		if err != nil {
			return err
		}

		// Full-replace: clear all child rows by tech_card_id. Grandchildren cascade
		// from their parents — colourway usages (+ their per-size consumption) via
		// tech_card_colorway, and detail media via tech_card_detail.
		for _, table := range []string{
			"tech_card_size", "tech_card_product", "tech_card_media",
			"tech_card_callout", "tech_card_revision", "tech_card_detail",
			"tech_card_bom_item", "tech_card_colorway",
			"tech_card_construction", "tech_card_operation", "tech_card_label",
			"tech_card_packaging", "tech_card_costing", "tech_card_issue", "tech_card_signoff",
			"tech_card_size_pattern", "tech_card_piece",
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
		if err := relinkSamplesToColorways(ctx, rep.DB(), id, sampleColorwayLinks); err != nil {
			return err
		}
		// Re-mint SKUs for products linked now and those unlinked by this save (revert to standalone).
		return remintCardProducts(ctx, rep.DB(), id, prevProductLinks)
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

// DeleteTechCard deletes a tech card by id (child sections cascade). It refuses when any of the
// card's samples has material stock movements: the sample rows cascade (ON DELETE CASCADE) and would
// orphan their issued-material cost, bypassing DeleteSample's ErrSampleHasMovements guard (NF-04).
// The check and the delete run in one transaction so a concurrent issue cannot slip between them.
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
		if err := storeutil.ExecNamed(ctx, rep.DB(),
			`DELETE FROM tech_card WHERE id = :id`, map[string]any{"id": id}); err != nil {
			return fmt.Errorf("failed to delete tech card: %w", err)
		}
		return nil
	})
}

// colorwayIdentityKey builds a stable key for a colourway across a full-replace (case-folded code +
// name), so a sample's colour-model link can be matched to the freshly-reinserted colourway.
func colorwayIdentityKey(code sql.NullString, name string) string {
	return strings.ToLower(strings.TrimSpace(code.String)) + "\x00" + strings.ToLower(strings.TrimSpace(name))
}

// captureSampleColorwayLinks returns sample_id → colourway identity key for a card's samples that
// currently reference a colourway (empty when none), so UpdateTechCard's full-replace of colourways
// does not silently drop the sample→colour-model link.
func captureSampleColorwayLinks(ctx context.Context, db dependency.DB, tcID int) (map[int]string, error) {
	rows, err := storeutil.QueryListNamed[struct {
		SampleId int            `db:"sample_id"`
		Code     sql.NullString `db:"code"`
		Name     string         `db:"name"`
	}](ctx, db, `
		SELECT s.id AS sample_id, c.code AS code, c.name AS name
		FROM sample s JOIN tech_card_colorway c ON c.id = s.colorway_id
		WHERE s.tech_card_id = :id`, map[string]any{"id": tcID})
	if err != nil {
		return nil, fmt.Errorf("capture sample colorway links: %w", err)
	}
	out := make(map[int]string, len(rows))
	for _, r := range rows {
		out[r.SampleId] = colorwayIdentityKey(r.Code, r.Name)
	}
	return out, nil
}

// relinkSamplesToColorways restores sample→colourway links captured before a full-replace by matching
// each sample's old colourway identity to a freshly-inserted colourway; an unmatched (removed or
// renamed) colourway leaves the link NULL, which is the honest result — that colour no longer exists.
func relinkSamplesToColorways(ctx context.Context, db dependency.DB, tcID int, links map[int]string) error {
	if len(links) == 0 {
		return nil
	}
	rows, err := storeutil.QueryListNamed[struct {
		Id   int            `db:"id"`
		Code sql.NullString `db:"code"`
		Name string         `db:"name"`
	}](ctx, db, `SELECT id, code, name FROM tech_card_colorway WHERE tech_card_id = :id`, map[string]any{"id": tcID})
	if err != nil {
		return fmt.Errorf("load reinserted colorways: %w", err)
	}
	keyToID := make(map[string]int, len(rows))
	for _, r := range rows {
		keyToID[colorwayIdentityKey(r.Code, r.Name)] = r.Id
	}
	for sampleID, key := range links {
		var cw any // NULL when the colourway no longer exists
		if id, ok := keyToID[key]; ok {
			cw = id
		}
		if err := storeutil.ExecNamed(ctx, db,
			`UPDATE sample SET colorway_id = :cw WHERE id = :id`,
			map[string]any{"cw": cw, "id": sampleID}); err != nil {
			return fmt.Errorf("relink sample %d colorway: %w", sampleID, err)
		}
	}
	return nil
}

// GetTechCardById returns a tech card with its child sections and resolved media.
func (s *Store) GetTechCardById(ctx context.Context, id int) (*entity.TechCard, error) {
	tc, err := storeutil.QueryNamedOne[entity.TechCard](ctx, s.DB,
		`SELECT * FROM tech_card WHERE id = :id`, map[string]any{"id": id})
	if err != nil {
		return nil, fmt.Errorf("failed to get tech card: %w", err)
	}
	cards := []entity.TechCard{tc}
	if err := s.enrich(ctx, cards); err != nil {
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
	if filter.Season != "" {
		where += " AND season LIKE :season"
		params["season"] = "%" + escapeLike(filter.Season) + "%"
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
	if err := insertTechCardSizes(ctx, db, id, tc.SizeIds, tc.SizeQuantities); err != nil {
		return err
	}
	if err := insertTechCardProducts(ctx, db, id, tc.ProductIds); err != nil {
		return err
	}
	if err := insertTechCardMedia(ctx, db, id, tc.Media); err != nil {
		return err
	}
	if err := insertTechCardCallouts(ctx, db, id, tc.Callouts); err != nil {
		return err
	}
	if err := insertTechCardRevisions(ctx, db, id, tc.Revisions); err != nil {
		return err
	}
	if err := insertTechCardDetails(ctx, db, id, tc.Details); err != nil {
		return err
	}
	// Materials (Phase 2): colourways (each with its usage recipe) and the BOM article
	// catalog. A usage's bom_item_index points into the BOM by position, so order is
	// not load-bearing between the two inserts.
	if err := insertTechCardColorways(ctx, db, id, tc.Colorways); err != nil {
		return err
	}
	if err := insertTechCardBom(ctx, db, id, tc.BomItems); err != nil {
		return err
	}
	// Cut-pieces (NF-05): must run AFTER colourways so their per-colourway fabric mapping can
	// resolve positional colorway_index → the freshly-inserted colorway_id (same tx).
	if err := insertTechCardPieces(ctx, db, id, tc.Pieces); err != nil {
		return err
	}
	// production (Phase 3)
	if err := insertTechCardConstruction(ctx, db, id, tc.Construction); err != nil {
		return err
	}
	if err := insertTechCardOperations(ctx, db, id, tc.Operations); err != nil {
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

func insertTechCardRevisions(ctx context.Context, db dependency.DB, id int, revisions []entity.TechCardRevision) error {
	if len(revisions) == 0 {
		return nil
	}
	rows := make([]map[string]any, 0, len(revisions))
	for i, r := range revisions {
		rows = append(rows, map[string]any{
			"tech_card_id":  id,
			"version":       r.Version,
			"revision_date": r.RevisionDate,
			"author":        r.Author,
			"section":       r.Section,
			"change_note":   r.ChangeNote,
			"display_order": i,
		})
	}
	if err := storeutil.BulkInsert(ctx, db, "tech_card_revision", rows); err != nil {
		return fmt.Errorf("failed to insert tech card revisions: %w", err)
	}
	return nil
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
