package entity

import (
	"bytes"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func nativeCrossRowReviewFixtureV8(t *testing.T) (*patternProductCompositeSidecarV8Review, PatternProductNativeCrossRowCurrentV8) {
	t.Helper()
	keys := []string{
		"01JABCDEFGHJKMNPQRSTVWXYZ0", "01JABCDEFGHJKMNPQRSTVWXYZ1",
		"01JABCDEFGHJKMNPQRSTVWXYZ2", "01JABCDEFGHJKMNPQRSTVWXYZ3",
		"01JABCDEFGHJKMNPQRSTVWXYZ4", "01JABCDEFGHJKMNPQRSTVWXYZ5",
	}
	row := func(index int, scope, digestDigit string) nativeCrossRowReviewRowV8 {
		return nativeCrossRowReviewRowV8{
			compositeSavedRowV3: compositeSavedRowV3{
				TechCardID: 11, CardLockVersion: 8, PatternLineKey: keys[index], ScopeKey: scope,
				URL: "https://files.example/" + scope + ".dxf", Revision: 5, SizeBytes: 1200,
				SHA256: strings.Repeat(digestDigit, 64),
			},
			PatternVersion: 2,
		}
	}
	a, b := row(0, "main", "a"), row(1, "pocketing", "b")
	identity := func(row nativeCrossRowReviewRowV8) SavedPatternRowIdentity {
		out := compositeRowIdentity(row.compositeSavedRowV3)
		out.PatternVersion = row.PatternVersion
		return out
	}
	wireA, wireB := `{"pieceLineKey":"`+keys[2]+`"}`, `{"pieceLineKey":"`+keys[3]+`"}`
	side := func(index int, row nativeCrossRowReviewRowV8, block, wire string) nativeCrossRowReviewSideV8 {
		fileID := nativeCrossRowSourceFileIDV8(row)
		prefix := "dxf-vertex-v1/dxf-curve-v1/f/" + hex.EncodeToString([]byte(fileID)) + "/"
		return nativeCrossRowReviewSideV8{
			Row: row, SideIndex: index, PartOrdinal: 0, PieceLineKey: keys[index+2],
			BlockName: block, PieceID: block,
			ContourID:    "dxf-piece-v1/p/" + hex.EncodeToString([]byte(block)) + "/s/4d/g/seam",
			NativePathID: 100 + index, SourceFileID: fileID, Direction: "forward",
			OrderedVertexIDs:          []string{prefix + "native-start", prefix + "native-end"},
			CapturedSavedAnchorSHA256: nativeCrossRowSealV8(nativeCrossRowAnchorDomainV8, []byte(wire)),
		}
	}
	review := &patternProductCompositeSidecarV8Review{
		SchemaVersion: patternProductCompositeSidecarDocumentV8,
		MediaType:     PatternProductCompositeSidecarV8MediaType,
		Encoding:      patternSemanticSidecarEncoding,
		Profile:       nativeCrossRowReviewProfileV8,
	}
	review.Bindings.AnchorRow = a
	review.Bindings.ServerAssemblyRevision = "asm:v1:0123456789abcdef0123456789abcdef:3"
	review.Bindings.CompleteSourceSHA256 = strings.Repeat("c", 64)
	review.Selection.OperationNumber = 170
	review.Selection.OwnedSeamKeys = keys[4:]
	review.Selection.SavedDirection = TechCardSeamReversed
	review.Selection.SizeID = 84
	review.Selection.SizeToken = "M"
	review.Selection.SizeBasis = nativeCrossRowReviewSizeBasisV8
	review.Selection.Members = []nativeCrossRowReviewSideV8{side(0, a, "FRONT", wireA), side(1, b, "BACK", wireB)}
	review.Review.SourceDecisionSHA256 = strings.Repeat("d", 64)
	review.Review.BoundSourceRequestSHA256 = strings.Repeat("e", 64)
	review.Review.GridSHA256 = strings.Repeat("f", 64)
	review.Review.StationCount = 3

	buildAnchor := func(index int) []TechCardSeamAnchor {
		return []TechCardSeamAnchor{{PieceLineKey: keys[2+index], LenMm: 10, Samples: make([]TechCardSeamSample, 5)}}
	}
	stitch := TechCardSeam{
		TechCardId: 11,
		TechCardSeamInput: TechCardSeamInput{
			SeamKey: keys[4], Kind: TechCardSeamKindEdge, Source: TechCardSeamSourceManual,
			Status: TechCardSeamConfirmed, Direction: TechCardSeamReversed,
			SideA: buildAnchor(0), SideB: buildAnchor(1),
		},
	}
	pose := stitch
	pose.Kind, pose.Direction, pose.SeamKey = TechCardSeamKindPose, TechCardSeamDirectionUnknown, keys[5]
	card := &TechCard{Id: 11, LockVersion: 8, TechCardInsert: TechCardInsert{SizeIds: []int{83, 84, 85}}}
	card.Operations = []TechCardOperation{{
		OperationNumber: sql.NullInt32{Int32: 170, Valid: true},
		Work:            sql.NullString{String: "join_lockstitch", Valid: true},
		InputKeys:       keys[2:4], SeamKeys: keys[4:],
	}}
	rawSource := `{"ownedSeams":[{"seamKey":"` + keys[4] +
		`","sideA":{"parts":[` + wireA + `]},"sideB":{"parts":[` + wireB + `]}}]}`
	source := &SavedAssemblyCompleteSourceV1{
		SourceSHA256: review.Bindings.CompleteSourceSHA256, CanonicalJSON: []byte(rawSource),
		Rows: []SavedPatternRowIdentity{identity(a), identity(b)}, OwnedSeams: []TechCardSeam{stitch, pose},
		Aliases: []SavedAssemblySourceAliasV1{
			{ScopeKey: "main", BlockName: "FRONT", PieceLineKey: keys[2]},
			{ScopeKey: "pocketing", BlockName: "BACK", PieceLineKey: keys[3]},
		},
	}
	return review, PatternProductNativeCrossRowCurrentV8{
		AnchorRow: identity(a), Card: card, Source: source,
		ServerAssemblyRevision: review.Bindings.ServerAssemblyRevision, SizeTokensByID: map[int]string{84: "M"},
	}
}

func nativeCrossRowWriteFixtureV8(t *testing.T, review *patternProductCompositeSidecarV8Review) PatternSemanticSidecarWrite {
	t.Helper()
	jsonBytes, err := json.Marshal(review)
	require.NoError(t, err)
	payload, err := canonicalJSONWithJSNumbers(jsonBytes)
	require.NoError(t, err)
	row, previous := review.Bindings.AnchorRow, ""
	return PatternSemanticSidecarWrite{
		TechCardId: row.TechCardID, PatternLineKey: row.PatternLineKey, ExpectedCardLockVersion: &row.CardLockVersion,
		ExpectedDxfSHA256: row.SHA256, ExpectedPatternVersion: row.PatternVersion, ExpectedSavedRowRevision: &row.Revision,
		ExpectedPreviousSidecarSHA256: &previous, ExpectedServerAssemblyRevision: &review.Bindings.ServerAssemblyRevision,
		ExpectedAssemblyTopologySHA256: &review.Bindings.CompleteSourceSHA256,
		SelectedOperationNumber:        review.Selection.OperationNumber,
		ExpectedPostSaveReceiptSHA256:  PatternProductNativeCrossRowReviewReceiptV8(payload),
		MediaType:                      PatternProductCompositeSidecarV8MediaType,
		SidecarSHA256:                  PatternSemanticSidecarPayloadSHA256(payload), Payload: payload,
	}
}

func TestPatternProductNativeCrossRowReviewV8FirstAndFreshRead(t *testing.T) {
	review, current := nativeCrossRowReviewFixtureV8(t)
	first := nativeCrossRowWriteFixtureV8(t, review)
	require.NoError(t, ValidateFirstPatternProductNativeCrossRowReviewV8(first, current))
	existingWriter := first
	existingWriter.By = "local-review-test"
	require.NoError(t, ValidatePatternSemanticSidecarWrite(&existingWriter))

	row := current.AnchorRow
	stored := PatternSemanticSidecar{
		TechCardId: row.TechCardID, PatternLineKey: row.PatternLineKey, CardLockVersion: row.CardLockVersion,
		DxfSHA256: row.SHA256, PatternVersion: row.PatternVersion, SavedRowRevision: row.SavedRowRevision,
		MediaType: first.MediaType, SidecarSHA256: first.SidecarSHA256, ByteLength: len(first.Payload),
		PostSaveReceiptSHA256: first.ExpectedPostSaveReceiptSHA256, Payload: bytes.Clone(first.Payload),
	}
	require.NoError(t, ValidateStoredPatternProductNativeCrossRowReviewV8(stored, current))
	current.Source.Rows[1].SavedRowRevision++
	require.ErrorIs(t, ValidateStoredPatternProductNativeCrossRowReviewV8(stored, current), ErrTechCardPatternSidecarBindingMoved)
	current.Source.Rows[1].SavedRowRevision--
	stored.Payload[0] = '['
	require.ErrorIs(t, ValidateStoredPatternProductNativeCrossRowReviewV8(stored, current), ErrTechCardPatternSidecarCorrupt)
}

func TestPatternProductNativeCrossRowReviewV8RefusesBadRequestCAS(t *testing.T) {
	for _, tc := range []struct {
		name string
		edit func(*PatternSemanticSidecarWrite)
	}{
		{"omitted prior", func(in *PatternSemanticSidecarWrite) { in.ExpectedPreviousSidecarSHA256 = nil }},
		{"nonempty prior", func(in *PatternSemanticSidecarWrite) { in.ExpectedPreviousSidecarSHA256 = &in.SidecarSHA256 }},
		{"omitted source CAS", func(in *PatternSemanticSidecarWrite) { in.ExpectedAssemblyTopologySHA256 = nil }},
		{"selected-topology SHA in source CAS", func(in *PatternSemanticSidecarWrite) { in.ExpectedAssemblyTopologySHA256 = &in.SidecarSHA256 }},
		{"omitted clock CAS", func(in *PatternSemanticSidecarWrite) { in.ExpectedServerAssemblyRevision = nil }},
		{"moved request operation", func(in *PatternSemanticSidecarWrite) { in.SelectedOperationNumber++ }},
		{"wrong review receipt", func(in *PatternSemanticSidecarWrite) { in.ExpectedPostSaveReceiptSHA256 = in.SidecarSHA256 }},
		{"wrong bytes digest", func(in *PatternSemanticSidecarWrite) { in.SidecarSHA256 = in.ExpectedPostSaveReceiptSHA256 }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			review, current := nativeCrossRowReviewFixtureV8(t)
			first := nativeCrossRowWriteFixtureV8(t, review)
			tc.edit(&first)
			require.ErrorContains(t, ValidateFirstPatternProductNativeCrossRowReviewV8(first, current), "request CAS")
		})
	}
}

func TestPatternProductNativeCrossRowReviewV8RefusesInvalidReview(t *testing.T) {
	for _, tc := range []struct {
		name string
		edit func(*patternProductCompositeSidecarV8Review)
	}{
		{"unsupported profile", func(d *patternProductCompositeSidecarV8Review) { d.Profile = "unverified" }},
		{"payload has non-null prior", func(d *patternProductCompositeSidecarV8Review) {
			prior := ""
			d.WriteCAS.ExpectedPreviousSidecarSHA256 = &prior
		}},
		{"generated layer", func(d *patternProductCompositeSidecarV8Review) { d.Selection.Members[1].Row.ScopeKey = "interlining" }},
		{"cut-only contour", func(d *patternProductCompositeSidecarV8Review) {
			d.Selection.Members[1].ContourID = strings.TrimSuffix(d.Selection.Members[1].ContourID, "/seam") + "/cut"
		}},
		{"deformed physical direction", func(d *patternProductCompositeSidecarV8Review) { d.Selection.Members[1].Direction = "unknown" }},
		{"same member row", func(d *patternProductCompositeSidecarV8Review) {
			d.Selection.Members[1].Row = d.Selection.Members[0].Row
		}},
		{"repeated side", func(d *patternProductCompositeSidecarV8Review) { d.Selection.Members[1].SideIndex = 0 }},
		{"wrong part ordinal", func(d *patternProductCompositeSidecarV8Review) { d.Selection.Members[1].PartOrdinal = 1 }},
		{"foreign source vertex", func(d *patternProductCompositeSidecarV8Review) {
			d.Selection.Members[1].OrderedVertexIDs[0] = d.Selection.Members[0].OrderedVertexIDs[0]
		}},
		{"duplicate vertex", func(d *patternProductCompositeSidecarV8Review) {
			d.Selection.Members[1].OrderedVertexIDs[0] = d.Selection.Members[1].OrderedVertexIDs[1]
		}},
		{"missing explicit size review", func(d *patternProductCompositeSidecarV8Review) { d.Selection.SizeBasis = "archive-default" }},
		{"short station count", func(d *patternProductCompositeSidecarV8Review) { d.Review.StationCount = 1 }},
		{"unsafe clock format", func(d *patternProductCompositeSidecarV8Review) {
			d.Bindings.ServerAssemblyRevision = "asm:v1:clock:003"
		}},
		{"clock overflow", func(d *patternProductCompositeSidecarV8Review) {
			d.Bindings.ServerAssemblyRevision = "asm:v1:0123456789abcdef0123456789abcdef:18446744073709551616"
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			review, current := nativeCrossRowReviewFixtureV8(t)
			tc.edit(review)
			first := nativeCrossRowWriteFixtureV8(t, review)
			require.Error(t, ValidateFirstPatternProductNativeCrossRowReviewV8(first, current))
		})
	}
}

func TestPatternProductNativeCrossRowReviewV8RefusesNonCanonicalShape(t *testing.T) {
	review, current := nativeCrossRowReviewFixtureV8(t)
	first := nativeCrossRowWriteFixtureV8(t, review)
	for _, payload := range [][]byte{
		append(bytes.Clone(first.Payload), '\n'),
		bytes.Replace(first.Payload, []byte(`"sideIndex":0,`), nil, 1),
		bytes.Replace(first.Payload, []byte(`"sideIndex":0,`), []byte(`"sideIndex":0,"sideIndex":0,`), 1),
		bytes.Replace(first.Payload, []byte(`"partOrdinal":0`), []byte(`"PartOrdinal":0`), 1),
		bytes.Replace(first.Payload, []byte(`"sizeToken":"M"`), []byte(`"physicalPlacement":{},"sizeToken":"M"`), 1),
	} {
		changed := first
		require.NotEqual(t, first.Payload, payload)
		changed.Payload = payload
		changed.SidecarSHA256 = PatternSemanticSidecarPayloadSHA256(payload)
		changed.ExpectedPostSaveReceiptSHA256 = PatternProductNativeCrossRowReviewReceiptV8(payload)
		require.Error(t, ValidateFirstPatternProductNativeCrossRowReviewV8(changed, current))
	}
}

func TestPatternProductNativeCrossRowReviewV8RefusesMovedCompleteBinding(t *testing.T) {
	for _, tc := range []struct {
		name string
		edit func(*PatternProductNativeCrossRowCurrentV8)
	}{
		{"clock", func(c *PatternProductNativeCrossRowCurrentV8) { c.ServerAssemblyRevision += "1" }},
		{"size", func(c *PatternProductNativeCrossRowCurrentV8) { c.SizeTokensByID[84] = "L" }},
		{"member revision", func(c *PatternProductNativeCrossRowCurrentV8) { c.Source.Rows[1].SavedRowRevision++ }},
		{"member pattern version", func(c *PatternProductNativeCrossRowCurrentV8) { c.Source.Rows[1].PatternVersion++ }},
		{"member alias changes", func(c *PatternProductNativeCrossRowCurrentV8) { c.Source.Aliases[1].ScopeKey = "interlining" }},
		{"AO seam order changes", func(c *PatternProductNativeCrossRowCurrentV8) {
			c.Card.Operations[0].SeamKeys = []string{c.Card.Operations[0].SeamKeys[1], c.Card.Operations[0].SeamKeys[0]}
		}},
		{"AO side order changes", func(c *PatternProductNativeCrossRowCurrentV8) { slices.Reverse(c.Card.Operations[0].InputKeys) }},
		{"physical status changes", func(c *PatternProductNativeCrossRowCurrentV8) { c.Source.OwnedSeams[0].Stale = true }},
		{"saved size differs", func(c *PatternProductNativeCrossRowCurrentV8) { c.Source.OwnedSeams[0].AnchoredSize = "M" }},
		{"POSE removed", func(c *PatternProductNativeCrossRowCurrentV8) { c.Source.OwnedSeams = c.Source.OwnedSeams[:1] }},
		{"captured anchor changes", func(c *PatternProductNativeCrossRowCurrentV8) {
			c.Source.CanonicalJSON = bytes.Replace(c.Source.CanonicalJSON, []byte(`"parts":[{`), []byte(`"parts":[{"lenMm":80,`), 1)
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			review, current := nativeCrossRowReviewFixtureV8(t)
			first := nativeCrossRowWriteFixtureV8(t, review)
			tc.edit(&current)
			require.ErrorIs(t, ValidateFirstPatternProductNativeCrossRowReviewV8(first, current), ErrTechCardPatternSidecarBindingMoved)
		})
	}
}
