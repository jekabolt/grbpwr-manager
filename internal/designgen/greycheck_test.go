package designgen

import (
	"bytes"
	"image"
	"image/color"
	"image/png"
	"testing"
)

// drawFlat — a white canvas with a black rectangle outline (the garment) whose inside is filled with
// `fill`, plus a thin anti-aliased-looking grey rim along the inner side of the outline.
func drawFlat(fill color.RGBA) []byte {
	img := image.NewRGBA(image.Rect(0, 0, 400, 300))
	for y := 0; y < 300; y++ {
		for x := 0; x < 400; x++ {
			img.Set(x, y, color.RGBA{255, 255, 255, 255})
		}
	}
	for y := 50; y <= 250; y++ {
		for x := 100; x <= 300; x++ {
			switch {
			case x <= 102 || x >= 298 || y <= 52 || y >= 248:
				img.Set(x, y, color.RGBA{0, 0, 0, 255}) // the outline
			case x == 103 || x == 297 || y == 53 || y == 247:
				img.Set(x, y, color.RGBA{150, 150, 150, 255}) // anti-aliased rim
			default:
				img.Set(x, y, fill)
			}
		}
	}
	var buf bytes.Buffer
	_ = png.Encode(&buf, img)
	return buf.Bytes()
}

// TestGreyCheckLabelsAFillNotTheLines — a grey fill inside the outline is labelled; a white inside
// with the same anti-aliased rim is not; a coloured fill is not grey; garbage bytes are silent.
//
// MUTATIONS IT CATCHES: the rim counted as fill (every flat would read grey), the outside counted as
// silhouette, saturation ignored.
func TestGreyCheckLabelsAFillNotTheLines(t *testing.T) {
	if f := flatPixelFlags(drawFlat(color.RGBA{190, 190, 190, 255})); len(f) != 1 || f[0] != FlagGrey {
		t.Fatalf("a grey fill must be labelled, got %v", f)
	}
	if f := flatPixelFlags(drawFlat(color.RGBA{255, 255, 255, 255})); f != nil {
		t.Fatalf("a white inside must not be labelled, got %v", f)
	}
	if f := flatPixelFlags(drawFlat(color.RGBA{200, 120, 60, 255})); f != nil {
		t.Fatalf("a coloured fill is not grey, got %v", f)
	}
	if f := flatPixelFlags([]byte("not a picture")); f != nil {
		t.Fatalf("undecodable bytes must be silent, got %v", f)
	}
}

func TestSplitCallsOverN(t *testing.T) {
	got := splitCallsOverN([]imageCall{{prompt: "p", n: 4}}, 1)
	if len(got) != 4 || got[0].n != 1 {
		t.Fatalf("four candidates on an n=1 engine are four calls, got %+v", got)
	}
	if got := splitCallsOverN([]imageCall{{prompt: "p", n: 4}}, 10); len(got) != 1 || got[0].n != 4 {
		t.Fatalf("an engine that returns ten keeps one call, got %+v", got)
	}
}

// TestFlatSheetIsBoughtAsCandidates — the frozen requested_outputs of a garment sheet is the call's
// n; a per_view run and a detail callout keep n = 1; an old flat (outputs 1) stays one picture.
func TestFlatSheetIsBoughtAsCandidates(t *testing.T) {
	sheet := Job{Kind: "flat", Views: []string{"front", "back"}, Layout: layoutOne, Outputs: FlatCandidates}
	calls, err := imageCalls(sheet)
	if err != nil || len(calls) != 1 || calls[0].n != FlatCandidates {
		t.Fatalf("a flat sheet buys %d candidates in one call, got %+v %v", FlatCandidates, calls, err)
	}
	old := sheet
	old.Outputs = 1
	if calls, _ := imageCalls(old); calls[0].n != 1 {
		t.Fatal("a flat queued before candidates stays one picture")
	}
	detail := Job{Kind: "flat", Views: []string{"detail"}, Layout: layoutOne, Outputs: 4}
	if calls, _ := imageCalls(detail); calls[0].n != 1 {
		t.Fatal("a detail callout is one close-up")
	}
	if FlatCandidatesFor([]string{"front"}, layoutPerView) != 0 || FlatCandidatesFor([]string{"front", "back"}, layoutOne) != FlatCandidates {
		t.Fatal("FlatCandidatesFor: only a garment sheet")
	}
}
