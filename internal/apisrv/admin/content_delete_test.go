package admin

import (
	"errors"
	"testing"

	mocks "github.com/jekabolt/grbpwr-manager/internal/dependency/mocks"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	pb_admin "github.com/jekabolt/grbpwr-manager/proto/gen/admin"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// Pure handler tests: every collaborator is a strict mockery mock, nothing opens a database.
// A call the test did not EXPECT fails it — that is how "S3 is not touched" and "the hero is not
// refreshed" are asserted below, without a single negative assertion spelled out.

type delFixture struct {
	s     *Server
	repo  *mocks.MockRepository
	media *mocks.MockMedia
	hero  *mocks.MockHero
	fs    *mocks.MockFileStore
}

func newDelFixture(t *testing.T) delFixture {
	t.Helper()
	f := delFixture{
		repo:  mocks.NewMockRepository(t),
		media: mocks.NewMockMedia(t),
		hero:  mocks.NewMockHero(t),
		fs:    mocks.NewMockFileStore(t),
	}
	f.repo.EXPECT().Media().Return(f.media).Maybe()
	f.repo.EXPECT().Hero().Return(f.hero).Maybe()
	f.s = &Server{repo: f.repo, bucket: f.fs}
	return f
}

func delMediaRow(id int) *entity.MediaFull {
	return &entity.MediaFull{Id: id, MediaItem: entity.MediaItem{
		FullSizeMediaURL:   "https://cdn/og.webp",
		CompressedMediaURL: "https://cdn/c.webp",
		ThumbnailMediaURL:  "https://cdn/t.webp",
	}}
}

func TestDeleteFromBucketRefusesMediaInUseAndKeepsItsObjects(t *testing.T) {
	f := newDelFixture(t)
	f.media.EXPECT().GetMediaById(mock.Anything, 1290).Return(delMediaRow(1290), nil).Once()
	f.media.EXPECT().DeleteMediaByIdIfUnused(mock.Anything, 1290).Return(false, []entity.MediaUsageRef{
		{MediaId: 1290, Kind: "product", EntityId: 412, Label: "Black Coat", Slot: "thumbnail"},
		{MediaId: 1290, Kind: "product", EntityId: 412, Label: "Black Coat", Slot: "gallery"},
		{MediaId: 1290, Kind: "tech_card", EntityId: 88, Label: "TC-01", Slot: "callout"},
	}, nil).Once()

	_, err := f.s.DeleteFromBucket(t.Context(), &pb_admin.DeleteFromBucketRequest{Id: 1290})
	st, ok := status.FromError(err)
	require.True(t, ok, "a gRPC status, not a bare error")
	require.Equal(t, codes.FailedPrecondition, st.Code())
	require.Equal(t, "media is in use: product Black Coat, tech card TC-01", st.Message())
	// No DeleteObjects / RefreshHero expectation: the strict mocks fail the test if either runs.
}

func TestDeleteFromBucketDeletesFreeMediaThenItsObjectsThenRefreshesHero(t *testing.T) {
	f := newDelFixture(t)
	f.media.EXPECT().GetMediaById(mock.Anything, 7).Return(delMediaRow(7), nil).Once()
	f.media.EXPECT().DeleteMediaByIdIfUnused(mock.Anything, 7).Return(true, nil, nil).Once()
	f.fs.EXPECT().DeleteObjects(mock.Anything, "https://cdn/og.webp", "https://cdn/c.webp", "https://cdn/t.webp").
		Return(nil).Once()
	f.hero.EXPECT().RefreshHero(mock.Anything).Return(nil).Once()

	resp, err := f.s.DeleteFromBucket(t.Context(), &pb_admin.DeleteFromBucketRequest{Id: 7})
	require.NoError(t, err)
	require.NotNil(t, resp)
}

func TestDeleteFromBucketMissingRowIsAHarmlessSuccess(t *testing.T) {
	// The row is already gone: nothing to load, nothing in S3 we can name, and the store calls a
	// missing row deleted. A repeated click must not turn into an error.
	f := newDelFixture(t)
	f.media.EXPECT().GetMediaById(mock.Anything, 9).Return(nil, errors.New("not found")).Once()
	f.media.EXPECT().DeleteMediaByIdIfUnused(mock.Anything, 9).Return(true, nil, nil).Once()
	f.hero.EXPECT().RefreshHero(mock.Anything).Return(nil).Once()

	_, err := f.s.DeleteFromBucket(t.Context(), &pb_admin.DeleteFromBucketRequest{Id: 9})
	require.NoError(t, err)
}

func TestDeleteFromBucketForeignKeyBackstopIsFailedPrecondition(t *testing.T) {
	f := newDelFixture(t)
	fkErr := errors.New("Error 1451: Cannot delete or update a parent row")
	f.media.EXPECT().GetMediaById(mock.Anything, 5).Return(delMediaRow(5), nil).Once()
	f.media.EXPECT().DeleteMediaByIdIfUnused(mock.Anything, 5).Return(false, nil, fkErr).Once()
	f.repo.EXPECT().IsErrForeignKeyViolation(fkErr).Return(true).Once()

	_, err := f.s.DeleteFromBucket(t.Context(), &pb_admin.DeleteFromBucketRequest{Id: 5})
	require.Equal(t, codes.FailedPrecondition, status.Code(err))
	require.Contains(t, status.Convert(err).Message(), mediaInUsePrefix)
}

func TestDeleteFromBucketStoreFailureIsInternalAndKeepsObjects(t *testing.T) {
	f := newDelFixture(t)
	boom := errors.New("connection reset")
	f.media.EXPECT().GetMediaById(mock.Anything, 5).Return(delMediaRow(5), nil).Once()
	f.media.EXPECT().DeleteMediaByIdIfUnused(mock.Anything, 5).Return(false, nil, boom).Once()
	f.repo.EXPECT().IsErrForeignKeyViolation(boom).Return(false).Once()

	_, err := f.s.DeleteFromBucket(t.Context(), &pb_admin.DeleteFromBucketRequest{Id: 5})
	require.Equal(t, codes.Internal, status.Code(err))
}

func TestDeleteFromBucketRejectsNonPositiveId(t *testing.T) {
	f := newDelFixture(t)
	for _, id := range []int32{0, -3} {
		_, err := f.s.DeleteFromBucket(t.Context(), &pb_admin.DeleteFromBucketRequest{Id: id})
		require.Equal(t, codes.InvalidArgument, status.Code(err), "id %d", id)
	}
}

func TestMediaInUseMessage(t *testing.T) {
	cases := []struct {
		name string
		refs []entity.MediaUsageRef
		want string
	}{
		{name: "no refs still reads as in use", want: "media is in use"},
		{
			name: "one entity in several slots is named once",
			refs: []entity.MediaUsageRef{
				{Kind: "tech_card", EntityId: 88, Label: "TC-01", Slot: "front"},
				{Kind: "tech_card", EntityId: 88, Label: "TC-01", Slot: "callout"},
			},
			want: "media is in use: tech card TC-01",
		},
		{
			name: "fallback labels that already carry the kind are not prefixed twice",
			refs: []entity.MediaUsageRef{
				{Kind: "product", EntityId: 412, Label: "product 412"},
				{Kind: "sample", EntityId: 3, Label: "TC-01 sample 3"},
				{Kind: "model", EntityId: 2, Label: ""},
			},
			want: "media is in use: product 412, TC-01 sample 3, model 2",
		},
		{
			name: "beyond the cap the rest is counted, not listed",
			refs: []entity.MediaUsageRef{
				{Kind: "product", EntityId: 1, Label: "A"},
				{Kind: "product", EntityId: 2, Label: "B"},
				{Kind: "design_picture", EntityId: 3, Label: "TC-02"},
				{Kind: "fitting", EntityId: 4, Label: "TC-02 round 1"},
				{Kind: "archive", EntityId: 5, Label: "SS26"},
			},
			want: "media is in use: product A, product B, design picture TC-02 +2 more",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			require.Equal(t, tc.want, mediaInUseMessage(tc.refs))
		})
	}
}
