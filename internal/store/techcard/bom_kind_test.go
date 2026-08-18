package techcard

import (
	"database/sql"
	"strings"
	"testing"
	"time"

	"github.com/jmoiron/sqlx"
	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/require"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
)

// TestBomKindSectionsPartitionValidSections is the anti-drift guard the derived list exists for.
//
// A BOM section is classified on exactly ONE axis and the three groups must therefore PARTITION
// entity.ValidTechCardBomSections: roll goods answer НАЗНАЧЕНИЕ (0265), `label` is owned by
// tech_card_label.label_type (0070), and everything else answers ЧТО ЭТО ЗА ПОЗИЦИЯ (0278). The
// assertion is deliberately a partition and not "kindEligible has these six values": a hand-written
// copy of the eligible list passes a value check forever, but the day somebody adds a twelfth
// section to the enum it lands in NO group and this fails — which is exactly the moment a stale copy
// would otherwise start silently refusing kinds on a family that should accept them.
func TestBomKindSectionsPartitionValidSections(t *testing.T) {
	seen := make(map[entity.TechCardBomSection]int, len(entity.ValidTechCardBomSections))
	for _, s := range rollGoodsSectionList {
		seen[s]++
	}
	seen[entity.BomSectionLabel]++
	for _, s := range kindEligibleSectionList {
		seen[s]++
	}

	for s, n := range seen {
		require.True(t, entity.ValidTechCardBomSections[s],
			"section %q is grouped but is not a valid BOM section", s)
		require.Equal(t, 1, n,
			"section %q is in more than one group; the three groups must be disjoint", s)
	}
	for s := range entity.ValidTechCardBomSections {
		require.Contains(t, seen, s,
			"section %q belongs to no classification group: it can carry neither назначение nor kind, and only tech_card_label owns `label`", s)
	}
	require.Len(t, seen, len(entity.ValidTechCardBomSections))
}

// TestBomKindHomeSectionsAreEligible is the other half of the same coupling: the pairing table in
// internal/entity names a home section per kind, and every one of those must be a section a kind is
// allowed on at all. A kind homed on a roll-goods or label section would be unreachable — the
// eligibility arm of validateBomKindSection would refuse it before the pairing arm ever ran — and
// nothing else in the system would say so.
//
// The reverse is NOT asserted: section='other' is eligible and has no kinds of its own on purpose.
// It is served solely by BomKindOther, the section-agnostic value.
func TestBomKindHomeSectionsAreEligible(t *testing.T) {
	agnostic := 0
	for kind := range entity.ValidTechCardBomKinds {
		home, ok := entity.BomKindHomeSection(kind)
		require.True(t, ok, "kind %q is in the valid set but has no home entry", kind)
		if home == entity.BomKindAnySection {
			agnostic++
			require.Equal(t, entity.BomKindOther, kind,
				"only `other` may be section-agnostic; %q must name a home section", kind)
			continue
		}
		require.True(t, kindEligibleSections[home],
			"kind %q is homed on section %q, which no kind may be placed on", kind, home)
	}
	require.Equal(t, 1, agnostic, "exactly one kind (other) may be section-agnostic")

	// The sentinel must not be mistakable for a section — that is the whole reason it is the empty
	// string and not, say, BomSectionOther (which is a real, eligible section a kind CAN sit on).
	require.False(t, entity.ValidTechCardBomSections[entity.BomKindAnySection])
	require.NotEqual(t, entity.BomSectionOther, entity.BomKindAnySection)
}

// TestValidateBomKindSection covers the three refusals and the two acceptances of the pairing check.
func TestValidateBomKindSection(t *testing.T) {
	line := func(section entity.TechCardBomSection, kind entity.TechCardBomKind) *entity.TechCardBomItem {
		b := &entity.TechCardBomItem{Section: section}
		if kind != "" {
			b.Kind = sql.NullString{String: string(kind), Valid: true}
		}
		return b
	}

	require.NoError(t, validateBomKindSection(line(entity.BomSectionHardware, entity.BomKindZipper), 0))
	require.NoError(t, validateBomKindSection(line(entity.BomSectionFabric, ""), 0),
		"a line with no kind is never refused, whatever its section")

	// `other` is the one value legal in EVERY eligible section, including the section that has no
	// kinds of its own.
	for _, s := range kindEligibleSectionList {
		require.NoError(t, validateBomKindSection(line(s, entity.BomKindOther), 0),
			"kind `other` must be legal on eligible section %q", s)
	}

	// Roll goods carry назначение instead; the refusal must say so rather than just "not allowed".
	err := validateBomKindSection(line(entity.BomSectionFabric, entity.BomKindZipper), 3)
	require.Error(t, err)
	require.Contains(t, err.Error(), "purpose")

	// A label's vocabulary is owned by tech_card_label.label_type — even `other` is refused there.
	require.Error(t, validateBomKindSection(line(entity.BomSectionLabel, entity.BomKindOther), 0))

	// Eligible section, wrong family: the message must name the home section AND the actual one.
	err = validateBomKindSection(line(entity.BomSectionThread, entity.BomKindZipper), 1)
	require.Error(t, err)
	require.Contains(t, err.Error(), string(entity.BomSectionHardware))
	require.Contains(t, err.Error(), string(entity.BomSectionThread))

	// An unknown value is refused, never silently dropped: the read path degrades, the write path must not.
	unknown := &entity.TechCardBomItem{
		Section: entity.BomSectionHardware,
		Kind:    sql.NullString{String: "zip", Valid: true},
	}
	require.Error(t, validateBomKindSection(unknown, 0))
}

// bomItemUpsertParams mirrors the map bomItemParams builds (plus the two provenance keys and the id
// the update adds), so a parameter added to a query and forgotten in the map — or the reverse —
// surfaces here as a bind failure instead of as a 500 on the card-save path.
func bomItemUpsertParams() map[string]any {
	b := &entity.TechCardBomItem{
		LineKey: "01ABCDEF0000000000000001",
		Section: entity.BomSectionHardware,
		Kind:    sql.NullString{String: string(entity.BomKindZipper), Valid: true},
		Name:    "молния",
	}
	params := bomItemParams(1, b, 0, b.LineKey)
	params["price_source"] = sql.NullString{}
	params["price_snapshot_at"] = time.Time{}
	// Wastage provenance (0296) is decided in Go (bomWastageProvenance) and added at the call
	// site, exactly like the price pair above.
	params["wastage_source"] = entity.BomWastageSourceManual
	params["wastage_lay_count"] = sql.NullInt64{}
	params["wastage_applied_at"] = sql.NullTime{}
	params["wastage_applied_percent"] = decimal.NullDecimal{}
	params["id"] = 7
	return params
}

// sqlx parses ':' ANYWHERE in a named query — including inside a `--` SQL comment — as a parameter,
// and a name the args map does not carry fails at BIND time, i.e. at request time. Both statements
// grew a guarded column pair in 0278; sqlx.Named reproduces both failure modes without a database.
func TestBomItemUpsertQueriesBind(t *testing.T) {
	for name, q := range map[string]string{"update": bomItemUpdateQuery, "insert": bomItemInsertQuery} {
		bound, _, err := sqlx.Named(q, bomItemUpsertParams())
		if err != nil {
			t.Fatalf("bom %s query does not bind: %v", name, err)
		}
		if strings.Contains(bound, ":") {
			t.Fatalf("bom %s query still holds a ':' after binding: %s", name, bound)
		}
	}
}
