//go:build boardlive

package admin

// LIVE MEASUREMENT of the moodboard labels (101 Ф1): the production ladder (designBoardLabelLadder,
// its prompts and parser) against OpenRouter on known photos. Never compiled by CI or a plain
// `go test`: the build tag keeps it out. It touches no database.
//
//	set -a; . ~/.config/grbpwr-probe.env; set +a
//	go test -tags boardlive ./internal/apisrv/admin/ -run TestBoardLabelLive -v -count=1
//
// Env: OPENROUTER_API_KEY (required, never printed); BOARD_CHEAP / BOARD_STRONG (slugs, default the
// 0403 routes); BOARD_FIXTURES (JSON {"<name>": {"url": …, "want": "front|back|side_l|side_r"}},
// default the six photos of cards 38 and 51).

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/aiprov"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
)

type boardLiveAI struct {
	key    string
	models map[string]string
	spent  float64
	log    []string
}

func (b *boardLiveAI) Enabled(purpose string) bool { return b.models[purpose] != "" }

func (b *boardLiveAI) Chat(ctx context.Context, purpose string, req aiprov.ChatRequest) (*aiprov.ChatResult, error) {
	model := b.models[purpose]
	parts := []map[string]any{{"type": "text", "text": req.User}}
	for _, u := range req.ImageURLs {
		parts = append(parts, map[string]any{"type": "image_url", "image_url": map[string]string{"url": u}})
	}
	body := map[string]any{
		"model": model, "max_tokens": req.MaxTokens,
		"messages":        []map[string]any{{"role": "system", "content": req.System}, {"role": "user", "content": parts}},
		"response_format": map[string]string{"type": "json_object"},
		"usage":           map[string]bool{"include": true},
	}
	if req.Effort != "" {
		body["reasoning"] = map[string]string{"effort": req.Effort}
	}
	raw, _ := json.Marshal(body)
	hreq, _ := http.NewRequestWithContext(ctx, http.MethodPost, "https://openrouter.ai/api/v1/chat/completions", bytes.NewReader(raw))
	hreq.Header.Set("Authorization", "Bearer "+b.key)
	hreq.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(hreq)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("http %d: %.300s", resp.StatusCode, data)
	}
	var out struct {
		Model   string `json:"model"`
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
		Usage struct {
			Cost float64 `json:"cost"`
		} `json:"usage"`
	}
	if err := json.Unmarshal(data, &out); err != nil || len(out.Choices) == 0 {
		return nil, fmt.Errorf("bad body: %.300s", data)
	}
	b.spent += out.Usage.Cost
	text := out.Choices[0].Message.Content
	b.log = append(b.log, fmt.Sprintf("%s %s $%.5f: %s", purpose, out.Model, out.Usage.Cost, strings.ReplaceAll(text, "\n", " ")))
	return &aiprov.ChatResult{Text: text, Model: out.Model}, nil
}

type boardLiveFixture struct {
	URL  string `json:"url"`
	Want string `json:"want"`
}

func TestBoardLabelLive(t *testing.T) {
	key := strings.TrimSpace(os.Getenv("OPENROUTER_API_KEY"))
	if key == "" {
		t.Skip("OPENROUTER_API_KEY is not set")
	}
	fixtures := map[string]boardLiveFixture{
		"38-124": {"https://files.grbpwr.com/grbpwr-com-beta/grbpwr-com-beta/2026/august/202608301954553194802-og.webp", "front"},
		// 125: the doc (101 §6) says «на деле правый бок»; the photo faces the picture's LEFT edge like 815, so by the
		// wearer's-flank geometry it is the LEFT flank (or the photo is mirrored). Scored by geometry.
		"38-125": {"https://files.grbpwr.com/grbpwr-com-beta/grbpwr-com-beta/2026/august/202608301954591934dbf-og.webp", "side_l"},
		"38-126": {"https://files.grbpwr.com/grbpwr-com-beta/grbpwr-com-beta/2026/august/20260830195503049d611-og.webp", "back"},
		"51-813": {"https://files.grbpwr.com/grbpwr-com-beta/grbpwr-com-beta/2026/october/20261006191129897dc18-og.webp", "front"},
		"51-815": {"https://files.grbpwr.com/grbpwr-com-beta/grbpwr-com-beta/2026/october/20261006191157113ac85-og.webp", "side_l"},
		"51-816": {"https://files.grbpwr.com/grbpwr-com-beta/grbpwr-com-beta/2026/october/202610061912109377ae8-og.webp", "back"},
	}
	if f := os.Getenv("BOARD_FIXTURES"); f != "" {
		raw, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		fixtures = map[string]boardLiveFixture{}
		if err := json.Unmarshal(raw, &fixtures); err != nil {
			t.Fatal(err)
		}
	}
	cheap, strong := os.Getenv("BOARD_CHEAP"), os.Getenv("BOARD_STRONG")
	if cheap == "" {
		cheap = "google/gemini-3.1-flash-lite"
	}
	if strong == "" {
		strong = "anthropic/claude-sonnet-5.5"
	}
	ai := &boardLiveAI{key: key, models: map[string]string{entity.AIPurposeBoardLabel: cheap, entity.AIPurposeBoardRead: strong}}
	names := make([]string, 0, len(fixtures))
	for n := range fixtures {
		names = append(names, n)
	}
	sort.Strings(names)
	hit, lrWrong := 0, 0
	for _, n := range names {
		f := fixtures[n]
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		got, err := designBoardLabelLadder(ctx, ai, designBoardPicture{MediaID: 1, Purpose: entity.TechCardMediaRoleTarget}, f.URL)
		cancel()
		verdict := "MISS"
		switch {
		case err != nil:
			verdict = "ERR " + err.Error()
		case got.Role == f.Want:
			verdict, hit = "OK", hit+1
		case got.Role == entity.DesignViewSide && strings.HasPrefix(f.Want, "side_"):
			verdict, hit = "OK (side, L/R honestly unsure)", hit+1
		case (got.Role == "side_l" || got.Role == "side_r") && strings.HasPrefix(f.Want, "side_"):
			verdict, lrWrong = "L/R WRONG", lrWrong+1
		}
		t.Logf("%s want %-6s got %-6q state %-7s src %-12s caption %q → %s", n, f.Want, got.Role, got.State, got.Source, got.ModelCaption, verdict)
	}
	for _, l := range ai.log {
		t.Log(l)
	}
	t.Logf("views %d/%d, L/R wrong %d, spent $%.4f", hit, len(names), lrWrong, ai.spent)
}
