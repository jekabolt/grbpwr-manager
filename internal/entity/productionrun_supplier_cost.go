package entity

import (
	"database/sql"
	"sort"
	"strings"
	"time"

	"github.com/shopspring/decimal"
)

// SUPPLIER PRICE ARTICLE OF A BOUGHT AUX RUN.
//
// A bought auxiliary card (aux_made_by = supplier) issues no material, so without help its run cost
// is only what somebody types. The store seeds one ordinary cost article per currency from the
// card's supplier price × the planned quantity: the costs block shows it, the operator corrects it
// to the real invoice, and the receipt values the good units off it like any other article.
//
// The marker is the run's own audit trail, not a column: every seed, re-seed and removal appends a
// supplier_price_seeded event listing the articles it wrote. While those articles are still
// present EXACTLY as written, they are the system's and follow their basis (plan or price). The
// moment the operator edits or deletes one (its base amount included), they are the operator's and
// nothing re-seeds them.

// SupplierPriceCostDescription is the description of the seeded article.
const SupplierPriceCostDescription = "supplier price"

// ProductionRunEventSupplierPriceSeeded records the supplier price articles a save wrote. An empty
// article list records their removal (the basis is gone and the articles were untouched).
const ProductionRunEventSupplierPriceSeeded = "supplier_price_seeded"

// SupplierPriceSeedArticle is one written article as the event remembers it. AmountBase is the base
// amount the seed was folded to (null when no rate was known): an operator who overrides it with the
// invoice's own rate has edited the article, exactly like one who changes the amount.
type SupplierPriceSeedArticle struct {
	Amount     decimal.Decimal     `json:"amount"`
	Currency   string              `json:"currency"`
	AmountBase decimal.NullDecimal `json:"amount_base"`
}

// SupplierPriceSeedBasis is one output material's share of the seed: why the figure is what it is.
type SupplierPriceSeedBasis struct {
	MaterialId int             `json:"material_id"`
	Qty        int             `json:"qty"`
	Price      decimal.Decimal `json:"price"`
	Currency   string          `json:"currency"`
}

// SupplierPriceSeedRecord is the payload of a supplier_price_seeded event.
type SupplierPriceSeedRecord struct {
	Articles []SupplierPriceSeedArticle `json:"articles"`
	Basis    []SupplierPriceSeedBasis   `json:"basis,omitempty"`
}

// SupplierPriceSeed is what the current plan and prices give: the articles and their basis. Empty
// Articles means no seed (nothing planned, an unresolved output or a material with no price).
type SupplierPriceSeed struct {
	Articles []ProductionRunCost
	Basis    []SupplierPriceSeedBasis
}

// SupplierPriceSeedOpen reports the run states whose costs the seed may write. Received and closed
// runs are facts; a cancelled run is never planned again; a partially received run's cost is
// already being booked.
func SupplierPriceSeedOpen(s ProductionRunStatus) bool {
	switch s {
	case ProductionRunDraft, ProductionRunPlanned, ProductionRunInProgress:
		return true
	}
	return false
}

// SupplierPrice picks a material's supplier price as of asOf: the newest manual or purchase row
// already in force (valid_from <= asOf; valid_from DESC, ties by currency ASC). A price dated ahead
// is not today's price. ok=false: none.
func SupplierPrice(prices []MaterialPrice, asOf time.Time) (MaterialPrice, bool) {
	var best MaterialPrice
	found := false
	for _, p := range prices {
		if p.Source != MaterialPriceSourceManual && p.Source != MaterialPriceSourcePurchase {
			continue
		}
		if !p.Price.IsPositive() || p.ValidFrom.After(asOf) {
			continue
		}
		if !found || p.ValidFrom.After(best.ValidFrom) ||
			(p.ValidFrom.Equal(best.ValidFrom) && strings.ToUpper(p.Currency) < strings.ToUpper(best.Currency)) {
			best, found = p, true
		}
	}
	best.Currency = strings.ToUpper(strings.TrimSpace(best.Currency))
	return best, found
}

// ComputeSupplierPriceSeed prices the plan: each line with planned qty > 0 takes the price of its
// own output material (variantMaterial for a colour line, legacyMaterial for the single-output
// line). It is all or nothing: one planned line whose material is unresolved or unpriced gives no
// seed, because a partial figure would understate the run without saying so.
func ComputeSupplierPriceSeed(lines []ProductionRunLine, variantMaterial map[int]int, legacyMaterial sql.NullInt64,
	prices map[int]MaterialPrice) SupplierPriceSeed {
	qtyByMaterial := map[int]int{}
	for i := range lines {
		ln := &lines[i]
		if ln.PlannedQty <= 0 {
			continue
		}
		var mat int
		if ln.OutputVariantId.Valid {
			m, ok := variantMaterial[int(ln.OutputVariantId.Int32)]
			if !ok {
				return SupplierPriceSeed{}
			}
			mat = m
		} else {
			if !legacyMaterial.Valid || ln.ProductId.Valid {
				return SupplierPriceSeed{}
			}
			mat = int(legacyMaterial.Int64)
		}
		if _, ok := prices[mat]; !ok {
			return SupplierPriceSeed{}
		}
		qtyByMaterial[mat] += ln.PlannedQty
	}
	if len(qtyByMaterial) == 0 {
		return SupplierPriceSeed{}
	}
	mats := make([]int, 0, len(qtyByMaterial))
	for m := range qtyByMaterial {
		mats = append(mats, m)
	}
	sort.Ints(mats)
	byCurrency := map[string]decimal.Decimal{}
	basis := make([]SupplierPriceSeedBasis, 0, len(mats))
	for _, m := range mats {
		p := prices[m]
		cur := strings.ToUpper(strings.TrimSpace(p.Currency))
		qty := qtyByMaterial[m]
		byCurrency[cur] = byCurrency[cur].Add(p.Price.Mul(decimal.NewFromInt(int64(qty))))
		basis = append(basis, SupplierPriceSeedBasis{MaterialId: m, Qty: qty, Price: p.Price, Currency: cur})
	}
	currencies := make([]string, 0, len(byCurrency))
	for c := range byCurrency {
		currencies = append(currencies, c)
	}
	sort.Strings(currencies)
	articles := make([]ProductionRunCost, 0, len(currencies))
	for _, c := range currencies {
		articles = append(articles, ProductionRunCost{
			Kind:        ProductionRunCostMaterials,
			Description: sql.NullString{String: SupplierPriceCostDescription, Valid: true},
			Amount:      byCurrency[c].RoundBank(2),
			Currency:    c,
		})
	}
	return SupplierPriceSeed{Articles: articles, Basis: basis}
}

// ReconcileSupplierPriceCost decides one save of an open bought-aux run. costs are the articles
// about to be written, seed is what the plan and prices give now, last is the last recorded seed
// (nil: never seeded). It returns the articles to write and the record to append (nil: no change).
//
//   - Seeded before and the articles are untouched: they follow the seed — replaced when the figure
//     moved, removed when there is no seed any more.
//   - Seeded before and the operator edited or deleted one: hands off, for good.
//   - Never seeded (or last removed): seed, unless a materials article is already there — that one
//     is the operator's own figure for the same goods, and adding the price would count it twice.
func ReconcileSupplierPriceCost(costs []ProductionRunCost, seed SupplierPriceSeed, last *SupplierPriceSeedRecord) ([]ProductionRunCost, *SupplierPriceSeedRecord) {
	next := &SupplierPriceSeedRecord{Articles: seedArticles(seed.Articles), Basis: seed.Basis}
	if last != nil && len(last.Articles) > 0 {
		idx, intact := matchSeededArticles(costs, last.Articles)
		if !intact || sameSeedArticles(next.Articles, last.Articles) {
			return costs, nil
		}
		drop := make(map[int]bool, len(idx))
		for _, i := range idx {
			drop[i] = true
		}
		out := make([]ProductionRunCost, 0, len(costs)-len(idx)+len(seed.Articles))
		for i := range costs {
			if !drop[i] {
				out = append(out, costs[i])
			}
		}
		out = append(out, seed.Articles...)
		if len(seed.Articles) == 0 {
			next.Basis = nil
		}
		return out, next
	}
	if len(seed.Articles) == 0 {
		return costs, nil
	}
	for i := range costs {
		if costs[i].Kind == ProductionRunCostMaterials {
			return costs, nil
		}
	}
	out := make([]ProductionRunCost, 0, len(costs)+len(seed.Articles))
	out = append(out, costs...)
	out = append(out, seed.Articles...)
	return out, next
}

func seedArticles(costs []ProductionRunCost) []SupplierPriceSeedArticle {
	out := make([]SupplierPriceSeedArticle, 0, len(costs))
	for i := range costs {
		out = append(out, SupplierPriceSeedArticle{Amount: costs[i].Amount, Currency: costs[i].Currency,
			AmountBase: costs[i].AmountBase})
	}
	return out
}

// sameSeedArticles compares amount and currency only: a costing rate that moved while the plan and
// price did not is no reason to rewrite a base amount already booked.
func sameSeedArticles(a, b []SupplierPriceSeedArticle) bool {
	if len(a) != len(b) {
		return false
	}
	used := make([]bool, len(b))
	for _, x := range a {
		hit := false
		for j, y := range b {
			if !used[j] && x.Amount.Equal(y.Amount) && strings.EqualFold(x.Currency, y.Currency) {
				used[j], hit = true, true
				break
			}
		}
		if !hit {
			return false
		}
	}
	return true
}

// matchSeededArticles finds every recorded article among costs, each matched once. An article
// counts only while it is exactly as seeded: materials, the seed description, the same amount,
// currency and base amount, and no document field filled (a supplier, a document ref, VAT or a payable state past
// accrued all mean the operator took it over).
func matchSeededArticles(costs []ProductionRunCost, recorded []SupplierPriceSeedArticle) ([]int, bool) {
	used := make([]bool, len(costs))
	idx := make([]int, 0, len(recorded))
	for _, a := range recorded {
		hit := -1
		for i := range costs {
			if !used[i] && seededArticleIntact(&costs[i], a) {
				hit = i
				break
			}
		}
		if hit < 0 {
			return nil, false
		}
		used[hit] = true
		idx = append(idx, hit)
	}
	return idx, true
}

func seededArticleIntact(c *ProductionRunCost, a SupplierPriceSeedArticle) bool {
	if c.Kind != ProductionRunCostMaterials || !c.Description.Valid ||
		strings.TrimSpace(c.Description.String) != SupplierPriceCostDescription {
		return false
	}
	if !c.Amount.Equal(a.Amount) || !strings.EqualFold(c.Currency, a.Currency) {
		return false
	}
	if c.AmountBase.Valid != a.AmountBase.Valid ||
		(c.AmountBase.Valid && !c.AmountBase.Decimal.Equal(a.AmountBase.Decimal)) {
		return false
	}
	if (c.SupplierId.Valid && c.SupplierId.Int64 != 0) ||
		(c.DocumentRef.Valid && strings.TrimSpace(c.DocumentRef.String) != "") ||
		c.VatRate.Valid || c.VatAmount.Valid {
		return false
	}
	if c.ApStatus.Valid && c.ApStatus.String != "" && c.ApStatus.String != "accrued" {
		return false
	}
	return true
}
