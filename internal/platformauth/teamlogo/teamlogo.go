// Package teamlogo turns an uploaded team logo into the one stored form (teams O §11): PNG or JPEG
// only, recognised by its bytes; at most MaxRawBytes; its header read before any pixel and refused
// above MaxSide on a side; decoded, fitted into Box×Box keeping the ratio, re-encoded as PNG.
// Anything else is ErrBadLogo, which the API answers with the fixed key bad_logo.
package teamlogo

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"image"
	"image/jpeg"
	"image/png"
	"strings"

	xdraw "golang.org/x/image/draw"
)

const (
	// MaxRawBytes is the largest upload accepted, before decoding.
	MaxRawBytes = 1 << 20
	// MaxSide is the largest width or height a header may declare.
	MaxSide = 4096
	// Box is the square a stored logo fits into.
	Box = 256
	// VersionLen is how many hex digits of the sha256 the URL carries as ?v=.
	VersionLen = 12
	// Path is the public route that serves a logo, followed by the team's slug.
	Path = "/api/auth/team-logo/"
)

// ErrBadLogo is every refusal: not PNG / JPEG, too big, a header above MaxSide, a broken image.
var ErrBadLogo = errors.New("teamlogo: bad logo")

// Logo is the stored form: PNG bytes and the hex sha256 of exactly those bytes.
type Logo struct {
	PNG    []byte
	SHA256 string
}

const (
	pngMagic  = "\x89PNG\r\n\x1a\n"
	jpegMagic = "\xff\xd8\xff"
)

// Process checks and normalises raw upload bytes. Empty input is ErrBadLogo too: callers treat
// «no logo» before calling.
func Process(raw []byte) (Logo, error) {
	if len(raw) == 0 || len(raw) > MaxRawBytes {
		return Logo{}, ErrBadLogo
	}
	var (
		decodeConfig func([]byte) (image.Config, error)
		decode       func([]byte) (image.Image, error)
	)
	switch {
	case bytes.HasPrefix(raw, []byte(pngMagic)):
		decodeConfig = func(b []byte) (image.Config, error) { return png.DecodeConfig(bytes.NewReader(b)) }
		decode = func(b []byte) (image.Image, error) { return png.Decode(bytes.NewReader(b)) }
	case bytes.HasPrefix(raw, []byte(jpegMagic)):
		decodeConfig = func(b []byte) (image.Config, error) { return jpeg.DecodeConfig(bytes.NewReader(b)) }
		decode = func(b []byte) (image.Image, error) { return jpeg.Decode(bytes.NewReader(b)) }
	default:
		return Logo{}, ErrBadLogo
	}
	// The header first: a small file may declare a huge canvas (a decompression bomb).
	cfg, err := decodeConfig(raw)
	if err != nil || cfg.Width <= 0 || cfg.Height <= 0 || cfg.Width > MaxSide || cfg.Height > MaxSide {
		return Logo{}, ErrBadLogo
	}
	src, err := decode(raw)
	if err != nil {
		return Logo{}, ErrBadLogo
	}
	b := src.Bounds()
	if b.Dx() != cfg.Width || b.Dy() != cfg.Height {
		return Logo{}, ErrBadLogo
	}
	w, h := Fit(b.Dx(), b.Dy())
	dst := image.NewNRGBA(image.Rect(0, 0, w, h))
	if w == b.Dx() && h == b.Dy() {
		xdraw.Draw(dst, dst.Bounds(), src, b.Min, xdraw.Src)
	} else {
		xdraw.CatmullRom.Scale(dst, dst.Bounds(), src, b, xdraw.Src, nil)
	}
	var out bytes.Buffer
	enc := png.Encoder{CompressionLevel: png.BestCompression}
	if err := enc.Encode(&out, dst); err != nil {
		return Logo{}, ErrBadLogo
	}
	sum := sha256.Sum256(out.Bytes())
	return Logo{PNG: out.Bytes(), SHA256: hex.EncodeToString(sum[:])}, nil
}

// Fit is the size of a w×h picture fitted into Box×Box keeping its ratio: a picture that already
// fits keeps its size (never enlarged); otherwise the longer side becomes Box and the shorter one
// is rounded, at least 1.
func Fit(w, h int) (int, int) {
	if w <= Box && h <= Box {
		return w, h
	}
	if w >= h {
		return Box, max(1, (h*Box+w/2)/w)
	}
	return max(1, (w*Box+h/2)/h), Box
}

// URL is the absolute address of a team's logo: apiBase (scheme and host, e.g.
// https://api.lekalo.studio) + Path + slug + ?v=<first VersionLen hex digits of the sha256>, so it
// changes when the logo does. Empty when the team has no logo (sha empty) or no base / slug.
func URL(apiBase, slug, sha string) string {
	apiBase = strings.TrimRight(strings.TrimSpace(apiBase), "/")
	if apiBase == "" || slug == "" || sha == "" {
		return ""
	}
	v := sha
	if len(v) > VersionLen {
		v = v[:VersionLen]
	}
	return apiBase + Path + slug + "?v=" + v
}
