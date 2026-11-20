package translate

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

type systemSpy struct{ system string }

func (s *systemSpy) Enabled() bool { return true }
func (s *systemSpy) Complete(_ context.Context, system, _ string, _ bool) (string, error) {
	s.system = system
	return `{"items":[{"id":0,"text":"bonjour"}]}`, nil
}

func TestBrandPrompts(t *testing.T) {
	spy := &systemSpy{}
	_, err := New(spy).Translate(context.Background(), "en", "fr", []string{"hello"})
	require.NoError(t, err)
	require.Equal(t, systemPrompt, spy.system, "team #1 keeps the house prompt byte for byte")
	require.True(t, strings.HasPrefix(systemPrompt, "You are a professional localization translator for GRBPWR, a high-end streetwear brand."))

	base := New(spy)
	_, err = base.WithBrand(" Acme Studio ").Translate(context.Background(), "en", "fr", []string{"hello"})
	require.NoError(t, err)
	require.True(t, strings.HasPrefix(spy.system, `You are a professional localization translator for the fashion brand "Acme Studio".`))
	require.Contains(t, spy.system, `the brand name "Acme Studio"`)
	require.NotContains(t, strings.ToLower(spy.system), "grbpwr")
	require.NotContains(t, spy.system, "streetwear")

	_, err = base.WithBrand("").Translate(context.Background(), "en", "fr", []string{"hello"})
	require.NoError(t, err)
	require.True(t, strings.HasPrefix(spy.system, "You are a professional localization translator for a fashion brand."))
	require.NotContains(t, strings.ToLower(spy.system), "grbpwr")

	_, err = base.Translate(context.Background(), "en", "fr", []string{"hello"})
	require.NoError(t, err)
	require.Equal(t, systemPrompt, spy.system, "WithBrand returns a copy")
}
