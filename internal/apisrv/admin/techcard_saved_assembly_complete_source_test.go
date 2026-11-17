package admin

import (
	"context"
	"database/sql"
	"strings"
	"testing"

	"github.com/jekabolt/grbpwr-manager/internal/dependency/mocks"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/jekabolt/grbpwr-manager/internal/patternaccess"
	pb_admin "github.com/jekabolt/grbpwr-manager/proto/gen/admin"
	pb_common "github.com/jekabolt/grbpwr-manager/proto/gen/common"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestGetTechCardSavedAssemblySnapshotCompleteSourceMintsEveryRow(t *testing.T) {
	for _, missing := range []bool{false, true} {
		name := "whole source"
		if missing {
			name = "missing download capability"
		}
		t.Run(name, func(t *testing.T) {
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
			access := map[string]entity.PatternObjectAccess{mainKey: {Id: 41, ObjectKey: mainKey}}
			if !missing {
				access[pocketKey] = entity.PatternObjectAccess{Id: 42, ObjectKey: pocketKey}
			}
			objects.EXPECT().EnsureByKeys(mock.Anything, []entity.PatternObjectRef{
				{Key: mainKey, Filename: "main.dxf"}, {Key: pocketKey, Filename: "pocket.dxf"},
			}).Return(access, nil).Once()
			digest := strings.Repeat("b", 64)
			techCards.EXPECT().GetTechCardSavedAssemblySnapshot(mock.Anything, mock.MatchedBy(func(in entity.SavedAssemblySnapshotRequest) bool {
				return in.ExpectedRow.TechCardID == main.TechCardID &&
					in.ExpectedRow.PatternLineKey == main.PatternLineKey &&
					in.SelectedOperationNumber == 0 &&
					in.Profile == entity.SavedAssemblySnapshotCompleteSourceV1 &&
					in.ExpectedCompleteSourceSHA256 == digest && in.ExpectedTopologySHA256 == ""
			})).Return(entity.SavedAssemblySnapshot{
				Row: main,
				Card: &entity.TechCard{TechCardInsert: entity.TechCardInsert{Operations: []entity.TechCardOperation{
					{OperationNumber: sql.NullInt32{Int32: 150, Valid: true}},
				}}},
				ServerAssemblyRevision: "asm:v1:0123456789abcdef0123456789abcdef:9",
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
				Profile:                      pb_admin.TechCardSavedAssemblySnapshotProfile_TECH_CARD_SAVED_ASSEMBLY_SNAPSHOT_PROFILE_COMPLETE_SOURCE_V1.Enum(),
				ExpectedCompleteSourceSha256: &digest,
			})
			if missing {
				require.Equal(t, codes.FailedPrecondition, status.Code(err))
				require.Nil(t, res)
				return
			}
			require.NoError(t, err)
			require.Empty(t, res.GetSelectedSeams())
			require.Empty(t, res.GetAssemblyTopologySha256())
			require.Len(t, res.GetOperations(), 1)
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
		})
	}
}
