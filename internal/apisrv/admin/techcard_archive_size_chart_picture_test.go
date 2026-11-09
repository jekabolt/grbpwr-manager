package admin

import (
	"context"
	"io"
	"testing"

	mocks "github.com/jekabolt/grbpwr-manager/internal/dependency/mocks"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/jekabolt/grbpwr-manager/internal/techcardarchive"
	pb_common "github.com/jekabolt/grbpwr-manager/proto/gen/common"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

// Картинка размерной таблицы (0459) едет в sizechart.json: мерки линий именами, изображение ссылкой
// media_ref, а байты загрузки — отдельной записью media/index.json, хотя ни один слот card.json её
// не называет. Эскиз, уже лежащий слотом карты, второй записью не становится.
func TestArchiveSizeChartPictureTravelsWithItsMedia(t *testing.T) {
	for _, tc := range []struct {
		name    string
		source  string
		mediaID int
		ids     []int
	}{
		{name: "upload is an extra slot", source: entity.StyleSizeChartPictureSourceUpload, mediaID: 4050, ids: []int{4020, 4050}},
		{name: "sketch is already a card slot", source: entity.StyleSizeChartPictureSourceSketch, mediaID: 4020, ids: []int{4020}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			repo := mocks.NewMockRepository(t)
			cards := mocks.NewMockTechCards(t)
			cache := mocks.NewMockCache(t)
			media := mocks.NewMockMedia(t)
			files := mocks.NewMockFileStore(t)
			repo.EXPECT().TechCards().Return(cards)
			repo.EXPECT().Cache().Return(cache)
			repo.EXPECT().Media().Return(media)
			cache.EXPECT().GetDictionaryInfo(mock.Anything).Return(archiveTestDictionary(), nil)
			cards.EXPECT().ListStyleAssembly(mock.Anything, 7).Return(nil, nil)

			card := &entity.TechCard{Id: 7}
			card.Media = []entity.TechCardMediaItem{{
				MediaId: 4020, Category: entity.TechCardMediaCategoryTechnical, Kind: entity.TechCardMediaFront,
			}}
			cards.EXPECT().GetStyleSizeChart(mock.Anything, 7).Return(entity.StyleSizeChart{
				StyleID: 7,
				Picture: &entity.StyleSizeChartPicture{
					Source: tc.source, MediaID: tc.mediaID, MediaURL: "https://cdn.grbpwr.com/x.jpg",
					Lines: []entity.StyleSizeChartLine{
						{MeasurementNameID: 11, X1: 0.1, Y1: 0.4, X2: 0.9, Y2: 0.4, Position: 1},
						{MeasurementNameID: 0, X1: 0.5, Y1: 0, X2: 0.5, Y2: 1, Position: 2},
						{MeasurementNameID: 99, X1: 0, Y1: 0, X2: 1, Y2: 1, Position: 3},
					},
				},
			}, nil)
			rows := map[int]entity.MediaFull{}
			for _, id := range tc.ids {
				rows[id] = entity.MediaFull{Id: id, MediaItem: entity.MediaItem{
					FullSizeMediaURL: "https://cdn.grbpwr.com/grbpwr-com/2026/11/p.jpg",
				}}
			}
			media.EXPECT().GetMediaByIds(mock.Anything, tc.ids).Return(rows, nil)
			files.EXPECT().GetManagedObject(mock.Anything, "grbpwr-com/2026/11/p.jpg").
				RunAndReturn(func(context.Context, string) (io.ReadCloser, int64, error) { return archiveTestObject("pic") })

			sc, err := (&Server{repo: repo, bucket: files}).collectArchiveSidecars(t.Context(), card)
			require.NoError(t, err)
			defer sc.Close()

			pic := sc.SizeChart.Picture
			require.NotNil(t, pic)
			require.Equal(t, tc.source, pic.Source)
			require.EqualValues(t, tc.mediaID, pic.MediaRef)
			require.Equal(t, []techcardarchive.SizeChartLine{
				{Measurement: "chest", X1: 0.1, Y1: 0.4, X2: 0.9, Y2: 0.4, Position: 1},
				{Measurement: "", X1: 0.5, Y1: 0, X2: 0.5, Y2: 1, Position: 2},
				{Measurement: "", X1: 0, Y1: 0, X2: 1, Y2: 1, Position: 3},
			}, pic.Lines, "линия с неизвестной меркой едет без точки, а не теряется")
			require.Equal(t, []techcardarchive.Reason{techcardarchive.ReasonMeasurementUnknown}, archiveHoleReasons(sc.Holes))

			refs := make([]int32, 0, len(sc.Media))
			for _, m := range sc.Media {
				refs = append(refs, m.Ref)
			}
			want := make([]int32, 0, len(tc.ids))
			for _, id := range tc.ids {
				want = append(want, int32(id))
			}
			require.Equal(t, want, refs)
		})
	}
}

// Иконка не несёт байтов; таблица без картинки не пишет ключа вовсе.
func TestArchiveSizeChartPictureIconAndNone(t *testing.T) {
	pic, holes := archiveSizeChartPicture(&entity.StyleSizeChartPicture{
		Source: entity.StyleSizeChartPictureSourceIcon, IconKey: "tee",
		Lines: []entity.StyleSizeChartLine{{MeasurementNameID: 12, X2: 1, Y2: 1, Position: 1}},
	}, map[int]string{12: "length"})
	require.Empty(t, holes)
	require.Equal(t, &techcardarchive.SizeChartPicture{
		Source: "icon", IconKey: "tee",
		Lines: []techcardarchive.SizeChartLine{{Measurement: "length", X2: 1, Y2: 1, Position: 1}},
	}, pic)

	pic, holes = archiveSizeChartPicture(nil, nil)
	require.Nil(t, pic)
	require.Empty(t, holes)

	// Изображение удалили из библиотеки (media_id SET NULL): картинка едет без него, дыра называет потерю.
	pic, holes = archiveSizeChartPicture(&entity.StyleSizeChartPicture{Source: entity.StyleSizeChartPictureSourceUpload}, nil)
	require.NotNil(t, pic)
	require.Zero(t, pic.MediaRef)
	require.Equal(t, []techcardarchive.Reason{techcardarchive.ReasonMediaObjectMissing}, archiveHoleReasons(holes))
}

// ────────────────────────────── import ──────────────────────────────

// An upload resolves through the same media mapping as the card's slots: new bytes stand as the
// negative placeholder; line names map to this base's ids; an unknown name keeps the line unpicked.
func TestResolveImportSizeChartPictureUploadAndLines(t *testing.T) {
	s, _, _, media := tcimpServer(t)
	a := tcimpNewArchive()
	fresh, freshSHA := a.blob(techcardarchive.DirMedia, ".jpg", []byte("P"))
	a.with(techcardarchive.FileMediaIndex, tcimpJSON(t, []techcardarchive.MediaIndexEntry{
		{Ref: 4050, File: fresh, SHA256: freshSHA},
	}))
	a.with(techcardarchive.FileSizeChart, tcimpJSON(t, techcardarchive.SizeChart{
		Cells: []techcardarchive.SizeChartCell{},
		Picture: &techcardarchive.SizeChartPicture{
			Source: "upload", MediaRef: 4050,
			Lines: []techcardarchive.SizeChartLine{
				{Measurement: "Chest", X1: 0.1, Y1: 0.4, X2: 0.9, Y2: 0.4, Position: 1},
				{Measurement: "", X1: 0.5, X2: 0.5, Y2: 1, Position: 2},
				{Measurement: "sleeve", X2: 1, Y2: 1, Position: 3},
			},
		},
	}))
	media.EXPECT().FindMediaByContentHash(mock.Anything, freshSHA).Return(nil, nil)

	res, err := s.resolveTechCardImport(t.Context(), a.open(t))
	require.NoError(t, err)

	pic := res.SizeChartPlan.Picture
	require.NotNil(t, pic)
	require.Equal(t, "upload", pic.Source)
	require.Len(t, res.MediaPlan, 1)
	require.Equal(t, int(res.MediaPlan[0].Placeholder), pic.MediaID, "the picture waits for the upload like any slot")
	require.Equal(t, []entity.StyleSizeChartLine{
		{MeasurementNameID: 110, X1: 0.1, Y1: 0.4, X2: 0.9, Y2: 0.4, Position: 1},
		{MeasurementNameID: 0, X1: 0.5, X2: 0.5, Y2: 1, Position: 2},
		{MeasurementNameID: 0, X2: 1, Y2: 1, Position: 3},
	}, pic.Lines)
	holes := tcimpHoles(res, techcardarchive.ReasonMeasurementUnknown)
	require.Len(t, holes, 1)
	require.Equal(t, "measurement=sleeve", holes[0].Ref)
	require.Equal(t, techcardarchive.StatusDegraded, holes[0].Status)

	// After the bytes move, the placeholder becomes the minted id; a failed upload drops the picture.
	placed := *res
	placedPic := *pic
	placed.SizeChartPlan.Picture = &placedPic
	tcflApplyMediaPlaceholders(&placed, &tcflPlacement{mediaByPlaceholder: map[int32]int32{res.MediaPlan[0].Placeholder: 777}})
	require.Equal(t, 777, placed.SizeChartPlan.Picture.MediaID)

	failedPic := *pic
	res.SizeChartPlan.Picture = &failedPic
	tcflApplyMediaPlaceholders(res, &tcflPlacement{mediaByPlaceholder: map[int32]int32{}})
	require.Nil(t, res.SizeChartPlan.Picture, "a sketch or upload with no image is no picture")
}

// A sketch whose bytes are not in the archive costs the picture and a media_missing line; an icon
// needs no bytes and survives; a reused file carries its final id.
func TestResolveImportSizeChartPictureMissingMediaIconAndReuse(t *testing.T) {
	t.Run("missing", func(t *testing.T) {
		s, _, _, _ := tcimpServer(t)
		a := tcimpNewArchive().with(techcardarchive.FileSizeChart, tcimpJSON(t, techcardarchive.SizeChart{
			Cells:   []techcardarchive.SizeChartCell{},
			Picture: &techcardarchive.SizeChartPicture{Source: "sketch", MediaRef: 4099},
		}))
		res, err := s.resolveTechCardImport(t.Context(), a.open(t))
		require.NoError(t, err)
		require.Nil(t, res.SizeChartPlan.Picture)
		holes := tcimpHoles(res, techcardarchive.ReasonMediaMissing)
		require.Len(t, holes, 1)
		require.Equal(t, "media_id=4099", holes[0].Ref)
	})
	t.Run("icon", func(t *testing.T) {
		s, _, _, _ := tcimpServer(t)
		a := tcimpNewArchive().with(techcardarchive.FileSizeChart, tcimpJSON(t, techcardarchive.SizeChart{
			Cells: []techcardarchive.SizeChartCell{},
			Picture: &techcardarchive.SizeChartPicture{Source: "icon", IconKey: "tee",
				Lines: []techcardarchive.SizeChartLine{{Measurement: "length", X2: 1, Y2: 1, Position: 4}}},
		}))
		res, err := s.resolveTechCardImport(t.Context(), a.open(t))
		require.NoError(t, err)
		require.Equal(t, &entity.StyleSizeChartPicture{Source: "icon", IconKey: "tee",
			Lines: []entity.StyleSizeChartLine{{MeasurementNameID: 120, X2: 1, Y2: 1, Position: 1}}},
			res.SizeChartPlan.Picture)
		require.Empty(t, res.Holes)
	})
	t.Run("reuse", func(t *testing.T) {
		s, _, _, media := tcimpServer(t)
		a := tcimpNewArchive()
		a.insert.TechnicalMedia = []*pb_common.TechCardMediaItem{{MediaId: 4020}}
		here, hereSHA := a.blob(techcardarchive.DirMedia, ".jpg", []byte("A"))
		a.with(techcardarchive.FileMediaIndex, tcimpJSON(t, []techcardarchive.MediaIndexEntry{
			{Ref: 4020, File: here, SHA256: hereSHA},
		}))
		a.with(techcardarchive.FileSizeChart, tcimpJSON(t, techcardarchive.SizeChart{
			Cells:   []techcardarchive.SizeChartCell{},
			Picture: &techcardarchive.SizeChartPicture{Source: "sketch", MediaRef: 4020},
		}))
		media.EXPECT().FindMediaByContentHash(mock.Anything, hereSHA).Return(&entity.MediaFull{Id: 9001}, nil)
		res, err := s.resolveTechCardImport(t.Context(), a.open(t))
		require.NoError(t, err)
		require.NotNil(t, res.SizeChartPlan.Picture)
		require.Equal(t, 9001, res.SizeChartPlan.Picture.MediaID)
	})
	t.Run("invalid", func(t *testing.T) {
		s, _, _, _ := tcimpServer(t)
		a := tcimpNewArchive().with(techcardarchive.FileSizeChart, tcimpJSON(t, techcardarchive.SizeChart{
			Cells: []techcardarchive.SizeChartCell{},
			Picture: &techcardarchive.SizeChartPicture{Source: "icon", IconKey: "tee",
				Lines: []techcardarchive.SizeChartLine{{X1: 2, Position: 1}}},
		}))
		res, err := s.resolveTechCardImport(t.Context(), a.open(t))
		require.NoError(t, err)
		require.Nil(t, res.SizeChartPlan.Picture)
		require.Len(t, tcimpHoles(res, techcardarchive.ReasonArchiveRowInvalid), 1)
	})
}
