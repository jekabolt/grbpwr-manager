package betaseed

import (
	"context"
	"fmt"
	"os"
	"regexp"

	auth "github.com/jekabolt/grbpwr-manager/proto/gen/auth"
)

// Authenticate logs in as user/pass and stores the bearer. If login fails and
// masterPassword is non-empty, it bootstraps the account (auth.Create) then logs
// in. The token is never printed by this package.
func (c *Client) Authenticate(ctx context.Context, user, pass, masterPassword string) error {
	if lr, err := c.AuthLogin(ctx, &auth.LoginRequest{Username: user, Password: pass}); err == nil && lr.GetAuthToken() != "" {
		c.token = lr.GetAuthToken()
		return nil
	} else if masterPassword == "" {
		return fmt.Errorf("login as %q failed and no master password to bootstrap: %w", user, err)
	}

	if _, err := c.AuthCreate(ctx, &auth.CreateRequest{
		User:           &auth.User{Username: user, Password: pass},
		MasterPassword: masterPassword,
	}); err != nil {
		return fmt.Errorf("bootstrap create account %q: %w", user, err)
	}
	lr, err := c.AuthLogin(ctx, &auth.LoginRequest{Username: user, Password: pass})
	if err != nil {
		return fmt.Errorf("login after bootstrap of %q: %w", user, err)
	}
	c.token = lr.GetAuthToken()
	return nil
}

// masterPwRe matches the AUTH_MASTER_PASSWORD env entry's value in a DO app.yaml,
// tolerating an optional scope: line between key and value.
var masterPwRe = regexp.MustCompile(`(?m)^- key: AUTH_MASTER_PASSWORD\n(?:.*\n)*?\s*value:\s*"?([^"\n]+)"?\s*$`)

// MasterPasswordFromYAML extracts AUTH_MASTER_PASSWORD from a DO app spec YAML.
// The value itself is returned but must never be logged by callers.
func MasterPasswordFromYAML(path string) (string, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("read %s: %w", path, err)
	}
	m := masterPwRe.FindSubmatch(b)
	if m == nil {
		return "", fmt.Errorf("AUTH_MASTER_PASSWORD not found in %s", path)
	}
	return string(m[1]), nil
}
