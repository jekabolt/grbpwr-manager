package campaignrender

import (
	"strings"
	"testing"
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

func TestResolvePreparedRejectsHashBreakingTemplate(t *testing.T) {
	_, err := ResolvePrepared(Rendered{
		HTML: UnsubscribePlaceholder + UnsubscribePlaceholder,
		Text: UnsubscribePlaceholder,
	}, "https://backend.example.test/unsubscribe")
	if err == nil {
		t.Fatal("duplicate placeholder was accepted")
	}
}
