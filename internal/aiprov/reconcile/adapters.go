package reconcile

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/shopspring/decimal"

	hosts "github.com/jekabolt/grbpwr-manager/internal/aiprov/endpoints"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
)

// ───────────────────────── the endpoints ─────────────────────────
//
// Every URL a fetch can reach — constants on the aiprov/endpoints hosts (see the package comment).
// Each is a free read of a cost report; a test walks the table and fails on anything that looks like a
// paid call.
//
// ⚠ THE SHAPES BELOW WERE WRITTEN FROM MEMORY OF THE PROVIDERS' DOCS AND ARE UNVERIFIED (G-05): the
// first live fetch on beta with the owner's keys is the real check. That is why every parser is
// STRICT: a field it expects and does not find refuses the whole fetch (logged, nothing written) — a
// renamed field must fail loudly, never sum to a zero that reads as their number.
const (
	openAICostsURL    = hosts.OpenAIHost + "/v1/organization/costs"
	anthropicCostURL  = hosts.AnthropicHost + "/v1/organizations/cost_report"
	falUsageURL       = hosts.FalHost + "/v1/models/usage"
	openRouterKeyURL  = hosts.OpenRouterHost + "/api/v1/key"
	anthropicVersion  = "2023-06-01" // the header every Anthropic request carries (as the probe)
	dayLayout         = "2006-01-02"
	maxBody           = 1 << 20 // a two-day cost report is a few KiB; a page of per-model rows stays far below
	midnightGuardSpan = 2 * time.Minute
)

// auth is how a provider wants the key.
type auth int

const (
	authBearer    auth = iota + 1 // Authorization: Bearer <key>
	authAnthropic                 // x-api-key: <key> + anthropic-version
	authFal                       // Authorization: Key <key>
)

// window is the days one fetch asks about: all UTC midnights, computed from ONE reading of the clock.
type window struct {
	at                         time.Time // the instant the request is built, UTC
	yesterday, today, tomorrow time.Time
}

func windowAt(now time.Time) window {
	u := now.UTC()
	today := time.Date(u.Year(), u.Month(), u.Day(), 0, 0, 0, 0, time.UTC)
	return window{at: u, yesterday: today.AddDate(0, 0, -1), today: today, tomorrow: today.AddDate(0, 0, 1)}
}

// dayAmount is one provider day in USD.
type dayAmount struct {
	day string // YYYY-MM-DD, UTC
	usd decimal.Decimal
}

// adapter is one provider's cost API.
type adapter struct {
	provider string
	// kind is the key it takes: admin = the reconciliation key; api = the key that pays for calls.
	kind  entity.AIKeyKind
	url   string
	query func(window) string
	auth  auth
	// parse turns a 2xx body into provider days. Anything it does not recognise is an error.
	parse func(body []byte, win window) ([]dayAmount, error)
	// runningTotal — the answer is the running total of the CURRENT UTC day (OpenRouter), not a
	// closed bucket: it is refused when the request straddles a UTC midnight (errAcrossMidnight).
	runningTotal bool
}

// adapters — every provider with a cost API, in the panel's provider order.
var adapters = []adapter{
	{
		provider: entity.AIProviderOpenAI, kind: entity.AIKeyAdmin, auth: authBearer,
		url: openAICostsURL, query: openAICostsQuery, parse: parseOpenAICosts,
	},
	{
		provider: entity.AIProviderAnthropic, kind: entity.AIKeyAdmin, auth: authAnthropic,
		url: anthropicCostURL, query: anthropicCostQuery, parse: parseAnthropicCost,
	},
	{
		provider: entity.AIProviderOpenRouter, kind: entity.AIKeyAPI, auth: authBearer,
		url: openRouterKeyURL, parse: parseOpenRouterKey, runningTotal: true,
	},
	{
		provider: entity.AIProviderFal, kind: entity.AIKeyAdmin, auth: authFal,
		url: falUsageURL, query: falUsageQuery, parse: parseFalUsage,
	},
}

func adapterOf(provider string) (adapter, bool) {
	for _, a := range adapters {
		if a.provider == provider {
			return a, true
		}
	}
	return adapter{}, false
}

// target is the full URL of the fetch for win.
func (a adapter) target(win window) string {
	if a.query == nil {
		return a.url
	}
	return a.url + "?" + a.query(win)
}

// openAICostsQuery — daily buckets from yesterday 00:00 UTC: yesterday (closed) and today (partial).
// UNVERIFIED (G-05): start_time (unix seconds), bucket_width=1d, limit = buckets per page.
func openAICostsQuery(win window) string {
	return fmt.Sprintf("start_time=%d&bucket_width=1d&limit=2", win.yesterday.Unix())
}

// anthropicCostQuery — [yesterday, tomorrow) in 1d buckets.
// UNVERIFIED (G-05): starting_at / ending_at RFC 3339, ending_at exclusive and allowed in the future.
func anthropicCostQuery(win window) string {
	return fmt.Sprintf("starting_at=%s&ending_at=%s&bucket_width=1d",
		win.yesterday.Format(time.RFC3339), win.tomorrow.Format(time.RFC3339))
}

// falUsageQuery — [yesterday, tomorrow) by day, UTC.
// UNVERIFIED (G-05): start / end ISO 8601, timeframe=day, timezone=UTC (the probe checks the admin key
// against /v1/account/billing — a DIFFERENT endpoint; nothing has ever called this one).
func falUsageQuery(win window) string {
	return fmt.Sprintf("start=%s&end=%s&timeframe=day&timezone=UTC",
		win.yesterday.Format(time.RFC3339), win.tomorrow.Format(time.RFC3339))
}

// ───────────────────────── the request ─────────────────────────

// defaultClient serves a Worker built without WithHTTPClient; each fetch is bounded by fetchTimeout
// through its context.
var defaultClient = &http.Client{CheckRedirect: refuseRedirect}

// refuseRedirect keeps a redirect as the answer instead of following it (probe.refuseRedirect): the
// key goes to the one URL the fetch was built for and nowhere else.
func refuseRedirect(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }

// httpClient is the client a fetch uses: the default for nil, otherwise a shallow copy of the
// caller's (same transport and pool) that refuses redirects.
func httpClient(c *http.Client) *http.Client {
	if c == nil {
		return defaultClient
	}
	cp := *c
	cp.CheckRedirect = refuseRedirect
	return &cp
}

// errAcrossMidnight — a running-total answer that may belong to either of two UTC days. Not a
// failure: the next tick asks again. Written, it could overwrite a day's last snapshot with the first
// minutes of the next day.
var errAcrossMidnight = errors.New("the request straddled a UTC midnight")

// fetch makes ONE GET and parses its answer. The status is judged before the body; a non-2xx, a
// redirect, a body over maxBody or a shape the parser does not know is an error, and no error carries
// the key or any text the provider wrote.
func (w *Worker) fetch(ctx context.Context, a adapter, key string) ([]dayAmount, error) {
	if !headerSafe(key) {
		return nil, fmt.Errorf("%s: the stored key is not a header value (spaces or control characters)", a.provider)
	}
	ctx, cancel := context.WithTimeout(ctx, fetchTimeout)
	defer cancel()

	win := windowAt(w.now())
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, a.target(win), nil)
	if err != nil {
		return nil, fmt.Errorf("%s: the request could not be built", a.provider)
	}
	req.Header.Set("Accept", "application/json")
	switch a.auth {
	case authBearer:
		req.Header.Set("Authorization", "Bearer "+key)
	case authAnthropic:
		req.Header.Set("x-api-key", key)
		req.Header.Set("anthropic-version", anthropicVersion)
	case authFal:
		req.Header.Set("Authorization", "Key "+key)
	}

	resp, err := w.client.Do(req)
	if err != nil {
		// net/http's error names the URL (no key in it: keys travel in headers) and the cause.
		return nil, fmt.Errorf("%s: cost api unreachable: %w", a.provider, err)
	}
	defer resp.Body.Close()
	answered := w.now().UTC()

	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		// The body is not read: an error body is never quoted (it can echo the refused key).
		if resp.StatusCode >= 300 && resp.StatusCode < 400 {
			return nil, fmt.Errorf("%s: cost api answered http %d; redirects are not followed", a.provider, resp.StatusCode)
		}
		return nil, fmt.Errorf("%s: cost api answered http %d", a.provider, resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBody+1))
	if err != nil {
		return nil, fmt.Errorf("%s: cost api answer could not be read: %w", a.provider, err)
	}
	if len(body) > maxBody {
		return nil, fmt.Errorf("%s: cost api answer is larger than %d bytes", a.provider, maxBody)
	}
	if a.runningTotal && utcDay(win.at.Add(-midnightGuardSpan)) != utcDay(answered.Add(midnightGuardSpan)) {
		// The provider evaluated «today» somewhere in [built, answered] on a clock that may differ
		// from ours; if that span (± the guard) touches two UTC days, the total may be either's.
		return nil, errAcrossMidnight
	}
	days, err := a.parse(body, win)
	if err != nil {
		return nil, fmt.Errorf("%s: cost api answer not understood: %w", a.provider, err)
	}
	return days, nil
}

func utcDay(t time.Time) string { return t.UTC().Format(dayLayout) }

// headerSafe — printable ASCII with no spaces, the only shape a key has and the only one net/http puts
// in a header unchanged (probe.headerSafe).
func headerSafe(key string) bool {
	for i := 0; i < len(key); i++ {
		if key[i] <= ' ' || key[i] >= 0x7f {
			return false
		}
	}
	return true
}

// ───────────────────────── the answers ─────────────────────────

// bucket is one provider day as its API reported it: its start and the amounts in it, in USD.
type bucket struct {
	start time.Time
	usd   decimal.Decimal
}

// inWindow turns buckets into the days of win (yesterday and today). A bucket must start at a UTC
// midnight — one that does not is a day of some other zone, and labelling it UTC would be a lie — and
// a day may appear once. A bucket outside the window (fal may add tomorrow's empty one) is not written;
// buckets that are ALL outside it mean the API did not answer the question asked, which is an error,
// not «nothing to write».
func inWindow(buckets []bucket, win window) ([]dayAmount, error) {
	want := map[string]bool{win.yesterday.Format(dayLayout): true, win.today.Format(dayLayout): true}
	seen := map[string]bool{}
	var days []dayAmount
	for _, b := range buckets {
		u := b.start.UTC()
		_, offset := b.start.Zone()
		if offset != 0 || !u.Equal(time.Date(u.Year(), u.Month(), u.Day(), 0, 0, 0, 0, time.UTC)) {
			return nil, fmt.Errorf("a bucket starts at %s, not at a UTC midnight", b.start.Format(time.RFC3339))
		}
		day := u.Format(dayLayout)
		if seen[day] {
			return nil, fmt.Errorf("day %s is reported twice", day)
		}
		seen[day] = true
		if !want[day] {
			continue
		}
		days = append(days, dayAmount{day: day, usd: b.usd})
	}
	if len(buckets) > 0 && len(days) == 0 {
		return nil, errors.New("no bucket falls on the days asked for")
	}
	return days, nil
}

// amountOf reads one money value: a JSON number or a numeric string, in USD. A present currency other
// than USD is refused rather than summed as dollars; an absent one is USD (the API's own default).
func amountOf(v *json.Number, currency, what string) (decimal.Decimal, error) {
	if v == nil {
		return decimal.Zero, fmt.Errorf("a %s without its amount", what)
	}
	d, err := decimal.NewFromString(v.String())
	if err != nil {
		return decimal.Zero, fmt.Errorf("a %s amount is not a number", what)
	}
	if c := strings.TrimSpace(currency); c != "" && !strings.EqualFold(c, "USD") {
		return decimal.Zero, fmt.Errorf("a %s is in %q, not USD", what, safeWord(c))
	}
	return d, nil
}

// safeWord bounds a provider-written word before it enters an error (a currency code is three
// letters; anything else is cut).
func safeWord(s string) string {
	if len(s) > 8 {
		return s[:8] + "…"
	}
	return s
}

// errMore — an answer that says it has another page. One GET per provider per tick: a sum over the
// first page would be a partial number presented as theirs.
var errMore = errors.New("the answer has more pages; a partial sum is not their number")

// parseOpenAICosts reads GET /v1/organization/costs.
//
// UNVERIFIED (G-05): {"object":"page","data":[{"object":"bucket","start_time":<unix>,"end_time":<unix>,
// "results":[{"object":"organization.costs.result","amount":{"value":<number>,"currency":"usd"},…}]}],
// "has_more":false,"next_page":null}. Amounts are USD.
func parseOpenAICosts(body []byte, win window) ([]dayAmount, error) {
	var page struct {
		Data *[]struct {
			StartTime *int64 `json:"start_time"`
			Results   *[]struct {
				Amount *struct {
					Value    *json.Number `json:"value"`
					Currency string       `json:"currency"`
				} `json:"amount"`
			} `json:"results"`
		} `json:"data"`
		HasMore bool `json:"has_more"`
	}
	if err := json.Unmarshal(body, &page); err != nil {
		return nil, err
	}
	if page.Data == nil {
		return nil, errors.New("no data array")
	}
	if page.HasMore {
		return nil, errMore
	}
	buckets := make([]bucket, 0, len(*page.Data))
	for _, b := range *page.Data {
		if b.StartTime == nil || b.Results == nil {
			return nil, errors.New("a bucket without start_time or results")
		}
		sum := decimal.Zero
		for _, r := range *b.Results {
			if r.Amount == nil {
				return nil, errors.New("a result without its amount")
			}
			v, err := amountOf(r.Amount.Value, r.Amount.Currency, "cost result")
			if err != nil {
				return nil, err
			}
			sum = sum.Add(v)
		}
		buckets = append(buckets, bucket{start: time.Unix(*b.StartTime, 0).UTC(), usd: sum})
	}
	return inWindow(buckets, win)
}

// parseAnthropicCost reads GET /v1/organizations/cost_report.
//
// UNVERIFIED (G-05): {"data":[{"starting_at":"<RFC 3339>","ending_at":"…","results":[{"currency":"USD",
// "amount":"<decimal string>",…}]}],"has_more":false,"next_page":null}. The amount is in CENTS
// (lowest currency units) as a decimal string, per the plan's reading of the docs — divided by 100
// here. If G-05 finds dollars, the report shows their number a hundred times too small: check the
// first live row against the console before trusting the column.
func parseAnthropicCost(body []byte, win window) ([]dayAmount, error) {
	var page struct {
		Data *[]struct {
			StartingAt *string `json:"starting_at"`
			Results    *[]struct {
				Amount   *json.Number `json:"amount"`
				Currency string       `json:"currency"`
			} `json:"results"`
		} `json:"data"`
		HasMore bool `json:"has_more"`
	}
	if err := json.Unmarshal(body, &page); err != nil {
		return nil, err
	}
	if page.Data == nil {
		return nil, errors.New("no data array")
	}
	if page.HasMore {
		return nil, errMore
	}
	hundred := decimal.NewFromInt(100)
	buckets := make([]bucket, 0, len(*page.Data))
	for _, b := range *page.Data {
		if b.StartingAt == nil || b.Results == nil {
			return nil, errors.New("a bucket without starting_at or results")
		}
		start, err := time.Parse(time.RFC3339, *b.StartingAt)
		if err != nil {
			return nil, errors.New("a bucket whose starting_at is not RFC 3339")
		}
		cents := decimal.Zero
		for _, r := range *b.Results {
			v, err := amountOf(r.Amount, r.Currency, "cost result")
			if err != nil {
				return nil, err
			}
			cents = cents.Add(v)
		}
		buckets = append(buckets, bucket{start: start, usd: cents.Div(hundred)})
	}
	return inWindow(buckets, win)
}

// parseFalUsage reads GET /v1/models/usage.
//
// UNVERIFIED (G-05) — the least certain of the four: {"time_series":[{"bucket":"<RFC 3339>",
// "results":[{"endpoint_id":"…","unit":"…","quantity":<n>,"cost_total":<USD>,"currency":"USD",…}]}],
// "has_more":false,"next_cursor":null}. The plan names the per-row amount cost_total; the API reference
// as remembered here shows it as cost (beside unit_price). A row carrying either is read (cost_total
// first); a row carrying neither refuses the fetch.
func parseFalUsage(body []byte, win window) ([]dayAmount, error) {
	var page struct {
		TimeSeries *[]struct {
			Bucket  *string `json:"bucket"`
			Results *[]struct {
				CostTotal *json.Number `json:"cost_total"`
				Cost      *json.Number `json:"cost"`
				Currency  string       `json:"currency"`
			} `json:"results"`
		} `json:"time_series"`
		HasMore bool `json:"has_more"`
	}
	if err := json.Unmarshal(body, &page); err != nil {
		return nil, err
	}
	if page.TimeSeries == nil {
		return nil, errors.New("no time_series array")
	}
	if page.HasMore {
		return nil, errMore
	}
	buckets := make([]bucket, 0, len(*page.TimeSeries))
	for _, b := range *page.TimeSeries {
		if b.Bucket == nil || b.Results == nil {
			return nil, errors.New("a bucket without its timestamp or results")
		}
		start, err := time.Parse(time.RFC3339, *b.Bucket)
		if err != nil {
			return nil, errors.New("a bucket whose timestamp is not RFC 3339")
		}
		sum := decimal.Zero
		for _, r := range *b.Results {
			amount := r.CostTotal
			if amount == nil {
				amount = r.Cost
			}
			v, err := amountOf(amount, r.Currency, "usage row")
			if err != nil {
				return nil, err
			}
			sum = sum.Add(v)
		}
		buckets = append(buckets, bucket{start: start, usd: sum})
	}
	return inWindow(buckets, win)
}

// parseOpenRouterKey reads GET /api/v1/key: data.usage_daily, the USD this key has spent in the
// CURRENT UTC day so far — one row, today's.
//
// UNVERIFIED (G-05): {"data":{"label":"…","usage":<USD>,"usage_daily":<USD>,"usage_weekly":…,
// "usage_monthly":…,"limit":…,"limit_remaining":…}}. It is THIS KEY's spend: calls made with another
// key (OPENROUTER_IMAGES_API_KEY when it differs from the chat key and no key is stored in the panel)
// are not in it, and neither are calls another app makes with the same key.
func parseOpenRouterKey(body []byte, win window) ([]dayAmount, error) {
	var v struct {
		Data *struct {
			UsageDaily *json.Number `json:"usage_daily"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &v); err != nil {
		return nil, err
	}
	if v.Data == nil {
		return nil, errors.New("no data object")
	}
	usd, err := amountOf(v.Data.UsageDaily, "", "usage_daily")
	if err != nil {
		return nil, err
	}
	return []dayAmount{{day: win.today.Format(dayLayout), usd: usd}}, nil
}
