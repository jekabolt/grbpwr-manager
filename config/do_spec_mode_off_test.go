package config

import (
	"os"
	"strings"
	"testing"
)

// The existing grbpwr installation stays teams mode OFF. lekalo.studio is a separate app with its own
// spec, so the grbpwr app specs must not carry its domains, its origin regex or the X-Team header.
func TestGrbpwrSpecsCarryNoTeamsSurface(t *testing.T) {
	for _, path := range []string{"../.do/app.yaml", "../.do/app-env.template.yaml"} {
		body, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		raw := string(body)
		for _, forbidden := range []string{"lekalo", "HTTP_ALLOWED_ORIGIN_REGEX", "Grpc-Metadata-X-Team", "PLATFORM_MYSQL_DSN"} {
			if strings.Contains(raw, forbidden) {
				t.Errorf("%s mentions %q: teams settings belong to the separate lekalo app spec", path, forbidden)
			}
		}
	}
}
