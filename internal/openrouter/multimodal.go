package openrouter

// Multimodal chat input: one user turn that carries PICTURES alongside the prompt.
//
// THE WIRE SHAPE LIVES IN THE TRANSPORT NOW (B-11, internal/aiprov/oaichat): the user turn as a list of
// parts, the system turn kept a plain string, the text part first, ≤ MaxImageParts pictures, only
// http(s):// and data:image/… addresses — with the same sentences as before. What stays here is what
// the two picture entry points CHOOSE: the slug and the reasoning effort under a ceiling.
//
// ⚠ AN EMPTY PICTURE LIST STILL SENDS A PARTS ARRAY (`[{"type":"text",…}]`), NOT A STRING — the bytes
// these entry points always sent, pinned by a golden in multimodal_test.go. That is what
// oaichat.Options{PartsAlways: true} is for (and aiprov.ChatRequest.UserAsParts, its router-side twin):
// a provider-neutral ChatRequest with no pictures is a plain text turn by the seam's contract, so the
// legacy shape has to be asked for.

import (
	"context"
	"fmt"
	"strings"

	"github.com/jekabolt/grbpwr-manager/internal/aiprov"
	"github.com/jekabolt/grbpwr-manager/internal/aiprov/oaichat"
)

// MaxImageParts caps how many pictures one request may carry.
//
// It is a guard on OUR side, not the provider's limit. A moodboard is a user-controlled list, and
// an unbounded one turns a single button press into a prompt of arbitrary size and arbitrary cost —
// images are billed as input tokens, so "how many" is "how much".
//
// ⚠ ЭКСПОРТИРОВАН ПОТОМУ, ЧТО ДВЕРЬ ОБЯЗАНА ОТКАЗЫВАТЬ ТЕМ ЖЕ ЧИСЛОМ, КОТОРЫМ ОТКАЗЫВАЕТ ТРАНСПОРТ —
// и поэтому с B-11 это не своя константа, а ЧИСЛО ТРАНСПОРТА (oaichat.MaxImageParts).
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
const MaxImageParts = oaichat.MaxImageParts

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
//
// ⚠ ПОТОЛОК И ВЫКЛЮЧЕННОЕ МЫШЛЕНИЕ — ЭТО ОДИН ДОГОВОР, А НЕ ДВЕ НАСТРОЙКИ. Ставя `max_tokens`, мы
// говорим «ответ помещается в N токенов»; думающая модель (defaultModel — anthropic/claude-sonnet-5)
// тратит рассуждения ИЗ ЭТОГО ЖЕ БЮДЖЕТА и ДО ответа, поэтому потолок без `reasoning:{effort:"none"}`
// покупает размышление вместо ответа. Это не гипотеза: openrouter.go:analysisReasoningEffort хранит
// замер первого живого прогона разбора тех-карты — 2500 токенов завершения, НОЛЬ контента, 42 с,
// ~$0.11. Ревью круга 19 нашло ровно эту дыру здесь: черновик конструкции поставил потолок 3000 и
// мышление не выключил.
//
// ПОЧЕМУ ПО НАЛИЧИЮ ПОТОЛКА, А НЕ ВСЕГДА. Прозаический черновик идеи зовёт этот же метод БЕЗ
// потолка, и его байты — контракт со старым клиентом (V-19): безусловный `reasoning` изменил бы
// запрос, у которого ничего не менялось, и на пути без потолка выключать нечего — там мышление
// тратит СВОИ токены, а не съедает чужие. Так связь остаётся одним решением в одном месте: кто
// ставит потолок, тот и получает выключенное мышление, и разъехаться им негде.
//
// Модели, которые не рассуждают, поле игнорируют; модель, у которой рассуждение ОБЯЗАТЕЛЬНО,
// откажется его выключать, и пустой ответ вернётся как ErrBudgetExhausted, а не молчанием.
func (c *Client) CompleteWithImages(
	ctx context.Context,
	systemPrompt, userPrompt string,
	imageURLs []string,
	jsonMode bool,
	maxTokens int,
) (text string, finishReason string, usage Usage, err error) {
	return c.completeWithImages(ctx, c.Model(), analysisReasoningEffort, systemPrompt, userPrompt, imageURLs, jsonMode, maxTokens)
}

// CompleteWithImagesOn is CompleteWithImages on a NAMED slug (PLAYGROUND B-15: the Ideas door has a
// slug of its own, OPENROUTER_MODEL_IDEAS, and a fallback). Same transport, same wire shape, same
// validation — with ONE difference, and it is deliberate: under a token ceiling it asks for
// leastReasoningEffort ("minimal"), not "none". A slug picked per feature may be a model whose
// reasoning is mandatory (the fallback openai/gpt-5-mini is one), and such a model rejects "none"
// outright; "minimal" is accepted everywhere and is the ideas default's own default. The CAP still
// bounds the spend: reasoning tokens come out of max_tokens.
//
// An empty model is refused before anything is sent: "" would reach the provider as a 400 that
// reads as the provider's fault rather than as ours.
func (c *Client) CompleteWithImagesOn(
	ctx context.Context,
	model string,
	systemPrompt, userPrompt string,
	imageURLs []string,
	jsonMode bool,
	maxTokens int,
) (text string, finishReason string, usage Usage, err error) {
	if !c.Enabled() {
		return "", "", Usage{}, ErrNotConfigured
	}
	if strings.TrimSpace(model) == "" {
		return "", "", Usage{}, fmt.Errorf("openrouter: a completion needs a model slug")
	}
	return c.completeWithImages(ctx, strings.TrimSpace(model), leastReasoningEffort, systemPrompt, userPrompt, imageURLs, jsonMode, maxTokens)
}

// completeWithImages is the one body of both: the slug and the capped reasoning effort are the
// only things the two callers choose. The prompt / picture checks (and their sentences) are the
// transport's — refused before the wire, never engaged.
func (c *Client) completeWithImages(
	ctx context.Context,
	model, cappedEffort string,
	systemPrompt, userPrompt string,
	imageURLs []string,
	jsonMode bool,
	maxTokens int,
) (text string, finishReason string, usage Usage, err error) {
	if !c.Enabled() {
		return "", "", Usage{}, ErrNotConfigured
	}
	req := aiprov.ChatRequest{
		System:    systemPrompt,
		User:      userPrompt,
		ImageURLs: imageURLs,
		JSONMode:  jsonMode,
	}
	if maxTokens > 0 {
		// ПОТОЛОК И ВЫКЛЮЧЕННОЕ МЫШЛЕНИЕ — ОДИН ДОГОВОР (см. CompleteWithImages). Потолок едет в
		// транспорт полем запроса, и срок вызова транспорт считает ИЗ ЭТОГО ЖЕ ЧИСЛА — ровно из того,
		// что уехало на провод (oaichat.buildRequest → post), поэтому разъехаться им негде.
		req.MaxTokens = maxTokens
		req.Effort = cappedEffort
	}
	return c.send(ctx, model, req, oaichat.Options{PartsAlways: true})
}
