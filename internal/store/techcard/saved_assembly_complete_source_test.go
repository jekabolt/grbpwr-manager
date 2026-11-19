package techcard

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"math"
	"slices"
	"strings"
	"testing"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/require"
)

const (
	completeSourceMainRow    = "01JABCDEFGHJKMNPQRSTVWXYZ0"
	completeSourcePocketRow  = "01JABCDEFGHJKMNPQRSTVWXYZ1"
	completeSourceFusingRow  = "01JABCDEFGHJKMNPQRSTVWXYZ2"
	completeSourceMainPiece  = "01JABCDEFGHJKMNPQRSTVWXYZ3"
	completeSourceOtherPiece = "01JABCDEFGHJKMNPQRSTVWXYZ4"
	completeSourceSeam       = "01JABCDEFGHJKMNPQRSTVWXYZ5"
	completeSourcePose       = "01JABCDEFGHJKMNPQRSTVWXYZ6"
	completeSourceUnowned    = "01JABCDEFGHJKMNPQRSTVWXYZ7"
	completeSourceMainBOM    = "01JABCDEFGHJKMNPQRSTVWXYZ8"
	completeSourcePocketBOM  = "01JABCDEFGHJKMNPQRSTVWXYZ9"
	completeSourceFusingBOM  = "01JABCDEFGHJKMNPQRSTVWXYZA"
	completeSourceClock      = "asm:v1:0123456789abcdef0123456789abcdef:19"
)

func completeSourceFixture() (*entity.TechCard, entity.SavedPatternRowIdentity, []entity.RollGoodsLine) {
	pattern := func(key, purpose, bom, basename string, version int) entity.TechCardSizePattern {
		return entity.TechCardSizePattern{
			LineKey: key, URL: "/patterns/" + basename,
			FabricPurpose: sql.NullString{String: purpose, Valid: purpose != ""},
			BomLineKey:    sql.NullString{String: bom, Valid: bom != ""},
			Filename:      sql.NullString{String: basename, Valid: true},
			SizeBytes:     sql.NullInt64{Int64: 1024 + int64(version), Valid: true},
			SHA256:        sql.NullString{String: strings.Repeat(string(rune('0'+version)), 64), Valid: true},
			Version:       version, SavedRowRevision: uint32(version + 10),
		}
	}
	main := pattern(completeSourceMainRow, "main", "", "main.dxf", 5)
	pocket := pattern(completeSourcePocketRow, "", completeSourcePocketBOM, "pocket.dxf", 6)
	fusing := pattern(completeSourceFusingRow, "", completeSourceFusingBOM, "fusing.dxf", 4)
	seam := savedAssemblyTestSeam(completeSourceSeam, completeSourceMainPiece, completeSourceOtherPiece)
	seam.TechCardId = 156
	seam.Kind = entity.TechCardSeamKindComposite
	seam.AnchoredSize = "" // as stored on the source trousers
	seam.SideB[0].Samples = []entity.TechCardSeamSample{{U: .1, V: .2}}
	seam.SideB[0].Notches = 1
	seam.SideB[0].TurnDeg = -12.5
	seam.SideB[0].Layer = entity.TechCardSeamLayerOver
	seam.SideB = append(seam.SideB, entity.TechCardSeamAnchor{PieceLineKey: completeSourceMainPiece})
	pose := savedAssemblyTestSeam(completeSourcePose, completeSourceMainPiece, completeSourceMainPiece)
	pose.TechCardId = 156
	pose.Kind = entity.TechCardSeamKindPose
	pose.Direction = entity.TechCardSeamDirectionUnknown
	pose.AnchoredSize = ""
	pose.Stale = true
	pose.SideA[0].Placement = &entity.TechCardSeamPlacement{XMm: 8, YMm: 14, RotDeg: 11, Flip: true}
	unowned := savedAssemblyTestSeam(completeSourceUnowned, completeSourceMainPiece, completeSourceOtherPiece)
	unowned.TechCardId = 156
	card := &entity.TechCard{
		Id: 156, LockVersion: 9,
		TechCardInsert: entity.TechCardInsert{
			Patterns: []entity.TechCardSizePattern{pocket, main, fusing},
			Pieces: []entity.TechCardPiece{
				{LineKey: completeSourceMainPiece, Name: "Front"},
				{LineKey: completeSourceOtherPiece, Name: "Pocket"},
			},
			PieceDxfAliases: []entity.TechCardPieceDxfAlias{
				{BomLineKey: completeSourceFusingBOM, BlockName: "FRONT", PieceLineKey: completeSourceMainPiece},
				{BomLineKey: completeSourcePocketBOM, BlockName: "PCK_L", PieceLineKey: completeSourceOtherPiece},
				{FabricPurpose: "main", BlockName: "PCK_L", PieceLineKey: completeSourceMainPiece},
			},
			Operations: []entity.TechCardOperation{
				{
					OperationNumber: sql.NullInt32{Int32: 10, Valid: true},
					Work:            sql.NullString{String: "press", Valid: true},
					PieceLineKeys:   []string{completeSourceMainPiece},
					InputKeys:       []string{completeSourceMainPiece},
					Note:            sql.NullString{String: "Keep the press instruction", Valid: true},
				},
				{
					OperationNumber: sql.NullInt32{Int32: 20, Valid: true},
					Work:            sql.NullString{String: "join_lockstitch", Valid: true},
					InputKeys:       []string{completeSourceMainPiece, completeSourceOtherPiece},
					PieceLineKeys:   []string{completeSourceMainPiece, completeSourceOtherPiece},
					SeamKeys:        []string{completeSourceSeam, completeSourcePose},
				},
			},
		},
		Seams: []entity.TechCardSeam{unowned, pose, seam},
	}
	anchor := entity.SavedPatternRowIdentity{
		TechCardID: card.Id, ScopeKey: "main", PatternLineKey: main.LineKey, CardLockVersion: card.LockVersion,
		URL: main.URL, SizeBytes: main.SizeBytes.Int64, SHA256: main.SHA256.String,
		SavedRowRevision: main.SavedRowRevision, PatternVersion: main.Version,
	}
	return card, anchor, []entity.RollGoodsLine{
		{LineKey: completeSourceMainBOM, Purpose: "main", Section: entity.BomSectionFabric},
		{LineKey: completeSourcePocketBOM, Purpose: "pocketing", Section: entity.BomSectionFabric},
		{LineKey: completeSourceFusingBOM, Section: entity.BomSectionInterlining},
	}
}

func TestSavedAssemblyCompleteSourceKeepsRawOwnedSeamsAndAllRows(t *testing.T) {
	card, anchor, lines := completeSourceFixture()
	source, err := buildSavedAssemblyCompleteSourceV1(card, anchor, lines, completeSourceClock)
	require.NoError(t, err)
	require.Equal(t, []entity.TechCardSeam{card.Seams[2], card.Seams[1]}, source.OwnedSeams)
	require.Equal(t, []string{completeSourceFusingBOM, "main", "pocketing"},
		[]string{source.Rows[0].ScopeKey, source.Rows[1].ScopeKey, source.Rows[2].ScopeKey})
	require.Equal(t, source.Rows[2].PatternLineKey, completeSourcePocketRow)
	require.EqualValues(t, 6, source.Rows[2].PatternVersion)
	require.EqualValues(t, 16, source.Rows[2].SavedRowRevision)
	require.Equal(t, []entity.SavedAssemblySourceAliasV1{
		{ScopeKey: completeSourceFusingBOM, BlockName: "FRONT", PieceLineKey: completeSourceMainPiece},
		{ScopeKey: "main", BlockName: "PCK_L", PieceLineKey: completeSourceMainPiece},
		{ScopeKey: "pocketing", BlockName: "PCK_L", PieceLineKey: completeSourceOtherPiece},
	}, source.Aliases)
	require.NotContains(t, string(source.CanonicalJSON), "downloadUrl")
	require.NotContains(t, string(source.CanonicalJSON), completeSourceUnowned)
	require.Contains(t, string(source.CanonicalJSON), `"note":"Keep the press instruction"`)
	require.Contains(t, string(source.CanonicalJSON), `"anchoredSize":""`)
	require.Contains(t, string(source.CanonicalJSON), `"placement":{"flip":true,"rotDeg":11,"xMm":8,"yMm":14}`)
	require.Contains(t, string(source.CanonicalJSON), `"notches":1`)
	sum := sha256.Sum256(append([]byte(savedAssemblyCompleteSourceDomainV1+"\x00"), source.CanonicalJSON...))
	require.Equal(t, hex.EncodeToString(sum[:]), source.SourceSHA256)

	var payload map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(source.CanonicalJSON, &payload))
	operations, err := savedAssemblyOperationsWireCanonicalJSON(card.Operations)
	require.NoError(t, err)
	require.Equal(t, string(operations), string(payload["operations"]))
	_, err = selectSavedAssemblySeams(card, 20)
	require.ErrorIs(t, err, entity.ErrTechCardSavedAssemblyUnsupported, "the selected V1 read still refuses unanchored seams and POSE")

	slices.Reverse(card.Patterns)
	slices.Reverse(card.PieceDxfAliases)
	slices.Reverse(card.Seams)
	reordered, err := buildCheckedSavedAssemblyCompleteSourceV1(
		card, source.Rows[2], lines, completeSourceClock, source.SourceSHA256,
	)
	require.NoError(t, err)
	require.Equal(t, source.SourceSHA256, reordered.SourceSHA256,
		"anchor choice, unordered row retrieval and orphan seam retrieval do not change the source")
}

func TestSavedAssemblyCompleteSourceDigestBindsCurrentInputs(t *testing.T) {
	base, anchor, lines := completeSourceFixture()
	want, err := buildSavedAssemblyCompleteSourceV1(base, anchor, lines, completeSourceClock)
	require.NoError(t, err)
	for _, tc := range []struct {
		name   string
		mutate func(*entity.TechCard)
	}{
		{"order of linked seam and POSE", func(c *entity.TechCard) { slices.Reverse(c.Operations[1].SeamKeys) }},
		{"complete AO workflow note", func(c *entity.TechCard) { c.Operations[0].Note.String = "Changed" }},
		{"secondary pattern revision", func(c *entity.TechCard) { c.Patterns[0].SavedRowRevision++ }},
		{"fusing pattern hash", func(c *entity.TechCard) { c.Patterns[2].SHA256.String = strings.Repeat("a", 64) }},
		{"placement", func(c *entity.TechCard) { c.Seams[1].SideA[0].Placement.XMm++ }},
		{"raw anchor samples", func(c *entity.TechCard) { c.Seams[2].SideB[0].Samples[0].V = .5 }},
		{"scope alias", func(c *entity.TechCard) { c.PieceDxfAliases[0].BlockName = "FRONT_ALT" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			card, _, _ := completeSourceFixture()
			tc.mutate(card)
			changed, err := buildSavedAssemblyCompleteSourceV1(card, anchor, lines, completeSourceClock)
			require.NoError(t, err)
			require.NotEqual(t, want.SourceSHA256, changed.SourceSHA256)
			stale, err := buildCheckedSavedAssemblyCompleteSourceV1(
				card, anchor, lines, completeSourceClock, want.SourceSHA256,
			)
			require.ErrorIs(t, err, entity.ErrTechCardSavedAssemblyMoved)
			require.Nil(t, stale)
		})
	}
	newClock, err := buildSavedAssemblyCompleteSourceV1(base, anchor, lines,
		"asm:v1:0123456789abcdef0123456789abcdef:20")
	require.NoError(t, err)
	require.NotEqual(t, want.SourceSHA256, newClock.SourceSHA256)
}

func TestSavedAssemblyCompleteSourceIncludesGeneratedGarmentLabelSources(t *testing.T) {
	const brand = "01M75ACEBB74BMT91RJ7WSTTJY"
	const composition = "01M75AP6W3H8619K02P2R37WFK"
	const unrelated = "01JABCDEFGHJKMNPQRSTVWXYZB"
	card, anchor, lines := completeSourceFixture()
	mm := func(value int64) decimal.NullDecimal {
		return decimal.NewNullDecimal(decimal.NewFromInt(value))
	}
	card.GarmentLabels = []entity.TechCardGarmentLabel{
		{LineKey: composition, Key: "composition", WidthMm: mm(30), HeightMm: mm(100), Pages: 1},
		{LineKey: unrelated, Key: "unused"},
		{LineKey: brand, Key: "Label logo", Kind: "brand", WidthMm: mm(44), HeightMm: mm(56),
			Fold: sql.NullString{String: "half", Valid: true}, FoldLinesMm: "28", Pages: 1},
	}
	card.Operations[1].OperationNumber.Int32 = 680
	card.Operations[1].Work.String = "attach_label"
	card.Operations[1].LabelKeys = []string{brand}
	card.Operations = append(card.Operations, entity.TechCardOperation{
		OperationNumber: sql.NullInt32{Int32: 690, Valid: true}, LabelKeys: []string{composition},
	})
	// Card 11's label attach step owns a partial seam and POSE with no anchored size.
	card.Seams[2].Kind = entity.TechCardSeamKindPartial
	card.Seams[2].SideB = card.Seams[2].SideB[:1]
	card.Seams[2].SideB[0].PieceLineKey = brand
	card.Seams[1].SideA[0].PieceLineKey = brand
	read := func() (*entity.SavedAssemblyCompleteSourceV1, error) {
		return buildSavedAssemblyCompleteSourceV1(card, anchor, lines, completeSourceClock)
	}
	source, err := read()
	require.NoError(t, err)
	require.Equal(t, []entity.TechCardGarmentLabel{card.GarmentLabels[2], card.GarmentLabels[0]}, source.GarmentLabels)
	require.Equal(t, []entity.TechCardSeam{card.Seams[2], card.Seams[1]}, source.OwnedSeams)
	require.NotContains(t, string(source.CanonicalJSON), unrelated)
	require.Contains(t, string(source.CanonicalJSON), `"foldLinesMm":"28"`)
	require.Contains(t, string(source.CanonicalJSON), `"heightMm":{"value":"56"}`)
	require.Contains(t, string(source.CanonicalJSON), `"widthMm":{"value":"44"}`)
	card.Operations[1].LabelKeys = nil
	anchorOnly, err := read()
	require.NoError(t, err)
	require.Equal(t, source.GarmentLabels, anchorOnly.GarmentLabels)
	card.Operations[1].LabelKeys = []string{brand}
	slices.Reverse(card.GarmentLabels)
	reordered, err := read()
	require.NoError(t, err)
	require.Equal(t, source.SourceSHA256, reordered.SourceSHA256)
	card.GarmentLabels[0].StitchLinesMm = "0"
	changed, err := read()
	require.NoError(t, err)
	require.NotEqual(t, source.SourceSHA256, changed.SourceSHA256)
	card.GarmentLabels[0].WidthMm = decimal.NullDecimal{}
	unsized, err := read()
	require.NoError(t, err)
	require.False(t, unsized.GarmentLabels[0].WidthMm.Valid)
	require.Contains(t, string(unsized.CanonicalJSON), `"widthMm":null`)
	card.Seams[2].SideB[0].PieceLineKey = "01JABCDEFGHJKMNPQRSTVWXYZC"
	_, err = read()
	require.ErrorIs(t, err, entity.ErrTechCardSavedAssemblyUnsupported)
	card.Seams[2].SideB[0].PieceLineKey = brand
	card.GarmentLabels = append(card.GarmentLabels, entity.TechCardGarmentLabel{LineKey: completeSourceMainPiece})
	_, err = read()
	require.ErrorIs(t, err, entity.ErrTechCardSavedAssemblyUnsupported)
	card.GarmentLabels = card.GarmentLabels[:len(card.GarmentLabels)-1]
	card.Operations[2].LabelKeys = []string{"01JABCDEFGHJKMNPQRSTVWXYZC"}
	_, err = read()
	require.ErrorIs(t, err, entity.ErrTechCardSavedAssemblyUnsupported)
}

func TestSavedAssemblyCompleteSourceRejectsIncompleteIdentities(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(*entity.TechCard)
	}{
		{"missing seam", func(c *entity.TechCard) { c.Seams = c.Seams[:2] }},
		{"same seam on two steps", func(c *entity.TechCard) { c.Operations[0].SeamKeys = []string{completeSourceSeam} }},
		{"duplicate operation", func(c *entity.TechCard) { c.Operations[1].OperationNumber = c.Operations[0].OperationNumber }},
		{"duplicate seam row", func(c *entity.TechCard) { c.Seams = append(c.Seams, c.Seams[2]) }},
		{"duplicate source row", func(c *entity.TechCard) { c.Patterns = append(c.Patterns, c.Patterns[0]) }},
		{"secondary SHA absent", func(c *entity.TechCard) { c.Patterns[0].SHA256.Valid = false }},
		{"off-card seam", func(c *entity.TechCard) { c.Seams[2].TechCardId++ }},
		{"unscoped anchor or operation piece", func(c *entity.TechCard) { c.PieceDxfAliases = slices.Delete(c.PieceDxfAliases, 1, 2) }},
		{"non-finite anchor", func(c *entity.TechCard) { c.Seams[2].SideB[0].PerimShare = math.Inf(1) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			card, anchor, lines := completeSourceFixture()
			tc.mutate(card)
			_, err := buildSavedAssemblyCompleteSourceV1(card, anchor, lines, completeSourceClock)
			require.ErrorIs(t, err, entity.ErrTechCardSavedAssemblyUnsupported)
		})
	}
	card, anchor, lines := completeSourceFixture()
	anchor.SavedRowRevision++
	_, err := buildSavedAssemblyCompleteSourceV1(card, anchor, lines, completeSourceClock)
	require.ErrorIs(t, err, entity.ErrTechCardSavedAssemblyMoved)
}

func TestSavedAssemblyCompleteSourceRequestIsOptIn(t *testing.T) {
	_, anchor, _ := completeSourceFixture()
	in := entity.SavedAssemblySnapshotRequest{
		ExpectedRow: anchor, Profile: entity.SavedAssemblySnapshotCompleteSourceV1,
		ExpectedServerAssemblyRevision: completeSourceClock,
		ExpectedCompleteSourceSHA256:   strings.Repeat("a", 64),
	}
	require.NoError(t, validateSavedAssemblySnapshotRequest(in))
	for _, invalid := range []func(*entity.SavedAssemblySnapshotRequest){
		func(r *entity.SavedAssemblySnapshotRequest) { r.Profile = 99 },
		func(r *entity.SavedAssemblySnapshotRequest) { r.SelectedOperationNumber = 10 },
		func(r *entity.SavedAssemblySnapshotRequest) { r.ExpectedTopologySHA256 = strings.Repeat("a", 64) },
		func(r *entity.SavedAssemblySnapshotRequest) { r.ExpectedCompleteSourceSHA256 = strings.Repeat("A", 64) },
		func(r *entity.SavedAssemblySnapshotRequest) { r.ExpectedServerAssemblyRevision = "asm:v1:legacy" },
		func(r *entity.SavedAssemblySnapshotRequest) {
			r.ExpectedRow.SizeBytes = savedAssemblyMaxJSSafeInteger + 1
		},
	} {
		bad := in
		invalid(&bad)
		require.Error(t, validateSavedAssemblySnapshotRequest(bad))
	}
	in.Profile = entity.SavedAssemblySnapshotSelected
	in.SelectedOperationNumber = 10
	require.Error(t, validateSavedAssemblySnapshotRequest(in))
	in.ExpectedCompleteSourceSHA256 = ""
	require.NoError(t, validateSavedAssemblySnapshotRequest(in))
}
