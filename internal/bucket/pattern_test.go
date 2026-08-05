package bucket

import (
	"strings"
	"testing"
)

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

func TestIsDXF(t *testing.T) {
	cases := map[string]struct {
		in   []byte
		want bool
	}{
		"ascii lf":              {[]byte("0\nSECTION\n2\nHEADER\n"), true},
		"ascii crlf":            {[]byte("0\r\nSECTION\r\n2\r\nHEADER\r\n"), true},
		"ascii indented codes":  {[]byte("  0\nSECTION\n  2\nHEADER\n"), true},
		"ascii bom":             {append([]byte{0xEF, 0xBB, 0xBF}, []byte("0\nSECTION\n")...), true},
		"leading 999 comment":   {[]byte("999\nexported by CAD, options 1\n999\nsecond note\n0\nSECTION\n"), true},
		"comment then nothing":  {[]byte("999\njust a comment\n"), false},
		"binary sentinel":       {append([]byte("AutoCAD Binary DXF\r\n\x1a\x00"), 0x01, 0x02), true},
		"zero then not section": {[]byte("0\nLINE\n"), false},
		"pdf":                   {[]byte("%PDF-1.7\n"), false},
		"garbage":               {[]byte("not a dxf at all"), false},
		"empty":                 {[]byte(""), false},
		"blank lines first":     {[]byte("\n\n0\nSECTION\n"), true},
		// Real exporters (AccuMark/Optitex/Lectra) front the file with multi-KB 999
		// provenance headers — the opening pair sits deep but must still be found.
		"999 prelude over 4KB":                 {append([]byte("999\n"+strings.Repeat("x", 8*1024)+"\n"), []byte("0\nSECTION\n")...), true},
		"999 pairs then nothing within window": {[]byte(strings.Repeat("999\nnote\n", 10*1024)), false},
	}
	for name, c := range cases {
		if got := isDXF(c.in); got != c.want {
			t.Errorf("%s: isDXF = %v, want %v", name, got, c.want)
		}
	}
}
