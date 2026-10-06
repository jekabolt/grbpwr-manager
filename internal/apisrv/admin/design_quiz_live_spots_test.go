//go:build quizlive

package admin

// 99-SPOTS §4 eval renders for the live harness (design_quiz_live_test.go): the picture with the
// numbered rings exactly as the client draws them (radius 9% of the tile's long side for a zone, 5%
// for a detail, clamped to 14–40 px on screen; 1px ink ring with a 1px white hairline outside; the
// number in a white box with a 1px ink border at the ring's upper right, flipped inside near the
// frame edge), rendered at QuizLiveSpotRenderLong px so a 360 px tile reads at 3×. Below the picture:
// the question and the legend. EXIF orientation is NOT applied (the decoded pixels as stored) — a
// rotated phone photo shows up as a systematic miss in the eval, which is the point (§4).

import (
	"bytes"
	"fmt"
	"image"
	"image/color"
	_ "image/gif"
	_ "image/jpeg"
	"image/png"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"golang.org/x/image/draw"
	"golang.org/x/image/font"
	"golang.org/x/image/font/basicfont"
	"golang.org/x/image/math/fixed"
	_ "golang.org/x/image/webp"
)

const (
	quizLiveSpotRenderLong = 1080 // the render's long side, px
	quizLiveSpotDefTile    = 360  // the on-screen tile's long side the rings are sized for, px
)

// quizLiveSpotRow — one spot in $QUIZ_OUT/spots.json (the judge's input).
type quizLiveSpotRow struct {
	Fixture    string `json:"fixture"`
	Model      string `json:"model"`
	Effort     string `json:"effort"`
	Run        int    `json:"run"`
	Picture    int    `json:"picture"` // 1-based «picture N»
	PictureURL string `json:"picture_url"`
	Role       string `json:"role"`
	QuestionID string `json:"question_id"`
	Question   string `json:"question"`
	N          int    `json:"n"` // the ring's number
	Label      string `json:"label"`
	X          int    `json:"x"`
	Y          int    `json:"y"`
	Scale      string `json:"scale"`
	At         int32  `json:"at"`
	PNG        string `json:"png"` // relative to QUIZ_OUT
}

func quizLiveSpotTile() int {
	if v, err := strconv.Atoi(os.Getenv("QUIZ_SPOT_TILE")); err == nil && v >= 100 {
		return v
	}
	return quizLiveSpotDefTile
}

func quizLivePicTag(q entity.DesignQuizQuestion) string {
	if q.MediaID == 0 {
		return ""
	}
	return " · picture " + strconv.Itoa(q.MediaID)
}

func quizLiveSpotsTag(q entity.DesignQuizQuestion) string {
	if len(q.Spots) == 0 {
		return ""
	}
	parts := make([]string, 0, len(q.Spots))
	for i, s := range q.Spots {
		parts = append(parts, fmt.Sprintf("%d %s (%s %d,%d)", i+1, s.Label, s.Scale, s.X, s.Y))
	}
	return " · spots: " + strings.Join(parts, "; ")
}

// quizLiveRenderRunSpots renders every picture question with spots of one run and returns its rows.
// Media id = «picture N» in the harness (quizLiveBoard), so the picture is pics[MediaID-1].
func quizLiveRenderRunSpots(out, fixture, tag string, r quizLiveResult, pics []quizLivePicture, images [][]byte, tile int) ([]quizLiveSpotRow, error) {
	var rows []quizLiveSpotRow
	var firstErr error
	for _, q := range r.Questions {
		if len(q.Spots) == 0 || q.MediaID < 1 || q.MediaID > len(images) {
			continue
		}
		rel := filepath.Join(fixture, "spots", tag+"_p"+strconv.Itoa(q.MediaID)+"_"+q.ID+".png")
		if err := quizLiveRenderSpots(images[q.MediaID-1], q, tile, filepath.Join(out, rel)); err != nil && firstErr == nil {
			firstErr = fmt.Errorf("%s: %w", rel, err)
		}
		pbSpots := designQuizSpotsToPb(q.Question, q.Spots)
		for i, s := range q.Spots {
			rows = append(rows, quizLiveSpotRow{
				Fixture: fixture, Model: r.Model, Effort: r.Effort, Run: r.Run,
				Picture: q.MediaID, PictureURL: pics[q.MediaID-1].URL, Role: pics[q.MediaID-1].Role,
				QuestionID: q.ID, Question: q.Question, N: i + 1,
				Label: s.Label, X: s.X, Y: s.Y, Scale: s.Scale, At: pbSpots[i].GetAt(), PNG: rel,
			})
		}
	}
	return rows, firstErr
}

var (
	quizLiveInk   = color.RGBA{0, 0, 0, 255}
	quizLiveWhite = color.RGBA{255, 255, 255, 255}
)

// quizLiveRenderSpots writes the picture with q's rings and a caption strip to path.
func quizLiveRenderSpots(data []byte, q entity.DesignQuizQuestion, tile int, path string) error {
	src, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return fmt.Errorf("decode: %w", err)
	}
	sb := src.Bounds()
	long := max(sb.Dx(), sb.Dy())
	w := sb.Dx() * quizLiveSpotRenderLong / long
	h := sb.Dy() * quizLiveSpotRenderLong / long
	k := float64(quizLiveSpotRenderLong) / float64(tile) // render px per on-screen px

	caption := quizLiveCaptionLines(q, w)
	const lineH, capScale, pad = 15, 2, 8
	capH := len(caption)*lineH*capScale + 2*pad
	dst := image.NewRGBA(image.Rect(0, 0, w, h+capH))
	draw.Draw(dst, dst.Bounds(), image.NewUniform(quizLiveWhite), image.Point{}, draw.Src)
	draw.CatmullRom.Scale(dst, image.Rect(0, 0, w, h), src, sb, draw.Over, nil)

	for i, s := range q.Spots {
		frac := 0.09
		if s.Scale == entity.DesignQuizSpotDetail {
			frac = 0.05
		}
		r := math.Min(math.Max(frac*float64(tile), 14), 40) * k
		cx := float64(s.X) / 1000 * float64(w)
		cy := float64(s.Y) / 1000 * float64(h)
		quizLiveRing(dst, cx, cy, r, k, h)
		quizLiveNumber(dst, i+1, cx, cy, r, k, w, h)
	}

	// Caption: basicfont 7×13 drawn at 1× then scaled ×2 (nearest) for legibility.
	cw := w / capScale
	cap1 := image.NewRGBA(image.Rect(0, 0, cw, len(caption)*lineH))
	draw.Draw(cap1, cap1.Bounds(), image.NewUniform(quizLiveWhite), image.Point{}, draw.Src)
	d := &font.Drawer{Dst: cap1, Src: image.NewUniform(quizLiveInk), Face: basicfont.Face7x13}
	for i, line := range caption {
		d.Dot = fixed.P(2, (i+1)*lineH-3)
		d.DrawString(line)
	}
	draw.NearestNeighbor.Scale(dst, image.Rect(0, h+pad, cw*capScale, h+pad+cap1.Bounds().Dy()*capScale), cap1, cap1.Bounds(), draw.Src, nil)

	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()
	return png.Encode(f, dst)
}

// quizLiveCaptionLines — the question (wrapped) then one legend line per spot, ASCII-folded for the
// bitmap font.
func quizLiveCaptionLines(q entity.DesignQuizQuestion, w int) []string {
	maxChars := max(w/2/7-1, 20)
	ascii := strings.NewReplacer("—", "-", "–", "-", "’", "'", "‘", "'", "“", `"`, "”", `"`, "…", "...", "×", "x", "·", "-")
	var lines []string
	for _, para := range []string{q.ID + " · " + q.Question} {
		line := ""
		for _, word := range strings.Fields(ascii.Replace(para)) {
			if line != "" && len(line)+1+len(word) > maxChars {
				lines = append(lines, line)
				line = ""
			}
			if line != "" {
				line += " "
			}
			line += word
		}
		if line != "" {
			lines = append(lines, line)
		}
	}
	for i, s := range q.Spots {
		lines = append(lines, fmt.Sprintf("(%d) %s  [%s  x=%d y=%d]", i+1, ascii.Replace(s.Label), s.Scale, s.X, s.Y))
	}
	return lines
}

// quizLiveRing — a 1 on-screen-px ink circle with a 1 px white hairline outside it, clipped to the
// picture (rows < maxY).
func quizLiveRing(dst *image.RGBA, cx, cy, r, k float64, maxY int) {
	inkIn, inkOut, whiteOut := r-k/2, r+k/2, r+1.5*k
	b := dst.Bounds()
	for y := int(cy - whiteOut - 1); y <= int(cy+whiteOut+1); y++ {
		if y < b.Min.Y || y >= maxY {
			continue
		}
		for x := int(cx - whiteOut - 1); x <= int(cx+whiteOut+1); x++ {
			if x < b.Min.X || x >= b.Max.X {
				continue
			}
			d := math.Hypot(float64(x)+0.5-cx, float64(y)+0.5-cy)
			switch {
			case d >= inkIn && d <= inkOut:
				dst.SetRGBA(x, y, quizLiveInk)
			case d > inkOut && d <= whiteOut:
				dst.SetRGBA(x, y, quizLiveWhite)
			}
		}
	}
}

// quizLiveNumber — the ring's number: a white box with a 1 px ink border at the ring's upper right
// (flipped to the other side of the ring when it would leave the picture).
func quizLiveNumber(dst *image.RGBA, n int, cx, cy, r, k float64, w, h int) {
	txt := strconv.Itoa(n)
	box := int(14 * k) // a 14 px box on screen around a 10 px digit
	bx := int(cx + r*math.Sqrt2/2)
	by := int(cy-r*math.Sqrt2/2) - box
	if bx+box > w {
		bx = int(cx-r*math.Sqrt2/2) - box
	}
	if by < 0 {
		by = int(cy + r*math.Sqrt2/2)
	}
	bx = min(max(bx, 0), w-box)
	by = min(max(by, 0), h-box)
	rect := image.Rect(bx, by, bx+box, by+box)
	draw.Draw(dst, rect, image.NewUniform(quizLiveInk), image.Point{}, draw.Src)
	bw := max(int(k), 1)
	draw.Draw(dst, rect.Inset(bw), image.NewUniform(quizLiveWhite), image.Point{}, draw.Src)

	glyph := image.NewRGBA(image.Rect(0, 0, 7*len(txt), 13))
	draw.Draw(glyph, glyph.Bounds(), image.NewUniform(quizLiveWhite), image.Point{}, draw.Src)
	(&font.Drawer{Dst: glyph, Src: image.NewUniform(quizLiveInk), Face: basicfont.Face7x13, Dot: fixed.P(0, 10)}).DrawString(txt)
	inner := rect.Inset(bw + max(int(k), 1))
	gh := inner.Dy()
	gw := gh * glyph.Bounds().Dx() / 13
	gx := inner.Min.X + (inner.Dx()-gw)/2
	draw.NearestNeighbor.Scale(dst, image.Rect(gx, inner.Min.Y, gx+gw, inner.Min.Y+gh), glyph, glyph.Bounds(), draw.Src, nil)
}

// TestDesignQuizLiveSpotsRender — the renderer alone, offline (no key, no network): a synthetic
// picture with a zone, a detail and an edge spot. QUIZ_SPOT_RENDER_OUT keeps the PNG for a look.
func TestDesignQuizLiveSpotsRender(t *testing.T) {
	img := image.NewRGBA(image.Rect(0, 0, 800, 1200))
	for y := 0; y < 1200; y++ {
		for x := 0; x < 800; x++ {
			img.SetRGBA(x, y, color.RGBA{uint8(60 + x/8), uint8(80 + y/10), 140, 255})
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	q := entity.DesignQuizQuestion{ID: "pic_1_edges", MediaID: 1,
		Question: "How are the inner strap and back edges finished — bound or turned?",
		Spots: []entity.DesignQuizSpot{{Label: "inner strap", X: 420, Y: 310, Scale: "detail"},
			{Label: "back edges", X: 500, Y: 700, Scale: "zone"}, {Label: "hem", X: 980, Y: 20, Scale: "zone"}}}
	dir := os.Getenv("QUIZ_SPOT_RENDER_OUT")
	if dir == "" {
		dir = t.TempDir()
	}
	r := quizLiveResult{Model: "m", Effort: "e", Run: 1, Questions: []entity.DesignQuizQuestion{q}}
	rows, err := quizLiveRenderRunSpots(dir, "synthetic", "m_e_1", r, []quizLivePicture{{URL: "synthetic", Role: "target"}}, [][]byte{buf.Bytes()}, quizLiveSpotDefTile)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 3 || rows[0].At != 12 || rows[2].At != -1 {
		t.Fatalf("rows: %+v", rows)
	}
	if _, err := os.Stat(filepath.Join(dir, rows[0].PNG)); err != nil {
		t.Fatal(err)
	}
	t.Logf("render: %s", filepath.Join(dir, rows[0].PNG))
}
