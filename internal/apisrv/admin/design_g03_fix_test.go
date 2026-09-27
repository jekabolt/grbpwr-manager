package admin

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"io"
	"testing"

	"github.com/jekabolt/grbpwr-manager/internal/dependency/mocks"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/jekabolt/grbpwr-manager/internal/fal"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

// ═══ G-03 fix pass — the door side: Fable M-1, m-1, m-2; Codex 7, 8 ═══

// falMedia — the rig's media store answers exactly `rows` (no defaults: an id missing here has no
// row), and its bucket serves `object` for any key.
func falMedia(rows map[int]entity.MediaFull, object []byte) func(t *testing.T, rig *designRunRig) {
	return func(t *testing.T, rig *designRunRig) {
		rig.repo.ExpectedCalls = pgDropCalls(rig.repo.ExpectedCalls, "Media")
		media := mocks.NewMockMedia(t)
		rig.repo.EXPECT().Media().Return(media).Maybe()
		media.EXPECT().GetMediaByIds(mock.Anything, mock.Anything).RunAndReturn(
			func(_ context.Context, ids []int) (map[int]entity.MediaFull, error) {
				out := map[int]entity.MediaFull{}
				for _, id := range ids {
					if m, ok := rows[id]; ok {
						out[id] = m
					}
				}
				return out, nil
			}).Maybe()
		files := mocks.NewMockFileStore(t)
		files.EXPECT().GetManagedObject(mock.Anything, mock.Anything).RunAndReturn(
			func(_ context.Context, _ string) (io.ReadCloser, int64, error) {
				return io.NopCloser(bytes.NewReader(object)), int64(len(object)), nil
			}).Maybe()
		rig.srv.bucket = files
	}
}

func pngRow(id, w, h int, url, hash string) entity.MediaFull {
	m := entity.MediaFull{Id: id, MediaItem: entity.MediaItem{FullSizeMediaURL: url, FullSizeWidth: w, FullSizeHeight: h}}
	if hash != "" {
		m.ContentHash = sql.NullString{String: hash, Valid: true}
	}
	return m
}

func sha(raw []byte) string {
	s := sha256.Sum256(raw)
	return hex.EncodeToString(s[:])
}

func g03DoorRows(t *testing.T) []falDoorRow {
	routes := withFalRoutes(fal.Config{})
	type setup = func(*testing.T, *designRunRig)
	good := maskPNG(t, 64, 64, 10)
	bigEmpty := maskPNG(t, 4000, 3001, 0) // 12.004 MP: past the door's decode cap
	smallEmpty := maskPNG(t, 3000, 3000, 0)
	return []falDoorRow{
		// Codex 8 — a source id with no media row is refused at the door, not reserved.
		{name: "extend: the source has no row", kind: entity.DesignRunKindExtend,
			params: extendParams("21:9", 4242), setup: []setup{routes, falMedia(map[int]entity.MediaFull{}, nil)},
			want: entity.DesignErrorCodeNoSourcePicture, why: "does not exist"},
		{name: "extend: a legacy row with no dimensions still passes", kind: entity.DesignRunKindExtend,
			params: extendParams("21:9", designRefMediaID),
			setup:  []setup{routes, falMedia(map[int]entity.MediaFull{designRefMediaID: pngRow(designRefMediaID, 0, 0, designPNGURL, "")}, nil)},
			price:  "0.12"},
		{name: "inpaint: the source has no row", kind: entity.DesignRunKindInpaint, ask: "x",
			params: inpaintParams(4242, inpaintMaskID),
			setup:  []setup{routes, falMedia(map[int]entity.MediaFull{inpaintMaskID: pngRow(inpaintMaskID, 64, 64, inpaintMaskURL, "")}, good)},
			want:   entity.DesignErrorCodeNoSourcePicture, why: "does not exist"},
		// Codex 7 — the mask is the picture's own file under another id.
		{name: "inpaint: the mask row carries the picture's content hash", kind: entity.DesignRunKindInpaint, ask: "x",
			params: inpaintParams(inpaintSourceID, inpaintMaskID),
			setup: []setup{routes, falMedia(map[int]entity.MediaFull{
				inpaintSourceID: pngRow(inpaintSourceID, 64, 64, designPNGURL, "ab12"),
				inpaintMaskID:   pngRow(inpaintMaskID, 64, 64, inpaintMaskURL, "AB12"),
			}, good)},
			want: entity.DesignErrorCodeMaskInvalid, why: "the picture itself"},
		{name: "inpaint: the mask row has no hash, its bytes are the picture's", kind: entity.DesignRunKindInpaint, ask: "x",
			params: inpaintParams(inpaintSourceID, inpaintMaskID),
			setup: []setup{routes, falMedia(map[int]entity.MediaFull{
				inpaintSourceID: pngRow(inpaintSourceID, 64, 64, designPNGURL, sha(good)),
				inpaintMaskID:   pngRow(inpaintMaskID, 64, 64, inpaintMaskURL, ""),
			}, good)},
			want: entity.DesignErrorCodeMaskInvalid, why: "the picture itself"},
		{name: "inpaint: two different files pass", kind: entity.DesignRunKindInpaint, ask: "x",
			params: inpaintParams(inpaintSourceID, inpaintMaskID),
			setup: []setup{routes, falMedia(map[int]entity.MediaFull{
				inpaintSourceID: pngRow(inpaintSourceID, 64, 64, designPNGURL, "ab12"),
				inpaintMaskID:   pngRow(inpaintMaskID, 64, 64, inpaintMaskURL, sha(good)),
			}, good)},
			price: "0.15"},
		// Fable m-2 — the door decodes at most 12 MP of mask; past it the worker's second lock decides.
		{name: "inpaint: an empty mask past the decode cap reaches the worker's lock", kind: entity.DesignRunKindInpaint, ask: "x",
			params: inpaintParams(inpaintSourceID, inpaintMaskID),
			setup: []setup{routes, falMedia(map[int]entity.MediaFull{
				inpaintSourceID: pngRow(inpaintSourceID, 4000, 3001, designPNGURL, ""),
				inpaintMaskID:   pngRow(inpaintMaskID, 4000, 3001, inpaintMaskURL, ""),
			}, bigEmpty)},
			price: "0.15"},
		{name: "inpaint: an empty mask under the cap is still refused at the door", kind: entity.DesignRunKindInpaint, ask: "x",
			params: inpaintParams(inpaintSourceID, inpaintMaskID),
			setup: []setup{routes, falMedia(map[int]entity.MediaFull{
				inpaintSourceID: pngRow(inpaintSourceID, 3000, 3000, designPNGURL, ""),
				inpaintMaskID:   pngRow(inpaintMaskID, 3000, 3000, inpaintMaskURL, ""),
			}, smallEmpty)},
			want: entity.DesignErrorCodeMaskEmpty},
		// Fable m-1 — a FAL_MODEL_* slug with no body is not available (and not a reserve question).
		{name: "extend: a foreign outpaint slug", kind: entity.DesignRunKindExtend,
			params: extendParams("21:9", designRefMediaID),
			setup:  []setup{withFalRoutes(fal.Config{ModelOutpaint: "fal-ai/ideogram/v3/reframe"})},
			want:   designReasonKindUnavailable, why: ""},
		{name: "inpaint: a foreign fill slug", kind: entity.DesignRunKindInpaint, ask: "x",
			params: inpaintParams(inpaintSourceID, inpaintMaskID),
			setup:  []setup{withFalRoutes(fal.Config{ModelFill: "fal-ai/flux-lora/inpainting"}), inpaintRig(64, 64, 64, 64, good)},
			want:   designReasonKindUnavailable},
	}
}

// TestTheG03DoorRowsREFUSE_BEFORE_THE_STORE — every row through the real StartDesignRun.
// MUTATIONS (measured red): designRefuseExtendTarget back to `!ok → nil` → the extend «no row» row
// reaches the store; drop the ContentHash comparison → the stored-hash row; drop the raw-bytes sha →
// the no-hash row; raise designMaskDoorMaxPixels to 13e6 → the big empty mask is refused at the door;
// drop the Unsupported branch of designRefuseFalRouteUnbounded → route_reserve_unbounded instead.
func TestTheG03DoorRowsREFUSE_BEFORE_THE_STORE(t *testing.T) {
	for _, c := range g03DoorRows(t) {
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
				require.Equal(t, c.price, rig.sent.PriceEstimate.Decimal.String())
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

// TestAForeignFalSlugIsNOT_ADVERTISED — Fable m-1 on the band: run_kinds omits the kind whose slug
// has no body, exactly as it omits an unbounded one.
func TestAForeignFalSlugIsNOT_ADVERTISED(t *testing.T) {
	s := &Server{}
	s.SetDesignFalRoutes(falRoutesFor(fal.Config{ModelOutpaint: "fal-ai/ideogram/v3/reframe"}))
	require.False(t, s.designFalRouteBounded(entity.DesignRunKindExtend))
	require.True(t, s.designFalRouteBounded(entity.DesignRunKindInpaint))
}

// TestAnExtendOrInpaintRerunMAY_NOT_SWAP_ITS_PICTURE — Fable M-1 (the G-02 M-2 class, third
// instance): extend keeps its source; inpaint keeps its source and MAY change its mask (the «area»).
// MUTATION (measured red): drop the designRefuseFalRerunPictureSwap call in StartDesignRun → the live
// door row reaches the store; return nil at its top → every unit row.
func TestAnExtendOrInpaintRerunMAY_NOT_SWAP_ITS_PICTURE(t *testing.T) {
	extendParent := []byte(`{"extra_input_media_ids":[88],"extend":{"aspect_ratio":"21:9"}}`)
	inpaintParent := []byte(`{"inpaint":{"source_media_id":88,"mask_media_id":90}}`)

	err := designRefuseFalRerunPictureSwap(entity.DesignRunKindExtend, extendParams("16:9", 91), 5, extendParent)
	require.Equal(t, "rerun_changes_pictures", ffReason(t, err))
	_, md := errorReason(t, err)
	require.Equal(t, "91", md["added"])
	require.Equal(t, "88", md["dropped"])
	require.Equal(t, "5", md["rerun_of"])

	require.NoError(t, designRefuseFalRerunPictureSwap(entity.DesignRunKindExtend, extendParams("16:9", 88), 5, extendParent),
		"the same picture to another format is a legal rerun")
	require.NoError(t, designRefuseFalRerunPictureSwap(entity.DesignRunKindExtend, nil, 5, extendParent), "a silent rerun")
	require.NoError(t, designRefuseFalRerunPictureSwap(entity.DesignRunKindExtend, extendParams("16:9", 91), 0, nil), "not a rerun")
	require.NoError(t, designRefuseFalRerunPictureSwap(entity.DesignRunKindExtend, extendParams("16:9"), 5, extendParent),
		"no source named: one_source_picture's refusal, not this one")

	err = designRefuseFalRerunPictureSwap(entity.DesignRunKindInpaint, inpaintParams(91, 90), 7, inpaintParent)
	require.Equal(t, "rerun_changes_pictures", ffReason(t, err))
	require.NoError(t, designRefuseFalRerunPictureSwap(entity.DesignRunKindInpaint, inpaintParams(88, 95), 7, inpaintParent),
		"a new mask on the same picture is a re-marked area — legal")
	require.NoError(t, designRefuseFalRerunPictureSwap(entity.DesignRunKindFreeform, extendParams("16:9", 91), 5, extendParent),
		"other kinds have their own guards")

	// The live door: refused before the store.
	rig := newDesignRunRig(t, designMoodCard(), designBandWith(true))
	withFalRoutes(fal.Config{})(t, rig)
	rig.design.EXPECT().GetRun(mock.Anything, 5).Return(&entity.DesignRun{
		Id: 5, TechCardId: designRunCardID, Kind: entity.DesignRunKindExtend, Params: entity.RawJSON(extendParent),
	}, nil).Maybe()
	rig.design.EXPECT().AssertMediaNotForeign(mock.Anything, designRunCardID, mock.Anything).Return(nil).Maybe()
	req := designStartRequest(entity.DesignRunKindExtend)
	req.RerunOfRunId = 5
	req.Params = extendParams("16:9", designRefMediaID)
	_, err = rig.srv.StartDesignRun(designRunCtx(), req)
	require.Equal(t, "rerun_changes_pictures", ffReason(t, err))
	require.Nil(t, rig.sent, "nothing reserved")
}
