package admin

import (
	"database/sql"
	"errors"
	"fmt"
	"testing"
	"time"

	mocks "github.com/jekabolt/grbpwr-manager/internal/dependency/mocks"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/jekabolt/grbpwr-manager/internal/techcardarchive"
	pb_admin "github.com/jekabolt/grbpwr-manager/proto/gen/admin"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// ─────────────────────────────────────────────────────────────────────────────
// Ф3.4 — the report RPCs, tested for the four ways they can lie.
//
//  1. They answer 500 to a card nobody imported. That is every hand-typed card in the base, the
//     client asks on a flag, and a 500 turns an ordinary card into an incident.
//  2. They answer 500 to a report written by a NEWER binary. A rolling deploy is enough to produce
//     one, and the whole point of an additive format is that the older reader keeps reading.
//  3. They answer NOT_FOUND to a report that is there but unreadable — retiring the evidence of a
//     card's own history under the sentence «this card was not imported».
//  4. They make a second acknowledgement an error, so the client's «close the banner» becomes a
//     gesture that can fail on a double click.
//
// Helpers here are prefixed tcrep* — the package already owns `tcimp*`, `tcup*`, `tcz*` and `amg*`.
// ─────────────────────────────────────────────────────────────────────────────

// tcrepServer wires a Server whose repository is a STRICT mock: an unexpected call fails the test,
// which is how «the handler asked the database for nothing else» is proved by the test being green.
func tcrepServer(t *testing.T) (*Server, *mocks.MockTechCards) {
	t.Helper()
	repo := mocks.NewMockRepository(t)
	cards := mocks.NewMockTechCards(t)
	repo.EXPECT().TechCards().Return(cards).Maybe()
	return &Server{repo: repo}, cards
}

// tcrepStoredReport is a report as the commit stored it — built and marshalled through the package
// that owns the report, never hand-written, so the test cannot pass against a shape the writer
// stopped producing.
func tcrepStoredReport(t *testing.T) []byte {
	t.Helper()
	c := techcardarchive.NewCounters()
	c.AddImported(techcardarchive.EntityMedia, 4)
	c.AddSkipped(techcardarchive.EntityMedia, 1)
	raw, err := techcardarchive.MarshalReport(techcardarchive.BuildReport(techcardarchive.ReportInput{
		ImportID:    "01J8ZZQ9V2R6M1K0",
		StyleNumber: "GRB-SS26-014",
		Stage:       "proto",
		Counters:    c,
		Holes: []techcardarchive.ImportHole{{
			Entity: techcardarchive.EntityMedia,
			Ref:    "media_id=812",
			Reason: techcardarchive.ReasonMediaMissing,
			Detail: "the archive names a picture it does not carry",
		}},
	}))
	require.NoError(t, err)
	return raw
}

// tcrepCounter picks one entity's tally out of the report by NAME.
func tcrepCounter(t *testing.T, rep *pb_admin.TechCardImportReport, entityName string) *pb_admin.TechCardImportCounter {
	t.Helper()
	for _, c := range rep.GetCounters() {
		if c.GetEntity() == entityName {
			return c
		}
	}
	t.Fatalf("the report carries no counter for %q", entityName)
	return nil
}

// tcrepCode is the grpc code of an error, or codes.OK for none.
func tcrepCode(t *testing.T, err error) codes.Code {
	t.Helper()
	if err == nil {
		return codes.OK
	}
	st, ok := status.FromError(err)
	require.True(t, ok, "the handler must answer with a grpc status, got %v", err)
	return st.Code()
}

// ────────────────────── 1. a card nobody imported is NORMAL ──────────────────────

// EVERY card anybody ever typed reaches this call with no row behind it. NOT_FOUND is the answer;
// Internal would make the ordinary case of the feature look like a fault of the server.
func TestImportReportRpcSaysNotFoundForACardNobodyImported(t *testing.T) {
	s, cards := tcrepServer(t)
	// The store wraps its scan error, which is exactly how the real one loses a bare sentinel —
	// so the handler is tested against errors.Is, not against equality.
	cards.EXPECT().GetTechCardImportReport(mock.Anything, 214).
		Return(entity.TechCardArchiveImportRecord{}, fmt.Errorf("struct scan: %w", sql.ErrNoRows)).Once()

	resp, err := s.GetTechCardImportReport(t.Context(), &pb_admin.GetTechCardImportReportRequest{TechCardId: 214})
	require.Nil(t, resp, "an absent report is an absent BODY, not an empty report")
	require.Equal(t, codes.NotFound, tcrepCode(t, err),
		"a card without an import is the ordinary case, never a 500")
}

// A row that names a card but carries no report has no report to show. The operator's answer is the
// same sentence, and the difference is a log line, not a status code.
func TestImportReportRpcTreatsARowWithoutAReportAsNoReport(t *testing.T) {
	s, cards := tcrepServer(t)
	cards.EXPECT().GetTechCardImportReport(mock.Anything, 214).Return(entity.TechCardArchiveImportRecord{
		ImportID: "01J8ZZQ9V2R6M1K0", Status: entity.TechCardImportStatusFailed,
	}, nil).Once()

	resp, err := s.GetTechCardImportReport(t.Context(), &pb_admin.GetTechCardImportReportRequest{TechCardId: 214})
	require.Nil(t, resp)
	require.Equal(t, codes.NotFound, tcrepCode(t, err))
}

// ────────────────────── 2. the report is PARSED, not passed through ──────────────────────

// The stored JSON has to come back as the message the client renders — with its counters countable
// and its lines carrying the action text the report dictionary owns.
func TestImportReportRpcAnswersTheStoredReportParsed(t *testing.T) {
	s, cards := tcrepServer(t)
	cards.EXPECT().GetTechCardImportReport(mock.Anything, 214).Return(entity.TechCardArchiveImportRecord{
		ImportID: "01J8ZZQ9V2R6M1K0", Status: entity.TechCardImportStatusCommitted,
		Report: tcrepStoredReport(t),
	}, nil).Once()

	resp, err := s.GetTechCardImportReport(t.Context(), &pb_admin.GetTechCardImportReportRequest{TechCardId: 214})
	require.NoError(t, err)
	require.NotNil(t, resp.GetReport(), "the report travels as a message, never as an opaque string")
	require.Equal(t, "GRB-SS26-014", resp.GetReport().GetStyleNumber())
	require.Equal(t, "proto", resp.GetReport().GetStage())
	require.Equal(t, "01J8ZZQ9V2R6M1K0", resp.GetReport().GetImportId())

	require.Len(t, resp.GetReport().GetLines(), 1)
	line := resp.GetReport().GetLines()[0]
	require.Equal(t, techcardarchive.EntityMedia, line.GetEntity())
	require.Equal(t, "media_id=812", line.GetRef())
	require.Equal(t, string(techcardarchive.ReasonMediaMissing), line.GetReason())
	require.NotEmpty(t, line.GetAction(), "a hole an operator can act on must arrive with its sentence")

	// BuildReport emits a row for every entity it knows, counted or not — «we counted none» has to
	// look different from «we never looked» — so the media row is picked out by name rather than by
	// position.
	media := tcrepCounter(t, resp.GetReport(), techcardarchive.EntityMedia)
	require.EqualValues(t, 4, media.GetImported())
	require.EqualValues(t, 1, media.GetSkipped())

	require.Nil(t, resp.GetAcknowledgedAt(),
		"an unread report answers with NO stamp: a zero timestamp reads as «closed in 1970»")
}

// The stamp is the banner's whole state, so it has to survive the trip.
func TestImportReportRpcCarriesTheAcknowledgementStamp(t *testing.T) {
	s, cards := tcrepServer(t)
	read := time.Date(2026, 8, 25, 14, 30, 0, 0, time.UTC)
	cards.EXPECT().GetTechCardImportReport(mock.Anything, 214).Return(entity.TechCardArchiveImportRecord{
		Report:         tcrepStoredReport(t),
		AcknowledgedAt: sql.NullTime{Time: read, Valid: true},
	}, nil).Once()

	resp, err := s.GetTechCardImportReport(t.Context(), &pb_admin.GetTechCardImportReportRequest{TechCardId: 214})
	require.NoError(t, err)
	require.NotNil(t, resp.GetAcknowledgedAt())
	require.True(t, read.Equal(resp.GetAcknowledgedAt().AsTime()))
}

// ────────────────────── 3. an older reader keeps reading ──────────────────────

// THE REPORT EVOLVES THE WAY THE FORMAT DOES. A newer binary writes a field this one has no member
// for — during a rolling deploy, within one cluster — and the card must still open. Unknown fields
// are dropped, known ones survive intact; a strict parse would answer 500 on a healthy card.
func TestImportReportRpcSurvivesAFieldThisServerDoesNotKnow(t *testing.T) {
	s, cards := tcrepServer(t)
	fromTheFuture := []byte(`{
	  "lines": [{"entity":"media","ref":"media_id=812","status":"skipped","reason":"media_missing",
	             "detail":"the archive names a picture it does not carry","action":"re-upload it",
	             "severity":"advisory"}],
	  "counters": [{"entity":"media","imported":4,"skipped":1,"degraded":0,"retried":2}],
	  "styleNumber": "GRB-SS26-014",
	  "stage": "proto",
	  "importId": "01J8ZZQ9V2R6M1K0",
	  "elapsedMs": 1840
	}`)
	cards.EXPECT().GetTechCardImportReport(mock.Anything, 214).Return(entity.TechCardArchiveImportRecord{
		Report: fromTheFuture,
	}, nil).Once()

	resp, err := s.GetTechCardImportReport(t.Context(), &pb_admin.GetTechCardImportReportRequest{TechCardId: 214})
	require.NoError(t, err, "an additive MINOR of the report must not close the card")
	require.Equal(t, "GRB-SS26-014", resp.GetReport().GetStyleNumber())
	require.Len(t, resp.GetReport().GetLines(), 1)
	require.Equal(t, "re-upload it", resp.GetReport().GetLines()[0].GetAction(),
		"the fields this server DOES know must survive the unknown ones being dropped")
	require.Len(t, resp.GetReport().GetCounters(), 1)
	require.EqualValues(t, 4, resp.GetReport().GetCounters()[0].GetImported())
}

// Leniency stops at «is this a report at all». A row that claims one and does not read as one is
// corruption of a record the operator is entitled to — and it must NOT be dressed up as «this card
// was not imported», which would retire the evidence silently.
func TestImportReportRpcRefusesToPassOffCorruptionAsAnAbsentReport(t *testing.T) {
	for name, stored := range map[string]string{
		"a known field of the wrong type": `{"lines": 3}`,
		"not an object at all":            `["a"]`,
		"not json":                        `nonsense`,
	} {
		t.Run(name, func(t *testing.T) {
			s, cards := tcrepServer(t)
			cards.EXPECT().GetTechCardImportReport(mock.Anything, 214).Return(entity.TechCardArchiveImportRecord{
				Report: []byte(stored),
			}, nil).Once()

			resp, err := s.GetTechCardImportReport(t.Context(), &pb_admin.GetTechCardImportReportRequest{TechCardId: 214})
			require.Nil(t, resp)
			require.Equal(t, codes.Internal, tcrepCode(t, err))
		})
	}
}

// A failure of the database is a failure of ours, and it is NOT the sentence «no such report».
func TestImportReportRpcDoesNotDisguiseAStoreFailureAsAnAbsentReport(t *testing.T) {
	s, cards := tcrepServer(t)
	cards.EXPECT().GetTechCardImportReport(mock.Anything, 214).
		Return(entity.TechCardArchiveImportRecord{}, errors.New("connection reset")).Once()

	_, err := s.GetTechCardImportReport(t.Context(), &pb_admin.GetTechCardImportReportRequest{TechCardId: 214})
	require.Equal(t, codes.Internal, tcrepCode(t, err))
}

// ────────────────────── 4. acknowledgement ──────────────────────

// A SECOND CLICK IS NOT AN ERROR. The store's guard is `acknowledged_at IS NULL`, so the repeat
// writes nothing; the handler must pass that straight through rather than inventing a conflict.
// The strict mock is what proves the handler does not read the row first to decide.
func TestImportReportRpcAckIsIdempotent(t *testing.T) {
	s, cards := tcrepServer(t)
	cards.EXPECT().AcknowledgeTechCardImport(mock.Anything, 214).Return(nil).Twice()

	for i := 1; i <= 2; i++ {
		resp, err := s.AcknowledgeTechCardImportReport(t.Context(),
			&pb_admin.AcknowledgeTechCardImportReportRequest{TechCardId: 214})
		require.NoError(t, err, "acknowledgement #%d must be an ordinary success", i)
		require.NotNil(t, resp)
	}
}

// Acknowledging a card that came from no archive matches no row and is still a success: the client
// closes a banner, and a NOT_FOUND here would make that gesture race whatever else touched the card.
func TestImportReportRpcAckOfACardNobodyImportedIsNotAnError(t *testing.T) {
	s, cards := tcrepServer(t)
	cards.EXPECT().AcknowledgeTechCardImport(mock.Anything, 77).Return(nil).Once()

	_, err := s.AcknowledgeTechCardImportReport(t.Context(),
		&pb_admin.AcknowledgeTechCardImportReportRequest{TechCardId: 77})
	require.NoError(t, err)
}

// A write that actually failed must not answer «closed».
func TestImportReportRpcAckReportsAStoreFailure(t *testing.T) {
	s, cards := tcrepServer(t)
	cards.EXPECT().AcknowledgeTechCardImport(mock.Anything, 214).Return(errors.New("deadlock")).Once()

	resp, err := s.AcknowledgeTechCardImportReport(t.Context(),
		&pb_admin.AcknowledgeTechCardImportReportRequest{TechCardId: 214})
	require.Nil(t, resp)
	require.Equal(t, codes.Internal, tcrepCode(t, err))
}

// ────────────────────── 5. neither call goes to the database for nothing ──────────────────────

// An absent id is the caller's mistake, answered before any query. The strict mock carries the
// second half of the claim: it holds NO expectation, so a query here would fail the test.
func TestImportReportRpcRefusesAnAbsentCardId(t *testing.T) {
	for _, id := range []int32{0, -1} {
		s, _ := tcrepServer(t)
		_, err := s.GetTechCardImportReport(t.Context(), &pb_admin.GetTechCardImportReportRequest{TechCardId: id})
		require.Equal(t, codes.InvalidArgument, tcrepCode(t, err), "get with id=%d", id)

		_, err = s.AcknowledgeTechCardImportReport(t.Context(),
			&pb_admin.AcknowledgeTechCardImportReportRequest{TechCardId: id})
		require.Equal(t, codes.InvalidArgument, tcrepCode(t, err), "acknowledge with id=%d", id)
	}
}
