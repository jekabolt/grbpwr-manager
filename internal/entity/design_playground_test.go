package entity

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Probes of the PLAYGROUND vocabulary (phase 2, B-01): the workflow keys, the preset lists that
// feed two different wire fields, and DesignWorkflowOf — the stamp the feed's SQL mirrors.

// The grid order is the owner's; the client lays the tiles out by this list.
func TestPlaygroundWorkflowsAreTheTwelveTilesInGRID_ORDER(t *testing.T) {
	// The owner's twelve, then tile 13 «Image to Video» (B-32, 28.09) after them.
	want := []string{
		"virtual_try_on", "fabric_to_image", "ghost_mannequin", "change_color", "swap_fabrics",
		"add_logo", "design_variations", "remove_background", "extend_image", "retouch_zone",
		"create_edit", "image_to_3d", "image_to_video",
	}
	require.Equal(t, want, PlaygroundWorkflows())

	// A copy: a caller's write never reaches the next caller.
	got := PlaygroundWorkflows()
	got[0] = "mutated"
	assert.Equal(t, "virtual_try_on", PlaygroundWorkflows()[0])

	for _, w := range want {
		assert.True(t, IsDesignWorkflow(w), w)
	}
	assert.False(t, IsDesignWorkflow(""))
	assert.False(t, IsDesignWorkflow("tryon"), "a preset is not a workflow key")
}

// Band field 26 feeds the OLD client, which draws every key as a chip of kind freeform: the new
// presets must never leak into it [Codex 10].
func TestFreeformPresetsSTAY_THE_OLD_THREE(t *testing.T) {
	require.Equal(t, []string{"free", "add_hardware", "repaint_parts"}, FreeformPresets())

	all := FreeformPresetsAll()
	require.Equal(t, []string{
		"free", "add_hardware", "repaint_parts",
		"tryon", "fabric_extract", "ghost_mannequin", "add_logo", "variations", "retouch",
	}, all)
	for _, p := range all {
		assert.True(t, IsFreeformPreset(p), p)
	}
	assert.False(t, IsFreeformPreset(""), "an empty preset stays illegal")
	assert.False(t, IsFreeformPreset("cutout"), "cutout is a kind, not a preset")

	// FreeformPresetsAll must not alias FreeformPresets' backing array.
	all[0] = "mutated"
	assert.Equal(t, "free", FreeformPresets()[0])
}

func TestFreeformRolesINCLUDE_THE_PLAYGROUND_FOUR(t *testing.T) {
	for _, r := range []string{"", "subject", "hardware", "cloth", "model", "product", "scene", "logo"} {
		assert.True(t, IsFreeformRole(r), r)
	}
	assert.False(t, IsFreeformRole("person"))
}

// Every kind × preset, recolor with and without a fabric picture. The SQL twin in
// store/design/band.go must read this same table.
func TestDesignWorkflowOfIsTheStampTABLE(t *testing.T) {
	cases := []struct {
		kind, preset string
		fabric       bool
		want         string
	}{
		{DesignRunKindFreeform, "tryon", false, "virtual_try_on"},
		{DesignRunKindFreeform, "fabric_extract", false, "fabric_to_image"},
		{DesignRunKindFreeform, "ghost_mannequin", false, "ghost_mannequin"},
		{DesignRunKindFreeform, "add_logo", false, "add_logo"},
		{DesignRunKindFreeform, "variations", false, "design_variations"},
		{DesignRunKindFreeform, "retouch", false, "retouch_zone"},
		{DesignRunKindFreeform, "free", false, "create_edit"},
		{DesignRunKindFreeform, "", false, "create_edit"},
		{DesignRunKindFreeform, "add_hardware", false, "create_edit"},
		{DesignRunKindFreeform, "repaint_parts", false, "create_edit"},
		{DesignRunKindFreeform, "unheard_of", false, "create_edit"},
		{DesignRunKindFreeform, "tryon", true, "virtual_try_on"},
		{DesignRunKindCutout, "", false, "remove_background"},
		{DesignRunKindRecolor, "", false, "change_color"},
		{DesignRunKindRecolor, "", true, "swap_fabrics"},
		{DesignRunKindThreed, "", false, "image_to_3d"},
		{DesignRunKindThreed, "", true, "image_to_3d"},
		{DesignRunKindFlat, "", false, ""},
		{DesignRunKindRender, "", true, ""},
		{DesignRunKindPattern, "", false, ""},
		{"vector", "", false, ""},
		{DesignRunKindDraftIdea, "", false, ""},
		{"", "tryon", false, ""},
	}
	for _, c := range cases {
		got := DesignWorkflowOf(c.kind, c.preset, c.fabric)
		assert.Equal(t, c.want, got, "kind=%q preset=%q fabric=%v", c.kind, c.preset, c.fabric)
		if got != "" {
			assert.True(t, IsDesignWorkflow(got), got)
		}
	}
}

func TestPlaygroundOptionVocabularies(t *testing.T) {
	for _, v := range []string{"", "auto", "full_body", "upper_body", "portrait", "hands", "feet", "product_detail"} {
		assert.True(t, IsDesignFraming(v), v)
	}
	assert.False(t, IsDesignFraming("waist"))
	for _, v := range []string{"", "auto", "eye_level", "slightly_above", "slightly_below", "low_angle"} {
		assert.True(t, IsDesignAngle(v), v)
	}
	assert.False(t, IsDesignAngle("high_angle"))
	for _, v := range []string{"", "edit", "reference"} {
		assert.True(t, IsDesignSceneMode(v), v)
	}
	assert.False(t, IsDesignSceneMode("text"))
	for _, v := range []string{"", "small", "medium", "large"} {
		assert.True(t, IsDesignLogoSize(v), v)
	}
	assert.False(t, IsDesignLogoSize("xl"))
	assert.Equal(t, 3, MaxDesignCreativity)
	assert.Equal(t, 4, MaxDesignThreedReferences)
}
