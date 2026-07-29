package campaignrender

import (
	"context"
	"strings"
	"testing"

	mocks "github.com/jekabolt/grbpwr-manager/internal/dependency/mocks"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
)

// TestFooterLocalization proves the campaign footer renders the caller-supplied localized labels
// (Input.Footer) in both the HTML and the plaintext alternative, and falls back to English when a
// field is empty. A spacer block keeps the render free of product/media repo calls.
func TestFooterLocalization(t *testing.T) {
	renderer, err := New()
	if err != nil {
		t.Fatalf("New(): %v", err)
	}
	rep := mocks.NewMockRepository(t) // no media/product blocks → no repo calls expected

	blocks := []entity.EmailBlock{{Type: entity.EmailBlockTypeSpacer, Spacer: &entity.EmailSpacerBlock{Height: 24}}}

	out, _, err := renderer.Render(context.Background(), rep, Input{
		Blocks:         blocks,
		LanguageID:     5,
		Langs:          []entity.Language{{Id: 5, Code: "ja", IsDefault: false}, {Id: 1, Code: "en", IsDefault: true}},
		UnsubscribeURL: "https://grbpwr.com/unsubscribe/token",
		Footer: entity.EmailFooterStrings{
			Help:      "お困りですか？",
			Faq:       "よくある質問",
			Aftersale: "アフターサービス",
			UnsubPre:  "メール配信の停止をご希望の場合は、",
			UnsubWord: "配信停止",
			// (nothing else) — all present here
		},
	})
	if err != nil {
		t.Fatalf("Render(): %v", err)
	}

	for _, want := range []string{"お困りですか？", "よくある質問", "アフターサービス", "メール配信の停止をご希望の場合は、", "配信停止"} {
		if !strings.Contains(out.HTML, want) {
			t.Errorf("HTML missing localized footer %q", want)
		}
		if !strings.Contains(out.Text, want) {
			t.Errorf("plaintext missing localized footer %q", want)
		}
	}
	// The hardcoded English must NOT appear when localized values are provided.
	for _, notWant := range []string{"NEED HELP?", "AFTER-SALE SERVICES"} {
		if strings.Contains(out.HTML, notWant) {
			t.Errorf("HTML still shows hardcoded %q", notWant)
		}
	}

	// Empty Footer → English fallback (unchanged behavior).
	out2, _, err := renderer.Render(context.Background(), rep, Input{
		Blocks:         blocks,
		LanguageID:     1,
		Langs:          []entity.Language{{Id: 1, Code: "en", IsDefault: true}},
		UnsubscribeURL: "https://grbpwr.com/unsubscribe/token",
	})
	if err != nil {
		t.Fatalf("Render() fallback: %v", err)
	}
	if !strings.Contains(out2.HTML, "NEED HELP?") || !strings.Contains(out2.Text, "NEED HELP?") {
		t.Error("empty Footer should fall back to the English literal")
	}
}
