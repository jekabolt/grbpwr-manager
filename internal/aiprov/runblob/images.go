package runblob

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"mime"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/aiprov"
	"github.com/jekabolt/grbpwr-manager/internal/orimages"
)

// ═══ THE IMAGE TRANSPORT (B-31): runblob draws image.generate ═══
//
// Images is the designgen.ImageTransport over this adapter: the panel names runblob on the
// image.generate route (primary, fallback or «default for images»), the route's chooser asks Serves,
// and a generation is paid here — one submit, a free poll, one download — and answered in the shape
// the image route already reads (orimages.Result: one decoded picture and the provider's own cost).
//
// SYNCHRONOUS FROM THE ROUTE'S POINT OF VIEW, ASYNCHRONOUS ON THE WIRE. runblob is a queue: the
// submit answers at once with an id and the price, the picture arrives 7–30 s later (Nano Banana; a
// Kling photo takes longer). The image route is synchronous (imageProvider.Execute: one call, one
// Result), so the wait lives here: Generate polls Status every imagePoll until the job is terminal
// or the family's ceiling runs out, then downloads the picture AT ONCE — the result URL lives on
// cdn.runblob.io for an undocumented time, and a link stored instead of bytes is a picture that
// disappears (the fal rule).
//
// ⚠ THE MONEY BOUNDARY IS THE 201. Before it every refusal is free (a bad slug, a reference Kling
// cannot fetch, a 4xx at the gate) and the route may try the next candidate. After it the job is
// bought: a poll that never finishes, a status read the provider refuses, a download that fails —
// each is an ENGAGED, NON-RETRYABLE CallError that names the generation id, so the run closes
// `unknown` and the chain never buys the picture a second time (D-16). The ONE exception is the
// provider's own word `failed`: runblob refunds every failed task, so that answer is free
// (Engaged:false) and terminal for this candidate (Retryable:false) — the ledger row reads `free`,
// no price is booked, and the route moves to the next candidate.
//
// ⚠ NO KEY ON THE DOWNLOAD. The picture's address comes out of the provider's JSON; the request that
// fetches it carries no Authorization header, whatever host the address names (fal.fetch, same rule).

// Slugs the transport serves — `<family path>/<model>` for Nano Banana and ChatGPT Images, the family
// path itself for the Kling photo endpoints (their model is the endpoint). These are the route row's /
// a run's `model`; the pricing catalogue lists the same ten (pricing.go, unpriced: runblob states its
// price per call).
const (
	SlugGeminiStandard = "gemini/standard"
	SlugGeminiPro      = "gemini/pro"
	SlugGeminiV2       = "gemini/v2"
	SlugGeminiV2Lite   = "gemini/v2_lite"
	SlugGeminiProVIP   = "gemini/pro_vip"
	SlugGeminiV2VIP    = "gemini/v2_vip"
	SlugKlingO1Photo   = PathKlingO1Photo
	SlugKlingO3Photo   = PathKlingO3Photo
	// SlugChatGPTImage / SlugChatGPTImage25 — ChatGPT Images (runblob-specs/chatgpt-images.md):
	// gpt-5-2 is the family's default and has no quality dial; chatgpt-2.5 takes quality flare |
	// sunburst and background transparent | opaque | auto.
	SlugChatGPTImage   = PathChatGPTImages + "/gpt-5-2"
	SlugChatGPTImage25 = PathChatGPTImages + "/chatgpt-2.5"

	// DefaultImageSlug is the transport's own default (ImageTransport.Model): a run and a route row
	// that name none draw Nano Banana standard — the cheapest documented family ($0.021 a picture on
	// the docs page, read 2026-09-28; UNVERIFIED (G-06) until a live submit states it).
	DefaultImageSlug = SlugGeminiStandard
)

const (
	// imagePoll is how often a pending generation is asked about (the docs: «poll every 5–10 s»).
	imagePoll = 5 * time.Second
	// geminiCeiling / klingPhotoCeiling bound the wait for ONE picture per family — the completion
	// budget of a synchronous route, as orimages' defaultTimeout is for its call. Nano Banana takes
	// 7–30 s by its page; a Kling photo is not timed on its page and gets the longer wait. A job
	// that outlives its ceiling is bought and unknown, never re-bought.
	geminiCeiling     = 3 * time.Minute
	klingPhotoCeiling = 5 * time.Minute
	// chatgptCeiling — ChatGPT Images is not timed on its page; it is a single-picture queue like
	// Nano Banana and gets the same wait.
	chatgptCeiling = geminiCeiling
	// MaxImageBytes caps ONE downloaded picture (orimages' 24 MiB was sized for base64 in JSON; a 4k
	// PNG straight off a CDN can be larger). Refused by name, never trimmed — a cut PNG opens half grey.
	MaxImageBytes = 48 << 20 // 48 MiB
	// downloadTimeout bounds the fetch of one picture.
	downloadTimeout = 90 * time.Second
	// imageErrorLimit bounds the CDN's error text inside a sentence.
	imageErrorLimit = 200
)

// Quality words of orimages.Request.Quality (the band's dial: auto | low | medium | high).
const qualityHigh = "high"

// Image dialects — which body a family takes.
const (
	// dialectKling — a Kling photo endpoint: `img_resolution`, `images_url` (http(s) only), no model.
	dialectKling = iota
	// dialectGemini — Nano Banana: `model`, `quality` standard | 2k, `images` (http(s) or data: urls).
	dialectGemini
	// dialectChatGPT — ChatGPT Images: `model`, `quality` flare | sunburst and `background` on
	// chatgpt-2.5 only, `images` (http(s) or data: urls, base64 too — not sent: our refs are urls).
	dialectChatGPT
)

// imageFamily is one family's wire dialect: the path, the reference rules, the enums, the ceiling.
type imageFamily struct {
	path    string
	dialect int
	// maxRefs — the family's reference ceiling (docs): over it is a refusal before the wire.
	maxRefs int
	// twoK — quality `high` may ask for 2k (Nano Banana: pro, v2, pro_vip, v2_vip only; standard and
	// v2_lite take `standard` alone — sending 2k there is a 422).
	twoK bool
	// dials — the model takes `quality` and `background` (ChatGPT Images: chatgpt-2.5 only; gpt-5-2
	// «silently ignores» both, so they are not sent there).
	dials bool
	// aspects — the family's aspect_ratio enum; a value outside it is NOT sent (the family's default
	// applies) rather than answered with a 4xx.
	aspects map[string]bool
	ceiling time.Duration
}

var (
	geminiAspects = set("auto", "21:9", "16:9", "4:3", "3:2", "1:1", "9:16", "3:4", "2:3", "5:4", "4:5")
	klingO1Aspect = set("9:16", "2:3", "3:4", "1:1", "4:3", "3:2", "16:9", "21:9")
	klingO3Aspect = set("auto", "9:16", "2:3", "3:4", "1:1", "4:3", "3:2", "16:9", "21:9")
	// chatgptAspect — the page's nine; its "auto" means «the parameter is not sent», so it is not here.
	chatgptAspect = set("1:1", "4:3", "3:4", "3:2", "2:3", "16:9", "9:16", "21:9", "4:5")
	// chatgptBackground — the page's background enum (chatgpt-2.5 only).
	chatgptBackground = set("transparent", "opaque", "auto")
)

func set(vs ...string) map[string]bool {
	m := make(map[string]bool, len(vs))
	for _, v := range vs {
		m[v] = true
	}
	return m
}

// imageSlugs — slug → (family, the body's model). The reference ceilings and the 2k rule are the
// Nano Banana page's (standard ≤ 4 pictures, pro / v2 ≤ 10, *_vip ≤ 8; 2k on pro, v2, pro_vip,
// v2_vip) and kling.json's (≤ 10), read 2026-09-28; ChatGPT Images' (≤ 8, both models) read
// 2026-09-29.
var imageSlugs = map[string]struct {
	fam   imageFamily
	model string
}{
	SlugGeminiStandard: {imageFamily{path: PathGemini, dialect: dialectGemini, maxRefs: 4, aspects: geminiAspects, ceiling: geminiCeiling}, "standard"},
	SlugGeminiPro:      {imageFamily{path: PathGemini, dialect: dialectGemini, maxRefs: 10, twoK: true, aspects: geminiAspects, ceiling: geminiCeiling}, "pro"},
	SlugGeminiV2:       {imageFamily{path: PathGemini, dialect: dialectGemini, maxRefs: 10, twoK: true, aspects: geminiAspects, ceiling: geminiCeiling}, "v2"},
	SlugGeminiV2Lite:   {imageFamily{path: PathGemini, dialect: dialectGemini, maxRefs: 10, aspects: geminiAspects, ceiling: geminiCeiling}, "v2_lite"},
	SlugGeminiProVIP:   {imageFamily{path: PathGemini, dialect: dialectGemini, maxRefs: 8, twoK: true, aspects: geminiAspects, ceiling: geminiCeiling}, "pro_vip"},
	SlugGeminiV2VIP:    {imageFamily{path: PathGemini, dialect: dialectGemini, maxRefs: 8, twoK: true, aspects: geminiAspects, ceiling: geminiCeiling}, "v2_vip"},
	SlugKlingO1Photo:   {imageFamily{path: PathKlingO1Photo, maxRefs: 10, aspects: klingO1Aspect, ceiling: klingPhotoCeiling}, ""},
	SlugKlingO3Photo:   {imageFamily{path: PathKlingO3Photo, maxRefs: 10, aspects: klingO3Aspect, ceiling: klingPhotoCeiling}, ""},
	SlugChatGPTImage:   {imageFamily{path: PathChatGPTImages, dialect: dialectChatGPT, maxRefs: 8, aspects: chatgptAspect, ceiling: chatgptCeiling}, "gpt-5-2"},
	SlugChatGPTImage25: {imageFamily{path: PathChatGPTImages, dialect: dialectChatGPT, maxRefs: 8, dials: true, aspects: chatgptAspect, ceiling: chatgptCeiling}, "chatgpt-2.5"},
}

// ImageSlugs — every slug the transport serves, sorted (the pricing catalogue's rows are pinned to it).
func ImageSlugs() []string {
	out := make([]string, 0, len(imageSlugs))
	for s := range imageSlugs {
		out = append(out, s)
	}
	sort.Strings(out)
	return out
}

// Images is the image transport over one Client. A nil *Images is valid and permanently disabled.
type Images struct {
	c *Client
	// download fetches the picture: the same RoundTripper, no redirect refusal (no key travels, so a
	// CDN redirect is harmless) and no Authorization header ever.
	download *http.Client
	// poll / ceilingOf / now — the wait's knobs, fields so a test can shrink them.
	poll      time.Duration
	ceilingOf func(imageFamily) time.Duration
	now       func() time.Time
}

// NewImages builds the image transport over c (nil c = disabled).
func NewImages(c *Client) *Images {
	var rt http.RoundTripper
	if c != nil {
		rt = c.cfg.Transport
	}
	return &Images{
		c:         c,
		download:  &http.Client{Transport: rt},
		poll:      imagePoll,
		ceilingOf: func(f imageFamily) time.Duration { return f.ceiling },
		now:       time.Now,
	}
}

// Model is the transport's own default slug (designgen.ImageTransport).
func (t *Images) Model() string { return DefaultImageSlug }

// Enabled — the adapter holds a key right now (the registry's KeyFunc, read per call). Nil-safe.
func (t *Images) Enabled() bool { return t != nil && t.c.Enabled() }

// Serves reports whether slug is one of the ten this transport draws (designgen.ImageTransport).
// Unknown → false: the chooser skips the candidate before any row is opened, as it skips a slug the
// OpenAI alias map does not carry. Nil-safe.
func (t *Images) Serves(slug string) bool {
	if t == nil {
		return false
	}
	_, ok := imageSlugs[strings.TrimSpace(slug)]
	return ok
}

// Generate draws ONE picture: submit → poll → download (see the section doc).
//
// Mapping from orimages.Request: Model (empty = DefaultImageSlug) picks the family and the body's
// model; Prompt goes as `prompt`; Quality `high` asks for 2k / 2k-resolution / sunburst where the
// family takes it, everything else the family's cheap tier (standard / 1k / flare); AspectRatio is
// sent when the family's enum lists it, else left to the family's default; InputReferences become
// `images` (Nano Banana, ChatGPT Images: http(s) and data: urls) or `images_url` (Kling: http(s) only —
// a data: url there is refused before the wire); Background goes to chatgpt-2.5 alone, when it is one
// of its three words. N, OutputFormat, OutputCompression and Resolution have no runblob counterpart
// and are not sent: runblob returns exactly one picture per task, in the raster the family produces.
func (t *Images) Generate(ctx context.Context, req orimages.Request) (*orimages.Result, error) {
	if !t.Enabled() {
		return nil, fail(aiprov.CodeNotConfigured, 0, false, false,
			fmt.Errorf("%s: no API key is set: %w", provider, aiprov.ErrNotConfigured))
	}
	slug := firstNonBlank(req.Model, DefaultImageSlug)
	spec, ok := imageSlugs[slug]
	if !ok {
		// The chooser never sends this (Serves said no); a direct caller hears it before the wire.
		return nil, fail(aiprov.CodeBadRequest, 0, false, false,
			fmt.Errorf("%s: %q is not an image model this transport draws (known: %s)",
				provider, truncate(slug, 60), strings.Join(ImageSlugs(), ", ")))
	}
	fam := spec.fam
	prompt := strings.TrimSpace(req.Prompt)
	if prompt == "" {
		return nil, fail(aiprov.CodeBadRequest, 0, false, false,
			fmt.Errorf("%s: a %s generation needs a prompt", provider, slug))
	}
	refs, err := fam.references(req.InputReferences, slug)
	if err != nil {
		return nil, fail(aiprov.CodeBadRequest, 0, false, false, err)
	}
	if req.N > 1 {
		// Nothing is narrowed here: runblob returns one picture per task, and the route's every call
		// asks for one (imageCalls). Said once, on the wire's side, so a caller that asked for more
		// reads why fewer came back.
		slog.Default().WarnContext(ctx, "runblob: one picture per task; n was asked and one will come back",
			slog.Int("n", req.N), slog.String("model", slug))
	}
	body := fam.body(prompt, spec.model, req, refs)

	sub, err := t.c.Submit(ctx, fam.path, body)
	if err != nil && (sub == nil || sub.ID == "") {
		// Free (a refusal before or at the gate — the chain may advance) or engaged with no id to
		// find the job by (unknown): the CallError already says which.
		return nil, err
	}
	if err != nil {
		// A 201 with a usable id and an unreadable price: the picture IS coming, and dropping it now
		// would pay for it and file nothing. Polled like any other; the ledger books no number.
		slog.Default().WarnContext(ctx, "runblob: the submit's price was unreadable; the generation is polled and booked unpriced",
			slog.String("generation_id", sub.ID), slog.String("path", fam.path), slog.String("err", err.Error()))
	}

	// ─── FROM HERE ON THE JOB IS BOUGHT: every failure below is engaged and terminal ───
	gen, err := t.await(ctx, fam, sub.ID)
	if err != nil {
		return nil, err
	}
	raw, mediaType, err := t.fetch(ctx, fam, sub.ID, gen.ResultURL())
	if err != nil {
		return nil, err
	}
	res := &orimages.Result{
		Model:  slug,
		Images: []orimages.Image{{Bytes: raw, MediaType: mediaType}},
	}
	if sub.PriceUSD.Valid {
		// The provider's own number, in USD — what the ledger books as cost_source provider (the
		// route reads Usage.Cost, imageCallEnd). NULL ("calculating", absent) stays 0 = unpriced.
		res.Usage.Cost = sub.PriceUSD.Decimal.InexactFloat64()
	}
	return res, nil
}

// references validates the reference pictures for the family: at most maxRefs; each an http(s) url
// with a host, or — Nano Banana and ChatGPT Images only — a data: url. All refused BEFORE the wire: Kling fetches its
// references itself and answers a base64 one with a 400/422 after the round trip at best.
func (f imageFamily) references(in []string, slug string) ([]string, error) {
	out := make([]string, 0, len(in))
	for _, r := range in {
		r = strings.TrimSpace(r)
		if r == "" {
			continue
		}
		if strings.HasPrefix(r, "data:") {
			if f.dialect == dialectKling {
				return nil, fmt.Errorf("%s: %s takes reference pictures by http(s) url only, and one was given as a data: url",
					provider, slug)
			}
			out = append(out, r)
			continue
		}
		u, err := url.Parse(r)
		if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" {
			return nil, fmt.Errorf("%s: a reference picture must be an http(s) url the provider can fetch, got %q",
				provider, truncate(r, 60))
		}
		out = append(out, r)
	}
	if len(out) > f.maxRefs {
		return nil, fmt.Errorf("%s: %d reference pictures in one call, and %s takes at most %d",
			provider, len(out), slug, f.maxRefs)
	}
	return out, nil
}

// body is the family's request. Every value is the caller's or the family's enum; nothing else is
// added (no callback_url: the route polls).
func (f imageFamily) body(prompt, model string, req orimages.Request, refs []string) map[string]any {
	body := map[string]any{"prompt": prompt}
	high := strings.EqualFold(strings.TrimSpace(req.Quality), qualityHigh)
	switch f.dialect {
	case dialectChatGPT:
		body["model"] = model
		if f.dials {
			// sunburst is the page's default AND the dearer tier: sent only when `high` asked for it,
			// the cheap tier otherwise — the band's rule on every family.
			if high {
				body["quality"] = "sunburst"
			} else {
				body["quality"] = "flare"
			}
			if bg := strings.ToLower(strings.TrimSpace(req.Background)); chatgptBackground[bg] {
				body["background"] = bg
			}
		}
		if len(refs) > 0 {
			body["images"] = refs
		}
	case dialectGemini:
		body["model"] = model
		if high && f.twoK {
			body["quality"] = "2k"
		} else {
			body["quality"] = "standard"
		}
		if len(refs) > 0 {
			body["images"] = refs
		}
	default:
		// 1k | 2k (| 4k on O3, never asked: the band's dial tops at `high`, and 4k is the dearest tier
		// nothing in the band prices). `high` → 2k, everything else the cheap tier.
		if high {
			body["img_resolution"] = "2k"
		} else {
			body["img_resolution"] = "1k"
		}
		if len(refs) > 0 {
			body["images_url"] = refs
		}
	}
	if ar := strings.TrimSpace(req.AspectRatio); ar != "" && f.aspects[ar] {
		body["aspect_ratio"] = ar
	}
	return body
}

// await polls the generation until it is terminal, the family's ceiling passes, or the caller leaves.
// The first read is immediate (a stand that answers `completed` at once costs no sleep); every later
// one waits imagePoll. A status read the adapter calls retryable (a transport hiccup, a 429, a 5xx,
// a garbled envelope) is looked at again — looking is free; a read it calls final (the key rejected
// mid-job, the id unknown, the caller's deadline) ends the wait as BOUGHT AND UNKNOWN.
func (t *Images) await(ctx context.Context, fam imageFamily, id string) (*Generation, error) {
	ceiling := t.ceilingOf(fam)
	deadline := t.now().Add(ceiling)
	for {
		g, err := t.c.Status(ctx, fam.path, id)
		ce, spoke := aiprov.AsCallError(err)
		switch {
		case err == nil:
			switch strings.ToLower(g.Status) {
			case StatusCompleted:
				return g, nil
			case StatusFailed:
				// REFUNDED by runblob's own rule: free for the ledger, terminal for this candidate.
				return nil, fail(aiprov.CodeProviderError, 0, false, false,
					fmt.Errorf("%s: generation %s (%s) failed at the provider and was refunded: %s",
						provider, id, fam.path, firstNonBlank(g.Failure(), "no reason given")))
			}
			// pending | processing — and any word the docs do not list: still running until the
			// ceiling says otherwise. Guessing «failed» on an unknown word would abandon a bought job.
		case spoke && ce.Retryable:
			// A free read that can be redone; the job is still bought and still running.
		default:
			code := aiprov.CodeProviderError
			if spoke && ce.Code != "" {
				code = ce.Code
			}
			return nil, bought(code, httpStatus(ce), id, fam.path, "its status can no longer be read", err)
		}
		remaining := deadline.Sub(t.now())
		if remaining <= 0 {
			return nil, bought(aiprov.CodeTimeout, 0, id, fam.path,
				fmt.Sprintf("it was not finished within %s", ceiling), nil)
		}
		wait := t.poll
		if wait > remaining {
			wait = remaining
		}
		timer := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil, bought(aiprov.Interruption(ctx, ctx.Err()), 0, id, fam.path,
				"the wait for it was cut short", ctx.Err())
		case <-timer.C:
		}
	}
}

// fetch downloads the finished picture: no Authorization header, http(s) only, capped, and its
// media type from the CDN's header when it names an image, else sniffed from the bytes.
func (t *Images) fetch(ctx context.Context, fam imageFamily, id, rawURL string) ([]byte, string, error) {
	if rawURL == "" {
		return nil, "", bought(aiprov.CodeEmptyAnswer, 0, id, fam.path, "it completed with no picture url", nil)
	}
	u, err := url.Parse(rawURL)
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" {
		return nil, "", bought(aiprov.CodeProviderError, 0, id, fam.path,
			fmt.Sprintf("its picture url is not fetchable: %q", truncate(rawURL, 80)), err)
	}
	fctx, cancel := context.WithTimeout(ctx, downloadTimeout)
	defer cancel()
	httpReq, err := http.NewRequestWithContext(fctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, "", bought(aiprov.CodeProviderError, 0, id, fam.path, "its picture request could not be built", err)
	}
	resp, err := t.download.Do(httpReq)
	if err != nil {
		return nil, "", bought(aiprov.Interruption(fctx, err), 0, id, fam.path, "its picture could not be fetched", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		msg, _ := readCapped(resp.Body, imageErrorLimit*4, "picture error")
		return nil, "", bought(aiprov.CodeProviderError, resp.StatusCode, id, fam.path,
			fmt.Sprintf("its picture url answered HTTP %d: %s", resp.StatusCode, truncate(strings.TrimSpace(string(msg)), imageErrorLimit)), nil)
	}
	raw, err := readCapped(resp.Body, MaxImageBytes, "picture")
	if err != nil {
		code := aiprov.CodeTooLarge
		if !errors.Is(err, aiprov.ErrResponseTooLarge) {
			code = aiprov.Interruption(fctx, err)
		}
		return nil, "", bought(code, resp.StatusCode, id, fam.path, "its picture could not be read", err)
	}
	mediaType := imageMediaType(resp.Header.Get("Content-Type"), raw)
	if !strings.HasPrefix(mediaType, "image/") {
		return nil, "", bought(aiprov.CodeProviderError, resp.StatusCode, id, fam.path,
			fmt.Sprintf("its picture url served %q, not a picture", mediaType), nil)
	}
	return raw, mediaType, nil
}

// imageMediaType prefers the CDN's own label when it names an image and sniffs the bytes otherwise
// (orimages.mediaTypeOf, the same preference): a CDN's `application/octet-stream` on a real PNG must
// not refuse the picture, and a `text/html` error page dressed as 200 must not be filed as one.
func imageMediaType(header string, raw []byte) string {
	if mt, _, err := mime.ParseMediaType(header); err == nil && strings.HasPrefix(mt, "image/") {
		return mt
	}
	return http.DetectContentType(raw)
}

// bought is the CallError of a failure AFTER the 201: engaged, never retryable, the generation id in
// the sentence so a person can find the job the money went to.
func bought(code string, status int, id, path, why string, err error) *aiprov.CallError {
	sentence := fmt.Errorf("%s: generation %s (%s) is bought and %s — the run must not be re-submitted",
		provider, id, path, why)
	if err != nil {
		sentence = fmt.Errorf("%w: %v", sentence, err)
	}
	return fail(code, status, true, false, sentence)
}

func httpStatus(ce *aiprov.CallError) int {
	if ce == nil {
		return 0
	}
	return ce.HTTPStatus
}
