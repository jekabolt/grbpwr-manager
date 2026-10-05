package designgen

import (
	"bytes"
	"context"
	"encoding/base64"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"strings"
	"testing"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/stretchr/testify/require"
)

// ─────────────────────────── T27: ARTWORK SIZE, PLACE, GUIDE ───────────────────────────

func t27PNG(t *testing.T, img image.Image) []byte {
	t.Helper()
	var buf bytes.Buffer
	require.NoError(t, png.Encode(&buf, img))
	return buf.Bytes()
}

// t27Flat — an opaque white flat w×h.
func t27Flat(w, h int) *image.NRGBA {
	m := image.NewNRGBA(image.Rect(0, 0, w, h))
	for i := range m.Pix {
		m.Pix[i] = 0xff
	}
	return m
}

// t27Logo — a 100×100 artwork, transparent but for an opaque block x 40..59, y 30..69 (the logo in
// the middle with margins all round — the shape of the asset behind run 76).
func t27Logo(c color.NRGBA) *image.NRGBA {
	m := image.NewNRGBA(image.Rect(0, 0, 100, 100))
	for y := 30; y < 70; y++ {
		for x := 40; x < 60; x++ {
			m.SetNRGBA(x, y, c)
		}
	}
	return m
}

func t27Decode(t *testing.T, uri string) image.Image {
	t.Helper()
	i := strings.Index(uri, ",")
	require.True(t, strings.HasPrefix(uri, "data:image/"), uri[:min(len(uri), 40)])
	raw, err := base64.StdEncoding.DecodeString(uri[i+1:])
	require.NoError(t, err)
	img, err := jpeg.Decode(bytes.NewReader(raw))
	require.NoError(t, err)
	return img
}

func TestArtworkTightenFindsTheContentBox(t *testing.T) {
	// Real transparency: alpha decides.
	cut, ok := artworkTighten(t27Logo(color.NRGBA{R: 200, A: 255}))
	require.True(t, ok)
	require.InDeltaSlice(t, []float64{0.4, 0.3, 0.6, 0.7}, cut.frac[:], 1e-9)
	require.Equal(t, image.Rect(0, 0, 20, 40), cut.img.Bounds())

	// Opaque on white: non-white decides, and the white ground becomes transparent in the cut.
	m := t27Flat(100, 100)
	for y := 10; y < 20; y++ {
		for x := 50; x < 90; x++ {
			m.SetNRGBA(x, y, color.NRGBA{R: 30, G: 30, B: 30, A: 255})
		}
	}
	m.SetNRGBA(60, 50, color.NRGBA{R: 255, G: 200, B: 255, A: 255}) // pale but coloured: chroma > 18
	cut, ok = artworkTighten(m)
	require.True(t, ok)
	require.InDeltaSlice(t, []float64{0.5, 0.1, 0.9, 0.51}, cut.frac[:], 1e-9)
	require.Equal(t, uint8(0), cut.img.NRGBAAt(0, 39).A, "the white ground is not the artwork")
	require.Equal(t, uint8(255), cut.img.NRGBAAt(0, 0).A)

	// Nothing reads as content → not tightened.
	_, ok = artworkTighten(t27Flat(50, 50))
	require.False(t, ok)
}

func TestArtworkSubQuadInterpolatesTheCorners(t *testing.T) {
	rect := []artworkCorner{{0.2, 0.2}, {0.6, 0.2}, {0.6, 0.6}, {0.2, 0.6}}
	got := artworkSubQuad(rect, [4]float64{0.25, 0.25, 0.75, 0.75})
	want := []artworkCorner{{0.3, 0.3}, {0.5, 0.3}, {0.5, 0.5}, {0.3, 0.5}}
	for i := range want {
		require.InDelta(t, want[i].X, got[i].X, 1e-12)
		require.InDelta(t, want[i].Y, got[i].Y, 1e-12)
	}
	// A free quad: the whole box is the quad itself, its centre the bilinear centre.
	q := []artworkCorner{{0.1, 0.1}, {0.5, 0.2}, {0.6, 0.7}, {0.0, 0.5}}
	require.Equal(t, q, artworkSubQuad(q, [4]float64{0, 0, 1, 1}))
	c := artworkSubQuad(q, [4]float64{0.5, 0.5, 0.5, 0.5})[0]
	require.InDelta(t, (0.1+0.5+0.6+0.0)/4, c.X, 1e-12)
	require.InDelta(t, (0.1+0.2+0.7+0.5)/4, c.Y, 1e-12)
}

func TestArtworkHomographyMapsTheSquareOntoTheQuad(t *testing.T) {
	p := [4][2]float64{{10, 12}, {90, 30}, {80, 95}, {5, 70}}
	m := artworkHomography(p)
	for k, uv := range [4][2]float64{{0, 0}, {1, 0}, {1, 1}, {0, 1}} {
		w := m[6]*uv[0] + m[7]*uv[1] + m[8]
		x := (m[0]*uv[0] + m[1]*uv[1] + m[2]) / w
		y := (m[3]*uv[0] + m[4]*uv[1] + m[5]) / w
		require.InDelta(t, p[k][0], x, 1e-9)
		require.InDelta(t, p[k][1], y, 1e-9)
	}
	inv, ok := artworkInvert3(m)
	require.True(t, ok)
	w := inv[6]*80 + inv[7]*95 + inv[8]
	require.InDelta(t, 1, (inv[0]*80+inv[1]*95+inv[2])/w, 1e-9)
	require.InDelta(t, 1, (inv[3]*80+inv[4]*95+inv[5])/w, 1e-9)
}

// TestWhiteThreadReadsOnTheGuide — a half-covered white pixel lands on the cloth-coloured underlay,
// not on the white flat.
func TestWhiteThreadReadsOnTheGuide(t *testing.T) {
	dst := t27Flat(100, 100)
	cut := image.NewNRGBA(image.Rect(0, 0, 10, 10))
	for i := 0; i < len(cut.Pix); i += 4 {
		cut.Pix[i], cut.Pix[i+1], cut.Pix[i+2], cut.Pix[i+3] = 255, 255, 255, 128
	}
	artworkDrawInQuad(dst, cut, []artworkCorner{{0.4, 0.4}, {0.5, 0.4}, {0.5, 0.5}, {0.4, 0.5}}, color.NRGBA{R: 0x53, G: 0x56, B: 0x5a, A: 255})
	in := dst.NRGBAAt(45, 45)
	require.Less(t, int(in.R), 220, "white thread must read against the flat")
	require.Equal(t, color.NRGBA{R: 255, G: 255, B: 255, A: 255}, dst.NRGBAAt(20, 20))
}

// ─── end to end through buildJob ───

const t27Params = `{"views":["front","back"],"layout":"one",` +
	`"colour":{"code":"GREY","hex":"#53565a","fabric_media_id":9}}`

// The back logo placed on the BACK flat (media 2) in the rectangle (0.3,0.2)–(0.7,0.5): its content
// (0.4..0.6 × 0.3..0.7 of the picture) lands at 46 %–54 % × 29 %–41 %.
func t27Inputs(note string) string {
	return `{"slots":[{"view_key":"front","media_id":1},{"view_key":"back","media_id":2}],` +
		`"artworks":[{"asset_id":7,"name":"back logo","media_id":30,"view":"back","flat_media_id":2,` +
		`"corners":[{"x":0.3,"y":0.2},{"x":0.7,"y":0.2},{"x":0.7,"y":0.5},{"x":0.3,"y":0.5}],` +
		`"note":"` + note + `"}]}`
}

func t27Objects(t *testing.T) *fakeObjects {
	return &fakeObjects{byKey: map[string][]byte{
		"m/1.png":  t27PNG(t, t27Flat(200, 300)),
		"m/2.png":  t27PNG(t, t27Flat(200, 300)),
		"m/3.png":  t27PNG(t, t27Flat(200, 300)),
		"m/30.png": t27PNG(t, t27Logo(color.NRGBA{R: 220, G: 10, B: 10, A: 255})),
		"m/31.png": t27PNG(t, t27Logo(color.NRGBA{R: 10, G: 10, B: 220, A: 255})),
	}}
}

func t27Job(t *testing.T, params, inputs string, engines []Engine) Job {
	t.Helper()
	r := testRun(1, entity.DesignRunKindRender)
	r.Params = entity.RawJSON(params)
	r.Inputs = entity.RawJSON(inputs)
	job, err := buildJobWith(context.Background(), media(1, 2, 3, 9, 30, 31), t27Objects(t), r, "medium", engines)
	require.NoError(t, err)
	return job
}

const t27Embroidery = "Make it real machine EMBROIDERY: dense satin-stitch thread in the artwork's own colours, " +
	"raised above the cloth by about 1–2 mm, visible stitch direction and the soft sheen of thread, stitched " +
	"edges that slightly gather the cloth around them. It is NOT a print, NOT a flat patch, NOT an appliqué " +
	"and has no backing or border of its own: only the artwork itself is stitched, its exact shape, colours " +
	"and letterforms kept."

// GOLDEN: embroidery with its guide — the tightened picture, the guide right after it, the content
// quad's corners and the B wording.
func TestT27EmbroideryGoesOutTightWithItsGuide(t *testing.T) {
	job := t27Job(t, t27Params, t27Inputs("embroidery"), EngineTable(""))
	require.Len(t, job.References, 5)
	require.True(t, strings.HasPrefix(job.References[2], "data:image/jpeg;base64,"), "the artwork goes out cropped")
	require.True(t, strings.HasPrefix(job.References[3], "data:image/jpeg;base64,"), "the guide")
	require.Equal(t, "https://cdn.example/m/9.png", job.References[4])
	require.Len(t, job.ReferenceViews, 5)

	require.Contains(t, job.Prompt, "- image 3: artwork «back logo» — embroidery: the artwork itself, cropped to "+
		"its edges, on the garment's own cloth colour, to be applied on the garment where the ARTWORK paragraph says\n")
	require.Contains(t, job.Prompt, "- image 4: placement guide — the back flat with the artwork drawn at its "+
		"exact size and position on the garment\n")
	want := "ARTWORK. Image 3 is the artwork «back logo» (embroidery). Image 4 shows EXACTLY where it goes and " +
		"how big it is: on the back of the garment, where image 4 draws it, at that size relative to the back " +
		"panel (its corners lie at top-left 46 %, 29 %; top-right 54 %, 29 %; bottom-right 54 %, 41 %; " +
		"bottom-left 46 %, 41 % of the back flat's frame). Reproduce that position and that size exactly — do " +
		"not enlarge it, do not move it. " + t27Embroidery + " The plain ground around it in image 3 is only " +
		"the garment's cloth colour and is not part of it. It follows the folds and takes the light of the " +
		"photograph. Nowhere else on the garment."
	require.Contains(t, job.Prompt, "\n\n"+want+"\n\n")

	// The artwork picture: the logo alone (20×40 → its aspect), flattened onto the cloth colour.
	art := t27Decode(t, job.References[2])
	require.Equal(t, 20, art.Bounds().Dx())
	require.Equal(t, 40, art.Bounds().Dy())

	// The guide: the logo's pixels inside the content quad on the back flat, the flat untouched
	// outside it — including inside the PLACED quad where the margins used to stretch the logo.
	g := t27Decode(t, job.References[3])
	require.Equal(t, image.Rect(0, 0, 200, 300), g.Bounds())
	r, gg, b, _ := g.At(100, 105).RGBA() // centre of 46–54 % × 29–41 %
	require.Greater(t, r>>8, uint32(150))
	require.Less(t, gg>>8, uint32(80))
	require.Less(t, b>>8, uint32(80))
	for _, p := range []image.Point{{64, 64}, {136, 145}, {100, 70}, {100, 140}} { // in the placed quad, off the content
		r, gg, b, _ := g.At(p.X, p.Y).RGBA()
		require.Greaterf(t, r>>8, uint32(240), "%v", p)
		require.Greaterf(t, gg>>8, uint32(240), "%v", p)
		require.Greaterf(t, b>>8, uint32(240), "%v", p)
	}
}

// GOLDEN: a print gets the ink wording.
func TestT27PrintGetsTheInkWording(t *testing.T) {
	job := t27Job(t, t27Params, t27Inputs("screen print"), EngineTable(""))
	require.Contains(t, job.Prompt, "ARTWORK. Image 3 is the artwork «back logo» (screen print). Image 4 shows EXACTLY")
	require.Contains(t, job.Prompt, "do not move it. Make it a real PRINT: screen-printed ink sitting in the weave — "+
		"flat, no relief, the cloth texture shows through the ink; its exact shape, colours and letterforms are "+
		"kept. It is NOT embroidery and NOT a patch. The plain ground around it in image 3 is only the garment's "+
		"cloth colour and is not part of it.")
	require.NotContains(t, job.Prompt, "EMBROIDERY:")
}

// GOLDEN: no room under the ceiling → no guide; the corners sentence (now the content quad) is the
// only placement instruction.
func TestT27WithoutRoomTheCornersAreTheOnlyPlacement(t *testing.T) {
	engines := []Engine{{Slug: "tight/engine", IsDefault: true, MaxRefs: 4}}
	job := t27Job(t, t27Params, t27Inputs("embroidery"), engines)
	require.Len(t, job.References, 4)
	require.NotContains(t, job.Prompt, "placement guide")
	want := "ARTWORK. Image 3 is the artwork «back logo» (embroidery). Apply it on the back of the garment, " +
		"wherever that side is visible, inside the four-cornered area marked on the back flat (image 2) whose " +
		"corners lie at — top-left 46 %, 29 %; top-right 54 %, 29 %; bottom-right 54 %, 41 %; bottom-left 46 %, " +
		"41 % — of that flat's frame (x from its left edge, y from its top edge), upright. " + t27Embroidery +
		" The plain ground around it in image 3 is only the garment's cloth colour and is not part of it. It " +
		"fills that area at the area's own proportions and perspective, follows the folds and takes the light " +
		"of the photograph. Nowhere else on the garment."
	require.Contains(t, job.Prompt, "\n\n"+want+"\n\n")
}

// An unreadable artwork picture keeps today's url, today's quad and no guide.
func TestT27AnUnreadableArtworkDegradesToToday(t *testing.T) {
	r := testRun(1, entity.DesignRunKindRender)
	r.Params = entity.RawJSON(t27Params)
	r.Inputs = entity.RawJSON(t27Inputs("embroidery"))
	objs := t27Objects(t)
	delete(objs.byKey, "m/30.png")
	job, err := buildJob(context.Background(), media(1, 2, 9, 30), objs, r, "medium")
	require.NoError(t, err)
	require.Equal(t, "https://cdn.example/m/30.png", job.References[2])
	require.Len(t, job.References, 4)
	require.Contains(t, job.Prompt, "top-left 30 %, 20 %; top-right 70 %, 20 %")
	require.Contains(t, job.Prompt, "Its white ground is not part of it.")
}

// CEILING DROP ORDER: guides go side_r, side_l, back, front.
func TestT27GuidesDropSideRFirstThenBack(t *testing.T) {
	params := `{"views":["front","back","side_r"],"layout":"one","colour":{"hex":"#53565a","fabric_media_id":9}}`
	quad := `"corners":[{"x":0.3,"y":0.2},{"x":0.7,"y":0.2},{"x":0.7,"y":0.5},{"x":0.3,"y":0.5}],"note":"embroidery"`
	inputs := `{"slots":[{"view_key":"front","media_id":1},{"view_key":"back","media_id":2},{"view_key":"side_r","media_id":3}],` +
		`"artworks":[` +
		`{"name":"chest","media_id":30,"view":"front","flat_media_id":1,` + quad + `},` +
		`{"name":"back","media_id":30,"view":"back","flat_media_id":2,` + quad + `},` +
		`{"name":"sleeve","media_id":31,"view":"side_r","flat_media_id":3,` + quad + `}]}`
	// 3 flats + 2 artwork pictures + swatch = 6 required.
	for _, tc := range []struct {
		max  int
		want []string
	}{
		{0, []string{"front", "back", "side_r"}}, // 0 = the default table row (16)
		{8, []string{"front", "back"}},
		{7, []string{"front"}},
		{6, nil},
	} {
		engines := EngineTable("")
		if tc.max > 0 {
			engines = []Engine{{Slug: "e", IsDefault: true, MaxRefs: tc.max}}
		}
		job := t27Job(t, params, inputs, engines)
		var got []string
		for _, line := range strings.Split(job.Prompt, "\n") {
			if i := strings.Index(line, ": placement guide — the "); i >= 0 {
				got = append(got, strings.Fields(line[i+len(": placement guide — the "):])[0])
			}
		}
		want := []string{}
		for _, v := range tc.want {
			want = append(want, strings.Fields(viewWord(v))[0])
		}
		if len(want) == 0 {
			want = nil
		}
		require.Equalf(t, want, got, "max %d", tc.max)
		require.Equal(t, 6+len(tc.want), len(job.References))
	}
}

// The guide of a side follows that side's last artwork picture; one media placed on two sides is
// one picture and both guides follow it, front first.
func TestT27GuideSitsRightAfterItsArtwork(t *testing.T) {
	quad := `"corners":[{"x":0.3,"y":0.2},{"x":0.7,"y":0.2},{"x":0.7,"y":0.5},{"x":0.3,"y":0.5}],"note":"print"`
	inputs := `{"slots":[{"view_key":"front","media_id":1},{"view_key":"back","media_id":2}],"artworks":[` +
		`{"name":"a","media_id":30,"view":"front","flat_media_id":1,` + quad + `},` +
		`{"name":"b","media_id":30,"view":"back","flat_media_id":2,` + quad + `}]}`
	job := t27Job(t, t27Params, inputs, EngineTable(""))
	require.Contains(t, job.Prompt, "- image 4: placement guide — the front flat")
	require.Contains(t, job.Prompt, "- image 5: placement guide — the back flat")
	require.Contains(t, job.Prompt, "- image 6: fabric photograph")
	require.Contains(t, job.Prompt, "Image 4 shows EXACTLY where it goes and how big it is: on the front")
	require.Contains(t, job.Prompt, "Image 5 shows EXACTLY where it goes and how big it is: on the back")
}

func TestArtworkTechniqueKind(t *testing.T) {
	for note, want := range map[string]string{
		"embroidery":             "embroidery",
		"chest embroidery":       "embroidery",
		"screen print":           "print",
		"Print":                  "print",
		"patch":                  "",
		"":                       "",
		"embroidered print look": "",
	} {
		require.Equalf(t, want, artworkTechniqueKind(artworkUse{Note: note}), "%q", note)
	}
	// No words frozen → the name speaks.
	require.Equal(t, "embroidery", artworkTechniqueKind(artworkUse{Name: "back embroidery"}))
}

func TestArtworkGroundOf(t *testing.T) {
	g, k := artworkGroundOf(runParams{Colour: &colourRecipe{Hex: "#53565a"}})
	require.Equal(t, artworkGroundCloth, k)
	require.Equal(t, color.NRGBA{R: 0x53, G: 0x56, B: 0x5a, A: 255}, g)
	_, k = artworkGroundOf(runParams{Colour: &colourRecipe{Hex: "#ffffff"}})
	require.Equal(t, artworkGroundGrey, k, "never onto white")
	g, k = artworkGroundOf(runParams{})
	require.Equal(t, artworkGroundGrey, k)
	require.Equal(t, artworkNeutralGround, g)
}
