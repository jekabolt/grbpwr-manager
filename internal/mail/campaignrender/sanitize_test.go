package campaignrender

import (
	"strings"
	"testing"
)

func TestSanitizeRichTextEmailAllowlist(t *testing.T) {
	t.Parallel()
	input := `<h2 style="color:red">Hello</h2><script>alert(1)</script>` +
		`<img src="https://tracker.example/pixel.gif">` +
		`<a href="javascript:alert(1)">bad</a>` +
		`<a href="data:text/plain,no">data</a>` +
		`<a href="https://grbpwr.com/p/test">safe</a>`

	got := SanitizeRichText(input)
	for _, forbidden := range []string{"<script", "<img", "style=", "javascript:", "data:"} {
		if strings.Contains(strings.ToLower(got), forbidden) {
			t.Fatalf("sanitized output contains %q: %s", forbidden, got)
		}
	}
	for _, required := range []string{`href="https://grbpwr.com/p/test"`, `target="_blank"`, "noopener", "noreferrer"} {
		if !strings.Contains(got, required) {
			t.Fatalf("sanitized output does not contain %q: %s", required, got)
		}
	}
}

func TestSanitizeSafeURLAndColor(t *testing.T) {
	t.Parallel()
	urlTests := map[string]string{
		"https://grbpwr.com/p/test": "https://grbpwr.com/p/test",
		"mailto:test@example.com":   "mailto:test@example.com",
		"http://grbpwr.com":         "",
		"javascript:alert(1)":       "",
		"data:text/plain,no":        "",
		"/relative":                 "",
	}
	for input, want := range urlTests {
		if got := safeURL(input); got != want {
			t.Errorf("safeURL(%q) = %q, want %q", input, got, want)
		}
	}

	colorTests := map[string]string{
		"#fff":        "#fff",
		"#A0b1C2":     "#A0b1C2",
		"red":         "#123456",
		"#123456;url": "#123456",
	}
	for input, want := range colorTests {
		if got := safeColor(input, "#123456"); got != want {
			t.Errorf("safeColor(%q) = %q, want %q", input, got, want)
		}
	}
}
