package bucket

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/tenant"
	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
	"github.com/stretchr/testify/require"
)

// The file proxy (lane V1) is proven by its refusals first: another team's key, a foreign host, http,
// an IP literal, a redirect to another host, a host resolving to a private address, an oversized
// body. Every refusal that can be decided before the network is checked to make no call at all.

const (
	fileKeyA = "0123456789abcdef0123456789abcdef"
	fileKeyB = "fedcba9876543210fedcba9876543210"
)

type proxyObj struct {
	body        []byte
	contentType string
}

// readS3 is a fake S3 that serves GET / HEAD of stored objects and records every call.
type readS3 struct {
	bucket   string
	mu       sync.Mutex
	objects  map[string]proxyObj
	requests []string
}

func (f *readS3) log() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.requests...)
}

func (f *readS3) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	key := strings.TrimPrefix(r.URL.Path, "/"+f.bucket+"/")
	f.mu.Lock()
	f.requests = append(f.requests, r.Method+" "+key)
	o, ok := f.objects[key]
	f.mu.Unlock()
	if !ok {
		w.Header().Set("Content-Type", "application/xml")
		w.WriteHeader(http.StatusNotFound)
		_, _ = io.WriteString(w, `<?xml version="1.0" encoding="UTF-8"?><Error><Code>NoSuchKey</Code><Message>missing</Message></Error>`)
		return
	}
	w.Header().Set("Content-Type", o.contentType)
	w.Header().Set("Content-Length", strconv.Itoa(len(o.body)))
	w.Header().Set("ETag", `"abc"`)
	w.Header().Set("Last-Modified", time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC).Format(http.TimeFormat))
	w.WriteHeader(http.StatusOK)
	if r.Method == http.MethodGet {
		_, _ = w.Write(o.body)
	}
}

func newProxyBucket(t *testing.T, teamsMode bool) (*Bucket, *readS3) {
	t.Helper()
	const bucketName = "probe-bucket"
	store := &readS3{bucket: bucketName, objects: map[string]proxyObj{}}
	srv := httptest.NewTLSServer(store)
	t.Cleanup(srv.Close)
	u, err := url.Parse(srv.URL)
	require.NoError(t, err)
	cli, err := minio.New(u.Host, &minio.Options{
		Creds:     credentials.NewStaticV4("probe", "probe12345", ""),
		Secure:    true,
		Transport: srv.Client().Transport,
		Region:    "us-east-1",
	})
	require.NoError(t, err)
	return &Bucket{Client: cli, Config: &Config{
		S3BucketName:      bucketName,
		S3Endpoint:        "s3.probe.test",
		BaseFolder:        "probe",
		SubdomainEndpoint: "cdn.probe.test",
		TeamsMode:         teamsMode,
	}}, store
}

func teamCtx(id int64, fileKey string) context.Context {
	return tenant.With(context.Background(), tenant.Team{ID: id, Slug: fmt.Sprintf("t%d", id), FileKey: fileKey})
}

func cdnURL(key string) string { return "https://cdn.probe.test/" + key }

func readAll(t *testing.T, o ProxyObject) []byte {
	t.Helper()
	defer o.Body.Close()
	b, err := io.ReadAll(o.Body)
	require.NoError(t, err)
	return b
}

func TestProxyServesOwnTeamFile(t *testing.T) {
	b, store := newProxyBucket(t, true)
	key := "probe/t/" + fileKeyA + "/tech-card-patterns/2026/july/p.dxf"
	store.objects[key] = proxyObj{body: []byte("0\nSECTION"), contentType: "application/dxf"}

	o, err := b.OpenProxyObject(teamCtx(2, fileKeyA), cdnURL(key))
	require.NoError(t, err)
	require.Equal(t, int64(9), o.Size)
	require.Equal(t, "application/dxf", o.ContentType)
	require.Equal(t, "0\nSECTION", string(readAll(t, o)))

	// The bucket-origin host and a presigned query are the same object.
	o, err = b.OpenProxyObject(teamCtx(2, fileKeyA), "https://probe-bucket.s3.probe.test/"+key+"?X-Amz-Signature=x")
	require.NoError(t, err)
	require.Equal(t, "0\nSECTION", string(readAll(t, o)))
}

func TestProxyRefusesAnotherTeamsKey(t *testing.T) {
	b, store := newProxyBucket(t, true)
	keyB := "probe/t/" + fileKeyB + "/2026/july/b.webp"
	legacy := "probe/2026/july/one.webp"
	store.objects[keyB] = proxyObj{body: []byte("b"), contentType: "image/webp"}
	store.objects[legacy] = proxyObj{body: []byte("1"), contentType: "image/webp"}

	for name, tc := range map[string]struct {
		ctx context.Context
		key string
	}{
		"team A reads team B":          {teamCtx(2, fileKeyA), keyB},
		"team A reads team #1":         {teamCtx(2, fileKeyA), legacy},
		"team #1 reads team B":         {teamCtx(tenant.TeamOneID, ""), keyB},
		"team without a file key":      {teamCtx(3, ""), "probe/t//2026/july/b.webp"},
		"team with a malformed key":    {teamCtx(3, "../.."), "probe/t/../../2026/b.webp"},
		"no team in teams mode":        {context.Background(), legacy},
		"dot segments out of a prefix": {teamCtx(2, fileKeyA), "probe/t/" + fileKeyA + "/../" + fileKeyB + "/2026/july/b.webp"},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := b.OpenProxyObject(tc.ctx, cdnURL(tc.key))
			require.ErrorIs(t, err, ErrProxyRefused)
		})
	}
	require.Empty(t, store.log(), "no refused key ever reached the bucket")

	// Team #1 still reads its own legacy key.
	o, err := b.OpenProxyObject(teamCtx(tenant.TeamOneID, ""), cdnURL(legacy))
	require.NoError(t, err)
	require.Equal(t, "1", string(readAll(t, o)))
}

func TestProxyRefusesPrivateSegments(t *testing.T) {
	b, store := newProxyBucket(t, false)
	for _, key := range []string{
		"probe/files-library/2026/july/secret.pdf",
		"probe/files-library/previews/2026/july/p.webp",
		ArchiveSegment + "/abc/techcard-1.zip",
		ImportSegment + "/abc/in.zip",
		"other-root/2026/july/x.webp",
	} {
		_, err := b.OpenProxyObject(context.Background(), cdnURL(key))
		require.ErrorIsf(t, err, ErrProxyRefused, "%s", key)
	}
	require.Empty(t, store.log())
}

func TestProxyRefusesForeignHostHTTPAndIPLiterals(t *testing.T) {
	b, store := newProxyBucket(t, false)
	for raw, want := range map[string]error{
		"https://attacker.example/probe/2026/july/a.webp":       ErrProxyRefused,
		"https://cdn.probe.test.attacker.example/probe/a.webp":  ErrProxyRefused,
		"http://cdn.probe.test/probe/2026/july/a.webp":          ErrProxyBadURL,
		"ftp://cdn.probe.test/probe/2026/july/a.webp":           ErrProxyBadURL,
		"https://127.0.0.1/probe/2026/july/a.webp":              ErrProxyBadURL,
		"https://169.254.169.254/latest/meta-data/":             ErrProxyBadURL,
		"https://10.0.0.5/probe/a.webp":                         ErrProxyBadURL,
		"https://[::1]/probe/a.webp":                            ErrProxyBadURL,
		"https://[fd00::1]:8443/probe/a.webp":                   ErrProxyBadURL,
		"https://cdn.probe.test@attacker.example/probe/a.webp":  ErrProxyBadURL,
		"https://user:pw@cdn.probe.test/probe/2026/july/a.webp": ErrProxyBadURL,
		"":                                     ErrProxyBadURL,
		"/probe/2026/a.webp":                   ErrProxyBadURL,
		"https:///probe/a.webp":                ErrProxyBadURL,
		"javascript:alert(1)":                  ErrProxyBadURL,
		"https://" + strings.Repeat("a", 5000): ErrProxyBadURL,
	} {
		_, err := b.OpenProxyObject(context.Background(), raw)
		require.ErrorIsf(t, err, want, "%q", raw)
	}
	require.Empty(t, store.log())
}

func TestProxyOversizedOwnObjectRefusedBeforeBody(t *testing.T) {
	b, store := newProxyBucket(t, false)
	b.proxyMax = 16
	key := "probe/2026/july/big.glb"
	store.objects[key] = proxyObj{body: []byte(strings.Repeat("x", 17)), contentType: "model/gltf-binary"}
	_, err := b.OpenProxyObject(context.Background(), cdnURL(key))
	require.ErrorIs(t, err, ErrProxyTooLarge)

	store.objects[key] = proxyObj{body: []byte(strings.Repeat("x", 16)), contentType: "model/gltf-binary"}
	o, err := b.OpenProxyObject(context.Background(), cdnURL(key))
	require.NoError(t, err)
	require.Len(t, readAll(t, o), 16, "exactly the cap passes")
}

func TestProxyMissingObjectIsNotFound(t *testing.T) {
	b, _ := newProxyBucket(t, false)
	_, err := b.OpenProxyObject(context.Background(), cdnURL("probe/2026/july/gone.webp"))
	require.ErrorIs(t, err, ErrProxyNotFound)
}

// --- read-only host ---

// proxyLegacy stands up the read-only host as "example.com" (the name in httptest's certificate):
// the injected client dials the test listener whatever the name resolves to.
func proxyLegacy(t *testing.T, teamsMode bool, h http.HandlerFunc) (*Bucket, *readS3, string, *atomic.Int32) {
	t.Helper()
	b, store := newProxyBucket(t, teamsMode)
	var hits atomic.Int32
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		h(w, r)
	}))
	t.Cleanup(srv.Close)
	cli := srv.Client()
	tr := cli.Transport.(*http.Transport).Clone()
	addr := srv.Listener.Addr().String()
	tr.DialContext = func(ctx context.Context, network, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, network, addr)
	}
	cli.Transport = tr
	b.Config.ReadOnlyHosts = "example.com"
	b.legacyHTTP = cli
	return b, store, "https://example.com", &hits
}

func TestProxyReadOnlyHostTeamOneOnly(t *testing.T) {
	b, store, origin, hits := proxyLegacy(t, true, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "image/webp")
		_, _ = io.WriteString(w, "legacy")
	})
	raw := origin + "/grbpwr-com/2024/may/old.webp"

	_, err := b.OpenProxyObject(teamCtx(2, fileKeyA), raw)
	require.ErrorIs(t, err, ErrProxyRefused)
	_, err = b.OpenProxyObject(context.Background(), raw)
	require.ErrorIs(t, err, ErrProxyRefused, "teams mode without a team")
	_, err = b.OpenProxyObject(teamCtx(tenant.TeamOneID, ""), origin+"/grbpwr-com/files-library/x.pdf")
	require.ErrorIs(t, err, ErrProxyRefused)
	require.Zero(t, hits.Load())
	require.Empty(t, store.log())

	o, err := b.OpenProxyObject(teamCtx(tenant.TeamOneID, ""), raw)
	require.NoError(t, err)
	require.Equal(t, "image/webp", o.ContentType)
	require.Equal(t, "legacy", string(readAll(t, o)))
	require.Equal(t, int32(1), hits.Load())
}

func TestProxyReadOnlyHostRedirectToForeignHostRefused(t *testing.T) {
	var foreignHits atomic.Int32
	foreign := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		foreignHits.Add(1)
		_, _ = io.WriteString(w, "foreign")
	}))
	t.Cleanup(foreign.Close)

	b, _, origin, hits := proxyLegacy(t, false, func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, foreign.URL+"/steal", http.StatusFound)
	})

	_, err := b.OpenProxyObject(context.Background(), origin+"/grbpwr-com/2024/may/old.webp")
	require.Error(t, err)
	require.Contains(t, err.Error(), "status 302")
	require.Equal(t, int32(1), hits.Load())
	require.Zero(t, foreignHits.Load(), "the redirect target was never fetched")
}

func TestProxyReadOnlyHostOversized(t *testing.T) {
	// Declared length over the cap: refused before the body.
	b, _, origin, _ := proxyLegacy(t, false, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, strings.Repeat("y", 9))
	})
	b.proxyMax = 8
	_, err := b.OpenProxyObject(context.Background(), origin+"/grbpwr-com/2024/may/big.webp")
	require.ErrorIs(t, err, ErrProxyTooLarge)

	// No length (chunked): the stream stops with ErrProxyTooLarge past the cap.
	b, _, origin, _ = proxyLegacy(t, false, func(w http.ResponseWriter, _ *http.Request) {
		for i := 0; i < 3; i++ {
			_, _ = io.WriteString(w, "zzzz")
			w.(http.Flusher).Flush()
		}
	})
	b.proxyMax = 8
	o, err := b.OpenProxyObject(context.Background(), origin+"/grbpwr-com/2024/may/big.webp")
	require.NoError(t, err)
	require.Equal(t, int64(-1), o.Size)
	_, err = io.ReadAll(o.Body)
	o.Body.Close()
	require.ErrorIs(t, err, ErrProxyTooLarge)
}

// Without an injected client the proxy dials through publicOnlyTransport: a read-only host whose name
// resolves to a loopback address is refused at the dial, and the server never sees a request.
func TestProxyReadOnlyHostResolvingToPrivateAddressRefused(t *testing.T) {
	b, store := newProxyBucket(t, false)
	var hits atomic.Int32
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		_, _ = io.WriteString(w, "internal")
	}))
	t.Cleanup(srv.Close)
	host := strings.Replace(strings.TrimPrefix(srv.URL, "https://"), "127.0.0.1", "localhost", 1)
	b.Config.ReadOnlyHosts = host // a NAME that resolves to loopback; legacyHTTP nil = the production client

	_, err := b.OpenProxyObject(context.Background(), "https://"+host+"/grbpwr-com/2024/may/old.webp")
	require.Error(t, err)
	require.True(t, errors.Is(err, errNonPublicAddress), "%v", err)
	require.Zero(t, hits.Load())
	require.Empty(t, store.log())
}

func TestRefuseNonPublicDial(t *testing.T) {
	for _, addr := range []string{
		"127.0.0.1:443", "[::1]:443", "10.1.2.3:443", "172.16.0.1:443", "192.168.1.1:443",
		"169.254.169.254:80", "[fe80::1]:443", "[fd00::1]:443", "0.0.0.0:443", "100.64.0.1:443",
		"[::ffff:127.0.0.1]:443", "224.0.0.1:443", "garbage",
	} {
		require.ErrorIsf(t, refuseNonPublicDial("tcp", addr, nil), errNonPublicAddress, "%s", addr)
	}
	for _, addr := range []string{"104.16.1.1:443", "[2606:4700::1]:443"} {
		require.NoErrorf(t, refuseNonPublicDial("tcp", addr, nil), "%s", addr)
	}
}

func TestTeamOwnsKeyModeOff(t *testing.T) {
	c := &Config{BaseFolder: "probe"}
	require.True(t, TeamOwnsKey(context.Background(), c, "probe/2026/a.webp"), "mode off: today's behaviour")
	require.True(t, TeamOwnsKey(teamCtx(2, ""), c, "probe/t/x/a.webp"))
	c.TeamsMode = true
	require.False(t, TeamOwnsKey(context.Background(), c, "probe/2026/a.webp"))
	require.True(t, TeamOwnsKey(teamCtx(2, fileKeyA), c, "probe/t/"+fileKeyA+"/a.webp"))
	require.False(t, TeamOwnsKey(teamCtx(2, fileKeyA), c, "probe/t/"+fileKeyA))
	require.False(t, TeamOwnsKey(context.Background(), nil, "x"))
}
