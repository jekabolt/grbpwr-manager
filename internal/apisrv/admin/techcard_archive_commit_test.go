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
// And five more the R4 review found, every one of them a REPEAT of something the first version
// handled exactly once, or a verdict taken on evidence that does not support it:
//
//	7. a status this binary has never heard of being read as a proven rollback, which unlocks the
//	   deletion of a live card's objects;
//	8. the SECOND candidate number being taken too, or the second picture vanishing, ending the
//	   import — and a number from our own older export being refused outright;
//	9. a reference that is not a picture disappearing and coming back as a bare five-hundred, when
//	   the upload is intact and pressing the button again is the whole remedy;
//	10. compensation deleting the media row a rival import ADOPTED between our upload and our
//	    failure — which no foreign key refuses, because the columns are ON DELETE SET NULL;
//	11. the settlement's re-read inheriting the request's cancellation, which would make «I do not
//	    know» the answer to every cancelled request and leave its files behind forever.
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

// expectRowTakenBack is compensation succeeding: the row this import minted is used by nobody, so
// it goes and its objects follow.
//
// THE DELETE IS THE GUARDED ONE, and that is not a detail of plumbing. Content-hash de-duplication
// means the row an import mints can be ADOPTED — matched by a rival import that then commits — and
// media(id) is referenced by columns that are ON DELETE SET NULL, so an unconditional delete would
// not fail against the winner's live card, it would succeed and blank its picture. The placement
// therefore asks DeleteMediaByIdIfUnused, which decides and deletes under one lock; here the answer
// is «nobody had it», which is the ordinary case. The opposite answer is
// TestCommitTechCardImportLeavesAnAdoptedMediaRowAlone.
func (r *tcciRig) expectRowTakenBack(mediaID int) {
	r.media.EXPECT().DeleteMediaByIdIfUnused(mock.Anything, mediaID).Return(true, nil, nil).Once()
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
	// bom is the BOM as the store received it, kept because one of its fields is a SERVER VERDICT
	// with no column and no wire field of its own (WastageClaimVerified): the only place it can be
	// observed is the payload handed over, and the only honest assertion about it is what the
	// store's own rule then makes of it.
	bom    []entity.TechCardBomItem
	report *pb_admin.TechCardImportReport
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
			at.bom = append([]entity.TechCardBomItem(nil), in.Card.BomItems...)
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

	// MySQL names the index in the message and the driver keeps that text, so this conflict is
	// CONFIRMED without a lookup: ListTechCards is not expected on this strict mock, and a handler
	// that walked the card list anyway to establish what the error already said fails here.
	taken := errors.New("Error 1062 (23000): Duplicate entry 'GRB-SS26-014' for key 'tech_card.uniq_tech_card_style_number'")
	r.classifies(true, false)
	r.cards.EXPECT().SuggestStyleNumber(mock.Anything, "SS", 2026).Return("SS26-0007", nil)

	seen := r.expectImport(t, tcciFails(taken), tcciImported(101))

	resp, err := tcciCommitCall(t, r.s)
	require.NoError(t, err)
	require.EqualValues(t, 101, resp.GetTechCardId())

	require.Len(t, *seen, 2, "one proposal was enough here: a second transaction, and it landed")
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

// tcciStyleTaken is a duplicate-key error that NAMES the style number index, which is how MySQL
// reports it and how this handler tells it from the equipment profile key.
func tcciStyleTaken(number string) error {
	return errors.New("Error 1062 (23000): Duplicate entry '" + number +
		"' for key 'tech_card.uniq_tech_card_style_number'")
}

// THE SECOND CANDIDATE BEING TAKEN TOO MUST NOT COST THE IMPORT.
//
// The window the retry closes does not close after one pass: between our proposal and our write,
// another import of the same season can take the number we were just given, and a repair allowed
// exactly one shot answers that by throwing away a whole card — against the owner's binding rule
// that a collision always yields a NEW card. So the machine walks candidates until one lands.
//
// The upload is still expected exactly Once: repeats re-run the transaction, never the files.
func TestCommitTechCardImportTriesAnotherCandidateWhenTheProposalIsTakenToo(t *testing.T) {
	r := tcciNewRig(t)
	a := tcciArchiveOneUpload(t, r)
	a.insert.SkuSeason = &pb_common.SkuSeason{Code: pb_common.SeasonEnum_SEASON_ENUM_SS, Year: 2026}
	r.serve(t, tcciZip(t, a))
	r.tcciExpectUpload(7001)
	r.rows(tcciRowOK(entity.TechCardImportStatusUploaded, 0, nil),
		tcciRowOK(entity.TechCardImportStatusCommitted, 111, tcciStoredReport(t, "SS26-0008")))

	r.classifies(true, false)
	r.cards.EXPECT().SuggestStyleNumber(mock.Anything, "SS", 2026).Return("SS26-0007", nil).Once()
	r.cards.EXPECT().SuggestStyleNumber(mock.Anything, "SS", 2026).Return("SS26-0008", nil).Once()

	seen := r.expectImport(t,
		tcciFails(tcciStyleTaken("GRB-SS26-014")),
		tcciFails(tcciStyleTaken("SS26-0007")),
		tcciImported(111))

	resp, err := tcciCommitCall(t, r.s)
	require.NoError(t, err, "two lost races cost the number twice; they do not cost the card")
	require.EqualValues(t, 111, resp.GetTechCardId())

	require.Len(t, *seen, 3)
	require.Equal(t, []string{"GRB-SS26-014", "SS26-0007", "SS26-0008"},
		[]string{(*seen)[0].styleNumber, (*seen)[1].styleNumber, (*seen)[2].styleNumber},
		"every attempt asks for a DIFFERENT number — a candidate repeated is a transaction spent to fail identically")
	require.Equal(t, entity.TechCardStageProto, (*seen)[2].stage, "a card with a number keeps its stage")

	lines := tcciReportLines((*seen)[2].report, techcardarchive.ReasonStyleNumberTaken)
	require.Len(t, lines, 1,
		"ONE line about the number however many candidates it took — the operator asked what became of "+
			"their article number, not what the second candidate was")
	require.Contains(t, lines[0].GetDetail(), "SS26-0008", "and it names the number the card actually landed under")
	require.Contains(t, lines[0].GetRef(), "GRB-SS26-014", "against the number the archive asked for")
}

// AND WHEN THE CANDIDATES RUN OUT, THE CARD STILL LANDS.
//
// The budget exists because «try another number forever» is how an import spends an afternoon
// writing nothing, and the exhausted outcome is the one the archive-with-no-season already takes:
// no number, stage `idea`, a line in the report. What must NOT happen is the whole import failing
// over a name a person types in five seconds.
func TestCommitTechCardImportLandsNumberlessWhenTheCandidatesRunOut(t *testing.T) {
	r := tcciNewRig(t)
	a := tcciArchiveOneUpload(t, r)
	a.insert.SkuSeason = &pb_common.SkuSeason{Code: pb_common.SeasonEnum_SEASON_ENUM_SS, Year: 2026}
	r.serve(t, tcciZip(t, a))
	r.tcciExpectUpload(7001)
	r.rows(tcciRowOK(entity.TechCardImportStatusUploaded, 0, nil),
		tcciRowOK(entity.TechCardImportStatusCommitted, 112, tcciStoredReport(t, "")))

	r.classifies(true, false)
	proposals := []string{"SS26-0007", "SS26-0008", "SS26-0009", "SS26-0010", "SS26-0011"}
	asked := 0
	r.cards.EXPECT().SuggestStyleNumber(mock.Anything, "SS", 2026).
		RunAndReturn(func(context.Context, string, int) (string, error) {
			require.Less(t, asked, len(proposals), "the machine asked for more candidates than its budget allows")
			asked++
			return proposals[asked-1], nil
		})

	// Every proposal is taken the instant it is handed out: the pathological base.
	answers := []func() (int, error){}
	for i := 0; i <= tcciMaxStyleNumberProposals; i++ {
		answers = append(answers, tcciFails(tcciStyleTaken("taken")))
	}
	answers = append(answers, tcciImported(112))
	seen := r.expectImport(t, answers...)

	resp, err := tcciCommitCall(t, r.s)
	require.NoError(t, err, "the card and everything in it is already in hand; it is not lost over a number")
	require.EqualValues(t, 112, resp.GetTechCardId())

	require.Equal(t, tcciMaxStyleNumberProposals, asked, "exactly the budget was spent, and not one more")
	require.Len(t, *seen, tcciMaxStyleNumberProposals+2,
		"the archive's number, one transaction per proposal, and the numberless landing")
	last := (*seen)[len(*seen)-1]
	require.Empty(t, last.styleNumber)
	require.Equal(t, entity.TechCardStageIdea, last.stage,
		"a numberless card can only be an idea — every later stage requires a number")
	require.Equal(t, entity.StyleNumberSourceGenerated, last.numberSource,
		"nobody typed this absence; the server chose it")
	lines := tcciReportLines(last.report, techcardarchive.ReasonStyleNumberTaken)
	require.Len(t, lines, 1)
	require.Contains(t, lines[0].GetDetail(), "WITHOUT a number")
}

// A PROPOSAL THE BASE HAS ALREADY REFUSED IS NOT OFFERED A SECOND TIME.
//
// The proposal comes from a read, and a read can be stale — two imports racing on the same season
// can both be told the same next number. Re-offering it would spend a transaction to land in
// exactly the same place, so the machine treats a repeat as «out of candidates» and lands the card.
func TestCommitTechCardImportWillNotOfferTheSameNumberTwice(t *testing.T) {
	r := tcciNewRig(t)
	a := tcciArchiveOneUpload(t, r)
	a.insert.SkuSeason = &pb_common.SkuSeason{Code: pb_common.SeasonEnum_SEASON_ENUM_SS, Year: 2026}
	r.serve(t, tcciZip(t, a))
	r.tcciExpectUpload(7001)
	r.rows(tcciRowOK(entity.TechCardImportStatusUploaded, 0, nil),
		tcciRowOK(entity.TechCardImportStatusCommitted, 113, tcciStoredReport(t, "")))

	r.classifies(true, false)
	// A base that keeps answering with the number that was just refused.
	r.cards.EXPECT().SuggestStyleNumber(mock.Anything, "SS", 2026).Return("SS26-0007", nil)

	seen := r.expectImport(t,
		tcciFails(tcciStyleTaken("GRB-SS26-014")),
		tcciFails(tcciStyleTaken("SS26-0007")),
		tcciImported(113))

	_, err := tcciCommitCall(t, r.s)
	require.NoError(t, err)
	require.Len(t, *seen, 3, "the repeat is not attempted; the third transaction is the numberless landing")
	require.Equal(t, "SS26-0007", (*seen)[1].styleNumber)
	require.Empty(t, (*seen)[2].styleNumber)
	require.Equal(t, entity.TechCardStageIdea, (*seen)[2].stage)
}

// A GRANDFATHERED NUMBER FROM OUR OWN EXPORT IS RENUMBERED, NOT REFUSED.
//
// The strict manual grammar (uppercase segments, no spaces) is younger than the cards people
// numbered by hand before it. An archive of one of those carries a number that is perfectly real on
// the source and unwritable here — and answering it with InvalidArgument would mean this server
// cannot import an archive IT PRODUCED. So the same machine that answers a taken number answers
// this: a proposal from the season, and a line in the report saying why.
func TestCommitTechCardImportRenumbersAGrandfatheredManualNumber(t *testing.T) {
	r := tcciNewRig(t)
	a := tcciArchiveOneUpload(t, r)
	a.insert.StyleNumber = "old coat 12" // lowercase and spaces: legal in 2019, unwritable now
	a.insert.StyleNumberSource = pb_common.StyleNumberSource_STYLE_NUMBER_SOURCE_MANUAL
	a.insert.SkuSeason = &pb_common.SkuSeason{Code: pb_common.SeasonEnum_SEASON_ENUM_SS, Year: 2026}
	r.serve(t, tcciZip(t, a))
	r.tcciExpectUpload(7001)
	r.rows(tcciRowOK(entity.TechCardImportStatusUploaded, 0, nil),
		tcciRowOK(entity.TechCardImportStatusCommitted, 114, tcciStoredReport(t, "SS26-0007")))

	// No transaction ever refuses anything here: the number is repaired BEFORE the first attempt,
	// so the classifiers are never even asked.
	r.cards.EXPECT().SuggestStyleNumber(mock.Anything, "SS", 2026).Return("SS26-0007", nil).Once()
	seen := r.expectImport(t, tcciImported(114))

	resp, err := tcciCommitCall(t, r.s)
	require.NoError(t, err, "our own export must be importable")
	require.EqualValues(t, 114, resp.GetTechCardId())

	require.Len(t, *seen, 1, "the number is fixed before the write, not by failing one")
	require.Equal(t, "SS26-0007", (*seen)[0].styleNumber)
	require.Equal(t, entity.StyleNumberSourceGenerated, (*seen)[0].numberSource)
	// The code comes from the CLOSED dictionary — the archive's own row carried a value this base
	// cannot hold, which is what archive_row_invalid means. It is not style_number_taken: nobody
	// here is using that number.
	lines := tcciReportLines((*seen)[0].report, techcardarchive.ReasonArchiveRowInvalid)
	require.Len(t, lines, 1)
	require.Contains(t, lines[0].GetRef(), "old coat 12")
	require.Contains(t, lines[0].GetDetail(), "SS26-0007")
	require.Empty(t, tcciReportLines((*seen)[0].report, techcardarchive.ReasonStyleNumberTaken),
		"the number was not taken — saying so would send the operator looking for a card that does not exist")
}

// A 1062 THAT IS NOT THE STYLE NUMBER MUST NOT RENAME ANYTHING.
//
// The driver reports one error number for every unique index in the schema, and a card write
// touches two (style_number and the equipment profile key of 0306). An implementation that renamed
// on the strength of the code alone would mint a fresh number and retry the identical conflict —
// until its budget ran out and the card landed numberless, for a collision that was never about the
// number. Here the message NAMES the other index, so the failure is passed through untouched and
// the store is called exactly once.
//
// Neither SuggestStyleNumber nor ListTechCards is expected on this strict mock: the name settles it,
// so nothing has to be looked up and nothing may be proposed.
func TestCommitTechCardImportDoesNotRenameOnSomebodyElsesUniqueIndex(t *testing.T) {
	r := tcciNewRig(t)
	a := tcciArchiveOneUpload(t, r)
	r.serve(t, tcciZip(t, a))
	r.tcciExpectUpload(7001)
	r.rows(tcciRowOK(entity.TechCardImportStatusUploaded, 0, nil),
		tcciRowOK(entity.TechCardImportStatusUploaded, 0, nil))

	r.classifies(true, false)
	// The transaction rolled back, so the files this import moved are taken back.
	r.expectRowTakenBack(7001)
	r.fs.EXPECT().DeleteObjects(mock.Anything, "https://cdn/og.webp", "https://cdn/c.webp", "https://cdn/t.webp").
		Return(nil).Once()

	seen := r.expectImport(t, tcciFails(errors.New(
		"Error 1062 (23000): Duplicate entry 'x' for key 'tech_card.uq_equipment_profile_key'")))

	_, err := tcciCommitCall(t, r.s)
	st, _ := status.FromError(err)
	require.Equal(t, codes.Internal, st.Code())
	require.Len(t, *seen, 1, "a duplicate that is not the style number is not retried")
}

// WHEN THE MESSAGE CARRIES NO INDEX NAME, the number is read back before anything is renamed — and a
// number that reads as FREE means the conflict was somebody else's index after all.
//
// This is the fallback half of the same question, and it has to keep working: a driver, a proxy or a
// future wrap that drops the text would otherwise turn every duplicate key into a rename.
func TestCommitTechCardImportFallsBackToTheLookupWhenTheKeyIsNotNamed(t *testing.T) {
	r := tcciNewRig(t)
	a := tcciArchiveOneUpload(t, r)
	r.serve(t, tcciZip(t, a))
	r.tcciExpectUpload(7001)
	r.rows(tcciRowOK(entity.TechCardImportStatusUploaded, 0, nil),
		tcciRowOK(entity.TechCardImportStatusUploaded, 0, nil))

	r.classifies(true, false)
	// Nobody here carries that number, so the duplicate was another index.
	r.cards.EXPECT().ListTechCards(mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything).
		Return(nil, 0, nil)
	r.expectRowTakenBack(7001)
	r.fs.EXPECT().DeleteObjects(mock.Anything, mock.Anything, mock.Anything, mock.Anything).Return(nil).Once()

	seen := r.expectImport(t, tcciFails(errors.New("duplicate key")))

	_, err := tcciCommitCall(t, r.s)
	st, _ := status.FromError(err)
	require.Equal(t, codes.Internal, st.Code())
	require.Len(t, *seen, 1, "the lookup said the number is free, so nothing was renamed")
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
	r.expectRowTakenBack(7001)
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

// A STATUS THIS SERVER HAS NEVER HEARD OF IS «I DO NOT KNOW», NOT «IT ROLLED BACK».
//
// The set of statuses is held in Go with no CHECK behind it precisely so it can grow, which makes
// «anything that is not `committed`» a rule that unlocks deletion on every word this binary has not
// been taught: a status minted by a newer binary during a rolling deploy, a value corrupted by
// hand. The deletion it unlocks is not recoverable — pattern objects have no foreign key to refuse
// it, and media rows are reachable through ON DELETE SET NULL columns.
//
// The strict mocks are the whole assertion: neither DeleteMediaByIdIfUnused nor DeleteObjects is
// expected, so a handler that read `committing` as a proven rollback fails here.
func TestCommitTechCardImportDoesNotCompensateOnAStatusItCannotRead(t *testing.T) {
	r := tcciNewRig(t)
	a := tcciArchiveOneUpload(t, r)
	r.serve(t, tcciZip(t, a))
	r.tcciExpectUpload(7001)
	r.rows(tcciRowOK(entity.TechCardImportStatusUploaded, 0, nil),
		tcciRowOK("committing", 0, nil))

	r.classifies(false, false)
	r.expectImport(t, tcciFails(errors.New("commit: invalid connection")))

	resp, err := tcciCommitCall(t, r.s)
	require.Nil(t, resp)
	st, _ := status.FromError(err)
	require.Equal(t, codes.Internal, st.Code(),
		"the import did not demonstrably land, so it is reported as a failure — but nothing was deleted")
}

// `committed` WITH NO CARD ID IS ALSO «I DO NOT KNOW».
//
// The claim and the card id are two statements of the SAME transaction, so this pairing cannot be
// produced by the write path at all — something else wrote the row. Guessing at that point is
// exactly what must not happen: the strict mocks expect no deletion.
func TestCommitTechCardImportDoesNotCompensateOnCommittedWithoutACardId(t *testing.T) {
	r := tcciNewRig(t)
	a := tcciArchiveOneUpload(t, r)
	r.serve(t, tcciZip(t, a))
	r.tcciExpectUpload(7001)
	r.rows(tcciRowOK(entity.TechCardImportStatusUploaded, 0, nil),
		tcciRowOK(entity.TechCardImportStatusCommitted, 0, nil))

	r.classifies(false, false)
	r.expectImport(t, tcciFails(errors.New("commit: broken pipe")))

	_, err := tcciCommitCall(t, r.s)
	st, _ := status.FromError(err)
	require.Equal(t, codes.Internal, st.Code())
}

// THE SETTLEMENT'S READ MUST SURVIVE THE REQUEST BEING CANCELLED, and nothing but a cancelled
// request proves it.
//
// The commonest reason to be settling at all is a context that has just died — the client hung up,
// the deadline passed — and a settlement that inherits that context can only ever answer «I do not
// know», which permanently forbids compensation and turns every cancelled request into permanent
// orphans. The detach (context.WithoutCancel) is therefore load-bearing, and it is INVISIBLE to a
// mock that ignores the context it is handed: removing it leaves every other case in this file
// green.
//
// So this case cancels the request context before the write returns, and the mock asserts on the
// context it actually receives.
func TestCommitTechCardImportSettlesOnAContextThatOutlivesTheRequest(t *testing.T) {
	r := tcciNewRig(t)
	a := tcciArchiveOneUpload(t, r)
	r.serve(t, tcciZip(t, a))
	r.tcciExpectUpload(7001)

	ctx, cancel := context.WithCancel(tcupWriterCtx())
	defer cancel()

	settleAlive := false
	calls := 0
	r.cards.EXPECT().GetTechCardImportByImportID(mock.Anything, tcciTestImportID).
		RunAndReturn(func(rctx context.Context, _ string) (entity.TechCardArchiveImportRecord, error) {
			calls++
			switch calls {
			case 1: // the pre-flight check, on the live request
				return tcciRow(entity.TechCardImportStatusUploaded, 0, nil), nil
			default: // the settlement, after the request was cancelled
				settleAlive = rctx.Err() == nil
				return tcciRow(entity.TechCardImportStatusUploaded, 0, nil), nil
			}
		})

	r.classifies(false, false)
	r.expectRowTakenBack(7001)
	r.fs.EXPECT().DeleteObjects(mock.Anything, mock.Anything, mock.Anything, mock.Anything).Return(nil).Once()

	r.cards.EXPECT().ImportTechCardArchive(mock.Anything, mock.Anything).
		RunAndReturn(func(context.Context, entity.TechCardArchiveImport) (int, error) {
			// The client hangs up while the transaction is in flight — the exact shape of failure
			// this whole settlement exists for.
			cancel()
			return 0, context.Canceled
		})

	_, err := r.s.CommitTechCardImport(ctx, &pb_admin.CommitTechCardImportRequest{ImportId: tcciTestImportID})
	require.Error(t, err)
	require.GreaterOrEqual(t, calls, 2, "the settlement read happened at all")
	require.True(t, settleAlive,
		"the settlement ran on a LIVE context: inheriting the cancelled one would answer «unknown» every "+
			"time and leave the files behind forever")
}

// The store refusing the claim inside its own transaction — the race this handler's pre-flight
// check cannot close — is definite: nothing of ours was written, so the files ARE taken back and
// the operator is told which card the winner made.
//
// «Taken back» is the GUARDED delete: the row goes only because nobody has it. The case where
// somebody does is the one below.
func TestCommitTechCardImportCompensatesWhenTheStoreSaysAlreadyCommitted(t *testing.T) {
	r := tcciNewRig(t)
	a := tcciArchiveOneUpload(t, r)
	r.serve(t, tcciZip(t, a))
	r.tcciExpectUpload(7001)
	r.rows(tcciRowOK(entity.TechCardImportStatusUploaded, 0, nil),
		tcciRowOK(entity.TechCardImportStatusCommitted, 512, nil))

	r.expectRowTakenBack(7001)
	r.fs.EXPECT().DeleteObjects(mock.Anything, "https://cdn/og.webp", "https://cdn/c.webp", "https://cdn/t.webp").
		Return(nil).Once()

	r.expectImport(t, tcciFails(entity.ErrImportAlreadyCommitted))

	_, err := tcciCommitCall(t, r.s)
	st, _ := status.FromError(err)
	require.Equal(t, codes.FailedPrecondition, st.Code())
	require.Contains(t, st.Message(), "512")
}

// THE LOSER OF THE RACE MUST NOT DELETE THE PICTURE THE WINNER ADOPTED.
//
// The trace is the mirror of the vanished-picture one and it is the reason compensation cannot be a
// plain delete: A uploads the bytes and mints media row M; B, resolving the same archive, matches M
// by content hash and plans to REUSE it; B wins the claim and commits a card pointing at M; A is
// told «already committed» and compensates. From A's side M looks exactly like the row it minted a
// moment ago — because it is. Deleting it would not even fail: tech_card_callout.media_id is
// ON DELETE SET NULL, so the delete SUCCEEDS and blanks a live card's picture on the way out.
//
// So the delete asks first, and «still used» is not an error — it means the row is no longer this
// import's to take back, AND NEITHER ARE THE OBJECTS UNDER IT: the strict bucket mock expects no
// DeleteObjects at all, so an implementation that kept the row but wiped the files it resolves to
// fails here. The refusal the operator gets is unchanged: it names the winner's card.
func TestCommitTechCardImportLeavesAnAdoptedMediaRowAlone(t *testing.T) {
	r := tcciNewRig(t)
	a := tcciArchiveOneUpload(t, r)
	r.serve(t, tcciZip(t, a))
	r.tcciExpectUpload(7001)
	r.rows(tcciRowOK(entity.TechCardImportStatusUploaded, 0, nil),
		tcciRowOK(entity.TechCardImportStatusCommitted, 512, nil))

	// The winner's card holds it now.
	r.media.EXPECT().DeleteMediaByIdIfUnused(mock.Anything, 7001).
		Return(false, []entity.MediaUsageRef{{Kind: "tech_card", EntityId: 512, Slot: "callout"}}, nil).Once()

	r.expectImport(t, tcciFails(entity.ErrImportAlreadyCommitted))

	_, err := tcciCommitCall(t, r.s)
	st, _ := status.FromError(err)
	require.Equal(t, codes.FailedPrecondition, st.Code(),
		"the loser is still told this archive is already imported")
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
	// The flag is not decoration: without it these two assertions live inside an `if` that a
	// missing counter row satisfies by never running, and the case goes green in a world where the
	// tally lost the media line entirely.
	counted := false
	for _, c := range (*seen)[1].report.GetCounters() {
		if c.GetEntity() == techcardarchive.EntityMedia {
			counted = true
			require.EqualValues(t, 1, c.GetImported(), "the picture that did land is still counted as imported")
			require.EqualValues(t, 1, c.GetSkipped(), "the lost one MOVED columns rather than vanishing from the tally")
		}
	}
	require.True(t, counted, "the report carries a media counter at all")
}

// A SECOND PICTURE VANISHING AFTER THE FIRST WAS PATCHED OUT IS ALSO A HOLE.
//
// The repair cannot be once-only. The window it closes is open for as long as the import is
// running, and the two reused rows in this fixture are taken away one at a time — the first before
// the first attempt, the second while the repaired attempt is on its way. A handler that repairs
// once answers the second disappearance with `Internal` and loses a card over two pictures.
//
// What bounds it is the set it asks about: the reuse targets NOT ALREADY KNOWN GONE. Each repair
// moves one out of that set for good, so the third attempt has nothing left to ask about — which is
// why GetMediaByIds is scripted exactly twice, with the second call carrying only the target the
// first one did not settle.
func TestCommitTechCardImportSurvivesTwoPicturesVanishingInARow(t *testing.T) {
	r := tcciNewRig(t)
	a := tcimpNewArchive()
	a.insert.TechnicalMedia = []*pb_common.TechCardMediaItem{{MediaId: 4020}, {MediaId: 4022}, {MediaId: 4021}}
	one, oneSHA := a.blob(techcardarchive.DirMedia, ".jpg", tcflJPEG("stored here already"))
	two, twoSHA := a.blob(techcardarchive.DirMedia, ".jpg", tcflJPEG("also stored here"))
	fresh, freshSHA := a.blob(techcardarchive.DirMedia, ".jpg", tcflJPEG("brand new"))
	a.with(techcardarchive.FileMediaIndex, tcimpJSON(t, []techcardarchive.MediaIndexEntry{
		{Ref: 4020, File: one, SHA256: oneSHA},
		{Ref: 4022, File: two, SHA256: twoSHA},
		{Ref: 4021, File: fresh, SHA256: freshSHA},
	}))
	r.media.EXPECT().FindMediaByContentHash(mock.Anything, oneSHA).Return(&entity.MediaFull{Id: 9001}, nil)
	r.media.EXPECT().FindMediaByContentHash(mock.Anything, twoSHA).Return(&entity.MediaFull{Id: 9002}, nil)
	r.media.EXPECT().FindMediaByContentHash(mock.Anything, freshSHA).Return(nil, nil)
	r.serve(t, tcciZip(t, a))
	r.tcciExpectUpload(7001)
	r.rows(tcciRowOK(entity.TechCardImportStatusUploaded, 0, nil),
		tcciRowOK(entity.TechCardImportStatusCommitted, 303, tcciStoredReport(t, "GRB-SS26-014")))

	r.classifies(false, true)
	// First re-check: 9001 is gone, 9002 is still there. Second: 9002 has gone too — and it is the
	// only id asked about, because 9001 is already out of the payload.
	r.media.EXPECT().GetMediaByIds(mock.Anything, []int{9001, 9002}).
		Return(map[int]entity.MediaFull{9002: {Id: 9002}}, nil).Once()
	r.media.EXPECT().GetMediaByIds(mock.Anything, []int{9002}).
		Return(map[int]entity.MediaFull{}, nil).Once()

	seen := r.expectImport(t,
		tcciFails(errors.New("Error 1452: foreign key constraint fails (`media_id`)")),
		tcciFails(errors.New("Error 1452: foreign key constraint fails (`media_id`)")),
		tcciImported(303))

	resp, err := tcciCommitCall(t, r.s)
	require.NoError(t, err, "two pictures are lost; the card is not")
	require.EqualValues(t, 303, resp.GetTechCardId())

	require.Len(t, *seen, 3)
	require.Equal(t, []int{9001, 9002, 7001}, (*seen)[0].mediaIDs)
	require.Equal(t, []int{9002, 7001}, (*seen)[1].mediaIDs)
	require.Equal(t, []int{7001}, (*seen)[2].mediaIDs,
		"the second disappearance is patched out too, rather than written as a dangling id")

	lines := tcciReportLines((*seen)[2].report, techcardarchive.ReasonMediaUploadFailed)
	require.Len(t, lines, 2, "each loss is reported once — and neither is reported twice by a repeated re-check")
	refs := []string{lines[0].GetRef(), lines[1].GetRef()}
	require.ElementsMatch(t, []string{"media_id=4020", "media_id=4022"}, refs)

	counted := false
	for _, c := range (*seen)[2].report.GetCounters() {
		if c.GetEntity() == techcardarchive.EntityMedia {
			counted = true
			require.EqualValues(t, 1, c.GetImported(), "one picture landed")
			require.EqualValues(t, 2, c.GetSkipped(), "both losses moved columns exactly once")
		}
	}
	require.True(t, counted, "the report carries a media counter at all")
}

// A REFERENCE THAT IS NOT A PICTURE VANISHING IS NOT A FIVE-HUNDRED.
//
// A category deleted between the resolve and the write cannot be repaired here — re-verifying every
// foreign key inside the transaction is a tier this handler does not have — so it must not be
// retried either: an identical second transaction fails identically. But the answer is not
// «internal error». The rollback left the import row `uploaded`, so the archive is still in the
// bucket and pressing commit again re-resolves against the catalogue AS IT IS NOW, which is exactly
// the fix. An answer that hides a working remedy behind a five-hundred sends the operator to file a
// ticket instead of pressing a button.
//
// Aborted is gRPC's word for «a concurrent change lost you this attempt, retry it», and the reason
// travels in the details so a panel can offer the button without matching English.
func TestCommitTechCardImportSaysAReferenceVanishedRatherThanFailingInternally(t *testing.T) {
	r := tcciNewRig(t)
	a := tcciArchiveOneUpload(t, r)
	r.serve(t, tcciZip(t, a))
	r.tcciExpectUpload(7001)
	r.rows(tcciRowOK(entity.TechCardImportStatusUploaded, 0, nil),
		tcciRowOK(entity.TechCardImportStatusUploaded, 0, nil))

	r.classifies(false, true)
	r.expectRowTakenBack(7001)
	r.fs.EXPECT().DeleteObjects(mock.Anything, mock.Anything, mock.Anything, mock.Anything).Return(nil).Once()

	seen := r.expectImport(t, tcciFails(errors.New("Error 1452: foreign key constraint fails (`category_id`)")))

	_, err := tcciCommitCall(t, r.s)
	st, ok := status.FromError(err)
	require.True(t, ok)
	require.Equal(t, codes.Aborted, st.Code(),
		"the operator can fix this by pressing the button again, and the code has to say so")
	require.Contains(t, st.Message(), "Press commit again")

	var info *errdetails.ErrorInfo
	for _, d := range st.Details() {
		if ei, is := d.(*errdetails.ErrorInfo); is {
			info = ei
		}
	}
	require.NotNil(t, info, "a panel must be able to offer the retry without parsing English")
	require.Equal(t, tcciReasonReferenceVanished, info.GetReason())
	require.Equal(t, tcciTestImportID, info.GetMetadata()["import_id"])

	require.Len(t, *seen, 1, "an unrepairable foreign key is not retried")
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

	r.expectRowTakenBack(7001)
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

	r.expectRowTakenBack(7001)
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
	r.expectRowTakenBack(7001)
	r.fs.EXPECT().DeleteObjects(mock.Anything, mock.Anything, mock.Anything, mock.Anything).Return(nil).Once()

	_, err := tcciCommitCall(t, r.s)
	st, _ := status.FromError(err)
	require.Equal(t, codes.InvalidArgument, st.Code())
	require.Contains(t, st.Message(), "shell 150", "the sentence names the раскладка the operator has to look at")
	require.Contains(t, st.Message(), "used_length_cm")
}

// ────────────────────────────── 9. archive money never reaches the write ────────────────────────

// THE BELT ON COSTING IS UNCONDITIONAL, AND ITS FAILURE BLAMES US.
//
// CreateTechCard refuses cost input from an account without costing:write, because there a person
// is sending their own figures and the question is a permission. Here the figures came out of
// somebody else's ZIP and the rule above every permission is that ARCHIVE MONEY DOES NOT TRAVEL:
// the resolver's sanitiser strips it, so the create path's own probe must be false on every payload
// that reaches the write, and asking costs nothing.
//
// If it ever fires, the archive is not the problem and the operator is not the problem — our
// sanitiser leaked. Which is why the answer is Internal naming the field, and NOT PermissionDenied:
// blaming a person's rights for another company's money would send them to ask for a permission
// that does not help and, if granted, would write those prices into this catalogue.
func TestCommitTechCardImportRefusesAPayloadThatSmuggledCostingIn(t *testing.T) {
	s, _, _, _ := tcimpServer(t)

	for _, tc := range []struct {
		name  string
		in    *pb_common.TechCardInsert
		field string
	}{{
		name:  "the costing block",
		in:    &pb_common.TechCardInsert{Costing: &pb_common.TechCardCosting{}},
		field: "costing",
	}, {
		name: "a purchase price on a BOM line",
		in: &pb_common.TechCardInsert{BomItems: []*pb_common.TechCardBomItem{
			{}, {UnitPrice: &pbdecimal.Decimal{Value: "12.50"}},
		}},
		field: "bom_items[1].unit_price",
	}} {
		t.Run(tc.name, func(t *testing.T) {
			_, _, err := s.tcciPayload(tcupWriterCtx(), &resolvedTechCardImport{Insert: tc.in})
			st, ok := status.FromError(err)
			require.True(t, ok)
			require.Equal(t, codes.Internal, st.Code(),
				"a leak of OUR sanitiser is our fault; PermissionDenied would blame the operator for it")
			require.Contains(t, st.Message(), tc.field,
				"the sentence names the field that leaked, which is the only thing a debugger wants")
		})
	}
}

// And the belt is silent on the payload the sanitiser actually produces: a card with BOM lines and
// no money passes it without a word. Without this half the case above is satisfied by a belt that
// refuses everything.
func TestCommitTechCardImportMoneyBeltPassesACleanPayload(t *testing.T) {
	require.NoError(t, tcciMoneyBelt(tcupWriterCtx(), &pb_common.TechCardInsert{
		StyleNumber: "GRB-SS26-014",
		BomItems:    []*pb_common.TechCardBomItem{{Name: "shell"}},
	}))
}

// ────────────────────────────── 10. the badge the resolver re-earned ────────────────────────────

// tcciWastageArchive is the resolver fixture's badged BOM line, made WRITABLE.
//
// The resolver's own cases stop at the plan and never convert, so their line carries no section —
// and the wire converter refuses a BOM line without one, which would fail this import for a reason
// that has nothing to do with the badge. Setting it here rather than in the shared fixture keeps
// that file's cases measuring exactly what they measure now.
func tcciWastageArchive(t *testing.T, percent string, layCount int32) *tcimpArchive {
	t.Helper()
	a := tcimpWastageArchive(t, percent, layCount)
	for _, b := range a.insert.BomItems {
		b.Section = pb_common.TechCardBomSection_TECH_CARD_BOM_SECTION_FABRIC
	}
	return a
}

// A WASTAGE BADGE THAT WAS RE-EARNED HERE MUST SURVIVE THE WRITE.
//
// The resolver checks the archive's «median over N measured lays» against THIS base's own lays and
// records the lines that re-earn it. That verdict has no column and no wire field — it lives on
// entity.TechCardBomItem.WastageClaimVerified, which is `db:"-"` — and the store is fail-closed
// around it: a payload merely SAYING 'lays' lands 'manual'. So the verdict has to be stamped onto
// the entity after it is converted, and the conversion lives on this side.
//
// Without that stamp the check is a read that decides nothing in ONE direction only: it still
// degrades what it cannot confirm, with a report line, and silently demotes what it CAN. A card
// exported from this base and restored into it would come back reading «entered by hand», with
// nothing anywhere to say a badge was lost. That is the exact silent loss the check was built for.
//
// THE ASSERTION IS THE END STATE, NOT THE FLAG: the payload goes through the store's own rule
// (entity.ResolveBomWastageProvenance, via tcimpStoredProvenance), so it measures what lands in the
// column rather than restating the boolean the handler just set.
func TestCommitTechCardImportKeepsAWastageBadgeThisBaseReEarned(t *testing.T) {
	r := tcciNewRig(t)
	runs := mocks.NewMockProductionRuns(t)
	r.repo.EXPECT().ProductionRuns().Return(runs).Maybe()
	r.cards.EXPECT().ListMaterials(mock.Anything, "", true).
		Return([]entity.MaterialWithPrice{tcimpCatalogRow(1001, "F-WOOL-320", "m")}, nil)
	tcimpExpectCalibration(r.cards, runs, 1001)

	a := tcciWastageArchive(t, "22.00", 3)
	r.serve(t, tcciZip(t, a))
	r.rows(tcciRowOK(entity.TechCardImportStatusUploaded, 0, nil),
		tcciRowOK(entity.TechCardImportStatusCommitted, 707, tcciStoredReport(t, "GRB-SS26-014")))
	seen := r.expectImport(t, tcciImported(707))

	resp, err := tcciCommitCall(t, r.s)
	require.NoError(t, err)
	require.EqualValues(t, 707, resp.GetTechCardId())

	require.Len(t, *seen, 1)
	require.Len(t, (*seen)[0].bom, 1)
	got := tcimpStoredProvenance((*seen)[0].bom[0])
	require.Equal(t, entity.BomWastageSourceLays, got.Source,
		"the claim was re-checked against this base's own lays and stood; the row must land carrying it")
	require.EqualValues(t, 3, got.LayCount.Int64)
	require.Equal(t, "22", got.AppliedPercent.Decimal.String(),
		"the badge is self-checking: it is stamped against the number it was confirmed for")

	require.Empty(t, tcciReportLines((*seen)[0].report, techcardarchive.ReasonWastageClaimDegraded),
		"nothing was degraded, so nothing is reported — a line here would send the operator to look at "+
			"a badge that is in perfect order")
}

// AND IT MUST SURVIVE A REPAIR, which rebuilds the entity from scratch.
//
// The retry path re-derives the payload from the repaired wire message — a BRAND NEW entity, built
// by the same converter, with the verdict once again absent because the wire has nowhere to carry
// it. A repair that forgot to re-stamp would demote every re-earned badge to 'manual' on the retry
// paths ONLY, which is the half nobody looks at: the ordinary commit would keep passing.
//
// The repair here is the vanished reused picture, because that is the one that rebuilds.
func TestCommitTechCardImportKeepsTheWastageBadgeAcrossARepair(t *testing.T) {
	r := tcciNewRig(t)
	runs := mocks.NewMockProductionRuns(t)
	r.repo.EXPECT().ProductionRuns().Return(runs).Maybe()
	r.cards.EXPECT().ListMaterials(mock.Anything, "", true).
		Return([]entity.MaterialWithPrice{tcimpCatalogRow(1001, "F-WOOL-320", "m")}, nil)
	tcimpExpectCalibration(r.cards, runs, 1001)

	a := tcciWastageArchive(t, "22.00", 3)
	a.insert.TechnicalMedia = []*pb_common.TechCardMediaItem{{MediaId: 4020}}
	here, hereSHA := a.blob(techcardarchive.DirMedia, ".jpg", tcflJPEG("already stored here"))
	a.with(techcardarchive.FileMediaIndex, tcimpJSON(t, []techcardarchive.MediaIndexEntry{
		{Ref: 4020, File: here, SHA256: hereSHA},
	}))
	// The resolver still sees the row; it disappears before the write, which is what forces the
	// rebuild. Nothing is uploaded (the picture is a REUSE), so there is nothing to compensate.
	r.media.EXPECT().FindMediaByContentHash(mock.Anything, hereSHA).Return(&entity.MediaFull{Id: 9001}, nil)
	r.media.EXPECT().GetMediaByIds(mock.Anything, []int{9001}).Return(map[int]entity.MediaFull{}, nil)

	r.serve(t, tcciZip(t, a))
	r.rows(tcciRowOK(entity.TechCardImportStatusUploaded, 0, nil),
		tcciRowOK(entity.TechCardImportStatusCommitted, 708, tcciStoredReport(t, "GRB-SS26-014")))
	r.classifies(false, true)

	seen := r.expectImport(t,
		tcciFails(errors.New("Error 1452: foreign key constraint fails (`media_id`)")),
		tcciImported(708))

	_, err := tcciCommitCall(t, r.s)
	require.NoError(t, err)
	require.Len(t, *seen, 2, "the picture is patched out and the transaction runs again")

	require.Len(t, (*seen)[1].bom, 1)
	got := tcimpStoredProvenance((*seen)[1].bom[0])
	require.Equal(t, entity.BomWastageSourceLays, got.Source,
		"losing a picture must not cost the card its wastage badge — the rebuild re-applies the verdict")
	require.EqualValues(t, 3, got.LayCount.Int64)
}
