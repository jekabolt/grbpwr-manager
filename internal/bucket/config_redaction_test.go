package bucket

import (
	"fmt"
	"strings"
	"testing"
)

// TestConfigStringRedactsSecrets guards problem 007: no fmt verb that honours Stringer may reveal the
// S3 access/secret keys, so an accidental print or log of the bucket config cannot leak credentials.
func TestConfigStringRedactsSecrets(t *testing.T) {
	c := Config{
		S3AccessKey:       "AKIA-super-secret-access",
		S3SecretAccessKey: "very-secret-key-value",
		S3Endpoint:        "fra1.digitaloceanspaces.com",
		S3BucketName:      "grbpwr",
	}
	for _, verb := range []string{"%v", "%+v", "%s"} {
		out := fmt.Sprintf(verb, c)
		if strings.Contains(out, "super-secret-access") || strings.Contains(out, "very-secret-key-value") {
			t.Fatalf("%s leaked a secret: %q", verb, out)
		}
		if !strings.Contains(out, "REDACTED") {
			t.Fatalf("%s did not redact secrets: %q", verb, out)
		}
		// non-secret fields stay visible for debuggability
		if !strings.Contains(out, "fra1.digitaloceanspaces.com") {
			t.Fatalf("%s dropped the non-secret endpoint: %q", verb, out)
		}
	}
	// a pointer to the config (as embedded in Bucket) must redact too
	if strings.Contains(fmt.Sprintf("%v", &c), "very-secret-key-value") {
		t.Fatal("*Config leaked a secret")
	}
}
