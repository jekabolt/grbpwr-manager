package admin

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"testing"

	mocks "github.com/jekabolt/grbpwr-manager/internal/dependency/mocks"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/jekabolt/grbpwr-manager/internal/techcardarchive"
	pb_common "github.com/jekabolt/grbpwr-manager/proto/gen/common"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
)

// ─────────────────────────────────────────────────────────────────────────────
// Ф3.1 — the questions this file actually asks.
//
// Every case here is about ONE of the four ways this phase can lose something quietly:
//
//	1. it deletes a picture it did not create (a reuse compensated as if it were an upload);
//	2. it lets one refused file take the whole card down, or lets a dead context turn into
//	   «the bucket refused all fourteen of your pictures»;
//	3. it hands bytes to the bucket before their digest is proved — the failure that is invisible
//	   because the file usually does verify;
//	4. it substitutes the media ids with the pass the brief originally asked for, and empties every
//	   picture of the import including the ones this base already had (R2-1).
//
// The fixtures are the resolver's own (tcimp*, techcard_archive_resolve_test.go): a real ZIP with
// real CRCs, opened through the real reader, resolved by the real resolver. Nothing about the plan
// is hand-built, because a hand-built plan is exactly where a wrong assumption about placeholders
// would survive. Helpers of this file are `tcfl*`.
// ─────────────────────────────────────────────────────────────────────────────

// tcflServer adds a STRICT bucket to the resolver's harness. Strict is the whole proof in half these
// tests: an upload nobody expected fails the test, so «nothing was uploaded» needs no assertion of
// its own.
func tcflServer(t *testing.T) (*Server, *mocks.MockMedia, *mocks.MockFileStore) {
	t.Helper()
	s, _, _, media := tcimpServer(t)
	fs := mocks.NewMockFileStore(t)
	fs.EXPECT().GetBaseFolder().Return("grbpwr").Maybe()
	s.bucket = fs
	return s, media, fs
}

// tcflJPEG is a payload the router recognises as a picture by its magic bytes — the bucket is
// mocked, so nothing ever decodes it, but the ROUTE is decided from these three bytes and that
// decision is under test.
func tcflJPEG(marker string) []byte { return append([]byte{0xFF, 0xD8, 0xFF}, marker...) }

func tcflSHA(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

func tcflMediaFull(id int32, urls ...string) *pb_common.MediaFull {
	info := make([]*pb_common.MediaInfo, 3)
	for i := range info {
		if i < len(urls) {
			info[i] = &pb_common.MediaInfo{MediaUrl: urls[i]}
		}
	}
	return &pb_common.MediaFull{
		Id:    id,
		Media: &pb_common.MediaItem{FullSize: info[0], Compressed: info[1], Thumbnail: info[2]},
	}
}

// ────────────────────────────── 1. reuse is not work, and is never taken back ──────────────────

// A picture this base already holds is matched by content hash and NOTHING is uploaded for it. The
// half that matters is the second one: it must not appear in uploadedKeys, because compensation
// deletes exactly that list — and the row it names belongs to every other card that points at it.
func TestArchiveFilesPlanReuseIsNeverUploadedAndNeverCompensated(t *testing.T) {
	s, media, fs := tcflServer(t)
	a := tcimpNewArchive()
	a.insert.TechnicalMedia = []*pb_common.TechCardMediaItem{{MediaId: 4020}, {MediaId: 4021}}
	here, hereSHA := a.blob(techcardarchive.DirMedia, ".jpg", tcflJPEG("already stored here"))
	fresh, freshSHA := a.blob(techcardarchive.DirMedia, ".jpg", tcflJPEG("brand new"))
	a.with(techcardarchive.FileMediaIndex, tcimpJSON(t, []techcardarchive.MediaIndexEntry{
		{Ref: 4020, File: here, SHA256: hereSHA},
		{Ref: 4021, File: fresh, SHA256: freshSHA},
	}))
	media.EXPECT().FindMediaByContentHash(mock.Anything, hereSHA).Return(&entity.MediaFull{Id: 9001}, nil)
	media.EXPECT().FindMediaByContentHash(mock.Anything, freshSHA).Return(nil, nil)

	arch := a.open(t)
	res, err := s.resolveTechCardImport(t.Context(), arch)
	require.NoError(t, err)

	// Exactly ONE upload — the strict mock refuses a second — and it carries the fresh file's
	// bytes in the data-uri envelope UploadContentImage parses.
	fs.EXPECT().UploadContentImage(mock.Anything, tcflDataURI("image/jpeg", tcflJPEG("brand new")), "grbpwr", mock.Anything).
		Return(tcflMediaFull(7001, "https://cdn/og.webp", "https://cdn/c.webp", "https://cdn/t.webp"), nil).Once()

	p, err := s.tcflPlaceImportFiles(t.Context(), arch, res)
	require.NoError(t, err)
	require.Equal(t, []string{"https://cdn/og.webp", "https://cdn/c.webp", "https://cdn/t.webp"}, p.uploadedKeys(),
		"only what this import created is compensable; the reused row's objects belong to whoever stored them")

	items := res.Insert.GetTechnicalMedia()
	require.Len(t, items, 2)
	require.EqualValues(t, 9001, items[0].GetMediaId(),
		"a reused picture keeps the FINAL id the resolver already wrote — it is not remapped a second time")
	require.EqualValues(t, 7001, items[1].GetMediaId(), "the negative placeholder becomes the row that was just minted")

	// And the compensation deletes that list and nothing else. DeleteMediaById(9001) is not
	// expected, so the strict mock fails the test if the reused row is ever touched.
	media.EXPECT().DeleteMediaById(mock.Anything, 7001).Return(nil).Once()
	fs.EXPECT().DeleteObjects(mock.Anything, "https://cdn/og.webp", "https://cdn/c.webp", "https://cdn/t.webp").
		Return(nil).Once()
	p.compensate(t.Context(), s)
}

// ────────────────────────────── 2. one file is a hole, not a failed import ─────────────────────

func TestArchiveFilesPlanOneRefusedFileIsAHoleNotAFailedImport(t *testing.T) {
	s, media, fs := tcflServer(t)
	a := tcimpNewArchive()
	a.insert.TechnicalMedia = []*pb_common.TechCardMediaItem{{MediaId: 4020}, {MediaId: 4021}}
	goodBody, badBody := tcflJPEG("stores fine"), tcflJPEG("bucket says no")
	good, goodSHA := a.blob(techcardarchive.DirMedia, ".jpg", goodBody)
	bad, badSHA := a.blob(techcardarchive.DirMedia, ".jpg", badBody)
	a.with(techcardarchive.FileMediaIndex, tcimpJSON(t, []techcardarchive.MediaIndexEntry{
		{Ref: 4020, File: good, SHA256: goodSHA},
		{Ref: 4021, File: bad, SHA256: badSHA},
	}))
	media.EXPECT().FindMediaByContentHash(mock.Anything, mock.Anything).Return(nil, nil).Twice()

	arch := a.open(t)
	res, err := s.resolveTechCardImport(t.Context(), arch)
	require.NoError(t, err)

	fs.EXPECT().UploadContentImage(mock.Anything, tcflDataURI("image/jpeg", goodBody), "grbpwr", mock.Anything).
		Return(tcflMediaFull(7001, "https://cdn/og.webp"), nil).Once()
	fs.EXPECT().UploadContentImage(mock.Anything, tcflDataURI("image/jpeg", badBody), "grbpwr", mock.Anything).
		Return(nil, errors.New("507 insufficient storage")).Once()

	p, err := s.tcflPlaceImportFiles(t.Context(), arch, res)
	require.NoError(t, err, "one file the bucket would not take costs one picture, not the card")
	require.Equal(t, []string{"https://cdn/og.webp"}, p.uploadedKeys())

	items := res.Insert.GetTechnicalMedia()
	require.Len(t, items, 1, "the slot whose picture never landed is dropped — the converter refuses media_id <= 0")
	require.EqualValues(t, 7001, items[0].GetMediaId())

	holes := tcimpHoles(res, techcardarchive.ReasonMediaUploadFailed)
	require.Len(t, holes, 1)
	require.Equal(t, "media_id=4021", holes[0].Ref)
	require.Equal(t, techcardarchive.EntityMedia, holes[0].Entity)
	require.Empty(t, tcimpHoles(res, techcardarchive.ReasonMediaObjectMissing),
		"media_object_missing is the EXPORT's code; the bytes were here and this instance refused them")

	tally := tcimpTally(t, res, techcardarchive.EntityMedia)
	require.Equal(t, 1, tally.Imported, "the resolver counted two PLANNED uploads; only one happened")
	require.Equal(t, 1, tally.Skipped)
}

// ────────────────────────────── 3. the digest, and the moment it is final ──────────────────────

// The failure this is built around: a digest is proved only at io.EOF, so a consumer that streamed
// the entry into the bucket would have handed over the whole file before learning it was wrong.
//
// The fixture makes the check land as late as it possibly can — an entry name carrying no digest of
// its own, so the index's is the only one and it is checked after the last byte. The strict bucket
// mock, with no upload expectation at all, is the assertion: not one byte reached it.
//
// The second subtest is the positive control. The same archive with an HONEST digest must upload —
// otherwise the first subtest would be green for a reader that refuses everything, and would prove
// nothing at all.
func TestArchiveFilesPlanADigestIsProvedBeforeAnyByteReachesTheBucket(t *testing.T) {
	body := tcflJPEG("the real bytes")

	build := func(t *testing.T, declared string) (*Server, *mocks.MockFileStore, *techcardarchive.Archive, *resolvedTechCardImport) {
		t.Helper()
		s, media, fs := tcflServer(t)
		a := tcimpNewArchive()
		a.insert.TechnicalMedia = []*pb_common.TechCardMediaItem{{MediaId: 4020}}
		// NOT a content-addressed name: with no digest in the name, openFile has nothing to
		// compare the index against up front, so the only verification left is the streaming one
		// that completes at EOF.
		a.with("media/photo.jpg", body)
		a.with(techcardarchive.FileMediaIndex, tcimpJSON(t, []techcardarchive.MediaIndexEntry{
			{Ref: 4020, File: "media/photo.jpg", SHA256: declared},
		}))
		media.EXPECT().FindMediaByContentHash(mock.Anything, declared).Return(nil, nil)

		arch := a.open(t)
		res, err := s.resolveTechCardImport(t.Context(), arch)
		require.NoError(t, err)
		return s, fs, arch, res
	}

	t.Run("a digest that only fails at the last byte uploads nothing", func(t *testing.T) {
		s, _, arch, res := build(t, tcflSHA([]byte("something else entirely")))

		p, err := s.tcflPlaceImportFiles(t.Context(), arch, res)
		require.Error(t, err)
		require.Nil(t, p)
		require.ErrorIs(t, err, techcardarchive.ErrCorrupt)
		require.True(t, techcardarchive.IsFatal(err))
		require.Empty(t, tcimpHoles(res, techcardarchive.ReasonMediaUploadFailed),
			"corruption is not a hole (FORMAT.md §1.2) — it ends the import instead of costing one picture")
	})

	t.Run("the same archive with an honest digest does upload", func(t *testing.T) {
		s, fs, arch, res := build(t, tcflSHA(body))
		fs.EXPECT().UploadContentImage(mock.Anything, tcflDataURI("image/jpeg", body), "grbpwr", mock.Anything).
			Return(tcflMediaFull(7001, "https://cdn/og.webp"), nil).Once()

		p, err := s.tcflPlaceImportFiles(t.Context(), arch, res)
		require.NoError(t, err)
		require.Equal(t, []string{"https://cdn/og.webp"}, p.uploadedKeys())
		require.EqualValues(t, 7001, res.Insert.GetTechnicalMedia()[0].GetMediaId())
	})
}

// A failure INSIDE this phase is this phase's to clean up: whatever the first file put in the bucket
// must be gone before the error reaches the caller, and the caller is handed nil so it cannot
// compensate the same objects a second time.
func TestArchiveFilesPlanCompensatesItsOwnFailure(t *testing.T) {
	s, media, fs := tcflServer(t)
	a := tcimpNewArchive()
	a.insert.TechnicalMedia = []*pb_common.TechCardMediaItem{{MediaId: 4020}, {MediaId: 4021}}
	goodBody := tcflJPEG("lands first")
	good, goodSHA := a.blob(techcardarchive.DirMedia, ".jpg", goodBody)
	a.with("media/liar.jpg", tcflJPEG("corrupt"))
	lie := tcflSHA([]byte("not what that entry holds"))
	a.with(techcardarchive.FileMediaIndex, tcimpJSON(t, []techcardarchive.MediaIndexEntry{
		{Ref: 4020, File: good, SHA256: goodSHA},
		{Ref: 4021, File: "media/liar.jpg", SHA256: lie},
	}))
	media.EXPECT().FindMediaByContentHash(mock.Anything, mock.Anything).Return(nil, nil).Twice()

	arch := a.open(t)
	res, err := s.resolveTechCardImport(t.Context(), arch)
	require.NoError(t, err)

	fs.EXPECT().UploadContentImage(mock.Anything, tcflDataURI("image/jpeg", goodBody), "grbpwr", mock.Anything).
		Return(tcflMediaFull(7001, "https://cdn/og.webp", "https://cdn/t.webp"), nil).Once()

	// The row goes BEFORE its objects, and the order is asserted rather than described: objects
	// deleted under a surviving row leave a row that still carries the archive's content_hash, and
	// the next import of the same archive would «reuse» it into a card full of 404s.
	var order []string
	media.EXPECT().DeleteMediaById(mock.Anything, 7001).
		Run(func(context.Context, int) { order = append(order, "row") }).Return(nil).Once()
	fs.EXPECT().DeleteObjects(mock.Anything, "https://cdn/og.webp", "https://cdn/t.webp").
		Run(func(context.Context, ...string) { order = append(order, "objects") }).Return(nil).Once()

	p, err := s.tcflPlaceImportFiles(t.Context(), arch, res)
	require.Error(t, err)
	require.Nil(t, p, "a caller cannot double-compensate what it was never handed")
	require.Equal(t, []string{"row", "objects"}, order)
}

// A cancelled context must not read as «the bucket refused every picture you have». Left to run, the
// loop would write one media_upload_failed line per file and hand back a card with no pictures at
// all — which is indistinguishable, on screen, from an archive whose files were genuinely rejected.
func TestArchiveFilesPlanACancelledContextStopsInsteadOfHollowingTheCard(t *testing.T) {
	s, media, fs := tcflServer(t)
	a := tcimpNewArchive()
	a.insert.TechnicalMedia = []*pb_common.TechCardMediaItem{{MediaId: 4020}, {MediaId: 4021}}
	firstBody, secondBody := tcflJPEG("lands"), tcflJPEG("never gets the chance")
	first, firstSHA := a.blob(techcardarchive.DirMedia, ".jpg", firstBody)
	second, secondSHA := a.blob(techcardarchive.DirMedia, ".jpg", secondBody)
	a.with(techcardarchive.FileMediaIndex, tcimpJSON(t, []techcardarchive.MediaIndexEntry{
		{Ref: 4020, File: first, SHA256: firstSHA},
		{Ref: 4021, File: second, SHA256: secondSHA},
	}))
	media.EXPECT().FindMediaByContentHash(mock.Anything, mock.Anything).Return(nil, nil).Twice()

	arch := a.open(t)
	res, err := s.resolveTechCardImport(t.Context(), arch)
	require.NoError(t, err)

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	fs.EXPECT().UploadContentImage(mock.Anything, tcflDataURI("image/jpeg", firstBody), "grbpwr", mock.Anything).
		Return(tcflMediaFull(7001, "https://cdn/og.webp"), nil).Once()
	fs.EXPECT().UploadContentImage(mock.Anything, tcflDataURI("image/jpeg", secondBody), "grbpwr", mock.Anything).
		Run(func(context.Context, string, string, string) { cancel() }).
		Return(nil, context.Canceled).Once()
	// Compensation runs on a context detached from the cancelled one — otherwise the cleanup of a
	// cancelled import would itself be cancelled, which is how the orphan is created.
	media.EXPECT().DeleteMediaById(mock.Anything, 7001).Return(nil).Once()
	fs.EXPECT().DeleteObjects(mock.Anything, "https://cdn/og.webp").Return(nil).Once()

	p, err := s.tcflPlaceImportFiles(ctx, arch, res)
	require.Error(t, err)
	require.Nil(t, p)
	require.Empty(t, tcimpHoles(res, techcardarchive.ReasonMediaUploadFailed),
		"a dead context is not a verdict about any file")
}

// ────────────────────────────── 4. R2-1: the substitution the brief asked for ──────────────────

// THE POSITIVE CONTROL FOR THE WHOLE PHASE. The brief for Ф3.1 said to build a «source id → new id»
// map and run RemapIntFieldsDeep over the insert. This proves what that would actually do — and
// therefore that the test above is not green by accident.
//
// The insert arriving here is ALREADY remapped: a reused picture carries its final id in this base,
// an upload carries a negative placeholder, and no source-instance number is left in the tree at
// all. RemapIntFieldsDeep clears every non-zero value it cannot find in the mapping, so a
// placeholder-keyed mapping finds neither and empties both.
func TestArchiveFilesPlanTheBriefsRemapWouldEmptyEveryReusedPicture(t *testing.T) {
	base := &pb_common.TechCardInsert{
		TechnicalMedia: []*pb_common.TechCardMediaItem{{MediaId: 9001}, {MediaId: -1}},
		Details:        []*pb_common.TechCardDetail{{Key: "d", MediaIds: []int32{9001, -1}}},
		Callouts:       []*pb_common.TechCardCallout{{Number: 1, MediaId: 0}},
	}

	wrong, ok := proto.Clone(base).(*pb_common.TechCardInsert)
	require.True(t, ok)
	techcardarchive.RemapIntFieldsDeep(wrong.ProtoReflect(), techcardarchive.MediaFieldNames,
		map[int64]int64{-1: 7001}, func(string, int64) {})
	require.Zero(t, wrong.GetTechnicalMedia()[0].GetMediaId(),
		"the cancelled instruction wipes the picture this base ALREADY had — the commonest slot of all")
	require.Equal(t, []int32{7001}, wrong.GetDetails()[0].GetMediaIds(),
		"and drops the reused id out of every repeated FK on the way")

	right, ok := proto.Clone(base).(*pb_common.TechCardInsert)
	require.True(t, ok)
	tcflSubstituteMediaPlaceholders(right.ProtoReflect(), map[int32]int32{-1: 7001}, nil)
	require.EqualValues(t, 9001, right.GetTechnicalMedia()[0].GetMediaId(), "a positive id is a row of THIS base: untouched")
	require.EqualValues(t, 7001, right.GetTechnicalMedia()[1].GetMediaId(), "a negative id is a placeholder: substituted")
	require.Equal(t, []int32{9001, 7001}, right.GetDetails()[0].GetMediaIds())
	require.Zero(t, right.GetCallouts()[0].GetMediaId(), "0 stays 0 — «not anchored to a picture» is not a miss")
}

// ────────────────────────────── 5. patterns ────────────────────────────────────────────────────

func TestArchiveFilesPlanPatternSheetsCarryTheirNewURLsAndALostSheetLeaves(t *testing.T) {
	s, _, fs := tcflServer(t)
	a := tcimpNewArchive()
	a.insert.Patterns = []*pb_common.TechCardSizePattern{{LineKey: "P1"}, {LineKey: "P2"}}
	dxfBody, pdfBody := []byte("0\nSECTION\n"), []byte("%PDF-1.4\n")
	dxf, dxfSHA := a.blob(techcardarchive.DirPatterns, ".dxf", dxfBody)
	pdf, pdfSHA := a.blob(techcardarchive.DirPatterns, ".pdf", pdfBody)
	a.with(techcardarchive.FilePatternsIndex, tcimpJSON(t, []techcardarchive.PatternIndexEntry{
		{LineKey: "P1", File: dxf, SHA256: dxfSHA, Filename: "front_v3.dxf"},
		{LineKey: "P2", File: pdf, SHA256: pdfSHA, Filename: "back.pdf"},
	}))

	arch := a.open(t)
	res, err := s.resolveTechCardImport(t.Context(), arch)
	require.NoError(t, err)
	require.Len(t, res.Insert.GetPatterns(), 2)
	require.Empty(t, res.Insert.GetPatterns()[0].GetUrl(), "the resolver leaves the url blank on purpose")

	fs.EXPECT().UploadPatternFile(mock.Anything, dxfBody, mock.Anything).
		Return("https://cdn/tech-card-patterns/p1.dxf", int64(len(dxfBody)), nil).Once()
	fs.EXPECT().UploadPatternFile(mock.Anything, pdfBody, mock.Anything).
		Return("", int64(0), errors.New("s3 refused the object")).Once()

	p, err := s.tcflPlaceImportFiles(t.Context(), arch, res)
	require.NoError(t, err)
	require.Equal(t, []string{"https://cdn/tech-card-patterns/p1.dxf"}, p.uploadedKeys())

	sheets := res.Insert.GetPatterns()
	require.Len(t, sheets, 1, "a sheet with no object must leave the payload: a blank url fails the converter, and that would cost the whole card")
	require.Equal(t, "P1", sheets[0].GetLineKey())
	require.Equal(t, "https://cdn/tech-card-patterns/p1.dxf", sheets[0].GetUrl())
	require.EqualValues(t, len(dxfBody), sheets[0].GetSizeBytes())

	holes := tcimpHoles(res, techcardarchive.ReasonPatternInvalid)
	require.Len(t, holes, 1)
	require.Equal(t, "line_key=P2", holes[0].Ref)
	require.Equal(t, techcardarchive.EntityPattern, holes[0].Entity)

	tally := tcimpTally(t, res, techcardarchive.EntityPattern)
	require.Equal(t, 1, tally.Imported)
	require.Equal(t, 1, tally.Skipped)
}

// ────────────────────────────── 6. routing ─────────────────────────────────────────────────────

// Video bytes and image bytes go to different paths, and the extension is not the only witness: a
// file whose magic numbers name a container is routed by THOSE, and the extension decides only what
// may be uploaded at all and what to do when the bytes say nothing.
//
// The last file is the R2-6 case: an extension outside FORMAT.md §1.1 is a hole ON SIGHT. Nothing
// reads it, nothing decodes it — the strict bucket mock, expecting exactly two calls, is what
// proves the third file never reached one.
func TestArchiveFilesPlanRoutesVideoByBytesAndRefusesAForeignExtension(t *testing.T) {
	s, media, fs := tcflServer(t)
	a := tcimpNewArchive()
	a.insert.TechnicalMedia = []*pb_common.TechCardMediaItem{{MediaId: 4020}, {MediaId: 4021}, {MediaId: 4022}}
	// Matroska magic under an .mp4 name: the BYTES win, so this must go up as video/webm.
	webmBody := append([]byte{0x1A, 0x45, 0xDF, 0xA3}, "matroska"...)
	// No magic this router knows: the extension is the only witness left, and it says mp4.
	mp4Body := []byte("no magic here, just a claim")
	tiffBody := []byte("II*\x00 a tiff nobody asked for")
	webm, webmSHA := a.blob(techcardarchive.DirMedia, ".mp4", webmBody)
	mp4, mp4SHA := a.blob(techcardarchive.DirMedia, ".mp4", mp4Body)
	tiff, tiffSHA := a.blob(techcardarchive.DirMedia, ".tiff", tiffBody)
	a.with(techcardarchive.FileMediaIndex, tcimpJSON(t, []techcardarchive.MediaIndexEntry{
		{Ref: 4020, File: webm, SHA256: webmSHA},
		{Ref: 4021, File: mp4, SHA256: mp4SHA},
		{Ref: 4022, File: tiff, SHA256: tiffSHA},
	}))
	media.EXPECT().FindMediaByContentHash(mock.Anything, mock.Anything).Return(nil, nil).Times(3)

	arch := a.open(t)
	res, err := s.resolveTechCardImport(t.Context(), arch)
	require.NoError(t, err)

	fs.EXPECT().UploadContentVideo(mock.Anything, webmBody, "grbpwr", mock.Anything, "video/webm").
		Return(tcflMediaFull(7001, "https://cdn/a.webm", "https://cdn/a.webm", "https://cdn/a.webm"), nil).Once()
	fs.EXPECT().UploadContentVideo(mock.Anything, mp4Body, "grbpwr", mock.Anything, "video/mp4").
		Return(tcflMediaFull(7002, "https://cdn/b.mp4", "https://cdn/b.mp4", "https://cdn/b.mp4"), nil).Once()

	p, err := s.tcflPlaceImportFiles(t.Context(), arch, res)
	require.NoError(t, err)

	items := res.Insert.GetTechnicalMedia()
	require.Len(t, items, 2, "the file with an extension the format does not list leaves the card")
	require.EqualValues(t, 7001, items[0].GetMediaId())
	require.EqualValues(t, 7002, items[1].GetMediaId())

	holes := tcimpHoles(res, techcardarchive.ReasonMediaUploadFailed)
	require.Len(t, holes, 1)
	require.Equal(t, "media_id=4022", holes[0].Ref)
	require.Contains(t, holes[0].Detail, "nothing was read or decoded")

	// A video row stores ONE object behind three urls. The list is passed on as it stands —
	// DeleteObjects collapses it by object key — so the record must not have quietly tidied it.
	require.Len(t, p.uploadedKeys(), 6)
}
