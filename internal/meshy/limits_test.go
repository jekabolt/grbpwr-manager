package meshy

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// TestSubmitRefusesAnOverlongPromptBeforeTheNetwork: the ceiling is a LOCAL fact, exactly like the
// image count, and the provider's answer to it is a 400 that a retry reproduces character for
// character. Sent anyway, that 400 used to land in the caller's default classification — «weather»
// — and burn the whole attempt cap re-posting the same sentence.
func TestSubmitRefusesAnOverlongPromptBeforeTheNetwork(t *testing.T) {
	called := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { called++ }))
	defer srv.Close()
	c := New(Config{APIKey: "k", BaseURL: srv.URL, HTTPTimeout: time.Second})

	req := sampleRequest()
	req.TexturePrompt = strings.Repeat("a", MaxTexturePrompt+1)
	if _, err := c.Submit(context.Background(), req); !errors.Is(err, ErrPromptTooLong) {
		t.Fatalf("err = %v, want ErrPromptTooLong", err)
	}
	if called != 0 {
		t.Fatalf("the refusal must cost no round trip, got %d requests", called)
	}
}

// ДЛИНА СЧИТАЕТСЯ В СИМВОЛАХ, А НЕ В БАЙТАХ. Провайдер считает то, что человек напечатал; счёт
// utf-8 байт отказал бы совершенно законной кириллической подсказке на половине потолка.
func TestPromptCeilingCountsCharactersNotBytes(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]any{"result": fakeTaskID})
	}))
	defer srv.Close()
	c := New(Config{APIKey: "k", BaseURL: srv.URL, HTTPTimeout: time.Second})

	req := sampleRequest()
	// Каждая буква — два байта: 1198 байт при 599 символах, то есть заведомо за байтовым потолком
	// и заведомо внутри символьного.
	req.TexturePrompt = strings.Repeat("я", MaxTexturePrompt-1)
	if _, err := c.Submit(context.Background(), req); err != nil {
		t.Fatalf("a prompt inside the character ceiling must be accepted: %v", err)
	}
}

// TestProviderFourHundredIsNotWeather: every 4xx that is not a rejected key, a rate limit or an
// unknown task means WE SENT SOMETHING WRONG. Without its own sentinel it reached the caller as a
// plain wrapped error, was classified as a transient fault and retried to the attempt cap — and
// the history row then read `provider_unavailable`, sending a person to the provider's status page
// for a request that was never acceptable.
//
// 5xx KEEPS THE GENERIC FORM, and that half matters as much: a server failing right now may well
// answer in thirty seconds, and turning it terminal would throw away runs that a retry would save.
func TestProviderFourHundredIsNotWeather(t *testing.T) {
	for _, tc := range []struct {
		name    string
		status  int
		wantBad bool
	}{
		{"400 bad request", http.StatusBadRequest, true},
		{"422 unprocessable", http.StatusUnprocessableEntity, true},
		{"409 conflict", http.StatusConflict, true},
		{"500 is still weather", http.StatusInternalServerError, false},
		{"503 is still weather", http.StatusServiceUnavailable, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(`{"message":"texture_prompt is too long"}`))
			}))
			defer srv.Close()
			c := New(Config{APIKey: "k", BaseURL: srv.URL, HTTPTimeout: time.Second})
			_, err := c.Submit(context.Background(), sampleRequest())
			if err == nil {
				t.Fatal("a non-2xx must be an error")
			}
			if got := errors.Is(err, ErrBadRequest); got != tc.wantBad {
				t.Fatalf("errors.Is(err, ErrBadRequest) = %v, want %v (err = %v)", got, tc.wantBad, err)
			}
		})
	}

	// 401 и 429 сохраняют СВОИ вердикты: у них есть собственные действия («почини ключ»,
	// «подожди»), и растворить их в общем «мы послали не то» значило бы стереть эту разницу.
	for _, tc := range []struct {
		status int
		want   error
	}{
		{http.StatusUnauthorized, ErrUnauthorized},
		{http.StatusForbidden, ErrUnauthorized},
		{http.StatusTooManyRequests, ErrRateLimited},
	} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(tc.status)
		}))
		c := New(Config{APIKey: "k", BaseURL: srv.URL, HTTPTimeout: time.Second})
		_, err := c.Submit(context.Background(), sampleRequest())
		if !errors.Is(err, tc.want) {
			t.Fatalf("HTTP %d: err = %v, want %v", tc.status, err, tc.want)
		}
		if errors.Is(err, ErrBadRequest) {
			t.Fatalf("HTTP %d must keep its own verdict, not fall into ErrBadRequest", tc.status)
		}
		srv.Close()
	}
}
