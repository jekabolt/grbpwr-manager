package dto

import (
	"reflect"
	"testing"
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	pb_common "github.com/jekabolt/grbpwr-manager/proto/gen/common"
)

// sampleEmailBlockTranslations builds two fully-populated translations (languages
// 1 and 2) so every field on entity.EmailBlockTranslation is exercised.
func sampleEmailBlockTranslations(prefix, slug string) []entity.EmailBlockTranslation {
	return []entity.EmailBlockTranslation{
		{
			LanguageID: 1,
			Heading:    prefix + " Heading EN",
			Subheading: prefix + " Subheading EN",
			Body:       prefix + " Body EN",
			Caption:    prefix + " Caption EN",
			CTALabel:   prefix + " Shop Now",
			CTAURL:     "https://example.com/" + slug + "/en",
			AltText:    prefix + " Alt EN",
			Preheader:  prefix + " Preheader EN",
			Links: []entity.EmailLink{
				{Label: prefix + " Link1 EN", URL: "https://example.com/" + slug + "/link1-en"},
				{Label: prefix + " Link2 EN", URL: "https://example.com/" + slug + "/link2-en"},
			},
		},
		{
			LanguageID: 2,
			Heading:    prefix + " Heading FR",
			Subheading: prefix + " Subheading FR",
			Body:       prefix + " Body FR",
			Caption:    prefix + " Caption FR",
			CTALabel:   prefix + " Acheter",
			CTAURL:     "https://example.com/" + slug + "/fr",
			AltText:    prefix + " Alt FR",
			Preheader:  prefix + " Preheader FR",
			Links: []entity.EmailLink{
				{Label: prefix + " Link1 FR", URL: "https://example.com/" + slug + "/link1-fr"},
				{Label: prefix + " Link2 FR", URL: "https://example.com/" + slug + "/link2-fr"},
			},
		},
	}
}

// TestEmailBlockConversionRoundTripAllTypes drives entity -> pb -> entity through
// convertEntityEmailBlockToPB / convertPBEmailBlockToEntity directly (both
// unexported, same package) for every one of the 12 EmailBlockType payloads, with
// every field of each payload populated with non-zero values.
func TestEmailBlockConversionRoundTripAllTypes(t *testing.T) {
	twoColumnLeft := []entity.EmailBlock{{
		Type:            entity.EmailBlockTypeImageLink,
		ImageLink:       &entity.EmailImageLinkBlock{MediaID: 601, URL: "https://cdn.example.com/two-column/left.jpg"},
		BackgroundColor: "#abcdef",
		Translations:    sampleEmailBlockTranslations("TwoColumnLeft", "two-column-left"),
	}}
	twoColumnRight := []entity.EmailBlock{{
		Type:            entity.EmailBlockTypeDivider,
		Divider:         &entity.EmailDividerBlock{Color: "#00ff00", Height: 3},
		BackgroundColor: "#fedcba",
		Translations:    sampleEmailBlockTranslations("TwoColumnRight", "two-column-right"),
	}}

	tests := []struct {
		name  string
		block entity.EmailBlock
	}{
		{
			name: "header",
			block: entity.EmailBlock{
				Type:            entity.EmailBlockTypeHeader,
				Header:          &entity.EmailHeaderBlock{LogoMediaID: 501},
				BackgroundColor: "#111111",
				Translations:    sampleEmailBlockTranslations("Header", "header"),
			},
		},
		{
			name: "image_link",
			block: entity.EmailBlock{
				Type:            entity.EmailBlockTypeImageLink,
				ImageLink:       &entity.EmailImageLinkBlock{MediaID: 502, URL: "https://cdn.example.com/hero.jpg"},
				BackgroundColor: "#222222",
				Translations:    sampleEmailBlockTranslations("ImageLink", "image-link"),
			},
		},
		{
			name: "rich_text",
			block: entity.EmailBlock{
				Type:            entity.EmailBlockTypeRichText,
				RichText:        &entity.EmailRichTextBlock{},
				BackgroundColor: "#333333",
				Translations:    sampleEmailBlockTranslations("RichText", "rich-text"),
			},
		},
		{
			name: "product_card",
			block: entity.EmailBlock{
				Type:            entity.EmailBlockTypeProductCard,
				ProductCard:     &entity.EmailProductCardBlock{ProductID: 777},
				BackgroundColor: "#444444",
				Translations:    sampleEmailBlockTranslations("ProductCard", "product-card"),
			},
		},
		{
			name: "product_grid",
			block: entity.EmailBlock{
				Type:            entity.EmailBlockTypeProductGrid,
				ProductGrid:     &entity.EmailProductGridBlock{ProductIDs: []int{101, 102, 103}, Columns: 3},
				BackgroundColor: "#555555",
				Translations:    sampleEmailBlockTranslations("ProductGrid", "product-grid"),
			},
		},
		{
			name: "cta_button",
			block: entity.EmailBlock{
				Type:            entity.EmailBlockTypeCTAButton,
				CTAButton:       &entity.EmailCTAButtonBlock{Style: "solid", Alignment: "center"},
				BackgroundColor: "#666666",
				Translations:    sampleEmailBlockTranslations("CTAButton", "cta-button"),
			},
		},
		{
			name: "divider",
			block: entity.EmailBlock{
				Type:            entity.EmailBlockTypeDivider,
				Divider:         &entity.EmailDividerBlock{Color: "#ff0000", Height: 2},
				BackgroundColor: "#777777",
				Translations:    sampleEmailBlockTranslations("Divider", "divider"),
			},
		},
		{
			name: "spacer",
			block: entity.EmailBlock{
				Type:            entity.EmailBlockTypeSpacer,
				Spacer:          &entity.EmailSpacerBlock{Height: 24},
				BackgroundColor: "#888888",
				Translations:    sampleEmailBlockTranslations("Spacer", "spacer"),
			},
		},
		{
			name: "two_column",
			block: entity.EmailBlock{
				Type:            entity.EmailBlockTypeTwoColumn,
				TwoColumn:       &entity.EmailTwoColumnBlock{Left: twoColumnLeft, Right: twoColumnRight},
				BackgroundColor: "#999999",
				Translations:    sampleEmailBlockTranslations("TwoColumn", "two-column"),
			},
		},
		{
			name: "social_links",
			block: entity.EmailBlock{
				Type: entity.EmailBlockTypeSocialLinks,
				SocialLinks: &entity.EmailSocialLinksBlock{Links: []entity.EmailSocialLink{
					{Network: "instagram", URL: "https://instagram.com/grbpwr"},
					{Network: "twitter", URL: "https://twitter.com/grbpwr"},
				}},
				BackgroundColor: "#aaaaaa",
				Translations:    sampleEmailBlockTranslations("SocialLinks", "social-links"),
			},
		},
		{
			name: "countdown",
			block: entity.EmailBlock{
				Type:            entity.EmailBlockTypeCountdown,
				Countdown:       &entity.EmailCountdownBlock{EndsAt: 1893456000},
				BackgroundColor: "#bbbbbb",
				Translations:    sampleEmailBlockTranslations("Countdown", "countdown"),
			},
		},
		{
			name: "video_thumb",
			block: entity.EmailBlock{
				Type:            entity.EmailBlockTypeVideoThumb,
				VideoThumb:      &entity.EmailVideoThumbBlock{MediaID: 909, VideoURL: "https://cdn.example.com/promo.mp4"},
				BackgroundColor: "#cccccc",
				Translations:    sampleEmailBlockTranslations("VideoThumb", "video-thumb"),
			},
		},
	}

	if len(tests) != 12 {
		t.Fatalf("test table covers %d block types, want 12", len(tests))
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			pbBlock := convertEntityEmailBlockToPB(&tc.block)
			roundTripped, err := convertPBEmailBlockToEntity(pbBlock)
			if err != nil {
				t.Fatalf("convert back: %v", err)
			}
			if !reflect.DeepEqual(tc.block, roundTripped) {
				t.Fatalf("round trip mismatch for %s:\n got:  %+v\nwant: %+v", tc.name, roundTripped, tc.block)
			}
		})
	}
}

func TestEmailCampaignConversionPreservesTypedBlocks(t *testing.T) {
	in := &pb_common.EmailCampaignInsert{
		Name:   "Launch",
		Topic:  pb_common.EmailCampaignTopic_EMAIL_CAMPAIGN_TOPIC_NEW_ARRIVALS,
		Status: pb_common.EmailCampaignStatus_EMAIL_CAMPAIGN_STATUS_DRAFT,
		Body: []*pb_common.EmailBlock{{
			Type: pb_common.EmailBlockType_EMAIL_BLOCK_TYPE_TWO_COLUMN,
			TwoColumn: &pb_common.EmailTwoColumnBlock{
				Left: []*pb_common.EmailBlock{{
					Type:     pb_common.EmailBlockType_EMAIL_BLOCK_TYPE_RICH_TEXT,
					RichText: &pb_common.EmailRichTextBlock{},
					Translations: []*pb_common.EmailBlockTranslation{{
						LanguageId: 1,
						Heading:    "New",
						Body:       "Body",
					}},
				}},
				Right: []*pb_common.EmailBlock{{
					Type: pb_common.EmailBlockType_EMAIL_BLOCK_TYPE_PRODUCT_CARD,
					ProductCard: &pb_common.EmailProductCardBlock{
						ProductId: 42,
					},
				}},
			},
		}},
		Variants: []*pb_common.EmailCampaignVariant{{
			Label: "A",
			SubjectI18N: []*pb_common.SubjectTranslation{{
				LanguageId: 1,
				Subject:    "Launch",
			}},
		}},
	}

	converted, err := ConvertPbEmailCampaignInsertToEntity(in)
	if err != nil {
		t.Fatalf("convert campaign: %v", err)
	}
	if got := converted.Body[0].TwoColumn.Right[0].ProductCard.ProductID; got != 42 {
		t.Fatalf("product id = %d, want 42", got)
	}

	full := &entity.EmailCampaignFull{
		ID:                  7,
		EmailCampaignInsert: *converted,
		CreatedAt:           time.Unix(100, 0).UTC(),
		UpdatedAt:           time.Unix(200, 0).UTC(),
	}
	roundTrip := ConvertEntityEmailCampaignFullToPb(full)
	if got := roundTrip.Body[0].TwoColumn.Left[0].Translations[0].Heading; got != "New" {
		t.Fatalf("heading = %q, want New", got)
	}
	if got := roundTrip.Variants[0].SubjectI18N[0].Subject; got != "Launch" {
		t.Fatalf("subject = %q, want Launch", got)
	}
}

func TestEmailSegmentConversionPreservesRecursivePredicate(t *testing.T) {
	in := &pb_common.EmailSegment{
		Id:   3,
		Name: "Recent buyers",
		Predicate: &pb_common.SegmentPredicate{
			Root: &pb_common.SegmentNode{
				Op: pb_common.SegmentOp_SEGMENT_OP_AND,
				Children: []*pb_common.SegmentNode{{
					Field:    "order_count",
					Operator: "gte",
					Values:   []string{"1"},
				}},
			},
		},
	}

	converted, err := ConvertPbEmailSegmentToEntity(in)
	if err != nil {
		t.Fatalf("convert segment: %v", err)
	}
	roundTrip := ConvertEntityEmailSegmentToPb(converted)
	if got := roundTrip.Predicate.Root.Children[0].Field; got != "order_count" {
		t.Fatalf("field = %q, want order_count", got)
	}
	if got := roundTrip.Predicate.Root.Op; got != pb_common.SegmentOp_SEGMENT_OP_AND {
		t.Fatalf("op = %v, want AND", got)
	}
}

// TestEmailSegmentConversionPreservesMultiLevelNestedPredicate covers a
// three-level tree: a top AND group holding a leaf plus a nested OR group,
// where the nested OR group itself holds two leaves.
func TestEmailSegmentConversionPreservesMultiLevelNestedPredicate(t *testing.T) {
	in := &pb_common.EmailSegment{
		Id:   5,
		Name: "VIP EU buyers",
		Predicate: &pb_common.SegmentPredicate{
			Root: &pb_common.SegmentNode{
				Op: pb_common.SegmentOp_SEGMENT_OP_AND,
				Children: []*pb_common.SegmentNode{
					{
						Field:    "order_count",
						Operator: "gte",
						Values:   []string{"3"},
					},
					{
						Op: pb_common.SegmentOp_SEGMENT_OP_OR,
						Children: []*pb_common.SegmentNode{
							{Field: "country", Operator: "eq", Values: []string{"DE"}},
							{Field: "country", Operator: "eq", Values: []string{"FR"}},
						},
					},
				},
			},
		},
	}

	converted, err := ConvertPbEmailSegmentToEntity(in)
	if err != nil {
		t.Fatalf("convert segment: %v", err)
	}

	root := converted.Predicate.Root
	if root.Op != entity.SegmentOpAnd {
		t.Fatalf("root op = %v, want AND", root.Op)
	}
	if len(root.Children) != 2 {
		t.Fatalf("root children = %d, want 2", len(root.Children))
	}
	nestedGroup := root.Children[1]
	if nestedGroup.Op != entity.SegmentOpOr {
		t.Fatalf("nested group op = %v, want OR", nestedGroup.Op)
	}
	if len(nestedGroup.Children) != 2 {
		t.Fatalf("nested group children = %d, want 2", len(nestedGroup.Children))
	}
	if got := nestedGroup.Children[0].Values[0]; got != "DE" {
		t.Fatalf("nested leaf 0 value = %q, want DE", got)
	}
	if got := nestedGroup.Children[1].Values[0]; got != "FR" {
		t.Fatalf("nested leaf 1 value = %q, want FR", got)
	}

	roundTrip := ConvertEntityEmailSegmentToPb(converted)
	rtRoot := roundTrip.Predicate.Root
	if rtRoot.Op != pb_common.SegmentOp_SEGMENT_OP_AND {
		t.Fatalf("round trip root op = %v, want AND", rtRoot.Op)
	}
	if len(rtRoot.Children) != 2 {
		t.Fatalf("round trip root children = %d, want 2", len(rtRoot.Children))
	}
	rtNested := rtRoot.Children[1]
	if rtNested.Op != pb_common.SegmentOp_SEGMENT_OP_OR {
		t.Fatalf("round trip nested op = %v, want OR", rtNested.Op)
	}
	if len(rtNested.Children) != 2 {
		t.Fatalf("round trip nested children = %d, want 2", len(rtNested.Children))
	}
	if got := rtNested.Children[0].Values[0]; got != "DE" {
		t.Fatalf("round trip nested leaf 0 value = %q, want DE", got)
	}
	if got := rtNested.Children[1].Field; got != "country" {
		t.Fatalf("round trip nested leaf 1 field = %q, want country", got)
	}
	if got := rtNested.Children[1].Values[0]; got != "FR" {
		t.Fatalf("round trip nested leaf 1 value = %q, want FR", got)
	}
}

// TestEmailCampaignConversionPreservesMultipleVariantSubjects covers a campaign
// with several A/B variants, each carrying its own subject_i18n set.
func TestEmailCampaignConversionPreservesMultipleVariantSubjects(t *testing.T) {
	in := &pb_common.EmailCampaignInsert{
		Name:   "Sale",
		Topic:  pb_common.EmailCampaignTopic_EMAIL_CAMPAIGN_TOPIC_EVENTS,
		Status: pb_common.EmailCampaignStatus_EMAIL_CAMPAIGN_STATUS_SCHEDULED,
		Variants: []*pb_common.EmailCampaignVariant{
			{
				Id:    1,
				Label: "Control",
				SubjectI18N: []*pb_common.SubjectTranslation{
					{LanguageId: 1, Subject: "Big Sale"},
					{LanguageId: 2, Subject: "Grande Vente"},
				},
			},
			{
				Id:    2,
				Label: "Variant B",
				SubjectI18N: []*pb_common.SubjectTranslation{
					{LanguageId: 1, Subject: "Huge Discounts"},
					{LanguageId: 2, Subject: "Remises Enormes"},
				},
				IsWinner: true,
			},
			{
				Id:    3,
				Label: "Variant C",
				SubjectI18N: []*pb_common.SubjectTranslation{
					{LanguageId: 1, Subject: "Don't Miss Out"},
					{LanguageId: 2, Subject: "Ne Manquez Pas"},
				},
			},
		},
	}

	converted, err := ConvertPbEmailCampaignInsertToEntity(in)
	if err != nil {
		t.Fatalf("convert campaign: %v", err)
	}
	if len(converted.Variants) != 3 {
		t.Fatalf("variants = %d, want 3", len(converted.Variants))
	}

	full := &entity.EmailCampaignFull{
		ID:                  9,
		EmailCampaignInsert: *converted,
		CreatedAt:           time.Unix(300, 0).UTC(),
		UpdatedAt:           time.Unix(400, 0).UTC(),
	}
	roundTrip := ConvertEntityEmailCampaignFullToPb(full)
	if len(roundTrip.Variants) != 3 {
		t.Fatalf("round trip variants = %d, want 3", len(roundTrip.Variants))
	}
	for i, want := range in.Variants {
		got := roundTrip.Variants[i]
		if got.Id != want.Id || got.Label != want.Label || got.IsWinner != want.IsWinner {
			t.Fatalf("variant %d = %+v, want %+v", i, got, want)
		}
		if len(got.SubjectI18N) != len(want.SubjectI18N) {
			t.Fatalf("variant %d subjects = %d, want %d", i, len(got.SubjectI18N), len(want.SubjectI18N))
		}
		for j, wantSubj := range want.SubjectI18N {
			gotSubj := got.SubjectI18N[j]
			if gotSubj.LanguageId != wantSubj.LanguageId || gotSubj.Subject != wantSubj.Subject {
				t.Fatalf("variant %d subject %d = %+v, want %+v", i, j, gotSubj, wantSubj)
			}
		}
	}
}
