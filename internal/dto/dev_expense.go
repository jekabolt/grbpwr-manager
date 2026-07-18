package dto

import (
	"database/sql"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	pb_common "github.com/jekabolt/grbpwr-manager/proto/gen/common"
	"github.com/shopspring/decimal"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// devExpenseKinds mirrors the tech_card_dev_expense.kind CHECK.
var devExpenseKinds = map[string]bool{
	"sample": true, "materials": true, "labour": true, "outsourcing": true, "other": true,
}

// ConvertPbDevExpenseInsertToEntity validates a development-cost insert and maps it to the entity.
// AmountBase is left unset — the caller folds it via FoldTechCardDevExpenseToBase with the FX.
func ConvertPbDevExpenseInsertToEntity(in *pb_common.TechCardDevExpenseInsert) (entity.TechCardDevExpense, error) {
	if in == nil {
		return entity.TechCardDevExpense{}, fmt.Errorf("dev expense: nil payload")
	}
	if in.TechCardId <= 0 {
		return entity.TechCardDevExpense{}, fmt.Errorf("dev expense: tech_card_id is required")
	}
	kind := strings.ToLower(strings.TrimSpace(in.Kind))
	if !devExpenseKinds[kind] {
		return entity.TechCardDevExpense{}, fmt.Errorf("dev expense: kind must be one of sample|materials|labour|outsourcing|other")
	}
	if len(in.Description) > maxVarchar255 {
		return entity.TechCardDevExpense{}, fmt.Errorf("dev expense: description must be at most %d characters", maxVarchar255)
	}
	amount, err := nullDecimalFromPb(in.Amount)
	if err != nil {
		return entity.TechCardDevExpense{}, fmt.Errorf("dev expense amount: %w", err)
	}
	if !amount.Valid || amount.Decimal.IsNegative() {
		return entity.TechCardDevExpense{}, fmt.Errorf("dev expense: amount must be a non-negative number")
	}
	currency := strings.ToUpper(strings.TrimSpace(in.Currency))
	if !IsExpenseCurrency(currency) {
		return entity.TechCardDevExpense{}, fmt.Errorf("dev expense: currency must be a supported currency or USDT")
	}
	e := entity.TechCardDevExpense{
		TechCardId:  int(in.TechCardId),
		Kind:        kind,
		Description: nullStringFromPb(in.Description),
		Amount:      amount.Decimal,
		Currency:    currency,
		IncurredAt:  nullDateFromPbTimestamp(in.IncurredAt),
	}
	if in.FittingId > 0 {
		e.FittingId = sql.NullInt32{Int32: in.FittingId, Valid: true}
	}
	if in.SampleId > 0 {
		e.SampleId = sql.NullInt32{Int32: in.SampleId, Valid: true}
	}
	return e, nil
}

// FoldTechCardDevExpenseToBase fills AmountBase (when unset) by folding Amount into the base
// currency via the costing FX rates. Left unset when the currency has no rate — the summary then
// reports has_unconverted so a partial total is honest.
func FoldTechCardDevExpenseToBase(e *entity.TechCardDevExpense, fx CostingFx) {
	if e.AmountBase.Valid {
		return
	}
	if base, ok := fx.toBase(e.Amount, e.Currency); ok {
		e.AmountBase = decimal.NullDecimal{Decimal: roundMoney(base), Valid: true}
	}
}

// ConvertEntityDevExpenseToPb maps a stored development-cost row to proto.
func ConvertEntityDevExpenseToPb(e entity.TechCardDevExpense) *pb_common.TechCardDevExpense {
	out := &pb_common.TechCardDevExpense{
		Id:          int32(e.Id),
		TechCardId:  int32(e.TechCardId),
		Kind:        e.Kind,
		Description: pbStringFromNull(e.Description),
		Amount:      pbDecimalFromDecimal(e.Amount),
		Currency:    e.Currency,
		AmountBase:  pbDecimalFromNull(e.AmountBase),
		CreatedAt:   timestamppb.New(e.CreatedAt),
	}
	if e.FittingId.Valid {
		out.FittingId = e.FittingId.Int32
	}
	if e.SampleId.Valid {
		out.SampleId = e.SampleId.Int32
	}
	if e.IncurredAt.Valid {
		out.IncurredAt = timestamppb.New(e.IncurredAt.Time)
	}
	return out
}

// ConvertEntityDevExpensesToPb maps a list of development-cost rows.
func ConvertEntityDevExpensesToPb(list []entity.TechCardDevExpense) []*pb_common.TechCardDevExpense {
	if len(list) == 0 {
		return nil
	}
	out := make([]*pb_common.TechCardDevExpense, len(list))
	for i := range list {
		out[i] = ConvertEntityDevExpenseToPb(list[i])
	}
	return out
}

// ComputeTechCardDevCostSummary rolls up a style's development-cost journal (output-only): the
// total in base currency, a per-kind breakdown, the amortized unit_cost_with_dev = production
// unit cost + dev_total / Σ order_qty, and (Q8) the R&D rollup — spend attributed to each fitting
// round, the rounds-to-approval, and the time-to-approval timeline. The amortized figure is set only
// when the size run and a base-currency production unit cost are both known; development stays a
// PERIOD cost and is never seeded into cost_price, so this is purely informational. has_unconverted
// flags a partial total (some row's currency had no FX rate). `fittings` carries the style's fitting
// rounds — an expense's fitting_id resolves to that fitting's round_number, restoring the S20
// attribution the frontend had dead-coded to 0. (Sample→round attribution is Q7/WS6: TODO-merge to
// also resolve round via sample_id once samples carry round_number; today rounds come from fittings.)
func ComputeTechCardDevCostSummary(card *entity.TechCard, expenses []entity.TechCardDevExpense, fittings []entity.Fitting, fx CostingFx) *pb_common.TechCardDevCostSummary {
	// fitting id → round number (only fittings that carry a round).
	fittingRound := make(map[int32]int32, len(fittings))
	for i := range fittings {
		if fittings[i].RoundNumber.Valid {
			fittingRound[int32(fittings[i].Id)] = fittings[i].RoundNumber.Int32
		}
	}

	totalBase := decimal.Zero
	hasUnconverted := false
	byKind := map[string]decimal.Decimal{}
	kindOrder := make([]string, 0)
	byRound := map[int32]decimal.Decimal{}
	byRoundCount := map[int32]int32{}
	roundOrder := make([]int32, 0)
	var firstExpenseAt time.Time
	haveFirstExpense := false
	for _, e := range expenses {
		// Timeline start: earliest incurred (else created) date, over ALL rows (even unconverted —
		// the spend still happened).
		when := e.CreatedAt
		if e.IncurredAt.Valid {
			when = e.IncurredAt.Time
		}
		if !when.IsZero() && (!haveFirstExpense || when.Before(firstExpenseAt)) {
			firstExpenseAt = when
			haveFirstExpense = true
		}
		if !e.AmountBase.Valid {
			hasUnconverted = true
			continue
		}
		totalBase = totalBase.Add(e.AmountBase.Decimal)
		if _, ok := byKind[e.Kind]; !ok {
			kindOrder = append(kindOrder, e.Kind)
		}
		byKind[e.Kind] = byKind[e.Kind].Add(e.AmountBase.Decimal)

		// Attribute to a fitting round (0 = not tied to a round).
		round := int32(0)
		if e.FittingId.Valid {
			if r, ok := fittingRound[e.FittingId.Int32]; ok {
				round = r
			}
		}
		if _, ok := byRound[round]; !ok {
			roundOrder = append(roundOrder, round)
		}
		byRound[round] = byRound[round].Add(e.AmountBase.Decimal)
		byRoundCount[round]++
	}

	out := &pb_common.TechCardDevCostSummary{
		TotalBase:      pbDecimalFromDecimal(roundMoney(totalBase)),
		HasUnconverted: hasUnconverted,
	}
	for _, k := range kindOrder {
		out.ByKind = append(out.ByKind, &pb_common.TechCardDevCostByKind{
			Kind:       k,
			AmountBase: pbDecimalFromDecimal(roundMoney(byKind[k])),
		})
	}
	sort.Slice(roundOrder, func(i, j int) bool { return roundOrder[i] < roundOrder[j] })
	for _, r := range roundOrder {
		out.ByRound = append(out.ByRound, &pb_common.TechCardDevCostByRound{
			RoundNumber:  r,
			AmountBase:   pbDecimalFromDecimal(roundMoney(byRound[r])),
			ExpenseCount: byRoundCount[r],
		})
	}

	// Rounds-to-approval + approval timeline from the fittings: the approving fitting (outcome=approved)
	// at the highest round is the approval point.
	var approvedAt time.Time
	haveApproval := false
	roundsToApproval := int32(0)
	for i := range fittings {
		f := &fittings[i]
		if !f.Outcome.Valid || entity.FittingOutcome(f.Outcome.String) != entity.FittingOutcomeApproved {
			continue
		}
		r := int32(0)
		if f.RoundNumber.Valid {
			r = f.RoundNumber.Int32
		}
		if !haveApproval || r > roundsToApproval {
			roundsToApproval = r
			approvedAt = f.FittingDate
			haveApproval = true
		}
	}
	out.RoundsToApproval = roundsToApproval
	if haveFirstExpense {
		out.FirstExpenseAt = timestamppb.New(firstExpenseAt)
	}
	if haveApproval {
		out.ApprovedAt = timestamppb.New(approvedAt)
		if haveFirstExpense && approvedAt.After(firstExpenseAt) {
			out.DaysToApproval = int32(approvedAt.Sub(firstExpenseAt).Hours() / 24)
		}
	}

	// Amortize the dev total over the current size run, added to the production unit cost.
	orderQty := 0
	if card != nil {
		for _, q := range card.SizeQuantities {
			if q.OrderQty > 0 {
				orderQty += q.OrderQty
			}
		}
	}
	out.OrderQty = int32(orderQty)
	if orderQty > 0 && totalBase.IsPositive() {
		if unit, ccy := ComputeTechCardUnitCost(card, fx); unit.Valid && strings.EqualFold(ccy, fx.Base) {
			perUnitDev := totalBase.Div(decimal.NewFromInt(int64(orderQty)))
			out.UnitCostWithDev = pbDecimalFromDecimal(roundMoney(unit.Decimal.Add(perUnitDev)))
		}
	}
	return out
}
