package cache

import (
	"context"
	"log/slog"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/jekabolt/grbpwr-manager/internal/tenant"
	"github.com/shopspring/decimal"
)

// Per-team getters and setters. Each resolves the team from ctx (team.go). A getter with no team in
// teams mode, or for a team whose load failed, returns its zero value; a setter writes nothing then
// (the database stays the truth; the next load reads it).

func writeSkipped(ctx context.Context, op string, err error) {
	slog.Default().WarnContext(ctx, "team cache write skipped",
		slog.String("op", op), slog.String("err", err.Error()))
}

// ─── Merch dictionaries ────────────────────────────────────────────────────────────────────────

// RefreshDictionary updates categories, sizes, collections (including CountMen/CountWomen),
// product tags, tags, fibres and colours of the team in ctx. Call after product add/update/delete so
// counts and tags stay accurate. Team #1's refresh also refreshes the process-wide definitions.
func RefreshDictionary(ctx context.Context, dInfo *entity.DictionaryInfo) {
	if dInfo == nil {
		return
	}
	s, tid, err := registry.getForWrite(ctx)
	if err != nil {
		writeSkipped(ctx, "RefreshDictionary", err)
		return
	}
	s.mu.Lock()
	s.setDictLocked(dInfo)
	// The system card id never changes once seeded; a partial DictionaryInfo (0) keeps the known id.
	if dInfo.ModelPhotoTechCardID > 0 {
		s.modelPhotoTechCardID = dInfo.ModelPhotoTechCardID
	}
	s.mu.Unlock()
	if tid == tenant.TeamOneID {
		setDefinitions(dInfo)
	}
}

func GetCategories(ctx context.Context) []entity.Category {
	s := registry.get(ctx)
	if s == nil {
		return nil
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.categories
}

func GetSizes(ctx context.Context) []entity.Size {
	s := registry.get(ctx)
	if s == nil {
		return nil
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.sizes
}

func GetCollections(ctx context.Context) []entity.Collection {
	s := registry.get(ctx)
	if s == nil {
		return nil
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.collections
}

func GetProductTags(ctx context.Context) []string {
	s := registry.get(ctx)
	if s == nil {
		return nil
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.productTags
}

// GetTags returns the controlled merchandising tag dictionary (R9) from the `tag` table — the set an
// admin creates via CreateTag and reuses by code/name. Distinct from GetProductTags (usage-derived).
func GetTags(ctx context.Context) []entity.TagDict {
	s := registry.get(ctx)
	if s == nil {
		return nil
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.tags
}

// GetColors returns the controlled colour dictionary of the team in ctx.
func GetColors(ctx context.Context) []entity.Color {
	return ColorDictFor(ctx).All()
}

// GetColorByCode returns the team's dictionary entry for a 3-char colour code.
func GetColorByCode(ctx context.Context, code string) (entity.Color, bool) {
	return ColorDictFor(ctx).ByCode(code)
}

// ColorDict is one team's colour dictionary, handed to code that has no ctx (internal/dto). It is an
// immutable snapshot: a refresh builds a new one.
type ColorDict struct {
	all    []entity.Color
	byCode map[string]entity.Color
}

// NewColorDict builds a colour dictionary from its entries (tests, the cache itself).
func NewColorDict(colors []entity.Color) ColorDict {
	if colors == nil {
		colors = []entity.Color{}
	}
	d := ColorDict{all: colors, byCode: make(map[string]entity.Color, len(colors))}
	for _, c := range colors {
		d.byCode[c.Code] = c
	}
	return d
}

// ByCode returns the entry for a 3-char colour code.
func (d ColorDict) ByCode(code string) (entity.Color, bool) {
	c, ok := d.byCode[code]
	return c, ok
}

// All returns every entry, archived ones included.
func (d ColorDict) All() []entity.Color { return d.all }

// ColorDictFor returns the colour dictionary of the team in ctx (empty with no team).
func ColorDictFor(ctx context.Context) ColorDict {
	s := registry.get(ctx)
	if s == nil {
		return ColorDict{}
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.colors
}

// GetFibers returns the controlled fibre vocabulary (S17/P0.4).
func GetFibers(ctx context.Context) []entity.Fiber {
	s := registry.get(ctx)
	if s == nil {
		return nil
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.fibers
}

// GetModelPhotoTechCardID returns the id of the team's hidden system tech card model-photo cutout
// runs are booked on, or 0 when it is unknown (dictionary not loaded or seed row missing).
func GetModelPhotoTechCardID(ctx context.Context) int {
	s := registry.get(ctx)
	if s == nil {
		return 0
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.modelPhotoTechCardID
}

// SetModelPhotoTechCardID replaces the team's cached system model-photo card id. The dictionary load
// is the production writer; this exists so tests can arm the guard without a full DictionaryInfo
// (RefreshDictionary would replace every other dictionary slice with the partial one's).
func SetModelPhotoTechCardID(ctx context.Context, id int) {
	s, _, err := registry.getForWrite(ctx)
	if err != nil {
		writeSkipped(ctx, "SetModelPhotoTechCardID", err)
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.modelPhotoTechCardID = id
}

// IsModelPhotoTechCard reports whether id is the team's hidden system model-photo tech card.
func IsModelPhotoTechCard(ctx context.Context, id int) bool {
	cardID := GetModelPhotoTechCardID(ctx)
	return cardID > 0 && id == cardID
}

// ─── Promos ────────────────────────────────────────────────────────────────────────────────────

func UpdatePromos(ctx context.Context, promos []entity.PromoCode) {
	s, _, err := registry.getForWrite(ctx)
	if err != nil {
		writeSkipped(ctx, "UpdatePromos", err)
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.promos = make(map[string]entity.PromoCode, len(promos))
	for _, p := range promos {
		s.promos[p.Code] = p
	}
}

func AddPromo(ctx context.Context, p entity.PromoCode) {
	s, _, err := registry.getForWrite(ctx)
	if err != nil {
		writeSkipped(ctx, "AddPromo", err)
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.promos[p.Code] = p
}

func GetPromoById(ctx context.Context, id int) (entity.PromoCode, bool) {
	s := registry.get(ctx)
	if s == nil {
		return entity.PromoCode{}, false
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, p := range s.promos {
		if p.Id == id {
			return p, true
		}
	}
	return entity.PromoCode{}, false
}

func DeletePromo(ctx context.Context, code string) {
	s, _, err := registry.getForWrite(ctx)
	if err != nil {
		writeSkipped(ctx, "DeletePromo", err)
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.promos, code)
}

func DisablePromo(ctx context.Context, code string) { setPromoAllowed(ctx, code, false) }

func EnablePromo(ctx context.Context, code string) { setPromoAllowed(ctx, code, true) }

func setPromoAllowed(ctx context.Context, code string, allowed bool) {
	s, _, err := registry.getForWrite(ctx)
	if err != nil {
		writeSkipped(ctx, "setPromoAllowed", err)
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if p, ok := s.promos[code]; ok {
		p.Allowed = allowed
		s.promos[code] = p
	}
}

func GetPromoByCode(ctx context.Context, code string) (entity.PromoCode, bool) {
	s := registry.get(ctx)
	if s == nil {
		return entity.PromoCode{}, false
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	p, ok := s.promos[code]
	return p, ok
}

// ─── Shipment carriers ─────────────────────────────────────────────────────────────────────────

func UpdateShipmentCarriers(ctx context.Context, scs []entity.ShipmentCarrier) {
	s, _, err := registry.getForWrite(ctx)
	if err != nil {
		writeSkipped(ctx, "UpdateShipmentCarriers", err)
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.carriers = make(map[int]entity.ShipmentCarrier, len(scs))
	for _, sc := range scs {
		s.carriers[sc.Id] = sc
	}
}

func GetShipmentCarrierById(ctx context.Context, id int) (entity.ShipmentCarrier, bool) {
	s := registry.get(ctx)
	if s == nil {
		return entity.ShipmentCarrier{}, false
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	sc, ok := s.carriers[id]
	return sc, ok
}

// CarrierLookup resolves a shipment carrier by id; ShipmentCarrierLookup hands one team's carriers
// to code that has no ctx (internal/dto mail builders).
type CarrierLookup func(id int) (entity.ShipmentCarrier, bool)

// ShipmentCarrierLookup returns a lookup over a copy of the carriers of the team in ctx.
func ShipmentCarrierLookup(ctx context.Context) CarrierLookup {
	s := registry.get(ctx)
	if s == nil {
		return func(int) (entity.ShipmentCarrier, bool) { return entity.ShipmentCarrier{}, false }
	}
	s.mu.RLock()
	m := make(map[int]entity.ShipmentCarrier, len(s.carriers))
	for id, sc := range s.carriers {
		m[id] = sc
	}
	s.mu.RUnlock()
	return func(id int) (entity.ShipmentCarrier, bool) {
		sc, ok := m[id]
		return sc, ok
	}
}

func UpdateShipmentCarrierAllowance(ctx context.Context, carrier string, allowed bool) {
	s, _, err := registry.getForWrite(ctx)
	if err != nil {
		writeSkipped(ctx, "UpdateShipmentCarrierAllowance", err)
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, sc := range s.carriers {
		if sc.Carrier == carrier {
			sc.Allowed = allowed
			s.carriers[sc.Id] = sc
			break
		}
	}
}

// UpdateShipmentCarrierCost is deprecated. Use UpdateShipmentCarriers instead.
func UpdateShipmentCarrierCost(ctx context.Context, carrier string, price decimal.Decimal) {
	s, _, err := registry.getForWrite(ctx)
	if err != nil {
		writeSkipped(ctx, "UpdateShipmentCarrierCost", err)
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, sc := range s.carriers {
		if sc.Carrier == carrier {
			// Update all prices to the same value (for backward compatibility)
			for i := range sc.Prices {
				sc.Prices[i].Price = price
			}
			s.carriers[sc.Id] = sc
		}
	}
}

func GetShipmentCarriers(ctx context.Context) []entity.ShipmentCarrier {
	s := registry.get(ctx)
	if s == nil {
		return []entity.ShipmentCarrier{}
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	scs := make([]entity.ShipmentCarrier, 0, len(s.carriers))
	for _, sc := range s.carriers {
		scs = append(scs, sc)
	}
	return scs
}

// ─── Payment methods (allowance per team; identity is process-wide) ───────────────────────────

func GetPaymentMethodById(ctx context.Context, id int) (PaymentMethod, bool) {
	s := registry.get(ctx)
	if s == nil {
		return PaymentMethod{}, false
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	name, ok := s.payByID[id]
	if !ok {
		return PaymentMethod{}, false
	}
	pm, ok := s.payByName[name]
	return pm, ok
}

func GetPaymentMethodByName(ctx context.Context, n entity.PaymentMethodName) (PaymentMethod, bool) {
	s := registry.get(ctx)
	if s == nil {
		return PaymentMethod{}, false
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	pm, ok := s.payByName[n]
	return pm, ok
}

func UpdatePaymentMethodAllowance(ctx context.Context, method entity.PaymentMethodName, allowed bool) {
	s, _, err := registry.getForWrite(ctx)
	if err != nil {
		writeSkipped(ctx, "UpdatePaymentMethodAllowance", err)
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	pm, ok := s.payByName[method]
	if !ok {
		return
	}
	pm.Method.Allowed = allowed
	s.payByName[method] = pm
	s.payByID[pm.Method.Id] = method
}

// RefreshEntityPaymentMethods rebuilds the team's payment-method list from its id table.
func RefreshEntityPaymentMethods(ctx context.Context) {
	s, _, err := registry.getForWrite(ctx)
	if err != nil {
		writeSkipped(ctx, "RefreshEntityPaymentMethods", err)
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	// Build a fresh slice rather than reusing the backing array, so concurrent
	// readers holding a previously returned slice never observe in-place mutation.
	refreshed := make([]entity.PaymentMethod, 0, len(s.payByID))
	for _, name := range s.payByID {
		refreshed = append(refreshed, s.payByName[name].Method)
	}
	s.payList = refreshed
}

func GetPaymentMethods(ctx context.Context) []entity.PaymentMethod {
	s := registry.get(ctx)
	if s == nil {
		return nil
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.payList
}

// GetPaymentMethodsFilteredByIsProd returns payment methods for checkout: only CARD if isProd, only
// CARD_TEST if !isProd. Respects the Allowed flag for each method.
func GetPaymentMethodsFilteredByIsProd(ctx context.Context) []entity.PaymentMethod {
	target := entity.CARD_TEST
	if GetPaymentIsProd(ctx) {
		target = entity.CARD
	}
	result := make([]entity.PaymentMethod, 0, 1)
	s := registry.get(ctx)
	if s == nil {
		return result
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, pm := range s.payList {
		if pm.Name == target && pm.Allowed {
			result = append(result, pm)
			break
		}
	}
	return result
}

// ─── Hero, announce, storefront settings ───────────────────────────────────────────────────────

func UpdateHero(ctx context.Context, hf *entity.HeroFullWithTranslations) {
	s, _, err := registry.getForWrite(ctx)
	if err != nil {
		writeSkipped(ctx, "UpdateHero", err)
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.hero = hf
}

// GetHero returns the team's resolved hero (an empty one with no team).
func GetHero(ctx context.Context) *entity.HeroFullWithTranslations {
	s := registry.get(ctx)
	if s == nil {
		return &entity.HeroFullWithTranslations{}
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.hero
}

// GetBackgroundHeroColor returns the storefront hero background CSS color (may be empty).
func GetBackgroundHeroColor(ctx context.Context) string {
	s := registry.get(ctx)
	if s == nil {
		return ""
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.heroBg
}

// SetBackgroundHeroColor updates the team's cached hero background color.
func SetBackgroundHeroColor(ctx context.Context, color string) {
	s, _, err := registry.getForWrite(ctx)
	if err != nil {
		writeSkipped(ctx, "SetBackgroundHeroColor", err)
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.heroBg = color
}

func SetAnnounce(ctx context.Context, link string, translations []entity.AnnounceTranslation) {
	s, _, err := registry.getForWrite(ctx)
	if err != nil {
		writeSkipped(ctx, "SetAnnounce", err)
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.announce = &entity.AnnounceWithTranslations{
		Link:         link,
		Translations: translations,
	}
}

// GetAnnounce returns the team's announce (an empty one with no team).
func GetAnnounce(ctx context.Context) *entity.AnnounceWithTranslations {
	s := registry.get(ctx)
	if s == nil {
		return &entity.AnnounceWithTranslations{}
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.announce
}

func UpdateComplimentaryShippingPrices(ctx context.Context, prices map[string]decimal.Decimal) {
	s, _, err := registry.getForWrite(ctx)
	if err != nil {
		writeSkipped(ctx, "UpdateComplimentaryShippingPrices", err)
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.complimentary = prices
}

// GetComplimentaryShippingPrices returns a copy of the team's prices (never nil).
func GetComplimentaryShippingPrices(ctx context.Context) map[string]decimal.Decimal {
	result := make(map[string]decimal.Decimal)
	s := registry.get(ctx)
	if s == nil {
		return result
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	for k, v := range s.complimentary {
		result[k] = v
	}
	return result
}

// SetTargetMarginPct updates the team's cached house margin target (alert_setting
// `target_margin_pct`). Called at boot / on load and again whenever UpsertAlertSettings changes it; a
// non-positive value is ignored so a partial write cannot silently turn every style's target into 0%.
func SetTargetMarginPct(ctx context.Context, v float64) {
	if v <= 0 {
		return
	}
	s, _, err := registry.getForWrite(ctx)
	if err != nil {
		writeSkipped(ctx, "SetTargetMarginPct", err)
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.targetMargin = v
}

// GetTargetMarginPct returns the team's house gross-margin target; the built-in default before the
// first load, after a failed one, or with no team (never another team's number, never 0).
func GetTargetMarginPct(ctx context.Context) float64 {
	s := registry.get(ctx)
	if s == nil {
		return entity.DefaultAlertThresholds().TargetMarginPct
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.targetMargin
}

// Memory-only storefront settings (the admin sets them, nothing persists them). A setter returns
// ErrNoTenant with no team in teams mode; a getter then returns the zero value.

func SetMaxOrderItems(ctx context.Context, n int) error {
	return registry.writeSettings(ctx, func(st *teamSettings) { st.maxOrderItems = n })
}

func GetMaxOrderItems(ctx context.Context) int {
	st, _ := registry.readSettings(ctx)
	return st.maxOrderItems
}

func SetBigMenu(ctx context.Context, enabled bool) error {
	return registry.writeSettings(ctx, func(st *teamSettings) { st.bigMenu = enabled })
}

func GetBigMenu(ctx context.Context) bool {
	st, _ := registry.readSettings(ctx)
	return st.bigMenu
}

func SetSiteAvailability(ctx context.Context, enabled bool) error {
	return registry.writeSettings(ctx, func(st *teamSettings) { st.siteEnabled = enabled })
}

func GetSiteAvailability(ctx context.Context) bool {
	st, _ := registry.readSettings(ctx)
	return st.siteEnabled
}

func SetOrderExpirationSeconds(ctx context.Context, seconds int) error {
	return registry.writeSettings(ctx, func(st *teamSettings) { st.orderExpirationSeconds = seconds })
}

func GetOrderExpirationSeconds(ctx context.Context) int {
	st, _ := registry.readSettings(ctx)
	return st.orderExpirationSeconds
}

func SetPaymentIsProd(ctx context.Context, isProd bool) error {
	return registry.writeSettings(ctx, func(st *teamSettings) { st.paymentIsProd = isProd })
}

func GetPaymentIsProd(ctx context.Context) bool {
	st, _ := registry.readSettings(ctx)
	return st.paymentIsProd
}
