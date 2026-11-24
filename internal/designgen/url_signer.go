package designgen

import "context"

// SetURLSigner hangs the bucket's signer of private team file urls on the worker (lane X13): in
// teams mode a team's own pictures are private, and a provider that fetches References itself
// (fal, Kling, OpenRouter, the OpenAI edits download) needs a signed url. sign returns its input
// unchanged for anything that is not a private file of the team in ctx. nil = no signing. Call
// before Start.
func (w *Worker) SetURLSigner(sign func(ctx context.Context, rawURL string) string) {
	w.urlSigner = sign
}

// signedJob is the copy of job handed to the paid call: References and ClothReferences signed, in
// fresh slices (the frozen job, its history row and the composite's re-reads keep the stored urls).
// Extend and Inpaint plans are not touched: their urls are read back through the bucket, never sent.
func (w *Worker) signedJob(ctx context.Context, job Job) Job {
	if w.urlSigner == nil {
		return job
	}
	job.References = w.signURLs(ctx, job.References)
	job.ClothReferences = w.signURLs(ctx, job.ClothReferences)
	return job
}

func (w *Worker) signURLs(ctx context.Context, urls []string) []string {
	if len(urls) == 0 {
		return urls
	}
	out := make([]string, len(urls))
	for i, u := range urls {
		out[i] = w.urlSigner(ctx, u)
	}
	return out
}
