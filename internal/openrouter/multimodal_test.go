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
	"testing"
)

// contentIsStillAString is a COMPILE-TIME assertion, and it is the cheapest half of this file.
//
// The whole hazard of adding multimodal input is the tempting edit: retype chatMessage.Content from
// string to `any`. That edit compiles everywhere, changes no call site, and turns four live paid
// features into runtime shapes nobody checks. If anyone ever makes it, this line stops being valid
// Go and the package refuses to build — which is the only failure mode fast enough to matter.
var contentIsStillAString string = chatMessage{}.Content

// TestTextPathStillSendsAPlainStringContent is the other half: the type is one thing, the BYTES on
// the wire are what the provider sees. The text features must keep sending `"content":"…"`, not an
// array of parts.
func TestTextPathStillSendsAPlainStringContent(t *testing.T) {
	var gotBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		gotBody = string(b)
		io.WriteString(w, `{"choices":[{"message":{"role":"assistant","content":"ok"}}]}`)
	}))
	defer srv.Close()

	c := New(Config{APIKey: "k", Model: "shared/slug", BaseURL: srv.URL})
	if _, err := c.Complete(context.Background(), "sys", "user", false); err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if !strings.Contains(gotBody, `{"role":"system","content":"sys"}`) ||
		!strings.Contains(gotBody, `{"role":"user","content":"user"}`) {
		t.Fatalf("the text path no longer sends string content: %s", gotBody)
	}
	if strings.Contains(gotBody, `"type":"text"`) {
		t.Errorf("the text path grew content parts it never had: %s", gotBody)
	}

	// Decoded, not eyeballed: content must be a JSON string, not an array.
	var req struct {
		Messages []struct {
			Role    string          `json:"role"`
			Content json.RawMessage `json:"content"`
		} `json:"messages"`
	}
	if err := json.Unmarshal([]byte(gotBody), &req); err != nil {
		t.Fatalf("request body is not decodable: %v", err)
	}
	for _, m := range req.Messages {
		var s string
		if err := json.Unmarshal(m.Content, &s); err != nil {
			t.Errorf("role %q content is not a JSON string any more: %s", m.Role, m.Content)
		}
	}
}

// TestCompleteWithImages_SendsPartsAndKeepsTheSystemTurnAsAString pins the multimodal wire shape:
// a text part first, then one image_url part per picture, with the system turn untouched.
func TestCompleteWithImages_SendsPartsAndKeepsTheSystemTurnAsAString(t *testing.T) {
	var gotBody, gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		b, _ := io.ReadAll(r.Body)
		gotBody = string(b)
		io.WriteString(w, `{"choices":[{"message":{"role":"assistant","content":"a sketch of a coat"},"finish_reason":"stop"}],`+
			`"usage":{"prompt_tokens":1200,"completion_tokens":80,"total_tokens":1280}}`)
	}))
	defer srv.Close()

	c := New(Config{APIKey: "k", Model: "shared/slug", BaseURL: srv.URL})
	text, finish, usage, err := c.CompleteWithImages(context.Background(), "sys", "describe the moodboard",
		[]string{"https://media.grbpwr.com/mood/1.png", "data:image/png;base64,AAAA"}, true, 900)
	if err != nil {
		t.Fatalf("CompleteWithImages: %v", err)
	}

	if gotPath != "/chat/completions" {
		t.Errorf("path = %q — multimodal input is still a CHAT call; only the content shape differs", gotPath)
	}
	if text != "a sketch of a coat" || finish != "stop" {
		t.Errorf("text/finish = %q/%q", text, finish)
	}
	if usage.Prompt != 1200 || usage.Total != 1280 {
		t.Errorf("usage = %+v — a picture prompt is a paid call and its size must reach the caller", usage)
	}

	var req struct {
		Model    string `json:"model"`
		Messages []struct {
			Role    string          `json:"role"`
			Content json.RawMessage `json:"content"`
		} `json:"messages"`
		MaxTokens      int `json:"max_tokens"`
		ResponseFormat *struct {
			Type string `json:"type"`
		} `json:"response_format"`
	}
	if err := json.Unmarshal([]byte(gotBody), &req); err != nil {
		t.Fatalf("request body is not decodable: %v\n%s", err, gotBody)
	}
	if req.Model != "shared/slug" {
		t.Errorf("model = %q, want the shared slug", req.Model)
	}
	if req.MaxTokens != 900 {
		t.Errorf("max_tokens = %d, want 900", req.MaxTokens)
	}
	if req.ResponseFormat == nil || req.ResponseFormat.Type != "json_object" {
		t.Errorf("jsonMode did not reach the request: %s", gotBody)
	}
	if len(req.Messages) != 2 {
		t.Fatalf("messages = %d, want system + user", len(req.Messages))
	}

	// System turn: still a plain string, byte-identical in shape to the text path.
	var sys string
	if err := json.Unmarshal(req.Messages[0].Content, &sys); err != nil {
		t.Errorf("the system turn became parts: %s", req.Messages[0].Content)
	} else if sys != "sys" {
		t.Errorf("system content = %q", sys)
	}

	// User turn: parts, text first.
	var parts []struct {
		Type     string `json:"type"`
		Text     string `json:"text"`
		ImageURL *struct {
			URL string `json:"url"`
		} `json:"image_url"`
	}
	if err := json.Unmarshal(req.Messages[1].Content, &parts); err != nil {
		t.Fatalf("the user turn is not a parts array: %s", req.Messages[1].Content)
	}
	if len(parts) != 3 {
		t.Fatalf("parts = %d, want 1 text + 2 images", len(parts))
	}
	if parts[0].Type != "text" || parts[0].Text != "describe the moodboard" {
		t.Errorf("first part = %+v, want the instruction — a model reading it AFTER the pictures has already started guessing", parts[0])
	}
	if parts[1].Type != "image_url" || parts[1].ImageURL == nil || parts[1].ImageURL.URL != "https://media.grbpwr.com/mood/1.png" {
		t.Errorf("second part = %+v, want the nested {\"url\":…} object the wire format requires", parts[1])
	}
	if parts[2].ImageURL == nil || !strings.HasPrefix(parts[2].ImageURL.URL, "data:image/png;base64,") {
		t.Errorf("third part = %+v, want the data URI passed through", parts[2])
	}
	// A text part must never carry an image_url key and vice versa.
	if parts[0].ImageURL != nil {
		t.Errorf("the text part carries an image_url key: %s", req.Messages[1].Content)
	}
	if parts[1].Text != "" {
		t.Errorf("an image part carries text: %s", req.Messages[1].Content)
	}
}

// TestCompleteWithImages_NoPicturesIsStillAValidRequest: an empty moodboard is a legitimate state,
// and forcing the caller to branch between two methods would put the same prompt in two places.
func TestCompleteWithImages_NoPicturesIsStillAValidRequest(t *testing.T) {
	var gotBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		gotBody = string(b)
		io.WriteString(w, `{"choices":[{"message":{"role":"assistant","content":"ok"}}]}`)
	}))
	defer srv.Close()

	c := New(Config{APIKey: "k", Model: "m", BaseURL: srv.URL})
	if _, _, _, err := c.CompleteWithImages(context.Background(), "sys", "prompt", nil, false, 0); err != nil {
		t.Fatalf("CompleteWithImages: %v", err)
	}
	if !strings.Contains(gotBody, `"type":"text"`) || strings.Contains(gotBody, "image_url") {
		t.Errorf("empty picture list must produce a lone text part: %s", gotBody)
	}
	if strings.Contains(gotBody, "max_tokens") {
		t.Errorf("maxTokens<=0 must omit the cap, leaving the provider default: %s", gotBody)
	}
}

// TestCompleteWithImages_RefusesBadInputBeforeTheNetwork. Every case here is our own mistake; the
// provider would answer 400 and the log would read like a provider fault.
func TestCompleteWithImages_RefusesBadInputBeforeTheNetwork(t *testing.T) {
	called := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { called++ }))
	defer srv.Close()
	c := New(Config{APIKey: "k", Model: "m", BaseURL: srv.URL})

	tooMany := make([]string, MaxImageParts+1)
	for i := range tooMany {
		tooMany[i] = "https://media.grbpwr.com/x.png"
	}

	cases := []struct {
		name   string
		prompt string
		urls   []string
		want   string
	}{
		{"no prompt", "  ", []string{"https://x/y.png"}, "needs a prompt"},
		{"too many pictures", "p", tooMany, "exceeds the 16-picture limit"},
		{"not a url", "p", []string{"file:///etc/passwd"}, "must be http(s)://"},
		{"empty address", "p", []string{"   "}, "empty picture address"},
		{"data uri without payload", "p", []string{"data:image/png"}, "carries no payload"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, _, _, err := c.CompleteWithImages(context.Background(), "sys", tc.prompt, tc.urls, false, 0)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want it to mention %q", err, tc.want)
			}
		})
	}
	if called != 0 {
		t.Errorf("%d bad requests reached the provider", called)
	}
}

// TestCompleteWithImages_NotConfigured: no key, no network, no ambiguity.
func TestCompleteWithImages_NotConfigured(t *testing.T) {
	called := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { called = true }))
	defer srv.Close()

	c := New(Config{BaseURL: srv.URL})
	if _, _, _, err := c.CompleteWithImages(context.Background(), "s", "u", nil, false, 0); !errors.Is(err, ErrNotConfigured) {
		t.Fatalf("err = %v, want ErrNotConfigured", err)
	}
	if _, _, _, err := (*Client)(nil).CompleteWithImages(context.Background(), "s", "u", nil, false, 0); !errors.Is(err, ErrNotConfigured) {
		t.Fatalf("nil client: err = %v, want ErrNotConfigured", err)
	}
	if called {
		t.Error("an unconfigured client reached the provider")
	}
}

// TestCompleteWithImages_SharesTheTransportClassification proves the second request shape did not
// come with a second copy of the status rules: a 404 is still the settings fault, not weather.
func TestCompleteWithImages_SharesTheTransportClassification(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		io.WriteString(w, `{"error":{"message":"No endpoints found"}}`)
	}))
	defer srv.Close()

	c := New(Config{APIKey: "k", Model: "gone/slug", BaseURL: srv.URL})
	_, _, _, err := c.CompleteWithImages(context.Background(), "s", "u", nil, false, 0)
	if !errors.Is(err, ErrModelUnavailable) {
		t.Fatalf("err = %v, want ErrModelUnavailable — one transport, one classification", err)
	}
}

// TestResponseCeilingIsLoud is the regression for the silent-truncation defect on the TEXT path.
//
// Before this, a body over the ceiling came back as a prefix: json.Unmarshal then failed with
// "unexpected end of JSON input", which names the provider as the culprit for a cut this code made
// — and a prefix that happened to parse would have been accepted as a complete answer.
//
// The two halves are the proof: exactly at the ceiling still works, one byte over is refused BY
// NAME. Without the first half a ceiling that refuses everything would pass this test.
func TestResponseCeilingIsLoud(t *testing.T) {
	// A body that is valid JSON at any length: pad with spaces after the object.
	valid := `{"choices":[{"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]}`

	t.Run("exactly at the ceiling is fine", func(t *testing.T) {
		body := valid + strings.Repeat(" ", maxResponseBytes-len(valid))
		if len(body) != maxResponseBytes {
			t.Fatalf("fixture is %d bytes, want exactly %d", len(body), maxResponseBytes)
		}
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			io.WriteString(w, body)
		}))
		defer srv.Close()

		c := New(Config{APIKey: "k", Model: "m", BaseURL: srv.URL})
		text, err := c.Complete(context.Background(), "s", "u", false)
		if err != nil {
			t.Fatalf("a body exactly at the ceiling must be accepted, got %v", err)
		}
		if text != "ok" {
			t.Errorf("text = %q", text)
		}
	})

	t.Run("one byte over is refused by name", func(t *testing.T) {
		body := valid + strings.Repeat(" ", maxResponseBytes-len(valid)+1)
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			io.WriteString(w, body)
		}))
		defer srv.Close()

		c := New(Config{APIKey: "k", Model: "m", BaseURL: srv.URL})
		_, err := c.Complete(context.Background(), "s", "u", false)
		if !errors.Is(err, ErrResponseTooLarge) {
			t.Fatalf("err = %v, want ErrResponseTooLarge", err)
		}
		if strings.Contains(err.Error(), "unexpected end of JSON input") {
			t.Errorf("the cut is still being reported as the provider's malformed JSON: %v", err)
		}
	})
}

// TestModelProbeCeilingIsLoudToo: the probe reads a body as well, and a silent trim there would
// turn a truthful "no endpoints" verdict into an undecodable prefix.
func TestModelProbeCeilingIsLoudToo(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"data":{"endpoints":[]}}`, strings.Repeat(" ", maxResponseBytes))
	}))
	defer srv.Close()

	c := New(Config{APIKey: "k", Model: "m", BaseURL: srv.URL})
	err := c.CheckModel(context.Background())
	if !errors.Is(err, ErrResponseTooLarge) {
		t.Fatalf("err = %v, want ErrResponseTooLarge", err)
	}
	if errors.Is(err, ErrModelUnavailable) {
		t.Error("an oversized probe body must not produce a retirement verdict")
	}
}
