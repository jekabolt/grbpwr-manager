package patternjoin

import (
	"context"
	"github.com/jekabolt/grbpwr-manager/internal/bucket"
	"github.com/minio/minio-go/v7"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"
)

// Exercise the actual production GetManagedObject (Stat and Get), including a nonempty
// deployment BaseFolder. Only S3's wire response is local.
func TestManagedBucketJoinBytes(t *testing.T) {
	objects, c, a, b, s := fixture(t, "r2000")
	c.BaseFolder = "shop"
	c.S3BucketName = "test-patterns"
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		prefix := "/test-patterns/shop/"
		if !strings.HasPrefix(r.URL.Path, prefix) {
			http.NotFound(w, r)
			return
		}
		data, ok := objects[strings.TrimPrefix(r.URL.Path, prefix)]
		if !ok {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("ETag", `"abcdefghijklmnopqrstuvwxyz012345"`)
		w.Header().Set("Last-Modified", "Wed, 18 Nov 2026 07:00:00 GMT")
		w.Header().Set("Content-Type", "application/dxf")
		w.Header().Set("Content-Length", strconv.Itoa(len(data)))
		if r.Method == "GET" {
			w.Write(data)
		}
	}))
	defer api.Close()
	u, _ := url.Parse(api.URL)
	client, e := minio.New(u.Host, &minio.Options{Secure: false, Region: "us-east-1", BucketLookup: minio.BucketLookupPath})
	if e != nil {
		t.Fatal(e)
	}
	managed := &bucket.Bucket{Client: client, Config: c}
	a.URL = strings.Replace(a.URL, "/tech-card-patterns", "/shop/tech-card-patterns", 1)
	b.URL = strings.Replace(b.URL, "/tech-card-patterns", "/shop/tech-card-patterns", 1)
	if _, e := ReadBinding(context.Background(), managed, c, a, b, s); e != nil {
		t.Fatal(e)
	}
	b.URL = strings.Replace(b.URL, "/shop/", "/staging/", 1)
	if _, e := ReadBinding(t.Context(), managed, c, a, b, s); e == nil {
		t.Fatal("accepted other deployment folder")
	}
}
