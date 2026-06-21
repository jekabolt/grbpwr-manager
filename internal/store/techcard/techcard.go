// Package techcard implements garment tech pack (техкарта) management: the header,
// size range, linked products, sketch media, callouts and revision log.
package techcard

import (
	"context"
	"database/sql"
	"fmt"
	"strings"

	"github.com/jekabolt/grbpwr-manager/internal/dependency"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
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

// header columns shared by INSERT (AddTechCard) and UPDATE (UpdateTechCard).
const techCardHeaderColumns = `style_number, name, brand, season, collection, category_id,
	target_gender, stage, status, approval_state, approved_by, approved_at, released_at, version, revision_date,
	base_model_id, base_sample_size_id, designer, constructor, technologist,
	target_cost, target_retail_price, currency, measurement_unit,
	description, concept, silhouette, fastening, pockets, sleeve_cuff, extra_details, collar,
	topstitching, aux_materials, notes`

const techCardHeaderValues = `:style_number, :name, :brand, :season, :collection, :category_id,
	:target_gender, :stage, :status, :approval_state, :approved_by, :approved_at, :released_at, :version, :revision_date,
	:base_model_id, :base_sample_size_id, :designer, :constructor, :technologist,
	:target_cost, :target_retail_price, :currency, :measurement_unit,
	:description, :concept, :silhouette, :fastening, :pockets, :sleeve_cuff, :extra_details, :collar,
	:topstitching, :aux_materials, :notes`

func techCardHeaderParams(tc *entity.TechCardInsert) map[string]any {
	return map[string]any{
		"style_number":        tc.StyleNumber,
		"name":                tc.Name,
		"brand":               tc.Brand,
		"season":              tc.Season,
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
		"target_cost":         tc.TargetCost,
		"target_retail_price": tc.TargetRetailPrice,
		"currency":            tc.Currency,
		"measurement_unit":    string(tc.MeasurementUnit),
		"description":         tc.Description,
		"silhouette":          tc.Silhouette,
		"collar":              tc.Collar,
		"fastening":           tc.Fastening,
		"pockets":             tc.Pockets,
		"sleeve_cuff":         tc.SleeveCuff,
		"extra_details":       tc.ExtraDetails,
		"topstitching":        tc.Topstitching,
		"aux_materials":       tc.AuxMaterials,
		"notes":               tc.Notes,
		"concept":             tc.Concept,
	}
}

// stampApprovalTimes auto-sets approved_at/released_at when the card enters the
// corresponding state without the timestamp already provided.
func (s *Store) stampApprovalTimes(tc *entity.TechCardInsert) {
	approvedOrReleased := tc.ApprovalState == entity.TechCardApprovalApproved ||
		tc.ApprovalState == entity.TechCardApprovalReleased
	if approvedOrReleased && !tc.ApprovedAt.Valid {
		tc.ApprovedAt = sql.NullTime{Time: s.Now(), Valid: true}
	}
	if tc.ApprovalState == entity.TechCardApprovalReleased && !tc.ReleasedAt.Valid {
		tc.ReleasedAt = sql.NullTime{Time: s.Now(), Valid: true}
	}
}

// AddTechCard inserts a tech card and its child sections, returning the new id.
func (s *Store) AddTechCard(ctx context.Context, tc *entity.TechCardInsert) (int, error) {
	s.stampApprovalTimes(tc)
	var id int
	err := s.txFunc(ctx, func(ctx context.Context, rep dependency.Repository) error {
		var err error
		id, err = storeutil.ExecNamedLastId(ctx, rep.DB(),
			fmt.Sprintf(`INSERT INTO tech_card (%s) VALUES (%s)`, techCardHeaderColumns, techCardHeaderValues),
			techCardHeaderParams(tc))
		if err != nil {
			return fmt.Errorf("failed to insert tech card: %w", err)
		}
		return insertTechCardChildren(ctx, rep.DB(), id, tc)
	})
	if err != nil {
		return 0, fmt.Errorf("can't add tech card: %w", err)
	}
	return id, nil
}

// UpdateTechCard updates a tech card and replaces its child sections. It is
// optimistically locked on expectedLockVersion (entity.ErrTechCardConflict on a
// mismatch), refuses to mutate a RELEASED card unless it is re-opened to DRAFT
// (entity.ErrTechCardReleased), and returns sql.ErrNoRows when no card exists.
func (s *Store) UpdateTechCard(ctx context.Context, id int, tc *entity.TechCardInsert, expectedLockVersion int) error {
	s.stampApprovalTimes(tc)
	err := s.txFunc(ctx, func(ctx context.Context, rep dependency.Repository) error {
		cur, err := storeutil.QueryNamedOne[struct {
			LockVersion   int    `db:"lock_version"`
			ApprovalState string `db:"approval_state"`
		}](ctx, rep.DB(),
			`SELECT lock_version, approval_state FROM tech_card WHERE id = :id`, map[string]any{"id": id})
		if err != nil {
			if err == sql.ErrNoRows {
				return sql.ErrNoRows
			}
			return fmt.Errorf("failed to load tech card for update: %w", err)
		}
		if cur.LockVersion != expectedLockVersion {
			return entity.ErrTechCardConflict
		}
		// A released card is frozen for the factory: only a re-open to draft is
		// allowed; any other edit while still released is rejected.
		if cur.ApprovalState == string(entity.TechCardApprovalReleased) &&
			tc.ApprovalState != entity.TechCardApprovalDraft {
			return entity.ErrTechCardReleased
		}

		params := techCardHeaderParams(tc)
		params["id"] = id
		params["expected_lock_version"] = expectedLockVersion
		if err := storeutil.ExecNamed(ctx, rep.DB(), `
			UPDATE tech_card SET
				lock_version = lock_version + 1,
				style_number = :style_number, name = :name, brand = :brand, season = :season,
				collection = :collection, category_id = :category_id, target_gender = :target_gender,
				stage = :stage, status = :status, approval_state = :approval_state,
				approved_by = :approved_by, approved_at = :approved_at, released_at = :released_at,
				version = :version, revision_date = :revision_date,
				base_model_id = :base_model_id, base_sample_size_id = :base_sample_size_id,
				designer = :designer, constructor = :constructor, technologist = :technologist,
				target_cost = :target_cost, target_retail_price = :target_retail_price, currency = :currency,
				measurement_unit = :measurement_unit,
				description = :description, concept = :concept, silhouette = :silhouette, collar = :collar,
				fastening = :fastening, pockets = :pockets, sleeve_cuff = :sleeve_cuff, extra_details = :extra_details,
				topstitching = :topstitching, aux_materials = :aux_materials, notes = :notes
			WHERE id = :id AND lock_version = :expected_lock_version`, params); err != nil {
			return fmt.Errorf("failed to update tech card: %w", err)
		}

		// Delete tech_card_bom_item before tech_card_colorway so the bom-colourway
		// matrix cascades out via the BOM line; pom grades/actuals and bom-colourway
		// cells cascade from their parents.
		for _, table := range []string{
			"tech_card_size", "tech_card_product", "tech_card_media",
			"tech_card_callout", "tech_card_revision",
			"tech_card_bom_item", "tech_card_colorway", "tech_card_pom_point",
			"tech_card_construction", "tech_card_operation", "tech_card_label",
			"tech_card_packaging", "tech_card_costing", "tech_card_issue",
		} {
			if err := storeutil.ExecNamed(ctx, rep.DB(),
				fmt.Sprintf(`DELETE FROM %s WHERE tech_card_id = :id`, table),
				map[string]any{"id": id}); err != nil {
				return fmt.Errorf("failed to clear %s: %w", table, err)
			}
		}
		return insertTechCardChildren(ctx, rep.DB(), id, tc)
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

// DeleteTechCard deletes a tech card by id (child sections cascade).
func (s *Store) DeleteTechCard(ctx context.Context, id int) error {
	if err := storeutil.ExecNamed(ctx, s.DB,
		`DELETE FROM tech_card WHERE id = :id`, map[string]any{"id": id}); err != nil {
		return fmt.Errorf("failed to delete tech card: %w", err)
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
	return cards, total, nil
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
	// Materials (Phase 2): colourways first so the BOM colour matrix can resolve
	// each colorway_index to a freshly-inserted colourway id.
	colorwayIds, err := insertTechCardColorways(ctx, db, id, tc.Colorways)
	if err != nil {
		return err
	}
	if err := insertTechCardBom(ctx, db, id, tc.BomItems, colorwayIds); err != nil {
		return err
	}
	if err := insertTechCardPom(ctx, db, id, tc.PomPoints); err != nil {
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
	return insertTechCardIssues(ctx, db, id, tc.Issues)
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
