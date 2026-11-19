package patternjoin

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"github.com/jekabolt/grbpwr-manager/internal/bucket"
	"io"
	"os"
	"strings"
	"testing"
)

type objectStore map[string][]byte

func (o objectStore) GetManagedObject(_ context.Context, key string) (io.ReadCloser, int64, error) {
	p, ok := o[key]
	if !ok {
		return nil, 0, errors.New("not stored")
	}
	return io.NopCloser(bytes.NewReader(p)), int64(len(p)), nil
}
func fixture(t *testing.T, dialect string) (objectStore, *bucket.Config, Asset, Asset, Selection) {
	t.Helper()
	o := objectStore{}
	var as []Asset
	for _, part := range []string{"source", "child"} {
		b, e := os.ReadFile("testdata/native-join-" + dialect + "-" + part + ".dxf")
		if e != nil {
			t.Fatal(e)
		}
		key := "tech-card-patterns/" + part + ".dxf"
		o[key] = b
		as = append(as, Asset{URL: "https://patterns.example/" + key, Size: int64(len(b)), SHA256: digest(b)})
	}
	raw, err := os.ReadFile("testdata/native-join-" + dialect + "-intent.json")
	if err != nil {
		t.Fatal(err)
	}
	var intent struct {
		Fixed, Moving struct{ SourceCurveID, StartVertexID, EndVertexID string }
	}
	if err = json.Unmarshal(raw, &intent); err != nil {
		t.Fatal(err)
	}
	return o, &bucket.Config{SubdomainEndpoint: "https://patterns.example"}, as[0], as[1], Selection{FixedInterval: DirectedEdge{Curve: intent.Fixed.SourceCurveID, Start: intent.Fixed.StartVertexID, End: intent.Fixed.EndVertexID}, MovingInterval: DirectedEdge{Curve: intent.Moving.SourceCurveID, Start: intent.Moving.StartVertexID, End: intent.Moving.EndVertexID}, CardID: 156, FabricPurpose: "main", BOMLineKey: "01JABCDEFGHJKMNPQRSTVWXYZ8", SizeToken: "M", SizeID: 4, Fixed: "FP", Moving: "BP", Result: "JN", FixedEdge: 1, MovingEdge: 3, TranslateX: -50000000}
}
func TestManagedReviewedNativeJoin(t *testing.T) {
	for _, d := range []string{"r12", "r2000"} {
		t.Run(d, func(t *testing.T) {
			o, c, a, b, s := fixture(t, d)
			v, e := ReadBinding(t.Context(), o, c, a, b, s)
			if e != nil {
				t.Fatal(e)
			}
			if v.OriginalBodySHA256 == v.ReviewedBodySHA256 || v.Original != a || v.Reviewed != b || v.Selection != s {
				t.Fatal(v)
			}
		})
	}
}
func TestRefusedBindings(t *testing.T) {
	for _, d := range []string{"r12", "r2000"} {
		for _, which := range []string{"host", "path", "sha", "size", "rank", "alias", "wrong-edge", "direction", "translation", "source-as-child", "native-mark", "copied-marker", "strip-mark", "new-scope", "extra-native-instance", "trailing-manifest"} {
			t.Run(d+"/"+which, func(t *testing.T) {
				o, c, a, b, s := fixture(t, d)
				key := "tech-card-patterns/child.dxf"
				body := o[key]
				nl := "\n"
				if bytes.Contains(body, []byte("\r\n")) {
					nl = "\r\n"
				}
				switch which {
				case "host":
					a.URL = strings.Replace(a.URL, "patterns.example", "other.test", 1)
				case "path":
					b.URL = strings.Replace(b.URL, "tech-card-patterns", "media", 1)
				case "sha":
					b.SHA256 = strings.Repeat("0", 64)
				case "size":
					a.Size--
				case "rank":
					s.SizeID++
				case "alias":
					s.Moving = "QP"
				case "new-scope":
					s.FabricPurpose = "lining"
				case "wrong-edge":
					s.FixedEdge = 2
				case "direction":
					s.Fixed, s.Moving = s.Moving, s.Fixed
				case "translation":
					s.TranslateX++
				case "source-as-child":
					b = a
				case "native-mark", "strip-mark", "extra-native-instance", "copied-marker", "trailing-manifest":
					switch which {
					case "copied-marker":
						i := bytes.Index(body, []byte("  0"+nl+"SECTION"))
						orig := o["tech-card-patterns/source.dxf"]
						j := bytes.Index(orig, []byte("  0"+nl+"SECTION"))
						if i < 0 || j < 0 {
							t.Fatal("fixture prologue")
						}
						body = append(bytes.Clone(orig[:j]), body[i:]...)
					case "native-mark":
						body = bytes.ReplaceAll(body, []byte("144.000000"), []byte("145.000000"))
					case "strip-mark":
						body = bytes.ReplaceAll(body, []byte("GRBPWR_POINT_ONLY"), []byte("0"))
					case "extra-native-instance":
						body = bytes.ReplaceAll(body, []byte(nl+"INSERT"+nl), []byte(nl+"LINE"+nl))
					case "trailing-manifest":
						body = append(bytes.Clone(body), []byte("999\nGRBPWR-MANIFEST v1z 1/1 AAA=\n")...)
					}
					if bytes.Equal(body, o[key]) {
						t.Fatal("mutation missed fixture")
					}
					o[key] = body
					b.SHA256 = digest(body)
					b.Size = int64(len(body))
				}
				if _, e := ReadBinding(t.Context(), o, c, a, b, s); e == nil {
					t.Fatal("accepted")
				}
			})
		}
	}
}

// Original files came from the ordinary worker with a saved GetTechCard projection.
// The child files came from the manual Chromium Wizard Apply and the production UploadPattern.
func TestOperatorMaterialBinding(t *testing.T) {
	for _, d := range []string{"r12-operator", "r2000-operator"} {
		t.Run(d, func(t *testing.T) {
			o, cfg, a, b, s := fixture(t, d)
			if _, err := ReadBinding(t.Context(), o, cfg, a, b, s); err != nil {
				t.Fatal(err)
			}
			key := "tech-card-patterns/child.dxf"
			original := o[key]
			for _, pair := range [][2]string{{"Material: Shell", "Material: Lining"}, {"MATERIAL: Shell", "MATERIAL: Lining"}, {"Category: Shell", "Category: Lining"}} {
				bad := bytes.Replace(original, []byte(pair[0]), []byte(pair[1]), 1)
				if bytes.Equal(original, bad) {
					continue
				}
				o[key] = bad
				changed := b
				changed.Size = int64(len(bad))
				changed.SHA256 = digest(bad)
				if _, err := ReadBinding(t.Context(), o, cfg, a, changed, s); err == nil {
					t.Fatalf("accepted altered child %s", pair[0])
				}
			}
			o[key] = original
			for _, wrong := range []string{"MAIN", "TECH_CARD_BOM_PURPOSE_MAIN", "lining", " main"} {
				selected := s
				selected.FabricPurpose = wrong
				if _, err := ReadBinding(t.Context(), o, cfg, a, b, selected); err == nil {
					t.Fatalf("accepted incorrect stored purpose %q", wrong)
				}
			}
		})
	}
}
