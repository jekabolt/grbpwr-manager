// Package meshy is a direct HTTP client for the Meshy AI 3D generation API
// (https://docs.meshy.ai). It turns the approved 2D views of a design — front, back, side —
// into a single textured GLB the browser can show, and it hands the CALLER THE BYTES, never
// a link.
//
// # WHY THIS IS NOT OPENROUTER, SAID OUT LOUD
//
// Requirement P-5 of the design band is "everything that can go through OpenRouter, goes through
// OpenRouter", and every other provider in this feature obeys it: text and images and vector all
// ride internal/openrouter. This package is the ONE named exception, and it is an exception of
// fact rather than of taste: OpenRouter has no 3D modality at all. Its model catalogue accepts
// output_modalities of text, image, audio, embeddings and speech — "3d" is not a value the API
// recognises, so there is no Meshy model there to route to, and no other 3D model either.
//
// This paragraph exists so the next reader does not "fix" the inconsistency. Reaching for a second
// HTTP client was a decision, not an oversight; the day OpenRouter grows a 3D modality, this
// package should be reconsidered, and until then P-5 simply has nothing to offer here.
//
// # THE TRAP THIS PACKAGE IS BUILT AROUND: LINKS THAT EXPIRE
//
// Meshy answers a finished task with URLs to the model, the textures and a thumbnail. THOSE URLS
// LIVE THREE DAYS. Storing one is storing a fact with a shelf life: the row still reads like a
// delivered model, the tile still renders in a review the same afternoon, and the paid artifact is
// simply gone by the middle of the week — with the credits already spent and no way to fetch it
// again.
//
// So this package treats the URL as a value that never leaves the function that received it:
//
//   - Collect performs ONE status lookup and, on SUCCEEDED, downloads the bytes inside that same
//     call, before it returns to anybody.
//   - The URL is a local variable. Result — the only type this package hands back from a finished
//     task — has NO url field of any kind, and a test enumerates its fields to keep it that way.
//     A caller cannot persist the expiring link, because it is never given one.
//   - The bytes go to an io.Writer the caller supplies (Sink), so a 20 MB GLB can stream into the
//     bucket on a 0.5 GB box instead of sitting in RAM waiting for a transaction.
//
// # THE OTHER MONEY-LOSING EDGE: A CEILING THAT CUTS THE FETCH
//
// Generation is asynchronous — submit returns a task id, and the answer arrives minutes later — so
// waiting needs a ceiling. But the ceiling must bound THE WAIT AND ONLY THE WAIT. A deadline that
// also bounds the download produces the single most expensive outcome available here: the task
// succeeded, the credits are spent, the model exists, and we hung up two seconds before receiving
// it, holding a link that dies in three days. Await therefore gives the download a budget of its
// own, taken from the caller's context rather than from the poll ceiling. See Await.
//
// # WHAT IT ASKS THE PROVIDER FOR
//
// Always GLB, and only GLB. The consumer is a browser (<model-viewer> / three.js), which reads
// glTF-binary and nothing else in this list; asking for fbx/obj/usdz alongside it would cost
// conversion time and bucket space for files nobody in this product opens. A finished task whose
// model_urls carry no glb is a failure (ErrNoGLB), not a partial success.
//
// No ai_model slug is baked in. The provider's own default is used unless the caller names one,
// because a hardcoded model slug is a thing that rots silently at the vendor — this codebase has
// already paid for that lesson once with a retired OpenRouter slug that took every AI feature down
// with it.
//
// # DEGRADATION
//
// The client is optional and nil-safe: with MESHY_API_KEY unset, Enabled() is false and every call
// returns ErrNotConfigured. That is deliberately a HONEST LOCK on the button rather than a run that
// waits forever — a design run submitted with no provider key would sit in 'pending' until a
// sweeper called it abandoned.
//
// # RETRIES ARE NOT HERE
//
// This client does not retry. A repeated submit is a repeated CHARGE, and the only place that can
// tell "the provider never saw it" from "the provider saw it and we lost the answer" is the worker,
// which owns the attempt row and the task id. An HTTP-level retry loop hidden in here would pay
// twice, off the books.
package meshy
