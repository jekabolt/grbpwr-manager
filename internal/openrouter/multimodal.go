package openrouter

// Multimodal chat input: one user turn that carries PICTURES alongside the prompt.
//
// WHY THIS IS A SECOND REQUEST STRUCT AND NOT A WIDENED FIELD.
//
// The obvious edit is to retype chatMessage.Content from `string` to `any` and let each caller put
// either a string or a slice of parts in it. It is also the edit that breaks the live features
// silently. Content is a string today at four call sites — operation drafting, note formatting,
// campaign translation and the tech-card analysis pass — and `any` makes every one of them compile
// unchanged while removing the compiler's ability to say what shape they send. From then on a
// mistake in any caller is a runtime JSON shape the provider rejects with a 400, in a feature
// nobody was editing at the time. The tech-card analysis pass in particular is a paid call whose
// whole chain (2500-token cap, 60 s server budget, 55 s screen budget) is tuned around the exact
// body it sends now.
//
// So the text path keeps its typed, string-content struct, byte-for-byte; the multimodal shape is
// a struct of its own; and the two SHARE THE TRANSPORT (postChatCompletion), which is where the
// things that must not drift actually live — the auth header, the 404 classification, the response
// ceiling, the envelope rules.
//
// WIRE FORMAT. OpenRouter is OpenAI-compatible here:
//
//	{"role":"user","content":[
//	   {"type":"text","text":"..."},
//	   {"type":"image_url","image_url":{"url":"https://… | data:image/png;base64,…"}}]}
//
// The system turn is deliberately NOT converted to parts: it is sent as the same plain-string
// chatMessage the text path sends, so a prompt that already works keeps producing identical bytes.

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

// MaxImageParts caps how many pictures one request may carry.
//
// It is a guard on OUR side, not the provider's limit. A moodboard is a user-controlled list, and
// an unbounded one turns a single button press into a prompt of arbitrary size and arbitrary cost —
// images are billed as input tokens, so "how many" is "how much".
//
// ⚠ ЭКСПОРТИРОВАН ПОТОМУ, ЧТО ДВЕРЬ ОБЯЗАНА ОТКАЗЫВАТЬ ТЕМ ЖЕ ЧИСЛОМ, КОТОРЫМ ОТКАЗЫВАЕТ ТРАНСПОРТ.
// DraftDesignIdea считает картинки доски ДО того, как заведёт оплаченный прогон, и сравнивает их
// ровно с этим числом. Своя константа рядом с вызовом была бы вторым числом, а два числа
// расходятся при правке одного: доска прошла бы дверь и упала бы здесь — уже после StartRun, то
// есть с зарезервированными деньгами и с прогоном, который надо закрывать.
//
// ⚠ И ОНО НАМЕРЕННО НЕ СВЕДЕНО С orimages.MaxInputReferences, ХОТЯ СЕГОДНЯ ОБА РАВНЫ 16. Это два
// РАЗНЫХ предела двух РАЗНЫХ эндпоинтов: там — сколько референсов принимает генератор картинок
// (замеренный факт про gpt-image), здесь — сколько картинок мы кладём в ОДИН чат-запрос. Их
// равенство сегодня — совпадение, а не тождество; связав их одной константой, мы получили бы
// молчаливо неверное число у обоих в тот день, когда любой из двух поставщиков сдвинет свой предел.
const MaxImageParts = 16

// contentPart is one member of a multimodal message's content array. Exactly one of Text / ImageURL
// is meaningful, selected by Type; the other is omitted from the wire.
type contentPart struct {
	Type     string        `json:"type"`
	Text     string        `json:"text,omitempty"`
	ImageURL *imagePartURL `json:"image_url,omitempty"`
}

// imagePartURL is the nested object an image part carries. It is a struct rather than a bare string
// because that is what the wire format says: {"image_url":{"url":"…"}}. Flattening it is a 400.
type imagePartURL struct {
	URL string `json:"url"`
}

// multimodalMessage is a chat turn whose content is a LIST OF PARTS rather than a string.
type multimodalMessage struct {
	Role    string        `json:"role"`
	Content []contentPart `json:"content"`
}

// multimodalRequest mirrors chatRequest field for field EXCEPT for Messages, which is `[]any` so
// the system turn can stay a plain-string chatMessage while the user turn is a multimodalMessage.
// That heterogeneity is the entire reason this struct exists; everything else about the request is
// deliberately the same, so a knob added to one shape is visibly missing from the other.
type multimodalRequest struct {
	Model          string          `json:"model"`
	Messages       []any           `json:"messages"`
	MaxTokens      int             `json:"max_tokens,omitempty"`
	Temperature    float64         `json:"temperature"`
	ResponseFormat *responseFormat `json:"response_format,omitempty"`
	Reasoning      *reasoningSpec  `json:"reasoning,omitempty"`
}

// CompleteWithImages runs ONE chat completion whose user turn carries the prompt AND the given
// pictures, and returns the assistant text with the same metadata CompleteWithMeta returns (finish
// reason, token usage) — a picture prompt is a paid call, and the two features that judge a reply
// (was it cut off? what did it cost?) need the same evidence here as there.
//
// imageURLs are passed to the provider AS ADDRESSES, not as bytes: an https:// URL the provider
// fetches itself, or a data: URI when the caller genuinely has only bytes. Sending our own public
// media URLs is the cheap path — no download, no re-encode, no base64 inflation through a process
// with 0.5 GiB of RAM.
//
// An EMPTY imageURLs list is allowed and degrades to an ordinary text completion. That is
// deliberate: the caller's picture list (a moodboard) can legitimately be empty, and forcing it to
// choose between two methods on that basis would put the same prompt in two places.
//
// maxTokens <= 0 omits the cap and leaves the provider default in force, exactly as elsewhere.
// The slug is the SHARED model — the one Complete uses — because that is the multimodal model this
// deployment already runs on; the analysis override is scoped to the analysis pass and is not
// dragged in here.
func (c *Client) CompleteWithImages(
	ctx context.Context,
	systemPrompt, userPrompt string,
	imageURLs []string,
	jsonMode bool,
	maxTokens int,
) (text string, finishReason string, usage Usage, err error) {
	if !c.Enabled() {
		return "", "", Usage{}, ErrNotConfigured
	}
	if strings.TrimSpace(userPrompt) == "" {
		return "", "", Usage{}, fmt.Errorf("openrouter: a multimodal request needs a prompt, pictures alone say nothing")
	}
	parts, err := buildContentParts(userPrompt, imageURLs)
	if err != nil {
		return "", "", Usage{}, err
	}

	req := multimodalRequest{
		Model: c.Model(),
		Messages: []any{
			// A plain-string system turn: identical bytes to what the text path sends.
			chatMessage{Role: "system", Content: systemPrompt},
			multimodalMessage{Role: "user", Content: parts},
		},
		Temperature: generationTemperature,
	}
	if jsonMode {
		req.ResponseFormat = &responseFormat{Type: "json_object"}
	}
	if maxTokens > 0 {
		req.MaxTokens = maxTokens
	}

	payload, err := json.Marshal(req)
	if err != nil {
		return "", "", Usage{}, fmt.Errorf("openrouter: marshal multimodal request: %w", err)
	}
	return c.postChatCompletion(ctx, payload)
}

// buildContentParts turns a prompt plus picture addresses into the content array, refusing anything
// it cannot vouch for. The text part comes FIRST on purpose: it is the instruction, and a model
// reading instructions after sixteen pictures is a model that has already started guessing.
func buildContentParts(userPrompt string, imageURLs []string) ([]contentPart, error) {
	if len(imageURLs) > MaxImageParts {
		return nil, fmt.Errorf("openrouter: %d pictures exceeds the %d-picture limit for one request",
			len(imageURLs), MaxImageParts)
	}
	parts := make([]contentPart, 0, len(imageURLs)+1)
	parts = append(parts, contentPart{Type: "text", Text: userPrompt})
	for i, raw := range imageURLs {
		u := strings.TrimSpace(raw)
		if err := validateImageURL(u); err != nil {
			return nil, fmt.Errorf("openrouter: picture %d: %w", i+1, err)
		}
		parts = append(parts, contentPart{Type: "image_url", ImageURL: &imagePartURL{URL: u}})
	}
	return parts, nil
}

// validateImageURL admits exactly two forms and rejects the rest.
//
// THE REJECTION IS THE POINT. The provider fetches whatever address we hand it, from its own
// network — so an unvalidated string here is an outbound fetch we authored on someone else's
// behalf. `file://`, `gopher://` and friends have no business in a picture list, and an empty
// string would become a 400 that reads as a provider fault rather than as our own missing value.
func validateImageURL(u string) error {
	switch {
	case u == "":
		return fmt.Errorf("empty picture address")
	case strings.HasPrefix(u, "https://"), strings.HasPrefix(u, "http://"):
		return nil
	case strings.HasPrefix(u, "data:image/"):
		// A data URI must actually carry the payload; "data:image/png" alone is a 400 waiting to
		// be blamed on the provider.
		if !strings.Contains(u, ",") {
			return fmt.Errorf("data URI carries no payload")
		}
		return nil
	default:
		return fmt.Errorf("picture address must be http(s):// or a data:image/… URI, got %q", truncate(u, 40))
	}
}
