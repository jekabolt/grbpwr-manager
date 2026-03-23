package mail

import (
	"context"
	"database/sql"
	"embed"
	"encoding/base64"
	"fmt"
	"html/template"
	"io"
	netmail "net/mail"
	"net/http"
	"strings"
	"time"

	"log/slog"

	"github.com/jekabolt/grbpwr-manager/internal/dependency"
	"github.com/jekabolt/grbpwr-manager/internal/dto"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	resend "github.com/jekabolt/grbpwr-manager/openapi/gen/resend"
	"github.com/sendgrid/sendgrid-go/helpers/mail"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

//go:embed templates/*.gohtml templates/partials/*.gohtml
var templatesFS embed.FS

const (
	resendAPIBaseURL = "https://api.resend.com/"
)

const maxEmailAddressLength = 254 // RFC 5321

var (
	mailApiLimitReached = status.Error(codes.ResourceExhausted, "mail api limit reached")
)

// validateEmailAddress checks format via net/mail.ParseAddress, enforces length limit,
// and returns the normalized address (local@domain) or an error.
func validateEmailAddress(to string) (string, error) {
	s := strings.TrimSpace(to)
	if s == "" {
		return "", fmt.Errorf("email address is empty")
	}
	if len(s) > maxEmailAddressLength {
		return "", fmt.Errorf("email address exceeds max length %d", maxEmailAddressLength)
	}
	addr, err := netmail.ParseAddress(s)
	if err != nil {
		return "", fmt.Errorf("invalid email format: %w", err)
	}
	return addr.Address, nil
}

// HTTPSendError is returned when the Resend API responds with a non-success HTTP status.
type HTTPSendError struct {
	StatusCode int
	Body       string
}

func (e *HTTPSendError) Error() string {
	return fmt.Sprintf("error sending email bad status code: %s, status code: %d", e.Body, e.StatusCode)
}

type Config struct {
	APIKey            string        `mapstructure:"sendgrid_api_key"`
	FromEmail         string        `mapstructure:"from_email"`
	FromName          string        `mapstructure:"from_email_name"`
	ReplyTo           string        `mapstructure:"reply_to"`
	WorkerInterval    time.Duration `mapstructure:"worker_interval"`
	MaxSendAttempts   int           `mapstructure:"max_send_attempts"`
	RetryBaseInterval time.Duration `mapstructure:"retry_base_interval"`
	RetryMaxInterval  time.Duration `mapstructure:"retry_max_interval"`
	// InlineSendLease is how long sendWithInsert keeps next_retry_at in the future so the worker
	// does not send the same row while the inline HTTP request to the provider is in flight.
	InlineSendLease time.Duration `mapstructure:"inline_send_lease"`
	// WebhookSecret is the Svix signing secret for verifying Resend webhook payloads.
	WebhookSecret string `mapstructure:"webhook_secret"`
	// UnsubscribeBaseURL is the base URL used to construct List-Unsubscribe headers and one-click
	// unsubscribe URLs. Example: "https://backend.grbpwr.com" (no trailing slash).
	UnsubscribeBaseURL string `mapstructure:"unsubscribe_base_url"`
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
		var missing []string
		if c.APIKey == "" {
			missing = append(missing, "api_key")
		}
		if c.FromEmail == "" {
			missing = append(missing, "from_email")
		}
		if c.FromName == "" {
			missing = append(missing, "from_name")
		}
		return nil, fmt.Errorf("incomplete mail config: missing %v", missing)
	}

	applyMailerRetryDefaults(c)

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
	// First, parse all partials into a base template
	partials, err := template.ParseFS(templatesFS, "templates/partials/*.gohtml")
	if err != nil {
		return fmt.Errorf("error parsing partial templates: %w", err)
	}

	// Read the directory entries from the embedded filesystem to get template names
	dirEntries, err := templatesFS.ReadDir("templates")
	if err != nil {
		return fmt.Errorf("error reading template directory: %w", err)
	}

	// Iterate over each file in the templates directory (not subdirectories)
	for _, entry := range dirEntries {
		// Skip directories (like partials/)
		if entry.IsDir() {
			continue
		}

		fileName := entry.Name()

		// Clone the partials for each main template
		tmpl, err := partials.Clone()
		if err != nil {
			return fmt.Errorf("error cloning partials: %w", err)
		}

		// Read the specific template file content
		content, err := templatesFS.ReadFile("templates/" + fileName)
		if err != nil {
			return fmt.Errorf("error reading template %s: %w", fileName, err)
		}

		// Parse the content and name it
		tmpl, err = tmpl.New(fileName).Parse(string(content))
		if err != nil {
			return fmt.Errorf("error parsing template %s: %w", fileName, err)
		}

		m.templates[templateName(fileName)] = tmpl
	}

	return nil
}

func (m *Mailer) buildSendMailRequest(to string, tn templateName, data interface{}) (*resend.SendEmailRequest, error) {
	normalizedTo, err := validateEmailAddress(to)
	if err != nil {
		return nil, fmt.Errorf("invalid recipient: %w", err)
	}

	tmpl, ok := m.templates[tn]
	if !ok {
		return nil, fmt.Errorf("template not found: %v", tn)
	}

	subject, ok := templateSubjects[tn]
	if !ok {
		return nil, fmt.Errorf("subject not found for template: %v", tn)
	}

	body := &strings.Builder{}
	// Execute the template by its filename
	if err := tmpl.ExecuteTemplate(body, string(tn), data); err != nil {
		return nil, fmt.Errorf("error executing template: %w", err)
	}

	html := body.String()

	replyTo := m.c.FromEmail
	if m.c.ReplyTo != "" {
		replyTo = m.c.ReplyTo
	}

	headers := m.listUnsubscribeHeaders(normalizedTo)
	sr := resend.SendEmailRequest{
		From:    fmt.Sprintf("%s <%s>", m.c.FromName, m.c.FromEmail),
		To:      []string{normalizedTo},
		Html:    &html,
		Subject: subject,
		ReplyTo: &replyTo,
		Headers: headers,
	}

	return &sr, nil
}

// listUnsubscribeHeaders returns RFC 8058 List-Unsubscribe headers for the given recipient.
// Returns nil when UnsubscribeBaseURL is not configured.
func (m *Mailer) listUnsubscribeHeaders(to string) *map[string]interface{} {
	if m.c.UnsubscribeBaseURL == "" {
		return nil
	}
	emailB64 := base64.StdEncoding.EncodeToString([]byte(to))
	unsubURL := fmt.Sprintf("%s/api/webhooks/list-unsubscribe/%s", m.c.UnsubscribeBaseURL, emailB64)
	h := map[string]interface{}{
		"List-Unsubscribe":      fmt.Sprintf("<%s>", unsubURL),
		"List-Unsubscribe-Post": "List-Unsubscribe=One-Click",
	}
	return &h
}

func (m *Mailer) send(ctx context.Context, ser *resend.SendEmailRequest) error {

	resp, err := m.cli.PostEmails(ctx, *ser)
	if err != nil {
		if resp != nil && resp.StatusCode == http.StatusTooManyRequests {
			return mailApiLimitReached
		}
		return fmt.Errorf("error sending email: %w", err)
	}

	return nil
}

func (m *Mailer) sendRaw(ctx context.Context, ser *entity.SendEmailRequest) error {
	normalizedTo, err := validateEmailAddress(ser.To)
	if err != nil {
		return fmt.Errorf("invalid recipient in stored email: %w", err)
	}
	ser.To = normalizedTo

	req, err := dto.EntitySendEmailRequestToResend(ser)
	if err != nil {
		return fmt.Errorf("error converting email: %w", err)
	}

	// Attach List-Unsubscribe headers when sending queued emails from the worker.
	req.Headers = m.listUnsubscribeHeaders(normalizedTo)

	resp, err := m.cli.PostEmails(ctx, *req)
	if err != nil {
		return fmt.Errorf("error sending email: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode == http.StatusTooManyRequests {
		return mailApiLimitReached
	}
	if resp.StatusCode != http.StatusOK {
		body, err := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
		if err != nil {
			slog.Default().ErrorContext(ctx, "failed to read error response body", slog.String("err", err.Error()))
			return &HTTPSendError{StatusCode: resp.StatusCode, Body: ""}
		}
		return &HTTPSendError{StatusCode: resp.StatusCode, Body: string(body)}
	}

	return nil
}

func (m *Mailer) sendWithInsert(ctx context.Context, rep dependency.Repository, ser *resend.SendEmailRequest) error {

	eser, err := dto.ResendSendEmailRequestToEntity(ser)
	if err != nil {
		return fmt.Errorf("error converting email: %w", err)
	}

	leaseUntil := time.Now().UTC().Add(m.c.InlineSendLease)
	eser.NextRetryAt = sql.NullTime{Time: leaseUntil, Valid: true}

	id, err := rep.Mail().AddMail(ctx, eser)
	if err != nil {
		return fmt.Errorf("error inserting email: %w", err)
	}

	err = m.send(ctx, ser)
	if err != nil {
		if clearErr := rep.Mail().ClearNextRetryAt(ctx, id); clearErr != nil {
			return fmt.Errorf("error releasing mail inline-send hold: %w", clearErr)
		}
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

// queueEmail queues an email for sending without attempting to send immediately.
// The background worker will pick it up and send it asynchronously.
func (m *Mailer) queueEmail(ctx context.Context, rep dependency.Repository, ser *resend.SendEmailRequest) error {
	eser, err := dto.ResendSendEmailRequestToEntity(ser)
	if err != nil {
		return fmt.Errorf("error converting email: %w", err)
	}

	_, err = rep.Mail().AddMail(ctx, eser)
	if err != nil {
		return fmt.Errorf("error inserting email: %w", err)
	}

	return nil
}
