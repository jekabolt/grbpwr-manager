package bucket

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"

	"github.com/jekabolt/grbpwr-manager/internal/tenant"
	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
	"github.com/stretchr/testify/require"
)

// aclS3 records the canned acl each PUT carried, by key.
type aclS3 struct {
	mu   sync.Mutex
	acls map[string]string
}

func (s *aclS3) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if r.Method != http.MethodPut {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	s.acls[strings.TrimPrefix(r.URL.Path, "/acl-bucket/")] = r.Header.Get("X-Amz-Acl")
	w.Header().Set("ETag", `"e"`)
}

func newACLBucket(t *testing.T, teamsMode bool) (*Bucket, *aclS3) {
	t.Helper()
	store := &aclS3{acls: map[string]string{}}
	srv := httptest.NewTLSServer(store)
	t.Cleanup(srv.Close)
	u, err := url.Parse(srv.URL)
	require.NoError(t, err)
	cli, err := minio.New(u.Host, &minio.Options{
		Creds:        credentials.NewStaticV4("probe", "probe12345", ""),
		Secure:       true,
		Transport:    srv.Client().Transport,
		Region:       "us-east-1",
		BucketLookup: minio.BucketLookupPath,
	})
	require.NoError(t, err)
	return &Bucket{Client: cli, Config: &Config{S3BucketName: "acl-bucket", BaseFolder: "probe", TeamsMode: teamsMode}}, store
}

func TestPutDropsPublicACLOnlyForTeamKeys(t *testing.T) {
	put := func(b *Bucket, ctx context.Context, key string) map[string]string {
		meta := map[string]string{"x-amz-acl": "public-read"}
		_, err := b.put(ctx, key, bytes.NewReader([]byte("x")), 1, minio.PutObjectOptions{UserMetadata: meta}, false)
		require.NoError(t, err)
		return meta
	}

	b, store := newACLBucket(t, true)
	teamKey := "probe/t/" + fileKeyA + "/probe/2026/november/a-og.webp"
	meta := put(b, teamCtx(2, fileKeyA), teamKey)
	require.Equal(t, "", store.acls[teamKey], "a team's work file is written private")
	require.Equal(t, "public-read", meta["x-amz-acl"], "the caller's map is not changed")

	legacy := "probe/probe/2026/november/b-og.webp"
	put(b, teamCtx(tenant.TeamOneID, ""), legacy)
	require.Equal(t, "public-read", store.acls[legacy], "team #1 keeps public-read")

	off, offStore := newACLBucket(t, false)
	put(off, context.Background(), teamKey)
	require.Equal(t, "public-read", offStore.acls[teamKey], "teams mode off writes as before")
}

const (
	signHost   = "lekalo-beta.fra1.digitaloceanspaces.com"
	signPrefix = "https://" + signHost + "/"
)

func newSignBucket(t *testing.T, mutate func(*Config)) *Bucket {
	t.Helper()
	// Region set: the location lookup answers from memory, no network in the test.
	cli, err := minio.New("fra1.digitaloceanspaces.com", &minio.Options{
		Creds:  credentials.NewStaticV4("probe", "probe12345", ""),
		Secure: true,
		Region: "fra1",
	})
	require.NoError(t, err)
	c := &Config{
		S3AccessKey:       "probe",
		S3SecretAccessKey: "probe12345",
		S3Endpoint:        "fra1.digitaloceanspaces.com",
		S3BucketName:      "lekalo-beta",
		BaseFolder:        "lekalo-beta",
		SubdomainEndpoint: signHost,
		ReadOnlyHosts:     "files.grbpwr.com",
		TeamsMode:         true,
	}
	if mutate != nil {
		mutate(c)
	}
	return &Bucket{Client: cli, Config: c}
}

func teamFileURL(fk, name string) string {
	return signPrefix + "lekalo-beta/t/" + fk + "/lekalo-beta/2026/november/" + name
}

func TestSignFileURLsSignsOwnTeamFileOnly(t *testing.T) {
	b := newSignBucket(t, nil)
	ctxA := teamCtx(2, fileKeyA)
	own := teamFileURL(fileKeyA, "a-og.webp")

	signed, ok := b.SignFileURLs(ctxA, own)
	require.True(t, ok)
	require.True(t, strings.HasPrefix(signed, own+"?"), "signed = stored url + query: %s", signed)
	q, err := url.Parse(signed)
	require.NoError(t, err)
	require.Equal(t, signHost, q.Host)
	require.NotEmpty(t, q.Query().Get("X-Amz-Signature"))
	require.Contains(t, q.Query().Get("X-Amz-Credential"), "/fra1/s3/")

	again, _ := b.SignFileURLs(ctxA, own)
	require.Equal(t, signed, again, "one string per key per window")
	require.Equal(t, signed, b.SignFileURL(ctxA, own))

	unchanged := map[string]struct {
		ctx context.Context
		s   string
	}{
		"another team's file":   {ctxA, teamFileURL(fileKeyB, "b-og.webp")},
		"team #1 caller":        {teamCtx(tenant.TeamOneID, ""), own},
		"team #1 legacy key":    {ctxA, signPrefix + "lekalo-beta/lekalo-beta/2026/november/c-og.webp"},
		"no team":               {context.Background(), own},
		"read-only host":        {ctxA, "https://files.grbpwr.com/lekalo-beta/t/" + fileKeyA + "/x.webp"},
		"foreign host":          {ctxA, "https://evil.example/lekalo-beta/t/" + fileKeyA + "/x.webp"},
		"already has a query":   {ctxA, own + "?X-Amz-Signature=abc"},
		"has a fragment":        {ctxA, own + "#page=2"},
		"dot segment":           {ctxA, signPrefix + "lekalo-beta/t/" + fileKeyA + "/../t/" + fileKeyB + "/x.webp"},
		"folder, not an object": {ctxA, signPrefix + "lekalo-beta/t/" + fileKeyA + "/"},
		"http, not https":       {ctxA, "http://" + signHost + "/lekalo-beta/t/" + fileKeyA + "/x.webp"},
	}
	for name, tc := range unchanged {
		out, ok := b.SignFileURLs(tc.ctx, tc.s)
		require.False(t, ok, name)
		require.Equal(t, tc.s, out, name)
	}

	off := newSignBucket(t, func(c *Config) { c.TeamsMode = false })
	out, ok := off.SignFileURLs(ctxA, own)
	require.False(t, ok)
	require.Equal(t, own, out)
}

func TestSignFileURLsInsideText(t *testing.T) {
	b := newSignBucket(t, nil)
	ctxA := teamCtx(2, fileKeyA)
	u1, u2 := teamFileURL(fileKeyA, "a-og.webp"), teamFileURL(fileKeyA, "p-1234.dxf")
	foreign := teamFileURL(fileKeyB, "b-og.webp")

	blob, err := json.Marshal(map[string]any{"cover": u1, "pieces": []string{u2, foreign}})
	require.NoError(t, err)
	signed, ok := b.SignFileURLs(ctxA, string(blob))
	require.True(t, ok)
	require.True(t, json.Valid([]byte(signed)), signed)
	var got struct {
		Cover  string   `json:"cover"`
		Pieces []string `json:"pieces"`
	}
	require.NoError(t, json.Unmarshal([]byte(signed), &got))
	require.True(t, strings.HasPrefix(got.Cover, u1+"?X-Amz-"))
	require.True(t, strings.HasPrefix(got.Pieces[0], u2+"?X-Amz-"))
	require.Equal(t, foreign, got.Pieces[1])

	md := "front ![](" + u1 + ") and " + u2 + " end"
	signedMD, ok := b.SignFileURLs(ctxA, md)
	require.True(t, ok)
	require.Contains(t, signedMD, "![]("+u1+"?X-Amz-")
	require.Contains(t, signedMD, " "+u2+"?X-Amz-")
	require.True(t, strings.HasSuffix(signedMD, " end"))

	back, ok := b.CanonicalFileURLs(signedMD)
	require.True(t, ok)
	require.Equal(t, md, back)
	backJSON, ok := b.CanonicalFileURLs(signed)
	require.True(t, ok)
	require.Equal(t, string(blob), backJSON)
}

func TestCanonicalFileURLs(t *testing.T) {
	b := newSignBucket(t, nil)
	ctxA := teamCtx(2, fileKeyA)
	own := teamFileURL(fileKeyA, "a-og.webp")
	signed := b.SignFileURL(ctxA, own)
	require.NotEqual(t, own, signed)

	back, ok := b.CanonicalFileURLs(signed)
	require.True(t, ok)
	require.Equal(t, own, back)

	keep := []string{
		own,
		"https://fra1.digitaloceanspaces.com/lekalo-beta/lekalo-beta/files/x.pdf?X-Amz-Signature=abc", // library presign
		signPrefix + "lekalo-beta/lekalo-beta/2026/november/c.webp?X-Amz-Signature=abc",               // not a team key
		own + "?v=2",
		"https://files.grbpwr.com/lekalo-beta/t/" + fileKeyA + "/x.webp?X-Amz-Signature=abc",
	}
	for _, s := range keep {
		out, ok := b.CanonicalFileURLs(s)
		require.False(t, ok, s)
		require.Equal(t, s, out)
	}

	// A CDN in front of the origin: the signature names the origin, the stored url the CDN.
	cdn := newSignBucket(t, func(c *Config) { c.SubdomainEndpoint = "files.lekalo.test" })
	key := "lekalo-beta/t/" + fileKeyA + "/lekalo-beta/2026/november/a-og.webp"
	cdnSigned := cdn.SignFileURL(ctxA, "https://files.lekalo.test/"+key)
	require.True(t, strings.HasPrefix(cdnSigned, signPrefix+key+"?X-Amz-"), cdnSigned)
	out, ok := cdn.CanonicalFileURLs(cdnSigned)
	require.True(t, ok)
	require.Equal(t, "https://files.lekalo.test/"+key, out)
}

func TestPrivateURLNeedle(t *testing.T) {
	b := newSignBucket(t, nil)
	n, ok := b.PrivateURLNeedle(teamCtx(2, fileKeyA))
	require.True(t, ok)
	require.Equal(t, "/lekalo-beta/t/"+fileKeyA+"/", n)
	for name, ctx := range map[string]context.Context{
		"team #1":     teamCtx(tenant.TeamOneID, ""),
		"no team":     context.Background(),
		"bad filekey": teamCtx(3, "nothex"),
	} {
		_, ok := b.PrivateURLNeedle(ctx)
		require.False(t, ok, name)
	}
	_, ok = newSignBucket(t, func(c *Config) { c.TeamsMode = false }).PrivateURLNeedle(teamCtx(2, fileKeyA))
	require.False(t, ok)
}

func TestProxyServesSignedURL(t *testing.T) {
	b, store := newProxyBucket(t, true)
	b.S3AccessKey, b.S3SecretAccessKey = "probe", "probe12345"
	key := "probe/t/" + fileKeyA + "/tech-card-patterns/2026/november/p.dxf"
	store.objects[key] = proxyObj{body: []byte("0\nSECTION"), contentType: "application/dxf"}

	signed := b.SignFileURL(teamCtx(2, fileKeyA), cdnURL(key))
	require.Contains(t, signed, "X-Amz-Signature=")
	o, err := b.OpenProxyObject(teamCtx(2, fileKeyA), signed)
	require.NoError(t, err)
	require.Equal(t, "0\nSECTION", string(readAll(t, o)))

	_, err = b.OpenProxyObject(teamCtx(3, fileKeyB), signed)
	require.Error(t, err, "a signed url is still team-checked by the proxy")
}
