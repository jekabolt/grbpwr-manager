package httpapi

import (
	"context"
	"errors"
	"strings"
	"testing"

	pb_common "github.com/jekabolt/grbpwr-manager/proto/gen/common"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/structpb"
)

// fakeFileURLs signs "<needle>…webp" by appending "?sig" and cuts "?sig" off again.
type fakeFileURLs struct {
	needle string
	signs  int
}

func (f *fakeFileURLs) PrivateURLNeedle(context.Context) (string, bool) {
	return f.needle, f.needle != ""
}

func (f *fakeFileURLs) SignFileURLs(_ context.Context, s string) (string, bool) {
	if !strings.Contains(s, f.needle) {
		return s, false
	}
	f.signs++
	return strings.ReplaceAll(s, ".webp", ".webp?sig"), true
}

func (f *fakeFileURLs) CanonicalFileURLs(s string) (string, bool) {
	if !strings.Contains(s, "?sig") {
		return s, false
	}
	return strings.ReplaceAll(s, "?sig", ""), true
}

const (
	ownFile   = "https://h.test/b/t/aaaa/x.webp"
	otherFile = "https://h.test/b/t/bbbb/y.webp"
)

func media(u string) *pb_common.MediaFull {
	return &pb_common.MediaFull{Id: 1, Media: &pb_common.MediaItem{
		FullSize:  &pb_common.MediaInfo{MediaUrl: u},
		Thumbnail: &pb_common.MediaInfo{MediaUrl: u},
	}}
}

func call(t *testing.T, fx FileURLRewriter, req, resp any, herr error) any {
	t.Helper()
	out, err := fileURLCall(context.Background(), fx, req, func(context.Context, any) (any, error) {
		return resp, herr
	})
	if herr != nil {
		require.ErrorIs(t, err, herr)
	} else {
		require.NoError(t, err)
	}
	return out
}

func TestFileURLCallSignsAClone(t *testing.T) {
	fx := &fakeFileURLs{needle: "/b/t/aaaa/"}
	resp := media(ownFile)
	out := call(t, fx, &pb_common.MediaInfo{}, resp, nil).(*pb_common.MediaFull)

	require.NotSame(t, resp, out)
	require.Equal(t, ownFile+"?sig", out.GetMedia().GetFullSize().GetMediaUrl())
	require.Equal(t, ownFile+"?sig", out.GetMedia().GetThumbnail().GetMediaUrl())
	require.Equal(t, ownFile, resp.GetMedia().GetFullSize().GetMediaUrl(), "the handler's value is never changed")
}

func TestFileURLCallWalksListsMapsAndNesting(t *testing.T) {
	fx := &fakeFileURLs{needle: "/b/t/aaaa/"}
	resp, err := structpb.NewStruct(map[string]any{
		"cover":  ownFile,
		"pieces": []any{ownFile, otherFile, map[string]any{"deep": ownFile}},
		"note":   "see " + ownFile + " here",
		"count":  3,
	})
	require.NoError(t, err)
	before := proto.Clone(resp)

	out := call(t, fx, &structpb.Struct{}, resp, nil).(*structpb.Struct)
	got := out.AsMap()
	require.Equal(t, ownFile+"?sig", got["cover"])
	pieces := got["pieces"].([]any)
	require.Equal(t, ownFile+"?sig", pieces[0])
	require.Equal(t, otherFile, pieces[1])
	require.Equal(t, ownFile+"?sig", pieces[2].(map[string]any)["deep"])
	require.Equal(t, "see "+ownFile+"?sig here", got["note"])
	require.True(t, proto.Equal(before, resp), "the handler's value is never changed")

	list := &pb_common.MediaFull{}
	require.True(t, proto.Equal(list, call(t, fx, &structpb.Struct{}, list, nil).(proto.Message)))
}

func TestFileURLCallLeavesOtherResponsesAlone(t *testing.T) {
	fx := &fakeFileURLs{needle: "/b/t/aaaa/"}
	resp := media(otherFile)
	require.Same(t, resp, call(t, fx, &pb_common.MediaInfo{}, resp, nil), "no own file: the very same value")

	noTeam := &fakeFileURLs{}
	resp = media(ownFile)
	require.Same(t, resp, call(t, noTeam, &pb_common.MediaInfo{}, resp, nil), "team #1 / no team: untouched")
	require.Zero(t, noTeam.signs)

	boom := errors.New("boom")
	require.Nil(t, call(t, fx, &pb_common.MediaInfo{}, nil, boom))
	require.Zero(t, fx.signs)
}

func TestFileURLCallCanonicalizesTheRequest(t *testing.T) {
	fx := &fakeFileURLs{}
	req := media(ownFile + "?sig")
	nested, err := structpb.NewStruct(map[string]any{"u": []any{ownFile + "?sig"}})
	require.NoError(t, err)

	var seen *pb_common.MediaFull
	_, err = fileURLCall(context.Background(), fx, req, func(_ context.Context, r any) (any, error) {
		seen = r.(*pb_common.MediaFull)
		return nil, nil
	})
	require.NoError(t, err)
	require.Equal(t, ownFile, seen.GetMedia().GetFullSize().GetMediaUrl())
	require.Equal(t, ownFile, seen.GetMedia().GetThumbnail().GetMediaUrl())

	call(t, fx, nested, nil, nil)
	require.Equal(t, ownFile, nested.AsMap()["u"].([]any)[0])
}

func TestFileURLUnaryIsAPassThroughWhenOff(t *testing.T) {
	s := &Server{}
	resp := media(ownFile + "?sig")
	out, err := s.fileURLUnary()(context.Background(), resp, &grpc.UnaryServerInfo{}, func(_ context.Context, r any) (any, error) {
		return r, nil
	})
	require.NoError(t, err)
	require.Same(t, resp, out)
	require.Equal(t, ownFile+"?sig", resp.GetMedia().GetFullSize().GetMediaUrl())
}
