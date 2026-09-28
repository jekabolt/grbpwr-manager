package designgen

import (
	"strconv"
	"strings"

	"github.com/jekabolt/grbpwr-manager/internal/aiprov/registry"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/jekabolt/grbpwr-manager/internal/fal"
)

// ═══ THE fal ROWS' MODEL (B-24) ═══
//
// image.extend / image.inpaint / image.cutout ride fal — the one provider serving edit and cutout —
// and until B-24 their route row's MODEL changed nothing: FAL_MODEL_OUTPAINT / FILL / CUTOUT decided.
// Now a row's model overrides the env slug, for the door (FalRoutesFunc: the tile, the reserve, the
// unsupported-slug refusal) and for the worker (Config.FalRouteModel → Job.Model, which each route reads
// before its env slug) through ONE expression. The provider stays fal; vector stays recraft (one
// provider, a fixed slug).

// falRoutePurposes — the run kinds whose route row may name a fal slug.
var falRoutePurposes = map[string]string{
	entity.DesignRunKindExtend:  entity.AIPurposeImageExtend,
	entity.DesignRunKindInpaint: entity.AIPurposeImageInpaint,
	entity.DesignRunKindCutout:  entity.AIPurposeImageCutout,
}

// FalRouteModel — the model the kind's configured route head names when that head is fal; "" = the env
// slug (no such row, a row naming none, another kind, no snapshot yet).
//
// ⚠ THE CONFIGURED HEAD (RouteHeadAt), NOT THE CALLABLE LIST. The slug is configuration: a key switched
// off, or a breaker held, must not quietly swap the model a run is priced and sent with — those refuse
// or wait through the kind gate, under the row's own slug.
func FalRouteModel(reg *registry.Registry, kind string) string {
	purpose, ok := falRoutePurposes[kind]
	if !ok || reg == nil {
		return ""
	}
	head, _, ok := reg.RouteHeadAt(purpose)
	if !ok || head.ProviderKey != entity.AIProviderFal {
		return ""
	}
	return strings.Trim(strings.TrimSpace(head.Model), "/")
}

// FalRoutesFunc — the door's extend / inpaint route objects off the LIVE route: FalRouteOf over a fal
// client whose ModelOutpaint / ModelFill is the row's model (cfg's own slug when the row names none). A
// row naming a slug the route builds no body for is FalRoute.Unsupported — the band hides the tile and
// the door refuses kind_not_available — and the worker refuses the same slug locally, free
// (outpaintBody / fillBody ask the same family test).
func FalRoutesFunc(reg *registry.Registry, cfg fal.Config) func() map[string]FalRoute {
	return func() map[string]FalRoute {
		out := make(map[string]FalRoute, 2)
		for _, kind := range []string{entity.DesignRunKindExtend, entity.DesignRunKindInpaint} {
			c := cfg
			m := FalRouteModel(reg, kind)
			if m != "" {
				if kind == entity.DesignRunKindExtend {
					c.ModelOutpaint = m
				} else {
					c.ModelFill = m
				}
			}
			r, _ := FalRouteOf(fal.New(c), kind)
			if r.Unsupported && m != "" {
				// The slug came from the PANEL, so the sentence sends the owner there, not to an env var
				// nobody set.
				r.Flag = falRoutePurposes[kind]
				r.Unbounded = "the " + r.Flag + " route's model is " + strconv.Quote(m) + ", a slug this route " +
					"builds no request body for — set it to one of " + strings.Join(falRouteSlugs(r.Route), " | ") +
					", or clear it, in admin → AI providers"
			}
			out[kind] = r
		}
		return out
	}
}
