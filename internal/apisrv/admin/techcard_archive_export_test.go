package admin

import (
	"archive/zip"
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	authsrv "github.com/jekabolt/grbpwr-manager/internal/apisrv/auth"
	mocks "github.com/jekabolt/grbpwr-manager/internal/dependency/mocks"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/jekabolt/grbpwr-manager/internal/techcardarchive"
	pb_admin "github.com/jekabolt/grbpwr-manager/proto/gen/admin"
	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

// Ф1.5 — ЭКСПОРТНЫЙ RPC ЦЕЛИКОМ, от карточки до объекта в бакете.
//
// Круг замыкается НАШЕЙ ЖЕ ЧИТАЛКОЙ: байты, которые хендлер отдал в UploadArchiveObject,
// открываются OpenArchive — тем самым кодом, которым их будет открывать импорт. Архив, который не
// открывается своей читалкой, это провал экспорта, и узнать об этом здесь дешевле, чем у партнёра.
//
// Что проверяется помимо круга — ровно то, чего круг НЕ ВИДИТ:
//   - надмножество размеров в id_maps (архив, где размер раскладки не назван, открывается прекрасно
//     и теряет раскладку на импорте);
//   - слияние дыр Ф1.2 и Ф1.3 в манифест и в ответ RPC;
//   - имя объекта, presign и журнальная строка — то, что живёт вне архива.

// archiveExportDictionary — словарь с размерами И категориями: category_path манифеста строится по
// именам, и без категорий в словаре его нечем проверить.
func archiveExportDictionary() *entity.DictionaryInfo {
	di := archiveTestDictionary()
	di.Categories = []entity.Category{
		{ID: 21, Name: "clothing", LevelID: 1},
		{ID: 22, Name: "outerwear", LevelID: 2},
		{ID: 23, Name: "jacket", LevelID: 3},
	}
	return di
}

// exportTestRig собирает Server на моках и отдаёт то, что понадобится каждому случаю.
type exportTestRig struct {
	server *Server
	cards  *mocks.MockTechCards
	cache  *mocks.MockCache
	media  *mocks.MockMedia
	files  *mocks.MockFileStore
	// uploaded — байты, которые хендлер стримил в бакет; заполняется моком загрузки.
	uploaded []byte
	// objectName — имя, с которым он их туда отдал (FORMAT.md §1).
	objectName string
}

func newExportTestRig(t *testing.T) *exportTestRig {
	t.Helper()
	repo := mocks.NewMockRepository(t)
	rig := &exportTestRig{
		cards: mocks.NewMockTechCards(t),
		cache: mocks.NewMockCache(t),
		media: mocks.NewMockMedia(t),
		files: mocks.NewMockFileStore(t),
	}
	repo.EXPECT().TechCards().Return(rig.cards)
	repo.EXPECT().Cache().Return(rig.cache)
	repo.EXPECT().Media().Return(rig.media)
	rig.cache.EXPECT().GetDictionaryInfo(mock.Anything).Return(archiveExportDictionary(), nil)

	// The bucket is where the archive actually goes, so the mock has to behave like a streaming
	// consumer: read to EOF and keep what arrived. io.ReadAll surfacing the pipe's error is the
	// whole reason a broken writer cannot produce a green test here.
	rig.files.EXPECT().UploadArchiveObject(mock.Anything, mock.Anything, mock.Anything).
		RunAndReturn(func(_ context.Context, r io.Reader, name string) (string, error) {
			body, err := io.ReadAll(r)
			rig.uploaded, rig.objectName = body, name
			if err != nil {
				return "", err
			}
			return techcardarchive.BucketPrefixArchives + "deadbeef/" + name, nil
		})
	rig.server = &Server{repo: repo, bucket: rig.files}
	return rig
}

// archive открывает то, что уехало в бакет, читалкой импорта.
func (r *exportTestRig) archive(t *testing.T) *techcardarchive.Archive {
	t.Helper()
	a, err := techcardarchive.OpenArchive(bytes.NewReader(r.uploaded), int64(len(r.uploaded)))
	require.NoError(t, err, "архив, который не открывается нашей же читалкой, — провал экспорта")
	return a
}

// exportTestCard — карточка со стилем, категорией, размерной таблицей, одним медиа, одной выкройкой
// и одним колорвеем. Не «полная»: полноту меряет Ф1.3, здесь меряется СШИВКА.
func exportTestCard() *entity.TechCard {
	card := &entity.TechCard{Id: 7, LockVersion: 37}
	card.StyleNumber = sql.NullString{String: "GRB-SS26-014", Valid: true}
	card.Name = "jacket"
	card.ApprovalState = entity.TechCardApprovalReleased
	card.TopCategoryId = sql.NullInt32{Int32: 21, Valid: true}
	card.SubCategoryId = sql.NullInt32{Int32: 22, Valid: true}
	card.TypeId = sql.NullInt32{Int32: 23, Valid: true}
	card.BomItems = []entity.TechCardBomItem{{
		Id: 501, LineKey: "BOM-SHELL", Name: "основная ткань",
		Section:    entity.BomSectionFabric,
		MaterialId: sql.NullInt64{Int64: 8120, Valid: true},
		Unit:       sql.NullString{String: "m", Valid: true},
	}}
	card.Colorways = []entity.TechCardColorway{{Id: 812, ColorCode: "BLK"}}
	card.Media = []entity.TechCardMediaItem{{
		MediaId: 4020, Category: entity.TechCardMediaCategoryTechnical, Kind: entity.TechCardMediaFront,
		Caption: sql.NullString{String: "front flat", Valid: true},
	}}
	card.Patterns = []entity.TechCardSizePattern{{
		LineKey: "PAT-BACK", SizeId: 4, Version: 1,
		URL: "https://cdn.grbpwr.com/tech-card-patterns/back_v1.dxf",
	}}
	return card
}

// expectHealthyCollectors wires the reads a healthy card makes.
func (r *exportTestRig) expectHealthyCollectors(t *testing.T) {
	t.Helper()
	r.cards.EXPECT().GetStyleSizeChart(mock.Anything, 7).Return(entity.StyleSizeChart{
		StyleID: 7,
		Cells: []entity.StyleSizeChartCell{
			{SizeID: 4, MeasurementNameID: 11, Value: decimal.RequireFromString("52")},
		},
	}, nil)
	r.cards.EXPECT().ListStyleAssembly(mock.Anything, 7).Return(nil, nil)
	r.cards.EXPECT().ListMaterials(mock.Anything, "", true).Return([]entity.MaterialWithPrice{{
		Material: entity.Material{Id: 8120, MaterialInsert: entity.MaterialInsert{
			Name: "wool melton 320", Code: sql.NullString{String: "F-WOOL-320", Valid: true},
			Unit: sql.NullString{String: "m", Valid: true}, MaterialClass: string(entity.MaterialClassFabric),
		}},
		LatestPrice: &entity.MaterialPrice{MaterialId: 8120, Price: decimal.RequireFromString("18.40"), Currency: "EUR"},
	}}, nil)
	r.media.EXPECT().GetMediaByIds(mock.Anything, []int{4020}).Return(map[int]entity.MediaFull{
		4020: {Id: 4020, MediaItem: entity.MediaItem{
			FullSizeMediaURL: "https://cdn.grbpwr.com/grbpwr-com/2026/08/a.jpg",
			FullSizeWidth:    2400, FullSizeHeight: 3200,
		}},
	}, nil)
	r.files.EXPECT().GetManagedObject(mock.Anything, "grbpwr-com/2026/08/a.jpg").
		RunAndReturn(func(context.Context, string) (io.ReadCloser, int64, error) { return archiveTestObject("jpeg-bytes") })
	r.files.EXPECT().GetManagedObject(mock.Anything, "tech-card-patterns/back_v1.dxf").
		RunAndReturn(func(context.Context, string) (io.ReadCloser, int64, error) { return archiveTestObject("DXF-BACK") })
}

func exportCtx() context.Context {
	return authsrv.PutAdminUsername(context.Background(), "im")
}

// TestExportTechCardArchiveClosesTheCircle: карточка → RPC → объект в бакете → НАША ЖЕ ЧИТАЛКА, и
// всё, что манифест заявил, в архиве действительно лежит.
func TestExportTechCardArchiveClosesTheCircle(t *testing.T) {
	rig := newExportTestRig(t)
	card := exportTestCard()
	rig.cards.EXPECT().GetTechCardByIdConsistent(mock.Anything, 7).Return(card, nil)
	rig.expectHealthyCollectors(t)

	expiry := time.Now().UTC().Add(archivePresignTTL)
	rig.files.EXPECT().PresignArchiveObject(mock.Anything, mock.Anything, archivePresignTTL).
		Return("https://spaces.example/signed", expiry, nil)
	// Экспорт уносит карту за пределы панели, и кроме этой строки об этом не помнит ничто:
	// объект живёт дни, ссылка минуты, а сама карточка экспортом не тронута.
	rig.cards.EXPECT().AppendTechCardArchiveExportedEvent(mock.Anything, 7, "im", mock.Anything).Return(nil)

	resp, err := rig.server.ExportTechCardArchive(exportCtx(), &pb_admin.ExportTechCardArchiveRequest{TechCardId: 7})
	require.NoError(t, err)
	require.Equal(t, "https://spaces.example/signed", resp.GetUrl())
	require.True(t, expiry.Equal(resp.GetExpiresAt().AsTime()))

	// Имя объекта — то, под которым файл ляжет скачавшему на диск (FORMAT.md §1).
	require.True(t, strings.HasPrefix(rig.objectName, "techcard-GRB-SS26-014-"), "имя объекта: %s", rig.objectName)
	require.True(t, strings.HasSuffix(rig.objectName, ".zip"))

	a := rig.archive(t)

	// manifest.json написан ПЕРВЫМ: потребитель, читающий поток вперёд, решает по нему, стоит ли
	// тратить полосу на остальное.
	zr, err := zip.NewReader(bytes.NewReader(rig.uploaded), int64(len(rig.uploaded)))
	require.NoError(t, err)
	require.Equal(t, techcardarchive.FileManifest, zr.File[0].Name)

	require.Equal(t, techcardarchive.FormatName, a.Manifest.Format)
	require.Equal(t, techcardarchive.MoneyPolicyStrippedV1, a.Manifest.MoneyPolicy)
	require.Equal(t, "im", a.Manifest.ExportedBy)
	require.Equal(t, int32(7), a.Manifest.Source.TechCardID)
	require.Equal(t, "GRB-SS26-014", a.Manifest.Source.StyleNumber)
	require.Equal(t, int32(37), a.Manifest.Source.LockVersion)
	require.Equal(t, "released", a.Manifest.Source.ApprovalStateAtExport)
	require.Empty(t, a.UnknownEntries)

	// id_maps.sizes — НАДМНОЖЕСТВО: в архиве назван один размер (выкройка под "m"), а карта имён
	// приезжает целиком. §5.7 ремапит через неё каждый size_id внутри блоба раскладки, а смешанный
	// настил называет размер, которого в card.json нет вовсе; недостача там — не потерянная
	// подпись, а дыра size_unknown, роняющая ВСЮ раскладку.
	require.Equal(t, map[string]string{"3": "s", "4": "m", "5": "l"}, a.Manifest.IDMaps.Sizes)
	require.Equal(t, []string{"clothing", "outerwear", "jacket"}, a.Manifest.IDMaps.CategoryPath)
	require.Equal(t, map[string]string{"812": "BLK"}, a.Manifest.IDMaps.Colorways)

	// Заявка манифеста и содержимое архива — одно и то же, до байта.
	require.Equal(t, techcardarchive.Contents{Media: 1, Patterns: 1, Markers: 0, Materials: 1}, a.Manifest.Contents)
	require.Empty(t, a.Manifest.ExportHoles, "здоровая карточка не даёт дыр")

	var mediaIndex []techcardarchive.MediaIndexEntry
	require.NoError(t, json.Unmarshal(mustArchiveFile(t, a, techcardarchive.FileMediaIndex), &mediaIndex))
	require.Len(t, mediaIndex, a.Manifest.Contents.Media)
	require.Equal(t, int32(4020), mediaIndex[0].Ref)
	body, err := a.ReadFileVerified(mediaIndex[0].File, mediaIndex[0].SHA256)
	require.NoError(t, err, "байты медиа обязаны сойтись с обоими отпечатками — в индексе и в имени")
	require.Equal(t, "jpeg-bytes", string(body))

	var patternIndex []techcardarchive.PatternIndexEntry
	require.NoError(t, json.Unmarshal(mustArchiveFile(t, a, techcardarchive.FilePatternsIndex), &patternIndex))
	require.Len(t, patternIndex, a.Manifest.Contents.Patterns)
	require.Equal(t, "PAT-BACK", patternIndex[0].LineKey)
	require.Equal(t, "m", *patternIndex[0].SizeName, "размер выкройки едет ИМЕНЕМ")
	body, err = a.ReadFileVerified(patternIndex[0].File, patternIndex[0].SHA256)
	require.NoError(t, err)
	require.Equal(t, "DXF-BACK", string(body))

	// card.json разбирается как то самое proto-сообщение, которое ждёт принимающая сторона.
	pbCard, err := a.CardJSON()
	require.NoError(t, err)
	require.Equal(t, "GRB-SS26-014", pbCard.GetTechCard().GetStyleNumber())

	// Карточки без раскладок не дают ни папки markers/, ни пустого индекса — выбор Ф1.5,
	// закреплённый тестом writer'а; здесь он проверяется на живом пути.
	require.False(t, a.Has(techcardarchive.FileMarkersIndex))
	require.False(t, a.Has(techcardarchive.FileAssembly))
	// А размерная таблица пишется ВСЕГДА.
	require.True(t, a.Has(techcardarchive.FileSizeChart))

	// Манифест в ответе RPC — та же самая заявка, а не второй её вывод.
	pbManifest := resp.GetManifest()
	require.Equal(t, techcardarchive.MoneyPolicyStrippedV1, pbManifest.GetMoneyPolicy())
	require.Equal(t, int32(37), pbManifest.GetSource().GetLockVersion())
	require.Empty(t, pbManifest.GetHoles())
	counters := map[string]int32{}
	for _, c := range pbManifest.GetCounters() {
		counters[c.GetEntity()] = c.GetCount()
	}
	require.Equal(t, map[string]int32{"media": 1, "patterns": 1, "markers": 0, "materials": 1}, counters)
}

func mustArchiveFile(t *testing.T, a *techcardarchive.Archive, name string) []byte {
	t.Helper()
	b, err := a.ReadFile(name)
	require.NoError(t, err, "запись %s", name)
	return b
}

// TestExportTechCardArchiveDegradesOnBrokenReferences — карточка, у которой УДАЛИЛИ МАТЕРИАЛ и
// ПОТЕРЯЛИ КАРТИНКУ.
//
// Экспорт обязан ЗАКОНЧИТЬСЯ АРХИВОМ, а не отказом: и то и другое — законные состояния живой базы,
// и отказ превратил бы одну потерянную картинку в «экспорт сломан». Потери называются в манифесте
// и доезжают до ответа RPC, потому что последний честный момент увидеть, чего в файле нет, — до
// того, как человек отдал его на фабрику.
func TestExportTechCardArchiveDegradesOnBrokenReferences(t *testing.T) {
	rig := newExportTestRig(t)
	card := exportTestCard()
	// Артикул 8121 в каталоге не значится, строка BOM самодостаточна и уезжает со своим именем.
	card.BomItems[0].MaterialId = sql.NullInt64{Int64: 8121, Valid: true}
	rig.cards.EXPECT().GetTechCardByIdConsistent(mock.Anything, 7).Return(card, nil)

	rig.cards.EXPECT().GetStyleSizeChart(mock.Anything, 7).Return(entity.StyleSizeChart{StyleID: 7}, nil)
	rig.cards.EXPECT().ListStyleAssembly(mock.Anything, 7).Return(nil, nil)
	rig.cards.EXPECT().ListMaterials(mock.Anything, "", true).Return([]entity.MaterialWithPrice{}, nil)
	rig.media.EXPECT().GetMediaByIds(mock.Anything, []int{4020}).Return(map[int]entity.MediaFull{
		4020: {Id: 4020, MediaItem: entity.MediaItem{
			FullSizeMediaURL: "https://cdn.grbpwr.com/grbpwr-com/2026/08/a.jpg",
		}},
	}, nil)
	// Объект картинки бакет не отдаёт: строка медиа жива, байтов нет.
	rig.files.EXPECT().GetManagedObject(mock.Anything, "grbpwr-com/2026/08/a.jpg").
		Return(nil, int64(0), errors.New("404 from bucket"))
	rig.files.EXPECT().GetManagedObject(mock.Anything, "tech-card-patterns/back_v1.dxf").
		RunAndReturn(func(context.Context, string) (io.ReadCloser, int64, error) { return archiveTestObject("DXF-BACK") })

	rig.files.EXPECT().PresignArchiveObject(mock.Anything, mock.Anything, archivePresignTTL).
		Return("https://spaces.example/signed", time.Now().UTC().Add(archivePresignTTL), nil)
	rig.cards.EXPECT().AppendTechCardArchiveExportedEvent(mock.Anything, 7, "im", mock.Anything).Return(nil)

	resp, err := rig.server.ExportTechCardArchive(exportCtx(), &pb_admin.ExportTechCardArchiveRequest{TechCardId: 7})
	require.NoError(t, err, "битая ссылка — дыра, а не отказ экспорта")

	a := rig.archive(t)

	reasons := map[techcardarchive.Reason]string{}
	for _, h := range a.Manifest.ExportHoles {
		reasons[h.Reason] = h.Ref
	}
	require.Contains(t, reasons, techcardarchive.ReasonMaterialNotFound)
	require.Equal(t, "bom_line_key=BOM-SHELL", reasons[techcardarchive.ReasonMaterialNotFound])
	require.Contains(t, reasons, techcardarchive.ReasonMediaObjectMissing)
	require.Equal(t, "media_id=4020", reasons[techcardarchive.ReasonMediaObjectMissing])

	// Дыра — это ОТСУТСТВИЕ записи, а не пустая запись: индекс, указывающий на файл, которого в
	// архиве нет, был бы приглашением импорту искать его.
	require.Equal(t, 0, a.Manifest.Contents.Media)
	require.Equal(t, 0, a.Manifest.Contents.Materials)
	require.False(t, a.Has(techcardarchive.FileMediaIndex))
	require.False(t, a.Has(techcardarchive.FileMaterialsIndex))
	// А выкройка, которая читается, уезжает как ни в чём не бывало: остальное довозится.
	require.Equal(t, 1, a.Manifest.Contents.Patterns)

	// Те же слова во второй раз — в ответе RPC, до того как человек отдаст файл наружу.
	pbReasons := map[string]bool{}
	for _, h := range resp.GetManifest().GetHoles() {
		pbReasons[h.GetReason()] = true
	}
	require.True(t, pbReasons[string(techcardarchive.ReasonMaterialNotFound)])
	require.True(t, pbReasons[string(techcardarchive.ReasonMediaObjectMissing)])
}

// TestExportTechCardArchiveMissingCard — карточки нет: NotFound, и ни одного обращения к бакету.
func TestExportTechCardArchiveMissingCard(t *testing.T) {
	repo := mocks.NewMockRepository(t)
	cards := mocks.NewMockTechCards(t)
	repo.EXPECT().TechCards().Return(cards)
	cards.EXPECT().GetTechCardByIdConsistent(mock.Anything, 404).Return(nil, sql.ErrNoRows)

	// bucket остаётся мокой БЕЗ ожиданий: любой поход в неё провалит тест сам.
	srv := &Server{repo: repo, bucket: mocks.NewMockFileStore(t)}
	_, err := srv.ExportTechCardArchive(exportCtx(), &pb_admin.ExportTechCardArchiveRequest{TechCardId: 404})
	require.Error(t, err)
	require.Contains(t, err.Error(), "not found")

	_, err = srv.ExportTechCardArchive(exportCtx(), &pb_admin.ExportTechCardArchiveRequest{TechCardId: 0})
	require.Error(t, err, "нулевой id — InvalidArgument, а не поход в базу")
}

// Журнальная строка обязана пережить ОТМЕНУ клиентского контекста.
//
// К моменту записи архив уже лежит в бакете и ссылка уже выдана: отмена не может «отменить»
// уехавший файл, а вот унести единственный след того, что он уехал, — может. Проверяется именно
// то, что видит сторона записи: ctx.Err() внутри вставки.
func TestJournalArchiveExportOutlivesACancelledClient(t *testing.T) {
	repo := mocks.NewMockRepository(t)
	cards := mocks.NewMockTechCards(t)
	repo.EXPECT().TechCards().Return(cards)

	var seen error
	cards.EXPECT().AppendTechCardArchiveExportedEvent(mock.Anything, 7, "im", mock.Anything).
		Run(func(ctx context.Context, _ int, _ string, _ string) { seen = ctx.Err() }).Return(nil)

	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	require.Error(t, ctx.Err(), "положительный контроль: контекст вызывающего действительно отменён")

	(&Server{repo: repo}).journalArchiveExport(ctx, &entity.TechCard{Id: 7},
		techcardarchive.Manifest{ExportedBy: "im"})
	require.NoError(t, seen,
		"запись журнала не должна видеть отменённый контекст: иначе обрыв клиента между заливкой и "+
			"журналом оставляет объект в бакете без строки аудита")
}
