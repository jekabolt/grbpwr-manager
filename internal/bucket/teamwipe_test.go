package bucket

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/minio/minio-go/v7"
)

const testFK = "0123456789abcdef0123456789abcdef"

func TestTeamWipePrefixGuards(t *testing.T) {
	on := &Config{TeamsMode: true, BaseFolder: "/lekalo/"}
	if p, err := TeamWipePrefix(on, testFK); err != nil || p != "lekalo/t/"+testFK+"/" {
		t.Fatalf("prefix %q %v", p, err)
	}
	for name, c := range map[string]struct {
		cfg *Config
		fk  string
	}{
		"nil config":     {nil, testFK},
		"teams mode off": {&Config{BaseFolder: "lekalo"}, testFK},
		"no base":        {&Config{TeamsMode: true, BaseFolder: " / "}, testFK},
		"empty key":      {on, ""},
		"upper key":      {on, strings.ToUpper(testFK)},
		"short key":      {on, testFK[:31]},
		"long key":       {on, testFK + "0"},
		"slash key":      {on, "0123456789abcdef/123456789abcdef"},
		"dots key":       {on, "..23456789abcdef0123456789abcdef"},
	} {
		if p, err := TeamWipePrefix(c.cfg, c.fk); !errors.Is(err, ErrWipePrefix) || p != "" {
			t.Errorf("%s: %q %v", name, p, err)
		}
	}
}

// wipeS3 lists every key in objs that starts with the prefix (and, to prove the second fence, the
// "leak" keys whatever the prefix), and removes what it is sent.
type wipeS3 struct {
	versioning string
	objs       []minio.ObjectInfo
	leak       []string
	failKey    string
	removed    []minio.ObjectInfo
	listedVers []bool
	verErr     error
}

func (f *wipeS3) GetBucketVersioning(context.Context, string) (minio.BucketVersioningConfiguration, error) {
	return minio.BucketVersioningConfiguration{Status: f.versioning}, f.verErr
}

func (f *wipeS3) ListObjects(_ context.Context, _ string, o minio.ListObjectsOptions) <-chan minio.ObjectInfo {
	f.listedVers = append(f.listedVers, o.WithVersions)
	ch := make(chan minio.ObjectInfo, len(f.objs)+len(f.leak))
	for _, ob := range f.objs {
		if strings.HasPrefix(ob.Key, o.Prefix) {
			ch <- ob
		}
	}
	for _, k := range f.leak {
		ch <- minio.ObjectInfo{Key: k}
	}
	close(ch)
	return ch
}

func (f *wipeS3) RemoveObjects(_ context.Context, _ string, in <-chan minio.ObjectInfo, _ minio.RemoveObjectsOptions) <-chan minio.RemoveObjectError {
	out := make(chan minio.RemoveObjectError, 1000)
	for ob := range in {
		if ob.Key == f.failKey {
			out <- minio.RemoveObjectError{ObjectName: ob.Key, VersionID: ob.VersionID, Err: errors.New("denied")}
			continue
		}
		f.removed = append(f.removed, ob)
		keep := f.objs[:0]
		for _, o := range f.objs {
			if o.Key != ob.Key || o.VersionID != ob.VersionID {
				keep = append(keep, o)
			}
		}
		f.objs = keep
	}
	close(out)
	return out
}

func TestWipePrefixDeletesOnlyThePrefix(t *testing.T) {
	ctx := context.Background()
	prefix := "lekalo/t/" + testFK + "/"
	other := "lekalo/t/ffffffffffffffffffffffffffffffff/a.png"
	f := &wipeS3{
		versioning: "Enabled",
		objs: []minio.ObjectInfo{
			{Key: prefix + "a.png", VersionID: "v1", Size: 10},
			{Key: prefix + "a.png", VersionID: "v2", Size: 11},
			{Key: prefix + "deep/b.pdf", VersionID: "v1", Size: 5},
			{Key: other, VersionID: "v1", Size: 7},
			{Key: "lekalo/products/team-one.jpg", VersionID: "v1", Size: 9},
		},
		leak: []string{"lekalo/products/team-one.jpg"},
	}
	st, err := wipePrefix(ctx, f, "b", prefix, 5, false)
	if err != nil {
		t.Fatal(err)
	}
	// The second listing still returns the leaked key, which is never deleted: Foreign counts it.
	if st.Objects != 3 || st.Bytes != 26 || !st.Done || st.Foreign == 0 {
		t.Fatalf("stats %+v", st)
	}
	for _, r := range f.removed {
		if !strings.HasPrefix(r.Key, prefix) {
			t.Fatalf("deleted %s outside the prefix", r.Key)
		}
	}
	if len(f.objs) != 2 {
		t.Fatalf("left %v, want the other team's and team #1's objects", f.objs)
	}
	for _, v := range f.listedVers {
		if !v {
			t.Fatal("versioned bucket listed without versions")
		}
	}
}

func TestWipePrefixUnversionedAndPreview(t *testing.T) {
	ctx := context.Background()
	prefix := "lekalo/t/" + testFK + "/"
	f := &wipeS3{objs: []minio.ObjectInfo{{Key: prefix + "a", Size: 3}, {Key: prefix + "b", Size: 4}}}
	st, err := wipePrefix(ctx, f, "b", prefix, 5, true)
	if err != nil || st.Objects != 2 || st.Bytes != 7 || len(f.removed) != 0 || len(f.objs) != 2 {
		t.Fatalf("preview %+v %v removed %d", st, err, len(f.removed))
	}
	if f.listedVers[0] {
		t.Fatal("unversioned bucket listed with versions")
	}
	if st, err := wipePrefix(ctx, f, "b", prefix, 5, false); err != nil || !st.Done || st.Objects != 2 {
		t.Fatalf("wipe %+v %v", st, err)
	}
	if st, err := wipePrefix(ctx, f, "b", prefix, 5, false); err != nil || !st.Done || st.Objects != 0 {
		t.Fatalf("rerun %+v %v", st, err)
	}
}

func TestWipePrefixFailures(t *testing.T) {
	ctx := context.Background()
	prefix := "lekalo/t/" + testFK + "/"
	f := &wipeS3{objs: []minio.ObjectInfo{{Key: prefix + "a"}, {Key: prefix + "b"}}, failKey: prefix + "b"}
	st, err := wipePrefix(ctx, f, "b", prefix, 5, false)
	if err == nil || st.Done || st.Objects != 1 {
		t.Fatalf("per-key failure %+v %v", st, err)
	}
	f = &wipeS3{verErr: errors.New("no access")}
	if _, err := wipePrefix(ctx, f, "b", prefix, 5, false); err == nil {
		t.Fatal("versioning error must fail the step")
	}
	for _, bad := range []string{"", "lekalo/", "lekalo/t/", "lekalo/t/x"} {
		if _, err := wipePrefix(ctx, &wipeS3{}, "b", bad, 5, false); !errors.Is(err, ErrWipePrefix) {
			t.Errorf("prefix %q accepted", bad)
		}
	}
	// A bounded call: 2500 objects, one batch per call, three calls.
	many := &wipeS3{}
	for i := range 2500 {
		many.objs = append(many.objs, minio.ObjectInfo{Key: prefix + strings.Repeat("x", 1+i%7) + string(rune('a'+i%26)) + itoa(i)})
	}
	calls := 0
	for {
		calls++
		st, err := wipePrefix(ctx, many, "b", prefix, 1, false)
		if err != nil {
			t.Fatal(err)
		}
		if st.Done {
			break
		}
		if calls > 5 {
			t.Fatal("never done")
		}
	}
	if calls != 3 || len(many.objs) != 0 {
		t.Fatalf("calls %d left %d", calls, len(many.objs))
	}
}

func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	var b []byte
	for ; i > 0; i /= 10 {
		b = append([]byte{byte('0' + i%10)}, b...)
	}
	return string(b)
}

func TestPreflightPrefix(t *testing.T) {
	ctx := context.Background()
	prefix := "lekalo/t/" + testFK + "/"
	f := &wipeS3{objs: []minio.ObjectInfo{{Key: prefix + "a"}}}
	if err := preflightPrefix(ctx, f, "b", prefix); err != nil {
		t.Fatal(err)
	}
	if len(f.listedVers) != 0 || len(f.removed) != 0 || len(f.objs) != 1 {
		t.Fatal("preflight listed or deleted")
	}
	if err := preflightPrefix(ctx, &wipeS3{verErr: errors.New("no access")}, "b", prefix); err == nil {
		t.Fatal("versioning error must fail the preflight")
	}
	for _, c := range [][2]string{{"", prefix}, {"b", ""}, {"b", "lekalo/t/"}, {"b", "lekalo/t/x"}} {
		if err := preflightPrefix(ctx, &wipeS3{}, c[0], c[1]); !errors.Is(err, ErrWipePrefix) {
			t.Errorf("bucket %q prefix %q accepted", c[0], c[1])
		}
	}
	b := &Bucket{Config: &Config{TeamsMode: false, BaseFolder: "lekalo"}}
	if err := b.PreflightTeamFiles(ctx, testFK); !errors.Is(err, ErrWipePrefix) {
		t.Fatalf("teams mode off: %v", err)
	}
}
