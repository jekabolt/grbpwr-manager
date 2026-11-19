package patternjoin

import (
	"bytes"
	"compress/flate"
	"encoding/base64"
	"fmt"
	"io"
	"strings"
	"testing"
)

// Repack only in tests: emulate an uploader who can recompute the manifest, its
// compressed chunks, the declared size and the full-file SHA after changing a marker.
func rewritten(t *testing.T, dxf []byte, mutate func([]byte) []byte, compressed func([]byte) []byte) []byte {
	t.Helper()
	rest := dxf
	var chunks []string
	nl := "\n"
	if bytes.Contains(dxf, []byte("\r\n")) {
		nl = "\r\n"
	}
	for bytes.HasPrefix(rest, []byte("999"+nl)) {
		_, tail, _ := bytes.Cut(rest, []byte(nl))
		line, next, _ := bytes.Cut(tail, []byte(nl))
		v := strings.Fields(string(line))
		chunks = append(chunks, v[len(v)-1])
		rest = next
	}
	c, e := base64.StdEncoding.DecodeString(strings.Join(chunks, ""))
	if e != nil {
		t.Fatal(e)
	}
	r := flate.NewReader(bytes.NewReader(c))
	raw, e := io.ReadAll(r)
	if e != nil {
		t.Fatal(e)
	}
	r.Close()
	if mutate != nil {
		next := mutate(raw)
		if bytes.Equal(next, raw) {
			t.Fatal("no manifest mutation")
		}
		raw = next
	}
	var buf bytes.Buffer
	w, _ := flate.NewWriter(&buf, flate.BestSpeed)
	w.Write(raw)
	w.Close()
	c = buf.Bytes()
	if compressed != nil {
		c = compressed(c)
	}
	seq := base64.StdEncoding.EncodeToString(c)
	count := (len(seq) + 191) / 192
	var prologue bytes.Buffer
	for i := 0; i < count; i++ {
		fmt.Fprintf(&prologue, "999%sGRBPWR-MANIFEST v1z %d/%d %s%s", nl, i+1, count, seq[192*i:min(len(seq), 192*(i+1))], nl)
	}
	return append(prologue.Bytes(), rest...)
}
func TestForgedOwnFormatManifest(t *testing.T) {
	for _, dialect := range []string{"r12", "r2000"} {
		for _, attack := range []string{"case-duplicate", "marker-label", "marker-coordinate", "marker-index", "removed-marker", "folded", "unrelated-material", "other-layer", "false-gate", "changed-rank", "compressed-trailer", "unbounded-inflate", "cut-hole", "end-vertex", "units", "duplicate-chunk", "out-of-order"} {
			t.Run(dialect+"/"+attack, func(t *testing.T) {
				o, c, a, b, s := fixture(t, dialect)
				k := "tech-card-patterns/child.dxf"
				before := o[k]
				var out []byte
				change := func(from, to string) {
					out = rewritten(t, before, func(v []byte) []byte { return bytes.Replace(v, []byte(from), []byte(to), 1) }, nil)
				}
				switch attack {
				case "case-duplicate":
					change(`"techCardId":156`, `"techCardId":156,"TECHCARDID":156`)
				case "marker-label":
					change("draft-cad:dart", "draft-cad:other")
				case "marker-coordinate":
					change("45000000,65000000", "45000001,65000000")
				case "marker-index":
					change(`"index":1`, `"index":0`)
				case "removed-marker":
					change(`"authoredInternal"`, `"ignoredPaths"`)
				case "folded":
					change(`"unfoldedFold":false`, `"unfoldedFold":true`)
				case "unrelated-material":
					change(`["main","01JABCDEFGHJKMNPQRSTVWXYZ8"]`, `["main","other"]`)
				case "other-layer":
					change(`"cut":"1"`, `"cut":"14"`)
				case "false-gate":
					change(`"passed":true`, `"passed":false`)
				case "changed-rank":
					change(`"rank":0`, `"rank":1`)
				case "compressed-trailer":
					out = rewritten(t, before, nil, func(p []byte) []byte { return append(p, 0) })
				case "unbounded-inflate":
					out = rewritten(t, before, func(p []byte) []byte { return append(p, bytes.Repeat([]byte(" "), maxManifest)...) }, nil)
				case "cut-hole":
					out = bytes.ReplaceAll(before, []byte("120.000000"), []byte("121.000000"))
				case "end-vertex":
					s.MovingInterval.End = s.FixedInterval.End
					out = before
				case "units":
					out = bytes.Replace(before, []byte("$INSUNITS"), []byte("$UNITLESS"), 1)
				case "duplicate-chunk":
					out = bytes.Replace(before, []byte(" v1z 2/"), []byte(" v1z 1/"), 1)
				case "out-of-order":
					out = bytes.Replace(before, []byte(" v1z 1/"), []byte(" v1z 2/"), 1)
				}
				if attack != "end-vertex" && bytes.Equal(out, before) {
					t.Fatal("no mutation")
				}
				o[k] = out
				b.Size = int64(len(out))
				b.SHA256 = digest(out)
				if _, err := ReadBinding(t.Context(), o, c, a, b, s); err == nil {
					t.Fatal("accepted forged input")
				}
			})
		}
	}
}

func TestR2000OwnerAndVisibilityArePartOfLineage(t *testing.T) {
	for _, which := range []string{"block", "entity", "insert", "layer"} {
		t.Run(which, func(t *testing.T) {
			o, c, a, b, s := fixture(t, "r2000")
			k := "tech-card-patterns/child.dxf"
			p := o[k]
			nl := "\r\n"
			var from, to string
			switch which {
			case "block":
				from = nl + "BLOCK" + nl + "  5" + nl + "29" + nl + "330" + nl + "14" + nl
				to = strings.Replace(from, nl+"14"+nl, nl+"B"+nl, 1)
			case "entity":
				from = nl + "LWPOLYLINE" + nl + "  5" + nl + "2B" + nl + "330" + nl + "14" + nl
				to = strings.Replace(from, nl+"14"+nl, nl+"B"+nl, 1)
			case "insert":
				from = nl + "INSERT" + nl + "  5" + nl + "3C" + nl + "330" + nl + "A" + nl
				to = strings.Replace(from, nl+"A"+nl, nl+"B"+nl, 1)
			case "layer":
				from = nl + " 62" + nl + "8" + nl + "  6" + nl + "CONTINUOUS" + nl
				to = strings.Replace(from, nl+"8"+nl, nl+"-8"+nl, 1)
			}
			out := bytes.Replace(p, []byte(from), []byte(to), 1)
			if bytes.Equal(p, out) {
				t.Fatal("fixture role did not match")
			}
			o[k] = out
			b.SHA256 = digest(out)
			b.Size = int64(len(out))
			if _, err := ReadBinding(t.Context(), o, c, a, b, s); err == nil {
				t.Fatal("accepted the wrong owner")
			}
		})
	}
}
