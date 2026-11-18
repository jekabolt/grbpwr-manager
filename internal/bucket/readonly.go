package bucket

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/tenant"
)

// READ-ONLY MANAGED HOSTS (teams wave, lane B8).
//
// A copied team database keeps file urls on the host of the installation it was copied from (team
// grbpwr in lekalo: files.grbpwr.com, bucket grbpwr). That installation is still live and owns those
// objects. BUCKET_S3_READONLY_HOSTS names such hosts. Two questions, two answers:
//
//   - «is this url ours to READ» (URLReadable): our own host, or a read-only host — the latter only
//     for team #1 (teams mode) / always with teams mode off. A read-only object lives in ANOTHER
//     bucket, so it is read over https from its public host, never by key from our bucket.
//   - «is this url ours to CHANGE» (URLChangeable / ManagedObjectKeyFromURL): our own host only, and
//     never a read-only host even when it is also configured as ours. A delete aimed at a read-only
//     host is refused before any network call with one WARN line, and the caller goes on as if the
//     object were not ours.
//
// Empty list = nothing here changes a byte of today's behaviour.

// ErrReadOnlyHost marks a change (delete / overwrite / move) aimed at a read-only host.
var ErrReadOnlyHost = errors.New("bucket: url is on a read-only host")

// errNotReadable marks a read-only host url read by a team that may not read it.
var errNotReadable = errors.New("bucket: url is on a read-only host this team may not read")

// legacyReadMaxBytes bounds one read from a read-only host; callers apply their own smaller caps.
const legacyReadMaxBytes = MaxArchiveObjectBytes

// ReadOnlyHosts returns the configured read-only hosts, lower-case, in order, without duplicates.
func ReadOnlyHosts(c *Config) []string {
	if c == nil {
		return nil
	}
	var out []string
	seen := map[string]struct{}{}
	for _, part := range strings.Split(c.ReadOnlyHosts, ",") {
		h := configuredURLHost(part)
		if h == "" {
			continue
		}
		if _, ok := seen[h]; ok {
			continue
		}
		seen[h] = struct{}{}
		out = append(out, h)
	}
	return out
}

// ValidateReadOnlyHosts checks the raw env value: each entry is a bare host or an https origin
// with no path, query or userinfo. Empty is valid (today's behaviour).
func ValidateReadOnlyHosts(raw string) error {
	for _, part := range strings.Split(raw, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		s := part
		if !strings.Contains(s, "://") {
			s = "https://" + s
		}
		u, err := url.Parse(s)
		if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil ||
			strings.Trim(u.Path, "/") != "" || u.RawQuery != "" || u.Fragment != "" {
			return fmt.Errorf("bucket read-only host %q must be a host or an https origin without a path", part)
		}
	}
	return nil
}

func (c *Config) isReadOnlyHost(host string) bool {
	if c == nil || strings.TrimSpace(c.ReadOnlyHosts) == "" {
		return false
	}
	host = strings.ToLower(host)
	for _, h := range ReadOnlyHosts(c) {
		if h == host {
			return true
		}
	}
	return false
}

// readOnlyHonoured: the read-only list belongs to team #1. With teams mode off every request is
// team #1 (a context with no team is a background job of the single installation).
func (c *Config) readOnlyHonoured(ctx context.Context) bool {
	if t, ok := tenant.From(ctx); ok {
		return t.ID == tenant.TeamOneID
	}
	return !c.TeamsMode
}

// readOnlyURL parses raw and reports whether it is an https url on a read-only host.
func (c *Config) readOnlyURL(raw string) (*url.URL, bool) {
	if c == nil || strings.TrimSpace(c.ReadOnlyHosts) == "" {
		return nil, false
	}
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil {
		return nil, false
	}
	return u, c.isReadOnlyHost(u.Host)
}

// URLChangeable answers «may this installation delete / overwrite / move the object behind raw»:
// only on our own host, never on a read-only one.
func URLChangeable(c *Config, raw string) bool {
	_, err := ManagedObjectKeyFromURL(c, raw)
	return err == nil
}

// URLReadable answers «may the team in ctx read the object behind raw as one of its files»: our own
// host, or a read-only host when the list is honoured for this team.
func URLReadable(ctx context.Context, c *Config, raw string) bool {
	if _, ok := c.readOnlyURL(raw); ok {
		return c.readOnlyHonoured(ctx)
	}
	return URLChangeable(c, raw)
}

// OpenStoredURL opens the object behind a url stored on a row (archive export, crop, design reads).
// A read-only host url is read from that host (team #1 only); any other url keeps today's path:
// the key parsed from the url, read from our bucket through GetManagedObject's segment gate.
func (b *Bucket) OpenStoredURL(ctx context.Context, rawURL string) (io.ReadCloser, int64, error) {
	if u, ok := b.Config.readOnlyURL(rawURL); ok {
		return b.openReadOnly(ctx, u)
	}
	key, err := ObjectKeyFromStoredURL(rawURL)
	if err != nil {
		return nil, 0, err
	}
	return b.GetManagedObject(ctx, key)
}

// OpenManagedURL is the host-checked reader (AI inline pictures): our own host by key, a read-only
// host over https (team #1 only), anything else refused before any network call.
func (b *Bucket) OpenManagedURL(ctx context.Context, rawURL string) (io.ReadCloser, int64, error) {
	if u, ok := b.Config.readOnlyURL(rawURL); ok {
		return b.openReadOnly(ctx, u)
	}
	key, err := ManagedObjectKeyFromURL(b.Config, rawURL)
	if err != nil {
		return nil, 0, err
	}
	return b.GetManagedObject(ctx, key)
}

// ManagedObjectGetter is the by-key reader every stored-url reader already holds.
type ManagedObjectGetter interface {
	GetManagedObject(ctx context.Context, objectKey string) (io.ReadCloser, int64, error)
}

type storedURLOpener interface {
	OpenStoredURL(ctx context.Context, rawURL string) (io.ReadCloser, int64, error)
}

// OpenStoredObject is OpenStoredURL for a caller that holds the bucket behind an interface. A reader
// that is not a *Bucket (a test fake) keeps today's path: key from the url, GetManagedObject.
func OpenStoredObject(ctx context.Context, objects ManagedObjectGetter, rawURL string) (io.ReadCloser, int64, error) {
	if o, ok := objects.(storedURLOpener); ok {
		return o.OpenStoredURL(ctx, rawURL)
	}
	key, err := ObjectKeyFromStoredURL(rawURL)
	if err != nil {
		return nil, 0, err
	}
	return objects.GetManagedObject(ctx, key)
}

// isAllowedReadOnlyKey: a read-only read takes the public objects an export needs (media, patterns)
// and nothing private: no files-library, archives or imports, no empty or dot segments.
func isAllowedReadOnlyKey(key string) bool {
	if key == "" {
		return false
	}
	for _, seg := range strings.Split(key, "/") {
		switch seg {
		case "", ".", "..", libraryFolder, ArchiveSegment, ImportSegment:
			return false
		}
	}
	return true
}

// legacyClient is the https client of read-only reads: the injected one (tests) or a fresh client on
// the shared default transport. Redirects are refused either way — a redirect would be a fetch of a
// host nobody configured.
func (b *Bucket) legacyClient() *http.Client {
	c := &http.Client{Timeout: 5 * time.Minute}
	if b.legacyHTTP != nil {
		cp := *b.legacyHTTP
		c = &cp
	}
	c.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return c
}

func (b *Bucket) openReadOnly(ctx context.Context, u *url.URL) (io.ReadCloser, int64, error) {
	if !b.Config.readOnlyHonoured(ctx) {
		return nil, 0, fmt.Errorf("%w: %s", errNotReadable, u.Host)
	}
	key := strings.Trim(u.Path, "/")
	if !isAllowedReadOnlyKey(key) {
		return nil, 0, fmt.Errorf("%w: %q on read-only host %s", ErrManagedKeyNotAllowed, key, u.Host)
	}
	target := (&url.URL{Scheme: "https", Host: u.Host, Path: "/" + key}).String()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return nil, 0, fmt.Errorf("read-only object %q: %w", key, err)
	}
	resp, err := b.legacyClient().Do(req)
	if err != nil {
		return nil, 0, fmt.Errorf("read-only object %q on %s: %w", key, u.Host, err)
	}
	if resp.StatusCode != http.StatusOK {
		resp.Body.Close()
		return nil, 0, fmt.Errorf("read-only object %q on %s: status %d", key, u.Host, resp.StatusCode)
	}
	if resp.ContentLength > legacyReadMaxBytes {
		resp.Body.Close()
		return nil, 0, fmt.Errorf("%w: read-only object %q is %d bytes", ErrArchiveObjectTooLarge, key, resp.ContentLength)
	}
	return readCloser{Reader: newCapReader(resp.Body, legacyReadMaxBytes), Closer: resp.Body}, resp.ContentLength, nil
}

type readCloser struct {
	io.Reader
	io.Closer
}
