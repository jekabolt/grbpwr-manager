package admin

import (
	"database/sql"
	"errors"
	"fmt"
	"testing"

	"github.com/jekabolt/grbpwr-manager/internal/dependency/mocks"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	pb_admin "github.com/jekabolt/grbpwr-manager/proto/gen/admin"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
)

// ═════ DeleteDesignPicture (O-68, D-74): the rows go in the store, the files go here ═════
//
// Strict mocks. The store's own transaction (branch walk, layers, children-first delete) is the
// store's business and lives on a database; what is judged here is the handler's half — the refusal
// mapping and the per-media verdict after the commit.

type deleteRig struct {
	s      *Server
	design *mocks.MockDesign
	media  *mocks.MockMedia
	files  *mocks.MockFileStore
}

func newDeleteRig(t *testing.T) *deleteRig {
	t.Helper()
	r := &deleteRig{design: mocks.NewMockDesign(t), media: mocks.NewMockMedia(t), files: mocks.NewMockFileStore(t)}
	repo := mocks.NewMockRepository(t)
	repo.EXPECT().Design().Return(r.design).Maybe()
	repo.EXPECT().Media().Return(r.media).Maybe()
	r.s = &Server{repo: repo, bucket: r.files}
	return r
}

func deleteMedia(id int) entity.MediaFull {
	return entity.MediaFull{Id: id, MediaItem: entity.MediaItem{
		FullSizeMediaURL:   fmt.Sprintf("https://files.grbpwr.test/design/%d-og.png", id),
		CompressedMediaURL: fmt.Sprintf("https://files.grbpwr.test/design/%d-c.webp", id),
		ThumbnailMediaURL:  fmt.Sprintf("https://files.grbpwr.test/design/%d-t.webp", id),
	}}
}

// A ROOT IS REFUSED picture_is_root, FailedPrecondition. MUTATION: drop the table row — Internal.
func TestDeleteDesignPictureRefusesARoot(t *testing.T) {
	r := newDeleteRig(t)
	r.design.EXPECT().DeletePicture(mock.Anything, 7, "designer").
		Return(nil, fmt.Errorf("%w: design picture 7", entity.ErrDesignPictureIsRoot)).Once()
	_, err := r.s.DeleteDesignPicture(designRunCtx(), &pb_admin.DeleteDesignPictureRequest{PictureId: 7})
	code, md := errorReason(t, err)
	require.Equal(t, codes.FailedPrecondition, code)
	require.Equal(t, "picture_is_root", md["reason"])
}

// AN UNKNOWN ID IS picture_not_found, NotFound — its own token, not the band's not_found.
func TestDeleteDesignPictureRefusesAnUnknownPicture(t *testing.T) {
	r := newDeleteRig(t)
	r.design.EXPECT().DeletePicture(mock.Anything, 7, "designer").
		Return(nil, fmt.Errorf("%w: design picture 7", entity.ErrDesignPictureNotFound)).Once()
	_, err := r.s.DeleteDesignPicture(designRunCtx(), &pb_admin.DeleteDesignPictureRequest{PictureId: 7})
	code, md := errorReason(t, err)
	require.Equal(t, codes.NotFound, code)
	require.Equal(t, "picture_not_found", md["reason"])
}

// A CROP WITH TWO EDITS: three picture ids in the store's order; a media that nobody else holds
// goes with all three of its objects; a media something else still holds is kept and its objects
// are not touched; a media whose objects could not be removed is kept too.
//
// MUTATIONS: skip DeleteObjects after a successful row delete (files expects it); put a kept media
// into deleted_media_ids; call DeleteObjects for the referenced media (strict mock).
func TestDeleteDesignPictureDropsTheMediaAfterTheRows(t *testing.T) {
	r := newDeleteRig(t)
	res := &entity.DesignPictureDeletion{
		PictureIds: []int{31, 30, 12},
		MediaIds:   []int{931, 930, 912},
		Media:      map[int]entity.MediaFull{931: deleteMedia(931), 930: deleteMedia(930), 912: deleteMedia(912)},
	}
	r.design.EXPECT().DeletePicture(mock.Anything, 12, "designer").Return(res, nil).Once()
	// 931: free — row and objects go.
	r.media.EXPECT().DeleteMediaByIdIfUnused(mock.Anything, 931).Return(true, nil, nil).Once()
	r.files.EXPECT().DeleteObjects(mock.Anything,
		"https://files.grbpwr.test/design/931-og.png",
		"https://files.grbpwr.test/design/931-t.webp",
		"https://files.grbpwr.test/design/931-c.webp").Return(nil).Once()
	// 930: still on the technical sheet — kept, objects untouched.
	r.media.EXPECT().DeleteMediaByIdIfUnused(mock.Anything, 930).
		Return(false, []entity.MediaUsageRef{{MediaId: 930, Kind: "tech_card_media", EntityId: 41}}, nil).Once()
	// 912: row went, bucket refused — kept and logged.
	r.media.EXPECT().DeleteMediaByIdIfUnused(mock.Anything, 912).Return(true, nil, nil).Once()
	r.files.EXPECT().DeleteObjects(mock.Anything,
		"https://files.grbpwr.test/design/912-og.png",
		"https://files.grbpwr.test/design/912-t.webp",
		"https://files.grbpwr.test/design/912-c.webp").Return(errors.New("s3: 503")).Once()

	resp, err := r.s.DeleteDesignPicture(designRunCtx(), &pb_admin.DeleteDesignPictureRequest{PictureId: 12})
	require.NoError(t, err)
	require.Equal(t, []int32{31, 30, 12}, resp.GetDeletedPictureIds())
	require.Equal(t, []int32{931}, resp.GetDeletedMediaIds())
	require.Equal(t, []int32{930, 912}, resp.GetKeptMediaIds())
}

// A MEDIA ROW ALREADY MISSING counts as gone: nothing to drop, nothing kept. And a media-store
// error keeps the id rather than failing the call — the pictures are already gone.
func TestDeleteDesignPictureTreatsAMissingRowAsGoneAndAStoreErrorAsKept(t *testing.T) {
	r := newDeleteRig(t)
	res := &entity.DesignPictureDeletion{PictureIds: []int{12}, MediaIds: []int{912, 913}, Media: map[int]entity.MediaFull{}}
	r.design.EXPECT().DeletePicture(mock.Anything, 12, "designer").Return(res, nil).Once()
	r.media.EXPECT().DeleteMediaByIdIfUnused(mock.Anything, 912).Return(true, nil, nil).Once()
	r.media.EXPECT().DeleteMediaByIdIfUnused(mock.Anything, 913).Return(false, nil, errors.New("db down")).Once()
	resp, err := r.s.DeleteDesignPicture(designRunCtx(), &pb_admin.DeleteDesignPictureRequest{PictureId: 12})
	require.NoError(t, err)
	require.Equal(t, []int32{912}, resp.GetDeletedMediaIds())
	require.Equal(t, []int32{913}, resp.GetKeptMediaIds())
}

// THE WALK IS PURE: levels, dedupe, a corrupt cycle stops rather than spins, and the root rule.
func TestDesignCollectSubtreeWalksLevelsAndStopsOnACycle(t *testing.T) {
	children := map[int][]entity.DesignSubtreeNode{
		12: {{Id: 30, MediaId: 930}, {Id: 31, MediaId: 931}},
		30: {{Id: 40, MediaId: 940}},
		40: {{Id: 12, MediaId: 912}}, // corruption: back to the root
	}
	var asked [][]int
	levels, err := entity.DesignCollectSubtree(entity.DesignSubtreeNode{Id: 12, MediaId: 912},
		func(parents []int) ([]entity.DesignSubtreeNode, error) {
			asked = append(asked, append([]int(nil), parents...))
			var out []entity.DesignSubtreeNode
			for _, p := range parents {
				out = append(out, children[p]...)
			}
			return out, nil
		})
	require.NoError(t, err)
	require.Len(t, levels, 3)
	require.Equal(t, []int{12}, ids(levels[0]))
	require.Equal(t, []int{30, 31}, ids(levels[1]))
	require.Equal(t, []int{40}, ids(levels[2]))
	require.Equal(t, [][]int{{12}, {30, 31}, {40}}, asked, "the root is never asked for twice")

	require.ErrorIs(t, entity.DesignPictureDeletable(entity.DesignPicture{Id: 1}), entity.ErrDesignPictureIsRoot)
	require.NoError(t, entity.DesignPictureDeletable(entity.DesignPicture{Id: 2, DerivedFrom: sql.NullInt32{Int32: 1, Valid: true}}))
}

func ids(level []entity.DesignSubtreeNode) []int {
	out := make([]int, 0, len(level))
	for _, n := range level {
		out = append(out, n.Id)
	}
	return out
}
