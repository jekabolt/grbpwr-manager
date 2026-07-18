package dto

import (
	"database/sql"
	"fmt"
	"strings"
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/cache"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/jekabolt/grbpwr-manager/internal/slug"
	pb_common "github.com/jekabolt/grbpwr-manager/proto/gen/common"
	pb_frontend "github.com/jekabolt/grbpwr-manager/proto/gen/frontend"
	"github.com/shopspring/decimal"
	datepb "google.golang.org/genproto/googleapis/type/date"
	pb_decimal "google.golang.org/genproto/googleapis/type/decimal"
	"google.golang.org/protobuf/types/known/timestamppb"
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
	if a.Phone.Valid {
		pb.Phone = a.Phone.String
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
	phone := strings.TrimSpace(pb.GetPhone())
	if phone != "" {
		ins.Phone = sql.NullString{String: phone, Valid: true}
	}
	return ins
}

// ─── Storefront catalogue projections (R3) ──────────────────────────────────────────────────────
// These build the public colourway shapes that carry NO catalogue primary keys — the storefront's only
// window onto a colourway. The public identity is base_sku / variant_sku / size code+ordinal.

// storefrontPublicSize resolves a size id to its public shape (code/name/system/ordinal, no DB id).
func storefrontPublicSize(sizeID int) *pb_frontend.PublicSize {
	sz, ok := cache.GetSizeById(sizeID)
	if !ok {
		return nil
	}
	return &pb_frontend.PublicSize{
		Code:   sz.Name,
		Name:   sz.Name,
		System: sizeSKUSystemEntityPBMap[sz.SkuSystem],
		SkuOrd: int32(sz.SkuOrd),
	}
}

// storefrontVariants projects a colourway's variants for the storefront: only ACTIVE variants are
// exposed, each addressed by its public variant SKU (never an internal id).
func storefrontVariants(sizes []entity.Variant) []*pb_frontend.StorefrontVariant {
	out := make([]*pb_frontend.StorefrontVariant, 0, len(sizes))
	for i := range sizes {
		v := &sizes[i]
		if entity.VariantStatus(v.Status) == entity.VariantStatusArchived {
			continue
		}
		out = append(out, &pb_frontend.StorefrontVariant{
			VariantSku: v.SKU.String,
			Size:       storefrontPublicSize(v.SizeId),
			SoldOut:    v.Quantity.LessThanOrEqual(decimal.Zero),
		})
	}
	return out
}

// storefrontSizeChart projects the resolved per-colourway measurements (from the style chart) as public
// cells (size code/name + measurement name + value, no ids). Best-effort: absent measurements yield nil.
func storefrontSizeChart(sizes []entity.Variant, measurements []entity.ProductMeasurement) *pb_frontend.PublicStyleSizeChart {
	if len(measurements) == 0 {
		return nil
	}
	// Measurements are keyed by product_size.id (the variant); map it back to the size id for PublicSize.
	sizeByVariant := make(map[int]int, len(sizes))
	for i := range sizes {
		sizeByVariant[sizes[i].Id] = sizes[i].SizeId
	}
	names := make(map[int]string)
	for _, mn := range cache.GetMeasurements() {
		names[mn.Id] = mn.Name
	}
	cells := make([]*pb_frontend.PublicMeasurement, 0, len(measurements))
	for _, m := range measurements {
		cells = append(cells, &pb_frontend.PublicMeasurement{
			Size:            storefrontPublicSize(sizeByVariant[m.ProductSizeId]),
			MeasurementName: names[m.MeasurementNameId],
			Value:           &pb_decimal.Decimal{Value: m.MeasurementValue.String()},
		})
	}
	return &pb_frontend.PublicStyleSizeChart{Cells: cells}
}

// storefrontDisplay projects the resolved merchandising + style facts for the storefront (output-only,
// no ids). Translations are the resolved merch overrides.
func storefrontDisplay(c *entity.Colorway) *pb_frontend.StorefrontColorwayDisplay {
	bi := &c.ProductDisplay.ProductBody.ProductBodyInsert
	tg, _ := ConvertEntityGenderToPbGenderEnum(bi.TargetGender)
	var translations []*pb_common.ColorwayInsertTranslation
	for _, t := range c.ProductDisplay.ProductBody.Translations {
		translations = append(translations, &pb_common.ColorwayInsertTranslation{
			LanguageId:  int32(t.LanguageId),
			Name:        t.Name,
			Description: t.Description,
		})
	}
	d := &pb_frontend.StorefrontColorwayDisplay{
		Thumbnail:        ConvertEntityToCommonMedia(&c.ProductDisplay.Thumbnail),
		Brand:            bi.Brand,
		CollectionCode:   bi.Collection,
		TargetGender:     tg,
		Fit:              bi.Fit.String,
		Composition:      bi.Composition.String, // legacy plain text ONLY (M1 fix) — see composition_entries below
		CareInstructions: bi.CareInstructions.String,
		Translations:     translations,
		UpdatedAt:        timestamppb.New(c.UpdatedAt),
		// Structured fibre composition (S17/M1 fix), alongside — never instead of — Composition above.
		CompositionEntries: compositionEntriesToPb(bi.CompositionEntries),
	}
	if c.ProductDisplay.SecondaryThumbnail != nil {
		d.SecondaryThumbnail = ConvertEntityToCommonMedia(c.ProductDisplay.SecondaryThumbnail)
	}
	// Merchandising facts the PDP/cards render (S-final finding): sale %, preorder window,
	// model-wears and category labels are public output-only facts — none are catalogue PKs.
	if bi.SalePercentage.Valid {
		d.SalePercentage = pbDecimalFromDecimal(bi.SalePercentage.Decimal)
	}
	if bi.Preorder.Valid {
		d.Preorder = timestamppb.New(bi.Preorder.Time)
	}
	if bi.ModelWearsHeightCm.Valid {
		d.ModelWearsHeightCm = bi.ModelWearsHeightCm.Int32
	}
	if bi.ModelWearsSizeId.Valid {
		if sz, ok := cache.GetSizeById(int(bi.ModelWearsSizeId.Int32)); ok {
			d.ModelWearsSizeCode = sz.Name
		}
	}
	for _, catID := range []sql.NullInt32{{Int32: int32(bi.TopCategoryId), Valid: bi.TopCategoryId != 0}, bi.SubCategoryId, bi.TypeId} {
		if !catID.Valid {
			continue
		}
		for _, cat := range cache.GetCategories() {
			if cat.ID == int(catID.Int32) {
				d.CategoryLabels = append(d.CategoryLabels, cat.Name)
				break
			}
		}
	}
	return d
}

// StorefrontColorwayFromFull projects a full colourway (detail view: variants + media + size chart) for
// the storefront, with no catalogue primary keys (R3). viewerTier is the requesting customer's
// un-spoofable loyalty tier (0 for guests); it drives the `locked` teaser flag but never withholds any
// display field — a locked colourway is fully renderable, it just cannot be purchased (enforced on the
// order path). Callers that must not reveal a hidden_for_non_qualified colourway to a non-qualifying
// viewer gate that upstream (the SQL catalogue read / the PDP handler); this projection trusts its input.
func StorefrontColorwayFromFull(e *entity.ColorwayFull, viewerTier int16) *pb_frontend.StorefrontColorway {
	if e == nil || e.Product == nil {
		return nil
	}
	c := e.Product
	return &pb_frontend.StorefrontColorway{
		BaseSku:      c.SKU,
		Slug:         slug.ProductPath(canonicalProductName(c.ProductDisplay.ProductBody.Translations), c.SKU),
		Display:      storefrontDisplay(c),
		Variants:     storefrontVariants(e.Sizes),
		Prices:       convertEntityPricesToPb(e.Prices),
		Media:        ConvertEntityMediaListToPbMedia(e.Media),
		SizeChart:    storefrontSizeChart(e.Sizes, e.Measurements),
		ColorCode:    c.ProductDisplay.ProductBody.ProductBodyInsert.ColorCode,
		SoldOut:      entity.SoldOutFromSizes(e.Sizes),
		Status:       pb_common.ColorwayLifecycleStatus(c.LifecycleStatus),
		Locked:       !entity.TierCanPurchase(viewerTier, c.MinTier()),
		RequiredTier: int32(c.MinTier()),
	}
}

// StorefrontColorwayFromColorway projects a colourway header (list/paged view: no variants/media/chart)
// for the storefront, with no catalogue primary keys (R3). viewerTier drives the `locked` teaser flag
// (see StorefrontColorwayFromFull); it never withholds display fields.
func StorefrontColorwayFromColorway(e *entity.Colorway, viewerTier int16) *pb_frontend.StorefrontColorway {
	if e == nil {
		return nil
	}
	return &pb_frontend.StorefrontColorway{
		BaseSku:      e.SKU,
		Slug:         slug.ProductPath(canonicalProductName(e.ProductDisplay.ProductBody.Translations), e.SKU),
		Display:      storefrontDisplay(e),
		Prices:       convertEntityPricesToPb(e.Prices),
		ColorCode:    e.ProductDisplay.ProductBody.ProductBodyInsert.ColorCode,
		SoldOut:      e.SoldOut,
		Status:       pb_common.ColorwayLifecycleStatus(e.LifecycleStatus),
		Locked:       !entity.TierCanPurchase(viewerTier, e.MinTier()),
		RequiredTier: int32(e.MinTier()),
	}
}

// ─── Storefront archive projections (R3, §7.6) ──────────────────────────────────────────────────
// Same shape as the admin archive read, but the product blocks carry StorefrontColorway (no catalogue
// PKs) and ArchiveList drops its id. The media/text/embed blocks carry no catalogue PKs and reuse the
// common pb types + their converters.

func storefrontArchiveList(al *entity.ArchiveList) *pb_frontend.StorefrontArchiveList {
	if al == nil {
		return nil
	}
	translations := make([]*pb_common.ArchiveInsertTranslation, 0, len(al.Translations))
	for _, t := range al.Translations {
		translations = append(translations, &pb_common.ArchiveInsertTranslation{
			LanguageId: int32(t.LanguageId),
			Heading:    t.Heading,
		})
	}
	return &pb_frontend.StorefrontArchiveList{
		Translations: translations,
		Tag:          al.Tag,
		Slug:         al.Slug,
		CreatedAt:    timestamppb.New(al.CreatedAt),
		Thumbnail:    ConvertEntityToCommonMedia(&al.Thumbnail),
		Code:         al.Code,
	}
}

func storefrontColorwaysFromList(products []entity.Colorway, viewerTier int16) []*pb_frontend.StorefrontColorway {
	out := make([]*pb_frontend.StorefrontColorway, 0, len(products))
	for i := range products {
		p := &products[i]
		// Leak-proofing: a hidden_for_non_qualified colourway must never be surfaced to a viewer who
		// does not qualify for it, even when an admin-curated archive tag/manual block pulled it in
		// (those product reads are not tier-filtered). Non-hidden gated colourways stay as locked teasers.
		if p.HiddenForNonQualified() && !entity.TierCanPurchase(viewerTier, p.MinTier()) {
			continue
		}
		out = append(out, StorefrontColorwayFromColorway(p, viewerTier))
	}
	return out
}

func storefrontArchiveItem(it *entity.ArchiveItemFull, viewerTier int16) *pb_frontend.StorefrontArchiveItemFull {
	if it == nil {
		return nil
	}
	out := &pb_frontend.StorefrontArchiveItemFull{Type: pb_common.ArchiveItemType(it.Type)}
	switch it.Type {
	case entity.ArchiveItemTypeMainMedia:
		if b := it.MainMedia; b != nil {
			out.MainMedia = &pb_common.ArchiveMainMediaFull{
				Media:       ConvertEntityToCommonMedia(&b.Media),
				AspectRatio: pb_common.ArchiveMediaAspectRatio(b.AspectRatio),
			}
		}
	case entity.ArchiveItemTypeMediaLine:
		if b := it.MediaLine; b != nil {
			out.MediaLine = &pb_common.ArchiveMediaLineFull{
				Media:       ConvertEntityMediaListToPbMedia(b.Media),
				AspectRatio: pb_common.ArchiveMediaAspectRatio(b.AspectRatio),
			}
		}
	case entity.ArchiveItemTypeText:
		if b := it.Text; b != nil {
			out.Text = &pb_common.ArchiveTextFull{Translations: convertEntityArchiveItemTranslationsToPb(b.Translations)}
		}
	case entity.ArchiveItemTypeEmbed:
		if b := it.Embed; b != nil {
			out.Embed = &pb_common.ArchiveEmbedFull{
				EmbedUrl:     b.EmbedUrl,
				Translations: convertEntityArchiveItemTranslationsToPb(b.Translations),
			}
		}
	case entity.ArchiveItemTypeMediaWithCaption:
		if b := it.MediaWithCaption; b != nil {
			out.MediaWithCaption = &pb_common.ArchiveMediaWithCaptionFull{
				Media:        ConvertEntityToCommonMedia(&b.Media),
				Link:         b.Link,
				AspectRatio:  pb_common.ArchiveMediaAspectRatio(b.AspectRatio),
				Translations: convertEntityArchiveItemTranslationsToPb(b.Translations),
			}
		}
	case entity.ArchiveItemTypeProduct:
		if b := it.Product; b != nil {
			pf := &pb_frontend.StorefrontArchiveProductFull{Translations: convertEntityArchiveItemTranslationsToPb(b.Translations)}
			// Leak-proofing: drop the embedded colourway if it is hidden_for_non_qualified and this
			// viewer does not qualify. Non-hidden gated colourways remain as locked teasers.
			if b.Product != nil && !(b.Product.HiddenForNonQualified() && !entity.TierCanPurchase(viewerTier, b.Product.MinTier())) {
				pf.Colorway = StorefrontColorwayFromColorway(b.Product, viewerTier)
			}
			out.Product = pf
		}
	case entity.ArchiveItemTypeProductsTag:
		if b := it.ProductsTag; b != nil {
			out.ProductsTag = &pb_frontend.StorefrontArchiveProductsTagFull{
				Tag:          b.Tag,
				Colorways:    storefrontColorwaysFromList(b.Products, viewerTier),
				Translations: convertEntityArchiveItemTranslationsToPb(b.Translations),
			}
		}
	case entity.ArchiveItemTypeProductsManual:
		if b := it.ProductsManual; b != nil {
			out.ProductsManual = &pb_frontend.StorefrontArchiveProductsManualFull{
				Colorways:    storefrontColorwaysFromList(b.Products, viewerTier),
				Translations: convertEntityArchiveItemTranslationsToPb(b.Translations),
			}
		}
	}
	return out
}

// StorefrontArchiveFullFromEntity projects a full archive for the storefront (no catalogue PKs, R3).
// viewerTier is the requesting customer's un-spoofable tier (0 for guests), threaded to the embedded
// product blocks so they render locked teasers and never leak hidden_for_non_qualified colourways.
func StorefrontArchiveFullFromEntity(af *entity.ArchiveFull, viewerTier int16) *pb_frontend.StorefrontArchiveFull {
	if af == nil {
		return nil
	}
	items := make([]*pb_frontend.StorefrontArchiveItemFull, 0, len(af.Items))
	for i := range af.Items {
		items = append(items, storefrontArchiveItem(&af.Items[i], viewerTier))
	}
	return &pb_frontend.StorefrontArchiveFull{
		ArchiveList: storefrontArchiveList(&af.ArchiveList),
		Items:       items,
	}
}

// StorefrontArchiveListFromEntity projects an archive list row for the storefront (no id, R3).
func StorefrontArchiveListFromEntity(al *entity.ArchiveList) *pb_frontend.StorefrontArchiveList {
	return storefrontArchiveList(al)
}
