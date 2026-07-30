package campaignrender

import (
	"errors"
	"fmt"
	"net/url"
	"strings"
)

const (
	UnsubscribePlaceholder = "{{UNSUB_URL}}"
	preparedUnsubscribeURL = "https://campaign-unsubscribe.invalid/one-click"
	// neutralizedUnsubscribePlaceholder is what authored copy containing the reserved
	// placeholder is rewritten to: visible to the admin, impossible to mistake for the
	// real placeholder.
	neutralizedUnsubscribePlaceholder = "[UNSUB_URL]"
)

// NeutralizeUnsubscribePlaceholder rewrites copy that literally contains the reserved
// {{UNSUB_URL}} snapshot placeholder. Prepare injects exactly one placeholder and
// ResolvePrepared rejects any snapshot holding a different count, so a literal typed into a
// heading or rich text used to fail every dispatch batch as payload_hash_mismatch — which
// terminally fails the whole audience with an error pointing at payload hashing instead of at
// the copy. Applied on write (SanitizeBlocks) and again on the rendered snapshot (Prepare), so
// campaigns saved before this guard cannot poison a send either.
func NeutralizeUnsubscribePlaceholder(value string) string {
	if !strings.Contains(value, UnsubscribePlaceholder) {
		return value
	}
	return strings.ReplaceAll(value, UnsubscribePlaceholder, neutralizedUnsubscribePlaceholder)
}

// Prepare renders with a valid sentinel URL, then replaces that resolved
// literal with one immutable placeholder in each representation. This keeps the
// renderer's HTTPS policy intact while ensuring snapshots contain no recipient
// data.
func (r *Renderer) Prepare(
	render func(unsubscribeURL string) (Rendered, []Warning, error),
) (Rendered, []Warning, error) {
	if r == nil {
		return Rendered{}, nil, errors.New("campaign renderer is nil")
	}
	rendered, warnings, err := render(preparedUnsubscribeURL)
	if err != nil {
		return Rendered{}, warnings, err
	}
	// Authored copy may itself contain the reserved placeholder literal; neutralize it before
	// injecting ours so the snapshot can never hold two.
	rendered.HTML = NeutralizeUnsubscribePlaceholder(rendered.HTML)
	rendered.Text = NeutralizeUnsubscribePlaceholder(rendered.Text)
	if strings.Count(rendered.HTML, preparedUnsubscribeURL) != 1 {
		return Rendered{}, warnings, fmt.Errorf(
			"campaign HTML must contain exactly one unsubscribe URL (authored copy must not contain %s)",
			preparedUnsubscribeURL)
	}
	if strings.Count(rendered.Text, preparedUnsubscribeURL) != 1 {
		return Rendered{}, warnings, fmt.Errorf(
			"campaign text must contain exactly one unsubscribe URL (authored copy must not contain %s)",
			preparedUnsubscribeURL)
	}
	rendered.HTML = strings.Replace(rendered.HTML, preparedUnsubscribeURL, UnsubscribePlaceholder, 1)
	rendered.Text = strings.Replace(rendered.Text, preparedUnsubscribeURL, UnsubscribePlaceholder, 1)
	return rendered, warnings, nil
}

// ResolvePrepared substitutes one recipient-specific topic URL. Any malformed
// snapshot or non-HTTPS URL is rejected before a provider request is built.
func ResolvePrepared(rendered Rendered, unsubscribeURL string) (Rendered, error) {
	parsed, err := url.Parse(strings.TrimSpace(unsubscribeURL))
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" {
		return Rendered{}, errors.New("campaign unsubscribe URL must be absolute HTTPS")
	}
	if strings.Count(rendered.HTML, UnsubscribePlaceholder) != 1 ||
		strings.Count(rendered.Text, UnsubscribePlaceholder) != 1 {
		return Rendered{}, errors.New("campaign render snapshot has invalid unsubscribe placeholder count")
	}
	rendered.HTML = strings.Replace(rendered.HTML, UnsubscribePlaceholder, unsubscribeURL, 1)
	rendered.Text = strings.Replace(rendered.Text, UnsubscribePlaceholder, unsubscribeURL, 1)
	return rendered, nil
}
