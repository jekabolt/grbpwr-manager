package slug

import "testing"

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
		{"", "SS26-00021-BLK", "/p/ss26-00021-blk"},           // empty name -> no leading dash
		{"!!!", "SS26-00021-BLK", "/p/ss26-00021-blk"},        // unusable name
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
