package recraft

import (
	"context"
	"errors"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"strconv"
	"strings"
	"testing"
)

// sampleSVG is a small, well-formed vector drawing: one path with a move, two cubic segments and a
// line. It is what a REDRAW looks like — a handful of nodes, mostly curved.
const sampleSVG = `<?xml version="1.0" encoding="UTF-8"?>
<svg xmlns="http://www.w3.org/2000/svg" width="512" height="512" viewBox="0 0 512 512">
  <path d="M10 10 C 20 20 30 30 40 40 S 50 50 60 60 L 70 70" fill="none" stroke="#000"/>
</svg>`

// fakeGen is a stand-in for the transport that spends the money (in production: the shared
// OpenRouter image client, internal/orimages). The seam is the whole point of the narrow
// ImageGenerator interface: this package can be tested without owning, or duplicating, that client.
type fakeGen struct {
	enabled bool
	resp    *GenerateResponse
	err     error

	calls   int
	lastReq GenerateRequest
}

func (f *fakeGen) Enabled() bool { return f.enabled }

func (f *fakeGen) GenerateImage(_ context.Context, req GenerateRequest) (*GenerateResponse, error) {
	f.calls++
	f.lastReq = req
	return f.resp, f.err
}

func okGen() *fakeGen {
	return &fakeGen{
		enabled: true,
		resp: &GenerateResponse{
			Bytes:       []byte(sampleSVG),
			ContentType: SVGContentType,
			Model:       ModelORVector,
			CostUSD:     0.08,
		},
	}
}

func TestImageToImage_Success(t *testing.T) {
	gen := okGen()
	c := New(Config{}, gen)

	if got := c.Route(); got != RouteOpenRouter {
		t.Fatalf("default route = %q, want %q (owner rule P-5: through OpenRouter)", got, RouteOpenRouter)
	}
	if !c.Enabled() {
		t.Fatal("a wired, keyed transport must read as enabled")
	}

	res, err := c.ImageToImage(context.Background(), ImageToImageRequest{
		Prompt: "technical flat of an oversized hoodie, clean line art",
		Image:  ImageInput{URL: "https://media.grbpwr.com/flat.png"},
	})
	if err != nil {
		t.Fatalf("ImageToImage: %v", err)
	}
	if gen.calls != 1 {
		t.Fatalf("transport called %d times, want exactly 1 (one press = one paid image)", gen.calls)
	}

	// An unset tier must resolve to the CHEAP model. A forgotten field must never cost $0.30.
	if res.Tier != TierVector {
		t.Errorf("tier = %q, want %q by default", res.Tier, TierVector)
	}
	if gen.lastReq.Model != ModelORVector {
		t.Errorf("model sent = %q, want %q", gen.lastReq.Model, ModelORVector)
	}
	if res.Model != ModelORVector {
		t.Errorf("recorded model = %q, want %q", res.Model, ModelORVector)
	}
	if res.ContentType != SVGContentType {
		t.Errorf("content type = %q, want %q", res.ContentType, SVGContentType)
	}
	if string(res.SVG) != sampleSVG {
		t.Error("the SVG must be handed back byte-for-byte: this package never rewrites the picture")
	}
	if res.CostUSD != 0.08 || res.EstimatedUSD != 0.08 {
		t.Errorf("cost = %v / estimate = %v, want 0.08 / 0.08", res.CostUSD, res.EstimatedUSD)
	}
	// The measurement that answers «ровный вектор, а не куча полигонов» in numbers.
	if res.Stats.Paths != 1 || res.Stats.CubicSegments != 2 || res.Stats.LineSegments != 1 {
		t.Errorf("stats = %+v, want 1 path / 2 cubic / 1 line", res.Stats)
	}
	if !res.Stats.HasCurves() {
		t.Error("HasCurves must be true for a drawing with cubic segments")
	}
	if res.Stats.ViewBox != "0 0 512 512" || res.Stats.Width != "512" {
		t.Errorf("root geometry = %q / %q, want the element's own words", res.Stats.ViewBox, res.Stats.Width)
	}
}

func TestImageToImage_ProTierSelectsTheProModel(t *testing.T) {
	// The owner asked for BOTH models to be selectable; this is that requirement, tested.
	gen := okGen()
	c := New(Config{}, gen)

	if _, err := c.ImageToImage(context.Background(), ImageToImageRequest{
		Tier:   TierProVector,
		Prompt: "technical flat",
		Image:  ImageInput{URL: "https://media.grbpwr.com/flat.png"},
	}); err != nil {
		t.Fatalf("ImageToImage: %v", err)
	}
	if gen.lastReq.Model != ModelORVectorPro {
		t.Fatalf("pro tier sent %q, want %q", gen.lastReq.Model, ModelORVectorPro)
	}
	if got := TierProVector.EstimatedUSD(); got != 0.30 {
		t.Errorf("pro estimate = %v, want 0.30", got)
	}
}

func TestModelsPerRouteAndOverrides(t *testing.T) {
	// The two routes spell the same two models differently, and sending one spelling to the other
	// endpoint is an instant 404. This pins both tables.
	or := New(Config{}, okGen())
	if or.Model(TierVector) != "recraft/recraft-v4-vector" || or.Model(TierProVector) != "recraft/recraft-v4-pro-vector" {
		t.Errorf("openrouter route models = %q / %q", or.Model(TierVector), or.Model(TierProVector))
	}
	direct := New(Config{Route: "direct", Direct: DirectConfig{APIKey: "k"}}, nil)
	if direct.Model(TierVector) != "recraftv4_vector" || direct.Model(TierProVector) != "recraftv4_pro_vector" {
		t.Errorf("direct route models = %q / %q", direct.Model(TierVector), direct.Model(TierProVector))
	}
	if direct.Route() != RouteDirect {
		t.Errorf("route = %q, want direct", direct.Route())
	}

	// The escape hatch: a rotted slug must be fixable with an env var, not a deploy.
	over := New(Config{ModelVector: "recraft/recraft-v5-vector", ModelVectorPro: "  "}, okGen())
	if over.Model(TierVector) != "recraft/recraft-v5-vector" {
		t.Errorf("override ignored: %q", over.Model(TierVector))
	}
	if over.Model(TierProVector) != ModelORVectorPro {
		t.Errorf("a blank override must fall back to the default, got %q", over.Model(TierProVector))
	}

	// A typo in RECRAFT_ROUTE must not silently disable a paid feature; it falls back to P-5.
	typo := New(Config{Route: "opnerouter"}, okGen())
	if typo.Route() != RouteOpenRouter {
		t.Errorf("unknown route = %q, want the openrouter default", typo.Route())
	}
}

func TestImageToImage_NotConfigured(t *testing.T) {
	cases := map[string]*Client{
		"no transport wired at all":     New(Config{}, nil),
		"transport wired but keyless":   New(Config{}, &fakeGen{enabled: false}),
		"direct route with no API key":  New(Config{Route: "direct"}, nil),
		"nil client (permanently dead)": nil,
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			if c.Enabled() {
				t.Fatal("Enabled must be false so the button can refuse up front instead of parking a run in pending")
			}
			_, err := c.ImageToImage(context.Background(), ImageToImageRequest{
				Prompt: "technical flat",
				Image:  ImageInput{URL: "https://media.grbpwr.com/flat.png"},
			})
			if !errors.Is(err, ErrNotConfigured) {
				t.Fatalf("err = %v, want ErrNotConfigured", err)
			}
		})
	}
}

func TestImageToImage_ProviderErrorsArePassedThrough(t *testing.T) {
	// The worker decides retry-or-not from the sentinel, so the service must not blur them.
	for _, sentinel := range []error{
		ErrRateLimited, ErrUnauthorized, ErrInsufficientCredits,
		ErrModelUnavailable, ErrBadRequest, ErrProviderFailure,
	} {
		gen := &fakeGen{enabled: true, err: sentinel}
		_, err := New(Config{}, gen).ImageToImage(context.Background(), ImageToImageRequest{
			Prompt: "technical flat",
			Image:  ImageInput{URL: "https://media.grbpwr.com/flat.png"},
		})
		if !errors.Is(err, sentinel) {
			t.Errorf("err = %v, want %v", err, sentinel)
		}
	}
}

func TestImageToImage_MalformedResponses(t *testing.T) {
	png := append([]byte("\x89PNG\r\n\x1a\n"), make([]byte, 32)...)
	cases := []struct {
		name string
		resp *GenerateResponse
		want error
	}{
		{"no bytes at all", &GenerateResponse{}, ErrInvalidResponse},
		{"nil response", nil, ErrInvalidResponse},
		{"not xml", &GenerateResponse{Bytes: []byte("this is not a drawing")}, ErrInvalidResponse},
		{"xml but not svg", &GenerateResponse{Bytes: []byte(`<html><body/></html>`)}, ErrInvalidResponse},
		{"truncated svg", &GenerateResponse{Bytes: []byte(`<svg><path d="M0 0"`)}, ErrInvalidResponse},
		// A raster arriving under a vector model name is its own fault: it means RECRAFT_MODEL_* was
		// pointed at a raster model, and storing it would silently defeat the whole requirement.
		{"a raster with an SVG label", &GenerateResponse{Bytes: png, ContentType: SVGContentType}, ErrNotVector},
		// The content-type hint is not evidence; the bytes decide.
		{"a script smuggled in", &GenerateResponse{Bytes: []byte(`<svg xmlns="http://www.w3.org/2000/svg"><script>alert(1)</script></svg>`)}, ErrUnsafeSVG},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			gen := &fakeGen{enabled: true, resp: tc.resp}
			_, err := New(Config{}, gen).ImageToImage(context.Background(), ImageToImageRequest{
				Prompt: "technical flat",
				Image:  ImageInput{URL: "https://media.grbpwr.com/flat.png"},
			})
			if !errors.Is(err, tc.want) {
				t.Fatalf("err = %v, want %v", err, tc.want)
			}
		})
	}
}

func TestImageToImage_RequestValidation(t *testing.T) {
	bad := 1.5
	good := 0.4
	cases := []struct {
		name string
		req  ImageToImageRequest
		want error
	}{
		{"no prompt", ImageToImageRequest{Image: ImageInput{URL: "https://x/y.png"}}, ErrBadRequest},
		{"no image", ImageToImageRequest{Prompt: "flat"}, ErrBadRequest},
		{"both url and bytes", ImageToImageRequest{Prompt: "flat", Image: ImageInput{URL: "https://x/y.png", Bytes: []byte{1}}}, ErrBadRequest},
		{"unknown tier", ImageToImageRequest{Tier: "ultra", Prompt: "flat", Image: ImageInput{URL: "https://x/y.png"}}, ErrBadRequest},
		{"strength out of range", ImageToImageRequest{Prompt: "flat", Image: ImageInput{URL: "https://x/y.png"}, Strength: &bad}, ErrBadRequest},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			gen := okGen()
			_, err := New(Config{}, gen).ImageToImage(context.Background(), tc.req)
			if !errors.Is(err, tc.want) {
				t.Fatalf("err = %v, want %v", err, tc.want)
			}
			if gen.calls != 0 {
				t.Fatal("a request refused by validation must not reach the paid transport")
			}
		})
	}

	// A valid strength travels to the transport untouched (the direct route is the only one that can
	// honour it, but the service must not swallow it).
	gen := okGen()
	if _, err := New(Config{}, gen).ImageToImage(context.Background(), ImageToImageRequest{
		Prompt: "flat", Image: ImageInput{URL: "https://x/y.png"}, Strength: &good,
	}); err != nil {
		t.Fatalf("ImageToImage: %v", err)
	}
	if gen.lastReq.Strength == nil || *gen.lastReq.Strength != good {
		t.Errorf("strength = %v, want %v passed through", gen.lastReq.Strength, good)
	}
}

// TestNoVectorizeAnywhereInPackage is the guard on the decision this whole package is built around.
//
// `vectorize` is Recraft's RASTER TRACER: it follows pixel boundaries and produces the «куча
// полигонов» the owner forbade. The requirement notes, read literally («генерятся в растре, потом
// переводятся в вектор»), point straight at it, so a future reader acting in good faith could
// "fix" this package by calling it. This test makes that impossible to do quietly: no string
// literal in the non-test sources may contain the word.
//
// Test files are excluded because this one has to name the thing it forbids.
func TestNoVectorizeAnywhereInPackage(t *testing.T) {
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("reading the package directory: %v", err)
	}
	needle := "vector" + "ize" // assembled so this test's own source is not the thing it finds
	fset := token.NewFileSet()
	scanned := 0
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		file, err := parser.ParseFile(fset, name, nil, 0)
		if err != nil {
			t.Fatalf("parsing %s: %v", name, err)
		}
		scanned++
		ast.Inspect(file, func(n ast.Node) bool {
			lit, ok := n.(*ast.BasicLit)
			if !ok || lit.Kind != token.STRING {
				return true
			}
			val, err := strconv.Unquote(lit.Value)
			if err != nil {
				val = lit.Value
			}
			if strings.Contains(strings.ToLower(val), needle) {
				t.Errorf("%s — string literal %q names the raster tracer. That endpoint produces "+
					"exactly the many-node soup requirement P-3 forbids; the vector path is "+
					"imageToImage with a vector model.", fset.Position(lit.Pos()), val)
			}
			return true
		})
	}
	if scanned == 0 {
		t.Fatal("no sources scanned: the guard would pass vacuously")
	}
}
