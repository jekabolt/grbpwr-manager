package mail

import (
	"context"
	"database/sql"
	"embed"
	"encoding/base64"
	"fmt"
	"html"
	"html/template"
	"io"
	"net/http"
	netmail "net/mail"
	"regexp"
	"strings"
	"sync"
	"time"

	"log/slog"

	"github.com/jekabolt/grbpwr-manager/internal/dependency"
	"github.com/jekabolt/grbpwr-manager/internal/dto"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/jekabolt/grbpwr-manager/internal/health"
	"github.com/jekabolt/grbpwr-manager/internal/storefront/tokenhash"
	resend "github.com/jekabolt/grbpwr-manager/openapi/gen/resend"
	"github.com/sendgrid/sendgrid-go/helpers/mail"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

//go:embed templates/*.gohtml templates/partials/*.gohtml
var templatesFS embed.FS

const (
	resendAPIBaseURL = "https://api.resend.com/"
	// resendHTTPTimeout bounds each Resend API request so a stalled TCP connection
	// cannot hang the caller indefinitely (e.g. an inline order-confirmation send).
	resendHTTPTimeout = 15 * time.Second
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

// NormalizeEmailAddress validates an email address and returns its normalized
// local@domain form. Callers that perform recipient-policy checks should use the
// same normalization as the final send path.
func NormalizeEmailAddress(to string) (string, error) {
	return validateEmailAddress(to)
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
	APIKey string `mapstructure:"sendgrid_api_key"`
	// Disabled, when true, suppresses BULK outbound email at the provider level
	// (both the inline send and the worker send) — order/tier/promo/support/etc.
	// Account sign-in emails (OTP + magic link, see alwaysSendSubjects) are the
	// exception and still dispatch, so beta login keeps working. Suppressed emails
	// are still built and recorded, just not sent to Resend. Used on beta to avoid
	// burning the Resend API quota during data seeding. Set via MAILER_DISABLED.
	Disabled  bool   `mapstructure:"disabled"`
	FromEmail string `mapstructure:"from_email"`
	FromName  string `mapstructure:"from_email_name"`
	ReplyTo   string `mapstructure:"reply_to"`
	// TestRecipients is a comma-separated allowlist for admin campaign test
	// sends. Empty intentionally permits any valid recipient for backwards
	// compatibility; suppression-list checks still apply.
	TestRecipients    string        `mapstructure:"test_recipients"`
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
	// UnsubscribePepper is the server-side HMAC key used to mint the per-recipient
	// one-click unsubscribe token embedded in the List-Unsubscribe URL (RFC 8058:
	// the URL must be unique and unguessable per recipient). Without it no
	// List-Unsubscribe header is emitted and the unsubscribe endpoint fails closed.
	UnsubscribePepper          string  `mapstructure:"unsubscribe_pepper"`
	ResendRequestsPerSecond    float64 `mapstructure:"resend_requests_per_second"`
	ResendBurst                int     `mapstructure:"resend_burst"`
	TransactionalReserveTokens int     `mapstructure:"transactional_reserve_tokens"`
	// LocalizationEnabled turns on per-recipient email language resolution. When false
	// (default, and on prod until sign-off) every email renders in the default locale
	// (en) — byte-for-byte the pre-feature behavior. Set via MAILER_LOCALIZATION_ENABLED.
	LocalizationEnabled bool `mapstructure:"localization_enabled"`
}

type Mailer struct {
	cli            dependency.Sender
	mailRepository dependency.Mail
	from           *mail.Email
	c              *Config
	ctx            context.Context
	cancel         context.CancelFunc
	templates      map[templateName]*template.Template
	catalog        *Catalog
	langRepo       dependency.RecipientLanguage
	wg             sync.WaitGroup
	tracker        health.Tracker
	budget         *ResendBudget
}

// Name implements health.Reporter.
func (m *Mailer) Name() string { return "mail" }

// LastSuccess implements health.Reporter (zero time until the first clean send tick).
func (m *Mailer) LastSuccess() time.Time { return m.tracker.LastSuccess() }

// addAuthHeader is a custom RequestEditorFn that adds an authorization header to the request
func addAuthHeader(token string) resend.RequestEditorFn {
	return func(ctx context.Context, req *http.Request) error {
		req.Header.Add("Authorization", fmt.Sprintf("Bearer %s", token))
		return nil
	}
}

func New(c *Config, mailRepository dependency.Mail, langRepo dependency.RecipientLanguage) (dependency.Mailer, error) {
	return new(c, mailRepository, langRepo)
}

func new(c *Config, mailRepository dependency.Mail, langRepo dependency.RecipientLanguage) (*Mailer, error) {
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

	if c.Disabled {
		slog.Default().Warn("mailer is DISABLED (MAILER_DISABLED) — bulk emails suppressed; only account sign-in (OTP/magic link) is still dispatched")
	}

	// Initialize the resend client with a bounded HTTP timeout so a stalled
	// Resend connection cannot hang the caller indefinitely.
	cli, err := resend.NewClient(resendAPIBaseURL,
		resend.WithHTTPClient(&http.Client{Timeout: resendHTTPTimeout}),
		resend.ClientOption(func(rc *resend.Client) error {
			rc.RequestEditors = append(rc.RequestEditors, addAuthHeader(c.APIKey))
			return nil
		}))
	if err != nil {
		return nil, fmt.Errorf("error creating resend client: %w", err)
	}

	// Build the i18n catalog from the embedded locale files.
	catalog, err := newCatalog()
	if err != nil {
		return nil, fmt.Errorf("error building i18n catalog: %w", err)
	}

	// Initialize the Mailer struct
	m := &Mailer{
		cli:            cli,
		mailRepository: mailRepository,
		from:           mail.NewEmail(c.FromName, c.FromEmail),
		c:              c,
		templates:      make(map[templateName]*template.Template),
		catalog:        catalog,
		langRepo:       langRepo,
		budget: NewResendBudget(
			c.ResendRequestsPerSecond,
			c.ResendBurst,
			c.TransactionalReserveTokens,
		),
	}

	// Parse email templates
	if err := m.parseTemplates(); err != nil {
		return nil, fmt.Errorf("error parsing templates: %w", err)
	}

	return m, nil
}

// currentSeason returns the fashion-calendar season label for t:
// Jan–Jun => "SS<YY>" (Spring/Summer), Jul–Dec => "FW<YY>" (Fall/Winter).
func currentSeason(t time.Time) string {
	yy := t.Year() % 100
	if int(t.Month()) <= 6 {
		return fmt.Sprintf("SS%02d", yy)
	}
	return fmt.Sprintf("FW%02d", yy)
}

// templateFuncs returns the FuncMap available to all email templates.
func templateFuncs() template.FuncMap {
	return template.FuncMap{
		// dict builds a map from alternating key/value pairs so a partial can
		// receive multiple named parameters (e.g. the reusable cta_button).
		"dict": func(values ...interface{}) (map[string]interface{}, error) {
			if len(values)%2 != 0 {
				return nil, fmt.Errorf("dict requires an even number of arguments")
			}
			m := make(map[string]interface{}, len(values)/2)
			for i := 0; i < len(values); i += 2 {
				key, ok := values[i].(string)
				if !ok {
					return nil, fmt.Errorf("dict keys must be strings")
				}
				m[key] = values[i+1]
			}
			return m, nil
		},
		// add is a small int helper for 1-based item numbering in templates.
		"add": func(a, b int) int { return a + b },
		// upper uppercases display text (e.g. order id shown in the body).
		"upper": strings.ToUpper,
		// unixNow stamps the email with its generation time (Lab Report header).
		"unixNow": func() int64 { return time.Now().Unix() },
		// season returns the current fashion-calendar season label, e.g. "SS26".
		"season": func() string { return currentSeason(time.Now()) },
		// i18n stubs — registered so templates using {{ t }} / {{ plural }} / {{ curLang }}
		// parse at startup. They are overridden per-render with locale-bound implementations
		// via localeFuncMap (see buildSendMailRequest); a template executed without the
		// override degrades to the key/default rather than failing to parse.
		"t":       func(key string, _ ...any) string { return key },
		"plural":  func(key string, _ int, _ ...any) string { return key },
		"curLang": func() string { return defaultLocale },
		"tHTML":   func(key string, _ ...any) template.HTML { return template.HTML(key) },
		"emph":    func(v any) template.HTML { return template.HTML(fmt.Sprint(v)) },
	}
}

func (m *Mailer) parseTemplates() error {
	// First, parse all partials into a base template
	partials, err := template.New("partials").Funcs(templateFuncs()).ParseFS(templatesFS, "templates/partials/*.gohtml")
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

	baseTmpl, ok := m.templates[tn]
	if !ok {
		return nil, fmt.Errorf("template not found: %v", tn)
	}

	// Resolve the recipient locale and bind a localizer. With localization disabled
	// (the default) this is always the default locale, so output matches pre-feature EN.
	loc := m.catalog.Localizer(m.resolveLocale(context.Background(), normalizedTo, localeHint(data)))

	subject, err := m.subject(tn, loc, data)
	if err != nil {
		return nil, err
	}

	// Clone the shared template and attach the locale-bound i18n funcs for this render,
	// so the parsed templates are never mutated across concurrent sends.
	tmpl, err := baseTmpl.Clone()
	if err != nil {
		return nil, fmt.Errorf("clone template: %w", err)
	}
	tmpl = tmpl.Funcs(localeFuncMap(loc))

	body := &strings.Builder{}
	// Execute the template by its filename
	if err := tmpl.ExecuteTemplate(body, string(tn), data); err != nil {
		return nil, fmt.Errorf("error executing template: %w", err)
	}

	html := body.String()
	text := htmlToText(html)

	replyTo := m.c.FromEmail
	if m.c.ReplyTo != "" {
		replyTo = m.c.ReplyTo
	}

	headers := m.listUnsubscribeHeaders(normalizedTo)
	sr := resend.SendEmailRequest{
		From:    fmt.Sprintf("%s <%s>", m.c.FromName, m.c.FromEmail),
		To:      []string{normalizedTo},
		Html:    &html,
		Text:    &text,
		Subject: subject,
		ReplyTo: &replyTo,
		Headers: headers,
	}

	return &sr, nil
}

// subjectKey maps each template to its catalog subject key. Order templates use a
// key that interpolates {{.OrderID}} (the uppercased order UUID); the rest are static.
var subjectKey = map[templateName]string{
	AccountLogin:            "account.login.subject",
	NewSubscriber:           "subscriber.welcome.subject",
	OrderConfirmed:          "order.confirmed.subject",
	OrderShipped:            "order.shipped.subject",
	OrderDelivered:          "order.delivered.subject",
	OrderCancelled:          "order.cancelled.subject",
	OrderRefundInitiated:    "order.refund.subject",
	OrderPendingReturn:      "order.return.subject",
	PromoCode:               "promo.code.subject",
	BackInStock:             "stock.back.subject",
	TierUpgrade:             "tier.upgrade.subject",
	TierDowngrade:           "tier.downgrade.subject",
	DowngradeReminder:       "tier.reminder.subject",
	TierRollbackAfterRefund: "tier.rollback.subject",
	FirstPurchaseThanks:     "tier.firstpurchase.subject",
	UnsubscribeConfirmation: "unsubscribe.confirm.subject",
	BirthdayGift:            "birthday.subject",
	EventInvite:             "event.invite.subject",
	HackerInvite:            "hacker.invite.subject",
}

// localeHint extracts the event locale carried on the email data — the site locale captured
// at purchase on order emails — or "" when the data carries none. It is the resolver's step-2
// hint, used when the recipient has no explicit account email_language (e.g. guests).
func localeHint(data interface{}) string {
	switch d := data.(type) {
	case *dto.OrderConfirmed:
		return d.Locale
	case *dto.OrderShipment:
		return d.Locale
	case *dto.OrderDelivered:
		return d.Locale
	case *dto.OrderCancelled:
		return d.Locale
	case *dto.OrderRefundInitiated:
		return d.Locale
	case *dto.OrderPendingReturn:
		return d.Locale
	}
	return ""
}

// orderSubjectID extracts the uppercased order UUID from order-email data, or "" for
// non-order emails, so an order subject key can interpolate {{.OrderID}}.
func orderSubjectID(data interface{}) string {
	id := ""
	switch d := data.(type) {
	case *dto.OrderConfirmed:
		id = d.OrderUUID
	case *dto.OrderShipment:
		id = d.OrderUUID
	case *dto.OrderDelivered:
		id = d.OrderUUID
	case *dto.OrderCancelled:
		id = d.OrderUUID
	case *dto.OrderRefundInitiated:
		id = d.OrderUUID
	case *dto.OrderPendingReturn:
		id = d.OrderUUID
	}
	return strings.ToUpper(id)
}

// subject returns the localized subject line for tn. Order emails interpolate the
// human-readable order id (e.g. "Order ORD-AB12 confirmed"); the {{.OrderID}} data is
// harmlessly ignored by the static (non-order) subject messages.
func (m *Mailer) subject(tn templateName, loc *Loc, data interface{}) (string, error) {
	key, ok := subjectKey[tn]
	if !ok {
		return "", fmt.Errorf("subject not found for template: %v", tn)
	}
	return loc.S(key, map[string]any{"OrderID": orderSubjectID(data)}), nil
}

var (
	reHTMLComment  = regexp.MustCompile(`(?is)<!--.*?-->`)
	reHTMLHead     = regexp.MustCompile(`(?is)<head[^>]*>.*?</head>`)
	reHTMLStyle    = regexp.MustCompile(`(?is)<style[^>]*>.*?</style>`)
	reHTMLHidden   = regexp.MustCompile(`(?is)<div[^>]*display:none[^>]*>.*?</div>`)
	reHTMLAnchor   = regexp.MustCompile(`(?is)<a[^>]*href="([^"]*)"[^>]*>(.*?)</a>`)
	reHTMLBreak    = regexp.MustCompile(`(?i)<br\s*/?>`)
	reHTMLBlockEnd = regexp.MustCompile(`(?i)</(p|div|tr|h1|h2|h3|h4|li|table)>`)
	reHTMLTag      = regexp.MustCompile(`(?is)<[^>]+>`)
	reHTMLSpaces   = regexp.MustCompile(`[ \t]+`)
	reHTMLNewlines = regexp.MustCompile(`\n{3,}`)
)

// htmlToText derives a readable plain-text alternative from a rendered HTML
// email. A multipart message (html + text) improves deliverability and is the
// accessible fallback for clients that do not render HTML.
func htmlToText(h string) string {
	s := reHTMLComment.ReplaceAllString(h, "")
	s = reHTMLHead.ReplaceAllString(s, "")
	s = reHTMLStyle.ReplaceAllString(s, "")
	s = reHTMLHidden.ReplaceAllString(s, "")
	// Turn links into "label (url)" before stripping tags.
	s = reHTMLAnchor.ReplaceAllString(s, "$2 ($1)")
	s = reHTMLBreak.ReplaceAllString(s, "\n")
	s = reHTMLBlockEnd.ReplaceAllString(s, "\n")
	s = reHTMLTag.ReplaceAllString(s, "")
	s = html.UnescapeString(s)

	lines := strings.Split(s, "\n")
	out := make([]string, 0, len(lines))
	for _, ln := range lines {
		ln = reHTMLSpaces.ReplaceAllString(ln, " ")
		out = append(out, strings.TrimSpace(ln))
	}
	s = strings.Join(out, "\n")
	s = reHTMLNewlines.ReplaceAllString(s, "\n\n")
	return strings.TrimSpace(s)
}

// listUnsubscribeHeaders returns RFC 8058 List-Unsubscribe headers for the given recipient.
// Returns nil when UnsubscribeBaseURL is not configured.
func (m *Mailer) listUnsubscribeHeaders(to string) *map[string]interface{} {
	// A base URL and a pepper are both required. Without the pepper we cannot mint
	// a per-recipient token, and an unauthenticated /{email_b64} link would let
	// anyone unsubscribe any guessable address. RFC 8058 requires the one-click URL
	// to be unique and unguessable per recipient, so omit the header rather than
	// emit an insecure one (the endpoint also fails closed without the pepper).
	if m.c.UnsubscribeBaseURL == "" || m.c.UnsubscribePepper == "" {
		return nil
	}
	emailB64 := base64.StdEncoding.EncodeToString([]byte(to))
	token := tokenhash.Hash(m.c.UnsubscribePepper, unsubscribeTokenValue(to))
	unsubURL := fmt.Sprintf("%s/api/webhooks/list-unsubscribe/%s/%s", m.c.UnsubscribeBaseURL, emailB64, token)
	h := map[string]interface{}{
		"List-Unsubscribe":      fmt.Sprintf("<%s>", unsubURL),
		"List-Unsubscribe-Post": "List-Unsubscribe=One-Click",
	}
	return &h
}

type sendOptions struct {
	exemptDisabledSuppression bool
	campaignLane              bool
}

func (m *Mailer) send(ctx context.Context, ser *resend.SendEmailRequest) error {
	return m.sendWithOptions(ctx, ser, sendOptions{})
}

func (m *Mailer) sendWithOptions(ctx context.Context, ser *resend.SendEmailRequest, opts sendOptions) error {
	if !opts.exemptDisabledSuppression && m.suppressed(ser.Subject) {
		return nil // bulk mail suppressed (e.g. beta seeding) — treat as sent, dispatch nothing
	}
	if opts.campaignLane {
		if !m.budget.TryCampaign() {
			return ErrCampaignBudgetUnavailable
		}
	} else if err := m.budget.WaitTransactional(ctx); err != nil {
		return fmt.Errorf("wait for transactional Resend budget: %w", err)
	}

	resp, err := m.cli.PostEmails(ctx, *ser)
	if resp != nil {
		// Drain and close the body so the underlying connection can be reused
		// rather than leaked (one leak per inline-sent email otherwise).
		defer func() {
			_, _ = io.Copy(io.Discard, resp.Body)
			_ = resp.Body.Close()
		}()
	}
	if err != nil {
		if resp != nil && resp.StatusCode == http.StatusTooManyRequests {
			retryAfter := parseRetryAfter(resp.Header.Get("Retry-After"), time.Now())
			if retryAfter <= 0 {
				retryAfter = time.Second
			}
			if opts.campaignLane {
				m.budget.CooldownCampaign(retryAfter)
			} else {
				m.budget.CooldownGlobal(retryAfter)
			}
			return mailApiLimitReached
		}
		return fmt.Errorf("error sending email: %w", err)
	}
	if resp == nil {
		// dependency.Sender test doubles historically return (nil, nil). The
		// production generated client always returns a response here.
		return nil
	}
	if resp.StatusCode == http.StatusTooManyRequests {
		retryAfter := parseRetryAfter(resp.Header.Get("Retry-After"), time.Now())
		if retryAfter <= 0 {
			retryAfter = time.Second
		}
		if opts.campaignLane {
			m.budget.CooldownCampaign(retryAfter)
		} else {
			m.budget.CooldownGlobal(retryAfter)
		}
		return mailApiLimitReached
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, readErr := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
		if readErr != nil {
			return &HTTPSendError{StatusCode: resp.StatusCode}
		}
		return &HTTPSendError{StatusCode: resp.StatusCode, Body: string(body)}
	}

	return nil
}

func (m *Mailer) sendRaw(ctx context.Context, ser *entity.SendEmailRequest) error {
	if m.suppressed(ser.Subject) {
		return nil // bulk mail suppressed (e.g. beta seeding) — treat as sent, dispatch nothing
	}
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
	if err := m.budget.WaitTransactional(ctx); err != nil {
		return fmt.Errorf("wait for transactional Resend budget: %w", err)
	}

	resp, err := m.cli.PostEmails(ctx, *req)
	if err != nil {
		return fmt.Errorf("error sending email: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode == http.StatusTooManyRequests {
		retryAfter := parseRetryAfter(resp.Header.Get("Retry-After"), time.Now())
		if retryAfter <= 0 {
			retryAfter = time.Second
		}
		m.budget.CooldownGlobal(retryAfter)
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
