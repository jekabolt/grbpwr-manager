package bucket

import (
	"context"
	"fmt"
	"io"
	"net/url"
	"path"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// The key of a new object: team #1 and teams mode off byte for byte today's formula; another team
// under {base}/t/{file_key}/; no team or no file key in teams mode → refused, never the legacy prefix.
func TestConstructFullPathTeamPrefix(t *testing.T) {
	now := time.Now().UTC()
	ym := fmt.Sprintf("%d/%s", now.Year(), strings.ToLower(now.Month().String()))
	legacy := func(base, folder string) string { // the formula before the team prefix
		return path.Clean(strings.Join([]string{base, folder, fmt.Sprint(now.Year()), strings.ToLower(now.Month().String()), "n.webp"}, "/"))
	}

	for _, tc := range []struct {
		name  string
		mode  bool
		ctx   context.Context
		want  string
		wantE error
	}{
		{"mode off, no team", false, context.Background(), legacy("probe", "probe"), nil},
		{"mode off, team 2 in ctx", false, teamCtx(2, fileKeyA), legacy("probe", "probe"), nil},
		{"team #1", true, teamCtx(1, ""), legacy("probe", "probe"), nil},
		{"team 2", true, teamCtx(2, fileKeyA), "probe/t/" + fileKeyA + "/probe/" + ym + "/n.webp", nil},
		{"team without a file key", true, teamCtx(3, ""), "", ErrNoTeamFileKey},
		{"team with a malformed file key", true, teamCtx(3, "../.."), "", ErrNoTeamFileKey},
		{"no team in teams mode", true, context.Background(), "", ErrNoTeamFileKey},
	} {
		b, _ := newProxyBucket(t, tc.mode)
		got, err := b.constructFullPath(tc.ctx, "probe", "n", "webp")
		if tc.wantE != nil {
			require.ErrorIsf(t, err, tc.wantE, tc.name)
			continue
		}
		require.NoErrorf(t, err, tc.name)
		require.Equalf(t, tc.want, got, tc.name)
	}

	b, _ := newProxyBucket(t, true)
	root, err := b.Config.teamObjectRoot(teamCtx(1, ""))
	require.NoError(t, err)
	require.Empty(t, root, "team #1 archives / imports stay at the bucket root")
	root, err = b.Config.teamObjectRoot(teamCtx(2, fileKeyA))
	require.NoError(t, err)
	require.Equal(t, "probe/t/"+fileKeyA+"/", root)
	_, err = b.Config.teamObjectRoot(teamCtx(3, ""))
	require.ErrorIs(t, err, ErrNoTeamFileKey)
}

// Acceptance: team 2 uploads a pattern → the key is under its prefix → the proxy serves it to team 2
// and refuses team 3 (and team #1). Team #1's own upload keeps the legacy key and stays readable.
func TestTeamUploadIsServedOnlyToItsTeam(t *testing.T) {
	b, store := newProxyBucket(t, true)
	dxf := []byte("0\nSECTION\n2\nHEADER\n")

	u, size, err := b.UploadPatternFile(teamCtx(2, fileKeyA), dxf, "p")
	require.NoError(t, err)
	require.Equal(t, int64(len(dxf)), size)
	parsed, err := url.Parse(u)
	require.NoError(t, err)
	key := strings.TrimPrefix(parsed.Path, "/")
	require.True(t, strings.HasPrefix(key, "probe/t/"+fileKeyA+"/tech-card-patterns/"), key)
	require.Contains(t, store.objects, key)

	o, err := b.OpenProxyObject(teamCtx(2, fileKeyA), u)
	require.NoError(t, err)
	got, err := io.ReadAll(o.Body)
	o.Body.Close()
	require.NoError(t, err)
	require.Equal(t, dxf, got)

	for name, ctx := range map[string]context.Context{"team 3": teamCtx(3, fileKeyB), "team #1": teamCtx(1, "")} {
		_, err = b.OpenProxyObject(ctx, u)
		require.ErrorIsf(t, err, ErrProxyRefused, name)
	}

	u1, _, err := b.UploadPatternFile(teamCtx(1, ""), dxf, "p")
	require.NoError(t, err)
	p1, err := url.Parse(u1)
	require.NoError(t, err)
	require.True(t, strings.HasPrefix(p1.Path, "/probe/tech-card-patterns/"), p1.Path)
	o, err = b.OpenProxyObject(teamCtx(1, ""), u1)
	require.NoError(t, err)
	o.Body.Close()
	_, err = b.OpenProxyObject(teamCtx(2, fileKeyA), u1)
	require.ErrorIs(t, err, ErrProxyRefused)

	// A team without a file key writes nothing at all (never under the legacy prefix).
	before := len(store.log())
	_, _, err = b.UploadPatternFile(teamCtx(3, ""), dxf, "p")
	require.ErrorIs(t, err, ErrNoTeamFileKey)
	require.Len(t, store.log(), before, "no request reached S3")
}

// Archive cleanup lists only the segment of the team in ctx: team #1 today's root segment, another
// team the one under its prefix.
func TestListObjectsOlderThanIsTeamScoped(t *testing.T) {
	b, store := newProxyBucket(t, true)
	cutoff := time.Now().Add(-time.Hour)
	_, err := b.ListObjectsOlderThan(teamCtx(1, ""), ArchiveSegment, cutoff)
	require.NoError(t, err)
	_, err = b.ListObjectsOlderThan(teamCtx(2, fileKeyA), ImportSegment, cutoff)
	require.NoError(t, err)
	_, err = b.ListObjectsOlderThan(teamCtx(3, ""), ArchiveSegment, cutoff)
	require.ErrorIs(t, err, ErrNoTeamFileKey)
	require.Equal(t, []string{
		"LIST " + ArchiveSegment + "/",
		"LIST probe/t/" + fileKeyA + "/" + ImportSegment + "/",
	}, store.log())
}
