package bucket

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
	"github.com/stretchr/testify/require"
)

// putS3 is a tiny S3: PUT stores, HEAD answers the size, DELETE removes; failPut answers 500.
type putS3 struct {
	mu      sync.Mutex
	objects map[string][]byte
	failPut bool
}

func (s *putS3) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	key := strings.TrimPrefix(r.URL.Path, "/meter-bucket/")
	switch r.Method {
	case http.MethodPost: // multipart (a stream): initiate, then complete
		q := r.URL.Query()
		if _, ok := q["uploads"]; ok {
			_, _ = w.Write([]byte(`<InitiateMultipartUploadResult><Bucket>meter-bucket</Bucket><Key>` + key + `</Key><UploadId>u1</UploadId></InitiateMultipartUploadResult>`))
			return
		}
		_, _ = io.ReadAll(r.Body)
		_, _ = w.Write([]byte(`<CompleteMultipartUploadResult><Bucket>meter-bucket</Bucket><Key>` + key + `</Key><ETag>"e"</ETag></CompleteMultipartUploadResult>`))
	case http.MethodPut:
		body, _ := io.ReadAll(r.Body)
		if s.failPut {
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte(`<Error><Code>InternalError</Code><Message>x</Message></Error>`))
			return
		}
		s.objects[key] = body
		w.Header().Set("ETag", `"e"`)
	case http.MethodHead:
		b, ok := s.objects[key]
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Length", strconv.Itoa(len(b)))
		w.Header().Set("ETag", `"e"`)
		w.Header().Set("Last-Modified", "Mon, 02 Jan 2006 15:04:05 GMT")
	case http.MethodDelete:
		delete(s.objects, key)
		w.WriteHeader(http.StatusNoContent)
	default:
		w.WriteHeader(http.StatusMethodNotAllowed)
	}
}

type fakeMeter struct {
	full       bool
	reserveErr error
	reserved   []int64
	streams    []bool
	added      []int64
}

func (m *fakeMeter) Reserve(_ context.Context, _ int64, n int64, stream bool) error {
	if m.reserveErr != nil {
		return m.reserveErr
	}
	if m.full {
		return ErrStorageFull
	}
	m.reserved = append(m.reserved, n)
	m.streams = append(m.streams, stream)
	return nil
}

func (m *fakeMeter) Add(_ context.Context, _ int64, delta int64) error {
	m.added = append(m.added, delta)
	return nil
}

func newMeterBucket(t *testing.T, teamsMode bool) (*Bucket, *putS3, *fakeMeter) {
	t.Helper()
	store := &putS3{objects: map[string][]byte{}}
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
	m := &fakeMeter{}
	b := &Bucket{Client: cli, Config: &Config{S3BucketName: "meter-bucket", BaseFolder: "probe", TeamsMode: teamsMode}}
	b.SetStorageMeter(m)
	return b, store, m
}

func TestPutRefusedAtCapWritesNothing(t *testing.T) {
	b, store, m := newMeterBucket(t, true)
	m.full = true
	_, err := b.put(teamCtx(2, fileKeyA), "probe/t/"+fileKeyA+"/a.bin", bytes.NewReader([]byte("12345")), 5, minio.PutObjectOptions{}, false)
	require.ErrorIs(t, err, ErrStorageFull)
	require.True(t, IsStorageFull(err))
	require.Empty(t, store.objects)
	require.Empty(t, m.added)
}

func TestPutKnownSizeReservesThenSettles(t *testing.T) {
	b, store, m := newMeterBucket(t, true)
	key := "probe/t/" + fileKeyA + "/a.bin"
	_, err := b.put(teamCtx(2, fileKeyA), key, bytes.NewReader([]byte("12345")), 5, minio.PutObjectOptions{}, false)
	require.NoError(t, err)
	require.Equal(t, []int64{5}, m.reserved)
	require.Equal(t, []bool{false}, m.streams)
	require.Empty(t, m.added) // the real size equals the reservation: nothing to move
	require.Len(t, store.objects[key], 5)

	// delete: the size comes off the count
	require.NoError(t, b.removeMetered(teamCtx(2, fileKeyA), key))
	require.Equal(t, []int64{-5}, m.added)
	require.Empty(t, store.objects)
}

func TestPutStreamCountsRealSize(t *testing.T) {
	b, _, m := newMeterBucket(t, true)
	_, err := b.put(teamCtx(2, fileKeyA), "probe/t/"+fileKeyA+"/s.bin", bytes.NewReader([]byte("1234567")), -1, minio.PutObjectOptions{}, false)
	require.NoError(t, err)
	require.Equal(t, []int64{0}, m.reserved)
	require.Equal(t, []bool{true}, m.streams)
	require.Equal(t, []int64{7}, m.added)
}

func TestPutFailedGivesReservationBack(t *testing.T) {
	b, store, m := newMeterBucket(t, true)
	store.failPut = true
	_, err := b.put(teamCtx(2, fileKeyA), "probe/t/"+fileKeyA+"/a.bin", bytes.NewReader([]byte("12345")), 5, minio.PutObjectOptions{}, false)
	require.Error(t, err)
	require.False(t, IsStorageFull(err))
	require.Equal(t, []int64{5}, m.reserved)
	require.Equal(t, []int64{-5}, m.added)
}

func TestPutExemptAndFailOpen(t *testing.T) {
	// the export archive passes a full team, and is still counted
	b, _, m := newMeterBucket(t, true)
	m.full = true
	_, err := b.put(teamCtx(2, fileKeyA), "probe/t/"+fileKeyA+"/x.zip", bytes.NewReader([]byte("123")), 3, minio.PutObjectOptions{}, true)
	require.NoError(t, err)
	require.Equal(t, []int64{3}, m.added)

	// a broken meter never stops an upload
	b, _, m = newMeterBucket(t, true)
	m.reserveErr = errors.New("db down")
	_, err = b.put(teamCtx(2, fileKeyA), "probe/t/"+fileKeyA+"/a.bin", bytes.NewReader([]byte("12")), 2, minio.PutObjectOptions{}, false)
	require.NoError(t, err)
	require.Equal(t, []int64{2}, m.added)
}

func TestPutNotMeteredOutsideTeams(t *testing.T) {
	b, _, m := newMeterBucket(t, false) // teams mode off: today's behaviour
	m.full = true
	_, err := b.put(teamCtx(2, fileKeyA), "probe/a.bin", bytes.NewReader([]byte("12")), 2, minio.PutObjectOptions{}, false)
	require.NoError(t, err)
	require.Empty(t, m.added)

	b, _, m = newMeterBucket(t, true) // no team in ctx
	m.full = true
	_, err = b.put(context.Background(), "probe/a.bin", bytes.NewReader([]byte("12")), 2, minio.PutObjectOptions{}, false)
	require.NoError(t, err)
	require.Empty(t, m.added)
}

func TestClassifyKey(t *testing.T) {
	for _, c := range []struct {
		base, key string
		owner     keyOwner
		fk        string
	}{
		{"probe", "probe/t/" + fileKeyA + "/x/y.png", keyTeam, fileKeyA},
		{"probe", "probe/t/nothex/y.png", keyNobody, ""},
		{"probe", "probe/t/" + fileKeyA, keyNobody, ""},
		{"probe", "probe/2026/july/a.png", keyTeamOne, ""},
		{"probe", ArchiveSegment + "/a.zip", keyTeamOne, ""},
		{"probe", ImportSegment + "/a.zip", keyTeamOne, ""},
		{"probe", "other-install/a.png", keyNobody, ""},
		{"", "t/" + fileKeyA + "/a.png", keyTeam, fileKeyA},
		{"", "2026/a.png", keyTeamOne, ""},
	} {
		o, fk := classifyKey(c.base, c.key)
		require.Equal(t, c.owner, o, c.key)
		require.Equal(t, c.fk, fk, c.key)
	}
}

// TestPutObjectOnlyInPut: every byte written to the bucket goes through put (the storage cap).
func TestPutObjectOnlyInPut(t *testing.T) {
	files, err := filepath.Glob("*.go")
	require.NoError(t, err)
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") || f == "meter.go" {
			continue
		}
		src, err := os.ReadFile(f)
		require.NoError(t, err)
		for _, call := range []string{".PutObject(", ".FPutObject(", ".CopyObject(", ".ComposeObject(", ".RemoveObject(", ".RemoveObjects(", "PresignedPut"} {
			// The day-30 wipe (D3) deletes a whole wiped team's prefix unmetered: its team_storage
			// row goes with the team's rows at finalize, so there is no count left to keep.
			if f == "teamwipe.go" && call == ".RemoveObjects(" {
				continue
			}
			require.NotContains(t, string(src), call, "%s writes the bucket outside put / removeMetered", f)
		}
	}
}

func TestIsStorageFullMatch(t *testing.T) {
	require.True(t, IsStorageFull(ErrStorageFull))
	require.True(t, IsStorageFull(fmt.Errorf("upload: %w", ErrStorageFull)))
	require.True(t, IsStorageFull(fmt.Errorf("upload: %v", ErrStorageFull)), "flattened chain still maps")
	require.False(t, IsStorageFull(nil))
	require.False(t, IsStorageFull(errors.New("disk storage full on host")))
	require.False(t, IsStorageFull(errors.New("bucket: storage full")))
	require.False(t, IsStorageFull(errors.New("storage_full")))
}

func TestStorageRoom(t *testing.T) {
	b, _, m := newMeterBucket(t, true)
	require.NoError(t, b.StorageRoom(teamCtx(2, fileKeyA)))
	require.Equal(t, []int64{0}, m.reserved, "a room check reserves nothing")
	require.Empty(t, m.added)

	m.full = true
	require.ErrorIs(t, b.StorageRoom(teamCtx(2, fileKeyA)), ErrStorageFull)

	m.full, m.reserveErr = false, errors.New("db down")
	require.NoError(t, b.StorageRoom(teamCtx(2, fileKeyA)), "a broken meter never stops work")

	off, _, offM := newMeterBucket(t, false)
	offM.full = true
	require.NoError(t, off.StorageRoom(teamCtx(2, fileKeyA)), "teams mode off: nothing is counted")
	require.NoError(t, b.StorageRoom(context.Background()), "no team in ctx: nothing is counted")
}
