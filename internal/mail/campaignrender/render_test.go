package campaignrender

import (
	"context"
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jekabolt/grbpwr-manager/internal/dependency/mocks"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/mock"
)

var updateGolden = flag.Bool("update", false, "update campaign renderer golden files")

func goldenTranslation(heading, body string) []entity.EmailBlockTranslation {
	return []entity.EmailBlockTranslation{{
		LanguageID: 1,
		Heading:    heading,
		Subheading: "Built for inboxes",
		Body:       body,
		Caption:    "A deterministic caption",
		CTALabel:   "EXPLORE",
		CTAURL:     "https://grbpwr.com/archive",
		AltText:    heading + " image",
		Preheader:  "The first header supplies this preheader",
		Links: []entity.EmailLink{{
			Label: "Shop",
			URL:   "https://grbpwr.com/shop",
		}},
	}}
}

func goldenMedia(id int, name string, width, height int) entity.MediaFull {
	return entity.MediaFull{
		Id: id,
		MediaItem: entity.MediaItem{
			CompressedMediaURL: "https://files.grbpwr.com/campaign/" + name + ".webp",
			CompressedWidth:    width,
			CompressedHeight:   height,
		},
	}
}

func goldenProduct(id int, sku, name string, price int64, sale int64) entity.Colorway {
	product := entity.Colorway{
		Id:              id,
		SKU:             sku,
		LifecycleStatus: entity.ColorwayStatusActive,
		ProductDisplay: entity.ColorwayDisplay{
			ProductBody: entity.ColorwayBody{
				ProductBodyInsert: entity.ColorwayBodyInsert{
					Brand: "GRBPWR",
				},
				Translations: []entity.ColorwayTranslationInsert{{
					LanguageId: 1,
					Name:       name,
				}},
			},
			Thumbnail: goldenMedia(id+1000, "product-"+sku, 720, 900),
		},
		Prices: []entity.ColorwayPrice{{
			Currency: "EUR",
			Price:    decimal.NewFromInt(price),
		}},
	}
	if sale > 0 {
		product.ProductDisplay.ProductBody.ProductBodyInsert.SalePercentage = decimal.NullDecimal{
			Decimal: decimal.NewFromInt(sale),
			Valid:   true,
		}
	}
	return product
}

func goldenBlocks() []entity.EmailBlock {
	return []entity.EmailBlock{
		{
			Type:            entity.EmailBlockTypeHeader,
			Header:          &entity.EmailHeaderBlock{LogoMediaID: 1},
			BackgroundColor: "#ffffff",
			Translations:    goldenTranslation("THE NEW SYSTEM", ""),
		},
		{
			Type:            entity.EmailBlockTypeImageLink,
			ImageLink:       &entity.EmailImageLinkBlock{MediaID: 2, URL: "https://grbpwr.com/archive"},
			BackgroundColor: "#f4f2ec",
			Translations:    goldenTranslation("Campaign image", ""),
		},
		{
			Type:            entity.EmailBlockTypeRichText,
			RichText:        &entity.EmailRichTextBlock{},
			BackgroundColor: "#ffffff",
			Translations: goldenTranslation("Rich", `<h2>Material intelligence</h2>`+
				`<p>Safe <strong>rich text</strong>.</p><script>unsafe()</script>`),
		},
		{
			Type:            entity.EmailBlockTypeProductCard,
			ProductCard:     &entity.EmailProductCardBlock{ProductID: 101},
			BackgroundColor: "#ffffff",
			Translations:    goldenTranslation("Featured", ""),
		},
		{
			Type:            entity.EmailBlockTypeProductGrid,
			ProductGrid:     &entity.EmailProductGridBlock{ProductIDs: []int{101, 102}, Columns: 2},
			BackgroundColor: "#ffffff",
			Translations:    goldenTranslation("Grid", ""),
		},
		{
			Type:            entity.EmailBlockTypeCTAButton,
			CTAButton:       &entity.EmailCTAButtonBlock{Style: "solid", Alignment: "center"},
			BackgroundColor: "#ffffff",
			Translations:    goldenTranslation("CTA", ""),
		},
		{
			Type:            entity.EmailBlockTypeDivider,
			Divider:         &entity.EmailDividerBlock{Color: "#0e0e0c", Height: 2},
			BackgroundColor: "#ffffff",
		},
		{
			Type:            entity.EmailBlockTypeSpacer,
			Spacer:          &entity.EmailSpacerBlock{Height: 24},
			BackgroundColor: "#ffffff",
		},
		{
			Type: entity.EmailBlockTypeTwoColumn,
			TwoColumn: &entity.EmailTwoColumnBlock{
				Left: []entity.EmailBlock{{
					Type:         entity.EmailBlockTypeRichText,
					RichText:     &entity.EmailRichTextBlock{},
					Translations: goldenTranslation("Left", "<p>Left column</p>"),
				}},
				Right: []entity.EmailBlock{{
					Type:         entity.EmailBlockTypeCTAButton,
					CTAButton:    &entity.EmailCTAButtonBlock{Style: "outline", Alignment: "center"},
					Translations: goldenTranslation("Right", ""),
				}},
			},
			BackgroundColor: "#f4f2ec",
		},
		{
			Type: entity.EmailBlockTypeSocialLinks,
			SocialLinks: &entity.EmailSocialLinksBlock{Links: []entity.EmailSocialLink{
				{Network: "instagram", URL: "https://instagram.com/grbpwr"},
				{Network: "github", URL: "https://github.com/grbpwr"},
			}},
			BackgroundColor: "#ffffff",
		},
		{
			Type:            entity.EmailBlockTypeCountdown,
			Countdown:       &entity.EmailCountdownBlock{EndsAt: 1786557600},
			BackgroundColor: "#ffffff",
			Translations:    goldenTranslation("RELEASE WINDOW", ""),
		},
		{
			Type:            entity.EmailBlockTypeVideoThumb,
			VideoThumb:      &entity.EmailVideoThumbBlock{MediaID: 3, VideoURL: "https://vimeo.com/123"},
			BackgroundColor: "#0e0e0c",
			Translations:    goldenTranslation("PROCESS FILM", ""),
		},
	}
}

func TestRenderGoldenRepresentativeBlocks(t *testing.T) {
	ctx := context.Background()
	rep := mocks.NewMockRepository(t)
	mediaRepo := mocks.NewMockMedia(t)
	productsRepo := mocks.NewMockProducts(t)
	rep.On("Media").Return(mediaRepo)
	rep.On("Products").Return(productsRepo)

	mediaRepo.On("GetMediaByIds", mock.Anything, mock.Anything).Return(map[int]entity.MediaFull{
		1: goldenMedia(1, "logo", 108, 108),
		2: goldenMedia(2, "hero", 1200, 800),
		3: goldenMedia(3, "video", 1200, 675),
	}, nil)
	productsRepo.On("GetProductsByIds", mock.Anything, mock.Anything).Return([]entity.Colorway{
		goldenProduct(101, "SS26-001", "SYSTEM JACKET", 300, 20),
		goldenProduct(102, "SS26-002", "SYSTEM TROUSER", 220, 0),
	}, nil)

	renderer, err := New()
	if err != nil {
		t.Fatalf("New() error: %v", err)
	}
	renderInput := func(blocks []entity.EmailBlock) (Rendered, []Warning, error) {
		return renderer.Render(ctx, rep, Input{
			Blocks:          blocks,
			BackgroundColor: "#f4f2ec",
			LanguageID:      9, // exercises default-language fallback to English
			Langs:           []entity.Language{{Id: 1, Code: "en", IsDefault: true}},
			UnsubscribeURL:  "https://grbpwr.com/unsubscribe/golden",
		})
	}

	blocks := goldenBlocks()
	rendered, warnings, err := renderInput(blocks)
	if err != nil {
		t.Fatalf("Render() error: %v", err)
	}
	if len(warnings) != 0 {
		t.Fatalf("Render() warnings: %#v", warnings)
	}
	if strings.Contains(strings.ToLower(rendered.HTML), "<script") {
		t.Fatal("rendered HTML contains a script element")
	}
	if strings.Contains(strings.ToLower(rendered.HTML), "<video") {
		t.Fatal("rendered HTML contains a video element")
	}
	for _, required := range []string{"THE NEW SYSTEM", "SYSTEM JACKET", "€240.00", "ENDS 12 AUG 2026", "https://grbpwr.com/unsubscribe/golden"} {
		if !strings.Contains(rendered.HTML, required) {
			t.Fatalf("rendered HTML does not contain %q", required)
		}
	}
	if !strings.Contains(rendered.Text, "GRBPWR SYSTEM JACKET") ||
		!strings.Contains(rendered.Text, "€240.00") {
		t.Fatalf("plaintext did not include semantic product snapshot:\n%s", rendered.Text)
	}

	assertGolden(t, "full.html.golden", rendered.HTML)
	assertGolden(t, "full.txt.golden", rendered.Text)

	blockNames := []string{
		"header",
		"image_link",
		"rich_text",
		"product_card",
		"product_grid",
		"cta_button",
		"divider",
		"spacer",
		"two_column",
		"social_links",
		"countdown",
		"video_thumb",
	}
	for i, name := range blockNames {
		blockRendered, blockWarnings, err := renderInput([]entity.EmailBlock{blocks[i]})
		if err != nil {
			t.Fatalf("render %s block: %v", name, err)
		}
		if len(blockWarnings) != 0 {
			t.Fatalf("render %s block warnings: %#v", name, blockWarnings)
		}
		assertGolden(t, "block_"+name+".html.golden", blockRendered.HTML)
		assertGolden(t, "block_"+name+".txt.golden", blockRendered.Text)
	}
}

func assertGolden(t *testing.T, name, got string) {
	t.Helper()
	path := filepath.Join("testdata", name)
	if *updateGolden {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatalf("create golden directory: %v", err)
		}
		if err := os.WriteFile(path, []byte(got), 0o644); err != nil {
			t.Fatalf("update golden %s: %v", name, err)
		}
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read golden %s: %v (run go test ./internal/mail/campaignrender -run Golden -update)", name, err)
	}
	if got != string(want) {
		t.Fatalf("%s mismatch (run with -update to refresh)", name)
	}
}
