package campaignrender

import (
	"strings"
	"testing"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
)

func TestPrepareAndResolvePreparedUnsubscribePlaceholder(t *testing.T) {
	renderer := &Renderer{}
	prepared, _, err := renderer.Prepare(func(unsubscribeURL string) (Rendered, []Warning, error) {
		return Rendered{
			HTML: `<a href="` + unsubscribeURL + `">unsubscribe</a>`,
			Text: "Unsubscribe: " + unsubscribeURL,
		}, nil, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(prepared.HTML, UnsubscribePlaceholder) != 1 ||
		strings.Count(prepared.Text, UnsubscribePlaceholder) != 1 {
		t.Fatalf("prepared snapshot = %#v", prepared)
	}
	const recipientURL = "https://backend.example.test/topic/address/token"
	resolved, err := ResolvePrepared(prepared, recipientURL)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(resolved.HTML, recipientURL) != 1 ||
		strings.Count(resolved.Text, recipientURL) != 1 {
		t.Fatalf("resolved snapshot = %#v", resolved)
	}
}

func TestPrepareNeutralizesAuthoredPlaceholderLiteral(t *testing.T) {
	renderer := &Renderer{}
	prepared, _, err := renderer.Prepare(func(unsubscribeURL string) (Rendered, []Warning, error) {
		// The admin typed the reserved placeholder into copy.
		return Rendered{
			HTML: `<p>use ` + UnsubscribePlaceholder + `</p><a href="` + unsubscribeURL + `">unsubscribe</a>`,
			Text: "use " + UnsubscribePlaceholder + "\nUnsubscribe: " + unsubscribeURL,
		}, nil, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(prepared.HTML, UnsubscribePlaceholder) != 1 ||
		strings.Count(prepared.Text, UnsubscribePlaceholder) != 1 {
		t.Fatalf("authored placeholder collided with the injected one: %#v", prepared)
	}
	// The snapshot must still resolve, otherwise every dispatch batch fails as a payload mismatch.
	if _, err := ResolvePrepared(prepared, "https://backend.example.test/unsubscribe"); err != nil {
		t.Fatalf("ResolvePrepared: %v", err)
	}
}

func TestSanitizeBlocksNeutralizesPlaceholderLiteral(t *testing.T) {
	blocks := []entity.EmailBlock{{
		Type: entity.EmailBlockTypeTwoColumn,
		TwoColumn: &entity.EmailTwoColumnBlock{
			Left: []entity.EmailBlock{{
				Type: entity.EmailBlockTypeHeader,
				Translations: []entity.EmailBlockTranslation{{
					LanguageID: 1,
					Heading:    "OPT OUT " + UnsubscribePlaceholder,
				}},
			}},
		},
	}}
	SanitizeBlocks(blocks)
	if got := blocks[0].TwoColumn.Left[0].Translations[0].Heading; strings.Contains(got, UnsubscribePlaceholder) {
		t.Fatalf("heading kept the reserved placeholder: %q", got)
	}
}

func TestResolvePreparedRejectsHashBreakingTemplate(t *testing.T) {
	_, err := ResolvePrepared(Rendered{
		HTML: UnsubscribePlaceholder + UnsubscribePlaceholder,
		Text: UnsubscribePlaceholder,
	}, "https://backend.example.test/unsubscribe")
	if err == nil {
		t.Fatal("duplicate placeholder was accepted")
	}
}
