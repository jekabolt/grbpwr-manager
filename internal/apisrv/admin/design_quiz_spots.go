package admin

import (
	"encoding/json"
	"math"
	"strconv"
	"strings"
	"unicode/utf16"
	"unicode/utf8"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	pb_admin "github.com/jekabolt/grbpwr-manager/proto/gen/admin"
)

// Moodboard quiz spots (99-SPOTS): a picture question may point at 1–3 places IN its picture, drawn
// by the client as numbered rings on the anchored board tile. The points come from the same quiz
// call (no second round trip); the server gates them so a doubtful spot is dropped, never the
// question.

// designQuizSpotsRule — the FIELDS line for "spots" (99-SPOTS §3), next to "picture".
const designQuizSpotsRule = `spots: on a picture question about a target or detail picture, the 1 to 3 places IN THAT PICTURE the question is about: {"label": the place in the question's own words (1–4 words), "x": 0–1000 from the left edge as the viewer sees it, "y": 0–1000 from the top, "scale": "zone" for a part (sleeve, yoke, the run of a hem) or "detail" for a small thing (a stitch line, a button, a label)}. Point at the centre of the place; for an edge or a seam, the middle of its visible run. Omit spots when the question is about the whole picture (silhouette, proportion, colour, fabric look, what to match) or when you cannot place it — a wrong spot is worse than none.`

const (
	designQuizMaxSpots          = 3
	designQuizSpotMax           = 1000 // x, y are 0..designQuizSpotMax across the picture
	designQuizMaxSpotLabelWords = 4
	designQuizMaxSpotLabelRunes = 48
	// designQuizSpotMinGap — two spots closer than this (4% of the frame, in 0..1000 units) are one.
	designQuizSpotMinGap = 40
)

// designQuizWholePictureAspects — picture-question aspects about the whole picture (99-SPOTS §1.2):
// pic_<id>_match / _mood / _material carry no spots; an aspect that names a part (pic_<id>_match_collar)
// keeps them.
var designQuizWholePictureAspects = map[string]bool{"match": true, "mood": true, "material": true}

// designQuizSpotsAllowed — the role and whole-picture gates (99-SPOTS §1.1–1.2): only a picture
// question, only on a target or detail picture (material is a swatch, mood and unmarked pictures are
// atmosphere), never on a whole-picture aspect.
func designQuizSpotsAllowed(mediaID int, role entity.TechCardMediaRole, decisionKey string) bool {
	if mediaID == 0 {
		return false
	}
	if role != entity.TechCardMediaRoleTarget && role != entity.TechCardMediaRoleDetail {
		return false
	}
	aspect := strings.TrimPrefix(decisionKey, "pic_"+strconv.Itoa(mediaID)+"_")
	return !designQuizWholePictureAspects[aspect]
}

// designQuizRawSpots reads the model's "spots" leniently: a list of objects whose numbers may be
// ints, floats or strings; a legacy fraction (0.42) is read as 420. Anything unreadable is skipped
// here with a coordinate of -1 so the shape gate drops it.
func designQuizRawSpots(raw json.RawMessage) []entity.DesignQuizSpot {
	if len(raw) == 0 {
		return nil
	}
	var items []json.RawMessage
	if json.Unmarshal(raw, &items) != nil {
		return nil
	}
	out := make([]entity.DesignQuizSpot, 0, len(items))
	for _, it := range items {
		var r struct {
			Label string          `json:"label"`
			X     json.RawMessage `json:"x"`
			Y     json.RawMessage `json:"y"`
			Scale string          `json:"scale"`
		}
		if json.Unmarshal(it, &r) != nil {
			continue
		}
		out = append(out, entity.DesignQuizSpot{
			Label: r.Label, X: designQuizSpotCoord(r.X), Y: designQuizSpotCoord(r.Y), Scale: r.Scale,
		})
	}
	return out
}

// designQuizSpotCoord — one coordinate as 0..1000; -1 when it is not a number. A value written with a
// decimal point and at most 1 is a fraction of the picture (0.42 → 420).
func designQuizSpotCoord(raw json.RawMessage) int {
	s := strings.TrimSpace(string(raw))
	if strings.HasPrefix(s, `"`) {
		var str string
		if json.Unmarshal(raw, &str) != nil {
			return -1
		}
		s = strings.TrimSpace(str)
	}
	f, err := strconv.ParseFloat(s, 64)
	if err != nil || math.IsNaN(f) || math.IsInf(f, 0) {
		return -1
	}
	if strings.ContainsAny(s, ".eE") && f >= 0 && f <= 1 {
		f *= designQuizSpotMax
	}
	return int(math.Round(f))
}

// designQuizCleanSpots — the shape gate (99-SPOTS §1.3): x, y in 0..1000, a label of 1–4 words,
// scale zone|detail (anything else reads as zone, the larger, more forgiving ring), spots closer
// than 4% of the frame to an earlier one dropped, at most 3. nil when none survive.
func designQuizCleanSpots(in []entity.DesignQuizSpot) []entity.DesignQuizSpot {
	var out []entity.DesignQuizSpot
	for _, s := range in {
		if len(out) == designQuizMaxSpots {
			break
		}
		if s.X < 0 || s.X > designQuizSpotMax || s.Y < 0 || s.Y > designQuizSpotMax {
			continue
		}
		label := designOneLine(s.Label)
		if n := len(strings.Fields(label)); n == 0 || n > designQuizMaxSpotLabelWords ||
			utf8.RuneCountInString(label) > designQuizMaxSpotLabelRunes {
			continue
		}
		scale := strings.ToLower(strings.TrimSpace(s.Scale))
		if scale != entity.DesignQuizSpotDetail {
			scale = entity.DesignQuizSpotZone
		}
		near := false
		for _, o := range out {
			if math.Hypot(float64(s.X-o.X), float64(s.Y-o.Y)) < designQuizSpotMinGap {
				near = true
				break
			}
		}
		if near {
			continue
		}
		out = append(out, entity.DesignQuizSpot{Label: label, X: s.X, Y: s.Y, Scale: scale})
	}
	return out
}

// designQuizSpotAt — where label sits inside question, case-insensitively, as a UTF-16 code unit
// offset (the client's JS string index); -1 when it is not there.
func designQuizSpotAt(question, label string) int32 {
	if label == "" {
		return -1
	}
	for i := range question { // rune starts
		if len(question)-i < len(label) {
			break
		}
		if strings.EqualFold(question[i:i+len(label)], label) {
			return int32(len(utf16.Encode([]rune(question[:i]))))
		}
	}
	return -1
}

func designQuizSpotsToPb(question string, in []entity.DesignQuizSpot) []*pb_admin.DesignQuizSpot {
	if len(in) == 0 {
		return nil
	}
	out := make([]*pb_admin.DesignQuizSpot, 0, len(in))
	for _, s := range in {
		out = append(out, &pb_admin.DesignQuizSpot{
			Label: s.Label, X: int32(s.X), Y: int32(s.Y), Scale: s.Scale, At: designQuizSpotAt(question, s.Label),
		})
	}
	return out
}

// designQuizSpotsFromPb — the client's echo; "at" is output only and ignored.
func designQuizSpotsFromPb(in []*pb_admin.DesignQuizSpot) []entity.DesignQuizSpot {
	out := make([]entity.DesignQuizSpot, 0, len(in))
	for _, s := range in {
		if s == nil {
			continue
		}
		out = append(out, entity.DesignQuizSpot{Label: s.GetLabel(), X: int(s.GetX()), Y: int(s.GetY()), Scale: s.GetScale()})
	}
	return out
}
