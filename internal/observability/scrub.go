package observability

import (
	"regexp"
	"strings"

	"github.com/getsentry/sentry-go"
)

var (
	emailRe = regexp.MustCompile(`[A-Za-z0-9._%+\-]+@[A-Za-z0-9.\-]+\.[A-Za-z]{2,}`)
	// A URL's query string or fragment inside free text: net/http errors quote the whole request
	// URL, API keys and tokens included.
	urlTailRe = regexp.MustCompile(`(https?://[^\s?#"'<>]+)[?#][^\s"'<>]*`)
)

// identityKeys are log attribute names that name a person (the codebase logs "username" and
// "email" on many paths); the admin is identified by admins.id only.
var identityKeys = map[string]bool{"username": true, "user_name": true, "email": true, "actor": true, "login": true, "sub": true}

// sensitiveKey matches attribute names whose values must never leave the process.
func sensitiveKey(k string) bool {
	k = strings.ToLower(k)
	if identityKeys[k] {
		return true
	}
	for _, s := range []string{"password", "passwd", "secret", "token", "authorization", "cookie", "api_key", "apikey", "dsn", "pepper"} {
		if strings.Contains(k, s) {
			return true
		}
	}
	return false
}

func scrubString(s string) string {
	return emailRe.ReplaceAllString(urlTailRe.ReplaceAllString(s, "${1}"), "[email]")
}

// scrubEvent is BeforeSend: emails and URL query strings out of every free-text field, secrets out
// of log attributes, and no request headers, cookies or query strings at all.
func scrubEvent(ev *sentry.Event, _ *sentry.EventHint) *sentry.Event {
	ev.Message = scrubString(ev.Message)
	for i := range ev.Exception {
		ev.Exception[i].Value = scrubString(ev.Exception[i].Value)
	}
	if attrs, ok := ev.Contexts["log"]; ok {
		for k, v := range attrs {
			if sensitiveKey(k) {
				attrs[k] = "[Filtered]"
				continue
			}
			if s, ok := v.(string); ok {
				attrs[k] = scrubString(s)
			}
		}
	}
	if ev.Request != nil {
		ev.Request.Headers = nil
		ev.Request.Cookies = ""
		ev.Request.QueryString = ""
		ev.Request.Data = ""
	}
	return ev
}
