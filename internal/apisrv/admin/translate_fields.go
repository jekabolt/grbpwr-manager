package admin

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jekabolt/grbpwr-manager/internal/aiprov"
	authsrv "github.com/jekabolt/grbpwr-manager/internal/apisrv/auth"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/jekabolt/grbpwr-manager/internal/tenant"
	pb_admin "github.com/jekabolt/grbpwr-manager/proto/gen/admin"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// TRANSLATE FIELDS (teams lane V2) — the backend twin of the admin client's Vercel /api/translate
// (api/_openrouter.js): same body, same prompt, same model settings, but through the AI router, so the
// key is the panel's, the call is a ledger row (ai_usage_event) of the CALLER'S team and the team's
// AI spend shows it. Purpose chat.email_translate: the translation route that already exists in every
// team schema (0373), so no migration; a purpose of its own would need a seed row.
const (
	translateFieldsPurpose = entity.AIPurposeEmailTranslate

	// Bounds of one press. The product/hero/archive editors send a handful of short fields into the
	// other five or six languages; these caps refuse an abuse before the paid call, not a real form.
	maxTranslateFields       = 30
	maxTranslateTargets      = 12
	maxTranslateFieldRunes   = 6000
	maxTranslateTotalRunes   = 20000
	maxTranslateNameRunes    = 64
	maxTranslateLangRunes    = 40
	translateFieldsMaxTokens = 4000 // _openrouter.js MAX_TOKENS

	translateFieldsNotConfiguredMsg = "translation is not configured: " + openRouterNoKeyMsg
	translateFieldsModelMsg         = "translation is misconfigured: " + modelUnavailableAdviceMsg
)

// translateFieldsTemperature is _openrouter.js's temperature (0.3); a func, not a package var
// (tenantgate G4), because ChatRequest takes a pointer.
func translateFieldsTemperature() *float64 { t := 0.3; return &t }

// translateCodeRe — a target code is a short locale tag ("fr", "zh", "pt-br"); it becomes a JSON key
// in the prompt and in the answer, so nothing else is let through.
var translateCodeRe = regexp.MustCompile(`^[a-z]{2,3}(-[a-z0-9]{2,8})?$`)

// translateFieldsIn is a validated request.
type translateFieldsIn struct {
	langName   string
	fields     map[string]string // non-empty values only
	names      []string          // sorted field names
	targets    []*pb_admin.TranslateTarget
	codes      []string
	maxLength  map[string]int // field → cap (>0 only)
	totalRunes int
	techpack   bool // TRANSLATE_CONTEXT_TECHPACK: factory vocabulary, not shop copy
}

func validateTranslateFields(req *pb_admin.TranslateFieldsRequest) (*translateFieldsIn, error) {
	src := req.GetSource()
	in := &translateFieldsIn{
		langName:  strings.TrimSpace(src.GetLangName()),
		fields:    map[string]string{},
		maxLength: map[string]int{},
		techpack:  req.GetContext() == pb_admin.TranslateContext_TRANSLATE_CONTEXT_TECHPACK,
	}
	if in.langName == "" || utf8.RuneCountInString(in.langName) > maxTranslateLangRunes {
		return nil, errors.New("source.lang_name is required")
	}
	for name, value := range src.GetFields() {
		if strings.TrimSpace(name) == "" || utf8.RuneCountInString(name) > maxTranslateNameRunes {
			return nil, fmt.Errorf("field name %q is invalid", name)
		}
		if strings.TrimSpace(value) == "" {
			continue
		}
		n := utf8.RuneCountInString(value)
		if n > maxTranslateFieldRunes {
			return nil, fmt.Errorf("field %q is too long (%d characters, limit %d)", name, n, maxTranslateFieldRunes)
		}
		in.totalRunes += n
		in.fields[name] = value
		in.names = append(in.names, name)
	}
	if len(in.fields) == 0 {
		return nil, errors.New("nothing to translate")
	}
	if len(in.fields) > maxTranslateFields {
		return nil, fmt.Errorf("too many fields (%d, limit %d)", len(in.fields), maxTranslateFields)
	}
	if in.totalRunes > maxTranslateTotalRunes {
		return nil, fmt.Errorf("text is too long for one translation (%d characters, limit %d)", in.totalRunes, maxTranslateTotalRunes)
	}
	sort.Strings(in.names)

	seen := map[string]bool{}
	for _, t := range req.GetTargets() {
		code := strings.ToLower(strings.TrimSpace(t.GetCode()))
		lang := strings.TrimSpace(t.GetLangName())
		if !translateCodeRe.MatchString(code) {
			return nil, fmt.Errorf("target code %q is invalid", t.GetCode())
		}
		if lang == "" || utf8.RuneCountInString(lang) > maxTranslateLangRunes {
			return nil, fmt.Errorf("target %q needs a lang_name", code)
		}
		if seen[code] {
			continue
		}
		seen[code] = true
		in.targets = append(in.targets, &pb_admin.TranslateTarget{Code: code, LangName: lang})
		in.codes = append(in.codes, code)
	}
	if len(in.targets) == 0 {
		return nil, errors.New("no target languages")
	}
	if len(in.targets) > maxTranslateTargets {
		return nil, fmt.Errorf("too many target languages (%d, limit %d)", len(in.targets), maxTranslateTargets)
	}
	for name, c := range req.GetConstraints() {
		if _, ok := in.fields[name]; ok && c.GetMaxLength() > 0 {
			in.maxLength[name] = int(c.GetMaxLength())
		}
	}
	return in, nil
}

// translateFieldsSystemPrompt is _openrouter.js's system prompt, line for line: the role lines by
// context (a tech pack goes to a sewing factory — the reader is a technologist, not a shopper), then
// the shared rules. The brand named is the calling team's (house = team #1, today's wording exactly;
// no brand data = no brand named).
func translateFieldsSystemPrompt(in *translateFieldsIn, brand promptBrand) string {
	shop, pack := "for the brand grbpwr.", " (brand grbpwr)."
	if !brand.House {
		shop, pack = "for a fashion brand.", "."
		if brand.Name != "" {
			shop, pack = "for the brand "+strconv.Quote(brand.Name)+".", " (brand "+strconv.Quote(brand.Name)+")."
		}
	}
	role := []string{"You are a professional fashion e-commerce translator " + shop}
	if in.techpack {
		role = []string{
			"You are a garment technologist translating a tech pack for a sewing factory" + pack,
			"Fields are operation names, part names, notes, materials and measurement points.",
			"Use the factory floor's standard terms of the target language, terse like the source.",
			"- Keep codes like FRT_L_MAIN, abbreviations SA, CB, CF, TS, RS, WS, GSM, numbers and units exactly as written.",
		}
	}
	return strings.Join(append(role,
		"Translate the provided fields from "+in.langName+" into each requested target language.",
		"Rules:",
		"- Preserve brand names, proper nouns, product/model names, sizes, measurements, and SKU-like tokens untranslated.",
		"- Keep the tone concise, premium, and on-brand. Do not add, remove, or explain anything.",
		"- When a field has a maxLength, the translated text MUST NOT exceed that many characters.",
		"- Translate every field for every target language. Never leave a field empty.",
		"Output ONLY a JSON object, no markdown, with this exact shape:",
		`{ "translations": { "<targetCode>": { "<fieldName>": "<translatedText>" } } }`,
		"Use exactly these target codes as keys: "+strings.Join(in.codes, ", ")+".",
		"Each target must contain exactly these fields: "+strings.Join(in.names, ", ")+".",
	), "\n")
}

// translateFieldsUserPrompt is _openrouter.js's user payload.
func translateFieldsUserPrompt(in *translateFieldsIn) (string, error) {
	type target struct {
		Code     string `json:"code"`
		Language string `json:"language"`
	}
	type constraint struct {
		MaxLength int `json:"maxLength"`
	}
	targets := make([]target, 0, len(in.targets))
	for _, t := range in.targets {
		targets = append(targets, target{Code: t.GetCode(), Language: t.GetLangName()})
	}
	constraints := make(map[string]constraint, len(in.maxLength))
	for name, n := range in.maxLength {
		constraints[name] = constraint{MaxLength: n}
	}
	b, err := json.Marshal(struct {
		SourceLanguage string                `json:"sourceLanguage"`
		Targets        []target              `json:"targets"`
		Fields         map[string]string     `json:"fields"`
		Constraints    map[string]constraint `json:"constraints"`
	}{in.langName, targets, in.fields, constraints})
	if err != nil {
		return "", fmt.Errorf("marshal translate payload: %w", err)
	}
	return string(b), nil
}

// parseTranslateFields keeps only what was asked: a requested code, a requested field, a non-empty
// string. A translation over its field's maxLength is cut to the cap (the client refuses longer text
// on save, and the model was told the cap). Nothing usable at all is an error: the call was paid for
// and produced nothing.
func parseTranslateFields(content string, in *translateFieldsIn) (map[string]*pb_admin.TranslatedFields, error) {
	js, ok := aiprov.ExtractJSONObject(content)
	if !ok {
		return nil, errors.New("translate: model output contained no JSON object")
	}
	var raw struct {
		Translations map[string]map[string]any `json:"translations"`
	}
	if err := json.Unmarshal([]byte(js), &raw); err != nil {
		return nil, fmt.Errorf("translate: parse model output: %w", err)
	}
	out := map[string]*pb_admin.TranslatedFields{}
	byCode := make(map[string]map[string]any, len(raw.Translations))
	for code, fields := range raw.Translations {
		byCode[strings.ToLower(strings.TrimSpace(code))] = fields
	}
	for _, code := range in.codes {
		fields, ok := byCode[code]
		if !ok {
			continue
		}
		got := map[string]string{}
		for _, name := range in.names {
			text, _ := fields[name].(string)
			text = strings.TrimSpace(text)
			if text == "" {
				continue
			}
			if n := in.maxLength[name]; n > 0 && utf8.RuneCountInString(text) > n {
				text = strings.TrimSpace(string([]rune(text)[:n]))
			}
			got[name] = text
		}
		if len(got) > 0 {
			out[code] = &pb_admin.TranslatedFields{Fields: got}
		}
	}
	if len(out) == 0 {
		return nil, errors.New("translate: model output held none of the requested languages")
	}
	return out, nil
}

// TranslateFields translates the source fields into every target language in ONE router call.
// Stores nothing. Gates (interceptors): feature core + ai; any account of the team may press it
// (rbac allowlist, write class — a suspended team cannot spend). Here: a token with no account name
// first (a legacy JWT without subject has no hourly window of its own, so it may not spend at all),
// then not configured / paused, then the request, then the text assistant's shared concurrency gate and per-admin hourly window
// (the same ones EnhanceText draws on: one budget for every ✦ press of a person), then the call.
// Only sizes, codes, timing and the model are logged — never the text.
func (s *Server) TranslateFields(ctx context.Context, req *pb_admin.TranslateFieldsRequest) (*pb_admin.TranslateFieldsResponse, error) {
	const purpose = translateFieldsPurpose
	admin := strings.TrimSpace(authsrv.GetAdminUsername(ctx))
	if admin == "" {
		return nil, status.Error(codes.Unauthenticated, "this session has no account name — sign in again")
	}
	if !s.ai.Enabled(purpose) {
		return nil, s.aiOffRefusal(purpose, translateFieldsNotConfiguredMsg)
	}
	in, err := validateTranslateFields(req)
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, err.Error())
	}
	user, err := translateFieldsUserPrompt(in)
	if err != nil {
		return nil, status.Error(codes.Internal, "can't build the translation request")
	}

	select {
	case s.enhanceSem <- struct{}{}:
		defer func() { <-s.enhanceSem }()
	default:
		return nil, status.Error(codes.ResourceExhausted, "the translator is busy right now — try again in a moment")
	}
	if !s.enhanceRuns.allow(tenant.Key(ctx, admin)) {
		return nil, status.Errorf(codes.ResourceExhausted,
			"this account has used the AI text tools %d times in the last hour — try again later", enhancePerAdminCalls)
	}

	started := time.Now()
	res, err := s.ai.Chat(ctx, purpose, aiprov.ChatRequest{
		System: translateFieldsSystemPrompt(in, s.translateBrand(ctx)), User: user, JSONMode: true,
		MaxTokens: translateFieldsMaxTokens, Temperature: translateFieldsTemperature(),
	})
	took := time.Since(started)
	model := s.aiModelOf(purpose, res)
	logAttrs := []any{
		slog.Int("fields", len(in.fields)), slog.Int("in_runes", in.totalRunes),
		slog.String("targets", strings.Join(in.codes, ",")), slog.Duration("took", took),
		slog.String("model", model),
	}
	if err != nil {
		if refusal, ok := aiUncalledRefusal(err, translateFieldsNotConfiguredMsg); ok {
			return nil, refusal
		}
		provider := s.aiProviderOf(purpose, res)
		// The class, not err.Error(): a provider that echoes the request would echo the text.
		slog.Default().ErrorContext(ctx, "translate fields failed", append(logAttrs,
			slog.String("err_class", enhanceErrClass(err)), slog.Bool("provider_engaged", aiprov.Engaged(err)),
			slog.String("provider", provider), slog.String("base_url", s.ai.BaseURL(provider)))...)
		if errors.Is(err, aiprov.ErrModelUnavailable) {
			return nil, aiModelRefusal(translateFieldsModelMsg, model)
		}
		if isEmptyModelAnswer(err) {
			return nil, status.Error(codes.Internal, "the translator returned nothing — try again")
		}
		return nil, status.Error(codes.Unavailable, "the translator is unavailable right now — try again in a moment")
	}
	translations, perr := parseTranslateFields(res.Text, in)
	if perr != nil {
		slog.Default().ErrorContext(ctx, "translate fields: unusable model answer",
			append(logAttrs, slog.Int("out_runes", utf8.RuneCountInString(res.Text)))...)
		return nil, status.Error(codes.Internal, "the translator returned an unreadable answer — try again")
	}
	slog.Default().InfoContext(ctx, "translated fields", append(logAttrs, slog.Int("languages", len(translations)))...)
	return &pb_admin.TranslateFieldsResponse{Translations: translations}, nil
}
