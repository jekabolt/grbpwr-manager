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

	"log/slog"

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
	cli            dependency.Sender
	mailRepository dependency.Mail
	from           *mail.Email
	c              *Config
	ctx            context.Context
	cancel         context.CancelFunc
	templates      map[templateName]*template.Template
}

// addAuthHeader is a custom RequestEditorFn that adds an authorization header to the request
func addAuthHeader(token string) resend.RequestEditorFn {
	return func(ctx context.Context, req *http.Request) error {
		req.Header.Add("Authorization", fmt.Sprintf("Bearer %s", token))
		return nil
	}
}

func New(c *Config, mailRepository dependency.Mail) (dependency.Mailer, error) {
	return new(c, mailRepository)
}

func new(c *Config, mailRepository dependency.Mail) (*Mailer, error) {
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
		cli:            cli,
		mailRepository: mailRepository,
		from:           mail.NewEmail(c.FromName, c.FromEmail),
		c:              c,
		templates:      make(map[templateName]*template.Template),
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

		m.templates[templateName(entry.Name())] = tmpl
	}

	return nil
}

func (m *Mailer) buildSendMailRequest(to string, tn templateName, data interface{}) (*resend.SendEmailRequest, error) {
	tmpl, ok := m.templates[tn]
	if !ok {
		return nil, fmt.Errorf("template not found: %v", tn)
	}

	subject, ok := templateSubjects[tn]
	if !ok {
		return nil, fmt.Errorf("subject not found for template: %v", tn)
	}

	body := &strings.Builder{}
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

	return &sr, nil

}

func (m *Mailer) send(ctx context.Context, ser *resend.SendEmailRequest) error {

	resp, err := m.cli.PostEmails(ctx, *ser)
	if err != nil {
		if resp.StatusCode == http.StatusTooManyRequests {
			return gerr.MailApiLimitReached
		}
		return fmt.Errorf("error sending email: %w", err)
	}

	return nil
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

func (m *Mailer) sendWithInsert(ctx context.Context, rep dependency.Repository, ser *resend.SendEmailRequest) error {

	eser, err := dto.ResendSendEmailRequestToEntity(ser)
	if err != nil {
		return fmt.Errorf("error converting email: %w", err)
	}

	id, err := rep.Mail().AddMail(ctx, eser)
	if err != nil {
		return fmt.Errorf("error inserting email: %w", err)
	}

	err = m.send(ctx, ser)
	if err != nil {
		// mail send failed, it will be retried by the worker
		slog.Default().ErrorContext(ctx, "can't send mail",
			slog.String("err", err.Error()),
		)
		return nil
	}

	err = rep.Mail().UpdateSent(ctx, id)
	if err != nil {
		return fmt.Errorf("error updating email: %w", err)
	}

	return nil
}
