package betaseed

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"strconv"
	"strings"
	"time"

	"google.golang.org/protobuf/types/known/timestamppb"

	decimal "google.golang.org/genproto/googleapis/type/decimal"

	admin "github.com/jekabolt/grbpwr-manager/proto/gen/admin"
	common "github.com/jekabolt/grbpwr-manager/proto/gen/common"
	frontend "github.com/jekabolt/grbpwr-manager/proto/gen/frontend"
)

// ExtrasResult summarises everything SeedExtras created/exercised, so a caller
// or a later verify phase can find the seeded entities. Passwords are never
// stored or printed.
type ExtrasResult struct {
	PromoCodes         []string // AddPromo codes (incl. the disabled + expired ones)
	ModelIDs           []int32  // AddModel ids
	TaskIDs            []int32  // AddTask ids
	AccountUsernames   []string // CreateAccount usernames (no passwords)
	ColorCodes         []string // dictionary colours ensured
	TagNames           []string // dictionary tags ensured
	CountriesActivated []string // SetCountryActive codes
	CarrierIDs         []int32  // AddShipmentCarrier ids
	MemberEmails       []string // storefront members created via SFSubscribeNewsletter
	MemberUserIDs      []int64  // resolved member user ids
	HackerInviteIDs    []int64  // GenerateHackerInvite ids
	SupportTicketIDs   []int32  // support tickets touched by admin update
	ReviewOrderUUIDs   []string // delivered orders that carry a submitted review
	Warnings           []string // per-domain soft failures / documented gaps
}

// SeedExtras populates every currently-empty admin section on beta. Each domain
// is independent: a failure is logged, recorded on the result, and the remaining
// domains still run. It returns the populated result plus a non-nil error when
// any domain failed (so a dev run surfaces regressions loudly).
func (s *Seeder) SeedExtras(ctx context.Context) (*ExtrasResult, error) {
	r := &ExtrasResult{}
	steps := []struct {
		name string
		fn   func(context.Context, *ExtrasResult) error
	}{
		{"promos", s.seedPromos},
		{"models", s.seedModels},
		{"tasks", s.seedTasks},
		{"accounts", s.seedAccounts},
		{"dictionaries", s.seedDictionaries},
		{"platform", s.seedPlatformConfig},
		{"members", s.seedMembers},
		{"support", s.seedSupportTickets},
		{"reviews", s.seedReviews},
	}
	var failed []string
	for _, st := range steps {
		s.logf("=== SeedExtras: %s ===", st.name)
		if err := st.fn(ctx, r); err != nil {
			s.logf("ERROR %s: %v", st.name, err)
			r.Warnings = append(r.Warnings, fmt.Sprintf("%s: %v", st.name, err))
			failed = append(failed, st.name)
		}
	}
	if len(failed) > 0 {
		return r, fmt.Errorf("SeedExtras: %d domain(s) failed: %s", len(failed), strings.Join(failed, ", "))
	}
	return r, nil
}

// scaleN returns a Volume-scaled count.
func (s *Seeder) scaleN(single, moderate, dense int) int {
	switch s.Vol {
	case VolDense:
		return dense
	case VolModerate:
		return moderate
	default:
		return single
	}
}

// isAlreadyExists reports whether err is a gateway conflict / duplicate-key
// response, so a reuse-or-create step can treat "already there" as success.
func isAlreadyExists(err error) bool {
	e, ok := AsAPIError(err)
	if !ok {
		return false
	}
	return e.Code == 409 || strings.Contains(e.Body, "Duplicate entry") || strings.Contains(e.Body, "already exists")
}

// isAlreadyArchived reports whether err is a "row is already archived / not found to archive" response,
// so an idempotent archive step (a throwaway row archived on a prior run) can treat it as success.
func isAlreadyArchived(err error) bool {
	e, ok := AsAPIError(err)
	if !ok {
		return false
	}
	return strings.Contains(e.Body, "already archived") || strings.Contains(e.Body, "not found or already archived")
}

// randPassword builds a strong, never-logged admin password (>= 8 chars, mixed
// classes). It is created, hashed server-side, and discarded here.
func randPassword() string {
	b := make([]byte, 18)
	if _, err := rand.Read(b); err != nil {
		return "Sd1!" + strconv.FormatInt(time.Now().UnixNano(), 36) + "Xy9"
	}
	return "Sd1!" + base64.RawURLEncoding.EncodeToString(b)
}

// ---------------------------------------------------------------- 1. promos

func (s *Seeder) seedPromos(ctx context.Context, r *ExtrasResult) error {
	now := time.Now()
	sfx := s.Run
	active := func(code string, disc string, freeShip bool, exp time.Time) *common.PromoCodeInsert {
		return &common.PromoCodeInsert{
			Code:         code,
			FreeShipping: freeShip,
			Discount:     decv(disc),
			Start:        timestamppb.New(now.Add(-24 * time.Hour)),
			Expiration:   timestamppb.New(exp),
			Allowed:      true,
		}
	}
	type spec struct {
		ins     *common.PromoCodeInsert
		disable bool
	}
	specs := []spec{
		{ins: active("SEEDPCT"+sfx, "15", false, now.Add(365*24*time.Hour))},                 // active 15% off
		{ins: active("SEEDSHIP"+sfx, "0", true, now.Add(365*24*time.Hour))},                  // active free shipping
		{ins: active("SEEDEXP"+sfx, "20", false, now.Add(-24*time.Hour))},                    // expired
		{ins: active("SEEDOFF"+sfx, "10", false, now.Add(365*24*time.Hour)), disable: true}, // active then disabled
	}
	for _, sp := range specs {
		if _, err := s.C.AddPromo(ctx, &admin.AddPromoRequest{Promo: sp.ins}); err != nil {
			return fmt.Errorf("AddPromo(%s): %w", sp.ins.GetCode(), err)
		}
		if sp.disable {
			if _, err := s.C.DisablePromoCode(ctx, &admin.DisablePromoCodeRequest{Code: sp.ins.GetCode()}); err != nil {
				return fmt.Errorf("DisablePromoCode(%s): %w", sp.ins.GetCode(), err)
			}
		}
		r.PromoCodes = append(r.PromoCodes, sp.ins.GetCode())
	}
	lp, err := s.C.ListPromos(ctx, &admin.ListPromosRequest{Limit: 200, OrderFactor: common.OrderFactor_ORDER_FACTOR_DESC})
	if err != nil {
		return fmt.Errorf("ListPromos: %w", err)
	}
	s.logf("promos: created %d (%v); %d total on beta", len(specs), r.PromoCodes, len(lp.GetPromoCodes()))
	return nil
}

// ---------------------------------------------------------------- 2. models

func (s *Seeder) seedModels(ctx context.Context, r *ExtrasResult) error {
	n := s.scaleN(3, 5, 8)
	genders := []common.GenderEnum{
		common.GenderEnum_GENDER_ENUM_FEMALE,
		common.GenderEnum_GENDER_ENUM_MALE,
		common.GenderEnum_GENDER_ENUM_UNISEX,
	}
	var defSizes []int32
	if id, err := s.SizeIDByName("m"); err == nil {
		defSizes = []int32{id}
	}
	for i := 0; i < n; i++ {
		thumb, err := s.UploadJPEG(ctx, fmt.Sprintf("model-%s-%02d", s.Run, i+1))
		if err != nil {
			return fmt.Errorf("UploadJPEG(model %d): %w", i, err)
		}
		mi := &common.ModelInsert{
			Name:        fmt.Sprintf("Seed Model %s-%02d", s.Run, i+1),
			Comment:     "beta seed showroom model",
			Gender:      genders[i%len(genders)],
			ThumbnailId: thumb,
			MediaIds:    []int32{thumb},
			Measurements: []*common.ModelMeasurement{
				{Name: common.BodyMeasurementName_BODY_MEASUREMENT_NAME_ACROSS_SHOULDER, ValueMm: 400 + int32(i*5)},
				{Name: common.BodyMeasurementName_BODY_MEASUREMENT_NAME_INSEAM, ValueMm: 800 + int32(i*10)},
			},
			DefaultSizeIds: defSizes,
		}
		resp, err := s.C.AddModel(ctx, &admin.AddModelRequest{Model: mi})
		if err != nil {
			return fmt.Errorf("AddModel(%d): %w", i, err)
		}
		r.ModelIDs = append(r.ModelIDs, resp.GetId())
	}
	if len(r.ModelIDs) > 0 {
		id := r.ModelIDs[0]
		thumb, err := s.UploadJPEG(ctx, "model-"+s.Run+"-upd")
		if err != nil {
			return fmt.Errorf("UploadJPEG(model update): %w", err)
		}
		if _, err := s.C.UpdateModel(ctx, &admin.UpdateModelRequest{Id: id, Model: &common.ModelInsert{
			Name:        fmt.Sprintf("Seed Model %s-01 (updated)", s.Run),
			Comment:     "beta seed showroom model — updated",
			Gender:      common.GenderEnum_GENDER_ENUM_FEMALE,
			ThumbnailId: thumb,
			MediaIds:    []int32{thumb},
			Measurements: []*common.ModelMeasurement{
				{Name: common.BodyMeasurementName_BODY_MEASUREMENT_NAME_ACROSS_SHOULDER, ValueMm: 420},
			},
			DefaultSizeIds: defSizes,
		}}); err != nil {
			return fmt.Errorf("UpdateModel(%d): %w", id, err)
		}
	}
	lm, err := s.C.ListModels(ctx, &admin.ListModelsRequest{Limit: 200})
	if err != nil {
		return fmt.Errorf("ListModels: %w", err)
	}
	s.logf("models: created %d, updated 1; %d total on beta", n, lm.GetTotal())
	return nil
}

// ---------------------------------------------------------------- 3. tasks

func (s *Seeder) seedTasks(ctx context.Context, r *ExtrasResult) error {
	n := s.scaleN(6, 10, 18)
	boards := []common.TaskBoard{
		common.TaskBoard_TASK_BOARD_DEVELOPMENT,
		common.TaskBoard_TASK_BOARD_DESIGN,
		common.TaskBoard_TASK_BOARD_MARKETING,
		common.TaskBoard_TASK_BOARD_PRODUCTION,
		common.TaskBoard_TASK_BOARD_SOURCING,
		common.TaskBoard_TASK_BOARD_CONTENT,
	}
	// Create across the pre-DONE columns; DONE is reached below via MoveTask so a
	// card lands there through a real transition.
	statuses := []common.TaskStatus{
		common.TaskStatus_TASK_STATUS_BACKLOG,
		common.TaskStatus_TASK_STATUS_TODO,
		common.TaskStatus_TASK_STATUS_IN_PROGRESS,
		common.TaskStatus_TASK_STATUS_REVIEW,
	}
	prios := []common.TaskPriority{
		common.TaskPriority_TASK_PRIORITY_LOW,
		common.TaskPriority_TASK_PRIORITY_MEDIUM,
		common.TaskPriority_TASK_PRIORITY_HIGH,
		common.TaskPriority_TASK_PRIORITY_URGENT,
	}
	var firstID int32
	for i := 0; i < n; i++ {
		ti := &common.TaskInsert{
			Title:       fmt.Sprintf("Seed task %s-%02d", s.Run, i+1),
			Description: "beta seed kanban card — representative work item",
			Priority:    prios[i%len(prios)],
			Labels:      []string{"beta-seed", strings.ToLower(boards[i%len(boards)].String())},
			DueDate:     timestamppb.New(time.Now().Add(time.Duration(i+2) * 24 * time.Hour)),
		}
		resp, err := s.C.AddTask(ctx, &admin.AddTaskRequest{
			Task:   ti,
			Board:  boards[i%len(boards)],
			Status: statuses[i%len(statuses)],
		})
		if err != nil {
			return fmt.Errorf("AddTask(%d): %w", i, err)
		}
		r.TaskIDs = append(r.TaskIDs, resp.GetId())
		if i == 0 {
			firstID = resp.GetId()
		}
	}
	if firstID != 0 {
		// Move the first card into DONE (keep its board: UNKNOWN board = keep current).
		if _, err := s.C.MoveTask(ctx, &admin.MoveTaskRequest{
			Id:       firstID,
			Board:    common.TaskBoard_TASK_BOARD_UNKNOWN,
			Status:   common.TaskStatus_TASK_STATUS_DONE,
			Position: 0,
		}); err != nil {
			return fmt.Errorf("MoveTask(%d): %w", firstID, err)
		}
		if _, err := s.C.AddTaskComment(ctx, &admin.AddTaskCommentRequest{
			Comment: &common.TaskCommentInsert{TaskId: firstID, Body: "beta seed comment: wrapped up, moving to done"},
		}); err != nil {
			return fmt.Errorf("AddTaskComment(%d): %w", firstID, err)
		}
		ci, err := s.C.AddTaskChecklistItem(ctx, &admin.AddTaskChecklistItemRequest{TaskId: firstID, Content: "draft the spec"})
		if err != nil {
			return fmt.Errorf("AddTaskChecklistItem(%d): %w", firstID, err)
		}
		if _, err := s.C.AddTaskChecklistItem(ctx, &admin.AddTaskChecklistItemRequest{TaskId: firstID, Content: "review with the team"}); err != nil {
			return fmt.Errorf("AddTaskChecklistItem2(%d): %w", firstID, err)
		}
		if _, err := s.C.SetTaskChecklistItemDone(ctx, &admin.SetTaskChecklistItemDoneRequest{Id: ci.GetId(), IsDone: true}); err != nil {
			return fmt.Errorf("SetTaskChecklistItemDone(%d): %w", ci.GetId(), err)
		}
	}
	lt, err := s.C.ListTasks(ctx, &admin.ListTasksRequest{Limit: 500})
	if err != nil {
		return fmt.Errorf("ListTasks: %w", err)
	}
	s.logf("tasks: created %d, moved+commented+checklisted 1; %d total on beta", n, lt.GetTotal())
	return nil
}

// ---------------------------------------------------------------- 4. accounts

func (s *Seeder) seedAccounts(ctx context.Context, r *ExtrasResult) error {
	sec, err := s.C.ListAccountSections(ctx, &admin.ListAccountSectionsRequest{})
	if err != nil {
		return fmt.Errorf("ListAccountSections: %w", err)
	}
	var keys []string
	for _, si := range sec.GetSections() {
		if k := si.GetKey(); k != "" {
			keys = append(keys, k)
		}
	}
	if len(keys) == 0 {
		return fmt.Errorf("ListAccountSections returned no section keys")
	}

	// Account 1: read-only across every section.
	u1 := "seed-viewer-" + s.Run
	perms1 := make([]*admin.AdminPermission, 0, len(keys))
	for _, k := range keys {
		perms1 = append(perms1, &admin.AdminPermission{Section: k, Access: admin.AccessLevel_ACCESS_LEVEL_READ})
	}
	if _, err := s.C.CreateAccount(ctx, &admin.CreateAccountRequest{Username: u1, Password: randPassword(), Permissions: perms1}); err != nil {
		return fmt.Errorf("CreateAccount(%s): %w", u1, err)
	}
	r.AccountUsernames = append(r.AccountUsernames, u1)

	// Account 2: write on the first few sections only.
	u2 := "seed-editor-" + s.Run
	var perms2 []*admin.AdminPermission
	for i, k := range keys {
		if i >= 4 {
			break
		}
		perms2 = append(perms2, &admin.AdminPermission{Section: k, Access: admin.AccessLevel_ACCESS_LEVEL_WRITE})
	}
	if _, err := s.C.CreateAccount(ctx, &admin.CreateAccountRequest{Username: u2, Password: randPassword(), Permissions: perms2}); err != nil {
		return fmt.Errorf("CreateAccount(%s): %w", u2, err)
	}
	r.AccountUsernames = append(r.AccountUsernames, u2)

	// Promote account 1's first section to write.
	perms1b := make([]*admin.AdminPermission, len(perms1))
	copy(perms1b, perms1)
	perms1b[0] = &admin.AdminPermission{Section: keys[0], Access: admin.AccessLevel_ACCESS_LEVEL_WRITE}
	if _, err := s.C.UpdateAccountPermissions(ctx, &admin.UpdateAccountPermissionsRequest{Username: u1, Permissions: perms1b}); err != nil {
		return fmt.Errorf("UpdateAccountPermissions(%s): %w", u1, err)
	}

	// Disable account 2.
	if _, err := s.C.SetAccountDisabled(ctx, &admin.SetAccountDisabledRequest{Username: u2, Disabled: true}); err != nil {
		return fmt.Errorf("SetAccountDisabled(%s): %w", u2, err)
	}
	s.logf("accounts: created %s (read→write on %q) and %s (write, then disabled)", u1, keys[0], u2)
	return nil
}

// ---------------------------------------------------------------- 5. dictionaries

func (s *Seeder) seedDictionaries(ctx context.Context, r *ExtrasResult) error {
	// expected_version 0 bypasses the optimistic-lock check (entity.CheckExpectedRevision),
	// so no per-namespace revision tracking is needed here.
	haveColor := map[string]bool{}
	for _, c := range s.Dict.GetColors() {
		haveColor[strings.ToUpper(c.GetCode())] = true
	}
	colors := []struct{ code, name, hex string }{
		{"SEA", "Seafoam", "#7FFFD4"},
		{"MUS", "Mustard", "#FFDB58"},
		{"TRQ", "Turquoise", "#40E0D0"},
	}
	for _, c := range colors {
		if !haveColor[c.code] {
			if _, err := s.C.CreateColor(ctx, &admin.CreateColorRequest{Code: c.code, Name: c.name, Hex: c.hex}); err != nil && !isAlreadyExists(err) {
				return fmt.Errorf("CreateColor(%s): %w", c.code, err)
			}
		}
		r.ColorCodes = append(r.ColorCodes, c.code)
	}

	haveTag := map[string]bool{}
	for _, t := range s.Dict.GetTags() {
		haveTag[strings.ToLower(t.GetName())] = true
	}
	tags := []string{"editorial", "runway", "restock"}
	for _, name := range tags {
		if !haveTag[strings.ToLower(name)] {
			if _, err := s.C.CreateTag(ctx, &admin.CreateTagRequest{Name: name}); err != nil && !isAlreadyExists(err) {
				return fmt.Errorf("CreateTag(%s): %w", name, err)
			}
		}
		r.TagNames = append(r.TagNames, name)
	}

	// Activate countries that actually exist in THIS environment's dictionary
	// (hardcoding ISO codes 404s — the server rejects codes it doesn't have).
	// Prefer currently-inactive ones; all-active is fine, SetCountryActive is
	// idempotent and the goal is simply non-empty country dropdowns. There is no
	// create-country RPC (SetCountryActive only toggles existing rows), so an empty
	// country dictionary is a documented gap, not an error.
	countries := s.Dict.GetCountries()
	if len(countries) == 0 {
		s.logf("WARN countries: dictionary has 0 countries on this env and there is no create-country RPC (SetCountryActive only toggles existing rows) — cannot seed via REST")
		r.Warnings = append(r.Warnings, "dictionaries/countries: empty country dictionary + no create-country RPC — documented gap")
	} else {
		var toActivate []string
		pick := func(code string) {
			if code == "" || len(toActivate) >= 4 {
				return
			}
			for _, t := range toActivate {
				if t == code {
					return
				}
			}
			toActivate = append(toActivate, code)
		}
		for _, c := range countries {
			if !c.GetActive() {
				pick(c.GetCode())
			}
		}
		for _, c := range countries {
			pick(c.GetCode())
		}
		for _, code := range toActivate {
			if _, err := s.C.SetCountryActive(ctx, &admin.SetCountryActiveRequest{Code: code, Active: true}); err != nil {
				return fmt.Errorf("SetCountryActive(%s): %w", code, err)
			}
			r.CountriesActivated = append(r.CountriesActivated, code)
		}
	}
	s.logf("dictionaries: colours=%v tags=%v countries-active=%v", r.ColorCodes, r.TagNames, r.CountriesActivated)
	return nil
}

// ---------------------------------------------------------------- 6. platform config

func (s *Seeder) seedPlatformConfig(ctx context.Context, r *ExtrasResult) error {
	// AddShipmentCarrier validates against currency.RequiredCurrencies() — all 7,
	// INCLUDING PLN (the proto comment listing 6 is stale). Build the map from the
	// same proven price source so it never drifts.
	prices := map[string]*decimal.Decimal{}
	for _, p := range s.Prices() {
		prices[p.GetCurrency()] = p.GetPrice()
	}
	// tracking_url must carry a %s placeholder where the tracking code is substituted.
	carriers := []struct{ name, url, eta, slug string }{
		{"DHL Express", "https://www.dhl.com/track?id=%s", "1-3 business days", "dhl"},
		{"UPS", "https://www.ups.com/track?tracknum=%s", "2-5 business days", "ups"},
		{"FedEx", "https://www.fedex.com/fedextrack/?trknbr=%s", "2-4 business days", "fedex"},
	}
	for _, c := range carriers {
		resp, err := s.C.AddShipmentCarrier(ctx, &admin.AddShipmentCarrierRequest{
			Carrier:              fmt.Sprintf("%s (seed %s)", c.name, s.Run),
			Prices:               prices,
			TrackingUrl:          c.url,
			ExpectedDeliveryTime: c.eta,
			Description:          "beta seed carrier",
			Allowed:              true,
			AftershipSlug:        c.slug,
		})
		if err != nil {
			return fmt.Errorf("AddShipmentCarrier(%s): %w", c.name, err)
		}
		r.CarrierIDs = append(r.CarrierIDs, resp.GetId())
	}

	// Only CARD / CARD_TEST are valid fee targets (dto.ConvertPbToEntityPaymentMethod
	// maps just those two); BANK_INVOICE/CASH are rejected.
	if _, err := s.C.UpsertPaymentMethodFees(ctx, &admin.UpsertPaymentMethodFeesRequest{Fees: []*admin.PaymentMethodFee{
		{PaymentMethod: common.PaymentMethodNameEnum_PAYMENT_METHOD_NAME_ENUM_CARD, FeePct: decv("1.5"), FeeFixed: decv("0.25")},
		{PaymentMethod: common.PaymentMethodNameEnum_PAYMENT_METHOD_NAME_ENUM_CARD_TEST, FeePct: decv("1.5"), FeeFixed: decv("0.25")},
	}}); err != nil {
		return fmt.Errorf("UpsertPaymentMethodFees: %w", err)
	}

	if _, err := s.C.SetBackgroundHeroColor(ctx, &admin.SetBackgroundHeroColorRequest{Color: "#F5F5F0"}); err != nil {
		return fmt.Errorf("SetBackgroundHeroColor: %w", err)
	}

	if err := s.updateSettingsConservative(ctx); err != nil {
		return err
	}
	s.logf("platform: carriers=%v, payment fees set, hero colour set, settings updated (site kept enabled)", r.CarrierIDs)
	return nil
}

// updateSettingsConservative re-reads current settings and echoes them back,
// only forcing site-available true and max-order-items >= 10. UpdateSettings
// applies every scalar it receives (a zero request would disable the site and
// wipe the announce), so preserving the current values is mandatory. Carriers
// and payment-method allowances are left empty = no change.
func (s *Seeder) updateSettingsConservative(ctx context.Context) error {
	dr, err := s.C.GetDictionary(ctx, &admin.GetDictionaryRequest{})
	if err != nil {
		return fmt.Errorf("GetDictionary(settings): %w", err)
	}
	d := dr.GetDictionary()
	maxItems := d.GetMaxOrderItems()
	if maxItems < 10 {
		maxItems = 10
	}
	var announce *common.Announce
	if a := d.GetAnnounce(); a != nil {
		announce = &common.Announce{Link: a.GetLink()}
		for _, t := range a.GetTranslations() {
			announce.Translations = append(announce.Translations, &common.AnnounceTranslation{
				LanguageId: t.GetLanguageId(),
				Text:       t.GetText(),
			})
		}
	}
	// Scalar fields are optional (presence) now — echo the current dictionary values as explicit
	// pointers so this full re-apply is a no-op except SiteAvailable=true (the seeder keeps beta up).
	if _, err := s.C.UpdateSettings(ctx, &admin.UpdateSettingsRequest{
		SiteAvailable:               pbool(true),
		MaxOrderItems:               p32(maxItems),
		BigMenu:                     pbool(d.GetBigMenu()),
		Announce:                    announce,
		OrderExpirationSeconds:      p32(d.GetOrderExpirationSeconds()),
		IsProd:                      pbool(d.GetIsProd()),
		ComplimentaryShippingPrices: d.GetComplimentaryShippingPrices(),
	}); err != nil {
		return fmt.Errorf("UpdateSettings: %w", err)
	}
	return nil
}

// ---------------------------------------------------------------- 7. members / loyalty

func (s *Seeder) seedMembers(ctx context.Context, r *ExtrasResult) error {
	n := s.scaleN(3, 6, 10)
	prefs := []frontend.ShoppingPreferenceEnum{
		frontend.ShoppingPreferenceEnum_SHOPPING_PREFERENCE_ENUM_ALL,
		frontend.ShoppingPreferenceEnum_SHOPPING_PREFERENCE_ENUM_MALE,
		frontend.ShoppingPreferenceEnum_SHOPPING_PREFERENCE_ENUM_FEMALE,
	}
	for i := 0; i < n; i++ {
		email := fmt.Sprintf("seed-member-%s-%02d@grbpwr.com", s.Run, i+1)
		// Only the first opts into newsletter (fires a welcome email; treated as
		// best-effort so a mail hiccup can't drop the created account).
		if _, err := s.C.SFSubscribeNewsletter(ctx, &frontend.SubscribeNewsletterRequest{
			Email:                email,
			Name:                 fmt.Sprintf("Seed Member %02d", i+1),
			ShoppingPreference:   prefs[i%len(prefs)],
			SubscribeNewsletter:  i == 0,
			SubscribeNewArrivals: true,
			SubscribeEvents:      i%2 == 0,
		}); err != nil {
			s.logf("WARN member subscribe %s: %v (account may still have been created)", email, err)
		}
		r.MemberEmails = append(r.MemberEmails, email)
	}

	lm, err := s.C.ListMembers(ctx, &admin.ListMembersRequest{Email: "seed-member-" + s.Run, Limit: 200})
	if err != nil {
		return fmt.Errorf("ListMembers: %w", err)
	}
	byEmail := map[string]int64{}
	for _, m := range lm.GetMembers() {
		byEmail[strings.ToLower(m.GetEmail())] = m.GetUserId()
		r.MemberUserIDs = append(r.MemberUserIDs, m.GetUserId())
	}

	if len(r.MemberEmails) > 0 {
		if uid := byEmail[strings.ToLower(r.MemberEmails[0])]; uid != 0 {
			if _, err := s.C.OverrideTier(ctx, &admin.OverrideTierRequest{
				UserId:  uid,
				NewTier: admin.TierCode_TIER_CODE_PLUS,
				Reason:  "beta seed: promote to grbpwr+ for demo visibility",
			}); err != nil {
				return fmt.Errorf("OverrideTier(%d): %w", uid, err)
			}
		}
	}
	if len(r.MemberEmails) > 1 {
		if uid := byEmail[strings.ToLower(r.MemberEmails[1])]; uid != 0 {
			if _, err := s.C.SetMemberStatus(ctx, &admin.SetMemberStatusRequest{UserId: uid, Status: "frozen"}); err != nil {
				return fmt.Errorf("SetMemberStatus(%d): %w", uid, err)
			}
		}
	}

	// Exercise tier config read + write (echo unchanged: only editable fields apply).
	tc, err := s.C.GetTierConfig(ctx, &admin.GetTierConfigRequest{})
	if err != nil {
		return fmt.Errorf("GetTierConfig: %w", err)
	}
	if len(tc.GetEntries()) > 0 {
		if _, err := s.C.UpdateTierConfig(ctx, &admin.UpdateTierConfigRequest{Entries: tc.GetEntries()}); err != nil {
			return fmt.Errorf("UpdateTierConfig: %w", err)
		}
	}

	hi, err := s.C.GenerateHackerInvite(ctx, &admin.GenerateHackerInviteRequest{
		Email:         fmt.Sprintf("seed-hacker-%s@grbpwr.com", s.Run),
		ExpiresInDays: 14,
	})
	if err != nil {
		return fmt.Errorf("GenerateHackerInvite: %w", err)
	}
	r.HackerInviteIDs = append(r.HackerInviteIDs, hi.GetInviteId())
	if _, err := s.C.ListHackerInvites(ctx, &admin.ListHackerInvitesRequest{ActiveOnly: true}); err != nil {
		return fmt.Errorf("ListHackerInvites: %w", err)
	}
	if _, err := s.C.GetTierAuditLog(ctx, &admin.GetTierAuditLogRequest{Limit: 50}); err != nil {
		return fmt.Errorf("GetTierAuditLog: %w", err)
	}
	s.logf("members: created %d (resolved %d user ids); tier override + status set; hacker invite id=%d",
		n, len(r.MemberUserIDs), hi.GetInviteId())
	return nil
}

// ---------------------------------------------------------------- 8. support tickets

func (s *Seeder) seedSupportTickets(ctx context.Context, r *ExtrasResult) error {
	n := s.scaleN(3, 4, 6)
	topics := []string{"order", "product", "shipping"}
	for i := 0; i < n; i++ {
		email := fmt.Sprintf("seed-support-%s-%02d@grbpwr.com", s.Run, i+1)
		if _, err := s.C.SFSubmitSupportTicket(ctx, &frontend.SubmitSupportTicketRequest{Ticket: &common.SupportTicketInsert{
			Topic:     topics[i%len(topics)],
			Subject:   fmt.Sprintf("Seed ticket %02d: question about my order", i+1),
			Email:     email,
			FirstName: "Seed",
			LastName:  fmt.Sprintf("Customer%02d", i+1),
			Notes:     "This is a beta seed support ticket, created to populate the support inbox.",
			Category:  "general",
			Priority:  common.SupportTicketPriority_SUPPORT_TICKET_PRIORITY_MEDIUM,
		}}); err != nil {
			return fmt.Errorf("SFSubmitSupportTicket(%s): %w", email, err)
		}
	}

	gp, err := s.C.GetSupportTicketsPaged(ctx, &admin.GetSupportTicketsPagedRequest{Limit: 200})
	if err != nil {
		return fmt.Errorf("GetSupportTicketsPaged: %w", err)
	}
	updated := 0
	for _, t := range gp.GetTickets() {
		if !strings.Contains(t.GetSupportTicketInsert().GetEmail(), "seed-support-"+s.Run) {
			continue
		}
		id := t.GetId()
		switch updated {
		case 0:
			notes := "beta seed: triaged, investigating"
			if _, err := s.C.UpdateSupportTicketStatus(ctx, &admin.UpdateSupportTicketStatusRequest{
				Id:            id,
				Status:        common.SupportTicketStatus_SUPPORT_TICKET_STATUS_IN_PROGRESS,
				InternalNotes: pstr(notes),
			}); err != nil {
				return fmt.Errorf("UpdateSupportTicketStatus(%d): %w", id, err)
			}
		case 1:
			pr := common.SupportTicketPriority_SUPPORT_TICKET_PRIORITY_HIGH
			cat := "billing"
			notes := "beta seed: escalated priority"
			if _, err := s.C.UpdateSupportTicket(ctx, &admin.UpdateSupportTicketRequest{
				Id:            id,
				Priority:      &pr,
				Category:      &cat,
				InternalNotes: pstr(notes),
			}); err != nil {
				return fmt.Errorf("UpdateSupportTicket(%d): %w", id, err)
			}
		}
		r.SupportTicketIDs = append(r.SupportTicketIDs, id)
		updated++
		if updated >= 2 {
			break
		}
	}
	s.logf("support: created %d tickets, admin-updated %d", n, updated)
	return nil
}

// ---------------------------------------------------------------- 9. reviews

type stockedVariant struct {
	colorwayID int32
	variantID  int64
}

// discoverStockedVariants finds up to want active colourways, each yielding one
// variant with stock (topping the variant up to 10 when it is running low so the
// review order — and re-runs — always have inventory to reserve).
func (s *Seeder) discoverStockedVariants(ctx context.Context, want int) ([]stockedVariant, error) {
	cw, err := s.C.GetColorwaysPaged(ctx, &admin.GetColorwaysPagedRequest{
		Limit:    30,
		Statuses: []common.ColorwayLifecycleStatus{common.ColorwayLifecycleStatus_COLORWAY_LIFECYCLE_STATUS_ACTIVE},
	})
	if err != nil {
		return nil, fmt.Errorf("GetColorwaysPaged: %w", err)
	}
	var out []stockedVariant
	for _, c := range cw.GetColorways() {
		if len(out) >= want {
			break
		}
		full, err := s.C.GetColorwayByID(ctx, &admin.GetColorwayByIDRequest{ColorwayId: c.GetId()})
		if err != nil {
			continue
		}
		for _, v := range full.GetColorway().GetVariants() {
			vid := int64(v.GetVariantId())
			if decFloat(v.GetQuantity()) < 2 {
				if _, err := s.C.UpdateVariantStock(ctx, &admin.UpdateVariantStockRequest{
					Mode:      common.StockAdjustmentMode_STOCK_ADJUSTMENT_MODE_SET,
					Quantity:  10,
					Reason:    common.StockChangeReason_STOCK_CHANGE_REASON_STOCK_COUNT,
					VariantId: vid,
				}); err != nil {
					continue
				}
			}
			out = append(out, stockedVariant{colorwayID: c.GetId(), variantID: vid})
			break
		}
	}
	return out, nil
}

func (s *Seeder) seedReviews(ctx context.Context, r *ExtrasResult) error {
	variants, err := s.discoverStockedVariants(ctx, 2)
	if err != nil {
		return err
	}
	if len(variants) == 0 {
		s.logf("WARN reviews: no stocked active variant found; skipping (a review needs a delivered order)")
		r.Warnings = append(r.Warnings, "reviews: skipped — no stocked active variant to build a delivered order")
		return nil
	}
	made := 0
	for i, v := range variants {
		email := fmt.Sprintf("seed-review-%s-%02d@grbpwr.com", s.Run, i+1)
		co, err := s.C.CreateCustomOrder(ctx, &admin.CreateCustomOrderRequest{
			Items:           []*common.CustomOrderItemInsert{{Quantity: 1, VariantId: v.variantID, CustomPrice: decv("120.00")}},
			ShippingAddress: seedAddress(),
			BillingAddress:  seedAddress(),
			Buyer:           &common.BuyerInsert{FirstName: "Seed", LastName: "Reviewer", Email: email, Phone: "+49301234599"},
			PaymentMethod:   common.PaymentMethodNameEnum_PAYMENT_METHOD_NAME_ENUM_BANK_INVOICE,
			ShipmentCarrierId: s.carrierID(),
			Currency:          "eur",
		})
		if err != nil {
			return fmt.Errorf("CreateCustomOrder(review %d): %w", i, err)
		}
		uuid := co.GetOrder().GetUuid()
		if uuid == "" {
			return fmt.Errorf("CreateCustomOrder(review %d) returned empty uuid", i)
		}
		ob, err := s.C.GetOrderByUUID(ctx, &admin.GetOrderByUUIDRequest{OrderUuid: uuid})
		if err != nil {
			return fmt.Errorf("GetOrderByUUID(review %s): %w", uuid, err)
		}
		var itemReviews []*common.OrderItemReviewInsert
		for _, it := range ob.GetOrder().GetOrderItems() {
			itemReviews = append(itemReviews, &common.OrderItemReviewInsert{
				OrderItemId: it.GetId(),
				Rating:      common.ProductRatingEnum_PRODUCT_RATING_ENUM_VERY_GOOD,
				FitRating:   common.FitScaleEnum_FIT_SCALE_ENUM_TRUE_TO_SIZE,
				Recommend:   true,
			})
		}
		if _, err := s.C.SetTrackingNumber(ctx, &admin.SetTrackingNumberRequest{OrderUuid: uuid, TrackingCode: "SEED-REVIEW-" + s.Run}); err != nil {
			return fmt.Errorf("SetTrackingNumber(review %s): %w", uuid, err)
		}
		if _, err := s.C.DeliveredOrder(ctx, &admin.DeliveredOrderRequest{OrderUuid: uuid}); err != nil {
			return fmt.Errorf("DeliveredOrder(review %s): %w", uuid, err)
		}
		if _, err := s.C.SFSubmitOrderReview(ctx, &frontend.SubmitOrderReviewRequest{
			OrderUuid: uuid,
			B64Email:  base64.StdEncoding.EncodeToString([]byte(email)),
			OrderReview: &common.OrderReviewInsert{
				DeliveryRating:       common.DeliverySpeedEnum_DELIVERY_SPEED_ENUM_FASTER_THAN_EXPECTED,
				PackagingRating:      common.PackagingConditionEnum_PACKAGING_CONDITION_ENUM_EXCELLENT,
				ReviewText:           "Beta seed review — excellent quality and quick delivery.",
				SophisticationRating: common.ProductRatingEnum_PRODUCT_RATING_ENUM_EXCELLENT,
			},
			ItemReviews: itemReviews,
		}); err != nil {
			return fmt.Errorf("SFSubmitOrderReview(review %s): %w", uuid, err)
		}
		r.ReviewOrderUUIDs = append(r.ReviewOrderUUIDs, uuid)
		made++
	}

	or, err := s.C.GetOrderReviewsPaged(ctx, &admin.GetOrderReviewsPagedRequest{Limit: 50})
	if err != nil {
		return fmt.Errorf("GetOrderReviewsPaged: %w", err)
	}
	if pr, err := s.C.GetProductReviewsPaged(ctx, &admin.GetProductReviewsPagedRequest{ProductId: variants[0].colorwayID, Limit: 50}); err == nil {
		s.logf("reviews: created %d order reviews (%d total); product %d has %d item reviews",
			made, or.GetTotal(), variants[0].colorwayID, pr.GetTotal())
	} else {
		s.logf("reviews: created %d order reviews (%d total); GetProductReviewsPaged: %v", made, or.GetTotal(), err)
	}
	return nil
}
