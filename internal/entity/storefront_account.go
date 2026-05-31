package entity

import (
	"database/sql"
	"time"
)

// StorefrontShoppingPreference is a value for storefront_account.shopping_preference ENUM.
type StorefrontShoppingPreference string

const (
	StorefrontShoppingMale   StorefrontShoppingPreference = "male"
	StorefrontShoppingFemale StorefrontShoppingPreference = "female"
	StorefrontShoppingAll    StorefrontShoppingPreference = "all"
)

var validStorefrontShoppingPreferences = map[StorefrontShoppingPreference]struct{}{
	StorefrontShoppingMale:   {},
	StorefrontShoppingFemale: {},
	StorefrontShoppingAll:    {},
}

// IsValidStorefrontShoppingPreference reports whether s is allowed for storefront_account.shopping_preference.
func IsValidStorefrontShoppingPreference(s string) bool {
	_, ok := validStorefrontShoppingPreferences[StorefrontShoppingPreference(s)]
	return ok
}

// StorefrontAccountTier is a value for storefront_account.account_tier ENUM.
type StorefrontAccountTier string

const (
	StorefrontAccountTierMember   StorefrontAccountTier = "member"
	StorefrontAccountTierPlus     StorefrontAccountTier = "plus"
	StorefrontAccountTierPlusPlus StorefrontAccountTier = "plus_plus"
	StorefrontAccountTierHacker   StorefrontAccountTier = "hacker"
)

var validStorefrontAccountTiers = map[StorefrontAccountTier]struct{}{
	StorefrontAccountTierMember:   {},
	StorefrontAccountTierPlus:     {},
	StorefrontAccountTierPlusPlus: {},
	StorefrontAccountTierHacker:   {},
}

// IsValidStorefrontAccountTier reports whether t is allowed for storefront_account.account_tier.
func IsValidStorefrontAccountTier(t string) bool {
	_, ok := validStorefrontAccountTiers[StorefrontAccountTier(t)]
	return ok
}

// Numeric tier codes used throughout loyalty logic and per-product gating.
// They are the canonical ordering key; the account_tier ENUM is the storage form.
const (
	TierCodeMember   int16 = 0
	TierCodePlus     int16 = 1
	TierCodePlusPlus int16 = 2
	TierCodeHacker   int16 = 99
)

var tierKeyToCode = map[StorefrontAccountTier]int16{
	StorefrontAccountTierMember:   TierCodeMember,
	StorefrontAccountTierPlus:     TierCodePlus,
	StorefrontAccountTierPlusPlus: TierCodePlusPlus,
	StorefrontAccountTierHacker:   TierCodeHacker,
}

var tierCodeToKey = map[int16]StorefrontAccountTier{
	TierCodeMember:   StorefrontAccountTierMember,
	TierCodePlus:     StorefrontAccountTierPlus,
	TierCodePlusPlus: StorefrontAccountTierPlusPlus,
	TierCodeHacker:   StorefrontAccountTierHacker,
}

// TierCode returns the numeric code for a tier key (defaults to member=0 if unknown).
func TierCode(t StorefrontAccountTier) int16 {
	if c, ok := tierKeyToCode[t]; ok {
		return c
	}
	return TierCodeMember
}

// TierKeyFromCode returns the tier key for a numeric code (defaults to member if unknown).
func TierKeyFromCode(code int16) StorefrontAccountTier {
	if k, ok := tierCodeToKey[code]; ok {
		return k
	}
	return StorefrontAccountTierMember
}

// IsNumericTier reports whether the tier participates in spend-based progression
// (member/plus/plus_plus). Hacker is a separate, invite-only track.
func IsNumericTier(t StorefrontAccountTier) bool {
	return t == StorefrontAccountTierMember || t == StorefrontAccountTierPlus || t == StorefrontAccountTierPlusPlus
}

// StorefrontAccountStatus is the lifecycle status of an account.
type StorefrontAccountStatus string

const (
	StorefrontStatusActive  StorefrontAccountStatus = "active"
	StorefrontStatusFrozen  StorefrontAccountStatus = "frozen"
	StorefrontStatusDeleted StorefrontAccountStatus = "deleted"
	StorefrontStatusErased  StorefrontAccountStatus = "erased"
)

var validStorefrontAccountStatuses = map[StorefrontAccountStatus]struct{}{
	StorefrontStatusActive:  {},
	StorefrontStatusFrozen:  {},
	StorefrontStatusDeleted: {},
	StorefrontStatusErased:  {},
}

// IsValidStorefrontAccountStatus reports whether s is allowed for storefront_account.status.
func IsValidStorefrontAccountStatus(s string) bool {
	_, ok := validStorefrontAccountStatuses[StorefrontAccountStatus(s)]
	return ok
}

// StorefrontAccount is a row in storefront_account.
type StorefrontAccount struct {
	ID                   int                          `db:"id"`
	Email                string                       `db:"email"`
	FirstName            string                       `db:"first_name"`
	LastName             string                       `db:"last_name"`
	BirthDate            sql.NullTime                 `db:"birth_date"`
	ShoppingPreference   StorefrontShoppingPreference `db:"shopping_preference"`
	Phone                sql.NullString               `db:"phone"`
	AccountTier          string                       `db:"account_tier"`
	SubscribeNewsletter  bool                         `db:"subscribe_newsletter"`
	SubscribeNewArrivals bool                         `db:"subscribe_new_arrivals"`
	SubscribeEvents      bool                         `db:"subscribe_events"`
	DefaultCountry       sql.NullString               `db:"default_country"`
	DefaultLanguage      sql.NullString               `db:"default_language"`
	Status               StorefrontAccountStatus      `db:"status"`
	TierUpgradeDate      sql.NullTime                 `db:"tier_upgrade_date"`
	NextReviewDate       sql.NullTime                 `db:"next_review_date"`
	DeletedAt            sql.NullTime                 `db:"deleted_at"`
	CreatedAt            time.Time                    `db:"created_at"`
	UpdatedAt            time.Time                    `db:"updated_at"`
}

// Tier returns the account_tier as a typed value.
func (a *StorefrontAccount) Tier() StorefrontAccountTier {
	return StorefrontAccountTier(a.AccountTier)
}

// StorefrontSavedAddress is a row in storefront_saved_address.
type StorefrontSavedAddress struct {
	ID             int            `db:"id"`
	AccountID      int            `db:"account_id"`
	Label          string         `db:"label"`
	Country        string         `db:"country"`
	State          sql.NullString `db:"state"`
	City           string         `db:"city"`
	AddressLineOne string         `db:"address_line_one"`
	AddressLineTwo sql.NullString `db:"address_line_two"`
	Company        sql.NullString `db:"company"`
	PostalCode     string         `db:"postal_code"`
	Phone          sql.NullString `db:"phone"`
	IsDefault      bool           `db:"is_default"`
	CreatedAt      time.Time      `db:"created_at"`
	UpdatedAt      time.Time      `db:"updated_at"`
}

// StorefrontSavedAddressInsert is used for insert/update of saved addresses.
type StorefrontSavedAddressInsert struct {
	Label          string         `db:"label"`
	Country        string         `db:"country"`
	State          sql.NullString `db:"state"`
	City           string         `db:"city"`
	AddressLineOne string         `db:"address_line_one"`
	AddressLineTwo sql.NullString `db:"address_line_two"`
	Company        sql.NullString `db:"company"`
	PostalCode     string         `db:"postal_code"`
	Phone          sql.NullString `db:"phone"`
	IsDefault      bool           `db:"is_default"`
}
