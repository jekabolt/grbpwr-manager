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
)

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
	if strings.Count(rendered.HTML, preparedUnsubscribeURL) != 1 {
		return Rendered{}, warnings, fmt.Errorf("campaign HTML must contain exactly one unsubscribe URL")
	}
	if strings.Count(rendered.Text, preparedUnsubscribeURL) != 1 {
		return Rendered{}, warnings, fmt.Errorf("campaign text must contain exactly one unsubscribe URL")
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
