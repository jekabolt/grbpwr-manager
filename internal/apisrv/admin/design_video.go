package admin

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/jekabolt/grbpwr-manager/internal/designgen"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	pb_common "github.com/jekabolt/grbpwr-manager/proto/gen/common"
	"github.com/shopspring/decimal"
	"google.golang.org/grpc/codes"
)

// ═══ THE VIDEO DOOR (B-32): ONE PICTURE OF THE CARD + WORDS → A 5-SECOND CLIP, runblob Kling ════════
//
// The playground's «Image to Video» tile takes the same door as every paid kind (StartDesignRun) with
// its own shape: `params.video.source_media_id` (the picture, must be this card's or a fresh upload,
// must be a PICTURE — the non-picture gate reads it through designRunInputMediaRefs), `ask` as the
// prompt (REQUIRED, ≤ designgen.VideoMaxPromptRunes), a duration the route sells (5) and the slug the
// server FREEZES from the panel's `video.generate` route row. Everything is refused BEFORE StartRun
// reserves the day's money; the kind gate refuses a keyless runblob as kind_not_available, in words.
//
// THE RESERVE is RUNBLOB_VIDEO_CEILING_USD per clip (designgen.Config.VideoCeiling), read live through
// the SAME function the worker's route reads (SetDesignVideoRoute ← designgen.VideoRouteFunc): the
// door and the submit see one number and one slug. runblob's price at submit is the truth; the worker
// logs a price above the reserve (never refuses — the job is bought).

// SetDesignVideoRoute hands the door the live video route (app.go, designgen.VideoRouteFunc). Called
// before serving, beside SetDesignThreedRoute; nil = a server without a video route, which reserves the
// table's default and freezes Kling's default slug.
func (s *Server) SetDesignVideoRoute(route func() designgen.VideoRoute) {
	s.designVideoRoute = route
}

// designVideoRouteNow — the live route, or the zero value.
func (s *Server) designVideoRouteNow() designgen.VideoRoute {
	if s.designVideoRoute == nil {
		return designgen.VideoRoute{}
	}
	return s.designVideoRoute()
}

// designVideoRunEstimate — what a video run reserves: the configured ceiling × outputs (one). The
// table row (designPriceEstimate, designgen.DefaultVideoCeilingUSD) stands when the route states no
// positive number — a server without the route, or a ceiling left at zero.
func (s *Server) designVideoRunEstimate(kind string, outputs int) (decimal.NullDecimal, bool) {
	if kind != entity.DesignRunKindVideo {
		return decimal.NullDecimal{}, false
	}
	per := designPriceEstimate[kind]
	if c := s.designVideoRouteNow().CeilingUSD; c.IsPositive() {
		per = c
	}
	if outputs < 1 {
		outputs = 1
	}
	return decimal.NullDecimal{Decimal: per.Mul(decimal.NewFromInt(int64(outputs))), Valid: true}, true
}

// designRefuseUnworkableVideo — the shape of a video run, on EFFECTIVE params (a rerun that cannot work
// does not work however it was spoken): one source picture, words, the one duration, a Kling slug.
func designRefuseUnworkableVideo(ask string, params *pb_common.DesignRunParams) error {
	v := params.GetVideo()
	if v.GetSourceMediaId() <= 0 {
		return designRefusal(codes.InvalidArgument, entity.DesignErrorCodeOneSourcePicture,
			"a video is animated from exactly one picture, and this run names none: put it in "+
				"params.video.source_media_id. Nothing was reserved and nothing was charged", nil)
	}
	if n := len(params.GetExtraInputMediaIds()); n > 0 || params.GetFreeform() != nil {
		// ONE LIST PER FACT (the freeform doctrine): the picture travels in params.video, and a second
		// list beside it would be a claim the worker never reads.
		return designRefusal(codes.InvalidArgument, entity.DesignErrorCodeOneSourcePicture,
			fmt.Sprintf("a video run reads its one picture from params.video.source_media_id and nothing "+
				"else; this run also names %d in params.extra_input_media_ids (or a params.freeform). Nothing "+
				"was reserved and nothing was charged", n),
			map[string]string{"named": strconv.Itoa(n)})
	}
	words := strings.TrimSpace(ask)
	if words == "" {
		return designRefusal(codes.InvalidArgument, entity.DesignErrorCodeWordsRequired,
			"the video route needs to be told what moves: Kling takes a prompt of 1–2500 characters, and "+
				"this run has none. Nothing was reserved and nothing was charged", nil)
	}
	if n := len([]rune(words)); n > designgen.VideoMaxPromptRunes {
		return designRefusal(codes.InvalidArgument, entity.DesignErrorCodeWordsRequired,
			fmt.Sprintf("the words are %d characters; the video route takes at most %d. Nothing was reserved "+
				"and nothing was charged", n, designgen.VideoMaxPromptRunes),
			map[string]string{"runes": strconv.Itoa(n), "max": strconv.Itoa(designgen.VideoMaxPromptRunes)})
	}
	if d := v.GetDuration(); d != 0 && d != designgen.VideoDurationSeconds {
		return designRefusal(codes.InvalidArgument, entity.DesignErrorCodeUnknownOption,
			fmt.Sprintf("params.video.duration %d — this route sells %d-second clips only (0 = the same). "+
				"Nothing was reserved and nothing was charged", d, designgen.VideoDurationSeconds),
			map[string]string{"duration": strconv.Itoa(int(d))})
	}
	if m := strings.TrimSpace(v.GetModel()); m != "" && !strings.HasPrefix(m, "kling_") {
		return designRefusal(codes.InvalidArgument, entity.DesignErrorCodeUnknownOption,
			fmt.Sprintf("params.video.model %q is not a Kling video slug (kling_*) — leave it empty and the "+
				"route row's model is used. Nothing was reserved and nothing was charged", m),
			map[string]string{"model": m})
	}
	return nil
}

// designFreezeVideoModel — THE SLUG THE CLIP IS BOUGHT WITH IS WRITTEN INTO THE RUN before the money,
// like an image run's engine (designFreezeImageModel): the route row's kling_* slug, else Kling's own
// default. A stated slug (a rerun of a frozen run, a client that names one) stands. The worker sends the
// frozen slug (job.VideoModel), so the history says what was bought even after the panel's row moves.
func (s *Server) designFreezeVideoModel(kind string, params *pb_common.DesignRunParams) {
	if kind != entity.DesignRunKindVideo {
		return
	}
	if params.GetVideo() == nil {
		params.Video = &pb_common.DesignVideoParams{}
	}
	if strings.TrimSpace(params.Video.GetModel()) != "" {
		return
	}
	if m := s.designVideoRouteNow().Model; m != "" {
		params.Video.Model = m
		return
	}
	params.Video.Model = designgen.DefaultVideoModel
}

// designVideoRefs — the input snapshot of a video run: the one picture it animates, and nothing of the
// card (the worker's referenceList for the kind is exactly this).
func designVideoRefs(params *pb_common.DesignRunParams) []*pb_common.DesignInputRef {
	out := []*pb_common.DesignInputRef{}
	if id := params.GetVideo().GetSourceMediaId(); id > 0 {
		out = append(out, &pb_common.DesignInputRef{MediaId: id, Note: "the picture to animate (the first frame)"})
	}
	return out
}
