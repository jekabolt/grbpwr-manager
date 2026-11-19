package entity

import (
	"bytes"
	"encoding/base64"
	"encoding/binary"
	"math"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func nativeWholePositionFixtureV10(layout *nativeWholeWorldLayoutV1, xyz []byte) {
	layout.Positions.Encoding = "float32-le-xyz"
	layout.Positions.ByteLength = len(xyz)
	layout.Positions.SHA256 = PatternSemanticSidecarPayloadSHA256(xyz)
	layout.Positions.Base64 = base64.StdEncoding.EncodeToString(xyz)
}

func nativeWholePlacementFixtureV10(t *testing.T, retained, previous PatternSemanticSidecar, body neutralPlacementObservedBodyV1) (
	nativeWholePlacementDocumentV10, nativeWholeWorldPlacementV1,
) {
	t.Helper()
	var review *patternProductCompositeSidecarV8Review
	if retained.MediaType == PatternProductCompositeSidecarV8MediaType {
		var err error
		review, err = decodeNativeCrossRowReviewV8(retained.Payload)
		require.NoError(t, err)
	} else {
		v9, err := decodeNativeCrossRowPlacementV9(retained.Payload)
		require.NoError(t, err)
		review = v9.review
	}
	document := nativeWholePlacementDocumentV10{
		SchemaVersion: patternProductCompositeSidecarDocumentV10,
		MediaType:     PatternProductCompositeSidecarV10MediaType,
		Encoding:      patternSemanticSidecarEncoding, Profile: nativeWholePlacementProfileV10,
		Bindings: review.Bindings,
	}
	document.WriteCAS.ExpectedPreviousSidecarSHA256 = previous.SidecarSHA256
	document.Components[0] = nativePlacementComponentV9(retainedNativeCrossRowKindV10, retained.MediaType, retained.Payload)
	world := nativeWholeWorldPlacementV1{
		SchemaVersion: "tech-card-complete-source-native-whole-world-placement/1", RetainedCarrierSHA256: retained.SidecarSHA256, Body: body,
		Layout: nativeWholeWorldLayoutV1{
			SizeID: review.Selection.SizeID, SizeToken: review.Selection.SizeToken,
			RuntimeCandidateSHA256: strings.Repeat("a", 64), CutGraphSHA256: strings.Repeat("b", 64),
			PieceRanges: []nativeWholeWorldPieceRangeV1{
				{PieceID: review.Selection.Members[0].PieceLineKey, VertexCount: 3, TriangleCount: 1},
				{PieceID: review.Selection.Members[1].PieceLineKey, VertexOffset: 3, VertexCount: 3, TriangleOffset: 1, TriangleCount: 1},
			},
		},
	}
	xyz := make([]byte, 6*3*4)
	binary.LittleEndian.PutUint32(xyz[4:8], math.Float32bits(1.25))
	nativeWholePositionFixtureV10(&world.Layout, xyz)
	return document, world
}

func nativeWholePlacementWriteV10(t *testing.T, document nativeWholePlacementDocumentV10, world nativeWholeWorldPlacementV1, template PatternSemanticSidecarWrite) PatternSemanticSidecarWrite {
	t.Helper()
	document.Components[1] = nativePlacementComponentV9(nativeWholeWorldPlacementKindV10, nativeWholeWorldPlacementMediaTypeV1,
		nativePlacementCanonicalV9(t, world))
	template.ExpectedPreviousSidecarSHA256 = &document.WriteCAS.ExpectedPreviousSidecarSHA256
	template.MediaType = PatternProductCompositeSidecarV10MediaType
	template.By = "whole-placement-test"
	template.Payload = nativePlacementCanonicalV9(t, document)
	template.SidecarSHA256 = PatternSemanticSidecarPayloadSHA256(template.Payload)
	template.ExpectedPostSaveReceiptSHA256 = PatternProductNativeWholePlacementReceiptV10(template.Payload)
	return template
}

func TestNativeWholePlacementV10LockedTransitionsAndColdRead(t *testing.T) {
	v9doc, v9world, template, v8, current := nativePlacementFixtureV9(t)
	v9doc.Components[1] = nativePlacementWorldComponentV9(t, v9world)
	v9 := nativePlacementStoredV9(nativePlacementWriteV9(t, v9doc, template), v8)
	for _, original := range []PatternSemanticSidecar{v8, v9} {
		t.Run(original.MediaType, func(t *testing.T) {
			body := v9world.Body
			body.ModelID++
			document, world := nativeWholePlacementFixtureV10(t, original, original, body)
			first := nativeWholePlacementWriteV10(t, document, world, template)
			require.NoError(t, ValidatePatternSemanticSidecarWrite(&first))
			model, err := CheckPatternProductNativeWholePlacementTransitionV10(first, original)
			require.NoError(t, err)
			require.Equal(t, body.NeutralPlacementModelAuthorityV1, model)
			require.NoError(t, ValidatePatternProductNativeWholePlacementWriteCurrentV10(first, current.Source, model))
			require.ErrorIs(t, ValidatePatternProductNativeWholePlacementWriteCurrentV10(first, current.Source, current.Model), ErrNeutralPlacementModelMoved)

			stored := nativePlacementStoredV9(first, original)
			parsed, err := readNativeWholePlacementBindingV10(stored)
			require.NoError(t, err)
			require.Equal(t, original.Payload, parsed.retainedBytes)
			require.NoError(t, ValidateStoredPatternProductNativeWholePlacementV10(stored, current.Source, model))

			stale := current.Source
			stale.ServerAssemblyRevision += "1"
			require.ErrorIs(t, ValidateStoredPatternProductNativeWholePlacementV10(stored, stale, model), ErrTechCardPatternSidecarBindingMoved)
			drift := model
			drift.ModelPhysicalInputsSHA256 = strings.Repeat("5", 64)
			require.ErrorIs(t, ValidateStoredPatternProductNativeWholePlacementV10(stored, current.Source, drift), ErrNeutralPlacementModelMoved)
			corrupt := stored
			corrupt.PostSaveReceiptSHA256 = strings.Repeat("f", 64)
			_, err = ReadPatternProductNativeWholePlacementBindingV10(corrupt)
			require.ErrorIs(t, err, ErrTechCardPatternSidecarCorrupt)

			document.WriteCAS.ExpectedPreviousSidecarSHA256 = stored.SidecarSHA256
			xyz, err := base64.StdEncoding.DecodeString(world.Layout.Positions.Base64)
			require.NoError(t, err)
			binary.LittleEndian.PutUint32(xyz[:4], math.Float32bits(-0.25))
			nativeWholePositionFixtureV10(&world.Layout, xyz)
			edit := nativeWholePlacementWriteV10(t, document, world, template)
			_, err = CheckPatternProductNativeWholePlacementTransitionV10(edit, stored)
			require.NoError(t, err)
			require.NotEqual(t, first.SidecarSHA256, edit.SidecarSHA256)

			_, err = CheckPatternProductNativeWholePlacementTransitionV10(edit, original)
			require.ErrorIs(t, err, ErrTechCardPatternSidecarBindingMoved)
			_, err = CheckPatternProductNativeWholePlacementTransitionV10(first, stored)
			require.ErrorIs(t, err, ErrTechCardPatternSidecarBindingMoved)

			other := v9
			if original.MediaType == v9.MediaType {
				other = v8
			}
			replace, replacementWorld := nativeWholePlacementFixtureV10(t, other, stored, body)
			_, err = CheckPatternProductNativeWholePlacementTransitionV10(nativeWholePlacementWriteV10(t, replace, replacementWorld, template), stored)
			require.ErrorContains(t, err, "same original carrier bytes")
			_, err = CheckPatternProductNativeCrossRowPlacementTransitionV9(
				nativePlacementWriteV9(t, v9doc, template), stored)
			require.ErrorIs(t, err, ErrTechCardPatternSidecarBindingMoved)
		})
	}
}

func TestNativeWholePlacementV10VertexCapacity(t *testing.T) {
	_, v9world, template, v8, current := nativePlacementFixtureV9(t)
	for _, test := range []struct {
		name        string
		vertexCount int
	}{
		{"continuous candidate", 2082},
		{"maximum", 2304},
	} {
		t.Run(test.name, func(t *testing.T) {
			document, world := nativeWholePlacementFixtureV10(t, v8, v8, v9world.Body)
			last := &world.Layout.PieceRanges[1]
			last.VertexCount = test.vertexCount - last.VertexOffset
			xyz := make([]byte, test.vertexCount*3*4)
			for vertex := range test.vertexCount {
				offset := vertex * 3 * 4
				binary.LittleEndian.PutUint32(xyz[offset:], math.Float32bits(float32(vertex)/1024))
			}
			nativeWholePositionFixtureV10(&world.Layout, xyz)
			write := nativeWholePlacementWriteV10(t, document, world, template)
			require.NoError(t, ValidatePatternSemanticSidecarWrite(&write))

			model, err := CheckPatternProductNativeWholePlacementTransitionV10(write, v8)
			require.NoError(t, err)
			require.NoError(t, ValidatePatternProductNativeWholePlacementWriteCurrentV10(write, current.Source, model))

			stored := nativePlacementStoredV9(write, v8)
			require.NoError(t, ValidateStoredPatternProductNativeWholePlacementV10(stored, current.Source, model))
			decoded, err := readNativeWholePlacementBindingV10(stored)
			require.NoError(t, err)
			require.Equal(t, world.Layout, decoded.world.Layout)
		})
	}
}

func TestNativeWholePlacementV10RejectsNonExactLayout(t *testing.T) {
	_, v9world, template, v8, _ := nativePlacementFixtureV9(t)
	tests := []struct {
		name string
		edit func(*nativeWholePlacementDocumentV10, *nativeWholeWorldPlacementV1)
		want string
	}{
		{"duplicate piece", func(_ *nativeWholePlacementDocumentV10, w *nativeWholeWorldPlacementV1) {
			w.Layout.PieceRanges[1].PieceID = w.Layout.PieceRanges[0].PieceID
		}, "unique"},
		{"id outside ULID", func(_ *nativeWholePlacementDocumentV10, w *nativeWholeWorldPlacementV1) {
			w.Layout.PieceRanges[1].PieceID = "Z" + w.Layout.PieceRanges[1].PieceID[1:]
		}, "canonical ULIDs"},
		{"too few pieces", func(_ *nativeWholePlacementDocumentV10, w *nativeWholeWorldPlacementV1) {
			w.Layout.PieceRanges = w.Layout.PieceRanges[:1]
		}, "two bounded"},
		{"vertex gap", func(_ *nativeWholePlacementDocumentV10, w *nativeWholeWorldPlacementV1) {
			w.Layout.PieceRanges[1].VertexOffset++
		}, "contiguous"},
		{"triangle gap", func(_ *nativeWholePlacementDocumentV10, w *nativeWholeWorldPlacementV1) {
			w.Layout.PieceRanges[1].TriangleOffset++
		}, "contiguous"},
		{"negative vertex count", func(_ *nativeWholePlacementDocumentV10, w *nativeWholeWorldPlacementV1) {
			w.Layout.PieceRanges[1].VertexCount = -1
		}, "positive"},
		{"vertex bound", func(_ *nativeWholePlacementDocumentV10, w *nativeWholeWorldPlacementV1) {
			const vertices = 2305
			last := &w.Layout.PieceRanges[1]
			last.VertexCount = vertices - last.VertexOffset
			nativeWholePositionFixtureV10(&w.Layout, make([]byte, vertices*3*4))
		}, "bounded"},
		{"triangle bound", func(_ *nativeWholePlacementDocumentV10, w *nativeWholeWorldPlacementV1) {
			w.Layout.PieceRanges[1].TriangleCount = 5000
		}, "bounded"},
		{"wrong bytes count", func(_ *nativeWholePlacementDocumentV10, w *nativeWholeWorldPlacementV1) {
			w.Layout.Positions.ByteLength--
		}, "three Float32"},
		{"wrong digest", func(_ *nativeWholePlacementDocumentV10, w *nativeWholeWorldPlacementV1) {
			w.Layout.Positions.SHA256 = strings.Repeat("f", 64)
		}, "position bytes or digest"},
		{"wrong base64", func(_ *nativeWholePlacementDocumentV10, w *nativeWholeWorldPlacementV1) {
			w.Layout.Positions.Base64 += "\n"
		}, "position bytes or digest"},
		{"wrong encoding", func(_ *nativeWholePlacementDocumentV10, w *nativeWholeWorldPlacementV1) {
			w.Layout.Positions.Encoding = "float32-be-xyz"
		}, "three Float32"},
		{"wrong size ID", func(_ *nativeWholePlacementDocumentV10, w *nativeWholeWorldPlacementV1) { w.Layout.SizeID++ }, "reviewed size"},
		{"wrong size token", func(_ *nativeWholePlacementDocumentV10, w *nativeWholeWorldPlacementV1) { w.Layout.SizeToken = "XL" }, "reviewed size"},
		{"wrong neutral pose", func(_ *nativeWholePlacementDocumentV10, w *nativeWholeWorldPlacementV1) {
			w.Body.Pose.CanonicalSHA256 = strings.Repeat("0", 64)
		}, "exact neutral pose"},
		{"exact UTC precision", func(_ *nativeWholePlacementDocumentV10, w *nativeWholeWorldPlacementV1) {
			w.Body.ModelUpdatedAt = "2026-11-18T07:21:08.01Z"
		}, "exact neutral pose"},
		{"wrong Complete seal", func(d *nativeWholePlacementDocumentV10, _ *nativeWholeWorldPlacementV1) {
			d.Bindings.CompleteSourceSHA256 = strings.Repeat("0", 64)
		}, "Complete Source binding"},
		{"wrong retained digest", func(_ *nativeWholePlacementDocumentV10, w *nativeWholeWorldPlacementV1) {
			w.RetainedCarrierSHA256 = strings.Repeat("0", 64)
		}, "exact carrier"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			doc, world := nativeWholePlacementFixtureV10(t, v8, v8, v9world.Body)
			test.edit(&doc, &world)
			in := nativeWholePlacementWriteV10(t, doc, world, template)
			require.ErrorContains(t, ValidatePatternSemanticSidecarWrite(&in), test.want)
		})
	}

	for _, bits := range []uint32{0x80000000, 0x7f800000, 0xff800000, 0x7fc00000, math.Float32bits(10.0001)} {
		doc, world := nativeWholePlacementFixtureV10(t, v8, v8, v9world.Body)
		xyz, err := base64.StdEncoding.DecodeString(world.Layout.Positions.Base64)
		require.NoError(t, err)
		binary.LittleEndian.PutUint32(xyz, bits)
		nativeWholePositionFixtureV10(&world.Layout, xyz)
		in := nativeWholePlacementWriteV10(t, doc, world, template)
		require.ErrorContains(t, ValidatePatternSemanticSidecarWrite(&in), "within 10 metres")
	}
	doc, world := nativeWholePlacementFixtureV10(t, v8, v8, v9world.Body)
	in := nativeWholePlacementWriteV10(t, doc, world, template)
	duplicate := bytes.Replace(in.Payload, []byte(`"encoding":"canonical-json-utf8"`), []byte(`"encoding":"canonical-json-utf8","encoding":"canonical-json-utf8"`), 1)
	_, err := decodeNativeWholePlacementV10(duplicate)
	require.ErrorContains(t, err, "exact canonical V10 shape")
	_, err = decodeNativeWholePlacementV10(append(in.Payload, '\n'))
	require.ErrorContains(t, err, "exact canonical V10 shape")
	wrongReceipt := in
	wrongReceipt.ExpectedPostSaveReceiptSHA256 = PatternProductNativeCrossRowPlacementReceiptV9(in.Payload)
	require.ErrorContains(t, ValidatePatternSemanticSidecarWrite(&wrongReceipt), "receipt differs")
}

func TestNativeWholePlacementV10RebuildsFromOlderComplete(t *testing.T) {
	_, v9world, template, v8, current := nativePlacementFixtureV9(t)
	initialDoc, initialWorld := nativeWholePlacementFixtureV10(t, v8, v8, v9world.Body)
	initial := nativeWholePlacementWriteV10(t, initialDoc, initialWorld, template)
	prior := nativePlacementStoredV9(initial, v8)

	same, sameWorld := nativeWholePlacementFixtureV10(t, v8, prior, v9world.Body)
	same.WriteCAS.RebuildStaleComplete = true
	_, err := CheckPatternProductNativeWholePlacementTransitionV10(nativeWholePlacementWriteV10(t, same, sameWorld, template), prior)
	require.ErrorContains(t, err, "newer Assembly source")

	review, err := decodeNativeCrossRowReviewV8(v8.Payload)
	require.NoError(t, err)
	review.Bindings.AnchorRow.CardLockVersion++
	review.Bindings.ServerAssemblyRevision = strings.TrimSuffix(review.Bindings.ServerAssemblyRevision, ":3") + ":4"
	review.Bindings.CompleteSourceSHA256 = strings.Repeat("e", 64)
	for i := range review.Selection.Members {
		review.Selection.Members[i].Row.CardLockVersion++
	}
	newTemplate := nativeCrossRowWriteFixtureV8(t, review)
	reviewBytes := newTemplate.Payload
	freshV8 := nativePlacementStoredV9(newTemplate, v8)
	freshV8.CardLockVersion++
	require.NotEqual(t, v8.Payload, reviewBytes)
	fresh := current.Source
	fresh.AnchorRow.CardLockVersion++
	fresh.ServerAssemblyRevision = review.Bindings.ServerAssemblyRevision
	card := *fresh.Card
	card.LockVersion++
	fresh.Card = &card
	source := *fresh.Source
	source.SourceSHA256 = review.Bindings.CompleteSourceSHA256
	source.Rows = append([]SavedPatternRowIdentity(nil), source.Rows...)
	for i := range source.Rows {
		source.Rows[i].CardLockVersion++
	}
	fresh.Source = &source

	doc, world := nativeWholePlacementFixtureV10(t, freshV8, prior, v9world.Body)
	implicit := nativeWholePlacementWriteV10(t, doc, world, newTemplate)
	_, err = CheckPatternProductNativeWholePlacementTransitionV10(implicit, prior)
	require.ErrorContains(t, err, "same original carrier bytes")
	doc.WriteCAS.RebuildStaleComplete = true
	explicit := nativeWholePlacementWriteV10(t, doc, world, newTemplate)
	model, err := CheckPatternProductNativeWholePlacementTransitionV10(explicit, prior)
	require.NoError(t, err)
	require.NoError(t, ValidatePatternProductNativeWholePlacementWriteCurrentV10(explicit, fresh, model))
	require.ErrorIs(t, ValidatePatternProductNativeWholePlacementWriteCurrentV10(explicit, current.Source, model), ErrTechCardPatternSidecarBindingMoved)
	saved := nativePlacementStoredV9(explicit, freshV8)
	require.NoError(t, ValidateStoredPatternProductNativeWholePlacementV10(saved, fresh, model))
	_, err = CheckPatternProductNativeWholePlacementTransitionV10(explicit, saved)
	require.ErrorIs(t, err, ErrTechCardPatternSidecarBindingMoved)

	// A normal position edit can still wrap the exact rebuilt review once it is current.
	doc.WriteCAS.RebuildStaleComplete = false
	doc.WriteCAS.ExpectedPreviousSidecarSHA256 = saved.SidecarSHA256
	_, err = CheckPatternProductNativeWholePlacementTransitionV10(nativeWholePlacementWriteV10(t, doc, world, newTemplate), saved)
	require.NoError(t, err)
}
