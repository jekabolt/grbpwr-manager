package admin

import (
	"context"
	"encoding/base64"
	"strings"
	"testing"

	"github.com/jekabolt/grbpwr-manager/internal/dependency"
	pb_admin "github.com/jekabolt/grbpwr-manager/proto/gen/admin"
	pb_common "github.com/jekabolt/grbpwr-manager/proto/gen/common"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// ─────────────────────────────────────────────────────────────────────────────
// B-1 — preserve_original picks the ROUTE, and the promise it makes is exact.
//
// The whole feature is one branch, so the probe is the pair that a wrong branch cannot satisfy at
// the same time: with the flag the bucket's VERBATIM method is called with the decoded payload and
// the re-encoding one is not; without the flag the re-encoding method is called with the untouched
// data url and the verbatim one is not. Asserting only the first half would stay green if the
// branch were inverted — both halves are the test.
//
// The third case is the honesty of the promise: verbatim covers JPEG, PNG, WebP and GIF, and HEIC
// has no verbatim path. That refusal must be an InvalidArgument that names the remedy, not an
// Internal that reads as a server fault — and it must land BEFORE anything reaches the bucket.
//
// The stub is hand-written rather than mockery's: this worktree has no generated mocks (they are
// gitignored codegen), and a probe of a two-way branch should not depend on a code generator. The
// embedded nil interface gives the same strictness as a strict mock — any method these tests did
// not stub panics instead of quietly returning a zero value.
// ─────────────────────────────────────────────────────────────────────────────

type verbatimBucketStub struct {
	dependency.FileStore // nil on purpose: an unexpected call panics rather than passing silently

	plainCalls    []string
	verbatimCalls [][]byte
}

func (b *verbatimBucketStub) GetBaseFolder() string { return "grbpwr" }

func (b *verbatimBucketStub) UploadContentImage(_ context.Context, rawB64Image, _, _ string) (*pb_common.MediaFull, error) {
	b.plainCalls = append(b.plainCalls, rawB64Image)
	return &pb_common.MediaFull{Id: 101}, nil
}

func (b *verbatimBucketStub) UploadContentImageVerbatim(_ context.Context, raw []byte, _, _ string) (*pb_common.MediaFull, error) {
	b.verbatimCalls = append(b.verbatimCalls, raw)
	return &pb_common.MediaFull{Id: 202}, nil
}

// verbatimDataURL wraps bytes the way the client sends them. The bucket is stubbed, so nothing ever
// decodes the raster — what these bytes decide is the ROUTE, and that decision is what is under test.
func verbatimDataURL(mediatype string, raw []byte) string {
	return "data:" + mediatype + ";base64," + base64.StdEncoding.EncodeToString(raw)
}

func verbatimPNG() []byte  { return append([]byte("\x89PNG\r\n\x1a\n"), "one-of-a-kind"...) }
func verbatimHEIC() []byte { return append([]byte("\x00\x00\x00\x20ftypheic"), "0000heic"...) }

// preserve_original=true routes to the verbatim method, with the payload's own bytes.
func TestUploadContentImagePreserveOriginalStoresTheBytesItWasGiven(t *testing.T) {
	fs := &verbatimBucketStub{}
	s := &Server{bucket: fs}
	raw := verbatimPNG()

	resp, err := s.UploadContentImage(context.Background(), &pb_admin.UploadContentImageRequest{
		RawB64Image:      verbatimDataURL("image/png", raw),
		PreserveOriginal: true,
	})
	require.NoError(t, err)
	assert.Equal(t, int32(202), resp.GetMedia().GetId(), "the row must come from the verbatim upload")

	require.Len(t, fs.verbatimCalls, 1, "preserve_original must reach UploadContentImageVerbatim")
	assert.Equal(t, raw, fs.verbatimCalls[0],
		"the verbatim path must receive the payload's own bytes — that is the entire promise")
	assert.Empty(t, fs.plainCalls, "the re-encoding path must not run as well")
}

// The other half: without the flag nothing about the old route changes, envelope included.
func TestUploadContentImageWithoutPreserveOriginalKeepsTheReEncodingRoute(t *testing.T) {
	fs := &verbatimBucketStub{}
	s := &Server{bucket: fs}
	dataURL := verbatimDataURL("image/png", verbatimPNG())

	resp, err := s.UploadContentImage(context.Background(), &pb_admin.UploadContentImageRequest{
		RawB64Image: dataURL,
	})
	require.NoError(t, err)
	assert.Equal(t, int32(101), resp.GetMedia().GetId(), "the row must come from the re-encoding upload")

	require.Len(t, fs.plainCalls, 1, "the default must still be UploadContentImage")
	assert.Equal(t, dataURL, fs.plainCalls[0], "the data url must reach the bucket untouched")
	assert.Empty(t, fs.verbatimCalls, "nothing may be stored verbatim without the flag")
}

// HEIC has no verbatim path. The refusal is the feature being honest about the size of its promise,
// so it must be actionable — and it must happen before a single byte is uploaded.
func TestUploadContentImagePreserveOriginalRefusesHEICClearly(t *testing.T) {
	fs := &verbatimBucketStub{}
	s := &Server{bucket: fs}

	_, err := s.UploadContentImage(context.Background(), &pb_admin.UploadContentImageRequest{
		RawB64Image:      verbatimDataURL("image/heic", verbatimHEIC()),
		PreserveOriginal: true,
	})
	require.Error(t, err)
	assert.Equal(t, codes.InvalidArgument, status.Code(err),
		"a format this path cannot keep is the caller's business, not a server fault")

	msg := status.Convert(err).Message()
	assert.Contains(t, msg, "image/heic", "the refusal must name what was sent")
	assert.Contains(t, msg, "preserve_original", "and the flag that caused it")
	assert.Contains(t, strings.ToLower(msg), "without preserve_original", "and the way forward")

	assert.Empty(t, fs.verbatimCalls, "the refusal must land before the bucket is touched")
	assert.Empty(t, fs.plainCalls, "and it must not silently fall back to a re-encode")
}

// A payload that is not the agreed envelope is refused the same way on both routes' terms: the flag
// does not change what a client has to send, so it must not turn a malformed request into a 500.
func TestUploadContentImagePreserveOriginalRefusesAMalformedEnvelope(t *testing.T) {
	fs := &verbatimBucketStub{}
	s := &Server{bucket: fs}

	for name, payload := range map[string]string{
		"no data url envelope": base64.StdEncoding.EncodeToString(verbatimPNG()),
		"not base64":           "data:image/png;base64,!!!!",
		"empty payload":        "data:image/png;base64,",
	} {
		t.Run(name, func(t *testing.T) {
			_, err := s.UploadContentImage(context.Background(), &pb_admin.UploadContentImageRequest{
				RawB64Image:      payload,
				PreserveOriginal: true,
			})
			require.Error(t, err)
			assert.Equal(t, codes.InvalidArgument, status.Code(err))
		})
	}
	assert.Empty(t, fs.verbatimCalls)
	assert.Empty(t, fs.plainCalls)
}

// The bytes decide, not the label: a client that mislabels a PNG still gets the verbatim route,
// because what can be stored 1:1 is a property of the payload. The bucket sniffs again and is the
// authority; this only proves the gate does not take the client's word for it.
func TestUploadContentImagePreserveOriginalTrustsTheBytesNotTheDeclaredType(t *testing.T) {
	fs := &verbatimBucketStub{}
	s := &Server{bucket: fs}

	_, err := s.UploadContentImage(context.Background(), &pb_admin.UploadContentImageRequest{
		RawB64Image:      verbatimDataURL("image/heic", verbatimPNG()),
		PreserveOriginal: true,
	})
	require.NoError(t, err)
	require.Len(t, fs.verbatimCalls, 1)
}
