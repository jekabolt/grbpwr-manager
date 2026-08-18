package admin

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/jekabolt/grbpwr-manager/internal/openrouter"
	pb_admin "github.com/jekabolt/grbpwr-manager/proto/gen/admin"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

const (
	// noteFormatNotConfiguredMsg is the single, clear message returned when OpenRouter is not
	// configured (no OPENROUTER_API_KEY). Kept as one const — like aiOpsNotConfiguredMsg for
	// tech-card operations — so the pre-check and the client-level ErrNotConfigured path report
	// identically. On beta the key is unset BY DESIGN, so this is the answer that path returns
	// every time: the client turns it into the "assistant not connected" state and the note
	// itself keeps working.
	noteFormatNotConfiguredMsg = "markdown assistant is not configured (set OPENROUTER_API_KEY)"

	// noteFormatModelUnavailableMsg is the OTHER misconfiguration, and it exists because the first
	// version of this handler did not have it: when the provider retired the configured model slug,
	// a 404 came back in 0.2 s and was reported as "unavailable right now — try again in a moment".
	// It was never going to become available. The person kept pressing, because the interface had
	// promised the fault was temporary.
	//
	// So this one names the SETTING, in the same shape as the missing key above. The recipe itself
	// is modelUnavailableAdviceMsg, shared with the two other features on this client — see there
	// for why it names two knobs and why the base URL's value stays in the log.
	noteFormatModelUnavailableMsg = "markdown assistant is misconfigured: " + modelUnavailableAdviceMsg

	// maxNoteFormatRunes caps what one call may format. It is the mockup's `toolong` threshold
	// (files-section.html, md=v3: `m.text.length > 12000`), measured here in RUNES rather than
	// bytes so a Cyrillic note is not silently allowed half the text of a Latin one. A longer
	// note gets an honest InvalidArgument BEFORE the model is called — a request that would
	// otherwise sit until the 60 s client timeout and then fail anyway — and the client offers
	// to send a selection instead.
	maxNoteFormatRunes = 12000

	// noteFormatSystemPrompt makes the model a typesetter, not an editor. The whole value of the
	// feature is that the author's words survive: a model that "improves" the wording returns
	// something the author must re-read line by line, which is worse than no assistant at all.
	// Hence the rules are stated as prohibitions, and the fallback for any doubt is "leave it as
	// a plain paragraph".
	noteFormatSystemPrompt = `You format plain text as markdown. You are a TYPESETTER, not an editor.

Absolute rules:
- Reproduce the author's wording EXACTLY. Every sentence, word, number, name and url stays as written, in the author's own language. Never rephrase, translate, shorten, expand, summarize, correct or reorder anything.
- Add NOTHING: no title the author did not write, no introduction, no conclusion, no commentary, no note about what you changed.
- Remove NOTHING: not a line, not an aside, not a repetition. If a fragment looks redundant, keep it.
- Keep the order of thoughts exactly as the author put them.
- Your only work is MARKUP: # headings for lines that are already headings; - or 1. for lines that are already a list; **bold** / *italic* only where the plain text already marks emphasis; ` + "`code`" + ` and fenced blocks for code, paths and commands; > for quoted passages; a table only for text already laid out in columns; [label](url) only where a url already appears with its label. Whitespace may be normalized (blank line between blocks, trailing spaces dropped).
- Markdown that is already in the input stays exactly as it is.
- When in doubt, leave the fragment as a plain paragraph. Under-formatting is correct; changing words is not.

Answer with the formatted markdown document and nothing else: no code fence wrapped around the whole answer, no preamble, no sign-off.`
)

// FormatLibraryNoteMarkdown turns the plain text the editor is holding into tidy markdown via
// OpenRouter. It is a SUGGESTION and persists NOTHING: the answer goes back as a string, the
// person decides whether to take it, and the write is still an ordinary SaveLibraryNoteContent
// with its CAS — so the assistant cannot overwrite anybody's text, not even by racing it.
//
// What goes to the model is EXACTLY req.Content — the whole buffer or a selection, the client
// decides. Not the file name, not the topics, not the owners, not the discussion: the leak
// perimeter is meant to be readable from this one function. That the request carries no file_id
// is also why this RPC legitimately stands outside the visibility-predicate points.
//
// Degradation: no API key → FailedPrecondition (the beta default); text over the rune cap →
// InvalidArgument before any call is made; too many calls in flight → ResourceExhausted, again
// before any call is made; a model slug the provider does not serve → FailedPrecondition, like the
// missing key and for the same reason (it is a setting, not weather, and no retry will fix it);
// transport/API failure → Unavailable; an empty answer OR one too long to save → Internal. The
// provider's raw error text never leaves the server — it is logged, and the caller gets a stable
// sentence it can show a human.
func (s *Server) FormatLibraryNoteMarkdown(
	ctx context.Context,
	req *pb_admin.FormatLibraryNoteMarkdownRequest,
) (*pb_admin.FormatLibraryNoteMarkdownResponse, error) {
	// The not-configured check comes first on purpose: with no key nothing about this request can
	// succeed, and "the assistant is not connected" is a truer answer than a complaint about the
	// text the person wrote.
	if !s.aiOps.Enabled() {
		return nil, aiRefusal(aiReasonNotConfigured, noteFormatNotConfiguredMsg, nil)
	}

	content := req.GetContent()
	if strings.TrimSpace(content) == "" {
		return nil, status.Error(codes.InvalidArgument, "content is required")
	}
	inRunes := utf8.RuneCountInString(content)
	if inRunes > maxNoteFormatRunes {
		return nil, status.Errorf(codes.InvalidArgument,
			"text is longer than the assistant takes in one go (%d characters, limit %d) — format a selection instead",
			inRunes, maxNoteFormatRunes)
	}

	// СЕМАФОР, И ОН ЗДЕСЬ НЕ ФОРМАЛЬНОСТЬ. Всё, что стоит выше, — проверки самого запроса; ниже
	// начинается вызов платного третьего лица, который держит горутину до 60 с. files:write есть у
	// всех, кто вообще работает с библиотекой, потолок ввода в рунах не мешает слать 12 000 рун
	// сколько угодно раз, и без этой двери одна вкладка с циклом превращается и в рост горутин, и в
	// счёт от провайдера. Форма списана с campaignTestSem — отказ немедленный, а не очередь:
	// человек, ждущий подсказки по форматированию, предпочтёт «попробуйте через минуту» минуте
	// молчания.
	select {
	case s.noteFormatSem <- struct{}{}:
		defer func() { <-s.noteFormatSem }()
	default:
		return nil, status.Error(codes.ResourceExhausted,
			"the markdown assistant is busy right now — try again in a moment")
	}

	started := time.Now()
	// jsonMode=false: the answer is a markdown document, not a JSON envelope. Temperature and
	// timeout are the client's defaults (0.2 / 60 s).
	raw, err := s.aiOps.Complete(ctx, noteFormatSystemPrompt, content, false)
	took := time.Since(started)
	if err != nil {
		if errors.Is(err, openrouter.ErrNotConfigured) {
			return nil, aiRefusal(aiReasonNotConfigured, noteFormatNotConfiguredMsg, nil)
		}
		// Only length, duration and the MODEL are logged. The note's text is the user's private
		// writing and has no business in the log stream — but the effective slug does: when the
		// provider retired the default model, the slug was visible in this line only because the
		// provider happened to repeat it in its own sentence. That was luck, not design, and a
		// differently-worded provider message would have cost hours of diagnosis.
		slog.Default().ErrorContext(ctx, "note markdown formatting failed",
			slog.Int("in_runes", inRunes), slog.Duration("took", took),
			slog.String("model", s.aiOps.Model()), slog.String("base_url", s.aiOps.BaseURL()),
			slog.String("err", err.Error()))
		if errors.Is(err, openrouter.ErrModelUnavailable) {
			return nil, aiModelRefusal(noteFormatModelUnavailableMsg, s.aiOps.Model())
		}
		if isEmptyModelAnswer(err) {
			return nil, status.Error(codes.Internal, "the assistant returned nothing to show — try again")
		}
		return nil, status.Error(codes.Unavailable, "the markdown assistant is unavailable right now — try again in a moment")
	}

	formatted := strings.TrimSpace(stripWrappingCodeFence(raw))
	if formatted == "" {
		slog.Default().ErrorContext(ctx, "note markdown formatting returned an empty document",
			slog.Int("in_runes", inRunes), slog.Duration("took", took))
		return nil, status.Error(codes.Internal, "the assistant returned nothing to show — try again")
	}
	// ПОТОЛОК НА ОТВЕТ, И ОН СВЕРЯЕТСЯ С ТЕМ ЖЕ ЧИСЛОМ, ЧТО И СОХРАНЕНИЕ (entity.MaxLibraryNoteBytes,
	// через который идёт dto.ValidateLibraryNoteContent). На входе стоит потолок в РУНАХ, на записи —
	// в БАЙТАХ, и модель, дописавшая разметку, легко выдаёт документ, который принять уже нельзя.
	// Отдать такой ответ значило бы: клиент заменяет буфер редактора, человек видит красивый текст —
	// и получает отказ на сохранении, потеряв то, что было в буфере до подсказки. Поэтому отказ
	// ЗДЕСЬ, до того как ответ уехал, и словами о том, что делать дальше.
	if len(formatted) > entity.MaxLibraryNoteBytes {
		slog.Default().ErrorContext(ctx, "note markdown formatting returned an unsavable document",
			slog.Int("in_runes", inRunes), slog.Int("out_bytes", len(formatted)),
			slog.Duration("took", took))
		return nil, status.Errorf(codes.Internal,
			"the assistant returned more text than a note can hold (%d bytes, limit %d) — format a smaller fragment",
			len(formatted), entity.MaxLibraryNoteBytes)
	}

	slog.Default().InfoContext(ctx, "formatted library note markdown",
		slog.Int("in_runes", inRunes), slog.Int("out_runes", utf8.RuneCountInString(formatted)),
		slog.Duration("took", took), slog.String("model", s.aiOps.Model()))

	return &pb_admin.FormatLibraryNoteMarkdownResponse{Content: formatted}, nil
}

// isEmptyModelAnswer distinguishes "the model said nothing" from "the call failed". The client
// package reports both as plain errors, so the sentinel is its wording; getting the split wrong
// only changes Internal↔Unavailable, never whether the note survives.
func isEmptyModelAnswer(err error) bool {
	msg := err.Error()
	return strings.Contains(msg, "empty message") || strings.Contains(msg, "no choices")
}

// stripWrappingCodeFence undoes the one model habit that would visibly break the result: wrapping
// the WHOLE answer in a fence, which turns an entire formatted note into one code block. It only
// unwraps when the fence really is the wrapper — an explicit ```markdown / ```md fence, or a bare
// fence that is the only pair in the answer and sits on the first and last lines. Anything else
// (an answer that merely starts with a code block, a fence with another language tag) is returned
// untouched, because a wrongly-stripped fence would corrupt real content.
func stripWrappingCodeFence(s string) string {
	trimmed := strings.TrimSpace(s)
	if !strings.HasPrefix(trimmed, "```") {
		return s
	}
	lines := strings.Split(trimmed, "\n")
	if len(lines) < 2 || strings.TrimSpace(lines[len(lines)-1]) != "```" {
		return s
	}
	switch strings.ToLower(strings.TrimSpace(strings.TrimPrefix(lines[0], "```"))) {
	case "markdown", "md":
		// An unambiguous wrapper: nobody asks for a markdown document that IS a markdown code
		// block, so inner fences are fine and only the outer pair goes.
	case "":
		fences := 0
		for _, l := range lines {
			if strings.HasPrefix(strings.TrimSpace(l), "```") {
				fences++
			}
		}
		if fences != 2 {
			return s
		}
	default:
		return s
	}
	return strings.Join(lines[1:len(lines)-1], "\n")
}
