package admin

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	authsrv "github.com/jekabolt/grbpwr-manager/internal/apisrv/auth"
	mocks "github.com/jekabolt/grbpwr-manager/internal/dependency/mocks"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/jekabolt/grbpwr-manager/internal/rbac"
	pb_admin "github.com/jekabolt/grbpwr-manager/proto/gen/admin"
	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// seenAuditStand is tcaStand with the seen marks under the test's control.
func seenAuditStand(t *testing.T, marks []entity.TechCardFindingSeen, marksErr error) *Server {
	t.Helper()
	repo := mocks.NewMockRepository(t)
	tc := mocks.NewMockTechCards(t)
	repo.EXPECT().TechCards().Return(tc)
	tc.EXPECT().GetTechCardById(mock.Anything, 7).Return(tcaCard(), nil)
	tc.EXPECT().GetCostingFxRatesToBase(mock.Anything).Return(map[string]decimal.Decimal{}, nil).Maybe()
	tc.EXPECT().ListTechCardFindingSeen(mock.Anything, 7).Return(marks, marksErr)
	return &Server{repo: repo}
}

func seenAudit(t *testing.T, ctx context.Context, marks []entity.TechCardFindingSeen, marksErr error) *pb_admin.GetTechCardConstructionAuditResponse {
	t.Helper()
	resp, err := seenAuditStand(t, marks, marksErr).GetTechCardConstructionAudit(ctx,
		&pb_admin.GetTechCardConstructionAuditRequest{TechCardId: 7})
	require.NoError(t, err)
	require.GreaterOrEqual(t, len(resp.GetFindings()), 2, "the fixture card must produce at least two findings")
	return resp
}

func TestConstructionAuditCarriesKeysAndAppliesSeenMarks(t *testing.T) {
	plain := seenAudit(t, tcaCostingCtx(), nil, nil)
	for _, f := range plain.GetFindings() {
		require.NotEmpty(t, f.GetCode(), "machine finding %q has no code", f.GetTitle())
		require.NotEmpty(t, f.GetKey(), "machine finding %q has no key", f.GetTitle())
		require.Len(t, f.GetFingerprint(), 16)
		require.False(t, f.GetSeen())
	}
	require.EqualValues(t, len(plain.GetFindings()), plain.GetUnseenCount())

	first, second := plain.GetFindings()[0], plain.GetFindings()[1]
	at := time.Date(2026, 10, 26, 9, 30, 0, 0, time.UTC)
	marks := []entity.TechCardFindingSeen{
		{TechCardId: 7, FindingKey: first.GetKey(), Fingerprint: first.GetFingerprint(), SeenBy: "anna", SeenAt: at},
		// Marked on older text: does not count.
		{TechCardId: 7, FindingKey: second.GetKey(), Fingerprint: "0000000000000000", SeenBy: "boris", SeenAt: at},
	}
	resp := seenAudit(t, tcaCostingCtx(), marks, nil)

	got := resp.GetFindings()[0]
	require.True(t, got.GetSeen())
	require.Equal(t, "anna", got.GetSeenBy())
	require.True(t, at.Equal(got.GetSeenAt().AsTime()))

	stale := resp.GetFindings()[1]
	require.False(t, stale.GetSeen())
	require.Empty(t, stale.GetSeenBy())
	require.Nil(t, stale.GetSeenAt())

	require.EqualValues(t, len(resp.GetFindings())-1, resp.GetUnseenCount())
}

// A CLIENT finding (computed in the browser, never by the audit) is marked through the same call:
// the server takes its key on shape alone.
func TestSetTechCardFindingSeenTakesAClientFindingKey(t *testing.T) {
	repo := mocks.NewMockRepository(t)
	tc := mocks.NewMockTechCards(t)
	repo.EXPECT().TechCards().Return(tc)
	tc.EXPECT().MarkTechCardFindingSeen(mock.Anything, 7, "pdf_sa_missing@seam:hem", "fedcba9876543210", "anna").Return(nil).Once()

	_, err := (&Server{repo: repo}).SetTechCardFindingSeen(seenWriterCtx(), &pb_admin.SetTechCardFindingSeenRequest{
		TechCardId: 7, Key: "pdf_sa_missing@seam:hem", Fingerprint: "fedcba9876543210", Seen: true,
	})
	require.NoError(t, err)
}

// seen_marks hands back the marks of client findings — and only those: a server-code mark rides on
// its finding, or (stale, or a withheld money finding) is not handed out at all.
func TestConstructionAuditReturnsClientSeenMarks(t *testing.T) {
	plain := seenAudit(t, tcaCostingCtx(), nil, nil)
	require.NotNil(t, plain.GetSeenMarks())
	require.Empty(t, plain.GetSeenMarks())

	first := plain.GetFindings()[0]
	at := time.Date(2026, 10, 26, 9, 30, 0, 0, time.UTC)
	marks := []entity.TechCardFindingSeen{
		{TechCardId: 7, FindingKey: first.GetKey(), Fingerprint: first.GetFingerprint(), SeenBy: "anna", SeenAt: at},
		{TechCardId: 7, FindingKey: "placeholder_price@bom:Gone", Fingerprint: "0123456789abcdef", SeenBy: "anna", SeenAt: at},
		{TechCardId: 7, FindingKey: "pdf_sa_missing@seam:hem", Fingerprint: "fedcba9876543210", SeenBy: "boris", SeenAt: at},
	}
	for _, ctx := range []context.Context{tcaCostingCtx(), tcaNoCostingCtx()} {
		resp := seenAudit(t, ctx, marks, nil)
		require.Len(t, resp.GetSeenMarks(), 1)
		m := resp.GetSeenMarks()[0]
		require.Equal(t, "pdf_sa_missing@seam:hem", m.GetKey())
		require.Equal(t, "fedcba9876543210", m.GetFingerprint())
		require.Equal(t, "boris", m.GetSeenBy())
		require.True(t, at.Equal(m.GetSeenAt().AsTime()))
		// A client mark is not a server finding: the server's unseen count does not move.
		require.EqualValues(t, countUnseen(resp.GetFindings()), resp.GetUnseenCount())
	}
}

func countUnseen(fs []*pb_admin.TechCardAnalysisFinding) int {
	n := 0
	for _, f := range fs {
		if !f.GetSeen() {
			n++
		}
	}
	return n
}

// A failed read of the marks must not cost the reader the audit: everything reads unseen.
func TestConstructionAuditSurvivesASeenLoadFailure(t *testing.T) {
	resp := seenAudit(t, tcaCostingCtx(), nil, errors.New("db down"))
	require.EqualValues(t, len(resp.GetFindings()), resp.GetUnseenCount())
}

// unseen_count is counted AFTER the costing redaction: a withheld money finding must not show up
// as a number the account cannot explain.
func TestConstructionAuditUnseenCountExcludesWithheldMoney(t *testing.T) {
	full := seenAudit(t, tcaCostingCtx(), nil, nil)
	redacted := seenAudit(t, tcaNoCostingCtx(), nil, nil)
	require.Less(t, len(redacted.GetFindings()), len(full.GetFindings()), "the fixture must carry a money finding")
	require.EqualValues(t, len(redacted.GetFindings()), redacted.GetUnseenCount())
}

func seenWriterCtx() context.Context {
	ctx := authsrv.PutAdminAuthz(context.Background(), authsrv.AdminAuthz{
		Perms: map[string]entity.AccessLevel{rbac.SectionTechCards: entity.AccessWrite},
	})
	return authsrv.PutAdminUsername(ctx, "anna")
}

func TestSetTechCardFindingSeenMarksWithTheCaller(t *testing.T) {
	repo := mocks.NewMockRepository(t)
	tc := mocks.NewMockTechCards(t)
	repo.EXPECT().TechCards().Return(tc)
	tc.EXPECT().MarkTechCardFindingSeen(mock.Anything, 7, "smv_missing@opfp:aaaaaaaa", "0123456789abcdef", "anna").Return(nil).Once()

	_, err := (&Server{repo: repo}).SetTechCardFindingSeen(seenWriterCtx(), &pb_admin.SetTechCardFindingSeenRequest{
		TechCardId: 7, Key: "smv_missing@opfp:aaaaaaaa", Fingerprint: "0123456789abcdef", Seen: true,
	})
	require.NoError(t, err)
}

func TestSetTechCardFindingSeenUnmarksWithoutAFingerprint(t *testing.T) {
	repo := mocks.NewMockRepository(t)
	tc := mocks.NewMockTechCards(t)
	repo.EXPECT().TechCards().Return(tc)
	tc.EXPECT().UnmarkTechCardFindingSeen(mock.Anything, 7, "smv_missing@op:10").Return(nil).Once()

	_, err := (&Server{repo: repo}).SetTechCardFindingSeen(seenWriterCtx(), &pb_admin.SetTechCardFindingSeenRequest{
		TechCardId: 7, Key: "smv_missing@op:10", Seen: false,
	})
	require.NoError(t, err)
}

// Every refusal happens before the store: the strict mocks carry no expectation, so a store call
// would fail the test by name.
func TestSetTechCardFindingSeenRefusesBadInput(t *testing.T) {
	for name, req := range map[string]*pb_admin.SetTechCardFindingSeenRequest{
		"no card":        {Key: "pdf_x@card", Fingerprint: "0123456789abcdef", Seen: true},
		"no key":         {TechCardId: 7, Fingerprint: "0123456789abcdef", Seen: true},
		"blank key":      {TechCardId: 7, Key: "   ", Seen: false},
		"key too long":   {TechCardId: 7, Key: strings.Repeat("k", 513), Fingerprint: "0123456789abcdef", Seen: true},
		"key not utf8":   {TechCardId: 7, Key: "k@\xff", Fingerprint: "0123456789abcdef", Seen: true},
		"no fingerprint": {TechCardId: 7, Key: "pdf_x@card", Seen: true},
		"upper hex":      {TechCardId: 7, Key: "pdf_x@card", Fingerprint: "0123456789ABCDEF", Seen: true},
		"short hex":      {TechCardId: 7, Key: "pdf_x@card", Fingerprint: "0123", Seen: true},
		"not hex at all": {TechCardId: 7, Key: "pdf_x@card", Fingerprint: "zzzzzzzzzzzzzzzz", Seen: true},
		"no code head":   {TechCardId: 7, Key: "card", Fingerprint: "0123456789abcdef", Seen: true},
		"upper code":     {TechCardId: 7, Key: "Pdf_x@card", Fingerprint: "0123456789abcdef", Seen: true},
		"unmark no head": {TechCardId: 7, Key: "card", Seen: false},
		// Well-formed, but in nobody's namespace: not an audit code and not pdf_*/asm_*.
		"bare code":        {TechCardId: 7, Key: "k@card", Fingerprint: "0123456789abcdef", Seen: true},
		"reserved code":    {TechCardId: 7, Key: "edge_missing@op:10", Fingerprint: "0123456789abcdef", Seen: true},
		"look-alike":       {TechCardId: 7, Key: "pdfx_gap@card", Fingerprint: "0123456789abcdef", Seen: true},
		"unmark bare code": {TechCardId: 7, Key: "x1@card", Seen: false},
	} {
		t.Run(name, func(t *testing.T) {
			s := &Server{repo: mocks.NewMockRepository(t)}
			_, err := s.SetTechCardFindingSeen(seenWriterCtx(), req)
			require.Equal(t, codes.InvalidArgument, status.Code(err), "%v", err)
		})
	}
}

func TestSetTechCardFindingSeenMapsStoreErrors(t *testing.T) {
	for name, tc := range map[string]struct {
		err  error
		fk   bool
		want codes.Code
	}{
		"card row gone": {err: fmt.Errorf("lock: %w", sql.ErrNoRows), want: codes.NotFound},
		"fk race":       {err: errors.New("store"), fk: true, want: codes.NotFound},
		"card full":     {err: fmt.Errorf("mark: %w", entity.ErrTechCardFindingSeenLimit), want: codes.FailedPrecondition},
		"other error":   {err: errors.New("store"), want: codes.Internal},
	} {
		t.Run(name, func(t *testing.T) {
			repo := mocks.NewMockRepository(t)
			techCards := mocks.NewMockTechCards(t)
			repo.EXPECT().TechCards().Return(techCards)
			techCards.EXPECT().MarkTechCardFindingSeen(mock.Anything, 7, "pdf_x@card", "0123456789abcdef", "anna").Return(tc.err)
			repo.EXPECT().IsErrForeignKeyViolation(tc.err).Return(tc.fk).Maybe()

			_, err := (&Server{repo: repo}).SetTechCardFindingSeen(seenWriterCtx(), &pb_admin.SetTechCardFindingSeenRequest{
				TechCardId: 7, Key: "pdf_x@card", Fingerprint: "0123456789abcdef", Seen: true,
			})
			require.Equal(t, tc.want, status.Code(err), "%v", err)
		})
	}
}

// A mark no finding can use — a server code under a key the server never produces, or a code
// outside every namespace that predates the check — is inert: it is not handed back in seen_marks
// and moves no count.
func TestConstructionAuditIgnoresMarksNoFindingCanUse(t *testing.T) {
	at := time.Date(2026, 10, 26, 9, 30, 0, 0, time.UTC)
	marks := []entity.TechCardFindingSeen{
		{TechCardId: 7, FindingKey: "smv_missing@client-ref", Fingerprint: "0123456789abcdef", SeenBy: "anna", SeenAt: at},
		{TechCardId: 7, FindingKey: "edge_missing@op:10", Fingerprint: "0123456789abcdef", SeenBy: "anna", SeenAt: at},
		{TechCardId: 7, FindingKey: "k@card", Fingerprint: "0123456789abcdef", SeenBy: "anna", SeenAt: at},
	}
	for _, ctx := range []context.Context{tcaCostingCtx(), tcaNoCostingCtx()} {
		resp := seenAudit(t, ctx, marks, nil)
		require.NotNil(t, resp.GetSeenMarks())
		require.Empty(t, resp.GetSeenMarks())
		require.EqualValues(t, len(resp.GetFindings()), resp.GetUnseenCount())
		for _, f := range resp.GetFindings() {
			require.False(t, f.GetSeen(), f.GetKey())
		}
	}
}
