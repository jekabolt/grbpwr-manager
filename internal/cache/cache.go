package cache

import (
	"context"
	"fmt"
	"sync"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	pb_common "github.com/jekabolt/grbpwr-manager/proto/gen/common"
)

type Status struct {
	Status entity.OrderStatus
	PB     pb_common.OrderStatusEnum
}

type Measurement struct {
	Measurement entity.MeasurementName
	PB          string
}

type PaymentMethod struct {
	Method entity.PaymentMethod
	PB     pb_common.PaymentMethodNameEnum
}

var (

	// Statuses
	OrderStatusPlaced            = Status{Status: entity.OrderStatus{Name: entity.Placed}, PB: pb_common.OrderStatusEnum_ORDER_STATUS_ENUM_PLACED}
	OrderStatusAwaitingPayment   = Status{Status: entity.OrderStatus{Name: entity.AwaitingPayment}, PB: pb_common.OrderStatusEnum_ORDER_STATUS_ENUM_AWAITING_PAYMENT}
	OrderStatusConfirmed         = Status{Status: entity.OrderStatus{Name: entity.Confirmed}, PB: pb_common.OrderStatusEnum_ORDER_STATUS_ENUM_CONFIRMED}
	OrderStatusShipped           = Status{Status: entity.OrderStatus{Name: entity.Shipped}, PB: pb_common.OrderStatusEnum_ORDER_STATUS_ENUM_SHIPPED}
	OrderStatusDelivered         = Status{Status: entity.OrderStatus{Name: entity.Delivered}, PB: pb_common.OrderStatusEnum_ORDER_STATUS_ENUM_DELIVERED}
	OrderStatusCancelled         = Status{Status: entity.OrderStatus{Name: entity.Cancelled}, PB: pb_common.OrderStatusEnum_ORDER_STATUS_ENUM_CANCELLED}
	OrderStatusPendingReturn     = Status{Status: entity.OrderStatus{Name: entity.PendingReturn}, PB: pb_common.OrderStatusEnum_ORDER_STATUS_ENUM_PENDING_RETURN}
	OrderStatusRefundInProgress  = Status{Status: entity.OrderStatus{Name: entity.RefundInProgress}, PB: pb_common.OrderStatusEnum_ORDER_STATUS_ENUM_REFUND_IN_PROGRESS}
	OrderStatusRefunded          = Status{Status: entity.OrderStatus{Name: entity.Refunded}, PB: pb_common.OrderStatusEnum_ORDER_STATUS_ENUM_REFUNDED}
	OrderStatusPartiallyRefunded = Status{Status: entity.OrderStatus{Name: entity.PartiallyRefunded}, PB: pb_common.OrderStatusEnum_ORDER_STATUS_ENUM_PARTIALLY_REFUNDED}

	orderStatuses = []*Status{
		&OrderStatusPlaced,
		&OrderStatusAwaitingPayment,
		&OrderStatusConfirmed,
		&OrderStatusShipped,
		&OrderStatusDelivered,
		&OrderStatusCancelled,
		&OrderStatusPendingReturn,
		&OrderStatusRefundInProgress,
		&OrderStatusRefunded,
		&OrderStatusPartiallyRefunded,
	}

	entityOrderStatuses = []entity.OrderStatus{}
	orderStatusesById   = map[int]*Status{}
	orderStatusesByName = map[entity.OrderStatusName]*Status{}

	// Category and size DEFINITIONS keyed by id (process-wide: seeded by migrations, no request
	// writes them). The lists with their per-team product counts are per team (GetCategories,
	// GetSizes).
	categoryById = map[int]entity.Category{}
	sizeById     = map[int]entity.Size{}

	// CategorySizeSystems: category -> permitted size-system(s) mapping (S10/WS5, migration 0175).
	entityCategorySizeSystems = []entity.CategorySizeSystem{}

	entityMeasurements = []entity.MeasurementName{}

	// PaymentMethods: the identity of the four known methods (name, proto enum, and the id the
	// boot load found). Whether a method is allowed is per team (GetPaymentMethodByName).
	PaymentMethodCard = PaymentMethod{Method: entity.PaymentMethod{
		Name: entity.CARD,
	}, PB: pb_common.PaymentMethodNameEnum_PAYMENT_METHOD_NAME_ENUM_CARD}
	PaymentMethodCardTest = PaymentMethod{Method: entity.PaymentMethod{
		Name: entity.CARD_TEST,
	}, PB: pb_common.PaymentMethodNameEnum_PAYMENT_METHOD_NAME_ENUM_CARD_TEST}
	PaymentMethodBankInvoice = PaymentMethod{Method: entity.PaymentMethod{
		Name: entity.BANK_INVOICE,
	}, PB: pb_common.PaymentMethodNameEnum_PAYMENT_METHOD_NAME_ENUM_BANK_INVOICE}
	PaymentMethodCash = PaymentMethod{Method: entity.PaymentMethod{
		Name: entity.CASH,
	}, PB: pb_common.PaymentMethodNameEnum_PAYMENT_METHOD_NAME_ENUM_CASH}

	paymentMethods = []*PaymentMethod{
		&PaymentMethodCard,
		&PaymentMethodCardTest,
		&PaymentMethodBankInvoice,
		&PaymentMethodCash,
	}

	// paymentMethodsById maps a payment_method id to its identity (name + proto enum).
	paymentMethodsById = map[int]*PaymentMethod{}

	paymentMethodIdByPbId = map[pb_common.PaymentMethodNameEnum]int{
		pb_common.PaymentMethodNameEnum_PAYMENT_METHOD_NAME_ENUM_CARD:         PaymentMethodCard.Method.Id,
		pb_common.PaymentMethodNameEnum_PAYMENT_METHOD_NAME_ENUM_CARD_TEST:    PaymentMethodCardTest.Method.Id,
		pb_common.PaymentMethodNameEnum_PAYMENT_METHOD_NAME_ENUM_BANK_INVOICE: PaymentMethodBankInvoice.Method.Id,
		pb_common.PaymentMethodNameEnum_PAYMENT_METHOD_NAME_ENUM_CASH:         PaymentMethodCash.Method.Id,
	}

	// Care dictionary: the controlled ISO 3758 vocabulary a style's stored care codes resolve
	// against. careIndex is derived from it on every load so read paths resolve a code string
	// without touching the database, and write paths validate against the same set.
	entityCareSymbols = []entity.CareSymbol{}
	careIndex         = entity.CareIndex{}

	entityLanguages = []entity.Language{}

	defaultCurrency = "EUR"

	// cacheMu guards the process-wide state above: definitions (categoryById, sizeById, the
	// category size-system mapping, care symbols), identity (order statuses, payment methods),
	// measurements, languages and the base currency. Per-team data lives in team.go.
	cacheMu sync.RWMutex
)

var (
	genders []pb_common.Genders = []pb_common.Genders{
		{
			Name: string(entity.Male),
			Id:   pb_common.GenderEnum_GENDER_ENUM_MALE,
		},
		{
			Name: string(entity.Female),
			Id:   pb_common.GenderEnum_GENDER_ENUM_FEMALE,
		},
		{
			Name: string(entity.Unisex),
			Id:   pb_common.GenderEnum_GENDER_ENUM_UNISEX,
		},
	}

	sortFactors []pb_common.SortFactors = []pb_common.SortFactors{
		{
			Name: string(entity.CreatedAt),
			Id:   pb_common.SortFactor_SORT_FACTOR_CREATED_AT,
		},

		{
			Name: string(entity.UpdatedAt),
			Id:   pb_common.SortFactor_SORT_FACTOR_UPDATED_AT,
		},

		{
			Name: string(entity.Name),
			Id:   pb_common.SortFactor_SORT_FACTOR_NAME,
		},

		{
			Name: string(entity.Price),
			Id:   pb_common.SortFactor_SORT_FACTOR_PRICE,
		},
	}

	orderFactors []pb_common.OrderFactors = []pb_common.OrderFactors{
		{
			Name: string(entity.Ascending),
			Id:   pb_common.OrderFactor_ORDER_FACTOR_ASC,
		},
		{
			Name: string(entity.Descending),
			Id:   pb_common.OrderFactor_ORDER_FACTOR_DESC,
		},
	}
)

// InitConsts is the boot load (store.New): it fills the process-wide definitions and identity from
// dInfo and the snapshot of the team in ctx (team #1 at boot; with teams mode off a ctx without a
// team is team #1). It returns an error when an order status or payment method is missing, after
// filling everything, exactly as the old process-wide cache did.
func InitConsts(ctx context.Context, dInfo *entity.DictionaryInfo, h *entity.HeroFullWithTranslations) error {
	tid, err := registry.teamID(ctx)
	if err != nil {
		return err
	}
	setDefinitions(dInfo)
	identityErr := setIdentity(dInfo)
	registry.install(tid, TeamData{Dict: dInfo, Hero: h})
	return identityErr
}

// setDefinitions replaces the process-wide category/size definitions, the category size-system
// mapping and the care vocabulary. Team #1's loads and dictionary refreshes call it.
func setDefinitions(d *entity.DictionaryInfo) {
	cacheMu.Lock()
	defer cacheMu.Unlock()
	categoryById = make(map[int]entity.Category, len(d.Categories))
	for _, c := range d.Categories {
		categoryById[c.ID] = c
	}
	sizeById = make(map[int]entity.Size, len(d.Sizes))
	for _, s := range d.Sizes {
		sizeById[s.Id] = s
	}
	entityCategorySizeSystems = d.CategorySizeSystems
	entityCareSymbols = d.CareSymbols
	careIndex = entity.BuildCareIndex(d.CareSymbols)
}

// setIdentity records the ids of the order statuses and payment methods, the measurement names and
// the languages. It returns an error when a known status or method has no row.
func setIdentity(d *entity.DictionaryInfo) error {
	cacheMu.Lock()
	defer cacheMu.Unlock()
	entityMeasurements = d.Measurements
	entityLanguages = d.Languages

	entityOrderStatuses = []entity.OrderStatus{}
	for _, os := range d.OrderStatuses {
		for _, s := range orderStatuses {
			if os.Name == s.Status.Name {
				s.Status.Id = os.Id
				orderStatusesById[os.Id] = s
				entityOrderStatuses = append(entityOrderStatuses, os)
				orderStatusesByName[os.Name] = s
			}
		}
	}

	for _, pm := range d.PaymentMethods {
		for _, p := range paymentMethods {
			if pm.Name == p.Method.Name {
				p.Method.Id = pm.Id
				paymentMethodsById[pm.Id] = p
				paymentMethodIdByPbId[p.PB] = pm.Id
			}
		}
	}

	// check if all consts are initialized
	for _, v := range orderStatuses {
		if v.Status.Id == 0 {
			return fmt.Errorf("order status %s not found", v.Status.Name)
		}
	}
	for _, v := range paymentMethods {
		if v.Method.Id == 0 {
			return fmt.Errorf("payment method %s not found", v.Method.Name)
		}
	}
	return nil
}

// setIdentityOnce fills the identity when no boot load did (a harness that loads team #1 lazily).
func setIdentityOnce(d *entity.DictionaryInfo) {
	cacheMu.RLock()
	done := len(orderStatusesById) > 0
	cacheMu.RUnlock()
	if !done {
		_ = setIdentity(d)
	}
}

func GetOrderStatusById(id int) (Status, bool) {
	cacheMu.RLock()
	defer cacheMu.RUnlock()
	st, ok := orderStatusesById[id]
	if !ok {
		return Status{}, false
	}
	return *st, true
}

func GetOrderStatusByName(n entity.OrderStatusName) (Status, bool) {
	cacheMu.RLock()
	defer cacheMu.RUnlock()
	st, ok := orderStatusesByName[n]
	if !ok {
		return Status{}, false
	}
	return *st, true
}

// OrderStatusIDsForNetRevenue returns status IDs for orders contributing to net revenue (confirmed, shipped, delivered, partially_refunded).
func OrderStatusIDsForNetRevenue() []int {
	names := []entity.OrderStatusName{entity.Confirmed, entity.Shipped, entity.Delivered, entity.PartiallyRefunded}
	ids := make([]int, 0, len(names))
	for _, n := range names {
		if s, ok := GetOrderStatusByName(n); ok {
			ids = append(ids, s.Status.Id)
		}
	}
	return ids
}

// OrderStatusIDsForRefund returns status IDs for orders with refunded_amount (net revenue set + refunded).
func OrderStatusIDsForRefund() []int {
	names := []entity.OrderStatusName{entity.Confirmed, entity.Shipped, entity.Delivered, entity.Refunded, entity.PartiallyRefunded}
	ids := make([]int, 0, len(names))
	for _, n := range names {
		if s, ok := GetOrderStatusByName(n); ok {
			ids = append(ids, s.Status.Id)
		}
	}
	return ids
}

// PaymentMethodNameByID returns the name of a payment_method id (identity only; whether the method
// is allowed is per team: GetPaymentMethodById).
func PaymentMethodNameByID(id int) (entity.PaymentMethodName, bool) {
	cacheMu.RLock()
	defer cacheMu.RUnlock()
	pm, ok := paymentMethodsById[id]
	if !ok {
		return "", false
	}
	return pm.Method.Name, true
}

// PaymentMethodPBByID returns the proto enum of a payment_method id (identity only).
func PaymentMethodPBByID(id int) (pb_common.PaymentMethodNameEnum, bool) {
	cacheMu.RLock()
	defer cacheMu.RUnlock()
	pm, ok := paymentMethodsById[id]
	if !ok {
		return pb_common.PaymentMethodNameEnum_PAYMENT_METHOD_NAME_ENUM_UNKNOWN, false
	}
	return pm.PB, true
}

func GetSizeById(id int) (entity.Size, bool) {
	cacheMu.RLock()
	defer cacheMu.RUnlock()
	s, ok := sizeById[id]
	return s, ok
}

// GetCategoryById returns a single category-tree node by id (S10/WS5): used to resolve a style's
// most-specific category into a human-readable label for size-system validation errors.
func GetCategoryById(id int) (entity.Category, bool) {
	cacheMu.RLock()
	defer cacheMu.RUnlock()
	c, ok := categoryById[id]
	return c, ok
}

// GetCategorySizeSystems returns the category -> permitted size-system(s) mapping (S10/WS5, migration
// 0175): both the admin dictionary (size picker) and server-side size-write validation
// (entity.ResolveSizeSystemPolicy) read this same slice, so they can never drift from each other.
func GetCategorySizeSystems() []entity.CategorySizeSystem {
	cacheMu.RLock()
	defer cacheMu.RUnlock()
	return entityCategorySizeSystems
}

// CategoryLabel returns a human-readable name for a style's most specific set category node (type >
// sub-category > top-category, S10/WS5), or "" when the style has no category assigned at all.
func CategoryLabel(path entity.StyleCategoryPath) string {
	id, ok := path.MostSpecificID()
	if !ok {
		return ""
	}
	c, ok := GetCategoryById(id)
	if !ok {
		return ""
	}
	return c.Name
}

func SetDefaultCurrency(c string) {
	cacheMu.Lock()
	defaultCurrency = c
	cacheMu.Unlock()
}

func GetBaseCurrency() string {
	cacheMu.RLock()
	defer cacheMu.RUnlock()
	return defaultCurrency
}

func GetMeasurements() []entity.MeasurementName {
	cacheMu.RLock()
	defer cacheMu.RUnlock()
	return entityMeasurements
}

func GetGenders() []pb_common.Genders {
	return genders
}

func GetSortFactors() []pb_common.SortFactors {
	return sortFactors
}

func GetOrderFactors() []pb_common.OrderFactors {
	return orderFactors
}

func GetOrderStatuses() []entity.OrderStatus {
	cacheMu.RLock()
	defer cacheMu.RUnlock()
	return entityOrderStatuses
}

// GetCareSymbols returns the controlled ISO 3758 care vocabulary in canonical print order.
func GetCareSymbols() []entity.CareSymbol {
	cacheMu.RLock()
	defer cacheMu.RUnlock()
	return entityCareSymbols
}

// GetCareIndex returns the care dictionary keyed by code, for resolving a stored care string on a
// read path or validating one on a write. It is rebuilt on every dictionary refresh, so callers
// should take it per request rather than holding it.
func GetCareIndex() entity.CareIndex {
	cacheMu.RLock()
	defer cacheMu.RUnlock()
	return careIndex
}

func GetLanguages() []entity.Language {
	cacheMu.RLock()
	defer cacheMu.RUnlock()
	return entityLanguages
}

func GetPaymentMethodIdByPbId(pbId pb_common.PaymentMethodNameEnum) int {
	cacheMu.RLock()
	defer cacheMu.RUnlock()
	id, ok := paymentMethodIdByPbId[pbId]
	if !ok {
		return 0
	}
	return id
}
