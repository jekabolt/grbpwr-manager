package shop

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// ErrDomainTaken: the domain sits on another Vercel project (ours or someone else's) and the
// provider cannot attach it here. The team removes it there first.
var ErrDomainTaken = errors.New("shop: domain taken on the provider")

// VercelConfig is the backend's access to the multi-shop storefront project on Vercel (X18-L5).
// Env: VERCEL_TOKEN (an access token scoped to the team that owns the project), VERCEL_PROJECT_ID
// (id or name of the project, e.g. lekalo-shop), VERCEL_TEAM_ID (the Vercel team id; empty for a
// personal account). An empty token or project leaves the fake provider in place.
type VercelConfig struct {
	Token     string `mapstructure:"token"`
	ProjectID string `mapstructure:"project_id"`
	TeamID    string `mapstructure:"team_id"`
}

// Enabled reports whether the real provider can run.
func (c VercelConfig) Enabled() bool {
	return strings.TrimSpace(c.Token) != "" && strings.TrimSpace(c.ProjectID) != ""
}

const (
	vercelAPI            = "https://api.vercel.com"
	vercelRequestTimeout = 10 * time.Second
	vercelAttempts       = 4
	vercelBackoffBase    = 500 * time.Millisecond
	vercelRetryAfterMax  = 10 * time.Second
	vercelBodyMax        = 1 << 20

	// Used only when Vercel's config answer carries no recommendation of its own.
	vercelDefaultIPv4  = "76.76.21.21"
	vercelDefaultCNAME = "cname.vercel-dns.com"
)

// Vercel is the DomainProvider over the Vercel REST API: custom domains are project domains of the
// one multi-shop storefront project. Every call is idempotent, has a timeout, and retries 429 / 5xx /
// network errors with backoff. The token only ever goes into the Authorization header.
type Vercel struct {
	token, project, team string
	base                 string
	hc                   *http.Client
	// sleep waits between attempts (tests replace it).
	sleep func(context.Context, time.Duration) error
}

// NewVercel builds the provider. base "" = the public API (tests pass an httptest server).
func NewVercel(c VercelConfig, base string, hc *http.Client) *Vercel {
	if base == "" {
		base = vercelAPI
	}
	if hc == nil {
		hc = &http.Client{Timeout: vercelRequestTimeout}
	}
	return &Vercel{
		token:   strings.TrimSpace(c.Token),
		project: strings.TrimSpace(c.ProjectID),
		team:    strings.TrimSpace(c.TeamID),
		base:    strings.TrimSuffix(base, "/"),
		hc:      hc,
		sleep:   sleepCtx,
	}
}

func sleepCtx(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// vercelError is a non-2xx answer: status and Vercel's error code/message (never a header).
type vercelError struct {
	Status  int
	Code    string
	Message string
}

func (e *vercelError) Error() string {
	return fmt.Sprintf("vercel: http %d %s: %s", e.Status, e.Code, e.Message)
}

func statusOf(err error) int {
	var ve *vercelError
	if errors.As(err, &ve) {
		return ve.Status
	}
	return 0
}

// projectDomain is the part of Vercel's ProjectDomain we read.
type projectDomain struct {
	Name         string `json:"name"`
	ApexName     string `json:"apexName"`
	Redirect     string `json:"redirect"`
	Verified     bool   `json:"verified"`
	Verification []struct {
		Type   string `json:"type"`
		Domain string `json:"domain"`
		Value  string `json:"value"`
	} `json:"verification"`
}

// domainConfig is the part of GET /v6/domains/{d}/config we read.
type domainConfig struct {
	ConfiguredBy     *string        `json:"configuredBy"`
	Misconfigured    bool           `json:"misconfigured"`
	RecommendedIPv4  []rankedValues `json:"recommendedIPv4"`
	RecommendedCNAME []rankedValue  `json:"recommendedCNAME"`
}

type rankedValues struct {
	Rank  int      `json:"rank"`
	Value []string `json:"value"`
}

type rankedValue struct {
	Rank  int    `json:"rank"`
	Value string `json:"value"`
}

// do sends one API call with retries; out (when non-nil) receives a 2xx JSON body.
func (v *Vercel) do(ctx context.Context, method, path string, q url.Values, body, out any) error {
	if q == nil {
		q = url.Values{}
	}
	if v.team != "" {
		q.Set("teamId", v.team)
	}
	u := v.base + path
	if len(q) > 0 {
		u += "?" + q.Encode()
	}
	var payload []byte
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("vercel: encode: %w", err)
		}
		payload = b
	}
	var last error
	for attempt := 1; attempt <= vercelAttempts; attempt++ {
		retry, wait, err := v.once(ctx, method, u, payload, out)
		if err == nil {
			return nil
		}
		last = err
		if !retry || attempt == vercelAttempts || ctx.Err() != nil {
			break
		}
		if wait <= 0 {
			wait = vercelBackoffBase << (attempt - 1)
			wait += time.Duration(rand.Int64N(int64(wait) / 2))
		}
		if err := v.sleep(ctx, wait); err != nil {
			return fmt.Errorf("%w (then %w)", last, err)
		}
	}
	return last
}

// once is one attempt: retry says whether another may help, wait is Retry-After when given.
func (v *Vercel) once(ctx context.Context, method, u string, payload []byte, out any) (retry bool, wait time.Duration, err error) {
	rctx, cancel := context.WithTimeout(ctx, vercelRequestTimeout)
	defer cancel()
	var rd io.Reader
	if payload != nil {
		rd = bytes.NewReader(payload)
	}
	req, err := http.NewRequestWithContext(rctx, method, u, rd)
	if err != nil {
		return false, 0, fmt.Errorf("vercel: request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+v.token)
	req.Header.Set("Accept", "application/json")
	if payload != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := v.hc.Do(req)
	if err != nil {
		// A transport error names the URL (no token there); the parent's cancel is final.
		return ctx.Err() == nil, 0, fmt.Errorf("vercel: %s %s: %w", method, redactURL(u), errors.Unwrap(err))
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, vercelBodyMax))
	if err != nil {
		return true, 0, fmt.Errorf("vercel: read body: %w", err)
	}
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		if out != nil && len(raw) > 0 {
			if err := json.Unmarshal(raw, out); err != nil {
				return false, 0, fmt.Errorf("vercel: decode %s: %w", redactURL(u), err)
			}
		}
		return false, 0, nil
	}
	ve := &vercelError{Status: resp.StatusCode}
	var eb struct {
		Error struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if json.Unmarshal(raw, &eb) == nil {
		ve.Code, ve.Message = eb.Error.Code, eb.Error.Message
	}
	switch {
	case resp.StatusCode == http.StatusTooManyRequests:
		return true, retryAfter(resp.Header), ve
	case resp.StatusCode >= 500:
		return true, retryAfter(resp.Header), ve
	}
	return false, 0, ve
}

// retryAfter reads Retry-After (seconds) or X-RateLimit-Reset (unix seconds), capped.
func retryAfter(h http.Header) time.Duration {
	var d time.Duration
	if s := h.Get("Retry-After"); s != "" {
		if n, err := strconv.Atoi(strings.TrimSpace(s)); err == nil && n >= 0 {
			d = time.Duration(n) * time.Second
		}
	} else if s := h.Get("X-RateLimit-Reset"); s != "" {
		if n, err := strconv.ParseInt(strings.TrimSpace(s), 10, 64); err == nil {
			d = time.Until(time.Unix(n, 0))
		}
	}
	if d > vercelRetryAfterMax {
		d = vercelRetryAfterMax
	}
	if d < 0 {
		d = 0
	}
	return d
}

// redactURL drops the query (team id) from a URL for an error message.
func redactURL(u string) string {
	if i := strings.IndexByte(u, '?'); i >= 0 {
		return u[:i]
	}
	return u
}

func (v *Vercel) projectPath(rest string) string {
	return "/v9/projects/" + url.PathEscape(v.project) + rest
}

func (v *Vercel) getProjectDomain(ctx context.Context, host string) (projectDomain, error) {
	var pd projectDomain
	err := v.do(ctx, http.MethodGet, v.projectPath("/domains/"+url.PathEscape(host)), nil, nil, &pd)
	return pd, err
}

// Attach adds host to the project. A host the project already has answers its state; a host on
// another project is ErrDomainTaken; a name Vercel refuses is ErrBadHost.
func (v *Vercel) Attach(ctx context.Context, host string) (DomainState, error) {
	var pd projectDomain
	err := v.do(ctx, http.MethodPost, "/v10/projects/"+url.PathEscape(v.project)+"/domains", nil,
		map[string]string{"name": host}, &pd)
	switch st := statusOf(err); {
	case err == nil:
	case st == http.StatusConflict:
		// Either ours already (idempotent) or another project's: only our project can say.
		got, gerr := v.getProjectDomain(ctx, host)
		if statusOf(gerr) == http.StatusNotFound {
			return DomainState{}, ErrDomainTaken
		}
		if gerr != nil {
			return DomainState{}, gerr
		}
		pd = got
	case st == http.StatusBadRequest:
		var ve *vercelError
		if errors.As(err, &ve) && strings.Contains(ve.Code, "invalid") {
			return DomainState{}, fmt.Errorf("%w: %v", ErrBadHost, err)
		}
		return DomainState{}, err
	default:
		return DomainState{}, err
	}
	return v.state(ctx, host, pd)
}

// redirectStatus is the redirect a "www." sibling answers with: permanent, method kept.
const redirectStatus = http.StatusPermanentRedirect

// AttachRedirect adds host to the project as a redirect to target (a domain of the project). A host
// the project already has gets the redirect when it has another one; a host on another project is
// ErrDomainTaken; a name Vercel refuses is ErrBadHost.
func (v *Vercel) AttachRedirect(ctx context.Context, host, target string) (DomainState, error) {
	var pd projectDomain
	err := v.do(ctx, http.MethodPost, "/v10/projects/"+url.PathEscape(v.project)+"/domains", nil,
		map[string]any{"name": host, "redirect": target, "redirectStatusCode": redirectStatus}, &pd)
	switch st := statusOf(err); {
	case err == nil:
	case st == http.StatusConflict:
		got, gerr := v.getProjectDomain(ctx, host)
		if statusOf(gerr) == http.StatusNotFound {
			return DomainState{}, ErrDomainTaken
		}
		if gerr != nil {
			return DomainState{}, gerr
		}
		pd = got
		if NormHost(pd.Redirect) != target {
			if err := v.do(ctx, http.MethodPatch, v.projectPath("/domains/"+url.PathEscape(host)), nil,
				map[string]any{"redirect": target, "redirectStatusCode": redirectStatus}, &pd); err != nil {
				return DomainState{}, err
			}
		}
	case st == http.StatusBadRequest:
		var ve *vercelError
		if errors.As(err, &ve) && strings.Contains(ve.Code, "invalid") {
			return DomainState{}, fmt.Errorf("%w: %v", ErrBadHost, err)
		}
		return DomainState{}, err
	default:
		return DomainState{}, err
	}
	return v.state(ctx, host, pd)
}

// Check asks Vercel to verify host (when it is not yet verified) and reads its DNS config.
func (v *Vercel) Check(ctx context.Context, host string) (DomainState, error) {
	pd, err := v.getProjectDomain(ctx, host)
	if statusOf(err) == http.StatusNotFound {
		// Not on the project (removed by hand, or removed by the shop screen while a check ran).
		// Never re-added here: that could resurrect a removed domain. Attaching again fixes it.
		return DomainState{Status: "failed"}, nil
	}
	if err != nil {
		return DomainState{}, err
	}
	if !pd.Verified {
		var vd projectDomain
		verr := v.do(ctx, http.MethodPost, v.projectPath("/domains/"+url.PathEscape(host)+"/verify"), nil, nil, &vd)
		switch st := statusOf(verr); {
		case verr == nil:
			pd = vd
		case st >= 400 && st < 500 && st != http.StatusTooManyRequests && st != http.StatusUnauthorized && st != http.StatusForbidden:
			// «Not verified yet» (missing TXT and the like): the state below says what is missing.
		default:
			return DomainState{}, verr
		}
	}
	return v.state(ctx, host, pd)
}

// Remove takes host off the project; a host it no longer has is fine.
func (v *Vercel) Remove(ctx context.Context, host string) error {
	err := v.do(ctx, http.MethodDelete, v.projectPath("/domains/"+url.PathEscape(host)), nil, nil, nil)
	if statusOf(err) == http.StatusNotFound {
		return nil
	}
	return err
}

// state builds the answer from the project domain and its DNS config.
//
//   - live: Vercel verified the domain and its DNS points at Vercel;
//   - verifying: Vercel sees DNS pointing at it (fully or partly) but has not confirmed yet;
//   - records_needed: nothing of ours is seen yet.
func (v *Vercel) state(ctx context.Context, host string, pd projectDomain) (DomainState, error) {
	var cfg domainConfig
	q := url.Values{"projectIdOrName": {v.project}}
	if err := v.do(ctx, http.MethodGet, "/v6/domains/"+url.PathEscape(host)+"/config", q, nil, &cfg); err != nil {
		return DomainState{}, err
	}
	routed := !cfg.Misconfigured
	recs := []Record{routingRecord(host, pd.ApexName, cfg, routed)}
	if !pd.Verified {
		for _, ch := range pd.Verification {
			recs = append(recs, Record{Type: strings.ToUpper(ch.Type), Name: relName(ch.Domain, apexOf(host, pd.ApexName)), Value: ch.Value})
		}
	}
	st := DomainState{Records: recs}
	switch {
	case pd.Verified && routed:
		st.Status = "live"
	case routed || (cfg.ConfiguredBy != nil && *cfg.ConfiguredBy != ""):
		st.Status = "verifying"
	default:
		st.Status = "records_needed"
	}
	return st, nil
}

func apexOf(host, apex string) string {
	if apex = NormHost(apex); apex != "" {
		return apex
	}
	labels := strings.Split(host, ".")
	if len(labels) <= 2 {
		return host
	}
	return strings.Join(labels[len(labels)-2:], ".")
}

// relName is a record name as a DNS screen wants it: "@" for the apex, else the part before it.
func relName(fqdn, apex string) string {
	fqdn = NormHost(fqdn)
	if fqdn == apex || fqdn == "" {
		return "@"
	}
	return strings.TrimSuffix(fqdn, "."+apex)
}

// routingRecord is the A (apex) or CNAME (subdomain) record Vercel recommends for this project.
func routingRecord(host, apex string, cfg domainConfig, found bool) Record {
	a := apexOf(host, apex)
	if host == a {
		ip := vercelDefaultIPv4
		if best := bestIPv4(cfg.RecommendedIPv4); best != "" {
			ip = best
		}
		return Record{Type: "A", Name: "@", Value: ip, Found: found}
	}
	cname := vercelDefaultCNAME
	if best := bestCNAME(cfg.RecommendedCNAME); best != "" {
		cname = best
	}
	return Record{Type: "CNAME", Name: relName(host, a), Value: strings.TrimSuffix(cname, "."), Found: found}
}

func bestIPv4(rs []rankedValues) string {
	best, rank := "", 0
	for _, r := range rs {
		if len(r.Value) > 0 && (best == "" || r.Rank < rank) {
			best, rank = r.Value[0], r.Rank
		}
	}
	return best
}

func bestCNAME(rs []rankedValue) string {
	best, rank := "", 0
	for _, r := range rs {
		if r.Value != "" && (best == "" || r.Rank < rank) {
			best, rank = r.Value, r.Rank
		}
	}
	return best
}
