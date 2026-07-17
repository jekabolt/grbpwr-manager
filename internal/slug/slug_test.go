package slug

import (
	"errors"
	"strings"
	"testing"
)

func TestKebab(t *testing.T) {
	tests := []struct{ in, want string }{
		{"Black Summer Shirt", "black-summer-shirt"},
		{"  spaced  out  ", "spaced-out"},
		{"Weird!!!Chars@@@Here", "weird-chars-here"},
		{"чёрная рубашка", "chernaya-rubashka"},
		{"", ""},
		{"!!!", ""},
		{"already-kebab", "already-kebab"},
		{"UPPER CASE", "upper-case"},
		{"multi---dash", "multi-dash"},
	}
	for _, tt := range tests {
		if got := Kebab(tt.in); got != tt.want {
			t.Errorf("Kebab(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestKebabMaxLen(t *testing.T) {
	long := ""
	for range 50 {
		long += "word "
	}
	got := Kebab(long)
	if len(got) > MaxPrettyLen {
		t.Errorf("Kebab exceeded MaxPrettyLen: len=%d", len(got))
	}
	if got == "" || got[len(got)-1] == '-' {
		t.Errorf("Kebab truncation left a trailing dash or empty: %q", got)
	}
}

func TestProductPath(t *testing.T) {
	tests := []struct {
		name, sku, want string
	}{
		{"Black Summer Shirt", "SS26-00021-BLK", "/p/black-summer-shirt-ss26-00021-blk"},
		{"", "SS26-00021-BLK", "/p/ss26-00021-blk"},    // empty name -> no leading dash
		{"!!!", "SS26-00021-BLK", "/p/ss26-00021-blk"}, // unusable name
		{"Пальто", "FW25-00003-NAV", "/p/palto-fw25-00003-nav"},
	}
	for _, tt := range tests {
		if got := ProductPath(tt.name, tt.sku); got != tt.want {
			t.Errorf("ProductPath(%q,%q) = %q, want %q", tt.name, tt.sku, got, tt.want)
		}
	}
}

func TestTimelinePath(t *testing.T) {
	tests := []struct {
		heading, code, want string
	}{
		{"Fall Drop 2026", "A1B2C3", "/timeline/fall-drop-2026-A1B2C3"},
		{"", "A1B2C3", "/timeline/A1B2C3"},
	}
	for _, tt := range tests {
		if got := TimelinePath(tt.heading, tt.code); got != tt.want {
			t.Errorf("TimelinePath(%q,%q) = %q, want %q", tt.heading, tt.code, got, tt.want)
		}
	}
}

func TestParseProductTail(t *testing.T) {
	ok := []struct{ path, want string }{
		{"/p/black-summer-shirt-ss26-00021-blk", "SS26-00021-BLK"}, // pretty with hyphens, lowercase
		{"/p/ss26-00021-blk", "SS26-00021-BLK"},                    // empty pretty
		{"/p/SS26-00021-BLK", "SS26-00021-BLK"},                    // already upper
		{"/p/palto-fw25-00003-nav", "FW25-00003-NAV"},
		{"/p/ss26-00021-red-ss26-00021-blk", "SS26-00021-BLK"}, // pretty ends in a SKU-like fragment: take the last
		{"/p/a-b-c-d-e-ss26-00021-blk", "SS26-00021-BLK"},      // many hyphens in pretty
		{"/p/ss26-00021-0a1", "SS26-00021-0A1"},                // color may carry digits
	}
	for _, c := range ok {
		got, err := ParseProductTail(c.path)
		if err != nil || got != c.want {
			t.Errorf("ParseProductTail(%q) = %q, %v; want %q, nil", c.path, got, err, c.want)
		}
	}

	bad := []string{
		"/x/ss26-00021-blk",           // wrong prefix
		"/product/male/name/42",       // old scheme
		"/timeline/ss26-00021-blk",    // wrong prefix
		"p/ss26-00021-blk",            // missing leading slash (strict)
		"/p/",                         // empty token
		"/p/ss26-00021-blk?utm=x",     // query
		"/p/ss26-00021-blk#frag",      // fragment
		"/p/ss26-00021-blk/",          // trailing slash / garbage
		"/p/ss26-00021-blk-",          // trailing dash
		"/p/ss26-00021-blk-04",        // variant-size suffix
		"/p/ss26-00021-blk2",          // collision suffix
		"/p/ss6-00021-blk",            // year too short
		"/p/ss266-00021-blk",          // year too long
		"/p/ss26-0021-blk",            // model too short
		"/p/ss26-000021-blk",          // model too long
		"/p/ss26-00021-bl",            // color too short
		"/p/ss26-00021-blkk",          // color too long
		"/p/zz26-00021-blk",           // unknown season code
		"/p/-ss26-00021-blk",          // leading dash before SKU (empty pretty segment)
		"/p/foo/bar-ss26-00021-blk",   // pretty must be one path segment
		"/p/foo--bar-ss26-00021-blk",  // canonical kebab only
		"/p/foo bar-ss26-00021-blk",   // whitespace alias
		"/p/foo%20bar-ss26-00021-blk", // encoded alias
		"/p/UPPER-ss26-00021-blk",     // builder pretty is lowercase
		"/p/" + strings.Repeat("a", MaxPrettyLen+1) + "-ss26-00021-blk",
	}
	for _, p := range bad {
		if got, err := ParseProductTail(p); err == nil {
			t.Errorf("ParseProductTail(%q) = %q, nil; want error", p, got)
		} else if !errors.Is(err, ErrNotProductTail) {
			t.Errorf("ParseProductTail(%q) err = %v; want ErrNotProductTail", p, err)
		}
	}
}

func TestParseArchiveTail(t *testing.T) {
	ok := []struct{ path, want string }{
		{"/timeline/fall-drop-2026-ar000c", "AR000C"}, // pretty with hyphens, lowercase
		{"/timeline/AR000C", "AR000C"},                // empty pretty
		{"/timeline/a-b-c-AR1", "AR1"},                // many hyphens, short code
		{"/timeline/arctic-ARZZ9", "ARZZ9"},           // pretty starting AR-ish
	}
	for _, c := range ok {
		got, err := ParseArchiveTail(c.path)
		if err != nil || got != c.want {
			t.Errorf("ParseArchiveTail(%q) = %q, %v; want %q, nil", c.path, got, err, c.want)
		}
	}

	bad := []string{
		"/p/AR000C",            // wrong prefix
		"/timelines/AR000C",    // wrong prefix
		"timeline/AR000C",      // missing leading slash
		"/timeline/",           // empty token
		"/timeline/000C",       // no AR prefix
		"/timeline/AR000C?x=1", // query
		"/timeline/AR000C#f",   // fragment
		"/timeline/AR000C/",    // trailing garbage
		"/timeline/AR000C-",    // trailing dash
		"/timeline/AR",         // AR alone (no base36 chars)
		"/timeline/foo/bar-AR000C",
		"/timeline/foo--bar-AR000C",
		"/timeline/foo bar-AR000C",
		"/timeline/" + strings.Repeat("a", MaxPrettyLen+1) + "-AR000C",
	}
	for _, p := range bad {
		if got, err := ParseArchiveTail(p); err == nil {
			t.Errorf("ParseArchiveTail(%q) = %q, nil; want error", p, got)
		} else if !errors.Is(err, ErrNotArchiveTail) {
			t.Errorf("ParseArchiveTail(%q) err = %v; want ErrNotArchiveTail", p, err)
		}
	}
}

// TestTailRoundTrip is acceptance #8: every builder output parses back to the resolve token.
func TestTailRoundTrip(t *testing.T) {
	products := []struct{ name, sku string }{
		{"Black Summer Shirt", "SS26-00021-BLK"},
		{"", "FW25-00003-NAV"},
		{"!!!", "PF24-99999-RED"},
		{"Пальто", "RC23-00007-GRY"},
		{"multi---dash name", "SS26-00001-0A1"},
	}
	for _, p := range products {
		path := ProductPath(p.name, p.sku)
		got, err := ParseProductTail(path)
		if err != nil || got != p.sku {
			t.Errorf("round-trip ProductPath(%q,%q)=%q -> %q,%v; want %q", p.name, p.sku, path, got, err, p.sku)
		}
	}

	archives := []struct{ heading, code string }{
		{"Fall Drop 2026", "AR000C"},
		{"", "AR1"},
		{"a-b-c", "ARZZ9"},
	}
	for _, a := range archives {
		path := TimelinePath(a.heading, a.code)
		got, err := ParseArchiveTail(path)
		if err != nil || got != a.code {
			t.Errorf("round-trip TimelinePath(%q,%q)=%q -> %q,%v; want %q", a.heading, a.code, path, got, err, a.code)
		}
	}
}
