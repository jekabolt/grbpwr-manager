package translate

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/jekabolt/grbpwr-manager/internal/openrouter"
)

// fakeCompleter echoes a scripted response, or synthesizes one from the input items so tests can
// assert the passthrough/degrade logic without an LLM.
type fakeCompleter struct {
	enabled  bool
	respond  func(user string) (string, error)
	lastUser string
}

func (f *fakeCompleter) Enabled() bool { return f.enabled }
func (f *fakeCompleter) Complete(_ context.Context, _, user string, _ bool) (string, error) {
	f.lastUser = user
	return f.respond(user)
}

// echoItems parses the item ids out of the user prompt's embedded JSON and returns a response that
// maps each id to `transform(text)`.
func echoResponse(transform func(id int, text string) string) func(string) (string, error) {
	return func(user string) (string, error) {
		// The prompt embeds an example {"items":...} in the instructions and the ACTUAL input
		// JSON last — parse the last occurrence.
		start := strings.LastIndex(user, `{"items"`)
		var in struct {
			Items []struct {
				ID   int    `json:"id"`
				Text string `json:"text"`
			} `json:"items"`
		}
		if err := json.Unmarshal([]byte(user[start:]), &in); err != nil {
			return "", err
		}
		out := struct {
			Items []map[string]any `json:"items"`
		}{}
		for _, it := range in.Items {
			out.Items = append(out.Items, map[string]any{"id": it.ID, "text": transform(it.ID, it.Text)})
		}
		b, _ := json.Marshal(out)
		return string(b), nil
	}
}

func svc(f *fakeCompleter) *Service { return newWithCompleter(f) }

func TestTranslateDisabled(t *testing.T) {
	_, err := svc(&fakeCompleter{enabled: false}).Translate(context.Background(), "en", "fr", []string{"hi"})
	if !errors.Is(err, openrouter.ErrNotConfigured) {
		t.Fatalf("want ErrNotConfigured, got %v", err)
	}
}

func TestTranslateSameLocaleAndEmpty(t *testing.T) {
	s := svc(&fakeCompleter{enabled: true, respond: func(string) (string, error) { t.Fatal("should not call model"); return "", nil }})
	got, err := s.Translate(context.Background(), "en", "en", []string{"a", "b"})
	if err != nil || got[0] != "a" || got[1] != "b" {
		t.Fatalf("same-locale passthrough failed: %v %v", got, err)
	}
	got, err = s.Translate(context.Background(), "en", "fr", []string{"", "  "})
	if err != nil || got[0] != "" || got[1] != "  " {
		t.Fatalf("all-empty passthrough failed: %v %v", got, err)
	}
}

func TestTranslateHappyPath(t *testing.T) {
	f := &fakeCompleter{enabled: true, respond: echoResponse(func(_ int, text string) string {
		return "FR:" + text // preserves any tags/placeholders since it only prefixes
	})}
	// Item 1 has HTML + a placeholder; the prefix keeps both, so it should be accepted.
	got, err := svc(f).Translate(context.Background(), "en", "fr",
		[]string{"HELLO", `WELCOME TO <span style="x">GRBPWR</span> {{.Tier}}`, ""})
	if err != nil {
		t.Fatalf("Translate: %v", err)
	}
	if got[0] != "FR:HELLO" {
		t.Errorf("item0 = %q", got[0])
	}
	if !strings.Contains(got[1], "<span") || !strings.Contains(got[1], "{{.Tier}}") || !strings.HasPrefix(got[1], "FR:") {
		t.Errorf("item1 lost markup/placeholder or prefix: %q", got[1])
	}
	if got[2] != "" {
		t.Errorf("empty item should stay empty, got %q", got[2])
	}
}

func TestTranslateDegradesOnBrokenMarkup(t *testing.T) {
	// Model drops the <span> and the placeholder → must degrade to the SOURCE for that item.
	f := &fakeCompleter{enabled: true, respond: echoResponse(func(id int, text string) string {
		if id == 0 {
			return "BROKEN no tags here" // src had a span + placeholder
		}
		return "FR:" + text
	})}
	src := []string{`GO <a href="u">HERE</a> {{.OrderID}}`, "PLAIN TEXT"}
	got, err := svc(f).Translate(context.Background(), "en", "de", src)
	if err != nil {
		t.Fatalf("Translate: %v", err)
	}
	if got[0] != src[0] {
		t.Errorf("broken-markup item should keep source %q, got %q", src[0], got[0])
	}
	if got[1] != "FR:PLAIN TEXT" {
		t.Errorf("clean item should translate, got %q", got[1])
	}
}

func TestTranslateMissingIDDegradesToSource(t *testing.T) {
	f := &fakeCompleter{enabled: true, respond: func(string) (string, error) {
		return `{"items":[{"id":0,"text":"only one"}]}`, nil // 2 requested, 1 returned
	}}
	got, err := svc(f).Translate(context.Background(), "en", "ja", []string{"a", "b"})
	if err != nil {
		t.Fatalf("Translate: %v", err)
	}
	if got[0] != "only one" || got[1] != "b" {
		t.Fatalf("an omitted id must keep its source text, got %#v", got)
	}
}

func TestTranslateUnusableOutputErrors(t *testing.T) {
	f := &fakeCompleter{enabled: true, respond: func(string) (string, error) {
		return `{"items":[]}`, nil
	}}
	_, err := svc(f).Translate(context.Background(), "en", "ja", []string{"a", "b"})
	if err == nil || !strings.Contains(err.Error(), "none of the") {
		t.Fatalf("want unusable-output error, got %v", err)
	}
}

func TestTranslateChunksLargeBatches(t *testing.T) {
	var perRequest []int
	echo := echoResponse(func(_ int, text string) string { return "FR:" + text })
	f := &fakeCompleter{enabled: true}
	f.respond = func(user string) (string, error) {
		perRequest = append(perRequest, requestedItemCount(t, user))
		return echo(user)
	}

	items := make([]string, maxItemsPerRequest*2+3)
	for i := range items {
		items[i] = "ITEM"
	}
	got, err := svc(f).Translate(context.Background(), "en", "fr", items)
	if err != nil {
		t.Fatalf("Translate: %v", err)
	}
	if len(perRequest) != 3 {
		t.Fatalf("want 3 chunked requests, got %d (%v)", len(perRequest), perRequest)
	}
	for _, count := range perRequest {
		if count > maxItemsPerRequest {
			t.Fatalf("request carried %d items, over the %d cap (%v)", count, maxItemsPerRequest, perRequest)
		}
	}
	for i, value := range got {
		if value != "FR:ITEM" {
			t.Fatalf("item %d not translated: %q", i, value)
		}
	}
}

func TestTranslateKeepsSucceededChunksOnFailure(t *testing.T) {
	echo := echoResponse(func(_ int, text string) string { return "FR:" + text })
	calls := 0
	f := &fakeCompleter{enabled: true}
	f.respond = func(user string) (string, error) {
		calls++
		if calls == 2 {
			return "", errors.New("provider timeout")
		}
		return echo(user)
	}

	items := make([]string, maxItemsPerRequest+2)
	for i := range items {
		items[i] = "ITEM"
	}
	got, err := svc(f).Translate(context.Background(), "en", "fr", items)
	if err == nil {
		t.Fatal("want the chunk failure reported")
	}
	if got[0] != "FR:ITEM" {
		t.Fatalf("first chunk should still be translated, got %q", got[0])
	}
	if got[len(got)-1] != "ITEM" {
		t.Fatalf("failed chunk should keep its source text, got %q", got[len(got)-1])
	}
}

// requestedItemCount counts the items embedded in the user prompt's trailing input JSON.
func requestedItemCount(t *testing.T, user string) int {
	t.Helper()
	start := strings.LastIndex(user, `{"items"`)
	var in struct {
		Items []json.RawMessage `json:"items"`
	}
	if err := json.Unmarshal([]byte(user[start:]), &in); err != nil {
		t.Fatalf("parse request items: %v", err)
	}
	return len(in.Items)
}
