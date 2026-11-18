package gemini

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"
)

// With OpenURL set, the transport reads the picture through it (the bucket's host-checked reader,
// which admits a read-only host for team #1) and never calls KeyFromURL; a refusal stays a refusal.
func TestReadObjectUsesOpenURL(t *testing.T) {
	const raw = "https://files.grbpwr.com/grbpwr-com/2024/may/a.png"
	var asked string
	c := New(Config{
		KeyFromURL: func(string) (string, error) { t.Fatal("KeyFromURL must not be called"); return "", nil },
		OpenURL: func(_ context.Context, u string) (io.ReadCloser, int64, error) {
			asked = u
			return io.NopCloser(strings.NewReader("png")), 3, nil
		},
	})
	_, data, err := c.readObject(context.Background(), 1, raw, 1<<20)
	if err != nil || string(data) != "png" || asked != raw {
		t.Fatalf("readObject = %q, %v (asked %q)", data, err, asked)
	}

	c = New(Config{OpenURL: func(context.Context, string) (io.ReadCloser, int64, error) {
		return nil, 0, errors.New("not readable")
	}})
	if _, _, err := c.readObject(context.Background(), 1, raw, 1<<20); err == nil {
		t.Fatal("a refused url must stay refused")
	}
}
