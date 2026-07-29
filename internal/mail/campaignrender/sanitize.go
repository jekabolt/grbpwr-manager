package campaignrender

import (
	"net/url"
	"regexp"
	"strings"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/microcosm-cc/bluemonday"
)

var emailColorPattern = regexp.MustCompile(`^#(?:[0-9a-fA-F]{3}|[0-9a-fA-F]{6})$`)

// richTextPolicy is deliberately narrower than bluemonday's UGC policy. Email
// campaign rich text is copy only: no layout elements, inline CSS, or remotely
// loaded content.
var richTextPolicy = func() *bluemonday.Policy {
	policy := bluemonday.NewPolicy()
	policy.AllowElements(
		"p", "br", "strong", "b", "em", "i", "u", "s", "a",
		"ul", "ol", "li", "h1", "h2", "h3", "blockquote", "span",
	)
	policy.AllowAttrs("href").OnElements("a")
	policy.AllowURLSchemes("https", "mailto")
	policy.RequireParseableURLs(true)
	policy.AddTargetBlankToFullyQualifiedLinks(true)
	policy.RequireNoReferrerOnLinks(true)
	return policy
}()

// SanitizeRichText returns the email-safe subset of an authored HTML fragment.
// It is called both before persistence and immediately before rendering.
func SanitizeRichText(value string) string {
	return richTextPolicy.Sanitize(value)
}

// SanitizeBlocks applies the write-side sanitizer recursively. It mutates only
// rich-text translation bodies and preserves all other authored fields.
func SanitizeBlocks(blocks []entity.EmailBlock) {
	for i := range blocks {
		block := &blocks[i]
		if block.Type == entity.EmailBlockTypeRichText {
			for j := range block.Translations {
				block.Translations[j].Body = SanitizeRichText(block.Translations[j].Body)
			}
		}
		if block.Type == entity.EmailBlockTypeTwoColumn && block.TwoColumn != nil {
			SanitizeBlocks(block.TwoColumn.Left)
			SanitizeBlocks(block.TwoColumn.Right)
		}
	}
}

// safeURL admits only absolute HTTPS URLs and mailto links. A scheme-less value the admin typed
// (e.g. "grbpwr.com/sale" or "hi@grbpwr.com") is coerced to https:// (or mailto:) first — otherwise
// a CTA/link with no scheme was silently dropped and the button never rendered. A value that DOES
// carry a scheme (including javascript:/data:) is left as-is and rejected below.
func safeURL(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	// Decide whether the admin typed a scheme. A "scheme" containing a dot is not a real
	// URL scheme (https/mailto/… have none) — it's a host:port the admin typed without a
	// scheme, e.g. "grbpwr.com:8443/sale", which url.Parse otherwise reads as scheme
	// "grbpwr.com" and we'd drop. javascript:/data: have no dot, so they still fall
	// through unchanged and are rejected by the switch below.
	if parsed, err := url.Parse(value); err != nil || parsed.Scheme == "" || strings.Contains(parsed.Scheme, ".") {
		if strings.Contains(value, "@") && !strings.ContainsAny(value, "/ :") {
			value = "mailto:" + value
		} else {
			value = "https://" + value
		}
	}
	parsed, err := url.Parse(value)
	if err != nil || parsed.Scheme == "" {
		return ""
	}
	switch strings.ToLower(parsed.Scheme) {
	case "https":
		if parsed.Host == "" {
			return ""
		}
	case "mailto":
		if parsed.Opaque == "" && parsed.Path == "" {
			return ""
		}
	default:
		return ""
	}
	return value
}

// normalizeLogoPosition constrains the header logo alignment to the three supported
// values, defaulting to center for anything unset or unrecognized.
func normalizeLogoPosition(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "left":
		return "left"
	case "right":
		return "right"
	default:
		return "center"
	}
}

// normalizeAspect constrains an image block's display aspect ratio to the supported
// set, defaulting to 16:9 (horizontal) for anything unset or unrecognized.
func normalizeAspect(value string) string {
	switch strings.TrimSpace(value) {
	case "1:1":
		return "1:1"
	case "4:5":
		return "4:5"
	default:
		return "16:9"
	}
}

// imageMaxWidthPx caps how wide an image block renders so vertical/square crops
// don't span the full body width. Horizontal images fill the 560px content column.
func imageMaxWidthPx(aspect string) int {
	switch normalizeAspect(aspect) {
	case "1:1":
		return 440
	case "4:5":
		return 380
	default:
		return 560
	}
}

func safeColor(value, fallback string) string {
	if emailColorPattern.MatchString(value) {
		return value
	}
	if emailColorPattern.MatchString(fallback) {
		return fallback
	}
	return "#ffffff"
}
