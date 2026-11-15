package techcard

import (
	"database/sql"
	"math"
	"os"
	"strings"
	"testing"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/stretchr/testify/require"
)

func TestSavedAssemblyTopologyGoldenV1(t *testing.T) {
	operations := []entity.TechCardOperation{
		{
			OperationNumber: sql.NullInt32{Int32: 10, Valid: true},
			Work:            sql.NullString{String: "join_lockstitch", Valid: true},
			InputKeys:       []string{"P-A", "P-B"},
			OutputUnitKey:   sql.NullString{String: "U-BODY", Valid: true},
			OutputUnitName:  sql.NullString{String: "Body", Valid: true},
			SeamKeys:        []string{"S-1", "S-2"},
		},
		{
			OperationNumber: sql.NullInt32{Int32: 20, Valid: true},
			Work:            sql.NullString{String: "press", Valid: true},
		},
	}
	seams := []entity.TechCardSeam{
		{TechCardSeamInput: entity.TechCardSeamInput{
			SeamKey: "S-1", Status: entity.TechCardSeamConfirmed, Kind: entity.TechCardSeamKindEdge,
			Direction: entity.TechCardSeamSame, Source: entity.TechCardSeamSourceManual, AnchoredSize: "M",
			SideA: []entity.TechCardSeamAnchor{{
				PieceLineKey: "P-A", EdgeHint: "ea", ContourSig: "ca", LenMm: 120.5,
				RangeFrom: 0, RangeTo: 1,
				Samples: []entity.TechCardSeamSample{{U: 0, V: 0}, {U: 1, V: 1}},
			}},
			SideB: []entity.TechCardSeamAnchor{{
				PieceLineKey: "P-B", EdgeHint: "eb", ContourSig: "cb", LenMm: 120.5,
				RangeFrom: 0.25, RangeTo: 0.75, Layer: entity.TechCardSeamLayerOver,
				Samples: []entity.TechCardSeamSample{{U: 0, V: 1}, {U: 1, V: 0}},
			}},
		}},
		{TechCardSeamInput: entity.TechCardSeamInput{
			SeamKey: "S-2", Status: entity.TechCardSeamConfirmed, Kind: entity.TechCardSeamKindPartial,
			Direction: entity.TechCardSeamReversed, Source: entity.TechCardSeamSourceGraph, AnchoredSize: "M",
			SideA: []entity.TechCardSeamAnchor{{
				PieceLineKey: "P-A", EdgeHint: "ea2", ContourSig: "ca2", LenMm: 8,
				RangeFrom: 0.1, RangeTo: 0.2, Layer: entity.TechCardSeamLayerUnder,
				Samples: []entity.TechCardSeamSample{},
			}},
			SideB: []entity.TechCardSeamAnchor{{
				PieceLineKey: "P-B", EdgeHint: "eb2", ContourSig: "cb2", LenMm: 8,
				RangeFrom: 0.8, RangeTo: 0.9,
				Samples: []entity.TechCardSeamSample{},
			}},
		}},
	}

	canonical, digest, err := savedAssemblyTopology(operations, seams, 10)
	require.NoError(t, err)
	const wantCanonical = `{"operations":[{"authoringOperationId":null,"inputKeys":["P-A","P-B"],"number":10,"outputUnitKey":"U-BODY","outputUnitName":"Body","phaseEKind":"stitch","seamKeys":["S-1","S-2"],"work":"join_lockstitch"},{"authoringOperationId":null,"inputKeys":[],"number":20,"outputUnitKey":"","outputUnitName":"","phaseEKind":null,"seamKeys":[],"work":"press"}],"seams":[{"anchoredSize":"M","confirmed":true,"direction":"same","kind":"edge","seamKey":"S-1","sideA":[{"contourSig":"ca","edgeHint":"ea","lenMm":120.5,"pieceLineKey":"P-A","range":[0,1],"samples":[[0,0],[1,1]]}],"sideB":[{"contourSig":"cb","edgeHint":"eb","layer":"over","lenMm":120.5,"pieceLineKey":"P-B","range":[0.25,0.75],"samples":[[0,1],[1,0]]}],"source":"manual","stale":false},{"anchoredSize":"M","confirmed":true,"direction":"reversed","kind":"partial","seamKey":"S-2","sideA":[{"contourSig":"ca2","edgeHint":"ea2","layer":"under","lenMm":8,"pieceLineKey":"P-A","range":[0.1,0.2],"samples":[]}],"sideB":[{"contourSig":"cb2","edgeHint":"eb2","lenMm":8,"pieceLineKey":"P-B","range":[0.8,0.9],"samples":[]}],"source":"graph","stale":false}],"selectedOperationNumber":10}`
	require.Equal(t, wantCanonical, string(canonical))
	require.Equal(t, "926162f78fd5c1ee9da48176559271eb20968a19c657c94be3ebc68b52c031fc", digest)
}

func TestSavedAssemblyCanonicalJSONMatchesJSONStringEscaping(t *testing.T) {
	canonical, err := savedAssemblyCanonicalJSON(map[string]any{
		"name": "A <B> & C\u2028D\u2029E", "escaped": "quote=\" slash=\\ control=\x01",
	})
	require.NoError(t, err)
	require.Equal(t,
		"{\"escaped\":\"quote=\\\" slash=\\\\ control=\\u0001\",\"name\":\"A <B> & C\u2028D\u2029E\"}",
		string(canonical))
}

func TestSavedAssemblyTopologyRejectsNegativeZero(t *testing.T) {
	_, _, err := savedAssemblyTopology(nil, []entity.TechCardSeam{{
		TechCardSeamInput: entity.TechCardSeamInput{
			SeamKey: "S-1",
			SideA:   []entity.TechCardSeamAnchor{{LenMm: math.Copysign(0, -1)}},
		},
	}}, 1)
	require.ErrorIs(t, err, entity.ErrTechCardSavedAssemblyUnsupported)
}

func TestSelectSavedAssemblySeamsFailsClosed(t *testing.T) {
	base := &entity.TechCard{
		TechCardInsert: entity.TechCardInsert{Operations: []entity.TechCardOperation{{
			OperationNumber: sql.NullInt32{Int32: 10, Valid: true},
			Work:            sql.NullString{String: "join_lockstitch", Valid: true},
			SeamKeys:        []string{"S-1", "S-2"},
		}}},
		Seams: []entity.TechCardSeam{
			{TechCardSeamInput: entity.TechCardSeamInput{
				SeamKey: "S-1", Status: entity.TechCardSeamConfirmed, Kind: entity.TechCardSeamKindEdge,
				Direction: entity.TechCardSeamSame, Source: entity.TechCardSeamSourceManual, AnchoredSize: "M",
				SideA: []entity.TechCardSeamAnchor{{PieceLineKey: "P-A"}}, SideB: []entity.TechCardSeamAnchor{{PieceLineKey: "P-B"}},
			}},
			{TechCardSeamInput: entity.TechCardSeamInput{
				SeamKey: "S-2", Status: entity.TechCardSeamConfirmed, Kind: entity.TechCardSeamKindEdge,
				Direction: entity.TechCardSeamSame, Source: entity.TechCardSeamSourceManual, AnchoredSize: "M",
				SideA: []entity.TechCardSeamAnchor{{PieceLineKey: "P-A"}}, SideB: []entity.TechCardSeamAnchor{{PieceLineKey: "P-B"}},
			}},
		},
	}

	t.Run("duplicate operation number", func(t *testing.T) {
		card := *base
		card.Operations = append([]entity.TechCardOperation(nil), base.Operations...)
		card.Operations = append(card.Operations, entity.TechCardOperation{
			OperationNumber: sql.NullInt32{Int32: 10, Valid: true},
		})
		_, err := selectSavedAssemblySeams(&card, 10)
		require.ErrorIs(t, err, entity.ErrTechCardSavedAssemblyUnsupported)
	})

	t.Run("second seam owner", func(t *testing.T) {
		card := *base
		card.Operations = append([]entity.TechCardOperation(nil), base.Operations...)
		card.Operations = append(card.Operations, entity.TechCardOperation{
			OperationNumber: sql.NullInt32{Int32: 20, Valid: true}, SeamKeys: []string{"S-1"},
		})
		_, err := selectSavedAssemblySeams(&card, 10)
		require.ErrorIs(t, err, entity.ErrTechCardSavedAssemblyUnsupported)
	})

	t.Run("stale selected seam", func(t *testing.T) {
		card := *base
		card.Seams = append([]entity.TechCardSeam(nil), base.Seams...)
		card.Seams[0].Stale = true
		_, err := selectSavedAssemblySeams(&card, 10)
		require.ErrorIs(t, err, entity.ErrTechCardSavedAssemblyUnsupported)
	})

	t.Run("mismatched pair", func(t *testing.T) {
		card := *base
		card.Seams = append([]entity.TechCardSeam(nil), base.Seams...)
		card.Seams[1].SideB = []entity.TechCardSeamAnchor{{PieceLineKey: "P-C"}}
		_, err := selectSavedAssemblySeams(&card, 10)
		require.ErrorIs(t, err, entity.ErrTechCardSavedAssemblyUnsupported)
	})
}

func TestSavedPatternRowIdentityUsesDedicatedRevision(t *testing.T) {
	base := entity.SavedPatternRowIdentity{
		TechCardID: 7, ScopeKey: "fabric:outer", PatternLineKey: "01JABCDEFGHJKMNPQRSTVWXYZ0",
		SavedRowRevision: 4, CardLockVersion: 8, URL: "/patterns/front.dxf", SizeBytes: 1234,
		SHA256: strings.Repeat("a", 64), PatternVersion: 3,
	}
	humanVersionChanged := base
	humanVersionChanged.PatternVersion = 99
	require.True(t, sameSavedPatternRow(base, humanVersionChanged),
		"the human-pinned pattern version is deliberately outside the exact eight-field row CAS")
	rowRevisionChanged := base
	rowRevisionChanged.SavedRowRevision++
	require.False(t, sameSavedPatternRow(base, rowRevisionChanged))
	for _, alias := range []string{strings.ToLower(base.PatternLineKey), base.PatternLineKey + " "} {
		drifted := base
		drifted.PatternLineKey = alias
		require.False(t, sameSavedPatternRow(base, drifted))
		require.Error(t, validateSavedAssemblySnapshotRequest(entity.SavedAssemblySnapshotRequest{
			ExpectedRow: drifted, SelectedOperationNumber: 10,
		}))
	}
}

func TestSavedAssemblyClockAndMigrationWiring(t *testing.T) {
	require.Equal(t, "asm:v1:0123456789abcdef0123456789abcdef:9",
		(assemblyClockRow{LineageID: "0123456789abcdef0123456789abcdef", Revision: 9}).Token())

	migration, err := os.ReadFile("../sql/0473_saved_assembly_authority.sql")
	require.NoError(t, err)
	sqlText := string(migration)
	require.Contains(t, sqlText, "saved_row_revision INT UNSIGNED NOT NULL DEFAULT 1")
	require.Contains(t, sqlText, "revision BIGINT UNSIGNED NOT NULL DEFAULT 1")
	require.Contains(t, sqlText, "INSERT IGNORE INTO tech_card_assembly_clock")

	techcardSource, err := os.ReadFile("techcard.go")
	require.NoError(t, err)
	require.Contains(t, string(techcardSource), "createTechCardAssemblyClock(ctx, rep.DB(), id)")
	require.Contains(t, string(techcardSource), "bumpTechCardAssemblyClock(ctx, rep.DB(), id)")
	require.Contains(t, string(techcardSource), "saved_row_revision = saved_row_revision + 1")

	seamsSource, err := os.ReadFile("seams.go")
	require.NoError(t, err)
	require.Contains(t, string(seamsSource), "bumpTechCardAssemblyClock(ctx, db, in.TechCardId)")
	require.Contains(t, string(seamsSource), "bumpTechCardAssemblyClock(ctx, db, techCardID)")
}

func TestEveryDirectTechCardCreatorCreatesAssemblyClockBeforeChildren(t *testing.T) {
	for _, tc := range []struct {
		name, file, insert, clock, children string
	}{
		{
			name: "ordinary create", file: "techcard.go",
			insert:   "INSERT INTO tech_card (%s, guided, create_request_id)",
			clock:    "createTechCardAssemblyClock(ctx, rep.DB(), id)",
			children: "insertTechCardChildren(ctx, rep.DB(), id",
		},
		{
			name: "season clone", file: "clone.go",
			insert:   "INSERT INTO tech_card (%s) VALUES (%s)",
			clock:    "createTechCardAssemblyClock(ctx, db, id)",
			children: "insertTechCardChildren(ctx, db, id",
		},
		{
			name: "archive import", file: "import.go",
			insert:   "INSERT INTO tech_card (%s) VALUES (%s)",
			clock:    "createTechCardAssemblyClock(ctx, db, newID)",
			children: "insertTechCardChildren(ctx, db, newID",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			source, err := os.ReadFile(tc.file)
			require.NoError(t, err)
			text := string(source)
			insert := strings.Index(text, tc.insert)
			clock := strings.Index(text, tc.clock)
			children := strings.Index(text, tc.children)
			require.NotEqual(t, -1, insert, "direct tech_card INSERT not found")
			require.NotEqual(t, -1, clock, "direct creator does not create its Assembly clock")
			require.NotEqual(t, -1, children, "child insert not found")
			require.Less(t, insert, clock, "Assembly clock must be created after the card row")
			require.Less(t, clock, children, "Assembly clock must exist before Assembly children")
		})
	}
}

func TestAssemblyClockBumpSelfHealsMissingRowsAndIncrementsExistingRows(t *testing.T) {
	body := funcBody(t, "assembly_clock.go", "func bumpTechCardAssemblyClock")
	insert := strings.Index(body, "INSERT INTO tech_card_assembly_clock")
	duplicate := strings.Index(body, "ON DUPLICATE KEY UPDATE")
	increment := strings.Index(body, "revision = revision + 1")
	require.GreaterOrEqual(t, insert, 0, "a card created outside the package must get a clock row")
	require.Greater(t, duplicate, insert, "the same statement must handle an existing clock")
	require.Greater(t, increment, duplicate, "an existing clock must advance exactly once")
	require.Contains(t, body, "VALUES (:id, LOWER(REPLACE(UUID(), '-', '')), 1)",
		"a repaired clock starts with a valid lineage and revision 1")
	require.NotContains(t, body, "UPDATE tech_card_assembly_clock SET",
		"an update-only bump would fail again for synthetic/direct card creators")
	require.Contains(t, body, "rows < 1 || rows > 2",
		"accept MySQL's insert=1 and duplicate-update=2 affected-row semantics only")
}
