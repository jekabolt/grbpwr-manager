package mail

import (
	"context"
	"embed"
	"fmt"
	"net/http"
	"path/filepath"
	"strings"
	"text/template"
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/dependency"
	"github.com/jekabolt/grbpwr-manager/internal/dto"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	gerr "github.com/jekabolt/grbpwr-manager/internal/errors"
	resend "github.com/jekabolt/grbpwr-manager/openapi/gen/resend"
	"github.com/sendgrid/sendgrid-go/helpers/mail"
)

//go:embed templates/*.gohtml
var templatesFS embed.FS

const (
	resendAPIBaseURL = "https://api.resend.com/"
)

type Config struct {
	APIKey         string        `mapstructure:"sendgrid_api_key"`
	FromEmail      string        `mapstructure:"from_email"`
	FromName       string        `mapstructure:"from_email_name"`
	ReplyTo        string        `mapstructure:"reply_to"`
	WorkerInterval time.Duration `mapstructure:"worker_interval"`
}

type Mailer struct {
	cli       dependency.Sender
	db        dependency.Mail
	from      *mail.Email
	c         *Config
	ctx       context.Context
	cancel    context.CancelFunc
	templates map[string]*template.Template
}

// addAuthHeader is a custom RequestEditorFn that adds an authorization header to the request
func addAuthHeader(token string) resend.RequestEditorFn {
	return func(ctx context.Context, req *http.Request) error {
		req.Header.Add("Authorization", "Bearer "+token)
		return nil
	}
}

func New(c *Config, db dependency.Mail) (*Mailer, error) {
	// Validate the configuration
	if c.APIKey == "" || c.FromEmail == "" || c.FromName == "" {
		return nil, fmt.Errorf("incomplete config: %+v", c)
	}

	// Initialize the resend client
	cli, err := resend.NewClient(resendAPIBaseURL, resend.ClientOption(func(rc *resend.Client) error {
		rc.RequestEditors = append(rc.RequestEditors, addAuthHeader(c.APIKey))
		return nil
	}))
	if err != nil {
		return nil, fmt.Errorf("error creating resend client: %w", err)
	}

	// Initialize the Mailer struct
	m := &Mailer{
		cli:       cli,
		db:        db,
		from:      mail.NewEmail(c.FromName, c.FromEmail),
		c:         c,
		templates: make(map[string]*template.Template),
	}

	// Parse email templates
	if err := m.parseTemplates(); err != nil {
		return nil, fmt.Errorf("error parsing templates: %w", err)
	}

	return m, nil
}

func (m *Mailer) parseTemplates() error {
	// Define the template directory path
	templateDir := "templates"

	// Read the directory entries from the embedded filesystem
	dirEntries, err := templatesFS.ReadDir(templateDir)
	if err != nil {
		return fmt.Errorf("error reading template directory: %w", err)
	}

	// Iterate over each file in the directory
	for _, entry := range dirEntries {
		// Skip directories
		if entry.IsDir() {
			continue
		}

		// Construct the full path of the template file
		templatePath := filepath.Join(templateDir, entry.Name())

		// Parse the template file
		tmpl, err := template.ParseFS(templatesFS, templatePath)
		if err != nil {
			return fmt.Errorf("error parsing template '%s': %w", entry.Name(), err)
		}

		m.templates[entry.Name()] = tmpl
	}

	return nil
}

func (m *Mailer) send(ctx context.Context, to, templateName string, data interface{}) (*entity.SendEmailRequest, error) {
	tmpl, ok := m.templates[templateName]
	if !ok {
		return nil, fmt.Errorf("template not found: %v", templateName)
	}

	subject, ok := templateSubjects[templateName]
	if !ok {
		return nil, fmt.Errorf("subject not found for template: %v", templateName)
	}

	body := new(strings.Builder)
	if err := tmpl.Execute(body, data); err != nil {
		return nil, fmt.Errorf("error executing template: %w", err)
	}

	html := body.String()
	sr := resend.SendEmailRequest{
		From:    fmt.Sprintf("%s <%s>", m.c.FromName, m.c.FromEmail),
		To:      []string{to},
		Html:    &html,
		Subject: subject,
		ReplyTo: &m.c.FromEmail,
	}
	esr := dto.ResendSendEmailRequestToEntity(&sr, to)
	resp, err := m.cli.PostEmails(ctx, sr)
	if err != nil {
		if resp.StatusCode == http.StatusTooManyRequests {
			return esr, gerr.MailApiLimitReached
		}
		return esr, fmt.Errorf("error sending email: %w", err)
	}

	return esr, nil
}

func (m *Mailer) sendRaw(ctx context.Context, ser *entity.SendEmailRequest) error {
	req, err := dto.EntitySendEmailRequestToResend(ser)
	if err != nil {
		return gerr.BadMailRequest
	}
	resp, err := m.cli.PostEmails(ctx, *req)
	if err != nil {
		return fmt.Errorf("error sending email: %w", err)
	}
	if resp.StatusCode == http.StatusTooManyRequests {
		return gerr.MailApiLimitReached
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("error sending email bad status code: %s, status code: %d", resp.Body, resp.StatusCode)
	}

	return nil
}
