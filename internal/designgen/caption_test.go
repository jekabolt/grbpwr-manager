package designgen

import (
	"context"
	"strings"
	"testing"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/stretchr/testify/require"
)

// captionLines pulls the numbered caption lines out of a composed prompt, in order.
func captionLines(t *testing.T, prompt string) []string {
	t.Helper()
	var out []string
	for _, l := range strings.Split(prompt, "\n") {
		if strings.HasPrefix(l, "- image ") {
			out = append(out, l)
		}
	}
	return out
}

// TestCaptionNumberKIsImageNumberK — the owner's «должно быть помечено какое медиа и что на нем»,
// as an assertion of CORRESPONDENCE rather than of presence.
//
// Before the refCaption list existed this was false three ways at once, in one job: a bench plate
// occupied position 1 with no caption at all, a reference with no role and no note sent its
// picture but produced no line, and a media row that failed to resolve dropped its picture but
// would have kept its words. The probe therefore builds exactly that job — slot + bare ref +
// captioned refs + extra + fabric swatch, with one reference unresolvable — and demands that the
// k-th caption line names the k-th attached picture, for every k, with the dropped picture's
// words gone entirely.
func TestCaptionNumberKIsImageNumberK(t *testing.T) {
	r := testRun(1, entity.DesignRunKindRender)
	r.Inputs = entity.RawJSON(`{
	  "refs": [
	    {"media_id": 11},
	    {"media_id": 13, "role": "front", "note": "NOTE-13-lost-picture"},
	    {"media_id": 12, "role": "back", "note": "NOTE-12-collar"}
	  ],
	  "slots": [{"view_key": "front", "media_id": 1}]
	}`)
	r.Params = entity.RawJSON(`{"extra_input_media_ids":[7],"colour":{"words":"olive","fabric_media_id":9}}`)

	// Media 13 is deliberately missing from the resolver: its picture cannot attach.
	job, err := buildJob(context.Background(), media(1, 11, 12, 7, 9), r, "medium")
	require.NoError(t, err)

	expected := map[string]string{
		"1":  "current state of the garment — front view",
		"11": "reference image",
		"12": "back — NOTE-12-collar",
		"7":  "additional reference image",
		"9":  "fabric photograph — the material this garment is made of: read its weave, texture, sheen and drape from here",
	}

	lines := captionLines(t, job.Prompt)
	require.Len(t, job.References, 5, "four resolvable inputs plus the swatch attach; the lost one does not")
	require.Len(t, lines, len(job.References),
		"every attached picture has exactly one caption line — a picture without words shifts every number after it")

	for k, u := range job.References {
		// The fake resolver's urls are https://cdn.example/m/<id>.png — recover the id.
		id := strings.TrimSuffix(u[strings.LastIndex(u, "/")+1:], ".png")
		require.Contains(t, lines[k], "- image "+itoa(k+1)+": ",
			"caption %d must carry its own number", k+1)
		require.Contains(t, lines[k], expected[id],
			"caption %d must describe references[%d] (media %s), not some other picture", k+1, k, id)
	}

	require.NotContains(t, job.Prompt, "NOTE-13-lost-picture",
		"words about a picture the model cannot see are an instruction about nothing")
}

// TestDuplicateMediaKeepsBothSourcesWords — one picture named by two sources is ONE attachment in
// its first position, and neither source's words are lost to the deduplication.
func TestDuplicateMediaKeepsBothSourcesWords(t *testing.T) {
	r := testRun(1, entity.DesignRunKindRender)
	r.Inputs = entity.RawJSON(`{
	  "refs": [{"media_id": 5, "role": "front", "note": "NOTE-5-neckline"}],
	  "slots": [{"view_key": "front", "media_id": 5}]
	}`)
	job, err := buildJob(context.Background(), media(5), r, "medium")
	require.NoError(t, err)

	require.Len(t, job.References, 1)
	lines := captionLines(t, job.Prompt)
	require.Len(t, lines, 1)
	require.Contains(t, lines[0], "current state of the garment — front view",
		"the slot's words survive the merge")
	require.Contains(t, lines[0], "NOTE-5-neckline",
		"the reference's words survive the merge")
}

// TestPromptIsRecordedBeforeAnyMoney — the record-then-spend half of the stored-text decision.
//
// The history row must carry the prompt BEFORE the first attempt row can exist, because the
// attempt row is where money starts: a worker that dies between the two leaves a run whose text
// is known and whose money is zero, never the reverse.
func TestPromptIsRecordedBeforeAnyMoney(t *testing.T) {
	st := &fakeStore{}
	img := &fakeProvider{name: "image", out: okOutcome(1, 0.04)}
	w := testWorker(st, media(11), newFakeSink(ContentTypePNG), Providers{Image: img})

	r := testRun(1, entity.DesignRunKindFlat)
	r.Inputs = entity.RawJSON(`{"refs":[{"media_id":11,"role":"front","note":"NOTE-collar"}]}`)
	require.NoError(t, w.execute(context.Background(), r, "tok"))

	require.NotEmpty(t, st.events, "the pass wrote nothing at all")
	require.Equal(t, "record_prompt", st.events[0],
		"the prompt write must come before every attempt row: %v", st.events)
	require.Len(t, st.recordedPrompts, 1)
	require.Len(t, img.calls, 1)
	require.Equal(t, img.calls[0].Prompt, st.recordedPrompts[0],
		"the recorded text and the sent text must be the SAME string, not two compositions")
	require.Contains(t, st.recordedPrompts[0], "- image 1: front — NOTE-collar",
		"what the person later reads is the numbered caption block the model got")
}

// TestPromptRecordFailureBuysNothing — the other edge of record-then-spend: a pass that cannot
// write the text down must not spend, so no provider ever receives words the history row does not
// carry.
func TestPromptRecordFailureBuysNothing(t *testing.T) {
	st := &fakeStore{recordErr: errBoom}
	img := &fakeProvider{name: "image", out: okOutcome(1, 0.04)}
	w := testWorker(st, media(11), newFakeSink(ContentTypePNG), Providers{Image: img})

	r := testRun(1, entity.DesignRunKindFlat)
	r.Inputs = entity.RawJSON(`{"refs":[{"media_id":11}]}`)
	err := w.execute(context.Background(), r, "tok")
	require.Error(t, err, "a pass that cannot record what it is about to send backs off")
	require.Empty(t, img.calls, "no provider call may precede the history write")
	require.Empty(t, st.started, "no attempt row, therefore no money")
}
