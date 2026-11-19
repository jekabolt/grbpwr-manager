package patternjoin

import (
	"bytes"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"
)

type coord struct{ X, Y int64 }
type group struct {
	code  int
	value string
}
type record struct {
	kind   string
	fields []group
}

func (r record) value(code int) string {
	for _, v := range r.fields {
		if v.code == code {
			return v.value
		}
	}
	return ""
}

// A role includes the native primitive, layer, group values and vertex order. Only
// generated database handles/subclasses are removed before comparing marks across files.
type primitive struct {
	Kind, Layer string
	Closed      bool
	Points      []coord
	Style       []string
}
type block struct {
	primitiveRef, insertRef string
	Outer, Seam, Grain      []primitive
	Marks                   []primitive
	labels                  map[string]string
}

var fixedNumber = regexp.MustCompile(`^-?(0|[1-9][0-9]{0,2})\.[0-9]{6}$`)

func number(v string) (int64, bool) {
	if !fixedNumber.MatchString(v) {
		return 0, false
	}
	n, e := strconv.ParseInt(strings.Replace(v, ".", "", 1), 10, 64)
	return n, e == nil
}

func records(raw []byte) ([]record, error) {
	if len(raw) > maxAsset {
		return nil, invalid
	}
	var out []record
	for len(raw) > 0 {
		a, rest, ok := bytes.Cut(raw, []byte("\n"))
		if !ok {
			return nil, invalid
		}
		b, next, ok := bytes.Cut(rest, []byte("\n"))
		if !ok {
			b = rest
			next = nil
		}
		raw = next
		k, e := strconv.Atoi(strings.TrimSpace(string(a)))
		if e != nil || k < 0 || k > 1071 || len(b) > 1024 || len(out) > 25000 {
			return nil, invalid
		}
		v := string(bytes.TrimSuffix(b, []byte("\r")))
		if k == 0 {
			out = append(out, record{kind: v})
			continue
		}
		if len(out) == 0 || len(out[len(out)-1].fields) > 4096 {
			return nil, invalid
		}
		out[len(out)-1].fields = append(out[len(out)-1].fields, group{k, v})
	}
	return out, nil
}

func (r record) allowed(keys ...int) bool {
	seen := map[int]bool{}
	for _, g := range r.fields {
		if !slices.Contains(keys, g.code) {
			return false
		}
		if seen[g.code] && g.code != 100 && !(r.kind == "LWPOLYLINE" && (g.code == 10 || g.code == 20)) {
			return false
		}
		seen[g.code] = true
	}
	return true
}

func (r record) points() ([]coord, error) {
	var out []coord
	for i, g := range r.fields {
		switch g.code {
		case 10, 11:
			if i+1 >= len(r.fields) || r.fields[i+1].code != g.code+10 {
				return nil, invalid
			}
			x, ok := number(g.value)
			y, okY := number(r.fields[i+1].value)
			if !ok || !okY {
				return nil, invalid
			}
			out = append(out, coord{x, y})
		case 20, 21:
			if i == 0 || r.fields[i-1].code != g.code-10 {
				return nil, invalid
			}
		case 30, 31, 43:
			if g.value != "0.000000" {
				return nil, invalid
			}
		}
	}
	return out, nil
}

func primitiveAt(rs []record, i *int) (primitive, error) {
	r := rs[*i]
	p := primitive{Kind: r.kind, Layer: r.value(8)}
	var e error
	switch r.kind {
	case "POLYLINE", "LWPOLYLINE":
		if flag := r.value(70); flag != "0" && flag != "1" {
			return p, invalid
		}
		p.Closed = r.value(70) == "1"
		if r.kind == "LWPOLYLINE" {
			if !r.allowed(5, 330, 100, 8, 90, 70, 43, 10, 20) {
				return p, invalid
			}
			p.Points, e = r.points()
			if r.value(90) != strconv.Itoa(len(p.Points)) {
				return p, invalid
			}
		} else {
			if !r.allowed(8, 66, 70) || r.value(66) != "1" {
				return p, invalid
			}
			for *i+1 < len(rs) && rs[*i+1].kind == "VERTEX" {
				*i++
				v := rs[*i]
				if v.value(8) != p.Layer || !v.allowed(8, 10, 20, 30) {
					return p, invalid
				}
				pts, err := v.points()
				if err != nil || len(pts) != 1 {
					return p, invalid
				}
				p.Points = append(p.Points, pts[0])
			}
			*i++
			if *i >= len(rs) || rs[*i].kind != "SEQEND" || !rs[*i].allowed(8) || rs[*i].value(8) != p.Layer {
				return p, invalid
			}
		}
		if len(p.Points) < 2 {
			return p, invalid
		}
	case "LINE", "POINT", "TEXT":
		switch r.kind {
		case "POINT":
			if !r.allowed(5, 330, 100, 8, 10, 20, 30) {
				return p, invalid
			}
		case "LINE":
			if !r.allowed(8, 10, 20, 30, 11, 21, 31) {
				return p, invalid
			}
		case "TEXT":
			if !r.allowed(5, 330, 100, 8, 10, 20, 30, 1, 40, 50, 7) {
				return p, invalid
			}
			p.Style = []string{r.value(1), r.value(40), r.value(50), r.value(7)}
		}
		p.Points, e = r.points()
		if len(p.Points) != 1 && r.kind != "LINE" || r.kind == "LINE" && len(p.Points) != 2 {
			return p, invalid
		}
	default:
		return p, invalid
	}
	return p, e
}

func inventory(raw []byte, ids []string, s Selection) (map[string]*block, string, error) {
	rs, err := records(raw)
	if err != nil {
		return nil, "", err
	}
	bs := map[string]*block{}
	inserts := map[string]int{}
	sec, active, version := "", "", ""
	seen := map[string]bool{}
	var sections []string
	finished := false
	ordinal := 0
	handles := map[string]bool{}
	owners := map[string]string{}
	layers := map[string]bool{}
	ref := func(r record) (string, error) {
		n := ordinal
		ordinal++
		if version == "AC1009" {
			if r.value(5) != "" {
				return "", invalid
			}
			return fmt.Sprintf("p/%d", n), nil
		}
		h := r.value(5)
		if h == "" {
			return "", invalid
		}
		return "h/" + hex.EncodeToString([]byte(h)), nil
	}
	for i := 0; i < len(rs); i++ {
		r := rs[i]
		if version == "AC1015" && r.kind != "SECTION" {
			if h := r.value(5); h != "" {
				if handles[h] || strings.ToUpper(h) != h {
					return nil, "", invalid
				}
				if _, e := strconv.ParseUint(h, 16, 64); e != nil {
					return nil, "", invalid
				}
				handles[h] = true
			}
		}
		switch r.kind {
		case "SECTION":
			if sec != "" || active != "" || finished {
				return nil, "", invalid
			}
			sec = r.value(2)
			sections = append(sections, sec)
			if seen[sec] {
				return nil, "", invalid
			}
			seen[sec] = true
			if sec == "HEADER" {
				if slices.Equal(r.fields, []group{{2, "HEADER"}, {9, "$ACADVER"}, {1, "AC1009"}, {9, "$INSUNITS"}, {70, "4"}}) {
					version = "AC1009"
				} else if slices.Equal(r.fields, []group{{2, "HEADER"}, {9, "$ACADVER"}, {1, "AC1015"}, {9, "$DWGCODEPAGE"}, {3, "UTF-8"}, {9, "$INSUNITS"}, {70, "4"}, {9, "$HANDSEED"}, {5, "FFFF"}}) {
					version = "AC1015"
				} else {
					return nil, "", invalid
				}
			}
			continue
		case "ENDSEC":
			if active != "" || sec == "" {
				return nil, "", invalid
			}
			sec = ""
			continue
		case "EOF":
			if i != len(rs)-1 || sec != "" {
				return nil, "", invalid
			}
			finished = true
			continue
		}
		if sec == "" {
			return nil, "", invalid
		}
		if sec == "TABLES" {
			if r.kind == "BLOCK_RECORD" {
				name, h := r.value(2), r.value(5)
				if owners[name] != "" || h == "" {
					return nil, "", invalid
				}
				owners[name] = h
			}
			if r.kind == "LAYER" {
				name := r.value(2)
				color, e := strconv.Atoi(r.value(62))
				linetype := "CONTINUOUS"
				if version == "AC1009" && name == "14" {
					linetype = "DASHED"
				}
				if name == "" || layers[name] || e != nil || color <= 0 || r.value(70) != "0" || r.value(6) != linetype {
					return nil, "", invalid
				}
				layers[name] = true
			}
		}
		if sec == "ENTITIES" {
			if r.kind == "TEXT" && version == "AC1009" && r.value(8) == "1" {
				key, txt, ok := strings.Cut(r.value(1), ": ")
				if !ok || !slices.Contains([]string{"Creation Date", "Creation Time", "Style Name", "Author", "Product", "Version", "Sample Size", "Units"}, key) || txt == "" || !r.allowed(8, 10, 20, 30, 40, 1, 50, 7) || r.value(40) != "0.250000" || r.value(50) != "0.000000" || r.value(7) != "STANDARD" {
					return nil, "", invalid
				}
				p, e := r.points()
				if e != nil || len(p) != 1 || p[0] != (coord{}) {
					return nil, "", invalid
				}
				continue
			}
			if r.kind != "INSERT" || !r.allowed(8, 2, 5, 330, 100, 10, 20, 30) || r.value(8) != "0" {
				return nil, "", invalid
			}
			pts, e := r.points()
			name := r.value(2)
			if e != nil || len(pts) != 1 || pts[0] != (coord{}) || bs[name] == nil || (version == "AC1015" && r.value(330) != owners["*Model_Space"]) {
				return nil, "", invalid
			}
			bs[name].insertRef, e = ref(r)
			if e != nil {
				return nil, "", e
			}
			inserts[name]++
			continue
		}
		if sec != "BLOCKS" {
			continue
		}
		if r.kind == "BLOCK" {
			activeName := r.value(2)
			if active != "" || activeName == "" || bs[activeName] != nil || !r.allowed(2, 3, 70, 8, 10, 20, 30, 1, 67, 5, 330, 100) || r.value(70) != "0" || (r.value(3) != activeName && !(r.value(3) == "" && (activeName == "*Model_Space" || activeName == "*Paper_Space"))) {
				return nil, "", invalid
			}
			pts, e := r.points()
			if e != nil || len(pts) != 1 || pts[0] != (coord{}) {
				return nil, "", invalid
			}
			if version == "AC1015" && (owners[activeName] == "" || owners[activeName] != r.value(330)) {
				return nil, "", invalid
			}
			active = activeName
			bs[active] = &block{labels: map[string]string{}}
			continue
		}
		if active == "" {
			return nil, "", invalid
		}
		if version == "AC1015" && r.value(330) != owners[active] {
			return nil, "", invalid
		}
		if r.kind == "ENDBLK" {
			if !r.allowed(5, 330, 100, 8, 67) || r.value(8) != "0" {
				return nil, "", invalid
			}
			active = ""
			continue
		}
		if active == "*Model_Space" || active == "*Paper_Space" {
			return nil, "", invalid
		}
		b := bs[active]
		nativeRef, e := ref(r)
		if e != nil {
			return nil, "", e
		}
		if !layers[r.value(8)] {
			return nil, "", invalid
		}
		p, e := primitiveAt(rs, &i)
		if e != nil {
			return nil, "", e
		}
		poly := p.Kind == "POLYLINE" || p.Kind == "LWPOLYLINE"
		switch {
		case p.Layer == "1" && p.Kind == "TEXT":
			key, v, ok := strings.Cut(p.Style[0], ": ")
			key = strings.ToUpper(key)
			if !ok || b.labels[key] != "" || !slices.Contains([]string{"PIECE NAME", "SIZE", "QUANTITY", "MATERIAL", "CATEGORY"}, key) {
				return nil, "", invalid
			}
			b.labels[key] = v
		case p.Layer == "1" && p.Closed && poly:
			if len(b.Outer) == 0 {
				b.primitiveRef = nativeRef
			}
			b.Outer = append(b.Outer, p)
		case p.Layer == "14" && p.Closed && poly:
			b.Seam = append(b.Seam, p)
		case p.Layer == "7" && !p.Closed && (poly || p.Kind == "LINE"):
			b.Grain = append(b.Grain, p)
		case p.Layer == "8" && poly || p.Layer == "13" && p.Kind == "POINT" || p.Layer == "GRBPWR_CAD_TEXT" && p.Kind == "TEXT" || p.Layer == "GRBPWR_POINT_ONLY" && p.Kind == "POINT":
			b.Marks = append(b.Marks, p)
		default:
			return nil, "", invalid
		}
	}
	expected := []string{"HEADER", "TABLES", "BLOCKS", "ENTITIES"}
	if version == "AC1015" {
		expected = []string{"HEADER", "CLASSES", "TABLES", "BLOCKS", "ENTITIES", "OBJECTS"}
	}
	if !slices.Equal(sections, expected) || !finished || !seen["BLOCKS"] || !seen["ENTITIES"] || version != "AC1009" && version != "AC1015" {
		return nil, "", invalid
	}
	if version == "AC1015" && len(owners) != len(bs) {
		return nil, "", invalid
	}
	for _, empty := range []string{"*Model_Space", "*Paper_Space"} {
		if inserts[empty] != 0 {
			return nil, "", invalid
		}
		delete(bs, empty)
	}
	if len(bs) != len(ids) {
		return nil, "", invalid
	}
	for _, id := range ids {
		name := id + "_" + s.SizeToken
		b := bs[name]
		if inserts[name] != 1 || b == nil || len(b.Outer) == 0 || len(b.Seam) != 1 || len(b.Grain) != 1 || b.labels["PIECE NAME"] != id || b.labels["SIZE"] != s.SizeToken || b.labels["QUANTITY"] != "1" || b.labels["MATERIAL"] == "" || (version == "AC1009") != (b.labels["CATEGORY"] != "") {
			return nil, "", invalid
		}
	}
	return bs, version, nil
}

func move(p primitive, delta coord) primitive {
	p.Points = slices.Clone(p.Points)
	for i, v := range p.Points {
		p.Points[i] = coord{v.X + delta.X, v.Y + delta.Y}
	}
	return p
}
func signature(p primitive) string { b, _ := json.Marshal(p); return string(b) }
func rectangle(b *block) ([]coord, bool) {
	p := b.Outer[0].Points
	if len(p) != 4 || !slices.Equal(p, b.Seam[0].Points) || !(p[0].X < p[2].X && p[0].Y < p[2].Y && p[1] == (coord{p[2].X, p[0].Y}) && p[3] == (coord{p[0].X, p[2].Y})) {
		return nil, false
	}
	return p, true
}
func checkNativeJoin(before, after *nativeFile, s Selection, assetSHA string) error {
	if before.dialect != after.dialect || before.sizeName != after.sizeName || before.sourceLabel != after.sourceLabel || before.fabricPurpose != after.fabricPurpose {
		return invalid
	}
	old, newBlocks := before.blocks, after.blocks
	a, b, c := old[s.Fixed+"_"+s.SizeToken], old[s.Moving+"_"+s.SizeToken], newBlocks[s.Result+"_"+s.SizeToken]
	// The writer takes display labels from the current cloth. The manifest and
	// locked BOM identify the cloth, while all three native labels must survive.
	for _, k := range []string{"MATERIAL", "CATEGORY"} {
		if a.labels[k] != b.labels[k] || b.labels[k] != c.labels[k] {
			return invalid
		}
	}
	if before.fabricPurpose == s.FabricPurpose && a.labels["MATERIAL"] != s.FabricPurpose {
		return invalid
	}
	hash, err := hex.DecodeString(assetSHA)
	if err != nil {
		return invalid
	}
	fileID := "s/" + strconv.FormatInt(int64(s.CardID), 36) + "/" + base64.RawURLEncoding.EncodeToString(hash)
	exact := func(b *block, id string, edge int) DirectedEdge {
		prefix := "dxf-curve-v1/f/" + hex.EncodeToString([]byte(fileID)) + "/i/n/b/" + hex.EncodeToString([]byte(id+"_"+s.SizeToken)) + "/" + b.insertRef + "/o/0/r/0/c/0/e/" + b.primitiveRef + "/s/0/p/"
		curve := prefix + strconv.Itoa(edge)
		v := func(k int) string { return "dxf-vertex-v1/" + prefix + strconv.Itoa(k) + "/sample/" + strconv.Itoa(k) }
		return DirectedEdge{Curve: curve, Start: v(edge), End: v((edge + 1) % 4)}
	}
	if s.FixedInterval != exact(a, s.Fixed, s.FixedEdge) || s.MovingInterval != exact(b, s.Moving, s.MovingEdge) {
		return invalid
	}
	pa, ok := rectangle(a)
	pb, okB := rectangle(b)
	pc, okC := rectangle(c)
	if !ok || !okB || !okC {
		return invalid
	}
	delta := coord{s.TranslateX, s.TranslateY}
	q := move(b.Outer[0], delta).Points
	if s.TranslateX < -2000000000 || s.TranslateX > 2000000000 || s.TranslateY < -2000000000 || s.TranslateY > 2000000000 || s.FixedEdge != 1 || s.MovingEdge != 3 || pa[1] != q[0] || pa[2] != q[3] || pc[0] != pa[0] || pc[2] != q[2] || len(pb) != 4 {
		return invalid
	}
	if signature(a.Grain[0]) != signature(move(b.Grain[0], delta)) || signature(a.Grain[0]) != signature(c.Grain[0]) {
		return invalid
	}
	// Signed hole winding, precise path order, native layer, point/text/drill styling and
	// coincident marks with multiplicity are carried through the exact translation.
	counts := map[string]int{}
	for _, x := range []struct {
		b     *block
		d     coord
		count int
	}{{a, coord{}, 1}, {b, delta, 1}, {c, coord{}, -1}} {
		for _, p := range x.b.Outer[1:] {
			counts[signature(move(p, x.d))] += x.count
		}
		for _, p := range x.b.Marks {
			counts[signature(move(p, x.d))] += x.count
		}
	}
	for _, n := range counts {
		if n != 0 {
			return invalid
		}
	}
	return nil
}
