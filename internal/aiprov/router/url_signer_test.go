package router

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
)

// TestChatSignsTheImageURLsOfACopy — with a signer set, the transport receives the signed picture
// urls (lane X13: a team's private files), the caller's request keeps the stored ones, and the rest
// of the request reaches the transport untouched. Without a signer, nothing changes.
//
// MUTATION: signImages writes into req.ImageURLs in place → red (the caller's slice changes).
// MUTATION: Chat skips signImages → red (the transport sees the bare url).
func TestChatSignsTheImageURLsOfACopy(t *testing.T) {
	const own, other = "https://h.test/b/t/aaaa/x.webp", "https://h.test/b/y.webp"
	rg := newRig(t, nil, testDefaults, 0)
	var signedCtx []context.Context
	rg.router.SetURLSigner(func(ctx context.Context, raw string) string {
		signedCtx = append(signedCtx, ctx)
		if strings.Contains(raw, "/t/aaaa/") {
			return raw + "?sig"
		}
		return raw
	})
	urls := []string{own, other}
	req := chatReq
	req.ImageURLs = urls

	_, err := rg.router.Chat(context.Background(), entity.AIPurposeTechCardEnhance, req)
	require.NoError(t, err)
	require.Len(t, rg.or.calls, 1)
	got := rg.or.calls[0].req
	require.Equal(t, []string{own + "?sig", other}, got.ImageURLs)
	got.ImageURLs = nil
	require.Equal(t, chatReq, got, "everything else reaches the transport untouched")
	require.Equal(t, []string{own, other}, urls, "the caller's slice keeps the stored urls")
	require.Len(t, signedCtx, 2)

	plain := newRig(t, nil, testDefaults, 0)
	_, err = plain.router.Chat(context.Background(), entity.AIPurposeTechCardEnhance, req)
	require.NoError(t, err)
	require.Equal(t, []string{own, other}, plain.or.calls[0].req.ImageURLs, "no signer: as before")

	none := newRig(t, nil, testDefaults, 0)
	none.router.SetURLSigner(func(context.Context, string) string { t.Fatal("no pictures: never called"); return "" })
	_, err = none.router.Chat(context.Background(), entity.AIPurposeTechCardEnhance, chatReq)
	require.NoError(t, err)
	require.Nil(t, none.or.calls[0].req.ImageURLs)
}
