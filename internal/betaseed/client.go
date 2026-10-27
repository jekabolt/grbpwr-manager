// Package betaseed is a typed, re-runnable seeder for the grbpwr BETA
// environment. It builds requests from the generated proto messages and sends
// them as protojson over the public grpc-gateway (native gRPC is not reachable
// through DigitalOcean's HTTP ingress). It talks only to the base beta host or a
// numbered beta host — a hard safety rail, never prod.
package betaseed

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
)

// BetaBackend is the default backend for the seeder. NewClient also permits
// numbered beta hosts such as backend-beta-02.grbpwr.com.
const BetaBackend = "https://backend-beta.grbpwr.com"

var betaBackendHostRe = regexp.MustCompile(`^backend-beta(-[0-9]{2})?\.grbpwr\.com$`)

// authHeader is the gateway header the admin auth interceptor forwards as
// metadata (internal/apisrv/auth.AuthMetadataKey). Plain "Authorization" is NOT
// forwarded by the gateway header-matcher, so this exact spelling matters.
const authHeader = "Grpc-Metadata-Authorization"

// Client is a protojson-over-gateway admin/auth/frontend client for beta.
type Client struct {
	base  string
	hc    *http.Client
	token string
	// LogRPC, when set, is called with every request for debug tracing.
	LogRPC func(verb, path string, code int)
}

// NewClient returns a beta-guarded client. target may be an allowed hostname or
// its https URL; paths, credentials, query strings, fragments and ports are
// refused so the safety decision is made on one exact host.
func NewClient(target string) (*Client, error) {
	target = strings.TrimSpace(target)
	if !strings.Contains(target, "://") {
		target = "https://" + target
	}
	u, err := url.Parse(target)
	if err != nil || u.Scheme != "https" || u.User != nil || u.Port() != "" ||
		(u.Path != "" && u.Path != "/") || u.RawQuery != "" || u.Fragment != "" {
		return nil, fmt.Errorf("refusing invalid beta backend target %q", target)
	}
	host := strings.ToLower(u.Hostname())
	if !betaBackendHostRe.MatchString(host) {
		return nil, fmt.Errorf("refusing non-beta backend host %q", host)
	}
	return &Client{
		base: "https://" + host,
		hc:   &http.Client{Timeout: 120 * time.Second},
	}, nil
}

// BaseURL returns the normalized guarded target used for RPCs.
func (c *Client) BaseURL() string { return c.base }

// Token returns the current bearer (empty until Authenticate).
func (c *Client) Token() string { return c.token }

// SetToken overrides the bearer (mainly for tests).
func (c *Client) SetToken(t string) { c.token = t }

// APIError is a non-2xx gateway response.
type APIError struct {
	Method string
	Path   string
	Code   int
	Body   string
}

func (e *APIError) Error() string {
	return fmt.Sprintf("%s %s -> HTTP %d: %s", e.Method, e.Path, e.Code, truncate(e.Body))
}

// AsAPIError extracts an *APIError from err, if present.
func AsAPIError(err error) (*APIError, bool) {
	if e, ok := err.(*APIError); ok {
		return e, true
	}
	return nil, false
}

var (
	jsonUnmarshal = protojson.UnmarshalOptions{DiscardUnknown: true}
	jsonMarshal   = protojson.MarshalOptions{}
	// Path-token names are proto field names, which may contain digits (e.g. {b64_email}); the
	// character class MUST include 0-9 or such tokens are left unsubstituted in the URL.
	pathParamRe = regexp.MustCompile(`\{([a-z0-9_]+)\}`)
)

// call sends one RPC. For POST/PUT/PATCH the whole message is the JSON body; for
// GET/DELETE, {tokens} in the path are filled from in and any remaining scalar
// fields become query params. Path {tokens} are always filled from in by proto
// field name.
func (c *Client) call(ctx context.Context, verb, tmpl string, in, out proto.Message) error {
	path, used, err := fillPath(tmpl, in)
	if err != nil {
		return err
	}

	hasBody := verb == http.MethodPost || verb == http.MethodPut || verb == http.MethodPatch
	var body io.Reader
	if hasBody {
		bb, err := jsonMarshal.Marshal(in)
		if err != nil {
			return fmt.Errorf("marshal %T: %w", in, err)
		}
		body = bytes.NewReader(bb)
	} else if q := queryFromMessage(in, used); q != "" {
		path += "?" + q
	}

	req, err := http.NewRequestWithContext(ctx, verb, c.base+path, body)
	if err != nil {
		return err
	}
	if hasBody {
		req.Header.Set("Content-Type", "application/json")
	}
	if c.token != "" {
		req.Header.Set(authHeader, "Bearer "+c.token)
	}

	resp, err := c.hc.Do(req)
	if err != nil {
		return fmt.Errorf("%s %s: %w", verb, path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	rb, _ := io.ReadAll(resp.Body)
	if c.LogRPC != nil {
		c.LogRPC(verb, path, resp.StatusCode)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return &APIError{Method: verb, Path: path, Code: resp.StatusCode, Body: string(rb)}
	}
	if out != nil && len(rb) > 0 {
		if err := jsonUnmarshal.Unmarshal(rb, out); err != nil {
			return fmt.Errorf("unmarshal resp %s %s: %w (body=%s)", verb, path, err, truncate(string(rb)))
		}
	}
	return nil
}

// fillPath substitutes {field} path tokens from in (matched by proto field name)
// and returns the resolved path plus the set of field names consumed.
func fillPath(tmpl string, in proto.Message) (string, map[string]bool, error) {
	used := map[string]bool{}
	msg := in.ProtoReflect()
	fields := msg.Descriptor().Fields()
	var perr error
	out := pathParamRe.ReplaceAllStringFunc(tmpl, func(tok string) string {
		name := tok[1 : len(tok)-1]
		fd := fields.ByName(protoreflect.Name(name))
		if fd == nil {
			perr = fmt.Errorf("path param %q is not a field of %s", name, msg.Descriptor().FullName())
			return tok
		}
		used[name] = true
		return url.PathEscape(fmt.Sprintf("%v", msg.Get(fd).Interface()))
	})
	return out, used, perr
}

// queryFromMessage builds a query string from top-level scalar fields of in,
// skipping path-consumed fields and zero values. Objects/arrays are ignored
// (rare for the GET reads the seeder issues).
func queryFromMessage(in proto.Message, skip map[string]bool) string {
	b, err := jsonMarshal.Marshal(in)
	if err != nil {
		return ""
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		return ""
	}
	q := url.Values{}
	fields := in.ProtoReflect().Descriptor().Fields()
	for i := 0; i < fields.Len(); i++ {
		fd := fields.Get(i)
		if skip[string(fd.Name())] {
			continue
		}
		v, ok := m[fd.JSONName()]
		if !ok {
			continue
		}
		switch vv := v.(type) {
		case string:
			if vv != "" {
				q.Set(fd.JSONName(), vv)
			}
		case float64:
			q.Set(fd.JSONName(), strconv.FormatFloat(vv, 'f', -1, 64))
		case bool:
			if vv {
				q.Set(fd.JSONName(), "true")
			}
		case []any:
			// Repeated scalar/enum field -> one query param per element (the gateway
			// reads e.g. ?sections=A&sections=B). protojson renders enums as their
			// string name, numbers as float64, bools as bool. Objects/nested repeated
			// are not URL-expressible and are skipped.
			for _, el := range vv {
				switch ev := el.(type) {
				case string:
					if ev != "" {
						q.Add(fd.JSONName(), ev)
					}
				case float64:
					q.Add(fd.JSONName(), strconv.FormatFloat(ev, 'f', -1, 64))
				case bool:
					q.Add(fd.JSONName(), strconv.FormatBool(ev))
				}
			}
		}
	}
	return q.Encode()
}

func truncate(s string) string {
	const max = 600
	if len(s) > max {
		return s[:max] + "…"
	}
	return s
}
