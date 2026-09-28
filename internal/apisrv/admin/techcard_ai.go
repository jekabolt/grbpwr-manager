package admin

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/shopspring/decimal"
	"google.golang.org/genproto/googleapis/rpc/errdetails"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// Shared plumbing of the admin AI handlers that call the AI router (s.ai) — the note assistant,
// campaign auto-translation, the design idea draft, the `ai ✦` rewrite (EnhanceText), the playground
// Ideas door (SuggestPrompts) and the construction analysis. Two kinds of thing live here:
//
//   - the ONE refusal vocabulary for a setting no retry can fix (aiRefusal / aiModelRefusal, the
//     no-key sentence openRouterNoKeyMsg and the dead-slug recipe modelUnavailableAdviceMsg), so that
//     every feature reports a missing key and a dead slug the same way, in words for a person and as
//     an ErrorInfo reason for a client;
//   - three small helpers other handlers borrow: resolveCategoryName (the analysis prompt),
//     decimalOrEmpty (the archive sidecars) and aiBoundedText (the design construction draft).
//
// Nothing here is a feature of its own: every symbol in this file has callers in other handlers.

// openRouterNoKeyMsg is THE ONE sentence for "the chat client has no key", shared by every feature
// that calls the AI router: where a key goes (the admin panel's AI providers page — keys are read
// through the AI providers registry) and the env variable that still works as the fallback. It says the
// same for openrouter switched off in the panel: the handler only knows the client has no key.
const openRouterNoKeyMsg = "no key for openrouter — save it in admin → AI providers"

// modelUnavailableAdviceMsg is THE ONE RECIPE for openrouter.ErrModelUnavailable, shared by every
// feature that calls the AI router — the note assistant, campaign auto-translation, the design idea
// draft and the `ai ✦` rewrite. It lives in one place because the fault does: they ride one provider and,
// with the per-feature overrides unset (the normal state), one slug, so they all die the moment the
// provider retires it — and a recipe copied into every handler is a recipe that will only ever be
// corrected in one of them.
//
// IT NAMES TWO KNOBS, NOT ONE. A 404 is also what a wrong OPENROUTER_BASE_URL (or a proxy that
// does not know the route) produces, and sending somebody to swap a perfectly good model would
// cost more than the original outage. The knob NAMES travel to the caller; the base URL's VALUE
// stays in the log, because a model slug is public (the construction analysis response already
// returns it) while an internal proxy hostname is not something to hand to every admin client.
//
// %q is the effective slug — without it the reader knows a setting is wrong but not what is in it.
const modelUnavailableAdviceMsg = "the provider serves no endpoint for model %q — check OPENROUTER_MODEL, and OPENROUTER_BASE_URL if this deployment overrides it"

// --- the machine-readable half of an AI refusal ---
//
// WHY IT EXISTS. Both unfixable causes arrive as one code, FailedPrecondition, so a client shows
// them identically — "the assistant is not connected". On beta that is the truth: there is no key
// and the state is normal. ON PROD THE KEY IS SET, and a retired model slug then looks exactly the
// same: a calm, expected state that moves nobody to investigate. Making the sentence honest had
// therefore made the breakage QUIET, and being quiet is precisely what made the last outage
// expensive — nobody knew until a person complained.
//
// The channel already exists and is already used: grpc-gateway carries google.rpc.Status.details
// into the JSON body, and the client's requestHandler parses details for field violations. So the
// cause travels as an ErrorInfo reason, machine-readable, while the human sentence stays as it is.
const (
	// aiErrorDomain scopes the reasons below. Stable: a client branches on the pair.
	aiErrorDomain = "ai.grbpwr.com"
	// aiReasonNotConfigured — no key (see openRouterNoKeyMsg for where one goes). On beta this is a
	// deployment fact, not a fault, and a client is right to stay quiet about it.
	aiReasonNotConfigured = "AI_NOT_CONFIGURED"
	// aiReasonModelUnavailable — the key is set and the provider serves no endpoint for the
	// configured slug. This one IS a fault and a client should look like it.
	aiReasonModelUnavailable = "AI_MODEL_UNAVAILABLE"
)

// aiRefusal builds the FailedPrecondition every AI feature returns for an unfixable setting: the
// sentence for a person, plus an ErrorInfo a client can branch on without matching English prose.
//
// If the detail cannot be attached the REFUSAL STILL GOES OUT, plain. A client that cannot read the
// reason falls back to the state it has always shown; one that loses the refusal itself would hand
// the person a broken screen instead. The detail is the bonus, never the payload.
func aiRefusal(reason, msg string, metadata map[string]string) error {
	st := status.New(codes.FailedPrecondition, msg)
	withDetails, err := st.WithDetails(&errdetails.ErrorInfo{
		Reason:   reason,
		Domain:   aiErrorDomain,
		Metadata: metadata,
	})
	if err != nil {
		return st.Err()
	}
	return withDetails.Err()
}

// aiModelRefusal is aiRefusal for the dead-slug case, where the slug belongs in the metadata as
// well as in the sentence: a client that wants to name it should not have to parse it back out.
func aiModelRefusal(msgFormat, model string) error {
	return aiRefusal(aiReasonModelUnavailable, fmt.Sprintf(msgFormat, model),
		map[string]string{"model": model})
}

// resolveCategoryName best-effort maps a category_id to its display name via the dictionary cache.
// Returns "" on an unset id or any lookup failure — the type is context, not a hard requirement.
func (s *Server) resolveCategoryName(ctx context.Context, categoryID sql.NullInt32) string {
	if !categoryID.Valid || categoryID.Int32 <= 0 {
		return ""
	}
	di, err := s.repo.Cache().GetDictionaryInfo(ctx)
	if err != nil {
		return ""
	}
	for _, c := range di.Categories {
		if int32(c.ID) == categoryID.Int32 {
			return c.Name
		}
	}
	return ""
}

// decimalOrEmpty renders a nullable decimal as its canonical string; "" when unset, so the output
// simply omits a value nobody configured instead of asserting a zero.
func decimalOrEmpty(d decimal.NullDecimal) string {
	if !d.Valid {
		return ""
	}
	return d.Decimal.String()
}

// aiBoundedText trims a short free-text answer to the column the save writes it into, in RUNES,
// marking the cut with an ellipsis.
//
// Cut rather than dropped: the head of an over-long answer is still what the model said, and
// dropping the whole of it would throw away the part that fits along with the part that does not.
// Marked rather than cut silently: a truncated Russian sentence read as if the model had ended it
// there is a different instruction from the one it wrote, and the ellipsis is what stops the draft
// from asserting it.
func aiBoundedText(s string, max int) string {
	s = strings.TrimSpace(s)
	if utf8.RuneCountInString(s) <= max {
		return s
	}
	return string([]rune(s)[:max-1]) + "…"
}
