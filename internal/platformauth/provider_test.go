package platformauth

import (
	"context"
	"errors"
	"testing"

	"github.com/jekabolt/grbpwr-manager/internal/platformstore"
	"github.com/jekabolt/grbpwr-manager/proto/gen/auth"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// A provider button shows only when every value it needs is set; blank counts as unset.
func TestOptionsFromConfig(t *testing.T) {
	apple := platformstore.OAuthConfig{AppleEnabled: "true", AppleServicesID: "svc", AppleTeamID: "team", AppleKeyID: "key", ApplePrivateKey: "pem"}
	cases := []struct {
		name          string
		c             platformstore.OAuthConfig
		google, apple bool
	}{
		{"nothing set", platformstore.OAuthConfig{}, false, false},
		{"google id only", platformstore.OAuthConfig{GoogleClientID: "id"}, false, false},
		{"google secret only", platformstore.OAuthConfig{GoogleClientSecret: "s"}, false, false},
		{"google blank secret", platformstore.OAuthConfig{GoogleClientID: "id", GoogleClientSecret: "  "}, false, false},
		{"google both", platformstore.OAuthConfig{GoogleClientID: "id", GoogleClientSecret: "s"}, true, false},
		{"apple all four and on", apple, false, true},
		{"apple all four, switch off", func() platformstore.OAuthConfig { c := apple; c.AppleEnabled = "false"; return c }(), false, false},
		{"apple all four, switch unreadable", func() platformstore.OAuthConfig { c := apple; c.AppleEnabled = "yes please"; return c }(), false, false},
		{"apple on, no key", func() platformstore.OAuthConfig { c := apple; c.ApplePrivateKey = ""; return c }(), false, false},
		{"apple on, blank team", func() platformstore.OAuthConfig { c := apple; c.AppleTeamID = " "; return c }(), false, false},
		{"apple on, no key id", func() platformstore.OAuthConfig { c := apple; c.AppleKeyID = ""; return c }(), false, false},
		{"apple on, no services id", func() platformstore.OAuthConfig { c := apple; c.AppleServicesID = ""; return c }(), false, false},
		{"both", func() platformstore.OAuthConfig {
			c := apple
			c.GoogleClientID, c.GoogleClientSecret = "id", "s"
			return c
		}(), true, true},
	}
	for _, c := range cases {
		o := OptionsFromConfig(c.c)
		if o.Google() != c.google || o.Apple() != c.apple {
			t.Errorf("%s: google=%v apple=%v, want %v %v", c.name, o.Google(), o.Apple(), c.google, c.apple)
		}
	}
}

type fixedOptions struct{ google, apple bool }

func (f fixedOptions) Google() bool { return f.google }
func (f fixedOptions) Apple() bool  { return f.apple }

// GetSignInOptions answers what the interface says (lane O3 swaps in its registry), and nothing
// while the service is unusable.
func TestGetSignInOptionsAsksTheInterface(t *testing.T) {
	ctx := context.Background()
	for _, o := range []fixedOptions{{true, false}, {false, true}, {false, false}, {true, true}} {
		s := &Service{options: o}
		got, err := s.GetSignInOptions(ctx, &auth.GetSignInOptionsRequest{})
		if err != nil || got.GetGoogle() != o.google || got.GetApple() != o.apple {
			t.Errorf("options %+v → %v %v", o, got, err)
		}
	}
	s := &Service{options: fixedOptions{true, true}, ready: errors.New("no control plane")}
	if _, err := s.GetSignInOptions(ctx, &auth.GetSignInOptionsRequest{}); status.Code(err) != codes.Unavailable {
		t.Fatalf("unusable service: %v, want Unavailable", err)
	}
	if code, key := s.CompleteProviderSignIn(ctx, platformstore.ProviderSignIn{Provider: platformstore.ProviderGoogle, Subject: "s"}); code != "" || key != KeyTryAgain {
		t.Fatalf("unusable completion: code %d bytes, key %q", len(code), key)
	}
}
