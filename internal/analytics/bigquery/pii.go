package bigquery

import (
	"fmt"
	"regexp"
)

// PII scrubbing for behavioural analytics (task 21). A buggy storefront can leak an email
// address into GA4's page_location (e.g. a query param that survives into the URL), which then
// lands in the bq_* page reports as PII and pollutes the aggregates. The precompute queries scrub
// it out at the source, BEFORE the value is grouped or stored, so the cache never holds it.
//
// BigQuery's REGEXP_REPLACE and Go's regexp both use RE2, so scrubPIIEmails (unit-tested) and the
// SQL emitted by scrubbedPageLocationSQL share the SAME pattern and behave identically — the Go
// test is a faithful check of the query's redaction.

// piiEmailPattern matches an email address embedded anywhere in a string (path or query).
const piiEmailPattern = `[A-Za-z0-9._%+\-]+@[A-Za-z0-9.\-]+\.[A-Za-z]{2,}`

// piiRedaction is the placeholder that replaces a redacted email.
const piiRedaction = "[email]"

var piiEmailRegexp = regexp.MustCompile(piiEmailPattern)

// scrubPIIEmails redacts every email address embedded in s to piiRedaction. The Go mirror of
// the SQL scrubber, kept in lockstep via the shared RE2 pattern.
func scrubPIIEmails(s string) string {
	return piiEmailRegexp.ReplaceAllString(s, piiRedaction)
}

// scrubbedPageLocationSQL returns a BigQuery scalar expression that reads the page_location
// event param and redacts any embedded email to piiRedaction, falling back to '/' when the param
// is absent. Use it in place of a raw page_location read wherever the value is grouped or stored.
func scrubbedPageLocationSQL() string {
	return fmt.Sprintf(
		`REGEXP_REPLACE(IFNULL((SELECT value.string_value FROM UNNEST(event_params) WHERE key = 'page_location'), '/'), r'%s', '%s')`,
		piiEmailPattern, piiRedaction,
	)
}
