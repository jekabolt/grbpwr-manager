package openrouter

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func sampleContext() TechCardContext {
	return TechCardContext{
		TechCardID:  42,
		StyleName:   "Oversized Hoodie",
		StyleNumber: "FW26-0007",
		Category:    "Hoodie",
		Gender:      "unisex",
		Pieces: []PieceContext{
			{Name: "front panel", PiecesPerGarment: 1, Grainline: "lengthwise"},
			{Name: "hood", PiecesPerGarment: 2, Mirrored: true},
		},
		BOM: []BOMItemContext{
			{Section: "fabric", Name: "French terry 320gsm", Composition: "100% cotton"},
			{Section: "thread", Name: "Poly core 120"},
		},
		Construction: &ConstructionContext{MainStitchType: "lockstitch 301", SeamAllowances: "1 cm"},
	}
}

func TestBuildUserPrompt_IncludesContextAndDescription(t *testing.T) {
	p := buildUserPrompt(sampleContext(), "  serge the side seams then coverstitch the hem  ")

	for _, want := range []string{
		"Oversized Hoodie", "FW26-0007", "Hoodie",
		"front panel", "hood", "mirrored pair",
		"French terry 320gsm", "100% cotton",
		"lockstitch 301",
		"serge the side seams then coverstitch the hem",
	} {
		if !strings.Contains(p, want) {
			t.Errorf("prompt missing %q\n---\n%s", want, p)
		}
	}
	// description must be trimmed in the prompt
	if strings.Contains(p, "  serge the side seams") {
		t.Errorf("description was not trimmed:\n%s", p)
	}
}

func TestBuildUserPrompt_OmitsEmptyFields(t *testing.T) {
	p := buildUserPrompt(TechCardContext{StyleName: "Tee"}, "sew it")
	if strings.Contains(p, "Brand:") || strings.Contains(p, "CUT PIECES") || strings.Contains(p, "BILL OF MATERIALS") {
		t.Errorf("empty sections should be omitted:\n%s", p)
	}
	if !strings.Contains(p, "Tee") || !strings.Contains(p, "sew it") {
		t.Errorf("required content missing:\n%s", p)
	}
}

func TestExtractJSON(t *testing.T) {
	cases := map[string]string{
		`{"operations":[]}`:                                          `{"operations":[]}`,
		"```json\n{\"operations\":[]}\n```":                          `{"operations":[]}`,
		"```\n{\"a\":1}\n```":                                        `{"a":1}`,
		"Here you go:\n{\"operations\":[{\"node\":\"x\"}]}\nThanks!": `{"operations":[{"node":"x"}]}`,
		"no json here":                                               "",
		"":                                                           "",
	}
	for in, want := range cases {
		if got := extractJSON(in); got != want {
			t.Errorf("extractJSON(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestParseResult_NumbersAsNumbersOrStrings(t *testing.T) {
	// A model may emit numeric fields as raw numbers OR as strings; both must parse.
	content := `{
	  "operations": [
	    {"node":"overlock side seams","operation_type":"overlock","stitches_per_cm":4,"time_norm_minutes":"0.8","operation_number":"10","callout_number":3},
	    {"node":"coverstitch hem","operation_type":"coverstitch","stitches_per_cm":"5","time_norm_minutes":1.2,"operation_number":20}
	  ],
	  "notes": "assumed 4-thread overlock"
	}`
	r, err := parseResult(content)
	if err != nil {
		t.Fatalf("parseResult: %v", err)
	}
	if len(r.Operations) != 2 {
		t.Fatalf("want 2 operations, got %d", len(r.Operations))
	}
	if r.Notes != "assumed 4-thread overlock" {
		t.Errorf("notes = %q", r.Notes)
	}
	o0 := r.Operations[0]
	if o0.StitchesPerCm.String() != "4" || o0.TimeNormMinutes.String() != "0.8" ||
		o0.OperationNumber.String() != "10" || o0.CalloutNumber.String() != "3" {
		t.Errorf("op0 numeric parse: spc=%q tn=%q num=%q co=%q",
			o0.StitchesPerCm, o0.TimeNormMinutes, o0.OperationNumber, o0.CalloutNumber)
	}
	o1 := r.Operations[1]
	if o1.StitchesPerCm.String() != "5" || o1.TimeNormMinutes.String() != "1.2" || o1.OperationNumber.String() != "20" {
		t.Errorf("op1 numeric parse: spc=%q tn=%q num=%q", o1.StitchesPerCm, o1.TimeNormMinutes, o1.OperationNumber)
	}
}

func TestParseResult_Fenced(t *testing.T) {
	r, err := parseResult("```json\n{\"operations\":[{\"node\":\"attach cuffs\"}]}\n```")
	if err != nil {
		t.Fatalf("parseResult fenced: %v", err)
	}
	if len(r.Operations) != 1 || r.Operations[0].Node != "attach cuffs" {
		t.Fatalf("unexpected parse: %+v", r.Operations)
	}
}

func TestParseResult_Errors(t *testing.T) {
	if _, err := parseResult("totally not json"); err == nil {
		t.Error("expected error for non-JSON content")
	}
	if _, err := parseResult(`{"operations": [ this is broken ]}`); err == nil {
		t.Error("expected error for malformed JSON")
	}
}

func TestJSONNum_Null(t *testing.T) {
	var op Operation
	if err := json.Unmarshal([]byte(`{"node":"x","stitches_per_cm":null}`), &op); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if op.StitchesPerCm.String() != "" {
		t.Errorf("null should decode to empty, got %q", op.StitchesPerCm)
	}
}

func TestEnabled_NilSafe(t *testing.T) {
	var c *Client
	if c.Enabled() {
		t.Error("nil client must be disabled")
	}
	if c.Model() != "" {
		t.Error("nil client model must be empty")
	}
	if New(Config{}).Enabled() {
		t.Error("client without api key must be disabled")
	}
	if !New(Config{APIKey: "k"}).Enabled() {
		t.Error("client with api key must be enabled")
	}
}

func TestGenerateOperations_NotConfigured(t *testing.T) {
	_, err := New(Config{}).GenerateOperations(context.Background(), TechCardContext{}, "sew it")
	if !errors.Is(err, ErrNotConfigured) {
		t.Errorf("want ErrNotConfigured, got %v", err)
	}
}

func TestGenerateOperations_Defaults(t *testing.T) {
	c := New(Config{APIKey: "k"})
	if c.Model() != defaultModel {
		t.Errorf("default model = %q, want %q", c.Model(), defaultModel)
	}
}

// TestGenerateOperations_RoundTrip stubs OpenRouter with httptest: it verifies the
// request (auth header, model, JSON body carrying the prompt) and that a well-formed
// chat response parses into operations — the full path minus a real API key.
func TestGenerateOperations_RoundTrip(t *testing.T) {
	var gotAuth, gotBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/chat/completions" {
			t.Errorf("unexpected path %q", r.URL.Path)
		}
		gotAuth = r.Header.Get("Authorization")
		b, _ := io.ReadAll(r.Body)
		gotBody = string(b)
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"model":"stub-model","choices":[{"message":{"role":"assistant","content":"{\"operations\":[{\"node\":\"join shoulders\",\"operation_type\":\"lockstitch\",\"time_norm_minutes\":0.5}],\"notes\":\"ok\"}"}}]}`)
	}))
	defer srv.Close()

	c := New(Config{APIKey: "secret-key", Model: "test/model", BaseURL: srv.URL})
	res, err := c.GenerateOperations(context.Background(), sampleContext(), "assemble it")
	if err != nil {
		t.Fatalf("GenerateOperations: %v", err)
	}
	if gotAuth != "Bearer secret-key" {
		t.Errorf("auth header = %q", gotAuth)
	}
	if !strings.Contains(gotBody, `"model":"test/model"`) {
		t.Errorf("request body missing model: %s", gotBody)
	}
	if !strings.Contains(gotBody, "assemble it") {
		t.Errorf("request body missing description brief: %s", gotBody)
	}
	if len(res.Operations) != 1 || res.Operations[0].Node != "join shoulders" {
		t.Fatalf("unexpected operations: %+v", res.Operations)
	}
	if res.Notes != "ok" {
		t.Errorf("notes = %q", res.Notes)
	}
}

func TestGenerateOperations_APIError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		io.WriteString(w, `{"error":{"message":"No auth credentials found","code":401}}`)
	}))
	defer srv.Close()

	c := New(Config{APIKey: "bad", BaseURL: srv.URL})
	_, err := c.GenerateOperations(context.Background(), TechCardContext{}, "sew it")
	if err == nil {
		t.Fatal("expected an API error")
	}
	if !strings.Contains(err.Error(), "No auth credentials found") || !strings.Contains(err.Error(), "401") {
		t.Errorf("error should surface the API message and status: %v", err)
	}
}

func TestGenerateOperations_MalformedModelJSON(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"choices":[{"message":{"content":"sorry, I can't do that"}}]}`)
	}))
	defer srv.Close()

	c := New(Config{APIKey: "k", BaseURL: srv.URL})
	_, err := c.GenerateOperations(context.Background(), TechCardContext{}, "sew it")
	if err == nil || !strings.Contains(err.Error(), "no JSON object") {
		t.Errorf("expected a clear parse error, got %v", err)
	}
}
