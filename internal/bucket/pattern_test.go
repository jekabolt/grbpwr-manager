package bucket

import "testing"

func TestIsPDF(t *testing.T) {
	cases := map[string]struct {
		in   []byte
		want bool
	}{
		"valid pdf":   {[]byte("%PDF-1.7\n%âãÏÓ"), true},
		"valid pdf17": {[]byte("%PDF-1.4 rest of file"), true},
		"not pdf":     {[]byte("not a pdf at all"), false},
		"png magic":   {[]byte("\x89PNG\r\n\x1a\n"), false},
		"too short":   {[]byte("%PDF"), false},
		"empty":       {[]byte(""), false},
	}
	for name, c := range cases {
		if got := isPDF(c.in); got != c.want {
			t.Errorf("%s: isPDF = %v, want %v", name, got, c.want)
		}
	}
}
