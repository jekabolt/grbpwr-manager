package admin

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/jekabolt/grbpwr-manager/internal/aiprov"
	"github.com/jekabolt/grbpwr-manager/internal/aiprov/aiprovtest"
	"github.com/jekabolt/grbpwr-manager/internal/aiprov/registry"
	"github.com/jekabolt/grbpwr-manager/internal/aiprov/router"
	authsrv "github.com/jekabolt/grbpwr-manager/internal/apisrv/auth"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/jekabolt/grbpwr-manager/internal/openrouter"
	"github.com/jekabolt/grbpwr-manager/internal/tenant"
	pb_admin "github.com/jekabolt/grbpwr-manager/proto/gen/admin"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// translateWire is what the fake OpenRouter saw on one call.
type translateWire struct {
	Model          string   `json:"model"`
	Temperature    *float64 `json:"temperature"`
	MaxTokens      int      `json:"max_tokens"`
	ResponseFormat struct {
		Type string `json:"type"`
	} `json:"response_format"`
	System, User string
}

func newTranslateOpenRouter(t *testing.T, answer string) (*openrouter.Client, *[]translateWire) {
	t.Helper()
	var (
		mu    sync.Mutex
		calls []translateWire
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		require.NoError(t, err)
		var req struct {
			translateWire
			Messages []struct {
				Role    string          `json:"role"`
				Content json.RawMessage `json:"content"`
			} `json:"messages"`
		}
		require.NoError(t, json.Unmarshal(body, &req))
		wire := req.translateWire
		for _, m := range req.Messages {
			text, _ := orContent(t, m.Content)
			switch m.Role {
			case "system":
				wire.System = text
			case "user":
				wire.User = text
			}
		}
		mu.Lock()
		calls = append(calls, wire)
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		out, _ := json.Marshal(map[string]any{
			"model":   "google/gemini-2.5-flash",
			"choices": []map[string]any{{"message": map[string]any{"content": answer}, "finish_reason": "stop"}},
			"usage":   map[string]any{"prompt_tokens": 100, "completion_tokens": 50, "cost": 0.0004},
		})
		_, _ = w.Write(out)
	}))
	t.Cleanup(srv.Close)
	return openrouter.New(openrouter.Config{APIKey: "test-key", BaseURL: srv.URL, Model: "google/gemini-2.5-flash"}), &calls
}

// teamLedgerStore records the team of the ctx every ledger row was begun on.
type teamLedgerStore struct {
	seedCfgStore
	mu    sync.Mutex
	teams []int64
}

func (s *teamLedgerStore) BeginCall(ctx context.Context, st entity.AICallStart) (int64, error) {
	s.mu.Lock()
	if t, ok := tenant.From(ctx); ok {
		s.teams = append(s.teams, t.ID)
	} else {
		s.teams = append(s.teams, 0)
	}
	s.mu.Unlock()
	return s.seedCfgStore.BeginCall(ctx, st)
}

// newLedgeredRouter is newSeededRouter with the ledger app.go gives it.
func newLedgeredRouter(t *testing.T, client *openrouter.Client) (*router.Router, *teamLedgerStore) {
	t.Helper()
	ring := aiTestRing(t, 7)
	var cfg entity.AIConfig
	for _, k := range entity.AIProviderKeys() {
		p := entity.AIProvider{Key: k, Label: k, Enabled: k == entity.AIProviderOpenRouter}
		if k == entity.AIProviderOpenRouter {
			p.APIKeyEnc = aiSeal(t, ring, k, entity.AIKeyAPI, "test-key")
		}
		cfg.Providers = append(cfg.Providers, p)
	}
	for _, p := range entity.AIPurposes() {
		cfg.Routes = append(cfg.Routes, entity.AIRoute{Purpose: p,
			Candidates: []entity.AIRouteCandidate{{Position: 1, ProviderKey: entity.AIProviderOpenRouter}}})
	}
	cfg.Settings = entity.AISettings{ConfigVersion: 1,
		DefaultChatProviderKey: entity.AIProviderOpenRouter, DefaultImageProviderKey: entity.AIProviderOpenRouter}
	cfg.BudgetTimezone = "Europe/Warsaw"
	store := &teamLedgerStore{seedCfgStore: seedCfgStore{Store: &aiprovtest.Store{}, cfg: cfg}}
	reg := registry.New(store, ring, registry.EnvKeys{})
	require.NoError(t, reg.Reload(context.Background()))
	ledger := aiprov.NewLedger(store, func() string { return "Europe/Warsaw" })
	return router.New(reg, ledger, map[string]aiprov.Chatter{entity.AIProviderOpenRouter: client.Transport()},
		AIRouterDefaults(client), client.CompletionBase()), store
}

// signedIn is the ctx an admin's named token gives: the interceptor put the username in.
func signedIn(ctx context.Context) context.Context { return authsrv.PutAdminUsername(ctx, "anna") }

func translateReq() *pb_admin.TranslateFieldsRequest {
	return &pb_admin.TranslateFieldsRequest{
		Source: &pb_admin.TranslateSource{LangName: "English", Fields: map[string]string{
			"name": "GRBPWR hoodie", "description": "Heavy cotton, size M.", "empty": "  ",
		}},
		Targets: []*pb_admin.TranslateTarget{
			{Code: "fr", LangName: "French"}, {Code: "de", LangName: "German"}, {Code: "FR", LangName: "French"},
		},
		Constraints: map[string]*pb_admin.TranslateConstraint{"name": {MaxLength: 10}, "ghost": {MaxLength: 3}},
	}
}

// The press: _openrouter.js's request on the wire, one ledger row on the CALLER'S team, and an answer
// holding only what was asked (no stray language or field, the cap applied).
func TestTranslateFieldsCallsRouterOnTheCallersTeam(t *testing.T) {
	answer := `{"translations":{
		"fr":{"name":"Sweat à capuche GRBPWR","description":"Coton épais, taille M.","stray":"x"},
		"de":{"name":"GRBPWR Hoodie","description":""},
		"xx":{"name":"nope"}}}`
	client, calls := newTranslateOpenRouter(t, answer)
	ai, store := newLedgeredRouter(t, client)
	s := &Server{ai: ai, enhanceSem: make(chan struct{}, maxConcurrentEnhance)}

	ctx := tenant.With(signedIn(context.Background()), tenant.Team{ID: 2, FileKey: "0123456789abcdef0123456789abcdef"})
	resp, err := s.TranslateFields(ctx, translateReq())
	require.NoError(t, err)

	got := map[string]map[string]string{}
	for code, f := range resp.GetTranslations() {
		got[code] = f.GetFields()
	}
	require.Equal(t, map[string]map[string]string{
		"fr": {"name": "Sweat à ca", "description": "Coton épais, taille M."},
		"de": {"name": "GRBPWR Hoo"},
	}, got)

	require.Len(t, *calls, 1, "one model call for every language")
	w := (*calls)[0]
	require.Equal(t, "json_object", w.ResponseFormat.Type)
	require.NotNil(t, w.Temperature)
	require.InDelta(t, 0.3, *w.Temperature, 1e-9)
	require.Equal(t, 4000, w.MaxTokens)
	require.Contains(t, w.System, "fashion e-commerce translator for the brand grbpwr")
	require.Contains(t, w.System, "Use exactly these target codes as keys: fr, de.")
	require.Contains(t, w.System, "Each target must contain exactly these fields: description, name.")
	var user struct {
		SourceLanguage string                    `json:"sourceLanguage"`
		Fields         map[string]string         `json:"fields"`
		Constraints    map[string]map[string]int `json:"constraints"`
	}
	require.NoError(t, json.Unmarshal([]byte(w.User), &user))
	require.Equal(t, "English", user.SourceLanguage)
	require.NotContains(t, user.Fields, "empty", "an empty field never reaches the model")
	require.Equal(t, map[string]map[string]int{"name": {"maxLength": 10}}, user.Constraints)

	rows := store.Rows()
	require.Len(t, rows, 1)
	require.Equal(t, entity.AIPurposeEmailTranslate, rows[0].Start.Purpose)
	require.Equal(t, []int64{2}, store.teams, "the ledger row is written on the caller's team ctx")
}

func TestTranslateFieldsRefusesBeforeAnyCall(t *testing.T) {
	client, calls := newTranslateOpenRouter(t, `{"translations":{}}`)
	s := &Server{ai: newTestRouter(client), enhanceSem: make(chan struct{}, maxConcurrentEnhance)}

	long := strings.Repeat("a", maxTranslateFieldRunes+1)
	for name, mutate := range map[string]func(r *pb_admin.TranslateFieldsRequest){
		"no source language": func(r *pb_admin.TranslateFieldsRequest) { r.Source.LangName = " " },
		"nothing to translate": func(r *pb_admin.TranslateFieldsRequest) {
			r.Source.Fields = map[string]string{"name": " "}
		},
		"field too long":   func(r *pb_admin.TranslateFieldsRequest) { r.Source.Fields["name"] = long },
		"no targets":       func(r *pb_admin.TranslateFieldsRequest) { r.Targets = nil },
		"bad target code":  func(r *pb_admin.TranslateFieldsRequest) { r.Targets[0].Code = `fr","x` },
		"target lang name": func(r *pb_admin.TranslateFieldsRequest) { r.Targets[0].LangName = "" },
	} {
		req := translateReq()
		mutate(req)
		_, err := s.TranslateFields(signedIn(context.Background()), req)
		require.Equalf(t, codes.InvalidArgument, status.Code(err), name)
	}
	require.Empty(t, *calls)

	off := &Server{ai: newTestRouter(openrouter.New(openrouter.Config{})), enhanceSem: make(chan struct{}, maxConcurrentEnhance)}
	_, err := off.TranslateFields(signedIn(context.Background()), translateReq())
	require.Equal(t, codes.FailedPrecondition, status.Code(err))
}

func TestTranslateFieldsUnusableAnswer(t *testing.T) {
	for name, answer := range map[string]string{
		"not json":          "Sure! Here you go.",
		"no asked language": `{"translations":{"es":{"name":"x"}}}`,
	} {
		client, _ := newTranslateOpenRouter(t, answer)
		s := &Server{ai: newTestRouter(client), enhanceSem: make(chan struct{}, maxConcurrentEnhance)}
		_, err := s.TranslateFields(signedIn(context.Background()), translateReq())
		require.Equalf(t, codes.Internal, status.Code(err), name)
	}
}

// A legacy JWT without subject passes the auth interceptor with no account name. It has no hourly
// window of its own, so it is refused before the AI check, before the request is read, before any call.
func TestTranslateFieldsRefusesTokenWithoutName(t *testing.T) {
	client, calls := newTranslateOpenRouter(t, `{"translations":{"fr":{"name":"x"}}}`)
	ai, store := newLedgeredRouter(t, client)
	s := &Server{ai: ai, enhanceSem: make(chan struct{}, maxConcurrentEnhance)}
	team := tenant.With(context.Background(), tenant.Team{ID: 2, FileKey: "0123456789abcdef0123456789abcdef"})

	for name, ctx := range map[string]context.Context{
		"no name":    team,
		"blank name": authsrv.PutAdminUsername(team, "  "),
	} {
		_, err := s.TranslateFields(ctx, translateReq())
		require.Equalf(t, codes.Unauthenticated, status.Code(err), name)
		_, err = s.TranslateFields(ctx, &pb_admin.TranslateFieldsRequest{})
		require.Equalf(t, codes.Unauthenticated, status.Code(err), "%s: refused before the request is read", name)
	}
	off := &Server{ai: newTestRouter(openrouter.New(openrouter.Config{})), enhanceSem: make(chan struct{}, maxConcurrentEnhance)}
	_, err := off.TranslateFields(team, translateReq())
	require.Equal(t, codes.Unauthenticated, status.Code(err), "refused before the AI check")

	require.Empty(t, *calls)
	require.Empty(t, store.Rows(), "no ledger row")
}

// context: TECHPACK sends _openrouter.js's factory role (a technologist reads it, not a shopper); no
// context sends the shop role, as before. The rules after the role are the same in both.
func TestTranslateFieldsTechpackContext(t *testing.T) {
	answer := `{"translations":{"fr":{"name":"x","description":"y"},"de":{"name":"x","description":"y"}}}`
	for name, tc := range map[string]struct {
		ctx       pb_admin.TranslateContext
		want, not []string
	}{
		"techpack": {
			ctx: pb_admin.TranslateContext_TRANSLATE_CONTEXT_TECHPACK,
			want: []string{
				"You are a garment technologist translating a tech pack for a sewing factory (brand grbpwr).\n" +
					"Fields are operation names, part names, notes, materials and measurement points.\n" +
					"Use the factory floor's standard terms of the target language, terse like the source.\n" +
					"- Keep codes like FRT_L_MAIN, abbreviations SA, CB, CF, TS, RS, WS, GSM, numbers and units exactly as written.\n" +
					"Translate the provided fields from English into each requested target language.\nRules:\n",
			},
			not: []string{"fashion e-commerce translator"},
		},
		"no context": {
			want: []string{"You are a professional fashion e-commerce translator for the brand grbpwr.\n" +
				"Translate the provided fields from English into each requested target language.\nRules:\n"},
			not: []string{"garment technologist", "FRT_L_MAIN"},
		},
	} {
		client, calls := newTranslateOpenRouter(t, answer)
		s := &Server{ai: newTestRouter(client), enhanceSem: make(chan struct{}, maxConcurrentEnhance)}
		req := translateReq()
		req.Context = tc.ctx
		_, err := s.TranslateFields(signedIn(context.Background()), req)
		require.NoErrorf(t, err, name)
		require.Lenf(t, *calls, 1, name)
		sys := (*calls)[0].System
		for _, w := range tc.want {
			require.Truef(t, strings.HasPrefix(sys, w), "%s: prompt starts with the role\n%s", name, sys)
		}
		for _, n := range tc.not {
			require.NotContainsf(t, sys, n, name)
		}
		require.Containsf(t, sys, "Each target must contain exactly these fields: description, name.", name)
	}
}
