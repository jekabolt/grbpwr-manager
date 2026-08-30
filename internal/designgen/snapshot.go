package designgen

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
)

// Layouts of a run's output — the DesignRunParams.layout dictionary.
const (
	layoutOne     = "one"
	layoutPerView = "per_view"
)

// runParams / runInputs are EXACTLY THE FIELDS THE WORKER NEEDS out of the frozen snapshot, and
// nothing more. They are a second, narrow reader of the same JSON the store reads narrowly — not
// a duplicate of the contract — for the reason the store gives in wave2.go: a full protobuf decode
// here would drag the wire into a package that has no business knowing about it.
//
// ⚠ THE KEYS ARE snake_case BECAUSE THE WRITER USES protojson WITH UseProtoNames: true. Switching
// that writer to the protojson default would turn every field below into a silent zero: no error,
// no picture missing, just a prompt that says less than it should and references that are not
// sent. That is the failure this repo has already met once.
type runParams struct {
	Views              []string      `json:"views"`
	Layout             string        `json:"layout"`
	Colour             *colourRecipe `json:"colour"`
	ExtraInputMediaIDs []int         `json:"extra_input_media_ids"`
	FixTarget          string        `json:"fix_target"`
	FixTargets         []string      `json:"fix_targets"`
	Threed             *threedParams `json:"threed"`
}

type colourRecipe struct {
	Source        string `json:"source"`
	Code          string `json:"code"`
	Hex           string `json:"hex"`
	Words         string `json:"words"`
	FabricMediaID int    `json:"fabric_media_id"`
}

type threedParams struct {
	Presentation string `json:"presentation"`
	FitOverride  string `json:"fit_override"`
}

// runInputs is the frozen input snapshot.
//
// ⚠ `mood` IS ABSENT FROM THIS STRUCT ON PURPOSE, AND ITS ABSENCE IS THE W-15 GUARANTEE. The
// moodboard is the mood, not the prompt: a moodboard picture reaches a model only when a person
// moves it into REFERENCES. A screen can promise that; only a reader that cannot see the field can
// guarantee it. Adding a `Mood` field here would silently undo the requirement, which is why the
// requirement is enforced by a type rather than by a filter somebody could forget to apply.
type runInputs struct {
	GarmentNote string      `json:"garment_note"`
	Fit         string      `json:"fit"`
	Refs        []inputRef  `json:"refs"`
	Slots       []inputSlot `json:"slots"`
}

type inputRef struct {
	MediaID  int            `json:"media_id"`
	Role     string         `json:"role"`
	Note     string         `json:"note"`
	Deleted  bool           `json:"deleted"`
	Callouts []inputCallout `json:"callouts"`
}

type inputCallout struct {
	Text string `json:"text"`
}

type inputSlot struct {
	ViewKey    string `json:"view_key"`
	DetailName string `json:"detail_name"`
	MediaID    int    `json:"media_id"`
}

// parseParams / parseInputs decode LENIENTLY, exactly as the store does: a snapshot that will not
// parse must not stop a paid job from running. What is lost is context in the prompt, not the run.
func parseParams(raw entity.RawJSON) runParams {
	var p runParams
	if len(raw) > 0 {
		_ = json.Unmarshal(raw, &p)
	}
	return p
}

func parseInputs(raw entity.RawJSON) runInputs {
	var in runInputs
	if len(raw) > 0 {
		_ = json.Unmarshal(raw, &in)
	}
	return in
}

// referenceMediaIDs is EVERY picture this run is allowed to show a model, in a stable order.
//
// THREE SOURCES, ALL OF THEM CHOSEN BY A PERSON: the references panel, the bench plates a fix or a
// render was pointed at, and the extra media dropped into a render. The moodboard is not among
// them and cannot be — see runInputs.
//
// ORDER IS MEANING ON THE 3D ROUTE, where Meshy reads the first url as the front view. Slots
// therefore come first and are sorted front, back, side_l, side_r, detail rather than in whatever
// order the snapshot happens to hold them.
func referenceMediaIDs(p runParams, in runInputs) []int {
	slots := append([]inputSlot(nil), in.Slots...)
	sort.SliceStable(slots, func(i, j int) bool {
		return viewRank(slots[i].ViewKey) < viewRank(slots[j].ViewKey)
	})

	seen := map[int]struct{}{}
	var out []int
	add := func(id int) {
		if id <= 0 {
			return
		}
		if _, dup := seen[id]; dup {
			return
		}
		seen[id] = struct{}{}
		out = append(out, id)
	}
	for _, s := range slots {
		add(s.MediaID)
	}
	for _, r := range in.Refs {
		// A reference whose media row is gone is remembered by the snapshot but cannot be fetched
		// by anyone; sending its id would produce a 404 at the provider, mid-paid-call.
		if r.Deleted {
			continue
		}
		add(r.MediaID)
	}
	for _, id := range p.ExtraInputMediaIDs {
		add(id)
	}
	if p.Colour != nil {
		add(p.Colour.FabricMediaID)
	}
	return out
}

// viewRank orders the silhouette so the front comes first. Anything unnamed sorts last.
func viewRank(v string) int {
	switch v {
	case entity.DesignViewFront:
		return 0
	case entity.DesignViewBack:
		return 1
	case entity.DesignViewSideL:
		return 2
	case entity.DesignViewSideR:
		return 3
	case entity.DesignViewDetail:
		return 4
	default:
		return 5
	}
}

// composePrompt turns the frozen snapshot into the words that go to the model.
//
// IT READS THE SNAPSHOT, NEVER THE CARD. The run may be picked up minutes or hours after it was
// launched, and by then the card's description, fit and references may all have moved. A prompt
// assembled from live data would make the history row a lie: it would say what was asked and the
// model would have been told something else.
func composePrompt(run entity.DesignRun, p runParams, in runInputs) string {
	var b strings.Builder
	write := func(label, value string) {
		value = strings.TrimSpace(value)
		if value == "" {
			return
		}
		if b.Len() > 0 {
			b.WriteString("\n\n")
		}
		if label != "" {
			b.WriteString(label)
			b.WriteString(":\n")
		}
		b.WriteString(value)
	}

	if run.Ask.Valid {
		write("", run.Ask.String)
	}
	write("garment", in.GarmentNote)
	write("fit", in.Fit)

	if c := p.Colour; c != nil {
		var parts []string
		if c.Words != "" {
			parts = append(parts, c.Words)
		}
		if c.Code != "" {
			parts = append(parts, "colourway "+c.Code)
		}
		if c.Hex != "" {
			parts = append(parts, c.Hex)
		}
		write("colour", strings.Join(parts, ", "))
	}
	if t := p.Threed; t != nil {
		var parts []string
		if t.Presentation != "" {
			parts = append(parts, "presentation "+t.Presentation)
		}
		if t.FitOverride != "" {
			parts = append(parts, "fit "+t.FitOverride)
		}
		write("turntable", strings.Join(parts, ", "))
	}

	// The references speak for themselves: the role a person gave each picture, the note they
	// wrote on it, and the callouts they pinned on it. This is the whole of W-7's «our prompt: the
	// pictures, the descriptions and the markup» that is expressible in words.
	var refLines []string
	for _, r := range in.Refs {
		if r.Deleted {
			continue
		}
		line := strings.TrimSpace(strings.TrimSpace(r.Role) + " — " + strings.TrimSpace(r.Note))
		line = strings.Trim(line, "— ")
		var marks []string
		for _, c := range r.Callouts {
			if t := strings.TrimSpace(c.Text); t != "" {
				marks = append(marks, t)
			}
		}
		if len(marks) > 0 {
			line = strings.TrimSpace(line + " [" + strings.Join(marks, "; ") + "]")
		}
		if line != "" {
			refLines = append(refLines, "- "+line)
		}
	}
	write("references", strings.Join(refLines, "\n"))

	// A fix names what it is fixing. Both spellings are read — the frozen scalar of an older run
	// and the list a new one uses — because the old meaning has to stay readable forever.
	targets := p.FixTargets
	if len(targets) == 0 && p.FixTarget != "" {
		targets = []string{p.FixTarget}
	}
	if len(targets) > 0 {
		write("correct these views", strings.Join(targets, ", "))
	}

	// ─── THE CRAFT, LAST, AND ON THE FLAT ROUTE ONLY (flatprompt.go).
	//
	// LAST IS A DECISION, NOT AN ACCIDENT. The human context above legitimately carries colour
	// words (a colourway recipe), callout texts and free-form notes — and a flat must stay black
	// line art even when they say "olive". Where the two collide, the words closer to the end of
	// the prompt are the ones an image model obeys, so the craft — whose exclusion list IS the
	// non-negotiable half — speaks after every human word, never before. The reverse order would
	// end the prompt on "colour: olive" and hand the collision to the wrong side. It also keeps
	// the owner's own opening literal: "Turn the garment shown in the reference image…" is a
	// sentence written to operate on material already given, and here everything it operates on
	// (the ask, the garment, the fit, the references and their markup) has already been said.
	//
	// FLAT ONLY: the owner supplied these reference prompts for flats. A render is a picture of
	// a garment in a scene and a 3D run is a Meshy build — neither is described by "black vector
	// line art on a plain white background", so both keep the bare context above. The vector kind
	// is a redraw of an already-approved raster and draft_idea never reaches the worker; neither
	// takes the block either.
	if run.Kind == entity.DesignRunKindFlat {
		write("", flatCraft(p, in))
	}
	return b.String()
}

// viewPrompt is the per-call instruction on the per_view route, where each paid call is made for
// one named side and the model must be told which.
func viewPrompt(base, view string) string {
	if strings.TrimSpace(view) == "" {
		return base
	}
	return strings.TrimSpace(base + "\n\nview:\n" + view)
}

// buildJob assembles everything a provider needs out of one claimed run, resolving media ids into
// the public urls the provider will fetch itself.
//
// URLS RATHER THAN BYTES, DELIBERATELY. Our design pictures already live in a public bucket, so
// the provider downloads them directly and nothing passes through this process — which has half a
// gigabyte of RAM and a base64 image is the thing most likely to end it.
func buildJob(ctx context.Context, media mediaResolver, run entity.DesignRun, quality string) (Job, error) {
	p := parseParams(run.Params)
	in := parseInputs(run.Inputs)

	job := Job{
		RunID:      run.Id,
		TechCardID: run.TechCardId,
		Kind:       run.Kind,
		Prompt:     composePrompt(run, p, in),
		Views:      p.Views,
		Layout:     p.Layout,
		Outputs:    run.RequestedOutputs,
		Quality:    quality,
	}

	ids := referenceMediaIDs(p, in)
	if len(ids) == 0 {
		return job, nil
	}
	byID, err := media.GetMediaByIds(ctx, ids)
	if err != nil {
		return Job{}, fmt.Errorf("failed to resolve the input media of design run %d: %w", run.Id, err)
	}
	for _, id := range ids {
		m, ok := byID[id]
		if !ok {
			// The row went away between the snapshot and the pass. Skipping is right: the id
			// cannot be fetched by the provider either, and refusing the whole run over one
			// missing reference would throw away a job whose remaining inputs are intact.
			continue
		}
		if u := strings.TrimSpace(m.FullSizeMediaURL); u != "" {
			job.References = append(job.References, u)
		}
	}
	return job, nil
}
