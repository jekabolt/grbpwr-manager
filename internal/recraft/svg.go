package recraft

import (
	"bytes"
	"encoding/xml"
	"fmt"
	"io"
	"strings"
)

// SVGContentType is what a checked result is stored as. Stated once here so the media row, the
// bucket object and the browser all agree.
const SVGContentType = "image/svg+xml"

// MaxSVGBytes caps the picture we accept. The box this runs on has 0.5 GiB of RAM and the bytes are
// held whole on their way to the bucket. It is also a signal in its own right: a "vector" that
// weighs megabytes is evidence that something traced a raster instead of drawing it.
const MaxSVGBytes = 8 << 20 // 8 MiB

// SVGStats is the SHAPE of what the model returned — the measurement that answers the owner's
// requirement in numbers instead of adjectives.
//
// «Ровный вектор, а не куча полигонов» is a property one can count. A redrawn garment is tens of
// paths and hundreds of nodes, most of them on curve segments. A traced raster is thousands of
// nodes, almost all of them on straight-line segments — which is what `vectorize` produces and what
// was forbidden. So a caller can show these numbers to a person, or refuse a result that is plainly
// a trace, without anybody having to open the file.
//
// This is measurement, not a verdict: the struct states counts and never decides.
type SVGStats struct {
	Bytes int
	// Width / Height / ViewBox are the root element's own words, verbatim and possibly empty. The
	// caller needs them to place the picture; guessing them from the geometry would be a lie.
	Width   string
	Height  string
	ViewBox string
	// Elements is every element in the document, root included.
	Elements int
	// Paths is the number of <path> elements; Shapes counts the primitive shapes
	// (rect/circle/ellipse/line/polygon/polyline) beside them.
	Paths  int
	Shapes int
	// Nodes is the total number of anchor points across all paths and polylines — the number a
	// person actually means when they say "a pile of polygons".
	Nodes int
	// LineSegments / CubicSegments / QuadSegments / ArcSegments split those nodes by the kind of
	// segment that arrives at them. Implicit repetitions are counted (a traced file leans on them
	// heavily, and counting only command letters would undercount exactly the case we care about).
	LineSegments  int
	CubicSegments int
	QuadSegments  int
	ArcSegments   int
	// PolylinePoints counts points inside <polyline>/<polygon>, which are already polylines and
	// carry no curvature at all.
	PolylinePoints int
	// TextNodes counts <text> elements. They matter because our stroke editor has no text: a label
	// that comes back as live text will not survive a round trip through it.
	TextNodes int
	// ExternalRefs counts references to another host (an <image href="https://…">). Not refused —
	// it is legal SVG — but it means the file is not self-contained, and a picture that silently
	// depends on somebody else's server will one day render empty.
	ExternalRefs int
}

// HasCurves reports whether the document contains any real curvature. A "vector" with none is
// either a genuinely angular drawing or a trace; either way a human should look at it.
func (s SVGStats) HasCurves() bool {
	return s.CubicSegments+s.QuadSegments+s.ArcSegments > 0
}

// CurveShare is the fraction of segments that are curved, 0..1. It is the single number that best
// separates "drawn" from "traced": a redraw is mostly curves, a trace is almost entirely straight
// lines. It returns 0 for a document with no segments at all.
func (s SVGStats) CurveShare() float64 {
	curved := s.CubicSegments + s.QuadSegments + s.ArcSegments
	total := curved + s.LineSegments + s.PolylinePoints
	if total == 0 {
		return 0
	}
	return float64(curved) / float64(total)
}

// InspectSVG validates the bytes and measures them.
//
// It answers three questions, in this order, because they fail differently:
//  1. Is it an SVG at all? A raster here means a RASTER MODEL was configured under a vector name
//     (ErrNotVector) — storing it would silently defeat the whole requirement.
//  2. Is it safe to serve? These bytes end up in our bucket and then in an admin's browser, so
//     active content is refused outright (ErrUnsafeSVG) rather than scrubbed: a partial scrub that
//     misses one vector is worse than a loud refusal of a file no legitimate generation produces.
//  3. What shape is it? — SVGStats, for a person to judge by.
//
// It NEVER rewrites the picture. In particular it does not flatten curves into polylines to suit
// our editor's stroke model; see the package doc.
func InspectSVG(raw []byte) (SVGStats, error) {
	stats := SVGStats{Bytes: len(raw)}
	if len(raw) == 0 {
		return stats, fmt.Errorf("%w: empty image", ErrInvalidResponse)
	}
	if len(raw) > MaxSVGBytes {
		return stats, fmt.Errorf("%w: image is %d bytes, over the %d cap", ErrInvalidResponse, len(raw), MaxSVGBytes)
	}
	if format := rasterFormat(raw); format != "" {
		return stats, fmt.Errorf("%w: the bytes are %s — check that RECRAFT_MODEL_* names a VECTOR model", ErrNotVector, format)
	}

	dec := xml.NewDecoder(bytes.NewReader(raw))
	// Named HTML entities appear in perfectly ordinary SVG text; refusing them would reject valid
	// files. Entity DECLARATIONS are a different matter and are rejected below.
	dec.Entity = xml.HTMLEntity
	dec.Strict = true

	sawRoot := false
	for {
		tok, err := dec.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			if !sawRoot {
				// Not XML and not a known raster: unusable, and we cannot say what it is.
				return stats, fmt.Errorf("%w: the bytes are not valid SVG: %v", ErrInvalidResponse, err)
			}
			return stats, fmt.Errorf("%w: malformed SVG: %v", ErrInvalidResponse, err)
		}

		switch t := tok.(type) {
		case xml.Directive:
			// A DOCTYPE carrying an ENTITY declaration is how an SVG becomes a decompression bomb or
			// an XXE probe. Legitimate generated art has no reason to declare entities.
			if bytes.Contains(bytes.ToUpper([]byte(t)), []byte("ENTITY")) {
				return stats, fmt.Errorf("%w: the document declares XML entities", ErrUnsafeSVG)
			}
		case xml.ProcInst:
			// <?xml …?> and friends. A processing instruction carries no behaviour in a browser.
		case xml.StartElement:
			name := strings.ToLower(t.Name.Local)
			if !sawRoot {
				if name != "svg" {
					return stats, fmt.Errorf("%w: the root element is <%s>, not <svg>", ErrInvalidResponse, t.Name.Local)
				}
				sawRoot = true
				for _, a := range t.Attr {
					switch strings.ToLower(a.Name.Local) {
					case "width":
						stats.Width = a.Value
					case "height":
						stats.Height = a.Value
					case "viewbox":
						stats.ViewBox = a.Value
					}
				}
			}
			stats.Elements++
			if err := checkUnsafeElement(name, t.Attr); err != nil {
				return stats, err
			}
			measureElement(name, t.Attr, &stats)
		}
	}
	if !sawRoot {
		return stats, fmt.Errorf("%w: no <svg> element in the response", ErrInvalidResponse)
	}
	return stats, nil
}

// rasterFormat names the picture format when the bytes are a known raster, else "". Magic numbers
// only — the point is to tell a human "a PNG came back", not to decode anything.
func rasterFormat(raw []byte) string {
	switch {
	case bytes.HasPrefix(raw, []byte("\x89PNG\r\n\x1a\n")):
		return "a PNG"
	case bytes.HasPrefix(raw, []byte{0xFF, 0xD8, 0xFF}):
		return "a JPEG"
	case bytes.HasPrefix(raw, []byte("GIF87a")), bytes.HasPrefix(raw, []byte("GIF89a")):
		return "a GIF"
	case len(raw) >= 12 && bytes.HasPrefix(raw, []byte("RIFF")) && bytes.Equal(raw[8:12], []byte("WEBP")):
		return "a WEBP"
	case bytes.HasPrefix(raw, []byte("%PDF-")):
		return "a PDF"
	}
	return ""
}

// unsafeElements carry or host executable content in a browser. `script` is the obvious one;
// `foreignObject` embeds arbitrary HTML (including a script) inside the drawing; the rest are
// document-embedding elements that have no business in generated art.
var unsafeElements = map[string]bool{
	"script":        true,
	"foreignobject": true,
	"iframe":        true,
	"embed":         true,
	"object":        true,
	"handler":       true, // SVG Tiny 1.2 event handler element
}

func checkUnsafeElement(name string, attrs []xml.Attr) error {
	if unsafeElements[name] {
		return fmt.Errorf("%w: the document contains <%s>", ErrUnsafeSVG, name)
	}
	for _, a := range attrs {
		an := strings.ToLower(a.Name.Local)
		// on* attributes are event handlers: onload, onclick, onmouseover…
		if strings.HasPrefix(an, "on") && len(an) > 2 {
			return fmt.Errorf("%w: the document carries an event handler attribute %q", ErrUnsafeSVG, a.Name.Local)
		}
		if an == "href" || an == "xlink:href" || an == "src" {
			v := strings.ToLower(strings.TrimSpace(a.Value))
			// Whitespace and control characters inside the scheme are the classic evasion
			// ("java\nscript:"), so they are removed before the comparison.
			v = strings.Map(func(r rune) rune {
				if r <= ' ' {
					return -1
				}
				return r
			}, v)
			if strings.HasPrefix(v, "javascript:") || strings.HasPrefix(v, "vbscript:") || strings.HasPrefix(v, "data:text/html") {
				return fmt.Errorf("%w: the document links to %q", ErrUnsafeSVG, a.Value)
			}
		}
	}
	return nil
}

// measureElement adds one element to the counts.
func measureElement(name string, attrs []xml.Attr, stats *SVGStats) {
	switch name {
	case "path":
		stats.Paths++
		for _, a := range attrs {
			if strings.ToLower(a.Name.Local) == "d" {
				countPathSegments(a.Value, stats)
			}
		}
	case "polyline", "polygon":
		stats.Shapes++
		for _, a := range attrs {
			if strings.ToLower(a.Name.Local) == "points" {
				// Two numbers per point.
				n := countNumbers(a.Value) / 2
				stats.PolylinePoints += n
				stats.Nodes += n
			}
		}
	case "rect", "circle", "ellipse", "line":
		stats.Shapes++
	case "text", "tspan":
		stats.TextNodes++
	}
	for _, a := range attrs {
		an := strings.ToLower(a.Name.Local)
		if an != "href" && an != "xlink:href" && an != "src" {
			continue
		}
		v := strings.ToLower(strings.TrimSpace(a.Value))
		if strings.HasPrefix(v, "http://") || strings.HasPrefix(v, "https://") || strings.HasPrefix(v, "//") {
			stats.ExternalRefs++
		}
	}
}

// pathArity is how many numbers one segment of each path command consumes.
var pathArity = map[byte]int{
	'M': 2, 'L': 2, 'T': 2,
	'H': 1, 'V': 1,
	'C': 6, 'S': 4, 'Q': 4,
	'A': 7,
	'Z': 0,
}

// countPathSegments counts the segments of one `d` attribute, honouring IMPLICIT REPETITION — the
// SVG rule that lets a command letter be followed by several coordinate groups ("C a b c d e f
// g h i j k l" is two cubic segments, and after an "M" the repeats are line segments, not moves).
//
// Counting only the command letters would be much simpler and would undercount exactly the file we
// most want to recognise: a traced raster leans on implicit repetition for its thousands of tiny
// line segments, and would report as a handful of commands.
func countPathSegments(d string, stats *SVGStats) {
	var cmd byte
	group := 0 // numbers consumed towards the current segment
	groupIndex := 0

	flush := func() {
		switch upper(cmd) {
		case 'M':
			// The first group after M is the move; every implicit repeat after it is a lineto.
			if groupIndex == 0 {
				stats.Nodes++
			} else {
				stats.LineSegments++
				stats.Nodes++
			}
		case 'L', 'H', 'V':
			stats.LineSegments++
			stats.Nodes++
		case 'C', 'S':
			stats.CubicSegments++
			stats.Nodes++
		case 'Q', 'T':
			stats.QuadSegments++
			stats.Nodes++
		case 'A':
			stats.ArcSegments++
			stats.Nodes++
		}
		groupIndex++
	}

	forEachToken(d, func(letter byte, isNumber bool) {
		if !isNumber {
			cmd, group, groupIndex = letter, 0, 0
			return
		}
		arity, known := pathArity[upper(cmd)]
		if !known || arity == 0 {
			return
		}
		group++
		if group == arity {
			flush()
			group = 0
		}
	})
}

func upper(b byte) byte {
	if b >= 'a' && b <= 'z' {
		return b - 32
	}
	return b
}

// forEachToken walks a path/points string and reports each token: a command letter, or a number.
// Numbers are not parsed — only counted — because nothing here needs their value.
//
// It follows the SVG grammar's awkward corners: numbers may be separated by nothing at all
// ("1-2" is two numbers), a second '.' starts a new number ("1.5.3"), and 'e'/'E' with an optional
// sign is an exponent rather than a new token.
func forEachToken(s string, fn func(tok byte, isNumber bool)) {
	i := 0
	for i < len(s) {
		c := s[i]
		switch {
		case c == ' ' || c == '\t' || c == '\n' || c == '\r' || c == ',':
			i++
		case (c >= 'A' && c <= 'Z') || (c >= 'a' && c <= 'z'):
			// A letter is a command unless it is the exponent marker of a number, which the number
			// scanner below consumes itself — so anything reaching here is a command.
			fn(c, false)
			i++
		case (c >= '0' && c <= '9') || c == '.' || c == '-' || c == '+':
			j := i
			if s[j] == '-' || s[j] == '+' {
				j++
			}
			seenDot := false
			for j < len(s) {
				d := s[j]
				if d >= '0' && d <= '9' {
					j++
					continue
				}
				if d == '.' && !seenDot {
					seenDot = true
					j++
					continue
				}
				if (d == 'e' || d == 'E') && j+1 < len(s) {
					k := j + 1
					if s[k] == '-' || s[k] == '+' {
						k++
					}
					if k < len(s) && s[k] >= '0' && s[k] <= '9' {
						j = k
						continue
					}
				}
				break
			}
			if j == i {
				// A lone sign or dot with no digits: skip it rather than loop forever.
				i++
				continue
			}
			fn('0', true)
			i = j
		default:
			i++
		}
	}
}

// countNumbers counts the numbers in a `points`-style list.
func countNumbers(s string) int {
	n := 0
	forEachToken(s, func(_ byte, isNumber bool) {
		if isNumber {
			n++
		}
	})
	return n
}
