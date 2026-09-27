package admin

import (
	"bytes"
	"context"
	"image"
	"image/jpeg"
	"image/png"
	"io"
	"testing"

	"github.com/jekabolt/grbpwr-manager/internal/bucket"
	"github.com/jekabolt/grbpwr-manager/internal/dependency/mocks"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/jekabolt/grbpwr-manager/internal/fal"
	pb_common "github.com/jekabolt/grbpwr-manager/proto/gen/common"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

// ═══ B-14 — kind=inpaint AT THE LIVE DOOR: the mask is validated BEFORE the reservation ═══

const (
	inpaintSourceID = designRefMediaID // 100, a PNG url
	inpaintMaskID   = 310
	inpaintMaskURL  = "https://cdn.grbpwr.com/grbpwr-com/design/2026/september/mask-310-og.png"
)

// maskPNG — a w×h mask, black with `white` pixels painted white (from the top-left corner).
func maskPNG(t *testing.T, w, h, white int) []byte {
	t.Helper()
	img := image.NewGray(image.Rect(0, 0, w, h))
	for i := 0; i < white && i < len(img.Pix); i++ {
		img.Pix[i] = 0xff
	}
	var buf bytes.Buffer
	require.NoError(t, png.Encode(&buf, img))
	return buf.Bytes()
}

// inpaintRig — the rig's media store answers the picture and the mask with these stored sizes, and
// its bucket serves `mask` for any key.
func inpaintRig(srcW, srcH, maskW, maskH int, mask []byte) func(t *testing.T, rig *designRunRig) {
	return func(t *testing.T, rig *designRunRig) {
		rig.repo.ExpectedCalls = pgDropCalls(rig.repo.ExpectedCalls, "Media")
		media := mocks.NewMockMedia(t)
		rig.repo.EXPECT().Media().Return(media).Maybe()
		all := designFormatMedia(nil)
		all[inpaintSourceID] = entity.MediaFull{Id: inpaintSourceID, MediaItem: entity.MediaItem{
			FullSizeMediaURL: designPNGURL, FullSizeWidth: srcW, FullSizeHeight: srcH}}
		all[inpaintMaskID] = entity.MediaFull{Id: inpaintMaskID, MediaItem: entity.MediaItem{
			FullSizeMediaURL: inpaintMaskURL, FullSizeWidth: maskW, FullSizeHeight: maskH}}
		media.EXPECT().GetMediaByIds(mock.Anything, mock.Anything).Return(all, nil).Maybe()
		rig.srv.bucket = maskKeyedFiles(t, mask)
	}
}

// maskKeyedFiles — a bucket that serves `mask` at the mask's key and ANOTHER file (the picture's own
// bytes, never the mask's) at every other key: since G-03 r2 (Codex 7) the door compares a legacy
// picture's object with the mask's bytes, so a rig serving one file everywhere would be a picture
// uploaded twice.
func maskKeyedFiles(t *testing.T, mask []byte) *mocks.MockFileStore {
	maskKey, err := bucket.ObjectKeyFromStoredURL(inpaintMaskURL)
	require.NoError(t, err)
	picture := []byte("the picture's own file, not the mask")
	files := mocks.NewMockFileStore(t)
	files.EXPECT().GetManagedObject(mock.Anything, mock.Anything).RunAndReturn(
		func(_ context.Context, key string) (io.ReadCloser, int64, error) {
			if key == maskKey {
				return io.NopCloser(bytes.NewReader(mask)), int64(len(mask)), nil
			}
			return io.NopCloser(bytes.NewReader(picture)), int64(len(picture)), nil
		}).Maybe()
	return files
}

func inpaintParams(src, mask int32) *pb_common.DesignRunParams {
	return &pb_common.DesignRunParams{Inpaint: &pb_common.DesignInpaintParams{SourceMediaId: src, MaskMediaId: mask}}
}

func falInpaintRows(t *testing.T) []falDoorRow {
	routes := withFalRoutes(fal.Config{})
	good := maskPNG(t, 64, 64, 10)
	black := maskPNG(t, 64, 64, 0)
	var jpg bytes.Buffer
	require.NoError(t, jpeg.Encode(&jpg, image.NewGray(image.Rect(0, 0, 64, 64)), nil))
	type setup = func(*testing.T, *designRunRig)
	return []falDoorRow{
		{name: "legal: stated sizes agree", kind: entity.DesignRunKindInpaint, ask: "a brass zip pull",
			params: inpaintParams(inpaintSourceID, inpaintMaskID),
			setup:  []setup{routes, inpaintRig(64, 64, 64, 64, good)}, price: "0.15"},
		{name: "legal: the picture's size is unknown, the mask is a painted PNG", kind: entity.DesignRunKindInpaint,
			ask: "a brass zip pull", params: inpaintParams(inpaintSourceID, inpaintMaskID),
			setup: []setup{routes, inpaintRig(0, 0, 0, 0, good)}, price: "0.15"},
		{name: "a fill tariff with its ceiling raises the reserve", kind: entity.DesignRunKindInpaint, ask: "x",
			params: inpaintParams(inpaintSourceID, inpaintMaskID),
			setup: []setup{withFalRoutes(fal.Config{UnitUSDFill: 0.1, UnitsCeilingFill: 3}),
				inpaintRig(64, 64, 64, 64, good)}, price: "0.3"},
		{name: "no picture", kind: entity.DesignRunKindInpaint, ask: "x", params: inpaintParams(0, inpaintMaskID),
			setup: []setup{routes, inpaintRig(64, 64, 64, 64, good)}, want: entity.DesignErrorCodeNoSourcePicture},
		{name: "no mask", kind: entity.DesignRunKindInpaint, ask: "x", params: inpaintParams(inpaintSourceID, 0),
			setup: []setup{routes, inpaintRig(64, 64, 64, 64, good)}, want: entity.DesignErrorCodeMaskRequired},
		{name: "the mask is the picture", kind: entity.DesignRunKindInpaint, ask: "x",
			params: inpaintParams(inpaintSourceID, inpaintSourceID),
			setup:  []setup{routes, inpaintRig(64, 64, 64, 64, good)}, want: entity.DesignErrorCodeMaskInvalid},
		{name: "no words", kind: entity.DesignRunKindInpaint, params: inpaintParams(inpaintSourceID, inpaintMaskID),
			setup: []setup{routes, inpaintRig(64, 64, 64, 64, good)}, want: entity.DesignErrorCodeWordsRequired},
		{name: "a second list of pictures", kind: entity.DesignRunKindInpaint, ask: "x",
			params: func() *pb_common.DesignRunParams {
				p := inpaintParams(inpaintSourceID, inpaintMaskID)
				p.ExtraInputMediaIds = []int32{designPlateMediaID}
				return p
			}(),
			setup: []setup{routes, inpaintRig(64, 64, 64, 64, good)}, want: entity.DesignErrorCodeOneListPerFact},
		{name: "the rows' sizes disagree", kind: entity.DesignRunKindInpaint, ask: "x",
			params: inpaintParams(inpaintSourceID, inpaintMaskID),
			setup:  []setup{routes, inpaintRig(64, 64, 128, 64, good)}, want: entity.DesignErrorCodeMaskSizeMismatch},
		{name: "the mask's header disagrees with the picture", kind: entity.DesignRunKindInpaint, ask: "x",
			params: inpaintParams(inpaintSourceID, inpaintMaskID),
			setup:  []setup{routes, inpaintRig(64, 64, 0, 0, maskPNG(t, 4, 4, 1))}, want: entity.DesignErrorCodeMaskSizeMismatch},
		{name: "a mask that is not a PNG", kind: entity.DesignRunKindInpaint, ask: "x",
			params: inpaintParams(inpaintSourceID, inpaintMaskID),
			setup:  []setup{routes, inpaintRig(64, 64, 64, 64, jpg.Bytes())}, want: entity.DesignErrorCodeMaskInvalid,
			why: "not a PNG"},
		{name: "nothing painted", kind: entity.DesignRunKindInpaint, ask: "x",
			params: inpaintParams(inpaintSourceID, inpaintMaskID),
			setup:  []setup{routes, inpaintRig(64, 64, 64, 64, black)}, want: entity.DesignErrorCodeMaskEmpty},
		{name: "a picture too small to crop", kind: entity.DesignRunKindInpaint, ask: "x",
			params: inpaintParams(inpaintSourceID, inpaintMaskID),
			setup:  []setup{routes, inpaintRig(50, 50, 50, 50, maskPNG(t, 50, 50, 3))}, want: entity.DesignErrorCodeSourceTooSmall},
		{name: "params.image on a retouch", kind: entity.DesignRunKindInpaint, ask: "x",
			params: func() *pb_common.DesignRunParams {
				p := inpaintParams(inpaintSourceID, inpaintMaskID)
				p.Image = &pb_common.DesignImageOptions{Model: "openai/gpt-image-2"}
				return p
			}(),
			setup: []setup{routes, inpaintRig(64, 64, 64, 64, good)}, want: entity.DesignErrorCodeImageOptionsForbidden},
		{name: "params.inpaint on a freeform run", kind: entity.DesignRunKindFreeform, ask: "x",
			params: func() *pb_common.DesignRunParams {
				p := ffParams("free")
				p.Inpaint = &pb_common.DesignInpaintParams{SourceMediaId: inpaintSourceID, MaskMediaId: inpaintMaskID}
				return p
			}(),
			want: entity.DesignErrorCodeInpaintForbidden},
		{name: "a fill tariff without its ceiling", kind: entity.DesignRunKindInpaint, ask: "x",
			params: inpaintParams(inpaintSourceID, inpaintMaskID),
			setup:  []setup{withFalRoutes(fal.Config{UnitUSDFill: 0.05}), inpaintRig(64, 64, 64, 64, good)},
			want:   entity.DesignErrorCodeRouteReserveUnbounded},
	}
}

// TestTheInpaintDoorREFUSES_BEFORE_THE_STORE — every row through the real StartDesignRun; a refusal
// never reaches StartRun. MUTATIONS (measured red): designgen.MaskThreshold = 0 → an all-black mask
// passes (the «nothing painted» row reaches the store); drop the designRefuseUnusableMask call → the
// size/PNG/empty rows reach it; drop the inpaint branch of designRefuseWindowSourceTooSmall → the
// too-small row reaches the mask check instead.
func TestTheInpaintDoorREFUSES_BEFORE_THE_STORE(t *testing.T) {
	for _, c := range falInpaintRows(t) {
		t.Run(c.name, func(t *testing.T) {
			rig := newDesignRunRig(t, designMoodCard(), designBandWith(true))
			for _, s := range c.setup {
				s(t, rig)
			}
			rig.design.EXPECT().AssertMediaNotForeign(mock.Anything, designRunCardID, mock.Anything).Return(nil).Maybe()
			req := designStartRequest(c.kind)
			req.Params = c.params
			req.Ask = c.ask
			_, err := rig.srv.StartDesignRun(designRunCtx(), req)
			if c.want == "" {
				require.NoError(t, err)
				require.NotNil(t, rig.sent)
				require.Equal(t, 1, rig.sent.RequestedOutputs)
				require.Equal(t, c.price, rig.sent.PriceEstimate.Decimal.String())
				// The snapshot records the picture AND the mask — both travel — and nothing of the card.
				raw := string(rig.sent.Inputs)
				require.Contains(t, raw, `"media_id":100`)
				require.Contains(t, raw, `"media_id":310`)
				require.NotContains(t, raw, "MOODWORDS")
				require.NotContains(t, raw, `"slots"`, "a retouch takes no bench plate")
				return
			}
			_, md := errorReason(t, err)
			require.Equalf(t, c.want, md["reason"], "%v", err)
			if c.why != "" {
				require.Contains(t, md["why"], c.why)
			}
			require.Nil(t, rig.sent, "refused before the store: nothing reserved")
		})
	}
}

// TestTheMaskRidesTheINPUT_DOORS — the mask and the picture are in designRunInputMediaRefs, so «not a
// picture», «display only» and «hidden» see both.
func TestTheMaskRidesTheINPUT_DOORS(t *testing.T) {
	refs := designRunInputMediaRefs(inpaintParams(11, 12), nil)
	got := map[int]string{}
	for _, r := range refs {
		got[r.ID] = r.Where
	}
	require.Equal(t, "params.inpaint.source_media_id", got[11])
	require.Equal(t, "params.inpaint.mask_media_id", got[12])
}
