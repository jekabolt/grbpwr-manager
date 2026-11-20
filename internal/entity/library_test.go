package entity

import (
	"database/sql"
	"encoding/json"
	"strings"
	"testing"
)

// mediaKindFixtures are the URL shapes every writer of media produces. store/library/schema_test.go
// inserts the same rows and asserts the generated lib_kind column agrees for the same shapes, so Go and SQL stay twins.
var mediaKindFixtures = []struct {
	Name, FullSize, Thumbnail string
	Want                      LibraryKind
}{
	{"webp image", "https://cdn/x/2026/11/1-og.webp", "https://cdn/x/2026/11/1-thumb.webp", LibraryKindImage},
	{"gif verbatim", "https://cdn/x/1-og.gif", "https://cdn/x/1-thumb.gif", LibraryKindImage},
	{"mp4 video", "https://cdn/x/1.mp4", "https://cdn/x/1.mp4", LibraryKindVideo},
	{"webm upper", "https://cdn/x/1.WEBM", "https://cdn/x/1.WEBM", LibraryKindVideo},
	{"glb with png preview", "https://cdn/x/1.glb", "https://cdn/x/1-thumb.png", LibraryKindModel},
	{"glb without preview", "https://cdn/x/1.glb", "https://cdn/x/1.glb", LibraryKindModel},
	{"svg vector", "https://cdn/x/1.svg", "https://cdn/x/1.svg", LibraryKindVector},
	{"mp4 full only is not video", "https://cdn/x/1.mp4", "https://cdn/x/1-thumb.webp", LibraryKindImage},
	{"trailing dot", "https://cdn/x/1.", "https://cdn/x/1.", LibraryKindImage},
}

func TestMediaKindOf(t *testing.T) {
	for _, tc := range mediaKindFixtures {
		if got := MediaKindOf(tc.FullSize, tc.Thumbnail); got != tc.Want {
			t.Errorf("%s: MediaKindOf = %v, want %v", tc.Name, got, tc.Want)
		}
	}
}

func TestFileKindOf(t *testing.T) {
	cases := map[string]LibraryKind{
		"image/svg+xml":            LibraryKindVector,
		"image/vnd.dxf":            LibraryKindDocument,
		"image/jpeg":               LibraryKindImage,
		"image/gif":                LibraryKindImage,
		"IMAGE/PNG; charset=x":     LibraryKindImage,
		"video/mp4":                LibraryKindVideo,
		"video/quicktime":          LibraryKindVideo,
		"model/gltf-binary":        LibraryKindModel,
		"application/pdf":          LibraryKindDocument,
		"text/markdown":            LibraryKindDocument,
		"":                         LibraryKindDocument,
		"application/octet-stream": LibraryKindDocument,
	}
	for ct, want := range cases {
		if got := FileKindOf(ct); got != want {
			t.Errorf("FileKindOf(%q) = %v, want %v", ct, got, want)
		}
	}
}

func TestCopyableToMedia(t *testing.T) {
	yes := []string{"image/jpeg", "image/png", "image/webp", "image/heic", "image/svg+xml", "video/mp4", "video/webm", "Image/JPEG"}
	no := []string{"image/gif", "image/avif", "image/vnd.dxf", "video/quicktime", "application/pdf", "model/gltf-binary", ""}
	for _, ct := range yes {
		if !CopyableToMedia(ct) {
			t.Errorf("CopyableToMedia(%q) = false, want true", ct)
		}
	}
	for _, ct := range no {
		if CopyableToMedia(ct) {
			t.Errorf("CopyableToMedia(%q) = true, want false", ct)
		}
	}
}

func TestLibraryKindValueRoundTrip(t *testing.T) {
	for k := LibraryKindImage; k <= LibraryKindDocument; k++ {
		if got := LibraryKindFromValue(k.String()); got != k {
			t.Errorf("round trip %v -> %q -> %v", k, k.String(), got)
		}
	}
	if LibraryKindUnspecified.String() != "" || LibraryKindFromValue("nope") != LibraryKindUnspecified {
		t.Error("unspecified must map to empty and back")
	}
}

// The meta is invisible to JSON: holder documents and fingerprints that marshal a MediaFull stay
// byte-identical after the embed.
func TestMediaLibraryMetaIsNotInJSON(t *testing.T) {
	m := MediaFull{Id: 1}
	m.MediaLibraryMeta = MediaLibraryMeta{Kind: "video", FileName: sql.NullString{String: "a.mp4", Valid: true}}
	b, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	if s := string(b); strings.Contains(s, "lib_") || strings.Contains(s, "a.mp4") || strings.Contains(s, "MediaLibraryMeta") {
		t.Fatalf("library meta leaked into JSON: %s", s)
	}
}
