package admin

import (
	"context"
	"testing"

	"github.com/jekabolt/grbpwr-manager/internal/dependency/mocks"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	pb_admin "github.com/jekabolt/grbpwr-manager/proto/gen/admin"
	pb_common "github.com/jekabolt/grbpwr-manager/proto/gen/common"
	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

// TestUpdateStyleSizeChartPictureTriState pins how the request's picture reaches the store (0459):
// picture_set = false (an older client) keeps the stored picture, picture_set with no picture
// clears it, and a set picture is passed through with its lines and returned in the response.
func TestUpdateStyleSizeChartPictureTriState(t *testing.T) {
	stored := &entity.StyleSizeChartPicture{
		Source: entity.StyleSizeChartPictureSourceSketch, MediaID: 4, MediaURL: "https://x/sketch.jpg",
		Lines: []entity.StyleSizeChartLine{{MeasurementNameID: 7, X1: 0.1, Y1: 0.2, X2: 0.3, Y2: 0.4, Position: 1}},
	}
	for name, tc := range map[string]struct {
		req  *pb_admin.UpdateStyleSizeChartRequest
		want *entity.StyleSizeChartPicture
	}{
		"not set keeps": {
			req:  &pb_admin.UpdateStyleSizeChartRequest{Picture: &pb_common.StyleSizeChartPicture{Source: "icon", IconKey: "ignored"}},
			want: nil,
		},
		"set without picture clears": {
			req:  &pb_admin.UpdateStyleSizeChartRequest{PictureSet: true},
			want: &entity.StyleSizeChartPicture{},
		},
		"set replaces": {
			req: &pb_admin.UpdateStyleSizeChartRequest{PictureSet: true, Picture: &pb_common.StyleSizeChartPicture{
				Source: "sketch", MediaId: 4, MediaUrl: "https://client/ignored.jpg",
				Lines: []*pb_common.StyleSizeChartLine{nil, {MeasurementNameId: 7, X1: 0.1, Y1: 0.2, X2: 0.3, Y2: 0.4, Position: 1}},
			}},
			want: &entity.StyleSizeChartPicture{
				Source: "sketch", MediaID: 4,
				Lines: []entity.StyleSizeChartLine{{MeasurementNameID: 7, X1: 0.1, Y1: 0.2, X2: 0.3, Y2: 0.4, Position: 1}},
			},
		},
	} {
		t.Run(name, func(t *testing.T) {
			repo := mocks.NewMockRepository(t)
			tc2 := mocks.NewMockTechCards(t)
			repo.EXPECT().TechCards().Return(tc2)
			var got *entity.StyleSizeChartPicture
			tc2.EXPECT().UpdateStyleSizeChart(mock.Anything, 9, 3, mock.Anything, 0, mock.Anything, mock.Anything,
				false, mock.Anything, mock.Anything).
				RunAndReturn(func(_ context.Context, _, _ int, _ []entity.StyleSizeChartCell, _ int,
					_ []entity.StyleSizeChartGradeStep, _ []entity.StyleSizeChartPoint, _ bool,
					_ *decimal.Decimal, picture *entity.StyleSizeChartPicture) (entity.StyleSizeChart, error) {
					got = picture
					return entity.StyleSizeChart{StyleID: 9, LockVersion: 4, Picture: stored}, nil
				})
			tc.req.StyleId, tc.req.ExpectedLockVersion = 9, 3
			resp, err := (&Server{repo: repo}).UpdateStyleSizeChart(fullAccessCtx(), tc.req)
			require.NoError(t, err)
			require.Equal(t, tc.want, got)
			pic := resp.GetChart().GetPicture()
			require.Equal(t, "https://x/sketch.jpg", pic.GetMediaUrl(), "the response carries the stored picture")
			require.Len(t, pic.GetLines(), 1)
		})
	}
}
