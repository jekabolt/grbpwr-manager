package bigquery

import (
	"strings"
	"testing"
)

// TestScrubPIIEmails validates the RE2 pattern shared by the Go scrubber and the BigQuery
// REGEXP_REPLACE (task 21). Because both use RE2, these cases describe the query's behaviour too.
func TestScrubPIIEmails(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"email in query param", "https://grbpwr.com/checkout?email=jane.doe@example.com&step=2",
			"https://grbpwr.com/checkout?email=[email]&step=2"},
		{"email in path", "https://grbpwr.com/u/john%doe@mail.co.uk/orders",
			"https://grbpwr.com/u/[email]/orders"},
		{"plus-addressing", "https://grbpwr.com/x?e=a.b+tag@sub.domain.com",
			"https://grbpwr.com/x?e=[email]"},
		{"url-encoded at sign", "https://grbpwr.com/checkout?email=jane.doe%40example.com&step=2",
			"https://grbpwr.com/checkout?email=[email]&step=2"},
		{"two emails", "?a=one@x.com&b=two@y.org",
			"?a=[email]&b=[email]"},
		{"no email untouched", "https://grbpwr.com/products/coat?color=black",
			"https://grbpwr.com/products/coat?color=black"},
		{"at sign without domain untouched", "https://grbpwr.com/@handle",
			"https://grbpwr.com/@handle"},
		{"root path", "/", "/"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := scrubPIIEmails(tc.in); got != tc.want {
				t.Errorf("scrubPIIEmails(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

// TestScrubbedPageLocationSQL pins that the emitted SQL redacts before reading page_location and
// uses the shared pattern + placeholder — so a query built with it can never group on raw PII.
func TestScrubbedPageLocationSQL(t *testing.T) {
	sql := scrubbedPageLocationSQL()
	for _, want := range []string{"REGEXP_REPLACE(", "page_location", piiEmailPattern, "'" + piiRedaction + "'"} {
		if !strings.Contains(sql, want) {
			t.Errorf("scrubbedPageLocationSQL() = %q, missing %q", sql, want)
		}
	}
}
