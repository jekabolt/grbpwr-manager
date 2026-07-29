package mail

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestRenderAllEmails renders every email template with representative mock data
// and writes the resulting HTML to ./preview_out/<template>.html plus an index.html.
// Run with:
//
//	go test ./internal/mail -run TestRenderAllEmails -v
//
// then open internal/mail/preview_out/index.html in a browser.
func TestRenderAllEmails(t *testing.T) {
	mailer := createTestMailer(t)

	samples := emailSamples()

	outDir := "preview_out"
	require.NoError(t, os.MkdirAll(outDir, 0o755))

	var links []string
	for _, s := range samples {
		req, err := mailer.buildSendMailRequest("customer@example.com", s.tn, s.data)
		require.NoError(t, err, "render %s", s.tn)
		require.NotNil(t, req.Html)

		fname := strings.TrimSuffix(string(s.tn), ".gohtml") + ".html"
		require.NoError(t, os.WriteFile(filepath.Join(outDir, fname), []byte(*req.Html), 0o644))
		links = append(links, "<li><a href=\""+fname+"\">"+req.Subject+"</a> <code>"+string(s.tn)+"</code></li>")
		t.Logf("rendered %-32s -> %s/%s", s.tn, outDir, fname)
	}

	index := "<!doctype html><meta charset=\"utf-8\"><title>GRBPWR email previews</title>" +
		"<body style=\"font-family:monospace;padding:24px;\"><h1>GRBPWR email previews</h1><ul>" +
		strings.Join(links, "\n") + "</ul></body>"
	require.NoError(t, os.WriteFile(filepath.Join(outDir, "index.html"), []byte(index), 0o644))
	t.Logf("open %s/index.html", outDir)
}
