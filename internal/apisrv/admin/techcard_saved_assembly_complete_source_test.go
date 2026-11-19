package admin

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"testing"

	"github.com/jekabolt/grbpwr-manager/internal/dependency/mocks"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/jekabolt/grbpwr-manager/internal/patternaccess"
	pb_admin "github.com/jekabolt/grbpwr-manager/proto/gen/admin"
	pb_common "github.com/jekabolt/grbpwr-manager/proto/gen/common"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
	"google.golang.org/genproto/googleapis/rpc/errdetails"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/encoding/protojson"
)

func TestGetTechCardSavedAssemblySnapshotCompleteSourceMintsEveryRow(t *testing.T) {
	for _, tc := range []struct {
		name    string
		missing bool
		folds   bool
	}{
		{name: "whole source"},
		{name: "missing download capability", missing: true},
		{name: "authored fold companion", folds: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			repo := mocks.NewMockRepository(t)
			techCards := mocks.NewMockTechCards(t)
			objects := mocks.NewMockPatternObjects(t)
			repo.EXPECT().TechCards().Return(techCards)
			service, err := patternaccess.New(objects, nil, nil, strings.Repeat("x", 64), "https://backend.example")
			require.NoError(t, err)
			t.Cleanup(service.Stop)

			mainKey := "base/tech-card-patterns/2026/august/main.dxf"
			pocketKey := "base/tech-card-patterns/2026/august/pocket.dxf"
			main := entity.SavedPatternRowIdentity{
				TechCardID: 7, ScopeKey: "main", PatternLineKey: patternSemanticSidecarHandlerKey,
				SavedRowRevision: 5, CardLockVersion: 9, URL: "https://files.grbpwr.com/" + mainKey,
				SizeBytes: 1234, SHA256: strings.Repeat("a", 64), PatternVersion: 4, Filename: "main.dxf",
			}
			pocket := main
			pocket.ScopeKey = "pocketing"
			pocket.PatternLineKey = "01JABCDEFGHJKMNPQRSTVWXYZ1"
			pocket.URL = "https://files.grbpwr.com/" + pocketKey
			pocket.Filename = "pocket.dxf"
			mainObject := entity.PatternObjectAccess{Id: 41, ObjectKey: mainKey}
			access := map[entity.PatternObjectID]entity.PatternObjectAccess{mainObject.ID(): mainObject}
			if !tc.missing {
				pocketObject := entity.PatternObjectAccess{Id: 42, ObjectKey: pocketKey}
				access[pocketObject.ID()] = pocketObject
			}
			objects.EXPECT().EnsureByKeys(mock.Anything, []entity.PatternObjectRef{
				{Key: mainKey, Filename: "main.dxf"}, {Key: pocketKey, Filename: "pocket.dxf"},
			}).Return(access, nil).Once()
			digest := strings.Repeat("b", 64)
			profile := entity.SavedAssemblySnapshotCompleteSourceV1
			var foldSource *entity.SavedAssemblyAuthoredFoldSourceV1
			var selectedSizeID int
			var expectedFoldDigest string
			var selectedSizeInput *int32
			var expectedFoldDigestInput *string
			if tc.folds {
				profile = entity.SavedAssemblySnapshotCompleteSourceWithAuthoredFoldsV1
				selectedSizeID, expectedFoldDigest = 58, strings.Repeat("c", 64)
				sizeID := int32(selectedSizeID)
				selectedSizeInput, expectedFoldDigestInput = &sizeID, &expectedFoldDigest
				foldSource = &entity.SavedAssemblyAuthoredFoldSourceV1{
					CompleteSourceSHA256: digest, SelectedSizeID: selectedSizeID, SelectedSizeToken: "M",
					SourceSHA256: expectedFoldDigest, CanonicalJSON: []byte(`{"folds":[]}`),
				}
			}
			carrierPayload := []byte(`{"prior":"carrier"}`)
			carrierSHA := entity.PatternSemanticSidecarPayloadSHA256(carrierPayload)
			techCards.EXPECT().GetTechCardSavedAssemblySnapshot(mock.Anything, mock.MatchedBy(func(in entity.SavedAssemblySnapshotRequest) bool {
				return in.ExpectedRow.TechCardID == main.TechCardID &&
					in.ExpectedRow.PatternLineKey == main.PatternLineKey &&
					in.SelectedOperationNumber == 0 &&
					in.Profile == profile && in.SelectedSizeID == selectedSizeID &&
					in.ExpectedAuthoredFoldSourceSHA256 == expectedFoldDigest &&
					in.ExpectedCompleteSourceSHA256 == digest && in.ExpectedTopologySHA256 == ""
			})).Return(entity.SavedAssemblySnapshot{
				Row: main,
				Card: &entity.TechCard{TechCardInsert: entity.TechCardInsert{Operations: []entity.TechCardOperation{
					{OperationNumber: sql.NullInt32{Int32: 150, Valid: true}},
				}}},
				ServerAssemblyRevision: "asm:v1:0123456789abcdef0123456789abcdef:9",
				AuthoredFoldSourceV1:   foldSource,
				SemanticSidecar: &entity.PatternSemanticSidecar{
					TechCardId: main.TechCardID, PatternLineKey: main.PatternLineKey,
					CardLockVersion: main.CardLockVersion, SavedRowRevision: main.SavedRowRevision,
					DxfSHA256: main.SHA256, PatternVersion: main.PatternVersion,
					MediaType:  entity.PatternProductCompositeSidecarV4MediaType,
					ByteLength: len(carrierPayload), Payload: carrierPayload, SidecarSHA256: carrierSHA,
				},
				CompleteSourceV1: &entity.SavedAssemblyCompleteSourceV1{
					Rows: []entity.SavedPatternRowIdentity{main, pocket},
					OwnedSeams: []entity.TechCardSeam{{TechCardSeamInput: entity.TechCardSeamInput{
						Kind: entity.TechCardSeamKindPose,
					}}},
					SourceSHA256: digest, CanonicalJSON: []byte(`{"sealed":true}`),
				},
			}, nil)

			server := &Server{repo: repo, patternURLs: service, patternURLsBaseURL: "https://backend.example"}
			res, err := server.GetTechCardSavedAssemblySnapshot(ctx, &pb_admin.GetTechCardSavedAssemblySnapshotRequest{
				TechCardId: 7, PatternLineKey: main.PatternLineKey, ExpectedScopeKey: main.ScopeKey,
				ExpectedSavedRowRevision: main.SavedRowRevision, ExpectedCardLockVersion: int32(main.CardLockVersion),
				ExpectedUrl: main.URL, ExpectedSizeBytes: main.SizeBytes, ExpectedSha256: main.SHA256,
				Profile:                          pb_admin.TechCardSavedAssemblySnapshotProfile(profile).Enum(),
				ExpectedCompleteSourceSha256:     &digest,
				SelectedSizeId:                   selectedSizeInput,
				ExpectedAuthoredFoldSourceSha256: expectedFoldDigestInput,
			})
			if tc.missing {
				require.Equal(t, codes.FailedPrecondition, status.Code(err))
				require.Nil(t, res)
				return
			}
			require.NoError(t, err)
			require.Empty(t, res.GetSelectedSeams())
			require.Empty(t, res.GetAssemblyTopologySha256())
			require.Len(t, res.GetOperations(), 1)
			require.Equal(t, main.PatternLineKey, res.GetSemanticSidecar().GetPatternLineKey())
			require.Equal(t, carrierSHA, res.GetSemanticSidecar().GetSidecarSha256())
			require.Equal(t, carrierPayload, res.GetSemanticSidecar().GetSidecarBytes())
			complete := res.GetCompleteSourceV1()
			require.Len(t, complete.GetRows(), 2)
			require.Equal(t, complete.GetRows()[0].GetDownloadUrl(), res.GetRow().GetDownloadUrl())
			for _, row := range complete.GetRows() {
				require.True(t, strings.HasPrefix(row.GetDownloadUrl(), "https://backend.example/api/p/i"))
				require.True(t, strings.HasSuffix(row.GetDownloadUrl(), "?dl=1"))
			}
			require.NotEqual(t, complete.GetRows()[0].GetDownloadUrl(), complete.GetRows()[1].GetDownloadUrl())
			require.Equal(t, digest, complete.GetSourceSha256())
			require.Equal(t, []byte(`{"sealed":true}`), complete.GetCanonicalJson())
			require.Equal(t, pb_common.TechCardSeamKind_TECH_CARD_SEAM_KIND_POSE, complete.GetOwnedSeams()[0].GetKind())
			require.Empty(t, complete.GetOwnedSeams()[0].GetAnchoredSize())
			if tc.folds {
				folds := res.GetAuthoredFoldSourceV1()
				require.Equal(t, complete.GetSourceSha256(), folds.GetCompleteSourceSha256())
				require.EqualValues(t, selectedSizeID, folds.GetSelectedSizeId())
				require.Equal(t, "M", folds.GetSelectedSizeToken())
				require.Equal(t, expectedFoldDigest, folds.GetSourceSha256())
			} else {
				require.Nil(t, res.GetAuthoredFoldSourceV1())
				wire, err := (protojson.MarshalOptions{EmitUnpopulated: true}).Marshal(res)
				require.NoError(t, err)
				require.NotContains(t, string(wire), "authoredFoldSourceV1")
			}
		})
	}
}

func TestSavedAssemblyAuthoredFoldRefusalPreservesFieldDetail(t *testing.T) {
	domainErr := errors.Join(entity.ErrTechCardSavedAssemblyUnsupported, entity.NewFieldViolation(
		"operations[0].fold_keys[0]", "stale_fold", "fold key", "reconfirm the fold"))
	got := status.Convert(savedAssemblySnapshotError(context.Background(), 7, domainErr))
	require.Equal(t, codes.FailedPrecondition, got.Code())
	require.Contains(t, got.Message(), "stale_fold")
	require.Len(t, got.Details(), 1)
	detail := got.Details()[0].(*errdetails.BadRequest)
	require.Equal(t, "operations[0].fold_keys[0]", detail.GetFieldViolations()[0].GetField())
}
