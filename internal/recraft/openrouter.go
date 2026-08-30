package recraft

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"net/url"
	"strings"

	"github.com/jekabolt/grbpwr-manager/internal/orimages"
)

// openRouterGenerator adapts the SHARED OpenRouter image client (internal/orimages) to this
// package's narrow ImageGenerator seam.
//
// It is an adapter and nothing more: no HTTP, no envelope parsing, no second copy of a client that
// already exists. That is the point — owner rule P-5 sends every picture through OpenRouter, and
// the vector models are ordinary rows of that same catalogue, reached at the same endpoint with a
// different slug.
type openRouterGenerator struct{ c *orimages.Client }

// NewOpenRouterGenerator wires the shared OpenRouter image client as this package's transport.
// A nil client is legal and reads as disabled.
func NewOpenRouterGenerator(c *orimages.Client) ImageGenerator { return openRouterGenerator{c: c} }

// Enabled forwards the shared client's own answer, so the button can refuse before a run row exists.
func (g openRouterGenerator) Enabled() bool { return g.c != nil && g.c.Enabled() }

// GenerateImage sends one paid vector redraw through OpenRouter.
func (g openRouterGenerator) GenerateImage(ctx context.Context, req GenerateRequest) (*GenerateResponse, error) {
	if !g.Enabled() {
		return nil, fmt.Errorf("%w: the OpenRouter image client holds no API key", ErrNotConfigured)
	}
	// THE DIAL THIS ROUTE DOES NOT HAVE. OpenRouter's image endpoint exposes no `strength`, so a
	// caller asking for one is REFUSED rather than quietly served a call that ignores it: "the
	// redraw wandered too far from the flat" is exactly the complaint that dial answers, and
	// silently dropping it would make the knob look broken instead of absent.
	if req.Strength != nil {
		return nil, fmt.Errorf("%w: strength is not available on the OpenRouter route; set RECRAFT_ROUTE=direct to use it", ErrBadRequest)
	}

	refs, err := inputReferences(req.Image)
	if err != nil {
		return nil, err
	}

	res, genErr := g.c.Generate(ctx, orimages.Request{
		Model:  req.Model,
		Prompt: promptWithNegative(req.Prompt, req.NegativePrompt),
		// One image per press. n on this endpoint means n VARIANTS OF ONE PROMPT, each billed.
		N:               1,
		InputReferences: refs,
		// OutputFormat is deliberately left empty. The vector models emit SVG as their only output,
		// and naming a format the endpoint may not accept for them would turn a working call into a
		// 400 for no gain.
	})

	// MONEY FIRST. The shared client returns its Usage even when the call failed after being
	// billed — an empty data array, an undecodable image — and that spend is real. Losing it here
	// is how a budget silently drifts from what was actually charged.
	charge := 0.0
	answered := req.Model
	if res != nil {
		charge = res.Usage.Cost
		if m := strings.TrimSpace(res.Model); m != "" {
			answered = m
		}
	}
	if genErr != nil {
		return nil, wrapCharged(translateORError(genErr), charge, 0, answered)
	}
	if res == nil || len(res.Images) == 0 {
		return nil, wrapCharged(fmt.Errorf("%w: the provider returned no image", ErrInvalidResponse), charge, 0, answered)
	}

	img := res.Images[0]
	return &GenerateResponse{
		Bytes:       img.Bytes,
		ContentType: img.MediaType,
		Model:       answered,
		CostUSD:     charge,
	}, nil
}

// inputReferences turns our one input picture into the reference list the endpoint takes.
//
// A URL crosses as a URL: the provider fetches it and the bytes never enter this process. Raw bytes
// have to become a data: URI, which inflates them by a third — the reason ImageInput.URL is the
// documented path and bytes are the exception.
func inputReferences(in ImageInput) ([]string, error) {
	if link := strings.TrimSpace(in.URL); link != "" {
		u, err := url.Parse(link)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
			return nil, fmt.Errorf("%w: the input image url %q is not an http(s) address the provider can fetch", ErrBadRequest, link)
		}
		return []string{link}, nil
	}
	if len(in.Bytes) == 0 {
		return nil, fmt.Errorf("%w: imageToImage needs an input image", ErrBadRequest)
	}
	ct := strings.TrimSpace(in.ContentType)
	if ct == "" {
		ct = "application/octet-stream"
	}
	return []string{"data:" + ct + ";base64," + base64.StdEncoding.EncodeToString(in.Bytes)}, nil
}

// promptWithNegative folds a negative prompt into the instruction, because this endpoint has no
// separate field for one. It is stated in the prompt rather than dropped: the caller asked for
// something and is entitled to have it said out loud to the model.
func promptWithNegative(prompt, negative string) string {
	prompt = strings.TrimSpace(prompt)
	negative = strings.TrimSpace(negative)
	if negative == "" {
		return prompt
	}
	return prompt + "\n\nAvoid: " + negative
}

// translateORError maps the shared client's vocabulary onto this package's sentinels, so a caller
// of ImageToImage sees ONE error vocabulary regardless of which route spent the money.
//
// HONEST GAP: the shared client does not classify 401 / 402 / 400 — they arrive as a bare error and
// THIS PARAGRAPH USED TO SAY the shared client cannot tell a rejected key from weather, and that
// the fix belonged in internal/orimages rather than here. That was true when it was written and
// stopped being true in the same wave: orimages now classifies 401/403 as ErrUnauthorized and 402
// as ErrOutOfCredit. The rationale outlived its cause, and while it stood, every vector run against
// a revoked key burned four retries over eight minutes and was filed as «provider unavailable» —
// so the person on duty would go looking at the provider instead of at the key.
//
// The mapping below is by SENTINEL, never by the provider's prose: matching a sentence is how a
// reworded message silently reclassifies a fault.
func translateORError(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, orimages.ErrNotConfigured):
		return fmt.Errorf("%w: %v", ErrNotConfigured, err)
	case errors.Is(err, orimages.ErrModelUnavailable):
		return fmt.Errorf("%w: %v", ErrModelUnavailable, err)
	case errors.Is(err, orimages.ErrUnauthorized):
		return fmt.Errorf("%w: %v", ErrUnauthorized, err)
	case errors.Is(err, orimages.ErrOutOfCredit):
		return fmt.Errorf("%w: %v", ErrInsufficientCredits, err)
	case errors.Is(err, orimages.ErrRateLimited):
		return fmt.Errorf("%w: %v", ErrRateLimited, err)
	case errors.Is(err, orimages.ErrNoImages):
		return fmt.Errorf("%w: %v", ErrInvalidResponse, err)
	case errors.Is(err, orimages.ErrResponseTooLarge):
		return fmt.Errorf("%w: %v", ErrInvalidResponse, err)
	case errors.Is(err, orimages.ErrProviderFailure):
		return fmt.Errorf("%w: %v", ErrProviderFailure, err)
	default:
		return fmt.Errorf("%w: %v", ErrProviderFailure, err)
	}
}
