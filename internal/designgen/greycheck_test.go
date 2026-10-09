package designgen

import (
	"bytes"
	"context"
	"encoding/binary"
	"hash/crc32"
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

// legacyFlatCandidates — what a straps press bought 05.10–06.10 (wave 10 made every press one sheet);
// a run queued then still carries it frozen.
const legacyFlatCandidates = 4

// TestFlatSheetIsBoughtAsCandidates — the frozen requested_outputs of a garment sheet is the call's
// n; a per_view run and a detail callout keep n = 1; an old flat (outputs 1) stays one picture.
func TestFlatSheetIsBoughtAsCandidates(t *testing.T) {
	sheet := Job{Kind: "flat", Views: []string{"front", "back"}, Layout: layoutOne, Outputs: legacyFlatCandidates}
	calls, err := imageCalls(sheet)
	if err != nil || len(calls) != 1 || calls[0].n != legacyFlatCandidates {
		t.Fatalf("a flat sheet buys %d candidates in one call, got %+v %v", legacyFlatCandidates, calls, err)
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
}

// TestGreyCheckSkipsADecompressionBomb — a PNG whose header declares a canvas over the budget is not
// decoded (no label, no allocation): 74 bytes claiming 40000×40000.
func TestGreyCheckSkipsADecompressionBomb(t *testing.T) {
	var buf bytes.Buffer
	_ = png.Encode(&buf, image.NewGray(image.Rect(0, 0, 1, 1)))
	raw := buf.Bytes()
	// IHDR width/height live at bytes 16..23
	bomb := append([]byte(nil), raw...)
	for i, v := range []byte{0, 0, 0x9c, 0x40, 0, 0, 0x9c, 0x40} {
		bomb[16+i] = v
	}
	binary.BigEndian.PutUint32(bomb[29:33], crc32.ChecksumIEEE(bomb[12:29]))
	if cfg, err := png.DecodeConfig(bytes.NewReader(bomb)); err != nil || cfg.Width != 40000 {
		t.Fatalf("the probe must be a valid header: %v %v", cfg, err)
	}
	if f := flatPixelFlags(bomb); f != nil {
		t.Fatalf("a bomb must be skipped, got %v", f)
	}
}

// TestFlatCandidatesOnAnUnknownSlugAreSingleCalls — a custom default slug (no catalogue row, n range
// unknown) buys four candidates as four n = 1 calls; a catalogue GPT row as one n = 4 call.
func TestFlatCandidatesOnAnUnknownSlugAreSingleCalls(t *testing.T) {
	job := Job{Kind: "flat", Views: []string{"front", "back"}, Layout: layoutOne, Outputs: legacyFlatCandidates, Prompt: "p"}
	custom := &fakeImageTransport{model: "acme/custom"}
	out, err := imageProvider{t: custom, providerKey: "openrouter"}.Execute(context.Background(), job)
	if err != nil || len(custom.calls) != legacyFlatCandidates || len(out.Artifacts) != legacyFlatCandidates {
		t.Fatalf("custom slug: %d calls, %v", len(custom.calls), err)
	}
	for _, c := range custom.calls {
		if c.N != 1 {
			t.Fatalf("n=%d on an unknown slug", c.N)
		}
	}
	gpt := &fakeImageTransport{model: EngineGPTImage25Flare}
	if _, err := (imageProvider{t: gpt, providerKey: "openrouter"}).Execute(context.Background(), job); err != nil || len(gpt.calls) != 1 || gpt.calls[0].N != legacyFlatCandidates {
		t.Fatalf("flare: %+v %v", gpt.calls, err)
	}
}
