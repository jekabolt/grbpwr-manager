package bucket

import "testing"

func TestObjectKeyFromURL(t *testing.T) {
	ok := map[string]string{
		"https://cdn.example.com/base/f/2026/july/x-og.webp":                  "base/f/2026/july/x-og.webp",
		"https://bucket.fra1.digitaloceanspaces.com/base/v/2026/july/clip.mp4": "base/v/2026/july/clip.mp4",
	}
	for in, want := range ok {
		got, err := objectKeyFromURL(in)
		if err != nil {
			t.Errorf("objectKeyFromURL(%q) unexpected error: %v", in, err)
			continue
		}
		if got != want {
			t.Errorf("objectKeyFromURL(%q) = %q, want %q", in, got, want)
		}
	}

	// A URL with no path (no object key) must error, not return "".
	if _, err := objectKeyFromURL("https://cdn.example.com/"); err == nil {
		t.Error("expected error for URL with no object key")
	}
}
