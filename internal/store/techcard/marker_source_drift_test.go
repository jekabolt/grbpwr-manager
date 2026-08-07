package techcard

import (
	"os"
	"regexp"
	"strings"
	"testing"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/stretchr/testify/require"
)

// TestMarkerSourceEnumMatchesMigration pins entity.ValidMarkerSources to the CHECK constraint in
// 0257 — the two are declared in different languages and nothing else stops them drifting apart.
// A value the Go side accepts and the CHECK refuses would surface as a driver 3819 at save time,
// long after the dto said "fine".
func TestMarkerSourceEnumMatchesMigration(t *testing.T) {
	raw, err := os.ReadFile("../sql/0257_tech_card_marker.sql")
	require.NoError(t, err)

	m := regexp.MustCompile(`chk_tcm_source CHECK \(source IN \(([^)]+)\)\)`).FindStringSubmatch(string(raw))
	require.Len(t, m, 2, "0257 must declare chk_tcm_source with an IN list")

	fromSQL := map[entity.MarkerSource]bool{}
	for _, v := range strings.Split(m[1], ",") {
		fromSQL[entity.MarkerSource(strings.Trim(strings.TrimSpace(v), "'"))] = true
	}
	require.Equal(t, entity.ValidMarkerSources, fromSQL)
}
