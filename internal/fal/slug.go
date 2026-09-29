package fal

import (
	"regexp"
	"strings"
)

// slugRe — the shape of a fal model slug (an endpoint id): two or more lowercase segments of letters,
// digits, dots, underscores and dashes, joined by single slashes, no segment starting or ending in a
// dot (so no "." or ".." segment) and nothing a URL would read as query, fragment or escape.
//
// ⚠ ONE EXPRESSION FOR EVERY CALLER (H3). The slug is POSTed as the request path under the fal key
// (SubmitJSON: "/"+model), so the rule that keeps "../x" off the wire lives here, beside the client
// that builds the path: designgen.FalRouteModel (the extend / inpaint / cutout rows) and the image
// transport's Serves (images.go) both ask ValidSlug. It moved from designgen (Codex REVIEW-F1 #4),
// unchanged.
var slugRe = regexp.MustCompile(`^[a-z0-9_-]+(?:\.[a-z0-9_-]+)*(?:/[a-z0-9_-]+(?:\.[a-z0-9_-]+)*)+$`)

// ValidSlug reports whether s — exactly as given, nothing trimmed — is a well-formed fal endpoint id
// (see slugRe). A caller that accepts " /owner/model/ " trims first; this answers for the bytes that
// would reach the wire.
func ValidSlug(s string) bool { return slugRe.MatchString(s) }

// normSlug is the slug as SubmitJSON sends it: surrounding blanks and slashes dropped.
func normSlug(s string) string { return strings.Trim(strings.TrimSpace(s), "/") }
