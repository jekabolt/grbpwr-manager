package designgen

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/stretchr/testify/require"
)

// ═══ M7b (07.10): THE FLAT PROMPT, BYTE FOR BYTE ═══
//
// The construction code left the flat route (the switch, the sentence generator, the frozen
// inputs.joins). Its removal must not move one byte of a flat prompt: these goldens were written by
// the code BEFORE the removal (beta 8d6e8f3 — the M3 «B0» counting sentence, 36860a6) and the code
// after it must reproduce them exactly. The fixtures are raw frozen JSON on
// purpose: a snapshot that still carries `joins` (every flat frozen before M7b) and a legacy
// `straps` run parse the same way before and after, and must draw the photos route's words.
//
// Rewrite the goldens only for an intended wording change: FLAT_PROMPT_GOLDEN_WRITE=1 go test
// ./internal/designgen/ -run TestFlatPromptBytes.
//
// MUTATIONS IT CATCHES: any wording drift of the craft (identify / counting sentence / style /
// layout / side-facing / mood / accepted views); a frozen join list or a straps mode reaching the
// prompt again; the missing-views line or the reference captions moving.

// flatPromptJoins — a frozen join list as the door wrote it before M7b (card 38's shape: straps, an
// open back, a sheer layer, absences).
const flatPromptJoins = `{"items":[` +
	`{"id":"neck_bind","kind":"binding","from":"NP_R","via":["CFN"],"to":"NP_L","type":"crew","layer":0,"visibility":"visible"},` +
	`{"id":"strap_L","kind":"strap","from":"NP_L","via":["CBN..UB_C:0.6"],"to":"UA_R..MB_R:0.3","layer":0,"visibility":"visible"},` +
	`{"id":"strap_R","kind":"strap","from":"NP_R","via":["CBN..UB_C:0.6"],"to":"UA_L..MB_L:0.3","layer":0,"visibility":"visible"},` +
	`{"id":"inner_v","kind":"edge","from":"NP_L","via":["BUST_C"],"to":"NP_R","layer":1,"visibility":"through"},` +
	`{"id":"back_open","kind":"opening","bounded_by":["strap_L","strap_R"]}],` +
	`"layers":[{"index":0,"name":"outer front","sheer":true,"face":"front"},{"index":1,"name":"inner bib","face":"front"}],` +
	`"absences":["no back neckline","no sleeves"],"confirmed":true}`

type flatPromptFixture struct {
	name, params, inputs string
	media                []int
}

var flatPromptFixtures = []flatPromptFixture{
	{"views4_photos",
		`{"views":["front","back","side_l","side_r"],"layout":"one"}`,
		`{"garment_note":"garment: blazer","refs":[{"media_id":11,"role":"front","note":"buttoned"},{"media_id":12,"role":"back"},{"media_id":13,"role":"side_l"}]}`,
		[]int{11, 12, 13}},
	{"views4_photos_no_side_photo",
		`{"views":["front","back","side_l","side_r"],"layout":"one"}`,
		`{"garment_note":"garment: tank top","refs":[{"media_id":11,"role":"front"},{"media_id":12,"role":"back","note":"open back"}]}`,
		[]int{11, 12}},
	{"views2_photos",
		`{"views":["front","back"],"layout":"one"}`,
		`{"garment_note":"garment: shirt","refs":[{"media_id":11,"role":"front"},{"media_id":12,"role":"back"}]}`,
		[]int{11, 12}},
	{"views2_per_view",
		`{"views":["front","back"],"layout":"per_view"}`,
		`{"garment_note":"garment: shirt","refs":[{"media_id":11,"role":"front"},{"media_id":12,"role":"back"}]}`,
		[]int{11, 12}},
	{"views4_mood",
		`{"views":["front","back","side_l","side_r"],"layout":"one"}`,
		`{"garment_note":"garment: dress","refs":[{"media_id":11,"role":"front"},{"media_id":14,"role":"mood"}]}`,
		[]int{11, 14}},
	{"views4_words_only",
		`{"views":["front","back","side_l","side_r"],"layout":"one"}`,
		`{"garment_note":"garment: trousers"}`,
		nil},
	{"hand_flat",
		`{"views":["front","back","side_l","side_r"],"layout":"one","flat":{"mode":"hand_flat","structure_refs":[{"media_id":7,"role":"front_flat"},{"media_id":8,"role":"back_flat"}]}}`,
		`{"garment_note":"garment: jacket","refs":[{"media_id":7,"role":"front_flat"},{"media_id":8,"role":"back_flat"},{"media_id":11,"role":"front","note":"the waist"}]}`,
		[]int{7, 8, 11}},
	{"detail_accepted_views",
		`{"views":["detail"],"layout":"one","detail_slot_ids":[5]}`,
		`{"garment_note":"garment: top","refs":[{"media_id":1,"role":"detail"}],"slots":[{"view_key":"back","media_id":12},{"view_key":"front","media_id":11},{"view_key":"detail","slot_id":5,"detail_name":"strap"}]}`,
		[]int{1, 11, 12}},
	// M14: the person's own flat words under the class line (TechCard.flat_words, typed in FLAT ›
	// WORDS). Written for M14; every fixture above stays byte for byte what it was.
	{"views4_photos_human_words",
		`{"views":["front","back","side_l","side_r"],"layout":"one"}`,
		`{"garment_note":"garment: blazer\nno topstitching on the lapel or the hem\ntwo buttons","refs":[{"media_id":11,"role":"front","note":"buttoned"},{"media_id":12,"role":"back"},{"media_id":13,"role":"side_l"}]}`,
		[]int{11, 12, 13}},
	{"detail_human_words",
		`{"views":["detail"],"layout":"one","detail_slot_ids":[5]}`,
		`{"garment_note":"garment: top\nthe straps cross once at the back","refs":[{"media_id":1,"role":"detail"}],"slots":[{"view_key":"back","media_id":12},{"view_key":"front","media_id":11},{"view_key":"detail","slot_id":5,"detail_name":"strap"}]}`,
		[]int{1, 11, 12}},
}

// flatPromptOf — the prompt the worker sends for a frozen flat run (buildJob, as the queue does).
func flatPromptOf(t *testing.T, params, inputs string, ids []int) string {
	t.Helper()
	run := testRun(1, entity.DesignRunKindFlat)
	run.Params = entity.RawJSON(params)
	run.Inputs = entity.RawJSON(inputs)
	job, err := buildJob(context.Background(), media(ids...), nil, run, "medium")
	require.NoError(t, err)
	return job.Prompt
}

// withFrozenJoins — the same snapshot with a join list frozen into it (every flat run before M7b).
func withFrozenJoins(inputs string) string {
	return strings.TrimSuffix(inputs, "}") + `,"joins":` + flatPromptJoins + "}"
}

func TestFlatPromptBytes(t *testing.T) {
	write := os.Getenv("FLAT_PROMPT_GOLDEN_WRITE") == "1"
	for _, f := range flatPromptFixtures {
		t.Run(f.name, func(t *testing.T) {
			got := flatPromptOf(t, f.params, f.inputs, f.media)
			path := filepath.Join("testdata", "flat_prompt_bytes", f.name+".txt")
			if write {
				require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
				require.NoError(t, os.WriteFile(path, []byte(got), 0o644))
			}
			want, err := os.ReadFile(path)
			require.NoError(t, err)
			require.Equal(t, string(want), got, "the flat prompt moved by a byte")
			// A frozen join list (every snapshot before M7b) says nothing to the image model.
			require.Equal(t, got, flatPromptOf(t, f.params, withFrozenJoins(f.inputs), f.media), "a frozen join list reached the prompt")
		})
	}
	// The garment views carry the M3 «B0» counting sentence; a legacy straps run draws the photos
	// route's words exactly, with or without its frozen list.
	views := flatPromptOf(t, flatPromptFixtures[0].params, flatPromptFixtures[0].inputs, flatPromptFixtures[0].media)
	require.Contains(t, views, "Count what the photos show — buttons, pockets, seams, panels, vents — and draw exactly that many: a missing element and an added element are equally wrong.")
	straps := `{"views":["front","back","side_l","side_r"],"layout":"one","flat":{"mode":"straps"}}`
	require.Equal(t, views, flatPromptOf(t, straps, flatPromptFixtures[0].inputs, flatPromptFixtures[0].media))
	require.Equal(t, views, flatPromptOf(t, straps, withFrozenJoins(flatPromptFixtures[0].inputs), flatPromptFixtures[0].media))
	for _, gone := range []string{"Landmark ruler", "JOIN LIST", "LAYERS", "ABSENT", "CHECK EVERY VIEW", "designer:"} {
		require.NotContains(t, views, gone)
	}
}
