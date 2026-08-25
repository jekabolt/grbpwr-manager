package admin

import (
	"archive/zip"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	mocks "github.com/jekabolt/grbpwr-manager/internal/dependency/mocks"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/jekabolt/grbpwr-manager/internal/techcardarchive"
	pb_common "github.com/jekabolt/grbpwr-manager/proto/gen/common"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
	pbdecimal "google.golang.org/genproto/googleapis/type/decimal"
	"google.golang.org/protobuf/encoding/protojson"
)

// ─────────────────────────────────────────────────────────────────────────────
// Ф2.3 — the identity resolver, tested the way the owner's rule demands.
//
// Every case below asks ONE question of a miss: does it come out as a HOLE WITH THE RIGHT CODE and
// a counter that moved, or as a silent zero? The second is the only failure mode that matters here.
// A resolver that drops an unknown size and says nothing looks exactly like a card that never had
// that size, and a card missing half its rows with an empty report is the thing this whole feature
// exists to make impossible.
//
// Helpers are prefixed tcimp* — the admin test package already owns `dec` and `tcz*`.
// ─────────────────────────────────────────────────────────────────────────────

// tcimpZip writes an honest ZIP: real bodies, real CRCs. Built in code and never committed as a
// fixture blob, for the reason the reader's tests give — "media/index.json points at a file the
// archive does not carry" is a sentence, while a .zip in testdata is 400 opaque bytes.
func tcimpZip(t *testing.T, files map[string][]byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for name, body := range files {
		w, err := zw.Create(name)
		require.NoError(t, err)
		_, err = w.Write(body)
		require.NoError(t, err)
	}
	require.NoError(t, zw.Close())
	return buf.Bytes()
}

func tcimpStrPtr(s string) *string { return &s }

func tcimpJSON(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	require.NoError(t, err)
	return b
}

// tcimpArchive is the fixture: a manifest, a card, and whatever sidecars a case needs.
type tcimpArchive struct {
	manifest techcardarchive.Manifest
	insert   *pb_common.TechCardInsert
	files    map[string][]byte
}

// tcimpNewArchive is a MINIMAL VALID archive of the format: the two mandatory entries, the money
// policy that the reader refuses an archive without, and a size table mapping the source ids used
// throughout these tests (3=s, 4=m, 5=l, 9=xxl — the one the target base will not have).
func tcimpNewArchive() *tcimpArchive {
	return &tcimpArchive{
		manifest: techcardarchive.Manifest{
			Format:        techcardarchive.FormatName,
			FormatVersion: techcardarchive.FormatVersion,
			MoneyPolicy:   techcardarchive.MoneyPolicyStrippedV1,
			Source:        techcardarchive.Source{Host: "backend.source.example", TechCardID: 214, StyleNumber: "GRB-SS26-014"},
			IDMaps: techcardarchive.IDMaps{
				Sizes: map[string]string{"3": "s", "4": "m", "5": "l", "9": "xxl"},
			},
		},
		insert: &pb_common.TechCardInsert{
			StyleNumber: "GRB-SS26-014",
			Name:        "coat",
			SizeIds:     []int32{3, 4},
		},
		files: map[string][]byte{},
	}
}

func (a *tcimpArchive) with(name string, body []byte) *tcimpArchive {
	a.files[name] = body
	return a
}

// blob adds a binary entry under the name the FORMAT dictates — <dir>/<sha256 of the body><ext> —
// and hands back that name and digest.
//
// NOT "media/aa.jpg". The reader classifies an entry by its name, and a name that does not carry a
// 64-hex digest is not a media file to it: it is an entry this server does not know, which lands in
// UnknownEntries and comes out as an unknown_entry line. A fixture that spells the name loosely
// therefore tests a different archive than the one an export writes — and it was this test that
// caught it.
func (a *tcimpArchive) blob(dir, ext string, body []byte) (name, sha string) {
	sum := sha256.Sum256(body)
	sha = hex.EncodeToString(sum[:])
	name = dir + sha + ext
	a.files[name] = body
	return name, sha
}

func (a *tcimpArchive) open(t *testing.T) *techcardarchive.Archive {
	t.Helper()
	cardJSON, err := protojson.Marshal(&pb_common.TechCard{Id: 214, TechCard: a.insert})
	require.NoError(t, err)

	files := map[string][]byte{
		techcardarchive.FileManifest: tcimpJSON(t, a.manifest),
		techcardarchive.FileCard:     cardJSON,
	}
	for k, v := range a.files {
		files[k] = v
	}
	raw := tcimpZip(t, files)
	arch, err := techcardarchive.OpenArchive(bytes.NewReader(raw), int64(len(raw)))
	require.NoError(t, err)
	return arch
}

// tcimpDictionary is the TARGET base's dictionary: sizes s/m/l (no xxl) and two measurements.
func tcimpDictionary() *entity.DictionaryInfo {
	top := 1
	sub := 2
	return &entity.DictionaryInfo{
		Sizes: []entity.Size{{Id: 30, Name: "s"}, {Id: 40, Name: "m"}, {Id: 50, Name: "l"}},
		Measurements: []entity.MeasurementName{
			{Id: 110, Name: "chest"}, {Id: 120, Name: "length"},
		},
		Categories: []entity.Category{
			{ID: 1, Name: "clothing", LevelID: entity.CategoryLevelTop},
			{ID: 2, Name: "outerwear", LevelID: entity.CategoryLevelSub, ParentID: &top},
			{ID: 3, Name: "jacket", LevelID: entity.CategoryLevelType, ParentID: &sub},
		},
	}
}

// tcimpServer wires a Server whose repo is a strict mock: an unexpected call fails the test, so
// "nothing extra was asked of the database" is proved by the test being green at all.
func tcimpServer(t *testing.T) (*Server, *mocks.MockRepository, *mocks.MockTechCards, *mocks.MockMedia) {
	t.Helper()
	repo := mocks.NewMockRepository(t)
	cards := mocks.NewMockTechCards(t)
	media := mocks.NewMockMedia(t)
	cache := mocks.NewMockCache(t)

	repo.EXPECT().Cache().Return(cache).Maybe()
	repo.EXPECT().TechCards().Return(cards).Maybe()
	repo.EXPECT().Media().Return(media).Maybe()
	cache.EXPECT().GetDictionaryInfo(mock.Anything).Return(tcimpDictionary(), nil).Maybe()

	return &Server{repo: repo}, repo, cards, media
}

// tcimpHoles filters the report holes by reason — the codes are the contract, the detail is free
// text with none.
func tcimpHoles(res *resolvedTechCardImport, reason techcardarchive.Reason) []techcardarchive.ImportHole {
	out := []techcardarchive.ImportHole{}
	for _, h := range res.Holes {
		if h.Reason == reason {
			out = append(out, h)
		}
	}
	return out
}

func tcimpTally(t *testing.T, res *resolvedTechCardImport, entityName string) techcardarchive.EntityTally {
	t.Helper()
	return res.Counters[entityName]
}

// ────────────────────────────── 1. sizes ──────────────────────────────

// A size the target dictionary does not have must leave the card, leave every size-scoped row, and
// leave a LINE — named by the size's NAME, which is what the operator would add to their dictionary,
// not by a number belonging to somebody else's base.
func TestResolveImportSizeUnknownIsAHoleNotASilentZero(t *testing.T) {
	s, _, _, _ := tcimpServer(t)
	a := tcimpNewArchive()
	a.insert.SizeIds = []int32{3, 4, 9} // 9 = xxl, absent here
	a.insert.BaseSampleSizeId = 9
	a.insert.SizeQuantities = []*pb_common.TechCardSizeQuantity{
		{SizeId: 4, OrderQty: 100},
		{SizeId: 9, OrderQty: 40},
	}
	a.insert.Patterns = []*pb_common.TechCardSizePattern{{LineKey: "P1", SizeId: 9}}
	sheet, sheetSHA := a.blob(techcardarchive.DirPatterns, ".dxf", []byte("dxf"))
	a.with(techcardarchive.FilePatternsIndex, tcimpJSON(t, []techcardarchive.PatternIndexEntry{
		{LineKey: "P1", File: sheet, SHA256: sheetSHA},
	}))

	res, err := s.resolveTechCardImport(t.Context(), a.open(t))
	require.NoError(t, err)

	require.Equal(t, []int32{30, 40}, res.Insert.GetSizeIds(), "the unknown size leaves the range, the known ones are remapped")
	require.Zero(t, res.Insert.GetBaseSampleSizeId(), "a cleared scalar size FK reads as unset, never as the source's number")
	require.Len(t, res.Insert.GetSizeQuantities(), 1, "a size-scoped row whose size vanished is dropped, not left saying nothing")
	require.EqualValues(t, 40, res.Insert.GetSizeQuantities()[0].GetSizeId())

	// A pattern sheet is the deliberate exception: size 0 is documented as «filed under no size»,
	// so the sheet imports and only loses its filing.
	require.Len(t, res.Insert.GetPatterns(), 1)
	require.Zero(t, res.Insert.GetPatterns()[0].GetSizeId())

	holes := tcimpHoles(res, techcardarchive.ReasonSizeUnknown)
	require.Len(t, holes, 1, "one missing size is one line, however many fields referenced it")
	require.Equal(t, techcardarchive.EntitySize, holes[0].Entity)
	require.Equal(t, "size_name=xxl", holes[0].Ref)

	tally := tcimpTally(t, res, techcardarchive.EntitySize)
	require.Equal(t, 2, tally.Imported)
	require.Equal(t, 1, tally.Skipped, "the skipped column is the only place the lost size is a NUMBER")
}

// ────────────────────────────── 2. measurements (the SECOND axis) ──────────────────────────────

// A measurement the target does not have must NOT be reported as a size problem: an operator told
// "size unknown" about a measurement opens the wrong dictionary. Its own code, its own entity word.
func TestResolveImportMeasurementUnknownIsNotASizeProblem(t *testing.T) {
	s, _, _, _ := tcimpServer(t)
	a := tcimpNewArchive().with(techcardarchive.FileSizeChart, tcimpJSON(t, techcardarchive.SizeChart{
		Cells: []techcardarchive.SizeChartCell{
			{SizeName: "s", Measurement: "chest", Value: "50"},
			{SizeName: "m", Measurement: "chest", Value: "52"},
			{SizeName: "m", Measurement: "обхват шеи", Value: "38"},
		},
		GradeBaseSizeName: "m",
		GradeSteps: []techcardarchive.SizeChartGradeStep{
			{Measurement: "chest", Step: "2"},
			{Measurement: "обхват шеи", Step: "1"},
		},
	}))

	res, err := s.resolveTechCardImport(t.Context(), a.open(t))
	require.NoError(t, err)

	require.Len(t, res.SizeChartPlan.Cells, 2, "the chart imports without the row it could not resolve")
	require.Equal(t, 30, res.SizeChartPlan.Cells[0].SizeID)
	require.Equal(t, 110, res.SizeChartPlan.Cells[0].MeasurementNameID)
	require.Equal(t, 40, res.SizeChartPlan.GradeBaseSizeID)
	require.Len(t, res.SizeChartPlan.GradeSteps, 1)

	holes := tcimpHoles(res, techcardarchive.ReasonMeasurementUnknown)
	require.Len(t, holes, 1)
	require.Equal(t, techcardarchive.EntityMeasurement, holes[0].Entity,
		"a measurement problem must not be filed under `size`")
	require.Equal(t, "measurement=обхват шеи", holes[0].Ref)
	require.Empty(t, tcimpHoles(res, techcardarchive.ReasonSizeUnknown),
		"nothing here is a size problem, and the size dictionary must not be blamed for it")
}

// A size name off the CHART that the base does not have is a size problem, counted once even when
// three rows name it.
func TestResolveImportSizeChartSizeUnknownCountsOnce(t *testing.T) {
	s, _, _, _ := tcimpServer(t)
	a := tcimpNewArchive().with(techcardarchive.FileSizeChart, tcimpJSON(t, techcardarchive.SizeChart{
		Cells: []techcardarchive.SizeChartCell{
			{SizeName: "xxl", Measurement: "chest", Value: "60"},
			{SizeName: "xxl", Measurement: "length", Value: "80"},
			{SizeName: "s", Measurement: "chest", Value: "50"},
		},
	}))

	res, err := s.resolveTechCardImport(t.Context(), a.open(t))
	require.NoError(t, err)
	require.Len(t, res.SizeChartPlan.Cells, 1)
	require.Len(t, tcimpHoles(res, techcardarchive.ReasonSizeUnknown), 1)
	require.Equal(t, 1, tcimpTally(t, res, techcardarchive.EntitySize).Skipped,
		"one unknown size named by three rows is one unresolved size, not three")
}

// ────────────────────────────── 3. category ──────────────────────────────

func TestResolveImportCategoryPathResolvesAndMisses(t *testing.T) {
	t.Run("resolved", func(t *testing.T) {
		s, _, _, _ := tcimpServer(t)
		a := tcimpNewArchive()
		a.insert.CategoryId = 777 // the SOURCE base's id, meaningless here
		a.manifest.IDMaps.CategoryPath = []string{"clothing", "outerwear", "jacket"}

		res, err := s.resolveTechCardImport(t.Context(), a.open(t))
		require.NoError(t, err)
		require.EqualValues(t, 3, res.Insert.GetCategoryId(), "the most specific node of the path")
		require.Empty(t, tcimpHoles(res, techcardarchive.ReasonCategoryUnknown))
	})

	t.Run("unresolved leaves 0 and says so", func(t *testing.T) {
		s, _, _, _ := tcimpServer(t)
		a := tcimpNewArchive()
		a.insert.CategoryId = 777
		a.manifest.IDMaps.CategoryPath = []string{"clothing", "swimwear"}

		res, err := s.resolveTechCardImport(t.Context(), a.open(t))
		require.NoError(t, err)
		require.Zero(t, res.Insert.GetCategoryId(),
			"the source's category_id must never survive — 0 is the contract's own «unset»")
		holes := tcimpHoles(res, techcardarchive.ReasonCategoryUnknown)
		require.Len(t, holes, 1)
		require.Equal(t, techcardarchive.EntityCard, holes[0].Entity)
		require.Equal(t, "category_path=clothing/swimwear", holes[0].Ref)
		require.Equal(t, techcardarchive.StatusDegraded, holes[0].Status, "the card lands, thinner")
	})

	t.Run("no path at all is not a hole", func(t *testing.T) {
		s, _, _, _ := tcimpServer(t)
		a := tcimpNewArchive()
		a.insert.CategoryId = 777

		res, err := s.resolveTechCardImport(t.Context(), a.open(t))
		require.NoError(t, err)
		require.Zero(t, res.Insert.GetCategoryId())
		require.Empty(t, tcimpHoles(res, techcardarchive.ReasonCategoryUnknown),
			"a card that had no category lost nothing, so there is nothing to report")
	})
}

// ────────────────────────────── 4. media ──────────────────────────────

// Three fates in one archive: bytes already here (reuse the row), bytes new here (a placeholder
// until Ф3.1 uploads), and a slot with no bytes at all (cleared, reported, counted).
func TestResolveImportMediaReuseUploadAndMissing(t *testing.T) {
	s, _, _, media := tcimpServer(t)
	a := tcimpNewArchive()
	a.insert.TechnicalMedia = []*pb_common.TechCardMediaItem{
		{MediaId: 4020}, // already stored here
		{MediaId: 4021}, // new bytes
		{MediaId: 4099}, // no file in the archive at all
	}
	a.insert.Callouts = []*pb_common.TechCardCallout{{Number: 1, MediaId: 4099}}
	a.insert.Details = []*pb_common.TechCardDetail{{Key: "d", MediaIds: []int32{4020, 4099}}}
	a.manifest.Contents.Media = 2
	here, hereSHA := a.blob(techcardarchive.DirMedia, ".jpg", []byte("A"))
	fresh, freshSHA := a.blob(techcardarchive.DirMedia, ".jpg", []byte("B"))
	a.with(techcardarchive.FileMediaIndex, tcimpJSON(t, []techcardarchive.MediaIndexEntry{
		{Ref: 4020, File: here, SHA256: hereSHA, Kind: "TECH_CARD_MEDIA_KIND_FRONT"},
		{Ref: 4021, File: fresh, SHA256: freshSHA},
	}))

	media.EXPECT().FindMediaByContentHash(mock.Anything, hereSHA).
		Return(&entity.MediaFull{Id: 9001}, nil)
	media.EXPECT().FindMediaByContentHash(mock.Anything, freshSHA).Return(nil, nil)

	res, err := s.resolveTechCardImport(t.Context(), a.open(t))
	require.NoError(t, err)

	require.Len(t, res.MediaPlan, 2)
	require.Equal(t, tcimpMediaReuse, res.MediaPlan[0].Action)
	require.EqualValues(t, 9001, res.MediaPlan[0].TargetID)
	require.Equal(t, tcimpMediaUpload, res.MediaPlan[1].Action)
	require.Negative(t, res.MediaPlan[1].Placeholder,
		"an upload stands in the insert as a NEGATIVE id, which can equal no real row")

	items := res.Insert.GetTechnicalMedia()
	require.Len(t, items, 2, "the slot with no bytes is removed — the converter refuses media_id <= 0")
	require.EqualValues(t, 9001, items[0].GetMediaId())
	require.Equal(t, res.MediaPlan[1].Placeholder, items[1].GetMediaId())

	require.Len(t, res.Insert.GetCallouts(), 1, "a callout keeps its geometry")
	require.Zero(t, res.Insert.GetCallouts()[0].GetMediaId(),
		"callout.media_id = 0 is the contract's «not anchored to a picture»")
	require.Equal(t, []int32{9001}, res.Insert.GetDetails()[0].GetMediaIds(),
		"a missing entry is dropped from a repeated FK, never appended as 0")

	holes := tcimpHoles(res, techcardarchive.ReasonMediaMissing)
	require.Len(t, holes, 1)
	require.Equal(t, "media_id=4099", holes[0].Ref)

	tally := tcimpTally(t, res, techcardarchive.EntityMedia)
	require.Equal(t, 2, tally.Imported)
	require.Equal(t, 1, tally.Skipped)
}

// Two source media ids whose bytes are identical are ONE file in the archive and must become ONE
// row here — the same thing FindMediaByContentHash does on the reuse side.
func TestResolveImportMediaSharesOnePlaceholderPerContent(t *testing.T) {
	s, _, _, media := tcimpServer(t)
	a := tcimpNewArchive()
	a.insert.TechnicalMedia = []*pb_common.TechCardMediaItem{{MediaId: 4020}, {MediaId: 4021}}
	shared, sharedSHA := a.blob(techcardarchive.DirMedia, ".jpg", []byte("A"))
	a.with(techcardarchive.FileMediaIndex, tcimpJSON(t, []techcardarchive.MediaIndexEntry{
		{Ref: 4020, File: shared, SHA256: sharedSHA},
		{Ref: 4021, File: shared, SHA256: sharedSHA},
	}))
	media.EXPECT().FindMediaByContentHash(mock.Anything, sharedSHA).Return(nil, nil).Twice()

	res, err := s.resolveTechCardImport(t.Context(), a.open(t))
	require.NoError(t, err)
	require.Equal(t, res.MediaPlan[0].Placeholder, res.MediaPlan[1].Placeholder)
	require.Equal(t, res.Insert.GetTechnicalMedia()[0].GetMediaId(), res.Insert.GetTechnicalMedia()[1].GetMediaId())
}

// ────────────────────────────── 5. materials ──────────────────────────────

// The three failure verdicts are three different lines, and in every one of them the BOM LINE still
// imports — carrying its own name, supplier and unit, which is what makes «a gap is a skip» honest
// rather than lossy.
func TestResolveImportMaterialVerdictsUnlinkButKeepTheLine(t *testing.T) {
	s, _, cards, _ := tcimpServer(t)
	a := tcimpNewArchive()
	a.insert.BomItems = []*pb_common.TechCardBomItem{
		{LineKey: "B-OK", Name: "wool", MaterialId: 8120},
		{LineKey: "B-DUP", Name: "lining", MaterialId: 8121},
		{LineKey: "B-UNIT", Name: "thread", MaterialId: 8122},
		{LineKey: "B-GONE", Name: "tape", MaterialId: 8123},
		{LineKey: "B-NOPASS", Name: "buttons", MaterialId: 8199},
		{LineKey: "B-FREE", Name: "typed by hand"},
	}
	a.manifest.Contents.Materials = 4
	a.with(techcardarchive.FileMaterialsIndex, tcimpJSON(t, []techcardarchive.MaterialPassport{
		{Ref: 8120, Code: "F-WOOL-320", Name: "wool melton", Unit: "m"},
		{Ref: 8121, Code: "F-DUP", Name: "lining", Unit: "m"},
		{Ref: 8122, Code: "T-40", Name: "thread", Unit: "cone"},
		{Ref: 8123, Code: "GONE-HERE", Name: "tape", Unit: "m"},
	}))

	cards.EXPECT().ListMaterials(mock.Anything, "", true).Return([]entity.MaterialWithPrice{
		tcimpCatalogRow(1001, "F-WOOL-320", "m"),
		tcimpCatalogRow(1002, "F-DUP", "m"),
		tcimpCatalogRow(1003, "F-DUP", "m"),
		tcimpCatalogRow(1004, "T-40", "pcs"),
	}, nil)

	res, err := s.resolveTechCardImport(t.Context(), a.open(t))
	require.NoError(t, err)

	byKey := map[string]*pb_common.TechCardBomItem{}
	for _, b := range res.Insert.GetBomItems() {
		byKey[b.GetLineKey()] = b
	}
	require.Len(t, byKey, 6, "every BOM line imports, linked or not")
	require.EqualValues(t, 1001, byKey["B-OK"].GetMaterialId())
	require.Zero(t, byKey["B-DUP"].GetMaterialId())
	require.Zero(t, byKey["B-UNIT"].GetMaterialId())
	require.Zero(t, byKey["B-GONE"].GetMaterialId())
	require.Zero(t, byKey["B-NOPASS"].GetMaterialId())
	require.Equal(t, "lining", byKey["B-DUP"].GetName(), "the line keeps its own facts")

	for reason, wantRef := range map[techcardarchive.Reason]string{
		techcardarchive.ReasonMaterialAmbiguous:    "bom_line_key=B-DUP",
		techcardarchive.ReasonMaterialUnitMismatch: "bom_line_key=B-UNIT",
	} {
		holes := tcimpHoles(res, reason)
		require.Len(t, holes, 1, "reason %s", reason)
		require.Equal(t, wantRef, holes[0].Ref)
		require.Equal(t, techcardarchive.EntityMaterial, holes[0].Entity)
		require.Equal(t, techcardarchive.StatusDegraded, holes[0].Status)
	}
	notFound := tcimpHoles(res, techcardarchive.ReasonMaterialNotFound)
	require.Len(t, notFound, 2, "an article missing from the catalogue and a passport missing from the archive are both not_found")

	tally := tcimpTally(t, res, techcardarchive.EntityBOMLine)
	require.Equal(t, 2, tally.Imported, "B-OK linked, B-FREE never asked for a link")
	require.Equal(t, 4, tally.Degraded)
	require.Equal(t, 6, tally.Sum(), "the tally accounts for every line, so a lost one cannot hide")
}

// tcimpAuxCard is a header-only auxiliary style, as ListTechCards returns them.
func tcimpAuxCard(id int, styleNumber string) entity.TechCard {
	c := entity.TechCard{Id: id}
	c.StyleNumber = tcimpNullString(styleNumber)
	return c
}

func tcimpCatalogRow(id int, code, unit string) entity.MaterialWithPrice {
	var m entity.MaterialWithPrice
	m.Id = id
	m.Name = code
	m.Code = tcimpNullString(code)
	m.Unit = tcimpNullString(unit)
	return m
}

// A card whose BOM names no catalogue article at all must not make the resolver read the catalogue:
// the strict mock fails on an unexpected ListMaterials, so a green test IS the proof.
func TestResolveImportSkipsTheCatalogueWhenNothingReferencesIt(t *testing.T) {
	s, _, _, _ := tcimpServer(t)
	a := tcimpNewArchive()
	a.insert.BomItems = []*pb_common.TechCardBomItem{{LineKey: "B1", Name: "typed by hand"}}

	res, err := s.resolveTechCardImport(t.Context(), a.open(t))
	require.NoError(t, err)
	require.Equal(t, 1, tcimpTally(t, res, techcardarchive.EntityBOMLine).Imported)
}

// ────────────────────────────── 6. work tokens ──────────────────────────────

// An unknown work token costs the step its THIRD axis and nothing else. The verb and the zone are
// the step's own facts, not a dictionary reference, and clearing them would quietly rewrite what the
// technologist recorded.
func TestResolveImportWorkTokenUnknownClearsOnlyTheWork(t *testing.T) {
	s, _, cards, _ := tcimpServer(t)
	a := tcimpNewArchive()
	a.insert.Operations = []*pb_common.TechCardOperation{
		{
			OperationNumber: 10, Work: "topstitch",
			OperationType: pb_common.TechCardOperationType_TECH_CARD_OPERATION_TYPE_MACHINE,
			Zone:          pb_common.TechCardGarmentZone_TECH_CARD_GARMENT_ZONE_SLEEVE,
			MachineType:   pb_common.TechCardMachineType_TECH_CARD_MACHINE_TYPE_LOCKSTITCH,
		},
		{
			OperationNumber: 20, Work: "work_this_base_never_seeded",
			OperationType: pb_common.TechCardOperationType_TECH_CARD_OPERATION_TYPE_MACHINE,
			Zone:          pb_common.TechCardGarmentZone_TECH_CARD_GARMENT_ZONE_COLLAR,
			MachineType:   pb_common.TechCardMachineType_TECH_CARD_MACHINE_TYPE_OVERLOCK,
		},
		{OperationNumber: 30}, // no work at all — costs nothing and asks nothing
	}
	cards.EXPECT().GetOperationWorkCatalog(mock.Anything).Return([]entity.OperationWork{
		{Token: "topstitch", Verb: "machine"},
	}, nil)

	res, err := s.resolveTechCardImport(t.Context(), a.open(t))
	require.NoError(t, err)

	ops := res.Insert.GetOperations()
	require.Equal(t, "topstitch", ops[0].GetWork())
	require.Empty(t, ops[1].GetWork(), "an unknown token would fail the write's foreign key and take the whole import with it")
	require.Equal(t, pb_common.TechCardOperationType_TECH_CARD_OPERATION_TYPE_MACHINE, ops[1].GetOperationType(),
		"the verb is the step's own fact and is untouched")
	require.Equal(t, pb_common.TechCardGarmentZone_TECH_CARD_GARMENT_ZONE_COLLAR, ops[1].GetZone())
	require.Equal(t, pb_common.TechCardMachineType_TECH_CARD_MACHINE_TYPE_OVERLOCK, ops[1].GetMachineType())

	holes := tcimpHoles(res, techcardarchive.ReasonWorkTokenUnknown)
	require.Len(t, holes, 1)
	require.Equal(t, techcardarchive.EntityOperation, holes[0].Entity)
	require.Equal(t, "operation_number=20", holes[0].Ref)
	require.Equal(t, techcardarchive.StatusDegraded, holes[0].Status)

	tally := tcimpTally(t, res, techcardarchive.EntityOperation)
	require.Equal(t, 2, tally.Imported)
	require.Equal(t, 1, tally.Degraded)

	require.True(t, res.Insert.GetOperationWorkAware(),
		"a server-built payload carrying work tokens MUST declare the axis, or the write refuses its own content")
}

// ────────────────────────────── 7. assembly ──────────────────────────────

func TestResolveImportAssemblyComponentNotFound(t *testing.T) {
	s, _, cards, _ := tcimpServer(t)
	a := tcimpNewArchive()
	sizeM := "m"
	a.with(techcardarchive.FileAssembly, tcimpJSON(t, []techcardarchive.AssemblyLink{
		{ComponentStyleNumber: "GRB-AUX-0012", Qty: "1", Active: true},
		{ComponentStyleNumber: "GRB-AUX-NOPE", Qty: "1", Active: true},
		{ComponentStyleNumber: "GRB-AUX-0012", SizeName: &sizeM, Qty: "2", Active: true},
	}))
	cards.EXPECT().ListTechCards(mock.Anything, 100, 0, entity.Ascending, mock.Anything).
		Return([]entity.TechCard{tcimpAuxCard(55, "GRB-AUX-0012")}, 1, nil)

	res, err := s.resolveTechCardImport(t.Context(), a.open(t))
	require.NoError(t, err)

	require.Len(t, res.AssemblyPlan, 2)
	require.Equal(t, 55, res.AssemblyPlan[0].ComponentTechCardId)
	require.False(t, res.AssemblyPlan[0].SizeId.Valid, "size_name null = the line applies to every size")
	require.EqualValues(t, 40, res.AssemblyPlan[1].SizeId.Int32)

	holes := tcimpHoles(res, techcardarchive.ReasonAssemblyComponentNotFound)
	require.Len(t, holes, 1)
	require.Equal(t, "component_style_number=GRB-AUX-NOPE", holes[0].Ref)
	require.Equal(t, techcardarchive.EntityAssembly, holes[0].Entity)

	tally := tcimpTally(t, res, techcardarchive.EntityAssembly)
	require.Equal(t, 2, tally.Imported)
	require.Equal(t, 1, tally.Skipped)
}

// An assembly line filed under a size this base does not have is DROPPED, never widened: a null size
// means «every size», i.e. a different number of labels per production run.
func TestResolveImportAssemblySizeUnknownDropsTheLineRatherThanWidenIt(t *testing.T) {
	s, _, cards, _ := tcimpServer(t)
	a := tcimpNewArchive()
	sizeXXL := "xxl"
	a.with(techcardarchive.FileAssembly, tcimpJSON(t, []techcardarchive.AssemblyLink{
		{ComponentStyleNumber: "GRB-AUX-0012", SizeName: &sizeXXL, Qty: "1", Active: true},
	}))
	cards.EXPECT().ListTechCards(mock.Anything, 100, 0, entity.Ascending, mock.Anything).
		Return([]entity.TechCard{tcimpAuxCard(55, "GRB-AUX-0012")}, 1, nil)

	res, err := s.resolveTechCardImport(t.Context(), a.open(t))
	require.NoError(t, err)
	require.Empty(t, res.AssemblyPlan)
	require.Len(t, tcimpHoles(res, techcardarchive.ReasonSizeUnknown), 1)
	require.Equal(t, 1, tcimpTally(t, res, techcardarchive.EntityAssembly).Skipped)
}

// The index of auxiliary styles is paged to the END. A one-page-deep lookup would report a component
// that EXISTS as missing — a hole is allowed to say "not here", never to say it confidently after
// looking at a hundred rows out of a hundred and one.
func TestResolveImportAssemblyIndexPagesToTheEnd(t *testing.T) {
	s, _, cards, _ := tcimpServer(t)
	a := tcimpNewArchive().with(techcardarchive.FileAssembly, tcimpJSON(t, []techcardarchive.AssemblyLink{
		{ComponentStyleNumber: "GRB-AUX-0101", Qty: "1", Active: true},
	}))

	first := make([]entity.TechCard, 0, 100)
	for i := 0; i < 100; i++ {
		first = append(first, tcimpAuxCard(i+1, fmt.Sprintf("GRB-AUX-%04d", i)))
	}
	cards.EXPECT().ListTechCards(mock.Anything, 100, 0, entity.Ascending, mock.Anything).
		Return(first, 101, nil)
	cards.EXPECT().ListTechCards(mock.Anything, 100, 100, entity.Ascending, mock.Anything).
		Return([]entity.TechCard{tcimpAuxCard(777, "GRB-AUX-0101")}, 101, nil)

	res, err := s.resolveTechCardImport(t.Context(), a.open(t))
	require.NoError(t, err)
	require.Len(t, res.AssemblyPlan, 1)
	require.Equal(t, 777, res.AssemblyPlan[0].ComponentTechCardId)
	require.Empty(t, tcimpHoles(res, techcardarchive.ReasonAssemblyComponentNotFound))
}

// ────────────────────────────── 8. markers ──────────────────────────────

// FORMAT.md §5.7 is the whole contract for the one entry that travels as RAW protojson, and every
// clause of it is a separate assertion here.
func TestResolveImportMarkerBlobRules(t *testing.T) {
	s, _, _, _ := tcimpServer(t)
	a := tcimpNewArchive()
	sizeM, sizeXXL := "m", "xxl"
	a.manifest.Contents.Markers = 4

	ok := tcimpMarkerBlob(t, &pb_common.TechCardMarker{
		Summary: &pb_common.TechCardMarkerSummary{
			Id: 771, TechCardId: 214, SizeId: 4, ColorwayId: 812, Name: "shell 150",
			Composition: []*pb_common.TechCardMarkerCompositionEntry{{SizeId: 4, Quantity: 2}},
		},
		Layout: &pb_common.TechCardMarkerLayout{
			Pieces: []*pb_common.TechCardMarkerPiece{
				{PieceId: 7, SizeId: 4, SourceUrl: "https://cdn.source-instance.example/x.dxf"},
			},
		},
	})
	lost := tcimpMarkerBlob(t, &pb_common.TechCardMarker{
		Summary: &pb_common.TechCardMarkerSummary{Id: 772, TechCardId: 214, Name: "mixed lay",
			Composition: []*pb_common.TechCardMarkerCompositionEntry{
				{SizeId: 4, Quantity: 1}, {SizeId: 9, Quantity: 1},
			}},
	})
	run := tcimpMarkerBlob(t, &pb_common.TechCardMarker{
		Summary: &pb_common.TechCardMarkerSummary{Id: 773, SizeId: 4, ProductionRunId: 5, Name: "run lay"},
	})
	clean := tcimpMarkerBlob(t, &pb_common.TechCardMarker{
		Summary: &pb_common.TechCardMarkerSummary{Id: 774, SizeId: 3, Name: "clean"},
	})

	a.with(techcardarchive.FileMarkersIndex, tcimpJSON(t, []techcardarchive.MarkerIndexEntry{
		{File: "markers/m-1.json", SizeName: &sizeM, MarkerName: "shell 150", BomLineKey: "B1"},
		{File: "markers/mixed-1.json", MarkerName: "mixed lay"},
		{File: "markers/m-2.json", SizeName: &sizeM, MarkerName: "run lay"},
		{File: "markers/s-1.json", SizeName: &sizeXXL, MarkerName: "clean"},
	})).
		with("markers/m-1.json", ok).
		with("markers/mixed-1.json", lost).
		with("markers/m-2.json", run).
		with("markers/s-1.json", clean)

	res, err := s.resolveTechCardImport(t.Context(), a.open(t))
	require.NoError(t, err)

	require.Len(t, res.MarkerPlan, 2, "the mixed lay lost a size and the run's marker does not travel")
	shell := res.MarkerPlan[0].Marker
	require.Zero(t, shell.GetSummary().GetId(), "the source row's own id is re-minted on insert")
	require.Zero(t, shell.GetSummary().GetTechCardId())
	require.Zero(t, shell.GetSummary().GetColorwayId(), "a colourway is a product and there is nothing to remap onto")
	require.EqualValues(t, 40, shell.GetSummary().GetSizeId())
	require.EqualValues(t, 40, shell.GetSummary().GetComposition()[0].GetSizeId())
	require.EqualValues(t, 40, shell.GetLayout().GetPieces()[0].GetSizeId())
	require.EqualValues(t, 7, shell.GetLayout().GetPieces()[0].GetPieceId(),
		"piece_id is layout-local — an identity of this blob and of nothing else — and is left alone")
	require.Empty(t, shell.GetLayout().GetPieces()[0].GetSourceUrl(), "the exporting instance's url does not travel")

	cw := tcimpHoles(res, techcardarchive.ReasonColorwaysNotApplied)
	require.Len(t, cw, 1)
	require.Equal(t, techcardarchive.EntityMarker, cw[0].Entity)
	require.Equal(t, "marker_name=shell 150", cw[0].Ref)
	require.Equal(t, techcardarchive.StatusDegraded, cw[0].Status, "the marker landed — thinner, not absent")

	sizeHoles := tcimpHoles(res, techcardarchive.ReasonSizeUnknown)
	require.Len(t, sizeHoles, 1)
	require.Equal(t, techcardarchive.EntityMarker, sizeHoles[0].Entity)
	require.Equal(t, "marker_name=mixed lay", sizeHoles[0].Ref)
	require.Equal(t, techcardarchive.StatusSkipped, sizeHoles[0].Status)

	tally := tcimpTally(t, res, techcardarchive.EntityMarker)
	require.Equal(t, 1, tally.Imported)
	require.Equal(t, 1, tally.Degraded)
	require.Equal(t, 2, tally.Skipped, "the dropped mixed lay AND the run's marker are both counted")
	require.Equal(t, 4, tally.Sum(), "four blobs went in, four are accounted for")
}

func tcimpMarkerBlob(t *testing.T, m *pb_common.TechCardMarker) []byte {
	t.Helper()
	b, err := protojson.Marshal(m)
	require.NoError(t, err)
	return b
}

// ────────────────────────────── 9. patterns ──────────────────────────────

// R1-1: a sheet with no file leaves the payload HERE, before conversion. The export blanks
// pattern.url and the converter requires a managed-host url, so a row left in place would fail the
// WHOLE import instead of producing the one hole it deserves.
func TestResolveImportPatternWithoutAFileLeavesBeforeConversion(t *testing.T) {
	s, _, _, _ := tcimpServer(t)
	a := tcimpNewArchive()
	a.insert.Patterns = []*pb_common.TechCardSizePattern{
		{LineKey: "P-OK", SizeId: 4, Version: 3},
		{LineKey: "P-NOFILE", SizeId: 4},
		{LineKey: "P-INDEXED-BUT-ABSENT", SizeId: 4},
		{SizeId: 4, Name: tcimpStrPtr("legacy row with no key")},
	}
	a.manifest.Contents.Patterns = 1
	sheet, sheetSHA := a.blob(techcardarchive.DirPatterns, ".dxf", []byte("dxf"))
	a.with(techcardarchive.FilePatternsIndex, tcimpJSON(t, []techcardarchive.PatternIndexEntry{
		{LineKey: "P-OK", File: sheet, SHA256: sheetSHA, Filename: "front_v3.dxf"},
		{LineKey: "P-INDEXED-BUT-ABSENT", File: techcardarchive.DirPatterns + strings.Repeat("f", 64) + ".dxf", SHA256: strings.Repeat("f", 64)},
	}))

	res, err := s.resolveTechCardImport(t.Context(), a.open(t))
	require.NoError(t, err)

	require.Len(t, res.Insert.GetPatterns(), 1)
	require.Equal(t, "P-OK", res.Insert.GetPatterns()[0].GetLineKey())
	require.Empty(t, res.Insert.GetPatterns()[0].GetUrl(),
		"the surviving row keeps a blank url on purpose — Ф3.1 substitutes it, and anything it misses must still fail loudly")
	require.Len(t, res.PatternPlan, 1)
	require.Equal(t, sheet, res.PatternPlan[0].File)

	holes := tcimpHoles(res, techcardarchive.ReasonPatternInvalid)
	require.Len(t, holes, 3)
	require.Equal(t, techcardarchive.EntityPattern, holes[0].Entity)

	tally := tcimpTally(t, res, techcardarchive.EntityPattern)
	require.Equal(t, 1, tally.Imported)
	require.Equal(t, 3, tally.Skipped)
}

// ────────────────────────────── 10. colourways and the ids with no map ──────────────────────────────

func TestResolveImportColourwaysTravelAsReferenceOnly(t *testing.T) {
	s, _, _, _ := tcimpServer(t)
	a := tcimpNewArchive()
	a.insert.Pieces = []*pb_common.TechCardPiece{
		{LineKey: "PC-1", Name: "перед", Materials: []*pb_common.TechCardPieceColorwayMaterial{
			{ColorwayId: 812, BomLineKey: "B1"},
		}},
		{LineKey: "PC-2", Name: "спинка"},
	}
	body := tcimpJSON(t, []techcardarchive.ColorwayPayload{
		{ColorCode: "BLK", BaseSKU: "X-BLK", Recipe: []techcardarchive.RecipeLine{{BomLineKey: "B1", MaterialRef: 8120}}},
		{ColorCode: "OLV"},
	})
	a.with(techcardarchive.FileColorways, body)

	res, err := s.resolveTechCardImport(t.Context(), a.open(t))
	require.NoError(t, err)

	require.JSONEq(t, string(body), string(res.ColorwaysRaw), "colorways.json travels VERBATIM for Ф6.2")
	require.Len(t, res.Insert.GetPieces(), 2, "the piece itself imports")
	require.Empty(t, res.Insert.GetPieces()[0].GetMaterials(),
		"the per-colourway cloth mapping names a product id of the source base and cannot travel")

	holes := tcimpHoles(res, techcardarchive.ReasonColorwaysNotApplied)
	require.Len(t, holes, 3, "two colourways plus the piece that lost its mapping")
	require.Equal(t, 2, tcimpTally(t, res, techcardarchive.EntityColorway).Skipped,
		"the colourway counter counts COLOURWAYS — the piece line is a line, not a colourway row")
}

// base_model_id and output_material_id name rows of the source base that no map travels beside. They
// are cleared, and 0 is what both fields document as «unset».
func TestResolveImportDropsTheIdsWithNoMap(t *testing.T) {
	s, _, _, _ := tcimpServer(t)
	a := tcimpNewArchive()
	a.insert.BaseModelId = 4242
	a.insert.OutputMaterialId = 6161
	a.insert.BomItems = []*pb_common.TechCardBomItem{{Id: 91, LineKey: "B1", Name: "label stock"}}
	a.insert.Labels = []*pb_common.TechCardLabel{
		{LabelType: pb_common.TechCardLabelType_TECH_CARD_LABEL_TYPE_MAIN, BomItemId: 91},
		{LabelType: pb_common.TechCardLabelType_TECH_CARD_LABEL_TYPE_CARE, BomItemId: 999},
	}
	a.insert.Operations = []*pb_common.TechCardOperation{
		{OperationNumber: 10, PieceIds: []int64{5, 6}, BomItemIds: []int64{91}},
	}

	res, err := s.resolveTechCardImport(t.Context(), a.open(t))
	require.NoError(t, err)

	require.Zero(t, res.Insert.GetBaseModelId())
	require.Zero(t, res.Insert.GetOutputMaterialId())
	require.Zero(t, res.Insert.GetBomItems()[0].GetId(), "the source row's PK does not travel into a write payload")
	require.Empty(t, res.Insert.GetOperations()[0].GetPieceIds())
	require.Empty(t, res.Insert.GetOperations()[0].GetBomItemIds())

	for _, l := range res.Insert.GetLabels() {
		require.Zero(t, l.GetBomItemId(), "a label's FK is the source base's BOM row id and would bind to a stranger")
	}
	require.Equal(t, []tcimpLabelLink{{LabelIndex: 0, BomLineKey: "B1"}}, res.LabelPlan,
		"the link is not lost — it is translated into the stable key for Ф3.2 to re-sew")
}

// ────────────────────────────── 11. sanitising and the positive control ──────────────────────────────

// The two unconditional passes, measured separately. An imported card may never LOOK signed, and the
// manifest's money_policy is a CLAIM the archive makes about itself — a hand-made bundle can type
// the flag and keep the prices, so the denylist runs anyway.
func TestResolveImportForcesDraftAndCutsMoney(t *testing.T) {
	s, _, _, _ := tcimpServer(t)
	a := tcimpNewArchive()
	a.insert.ApprovalState = pb_common.TechCardApprovalState_TECH_CARD_APPROVAL_STATE_RELEASED
	a.insert.Signoffs = []*pb_common.TechCardSignoff{{Section: pb_common.TechCardSignoffSection_TECH_CARD_SIGNOFF_SECTION_CONSTRUCTION, SignedBy: "somebody"}}
	a.insert.Costing = &pb_common.TechCardCosting{TargetMarginPct: &pbdecimal.Decimal{Value: "55"}}
	a.insert.BomItems = []*pb_common.TechCardBomItem{
		{LineKey: "B1", Name: "wool", UnitPrice: &pbdecimal.Decimal{Value: "42.50"}, Currency: "EUR"},
	}

	res, err := s.resolveTechCardImport(t.Context(), a.open(t))
	require.NoError(t, err)

	require.Equal(t, techcardarchive.SanitizedApprovalState, res.Insert.GetApprovalState())
	require.Empty(t, res.Insert.GetSignoffs(), "the create pipeline COERCES sign-offs rather than refusing them")
	require.Nil(t, res.Insert.GetCosting(), "the costing block is cut whole, so a money field added to it later cannot leak")
	require.Nil(t, res.Insert.GetBomItems()[0].GetUnitPrice())
	require.Empty(t, res.Insert.GetBomItems()[0].GetCurrency())
}

// The counters are the half of the report that a list of holes cannot produce, and this is the case
// that proves it: a clean archive writes NO lines at all, and its counters are what say the card
// arrived whole. Fed through Ф2.4's positive control, the same numbers are what separate «clean» from
// «the parser died halfway».
func TestResolveImportCountersCarryACleanImportAndSatisfyThePositiveControl(t *testing.T) {
	s, _, cards, media := tcimpServer(t)
	a := tcimpNewArchive()
	a.insert.BomItems = []*pb_common.TechCardBomItem{{LineKey: "B1", Name: "wool", MaterialId: 8120}}
	a.insert.TechnicalMedia = []*pb_common.TechCardMediaItem{{MediaId: 4020}}
	a.insert.Patterns = []*pb_common.TechCardSizePattern{{LineKey: "P1", SizeId: 4}}
	a.insert.Operations = []*pb_common.TechCardOperation{{OperationNumber: 10}}
	a.manifest.Contents = techcardarchive.Contents{Media: 1, Patterns: 1, Markers: 1, Materials: 1}

	photo, photoSHA := a.blob(techcardarchive.DirMedia, ".jpg", []byte("A"))
	sheet, sheetSHA := a.blob(techcardarchive.DirPatterns, ".dxf", []byte("dxf"))
	a.with(techcardarchive.FileMaterialsIndex, tcimpJSON(t, []techcardarchive.MaterialPassport{
		{Ref: 8120, Code: "F-WOOL-320", Name: "wool melton", Unit: "m"},
	})).
		with(techcardarchive.FileMediaIndex, tcimpJSON(t, []techcardarchive.MediaIndexEntry{
			{Ref: 4020, File: photo, SHA256: photoSHA},
		})).
		with(techcardarchive.FilePatternsIndex, tcimpJSON(t, []techcardarchive.PatternIndexEntry{
			{LineKey: "P1", File: sheet, SHA256: sheetSHA},
		})).
		with(techcardarchive.FileMarkersIndex, tcimpJSON(t, []techcardarchive.MarkerIndexEntry{
			{File: "markers/m-1.json", MarkerName: "shell"},
		})).with("markers/m-1.json", tcimpMarkerBlob(t, &pb_common.TechCardMarker{
		Summary: &pb_common.TechCardMarkerSummary{Id: 771, TechCardId: 214, SizeId: 4, Name: "shell"},
	}))

	cards.EXPECT().ListMaterials(mock.Anything, "", true).Return([]entity.MaterialWithPrice{
		tcimpCatalogRow(1001, "F-WOOL-320", "m"),
	}, nil)
	media.EXPECT().FindMediaByContentHash(mock.Anything, photoSHA).Return(nil, nil)

	arch := a.open(t)
	res, err := s.resolveTechCardImport(t.Context(), arch)
	require.NoError(t, err)
	require.Empty(t, res.Holes, "a clean archive produces no lines — which is exactly why the counters must exist")

	for _, e := range []string{
		techcardarchive.EntityBOMLine, techcardarchive.EntityMedia, techcardarchive.EntityPattern,
		techcardarchive.EntityMarker, techcardarchive.EntitySize, techcardarchive.EntityOperation,
	} {
		require.Positive(t, tcimpTally(t, res, e).Sum(), "entity %s must not report a zero it did not measure", e)
	}

	report := techcardarchive.BuildReport(techcardarchive.ReportInput{
		ImportID: "01J", StyleNumber: res.Insert.GetStyleNumber(), Stage: "draft", Counters: res.Counters,
		Holes: res.Holes, ExportHoles: arch.Manifest.ExportHoles,
	})
	require.NoError(t, techcardarchive.ValidateReportAgainstManifest(report, arch.Manifest))

	// THE MUTATION THAT PROVES THE CONTROL IS ALIVE: the same manifest against a tally that saw
	// nothing is exactly what a parser that fell over halfway produces, and it must be refused.
	dead := techcardarchive.BuildReport(techcardarchive.ReportInput{Counters: techcardarchive.NewCounters()})
	require.ErrorIs(t, techcardarchive.ValidateReportAgainstManifest(dead, arch.Manifest),
		techcardarchive.ErrParseControl)
}

// An entry this server does not know is LISTED, never swallowed: that is the MINOR rule of §3, and
// the difference between a missing piece and an absent one.
func TestResolveImportListsUnknownEntries(t *testing.T) {
	s, _, _, _ := tcimpServer(t)
	a := tcimpNewArchive().with("future/whatever.json", []byte("{}"))

	res, err := s.resolveTechCardImport(t.Context(), a.open(t))
	require.NoError(t, err)
	holes := tcimpHoles(res, techcardarchive.ReasonUnknownEntry)
	require.Len(t, holes, 1)
	require.Equal(t, techcardarchive.EntityArchive, holes[0].Entity)
	require.Equal(t, "entry=future/whatever.json", holes[0].Ref)
}

// A sidecar whose bytes do not hold what they claim is CORRUPTION, and corruption fails the whole
// import (§1.2). It is the one class of problem that is not a hole: a half-parsed sidecar would
// import half a card while reporting nothing.
func TestResolveImportRefusesACorruptSidecar(t *testing.T) {
	s, _, _, _ := tcimpServer(t)
	a := tcimpNewArchive().with(techcardarchive.FileSizeChart, []byte("{not json"))

	_, err := s.resolveTechCardImport(t.Context(), a.open(t))
	require.ErrorIs(t, err, techcardarchive.ErrCorrupt)
	require.True(t, techcardarchive.IsFatal(err))
}
