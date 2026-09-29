package designgen

import (
	"strings"

	"github.com/jekabolt/grbpwr-manager/internal/fal"
	"github.com/shopspring/decimal"
)

// ═══ THE CONFIGURED 3D ROUTE, AS THE DOOR AND THE BAND SEE IT (G-02, Codex 3 + 4, Fable M-3) ═══
//
// The door used to answer two questions about 3D from constants: WHICH build options a run may
// state (texture / pbr / quality — always all three) and WHAT one build reserves (then: the max of
// fal's published price and the direct Meshy provider at $0.02 a credit; that provider left on
// 2026-09-29, 3D is fal only). Both are properties of the route this deployment
// actually wired, and both went wrong on a configuration nobody on the door could see:
//
//   - FAL_MODEL_3D = the retired hitem3d slug: its body sends fixed constants, so every option was
//     sold and dropped (Codex 3);
//   - FAL_UNIT_USD: the collect books at the configured tariff, the door reserved
//     at the default one — the reservation below the booking (Codex 4);
//   - pbr on fal meshy/v7: its GLB size is unmeasured and the 64 MiB cap fails AFTER the charge,
//     so it stays off until DESIGN_THREED_PBR says a smoke measured it (M-3).
//
// ThreedRoute is ONE value built in app.go from the same clients the worker is given, handed to the
// admin server the way the engine table and the kind gate are.

// Build-option names, as the band spells them (GetDesignBandResponse.threed_options).
const (
	ThreedOptionTexture = "texture"
	ThreedOptionPBR     = "pbr"
	ThreedOptionQuality = "quality"
	// ThreedOptionSurfaceHint — the person's own words about the surface (params.threed.surface_hint).
	// A route reads them only when its model has a text field (the meshy family on fal; never the
	// hitem3d override), and only on a TEXTURED build — an untextured one has no stage to hand them to.
	ThreedOptionSurfaceHint = "surface_hint"
)

// ThreedRoute — what the configured 3D route honours, and the most one build of it may book.
type ThreedRoute struct {
	// Provider — fal (the only 3D provider since 2026-09-29), for the refusal sentences.
	Provider string
	// Options — the build options this route READS, in band order. An option absent here is one the
	// route would drop; the door refuses a non-default value of it before any money moves.
	Options []string
	// ceiling — the most one build with these options may book on this route at this deployment's
	// tariff; ok = false when no such number exists (fal with a tariff and no units ceiling).
	ceiling func(texture, quality string) (decimal.Decimal, bool)
	// unbounded — the sentence naming the setting that would give the reserve a number to stand on.
	unbounded string
}

// Honours reports whether the route reads the option.
func (r ThreedRoute) Honours(option string) bool {
	for _, o := range r.Options {
		if o == option {
			return true
		}
	}
	return false
}

// CeilingUSD — the most one build with these options may BOOK on this route (ok = false: unbounded).
func (r ThreedRoute) CeilingUSD(texture, quality string) (decimal.Decimal, bool) {
	if r.ceiling == nil {
		return decimal.Zero, false
	}
	return r.ceiling(texture, quality)
}

// Unbounded — why CeilingUSD has no number (empty when it has one).
func (r ThreedRoute) Unbounded() string {
	if _, ok := r.CeilingUSD("", ""); ok {
		return ""
	}
	return r.unbounded
}

// threedRouteOptions — texture and quality when the route reads build options at all; pbr only when
// the deployment has also turned it on (DESIGN_THREED_PBR); surface_hint when the model has a text
// field to carry it.
func threedRouteOptions(readsOptions, pbr, readsText bool) []string {
	out := []string{}
	if readsOptions {
		out = append(out, ThreedOptionTexture)
		if pbr {
			out = append(out, ThreedOptionPBR)
		}
		out = append(out, ThreedOptionQuality)
	}
	if readsText {
		out = append(out, ThreedOptionSurfaceHint)
	}
	return out
}

// ═══ WHAT A RUN STATES THAT THE ROUTE WOULD DROP (G-02 r2, Codex 1 + 2) ═══
//
// ONE EXPRESSION, ASKED TWICE: by the door on the run's effective params before anything is reserved
// (designRefuseThreedRoute), and by the worker on the frozen params right before a FRESH submit
// (dispatch.go). Between the two lie a redeploy and a changed configuration — FAL_MODEL_3D moved to
// the hitem3d override, DESIGN_THREED_PBR turned off — and a run accepted under the old route must
// not be paid for under the new one with its options silently dropped.

// ThreedUnread — the first option these values state that route r would NOT read, with the reason in
// words; option == "" when the route reads everything stated. A value equal to the route's own
// constant (texture on, pbr off, quality standard, empty) asks for nothing, so it is never unread.
//
// r == nil is a deployment with no 3D route wired: nothing is read (fail closed).
func ThreedUnread(r *ThreedRoute, texture, pbr, quality, surfaceHint string) (option, why string) {
	honours := func(o string) bool { return r != nil && r.Honours(o) }
	noOptions := "the configured 3D model takes no per-run build options, so it would be dropped"
	if strings.TrimSpace(texture) == fal.OptionOff && !honours(ThreedOptionTexture) {
		return ThreedOptionTexture, noOptions
	}
	if strings.TrimSpace(pbr) == fal.OptionOn && !honours(ThreedOptionPBR) {
		if r == nil || r.Honours(ThreedOptionTexture) {
			return ThreedOptionPBR, "realistic materials are off on this server (DESIGN_THREED_PBR) until " +
				"their model size is measured under the 64 MiB cap"
		}
		return ThreedOptionPBR, noOptions
	}
	if strings.TrimSpace(quality) == fal.QualityDetailed && !honours(ThreedOptionQuality) {
		return ThreedOptionQuality, noOptions
	}
	if strings.TrimSpace(surfaceHint) != "" {
		if !honours(ThreedOptionSurfaceHint) {
			return ThreedOptionSurfaceHint, "the configured 3D model has no text field, so these words " +
				"would reach no provider"
		}
		if strings.TrimSpace(texture) == fal.OptionOff {
			return ThreedOptionSurfaceHint, "an untextured build has no texturing stage to read these " +
				"words — turn texture on, or leave the hint empty"
		}
	}
	return "", ""
}

// ThreedRouteOf — the route a wired 3D provider IS, read off the same client it pays with; nil for a
// provider that is not the fal route (nothing is known about what it reads). The 3D chooser
// (threed_choice.go) reads it per candidate for the door's View, and the worker asks it again of the
// candidate it is about to pay, before every fresh submit — a candidate is the route of its inner provider.
func ThreedRouteOf(p Provider, pbr bool) *ThreedRoute {
	var r ThreedRoute
	switch v := p.(type) {
	case falThreedProvider:
		r = FalThreedRoute(v.c, pbr)
	case threedCandidate:
		return ThreedRouteOf(v.inner, pbr)
	default:
		return nil
	}
	return &r
}

// FalThreedRoute — the fal route as configured: options only on the meshy family
// (fal.Client.AcceptsBuildOptions), the ceiling from fal.Client.RequestCeilingUSDForQuality — the
// published per-build price without a tariff (exactly what the collect books), tariff × the stated
// units ceiling with one, nothing with a tariff and no ceiling.
func FalThreedRoute(c *fal.Client, pbr bool) ThreedRoute {
	return ThreedRoute{
		Provider: ThreedProviderFal,
		Options:  threedRouteOptions(c.AcceptsBuildOptions(), pbr, c.AcceptsTexturePrompt()),
		ceiling: func(_, quality string) (decimal.Decimal, bool) {
			return c.RequestCeilingUSDForQuality(quality)
		},
		unbounded: "FAL_UNIT_USD is set and FAL_UNITS_CEILING_3D is not: a 3D build would book " +
			"FAL_UNIT_USD × whatever units fal reports, and no reservation can cover that. Set " +
			"FAL_UNITS_CEILING_3D, or unset FAL_UNIT_USD to book fal's published per-build price",
	}
}
