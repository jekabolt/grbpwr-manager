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
	CreatedAt            time.Time                    `db:"created_at"`
	UpdatedAt            time.Time                    `db:"updated_at"`
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
	IsDefault      bool           `db:"is_default"`
}
