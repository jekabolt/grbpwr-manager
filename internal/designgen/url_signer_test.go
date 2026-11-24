package designgen

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestSignedJobSignsFreshSlices — the paid call gets signed References and ClothReferences (lane
// X13: a team's private files), in new slices: the frozen job keeps the stored urls, so its history
// row and the composite's re-reads never see a signature. No signer: the very same job.
//
// MUTATION: signURLs writes into the job's slice → red (the frozen job changes).
func TestSignedJobSignsFreshSlices(t *testing.T) {
	const own, other = "https://h.test/b/t/aaaa/x.webp", "data:image/png;base64,AAAA"
	w := &Worker{}
	job := Job{Prompt: "p", References: []string{own, other}, ClothReferences: []string{own}}
	require.Equal(t, job, w.signedJob(context.Background(), job), "no signer: as before")

	w.SetURLSigner(func(_ context.Context, raw string) string {
		if strings.Contains(raw, "/t/aaaa/") {
			return raw + "?sig"
		}
		return raw
	})
	got := w.signedJob(context.Background(), job)
	require.Equal(t, []string{own + "?sig", other}, got.References)
	require.Equal(t, []string{own + "?sig"}, got.ClothReferences)
	require.Equal(t, "p", got.Prompt)
	require.Equal(t, []string{own, other}, job.References, "the frozen job keeps the stored urls")
	require.Equal(t, []string{own}, job.ClothReferences)

	empty := w.signedJob(context.Background(), Job{Prompt: "q"})
	require.Nil(t, empty.References)
	require.Nil(t, empty.ClothReferences)
}
