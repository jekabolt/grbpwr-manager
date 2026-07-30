// Package translate localizes short marketing/UI strings between locales via an LLM
// (OpenRouter), preserving inline HTML, URLs, interpolation placeholders and brand terms. It
// backs the admin "auto-translate campaign" action: the admin authors a campaign in English and
// this fills the other locales' block/subject translations for review before launch.
package translate

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strings"

	"github.com/jekabolt/grbpwr-manager/internal/openrouter"
)

// completer is the OpenRouter primitive the service needs (satisfied by *openrouter.Client),
// narrowed so tests can supply a fake.
type completer interface {
	Complete(ctx context.Context, systemPrompt, userPrompt string, jsonMode bool) (string, error)
	Enabled() bool
}

// Service translates batches of strings between locales. A nil-backed service is valid and simply
// disabled (Enabled() == false).
type Service struct {
	client completer
}

// New builds a Service over an OpenRouter client (which may be a disabled/nil-key client).
func New(client *openrouter.Client) *Service { return &Service{client: client} }

// newWithCompleter is the test seam.
func newWithCompleter(c completer) *Service { return &Service{client: c} }

// Enabled reports whether translation is configured (API key present).
func (s *Service) Enabled() bool { return s != nil && s.client != nil && s.client.Enabled() }

var localeNames = map[string]string{
	"en": "English", "fr": "French", "de": "German", "it": "Italian",
	"ja": "Japanese", "zh": "Simplified Chinese", "ko": "Korean",
}

func localeName(code string) string {
	if n, ok := localeNames[strings.ToLower(strings.TrimSpace(code))]; ok {
		return n
	}
	return code
}

const systemPrompt = `You are a professional localization translator for GRBPWR, a high-end streetwear brand. Translate marketing and UI copy faithfully in a confident, minimal, fashion-forward brand voice — natural in the target language, not literal.

Rules:
- Translate the "text" of each item. Keep each item's "id" unchanged.
- Preserve ALL inline HTML EXACTLY: every tag, attribute and the structure. Translate only the human-readable text between tags. Never add, remove, reorder, or alter tags/attributes.
- NEVER translate: URLs, email addresses, the brand name "GRBPWR", product names, or any {{...}} interpolation placeholder — copy them verbatim. You may move a {{...}} within a sentence to fit grammar, but never rename or drop one.
- Match the source casing convention: if the source is ALL-CAPS, output ALL-CAPS where the target script has letter case (CJK has no case).
- Do not add explanations. Return ONLY the requested JSON object — no prose, no markdown code fences.`

// maxItemsPerRequest bounds how many strings travel in one model request. A single unbounded
// request is the difference between "flaky" and "always fails": provider output limits and the
// 60s HTTP timeout make a large batch fail deterministically, so long campaigns could never be
// translated at all.
const maxItemsPerRequest = 25

// item is one string queued for translation, paired with its index in the caller's slice.
type item struct {
	idx  int
	text string
}

// Translate translates each item from sourceLocale to targetLocale, returning a slice of the same
// length and order. Empty items and a same-locale request pass through unchanged. Preserves inline
// HTML, URLs, {{...}} placeholders and brand terms; if the model returns markup whose tag multiset
// differs from the source (or omits/drops an item), that item degrades to the SOURCE text rather
// than shipping broken HTML or voiding the batch. Items are sent in chunks of maxItemsPerRequest;
// on error the partially translated slice is returned alongside it, so a caller may keep the
// chunks that did land.
func (s *Service) Translate(ctx context.Context, sourceLocale, targetLocale string, items []string) ([]string, error) {
	if !s.Enabled() {
		return nil, openrouter.ErrNotConfigured
	}
	out := append([]string(nil), items...)
	if strings.EqualFold(sourceLocale, targetLocale) {
		return out, nil
	}
	var todo []item
	for i, it := range items {
		if strings.TrimSpace(it) != "" {
			todo = append(todo, item{i, it})
		}
	}
	if len(todo) == 0 {
		return out, nil
	}
	for start := 0; start < len(todo); start += maxItemsPerRequest {
		end := min(start+maxItemsPerRequest, len(todo))
		if err := s.translateChunk(ctx, sourceLocale, targetLocale, todo[start:end], out); err != nil {
			return out, err
		}
	}
	return out, nil
}

// translateChunk translates one bounded chunk in a single JSON-in/JSON-out call and writes the
// accepted results into out at each item's original index.
func (s *Service) translateChunk(
	ctx context.Context,
	sourceLocale, targetLocale string,
	chunk []item,
	out []string,
) error {
	inItems := make([]map[string]any, len(chunk))
	for i, p := range chunk {
		inItems[i] = map[string]any{"id": i, "text": p.text}
	}
	inJSON, err := json.Marshal(map[string]any{"items": inItems})
	if err != nil {
		return fmt.Errorf("translate: marshal items: %w", err)
	}
	user := fmt.Sprintf(
		"Translate the \"text\" of each item from %s to %s.\nReturn ONLY a JSON object of the form {\"items\":[{\"id\":<same id>,\"text\":\"<translation>\"}]} containing EVERY id.\n\n%s",
		localeName(sourceLocale), localeName(targetLocale), string(inJSON),
	)

	content, err := s.client.Complete(ctx, systemPrompt, user, true)
	if err != nil {
		return fmt.Errorf("translate %s→%s: %w", sourceLocale, targetLocale, err)
	}
	translated, err := parseItems(content, len(chunk))
	if err != nil {
		return err
	}
	for i, p := range chunk {
		candidate := strings.TrimSpace(translated[i])
		if candidate == "" || !preservesMarkup(p.text, translated[i]) {
			continue // keep source text for this field (safe degrade)
		}
		out[p.idx] = translated[i]
	}
	return nil
}

// parseItems extracts the JSON object from model content and maps id→text. An id the model omitted
// comes back empty, which the caller degrades to the source text for that one string — dropping a
// single id must not void a whole (already paid for) batch. Output holding none of the requested
// ids is a broken response and is an error.
func parseItems(content string, n int) ([]string, error) {
	js := extractJSONObject(content)
	if js == "" {
		return nil, fmt.Errorf("translate: model output contained no JSON object")
	}
	var parsed struct {
		Items []struct {
			ID   int    `json:"id"`
			Text string `json:"text"`
		} `json:"items"`
	}
	if err := json.Unmarshal([]byte(js), &parsed); err != nil {
		return nil, fmt.Errorf("translate: model output not valid JSON: %w", err)
	}
	out := make([]string, n)
	seen := 0
	for _, it := range parsed.Items {
		if it.ID < 0 || it.ID >= n || out[it.ID] != "" {
			continue
		}
		out[it.ID] = it.Text
		seen++
	}
	if seen == 0 {
		return nil, fmt.Errorf("translate: model output contained none of the %d requested item ids", n)
	}
	return out, nil
}

func extractJSONObject(s string) string {
	s = strings.TrimSpace(s)
	if strings.HasPrefix(s, "```") {
		s = strings.TrimPrefix(s, "```")
		if i := strings.IndexByte(s, '\n'); i >= 0 {
			s = s[i+1:]
		}
		if j := strings.LastIndex(s, "```"); j >= 0 {
			s = s[:j]
		}
		s = strings.TrimSpace(s)
	}
	start := strings.IndexByte(s, '{')
	end := strings.LastIndexByte(s, '}')
	if start < 0 || end < 0 || end < start {
		return ""
	}
	return s[start : end+1]
}

var (
	htmlTagRe     = regexp.MustCompile(`</?[A-Za-z][^>]*>`)
	placeholderRe = regexp.MustCompile(`\{\{\s*\.?[A-Za-z0-9_]+\s*\}\}`)
)

// preservesMarkup reports whether dst keeps the same multiset of HTML tags and the same set of
// {{...}} placeholders as src — the integrity gate that keeps a mistranslation from injecting or
// dropping markup/placeholders. Whitespace inside placeholders is normalized.
func preservesMarkup(src, dst string) bool {
	return equalMultiset(htmlTagRe.FindAllString(src, -1), htmlTagRe.FindAllString(dst, -1)) &&
		equalSet(normPlaceholders(src), normPlaceholders(dst))
}

func normPlaceholders(s string) []string {
	m := placeholderRe.FindAllString(s, -1)
	out := make([]string, len(m))
	for i, p := range m {
		out[i] = strings.ReplaceAll(p, " ", "")
	}
	return out
}

func equalMultiset(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	as := append([]string(nil), a...)
	bs := append([]string(nil), b...)
	sort.Strings(as)
	sort.Strings(bs)
	for i := range as {
		if as[i] != bs[i] {
			return false
		}
	}
	return true
}

func equalSet(a, b []string) bool {
	seen := make(map[string]int, len(a))
	for _, x := range a {
		seen[x]++
	}
	for _, x := range b {
		seen[x]--
	}
	for _, v := range seen {
		if v != 0 {
			return false
		}
	}
	return true
}
