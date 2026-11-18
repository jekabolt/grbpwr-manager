package entity

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func nativePlacementCanonicalV9(t *testing.T, value any) []byte {
	t.Helper()
	raw, err := json.Marshal(value)
	require.NoError(t, err)
	canonical, err := canonicalJSONWithJSNumbers(raw)
	require.NoError(t, err)
	return canonical
}

func nativePlacementComponentV9(kind, media string, payload []byte) compositeComponentV3 {
	return compositeComponentV3{
		Kind: kind, MediaType: media, ByteLength: len(payload),
		SHA256: PatternSemanticSidecarPayloadSHA256(payload), Base64: base64.StdEncoding.EncodeToString(payload),
	}
}

func nativePlacementFixtureV9(t *testing.T) (
	nativeCrossRowPlacementDocumentV9, nativeCrossRowWorldPlacementV1, PatternSemanticSidecarWrite,
	PatternSemanticSidecar, PatternProductNativeCrossRowPlacementCurrentV9,
) {
	t.Helper()
	review, source := nativeCrossRowReviewFixtureV8(t)
	v8 := nativeCrossRowWriteFixtureV8(t, review)
	prior := PatternSemanticSidecar{
		TechCardId: v8.TechCardId, PatternLineKey: v8.PatternLineKey, CardLockVersion: *v8.ExpectedCardLockVersion,
		SavedRowRevision: *v8.ExpectedSavedRowRevision, DxfSHA256: v8.ExpectedDxfSHA256, PatternVersion: v8.ExpectedPatternVersion,
		SidecarSHA256: v8.SidecarSHA256, MediaType: v8.MediaType, ByteLength: len(v8.Payload),
		Payload: bytes.Clone(v8.Payload), PostSaveReceiptSHA256: v8.ExpectedPostSaveReceiptSHA256,
	}
	modelSHA, err := PersistedModelPhysicalInputsSHA256V1(1, Unisex, []ModelMeasurement{{Name: BodyWaist, ValueMM: 740}})
	require.NoError(t, err)
	neutral, err := NeutralPlacementPoseCanonicalSHA256V1()
	require.NoError(t, err)
	model := NeutralPlacementModelAuthorityV1{
		ModelID: 1, ModelUpdatedAt: "2026-11-18T07:21:08.012Z", ModelPhysicalInputsSHA256: modelSHA,
		Pose: NeutralPlacementPoseBindingV1{Kind: NeutralPlacementPoseKindV1, CanonicalSHA256: neutral},
	}
	document := nativeCrossRowPlacementDocumentV9{
		SchemaVersion: patternProductCompositeSidecarDocumentV9, MediaType: PatternProductCompositeSidecarV9MediaType,
		Encoding: patternSemanticSidecarEncoding, Profile: nativeCrossRowPlacementProfileV9, Bindings: review.Bindings,
	}
	document.WriteCAS.ExpectedPreviousSidecarSHA256 = prior.SidecarSHA256
	document.Components[0] = nativePlacementComponentV9("native-cross-row-review-v8", prior.MediaType, prior.Payload)

	world := nativeCrossRowWorldPlacementV1{
		SchemaVersion: nativeCrossRowWorldPlacementDocumentV1, ReviewSHA256: prior.SidecarSHA256,
	}
	world.Body.NeutralPlacementModelAuthorityV1 = model
	world.Body.Observed.BodyPositionsFloat32LeSHA256 = strings.Repeat("1", 64)
	world.Body.Observed.BodyTrianglesUint32LeSHA256 = strings.Repeat("2", 64)
	world.Placement.CenterMm = [3]float64{-7.707, 1010, 220.1}
	world.Placement.DepthOffsetsMm = [2]int{4, 0}
	directed := &world.Placement.DirectedCut
	directed.GridSHA256 = review.Review.GridSHA256
	directed.CarrierMaterialPositionsFloat64LeSHA256 = strings.Repeat("3", 64)
	directed.CarrierTrianglesUint32LeSHA256 = strings.Repeat("4", 64)
	directed.PositionsInvMassFloat32LeSHA256 = strings.Repeat("5", 64)
	for i, member := range review.Selection.Members {
		center := world.Placement.CenterMm
		directed.Pieces[i] = nativeCrossRowPieceWorldPlacementV1{
			PieceID: member.PieceLineKey, OriginMm: [2]float64{25.25 + float64(i), 123.4567},
			CenterM: [3]float64{center[0] / 1000, center[1] / 1000, center[2]/1000 + float64(world.Placement.DepthOffsetsMm[i])/1000},
			Cos:     0.6, Sin: -0.8,
		}
	}
	return document, world, v8, prior, PatternProductNativeCrossRowPlacementCurrentV9{Source: source, Model: model}
}

func nativePlacementWriteV9(t *testing.T, document nativeCrossRowPlacementDocumentV9, v8 PatternSemanticSidecarWrite) PatternSemanticSidecarWrite {
	t.Helper()
	in := v8
	in.MediaType = PatternProductCompositeSidecarV9MediaType
	in.ExpectedPreviousSidecarSHA256 = &document.WriteCAS.ExpectedPreviousSidecarSHA256
	in.Payload = nativePlacementCanonicalV9(t, document)
	in.SidecarSHA256 = PatternSemanticSidecarPayloadSHA256(in.Payload)
	in.ExpectedPostSaveReceiptSHA256 = PatternProductNativeCrossRowPlacementReceiptV9(in.Payload)
	in.By = "local-placement-review"
	return in
}

func nativePlacementWorldComponentV9(t *testing.T, world nativeCrossRowWorldPlacementV1) compositeComponentV3 {
	t.Helper()
	return nativePlacementComponentV9("native-cross-row-world-placement-v1", nativeCrossRowWorldPlacementMediaTypeV1,
		nativePlacementCanonicalV9(t, world))
}

func nativePlacementStoredV9(v9 PatternSemanticSidecarWrite, prior PatternSemanticSidecar) PatternSemanticSidecar {
	stored := prior
	stored.SidecarSHA256, stored.MediaType = v9.SidecarSHA256, v9.MediaType
	stored.Payload, stored.ByteLength = bytes.Clone(v9.Payload), len(v9.Payload)
	stored.PostSaveReceiptSHA256 = v9.ExpectedPostSaveReceiptSHA256
	return stored
}

func TestNativeCrossRowPlacementV9FirstAndColdRead(t *testing.T) {
	document, world, v8, prior, current := nativePlacementFixtureV9(t)
	document.Components[1] = nativePlacementWorldComponentV9(t, world)
	in := nativePlacementWriteV9(t, document, v8)
	binding, err := ValidatePatternProductNativeCrossRowPlacementWriteV9(in)
	require.NoError(t, err)
	require.Equal(t, current.Model, binding)
	require.NoError(t, ValidateFirstPatternProductNativeCrossRowPlacementV9(in, prior, current))
	require.True(t, ValidPatternSemanticSidecarMediaType(in.MediaType))
	require.NoError(t, ValidatePatternSemanticSidecarWrite(&in))

	stored := nativePlacementStoredV9(in, prior)
	binding, err = ReadPatternProductNativeCrossRowPlacementBindingV9(stored)
	require.NoError(t, err)
	require.Equal(t, current.Model, binding)
	require.NoError(t, ValidateStoredPatternProductNativeCrossRowPlacementV9(stored, current))

	current.Model.ModelPhysicalInputsSHA256 = strings.Repeat("6", 64)
	require.ErrorIs(t, ValidateStoredPatternProductNativeCrossRowPlacementV9(stored, current), ErrNeutralPlacementModelMoved)
	require.ErrorIs(t, ValidateFirstPatternProductNativeCrossRowPlacementV9(in, prior, current), ErrTechCardPatternSidecarBindingMoved)
	_, err = ReadPatternProductNativeCrossRowPlacementBindingV9(stored)
	require.NoError(t, err, "structural readback does not make the old model authority current")
	stored.PostSaveReceiptSHA256 = prior.PostSaveReceiptSHA256
	_, err = ReadPatternProductNativeCrossRowPlacementBindingV9(stored)
	require.ErrorIs(t, err, ErrTechCardPatternSidecarCorrupt)
}

func TestNativeCrossRowPlacementV9RefusesWorldSourceAndCanonicalDrift(t *testing.T) {
	for _, tc := range []struct {
		name string
		edit func(*nativeCrossRowPlacementDocumentV9, *nativeCrossRowWorldPlacementV1)
	}{
		{"different outer seal", func(d *nativeCrossRowPlacementDocumentV9, _ *nativeCrossRowWorldPlacementV1) {
			d.Bindings.CompleteSourceSHA256 = strings.Repeat("a", 64)
		}},
		{"grid", func(_ *nativeCrossRowPlacementDocumentV9, w *nativeCrossRowWorldPlacementV1) {
			w.Placement.DirectedCut.GridSHA256 = strings.Repeat("a", 64)
		}},
		{"ordered piece keys", func(_ *nativeCrossRowPlacementDocumentV9, w *nativeCrossRowWorldPlacementV1) {
			p := &w.Placement.DirectedCut.Pieces
			p[0].PieceID, p[1].PieceID = p[1].PieceID, p[0].PieceID
		}},
		{"ordered depth", func(_ *nativeCrossRowPlacementDocumentV9, w *nativeCrossRowWorldPlacementV1) {
			w.Placement.DepthOffsetsMm = [2]int{0, 4}
		}},
		{"exact SI conversion", func(_ *nativeCrossRowPlacementDocumentV9, w *nativeCrossRowWorldPlacementV1) {
			w.Placement.DirectedCut.Pieces[0].CenterM[2] = w.Placement.CenterMm[2]
		}},
		{"bounded center", func(_ *nativeCrossRowPlacementDocumentV9, w *nativeCrossRowWorldPlacementV1) {
			w.Placement.CenterMm[1] = 10000.001
		}},
		{"unit rotation", func(_ *nativeCrossRowPlacementDocumentV9, w *nativeCrossRowWorldPlacementV1) {
			w.Placement.DirectedCut.Pieces[0].Cos = 0.7
			w.Placement.DirectedCut.Pieces[1].Cos = 0.7
		}},
		{"different axes", func(_ *nativeCrossRowPlacementDocumentV9, w *nativeCrossRowWorldPlacementV1) {
			w.Placement.DirectedCut.Pieces[1].Sin *= -1
		}},
		{"unsupported body pose", func(_ *nativeCrossRowPlacementDocumentV9, w *nativeCrossRowWorldPlacementV1) {
			w.Body.Pose.Kind = "stored-direct-v9"
		}},
		{"different neutral pose bytes", func(_ *nativeCrossRowPlacementDocumentV9, w *nativeCrossRowWorldPlacementV1) {
			w.Body.Pose.CanonicalSHA256 = strings.Repeat("a", 64)
		}},
		{"noncanonical timestamp", func(_ *nativeCrossRowPlacementDocumentV9, w *nativeCrossRowWorldPlacementV1) {
			w.Body.ModelUpdatedAt = "2026-11-18T07:21:08.0120Z"
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			document, world, v8, _, _ := nativePlacementFixtureV9(t)
			tc.edit(&document, &world)
			document.Components[1] = nativePlacementWorldComponentV9(t, world)
			_, err := ValidatePatternProductNativeCrossRowPlacementWriteV9(nativePlacementWriteV9(t, document, v8))
			require.Error(t, err)
		})
	}

	document, world, v8, _, _ := nativePlacementFixtureV9(t)
	raw := nativePlacementCanonicalV9(t, world)
	// Valid JSON and matching new component/outer digests cannot mask an omitted
	// zero or a duplicate key. The exact shape is enforced inside the descriptor.
	for _, edited := range [][]byte{
		append([]byte(" "), raw...),
		bytes.Replace(raw, []byte(`"depthOffsetsMm":[4,0]`), []byte(`"depthOffsetsMm":[4]`), 1),
		bytes.Replace(raw, []byte(`"modelId":1`), []byte(`"modelId":1,"modelId":1`), 1),
	} {
		require.NotEqual(t, raw, edited)
		document.Components[1] = nativePlacementComponentV9("native-cross-row-world-placement-v1", nativeCrossRowWorldPlacementMediaTypeV1, edited)
		_, err := ValidatePatternProductNativeCrossRowPlacementWriteV9(nativePlacementWriteV9(t, document, v8))
		require.Error(t, err)
	}

	document.Components[1] = nativePlacementWorldComponentV9(t, world)
	in := nativePlacementWriteV9(t, document, v8)
	in.Payload = append([]byte(" "), in.Payload...)
	in.SidecarSHA256 = PatternSemanticSidecarPayloadSHA256(in.Payload)
	in.ExpectedPostSaveReceiptSHA256 = PatternProductNativeCrossRowPlacementReceiptV9(in.Payload)
	_, err := ValidatePatternProductNativeCrossRowPlacementWriteV9(in)
	require.Error(t, err, "whole JSON must also be canonical")
}

func TestNativeCrossRowPlacementV9RefusesSubstitutedPriorAndMovedSources(t *testing.T) {
	document, world, v8, prior, current := nativePlacementFixtureV9(t)
	document.Components[1] = nativePlacementWorldComponentV9(t, world)
	in := nativePlacementWriteV9(t, document, v8)
	require.ErrorIs(t, ValidateFirstPatternProductNativeCrossRowPlacementV9(in, PatternSemanticSidecar{}, current), ErrTechCardPatternSidecarBindingMoved)
	require.ErrorIs(t, ValidateFirstPatternProductNativeCrossRowPlacementV9(in, nativePlacementStoredV9(in, prior), current), ErrTechCardPatternSidecarBindingMoved)
	stale := prior
	stale.SavedRowRevision++
	require.ErrorIs(t, ValidateFirstPatternProductNativeCrossRowPlacementV9(in, stale, current), ErrTechCardPatternSidecarCorrupt)
	inBad := in
	inBad.ExpectedPreviousSidecarSHA256 = nil
	_, err := ValidatePatternProductNativeCrossRowPlacementWriteV9(inBad)
	require.Error(t, err)
	inBad = in
	inBad.ExpectedAssemblyTopologySHA256 = &in.SidecarSHA256
	_, err = ValidatePatternProductNativeCrossRowPlacementWriteV9(inBad)
	require.Error(t, err)

	// Rebuild an otherwise valid V9 around a different canonical V8 document.
	// The outer CAS and both descriptors attest to its bytes, but it never was
	// the locked prior on this MAIN row.
	alternate, _ := nativeCrossRowReviewFixtureV8(t)
	alternate.Review.BoundSourceRequestSHA256 = strings.Repeat("9", 64)
	altV8 := nativeCrossRowWriteFixtureV8(t, alternate)
	document.Components[0] = nativePlacementComponentV9("native-cross-row-review-v8", altV8.MediaType, altV8.Payload)
	document.WriteCAS.ExpectedPreviousSidecarSHA256 = altV8.SidecarSHA256
	world.ReviewSHA256 = altV8.SidecarSHA256
	document.Components[1] = nativePlacementWorldComponentV9(t, world)
	forged := nativePlacementWriteV9(t, document, v8)
	_, err = ValidatePatternProductNativeCrossRowPlacementWriteV9(forged)
	require.NoError(t, err)
	require.ErrorIs(t, ValidateFirstPatternProductNativeCrossRowPlacementV9(forged, prior, current), ErrTechCardPatternSidecarBindingMoved)

	stored := nativePlacementStoredV9(in, prior)
	current.Source.Source.Rows[1].SavedRowRevision++
	require.ErrorIs(t, ValidateFirstPatternProductNativeCrossRowPlacementV9(in, prior, current), ErrTechCardPatternSidecarBindingMoved)
	require.ErrorIs(t, ValidateStoredPatternProductNativeCrossRowPlacementV9(stored, current), ErrTechCardPatternSidecarBindingMoved)
	current.Source.Source.Rows[1].SavedRowRevision--
	current.Source.Card.SizeIds = []int{83, 85}
	require.ErrorIs(t, ValidateStoredPatternProductNativeCrossRowPlacementV9(stored, current), ErrTechCardPatternSidecarBindingMoved)
}
