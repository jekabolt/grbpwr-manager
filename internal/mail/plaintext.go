package mail

// HTMLToText exposes the transactional mailer's well-tested HTML-fragment
// conversion for campaign rich-text blocks. Campaign documents themselves use
// a semantic block walker rather than stripping the rendered table layout.
func HTMLToText(value string) string {
	return htmlToText(value)
}
