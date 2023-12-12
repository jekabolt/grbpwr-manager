package mail

import (
	"context"
	"embed"
	"fmt"
	"path/filepath"
	"strings"
	"text/template"

	"github.com/jekabolt/grbpwr-manager/internal/dependency"
	"github.com/jekabolt/grbpwr-manager/internal/dto"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/resendlabs/resend-go"
	"github.com/sendgrid/sendgrid-go/helpers/mail"
)

//go:embed templates/*.gohtml
var templatesFS embed.FS

type Config struct {
	APIKey    string `mapstructure:"sendgrid_api_key"`
	FromEmail string `mapstructure:"from_email"`
	FromName  string `mapstructure:"from_email_name"`
	ReplyTo   string `mapstructure:"reply_to"`
}

type Mailer struct {
	cli       *resend.Client
	db        dependency.Mail
	from      *mail.Email
	c         *Config
	templates map[string]*template.Template
}

func New(c *Config, db dependency.Mail) (dependency.Mailer, error) {
	if c.APIKey == "" || c.FromEmail == "" || c.FromName == "" {
		return nil, fmt.Errorf("incomplete config: %+v", c)
	}

	m := &Mailer{
		cli:       resend.NewClient(c.APIKey),
		db:        db,
		from:      mail.NewEmail(c.FromName, c.FromEmail),
		c:         c,
		templates: make(map[string]*template.Template),
	}

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

	sr := &resend.SendEmailRequest{
		From:    fmt.Sprintf("%s <%s>", m.c.FromName, m.c.FromEmail),
		To:      []string{to},
		Html:    body.String(),
		Subject: subject,
		ReplyTo: m.c.FromEmail,
	}
	esr := dto.ResendSendEmailRequestToEntity(sr, to)
	_, err := m.cli.Emails.Send(sr)
	if err != nil {
		return esr, fmt.Errorf("error sending email: %w", err)
	}

	return esr, nil
}
