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

// safeURL admits only absolute HTTPS URLs and mailto links.
func safeURL(value string) string {
	value = strings.TrimSpace(value)
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

func safeColor(value, fallback string) string {
	if emailColorPattern.MatchString(value) {
		return value
	}
	if emailColorPattern.MatchString(fallback) {
		return fallback
	}
	return "#ffffff"
}
