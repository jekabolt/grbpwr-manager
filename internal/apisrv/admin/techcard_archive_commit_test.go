package admin

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"testing"

	"github.com/jekabolt/grbpwr-manager/internal/dependency"
	mocks "github.com/jekabolt/grbpwr-manager/internal/dependency/mocks"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/jekabolt/grbpwr-manager/internal/techcardarchive"
	pb_admin "github.com/jekabolt/grbpwr-manager/proto/gen/admin"
	pb_common "github.com/jekabolt/grbpwr-manager/proto/gen/common"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
	"google.golang.org/genproto/googleapis/rpc/errdetails"
	pbdecimal "google.golang.org/genproto/googleapis/type/decimal"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/encoding/protojson"
)

// ─────────────────────────────────────────────────────────────────────────────
// Ф3.3 — the questions this file asks of the commit.
//
// The commit is the one call in the import path that cannot be undone by not calling it again, so
// every case below is about a way of losing something that only the WRITE can lose:
//
//	1. a second press making a second card;
//	2. a style number taken between the dry run and the click — and the retry re-uploading the files
//	   it already moved, or looping on somebody else's unique index;
//	3. a failed transaction leaving its pictures and sheets behind;
//	4. an AMBIGUOUS commit — the server applied it, the answer was lost — being «cleaned up», which
//	   deletes the pattern objects of a card that is alive;
//	5. a reused picture disappearing under a parallel import and taking the whole card down with it
//	   instead of leaving a hole;
//	6. the answer carrying the report this handler built rather than the one the transaction stored,
//	   which understates every row the write dropped.
//
// The archive is the resolver's own fixture (tcimp*): a real ZIP with real CRCs, opened by the real
// reader and resolved by the real resolver. The mocks are STRICT, so «nothing else was touched» —
// no second upload, no compensation — needs no assertion of its own: an unexpected call fails the
// test. Helpers here are `tcci*`.
// ─────────────────────────────────────────────────────────────────────────────

const tcciTestImportID = "AAAAAAAAAAAAAAAAAAAAAAAAAA"

const tcciTestObjectKey = techcardarchive.BucketPrefixImports + tcciTestImportID + ".zip"

// tcciRig is a Server whose repository, media table and bucket are all strict mocks.
type tcciRig struct {
	s     *Server
	repo  *mocks.MockRepository
	cards *mocks.MockTechCards
	media *mocks.MockMedia
	fs    *mocks.MockFileStore
}

func tcciNewRig(t *testing.T) *tcciRig {
	t.Helper()
	s, repo, cards, media := tcimpServer(t)
	fs := mocks.NewMockFileStore(t)
	fs.EXPECT().GetBaseFolder().Return("grbpwr").Maybe()
	s.bucket = fs
	return &tcciRig{s: s, repo: repo, cards: cards, media: media, fs: fs}
}

// classifies scripts the two error classifiers. They are set PER CASE and never defaulted in the
// rig: mockery answers with the FIRST expectation whose arguments match, so a permissive default
// would shadow the override and every «this was a duplicate key» case would silently become «an
// ordinary failure» — green, and testing nothing.
func (r *tcciRig) classifies(unique, foreignKey bool) {
	r.repo.EXPECT().IsErrUniqueViolation(mock.Anything).Return(unique).Maybe()
	r.repo.EXPECT().IsErrForeignKeyViolation(mock.Anything).Return(foreignKey).Maybe()
}

// tcciZip writes the fixture archive out as bytes.
//
// It repeats tcimpArchive.open's construction because that helper hands back an opened *Archive and
// the commit reads its bytes out of the bucket — the object key round trip is part of what is under
// test here, so the ZIP has to exist as bytes before the handler runs.
func tcciZip(t *testing.T, a *tcimpArchive) []byte {
	t.Helper()
	card := &pb_common.TechCard{Id: 214, TechCard: a.insert}
	if a.outer != nil {
		a.outer(card)
	}
	cardJSON, err := protojson.Marshal(card)
	require.NoError(t, err)
	files := map[string][]byte{
		techcardarchive.FileManifest: tcimpJSON(t, a.manifest),
		techcardarchive.FileCard:     cardJSON,
	}
	for k, v := range a.files {
		files[k] = v
	}
	return tcimpZip(t, files)
}

// serve makes the bucket hand the archive back the way the real one does.
func (r *tcciRig) serve(t *testing.T, raw []byte) {
	t.Helper()
	r.fs.EXPECT().GetImportObjectReaderAt(mock.Anything, tcciTestObjectKey).
		RunAndReturn(func(context.Context, string) (dependency.ReaderAtCloser, int64, error) {
			return tcupReaderAt{bytes.NewReader(raw)}, int64(len(raw)), nil
		}).Maybe()
}

// tcciRow is the tech_card_import row as the handler reads it.
func tcciRow(status string, techCardID int, report []byte) entity.TechCardArchiveImportRecord {
	row := entity.TechCardArchiveImportRecord{
		ImportID: tcciTestImportID, ObjectKey: tcciTestObjectKey, Status: status, Report: report,
	}
	if techCardID > 0 {
		row.TechCardID = sql.NullInt32{Int32: int32(techCardID), Valid: true}
	}
	return row
}

// rows scripts the successive answers to GetTechCardImportByImportID: the pre-flight check, then
// whatever the settlement or the read-back sees. The last entry answers every call after it.
func (r *tcciRig) rows(answers ...func() (entity.TechCardArchiveImportRecord, error)) *int {
	calls := new(int)
	r.cards.EXPECT().GetTechCardImportByImportID(mock.Anything, tcciTestImportID).
		RunAndReturn(func(context.Context, string) (entity.TechCardArchiveImportRecord, error) {
			i := *calls
			*calls++
			if i >= len(answers) {
				i = len(answers) - 1
			}
			return answers[i]()
		})
	return calls
}

func tcciRowOK(status string, techCardID int, report []byte) func() (entity.TechCardArchiveImportRecord, error) {
	return func() (entity.TechCardArchiveImportRecord, error) { return tcciRow(status, techCardID, report), nil }
}

func tcciRowErr(err error) func() (entity.TechCardArchiveImportRecord, error) {
	return func() (entity.TechCardArchiveImportRecord, error) {
		return entity.TechCardArchiveImportRecord{}, err
	}
}

// tcciAttempt is a SNAPSHOT of one ImportTechCardArchive call.
//
// A snapshot and not the struct itself, because the payload is a pointer the handler mutates
// between attempts: keeping `in` would make the first attempt's style number read as the second's,
// and the retry test would pass in a world with no retry at all.
type tcciAttempt struct {
	importID     string
	actor        string
	sourceHost   string
	styleNumber  string
	numberSource entity.StyleNumberSource
	stage        entity.TechCardStage
	mediaIDs     []int
	markers      []entity.TechCardMarkerInsert
	report       *pb_admin.TechCardImportReport
}

// expectImport scripts the store's answers, one per attempt, and records what each attempt carried.
func (r *tcciRig) expectImport(t *testing.T, answers ...func() (int, error)) *[]tcciAttempt {
	t.Helper()
	seen := new([]tcciAttempt)
	r.cards.EXPECT().ImportTechCardArchive(mock.Anything, mock.Anything).
		RunAndReturn(func(_ context.Context, in entity.TechCardArchiveImport) (int, error) {
			at := tcciAttempt{
				importID: in.ImportID, actor: in.Actor, sourceHost: in.SourceHost,
				styleNumber: in.Card.StyleNumber.String, numberSource: in.Card.StyleNumberSource,
				stage: in.Card.Stage,
			}
			for _, m := range in.Card.Media {
				at.mediaIDs = append(at.mediaIDs, m.MediaId)
			}
			at.markers = in.Markers
			rep := &pb_admin.TechCardImportReport{}
			require.NoError(t, protojson.Unmarshal(in.Report, rep),
				"the store is handed a report it can parse — it refuses anything else before writing a row")
			at.report = rep
			*seen = append(*seen, at)

			i := len(*seen) - 1
			if i >= len(answers) {
				i = len(answers) - 1
			}
			return answers[i]()
		})
	return seen
}

func tcciImported(id int) func() (int, error) { return func() (int, error) { return id, nil } }
func tcciFails(err error) func() (int, error) { return func() (int, error) { return 0, err } }

// tcciArchiveOneUpload is the fixture every case starts from: a card with one picture the target
// base does NOT hold, so exactly one file is uploaded and there is something to compensate.
func tcciArchiveOneUpload(t *testing.T, r *tcciRig) *tcimpArchive {
	t.Helper()
	a := tcimpNewArchive()
	a.insert.TechnicalMedia = []*pb_common.TechCardMediaItem{{MediaId: 4021}}
	fresh, freshSHA := a.blob(techcardarchive.DirMedia, ".jpg", tcflJPEG("brand new"))
	a.with(techcardarchive.FileMediaIndex, tcimpJSON(t, []techcardarchive.MediaIndexEntry{
		{Ref: 4021, File: fresh, SHA256: freshSHA},
	}))
	r.media.EXPECT().FindMediaByContentHash(mock.Anything, freshSHA).Return(nil, nil).Maybe()
	return a
}

// tcciExpectUpload allows exactly ONE upload of the fixture's picture. `Once()` is half the proof of
// the retry test: a second attempt that re-placed the files would fail here.
func (r *tcciRig) tcciExpectUpload(mediaID int32) {
	r.fs.EXPECT().UploadContentImageVerbatim(mock.Anything, tcflJPEG("brand new"), "grbpwr", mock.Anything).
		Return(tcflMediaFull(mediaID, "https://cdn/og.webp", "https://cdn/c.webp", "https://cdn/t.webp"), nil).Once()
}

// tcciStoredReport is a report with a line NOTHING in this handler builds — the mark of «the store
// amended this inside its transaction». Whatever comes back on the wire either carries that line or
// is not the stored document.
func tcciStoredReport(t *testing.T, styleNumber string) []byte {
	t.Helper()
	raw, err := techcardarchive.MarshalReport(techcardarchive.BuildReport(techcardarchive.ReportInput{
		ImportID:    tcciTestImportID,
		StyleNumber: styleNumber,
		Stage:       string(entity.TechCardStageProto),
		Counters:    techcardarchive.NewCounters(),
		Holes: []techcardarchive.ImportHole{{
			Entity: techcardarchive.EntitySize, Ref: "size_name=xxl",
			Status: techcardarchive.StatusSkipped, Reason: techcardarchive.ReasonSizeUnknown,
			Detail: "the write dropped this chart row: the imported card does not make that size",
		}},
	}))
	require.NoError(t, err)
	return raw
}

func tcciReportLines(rep *pb_admin.TechCardImportReport, reason techcardarchive.Reason) []*pb_admin.TechCardImportReportLine {
	out := []*pb_admin.TechCardImportReportLine{}
	for _, l := range rep.GetLines() {
		if l.GetReason() == string(reason) {
			out = append(out, l)
		}
	}
	return out
}

func tcciCommitCall(t *testing.T, s *Server) (*pb_admin.CommitTechCardImportResponse, error) {
	t.Helper()
	return s.CommitTechCardImport(tcupWriterCtx(),
		&pb_admin.CommitTechCardImportRequest{ImportId: tcciTestImportID})
}

// ────────────────────────────── 1. one archive, one card ──────────────────────────────

// THE SECOND PRESS MUST NOT MAKE A SECOND CARD, and must say which card the first one made.
//
// The whole rig is strict and nothing but the row read is expected: an implementation that opened
// the archive, resolved it and only then discovered the collision would fail on the bucket call —
// which is the point. The refusal happens before a byte of the archive is touched.
func TestCommitTechCardImportRefusesASecondCommitAndNamesTheFirstCard(t *testing.T) {
	r := tcciNewRig(t)
	r.rows(tcciRowOK(entity.TechCardImportStatusCommitted, 77, nil))

	resp, err := tcciCommitCall(t, r.s)
	require.Nil(t, resp)
	st, ok := status.FromError(err)
	require.True(t, ok)
	require.Equal(t, codes.FailedPrecondition, st.Code(), "a double click is a precondition, not a fault")
	require.Contains(t, st.Message(), "77", "the operator is told WHICH card already exists")

	var info *errdetails.ErrorInfo
	for _, d := range st.Details() {
		if ei, is := d.(*errdetails.ErrorInfo); is {
			info = ei
		}
	}
	require.NotNil(t, info, "a panel must be able to navigate to the card without parsing English")
	require.Equal(t, tcciReasonAlreadyCommitted, info.GetReason())
	require.Equal(t, "77", info.GetMetadata()["tech_card_id"])
}

// An upload row that is neither fresh nor finished is refused with its own word, and never silently
// treated as committable.
func TestCommitTechCardImportRefusesAnExpiredUpload(t *testing.T) {
	r := tcciNewRig(t)
	r.rows(tcciRowOK(entity.TechCardImportStatusExpired, 0, nil))

	_, err := tcciCommitCall(t, r.s)
	st, _ := status.FromError(err)
	require.Equal(t, codes.FailedPrecondition, st.Code())
	require.Contains(t, st.Message(), "expired")
}

// An import id nobody uploaded is NOT_FOUND — a distinct answer from «already imported», because
// the two send the operator to two different places.
func TestCommitTechCardImportUnknownIdIsNotFound(t *testing.T) {
	r := tcciNewRig(t)
	r.rows(tcciRowErr(sql.ErrNoRows))

	_, err := tcciCommitCall(t, r.s)
	st, _ := status.FromError(err)
	require.Equal(t, codes.NotFound, st.Code())
}

// ────────────────────────────── 2. the number was taken ──────────────────────────────

// A style number taken between the dry run and the click costs the NUMBER, not the import.
//
// Three things are proved together, and the third is the one a naive retry gets wrong:
//   - the second attempt carries a server-proposed number with source=generated;
//   - the report the store is handed says the FINAL number and carries the style_number_taken line;
//   - THE FILES ARE NOT MOVED AGAIN. The upload is expected exactly Once, so a retry that re-placed
//     the archive's files — minting a second media row per picture and orphaning the first — fails
//     here rather than passing quietly.
func TestCommitTechCardImportRetriesWithAGeneratedStyleNumber(t *testing.T) {
	r := tcciNewRig(t)
	a := tcciArchiveOneUpload(t, r)
	a.insert.SkuSeason = &pb_common.SkuSeason{Code: pb_common.SeasonEnum_SEASON_ENUM_SS, Year: 2026}
	r.serve(t, tcciZip(t, a))
	r.tcciExpectUpload(7001)
	r.rows(tcciRowOK(entity.TechCardImportStatusUploaded, 0, nil),
		tcciRowOK(entity.TechCardImportStatusCommitted, 101, tcciStoredReport(t, "SS26-0007")))

	taken := errors.New("Error 1062: Duplicate entry 'GRB-SS26-014' for key 'style_number'")
	r.classifies(true, false)
	r.cards.EXPECT().ListTechCards(mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything).
		Return([]entity.TechCard{{TechCardInsert: entity.TechCardInsert{
			StyleNumber: sql.NullString{String: "GRB-SS26-014", Valid: true}}}}, 1, nil)
	r.cards.EXPECT().SuggestStyleNumber(mock.Anything, "SS", 2026).Return("SS26-0007", nil)

	seen := r.expectImport(t, tcciFails(taken), tcciImported(101))

	resp, err := tcciCommitCall(t, r.s)
	require.NoError(t, err)
	require.EqualValues(t, 101, resp.GetTechCardId())

	require.Len(t, *seen, 2, "the number is retried exactly once, in a second transaction")
	require.Equal(t, "GRB-SS26-014", (*seen)[0].styleNumber, "the first attempt asks for the archive's number")
	require.Equal(t, "SS26-0007", (*seen)[1].styleNumber)
	require.Equal(t, entity.StyleNumberSourceGenerated, (*seen)[1].numberSource,
		"a number the server proposed is not a manual override")
	require.Equal(t, entity.TechCardStageProto, (*seen)[1].stage, "a card with a number keeps its stage")

	rep := (*seen)[1].report
	require.Equal(t, "SS26-0007", rep.GetStyleNumber(),
		"style_number in the report is FINAL — as the card landed here, not as the archive asked")
	lines := tcciReportLines(rep, techcardarchive.ReasonStyleNumberTaken)
	require.Len(t, lines, 1)
	require.Contains(t, lines[0].GetDetail(), "SS26-0007")
	require.NotEmpty(t, lines[0].GetAction(), "every line carries the sentence that closes it")
}

// With NO SEASON there is nothing to generate a replacement from, so the card lands with no number
// at all — and the contract forces `idea` with it, because a style number is required from `proto`
// onward and a numberless card at any later stage is unwritable.
func TestCommitTechCardImportWithoutASeasonLandsNumberlessAtIdea(t *testing.T) {
	r := tcciNewRig(t)
	a := tcciArchiveOneUpload(t, r)
	r.serve(t, tcciZip(t, a))
	r.tcciExpectUpload(7001)
	r.rows(tcciRowOK(entity.TechCardImportStatusUploaded, 0, nil),
		tcciRowOK(entity.TechCardImportStatusCommitted, 102, tcciStoredReport(t, "")))

	r.classifies(true, false)
	r.cards.EXPECT().ListTechCards(mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything).
		Return([]entity.TechCard{{TechCardInsert: entity.TechCardInsert{
			StyleNumber: sql.NullString{String: "GRB-SS26-014", Valid: true}}}}, 1, nil)

	seen := r.expectImport(t, tcciFails(errors.New("Error 1062: Duplicate entry")), tcciImported(102))

	_, err := tcciCommitCall(t, r.s)
	require.NoError(t, err)
	require.Len(t, *seen, 2)
	require.Empty(t, (*seen)[1].styleNumber)
	require.Equal(t, entity.TechCardStageIdea, (*seen)[1].stage,
		"a numberless card can only be an idea — every later stage requires a number")
	require.Len(t, tcciReportLines((*seen)[1].report, techcardarchive.ReasonStyleNumberTaken), 1)
}

// A 1062 THAT IS NOT THE STYLE NUMBER MUST NOT RENAME ANYTHING.
//
// The driver reports one error number for every unique index in the schema, and a card write
// touches two (style_number and the equipment profile key of 0306). An implementation that renamed
// on the strength of the code alone would mint a fresh number and retry the identical conflict —
// forever. Here the number reads back as FREE, so the failure is passed through untouched and the
// store is called exactly once.
func TestCommitTechCardImportDoesNotRenameOnSomebodyElsesUniqueIndex(t *testing.T) {
	r := tcciNewRig(t)
	a := tcciArchiveOneUpload(t, r)
	r.serve(t, tcciZip(t, a))
	r.tcciExpectUpload(7001)
	r.rows(tcciRowOK(entity.TechCardImportStatusUploaded, 0, nil),
		tcciRowOK(entity.TechCardImportStatusUploaded, 0, nil))

	r.classifies(true, false)
	// Nobody here carries that number: the conflict was another index.
	r.cards.EXPECT().ListTechCards(mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything).
		Return(nil, 0, nil)
	// The transaction rolled back, so the files this import moved are taken back.
	r.media.EXPECT().DeleteMediaById(mock.Anything, 7001).Return(nil).Once()
	r.fs.EXPECT().DeleteObjects(mock.Anything, "https://cdn/og.webp", "https://cdn/c.webp", "https://cdn/t.webp").
		Return(nil).Once()

	seen := r.expectImport(t, tcciFails(errors.New("Error 1062: Duplicate entry for key 'uq_equipment_profile_key'")))

	_, err := tcciCommitCall(t, r.s)
	st, _ := status.FromError(err)
	require.Equal(t, codes.Internal, st.Code())
	// SuggestStyleNumber is not expected on this strict mock, so a handler that proposed a number
	// here fails the test without an assertion of its own.
	require.Len(t, *seen, 1, "a duplicate that is not the style number is not retried")
}

// ────────────────────────────── 3. a failed transaction takes its files back ────────────────────

// A commit that rolled back leaves NOTHING behind: the media row it minted and the objects behind
// it are deleted, in that order.
//
// The verdict is read off the import row on a fresh connection — still `uploaded`, therefore the
// claim rolled back with everything else — and only THAT unlocks the deletion.
func TestCommitTechCardImportCompensatesARolledBackTransaction(t *testing.T) {
	r := tcciNewRig(t)
	a := tcciArchiveOneUpload(t, r)
	r.serve(t, tcciZip(t, a))
	r.tcciExpectUpload(7001)
	r.rows(tcciRowOK(entity.TechCardImportStatusUploaded, 0, nil),
		tcciRowOK(entity.TechCardImportStatusUploaded, 0, nil))

	r.classifies(false, false)
	r.media.EXPECT().DeleteMediaById(mock.Anything, 7001).Return(nil).Once()
	r.fs.EXPECT().DeleteObjects(mock.Anything, "https://cdn/og.webp", "https://cdn/c.webp", "https://cdn/t.webp").
		Return(nil).Once()

	r.expectImport(t, tcciFails(errors.New("insert imported tech card: connection reset")))

	resp, err := tcciCommitCall(t, r.s)
	require.Nil(t, resp)
	st, _ := status.FromError(err)
	require.Equal(t, codes.Internal, st.Code())
	require.Contains(t, st.Message(), "no card was created")
}

// ────────────────────────────── 4. the ambiguous commit ──────────────────────────────

// AN ERROR OUT OF THE COMMIT DOES NOT PROVE A ROLLBACK, and this is the case where believing it
// would cost a card.
//
// The row says `committed` with a card id — both written by the SAME transaction, so seeing either
// on a fresh connection is proof the whole thing landed and the failure was on the way back. The
// import is answered as the SUCCESS it is, and nothing is deleted: the strict bucket and media
// mocks expect no deletion at all, so a handler that «cleaned up» here — taking the pattern objects
// of a live card, which no foreign key protects — fails this test.
func TestCommitTechCardImportDoesNotCompensateACommitThatActuallyLanded(t *testing.T) {
	r := tcciNewRig(t)
	a := tcciArchiveOneUpload(t, r)
	r.serve(t, tcciZip(t, a))
	r.tcciExpectUpload(7001)
	stored := tcciStoredReport(t, "GRB-SS26-014")
	r.rows(tcciRowOK(entity.TechCardImportStatusUploaded, 0, nil),
		tcciRowOK(entity.TechCardImportStatusCommitted, 314, stored))

	r.classifies(false, false)
	r.expectImport(t, tcciFails(errors.New("commit: invalid connection")))

	resp, err := tcciCommitCall(t, r.s)
	require.NoError(t, err, "the card exists; failing the RPC would tell the operator otherwise")
	require.EqualValues(t, 314, resp.GetTechCardId())
	require.NotNil(t, resp.GetReport())
}

// WHEN THE OUTCOME CANNOT BE ESTABLISHED, NOTHING IS DELETED EITHER.
//
// An orphaned object costs storage until the sweeper finds it. An object deleted out from under a
// live card costs the card. So «I do not know» is treated exactly like «it committed» for the
// purpose of touching the bucket — and the strict mocks, which expect no deletion, are what proves
// it.
func TestCommitTechCardImportLeavesFilesAloneWhenTheOutcomeIsUnknown(t *testing.T) {
	r := tcciNewRig(t)
	a := tcciArchiveOneUpload(t, r)
	r.serve(t, tcciZip(t, a))
	r.tcciExpectUpload(7001)
	r.rows(tcciRowOK(entity.TechCardImportStatusUploaded, 0, nil),
		tcciRowErr(errors.New("dial tcp: connection refused")))

	r.classifies(false, false)
	r.expectImport(t, tcciFails(errors.New("commit: broken pipe")))

	resp, err := tcciCommitCall(t, r.s)
	require.Nil(t, resp)
	st, _ := status.FromError(err)
	require.Equal(t, codes.Internal, st.Code())
}

// The store refusing the claim inside its own transaction — the race this handler's pre-flight
// check cannot close — is definite: nothing of ours was written, so the files ARE taken back and
// the operator is told which card the winner made.
func TestCommitTechCardImportCompensatesWhenTheStoreSaysAlreadyCommitted(t *testing.T) {
	r := tcciNewRig(t)
	a := tcciArchiveOneUpload(t, r)
	r.serve(t, tcciZip(t, a))
	r.tcciExpectUpload(7001)
	r.rows(tcciRowOK(entity.TechCardImportStatusUploaded, 0, nil),
		tcciRowOK(entity.TechCardImportStatusCommitted, 512, nil))

	r.media.EXPECT().DeleteMediaById(mock.Anything, 7001).Return(nil).Once()
	r.fs.EXPECT().DeleteObjects(mock.Anything, "https://cdn/og.webp", "https://cdn/c.webp", "https://cdn/t.webp").
		Return(nil).Once()

	r.expectImport(t, tcciFails(entity.ErrImportAlreadyCommitted))

	_, err := tcciCommitCall(t, r.s)
	st, _ := status.FromError(err)
	require.Equal(t, codes.FailedPrecondition, st.Code())
	require.Contains(t, st.Message(), "512")
}

// ────────────────────────────── 5. a reused picture that vanished ──────────────────────────────

// A PICTURE THAT DISAPPEARED IS A HOLE, NEVER A REFUSAL.
//
// Ф3.1 mints a media row before this transaction opens, so a parallel import can match it by
// content hash and plan to REUSE it; if the first import then compensates, the row goes and the
// second one's foreign key points at nothing. The owner's binding rule is that a reference which
// cannot be placed is a skip with a line in the report — so the row is dropped from the payload,
// the line is written, and the card still lands.
func TestCommitTechCardImportTurnsAVanishedReusedPictureIntoAHole(t *testing.T) {
	r := tcciNewRig(t)
	a := tcimpNewArchive()
	a.insert.TechnicalMedia = []*pb_common.TechCardMediaItem{{MediaId: 4020}, {MediaId: 4021}}
	here, hereSHA := a.blob(techcardarchive.DirMedia, ".jpg", tcflJPEG("already stored here"))
	fresh, freshSHA := a.blob(techcardarchive.DirMedia, ".jpg", tcflJPEG("brand new"))
	a.with(techcardarchive.FileMediaIndex, tcimpJSON(t, []techcardarchive.MediaIndexEntry{
		{Ref: 4020, File: here, SHA256: hereSHA},
		{Ref: 4021, File: fresh, SHA256: freshSHA},
	}))
	// The resolver still SEES the row: the race is that it disappears afterwards.
	r.media.EXPECT().FindMediaByContentHash(mock.Anything, hereSHA).Return(&entity.MediaFull{Id: 9001}, nil)
	r.media.EXPECT().FindMediaByContentHash(mock.Anything, freshSHA).Return(nil, nil)
	r.serve(t, tcciZip(t, a))
	r.tcciExpectUpload(7001)
	r.rows(tcciRowOK(entity.TechCardImportStatusUploaded, 0, nil),
		tcciRowOK(entity.TechCardImportStatusCommitted, 202, tcciStoredReport(t, "GRB-SS26-014")))

	r.classifies(false, true)
	// …and by the time the write asks, it is gone.
	r.media.EXPECT().GetMediaByIds(mock.Anything, []int{9001}).Return(map[int]entity.MediaFull{}, nil)

	seen := r.expectImport(t,
		tcciFails(errors.New("Error 1452: foreign key constraint fails (`media_id`)")),
		tcciImported(202))

	resp, err := tcciCommitCall(t, r.s)
	require.NoError(t, err, "one picture is lost; the card is not")
	require.EqualValues(t, 202, resp.GetTechCardId())

	require.Len(t, *seen, 2)
	require.Equal(t, []int{9001, 7001}, (*seen)[0].mediaIDs, "the first attempt still points at the reused row")
	require.Equal(t, []int{7001}, (*seen)[1].mediaIDs,
		"the vanished row is dropped from the payload, not written as a dangling id")

	lines := tcciReportLines((*seen)[1].report, techcardarchive.ReasonMediaUploadFailed)
	require.Len(t, lines, 1, "the loss is reported once, against the picture the archive named")
	require.Equal(t, "media_id=4020", lines[0].GetRef())
	require.Equal(t, techcardarchive.StatusSkipped, lines[0].GetStatus())
	for _, c := range (*seen)[1].report.GetCounters() {
		if c.GetEntity() == techcardarchive.EntityMedia {
			require.EqualValues(t, 1, c.GetImported(), "the picture that did land is still counted as imported")
			require.EqualValues(t, 1, c.GetSkipped(), "the lost one MOVED columns rather than vanishing from the tally")
		}
	}
}

// A foreign key that is NOT a media row cannot be repaired here, so it must not be retried: an
// identical second transaction would fail identically, and the files would be settled twice.
func TestCommitTechCardImportDoesNotRetryAForeignKeyItCannotRepair(t *testing.T) {
	r := tcciNewRig(t)
	a := tcciArchiveOneUpload(t, r)
	r.serve(t, tcciZip(t, a))
	r.tcciExpectUpload(7001)
	r.rows(tcciRowOK(entity.TechCardImportStatusUploaded, 0, nil),
		tcciRowOK(entity.TechCardImportStatusUploaded, 0, nil))

	r.classifies(false, true)
	r.media.EXPECT().DeleteMediaById(mock.Anything, 7001).Return(nil).Once()
	r.fs.EXPECT().DeleteObjects(mock.Anything, mock.Anything, mock.Anything, mock.Anything).Return(nil).Once()

	seen := r.expectImport(t, tcciFails(errors.New("Error 1452: foreign key constraint fails (`category_id`)")))

	_, err := tcciCommitCall(t, r.s)
	st, _ := status.FromError(err)
	require.Equal(t, codes.Internal, st.Code())
	require.Len(t, *seen, 1)
}

// ────────────────────────────── 6. the answer is the STORED report ──────────────────────────────

// THE REPORT ON THE WIRE IS THE ONE THE TRANSACTION STORED, not the one this handler built.
//
// The write drops rows only it can know about — chart cells outside the imported card's own size
// range, a grade base whose size did not survive, areas, an assembly line — and amends the report
// with them before stamping it. Answering with the pre-transaction copy would show an operator a
// document that counts those rows as imported; they read it once, believe it, and never look at the
// card again.
//
// The stored fixture carries a line this handler has no way to produce, which is what makes the two
// documents distinguishable at all.
func TestCommitTechCardImportAnswersWithTheReportTheStoreStamped(t *testing.T) {
	r := tcciNewRig(t)
	a := tcciArchiveOneUpload(t, r)
	r.serve(t, tcciZip(t, a))
	r.tcciExpectUpload(7001)
	stored := tcciStoredReport(t, "GRB-SS26-014")
	r.rows(tcciRowOK(entity.TechCardImportStatusUploaded, 0, nil),
		tcciRowOK(entity.TechCardImportStatusCommitted, 404, stored))

	seen := r.expectImport(t, tcciImported(404))

	resp, err := tcciCommitCall(t, r.s)
	require.NoError(t, err)
	require.EqualValues(t, 404, resp.GetTechCardId())

	require.Len(t, *seen, 1)
	require.Empty(t, tcciReportLines((*seen)[0].report, techcardarchive.ReasonSizeUnknown),
		"the fixture's amendment is one this handler cannot build — otherwise the two documents are indistinguishable")
	require.Len(t, tcciReportLines(resp.GetReport(), techcardarchive.ReasonSizeUnknown), 1,
		"what comes back carries the write's own losses, so it is the stored document and not the built one")

	// And the provenance the store journals is the archive's, with this operator as the actor.
	require.Equal(t, tcciTestImportID, (*seen)[0].importID)
	require.Equal(t, "im", (*seen)[0].actor)
	require.Equal(t, "backend.source.example", (*seen)[0].sourceHost)
}

// A read-back that fails does NOT fail the import: the card exists, so the answer carries its id
// with no report rather than a stale one, and the report is still on the card for Ф3.4 to fetch.
func TestCommitTechCardImportSurvivesAnUnreadableStoredReport(t *testing.T) {
	r := tcciNewRig(t)
	a := tcciArchiveOneUpload(t, r)
	r.serve(t, tcciZip(t, a))
	r.tcciExpectUpload(7001)
	r.rows(tcciRowOK(entity.TechCardImportStatusUploaded, 0, nil),
		tcciRowErr(errors.New("dial tcp: connection refused")))

	r.expectImport(t, tcciImported(505))

	resp, err := tcciCommitCall(t, r.s)
	require.NoError(t, err, "the card is in the catalogue; a failed read-back must not say otherwise")
	require.EqualValues(t, 505, resp.GetTechCardId())
	require.Nil(t, resp.GetReport(), "an absent report is honest; a stale one is not")
}

// ────────────────────────────── 7. the payload passes the create's own gates ────────────────────

// THE CAPABILITY SHIELDS RUN ON AN IMPORT TOO, and a payload that fails one is a REFUSAL rather
// than a hole.
//
// «A client would never send that» is not an argument available here: the insert was assembled by
// this server out of a file, and a hand-made archive is a client with no bundle version and no
// manners. A gate failing means the archive contradicts the contract the card tables are written
// under, which is the corrupt-archive case — not the missing-reference case that degrades into a
// report line.
//
// The files ARE taken back: the payload never reached the store, so nothing points at them.
func TestCommitTechCardImportRefusesAPayloadThatFailsAWireGate(t *testing.T) {
	r := tcciNewRig(t)
	a := tcciArchiveOneUpload(t, r)
	// «Drop the photos» in the same payload that carries one. NOT the outdated-bundle branch: the
	// resolver sets all six capability flags itself (the payload is server-built and knows every
	// field by construction), so the only reachable wire refusal is the CONTRADICTION.
	a.insert.MediaCleared = true
	a.insert.Operations = []*pb_common.TechCardOperation{{Media: []*pb_common.TechCardOperationMedia{{MediaId: 4021}}}}
	r.serve(t, tcciZip(t, a))
	r.tcciExpectUpload(7001)
	r.rows(tcciRowOK(entity.TechCardImportStatusUploaded, 0, nil))

	r.media.EXPECT().DeleteMediaById(mock.Anything, 7001).Return(nil).Once()
	r.fs.EXPECT().DeleteObjects(mock.Anything, "https://cdn/og.webp", "https://cdn/c.webp", "https://cdn/t.webp").
		Return(nil).Once()

	// ImportTechCardArchive is not expected on this strict mock, so a handler that skipped the
	// gates and wrote the card fails here without an assertion of its own.
	_, err := tcciCommitCall(t, r.s)
	st, _ := status.FromError(err)
	require.Equal(t, codes.InvalidArgument, st.Code())
	require.Contains(t, st.Message(), "together with operation photos",
		"the WIRE gate spoke — the half that reads the payload alone, before any conversion")
}

// The STORED gates run too, with nil for the stored card — which is not a stub but exactly what an
// import is: the card does not exist yet, so there is nothing to erase, and «clear the photos of a
// card being created» is a sentence that has to be refused rather than passed over.
func TestCommitTechCardImportRunsTheStoredGatesWithNoStoredCard(t *testing.T) {
	r := tcciNewRig(t)
	a := tcciArchiveOneUpload(t, r)
	a.insert.MediaCleared = true
	a.insert.MediaAware = true
	r.serve(t, tcciZip(t, a))
	r.tcciExpectUpload(7001)
	r.rows(tcciRowOK(entity.TechCardImportStatusUploaded, 0, nil))

	r.media.EXPECT().DeleteMediaById(mock.Anything, 7001).Return(nil).Once()
	r.fs.EXPECT().DeleteObjects(mock.Anything, "https://cdn/og.webp", "https://cdn/c.webp", "https://cdn/t.webp").
		Return(nil).Once()

	_, err := tcciCommitCall(t, r.s)
	st, _ := status.FromError(err)
	require.Equal(t, codes.InvalidArgument, st.Code())
	require.Contains(t, st.Message(), "no operation photos to clear",
		"the stored gate spoke, which it can only do if it was called with nil for the stored card")
}

// ────────────────────────────── 8. раскладки fold back into the write shape ─────────────────────

// tcciWithMarker adds one legal раскладка to the fixture and returns the archive.
func tcciWithMarker(t *testing.T, a *tcimpArchive, mutate func(*pb_common.TechCardMarker)) *tcimpArchive {
	t.Helper()
	sizeM := "m"
	m := &pb_common.TechCardMarker{
		Summary: &pb_common.TechCardMarkerSummary{
			Id: 771, TechCardId: 214, Name: "shell 150", Source: "auto",
			FabricWidthCm: &pbdecimal.Decimal{Value: "150"},
			UsedLengthCm:  &pbdecimal.Decimal{Value: "512.437"},
			PlacedCount:   1, TotalCount: 1,
		},
		Layout: &pb_common.TechCardMarkerLayout{
			SchemaVersion: entity.MarkerLayoutSchemaWithComposition,
			Composition:   []*pb_common.TechCardMarkerCompositionEntry{{SizeId: 4, Quantity: 2}},
			Pieces: []*pb_common.TechCardMarkerPiece{
				{PieceId: 7, SizeId: 4, Quantity: 1, SourceUrl: "https://cdn.source-instance.example/x.dxf"},
			},
			Placements: []*pb_common.TechCardMarkerPlacement{{PieceId: 7}},
		},
	}
	if mutate != nil {
		mutate(m)
	}
	return a.with(techcardarchive.FileMarkersIndex, tcimpJSON(t, []techcardarchive.MarkerIndexEntry{
		{File: "markers/m-1.json", SizeName: &sizeM, MarkerName: "shell 150", BomLineKey: "B1"},
	})).with("markers/m-1.json", tcimpMarkerBlob(t, m))
}

// A раскладка travels in the archive's READ shape (summary + layout) and the store takes the WRITE
// one, so the commit folds it back — THROUGH THE WIRE CONVERTER.
//
// Not by hand: dto.ConvertPbTechCardMarkerInsertToEntity is where a marker's form is decided, and a
// second transcription would be a second opinion about what a marker is. What this case pins is
// that the fold happens at all and that it takes the index's word for the two fields the index
// owns — the name and the cloth line.
func TestCommitTechCardImportFoldsAMarkerBackIntoTheWriteShape(t *testing.T) {
	r := tcciNewRig(t)
	a := tcciWithMarker(t, tcciArchiveOneUpload(t, r), nil)
	r.serve(t, tcciZip(t, a))
	r.tcciExpectUpload(7001)
	r.rows(tcciRowOK(entity.TechCardImportStatusUploaded, 0, nil),
		tcciRowOK(entity.TechCardImportStatusCommitted, 606, tcciStoredReport(t, "GRB-SS26-014")))
	seen := r.expectImport(t, tcciImported(606))

	_, err := tcciCommitCall(t, r.s)
	require.NoError(t, err)
	require.Len(t, *seen, 1)
	markers := (*seen)[0].markers
	require.Len(t, markers, 1)
	require.Equal(t, "shell 150", markers[0].Name)
	require.Equal(t, "B1", markers[0].BomLineKey, "the cloth link is the index's, re-sewn by the store from the key")
	require.Equal(t, "512.44", markers[0].UsedLengthCm.String(),
		"rounded to the column's scale by the CONVERTER — proof the fold went through it rather than "+
			"copying the summary field across by hand")
	require.Len(t, markers[0].Composition, 1)
	require.Equal(t, 40, markers[0].Composition[0].SizeId, "the состав carries THIS base's size ids")
	require.NotEmpty(t, markers[0].Layout, "the geometry is re-marshalled and stored as the blob")
	require.NotContains(t, markers[0].Layout, "source-instance.example",
		"the exporting instance's url does not travel inside the blob either")
	require.Zero(t, markers[0].ColorwayId, "an import creates no products, so no раскладка is pinned to one")
	require.Zero(t, markers[0].ProductionRunId, "only card markers travel")
}

// A раскладка this server's own converter refuses FAILS THE IMPORT — it is not degraded into a
// hole. Our export cannot produce one, so a refusal means the file was written by something else,
// and that is the corrupt-archive case rather than the missing-reference case. It is also the
// answer the store gives two layers down for the same class of payload.
//
// The refusal happens before the transaction: ImportTechCardArchive is not expected on this strict
// mock, and the files that were moved are taken back.
func TestCommitTechCardImportRefusesAMarkerItsOwnConverterWouldNot(t *testing.T) {
	r := tcciNewRig(t)
	a := tcciWithMarker(t, tcciArchiveOneUpload(t, r), func(m *pb_common.TechCardMarker) {
		m.Summary.UsedLengthCm = &pbdecimal.Decimal{Value: "0"} // a раскладка that consumed no cloth
	})
	r.serve(t, tcciZip(t, a))
	r.tcciExpectUpload(7001)
	r.rows(tcciRowOK(entity.TechCardImportStatusUploaded, 0, nil))
	r.media.EXPECT().DeleteMediaById(mock.Anything, 7001).Return(nil).Once()
	r.fs.EXPECT().DeleteObjects(mock.Anything, mock.Anything, mock.Anything, mock.Anything).Return(nil).Once()

	_, err := tcciCommitCall(t, r.s)
	st, _ := status.FromError(err)
	require.Equal(t, codes.InvalidArgument, st.Code())
	require.Contains(t, st.Message(), "shell 150", "the sentence names the раскладка the operator has to look at")
	require.Contains(t, st.Message(), "used_length_cm")
}
