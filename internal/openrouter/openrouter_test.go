package openrouter

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
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
			{Name: "hood", PiecesPerGarment: 2},
		},
		BOM: []BOMItemContext{
			{Section: "fabric", Name: "French terry 320gsm", Composition: "100% cotton"},
			{Section: "thread", Name: "Poly core 120"},
		},
		Construction: &ConstructionContext{
			DefaultSeamClass:     "ss_plain",
			DefaultStitchesPerCm: "4",
			// The equipment park, already rendered by the caller. It replaces the single
			// OverlockThreadCount: one thread count could describe one overlock, and a card runs
			// several machines whose settings a step is expected to inherit rather than restate.
			MachineProfiles: []string{`overlock ("оверлок у окна"): 4 threads, ballpoint needle Nm 90, 4 st/cm`},
			PressProfiles:   []string{"fusing_press for fusing: 150 °C, 12 s, 3.5 N/cm², no steam"},
		},
	}
}

func TestBuildUserPrompt_IncludesContextAndDescription(t *testing.T) {
	p := buildUserPrompt(sampleContext(), "  serge the side seams then coverstitch the hem  ")

	for _, want := range []string{
		"Oversized Hoodie", "FW26-0007", "Hoodie",
		"front panel", "hood", "x2 per garment",
		"French terry 320gsm", "100% cotton",
		// The card DEFAULTS, which the prompt now states so a draft can omit the fields that match
		// them — the old free-text "lockstitch 301" was a stitch class the step already carries.
		"ss_plain", "4 stitches/cm",
		// The equipment park, for the same reason one step further: a draft that knows the card runs
		// a 4-thread overlock at 4 st/cm can name the machine and stay silent about the settings.
		"CARD MACHINES", `overlock ("оверлок у окна"): 4 threads`,
		"CARD PRESSING EQUIPMENT", "fusing_press for fusing: 150 °C",
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
	// A card with no equipment park says nothing about equipment. An empty heading would read as
	// «this style is sewn on no machines», which is a fact nobody entered.
	if strings.Contains(p, "CARD MACHINES") || strings.Contains(p, "CARD PRESSING EQUIPMENT") {
		t.Errorf("an empty equipment park must contribute nothing:\n%s", p)
	}
	if !strings.Contains(p, "Tee") || !strings.Contains(p, "sew it") {
		t.Errorf("required content missing:\n%s", p)
	}
}

// TestSystemPrompt_CarriesEveryVocabulary is the drift guard the hand-written lists could not have.
// A token missing from the prompt is a token the model NEVER emits, and nothing downstream can see
// that: the answer simply never contains the word. The previous prompt listed eight attachment
// kinds against a vocabulary of twelve and nobody noticed, which is the whole argument for rendering
// these from the entity slices.
func TestSystemPrompt_CarriesEveryVocabulary(t *testing.T) {
	for name, tokens := range map[string][]string{
		"operation_type":  entity.OperationTypeTokens,
		"machine_type":    entity.MachineTypeTokens,
		"press_equipment": entity.PressEquipmentTokens,
		"needle_type":     entity.NeedleTypeTokens,
		"thread_tension":  entity.ThreadTensionTokens,
		"press_cloth":     entity.PressClothTokens,
		"zone":            entity.GarmentZoneTokens,
		"seam_class":      entity.SeamClassTokens,
		"attachment_kind": entity.AttachmentKindTokens,
		"topstitch_mode":  entity.TopstitchModeTokens,
	} {
		for _, tok := range tokens {
			if tok == "unknown" { // a storage placeholder, never an answer
				continue
			}
			if !strings.Contains(systemPrompt, tok) {
				t.Errorf("%s token %q is missing from the system prompt — the model can never emit it", name, tok)
			}
		}
	}
	// The bands the SAVE enforces (§4.1) are stated from the same constants the save checks against,
	// so a draft outside them is a prompt bug rather than a surprise at the point of saving.
	for _, want := range []string{
		promptRange(entity.MinThreadCount, entity.MaxThreadCount),
		promptRange(entity.MinNeedleSizeNm, entity.MaxNeedleSizeNm),
		promptRange(entity.MinPressTemperatureC, entity.MaxPressTemperatureC),
		promptRange(entity.MinPressDwellSec, entity.MaxPressDwellSec),
		promptRange(entity.MinPressPressureNCm2, entity.MaxPressPressureNCm2),
		promptRange(entity.MinStitchWidthMm, entity.MaxStitchWidthMm),
	} {
		if !strings.Contains(systemPrompt, want) {
			t.Errorf("system prompt does not state the range %q", want)
		}
	}
	if strings.Contains(systemPrompt, "{{") {
		t.Errorf("an unfilled placeholder survived into the prompt:\n%s", systemPrompt)
	}
}

// TestSystemPrompt_OmissionMeansInheritNotNo guards the seam between the prompt and the NONE/UNKNOWN
// split. Once a step could inherit an attachment from a machine profile, «omit the field when no
// attachment is used» stopped being true and started being backwards: an omitted attachment_kind now
// puts the PROFILE'S foot on a step that was meant to run bare. The token is the only way to say no,
// and the prompt is the only place the model can learn that.
func TestSystemPrompt_OmissionMeansInheritNotNo(t *testing.T) {
	if strings.Contains(systemPrompt, "omit when no attachment is used") {
		t.Error("the prompt still tells the model to omit attachment_kind for «no attachment» — that now inherits the profile's")
	}
	for _, want := range []string{
		`"none" = SEWN BARE`,
		"AN OMITTED FIELD INHERITS; IT DOES NOT MEAN \"NO\".",
	} {
		if !strings.Contains(systemPrompt, want) {
			t.Errorf("the prompt does not state %q", want)
		}
	}
}

// TestSystemPrompt_CarriesTheThreadTensionQualifier: the scale is closed, so «other» says nothing on
// its own. The qualifier has to be askable in the prompt AND decodable in the answer — a field the
// prompt never mentions is one the model never sends, and a field the shape never declares is one
// the decoder silently drops.
func TestSystemPrompt_CarriesTheThreadTensionQualifier(t *testing.T) {
	if !strings.Contains(systemPrompt, "thread_tension_note") {
		t.Fatal("the prompt never asks for thread_tension_note; at tension «other» the draft can say nothing")
	}
	if !strings.Contains(systemPrompt, fmt.Sprint(entity.MaxThreadTensionNoteLen)) {
		t.Error("the prompt does not state the qualifier's length bound, which the save enforces")
	}
	var op Operation
	if err := json.Unmarshal([]byte(`{"thread_tension":"other","thread_tension_note":"x"}`), &op); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if op.ThreadTensionNote != "x" {
		t.Errorf("the answer shape drops the qualifier: %+v", op)
	}
}

// TestUserPrompt_PromisesInheritanceOnlyWhereThereIsOne. The context lines are rendered by the
// caller, which marks an equipment it holds several profiles of; the prompt's job is to have told the
// model what that mark means. A heading that promised inheritance unconditionally would be asking for
// omissions that inherit from nothing, because only a sole profile can be attached to a step.
func TestUserPrompt_PromisesInheritanceOnlyWhereThereIsOne(t *testing.T) {
	p := buildUserPrompt(TechCardContext{
		Construction: &ConstructionContext{
			MachineProfiles: []string{"overlock: 4 threads", "lockstitch [SEVERAL profiles of this equipment on the card]: 3 st/cm"},
			PressProfiles:   []string{"iron for press: 150 °C"},
		},
	}, "sew it")
	for _, want := range []string{"SEVERAL", "CARD MACHINES", "CARD PRESSING EQUIPMENT"} {
		if !strings.Contains(p, want) {
			t.Errorf("user prompt missing %q\n---\n%s", want, p)
		}
	}
	if !strings.Contains(systemPrompt, "SEVERAL") {
		t.Error("the system prompt never explains the SEVERAL mark the context uses")
	}
	// And it must not have kept the unconditional promise it replaced.
	if strings.Contains(p, "a machine step inherits these") {
		t.Error("the machines heading still promises inheritance unconditionally")
	}
}

func TestExtractJSON(t *testing.T) {
	cases := map[string]string{
		`{"operations":[]}`:                                          `{"operations":[]}`,
		"```json\n{\"operations\":[]}\n```":                          `{"operations":[]}`,
		"```\n{\"a\":1}\n```":                                        `{"a":1}`,
		"Here you go:\n{\"operations\":[{\"zone\":\"x\"}]}\nThanks!": `{"operations":[{"zone":"x"}]}`,
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
	    {"zone":"outer","note":"overlock side seams","operation_type":"overlock","stitches_per_cm":4,"smv_minutes":"0.8","operation_number":"10","callout_number":3},
	    {"zone":"hem","note":"coverstitch hem","operation_type":"coverstitch","stitches_per_cm":"5","smv_minutes":1.2,"operation_number":20}
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
	if o0.StitchesPerCm.String() != "4" || o0.SmvMinutes.String() != "0.8" ||
		o0.OperationNumber.String() != "10" || o0.CalloutNumber.String() != "3" {
		t.Errorf("op0 numeric parse: spc=%q tn=%q num=%q co=%q",
			o0.StitchesPerCm, o0.SmvMinutes, o0.OperationNumber, o0.CalloutNumber)
	}
	o1 := r.Operations[1]
	if o1.StitchesPerCm.String() != "5" || o1.SmvMinutes.String() != "1.2" || o1.OperationNumber.String() != "20" {
		t.Errorf("op1 numeric parse: spc=%q tn=%q num=%q", o1.StitchesPerCm, o1.SmvMinutes, o1.OperationNumber)
	}
}

// TestParseResult_MachineAndPressBlocks pins the shape the split produced: the machine axis, the
// ВТО block, a LEGACY type word (which the model will keep answering with — it was the operation
// type until this phase and is in every sewing text), and a press step that names no equipment.
//
// The last one is the important one: an incomplete step is a DRAFT WITH A BLANK, not a parse error.
// Refusing the document would throw away the three complete steps beside it.
func TestParseResult_MachineAndPressBlocks(t *testing.T) {
	content := `{
	  "operations": [
	    {"zone":"shoulder","operation_type":"overlock","stitches_per_cm":4},
	    {"zone":"hem","operation_type":"machine","machine_type":"coverstitch","thread_count":4,
	     "needle_type":"ballpoint","needle_size_nm":"90","thread_tension":"looser","stitch_width_mm":"5.2"},
	    {"zone":"front","operation_type":"press_open","press_equipment":"iron","press_temperature_c":150,
	     "press_dwell_sec":"12","press_pressure_n_cm2":"3.5","press_steam":"no","press_cloth":"damp_press_cloth"},
	    {"zone":"collar","operation_type":"press"}
	  ]
	}`
	r, err := parseResult(content)
	if err != nil {
		t.Fatalf("parseResult: %v", err)
	}
	if len(r.Operations) != 4 {
		t.Fatalf("want 4 operations, got %d", len(r.Operations))
	}

	// The legacy word survives the transport untouched; canonicalising it into (machine, overlock)
	// is the mapper's job one package over, and it needs the word to do it.
	if got := r.Operations[0].OperationType; got != "overlock" {
		t.Errorf("legacy operation_type = %q, want it carried through verbatim", got)
	}

	m := r.Operations[1]
	if m.MachineType != "coverstitch" || m.NeedleType != "ballpoint" || m.ThreadTension != "looser" {
		t.Errorf("machine tokens: type=%q needle=%q tension=%q", m.MachineType, m.NeedleType, m.ThreadTension)
	}
	// Numbers arrive as numbers OR as strings, and both spellings appear in this one step.
	if m.ThreadCount.String() != "4" || m.NeedleSizeNm.String() != "90" || m.StitchWidthMm.String() != "5.2" {
		t.Errorf("machine numbers: threads=%q needle=%q width=%q",
			m.ThreadCount, m.NeedleSizeNm, m.StitchWidthMm)
	}

	p := r.Operations[2]
	if p.PressEquipment != "iron" || p.PressCloth != "damp_press_cloth" {
		t.Errorf("press tokens: equipment=%q cloth=%q", p.PressEquipment, p.PressCloth)
	}
	if p.PressTemperatureC.String() != "150" || p.PressDwellSec.String() != "12" || p.PressPressureNCm2.String() != "3.5" {
		t.Errorf("press numbers: t=%q dwell=%q pressure=%q",
			p.PressTemperatureC, p.PressDwellSec, p.PressPressureNCm2)
	}
	// «no» is an ANSWER — «без пара» — and has to reach the wire as an explicit false, not as silence.
	steam := p.PressSteam.Ptr()
	if steam == nil || *steam {
		t.Errorf("press_steam «no» = %v, want an explicit false", steam)
	}

	bare := r.Operations[3]
	if bare.OperationType != "press" || bare.PressEquipment != "" {
		t.Errorf("bare press step = %+v", bare)
	}
	if bare.PressSteam.Ptr() != nil {
		t.Error("an unstated press_steam must stay unstated, not become false")
	}
}

// TestJSONBool_Tolerance: a model hedges in words, and a plain *bool would answer an
// UnmarshalTypeError for the WHOLE document — one «yes» in one step would cost the entire draft.
func TestJSONBool_Tolerance(t *testing.T) {
	cases := map[string]*bool{
		`true`:    ptr(true),
		`"true"`:  ptr(true),
		`"Yes"`:   ptr(true),
		`1`:       ptr(true),
		`false`:   ptr(false),
		`"no"`:    ptr(false),
		`0`:       ptr(false),
		`null`:    nil,
		`""`:      nil,
		`"maybe"`: nil, // unrecognised = not stated, never an error
	}
	for in, want := range cases {
		var op Operation
		if err := json.Unmarshal([]byte(`{"press_steam":`+in+`}`), &op); err != nil {
			t.Fatalf("press_steam %s must never fail to unmarshal: %v", in, err)
		}
		got := op.PressSteam.Ptr()
		switch {
		case want == nil && got != nil:
			t.Errorf("press_steam %s = %v, want unstated", in, *got)
		case want != nil && (got == nil || *got != *want):
			t.Errorf("press_steam %s = %v, want %v", in, got, *want)
		}
	}
}

func ptr(b bool) *bool { return &b }

func TestParseResult_Fenced(t *testing.T) {
	r, err := parseResult("```json\n{\"operations\":[{\"zone\":\"sleeve\"}]}\n```")
	if err != nil {
		t.Fatalf("parseResult fenced: %v", err)
	}
	if len(r.Operations) != 1 || r.Operations[0].Zone != "sleeve" {
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
	if err := json.Unmarshal([]byte(`{"zone":"x","stitches_per_cm":null}`), &op); err != nil {
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
		io.WriteString(w, `{"model":"stub-model","choices":[{"message":{"role":"assistant","content":"{\"operations\":[{\"zone\":\"shoulder\",\"operation_type\":\"lockstitch\",\"smv_minutes\":0.5}],\"notes\":\"ok\"}"}}]}`)
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
	if len(res.Operations) != 1 || res.Operations[0].Zone != "shoulder" {
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

// TestChat_ModelUnavailableIsAConfigurationFault reproduces the beta outage of 2026-08-17 byte for
// byte: `anthropic/claude-3.5-sonnet` was retired at the provider, the call came back in 0.2 s with
// HTTP 404 and the body below, and every caller reported it as "unavailable right now — try again
// in a moment". It was never going to become available, and the retry never had a chance.
//
// What is pinned here is the SPLIT, not the wording: a 404 is a configuration fault (a sentinel the
// caller can branch on), while 5xx and a broken transport stay ordinary errors. The provider's own
// sentence must still travel inside the error — it is what a log reader needs — but it must not be
// what decides the classification.
func TestChat_ModelUnavailableIsAConfigurationFault(t *testing.T) {
	// The exact body the live provider returned; kept verbatim so this test breaks if we ever start
	// depending on parsing it.
	const liveBody = `{"error":{"message":"No endpoints found for anthropic/claude-3.5-sonnet.","code":404}}`

	t.Run("404 becomes ErrModelUnavailable", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusNotFound)
			io.WriteString(w, liveBody)
		}))
		defer srv.Close()

		c := New(Config{APIKey: "k", Model: "anthropic/claude-3.5-sonnet", BaseURL: srv.URL})

		// Both entry points must classify identically: the note assistant goes through Complete,
		// the tech-card draft through GenerateOperations, and they share one client.
		_, errComplete := c.Complete(context.Background(), "sys", "user", false)
		if !errors.Is(errComplete, ErrModelUnavailable) {
			t.Errorf("Complete: want ErrModelUnavailable, got %v", errComplete)
		}
		_, errGen := c.GenerateOperations(context.Background(), TechCardContext{}, "sew it")
		if !errors.Is(errGen, ErrModelUnavailable) {
			t.Errorf("GenerateOperations: want ErrModelUnavailable, got %v", errGen)
		}

		// The provider's sentence and the status still ride along for the log: the sentinel says
		// what KIND of fault it is, it does not swallow what happened.
		for _, err := range []error{errComplete, errGen} {
			if err == nil {
				continue
			}
			if !strings.Contains(err.Error(), "No endpoints found for anthropic/claude-3.5-sonnet.") {
				t.Errorf("provider message must survive into the error text: %v", err)
			}
			if !strings.Contains(err.Error(), "404") {
				t.Errorf("status must survive into the error text: %v", err)
			}
		}
	})

	t.Run("5xx stays an ordinary retryable error", func(t *testing.T) {
		// The other half of the split. Without this, "everything is a configuration fault" would
		// pass the test above and mislabel every genuine outage.
		for _, code := range []int{http.StatusInternalServerError, http.StatusBadGateway, http.StatusServiceUnavailable, http.StatusTooManyRequests} {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(code)
				io.WriteString(w, `{"error":{"message":"upstream is having a moment"}}`)
			}))
			_, err := New(Config{APIKey: "k", BaseURL: srv.URL}).Complete(context.Background(), "sys", "user", false)
			srv.Close()
			if err == nil {
				t.Fatalf("HTTP %d: expected an error", code)
			}
			if errors.Is(err, ErrModelUnavailable) {
				t.Errorf("HTTP %d must NOT be a configuration fault: %v", code, err)
			}
		}
	})

	t.Run("a dead transport stays an ordinary error", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
		url := srv.URL
		srv.Close() // nothing is listening any more
		_, err := New(Config{APIKey: "k", BaseURL: url}).Complete(context.Background(), "sys", "user", false)
		if err == nil {
			t.Fatal("expected a transport error")
		}
		if errors.Is(err, ErrModelUnavailable) {
			t.Errorf("a transport failure must NOT be a configuration fault: %v", err)
		}
	})
}

// TestCheckModel pins the STARTUP PROBE, and mostly it pins the two ways of building it wrong.
// Both were measured against the live API before this was written:
//
//   - GET /models/{slug} — the obvious route — answers 404 for every slug, including live ones.
//     A probe on that route shouts on every boot of every deployment.
//   - GET /models/{slug}/endpoints answers 200 for the RETIRED slug as well. What separates it
//     from a live one is an EMPTY endpoints array — 9 endpoints for anthropic/claude-sonnet-5,
//     0 for anthropic/claude-3.5-sonnet. So the empty array is the alarm, not the status.
//
// The rest is the silence contract: anything that is not a clear verdict must produce no alarm,
// because an alarm that fires on a slow network teaches people to ignore the real one.
func TestCheckModel(t *testing.T) {
	// Bodies trimmed from the real responses, keeping the shape that matters.
	const liveBody = `{"data":{"id":"anthropic/claude-sonnet-5","endpoints":[{"provider_name":"Anthropic"},{"provider_name":"Azure"}]}}`
	const retiredBody = `{"data":{"id":"anthropic/claude-3.5-sonnet","endpoints":[]}}`

	probe := func(t *testing.T, status int, body string) (error, string) {
		t.Helper()
		var gotPath, gotAuth string
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			gotPath, gotAuth = r.URL.Path, r.Header.Get("Authorization")
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(status)
			io.WriteString(w, body)
		}))
		defer srv.Close()
		c := New(Config{APIKey: "k", Model: "anthropic/claude-sonnet-5", BaseURL: srv.URL})
		err := c.CheckModel(context.Background())
		if gotAuth != "" {
			t.Errorf("the probe must not send the key to a public route, sent %q", gotAuth)
		}
		return err, gotPath
	}

	t.Run("a slug with live endpoints is silent", func(t *testing.T) {
		err, path := probe(t, http.StatusOK, liveBody)
		if err != nil {
			t.Errorf("want no verdict, got %v", err)
		}
		if path != "/models/anthropic/claude-sonnet-5/endpoints" {
			t.Errorf("probe hit %q — the /models/{slug} route 404s for every slug, live ones too", path)
		}
	})

	t.Run("200 with an EMPTY endpoints array is the alarm", func(t *testing.T) {
		// This is the exact case that broke beta, and the case a status-only probe misses.
		err, _ := probe(t, http.StatusOK, retiredBody)
		if !errors.Is(err, ErrModelUnavailable) {
			t.Errorf("a retired slug answers 200 with zero endpoints; want ErrModelUnavailable, got %v", err)
		}
	})

	t.Run("404 (slug never existed) is the alarm too", func(t *testing.T) {
		err, _ := probe(t, http.StatusNotFound, `{"error":{"message":"No endpoints found."}}`)
		if !errors.Is(err, ErrModelUnavailable) {
			t.Errorf("want ErrModelUnavailable, got %v", err)
		}
	})

	t.Run("everything unclear is silence, not bad news", func(t *testing.T) {
		for name, tc := range map[string]struct {
			status int
			body   string
		}{
			"provider outage":    {http.StatusInternalServerError, `{"error":{"message":"boom"}}`},
			"proxy demands auth": {http.StatusUnauthorized, `{"error":{"message":"no credentials"}}`},
			"not json at all":    {http.StatusOK, `<html>proxy error</html>`},
			"reshaped api":       {http.StatusOK, `{"data":{"id":"x"}}`},
			"no data envelope":   {http.StatusOK, `{"items":[]}`},
		} {
			t.Run(name, func(t *testing.T) {
				err, _ := probe(t, tc.status, tc.body)
				if err == nil {
					t.Errorf("want an ordinary error, got a clean bill of health")
				}
				if errors.Is(err, ErrModelUnavailable) {
					t.Errorf("must NOT raise the alarm on an unclear answer: %v", err)
				}
			})
		}
	})

	t.Run("an unreachable provider is silence", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
		url := srv.URL
		srv.Close()
		err := New(Config{APIKey: "k", BaseURL: url}).CheckModel(context.Background())
		if err == nil || errors.Is(err, ErrModelUnavailable) {
			t.Errorf("a boot with no network must not accuse the model: %v", err)
		}
	})

	t.Run("no key: the provider is not called at all", func(t *testing.T) {
		called := false
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { called = true }))
		defer srv.Close()
		if err := New(Config{BaseURL: srv.URL}).CheckModel(context.Background()); !errors.Is(err, ErrNotConfigured) {
			t.Errorf("want ErrNotConfigured, got %v", err)
		}
		if called {
			t.Error("a disabled client must not probe")
		}
	})
}

// TestWarnIfModelRetired pins the START-UP CONTRACT, which matters more here than the message:
// this runs while the process is coming up, and a check that can block or crash a boot is a worse
// defect than the one it reports.
func TestWarnIfModelRetired(t *testing.T) {
	t.Run("returns immediately and probes in the background", func(t *testing.T) {
		hit := make(chan string, 1)
		release := make(chan struct{})
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			<-release // the handler is held: a caller that waited for it would be stuck here
			hit <- r.URL.Path
			w.Header().Set("Content-Type", "application/json")
			io.WriteString(w, `{"data":{"endpoints":[]}}`)
		}))
		defer srv.Close()

		c := New(Config{APIKey: "k", Model: "m/x", BaseURL: srv.URL})
		start := time.Now()
		c.WarnIfModelRetired()
		if elapsed := time.Since(start); elapsed > time.Second {
			t.Fatalf("start-up was blocked for %v", elapsed)
		}
		close(release)
		select {
		case path := <-hit:
			if path != "/models/m/x/endpoints" {
				t.Errorf("probed %q", path)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("the background probe never ran")
		}
	})

	t.Run("no key: no goroutine, no call", func(t *testing.T) {
		called := make(chan struct{}, 1)
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { called <- struct{}{} }))
		defer srv.Close()
		New(Config{BaseURL: srv.URL}).WarnIfModelRetired()
		select {
		case <-called:
			t.Error("a disabled client must not probe at boot")
		case <-time.After(300 * time.Millisecond):
		}
	})

	t.Run("a nil client is a no-op, not a boot crash", func(t *testing.T) {
		var c *Client
		c.WarnIfModelRetired()
	})
}

// TestCompleteWithMeta_CarriesFinishReasonAndUsage pins the two numbers the analysis pass cannot
// work without, and it asserts them NON-ZERO on purpose.
//
// Both failure modes here are silent. A forgotten `json:"usage"` tag (the field was not parsed at
// all before this change) leaves every count at zero while the call still succeeds — so a test that
// only checked "no error", or that compared against a zero-valued Usage, would pass on exactly the
// bug it is meant to catch. A dropped finish_reason turns a reply truncated by the token cap into
// something indistinguishable from a model that emitted broken JSON.
func TestCompleteWithMeta_CarriesFinishReasonAndUsage(t *testing.T) {
	var gotBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		gotBody = string(b)
		w.Header().Set("Content-Type", "application/json")
		// Shaped like a real OpenRouter reply: usage is a sibling of choices, not a member of one.
		io.WriteString(w, `{"model":"stub-model","choices":[{"message":{"role":"assistant","content":"{\"findings\":[]}"},"finish_reason":"length"}],`+
			`"usage":{"prompt_tokens":4321,"completion_tokens":2500,"total_tokens":6821}}`)
	}))
	defer srv.Close()

	c := New(Config{APIKey: "k", Model: "shared/slug", BaseURL: srv.URL})
	text, finishReason, usage, err := c.CompleteWithMeta(context.Background(), "sys", "user", true, 2500)
	if err != nil {
		t.Fatalf("CompleteWithMeta: %v", err)
	}
	if text != `{"findings":[]}` {
		t.Errorf("content = %q", text)
	}
	if finishReason != "length" {
		t.Errorf("finishReason = %q, want %q — without it a truncated reply reads as malformed JSON", finishReason, "length")
	}
	if usage.Prompt != 4321 {
		t.Errorf("usage.Prompt = %d, want 4321 — zero means the `json:\"prompt_tokens\"` field never decoded", usage.Prompt)
	}
	if usage.Completion != 2500 {
		t.Errorf("usage.Completion = %d, want 2500 — zero means the `json:\"completion_tokens\"` field never decoded", usage.Completion)
	}
	if usage.Total != 6821 {
		t.Errorf("usage.Total = %d, want 6821 — zero means the response has no `json:\"usage\"` tag and every run logs as free", usage.Total)
	}
	if !strings.Contains(gotBody, `"max_tokens":2500`) {
		t.Errorf("request body carries no explicit completion cap: %s", gotBody)
	}
	if !strings.Contains(gotBody, `"response_format":{"type":"json_object"}`) {
		t.Errorf("jsonMode did not reach the request: %s", gotBody)
	}
}

// TestCompleteWithMeta_UsesTheAnalysisSlugWhileCompleteKeepsTheSharedOne pins the whole point of
// OPENROUTER_MODEL_ANALYSIS: it escalates the analysis pass ALONE. If it leaked into Complete, note
// formatting and campaign translation would silently move to a different (and pricier) model; if it
// never reached CompleteWithMeta, the variable would be decoration.
func TestCompleteWithMeta_UsesTheAnalysisSlugWhileCompleteKeepsTheSharedOne(t *testing.T) {
	var mu sync.Mutex
	var bodies []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		mu.Lock()
		bodies = append(bodies, string(b))
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"choices":[{"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]}`)
	}))
	defer srv.Close()

	c := New(Config{APIKey: "k", Model: "shared/slug", ModelAnalysis: "escalated/slug", BaseURL: srv.URL})
	if _, _, _, err := c.CompleteWithMeta(context.Background(), "sys", "user", false, 0); err != nil {
		t.Fatalf("CompleteWithMeta: %v", err)
	}
	if _, err := c.Complete(context.Background(), "sys", "user", false); err != nil {
		t.Fatalf("Complete: %v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(bodies) != 2 {
		t.Fatalf("want 2 requests, got %d", len(bodies))
	}
	if !strings.Contains(bodies[0], `"model":"escalated/slug"`) {
		t.Errorf("CompleteWithMeta did not send the analysis slug: %s", bodies[0])
	}
	if !strings.Contains(bodies[1], `"model":"shared/slug"`) {
		t.Errorf("Complete must stay on the shared slug, sent: %s", bodies[1])
	}
	// maxTokens <= 0 must leave the provider default in force rather than send a cap of zero,
	// which the API would read as "no room for an answer".
	if strings.Contains(bodies[1], "max_tokens") {
		t.Errorf("an unset cap must be omitted from the request, not sent as zero: %s", bodies[1])
	}
}

// TestAnalysisModel_UnsetOverrideMeansTheSharedSlug covers the state every deployment is actually
// in. Empty is not an error and must not become a second baked-in default.
func TestAnalysisModel_UnsetOverrideMeansTheSharedSlug(t *testing.T) {
	for name, tc := range map[string]struct {
		cfg  Config
		want string
	}{
		"override unset":        {Config{Model: "shared/slug"}, "shared/slug"},
		"override blank":        {Config{Model: "shared/slug", ModelAnalysis: "   "}, "shared/slug"},
		"override set":          {Config{Model: "shared/slug", ModelAnalysis: " escalated/slug "}, "escalated/slug"},
		"nothing configured":    {Config{}, defaultModel},
		"override with no base": {Config{ModelAnalysis: "escalated/slug"}, "escalated/slug"},
	} {
		t.Run(name, func(t *testing.T) {
			if got := New(tc.cfg).AnalysisModel(); got != tc.want {
				t.Errorf("AnalysisModel() = %q, want %q", got, tc.want)
			}
		})
	}

	t.Run("a nil client is not a panic", func(t *testing.T) {
		var c *Client
		if got := c.AnalysisModel(); got != "" {
			t.Errorf("AnalysisModel() = %q, want empty", got)
		}
	})
}

// TestWarnIfModelRetired_ProbesEveryEffectiveSlugOnce extends the boot warning to the SET of slugs
// the client can send. A retired analysis slug is the same invisible fault as a retired shared one
// — and probing a single slug twice, when the override happens to equal the shared value, would
// shout twice about one fault and double the boot traffic.
func TestWarnIfModelRetired_ProbesEveryEffectiveSlugOnce(t *testing.T) {
	probe := func(t *testing.T, cfg Config, want int) []string {
		t.Helper()
		var mu sync.Mutex
		paths := []string{}
		done := make(chan struct{})
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			mu.Lock()
			paths = append(paths, r.URL.Path)
			if len(paths) == want {
				close(done)
			}
			mu.Unlock()
			w.Header().Set("Content-Type", "application/json")
			io.WriteString(w, `{"data":{"endpoints":[]}}`) // the retired shape: 200, zero endpoints
		}))
		defer srv.Close()

		cfg.APIKey, cfg.BaseURL = "k", srv.URL
		New(cfg).WarnIfModelRetired()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			mu.Lock()
			got := append([]string{}, paths...)
			mu.Unlock()
			t.Fatalf("want %d probes, saw %v", want, got)
		}
		// Give a stray extra probe a chance to arrive before counting.
		time.Sleep(200 * time.Millisecond)
		mu.Lock()
		defer mu.Unlock()
		if len(paths) != want {
			t.Fatalf("want %d probes, got %d: %v", want, len(paths), paths)
		}
		return append([]string{}, paths...)
	}

	t.Run("both slugs are probed when the override differs", func(t *testing.T) {
		paths := probe(t, Config{Model: "shared/slug", ModelAnalysis: "escalated/slug"}, 2)
		seen := map[string]bool{}
		for _, p := range paths {
			seen[p] = true
		}
		if !seen["/models/shared/slug/endpoints"] {
			t.Errorf("the shared slug was not probed: %v", paths)
		}
		if !seen["/models/escalated/slug/endpoints"] {
			t.Errorf("the analysis slug was not probed — a retired override would stay invisible: %v", paths)
		}
	})

	t.Run("one probe when the override repeats the shared slug", func(t *testing.T) {
		paths := probe(t, Config{Model: "shared/slug", ModelAnalysis: "shared/slug"}, 1)
		if paths[0] != "/models/shared/slug/endpoints" {
			t.Errorf("probed %v", paths)
		}
	})

	t.Run("one probe when no override is set", func(t *testing.T) {
		paths := probe(t, Config{Model: "shared/slug"}, 1)
		if paths[0] != "/models/shared/slug/endpoints" {
			t.Errorf("probed %v", paths)
		}
	})
}
