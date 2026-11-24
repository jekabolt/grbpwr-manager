package bucket

import (
	"context"
	"log/slog"
	"net/url"
	"path"
	"strings"
	"sync"
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/tenant"
	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
)

// PRIVATE TEAM FILES (lane X13).
//
// In teams mode an object under {base}/t/{file_key}/… is written WITHOUT public-read (put drops the
// acl), so its bare url answers 403 to anyone without a signature. The members of the team read it
// through a signed url: the stored url with `?X-Amz-…` appended. The signature is virtual-host style
// on the bucket's origin host, so the signed url keeps the stored url's host and path, and the
// client's own host checks (direct fetch first, the proxy as the fallback) stay true. The api
// signs on the way out (httpapi's file url interceptor) and cuts the signature off on the way in
// (CanonicalFileURLs), so a row never stores a signature.
//
// Team #1 and teams mode off are untouched: their keys never sit under {base}/t/, their objects keep
// public-read, and nothing here signs them. A read-only host (another installation's CDN) is never
// one of the managed hosts below, so its urls are never signed or rewritten either.

// signedURLMemoCap bounds the memo of signed urls. Past it the memo starts over: the urls stay valid,
// only their string stability within the window is lost for the keys signed after the reset.
const signedURLMemoCap = 20000

// teamURLSigner is the bucket's lazily built virtual-host presigner plus the memo of its urls for the
// current window (presignWindow, the same 6 h grid as the pattern presign: valid 6 to 12 h, one
// stable string per key per window, so the browser cache and <object> embeds keep working).
type teamURLSigner struct {
	mu     sync.Mutex
	cli    *minio.Client
	window int64
	urls   map[string]string
}

// teamPrivateRoot is the key prefix every team other than #1 writes under: "{base}/t/".
func (c *Config) teamPrivateRoot() string {
	root := teamSegment + "/"
	if base := strings.Trim(strings.TrimSpace(c.BaseFolder), "/"); base != "" {
		root = base + "/" + root
	}
	return root
}

// isPrivateTeamKey: the key is a team's work file that must not be public (teams mode on, under
// {base}/t/). Team #1's legacy keys and every key with teams mode off are not.
func (c *Config) isPrivateTeamKey(key string) bool {
	if c == nil || !c.TeamsMode {
		return false
	}
	return strings.HasPrefix(strings.TrimLeft(key, "/"), c.teamPrivateRoot())
}

// withoutPublicACL is a copy of the put metadata with the canned acl removed (any spelling), so the
// object gets the bucket default: private. The caller's map is never changed.
func withoutPublicACL(meta map[string]string) map[string]string {
	if len(meta) == 0 {
		return meta
	}
	out := make(map[string]string, len(meta))
	for k, v := range meta {
		if strings.EqualFold(strings.TrimSpace(k), "x-amz-acl") {
			continue
		}
		out[k] = v
	}
	return out
}

// PrivateURLNeedle is the substring every url of the ctx team's private files carries:
// "/{base}/t/{file_key}/". ok is false when there is nothing to sign for this caller: teams mode off,
// no team, team #1, or a team without a valid file key. A cheap pre-check for whole responses.
func (b *Bucket) PrivateURLNeedle(ctx context.Context) (string, bool) {
	if b == nil || b.Config == nil || !b.Config.TeamsMode {
		return "", false
	}
	t, ok := tenant.From(ctx)
	if !ok || t.ID == tenant.TeamOneID || !validFileKey(t.FileKey) {
		return "", false
	}
	return "/" + b.Config.teamPrivateRoot() + t.FileKey + "/", true
}

// managedURLPrefixes are "https://{host}/" of the CDN host and the virtual-hosted origin, deduplicated,
// minus any host that is also configured read-only (never ours to rewrite).
func (c *Config) managedURLPrefixes() (cdn, origin string, prefixes []string) {
	cdn = configuredURLHost(c.SubdomainEndpoint)
	if ep := configuredURLHost(c.S3Endpoint); ep != "" && c.S3BucketName != "" {
		origin = strings.ToLower(c.S3BucketName + "." + ep)
	}
	for _, h := range []string{cdn, origin} {
		if h == "" || c.isReadOnlyHost(h) {
			continue
		}
		p := "https://" + h + "/"
		if len(prefixes) == 1 && prefixes[0] == p {
			continue
		}
		prefixes = append(prefixes, p)
	}
	return cdn, origin, prefixes
}

// SignFileURLs returns s with every bare url of the ctx team's private files replaced by a signed
// one; changed is false when nothing was signed. s may be a url or any text that embeds urls (a
// JSON snapshot, markdown). A url that already carries a query or a fragment is left alone, and so
// is every url of another team, of team #1, or of a host that is not this bucket's.
func (b *Bucket) SignFileURLs(ctx context.Context, s string) (string, bool) {
	needle, ok := b.PrivateURLNeedle(ctx)
	if !ok || !strings.Contains(s, needle) {
		return s, false
	}
	_, _, prefixes := b.Config.managedURLPrefixes()
	if len(prefixes) == 0 {
		return s, false
	}
	// One url filling the whole string (the usual field): it ends where the string ends.
	whole := !strings.ContainsAny(s, " \t\r\n\"'<>\\`")
	var out strings.Builder
	changed := false
	last, pos := 0, 0
	for {
		i, p := nextURLPrefix(s, pos, prefixes)
		if i < 0 {
			break
		}
		start := i + len(p)
		end := start
		if whole && i == 0 {
			end = len(s)
			if j := strings.IndexAny(s, "?#"); j >= 0 {
				end = j
			}
		} else {
			for end < len(s) && !isSignStop(s[end]) {
				end++
			}
		}
		pos = end
		if end < len(s) && (s[end] == '?' || s[end] == '#') {
			continue // already a query (a signature or someone's own): not ours to change
		}
		signed, ok := b.signOwnKey(ctx, s[start:end])
		if !ok {
			continue
		}
		out.WriteString(s[last:i])
		out.WriteString(signed)
		last = end
		changed = true
	}
	if !changed {
		return s, false
	}
	out.WriteString(s[last:])
	return out.String(), true
}

// SignFileURL is SignFileURLs for one url (the AI providers' fetch lists).
func (b *Bucket) SignFileURL(ctx context.Context, raw string) string {
	out, _ := b.SignFileURLs(ctx, raw)
	return out
}

// CanonicalFileURLs cuts the signature off every signed url of a private team file in s, so what
// the client sends back is the stored url again (rows never store a signature, and the write-side
// host checks keep matching). The host becomes the CDN host when the signature named the origin.
// Not team-scoped: it only ever removes our own signature query.
func (b *Bucket) CanonicalFileURLs(s string) (string, bool) {
	if b == nil || b.Config == nil || !b.Config.TeamsMode || !strings.Contains(s, "X-Amz-Signature=") {
		return s, false
	}
	cdn, origin, prefixes := b.Config.managedURLPrefixes()
	root := b.Config.teamPrivateRoot()
	var out strings.Builder
	changed := false
	last, pos := 0, 0
	for {
		i, p := nextURLPrefix(s, pos, prefixes)
		if i < 0 {
			break
		}
		end := i + len(p)
		for end < len(s) && !isCanonStop(s[end]) {
			end++
		}
		pos = end
		cand := s[i:end]
		q := strings.IndexByte(cand, '?')
		if q < 0 || !strings.HasPrefix(cand[len(p):], root) || !strings.Contains(cand[q:], "X-Amz-Signature=") {
			continue
		}
		bare := cand[:q]
		if origin != "" && cdn != "" && cdn != origin && strings.HasPrefix(bare, "https://"+origin+"/") {
			bare = "https://" + cdn + bare[len("https://"+origin):]
		}
		out.WriteString(s[last:i])
		out.WriteString(bare)
		last = end
		changed = true
	}
	if !changed {
		return s, false
	}
	out.WriteString(s[last:])
	return out.String(), true
}

// nextURLPrefix finds the earliest occurrence of any prefix in s at or after pos.
func nextURLPrefix(s string, pos int, prefixes []string) (int, string) {
	best, bestP := -1, ""
	for _, p := range prefixes {
		if j := strings.Index(s[pos:], p); j >= 0 && (best < 0 || pos+j < best) {
			best, bestP = pos+j, p
		}
	}
	return best, bestP
}

// isCanonStop ends a url embedded in text: whitespace, quotes and the brackets of markdown/JSON/HTML.
func isCanonStop(c byte) bool {
	switch c {
	case ' ', '\t', '\r', '\n', '"', '\'', '<', '>', '\\', '`', '(', ')', '[', ']', '{', '}', '|':
		return true
	}
	return false
}

// isSignStop ends the path of an embedded url: isCanonStop plus the start of a query or a fragment.
func isSignStop(c byte) bool {
	return c == '?' || c == '#' || isCanonStop(c)
}

// signOwnKey signs the object named by a url path (escaped as stored) when it is a private file of
// the ctx team. A path that does not decode, is not clean, or names another team's key is refused.
func (b *Bucket) signOwnKey(ctx context.Context, rawPath string) (string, bool) {
	key, err := url.PathUnescape(rawPath)
	if err != nil || key == "" || strings.HasSuffix(key, "/") || path.Clean(key) != key {
		return "", false
	}
	if !b.Config.isPrivateTeamKey(key) || !TeamOwnsKey(ctx, b.Config, key) {
		return "", false
	}
	signed, err := b.signedURLFor(ctx, key)
	if err != nil {
		slog.Default().WarnContext(ctx, "bucket: can't sign a team file url, it stays bare",
			slog.String("err", err.Error()))
		return "", false
	}
	return signed, true
}

// signedURLFor is the memoized virtual-host presigned GET of key for the current window.
func (b *Bucket) signedURLFor(ctx context.Context, key string) (string, error) {
	now := time.Now().UTC()
	windowStart := now.Truncate(presignWindow)
	expiresAt := windowStart.Add(2 * presignWindow)

	s := &b.signer
	s.mu.Lock()
	if s.window != windowStart.Unix() || s.urls == nil || len(s.urls) >= signedURLMemoCap {
		s.window = windowStart.Unix()
		s.urls = map[string]string{}
	}
	if u, ok := s.urls[key]; ok {
		s.mu.Unlock()
		return u, nil
	}
	cli, err := b.signerClientLocked(ctx)
	s.mu.Unlock()
	if err != nil {
		return "", err
	}
	u, err := cli.PresignedGetObject(ctx, b.S3BucketName, key, expiresAt.Sub(now), nil)
	if err != nil {
		return "", err
	}
	signed := u.String()
	s.mu.Lock()
	if s.window == windowStart.Unix() {
		if prev, ok := s.urls[key]; ok {
			signed = prev // a concurrent first signature won: serve one string per window
		} else {
			s.urls[key] = signed
		}
	}
	s.mu.Unlock()
	return signed, nil
}

// signerClientLocked builds (once) the presigner: the bucket's own credentials and endpoint with
// virtual-host lookup, in the region the main client resolves for the bucket. A failure is not
// remembered, so the next call tries again. Called with b.signer.mu held.
func (b *Bucket) signerClientLocked(ctx context.Context) (*minio.Client, error) {
	if b.signer.cli != nil {
		return b.signer.cli, nil
	}
	region := ""
	if b.Client != nil {
		r, err := b.Client.GetBucketLocation(ctx, b.S3BucketName)
		if err != nil {
			return nil, err
		}
		region = r
	}
	cli, err := minio.New(b.S3Endpoint, &minio.Options{
		Creds:        credentials.NewStaticV4(b.S3AccessKey, b.S3SecretAccessKey, ""),
		Secure:       true,
		Region:       region,
		BucketLookup: minio.BucketLookupDNS,
	})
	if err != nil {
		return nil, err
	}
	b.signer.cli = cli
	return cli, nil
}
