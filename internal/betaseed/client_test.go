package betaseed

import (
	"strings"
	"testing"
)

func TestNewClientAllowsOnlyBetaBackendHosts(t *testing.T) {
	tests := []struct {
		name     string
		target   string
		wantBase string
		wantErr  bool
	}{
		{name: "default URL", target: BetaBackend, wantBase: BetaBackend},
		{name: "default host", target: "backend-beta.grbpwr.com", wantBase: BetaBackend},
		{name: "numbered URL", target: "https://backend-beta-02.grbpwr.com", wantBase: "https://backend-beta-02.grbpwr.com"},
		{name: "numbered host", target: "backend-beta-99.grbpwr.com", wantBase: "https://backend-beta-99.grbpwr.com"},
		{name: "prod", target: "https://backend.grbpwr.com", wantErr: true},
		{name: "one digit", target: "backend-beta-2.grbpwr.com", wantErr: true},
		{name: "three digits", target: "backend-beta-002.grbpwr.com", wantErr: true},
		{name: "foreign suffix", target: "backend-beta-02.grbpwr.com.evil.test", wantErr: true},
		{name: "insecure scheme", target: "http://backend-beta-02.grbpwr.com", wantErr: true},
		{name: "port", target: "https://backend-beta-02.grbpwr.com:8443", wantErr: true},
		{name: "path", target: "https://backend-beta-02.grbpwr.com/api", wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			client, err := NewClient(tt.target)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("NewClient(%q) unexpectedly succeeded", tt.target)
				}
				if !strings.Contains(err.Error(), "refusing") {
					t.Fatalf("NewClient(%q) error = %q, want safety refusal", tt.target, err)
				}
				return
			}
			if err != nil {
				t.Fatalf("NewClient(%q) error = %v", tt.target, err)
			}
			if got := client.BaseURL(); got != tt.wantBase {
				t.Fatalf("BaseURL() = %q, want %q", got, tt.wantBase)
			}
		})
	}
}
