package designgen

import (
	"bytes"
	"context"
	"image"
	"image/color"
	"image/draw"
	"image/jpeg"
	"log/slog"
	"math"
	"strconv"
	"strings"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/jekabolt/grbpwr-manager/internal/orimages"
)

// ─────────────────────────── ARTWORK: SIZE, PLACE AND GUIDE (T27) ───────────────────────────
//
// ⚠ MEASURED (paint-parts t27 A/B on gpt-image-2): an artwork asset is usually a square picture
// with the logo in the middle ~40 % and transparent margins all round. Sent whole, under words that
// said «it fills that area», the model stretched the LOGO to the whole placed quad — margins
// included — and the back embroidery came out two and a half times too big (run 76). The fix that
// won the A/B has three halves, all here:
//
//  1. the picture goes out CROPPED to its content and flattened onto the cloth colour (mid-grey when
//     the recipe states none — never white, or white thread vanishes), and the placed quad is
//     shrunk to the content's sub-quad, so «fills that area» becomes true;
//  2. one PLACEMENT GUIDE per side carrying artworks: that side's bench flat with every artwork of
//     the side drawn in its content quad — the model copies size and place off a picture far more
//     faithfully than off percentages;
//  3. the paragraph names the guide and the technique (renderartwork.go).
//
// Guides are optional: they ride only within the engine's reference ceiling and are dropped
// side_r, side_l, back, front before anything else — the door counts the required pictures only
// (designImageCallRequiredImages) and prices the guides within the ceiling.

const (
	// artworkAlphaFloor — a pixel of a picture with real transparency is content above this alpha.
	artworkAlphaFloor = 10
	// artworkWhiteFloor / artworkChromaFloor — on an opaque picture, content is any pixel darker
	// than this on its darkest channel or more coloured than this (max − min channel).
	artworkWhiteFloor  = 235
	artworkChromaFloor = 18
	// artworkReadSide — the artwork is measured on a copy no longer than this (memory: the worker
	// has half a gigabyte, and an asset can be 8000 px).
	artworkReadSide = 2048
	// artworkSendSide — the cropped artwork the model sees.
	artworkSendSide = 1024
	// artworkGuideSide — the guide's long side.
	artworkGuideSide = 1536
)

// artworkGroundKind — what the tightened artwork was flattened onto; "" = not tightened.
type artworkGroundKind string

const (
	artworkGroundCloth artworkGroundKind = "cloth"
	artworkGroundGrey  artworkGroundKind = "grey"
)

var artworkNeutralGround = color.NRGBA{R: 0x80, G: 0x80, B: 0x80, A: 0xff}

// artworkGroundOf — the cloth colour of the recipe (its hex, else the first cloth's that states
// one), else mid-grey. A near-white cloth flattens onto grey too: white thread on a white ground is
// an artwork the model cannot see.
func artworkGroundOf(p runParams) (color.NRGBA, artworkGroundKind) {
	if c := p.Colour; c != nil {
		hexes := []string{c.Hex}
		for _, f := range statedCloths(c) {
			hexes = append(hexes, f.ColourHex)
		}
		for _, h := range hexes {
			if g, ok := artworkParseHex(h); ok && !artworkNearWhite(g) {
				return g, artworkGroundCloth
			}
		}
	}
	return artworkNeutralGround, artworkGroundGrey
}

func artworkParseHex(s string) (color.NRGBA, bool) {
	s = strings.TrimPrefix(strings.TrimSpace(s), "#")
	if len(s) != 6 {
		return color.NRGBA{}, false
	}
	v, err := strconv.ParseUint(s, 16, 32)
	if err != nil {
		return color.NRGBA{}, false
	}
	return color.NRGBA{R: uint8(v >> 16), G: uint8(v >> 8), B: uint8(v), A: 0xff}, true
}

func artworkNearWhite(c color.NRGBA) bool {
	return c.R >= artworkWhiteFloor && c.G >= artworkWhiteFloor && c.B >= artworkWhiteFloor
}

// artworkCut — one artwork tightened: the content bbox in fractions of the picture (x0, y0, x1,
// y1) and the content itself, ≤ artworkSendSide, its alpha the content mask.
type artworkCut struct {
	frac [4]float64
	img  *image.NRGBA
}

// artworkTighten finds the content of an artwork picture and cuts it out. Content is alpha >
// artworkAlphaFloor when the picture carries real transparency, else non-white (min channel <
// artworkWhiteFloor or chroma > artworkChromaFloor) — and on an opaque picture the white ground
// becomes transparent in the cut, so it can be flattened onto the cloth and drawn on a flat.
// ok = false: nothing reads as content (a blank picture) — it goes out as stored.
func artworkTighten(src image.Image) (artworkCut, bool) {
	fitted := freeformFit(src, artworkReadSide)
	b := fitted.Bounds()
	if b.Empty() {
		return artworkCut{}, false
	}
	m := image.NewNRGBA(image.Rect(0, 0, b.Dx(), b.Dy()))
	draw.Draw(m, m.Bounds(), fitted, b.Min, draw.Src)
	hasAlpha := hasTransparentPixel(m)
	W, H := m.Bounds().Dx(), m.Bounds().Dy()
	x0, y0, x1, y1 := W, H, -1, -1
	for y := 0; y < H; y++ {
		row := m.Pix[y*m.Stride : y*m.Stride+W*4]
		for x := 0; x < W; x++ {
			px := row[x*4 : x*4+4]
			content := false
			if hasAlpha {
				content = px[3] > artworkAlphaFloor
			} else {
				lo, hi := min(px[0], px[1], px[2]), max(px[0], px[1], px[2])
				content = lo < artworkWhiteFloor || int(hi)-int(lo) > artworkChromaFloor
				if !content {
					px[3] = 0 // the white ground of an opaque picture is not the artwork
				}
			}
			if content {
				x0, y0 = min(x0, x), min(y0, y)
				x1, y1 = max(x1, x), max(y1, y)
			}
		}
	}
	if x1 < x0 || y1 < y0 {
		return artworkCut{}, false
	}
	rect := image.Rect(x0, y0, x1+1, y1+1)
	cut := artworkCut{frac: [4]float64{
		float64(rect.Min.X) / float64(W), float64(rect.Min.Y) / float64(H),
		float64(rect.Max.X) / float64(W), float64(rect.Max.Y) / float64(H),
	}}
	if w, h, fits := freeformFitSize(rect.Dx(), rect.Dy(), artworkSendSide); fits {
		cut.img = image.NewNRGBA(image.Rect(0, 0, rect.Dx(), rect.Dy()))
		draw.Draw(cut.img, cut.img.Bounds(), m, rect.Min, draw.Src)
	} else {
		cut.img = image.NewNRGBA(image.Rect(0, 0, w, h))
		leanScale(cut.img, cut.img.Bounds(), m, rect)
	}
	return cut, true
}

// artworkOnGround — the cut flattened onto the ground, as a JPEG data URI (opaque by then).
func artworkOnGround(cut *image.NRGBA, ground color.NRGBA) (string, error) {
	b := cut.Bounds()
	out := image.NewNRGBA(b)
	for y := b.Min.Y; y < b.Max.Y; y++ {
		for x := b.Min.X; x < b.Max.X; x++ {
			s := cut.NRGBAAt(x, y)
			out.SetNRGBA(x, y, artworkBlend(ground, s, s.A))
		}
	}
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, out, &jpeg.Options{Quality: freeformJPEGQuality}); err != nil {
		return "", err
	}
	return freeformDataURI("image/jpeg", buf.Bytes()), nil
}

// artworkBlend — `over` composited onto `under` at coverage a (0..255); the result is opaque.
func artworkBlend(under, over color.NRGBA, a uint8) color.NRGBA {
	f := float64(a) / 255
	mix := func(u, o uint8) uint8 { return uint8(math.Round(float64(u)*(1-f) + float64(o)*f)) }
	return color.NRGBA{R: mix(under.R, over.R), G: mix(under.G, over.G), B: mix(under.B, over.B), A: 0xff}
}

// artworkSubQuad maps the content bbox (fractions of the artwork picture) into the placed quad by
// bilinear interpolation of its four corners TL, TR, BR, BL.
func artworkSubQuad(q []artworkCorner, frac [4]float64) []artworkCorner {
	at := func(u, v float64) artworkCorner {
		return artworkCorner{
			X: (1-u)*(1-v)*q[0].X + u*(1-v)*q[1].X + u*v*q[2].X + (1-u)*v*q[3].X,
			Y: (1-u)*(1-v)*q[0].Y + u*(1-v)*q[1].Y + u*v*q[2].Y + (1-u)*v*q[3].Y,
		}
	}
	return []artworkCorner{at(frac[0], frac[1]), at(frac[2], frac[1]), at(frac[2], frac[3]), at(frac[0], frac[3])}
}

// artworkHomography — the projective map of the unit square onto the quad p (TL, TR, BR, BL, in
// pixels), as a 3×3 row-major matrix: (x, y, w) = M · (u, v, 1). Heckbert's square-to-quad.
func artworkHomography(p [4][2]float64) [9]float64 {
	x0, y0, x1, y1, x2, y2, x3, y3 := p[0][0], p[0][1], p[1][0], p[1][1], p[2][0], p[2][1], p[3][0], p[3][1]
	sx, sy := x0-x1+x2-x3, y0-y1+y2-y3
	var g, h float64
	if sx != 0 || sy != 0 {
		dx1, dy1, dx2, dy2 := x1-x2, y1-y2, x3-x2, y3-y2
		den := dx1*dy2 - dx2*dy1
		if den != 0 {
			g = (sx*dy2 - dx2*sy) / den
			h = (dx1*sy - sx*dy1) / den
		}
	}
	return [9]float64{
		x1 - x0 + g*x1, x3 - x0 + h*x3, x0,
		y1 - y0 + g*y1, y3 - y0 + h*y3, y0,
		g, h, 1,
	}
}

// artworkInvert3 — the inverse of a 3×3 (adjugate over determinant); ok = false when singular.
func artworkInvert3(m [9]float64) ([9]float64, bool) {
	a, b, c, d, e, f, g, h, i := m[0], m[1], m[2], m[3], m[4], m[5], m[6], m[7], m[8]
	A, B, C := e*i-f*h, -(d*i - f*g), d*h-e*g
	det := a*A + b*B + c*C
	if math.Abs(det) < 1e-12 {
		return [9]float64{}, false
	}
	adj := [9]float64{
		A, -(b*i - c*h), b*f - c*e,
		B, a*i - c*g, -(a*f - c*d),
		C, -(a*h - b*g), a*e - b*d,
	}
	for k := range adj {
		adj[k] /= det
	}
	return adj, true
}

// artworkDrawInQuad warps the cut into the quad q (fractions of dst's frame) by its homography, on a
// ground-coloured underlay under the cut's alpha — so white thread reads on a white flat.
func artworkDrawInQuad(dst *image.NRGBA, cut *image.NRGBA, q []artworkCorner, ground color.NRGBA) {
	db := dst.Bounds()
	W, H := float64(db.Dx()), float64(db.Dy())
	var px [4][2]float64
	minX, minY, maxX, maxY := math.Inf(1), math.Inf(1), math.Inf(-1), math.Inf(-1)
	for k := 0; k < 4; k++ {
		px[k] = [2]float64{q[k].X * W, q[k].Y * H}
		minX, maxX = math.Min(minX, px[k][0]), math.Max(maxX, px[k][0])
		minY, maxY = math.Min(minY, px[k][1]), math.Max(maxY, px[k][1])
	}
	inv, ok := artworkInvert3(artworkHomography(px))
	if !ok {
		return
	}
	area := image.Rect(int(math.Floor(minX)), int(math.Floor(minY)), int(math.Ceil(maxX)), int(math.Ceil(maxY))).
		Add(db.Min).Intersect(db)
	if area.Empty() {
		return
	}
	// The cut scaled to about the quad's size first, so the per-pixel lookup below can be nearest
	// without aliasing thin letters away.
	sw, sh := max(1, int(math.Ceil(maxX-minX))), max(1, int(math.Ceil(maxY-minY)))
	src := cut
	if cb := cut.Bounds(); cb.Dx() > sw || cb.Dy() > sh {
		src = image.NewNRGBA(image.Rect(0, 0, min(sw, cb.Dx()), min(sh, cb.Dy())))
		leanScale(src, src.Bounds(), cut, cb)
	}
	sb := src.Bounds()
	for y := area.Min.Y; y < area.Max.Y; y++ {
		for x := area.Min.X; x < area.Max.X; x++ {
			fx, fy := float64(x-db.Min.X)+0.5, float64(y-db.Min.Y)+0.5
			w := inv[6]*fx + inv[7]*fy + inv[8]
			if w == 0 {
				continue
			}
			u := (inv[0]*fx + inv[1]*fy + inv[2]) / w
			v := (inv[3]*fx + inv[4]*fy + inv[5]) / w
			if u < 0 || u >= 1 || v < 0 || v >= 1 {
				continue
			}
			s := src.NRGBAAt(sb.Min.X+int(u*float64(sb.Dx())), sb.Min.Y+int(v*float64(sb.Dy())))
			if s.A == 0 {
				continue
			}
			under := artworkBlend(dst.NRGBAAt(x, y), ground, s.A)
			dst.SetNRGBA(x, y, artworkBlend(under, s, s.A))
		}
	}
}

// artworkGuide composes one side's guide: the flat (fitted to artworkGuideSide) with every artwork
// of that side drawn into its content quad. JPEG data URI.
func artworkGuide(flat image.Image, arts []artworkUse, cuts map[int]artworkCut, ground color.NRGBA) (string, error) {
	fitted := freeformFit(flat, artworkGuideSide)
	b := fitted.Bounds()
	dst := image.NewNRGBA(image.Rect(0, 0, b.Dx(), b.Dy()))
	// A flat with transparency reads as white paper, the way the studio shows it.
	draw.Draw(dst, dst.Bounds(), image.NewUniform(color.White), image.Point{}, draw.Src)
	draw.Draw(dst, dst.Bounds(), fitted, b.Min, draw.Over)
	for _, a := range arts {
		if c, ok := cuts[a.MediaID]; ok && len(a.Corners) == 4 {
			artworkDrawInQuad(dst, c.img, a.Corners, ground)
		}
	}
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, dst, &jpeg.Options{Quality: freeformJPEGQuality}); err != nil {
		return "", err
	}
	return freeformDataURI("image/jpeg", buf.Bytes()), nil
}

// artworkGuideCaption — the guide's line in the reference list.
func artworkGuideCaption(view string, n int) string {
	if n > 1 {
		return "placement guide — the " + viewWord(view) + " flat with its " + strconv.Itoa(n) +
			" artworks drawn at their exact size and position on the garment"
	}
	return "placement guide — the " + viewWord(view) + " flat with the artwork drawn at its exact size and position on the garment"
}

// artworkGuideOrder — the sides that may carry a guide, in KEEP order: a ceiling drops them from
// the end (side_r, side_l, back, front). The door counts guides by the same list.
var artworkGuideOrder = []string{entity.DesignViewFront, entity.DesignViewBack, entity.DesignViewSideL, entity.DesignViewSideR}

// renderMaxRefs — the reference ceiling of the engine this render goes to, read the way the door
// reads it (designRefuseRenderArtworks): the table row of the stated slug, the default row when none
// is stated; the catalogue for a slug the table does not list; else the client's own ceiling.
func renderMaxRefs(model string, engines []Engine) int {
	if e, ok := FindEngine(engines, model); ok && e.MaxRefs > 0 {
		return e.MaxRefs
	}
	if e, ok := catalogueEngine(model); ok && e.MaxRefs > 0 {
		return e.MaxRefs
	}
	return orimages.MaxInputReferences
}

// deriveRenderArtworks tightens every attached artwork picture, shrinks the quads in `in` to the
// content, and inserts one placement guide per side right after that side's last artwork picture —
// as many as fit under maxRefs. References, ReferenceViews and attached move together. Anything that
// cannot be read degrades to today's behaviour for that artwork (picture as stored, whole quad, no
// guide on its side) and is logged: a guide is help, never a reason to fail a paid run.
func deriveRenderArtworks(ctx context.Context, objects objectFetcher, p runParams, in *runInputs, job *Job,
	attached []refCaption, maxRefs int) []refCaption {
	if objects == nil || len(in.Artworks) == 0 || len(job.References) != len(attached) {
		return attached
	}
	ground, groundKind := artworkGroundOf(p)
	arts := append([]artworkUse(nil), in.Artworks...)

	// 1. TIGHTEN each artwork picture once (one media may be placed on several sides).
	cuts := map[int]artworkCut{}
	for i, rc := range attached {
		if !rc.IsArtwork || rc.MediaID <= 0 {
			continue
		}
		img, err := freeformFetchImage(ctx, objects, job.References[i])
		if err != nil {
			slog.Default().WarnContext(ctx, "design render: artwork picture unreadable, sent as stored",
				"run", job.RunID, "media", rc.MediaID, "err", err)
			continue
		}
		cut, ok := artworkTighten(img)
		if !ok {
			continue
		}
		uri, err := artworkOnGround(cut.img, ground)
		if err != nil {
			continue
		}
		job.References[i] = uri
		cuts[rc.MediaID] = cut
		for k := range arts {
			if arts[k].MediaID == rc.MediaID {
				attached[i].Caption = artworkCaptionOn(arts[k], groundKind)
				break
			}
		}
	}
	for k := range arts {
		if c, ok := cuts[arts[k].MediaID]; ok && len(arts[k].Corners) == 4 {
			arts[k].Corners = artworkSubQuad(arts[k].Corners, c.frac)
			arts[k].Ground = groundKind
		}
	}
	in.Artworks = arts

	// 2. GUIDES: a side qualifies when every artwork the prompt will speak of there was tightened.
	type sideGuide struct {
		view  string
		flat  int // index in attached
		after int // index of the side's last artwork picture
		arts  []artworkUse
		uri   string
	}
	var sides []sideGuide
	for _, view := range artworkGuideOrder {
		g := sideGuide{view: view, flat: -1, after: -1}
		complete := true
		for _, a := range arts {
			if a.View != view || len(a.Corners) != 4 {
				continue
			}
			k := artworkNumberOf(attached, a.MediaID)
			j := imageNumberOf(attached, a.FlatMediaID)
			if k == 0 || j == 0 || j == k {
				continue // no paragraph speaks of it
			}
			if _, ok := cuts[a.MediaID]; !ok {
				complete = false
				break
			}
			if g.flat >= 0 && g.flat != j-1 {
				complete = false // two flats on one side: no single picture can guide both
				break
			}
			g.flat = j - 1
			g.after = max(g.after, k-1)
			g.arts = append(g.arts, a)
		}
		if complete && len(g.arts) > 0 {
			sides = append(sides, g)
		}
	}
	room := len(sides)
	if maxRefs > 0 {
		room = min(room, max(0, maxRefs-len(job.References)))
	}
	sides = sides[:room]
	var guides []sideGuide
	for _, g := range sides {
		flat, err := freeformFetchImage(ctx, objects, job.References[g.flat])
		if err != nil {
			slog.Default().WarnContext(ctx, "design render: flat unreadable, no placement guide for its side",
				"run", job.RunID, "view", g.view, "err", err)
			continue
		}
		uri, err := artworkGuide(flat, g.arts, cuts, ground)
		if err != nil {
			continue
		}
		g.uri = uri
		guides = append(guides, g)
	}
	if len(guides) == 0 {
		return attached
	}

	// 3. INSERT each guide right after its side's last artwork picture (side order kept on a tie).
	hasViews := len(job.ReferenceViews) == len(job.References)
	refs := make([]string, 0, len(job.References)+len(guides))
	views := make([]string, 0, len(job.References)+len(guides))
	out := make([]refCaption, 0, len(attached)+len(guides))
	for i, rc := range attached {
		refs = append(refs, job.References[i])
		if hasViews {
			views = append(views, job.ReferenceViews[i])
		}
		out = append(out, rc)
		for _, g := range guides {
			if g.after != i {
				continue
			}
			refs = append(refs, g.uri)
			views = append(views, "")
			out = append(out, refCaption{Caption: artworkGuideCaption(g.view, len(g.arts)), GuideView: g.view})
		}
	}
	job.References = refs
	if hasViews {
		job.ReferenceViews = views
	}
	return out
}
