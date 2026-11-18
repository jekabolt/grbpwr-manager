package bucket

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/jekabolt/grbpwr-manager/internal/tenant"
	"github.com/stretchr/testify/require"
)

// The legacy grbpwr file host is admitted as a READ-ONLY managed host (teams wave, lane B8). The
// tests below prove the refusals first: a delete, an overwrite question, a cleanup pass and a read by
// a team other than #1 never reach the read-only host — and never reach our own bucket either.

const legacyHost = "files.grbpwr.com"

// captureWarn swaps the default logger for the duration of the test and returns what was logged.
func captureWarn(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&lockedWriter{w: &buf}, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(prev) })
	return &buf
}

type lockedWriter struct {
	mu sync.Mutex
	w  io.Writer
}

func (l *lockedWriter) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.w.Write(p)
}

func TestDeleteObjectsRefusesReadOnlyHostBeforeNetwork(t *testing.T) {
	logs := captureWarn(t)
	b, store, _ := newProbeBucket(t)
	b.Config.ReadOnlyHosts = "https://" + legacyHost

	own := "probe/2026/july/own-og.webp"
	store.objects[own] = []byte("own")
	legacyURL := "https://" + legacyHost + "/grbpwr-com/2024/may/old-og.webp"

	err := b.DeleteObjects(context.Background(), "https://cdn.probe.test/"+own, legacyURL)
	require.NoError(t, err, "a read-only url is not ours: the caller continues as if it were never there")

	require.Equal(t, []string{"DELETE " + own}, store.requestLog(), "exactly one S3 call, for our own object")
	out := logs.String()
	require.Contains(t, out, "refusing to delete: read-only host")
	require.Contains(t, out, legacyHost)
	require.Contains(t, out, "grbpwr-com/2024/may/old-og.webp")
	require.Contains(t, out, "level=WARN")
}

// A read-only host that is ALSO one of our writable hosts would let uploads land on it: boot is
// refused, naming the variable and the host. Both our CDN host and our bucket-origin host count.
func TestReadOnlyHostOverlappingManagedHostRefusesBoot(t *testing.T) {
	for ro, host := range map[string]string{
		"cdn.probe.test":                   "cdn.probe.test",
		"https://CDN.probe.test/":          "cdn.probe.test",
		"probe.s3.probe.test":              "probe.s3.probe.test",
		"files.grbpwr.com, cdn.probe.test": "cdn.probe.test",
	} {
		c := &Config{SubdomainEndpoint: "cdn.probe.test", S3Endpoint: "s3.probe.test", S3BucketName: "probe", ReadOnlyHosts: ro}
		err := ValidateReadOnly(c)
		require.Errorf(t, err, "%q", ro)
		require.Contains(t, err.Error(), "BUCKET_S3_READONLY_HOSTS")
		require.Contains(t, err.Error(), host)
		_, err = New(c, nil)
		require.Error(t, err, "bucket.New refuses it before any network call")
	}
	require.NoError(t, ValidateReadOnly(&Config{SubdomainEndpoint: "cdn.probe.test", S3Endpoint: "s3.probe.test", S3BucketName: "probe", ReadOnlyHosts: legacyHost}))
}

// BUCKET_S3_READONLY_BUCKETS guards the case where the read-only CDN host differs from the S3 host:
// our write endpoint + bucket must not be one of the read-only buckets.
func TestReadOnlyBucketMatchingWriteBucketRefusesBoot(t *testing.T) {
	c := &Config{SubdomainEndpoint: "cdn.lekalo.test", S3Endpoint: "fra1.digitaloceanspaces.com", S3BucketName: "grbpwr",
		ReadOnlyHosts: legacyHost, ReadOnlyBuckets: "ams3.digitaloceanspaces.com/other, https://FRA1.digitaloceanspaces.com/GRBPWR"}
	err := ValidateReadOnly(c)
	require.Error(t, err)
	require.Contains(t, err.Error(), "BUCKET_S3_READONLY_BUCKETS")
	require.Contains(t, err.Error(), "fra1.digitaloceanspaces.com/grbpwr")

	c.S3BucketName = "lekalo"
	require.NoError(t, ValidateReadOnly(c), "another bucket on the same endpoint is fine")
	c.ReadOnlyBuckets = "fra1.digitaloceanspaces.com"
	require.Error(t, ValidateReadOnly(c), "an entry without a bucket is malformed")
	c.ReadOnlyBuckets = ""
	require.NoError(t, ValidateReadOnly(c), "unset = no check")
	require.NoError(t, ValidateReadOnly(&Config{SubdomainEndpoint: "cdn.lekalo.test"}), "mode OFF: nothing required")
}

// The change question (delete / overwrite / move) is answered by ManagedObjectKeyFromURL: a url on a
// read-only host has no changeable key, in every team and in both modes.
func TestChangeQuestionRefusesReadOnlyHost(t *testing.T) {
	c := &Config{SubdomainEndpoint: "cdn.lekalo.test", ReadOnlyHosts: legacyHost}
	legacyURL := "https://" + legacyHost + "/grbpwr-com/2024/may/a.webp"

	_, err := ManagedObjectKeyFromURL(c, legacyURL)
	require.Error(t, err)
	require.False(t, URLChangeable(c, legacyURL))
	require.True(t, URLChangeable(c, "https://cdn.lekalo.test/lk/2026/a.webp"))

	ctx := tenant.With(context.Background(), tenant.Team{ID: tenant.TeamOneID})
	require.True(t, URLReadable(ctx, c, legacyURL), "team #1 may read it")
	require.True(t, URLReadable(ctx, c, "https://cdn.lekalo.test/lk/2026/a.webp"))
	require.False(t, URLReadable(ctx, c, "https://attacker.example/a.webp"))
}

// The cleanup passes (orphaned pattern objects, media row removal, failed-upload compensation) all
// delete through DeleteObjects. A pass over a mix of our urls and copied legacy urls deletes ours,
// refuses the legacy ones without a network call and reports success, so the caller drops its rows.
func TestCleanupPassSkipsReadOnlyObjects(t *testing.T) {
	captureWarn(t)
	b, store, _ := newProbeBucket(t)
	b.Config.ReadOnlyHosts = "files.grbpwr.com, https://files2.grbpwr.com/"

	urls := []string{
		"https://files.grbpwr.com/grbpwr-com/tech-card-patterns/2024/may/p-1.pdf",
		"https://cdn.probe.test/probe/tech-card-patterns/2026/july/p-2.pdf",
		"https://files2.grbpwr.com/grbpwr-com/tech-card-patterns/2024/may/p-3.pdf",
	}
	require.NoError(t, b.DeleteObjects(context.Background(), urls...))
	require.Equal(t, []string{"DELETE probe/tech-card-patterns/2026/july/p-2.pdf"}, store.requestLog())
}

// legacyServer stands in for the old CDN: it serves any path and counts hits.
func legacyServer(t *testing.T, body string) (*httptest.Server, *atomic.Int32) {
	t.Helper()
	var hits atomic.Int32
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		if r.Method != http.MethodGet {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		_, _ = io.WriteString(w, body)
	}))
	t.Cleanup(srv.Close)
	return srv, &hits
}

func legacyBucket(t *testing.T, teamsMode bool) (*Bucket, *fakeS3, string, *atomic.Int32) {
	t.Helper()
	b, store, _ := newProbeBucket(t)
	srv, hits := legacyServer(t, "legacy-bytes")
	u, err := url.Parse(srv.URL)
	require.NoError(t, err)
	b.Config.ReadOnlyHosts = u.Host
	b.Config.TeamsMode = teamsMode
	b.legacyHTTP = srv.Client()
	return b, store, "https://" + u.Host + "/grbpwr-com/2024/may/old-og.webp", hits
}

func TestReadOnlyHostRefusedForOtherTeam(t *testing.T) {
	b, store, legacyURL, hits := legacyBucket(t, true)
	ctx := tenant.With(context.Background(), tenant.Team{ID: 2, DBName: "lkb_t_abcdefgh", Slug: "other"})

	_, _, err := b.OpenStoredURL(ctx, legacyURL)
	require.Error(t, err)
	_, _, err = b.OpenManagedURL(ctx, legacyURL)
	require.Error(t, err)
	_, _, err = OpenStoredObject(ctx, b, legacyURL)
	require.Error(t, err)
	require.False(t, URLReadable(ctx, b.Config, legacyURL))
	require.Zero(t, hits.Load(), "the read-only host was never asked")
	require.Empty(t, store.requestLog(), "nor was our own bucket asked for a foreign key")
}

func TestReadOnlyHostRefusedWithoutTeamInTeamsMode(t *testing.T) {
	b, store, legacyURL, hits := legacyBucket(t, true)
	_, _, err := b.OpenStoredURL(context.Background(), legacyURL)
	require.Error(t, err)
	require.Zero(t, hits.Load())
	require.Empty(t, store.requestLog())
}

func TestReadOnlyHostReadByTeamOne(t *testing.T) {
	for name, tc := range map[string]struct {
		teamsMode bool
		ctx       context.Context
	}{
		"teams mode, team 1": {true, tenant.With(context.Background(), tenant.Team{ID: tenant.TeamOneID})},
		"mode off, team 1":   {false, tenant.With(context.Background(), tenant.Team{ID: tenant.TeamOneID})},
		"mode off, no team":  {false, context.Background()},
	} {
		t.Run(name, func(t *testing.T) {
			b, store, legacyURL, hits := legacyBucket(t, tc.teamsMode)
			for _, open := range []func(context.Context, string) (io.ReadCloser, int64, error){
				b.OpenStoredURL, b.OpenManagedURL,
				func(ctx context.Context, raw string) (io.ReadCloser, int64, error) {
					return OpenStoredObject(ctx, b, raw)
				},
			} {
				rc, size, err := open(tc.ctx, legacyURL)
				require.NoError(t, err)
				got, err := io.ReadAll(rc)
				rc.Close()
				require.NoError(t, err)
				require.Equal(t, "legacy-bytes", string(got))
				require.Equal(t, int64(len("legacy-bytes")), size)
			}
			require.Equal(t, int32(3), hits.Load())
			require.Empty(t, store.requestLog(), "a legacy read never touches our bucket")
		})
	}
}

// A read-only read refuses keys no export needs: the private library, archives and imports, and any
// dot segment.
func TestReadOnlyReadRefusesPrivateAndDotKeys(t *testing.T) {
	b, _, legacyURL, hits := legacyBucket(t, false)
	base := strings.TrimSuffix(legacyURL, "/grbpwr-com/2024/may/old-og.webp")
	for _, p := range []string{
		"/grbpwr-com/files-library/x.pdf",
		"/techcard-archives/x.zip",
		"/grbpwr-com/../files-library/x.pdf",
		"/",
	} {
		_, _, err := b.OpenStoredURL(context.Background(), base+p)
		require.Errorf(t, err, "path %q", p)
	}
	require.Zero(t, hits.Load())
}

// Empty list = today's path: a url on any other host is read by key from our own bucket, and the
// legacy client is never used.
func TestEmptyReadOnlyListKeepsTodaysReadPath(t *testing.T) {
	b, store, legacyURL, hits := legacyBucket(t, false)
	b.Config.ReadOnlyHosts = ""
	// A key under our base folder, so the segment gate lets today's path reach the bucket.
	sameKeyURL := strings.Replace(legacyURL, "/grbpwr-com/", "/probe/", 1)
	_, _, _ = OpenStoredObject(context.Background(), b, sameKeyURL)
	require.Zero(t, hits.Load())
	require.NotEmpty(t, store.requestLog(), "the read went to our bucket by key, as before")
}

func TestReadOnlyHostsParsing(t *testing.T) {
	c := &Config{ReadOnlyHosts: " https://Files.Grbpwr.com/ , files2.example ,, "}
	require.Equal(t, []string{"files.grbpwr.com", "files2.example"}, ReadOnlyHosts(c))
	require.NoError(t, ValidateReadOnlyHosts(c.ReadOnlyHosts))
	require.NoError(t, ValidateReadOnlyHosts(""))
	for _, bad := range []string{"http://files.grbpwr.com", "https://files.grbpwr.com/base", "https://u@files.grbpwr.com", "https://"} {
		require.Errorf(t, ValidateReadOnlyHosts(bad), "%q", bad)
	}
	require.Empty(t, ReadOnlyHosts(&Config{}))
	require.Empty(t, ReadOnlyHosts(nil))
}

// A copied pattern row records the read-only host it came from; its link is built from that host
// alone. The same key also sits in our bucket with other bytes: it is never looked at — no
// StatObject, no request of any kind — and a row from the second configured host links to the
// second host, not the first.
func TestReadOnlyPatternURLUsesRecordedHostWithoutBucketCall(t *testing.T) {
	b, store, _ := newProbeBucket(t)
	b.Config.ReadOnlyHosts = legacyHost + ", files2.grbpwr.com"
	b.Config.TeamsMode = true
	team1 := tenant.With(context.Background(), tenant.Team{ID: tenant.TeamOneID})
	other := tenant.With(context.Background(), tenant.Team{ID: 2})

	key := "grbpwr-com/tech-card-patterns/2024/may/p.pdf"
	store.objects[key] = []byte("%PDF- our different bytes")

	host, ok := b.PatternObjectSource(team1, "https://FILES2.grbpwr.com/"+key)
	require.True(t, ok)
	require.Equal(t, "files2.grbpwr.com", host)
	host, ok = b.PatternObjectSource(team1, "https://cdn.probe.test/"+key)
	require.True(t, ok)
	require.Empty(t, host, "our own host: our bucket")
	_, ok = b.PatternObjectSource(other, "https://"+legacyHost+"/"+key)
	require.False(t, ok, "another team: not one of its pattern objects")

	got, _, err := b.ReadOnlyPatternURL(team1, "files2.grbpwr.com", key)
	require.NoError(t, err)
	require.Equal(t, "https://files2.grbpwr.com/"+key, got)
	got, _, err = b.ReadOnlyPatternURL(team1, legacyHost, key)
	require.NoError(t, err)
	require.Equal(t, "https://"+legacyHost+"/"+key, got)

	_, _, err = b.ReadOnlyPatternURL(other, legacyHost, key)
	require.Error(t, err)
	_, _, err = b.ReadOnlyPatternURL(team1, "attacker.example", key)
	require.Error(t, err, "a host that is no longer configured is refused")
	_, _, err = b.ReadOnlyPatternURL(team1, legacyHost, "grbpwr-com/files-library/x.pdf")
	require.Error(t, err)

	// Our own pattern object: today's presign, no probe either.
	got, _, err = b.PresignPatternObject(team1, key, false, "")
	require.NoError(t, err)
	require.Contains(t, got, "X-Amz-Signature")
	require.NotContains(t, got, legacyHost)

	require.Empty(t, store.requestLog(), "no bucket request at all")
}

// Empty list: every pattern url is our bucket's, no call of any kind.
func TestPatternObjectSourceEmptyListIsTodays(t *testing.T) {
	b, store, _ := newProbeBucket(t)
	host, ok := b.PatternObjectSource(context.Background(), "https://"+legacyHost+"/grbpwr-com/tech-card-patterns/p.pdf")
	require.True(t, ok)
	require.Empty(t, host)
	require.Empty(t, store.requestLog())
}
