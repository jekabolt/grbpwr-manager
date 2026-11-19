package bucket

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/jekabolt/grbpwr-manager/internal/tenant"
)

// TEAM KEY PREFIX (teams wave, lane V1b; 02-DESIGN §11).
//
// Every object a team other than #1 writes lands under {base}/t/{file_key}/…: media and design
// media ({base}/t/{fk}/{folder}/{yyyy}/{month}/{name}), pattern files, the files library and its
// previews, labels, tech-card archives and imports ({base}/t/{fk}/techcard-archives/…). Team #1 and
// teams mode OFF keep today's keys byte for byte, so every existing row keeps resolving and nothing
// is moved. TeamOwnsKey (proxy.go) is the reading side of the same rule.

// ErrNoTeamFileKey: in teams mode a write has no team in ctx, or the team has no valid file key.
// Writing under the legacy prefix instead would put the file in team #1's space, so the write is
// refused (the app root ctx carries team #1, so a worker or boot step never lands here).
var ErrNoTeamFileKey = errors.New("bucket: no team file key for this write")

// teamKeyParts are the key segments between the base folder and the object's own folder for the team
// in ctx: nil for team #1 and teams mode off; [t, file_key] for another team.
func (c *Config) teamKeyParts(ctx context.Context) ([]string, error) {
	if c == nil || !c.TeamsMode {
		return nil, nil
	}
	t, ok := tenant.From(ctx)
	if !ok {
		return nil, fmt.Errorf("%w: no team in ctx", ErrNoTeamFileKey)
	}
	if t.ID == tenant.TeamOneID {
		return nil, nil
	}
	if !validFileKey(t.FileKey) {
		return nil, fmt.Errorf("%w: team %d", ErrNoTeamFileKey, t.ID)
	}
	return []string{teamSegment, t.FileKey}, nil
}

// teamObjectRoot is the key prefix (with a trailing slash) of objects that are not under the base
// folder today — tech-card archives and imports: "" for team #1 / mode off (byte for byte today's
// root keys), "{base}/t/{file_key}/" for another team.
func (c *Config) teamObjectRoot(ctx context.Context) (string, error) {
	parts, err := c.teamKeyParts(ctx)
	if err != nil || parts == nil {
		return "", err
	}
	if base := strings.Trim(strings.TrimSpace(c.BaseFolder), "/"); base != "" {
		parts = append([]string{base}, parts...)
	}
	return strings.Join(parts, "/") + "/", nil
}
