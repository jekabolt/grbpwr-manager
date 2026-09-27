package design_test

import (
	"context"
	"strconv"
	"testing"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/jekabolt/grbpwr-manager/internal/store/design"
	"github.com/stretchr/testify/require"
)

// B-07 LIVE PROBE — ONE PLAYGROUND TILE DOES NOT EVICT ANOTHER, AND EVERY OUTPUT CARRIES ITS TILE.
//
// ⚠ WRITTEN AND COMPILED, NOT EXECUTED BY ITS AUTHOR: store tests run only in the disposable
// container (CI=1 + MYSQL_*, see the head of wave2_db_test.go — without CI=1 it is skipped before a
// connection opens). The same SQL was executed on TEMPORARY tables of a throwaway MySQL 8.0.46 and
// 8.4.10 by apisrv/admin/design_feed_workflow_twin_test.go; what only this probe reaches is the
// real GetBand path — the StructScan of `run_workflow` / `workflow` into the store's rows and the
// band fields they fill.
//
// THE STAND. A quiet Virtual Try-On output is the OLDEST picture of the card; then the ceiling + 3
// Create/Edit plays, all on colourway 0 (as every playground run is). With the section-1 window cut
// by colourway and section only, the try-on went first; cut by workflow too, it stays.
//
// MUTATIONS THAT TURN IT RED: drop the window key from PARTITION BY (the try-on vanishes); group the
// count by the window key instead of the workflow (recolor keys vanish from the per-workflow map);
// assign instead of add in loadCardOutputs (a per-workflow total names one colourway's group);
// swap the recolor branch (swap/change stamps trade places).
func TestDesignDBPlaygroundTilesDoNotEvictEachOther(t *testing.T) {
	rep, raw := probeRepository(t)
	ctx := context.Background()
	card := probeCard(t, raw)
	media := probeMedia(t, raw)

	withParams := func(run int, params string) int {
		_, err := raw.Exec(`UPDATE design_run SET params = CAST(? AS JSON) WHERE id = ?`, params, run)
		require.NoError(t, err)
		return run
	}
	picture := func(run, ordinal int, kind string, cw int) int {
		return outputsProbePicture(t, raw, card, run, media, ordinal, kind, cw, 0, false)
	}

	tryonRun := withParams(outputsProbeRun(t, raw, card, entity.DesignRunKindFreeform, 1, 0),
		`{"freeform":{"preset":"tryon"}}`)
	tryon := picture(tryonRun, 0, entity.DesignPictureKindFreeform, 0)

	over := design.MaxCardOutputsPerColorway + 3
	busyRun := withParams(outputsProbeRun(t, raw, card, entity.DesignRunKindFreeform, 2, 0),
		`{"freeform":{"preset":"free"}}`)
	busy := make([]int, 0, over)
	for i := 0; i < over; i++ {
		busy = append(busy, picture(busyRun, i, entity.DesignPictureKindFreeform, 0))
	}

	// Recolor keeps colourway semantics (section 0); the cloth WITH a picture makes it swap_fabrics,
	// a words-only cloth (media_id 0, omitted by protojson or written out) does not.
	swapRun := withParams(outputsProbeRun(t, raw, card, entity.DesignRunKindRecolor, 3, 0),
		`{"colour":{"fabrics":[{"name":"rib"},{"name":"jersey","media_id":`+strconv.Itoa(media)+`}]}}`)
	swap := picture(swapRun, 0, entity.DesignPictureKindOfRun(entity.DesignRunKindRecolor), 0)
	changeRun := withParams(outputsProbeRun(t, raw, card, entity.DesignRunKindRecolor, 4, 0),
		`{"colour":{"hex":"#112233","fabrics":[{"name":"rib","media_id":0}]}}`)
	change := picture(changeRun, 0, entity.DesignPictureKindOfRun(entity.DesignRunKindRecolor), 0)
	renderRun := outputsProbeRun(t, raw, card, entity.DesignRunKindRender, 5, 0)
	render := picture(renderRun, 0, entity.DesignPictureKindRender, 0)

	band, err := rep.Design().GetBand(ctx, card, design.DefaultRunPageLimit)
	require.NoError(t, err)
	got := map[int]entity.DesignCardOutput{}
	createEdit := 0
	for _, o := range band.Outputs {
		got[o.Picture.Id] = o
		if o.RunWorkflow == entity.DesignWorkflowCreateEdit {
			createEdit++
		}
	}

	require.Contains(t, got, tryon, "the quiet tile's only output survives sixty plays of another tile")
	require.Equal(t, entity.DesignWorkflowVirtualTryOn, got[tryon].RunWorkflow)
	require.Equal(t, design.MaxCardOutputsPerColorway, createEdit, "the busy tile is capped by its own window")
	require.NotContains(t, got, busy[0], "…and loses its OLDEST outputs")
	require.Equal(t, entity.DesignWorkflowSwapFabrics, got[swap].RunWorkflow)
	require.Equal(t, entity.DesignWorkflowChangeColor, got[change].RunWorkflow)
	require.Equal(t, "", got[render].RunWorkflow, "a render belongs to no playground tile")

	require.Equal(t, map[string]int{
		entity.DesignWorkflowVirtualTryOn: 1,
		entity.DesignWorkflowCreateEdit:   over,
		entity.DesignWorkflowSwapFabrics:  1,
		entity.DesignWorkflowChangeColor:  1,
	}, band.OutputsTotalByWorkflow)
	require.Equal(t, over+4, band.OutputsTotalByColorway[0])
	require.Equal(t, over+4, band.OutputsTotal)
}
