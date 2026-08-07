package dto

import (
	"database/sql"
	"os"
	"testing"

	"github.com/jekabolt/grbpwr-manager/internal/cache"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
)

func TestMain(m *testing.M) {
	// Pattern-url write validation is fail-closed on the host allowlist (production wires
	// it from the bucket config); the fixtures below use "cdn" as the managed host.
	SetManagedPatternHosts("cdn", "files.grbpwr.com")
	cache.RefreshDictionary(&entity.DictionaryInfo{Colors: []entity.Color{
		{ID: 1, Code: "BLK", Name: "black", Hex: sql.NullString{String: "#000000", Valid: true}},
		{ID: 2, Code: "WHT", Name: "white", Hex: sql.NullString{String: "#FFFFFF", Valid: true}},
		{ID: 3, Code: "OFW", Name: "off-white", Hex: sql.NullString{String: "#F5F5F0", Valid: true}},
		{ID: 4, Code: "NAV", Name: "navy", Hex: sql.NullString{String: "#1A2238", Valid: true}},
		{ID: 5, Code: "UNK", Name: "unknown"},
	}})
	os.Exit(m.Run())
}
