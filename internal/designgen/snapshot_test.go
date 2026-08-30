package designgen

import (
	"context"
	"database/sql"
	"strings"
	"testing"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/stretchr/testify/require"
)

func media(ids ...int) fakeMedia {
	m := fakeMedia{byID: map[int]entity.MediaFull{}}
	for _, id := range ids {
		m.byID[id] = entity.MediaFull{
			Id:        id,
			MediaItem: entity.MediaItem{FullSizeMediaURL: "https://cdn.example/m/" + itoa(id) + ".png"},
		}
	}
	return m
}

func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	var b []byte
	for i > 0 {
		b = append([]byte{byte('0' + i%10)}, b...)
		i /= 10
	}
	return string(b)
}

// TestMoodboardNeverReachesTheProvider — W-15, and it is the whole guarantee.
//
// The screen promises that the moodboard is the mood and not the prompt; a promise on a screen is
// not a guarantee. The guarantee is that the snapshot reader has no field for `mood` at all, so a
// moodboard picture cannot be sent even by a caller that wanted to. The frozen JSON below carries
// mood callouts on media 500 and 501, and neither may appear anywhere in the job.
func TestMoodboardNeverReachesTheProvider(t *testing.T) {
	inputs := entity.RawJSON(`{
	  "garment_note": "boxy overshirt",
	  "fit": "oversized",
	  "mood": {
	    "note": "brutalist workwear, ninety percent grey",
	    "callouts": [
	      {"media_id": 500, "text": "this collar"},
	      {"media_id": 501, "text": "this hem"}
	    ]
	  },
	  "refs": [{"media_id": 11, "role": "silhouette", "note": "the shape"}],
	  "slots": [{"view_key": "front", "media_id": 12}]
	}`)
	r := testRun(1, entity.DesignRunKindFlat)
	r.Inputs = inputs
	r.Ask = sql.NullString{String: "draw the flat", Valid: true}

	job, err := buildJob(context.Background(), media(11, 12, 500, 501), r, "medium")
	require.NoError(t, err)

	for _, u := range job.References {
		require.NotContains(t, u, "/500.", "a moodboard picture reached the provider")
		require.NotContains(t, u, "/501.", "a moodboard picture reached the provider")
	}
	require.Len(t, job.References, 2)
	require.NotContains(t, job.Prompt, "brutalist workwear",
		"the moodboard note is not the prompt either")
	require.NotContains(t, job.Prompt, "this collar")
	// …and what a person DID put in references is there.
	require.Contains(t, job.Prompt, "boxy overshirt")
	require.Contains(t, job.Prompt, "the shape")
}

// TestBenchPlatesComeFirstAndFrontIsFirstOfThose. Order is meaning on the 3D route: Meshy reads
// image_urls[0] as the primary frontal reference, so a snapshot that happens to hold the back first
// would produce a model built the wrong way round.
func TestBenchPlatesComeFirstAndFrontIsFirstOfThose(t *testing.T) {
	r := testRun(1, entity.DesignRunKindThreed)
	r.Inputs = entity.RawJSON(`{
	  "refs": [{"media_id": 90}],
	  "slots": [
	    {"view_key": "side_r", "media_id": 4},
	    {"view_key": "back",   "media_id": 2},
	    {"view_key": "front",  "media_id": 1},
	    {"view_key": "side_l", "media_id": 3}
	  ]
	}`)
	job, err := buildJob(context.Background(), media(1, 2, 3, 4, 90), r, "medium")
	require.NoError(t, err)
	require.Equal(t, []string{
		"https://cdn.example/m/1.png",
		"https://cdn.example/m/2.png",
		"https://cdn.example/m/3.png",
		"https://cdn.example/m/4.png",
		"https://cdn.example/m/90.png",
	}, job.References)
}

// TestDeletedReferenceIsNotSent. Its id survives in the snapshot so the panel can say which input
// disappeared; sending it would be a 404 at the provider in the middle of a paid call.
func TestDeletedReferenceIsNotSent(t *testing.T) {
	r := testRun(1, entity.DesignRunKindFlat)
	r.Inputs = entity.RawJSON(`{"refs":[{"media_id":11,"deleted":true},{"media_id":12}]}`)
	job, err := buildJob(context.Background(), media(11, 12), r, "medium")
	require.NoError(t, err)
	require.Equal(t, []string{"https://cdn.example/m/12.png"}, job.References)
}

// TestSnapshotKeysAreSnakeCase.
//
// The writer is protojson with UseProtoNames: true. If it were ever switched to the protojson
// default, every field below would decode as a silent zero — no error, no missing picture, just a
// prompt that says less than it should and references that are never sent. This asserts the reader
// against the spelling the writer actually uses.
func TestSnapshotKeysAreSnakeCase(t *testing.T) {
	p := parseParams(entity.RawJSON(`{"views":["front","back"],"layout":"per_view","extra_input_media_ids":[7],"fix_targets":["back"],"colour":{"words":"olive","fabric_media_id":9}}`))
	require.Equal(t, []string{"front", "back"}, p.Views)
	require.Equal(t, layoutPerView, p.Layout)
	require.Equal(t, []int{7}, p.ExtraInputMediaIDs)
	require.Equal(t, []string{"back"}, p.FixTargets)
	require.NotNil(t, p.Colour)
	require.Equal(t, 9, p.Colour.FabricMediaID)

	in := parseInputs(entity.RawJSON(`{"garment_note":"g","refs":[{"media_id":1,"callouts":[{"text":"here"}]}],"slots":[{"view_key":"front","media_id":2}]}`))
	require.Equal(t, "g", in.GarmentNote)
	require.Len(t, in.Refs, 1)
	require.Len(t, in.Refs[0].Callouts, 1)
	require.Equal(t, entity.DesignViewFront, in.Slots[0].ViewKey)
}

// TestBrokenSnapshotDoesNotStopAPaidJob — the store parses leniently for the same reason: what is
// lost is context in the prompt, not the run somebody is waiting for.
func TestBrokenSnapshotDoesNotStopAPaidJob(t *testing.T) {
	r := testRun(1, entity.DesignRunKindFlat)
	r.Inputs = entity.RawJSON(`{"refs": [ this is not json`)
	r.Ask = sql.NullString{String: "draw it", Valid: true}
	job, err := buildJob(context.Background(), media(), r, "medium")
	require.NoError(t, err)
	require.Contains(t, job.Prompt, "draw it")
}

// TestPerViewIsOnePaidCallPerView. The provider's `n` returns n VARIANTS OF ONE PROMPT, so reading
// it as "how many views" would buy three copies of the same drawing and deliver no back or side.
func TestPerViewIsOnePaidCallPerView(t *testing.T) {
	calls := imageCalls(Job{
		Prompt: "flat of a shirt", Layout: layoutPerView,
		Views: []string{"front", "back", "side_l"}, Outputs: 3,
	})
	require.Len(t, calls, 3)
	for i, v := range []string{"front", "back", "side_l"} {
		require.Equal(t, 1, calls[i].n, "each view is its own prompt, never an extra variant")
		require.Equal(t, v, calls[i].view)
		require.True(t, strings.Contains(calls[i].prompt, v), "the model has to be told which view")
	}
}

// TestCompositeIsOneCallAndClaimsNoView. A composite carries several views and therefore has no
// single one; naming the first would hand the splitter a wrong hint.
func TestCompositeIsOneCallAndClaimsNoView(t *testing.T) {
	calls := imageCalls(Job{
		Prompt: "one sheet", Layout: layoutOne,
		Views: []string{"front", "back", "side_l"}, Outputs: 1,
	})
	require.Len(t, calls, 1)
	require.Equal(t, 1, calls[0].n)
	require.Empty(t, calls[0].view)
}

// TestFlatNeverAsksForABackgroundTheModelDoesNotKnow.
//
// This test replaces one that asserted the opposite. The old one required `transparent`, which the
// default model (gpt-image-2) does not accept at all — so it guarded a value that turns every flat
// run into a 400, and it was green the whole time because the package's tests use a stub.
//
// The assertion is written as «never transparent» rather than «equals opaque» on purpose: the point
// is not the particular word, it is that we must not order a background outside the model's own
// vocabulary. If the family ever regains transparency this test says so instead of forbidding it.
func TestFlatNeverAsksForABackgroundTheModelDoesNotKnow(t *testing.T) {
	got := backgroundFor(entity.DesignRunKindFlat)
	require.NotEqual(t, "transparent", got,
		"gpt-image-2 lists background as auto|opaque; asking for transparency is a 400 on every flat")
	require.Contains(t, []string{"auto", "opaque"}, got,
		"a flat must state a background the model knows, and state it rather than leave it to auto")
	require.Empty(t, backgroundFor(entity.DesignRunKindRender),
		"a render is a scene and keeps the provider's own default")
}

// TestCompositeNeverBuysVariantsByAccident.
//
// `n` is variants of one prompt; requested_outputs is how many pictures the history expects. A run
// laid out as `one` with three views is ONE composite sheet carrying all three — reading its
// requested_outputs as `n` would buy three whole composites at three times the price, and deliver
// nothing anybody asked for.
func TestCompositeNeverBuysVariantsByAccident(t *testing.T) {
	calls := imageCalls(Job{
		Prompt: "one sheet", Layout: layoutOne,
		Views: []string{"front", "back", "side_l"}, Outputs: 3,
	})
	require.Len(t, calls, 1)
	require.Equal(t, 1, calls[0].n)
}

// TestTheWordsAROUNDTheReferencesReachTheModEL — W-3 and W-7 as the worker sees them.
//
// The three fields this asserts are the ones the band spent a wave filing into the snapshot: the
// garment's own description, the human's note on ONE reference, and the markup pinned on it. Each
// is a separate hop — column, entity, snapshot, prompt — and each of them was, until this wave,
// capable of going missing without a single error: the prompt would simply say less.
//
// The moodboard line is here as the boundary, not as decoration: `garment_note` is NOT the board's
// note, and a fix that merged the two would send to the model exactly what W-15 forbids.
func TestTheWordsAroundTheReferencesReachTheModel(t *testing.T) {
	r := testRun(1, entity.DesignRunKindFlat)
	r.Inputs = entity.RawJSON(`{
	  "garment_note": "GARMENT-oversized boxy shirt",
	  "mood": {"note": "BOARD-brutalist workwear"},
	  "refs": [{
	    "media_id": 11,
	    "role": "front",
	    "note": "NOTE-only the collar",
	    "callouts": [{"media_id": 11, "text": "MARK-topstitch here"}]
	  }]
	}`)
	job, err := buildJob(context.Background(), media(11), r, "medium")
	require.NoError(t, err)

	require.Contains(t, job.Prompt, "GARMENT-oversized boxy shirt",
		"the garment description rides into EVERY run; without it the model draws an unnamed thing")
	require.Contains(t, job.Prompt, "NOTE-only the collar",
		"eight references with roles and no notes state eight sides and no intent")
	require.Contains(t, job.Prompt, "MARK-topstitch here",
		"«our prompt: the pictures, the descriptions and the markup» — the markup is the third third")
	require.NotContains(t, job.Prompt, "BOARD-brutalist workwear",
		"the board is the mood, not the prompt: the reader has no field for it at all")
}
