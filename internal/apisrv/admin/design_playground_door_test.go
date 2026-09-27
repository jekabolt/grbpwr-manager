package admin

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/jekabolt/grbpwr-manager/internal/dependency/mocks"
	"github.com/jekabolt/grbpwr-manager/internal/designgen"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/jekabolt/grbpwr-manager/internal/store/design"
	pb_admin "github.com/jekabolt/grbpwr-manager/proto/gen/admin"
	pb_common "github.com/jekabolt/grbpwr-manager/proto/gen/common"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/status"
)

// ═══ B-10 — THE PLAYGROUND DOOR, CONSOLIDATED, AT THE LIVE StartDesignRun ═══════════════════════
//
// The lanes proved each refusal on its own helper (designRefuseUnworkableSources,
// designRefuseMalformedFreeform, designRefuseImageOptions, designRefuseMalformedThreedReferences…).
// What no lane could prove alone is that every one of those helpers is WIRED into the one door the
// client calls, and that each refusal lands BEFORE StartRun — the call that reserves the day's
// money. This file drives StartDesignRun itself: one row per (tile, shape), every playground code
// at least once, `rig.sent == nil` on every refusal and `rig.sent != nil` on every legal control.

// designPlaygroundDoorCodes — every machine word the phase-2 door can answer. The table below must
// name each of them at least once (TestThePlaygroundDoorNAMES_EVERY_CODE…), so a code added to the
// door without a row here is a red test.
var designPlaygroundDoorCodes = []string{
	// §7 preset shapes
	entity.DesignErrorCodeRoleRequired, entity.DesignErrorCodeOneSourcePicture,
	entity.DesignErrorCodeWordsRequired, entity.DesignErrorCodeOneRegion,
	entity.DesignErrorCodeUnknownOption, entity.DesignErrorCodeOptionNotRead,
	"unknown_role", "unknown_preset", "freeform_forbidden", "too_many_pictures",
	// §13 model provenance
	entity.DesignErrorCodeModelPhotoMismatch, entity.DesignErrorCodeModelNotFound,
	// rerun guards
	entity.DesignErrorCodeRerunChangesWorkflow, "rerun_changes_pictures",
	// §6 engines
	entity.DesignErrorCodeUnknownImageModel, entity.DesignErrorCodeQualityNotSupported,
	entity.DesignErrorCodeAspectNotSupported, entity.DesignErrorCodeBackgroundNotSupported,
	entity.DesignErrorCodeImageOptionsForbidden,
	// §12 3D reference mode (+ the card boundary and the input doors over the new id list)
	"threed_forbidden", "duplicate_picture", "foreign_media", entity.DesignErrorCodeDisplayOnlyInput,
	// the cloth-only recolour negative control
	"cloth_without_picture",
}

type playgroundDoorRow struct {
	name   string
	kind   string
	params *pb_common.DesignRunParams
	rerun  *entity.DesignRun // non-nil → req.RerunOfRunId = rerun.Id, GetRun answers it
	setup  func(t *testing.T, rig *designRunRig)
	want   string // '' = legal: the store is reached
}

const (
	pgModel   = entity.DesignFreeformRoleModel
	pgProduct = entity.DesignFreeformRoleProduct
	pgScene   = entity.DesignFreeformRoleScene
	pgLogo    = entity.DesignFreeformRoleLogo
)

func pgMarshal(t *testing.T, p *pb_common.DesignRunParams) entity.RawJSON {
	t.Helper()
	b, err := designMarshalJSON(p)
	require.NoError(t, err)
	return entity.RawJSON(b)
}

func pgModels(profile *entity.Model, err error) func(t *testing.T, rig *designRunRig) {
	return func(t *testing.T, rig *designRunRig) {
		models := mocks.NewMockModels(t)
		rig.repo.EXPECT().Models().Return(models).Maybe()
		models.EXPECT().GetModelById(mock.Anything, 5).Return(profile, err).Once()
	}
}

func pgEngines(t *testing.T, rig *designRunRig) {
	rig.srv.SetDesignEngines(func() []designgen.Engine { return designgen.EngineTable("") })
}

func pgThreed(t *pb_common.DesignThreedParams) *pb_common.DesignRunParams {
	return &pb_common.DesignRunParams{Threed: t}
}

func pgRecolour(extra []int32, c *pb_common.DesignColourRecipe) *pb_common.DesignRunParams {
	return &pb_common.DesignRunParams{ExtraInputMediaIds: extra, Colour: c}
}

func pgCloth(ids ...int32) *pb_common.DesignColourRecipe {
	c := &pb_common.DesignColourRecipe{}
	for _, id := range ids {
		c.Fabrics = append(c.Fabrics, &pb_common.DesignFabricUse{Name: "cloth", MediaId: id})
	}
	return c
}

func playgroundDoorRows(t *testing.T) []playgroundDoorRow {
	P, O, I := ffParams, ffWithOptions, ffItem
	tryon := func() *pb_common.DesignRunParams {
		return P(entity.DesignFreeformPresetTryon, I(11, pgModel, 0), I(12, pgProduct, 0))
	}
	parent := func(kind string, p *pb_common.DesignRunParams) *entity.DesignRun {
		return &entity.DesignRun{Id: 12, TechCardId: designRunCardID, Kind: kind, Params: pgMarshal(t, p)}
	}
	profile := &entity.Model{Id: 5}
	profile.ThumbnailId = sql.NullInt32{Int32: 11, Valid: true}
	profile.MediaIds = []int{71}
	return []playgroundDoorRow{
		// ─── tile 11 create_edit (free) ───
		{name: "free: words, no picture", kind: entity.DesignRunKindFreeform,
			params: P(entity.DesignFreeformPresetFree), want: entity.DesignErrorCodeWordsRequired},
		{name: "free: one picture", kind: entity.DesignRunKindFreeform,
			params: P(entity.DesignFreeformPresetFree, I(11, "", 0))},
		{name: "free: a tryon role", kind: entity.DesignRunKindFreeform,
			params: P(entity.DesignFreeformPresetFree, I(11, pgModel, 0)), want: "unknown_role"},
		{name: "free: logo_size is not read", kind: entity.DesignRunKindFreeform,
			params: O(P(entity.DesignFreeformPresetFree, I(11, "", 0)),
				&pb_common.DesignWorkflowOptions{LogoSize: entity.DesignLogoSizeLarge}),
			want: entity.DesignErrorCodeOptionNotRead},
		{name: "unknown preset", kind: entity.DesignRunKindFreeform,
			params: P("make_it_pop", I(11, "", 0)), want: "unknown_preset"},
		{name: "freeform on render", kind: entity.DesignRunKindRender,
			params: P(entity.DesignFreeformPresetFree, I(11, "", 0)), want: "freeform_forbidden"},

		// ─── tile 1 virtual_try_on ───
		{name: "tryon: model + product", kind: entity.DesignRunKindFreeform, params: tryon()},
		{name: "tryon: no model", kind: entity.DesignRunKindFreeform,
			params: P(entity.DesignFreeformPresetTryon, I(12, pgProduct, 0)), want: entity.DesignErrorCodeRoleRequired},
		{name: "tryon: reference scene missing", kind: entity.DesignRunKindFreeform,
			params: O(tryon(), &pb_common.DesignWorkflowOptions{SceneMode: entity.DesignSceneModeReference}),
			want:   entity.DesignErrorCodeRoleRequired},
		{name: "tryon: reference scene given", kind: entity.DesignRunKindFreeform,
			params: O(P(entity.DesignFreeformPresetTryon, I(11, pgModel, 0), I(12, pgProduct, 0), I(13, pgScene, 0)),
				&pb_common.DesignWorkflowOptions{SceneMode: entity.DesignSceneModeReference})},
		{name: "tryon: five products", kind: entity.DesignRunKindFreeform,
			params: P(entity.DesignFreeformPresetTryon, I(11, pgModel, 0), I(12, pgProduct, 0), I(13, pgProduct, 0),
				I(14, pgProduct, 0), I(15, pgProduct, 0), I(16, pgProduct, 0)),
			want: "too_many_pictures"},
		{name: "tryon: unknown framing", kind: entity.DesignRunKindFreeform,
			params: O(tryon(), &pb_common.DesignWorkflowOptions{Framing: "cowboy_shot"}),
			want:   entity.DesignErrorCodeUnknownOption},
		{name: "tryon: the model's thumbnail", kind: entity.DesignRunKindFreeform,
			params: O(tryon(), &pb_common.DesignWorkflowOptions{ModelId: 5}), setup: pgModels(profile, nil)},
		{name: "tryon: another model's photo", kind: entity.DesignRunKindFreeform,
			params: O(P(entity.DesignFreeformPresetTryon, I(99, pgModel, 0), I(12, pgProduct, 0)),
				&pb_common.DesignWorkflowOptions{ModelId: 5}),
			setup: pgModels(profile, nil), want: entity.DesignErrorCodeModelPhotoMismatch},
		{name: "tryon: an unknown model profile", kind: entity.DesignRunKindFreeform,
			params: O(tryon(), &pb_common.DesignWorkflowOptions{ModelId: 5}),
			setup:  pgModels(nil, fmt.Errorf("get: %w", sql.ErrNoRows)), want: entity.DesignErrorCodeModelNotFound},

		// ─── tiles 2 / 3 / 7: exactly one picture ───
		{name: "fabric_extract: one", kind: entity.DesignRunKindFreeform,
			params: P(entity.DesignFreeformPresetFabricExtract, I(11, "", 0))},
		{name: "fabric_extract: two", kind: entity.DesignRunKindFreeform,
			params: P(entity.DesignFreeformPresetFabricExtract, I(11, "", 0), I(12, "", 0)),
			want:   entity.DesignErrorCodeOneSourcePicture},
		{name: "ghost_mannequin: one", kind: entity.DesignRunKindFreeform,
			params: P(entity.DesignFreeformPresetGhostMannequin, I(11, "", 0))},
		{name: "ghost_mannequin: a marked area", kind: entity.DesignRunKindFreeform,
			params: P(entity.DesignFreeformPresetGhostMannequin, I(11, "", 1)), want: entity.DesignErrorCodeOneRegion},
		{name: "variations: creativity 2", kind: entity.DesignRunKindFreeform,
			params: O(P(entity.DesignFreeformPresetVariations, I(11, "", 0)), &pb_common.DesignWorkflowOptions{Creativity: 2})},
		{name: "variations: creativity out of range", kind: entity.DesignRunKindFreeform,
			params: O(P(entity.DesignFreeformPresetVariations, I(11, "", 0)),
				&pb_common.DesignWorkflowOptions{Creativity: entity.MaxDesignCreativity + 1}),
			want: entity.DesignErrorCodeUnknownOption},

		// ─── tile 6 add_logo ───
		{name: "add_logo: garment + logo", kind: entity.DesignRunKindFreeform,
			params: O(P(entity.DesignFreeformPresetAddLogo, I(11, "", 0), I(12, pgLogo, 0)),
				&pb_common.DesignWorkflowOptions{LogoSize: entity.DesignLogoSizeSmall})},
		{name: "add_logo: no logo", kind: entity.DesignRunKindFreeform,
			params: P(entity.DesignFreeformPresetAddLogo, I(11, "", 0)), want: entity.DesignErrorCodeRoleRequired},
		{name: "add_logo: two logos", kind: entity.DesignRunKindFreeform,
			params: P(entity.DesignFreeformPresetAddLogo, I(11, "", 0), I(12, pgLogo, 0), I(13, pgLogo, 0)),
			want:   entity.DesignErrorCodeOneSourcePicture},
		{name: "add_logo: a huge logo", kind: entity.DesignRunKindFreeform,
			params: O(P(entity.DesignFreeformPresetAddLogo, I(11, "", 0), I(12, pgLogo, 0)),
				&pb_common.DesignWorkflowOptions{LogoSize: "huge"}),
			want: entity.DesignErrorCodeUnknownOption},

		// ─── tile 10 retouch_zone (the rectangle window) ───
		{name: "retouch: one area with words", kind: entity.DesignRunKindFreeform,
			params: P(entity.DesignFreeformPresetRetouch, I(11, "", 1, "remove the stain"))},
		{name: "retouch: no words", kind: entity.DesignRunKindFreeform,
			params: P(entity.DesignFreeformPresetRetouch, I(11, "", 1)), want: entity.DesignErrorCodeWordsRequired},
		{name: "retouch: two areas", kind: entity.DesignRunKindFreeform,
			params: P(entity.DesignFreeformPresetRetouch, I(11, "", 2, "x")), want: entity.DesignErrorCodeOneRegion},

		// ─── engines (tiles 1/7/11 + render) ───
		{name: "engine: a named engine and tier", kind: entity.DesignRunKindFreeform, setup: pgEngines,
			params: withImage(P(entity.DesignFreeformPresetFree, I(11, "", 0)),
				&pb_common.DesignImageOptions{Model: designgen.EngineGPTImage2, Quality: "high", AspectRatio: "3:4"})},
		{name: "engine: an unknown slug", kind: entity.DesignRunKindFreeform, setup: pgEngines,
			params: withImage(P(entity.DesignFreeformPresetFree, I(11, "", 0)),
				&pb_common.DesignImageOptions{Model: "nobody/knows"}),
			want: entity.DesignErrorCodeUnknownImageModel},
		{name: "engine: xhigh", kind: entity.DesignRunKindFreeform, setup: pgEngines,
			params: withImage(P(entity.DesignFreeformPresetFree, I(11, "", 0)),
				&pb_common.DesignImageOptions{Model: designgen.EngineGPTImage2, Quality: "xhigh"}),
			want: entity.DesignErrorCodeQualityNotSupported},
		{name: "engine: 5:4", kind: entity.DesignRunKindFreeform, setup: pgEngines,
			params: withImage(P(entity.DesignFreeformPresetFree, I(11, "", 0)),
				&pb_common.DesignImageOptions{AspectRatio: "5:4"}),
			want: entity.DesignErrorCodeAspectNotSupported},
		{name: "engine: transparent on gpt-image-2", kind: entity.DesignRunKindFreeform, setup: pgEngines,
			params: withImage(P(entity.DesignFreeformPresetFree, I(11, "", 0)),
				&pb_common.DesignImageOptions{Model: designgen.EngineGPTImage2, Background: "transparent"}),
			want: entity.DesignErrorCodeBackgroundNotSupported},
		{name: "engine: image options on threed", kind: entity.DesignRunKindThreed, setup: pgEngines,
			params: withImage(&pb_common.DesignRunParams{}, &pb_common.DesignImageOptions{Model: designgen.EngineGPTImage2}),
			want:   entity.DesignErrorCodeImageOptionsForbidden},

		// ─── tile 12 image_to_3d, reference mode ───
		{name: "3d: two named pictures, detailed", kind: entity.DesignRunKindThreed,
			params: pgThreed(&pb_common.DesignThreedParams{ReferenceMediaIds: []int32{31, 32}, Quality: "detailed", Pbr: "on"})},
		{name: "3d: five named pictures", kind: entity.DesignRunKindThreed,
			params: pgThreed(&pb_common.DesignThreedParams{ReferenceMediaIds: []int32{31, 32, 33, 34, 35}}),
			want:   "too_many_pictures"},
		{name: "3d: one picture twice", kind: entity.DesignRunKindThreed,
			params: pgThreed(&pb_common.DesignThreedParams{ReferenceMediaIds: []int32{31, 31}}),
			want:   "duplicate_picture"},
		{name: "3d: quality ultra", kind: entity.DesignRunKindThreed,
			params: pgThreed(&pb_common.DesignThreedParams{ReferenceMediaIds: []int32{31}, Quality: "ultra"}),
			want:   entity.DesignErrorCodeUnknownOption},
		{name: "3d: follow is not advertised", kind: entity.DesignRunKindThreed,
			params: pgThreed(&pb_common.DesignThreedParams{ReferenceMediaIds: []int32{31}, Follow: "shape"}),
			want:   entity.DesignErrorCodeOptionNotRead},
		{name: "3d: pbr on an untextured model", kind: entity.DesignRunKindThreed,
			params: pgThreed(&pb_common.DesignThreedParams{ReferenceMediaIds: []int32{31}, Texture: "off", Pbr: "on"}),
			want:   entity.DesignErrorCodeOptionNotRead},
		{name: "3d fields on a render run", kind: entity.DesignRunKindRender,
			params: pgThreed(&pb_common.DesignThreedParams{Quality: "detailed"}), want: "threed_forbidden"},
		{name: "3d: a picture of another card", kind: entity.DesignRunKindThreed,
			params: pgThreed(&pb_common.DesignThreedParams{ReferenceMediaIds: []int32{31, 4040}}),
			setup: func(t *testing.T, rig *designRunRig) {
				rig.design.EXPECT().AssertMediaNotForeign(mock.Anything, designRunCardID, []int{31, 4040}).
					Return(entity.ErrDesignForeignMedia).Once()
			},
			want: "foreign_media"},
		{name: "3d: a display-only picture", kind: entity.DesignRunKindThreed,
			params: pgThreed(&pb_common.DesignThreedParams{ReferenceMediaIds: []int32{31, 4141}}),
			setup: func(t *testing.T, rig *designRunRig) {
				rig.design.ExpectedCalls = pgDropCalls(rig.design.ExpectedCalls, "MediaHeldDisplayOnly")
				rig.design.EXPECT().MediaHeldDisplayOnly(mock.Anything, mock.Anything).Return([]int{4141}, nil).Maybe()
			},
			want: entity.DesignErrorCodeDisplayOnlyInput},

		// ─── tiles 4 / 5 recolour (B-10 §6: cloth-only swap passes the door) ───
		{name: "swap_fabrics: a pictured cloth, no colour, asset 0", kind: entity.DesignRunKindRecolor,
			params: pgRecolour([]int32{11}, func() *pb_common.DesignColourRecipe {
				c := pgCloth(70)
				c.FabricMediaId = 70 // tile 5 echoes the first cloth
				return c
			}())},
		{name: "swap_fabrics: the cloth has no picture", kind: entity.DesignRunKindRecolor,
			params: pgRecolour([]int32{11}, pgCloth(0)), want: "cloth_without_picture"},

		// ─── rerun guards ───
		{name: "rerun: free → tryon", kind: entity.DesignRunKindFreeform, params: tryon(),
			rerun: parent(entity.DesignRunKindFreeform, P(entity.DesignFreeformPresetFree, I(11, "", 0), I(12, "", 0))),
			want:  entity.DesignErrorCodeRerunChangesWorkflow},
		{name: "rerun: recolour gains a cloth picture", kind: entity.DesignRunKindRecolor,
			params: pgRecolour([]int32{11}, pgCloth(70)),
			rerun:  parent(entity.DesignRunKindRecolor, pgRecolour([]int32{11}, &pb_common.DesignColourRecipe{Hex: "#112233"})),
			want:   entity.DesignErrorCodeRerunChangesWorkflow},
		{name: "rerun: swaps its picture", kind: entity.DesignRunKindFreeform,
			params: P(entity.DesignFreeformPresetFree, I(88, "", 0)),
			rerun:  parent(entity.DesignRunKindFreeform, P(entity.DesignFreeformPresetFree, I(11, "", 0))),
			want:   "rerun_changes_pictures"},
		{name: "rerun: same tile, items reordered", kind: entity.DesignRunKindFreeform,
			params: P(entity.DesignFreeformPresetFree, I(12, "", 0), I(11, "", 0)),
			rerun:  parent(entity.DesignRunKindFreeform, P(entity.DesignFreeformPresetFree, I(11, "", 0), I(12, "", 0)))},
		{name: "rerun: silent, inherits everything", kind: entity.DesignRunKindFreeform, params: nil,
			rerun: parent(entity.DesignRunKindFreeform, tryon())},
	}
}

// pgDropCalls removes the rig's default expectation for one method, so a row can state its own.
func pgDropCalls(calls []*mock.Call, method string) []*mock.Call {
	out := calls[:0]
	for _, c := range calls {
		if c.Method != method {
			out = append(out, c)
		}
	}
	return out
}

// TestThePlaygroundDoorREFUSES_BEFORE_THE_STORE_AND_LETS_EVERY_TILE_THROUGH — every row through the
// real StartDesignRun. MUTATIONS (each measured red, on exactly the rows named):
//   - drop the designRefuseMalformedThreedReferences call → the six 3D shape rows reach the store;
//   - drop the card boundary over params.threed.reference_media_ids → «a picture of another card»;
//   - drop the reference-ids loop of designRunInputMediaRefs → «a display-only picture»;
//   - designRefuseRerunChangesWorkflow answering nil → both rerun_changes_workflow rows;
//   - designRefuseUnworkableRecolourCloth demanding a colour even with a pictured cloth → the legal
//     cloth-only swap row (B-10 §6);
//   - skip designRefuseModelPhotoMismatch → the three model-profile rows;
//   - skip designRefuseImageOptions → the five engine refusal rows.
func TestThePlaygroundDoorREFUSES_BEFORE_THE_STORE_AND_LETS_EVERY_TILE_THROUGH(t *testing.T) {
	for _, c := range playgroundDoorRows(t) {
		t.Run(c.name, func(t *testing.T) {
			rig := newDesignRunRig(t, designMoodCard(), designBandWith(true))
			if c.setup != nil {
				c.setup(t, rig)
			}
			rig.design.EXPECT().AssertMediaNotForeign(mock.Anything, designRunCardID, mock.Anything).Return(nil).Maybe()
			req := designStartRequest(c.kind)
			req.Params = c.params
			if c.rerun != nil {
				req.RerunOfRunId = int32(c.rerun.Id)
				rig.design.EXPECT().GetRun(mock.Anything, c.rerun.Id).Return(c.rerun, nil).Maybe()
			}
			_, err := rig.srv.StartDesignRun(designRunCtx(), req)
			if c.want == "" {
				require.NoError(t, err)
				require.NotNil(t, rig.sent, "a legal shape reaches the store")
				require.Truef(t, rig.sent.PriceEstimate.Valid && rig.sent.PriceEstimate.Decimal.IsPositive(),
					"a run the door accepts reserves a positive amount, got %v", rig.sent.PriceEstimate)
				return
			}
			_, md := errorReason(t, err)
			require.Equalf(t, c.want, md["reason"], "%v", err)
			require.Nil(t, rig.sent, "refused before the store: nothing reserved")
		})
	}
}

// TestThePlaygroundDoorNAMES_EVERY_CODE — the table above is complete: every playground code has a
// row, and every row's code is a known playground code (a typo in `want` would otherwise be a row
// that proves nothing).
func TestThePlaygroundDoorNAMES_EVERY_CODE(t *testing.T) {
	known := map[string]bool{}
	for _, c := range designPlaygroundDoorCodes {
		known[c] = true
	}
	seen := map[string]bool{}
	for _, r := range playgroundDoorRows(t) {
		if r.want != "" {
			require.Truef(t, known[r.want], "row %q wants %q, which is not a playground code", r.name, r.want)
			seen[r.want] = true
		}
	}
	var missing []string
	for _, c := range designPlaygroundDoorCodes {
		if !seen[c] {
			missing = append(missing, c)
		}
	}
	sort.Strings(missing)
	require.Empty(t, missing, "codes with no row at the live door")

	// Every preset of the vocabulary has at least one LEGAL row (add_hardware / repaint_parts keep
	// their pre-playground doors and are covered by design_freeform_test).
	legal := map[string]bool{}
	for _, r := range playgroundDoorRows(t) {
		if r.want == "" && r.kind == entity.DesignRunKindFreeform && r.params != nil {
			legal[r.params.GetFreeform().GetPreset()] = true
		}
	}
	for _, p := range entity.FreeformPresetsAll() {
		if p == entity.DesignFreeformPresetAddHardware || p == entity.DesignFreeformPresetRepaintParts {
			continue
		}
		require.Truef(t, legal[p], "preset %q has no legal row at the live door", p)
	}
}

// TestTheStampedWorkflowOfEachLegalRow — the tile a legal run is filed under (designWorkflowOf, the
// function of the rerun guard and the Go twin of the feed's SQL) is the tile the client opened.
// Cloth-only recolour is swap_fabrics, a recolour with a colour is change_color.
func TestTheStampedWorkflowOfEachLegalRow(t *testing.T) {
	cases := []struct {
		kind string
		p    *pb_common.DesignRunParams
		want string
	}{
		{entity.DesignRunKindFreeform, ffParams(entity.DesignFreeformPresetTryon), entity.DesignWorkflowVirtualTryOn},
		{entity.DesignRunKindFreeform, ffParams(entity.DesignFreeformPresetFabricExtract), entity.DesignWorkflowFabricToImage},
		{entity.DesignRunKindFreeform, ffParams(entity.DesignFreeformPresetGhostMannequin), entity.DesignWorkflowGhostMannequin},
		{entity.DesignRunKindFreeform, ffParams(entity.DesignFreeformPresetAddLogo), entity.DesignWorkflowAddLogo},
		{entity.DesignRunKindFreeform, ffParams(entity.DesignFreeformPresetVariations), entity.DesignWorkflowDesignVariations},
		{entity.DesignRunKindFreeform, ffParams(entity.DesignFreeformPresetRetouch), entity.DesignWorkflowRetouchZone},
		{entity.DesignRunKindFreeform, ffParams(entity.DesignFreeformPresetFree), entity.DesignWorkflowCreateEdit},
		{entity.DesignRunKindFreeform, ffParams(entity.DesignFreeformPresetAddHardware), entity.DesignWorkflowCreateEdit},
		{entity.DesignRunKindFreeform, nil, entity.DesignWorkflowCreateEdit},
		{entity.DesignRunKindCutout, nil, entity.DesignWorkflowRemoveBackground},
		{entity.DesignRunKindRecolor, pgRecolour([]int32{11}, pgCloth(70)), entity.DesignWorkflowSwapFabrics},
		{entity.DesignRunKindRecolor, pgRecolour([]int32{11}, pgCloth(0, 70)), entity.DesignWorkflowSwapFabrics},
		{entity.DesignRunKindRecolor, pgRecolour([]int32{11}, pgCloth(0)), entity.DesignWorkflowChangeColor},
		{entity.DesignRunKindRecolor, pgRecolour([]int32{11}, &pb_common.DesignColourRecipe{Hex: "#112233", FabricMediaId: 70}),
			entity.DesignWorkflowChangeColor},
		{entity.DesignRunKindThreed, pgThreed(&pb_common.DesignThreedParams{ReferenceMediaIds: []int32{31}}), entity.DesignWorkflowImageTo3D},
		{entity.DesignRunKindRender, nil, ""},
	}
	for _, c := range cases {
		require.Equalf(t, c.want, designWorkflowOf(c.kind, c.p), "%s %v", c.kind, c.p)
		// The admin rule and the entity rule are one: designWorkflowOf only decides the fabric bit.
		require.Equal(t, entity.DesignWorkflowOf(c.kind, c.p.GetFreeform().GetPreset(),
			designAnyClothWithPicture(c.p.GetColour())), designWorkflowOf(c.kind, c.p))
	}
}

// ─────────────── the feed's fabric predicate, without a database ───────────────

// feedFabricPattern pulls the regular expression out of the store's fabric-picture predicate. The
// shape is asserted, so a rewrite of the predicate must come with a rewrite of this reader.
func feedFabricPattern(t *testing.T) *regexp.Regexp {
	t.Helper()
	const head = `REGEXP_LIKE(CAST(JSON_EXTRACT(r.params, '$.colour.fabrics[*].media_id') AS CHAR), '`
	src := strings.TrimSpace(design.CardOutputsFeed().FabricPicture)
	require.True(t, strings.HasPrefix(src, head), "the fabric predicate changed shape: %s", src)
	require.True(t, strings.HasSuffix(src, `')`), src)
	return regexp.MustCompile(src[len(head) : len(src)-2])
}

// mysqlFabricMediaIDs renders JSON_EXTRACT(params, '$.colour.fabrics[*].media_id') the way MySQL
// 8 prints it after CAST AS CHAR: a wildcard path always yields an array of the values that EXIST
// (a fabric without the key contributes nothing), elements separated by ", "; no match → SQL NULL
// (ok = false), and REGEXP_LIKE(NULL) is NULL, i.e. the CASE's ELSE.
func mysqlFabricMediaIDs(t *testing.T, raw string) (string, bool) {
	t.Helper()
	var doc struct {
		Colour *struct {
			Fabrics []map[string]json.RawMessage `json:"fabrics"`
		} `json:"colour"`
	}
	if strings.TrimSpace(raw) == "null" {
		return "", false
	}
	require.NoError(t, json.Unmarshal([]byte(raw), &doc))
	if doc.Colour == nil {
		return "", false
	}
	var parts []string
	for _, f := range doc.Colour.Fabrics {
		v, ok := f["media_id"]
		if !ok {
			continue
		}
		parts = append(parts, string(v)) // numbers, "strings" and null print as in the document
	}
	if len(parts) == 0 {
		return "", false
	}
	return "[" + strings.Join(parts, ", ") + "]", true
}

// TestTheFeedFabricPredicateREADS_WHAT_THE_DOOR_READS — the recolor branch of the feed's CASE
// («some fabrics[i].media_id > 0») against designAnyClothWithPicture, on the bytes the production
// encoder freezes plus the hand-written edge cases, with no database. The live twin
// (TestFeedWorkflowSQLLiveOnThrowawayMySQL) proves the same on a real MySQL when one is available;
// this one runs everywhere.
//
// MUTATIONS (each measured red): the SQL pattern with `[0-9]` for `[1-9]` (explicit-zero row);
// the pattern without the `-` exclusion (negative row); designAnyClothWithPicture also reading the
// scalar fabric_media_id (scalar-only row).
func TestTheFeedFabricPredicateREADS_WHAT_THE_DOOR_READS(t *testing.T) {
	re := feedFabricPattern(t)
	enc := func(c *pb_common.DesignColourRecipe) string {
		b, err := designMarshalJSON(&pb_common.DesignRunParams{Colour: c})
		require.NoError(t, err)
		return string(b)
	}
	scalarOnly := &pb_common.DesignColourRecipe{FabricMediaId: 9}
	for name, raw := range map[string]string{
		"no colour":           enc(nil),
		"no fabrics":          enc(&pb_common.DesignColourRecipe{Hex: "#112233"}),
		"words-only cloth":    enc(pgCloth(0)),
		"one pictured":        enc(pgCloth(7)),
		"zero then pictured":  enc(pgCloth(0, 12)),
		"negative":            enc(pgCloth(-3)),
		"negative, then ten":  enc(pgCloth(-3, 10)),
		"two words-only":      enc(pgCloth(0, 0)),
		"scalar only":         enc(scalarOnly),
		"big id":              enc(pgCloth(0, 2000000000)),
		"id 100":              enc(pgCloth(100)),
		"explicit zero":       `{"colour":{"fabrics":[{"media_id":0}]}}`,
		"null media":          `{"colour":{"fabrics":[{"media_id":null}]}}`,
		"quoted 15":           `{"colour":{"fabrics":[{"media_id":"15"}]}}`,
		"quoted 0":            `{"colour":{"fabrics":[{"media_id":"0"}]}}`,
		"json null document":  `null`,
		"negative zero mixed": `{"colour":{"fabrics":[{"media_id":-10},{"media_id":0}]}}`,
	} {
		t.Run(name, func(t *testing.T) {
			text, ok := mysqlFabricMediaIDs(t, raw)
			sqlSays := ok && re.MatchString(text)
			p := &pb_common.DesignRunParams{}
			if strings.TrimSpace(raw) != "null" {
				require.NoError(t, designUnmarshalJSON([]byte(raw), p))
			}
			require.Equalf(t, designAnyClothWithPicture(p.GetColour()), sqlSays,
				"params %s → MySQL text %q: the feed and the door disagree about a pictured cloth", raw, text)
			want := entity.DesignWorkflowChangeColor
			if sqlSays {
				want = entity.DesignWorkflowSwapFabrics
			}
			require.Equal(t, want, designWorkflowOf(entity.DesignRunKindRecolor, p))
		})
	}
}

// ─────────────── the band: capability per gate, and the feed's two wires ───────────────

// TestTheCapabilityListsFOLLOW_EACH_GATE — close ONE route at a time: exactly its tiles leave
// playground_workflows, threed_options empties only with the 3D route, image_models only when both
// image routes (freeform, render) are closed; all three always present. MUTATIONS (each measured
// red): designPlaygroundWorkflows keying swap_fabrics on the freeform gate; designThreedOptions
// ignoring the threed gate.
func TestTheCapabilityListsFOLLOW_EACH_GATE(t *testing.T) {
	closes := map[string][]string{
		entity.DesignRunKindFreeform: {
			entity.DesignWorkflowVirtualTryOn, entity.DesignWorkflowFabricToImage,
			entity.DesignWorkflowGhostMannequin, entity.DesignWorkflowAddLogo,
			entity.DesignWorkflowDesignVariations, entity.DesignWorkflowRetouchZone,
			entity.DesignWorkflowCreateEdit,
		},
		entity.DesignRunKindRecolor: {entity.DesignWorkflowChangeColor, entity.DesignWorkflowSwapFabrics},
		entity.DesignRunKindCutout:  {entity.DesignWorkflowRemoveBackground},
		entity.DesignRunKindThreed:  {entity.DesignWorkflowImageTo3D},
		entity.DesignRunKindRender:  {},
	}
	band := func(closed ...string) *pb_admin.GetDesignBandResponse {
		repo := mocks.NewMockRepository(t)
		d := mocks.NewMockDesign(t)
		repo.EXPECT().Design().Return(d).Maybe()
		d.EXPECT().GetBand(mock.Anything, mock.Anything, mock.Anything).Return(&entity.DesignBand{}, nil).Once()
		s := &Server{repo: repo}
		s.SetDesignGenerationEnabled(true)
		s.SetDesignEngines(func() []designgen.Engine { return designgen.EngineTable("") })
		s.SetDesignKindGate(func(kind string) error {
			for _, c := range closed {
				if c == kind {
					return status.Error(9, "closed")
				}
			}
			return nil
		})
		resp, err := s.GetDesignBand(designRunCtx(), &pb_admin.GetDesignBandRequest{TechCardId: 7})
		require.NoError(t, err)
		require.NotNil(t, resp.GetPlaygroundWorkflows())
		require.NotNil(t, resp.GetImageModels())
		require.NotNil(t, resp.GetThreedOptions())
		return resp
	}
	all := band().GetPlaygroundWorkflows()
	require.NotContains(t, all, entity.DesignWorkflowExtendImage, "no outpaint route in phase 2")
	for kind, gone := range closes {
		t.Run(kind, func(t *testing.T) {
			resp := band(kind)
			want := []string{}
			for _, w := range all {
				drop := false
				for _, g := range gone {
					drop = drop || g == w
				}
				if !drop {
					want = append(want, w)
				}
			}
			require.Equal(t, want, resp.GetPlaygroundWorkflows())
			if kind == entity.DesignRunKindThreed {
				require.Empty(t, resp.GetThreedOptions())
			} else {
				require.Equal(t, []string{"texture", "pbr", "quality"}, resp.GetThreedOptions())
			}
			require.NotEmpty(t, resp.GetImageModels(), "one image route open keeps the picker")
		})
	}
	require.Empty(t, band(entity.DesignRunKindFreeform, entity.DesignRunKindRender).GetImageModels(),
		"both image routes closed: no picker")
}

// TestTheBandCARRIES_THE_FEEDS_WORKFLOW — the two B-07 lines in GetDesignBand: every output carries
// its run_workflow, and outputs_total_by_workflow arrives as the store counted it (present-empty
// when the store counted nothing). MUTATIONS (each measured red): drop `RunWorkflow:` from
// designCardOutputsToPb; drop `OutputsTotalByWorkflow:` from the response literal.
func TestTheBandCARRIES_THE_FEEDS_WORKFLOW(t *testing.T) {
	repo := mocks.NewMockRepository(t)
	d := mocks.NewMockDesign(t)
	repo.EXPECT().Design().Return(d).Maybe()
	d.EXPECT().GetBand(mock.Anything, mock.Anything, mock.Anything).Return(&entity.DesignBand{
		Outputs: []entity.DesignCardOutput{
			{Picture: entity.DesignPicture{Id: 1, TechCardId: 7}, RunId: 40, RunKind: entity.DesignRunKindFreeform,
				RunWorkflow: entity.DesignWorkflowVirtualTryOn},
			{Picture: entity.DesignPicture{Id: 2, TechCardId: 7}, RunId: 41, RunKind: entity.DesignRunKindRecolor,
				RunWorkflow: entity.DesignWorkflowSwapFabrics},
			{Picture: entity.DesignPicture{Id: 3, TechCardId: 7}},
		},
		OutputsTotal: 64,
		OutputsTotalByWorkflow: map[string]int{
			entity.DesignWorkflowVirtualTryOn: 61, entity.DesignWorkflowSwapFabrics: 3,
		},
	}, nil).Once()
	s := &Server{repo: repo}
	resp, err := s.GetDesignBand(designRunCtx(), &pb_admin.GetDesignBandRequest{TechCardId: 7})
	require.NoError(t, err)
	var got []string
	for _, o := range resp.GetOutputs() {
		got = append(got, o.GetRunWorkflow())
	}
	require.Equal(t, []string{entity.DesignWorkflowVirtualTryOn, entity.DesignWorkflowSwapFabrics, ""}, got)
	require.Equal(t, map[string]int32{
		entity.DesignWorkflowVirtualTryOn: 61, entity.DesignWorkflowSwapFabrics: 3,
	}, resp.GetOutputsTotalByWorkflow())
	require.Empty(t, intMapStringToPb(nil))
	require.NotNil(t, intMapStringToPb(nil), "present-empty, never nil")
}

// ─────────────── the reserve: kind threed through designEstimateForRun ───────────────

// TestTheThreedReserveGOES_THROUGH_THE_RUN_ESTIMATE — the B-09 price reaches the one function the
// door prices with (designEstimateForRun), with and without an engine table, and the default
// options stay the kind's table row. MUTATION (measured red): remove the designThreedRunEstimate
// seam from designEstimateForRun (detailed/untextured rows fall back to 1.2).
func TestTheThreedReserveGOES_THROUGH_THE_RUN_ESTIMATE(t *testing.T) {
	for _, s := range []*Server{{}, engineServer()} {
		for _, c := range []struct {
			texture, quality string
			outputs          int
			want             string
		}{
			{"", "", 1, designThreedCeilingUSD().String()},
			{"on", "standard", 1, designThreedCeilingUSD().String()},
			{"", "detailed", 1, "1.4"},
			{"off", "", 1, "1.2"}, // fal publishes no cheaper untextured figure; Meshy 0.40 < 1.20
			{"", "detailed", 2, "2.8"},
		} {
			p := pgThreed(&pb_common.DesignThreedParams{ReferenceMediaIds: []int32{31}, Texture: c.texture, Quality: c.quality})
			got := s.designEstimateForRun(entity.DesignRunKindThreed, c.outputs, p, nil)
			require.True(t, got.Valid)
			require.Equalf(t, c.want, got.Decimal.String(), "texture=%q quality=%q outputs=%d", c.texture, c.quality, c.outputs)
		}
		require.True(t, s.designEstimateForRun(entity.DesignRunKindThreed, 1, nil, nil).Decimal.
			Equal(designEstimateFor(entity.DesignRunKindThreed, 1).Decimal), "a bench run pays today's row")
	}
	// Every kind the door accepts is priced positive through the run estimate too, engines or not.
	for _, s := range []*Server{{}, engineServer()} {
		for _, kind := range []string{
			entity.DesignRunKindFlat, entity.DesignRunKindRender, entity.DesignRunKindThreed,
			entity.DesignRunKindVector, entity.DesignRunKindRecolor, entity.DesignRunKindPattern,
			entity.DesignRunKindFreeform, entity.DesignRunKindCutout,
		} {
			got := s.designEstimateForRun(kind, 1, &pb_common.DesignRunParams{}, nil)
			require.Truef(t, got.Valid && got.Decimal.IsPositive(), "%s: %v", kind, got)
		}
	}
}
