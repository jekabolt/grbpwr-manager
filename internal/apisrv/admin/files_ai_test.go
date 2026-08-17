package admin

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/jekabolt/grbpwr-manager/internal/openrouter"
	pb_admin "github.com/jekabolt/grbpwr-manager/proto/gen/admin"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// fakeORCall is what the fake OpenRouter endpoint saw on one call.
type fakeORCall struct {
	System         string
	User           string
	ResponseFormat any
}

// newFakeOpenRouter stands up a fake chat/completions endpoint and returns a client wired to it
// plus a pointer to the recorded calls. The handler decides the reply, so a test can serve a good
// answer, a 500, or an empty message.
func newFakeOpenRouter(t *testing.T, reply func(w http.ResponseWriter)) (*openrouter.Client, *[]fakeORCall) {
	t.Helper()
	calls := make([]fakeORCall, 0, 2)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		require.NoError(t, err)
		var req struct {
			Messages []struct {
				Role    string `json:"role"`
				Content string `json:"content"`
			} `json:"messages"`
			ResponseFormat any `json:"response_format"`
		}
		require.NoError(t, json.Unmarshal(body, &req))
		call := fakeORCall{ResponseFormat: req.ResponseFormat}
		for _, m := range req.Messages {
			switch m.Role {
			case "system":
				call.System = m.Content
			case "user":
				call.User = m.Content
			}
		}
		calls = append(calls, call)
		reply(w)
	}))
	t.Cleanup(srv.Close)
	return openrouter.New(openrouter.Config{APIKey: "test-key", BaseURL: srv.URL}), &calls
}

// orReplyWithContent serves one well-formed completion carrying the given assistant message.
func orReplyWithContent(content string) func(http.ResponseWriter) {
	return func(w http.ResponseWriter) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"choices": []any{map[string]any{"message": map[string]any{"role": "assistant", "content": content}}},
		})
	}
}

// TestFormatLibraryNoteMarkdownNotConfigured pins the beta default: no OPENROUTER_API_KEY means a
// clear FailedPrecondition, never a panic and never an empty suggestion. Both shapes of "no key"
// are covered — a nil client (the field is nil-safe) and a client built with an empty key.
func TestFormatLibraryNoteMarkdownNotConfigured(t *testing.T) {
	for name, s := range map[string]*Server{
		"nil client":     {},
		"client, no key": {aiOps: openrouter.New(openrouter.Config{})},
		"key is blank":   {aiOps: openrouter.New(openrouter.Config{APIKey: "   "})},
	} {
		t.Run(name, func(t *testing.T) {
			resp, err := s.FormatLibraryNoteMarkdown(context.Background(),
				&pb_admin.FormatLibraryNoteMarkdownRequest{Content: "первая строка\nвторая строка"})
			require.Nil(t, resp)
			require.Error(t, err)
			require.Equal(t, codes.FailedPrecondition, status.Code(err))
			require.Equal(t, noteFormatNotConfiguredMsg, status.Convert(err).Message())
		})
	}
}

// TestFormatLibraryNoteMarkdownRuneCap proves the cap is measured in RUNES and enforced BEFORE the
// model is called: 12 000 Cyrillic characters (24 000 bytes) go through, 12 001 are refused
// without a request leaving the process.
func TestFormatLibraryNoteMarkdownRuneCap(t *testing.T) {
	client, calls := newFakeOpenRouter(t, orReplyWithContent("# ок"))
	s := &Server{aiOps: client}

	atCap := strings.Repeat("я", maxNoteFormatRunes)
	require.Len(t, []byte(atCap), 2*maxNoteFormatRunes, "the fixture must be multi-byte, else the test proves nothing")
	resp, err := s.FormatLibraryNoteMarkdown(context.Background(),
		&pb_admin.FormatLibraryNoteMarkdownRequest{Content: atCap})
	require.NoError(t, err)
	require.Equal(t, "# ок", resp.GetContent())
	require.Len(t, *calls, 1)

	overCap := strings.Repeat("я", maxNoteFormatRunes+1)
	resp, err = s.FormatLibraryNoteMarkdown(context.Background(),
		&pb_admin.FormatLibraryNoteMarkdownRequest{Content: overCap})
	require.Nil(t, resp)
	require.Equal(t, codes.InvalidArgument, status.Code(err))
	require.Contains(t, status.Convert(err).Message(), "longer than the assistant takes")
	require.Len(t, *calls, 1, "an over-cap request must never reach the model")
}

// TestFormatLibraryNoteMarkdownEmptyContent: nothing to format is a caller error, not a model call.
func TestFormatLibraryNoteMarkdownEmptyContent(t *testing.T) {
	client, calls := newFakeOpenRouter(t, orReplyWithContent("# ок"))
	s := &Server{aiOps: client}

	for _, content := range []string{"", "   \n\t "} {
		resp, err := s.FormatLibraryNoteMarkdown(context.Background(),
			&pb_admin.FormatLibraryNoteMarkdownRequest{Content: content})
		require.Nil(t, resp)
		require.Equal(t, codes.InvalidArgument, status.Code(err))
	}
	require.Empty(t, *calls)
}

// TestFormatLibraryNoteMarkdownSendsOnlyTheText is the leak-perimeter test: the user message is
// byte-for-byte the content that was sent, and nothing else travels with it. jsonMode stays off —
// the answer must be a markdown document, not a JSON envelope.
func TestFormatLibraryNoteMarkdownSendsOnlyTheText(t *testing.T) {
	client, calls := newFakeOpenRouter(t, orReplyWithContent("# заголовок\n\nтекст"))
	s := &Server{aiOps: client}

	const note = "заголовок\n\nтекст про ткань\n- пункт"
	resp, err := s.FormatLibraryNoteMarkdown(context.Background(),
		&pb_admin.FormatLibraryNoteMarkdownRequest{Content: note})
	require.NoError(t, err)
	require.Equal(t, "# заголовок\n\nтекст", resp.GetContent())

	require.Len(t, *calls, 1)
	call := (*calls)[0]
	require.Equal(t, note, call.User, "the model must receive exactly the submitted text")
	require.Equal(t, noteFormatSystemPrompt, call.System)
	require.Nil(t, call.ResponseFormat, "jsonMode must stay off for a markdown answer")
}

// TestFormatLibraryNoteMarkdownUnwrapsFence: a model that wraps the whole document in a fence would
// otherwise hand back a note that is one giant code block.
func TestFormatLibraryNoteMarkdownUnwrapsFence(t *testing.T) {
	client, _ := newFakeOpenRouter(t, orReplyWithContent("```markdown\n# заголовок\n\nтекст\n```"))
	s := &Server{aiOps: client}

	resp, err := s.FormatLibraryNoteMarkdown(context.Background(),
		&pb_admin.FormatLibraryNoteMarkdownRequest{Content: "заголовок\n\nтекст"})
	require.NoError(t, err)
	require.Equal(t, "# заголовок\n\nтекст", resp.GetContent())
}

// TestFormatLibraryNoteMarkdownUpstreamFailure: a provider failure is Unavailable, and its raw text
// (which can carry provider ids, quota details and key hints) does not travel to the client.
func TestFormatLibraryNoteMarkdownUpstreamFailure(t *testing.T) {
	client, _ := newFakeOpenRouter(t, func(w http.ResponseWriter) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"error":{"message":"upstream provider exploded: key sk-secret-123"}}`))
	})
	s := &Server{aiOps: client}

	resp, err := s.FormatLibraryNoteMarkdown(context.Background(),
		&pb_admin.FormatLibraryNoteMarkdownRequest{Content: "текст"})
	require.Nil(t, resp)
	require.Equal(t, codes.Unavailable, status.Code(err))
	require.NotContains(t, status.Convert(err).Message(), "sk-secret-123")
	require.NotContains(t, status.Convert(err).Message(), "exploded")
}

// TestFormatLibraryNoteMarkdownEmptyAnswer: a model that says nothing is a content problem
// (Internal), not a transport one — and it must never surface as an empty "suggestion" that a
// client could paste over the author's text.
func TestFormatLibraryNoteMarkdownEmptyAnswer(t *testing.T) {
	for name, reply := range map[string]func(http.ResponseWriter){
		"blank message": orReplyWithContent("   \n  "),
		"no choices": func(w http.ResponseWriter) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"choices":[]}`))
		},
	} {
		t.Run(name, func(t *testing.T) {
			client, _ := newFakeOpenRouter(t, reply)
			s := &Server{aiOps: client}
			resp, err := s.FormatLibraryNoteMarkdown(context.Background(),
				&pb_admin.FormatLibraryNoteMarkdownRequest{Content: "текст"})
			require.Nil(t, resp)
			require.Equal(t, codes.Internal, status.Code(err))
		})
	}
}

// TestStripWrappingCodeFence pins where unwrapping stops: only a true wrapper is removed, and real
// content that merely begins with a fence is left alone.
func TestStripWrappingCodeFence(t *testing.T) {
	for name, tc := range map[string]struct{ in, want string }{
		"markdown wrapper": {"```markdown\n# a\n\nb\n```", "# a\n\nb"},
		"md wrapper":       {"```md\n# a\n```", "# a"},
		"bare wrapper":     {"```\n# a\n\nb\n```", "# a\n\nb"},
		"wrapper with inner fence": {
			"```markdown\n# a\n\n```go\nx := 1\n```\n```",
			"# a\n\n```go\nx := 1\n```",
		},
		"lone code block is content": {
			"```\nx := 1\n```\n\nтекст\n\n```\ny := 2\n```",
			"```\nx := 1\n```\n\nтекст\n\n```\ny := 2\n```",
		},
		"tagged block is content": {"```go\nx := 1\n```", "```go\nx := 1\n```"},
		"no fence at all":         {"# a\n\nb", "# a\n\nb"},
		"unclosed fence":          {"```markdown\n# a", "```markdown\n# a"},
	} {
		t.Run(name, func(t *testing.T) {
			require.Equal(t, tc.want, stripWrappingCodeFence(tc.in))
		})
	}
}
