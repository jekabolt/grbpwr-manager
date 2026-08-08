// Package costbasisreport answers one question before the base-size costing basis is switched on
// against real data: WHICH styles change price, by how much, and which stop having a price at all.
//
// It is a migration instrument, not a feature. It exists because the change it measures is silent
// by nature — the number on the costing tab and in product.cost_price moves without anybody editing
// a card — and a silent repricing of the whole catalogue is not something to discover from a margin
// report a month later. Delete it once the basis has landed and been reconciled.
//
// READ-ONLY, and structurally so: it holds a repository built by store.NewForTest with automigrate
// forced off (store.New would migrate AND run the SKU backfill, both writes) and it never calls a
// write method. Nothing here may grow one.
package costbasisreport

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strings"

	"github.com/jekabolt/grbpwr-manager/internal/dependency"
	"github.com/jekabolt/grbpwr-manager/internal/dto"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/shopspring/decimal"
)

// Options narrows what the report walks. The zero value walks every tech card.
type Options struct {
	// CardIDs limits the run to these tech cards; empty = all of them.
	CardIDs []int
	// OnlyChanged drops cards whose every colourway costs exactly what it costed before. On a large
	// catalogue that is most of them — a card with no size-graded norm anywhere cannot move.
	OnlyChanged bool
	// JSON emits the machine-readable document instead of the table.
	JSON bool
	// PageSize is the ListTechCards page. ListTechCards clamps to 100 and treats 0 as 50, so the
	// walk pages explicitly rather than pretending one call returns the catalogue.
	PageSize int
}

// Report is the whole machine-readable document (the --json form). The table renderer prints the
// same values, so the two can never disagree about what was found.
type Report struct {
	BaseCurrency string       `json:"base_currency"`
	Cards        []CardReport `json:"cards"`
	Summary      Summary      `json:"summary"`
}

// CardReport is one tech card: its size context, what happens to each colourway, and whether its
// costing sign-off stops matching the content it was given for.
type CardReport struct {
	TechCardID  int    `json:"tech_card_id"`
	StyleNumber string `json:"style_number"`
	Name        string `json:"name"`

	SizeRange    []SizeRef `json:"size_range"`
	BaseSize     *SizeRef  `json:"base_size"` // nil = the card names none → graded norms are uncosted
	CostingCcy   string    `json:"costing_currency"`
	DeclaredRun  []SizeQty `json:"declared_run"` // tech_card.size_quantities, the retired denominator
	DeclaredQty  int       `json:"declared_qty_total"`
	CostingState string    `json:"costing_signoff_state"`
	// SignoffGoesStale is true when an APPROVED costing sign-off carries a digest that the current
	// content no longer produces. It is expected to be true wherever a digest was ever stamped: the
	// costing projection took base_sample_size_id in with this change, so the fingerprint moved for
	// every card. Reported per card anyway, because "how many approvals have to be re-signed" is
	// the operational size of the change and nobody should have to infer it.
	SignoffGoesStale bool `json:"signoff_goes_stale"`

	Colorways []ColorwayReport `json:"colorways"`
}

type SizeRef struct {
	ID   int    `json:"id"`
	Name string `json:"name"`
}

type SizeQty struct {
	SizeID   int    `json:"size_id"`
	SizeName string `json:"size_name"`
	Qty      int    `json:"qty"`
}

// ColorwayReport is the money: what this colourway costed on the old basis, what it costs on the
// new one, and what is stored on the product today.
type ColorwayReport struct {
	ColorwayID int    `json:"colorway_id"`
	ProductID  int    `json:"product_id"`
	Name       string `json:"name"`

	OldUnitCost *string `json:"old_unit_cost"` // nil = the old basis could not cost it either
	NewUnitCost *string `json:"new_unit_cost"` // nil = uncosted on the new basis
	// Currencies are reported per side, not as one field. A figure that folds to base on one basis
	// and falls back to the costing currency on the other is two different units of measure, and
	// labelling both with the surviving one would present the pair as comparable when it is not.
	OldCurrency string `json:"old_currency"`
	NewCurrency string `json:"new_currency"`
	// MixedCurrency is set when old and new resolved in different currencies. The delta is then
	// withheld rather than computed across two currencies.
	MixedCurrency bool    `json:"mixed_currency"`
	DeltaAbs      *string `json:"delta_abs"`
	DeltaPct      *string `json:"delta_pct"`
	// BecameUncosted is the finding that matters most: this colourway HAD a computable standard cost
	// and no longer does, so the seed will stop writing product.cost_price for it.
	BecameUncosted bool `json:"became_uncosted"`
	// BecameCosted is its mirror, and it is NOT symmetric noise: the old basis produced nothing when
	// the card declared no typical run at all, so styles that simply never had a fake mix acquire a
	// standard cost here. Counting only the losses would have made the change look purely subtractive.
	BecameCosted bool `json:"became_costed"`
	// Changed is the honest verdict, taken from the raw figures rather than from the rounded
	// percentage: a validity flip, a currency flip, or any difference in the money value. A
	// one-cent move on a large cost rounds to 0.00% and must still count as changed.
	Changed bool `json:"changed"`

	StoredCostPrice  *string `json:"stored_cost_price"`
	StoredCostSource string  `json:"stored_cost_source"` // manual | tech_card | production_run | ""
	// SeedMayOverwrite mirrors the store's own provenance predicate (cost_price_source IS NULL OR =
	// 'tech_card'). A manual or production-run cost is NOT repriced by this change however far the
	// card figure moves — that distinction is the difference between "the catalogue reprices" and
	// "the tech-card tab shows a different number".
	SeedMayOverwrite bool `json:"seed_may_overwrite"`
	// RepricesProduct is the operational answer SeedMayOverwrite alone cannot give: provenance
	// permits the write AND the new figure actually exists AND it is in the base currency (every
	// seed call site gates on that) AND it differs from what is stored. This is the count of
	// products whose COGS really moves.
	RepricesProduct bool `json:"reprices_product"`

	Usages []UsageReport `json:"usages"`
}

// UsageReport is one расход inside a colourway, with the two norms side by side.
type UsageReport struct {
	Material string `json:"material"`
	// SizeGraded false = the norm is a single per-garment number; it is untouched by this change and
	// carries no old/new norm pair. Reported anyway so a card's recipe reads whole.
	SizeGraded  bool       `json:"size_graded"`
	NormsBySize []SizeNorm `json:"norms_by_size"`
	// OldNorm is the qty-weighted average the retired basis derived: Σ(norm × declared qty) ÷ Σ
	// declared qty over ALL sizes with a positive declared quantity. nil when it was not computable.
	OldNorm *string `json:"old_norm"`
	// NewNorm is the norm recorded on the base sample size. nil = no basis (no base size, or this
	// usage was never graded on it) → the line is not costed.
	NewNorm    *string `json:"new_norm"`
	PerGarment *string `json:"per_garment_norm"` // the single number, when the usage is not graded
	Uncosted   bool    `json:"uncosted"`
}

type SizeNorm struct {
	SizeID   int    `json:"size_id"`
	SizeName string `json:"size_name"`
	Norm     string `json:"norm"`
	// PartOfDeclaredRun marks a size that carries a declared quantity. A size with a quantity but NO
	// norm on this usage is what made the old average understate: the quantity went into the
	// denominator and nothing went into the numerator.
	PartOfDeclaredRun bool `json:"part_of_declared_run"`
}

// Summary is the answer to "how big is this". Percentiles are over |delta %| of every colourway
// that has BOTH an old and a new figure in one currency; the ones that lost their figure are
// counted separately, because a percentile cannot express "there is no number any more".
type Summary struct {
	CardsScanned      int    `json:"cards_scanned"`
	CardsAffected     int    `json:"cards_affected"`
	ColorwaysCompared int    `json:"colorways_compared"`
	P50DeltaPct       string `json:"p50_delta_pct"`
	P90DeltaPct       string `json:"p90_delta_pct"`
	P95DeltaPct       string `json:"p95_delta_pct"`
	MaxDeltaPct       string `json:"max_delta_pct"`
	Over2Pct          int    `json:"colorways_over_2_pct"`
	Over5Pct          int    `json:"colorways_over_5_pct"`
	Over10Pct         int    `json:"colorways_over_10_pct"`
	// ProductsRepriced counts colourways whose stored product.cost_price will actually be rewritten
	// (provenance permits it, the new figure exists in the base currency, and it differs). It is the
	// figure to quote for "how much of the catalogue moves" — CardsAffected includes cards whose
	// only change is a sign-off that must be re-signed.
	ProductsRepriced int `json:"products_repriced"`
	// BecameCosted is the mirror of BecameUncosted: styles that had no computable cost under the old
	// basis (typically because they declared no typical run) and acquire one now.
	BecameCosted       int      `json:"became_costed"`
	BecameUncosted     []string `json:"became_uncosted"`      // "card 12 (ST-001) / Black"
	SignoffsGoingStale int      `json:"signoffs_going_stale"` // approved costing sign-offs that stop matching
}

// Run walks the catalogue and builds the report. It performs reads only.
func Run(ctx context.Context, repo dependency.Repository, baseCurrency string, opts Options) (*Report, error) {
	sizeNames, err := loadSizeNames(ctx, repo)
	if err != nil {
		return nil, err
	}
	rates, err := repo.TechCards().GetCostingFxRatesToBase(ctx)
	if err != nil {
		return nil, fmt.Errorf("costing fx rates: %w", err)
	}
	fx := dto.CostingFx{ToBase: rates, Base: baseCurrency}

	ids := opts.CardIDs
	if len(ids) == 0 {
		if ids, err = listAllCardIDs(ctx, repo, opts.PageSize); err != nil {
			return nil, err
		}
	}

	rep := &Report{BaseCurrency: baseCurrency}
	deltas := []decimal.Decimal{}
	for _, id := range ids {
		card, err := repo.TechCards().GetTechCardByIdConsistent(ctx, id)
		if err != nil {
			return nil, fmt.Errorf("tech card %d: %w", id, err)
		}
		if card == nil {
			continue
		}
		// A card with NO costing row is still walked. Its prices cannot move, but its costing
		// sign-off can go stale all the same — the projection is [costing, qty, baseSize] and the
		// third element was added for every card, costing row or not. Skipping these here made the
		// report understate the re-approval wave, which is the one number this change is judged on.
		rep.Summary.CardsScanned++
		cr := buildCard(card, fx, sizeNames)
		changed := false
		for _, cw := range cr.Colorways {
			if cw.Changed {
				changed = true
			}
			if cw.BecameUncosted {
				rep.Summary.BecameUncosted = append(rep.Summary.BecameUncosted,
					fmt.Sprintf("card %d (%s) / %s", cr.TechCardID, cr.StyleNumber, cw.Name))
			}
			if cw.BecameCosted {
				rep.Summary.BecameCosted++
			}
			if cw.RepricesProduct {
				rep.Summary.ProductsRepriced++
			}
			if cw.DeltaPct == nil {
				continue
			}
			d := decimal.RequireFromString(*cw.DeltaPct).Abs()
			rep.Summary.ColorwaysCompared++
			deltas = append(deltas, d)
			if d.GreaterThan(decimal.NewFromInt(2)) {
				rep.Summary.Over2Pct++
			}
			if d.GreaterThan(decimal.NewFromInt(5)) {
				rep.Summary.Over5Pct++
			}
			if d.GreaterThan(decimal.NewFromInt(10)) {
				rep.Summary.Over10Pct++
			}
		}
		if cr.SignoffGoesStale {
			rep.Summary.SignoffsGoingStale++
			changed = true // a re-approval is a change to act on even when no number moved
		}
		if changed {
			rep.Summary.CardsAffected++
		}
		if opts.OnlyChanged && !changed {
			continue
		}
		rep.Cards = append(rep.Cards, cr)
	}

	sort.Slice(deltas, func(i, j int) bool { return deltas[i].LessThan(deltas[j]) })
	rep.Summary.P50DeltaPct = percentile(deltas, 50)
	rep.Summary.P90DeltaPct = percentile(deltas, 90)
	rep.Summary.P95DeltaPct = percentile(deltas, 95)
	rep.Summary.MaxDeltaPct = percentile(deltas, 100)
	return rep, nil
}

// listAllCardIDs pages ListTechCards to the end. It pages deliberately: the store clamps limit to
// 100 and turns 0 into 50, so a single call would quietly report on the first page of the
// catalogue and call it the catalogue.
func listAllCardIDs(ctx context.Context, repo dependency.Repository, pageSize int) ([]int, error) {
	if pageSize <= 0 || pageSize > 100 {
		pageSize = 100
	}
	var ids []int
	for offset := 0; ; offset += pageSize {
		page, total, err := repo.TechCards().ListTechCards(ctx, pageSize, offset, entity.Ascending, entity.TechCardListFilter{})
		if err != nil {
			return nil, fmt.Errorf("list tech cards (offset %d): %w", offset, err)
		}
		for i := range page {
			ids = append(ids, page[i].Id)
		}
		if len(page) == 0 || len(ids) >= total {
			return ids, nil
		}
	}
}

func loadSizeNames(ctx context.Context, repo dependency.Repository) (map[int]string, error) {
	// GetDictionaryInfo, not cache.GetSizeById: the cache is filled by cache.InitConsts, which also
	// wants the hero — a much bigger read for a size label. This is the same dictionary the cache
	// would have been built from.
	di, err := repo.Cache().GetDictionaryInfo(ctx)
	if err != nil {
		return nil, fmt.Errorf("dictionary: %w", err)
	}
	out := make(map[int]string, len(di.Sizes))
	for _, s := range di.Sizes {
		out[s.Id] = s.Name
	}
	return out, nil
}

func buildCard(card *entity.TechCard, fx dto.CostingFx, sizeNames map[int]string) CardReport {
	cr := CardReport{
		TechCardID:  card.Id,
		StyleNumber: card.StyleNumber.String,
		Name:        card.Name,
	}
	if card.Costing != nil {
		cr.CostingCcy = card.Costing.Currency.String
	}
	for _, id := range card.SizeIds {
		cr.SizeRange = append(cr.SizeRange, SizeRef{ID: id, Name: sizeNames[id]})
	}
	if b := card.CostingBaseSizeID(); b > 0 {
		cr.BaseSize = &SizeRef{ID: b, Name: sizeNames[b]}
	}
	declaredQty := map[int]int{}
	for _, q := range card.SizeQuantities {
		cr.DeclaredRun = append(cr.DeclaredRun, SizeQty{SizeID: q.SizeId, SizeName: sizeNames[q.SizeId], Qty: q.OrderQty})
		if q.OrderQty > 0 {
			declaredQty[q.SizeId] = q.OrderQty
			cr.DeclaredQty += q.OrderQty
		}
	}

	// The costing sign-off's verdict. A card with no stamped digest cannot go stale — there is
	// nothing recorded to compare the new content against.
	digests := dto.TechCardSectionDigests(&card.TechCardInsert)
	for _, so := range card.Signoffs {
		if so.Section != entity.SignoffCosting {
			continue
		}
		cr.CostingState = string(so.State)
		if so.State == entity.SignoffStateApproved && so.SignedDigest.Valid && so.SignedDigest.String != "" {
			cr.SignoffGoesStale = so.SignedDigest.String != digests[entity.SignoffCosting]
		}
	}

	old := oldBasisCard(card, declaredQty, cr.DeclaredQty)
	for i := range card.Colorways {
		cw := &card.Colorways[i]
		cr.Colorways = append(cr.Colorways, buildColorway(card, old, cw, i, fx, sizeNames, declaredQty))
	}
	return cr
}

func buildColorway(card, old *entity.TechCard, cw *entity.TechCardColorway, idx int,
	fx dto.CostingFx, sizeNames map[int]string, declaredQty map[int]int) ColorwayReport {

	out := ColorwayReport{
		ColorwayID: cw.Id,
		ProductID:  int(cw.ProductId.Int32),
		Name:       cw.Name,
	}
	if cw.CostPrice.Valid {
		s := cw.CostPrice.Decimal.StringFixed(2)
		out.StoredCostPrice = &s
	}
	out.StoredCostSource = cw.CostPriceSource.String
	// Mirrors internal/store/product's own guard: NULL or 'tech_card' may be reseeded; a manual
	// price and a production-run actual may not.
	out.SeedMayOverwrite = !cw.CostPriceSource.Valid ||
		cw.CostPriceSource.String == "" || cw.CostPriceSource.String == "tech_card"

	newCost, newCcy := colorwayUnitCost(card, cw, idx, fx)
	oldCost, oldCcy := colorwayUnitCost(old, &old.Colorways[idx], idx, fx)

	if oldCost.Valid {
		s := oldCost.Decimal.StringFixed(2)
		out.OldUnitCost = &s
		out.OldCurrency = oldCcy
	}
	if newCost.Valid {
		s := newCost.Decimal.StringFixed(2)
		out.NewUnitCost = &s
		out.NewCurrency = newCcy
	}
	out.BecameUncosted = oldCost.Valid && !newCost.Valid
	out.BecameCosted = !oldCost.Valid && newCost.Valid
	if oldCost.Valid && newCost.Valid {
		if !strings.EqualFold(oldCcy, newCcy) {
			// Two currencies is not a delta. It happens when one side folds to base and the other
			// falls back to the costing currency; subtracting them would invent a number.
			out.MixedCurrency = true
		} else {
			abs := newCost.Decimal.Sub(oldCost.Decimal)
			a := abs.StringFixed(2)
			out.DeltaAbs = &a
			if oldCost.Decimal.IsZero() {
				zero := "0"
				out.DeltaPct = &zero
			} else {
				p := abs.Div(oldCost.Decimal).Mul(decimal.NewFromInt(100)).Round(2).String()
				out.DeltaPct = &p
			}
		}
	}
	// Read the verdict off the RAW figures. Deriving it from DeltaPct instead would drop two real
	// changes on the floor: a style that gains or loses its cost entirely (no percentage exists),
	// and a cent-level move whose percentage rounds to 0.00 on a large cost.
	out.Changed = out.BecameUncosted || out.BecameCosted || out.MixedCurrency ||
		(oldCost.Valid && newCost.Valid && !oldCost.Decimal.Equal(newCost.Decimal))

	// Whether the catalogue actually moves: provenance must permit the write, the new figure must
	// exist in the base currency (every seed call site gates on exactly that), and it must differ
	// from the price stored today.
	out.RepricesProduct = out.SeedMayOverwrite && newCost.Valid &&
		strings.EqualFold(newCcy, fx.Base) &&
		(!cw.CostPrice.Valid || !cw.CostPrice.Decimal.Equal(newCost.Decimal))

	baseSize := card.CostingBaseSizeID()
	for i := range cw.Usages {
		out.Usages = append(out.Usages, buildUsage(card, &cw.Usages[i], baseSize, sizeNames, declaredQty))
	}
	return out
}

// colorwayUnitCost prices one colourway through the LIVE seeding entry point, so the report can
// never report a figure the system would not actually write. ComputeColorwayUnitCost keys off the
// product id; a colourway that is not linked to a product (a draft) has none, and the style figure
// is then the only thing that describes it.
func colorwayUnitCost(card *entity.TechCard, cw *entity.TechCardColorway, idx int, fx dto.CostingFx) (decimal.NullDecimal, string) {
	if cw.ProductId.Valid && cw.ProductId.Int32 > 0 {
		return dto.ComputeColorwayUnitCost(card, int(cw.ProductId.Int32), fx)
	}
	if idx == 0 {
		return dto.ComputeTechCardUnitCost(card, fx)
	}
	return decimal.NullDecimal{}, ""
}

func buildUsage(card *entity.TechCard, u *entity.TechCardColorwayUsage, baseSize int,
	sizeNames map[int]string, declaredQty map[int]int) UsageReport {

	out := UsageReport{Material: usageMaterialName(card, u)}
	if len(u.SizeConsumptions) == 0 {
		switch {
		case u.Quantity.Valid:
			s := u.Quantity.Decimal.String()
			out.PerGarment = &s
		case u.Consumption.Valid:
			s := u.Consumption.Decimal.String()
			out.PerGarment = &s
		default:
			out.Uncosted = true
		}
		return out
	}

	out.SizeGraded = true
	for _, sc := range u.SizeConsumptions {
		_, inRun := declaredQty[sc.SizeId]
		out.NormsBySize = append(out.NormsBySize, SizeNorm{
			SizeID: sc.SizeId, SizeName: sizeNames[sc.SizeId],
			Norm: sc.Consumption.String(), PartOfDeclaredRun: inRun,
		})
	}
	if n, ok := oldWeightedNorm(u, declaredQty); ok {
		s := n.String()
		out.OldNorm = &s
	}
	for _, sc := range u.SizeConsumptions {
		if sc.SizeId == baseSize {
			s := sc.Consumption.String()
			out.NewNorm = &s
			break
		}
	}
	out.Uncosted = out.NewNorm == nil
	return out
}

func usageMaterialName(card *entity.TechCard, u *entity.TechCardColorwayUsage) string {
	for i := range card.BomItems {
		b := &card.BomItems[i]
		if u.BomItemId.Valid && int64(b.Id) == u.BomItemId.Int64 {
			return b.Name
		}
	}
	if u.BomItemIndex.Valid && int(u.BomItemIndex.Int32) < len(card.BomItems) && u.BomItemIndex.Int32 >= 0 {
		return card.BomItems[u.BomItemIndex.Int32].Name
	}
	return "(unresolved BOM line)"
}

// oldWeightedNorm reproduces the RETIRED basis's per-garment norm for one size-graded usage:
//
//	Σ(norm_size × declared_qty_size) ÷ Σ declared_qty
//
// Note the asymmetry, and that it is the defect this change removes, not an approximation of it:
// the numerator runs over the sizes THIS usage was graded on, while the denominator is the whole
// declared run. A usage graded on 2 of 4 sizes was therefore divided by a run it only half covered
// and came out systematically CHEAP — so some of the increases this report shows are not a
// repricing at all, they are the correction of an undercount.
func oldWeightedNorm(u *entity.TechCardColorwayUsage, declaredQty map[int]int) (decimal.Decimal, bool) {
	total := 0
	for _, q := range declaredQty {
		total += q
	}
	if total <= 0 {
		return decimal.Zero, false
	}
	num := decimal.Zero
	for _, sc := range u.SizeConsumptions {
		q, ok := declaredQty[sc.SizeId]
		if !ok || q <= 0 {
			continue
		}
		num = num.Add(sc.Consumption.Mul(decimal.NewFromInt(int64(q))))
	}
	if num.IsZero() {
		return decimal.Zero, false
	}
	return num.Div(decimal.NewFromInt(int64(total))), true
}

// oldBasisCard is a copy of the card in which every size-graded usage has been collapsed into the
// single per-garment norm the retired basis derived for it (oldWeightedNorm), with its per-size
// grading and any stray Quantity removed so the live costing code takes the per-garment branch.
//
// WHY A SHADOW CARD AND NOT A SECOND IMPLEMENTATION. The "before" figure has to include article
// pricing, colourway pins, wastage provenance, currency buckets, FX folding, defect % and the
// completeness gate — every one of which the real costing does and a re-implementation would drift
// from. Rewriting the ONE input that changed and running the production code over it gives an
// old-basis number the system genuinely used to produce.
//
// THE ONE KNOWN INFIDELITY, stated rather than glossed: the division moved. The retired basis
// divided the PRICED run total by the quantity; this divides the NORM and then prices it. Both go
// through decimal's 16-place division, so the two differ in the 16th significant digit — invisible
// except when the true old value sits exactly on a half-cent, where the displayed "before" can land
// one cent low (norm 1 ÷ 3 sizes at 0.045: 0.015 exactly vs 0.01499…). One cent on a unit cost is
// far below every threshold this report groups by; it is called out so nobody later reads a 0.01
// discrepancy against the live tab as evidence of a costing bug.
//
// A usage whose old norm was not computable (no declared run, or none of its graded sizes carried a
// quantity) is left with NO norm at all, so the old side reports "uncosted" exactly as it did.
func oldBasisCard(card *entity.TechCard, declaredQty map[int]int, totalQty int) *entity.TechCard {
	cp := *card
	cp.Colorways = make([]entity.TechCardColorway, len(card.Colorways))
	copy(cp.Colorways, card.Colorways)
	for i := range cp.Colorways {
		src := card.Colorways[i].Usages
		usages := make([]entity.TechCardColorwayUsage, len(src))
		copy(usages, src)
		for j := range usages {
			u := &usages[j]
			if len(u.SizeConsumptions) == 0 {
				continue
			}
			norm, ok := oldWeightedNorm(u, declaredQty)
			u.SizeConsumptions = nil
			u.Quantity = decimal.NullDecimal{}
			if ok {
				u.Consumption = decimal.NullDecimal{Decimal: norm, Valid: true}
			} else {
				u.Consumption = decimal.NullDecimal{}
			}
		}
		cp.Colorways[i].Usages = usages
	}
	return &cp
}

// percentile returns the p-th percentile of an ASCENDING slice by NEAREST RANK (index
// ⌈p·n/100⌉−1), "0" when empty.
//
// Nearest rank, not (n−1)·p/100: on small samples the latter collapses upward. With two deltas
// [1%, 100%] it reported p90 = p95 = 1% — reading as "the tail is negligible" on a catalogue where
// half the styles doubled. A migration report is used precisely when the sample is small and the
// tail is the whole question.
func percentile(sorted []decimal.Decimal, p int) string {
	if len(sorted) == 0 {
		return "0"
	}
	rank := (p*len(sorted) + 99) / 100 // ⌈p·n/100⌉ in integer arithmetic
	if rank < 1 {
		rank = 1
	}
	if rank > len(sorted) {
		rank = len(sorted)
	}
	return sorted[rank-1].String()
}

// Write renders the report: the machine-readable document when opts.JSON, the table otherwise.
func (r *Report) Write(w io.Writer, opts Options) error {
	if opts.JSON {
		enc := json.NewEncoder(w)
		enc.SetIndent("", "  ")
		return enc.Encode(r)
	}
	r.writeTable(w)
	return nil
}

func (r *Report) writeTable(w io.Writer) {
	for _, c := range r.Cards {
		base := "— NONE (graded norms are not costed)"
		if c.BaseSize != nil {
			base = fmt.Sprintf("%s (id %d)", nameOr(c.BaseSize.Name, c.BaseSize.ID), c.BaseSize.ID)
		}
		fmt.Fprintf(w, "\n══ card %d  %s  %q\n", c.TechCardID, orDash(c.StyleNumber), c.Name)
		fmt.Fprintf(w, "   sizes: %s\n   base size: %s\n", joinSizes(c.SizeRange), base)
		fmt.Fprintf(w, "   declared run (retired denominator): %s  Σ=%d\n", joinQty(c.DeclaredRun), c.DeclaredQty)
		fmt.Fprintf(w, "   costing sign-off: %s%s\n", orDash(c.CostingState), staleNote(c.SignoffGoesStale))

		for _, cw := range c.Colorways {
			fmt.Fprintf(w, "   ── colourway %d %q (product %d)\n", cw.ColorwayID, cw.Name, cw.ProductID)
			fmt.Fprintf(w, "      unit cost: %s → %s   Δ %s (%s%%)%s\n",
				orDash(deref(cw.OldUnitCost))+" "+orDash(cw.OldCurrency),
				orDash(deref(cw.NewUnitCost))+" "+orDash(cw.NewCurrency),
				orDash(deref(cw.DeltaAbs)), orDash(deref(cw.DeltaPct)), flags(cw))
			fmt.Fprintf(w, "      stored cost_price: %s (%s)%s%s\n",
				orDash(deref(cw.StoredCostPrice)), orDash(cw.StoredCostSource),
				overwriteNote(cw.SeedMayOverwrite), repriceNote(cw.RepricesProduct))
			for _, u := range cw.Usages {
				if !u.SizeGraded {
					fmt.Fprintf(w, "        · %-28s per garment %s%s\n",
						truncate(u.Material, 28), orDash(deref(u.PerGarment)), uncostedNote(u.Uncosted))
					continue
				}
				fmt.Fprintf(w, "        · %-28s graded [%s]\n", truncate(u.Material, 28), joinNorms(u.NormsBySize))
				fmt.Fprintf(w, "          %-28s old avg %s → base-size %s%s\n", "",
					orDash(deref(u.OldNorm)), orDash(deref(u.NewNorm)), uncostedNote(u.Uncosted))
			}
		}
	}

	s := r.Summary
	fmt.Fprintf(w, "\n═══════════════ SUMMARY (base currency %s) ═══════════════\n", r.BaseCurrency)
	fmt.Fprintf(w, "cards scanned                : %d\n", s.CardsScanned)
	fmt.Fprintf(w, "cards whose cost moves       : %d\n", s.CardsAffected)
	fmt.Fprintf(w, "colourways compared          : %d\n", s.ColorwaysCompared)
	fmt.Fprintf(w, "|Δ%%| p50 / p90 / p95 / max   : %s / %s / %s / %s\n", s.P50DeltaPct, s.P90DeltaPct, s.P95DeltaPct, s.MaxDeltaPct)
	fmt.Fprintf(w, "colourways over 2%% / 5%% / 10%%: %d / %d / %d\n", s.Over2Pct, s.Over5Pct, s.Over10Pct)
	fmt.Fprintf(w, "products whose cost_price moves : %d\n", s.ProductsRepriced)
	fmt.Fprintf(w, "colourways that GAIN a cost     : %d\n", s.BecameCosted)
	fmt.Fprintf(w, "approved costing sign-offs going stale: %d\n", s.SignoffsGoingStale)
	fmt.Fprintf(w, "\nBECOMING UNCOSTED (%d) — the seed will stop writing product.cost_price for these:\n", len(s.BecameUncosted))
	if len(s.BecameUncosted) == 0 {
		fmt.Fprintln(w, "  (none)")
	}
	for _, l := range s.BecameUncosted {
		fmt.Fprintf(w, "  · %s\n", l)
	}
}

func flags(cw ColorwayReport) string {
	switch {
	case cw.BecameUncosted:
		return "  ⚠ BECOMES UNCOSTED"
	case cw.MixedCurrency:
		return "  ⚠ currencies differ, no delta"
	}
	return ""
}

func staleNote(stale bool) string {
	if stale {
		return "  ⚠ digest no longer matches — approval goes stale"
	}
	return ""
}

func overwriteNote(may bool) string {
	if may {
		return ""
	}
	return "  [locked: manual/production-run cost is not overwritten]"
}

func repriceNote(reprices bool) string {
	if reprices {
		return "  → WILL BE REWRITTEN"
	}
	return ""
}

func uncostedNote(u bool) string {
	if u {
		return "  ⚠ NOT COSTED"
	}
	return ""
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

func orDash(s string) string {
	if strings.TrimSpace(s) == "" {
		return "—"
	}
	return s
}

func nameOr(name string, id int) string {
	if strings.TrimSpace(name) == "" {
		return fmt.Sprintf("size#%d", id)
	}
	return name
}

func joinSizes(ss []SizeRef) string {
	if len(ss) == 0 {
		return "—"
	}
	parts := make([]string, 0, len(ss))
	for _, s := range ss {
		parts = append(parts, nameOr(s.Name, s.ID))
	}
	return strings.Join(parts, ", ")
}

func joinQty(qs []SizeQty) string {
	if len(qs) == 0 {
		return "—"
	}
	parts := make([]string, 0, len(qs))
	for _, q := range qs {
		parts = append(parts, fmt.Sprintf("%s:%d", nameOr(q.SizeName, q.SizeID), q.Qty))
	}
	return strings.Join(parts, " ")
}

func joinNorms(ns []SizeNorm) string {
	parts := make([]string, 0, len(ns))
	for _, n := range ns {
		mark := ""
		if !n.PartOfDeclaredRun {
			mark = "*" // graded but outside the declared run: it never reached the old average
		}
		parts = append(parts, fmt.Sprintf("%s=%s%s", nameOr(n.SizeName, n.SizeID), n.Norm, mark))
	}
	return strings.Join(parts, " ")
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n-1] + "…"
}
