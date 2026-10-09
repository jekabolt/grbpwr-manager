package bucket

import (
	"bytes"
	"strconv"
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

// manifestPrologue builds n bytes (at least) of leading 999 pairs shaped like the
// importer's conversion manifest: "999" / "GRBPWR-MANIFEST v1 i/n <base64>".
func manifestPrologue(minBytes int, eol string) []byte {
	chunk := strings.Repeat("QUJDREVGR0hJSktMTU5PUFFSU1RVVldYWVo", 7) // ~245 chars of base64
	var b strings.Builder
	for i := 0; b.Len() < minBytes; i++ {
		b.WriteString("999" + eol)
		b.WriteString("GRBPWR-MANIFEST v1 " + strconv.Itoa(i) + "/n " + chunk + eol)
	}
	return []byte(b.String())
}

func TestIsDXFLargeCommentPrologue(t *testing.T) {
	cloHead := "  0\r\nSECTION\r\n  2\r\nHEADER\r\n  9\r\n$ACADVER\r\n  1\r\nAC1009\r\n  0\r\nENDSEC\r\n"
	join := func(parts ...[]byte) []byte { return bytes.Join(parts, nil) }
	bom := []byte{0xEF, 0xBB, 0xBF}
	cases := map[string]struct {
		in   []byte
		want bool
	}{
		"200KB manifest then SECTION":         {join(manifestPrologue(200<<10, "\n"), []byte("0\nSECTION\n2\nHEADER\n")), true},
		"200KB manifest then not SECTION":     {join(manifestPrologue(200<<10, "\n"), []byte("0\nLINE\n")), false},
		"200KB manifest then other code":      {join(manifestPrologue(200<<10, "\n"), []byte("2\nHEADER\n")), false},
		"200KB manifest crlf":                 {join(manifestPrologue(200<<10, "\r\n"), []byte("0\r\nSECTION\r\n")), true},
		"200KB manifest bom":                  {join(bom, manifestPrologue(200<<10, "\n"), []byte("0\nSECTION\n")), true},
		"5MB of comments then SECTION":        {join(manifestPrologue(5<<20, "\n"), []byte("0\nSECTION\n")), false},
		"one 5MB comment value":               {join([]byte("999\n"), bytes.Repeat([]byte("x"), 5<<20), []byte("\n0\nSECTION\n")), false},
		"3.9MB of comments then SECTION":      {join(manifestPrologue(3900<<10, "\n"), []byte("0\nSECTION\n")), true},
		"manifest ends inside comment":        {manifestPrologue(200<<10, "\n")[:200<<10-3], false},
		"binary sentinel":                     {append([]byte("AutoCAD Binary DXF\r\n\x1a\x00"), make([]byte, 128<<10)...), true},
		"clo head":                            {[]byte(cloHead), true},
		"clo head bom":                        {join(bom, []byte(cloHead)), true},
		"pdf":                                 {join([]byte("%PDF-1.7\n%\xe2\xe3\xcf\xd3\n"), bytes.Repeat([]byte("x"), 100<<10)), false},
		"no comments: 0 past 64KB blank run":  {join(bytes.Repeat([]byte("\n"), 64<<10), []byte("0\nSECTION\n")), false},
		"no comments: SECTION cut by window":  {join(bytes.Repeat([]byte("\n"), 64<<10-6), []byte("0\nSECTION\n")), false},
		"no comments: SECTION ends at window": {join(bytes.Repeat([]byte("\n"), 64<<10-9), []byte("0\nSECTION\n")), true},
	}
	for name, c := range cases {
		if got := isDXF(c.in); got != c.want {
			t.Errorf("%s: isDXF = %v, want %v", name, got, c.want)
		}
	}
}
