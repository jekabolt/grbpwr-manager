package techcard

import (
	"context"
	"fmt"

	"github.com/jekabolt/grbpwr-manager/internal/dependency"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/jekabolt/grbpwr-manager/internal/store/storeutil"
)

// --- inserts (called within the AddTechCard / UpdateTechCard transaction) ---

func insertTechCardConstruction(ctx context.Context, db dependency.DB, tcID int, c *entity.TechCardConstruction) error {
	if c == nil {
		return nil
	}
	if err := storeutil.ExecNamed(ctx, db, `
		INSERT INTO tech_card_construction
			(tech_card_id, main_stitch_type, stitch_density, overlock_threads, seam_allowances,
			 hem_finish, pressing, machine_class, notes, labour_rate, labour_rate_currency)
		VALUES (:tech_card_id, :main_stitch_type, :stitch_density, :overlock_threads, :seam_allowances,
			 :hem_finish, :pressing, :machine_class, :notes, :labour_rate, :labour_rate_currency)`,
		map[string]any{
			"tech_card_id":         tcID,
			"main_stitch_type":     c.MainStitchType,
			"stitch_density":       c.StitchDensity,
			"overlock_threads":     c.OverlockThreads,
			"seam_allowances":      c.SeamAllowances,
			"hem_finish":           c.HemFinish,
			"pressing":             c.Pressing,
			"machine_class":        c.MachineClass,
			"notes":                c.Notes,
			"labour_rate":          c.LabourRate,
			"labour_rate_currency": c.LabourRateCurrency,
		}); err != nil {
		return fmt.Errorf("failed to insert tech card construction: %w", err)
	}
	return nil
}

func insertTechCardOperations(ctx context.Context, db dependency.DB, tcID int, ops []entity.TechCardOperation) error {
	if len(ops) == 0 {
		return nil
	}
	rows := make([]map[string]any, 0, len(ops))
	for i, o := range ops {
		rows = append(rows, map[string]any{
			"tech_card_id":     tcID,
			"operation_number": o.OperationNumber,
			"node":             o.Node,
			"description":      o.Description,
			"seam_type":        o.SeamType,
			"machine":          o.Machine,
			"stitches_per_cm":  o.StitchesPerCm,
			"topstitch_width":  o.TopstitchWidth,
			"seam_allowance":   o.SeamAllowance,
			"thread":           o.Thread,
			"needle":           o.Needle,
			"time_norm":        o.TimeNorm,
			"note":             o.Note,
			"display_order":    i,
		})
	}
	if err := storeutil.BulkInsert(ctx, db, "tech_card_operation", rows); err != nil {
		return fmt.Errorf("failed to insert tech card operations: %w", err)
	}
	return nil
}

func insertTechCardLabels(ctx context.Context, db dependency.DB, tcID int, labels []entity.TechCardLabel) error {
	if len(labels) == 0 {
		return nil
	}
	rows := make([]map[string]any, 0, len(labels))
	for i, l := range labels {
		rows = append(rows, map[string]any{
			"tech_card_id":  tcID,
			"label_type":    string(l.LabelType),
			"content":       l.Content,
			"placement":     l.Placement,
			"attachment":    l.Attachment,
			"size":          l.Size,
			"note":          l.Note,
			"display_order": i,
		})
	}
	if err := storeutil.BulkInsert(ctx, db, "tech_card_label", rows); err != nil {
		return fmt.Errorf("failed to insert tech card labels: %w", err)
	}
	return nil
}

func insertTechCardPackaging(ctx context.Context, db dependency.DB, tcID int, p *entity.TechCardPackaging) error {
	if p == nil {
		return nil
	}
	if err := storeutil.ExecNamed(ctx, db, `
		INSERT INTO tech_card_packaging
			(tech_card_id, folding_method, polybag, bag_sticker, inserts, units_per_box,
			 box_marking, box_dimensions, weight_net, weight_gross, notes)
		VALUES (:tech_card_id, :folding_method, :polybag, :bag_sticker, :inserts, :units_per_box,
			 :box_marking, :box_dimensions, :weight_net, :weight_gross, :notes)`,
		map[string]any{
			"tech_card_id":   tcID,
			"folding_method": p.FoldingMethod,
			"polybag":        p.Polybag,
			"bag_sticker":    p.BagSticker,
			"inserts":        p.Inserts,
			"units_per_box":  p.UnitsPerBox,
			"box_marking":    p.BoxMarking,
			"box_dimensions": p.BoxDimensions,
			"weight_net":     p.WeightNet,
			"weight_gross":   p.WeightGross,
			"notes":          p.Notes,
		}); err != nil {
		return fmt.Errorf("failed to insert tech card packaging: %w", err)
	}
	return nil
}

func insertTechCardCosting(ctx context.Context, db dependency.DB, tcID int, c *entity.TechCardCosting) error {
	if c == nil {
		return nil
	}
	if err := storeutil.ExecNamed(ctx, db, `
		INSERT INTO tech_card_costing
			(tech_card_id, cmt_cost, hardware_cost, packaging_cost, logistics_cost, overhead_cost,
			 defect_percent, markup_multiplier, wholesale_price, retail_price, currency, notes)
		VALUES (:tech_card_id, :cmt_cost, :hardware_cost, :packaging_cost, :logistics_cost, :overhead_cost,
			 :defect_percent, :markup_multiplier, :wholesale_price, :retail_price, :currency, :notes)`,
		map[string]any{
			"tech_card_id":      tcID,
			"cmt_cost":          c.CmtCost,
			"hardware_cost":     c.HardwareCost,
			"packaging_cost":    c.PackagingCost,
			"logistics_cost":    c.LogisticsCost,
			"overhead_cost":     c.OverheadCost,
			"defect_percent":    c.DefectPercent,
			"markup_multiplier": c.MarkupMultiplier,
			"wholesale_price":   c.WholesalePrice,
			"retail_price":      c.RetailPrice,
			"currency":          c.Currency,
			"notes":             c.Notes,
		}); err != nil {
		return fmt.Errorf("failed to insert tech card costing: %w", err)
	}
	return nil
}

func insertTechCardIssues(ctx context.Context, db dependency.DB, tcID int, issues []entity.TechCardIssue) error {
	if len(issues) == 0 {
		return nil
	}
	rows := make([]map[string]any, 0, len(issues))
	for i, is := range issues {
		rows = append(rows, map[string]any{
			"tech_card_id":     tcID,
			"operation_number": is.OperationNumber,
			"callout_number":   is.CalloutNumber,
			"raised_by":        is.RaisedBy,
			"severity":         string(is.Severity),
			"status":           string(is.Status),
			"description":      is.Description,
			"resolution_note":  is.ResolutionNote,
			"display_order":    i,
		})
	}
	if err := storeutil.BulkInsert(ctx, db, "tech_card_issue", rows); err != nil {
		return fmt.Errorf("failed to insert tech card issues: %w", err)
	}
	return nil
}

// --- enrich (load production sections for read paths) ---

type techCardConstructionRow struct {
	TechCardID int `db:"tech_card_id"`
	entity.TechCardConstruction
}

type techCardIssueRow struct {
	TechCardID int `db:"tech_card_id"`
	entity.TechCardIssue
}

type techCardOperationRow struct {
	TechCardID int `db:"tech_card_id"`
	entity.TechCardOperation
}

type techCardLabelRow struct {
	TechCardID int `db:"tech_card_id"`
	entity.TechCardLabel
}

type techCardPackagingRow struct {
	TechCardID int `db:"tech_card_id"`
	entity.TechCardPackaging
}

type techCardCostingRow struct {
	TechCardID int `db:"tech_card_id"`
	entity.TechCardCosting
}

// enrichProduction loads the construction, operations, labels, packaging and
// costing sections for each card and attaches them.
func (s *Store) enrichProduction(ctx context.Context, cards []entity.TechCard) error {
	if len(cards) == 0 {
		return nil
	}
	ids := make([]int, 0, len(cards))
	for i := range cards {
		ids = append(ids, cards[i].Id)
	}

	consRows, err := storeutil.QueryListNamed[techCardConstructionRow](ctx, s.DB,
		`SELECT * FROM tech_card_construction WHERE tech_card_id IN (:ids)`, map[string]any{"ids": ids})
	if err != nil {
		return fmt.Errorf("can't load tech card construction: %w", err)
	}
	consByCard := make(map[int]*entity.TechCardConstruction, len(consRows))
	for i := range consRows {
		c := consRows[i].TechCardConstruction
		consByCard[consRows[i].TechCardID] = &c
	}

	opRows, err := storeutil.QueryListNamed[techCardOperationRow](ctx, s.DB, `
		SELECT tech_card_id, operation_number, node, description, seam_type, machine, stitches_per_cm,
		       topstitch_width, seam_allowance, thread, needle, time_norm, note
		FROM tech_card_operation
		WHERE tech_card_id IN (:ids)
		ORDER BY tech_card_id, display_order`, map[string]any{"ids": ids})
	if err != nil {
		return fmt.Errorf("can't load tech card operations: %w", err)
	}
	opsByCard := make(map[int][]entity.TechCardOperation, len(ids))
	for _, r := range opRows {
		opsByCard[r.TechCardID] = append(opsByCard[r.TechCardID], r.TechCardOperation)
	}

	labelRows, err := storeutil.QueryListNamed[techCardLabelRow](ctx, s.DB, `
		SELECT tech_card_id, label_type, content, placement, attachment, size, note
		FROM tech_card_label
		WHERE tech_card_id IN (:ids)
		ORDER BY tech_card_id, display_order`, map[string]any{"ids": ids})
	if err != nil {
		return fmt.Errorf("can't load tech card labels: %w", err)
	}
	labelsByCard := make(map[int][]entity.TechCardLabel, len(ids))
	for _, r := range labelRows {
		labelsByCard[r.TechCardID] = append(labelsByCard[r.TechCardID], r.TechCardLabel)
	}

	pkgRows, err := storeutil.QueryListNamed[techCardPackagingRow](ctx, s.DB,
		`SELECT * FROM tech_card_packaging WHERE tech_card_id IN (:ids)`, map[string]any{"ids": ids})
	if err != nil {
		return fmt.Errorf("can't load tech card packaging: %w", err)
	}
	pkgByCard := make(map[int]*entity.TechCardPackaging, len(pkgRows))
	for i := range pkgRows {
		p := pkgRows[i].TechCardPackaging
		pkgByCard[pkgRows[i].TechCardID] = &p
	}

	costRows, err := storeutil.QueryListNamed[techCardCostingRow](ctx, s.DB,
		`SELECT * FROM tech_card_costing WHERE tech_card_id IN (:ids)`, map[string]any{"ids": ids})
	if err != nil {
		return fmt.Errorf("can't load tech card costing: %w", err)
	}
	costByCard := make(map[int]*entity.TechCardCosting, len(costRows))
	for i := range costRows {
		c := costRows[i].TechCardCosting
		costByCard[costRows[i].TechCardID] = &c
	}

	issueRows, err := storeutil.QueryListNamed[techCardIssueRow](ctx, s.DB, `
		SELECT tech_card_id, operation_number, callout_number, raised_by, severity, status, description, resolution_note
		FROM tech_card_issue
		WHERE tech_card_id IN (:ids)
		ORDER BY tech_card_id, display_order`, map[string]any{"ids": ids})
	if err != nil {
		return fmt.Errorf("can't load tech card issues: %w", err)
	}
	issuesByCard := make(map[int][]entity.TechCardIssue, len(ids))
	for _, r := range issueRows {
		issuesByCard[r.TechCardID] = append(issuesByCard[r.TechCardID], r.TechCardIssue)
	}

	for i := range cards {
		id := cards[i].Id
		cards[i].Construction = consByCard[id]
		cards[i].Operations = opsByCard[id]
		cards[i].Labels = labelsByCard[id]
		cards[i].Packaging = pkgByCard[id]
		cards[i].Costing = costByCard[id]
		cards[i].Issues = issuesByCard[id]
	}
	return nil
}
