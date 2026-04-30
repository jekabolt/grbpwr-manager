package dto

import (
	"database/sql"
	"fmt"
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	pb_frontend "github.com/jekabolt/grbpwr-manager/proto/gen/frontend"
	datepb "google.golang.org/genproto/googleapis/type/date"
)

var storefrontShoppingEntityPbMap = map[entity.StorefrontShoppingPreference]pb_frontend.ShoppingPreferenceEnum{
	entity.StorefrontShoppingMale:   pb_frontend.ShoppingPreferenceEnum_SHOPPING_PREFERENCE_ENUM_MALE,
	entity.StorefrontShoppingFemale: pb_frontend.ShoppingPreferenceEnum_SHOPPING_PREFERENCE_ENUM_FEMALE,
	entity.StorefrontShoppingAll:    pb_frontend.ShoppingPreferenceEnum_SHOPPING_PREFERENCE_ENUM_ALL,
}

var storefrontShoppingPbEntityMap = map[pb_frontend.ShoppingPreferenceEnum]entity.StorefrontShoppingPreference{
	pb_frontend.ShoppingPreferenceEnum_SHOPPING_PREFERENCE_ENUM_MALE:   entity.StorefrontShoppingMale,
	pb_frontend.ShoppingPreferenceEnum_SHOPPING_PREFERENCE_ENUM_FEMALE: entity.StorefrontShoppingFemale,
	pb_frontend.ShoppingPreferenceEnum_SHOPPING_PREFERENCE_ENUM_ALL:    entity.StorefrontShoppingAll,
}

var storefrontAccountTierEntityPbMap = map[entity.StorefrontAccountTier]pb_frontend.AccountTierEnum{
	entity.StorefrontAccountTierMember:   pb_frontend.AccountTierEnum_ACCOUNT_TIER_ENUM_MEMBER,
	entity.StorefrontAccountTierPlus:     pb_frontend.AccountTierEnum_ACCOUNT_TIER_ENUM_PLUS,
	entity.StorefrontAccountTierPlusPlus: pb_frontend.AccountTierEnum_ACCOUNT_TIER_ENUM_PLUS_PLUS,
	entity.StorefrontAccountTierHacker:   pb_frontend.AccountTierEnum_ACCOUNT_TIER_ENUM_HACKER,
}

// ConvertPbShoppingPreferenceEnumToEntity maps API enum to DB string values.
func ConvertPbShoppingPreferenceEnumToEntity(pb pb_frontend.ShoppingPreferenceEnum) (entity.StorefrontShoppingPreference, error) {
	g, ok := storefrontShoppingPbEntityMap[pb]
	if !ok {
		return "", fmt.Errorf("unknown shopping preference enum %v", pb)
	}
	return g, nil
}

// ConvertEntityShoppingPreferenceToPb maps DB value to API enum.
func ConvertEntityShoppingPreferenceToPb(s entity.StorefrontShoppingPreference) (pb_frontend.ShoppingPreferenceEnum, error) {
	g, ok := storefrontShoppingEntityPbMap[s]
	if !ok {
		return pb_frontend.ShoppingPreferenceEnum_SHOPPING_PREFERENCE_ENUM_UNKNOWN, fmt.Errorf("unknown shopping preference %q", s)
	}
	return g, nil
}

// ConvertEntityAccountTierToPb maps DB account tier to API enum.
func ConvertEntityAccountTierToPb(t entity.StorefrontAccountTier) pb_frontend.AccountTierEnum {
	pb, ok := storefrontAccountTierEntityPbMap[t]
	if !ok {
		return pb_frontend.AccountTierEnum_ACCOUNT_TIER_ENUM_UNKNOWN
	}
	return pb
}

// EntityStorefrontAccountToPb maps a DB account to the frontend API message.
func EntityStorefrontAccountToPb(a *entity.StorefrontAccount, addresses []*pb_frontend.StorefrontSavedAddress) (*pb_frontend.StorefrontAccount, error) {
	if a == nil {
		return nil, fmt.Errorf("account is nil")
	}
	var bd *datepb.Date
	if a.BirthDate.Valid {
		t := a.BirthDate.Time.UTC()
		bd = &datepb.Date{
			Year:  int32(t.Year()),
			Month: int32(t.Month()),
			Day:   int32(t.Day()),
		}
	}
	shoppingPref := pb_frontend.ShoppingPreferenceEnum_SHOPPING_PREFERENCE_ENUM_UNKNOWN
	if a.ShoppingPreference != "" {
		g, err := ConvertEntityShoppingPreferenceToPb(a.ShoppingPreference)
		if err != nil {
			shoppingPref = pb_frontend.ShoppingPreferenceEnum_SHOPPING_PREFERENCE_ENUM_UNKNOWN
		} else {
			shoppingPref = g
		}
	}
	phone := ""
	if a.Phone.Valid {
		phone = a.Phone.String
	}
	defaultCountry := ""
	if a.DefaultCountry.Valid {
		defaultCountry = a.DefaultCountry.String
	}
	defaultLanguage := ""
	if a.DefaultLanguage.Valid {
		defaultLanguage = a.DefaultLanguage.String
	}
	accountTier := ConvertEntityAccountTierToPb(entity.StorefrontAccountTier(a.AccountTier))
	return &pb_frontend.StorefrontAccount{
		Email:                a.Email,
		FirstName:            a.FirstName,
		LastName:             a.LastName,
		BirthDate:            bd,
		ShoppingPreference:   shoppingPref,
		Phone:                phone,
		SubscribeNewsletter:  a.SubscribeNewsletter,
		SubscribeNewArrivals: a.SubscribeNewArrivals,
		SubscribeEvents:      a.SubscribeEvents,
		AccountTier:          accountTier,
		Addresses:            addresses,
		DefaultCountry:       defaultCountry,
		DefaultLanguage:      defaultLanguage,
	}, nil
}

// PbDateToNullTime converts a protobuf Date to sql.NullTime.
func PbDateToNullTime(d *datepb.Date) sql.NullTime {
	if d == nil || d.Year == 0 {
		return sql.NullTime{}
	}
	t := time.Date(int(d.Year), time.Month(d.Month), int(d.Day), 0, 0, 0, 0, time.UTC)
	return sql.NullTime{Time: t, Valid: true}
}

// EntityStorefrontSavedAddressToPb maps a saved address row to proto.
func EntityStorefrontSavedAddressToPb(a *entity.StorefrontSavedAddress) *pb_frontend.StorefrontSavedAddress {
	pb := &pb_frontend.StorefrontSavedAddress{
		Id:             int32(a.ID),
		Label:          a.Label,
		Country:        a.Country,
		City:           a.City,
		AddressLineOne: a.AddressLineOne,
		PostalCode:     a.PostalCode,
		IsDefault:      a.IsDefault,
	}
	if a.State.Valid {
		pb.State = a.State.String
	}
	if a.AddressLineTwo.Valid {
		pb.AddressLineTwo = a.AddressLineTwo.String
	}
	if a.Company.Valid {
		pb.Company = a.Company.String
	}
	return pb
}

// PbStorefrontSavedAddressToInsert converts request body to entity insert (id ignored).
func PbStorefrontSavedAddressToInsert(pb *pb_frontend.StorefrontSavedAddress) *entity.StorefrontSavedAddressInsert {
	if pb == nil {
		return nil
	}
	ins := &entity.StorefrontSavedAddressInsert{
		Label:          pb.GetLabel(),
		Country:        pb.GetCountry(),
		City:           pb.GetCity(),
		AddressLineOne: pb.GetAddressLineOne(),
		PostalCode:     pb.GetPostalCode(),
		IsDefault:      pb.GetIsDefault(),
	}
	if pb.GetState() != "" {
		ins.State = sql.NullString{String: pb.GetState(), Valid: true}
	}
	if pb.GetAddressLineTwo() != "" {
		ins.AddressLineTwo = sql.NullString{String: pb.GetAddressLineTwo(), Valid: true}
	}
	if pb.GetCompany() != "" {
		ins.Company = sql.NullString{String: pb.GetCompany(), Valid: true}
	}
	return ins
}
