// Package svgcheck validates and measures an SVG before we store it and serve it back from our own
// public host: bucket.UploadContentNonRaster runs every SVG (a manual upload, an imported file)
// through InspectSVG, and nothing is written to the bucket until it has passed.
//
// It answers three questions — is it an SVG at all, is it safe to serve, what shape is it — and it
// NEVER rewrites the picture. In particular it does not flatten curves into polylines to suit the
// editor's stroke model: making curves editable is work on the editor, not a silent approximation
// here.
//
// (The inspector was born inside the former Recraft vector-generation package; vector generation
// was removed on 2026-09-29 and the inspector moved here unchanged, because uploads still need it.)
package svgcheck

import "errors"

var (
	// ErrInvalidResponse — the bytes are not what an SVG upload promises: empty, over the cap,
	// undecodable, not XML, or XML whose root is not <svg>.
	ErrInvalidResponse = errors.New("svgcheck: malformed SVG")

	// ErrNotVector — the bytes are a known raster (PNG, JPEG, GIF, WEBP, PDF) labelled as SVG.
	// Separate because storing those bytes would show a "vector" that is a bitmap.
	ErrNotVector = errors.New("svgcheck: the bytes are a raster image, not SVG")

	// ErrUnsafeSVG — the SVG carries active content (script, event handlers, javascript: links) or
	// an entity declaration. We publish these bytes from our own bucket into an admin's browser, so
	// they are refused rather than scrubbed: a partial scrub that misses one vector is worse than a
	// loud refusal of a file no legitimate drawing needs.
	ErrUnsafeSVG = errors.New("svgcheck: the SVG contains active or unsafe content")
)
