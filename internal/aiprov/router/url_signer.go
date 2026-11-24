package router

import (
	"context"

	"github.com/jekabolt/grbpwr-manager/internal/aiprov"
)

// SetURLSigner hangs the bucket's signer of private team file urls on the router (lane X13): in
// teams mode a team's own pictures are private, and a chat transport that hands ImageURLs to the
// provider (OpenRouter, Anthropic's url source) needs a signed url the provider can fetch. sign
// returns its input unchanged for anything that is not a private file of the team in ctx. nil = no
// signing. Call at boot, before the first Chat.
func (r *Router) SetURLSigner(sign func(ctx context.Context, rawURL string) string) {
	if r != nil {
		r.signURL = sign
	}
}

// signImages is req with its ImageURLs signed, in a fresh slice: the caller's request is not changed.
// A nil router (disabled) passes req through; Chat refuses it right after.
func (r *Router) signImages(ctx context.Context, req aiprov.ChatRequest) aiprov.ChatRequest {
	if r == nil || r.signURL == nil || len(req.ImageURLs) == 0 {
		return req
	}
	urls := make([]string, len(req.ImageURLs))
	for i, u := range req.ImageURLs {
		urls[i] = r.signURL(ctx, u)
	}
	req.ImageURLs = urls
	return req
}
