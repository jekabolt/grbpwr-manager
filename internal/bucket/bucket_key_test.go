package bucket

import "testing"

func TestObjectKeyFromURL(t *testing.T) {
	ok := map[string]string{
		"https://cdn.example.com/base/f/2026/july/x-og.webp":                   "base/f/2026/july/x-og.webp",
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

func TestManagedObjectKeyFromURLRequiresConfiguredHost(t *testing.T) {
	b := &Bucket{Config: &Config{
		S3Endpoint:        "fra1.digitaloceanspaces.com",
		S3BucketName:      "grbpwr-patterns",
		SubdomainEndpoint: "cdn.grbpwr.example",
	}}
	const want = "base/tech-card-patterns/2026/august/sheet.pdf"
	for _, raw := range []string{
		"https://cdn.grbpwr.example/" + want,
		"https://grbpwr-patterns.fra1.digitaloceanspaces.com/" + want,
	} {
		got, err := b.managedObjectKeyFromURL(raw)
		if err != nil {
			t.Fatalf("managedObjectKeyFromURL(%q): %v", raw, err)
		}
		if got != want {
			t.Fatalf("managedObjectKeyFromURL(%q) = %q, want %q", raw, got, want)
		}
	}
	for _, raw := range []string{
		"https://attacker.example/" + want,
		"https://cdn.grbpwr.example.attacker.invalid/" + want,
		"http://cdn.grbpwr.example/" + want,
	} {
		if _, err := b.managedObjectKeyFromURL(raw); err == nil {
			t.Fatalf("managedObjectKeyFromURL(%q) unexpectedly accepted an unmanaged URL", raw)
		}
	}
}
