package mail

import (
	"strings"
	"testing"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
)

func TestCampaignUnsubscribeURLIsTopicScopedAndNormalized(t *testing.T) {
	mailer := &Mailer{c: &Config{
		UnsubscribeBaseURL: "https://backend.example.test/",
		UnsubscribePepper:  "test-pepper",
	}}
	first, err := mailer.CampaignUnsubscribeURL(
		entity.EmailCampaignTopicEvents,
		" Customer@Example.COM ",
	)
	if err != nil {
		t.Fatal(err)
	}
	second, err := mailer.CampaignUnsubscribeURL(
		entity.EmailCampaignTopicEvents,
		"customer@example.com",
	)
	if err != nil {
		t.Fatal(err)
	}
	if first != second {
		t.Fatalf("normalized URLs differ:\n%s\n%s", first, second)
	}
	if !strings.Contains(first, "/list-unsubscribe/events/") {
		t.Fatalf("URL is not topic scoped: %s", first)
	}
	if strings.Contains(first, "=") {
		t.Fatalf("URL-safe base64 path contains padding: %s", first)
	}
}

func TestCampaignDispatchRequiresUnsubscribeConfiguration(t *testing.T) {
	mailer := &Mailer{c: &Config{}}
	if err := mailer.CampaignDispatchConfigured(); err == nil {
		t.Fatal("empty unsubscribe configuration was accepted")
	}
	mailer.c.UnsubscribeBaseURL = "https://backend.example.test"
	if err := mailer.CampaignDispatchConfigured(); err == nil {
		t.Fatal("missing unsubscribe pepper was accepted")
	}
}
