package admin

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"image/png"
	"strconv"
	"strings"

	"github.com/jekabolt/grbpwr-manager/internal/bucket"
	"github.com/jekabolt/grbpwr-manager/internal/designgen"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	pb_common "github.com/jekabolt/grbpwr-manager/proto/gen/common"
	"google.golang.org/grpc/codes"
)

// ═══ PLAYGROUND phase 3 — THE MASK RETOUCH AT THE DOOR (kind=inpaint, tile 10's mask route) ═══
//
// The mask is validated BEFORE the reservation (owner default, 07-PHASE3 §6): a mask that is missing,
// the wrong size, not a PNG, or has nothing painted on it would otherwise hold the day's money until
// the worker refused it. Everything here is free.

// designRefuseUnworkableInpaint — the shape of a mask retouch on EFFECTIVE params, in order: a
// picture, a mask, two different ids, words, and no second list of pictures.
func designRefuseUnworkableInpaint(ask string, params *pb_common.DesignRunParams) error {
	in := params.GetInpaint()
	if in.GetSourceMediaId() <= 0 {
		return designRefusal(codes.InvalidArgument, entity.DesignErrorCodeNoSourcePicture,
			"a mask retouch needs the picture it retouches: name it in params.inpaint.source_media_id. "+
				"Nothing was reserved and nothing was charged", nil)
	}
	if in.GetMaskMediaId() <= 0 {
		return designRefusal(codes.InvalidArgument, entity.DesignErrorCodeMaskRequired,
			"a mask retouch needs its painted mask: upload the PNG and name it in params.inpaint.mask_media_id. "+
				"Nothing was reserved and nothing was charged", nil)
	}
	if in.GetMaskMediaId() == in.GetSourceMediaId() {
		return designRefusal(codes.InvalidArgument, entity.DesignErrorCodeMaskInvalid,
			"params.inpaint.mask_media_id names the picture itself — the mask is a separate PNG, white where "+
				"the picture changes. Nothing was reserved and nothing was charged",
			map[string]string{"why": "the mask is the source"})
	}
	if strings.TrimSpace(ask) == "" {
		return designRefusal(codes.InvalidArgument, entity.DesignErrorCodeWordsRequired,
			"a mask retouch paints what you describe: say what goes into the painted zone in `ask`. "+
				"Nothing was reserved and nothing was charged", nil)
	}
	if params.GetFreeform() != nil || len(params.GetExtraInputMediaIds()) > 0 {
		return designRefusal(codes.InvalidArgument, entity.DesignErrorCodeOneListPerFact,
			"a mask retouch names its picture in params.inpaint and nowhere else — clear params.freeform and "+
				"params.extra_input_media_ids. Nothing was reserved and nothing was charged", nil)
	}
	return nil
}

// designMaskDoorMaxPixels — the largest mask the door decodes (12 MP: ≤ 48 MB of NRGBA, 12 MB for the
// grey PNG the client paints). See designRefuseUnusableMask.
const designMaskDoorMaxPixels = 12_000_000

// designRefuseUnusableMask — THE MASK ITSELF, read once, before the reservation:
//
//   - both rows' stored full-size dimensions, when stated, must agree → mask_size_mismatch;
//   - the mask's bytes must be a readable PNG — the client uploads it verbatim
//     (UploadContentImageRequest.preserve_original), which is bit-exact; a re-encoded WebP is not,
//     and is refused → mask_invalid{why};
//   - the PNG header must agree with the picture's stated size → mask_size_mismatch;
//   - something must be painted (designgen.MaskPaintedPixels, the worker's own predicate) → mask_empty.
//
// ONE object read of a small PNG inside the RPC, bounded by designSplitMaxSourceBytes and by the
// process's pixel budget (header first). The worker re-reads and re-decides (the second lock) — a
// picture with no stored dimensions reaches only it.
func (s *Server) designRefuseUnusableMask(ctx context.Context, kind string, params *pb_common.DesignRunParams) error {
	if kind != entity.DesignRunKindInpaint {
		return nil
	}
	srcID := int(params.GetInpaint().GetSourceMediaId())
	maskID := int(params.GetInpaint().GetMaskMediaId())
	byID, err := s.repo.Media().GetMediaByIds(ctx, []int{srcID, maskID})
	if err != nil {
		return designError(ctx, "failed to read the picture and the mask of a retouch", err, nil)
	}
	invalid := func(why string) error {
		return designRefusal(codes.InvalidArgument, entity.DesignErrorCodeMaskInvalid,
			fmt.Sprintf("the mask %d cannot be used: %s. Upload the painted mask as a PNG. Nothing was "+
				"reserved and nothing was charged", maskID, why),
			map[string]string{"why": why, "mask_media_id": strconv.Itoa(maskID)})
	}
	mismatch := func(src, mask string) error {
		return designRefusal(codes.InvalidArgument, entity.DesignErrorCodeMaskSizeMismatch,
			fmt.Sprintf("the mask is %s and the picture is %s — the mask must be the picture's exact size. "+
				"Nothing was reserved and nothing was charged", mask, src),
			map[string]string{"source": src, "mask": mask})
	}
	dims := func(w, h int) string { return strconv.Itoa(w) + "x" + strconv.Itoa(h) }

	src, ok := byID[srcID]
	if !ok {
		return designRefuseMissingSource(srcID, "params.inpaint.source_media_id") // G-03, Codex 8
	}
	m, ok := byID[maskID]
	if !ok || strings.TrimSpace(m.FullSizeMediaURL) == "" {
		return invalid("the mask picture does not exist")
	}
	// ⚠ «THE MASK IS THE PICTURE» IS A FACT OF THE CONTENT, NOT OF THE ID (G-03, Codex 7). The same
	// bright PNG uploaded twice is two rows with one content hash, and passes every other check (the
	// size agrees, it is a PNG, its bright pixels «paint» it). The stored hashes are compared first;
	// the mask's own bytes are hashed below for a row that has none.
	sameFile := invalid("it is the picture itself under another id (the same file) — the mask is a separate PNG, white where the picture changes")
	if m.ContentHash.Valid && src.ContentHash.Valid && m.ContentHash.String != "" &&
		strings.EqualFold(m.ContentHash.String, src.ContentHash.String) {
		return sameFile
	}
	srcKnown := src.FullSizeWidth > 0 && src.FullSizeHeight > 0
	if srcKnown && m.FullSizeWidth > 0 && m.FullSizeHeight > 0 &&
		(m.FullSizeWidth != src.FullSizeWidth || m.FullSizeHeight != src.FullSizeHeight) {
		return mismatch(dims(src.FullSizeWidth, src.FullSizeHeight), dims(m.FullSizeWidth, m.FullSizeHeight))
	}
	raw, err := s.designFetchObject(ctx, m.FullSizeMediaURL)
	if err != nil {
		return designError(ctx, "failed to read the mask of a retouch", err, nil)
	}
	if len(raw) < 8 || !bytes.Equal(raw[:8], []byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1A, '\n'}) {
		return invalid("it is not a PNG (upload it verbatim, not re-encoded)")
	}
	if src.ContentHash.Valid && src.ContentHash.String != "" {
		sum := sha256.Sum256(raw)
		if strings.EqualFold(hex.EncodeToString(sum[:]), src.ContentHash.String) {
			return sameFile
		}
	}
	cfg, err := png.DecodeConfig(bytes.NewReader(raw))
	if err != nil {
		return invalid("it is not a readable PNG")
	}
	if !bucket.ImageWithinBudget(cfg.Width, cfg.Height) {
		return invalid("its header declares " + dims(cfg.Width, cfg.Height) + " pixels, past the size any picture here may have")
	}
	if srcKnown && (cfg.Width != src.FullSizeWidth || cfg.Height != src.FullSizeHeight) {
		return mismatch(dims(src.FullSizeWidth, src.FullSizeHeight), dims(cfg.Width, cfg.Height))
	}
	// ⚠ THE RPC DECODES A MASK OF AT MOST designMaskDoorMaxPixels (G-03, Fable m-2). A mask is the size
	// of its picture — up to 40 MP by the bucket's budget, 160 MB of NRGBA per call, several admins at
	// once. Past the cap the door stops at the header (PNG, size, budget: all checked above) and the
	// worker's second lock decides «nothing painted» — free and terminal there too, only a tick later.
	if int64(cfg.Width)*int64(cfg.Height) > designMaskDoorMaxPixels {
		return nil
	}
	img, err := png.Decode(bytes.NewReader(raw))
	if err != nil {
		return invalid("it is not a readable PNG")
	}
	if designgen.MaskPaintedPixels(img) == 0 {
		return designRefusal(codes.InvalidArgument, entity.DesignErrorCodeMaskEmpty,
			"nothing is painted on the mask — paint the zone to change (white) before retouching. Nothing was "+
				"reserved and nothing was charged",
			map[string]string{"mask_media_id": strconv.Itoa(maskID)})
	}
	return nil
}

// designInpaintRefs — the snapshot's record of what a mask retouch sends: the picture and its mask,
// both of which travel to the provider (image_url, mask_url). Like designFreeformRefs, rebuilt from
// params on a rerun rather than narrowed from the parent's snapshot.
func designInpaintRefs(params *pb_common.DesignRunParams) []*pb_common.DesignInputRef {
	in := params.GetInpaint()
	out := []*pb_common.DesignInputRef{}
	if id := in.GetSourceMediaId(); id > 0 {
		out = append(out, &pb_common.DesignInputRef{MediaId: id, Note: "the picture to retouch"})
	}
	if id := in.GetMaskMediaId(); id > 0 && id != in.GetSourceMediaId() {
		out = append(out, &pb_common.DesignInputRef{MediaId: id, Note: "the painted mask (white = repaint)"})
	}
	return out
}
