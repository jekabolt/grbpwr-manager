package bucket

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
	"syscall"
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/tenant"
	"github.com/minio/minio-go/v7"
)

// AUTHENTICATED FILE PROXY (teams wave, lane V1).
//
// The admin client needs the bytes of a file it already holds a url for (DXF quick view, marker
// layout, crop, 3D model), and the CDN serves those objects without CORS headers. On Vercel a
// serverless function fetched any url for it; the lekalo static site has no functions, so the
// backend does it — but never as an open proxy. OpenProxyObject is the only decision:
//
//   - the url is https, has no userinfo, and its host is not an IP literal;
//   - our own host (CDN subdomain / bucket origin): the object is read BY KEY from our bucket with
//     our credentials — no request ever goes to the host written in the url, so there is nothing to
//     redirect and no address to aim at; the key must sit in a folder on proxyFolders (an allow-list,
//     not a deny-list) and, in teams mode, inside the caller's team prefix;
//   - a read-only host (BUCKET_S3_READONLY_HOSTS, team #1 / mode off only): an https GET to that
//     configured host, redirects refused, the dial refused when the name resolves to a loopback /
//     private / link-local address;
//   - anything else is refused before any network call.
//
// The size is capped (MaxProxyObjectBytes) before the first byte and again while streaming.

// MaxProxyObjectBytes caps one proxied object: the largest media payload accepted on upload (a 3D
// model, maxModelPayloadBytes); video is 50 MiB, a pattern 40 MiB.
const MaxProxyObjectBytes int64 = maxModelPayloadBytes

// proxyLimit is the cap in force (tests lower it to prove the refusal without 64 MiB bodies).
func (b *Bucket) proxyLimit() int64 {
	if b.proxyMax > 0 {
		return b.proxyMax
	}
	return MaxProxyObjectBytes
}

var (
	// ErrProxyRefused: the url is not a file this caller may read through the proxy.
	ErrProxyRefused = errors.New("bucket: url is not a managed file of this team")
	// ErrProxyBadURL: the url is malformed, not https, carries userinfo or an IP literal host.
	ErrProxyBadURL = errors.New("bucket: url must be a plain https url on a managed host")
	// ErrProxyTooLarge: the object is larger than MaxProxyObjectBytes.
	ErrProxyTooLarge = errors.New("bucket: object exceeds the proxy size cap")
	// ErrProxyNotFound: the object does not exist.
	ErrProxyNotFound = errors.New("bucket: object not found")
)

// ProxyObject is an opened object: Body must be closed. Size is -1 when unknown (a read-only host
// that sent no Content-Length); Body then stops with ErrProxyTooLarge past the cap.
type ProxyObject struct {
	Body        io.ReadCloser
	Size        int64
	ContentType string
}

// teamSegment is the folder under BUCKET_BASE_FOLDER that holds the files of teams other than #1:
// {base}/t/{file_key}/{folder}/... (02-DESIGN §11). Team #1 keeps the legacy prefix.
const teamSegment = "t"

// TeamOwnsKey answers «does object key belong to the team in ctx» for an object of OUR bucket.
// Teams mode off: every key is team #1's (true). Teams mode on: no team in ctx → false; team #1 →
// any key outside {base}/t/; another team → only keys under {base}/t/{file_key}/, and a team with
// no (or a malformed) file_key owns nothing. One definition for every per-team reader.
func TeamOwnsKey(ctx context.Context, c *Config, key string) bool {
	if c == nil {
		return false
	}
	if !c.TeamsMode {
		return true
	}
	t, ok := tenant.From(ctx)
	if !ok {
		return false
	}
	key = strings.Trim(key, "/")
	root := teamSegment + "/"
	if base := strings.Trim(strings.TrimSpace(c.BaseFolder), "/"); base != "" {
		root = base + "/" + root
	}
	if t.ID == tenant.TeamOneID {
		return !strings.HasPrefix(key, root)
	}
	if !validFileKey(t.FileKey) {
		return false
	}
	return strings.HasPrefix(key, root+t.FileKey+"/")
}

// validFileKey: a team file key is 32 lower-case hex characters (16 random bytes).
func validFileKey(k string) bool {
	if len(k) != 32 {
		return false
	}
	for _, r := range k {
		if (r < '0' || r > '9') && (r < 'a' || r > 'f') {
			return false
		}
	}
	return true
}

// designFolder is where AI design media is written (designgen.designMediaFolder, and the band
// render in apisrv/admin/design_band.go).
const designFolder = "design"

// proxyDeniedSegments may not appear ANYWHERE in a proxied key, even under an allowed folder: the
// files library (its own capped reader), shipping labels, tech-card archives and imports. These are
// private objects handed out as short-lived signed links behind their own permissions; the proxy
// reads with OUR credentials, so letting one through would turn an expired signature or a missing
// permission into a read.
func proxyDeniedSegment(segment string) bool {
	switch segment {
	case libraryFolder, labelFolder, ArchiveSegment, ImportSegment:
		return true
	}
	return false
}

// proxyKeyAllowed is the allow-list: the key is {base}/{folder}/… for team #1 (and mode off) or
// {base}/t/{file_key}/{folder}/… for another team, where {folder} is one the SPA really reads through
// the proxy: card media (the base folder itself — constructFullPath is called with it as folder), AI
// design media, pattern files (DXF quick view, markers). Everything else — an unknown folder, a key
// straight under the base, a denied segment anywhere, an empty or dot segment — is refused. Which
// team the prefix belongs to is TeamOwnsKey's question, asked after this one.
func (b *Bucket) proxyKeyAllowed(key string) bool {
	base := strings.Trim(strings.TrimSpace(b.BaseFolder), "/")
	segs := strings.Split(key, "/")
	for _, s := range segs {
		if s == "" || s == "." || s == ".." || proxyDeniedSegment(s) {
			return false
		}
	}
	rest := segs
	if base != "" {
		if rest[0] != base {
			return false
		}
		rest = rest[1:]
	}
	if len(rest) > 0 && rest[0] == teamSegment {
		if len(rest) < 2 {
			return false
		}
		rest = rest[2:]
	}
	if len(rest) < 2 { // a folder and at least an object name
		return false
	}
	switch rest[0] {
	case designFolder, patternFolder:
		return true
	case base:
		return base != ""
	}
	return false
}

// parseProxyURL is the first gate: https, a host that is a name (not an IP literal), no userinfo.
func parseProxyURL(raw string) (*url.URL, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" || len(raw) > 4096 {
		return nil, ErrProxyBadURL
	}
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.Opaque != "" {
		return nil, ErrProxyBadURL
	}
	host := u.Hostname()
	if host == "" {
		return nil, ErrProxyBadURL
	}
	if _, err := netip.ParseAddr(strings.Trim(host, "[]")); err == nil {
		return nil, ErrProxyBadURL
	}
	return u, nil
}

// OpenProxyObject opens the file behind rawURL for the caller in ctx, or refuses (see the comment at
// the top of this file).
func (b *Bucket) OpenProxyObject(ctx context.Context, rawURL string) (ProxyObject, error) {
	u, err := parseProxyURL(rawURL)
	if err != nil {
		return ProxyObject{}, err
	}
	if ro, ok := b.Config.readOnlyURL(u.String()); ok {
		return b.openProxyReadOnly(ctx, ro)
	}
	key, err := ManagedObjectKeyFromURL(b.Config, u.String())
	if err != nil {
		return ProxyObject{}, fmt.Errorf("%w: %v", ErrProxyRefused, err)
	}
	key = strings.Trim(key, "/")
	if !b.proxyKeyAllowed(key) {
		return ProxyObject{}, fmt.Errorf("%w: key %q is not in a proxied folder", ErrProxyRefused, key)
	}
	if !TeamOwnsKey(ctx, b.Config, key) {
		return ProxyObject{}, fmt.Errorf("%w: key %q is outside the team's prefix", ErrProxyRefused, key)
	}
	obj, err := b.Client.GetObject(ctx, b.S3BucketName, key, minio.GetObjectOptions{})
	if err != nil {
		return ProxyObject{}, fmt.Errorf("proxy object %q: %w", key, err)
	}
	info, err := obj.Stat()
	if err != nil {
		obj.Close()
		if minio.ToErrorResponse(err).Code == "NoSuchKey" || minio.ToErrorResponse(err).StatusCode == http.StatusNotFound {
			return ProxyObject{}, fmt.Errorf("%w: %q", ErrProxyNotFound, key)
		}
		return ProxyObject{}, fmt.Errorf("stat proxy object %q: %w", key, err)
	}
	if info.Size > b.proxyLimit() {
		obj.Close()
		return ProxyObject{}, fmt.Errorf("%w: %q is %d bytes", ErrProxyTooLarge, key, info.Size)
	}
	return ProxyObject{
		Body:        readCloser{Reader: newProxyCapReader(obj, b.proxyLimit()), Closer: obj},
		Size:        info.Size,
		ContentType: info.ContentType,
	}, nil
}

func (b *Bucket) openProxyReadOnly(ctx context.Context, u *url.URL) (ProxyObject, error) {
	if !b.Config.readOnlyHonoured(ctx) {
		return ProxyObject{}, fmt.Errorf("%w: read-only host %s", ErrProxyRefused, u.Host)
	}
	key := strings.Trim(u.Path, "/")
	if !isAllowedReadOnlyKey(key) {
		return ProxyObject{}, fmt.Errorf("%w: %q on read-only host %s", ErrProxyRefused, key, u.Host)
	}
	target := (&url.URL{Scheme: "https", Host: u.Host, Path: "/" + key}).String()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return ProxyObject{}, fmt.Errorf("proxy read-only object %q: %w", key, err)
	}
	resp, err := b.proxyClient().Do(req)
	if err != nil {
		return ProxyObject{}, fmt.Errorf("proxy read-only object %q on %s: %w", key, u.Host, err)
	}
	switch {
	case resp.StatusCode == http.StatusNotFound || resp.StatusCode == http.StatusForbidden:
		resp.Body.Close()
		return ProxyObject{}, fmt.Errorf("%w: %q on %s (status %d)", ErrProxyNotFound, key, u.Host, resp.StatusCode)
	case resp.StatusCode != http.StatusOK:
		// A 3xx lands here too: the client never follows a redirect.
		resp.Body.Close()
		return ProxyObject{}, fmt.Errorf("proxy read-only object %q on %s: status %d", key, u.Host, resp.StatusCode)
	}
	if resp.ContentLength > b.proxyLimit() {
		resp.Body.Close()
		return ProxyObject{}, fmt.Errorf("%w: %q is %d bytes", ErrProxyTooLarge, key, resp.ContentLength)
	}
	return ProxyObject{
		Body:        readCloser{Reader: newProxyCapReader(resp.Body, b.proxyLimit()), Closer: resp.Body},
		Size:        resp.ContentLength,
		ContentType: resp.Header.Get("Content-Type"),
	}, nil
}

// proxyClient is the ONE client of read-only proxy reads, built on first use and kept on the Bucket:
// a transport per request would hold its idle connections until IdleConnTimeout, so repeated small
// reads would pile up file descriptors. Redirects are refused (a redirect would be a fetch of a host
// nobody configured) and, on the real transport, a dial to a non-public address is refused. Tests
// inject legacyHTTP (a loopback TLS server), which keeps the redirect rule; the dial guard is proven
// on its own.
func (b *Bucket) proxyClient() *http.Client {
	b.proxyOnce.Do(func() {
		c := &http.Client{Timeout: 2 * time.Minute, Transport: publicOnlyTransport()}
		if b.legacyHTTP != nil {
			cp := *b.legacyHTTP
			c = &cp
		}
		c.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
		b.proxyHTTP = c
	})
	return b.proxyHTTP
}

// publicOnlyTransport is the default transport with a dialer that refuses loopback, private,
// link-local, multicast and unspecified addresses AFTER name resolution (so a DNS answer cannot
// point the proxy inside the network), refuses any proxy from the environment, and caps the
// connections per host (a burst of reads queues instead of opening sockets without bound).
func publicOnlyTransport() *http.Transport {
	t := http.DefaultTransport.(*http.Transport).Clone()
	t.Proxy = nil
	d := &net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second, Control: refuseNonPublicDial}
	t.DialContext = d.DialContext
	t.MaxConnsPerHost = 16
	t.MaxIdleConnsPerHost = 4
	t.MaxIdleConns = 16
	t.IdleConnTimeout = 90 * time.Second
	return t
}

// errNonPublicAddress marks a dial refused by refuseNonPublicDial.
var errNonPublicAddress = errors.New("bucket: refusing to dial a non-public address")

// refuseNonPublicDial is a net.Dialer Control: it sees the resolved ip:port of every dial.
func refuseNonPublicDial(_, address string, _ syscall.RawConn) error {
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return fmt.Errorf("%w: %q", errNonPublicAddress, address)
	}
	ip, err := netip.ParseAddr(host)
	if err != nil || !publicAddr(ip.Unmap()) {
		return fmt.Errorf("%w: %s", errNonPublicAddress, host)
	}
	return nil
}

func publicAddr(ip netip.Addr) bool {
	return ip.IsValid() && !ip.IsLoopback() && !ip.IsPrivate() && !ip.IsLinkLocalUnicast() &&
		!ip.IsLinkLocalMulticast() && !ip.IsInterfaceLocalMulticast() && !ip.IsMulticast() &&
		!ip.IsUnspecified() && !carrierGradeNAT(ip)
}

// carrierGradeNAT is 100.64.0.0/10 (RFC 6598, shared address space; not covered by IsPrivate).
func carrierGradeNAT(ip netip.Addr) bool {
	if !ip.Is4() {
		return false
	}
	a := ip.As4()
	return a[0] == 100 && a[1]&0xC0 == 64
}

// proxyCapReader stops a stream that runs past MaxProxyObjectBytes with ErrProxyTooLarge.
type proxyCapReader struct {
	r     io.Reader
	limit int64
	left  int64
	err   error
}

func newProxyCapReader(r io.Reader, limit int64) *proxyCapReader {
	return &proxyCapReader{r: r, limit: limit, left: limit}
}

func (c *proxyCapReader) Read(p []byte) (int, error) {
	if c.err != nil {
		return 0, c.err
	}
	if int64(len(p)) > c.left+1 {
		p = p[:c.left+1]
	}
	n, err := c.r.Read(p)
	c.left -= int64(n)
	if c.left < 0 {
		c.err = fmt.Errorf("%w: over %d bytes", ErrProxyTooLarge, c.limit)
		return 0, c.err
	}
	return n, err
}
