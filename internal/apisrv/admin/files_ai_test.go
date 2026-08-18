package admin

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/jekabolt/grbpwr-manager/internal/dto"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
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

// newNoteFormatServer собирает Server так же, как его собирает New: с семафором. Голый
// &Server{aiOps: ...} здесь больше не годится, и это НАМЕРЕННО — семафор не nil-safe, потому что
// nil-канал молча снял бы потолок, а не назвал бы отсутствие сборки.
func newNoteFormatServer(client *openrouter.Client) *Server {
	return &Server{aiOps: client, noteFormatSem: make(chan struct{}, maxConcurrentNoteFormats)}
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
	s := newNoteFormatServer(client)

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
	s := newNoteFormatServer(client)

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
	s := newNoteFormatServer(client)

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
	s := newNoteFormatServer(client)

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
	s := newNoteFormatServer(client)

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
			s := newNoteFormatServer(client)
			resp, err := s.FormatLibraryNoteMarkdown(context.Background(),
				&pb_admin.FormatLibraryNoteMarkdownRequest{Content: "текст"})
			require.Nil(t, resp)
			require.Equal(t, codes.Internal, status.Code(err))
		})
	}
}

// TestFormatLibraryNoteMarkdownIsBounded — ДВЕ ДВЕРИ, КОТОРЫХ У Ф9 НЕ БЫЛО.
//
// Обе про одно: RPC — это неограниченный прокси к платной модели, доступный каждому с files:write.
//
//  1. СЕМАФОР. Пока потолок занят, следующий вызов отбивается НЕМЕДЛЕННО и не доходит до модели.
//     Проверяется именно это (модель не увидела запроса), а не только код ответа: очередь вместо
//     отказа означала бы, что горутины всё равно копятся, просто молча.
//  2. ПОТОЛОК НА ОТВЕТ. Модель может вернуть текст, который сохранить уже нельзя
//     (entity.MaxLibraryNoteBytes). Отдать такой ответ значит: клиент заменил буфер редактора,
//     человек увидел результат — и потерял его на отказе сохранения. Отказ обязан случиться ДО
//     того, как ответ уехал.
func TestFormatLibraryNoteMarkdownIsBounded(t *testing.T) {
	t.Run("семафор отбивает лишний вызов, не доводя его до модели", func(t *testing.T) {
		client, calls := newFakeOpenRouter(t, orReplyWithContent("# ок"))
		s := newNoteFormatServer(client)
		// Потолок выбран целиком: занимаем все места, как это делают живые запросы.
		for range maxConcurrentNoteFormats {
			s.noteFormatSem <- struct{}{}
		}

		resp, err := s.FormatLibraryNoteMarkdown(context.Background(),
			&pb_admin.FormatLibraryNoteMarkdownRequest{Content: "текст"})
		require.Nil(t, resp)
		require.Equal(t, codes.ResourceExhausted, status.Code(err))
		require.Empty(t, *calls, "отбитый по семафору вызов не имеет права дойти до провайдера")

		// Место освободилось — следующий проходит: это потолок, а не выключатель.
		<-s.noteFormatSem
		resp, err = s.FormatLibraryNoteMarkdown(context.Background(),
			&pb_admin.FormatLibraryNoteMarkdownRequest{Content: "текст"})
		require.NoError(t, err)
		require.Equal(t, "# ок", resp.GetContent())
		require.Len(t, *calls, 1)
	})

	t.Run("нил-семафор отказывает, а не снимает потолок", func(t *testing.T) {
		// Server, собранный мимо New, обязан отвечать отказом: тихая работа без потолка — это
		// ровно та половина механики, которая выглядит рабочей.
		client, calls := newFakeOpenRouter(t, orReplyWithContent("# ок"))
		resp, err := (&Server{aiOps: client}).FormatLibraryNoteMarkdown(context.Background(),
			&pb_admin.FormatLibraryNoteMarkdownRequest{Content: "текст"})
		require.Nil(t, resp)
		require.Equal(t, codes.ResourceExhausted, status.Code(err))
		require.Empty(t, *calls)
	})

	t.Run("ответ длиннее заметки отвергается до того, как уедет клиенту", func(t *testing.T) {
		tooLong := strings.Repeat("я", entity.MaxLibraryNoteBytes) // кириллица: 2 байта на символ
		require.Greater(t, len(tooLong), entity.MaxLibraryNoteBytes)
		client, _ := newFakeOpenRouter(t, orReplyWithContent(tooLong))
		s := newNoteFormatServer(client)

		resp, err := s.FormatLibraryNoteMarkdown(context.Background(),
			&pb_admin.FormatLibraryNoteMarkdownRequest{Content: "коротко"})
		require.Nil(t, resp, "ответ, который нельзя сохранить, не имеет права доехать до редактора")
		require.Equal(t, codes.Internal, status.Code(err))
		require.Contains(t, status.Convert(err).Message(), "more text than a note can hold")

		// Ровно на пределе — проходит: потолок отвергает то, что БОЛЬШЕ, а не то, что «около».
		atCap := strings.Repeat("a", entity.MaxLibraryNoteBytes)
		client, _ = newFakeOpenRouter(t, orReplyWithContent(atCap))
		resp, err = newNoteFormatServer(client).FormatLibraryNoteMarkdown(context.Background(),
			&pb_admin.FormatLibraryNoteMarkdownRequest{Content: "коротко"})
		require.NoError(t, err)
		require.Len(t, []byte(resp.GetContent()), entity.MaxLibraryNoteBytes)
	})

	t.Run("предел ответа — то же число, что и у сохранения", func(t *testing.T) {
		// Разъехавшиеся числа означали бы ответ, который проходит здесь и падает на записи, — то
		// есть ровно тот сценарий, ради которого проверка и заведена.
		require.NoError(t, dto.ValidateLibraryNoteContent(strings.Repeat("a", entity.MaxLibraryNoteBytes)))
		require.Error(t, dto.ValidateLibraryNoteContent(strings.Repeat("a", entity.MaxLibraryNoteBytes+1)))
	})
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

// TestFormatLibraryNoteMarkdownModelUnavailable — ОТКАЗ ОБЯЗАН НАЗЫВАТЬ НАСТРОЙКУ, А НЕ ПОГОДУ.
//
// Это ровно тот отказ, что был на бете 17.08: слуг `anthropic/claude-3.5-sonnet` сняли с
// обслуживания, провайдер ответил 404 за 0,2 с, а человек получил «the markdown assistant is
// unavailable right now — try again in a moment» и жал кнопку снова. Повтор был обречён: неверная
// настройка не заживает ни через минуту, ни через неделю.
//
// Проверяется пара, а не одна половина: 404 — FailedPrecondition со словами про OPENROUTER_MODEL,
// 503 — по-прежнему Unavailable. Одна половина без другой означала бы либо прежнюю ложь, либо
// новую: «почините настройку» про обычный сбой провайдера.
func TestFormatLibraryNoteMarkdownModelUnavailable(t *testing.T) {
	// Тело — дословно то, что вернул живой провайдер.
	const liveBody = `{"error":{"message":"No endpoints found for anthropic/claude-3.5-sonnet.","code":404}}`

	t.Run("404 — это настройка, а не временная недоступность", func(t *testing.T) {
		client, _ := newFakeOpenRouter(t, func(w http.ResponseWriter) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(liveBody))
		})
		s := newNoteFormatServer(client)

		resp, err := s.FormatLibraryNoteMarkdown(context.Background(),
			&pb_admin.FormatLibraryNoteMarkdownRequest{Content: "текст"})
		require.Nil(t, resp)
		require.Equal(t, codes.FailedPrecondition, status.Code(err),
			"неустранимая настройка не имеет права приходить как Unavailable: клиент предложит повтор, а повтор обречён")
		msg := status.Convert(err).Message()
		require.Contains(t, msg, "OPENROUTER_MODEL", "отказ обязан называть настройку, которую нужно поправить")
		require.NotContains(t, msg, "try again", "повторять нечего")
	})

	t.Run("обычный сбой провайдера остаётся Unavailable", func(t *testing.T) {
		client, _ := newFakeOpenRouter(t, func(w http.ResponseWriter) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = w.Write([]byte(`{"error":{"message":"upstream is having a moment"}}`))
		})
		resp, err := newNoteFormatServer(client).FormatLibraryNoteMarkdown(context.Background(),
			&pb_admin.FormatLibraryNoteMarkdownRequest{Content: "текст"})
		require.Nil(t, resp)
		require.Equal(t, codes.Unavailable, status.Code(err))
	})
}
