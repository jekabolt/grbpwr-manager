package entity

import "testing"

func TestParseLibraryRatio(t *testing.T) {
	for _, tc := range []struct {
		in   string
		w, h int
		ok   bool
	}{
		{"4:5", 4, 5, true},
		{"8:10", 4, 5, true},
		{" 16:9 ", 16, 9, true},
		{"1080:1350", 4, 5, true},
		{"0:5", 0, 0, false},
		{"4:", 0, 0, false},
		{"4x5", 0, 0, false},
		{"-4:5", 0, 0, false},
		{"123456:1", 0, 0, false},
		{"other", 0, 0, false},
		{"", 0, 0, false},
	} {
		w, h, ok := ParseLibraryRatio(tc.in)
		if w != tc.w || h != tc.h || ok != tc.ok {
			t.Errorf("ParseLibraryRatio(%q) = %d, %d, %v; want %d, %d, %v", tc.in, w, h, ok, tc.w, tc.h, tc.ok)
		}
	}
}

func TestLibraryRatioKey(t *testing.T) {
	for _, tc := range []struct {
		w, h int
		want string
	}{
		{1080, 1350, "4:5"},
		{1920, 1080, "16:9"},
		{1000, 1000, "1:1"},
		{1080, 1920, "9:16"},
		{2000, 1000, "2:1"},
		{1447, 1080, LibraryRatioOther},
		{0, 1080, ""},
		{1080, 0, ""},
	} {
		if got := LibraryRatioKey(tc.w, tc.h); got != tc.want {
			t.Errorf("LibraryRatioKey(%d, %d) = %q, want %q", tc.w, tc.h, got, tc.want)
		}
	}
	for _, n := range NamedRatios {
		w, h, ok := ParseLibraryRatio(n)
		if !ok || LibraryRatioKey(w, h) != n {
			t.Errorf("named ratio %q is not its own key", n)
		}
	}
}
