package httpapi

import (
	"context"
	"errors"
	"strings"
	"testing"

	pb_admin "github.com/jekabolt/grbpwr-manager/proto/gen/admin"
	pb_common "github.com/jekabolt/grbpwr-manager/proto/gen/common"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/reflect/protoregistry"
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

// TestSignSkipNamesAreRealFields: a renamed or moved field must fail here, not silently get signed
// again and break the seals the skip list exists for.
func TestSignSkipNamesAreRealFields(t *testing.T) {
	require.NotEmpty(t, signSkip)
	for name := range signSkip {
		d, err := protoregistry.GlobalFiles.FindDescriptorByName(name)
		require.NoError(t, err, name)
		_, isField := d.(protoreflect.FieldDescriptor)
		require.True(t, isField, "%s is not a field", name)
	}
}

// TestFileURLCallLeavesRowIdentityBare: the saved-row urls stay the stored string (sealed and
// compared by both sides), while the media of the same response is still signed.
func TestFileURLCallLeavesRowIdentityBare(t *testing.T) {
	fx := &fakeFileURLs{needle: "/b/t/aaaa/"}
	const dxf = "https://h.test/b/t/aaaa/main.webp" // the fake signs ".webp"
	card := &pb_admin.GetTechCardResponse{TechCard: &pb_common.TechCard{Id: 7,
		TechCard:               &pb_common.TechCardInsert{Patterns: []*pb_common.TechCardSizePattern{{Url: dxf, LineKey: "L"}}},
		ResolvedTechnicalMedia: []*pb_common.TechCardMediaFull{{Media: media(ownFile)}},
	}}
	out := call(t, fx, &pb_admin.GetTechCardRequest{}, card, nil).(*pb_admin.GetTechCardResponse)
	require.Equal(t, dxf, out.GetTechCard().GetTechCard().GetPatterns()[0].GetUrl())
	require.Equal(t, ownFile+"?sig",
		out.GetTechCard().GetResolvedTechnicalMedia()[0].GetMedia().GetMedia().GetFullSize().GetMediaUrl())

	// The saved Assembly read: identity urls and every sealed list stay byte-identical, free text
	// included; there is nothing else to sign, so the very same value goes back.
	row := func() *pb_admin.TechCardSavedPatternRowIdentity {
		return &pb_admin.TechCardSavedPatternRowIdentity{TechCardId: 7, Url: dxf, DownloadUrl: "/api/p/token?dl=1"}
	}
	snapshot := &pb_admin.GetTechCardSavedAssemblySnapshotResponse{
		Row:              row(),
		Operations:       []*pb_common.TechCardOperation{{Note: "see " + ownFile}},
		CompleteSourceV1: &pb_admin.TechCardSavedAssemblyCompleteSourceV1{Rows: []*pb_admin.TechCardSavedPatternRowIdentity{row()}},
	}
	require.Same(t, snapshot, call(t, fx, &pb_admin.GetTechCardSavedAssemblySnapshotRequest{}, snapshot, nil))
	require.Equal(t, 2, fx.signs, "only the card's two media strings were signed")

	// A tab loaded before the skip list sends a signed identity url back: it is cut to the stored form.
	req := &pb_admin.GetTechCardSavedAssemblySnapshotRequest{ExpectedUrl: dxf + "?sig"}
	call(t, fx, req, nil, nil)
	require.Equal(t, dxf, req.GetExpectedUrl())
}
