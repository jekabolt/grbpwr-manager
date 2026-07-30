package campaignrender

import (
	"context"
	"strings"
	"testing"

	"github.com/jekabolt/grbpwr-manager/internal/dependency/mocks"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/stretchr/testify/mock"
)

func TestRejectedAuthoredURLsWarnInsteadOfVanishing(t *testing.T) {
	t.Parallel()
	langs := []entity.Language{{Id: 1, Code: "en", IsDefault: true}}
	blocks := []blockView{
		{
			Type:       entity.EmailBlockTypeCTAButton,
			BlockIndex: 0,
			CTAButton:  &ctaButtonView{},
			Translations: []entity.EmailBlockTranslation{{
				LanguageID: 1,
				CTALabel:   "SHOP",
				CTAURL:     "javascript:alert(1)",
			}},
		},
		{
			Type:       entity.EmailBlockTypeHeader,
			BlockIndex: 1,
			Translations: []entity.EmailBlockTranslation{{
				LanguageID: 1,
				Links:      []entity.EmailLink{{Label: "SALE", URL: "/sale"}},
			}},
		},
	}
	var warnings []Warning
	for i := range blocks {
		warnings = append(warnings, applyTranslation(&blocks[i], 1, langs)...)
	}
	if len(warnings) != 2 {
		t.Fatalf("warnings = %#v, want one per rejected URL", warnings)
	}
	if blocks[0].CTAButton.URL != "" || len(blocks[1].Copy.Links) != 0 {
		t.Fatalf("rejected URLs were not dropped: %#v", blocks)
	}
	if !strings.Contains(warnings[0].Reason, "cta_button") || warnings[1].BlockIndex != 1 {
		t.Fatalf("warnings = %#v", warnings)
	}

	// An http:// link is upgraded, not reported.
	upgraded := blockView{
		Type:       entity.EmailBlockTypeCTAButton,
		BlockIndex: 0,
		CTAButton:  &ctaButtonView{},
		Translations: []entity.EmailBlockTranslation{{
			LanguageID: 1, CTALabel: "SHOP", CTAURL: "http://grbpwr.com/sale",
		}},
	}
	if got := applyTranslation(&upgraded, 1, langs); len(got) != 0 {
		t.Fatalf("http link warned instead of being upgraded: %#v", got)
	}
	if upgraded.CTAButton.URL != "https://grbpwr.com/sale" {
		t.Fatalf("http link was not upgraded: %q", upgraded.CTAButton.URL)
	}
}

func TestResolveMissingProductSkipsBlockAndWarns(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	rep := mocks.NewMockRepository(t)
	products := mocks.NewMockProducts(t)
	rep.On("Products").Return(products)
	products.On("GetProductsByIds", mock.Anything, mock.Anything).Return([]entity.Colorway{}, nil)

	blocks := []entity.EmailBlock{
		{
			Type:        entity.EmailBlockTypeProductCard,
			ProductCard: &entity.EmailProductCardBlock{ProductID: 404},
		},
		{
			Type:     entity.EmailBlockTypeRichText,
			RichText: &entity.EmailRichTextBlock{},
			Translations: []entity.EmailBlockTranslation{{
				LanguageID: 1,
				Body:       "<p>Sibling survives</p>",
			}},
		},
	}
	r := newResolver(rep)
	r.prime(ctx, collectMediaIDs(blocks), collectProductIDs(blocks))
	resolved, warnings := resolveBlocks(ctx, r, blocks, 1, nil, "#ffffff", 0, 0)

	if len(warnings) != 1 {
		t.Fatalf("warnings = %#v, want exactly one", warnings)
	}
	if warnings[0].BlockIndex != 0 || !strings.Contains(warnings[0].Reason, "404") {
		t.Fatalf("warning = %#v", warnings[0])
	}
	if len(resolved) != 1 || resolved[0].Type != entity.EmailBlockTypeRichText {
		t.Fatalf("resolved = %#v, want surviving rich-text sibling", resolved)
	}
}
