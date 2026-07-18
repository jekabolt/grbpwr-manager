package betaseed

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	decimal "google.golang.org/genproto/googleapis/type/decimal"

	admin "github.com/jekabolt/grbpwr-manager/proto/gen/admin"
	common "github.com/jekabolt/grbpwr-manager/proto/gen/common"

	"github.com/jekabolt/grbpwr-manager/internal/currency"
)

// Volume selects how many full catalog flows SeedCatalog runs.
type Volume int

const (
	VolSingle   Volume = iota // 1 published style
	VolModerate               // 5 published styles
	VolDense                  // 15 published styles
)

// count maps a Volume to the number of styles to seed.
func count(v Volume) int {
	switch v {
	case VolDense:
		return 15
	case VolModerate:
		return 5
	default:
		return 1
	}
}

// seedTag is the merchandising tag every seeded colourway carries; hero nav_featured
// and the archive products-by-tag block reference it so they resolve to real products.
const seedTag = "beta-seed"

// Seeder holds the resolved dictionary and per-run identity used to drive the beta
// catalog + storefront flow. Construct it with NewSeeder.
type Seeder struct {
	C      *Client            // proven beta gateway client
	Dict   *common.Dictionary // resolved once in NewSeeder
	Vol    Volume             // how many styles SeedCatalog mints
	Run    string             // unique per-run suffix (unix seconds) for idempotent style numbers
	Log    func(format string, args ...any)
	LangID int32 // default language id
}

// NewSeeder resolves the dictionary and default language once, validates the price
// map against currency.RequiredCurrencies (drift guard), and returns a ready Seeder.
func NewSeeder(ctx context.Context, c *Client, vol Volume) (*Seeder, error) {
	if c == nil {
		return nil, fmt.Errorf("betaseed: nil client")
	}
	if err := validatePriceCoverage(); err != nil {
		return nil, err
	}
	dr, err := c.GetDictionary(ctx, &admin.GetDictionaryRequest{})
	if err != nil {
		return nil, fmt.Errorf("get dictionary: %w", err)
	}
	d := dr.GetDictionary()
	if d == nil {
		return nil, fmt.Errorf("get dictionary: empty dictionary in response")
	}
	lang := int32(1)
	for _, l := range d.GetLanguages() {
		if l.GetIsDefault() {
			lang = l.GetId()
			break
		}
	}
	return &Seeder{
		C:      c,
		Dict:   d,
		Vol:    vol,
		Run:    strconv.FormatInt(time.Now().Unix(), 10),
		LangID: lang,
	}, nil
}

// logf is a nil-safe progress logger.
func (s *Seeder) logf(format string, args ...any) {
	if s.Log != nil {
		s.Log(format, args...)
	}
}

// SizeIDByName resolves a size id from the dictionary by public name ("m","l",…).
func (s *Seeder) SizeIDByName(name string) (int32, error) {
	for _, sz := range s.Dict.GetSizes() {
		if strings.EqualFold(sz.GetName(), name) {
			return sz.GetId(), nil
		}
	}
	return 0, fmt.Errorf("size %q not found in dictionary", name)
}

// CategoryChain returns a valid top→sub→type category chain. leaf is the category id
// CreateTechCard requires (the deepest = type level); typ is the same value used for
// UpdateStyle.type_id. Mirrors the bash: type = first level=="type" category, sub =
// its parent, top = sub's parent (falling back to sub when the parent is unset).
func (s *Seeder) CategoryChain() (top, sub, typ, leaf int32, err error) {
	cats := s.Dict.GetCategories()
	var typeCat *common.Category
	for _, c := range cats {
		if strings.EqualFold(c.GetLevel(), "type") {
			typeCat = c
			break
		}
	}
	if typeCat == nil {
		return 0, 0, 0, 0, fmt.Errorf("no leaf (type) category in dictionary")
	}
	typ = typeCat.GetId()
	leaf = typ
	sub = typeCat.GetParentId()
	top = sub
	for _, c := range cats {
		if c.GetId() == sub {
			if c.GetParentId() != 0 {
				top = c.GetParentId()
			}
			break
		}
	}
	return top, sub, typ, leaf, nil
}

// ColorByCode resolves a non-archived dictionary colour by code, falling back to the
// first non-archived colour when the requested code is absent (like the bash).
func (s *Seeder) ColorByCode(code string) (string, int32, error) {
	var first *common.Color
	for _, col := range s.Dict.GetColors() {
		if col.GetArchived() {
			continue
		}
		if first == nil {
			first = col
		}
		if strings.EqualFold(col.GetCode(), code) {
			return col.GetCode(), col.GetId(), nil
		}
	}
	if first != nil {
		return first.GetCode(), first.GetId(), nil
	}
	return "", 0, fmt.Errorf("no non-archived colours in dictionary")
}

// MeasurementIDs returns the first n measurement-name ids from the dictionary.
func (s *Seeder) MeasurementIDs(n int) ([]int32, error) {
	ms := s.Dict.GetMeasurements()
	if len(ms) < n {
		return nil, fmt.Errorf("need %d measurements, dictionary has %d", n, len(ms))
	}
	ids := make([]int32, 0, n)
	for i := 0; i < n; i++ {
		ids = append(ids, ms[i].GetId())
	}
	return ids, nil
}

// CountryCode returns a manufacture country code, preferring "DE" when active.
func (s *Seeder) CountryCode() string {
	var firstActive string
	for _, c := range s.Dict.GetCountries() {
		if !c.GetActive() {
			continue
		}
		if firstActive == "" {
			firstActive = c.GetCode()
		}
		if strings.EqualFold(c.GetCode(), "DE") {
			return "DE"
		}
	}
	if firstActive != "" {
		return firstActive
	}
	return "DE"
}

// carrierID returns the first allowed shipment carrier id (fallback: first carrier, then 1).
func (s *Seeder) carrierID() int32 {
	carriers := s.Dict.GetShipmentCarriers()
	for _, c := range carriers {
		if c.GetShipmentCarrier().GetAllowed() {
			return c.GetId()
		}
	}
	if len(carriers) > 0 {
		return carriers[0].GetId()
	}
	return 1
}

// priceAmounts is the per-currency catalogue amount used for every seeded colourway.
// Every amount is comfortably above the Stripe minimum for that currency
// (internal/currency.minimumAmounts). Its key set MUST cover
// currency.RequiredCurrencies(); validatePriceCoverage / Prices enforce that.
var priceAmounts = map[string]string{
	"EUR": "120.00",
	"USD": "130.00",
	"GBP": "100.00",
	"JPY": "18000", // zero-decimal
	"CNY": "950.00",
	"KRW": "160000", // zero-decimal
	"PLN": "500.00",
}

// validatePriceCoverage fails loudly if any required currency lacks a seed amount
// (drift from currency.RequiredCurrencies — e.g. the PLN regression).
func validatePriceCoverage() error {
	var missing []string
	for _, cur := range currency.RequiredCurrencies() {
		if _, ok := priceAmounts[cur]; !ok {
			missing = append(missing, cur)
		}
	}
	if len(missing) > 0 {
		return fmt.Errorf("seed price map missing required currencies %v — add amounts above the Stripe minimum "+
			"(drift from currency.RequiredCurrencies; PublishColorway will 500 with 'missing required currencies')", missing)
	}
	return nil
}

// Prices builds the full required-currency price list from currency.RequiredCurrencies().
// It panics on drift (a required currency without a seed amount); NewSeeder validates the
// map up front so a live run never reaches that panic.
func (s *Seeder) Prices() []*common.ColorwayPriceInsert {
	reqs := currency.RequiredCurrencies()
	out := make([]*common.ColorwayPriceInsert, 0, len(reqs))
	for _, cur := range reqs {
		amt, ok := priceAmounts[cur]
		if !ok {
			panic(fmt.Sprintf("betaseed: no seed price amount for required currency %q", cur))
		}
		out = append(out, &common.ColorwayPriceInsert{
			Currency: cur,
			Price:    &decimal.Decimal{Value: amt},
		})
	}
	return out
}

// lockVersion reads the current shared tech_card lock_version via the style size-chart,
// mirroring the bash lockver() dance (UpdateStyle / UpdateStyleSizeChart / Publish all
// bump it, so it must be re-read before each mutation).
func (s *Seeder) lockVersion(ctx context.Context, styleID int32) (uint64, error) {
	r, err := s.C.GetStyleSizeChart(ctx, &admin.GetStyleSizeChartRequest{StyleId: styleID})
	if err != nil {
		return 0, fmt.Errorf("read size-chart lock_version for style %d: %w", styleID, err)
	}
	return uint64(r.GetChart().GetLockVersion()), nil
}
