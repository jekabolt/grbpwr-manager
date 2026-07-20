package accounting

import (
	"strconv"
	"strings"
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
)

// BuildMaterialMovementEntry builds the journal entry for one material-stock movement (rules M1–M8,
// docs/plan-accounting/04-posting-rules.md). One entry per movement (source_key = movement id) —
// idempotent and readable in drill-down.
//
// V = round2(quantity x unit_cost_base) is the movement's frozen base value. An uncosted movement
// (unit_cost_base NULL) or a zero value posts nothing and returns ErrSkipUncosted — reconciliation
// surfaces it. occurred_at is the movement's occurred_at (falling back to created_at), clamped up
// to the accounting start date; clamping a movement that lands in an already-closed period is the
// worker's job (04/09 FAQ 25), not the builder's.
//
// M8 (adjustment) takes its direction from the on-hand delta (quantity on the row is abs(delta)):
// a positive delta is a stock gain (Dr 1110 / Cr 5090), a negative delta a loss (Dr 5090 / Cr 1110).
func BuildMaterialMovementEntry(m entity.AcctMovementFacts, startDate time.Time) (entity.AcctJournalEntryInsert, error) {
	if !m.UnitCostBase.Valid {
		return entity.AcctJournalEntryInsert{}, ErrSkipUncosted
	}
	v := m.Quantity.Mul(m.UnitCostBase.Decimal).Round(2)
	if !v.IsPositive() {
		return entity.AcctJournalEntryInsert{}, ErrSkipUncosted
	}

	var dr, cr string
	var sourceType entity.AcctSourceType
	switch m.MovementType {
	case entity.MaterialMovementReceipt: // M1: purchase in
		dr, cr, sourceType = Acc1110, Acc2010, entity.AcctSourceMaterialReceipt
	case entity.MaterialMovementReceiptProduction: // M2: our auxiliary run lands in stock
		dr, cr, sourceType = Acc1110, Acc1120, entity.AcctSourceMaterialReceipt
	case entity.MaterialMovementIssueProduction: // M3: issued into a run
		dr, cr, sourceType = Acc1120, Acc1110, entity.AcctSourceMaterialIssue
	case entity.MaterialMovementIssueSample: // M4: issued to a sample
		dr, cr, sourceType = Acc6210, Acc1110, entity.AcctSourceMaterialIssue
	case entity.MaterialMovementReturnProduction: // M5: unused remainder back from a run
		dr, cr, sourceType = Acc1110, Acc1120, entity.AcctSourceMaterialReturn
	case entity.MaterialMovementReturnSample: // M6: returned from a sample
		dr, cr, sourceType = Acc1110, Acc6210, entity.AcctSourceMaterialReturn
	case entity.MaterialMovementWriteoff: // M7: damage / loss / defect
		dr, cr, sourceType = Acc5040, Acc1110, entity.AcctSourceMaterialWriteoff
	case entity.MaterialMovementAdjustment: // M8: stock count — direction from the delta
		delta := m.OnHandAfter.Sub(m.OnHandBefore)
		if delta.IsZero() {
			return entity.AcctJournalEntryInsert{}, ErrSkipUncosted
		}
		if delta.IsPositive() {
			dr, cr = Acc1110, Acc5090
		} else {
			dr, cr = Acc5090, Acc1110
		}
		sourceType = entity.AcctSourceMaterialAdjustment
	default:
		return entity.AcctJournalEntryInsert{}, ErrUnknownMovementType
	}

	occ := m.CreatedAt
	if m.OccurredAt.Valid {
		occ = m.OccurredAt.Time
	}
	if occ.Before(startDate) {
		occ = startDate
	}

	return entity.AcctJournalEntryInsert{
		OccurredAt:  occ,
		Description: movementDescription(m),
		SourceType:  sourceType,
		SourceKey:   strconv.Itoa(m.Id),
		CreatedBy:   createdBySystem,
		Lines: []entity.AcctJournalLineInsert{
			{AccountCode: dr, Side: entity.AcctSideDebit, Amount: v},
			{AccountCode: cr, Side: entity.AcctSideCredit, Amount: v},
		},
	}, nil
}

// movementDescription is the material name joined with the movement's reason / comment, bounded to
// the description column width.
func movementDescription(m entity.AcctMovementFacts) string {
	parts := []string{m.MaterialName}
	if m.Reason.Valid && strings.TrimSpace(m.Reason.String) != "" {
		parts = append(parts, strings.TrimSpace(m.Reason.String))
	}
	if m.Comment.Valid && strings.TrimSpace(m.Comment.String) != "" {
		parts = append(parts, strings.TrimSpace(m.Comment.String))
	}
	return truncateRunes(strings.Join(parts, " — "), descMaxLen)
}
