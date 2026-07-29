package localeutil

import "testing"

func TestCanonical(t *testing.T) {
	cases := map[string]string{
		"en": "en", "EN": "en", "en-US": "en", "en_GB": "en", " fr ": "fr",
		"zh": "zh", "cn": "zh", "CN": "zh", "zh-Hant": "zh",
		"ko": "ko", "kr": "ko", "KR": "ko",
		"de": "de", "it": "it", "ja": "ja",
		"": "", "xx": "", "klingon": "", "e": "",
	}
	for in, want := range cases {
		if got := Canonical(in); got != want {
			t.Errorf("Canonical(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestCanonicalOrDefault(t *testing.T) {
	if got := CanonicalOrDefault("xx"); got != "en" {
		t.Errorf("CanonicalOrDefault(xx) = %q, want en", got)
	}
	if got := CanonicalOrDefault("cn"); got != "zh" {
		t.Errorf("CanonicalOrDefault(cn) = %q, want zh", got)
	}
	if got := CanonicalOrDefault(""); got != "en" {
		t.Errorf("CanonicalOrDefault(\"\") = %q, want en", got)
	}
}

func TestIsSupported(t *testing.T) {
	if IsSupported("xx") {
		t.Error("xx must not be supported")
	}
	if !IsSupported("ja") || !IsSupported("kr") {
		t.Error("ja and kr(->ko) must be supported")
	}
}
